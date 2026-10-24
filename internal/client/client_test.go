package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testToken = "deed_at_0123456789abcdefghijklmnopqrstuvwxyzABCDEFG"

func newTestClient(t *testing.T, h http.Handler) (*Client, *[]time.Duration) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(Config{Endpoint: srv.URL, Token: testToken, UserAgent: "terraform-provider-deed/test", MaxRetries: 3,
		MinBackoff: time.Millisecond, MaxBackoff: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	sleeps := []time.Duration{}
	c.Sleep = func(_ context.Context, d time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		sleeps = append(sleeps, d)
		return nil
	}
	return c, &sleeps
}

func writeJSON(w http.ResponseWriter, status int, etag string, v any) {
	w.Header().Set("Content-Type", "application/json")
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeProblem(w http.ResponseWriter, status int, code, detail string, fields []FieldError) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": http.StatusText(status), "status": status,
		"detail": detail, "code": code, "fields": fields})
}

// dropConnection closes the TCP connection without a response, simulating a network failure after the server
// processed the request.
func dropConnection(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	hj, ok := w.(http.Hijacker)
	if !ok {
		t.Fatal("no hijacker")
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
}

func TestValidateToken(t *testing.T) {
	good := testToken
	if err := ValidateToken(good); err != nil {
		t.Fatalf("valid token refused: %v", err)
	}
	for _, bad := range []string{
		"",
		"deed_st_0123456789abcdefghijklmnopqrstuvwxyzABCDEFG", // source token
		"deed_at_",
		"deed_at_0123456789abcdefghijklmnopqrstuvwxyzABCDEF",   // 42 characters
		"deed_at_0123456789abcdefghijklmnopqrstuvwxyzABCDEFGH", // 44 characters
		"deed_at_0123456789abcdefghijklmnopqrstuvwxyzABCDE-G",  // not base62
		"deed_at_0123456789abcdefghijklmnopqrstuvwxyzABCDE G",
		"porch_at_0123456789abcdefghijklmnopqrstuvwxyzABCDEFG",
	} {
		err := ValidateToken(bad)
		if err == nil {
			t.Errorf("accepted %q", bad)
			continue
		}
		if len(bad) > len(TokenPrefix) && strings.Contains(err.Error(), bad[len(TokenPrefix):]) {
			t.Errorf("error echoes the token: %v", err)
		}
	}
}

func TestNewValidatesConfig(t *testing.T) {
	cases := []struct {
		endpoint, token, wantErr string
	}{
		{"https://deed.example.com", "deed_setup_x", "deed_at_"},
		{"https://deed.example.com", "deed_at_abc", "malformed"},
		{"deed.example.com", testToken, "absolute"},
		{"ftp://deed.example.com", testToken, "absolute"},
		{"https://user:pw@deed.example.com", testToken, "credentials"},
		{"https://deed.example.com?x=1", testToken, "query"},
	}
	for _, tc := range cases {
		_, err := New(Config{Endpoint: tc.endpoint, Token: tc.token})
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("New(%q, %q) = %v, want error containing %q", tc.endpoint, tc.token, err, tc.wantErr)
		}
		if err != nil && strings.Contains(err.Error(), testToken) {
			t.Errorf("error leaks the token: %v", err)
		}
	}
	if _, err := New(Config{Endpoint: "https://deed.example.com", Token: testToken, CAFile: "/nonexistent/ca.pem"}); err == nil {
		t.Error("missing CA file accepted")
	}
}

func TestParseEndpoint(t *testing.T) {
	for in, want := range map[string]string{
		"https://deed.example.com":               "https://deed.example.com/api/v1",
		"https://deed.example.com/":              "https://deed.example.com/api/v1",
		"https://deed.example.com/api/v1":        "https://deed.example.com/api/v1",
		"https://deed.example.com/deed/":         "https://deed.example.com/deed/api/v1",
		"http://192.0.2.10:8787/deed/api/v1/":    "http://192.0.2.10:8787/deed/api/v1",
		"  https://deed.example.com:8443/api/v1": "https://deed.example.com:8443/api/v1",
	} {
		u, err := ParseEndpoint(in)
		if err != nil || u.String() != want {
			t.Errorf("ParseEndpoint(%q) = %v, %v; want %s", in, u, err, want)
		}
	}
}

func TestPathSegmentsAreEscaped(t *testing.T) {
	var got []string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.EscapedPath())
		writeJSON(w, http.StatusOK, `"3"`, Kind{Name: "example.com/ups", ResourceVersion: 3})
	}))
	k, err := c.GetKind(context.Background(), "example.com/ups")
	if err != nil {
		t.Fatal(err)
	}
	if k.ETag != `"3"` {
		t.Errorf("etag %q", k.ETag)
	}
	if _, err := c.GetDeclaration(context.Background(), "ent_1", "terraform"); err != nil {
		t.Fatal(err)
	}
	if got[0] != "/api/v1/kinds/example.com%2Fups" || got[1] != "/api/v1/entities/ent_1/declarations/terraform" {
		t.Errorf("paths %v", got)
	}
}

func TestHeadersIfMatchAndIdempotencyKey(t *testing.T) {
	var seen []*http.Request
	var mu sync.Mutex
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Clone(context.Background()))
		mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/entities":
			writeJSON(w, http.StatusCreated, `"1"`, Declaration{EntityID: "ent_1", Source: "terraform", ResourceVersion: 1})
		case r.Method == http.MethodGet:
			writeJSON(w, http.StatusOK, `"1"`, Declaration{EntityID: "ent_1", Source: "terraform", ResourceVersion: 1})
		case r.Method == http.MethodPut:
			if r.Header.Get("If-Match") != `"1"` {
				writeProblem(w, http.StatusPreconditionFailed, CodePreconditionFailed, "declaration was modified concurrently", nil)
				return
			}
			writeJSON(w, http.StatusOK, `"2"`, Declaration{EntityID: "ent_1", Source: "terraform", ResourceVersion: 2})
		case r.Method == http.MethodPatch:
			writeJSON(w, http.StatusOK, `"5"`, Project{ID: "prj_1", Name: "p", ResourceVersion: 5})
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	ctx := context.Background()
	d, err := c.CreateEntity(ctx, EntityCreate{Project: "p", Source: "terraform", Kind: "server", Name: "a"})
	if err != nil || d.ETag != `"1"` {
		t.Fatalf("create: %+v %v", d, err)
	}
	if _, err := c.GetDeclaration(ctx, "ent_1", "terraform"); err != nil {
		t.Fatal(err)
	}
	_, err = c.PutDeclaration(ctx, "ent_1", "terraform", `"9"`, DeclarationInput{})
	if !IsPreconditionFailed(err) || !HasCode(err, CodePreconditionFailed) {
		t.Fatalf("stale If-Match: %v", err)
	}
	up, err := c.PutDeclaration(ctx, "ent_1", "terraform", d.ETag, DeclarationInput{})
	if err != nil || up.ETag != `"2"` {
		t.Fatalf("put: %+v %v", up, err)
	}
	if _, err := c.UpdateProject(ctx, "p", `"4"`, ProjectInput{DisplayName: "P"}); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteDeclaration(ctx, "ent_1", "terraform"); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 6 {
		t.Fatalf("requests: %d", len(seen))
	}
	for i, r := range seen {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Errorf("request %d: missing bearer token", i)
		}
		if r.Header.Get("User-Agent") != "terraform-provider-deed/test" {
			t.Errorf("request %d: user agent %q", i, r.Header.Get("User-Agent"))
		}
		if r.Header.Get("X-Deed-CSRF") != "" || r.Header.Get("Cookie") != "" {
			t.Errorf("request %d: ambient credentials sent", i)
		}
		wantKey := r.Method == http.MethodPost
		if (r.Header.Get("Idempotency-Key") != "") != wantKey {
			t.Errorf("request %d (%s): Idempotency-Key %q", i, r.Method, r.Header.Get("Idempotency-Key"))
		}
	}
	if seen[2].Header.Get("If-Match") != `"9"` || seen[3].Header.Get("If-Match") != `"1"` || seen[4].Header.Get("If-Match") != `"4"` {
		t.Errorf("If-Match not sent on PUT/PATCH")
	}
	if seen[0].Header.Get("If-Match") != "" || seen[1].Header.Get("If-Match") != "" || seen[5].Header.Get("If-Match") != "" {
		t.Errorf("If-Match sent on POST/GET/DELETE")
	}
}

func TestETagFallsBackToVersion(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Idempotent-Replayed", "true")
		writeJSON(w, http.StatusCreated, "", Declaration{EntityID: "ent_1", ResourceVersion: 7})
	}))
	d, err := c.CreateEntity(context.Background(), EntityCreate{Project: "p", Source: "terraform", Kind: "server", Name: "a"})
	if err != nil || d.ETag != `"7"` {
		t.Fatalf("got %+v, %v", d, err)
	}
}

func TestPostRetriesWithSameIdempotencyKey(t *testing.T) {
	var calls atomic.Int32
	var keys []string
	var mu sync.Mutex
	c, sleeps := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"name":"nas-01"`) {
			t.Errorf("body not resent: %s", body)
		}
		switch calls.Add(1) {
		case 1:
			dropConnection(t, w)
		case 2:
			writeProblem(w, http.StatusServiceUnavailable, "unavailable", "try again later", nil)
		case 3:
			writeProblem(w, http.StatusConflict, "idempotency_in_progress", "retry shortly", nil)
		default:
			writeJSON(w, http.StatusCreated, `"1"`, Declaration{EntityID: "ent_1", ResourceVersion: 1})
		}
	}))
	d, err := c.CreateEntity(context.Background(), EntityCreate{Project: "p", Source: "terraform", Kind: "server", Name: "nas-01"})
	if err != nil || d.EntityID != "ent_1" {
		t.Fatalf("got %+v, %v", d, err)
	}
	if len(keys) != 4 {
		t.Fatalf("attempts = %d, want 4", len(keys))
	}
	for _, k := range keys {
		if k == "" || k != keys[0] {
			t.Fatalf("idempotency keys differ across retries: %v", keys)
		}
	}
	if len(*sleeps) != 3 {
		t.Errorf("sleeps %v", *sleeps)
	}
	// A second logical create uses a fresh key.
	calls.Store(10)
	if _, err := c.CreateEntity(context.Background(), EntityCreate{Project: "p", Source: "terraform", Kind: "server", Name: "nas-01"}); err != nil {
		t.Fatal(err)
	}
	if keys[len(keys)-1] == keys[0] {
		t.Error("idempotency key reused for a new logical create")
	}
}

func TestRetryAfterIsHonoured(t *testing.T) {
	var calls atomic.Int32
	date := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	c, sleeps := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.Header().Set("Retry-After", "3")
			writeProblem(w, http.StatusTooManyRequests, "rate_limited", "slow down", nil)
		case 2:
			w.Header().Set("Retry-After", date.Add(2*time.Second).Format(http.TimeFormat))
			writeProblem(w, http.StatusServiceUnavailable, "unavailable", "busy", nil)
		case 3:
			w.Header().Set("Retry-After", "3600") // capped at MaxBackoff
			writeProblem(w, http.StatusServiceUnavailable, "unavailable", "busy", nil)
		default:
			writeJSON(w, http.StatusOK, `"1"`, Project{ID: "prj_1", ResourceVersion: 1})
		}
	}))
	c.Now = func() time.Time { return date }
	if _, err := c.GetProject(context.Background(), "p"); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{3 * time.Second, 2 * time.Second, 10 * time.Second}
	if fmt.Sprint(*sleeps) != fmt.Sprint(want) {
		t.Fatalf("sleeps = %v, want %v", *sleeps, want)
	}
}

func TestBackoffIsJitteredAndBounded(t *testing.T) {
	c, err := New(Config{Endpoint: "https://deed.example.com", Token: testToken, MinBackoff: 100 * time.Millisecond, MaxBackoff: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	for attempt := range 8 {
		d := c.backoff(attempt, nil)
		hi := min(100*time.Millisecond<<attempt, time.Second)
		if d < hi/2 || d > hi {
			t.Errorf("attempt %d: backoff %v outside [%v, %v]", attempt, d, hi/2, hi)
		}
	}
}

func TestRetryRules(t *testing.T) {
	cases := []struct {
		name     string
		req      request
		status   int
		netErr   bool
		attempts int32
	}{
		{"POST without idempotency key, network error", request{method: http.MethodPost, path: "/x"}, 0, true, 1},
		{"POST without idempotency key, 500", request{method: http.MethodPost, path: "/x"}, 500, false, 1},
		{"POST without idempotency key, 502", request{method: http.MethodPost, path: "/x"}, 502, false, 1},
		{"POST without idempotency key, 503", request{method: http.MethodPost, path: "/x"}, 503, false, 4},
		{"POST without idempotency key, 429", request{method: http.MethodPost, path: "/x"}, 429, false, 4},
		{"POST with idempotency key, network error", request{method: http.MethodPost, path: "/x", idempotent: true}, 0, true, 4},
		{"POST with idempotency key, 500", request{method: http.MethodPost, path: "/x", idempotent: true}, 500, false, 4},
		{"conditional PUT, network error", request{method: http.MethodPut, path: "/x", ifMatch: `"1"`}, 0, true, 1},
		{"conditional PUT, 500", request{method: http.MethodPut, path: "/x", ifMatch: `"1"`}, 500, false, 1},
		{"conditional PUT, 503", request{method: http.MethodPut, path: "/x", ifMatch: `"1"`}, 503, false, 4},
		{"conditional PATCH, 500", request{method: http.MethodPatch, path: "/x", ifMatch: `"1"`}, 500, false, 1},
		{"unconditional PUT, 500", request{method: http.MethodPut, path: "/x"}, 500, false, 4},
		{"GET, network error", request{method: http.MethodGet, path: "/x"}, 0, true, 4},
		{"GET, 501", request{method: http.MethodGet, path: "/x"}, 501, false, 1},
		{"DELETE, 502", request{method: http.MethodDelete, path: "/x"}, 502, false, 4},
		{"GET, 404", request{method: http.MethodGet, path: "/x"}, 404, false, 1},
		{"PUT, 412", request{method: http.MethodPut, path: "/x", ifMatch: `"1"`}, 412, false, 1},
		{"POST, 409 conflict", request{method: http.MethodPost, path: "/x", idempotent: true}, 409, false, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				if tc.netErr {
					dropConnection(t, w)
					return
				}
				writeProblem(w, tc.status, "", "failure", nil)
			}))
			req := tc.req
			req.body = map[string]string{"a": "b"}
			if _, err := c.do(context.Background(), &req); err == nil {
				t.Fatal("expected an error")
			}
			if got := calls.Load(); got != tc.attempts {
				t.Errorf("attempts = %d, want %d", got, tc.attempts)
			}
		})
	}
}

func TestProblemDecoding(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/entities":
			writeProblem(w, http.StatusUnprocessableEntity, CodeValidationFailed, "spec does not validate against kind server",
				[]FieldError{{Field: "spec/cpu_cores", Message: "got string, want integer"}, {Field: "metadata.labels.Team", Message: "invalid key"}})
		case "/api/v1/projects/p":
			// Not a problem document: summarised by status only, never echoed.
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, "<html>upstream secret-value-123</html>")
		}
	}))
	c.maxRetries = 0
	_, err := c.CreateEntity(context.Background(), EntityCreate{Project: "p", Source: "terraform", Kind: "server", Name: "a"})
	ae, ok := AsAPIError(err)
	if !ok || ae.StatusCode != http.StatusUnprocessableEntity || ae.Code != CodeValidationFailed || len(ae.Fields) != 2 || ae.Fields[0].Field != "spec/cpu_cores" {
		t.Fatalf("problem: %#v", err)
	}
	if !strings.Contains(err.Error(), "spec/cpu_cores: got string, want integer") || !strings.Contains(ae.Summary(), "422 validation_failed") {
		t.Errorf("message: %v", err)
	}
	_, err = c.GetProject(context.Background(), "p")
	ae, ok = AsAPIError(err)
	if !ok || ae.StatusCode != http.StatusBadGateway || ae.Code != "internal" || strings.Contains(err.Error(), "secret-value-123") {
		t.Fatalf("non-problem body: %v", err)
	}
	if IsNotFound(err) {
		t.Error("502 reported as not found")
	}
}

func TestObserverNeverSeesBodies(t *testing.T) {
	const secret = "s3cr3t-spec-value"
	type obs struct {
		method, path string
		status       int
		err          string
	}
	var got []obs
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			writeProblem(w, http.StatusConflict, CodeAlreadyExists, "already declared", nil)
			return
		}
		writeJSON(w, http.StatusOK, `"1"`, Declaration{EntityID: "ent_1", Spec: json.RawMessage(`{"x":"` + secret + `"}`)})
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{Endpoint: srv.URL, Token: testToken, Observer: func(_ context.Context, method, path string, status, _ int, err error) {
		e := ""
		if err != nil {
			e = err.Error()
		}
		got = append(got, obs{method, path, status, e})
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetDeclaration(context.Background(), "ent_1", "terraform"); err != nil {
		t.Fatal(err)
	}
	_, err = c.CreateEntity(context.Background(), EntityCreate{Project: "p", Source: "terraform", Kind: "server", Name: "a",
		Spec: json.RawMessage(`{"x":"` + secret + `"}`)})
	if !HasCode(err, CodeAlreadyExists) {
		t.Fatalf("create: %v", err)
	}
	if len(got) != 2 || got[0].status != 200 || got[1].status != 409 || got[0].path != "/entities/ent_1/declarations/terraform" {
		t.Fatalf("observations: %+v", got)
	}
	for _, o := range got {
		if strings.Contains(fmt.Sprint(o), secret) || strings.Contains(fmt.Sprint(o), testToken) {
			t.Errorf("observer saw a body or the token: %+v", o)
		}
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error contains request body: %v", err)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var hit atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Store(true)
		if r.Header.Get("Authorization") != "" {
			t.Error("bearer token forwarded to the redirect target")
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(other.Close)
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
	}))
	_, err := c.GetProject(context.Background(), "p")
	if err == nil || !HasCode(err, "unexpected_redirect") {
		t.Fatalf("redirect: %v", err)
	}
	if hit.Load() {
		t.Error("redirect followed")
	}
}

func TestResponseBodyIsCapped(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"`+strings.Repeat("a", MaxBodyBytes)+`"}`)
	}))
	_, err := c.GetProject(context.Background(), "p")
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("got %v", err)
	}
}

func TestContextCancellationStopsRetries(t *testing.T) {
	var calls atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writeProblem(w, http.StatusServiceUnavailable, "unavailable", "busy", nil)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	c.Sleep = func(context.Context, time.Duration) error { cancel(); return context.Canceled }
	_, err := c.GetProject(ctx, "p")
	if !HasCode(err, "unavailable") || calls.Load() != 1 {
		t.Fatalf("got %v after %d calls", err, calls.Load())
	}
}

func TestRelationLiveAssertion(t *testing.T) {
	r := Relation{Assertions: []Assertion{{Source: "terraform", Live: false}, {Source: "api", Live: true}}}
	if r.LiveAssertion("terraform") != nil || r.LiveAssertion("api") == nil || r.LiveAssertion("ui") != nil {
		t.Fatal("LiveAssertion")
	}
}

func TestGetMemberScansList(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, "", []ProjectMember{{UserID: "usr_1", Role: "editor"}})
	}))
	m, err := c.GetMember(context.Background(), "p", "usr_1")
	if err != nil || m.Role != "editor" {
		t.Fatalf("%+v %v", m, err)
	}
	if _, err := c.GetMember(context.Background(), "p", "usr_2"); !IsNotFound(err) {
		t.Fatalf("missing member: %v", err)
	}
}

func TestListEntitiesFollowsCursor(t *testing.T) {
	var queries []string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		if r.URL.Query().Get("cursor") == "" {
			writeJSON(w, http.StatusOK, "", entityPage{Items: []Entity{{Kind: "server"}}, NextCursor: "c2"})
			return
		}
		writeJSON(w, http.StatusOK, "", entityPage{Items: []Entity{{Kind: "vm"}}})
	}))
	es, err := c.ListEntities(context.Background(), EntityFilter{Projects: []string{"homelab"}, Labels: map[string]string{"b": "2", "a": "1"}, Query: "nas"})
	if err != nil || len(es) != 2 {
		t.Fatalf("%v %v", es, err)
	}
	if !strings.Contains(queries[0], "label=a%3D1&label=b%3D2") || !strings.Contains(queries[0], "project=homelab") ||
		!strings.Contains(queries[0], "q=nas") || !strings.Contains(queries[1], "cursor=c2") {
		t.Errorf("queries %v", queries)
	}
}
