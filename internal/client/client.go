// Package client is a small, typed client for the Deed API (/api/v1).
//
// It implements the conventions Terraform relies on: bearer API tokens (deed_at_…), `If-Match` with the ETag
// (resource version) on conditional PUT/PATCH, one `Idempotency-Key` per logical POST that is reused across retries,
// retries with jittered exponential backoff that honour `Retry-After`, RFC 9457 problem decoding and a hard cap on
// response sizes. Redirects are never followed, so the bearer token cannot be forwarded to another origin.
//
// The client never logs request or response bodies and never embeds them in errors (only the problem document's
// code, detail and field messages), so secrets cannot leak through it.
//
// Adapted from terraform-provider-porch internal/client (Apache-2.0).
package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// TokenPrefix is the prefix of Deed user API tokens (ADR-005).
const TokenPrefix = "deed_at_"

// tokenSecretLen is the length of the base62 secret that follows the prefix.
const tokenSecretLen = 43

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

const apiPrefix = "/api/v1"

// MaxBodyBytes caps every response body the client reads.
const MaxBodyBytes = 8 << 20

// Config configures a Client.
type Config struct {
	// Endpoint is the public URL of the Deed installation, e.g. https://deed.example.com.
	Endpoint string
	// Token is a user API token (deed_at_…).
	Token string
	// CAFile optionally points to a PEM bundle trusted in addition to the system roots.
	CAFile string
	// UserAgent is sent on every request.
	UserAgent string
	// HTTPClient overrides the HTTP client (tests). CAFile is ignored when set; redirects are still refused.
	HTTPClient *http.Client
	// MaxRetries is the number of retries after the first attempt (default 4; negative disables retries).
	MaxRetries int
	// MinBackoff and MaxBackoff bound the exponential backoff (defaults 250ms and 8s).
	MinBackoff, MaxBackoff time.Duration
	// Timeout is the per-attempt timeout (default 60s).
	Timeout time.Duration
	// Observer, when set, is called after every attempt with method, path, status and attempt number. It never sees
	// request or response bodies or headers.
	Observer func(ctx context.Context, method, path string, status int, attempt int, err error)
}

// Client talks to one Deed installation.
type Client struct {
	base       *url.URL
	token      string
	userAgent  string
	hc         *http.Client
	maxRetries int
	minBackoff time.Duration
	maxBackoff time.Duration
	observer   func(ctx context.Context, method, path string, status int, attempt int, err error)

	// NewIdempotencyKey generates keys for POST requests (overridable in tests).
	NewIdempotencyKey func() string
	// Sleep waits between retries (overridable in tests).
	Sleep func(ctx context.Context, d time.Duration) error
	// Now returns the current time (Retry-After dates; overridable in tests).
	Now func() time.Time
}

// ValidateToken checks the API token format (prefix and 43 base62 characters) without revealing the token.
func ValidateToken(token string) error {
	if !strings.HasPrefix(token, TokenPrefix) {
		return fmt.Errorf("token must be a Deed API token starting with %q", TokenPrefix)
	}
	rest := token[len(TokenPrefix):]
	if len(rest) != tokenSecretLen {
		return fmt.Errorf("token is malformed: expected %q followed by %d base62 characters", TokenPrefix, tokenSecretLen)
	}
	for i := range len(rest) {
		if !strings.ContainsRune(base62, rune(rest[i])) {
			return fmt.Errorf("token is malformed: expected %q followed by %d base62 characters", TokenPrefix, tokenSecretLen)
		}
	}
	return nil
}

// ParseEndpoint normalises an endpoint URL (scheme://host[:port][/base]) and returns the API base URL.
func ParseEndpoint(endpoint string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return nil, errors.New("endpoint is not a valid URL")
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, errors.New("endpoint must be an absolute http(s) URL such as https://deed.example.com")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("endpoint must not contain credentials, a query or a fragment")
	}
	p := strings.TrimSuffix(u.Path, "/")
	p = strings.TrimSuffix(p, apiPrefix)
	u.Path = p + apiPrefix
	u.RawPath = ""
	return u, nil
}

// New builds a client.
func New(cfg Config) (*Client, error) {
	base, err := ParseEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	if err := ValidateToken(cfg.Token); err != nil {
		return nil, err
	}
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	hc := cfg.HTTPClient
	if hc == nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		if cfg.CAFile != "" {
			pem, err := os.ReadFile(cfg.CAFile)
			if err != nil {
				return nil, fmt.Errorf("ca_file: %w", err)
			}
			pool, err := x509.SystemCertPool()
			if err != nil || pool == nil {
				pool = x509.NewCertPool()
			}
			if !pool.AppendCertsFromPEM(pem) {
				return nil, errors.New("ca_file: no PEM certificates found")
			}
			tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		}
		timeout := cfg.Timeout
		if timeout == 0 {
			timeout = 60 * time.Second
		}
		// The API never redirects; following one could forward the bearer token elsewhere.
		hc = &http.Client{Transport: tr, Timeout: timeout, CheckRedirect: noRedirect}
	} else {
		cp := *hc
		cp.CheckRedirect = noRedirect
		hc = &cp
	}
	c := &Client{base: base, token: cfg.Token, userAgent: cfg.UserAgent, hc: hc, maxRetries: cfg.MaxRetries,
		minBackoff: cfg.MinBackoff, maxBackoff: cfg.MaxBackoff, observer: cfg.Observer, NewIdempotencyKey: randomKey, Sleep: sleepCtx,
		Now: time.Now}
	if c.userAgent == "" {
		c.userAgent = "terraform-provider-deed"
	}
	if cfg.MaxRetries == 0 {
		c.maxRetries = 4
	} else if cfg.MaxRetries < 0 {
		c.maxRetries = 0
	}
	if c.minBackoff == 0 {
		c.minBackoff = 250 * time.Millisecond
	}
	if c.maxBackoff == 0 {
		c.maxBackoff = 8 * time.Second
	}
	return c, nil
}

// Endpoint returns the public origin the client talks to (without /api/v1).
func (c *Client) Endpoint() string {
	u := *c.base
	u.Path = strings.TrimSuffix(u.Path, apiPrefix)
	return strings.TrimSuffix(u.String(), "/")
}

func randomKey() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return "tf-" + base64.RawURLEncoding.EncodeToString(b)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// request describes one logical API call.
type request struct {
	method string
	// path is relative to /api/v1 and already escaped (build it with p()).
	path    string
	query   url.Values
	body    any
	ifMatch string
	// idempotent marks a POST that carries an Idempotency-Key, reused across retries, which makes it safe to repeat.
	idempotent bool
	// noRetry makes a single attempt (best-effort probes).
	noRetry bool
}

// response is a raw API response.
type response struct {
	status int
	header http.Header
	body   []byte
}

func (r *response) etag() string { return r.header.Get("ETag") }

// p builds an escaped API path from literal segments: p("kinds", "example.com/ups") = "/kinds/example.com%2Fups".
func p(segments ...string) string {
	var b strings.Builder
	for _, s := range segments {
		b.WriteByte('/')
		b.WriteString(url.PathEscape(s))
	}
	return b.String()
}

func (c *Client) url(path string, q url.Values) string {
	u := *c.base
	raw := c.base.EscapedPath() + path
	if unescaped, err := url.PathUnescape(raw); err == nil {
		u.Path = unescaped
	} else {
		u.Path = raw
	}
	u.RawPath = raw
	if len(q) > 0 {
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// retryable reports whether a failed attempt may be repeated.
//
//   - GET, HEAD and DELETE are idempotent; so is a PUT without If-Match (a full replacement).
//   - A POST is repeated only when it carries an Idempotency-Key: the server then replays the first outcome instead of
//     running the request twice. A POST without one is never repeated after an ambiguous failure (network error or 5xx),
//     because the first attempt may have been applied.
//   - Conditional writes (PUT/PATCH with If-Match) would fail with 412 if a lost-but-applied attempt were repeated,
//     so they are only retried when the server certainly did not apply them.
//   - 429 and 503 mean the request was not processed and are always retried.
func retryable(req *request, status int, netErr bool, code string) bool {
	safe := req.method == http.MethodGet || req.method == http.MethodHead || req.method == http.MethodDelete ||
		(req.method == http.MethodPut && req.ifMatch == "") || (req.method == http.MethodPost && req.idempotent)
	switch {
	case netErr:
		return safe
	case status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable:
		return true
	case status == http.StatusConflict && code == "idempotency_in_progress":
		return req.idempotent
	case status >= 500 && status != http.StatusNotImplemented:
		return safe
	}
	return false
}

// backoff returns the wait before the next attempt: Retry-After (seconds or an HTTP date, capped at MaxBackoff) when
// the server sent one, otherwise exponential backoff with jitter in [d/2, d].
func (c *Client) backoff(attempt int, h http.Header) time.Duration {
	if h != nil {
		if s := strings.TrimSpace(h.Get("Retry-After")); s != "" {
			if n, err := strconv.Atoi(s); err == nil && n >= 0 {
				return min(time.Duration(n)*time.Second, c.maxBackoff)
			}
			if t, err := http.ParseTime(s); err == nil {
				d := t.Sub(c.Now())
				if d < 0 {
					d = 0
				}
				return min(d, c.maxBackoff)
			}
		}
	}
	d := c.minBackoff << min(attempt, 20)
	if d <= 0 || d > c.maxBackoff {
		d = c.maxBackoff
	}
	half := int64(d / 2)
	if half > 0 {
		if n, err := rand.Int(rand.Reader, big.NewInt(half)); err == nil {
			return time.Duration(half + n.Int64())
		}
	}
	return d
}

// ErrBodyTooLarge is returned when a response exceeds MaxBodyBytes.
var ErrBodyTooLarge = fmt.Errorf("response body exceeds %d bytes", MaxBodyBytes)

func readBody(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxBodyBytes {
		return nil, ErrBodyTooLarge
	}
	return b, nil
}

// do performs a request with retries and returns the final response (status < 400) or an error (*APIError for
// problem responses, *TransportError for network failures).
func (c *Client) do(ctx context.Context, req *request) (*response, error) {
	var payload []byte
	if req.body != nil {
		var err error
		if payload, err = json.Marshal(req.body); err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
	}
	key := ""
	if req.idempotent {
		key = c.NewIdempotencyKey()
	}
	target := c.url(req.path, req.query)
	maxRetries := c.maxRetries
	if req.noRetry {
		maxRetries = 0
	}
	var lastErr error
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, err
		}
		var rdr io.Reader
		if payload != nil {
			rdr = bytes.NewReader(payload)
		}
		hreq, err := http.NewRequestWithContext(ctx, req.method, target, rdr)
		if err != nil {
			return nil, err
		}
		hreq.Header.Set("Authorization", "Bearer "+c.token)
		hreq.Header.Set("User-Agent", c.userAgent)
		hreq.Header.Set("Accept", "application/json, application/problem+json")
		if payload != nil {
			hreq.Header.Set("Content-Type", "application/json")
		}
		if req.ifMatch != "" {
			hreq.Header.Set("If-Match", req.ifMatch)
		}
		if key != "" {
			hreq.Header.Set("Idempotency-Key", key)
		}
		hresp, err := c.hc.Do(hreq)
		if err != nil {
			c.observe(ctx, req, 0, attempt, err)
			lastErr = &TransportError{Method: req.method, Path: req.path, Err: err}
			if ctx.Err() == nil && attempt < maxRetries && retryable(req, 0, true, "") {
				if serr := c.Sleep(ctx, c.backoff(attempt, nil)); serr != nil {
					return nil, lastErr
				}
				continue
			}
			return nil, lastErr
		}
		body, rerr := readBody(hresp.Body)
		_ = hresp.Body.Close()
		c.observe(ctx, req, hresp.StatusCode, attempt, rerr)
		if errors.Is(rerr, ErrBodyTooLarge) {
			return nil, &TransportError{Method: req.method, Path: req.path, Err: rerr}
		}
		if rerr != nil {
			lastErr = &TransportError{Method: req.method, Path: req.path, Err: rerr}
			if attempt < maxRetries && retryable(req, 0, true, "") {
				if serr := c.Sleep(ctx, c.backoff(attempt, nil)); serr != nil {
					return nil, lastErr
				}
				continue
			}
			return nil, lastErr
		}
		resp := &response{status: hresp.StatusCode, header: hresp.Header, body: body}
		if resp.status >= 300 && resp.status < 400 {
			return nil, &APIError{StatusCode: resp.status, Code: "unexpected_redirect", Method: req.method, Path: req.path,
				Detail: "the server answered with a redirect, which the client never follows (check the endpoint)"}
		}
		if resp.status < 400 {
			return resp, nil
		}
		apiErr := decodeProblem(req.method, req.path, resp)
		lastErr = apiErr
		if attempt < maxRetries && retryable(req, resp.status, false, apiErr.Code) {
			if serr := c.Sleep(ctx, c.backoff(attempt, resp.header)); serr != nil {
				return nil, lastErr
			}
			continue
		}
		return resp, apiErr
	}
}

func (c *Client) observe(ctx context.Context, req *request, status, attempt int, err error) {
	if c.observer != nil {
		c.observer(ctx, req.method, req.path, status, attempt, err)
	}
}

// decode unmarshals a JSON response body.
func decode[T any](resp *response) (*T, error) {
	var out T
	if len(bytes.TrimSpace(resp.body)) == 0 {
		return &out, nil
	}
	if err := json.Unmarshal(resp.body, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}

// call performs req and decodes the body into T.
func call[T any](ctx context.Context, c *Client, req *request) (*T, *response, error) {
	resp, err := c.do(ctx, req)
	if err != nil {
		return nil, resp, err
	}
	out, err := decode[T](resp)
	return out, resp, err
}

// etagFor returns the response ETag, falling back to the body version (idempotent replays may carry no ETag header).
func etagFor(resp *response, version int64) string {
	if resp != nil {
		if e := resp.etag(); e != "" {
			return e
		}
	}
	if version > 0 {
		return ETagOf(version)
	}
	return ""
}

// ETagOf formats a resource version as an ETag.
func ETagOf(version int64) string { return strconv.Quote(strconv.FormatInt(version, 10)) }
