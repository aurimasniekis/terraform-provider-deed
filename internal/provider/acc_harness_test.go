package provider

// Acceptance test harness (adapted from terraform-provider-porch, Apache-2.0): when TF_ACC is set, TestMain starts the
// real `deed serve` in development mode (free loopback ports, a 0700 temporary data directory), completes the
// first-run setup as a local owner (password + TOTP enrollment), issues API tokens and points the provider at it.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // RFC 6238 TOTP uses HMAC-SHA1
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/terraform-provider-deed/internal/client"
)

// accEnv describes the Deed instance the acceptance tests run against.
type accEnv struct {
	Endpoint   string // public URL (http://127.0.0.1:PORT)
	AdminToken string // installation admin API token of the owner
	// EditorToken is capped at the editor role and restricted to LimitedProject (negative tests).
	EditorToken    string
	LimitedProject string
	// OwnerID is the owner's user ID; MemberID a second local user for project membership tests.
	OwnerID  string
	MemberID string
	logDir   string
}

var acc *accEnv

func TestMain(m *testing.M) {
	// Use a locally installed Terraform CLI so nothing is downloaded.
	if os.Getenv("TF_ACC_TERRAFORM_PATH") == "" {
		if p, err := exec.LookPath("terraform"); err == nil {
			_ = os.Setenv("TF_ACC_TERRAFORM_PATH", p)
		} else if _, err := os.Stat("/opt/homebrew/bin/terraform"); err == nil {
			_ = os.Setenv("TF_ACC_TERRAFORM_PATH", "/opt/homebrew/bin/terraform")
		}
	}
	_ = os.Setenv("CHECKPOINT_DISABLE", "1")
	if os.Getenv("TF_ACC") == "" {
		os.Exit(m.Run())
	}
	env, stop, err := startDeed()
	if err != nil {
		fmt.Fprintln(os.Stderr, "acceptance harness:", err)
		if env != nil {
			dumpLog(filepath.Join(env.logDir, "deed.log"))
		}
		stop()
		os.Exit(1)
	}
	acc = env
	_ = os.Setenv(EnvEndpoint, env.Endpoint)
	_ = os.Setenv(EnvToken, env.AdminToken)
	_ = os.Unsetenv(EnvSource)
	code := m.Run()
	if code != 0 {
		dumpLog(filepath.Join(env.logDir, "deed.log"))
	}
	stop()
	os.Exit(code)
}

func dumpLog(p string) {
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	if len(b) > 8000 {
		b = b[len(b)-8000:]
	}
	fmt.Fprintf(os.Stderr, "--- tail of %s\n%s\n", p, b)
}

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func deedBinary() string {
	if p := os.Getenv("DEED_ACC_BINARY"); p != "" {
		return p
	}
	return filepath.Join(repoRoot(), "bin", "deed")
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// childEnv is the environment for the deed binary: Deed rejects unknown DEED_* variables (the provider's own
// DEED_ENDPOINT/DEED_TOKEN/DEED_SOURCE, CI's DEED_COMMIT, …), so every DEED_* variable except DEED_CONFIG is removed.
func childEnv() []string {
	out := []string{}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "DEED_") && !strings.HasPrefix(kv, "DEED_CONFIG=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// startDeed launches Deed and returns the environment and a stop function.
func startDeed() (*accEnv, func(), error) {
	var srv *exec.Cmd
	work, err := os.MkdirTemp("", "deed-tfacc-")
	stop := func() {
		if srv != nil && srv.Process != nil {
			_ = srv.Process.Kill()
			_, _ = srv.Process.Wait()
		}
		if work != "" && os.Getenv("DEED_ACC_KEEP") == "" {
			_ = os.RemoveAll(work)
		}
	}
	if err != nil {
		return nil, stop, err
	}
	env := &accEnv{logDir: work}
	if err := os.Chmod(work, 0o700); err != nil {
		return env, stop, err
	}
	bin := deedBinary()
	if _, err := os.Stat(bin); err != nil {
		return env, stop, fmt.Errorf("deed binary not found at %s (run `make deed-bin` or set DEED_ACC_BINARY)", bin)
	}
	pub, err := freePort()
	if err != nil {
		return env, stop, err
	}
	mgmt, err := freePort()
	if err != nil {
		return env, stop, err
	}
	origin := fmt.Sprintf("http://127.0.0.1:%d", pub)
	env.Endpoint = origin
	cfgPath := filepath.Join(work, "deed.yaml")
	cfg := fmt.Sprintf(`mode: development
data_dir: %s
public_url: %s
http:
  listen: 127.0.0.1:%d
management:
  listen: 127.0.0.1:%d
log:
  format: text
  level: warn
auth:
  local:
    enabled: true
`, filepath.Join(work, "data"), origin, pub, mgmt)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		return env, stop, err
	}
	boot := exec.Command(bin, "admin", "bootstrap", "-config", cfgPath) //nolint:gosec // test binary
	boot.Env = childEnv()
	out, err := boot.CombinedOutput()
	if err != nil {
		return env, stop, fmt.Errorf("deed admin bootstrap: %w: %s", err, out)
	}
	setupToken := regexp.MustCompile(`deed_setup_[A-Za-z0-9_-]+`).FindString(string(out))
	if setupToken == "" {
		return env, stop, errors.New("deed admin bootstrap printed no setup token")
	}
	logf, err := os.Create(filepath.Join(work, "deed.log"))
	if err != nil {
		return env, stop, err
	}
	srv = exec.Command(bin, "serve", "-config", cfgPath) //nolint:gosec // test binary
	srv.Stdout, srv.Stderr, srv.Env = logf, logf, childEnv()
	if err := srv.Start(); err != nil {
		return env, stop, err
	}
	ready := false
	for range 150 {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/readyz", mgmt)) //nolint:noctx // test
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		return env, stop, errors.New("deed did not become ready")
	}
	if err := setupOwner(env, origin, setupToken); err != nil {
		return env, stop, err
	}
	return env, stop, nil
}

// setupOwner completes the bootstrap link as a local owner, enrolls TOTP (owners must), issues the tokens and
// creates the fixtures the tests share.
func setupOwner(env *accEnv, origin, setupToken string) error {
	owner, err := newBrowser(origin)
	if err != nil {
		return err
	}
	var me struct {
		UserID string `json:"user_id"`
		CSRF   string `json:"csrf_token"`
	}
	if err := owner.json(http.MethodPost, "/api/v1/auth/setup/complete", map[string]string{"token": setupToken, "email": "owner@example.com",
		"display_name": "Acceptance Owner", "password": "acceptance-owner-password"}, &me); err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	owner.csrf = me.CSRF
	env.OwnerID = me.UserID
	var enroll struct {
		Secret string `json:"secret"`
	}
	if err := owner.json(http.MethodPost, "/api/v1/me/mfa/totp", nil, &enroll); err != nil {
		return fmt.Errorf("begin TOTP: %w", err)
	}
	code, err := totpCode(enroll.Secret, time.Now())
	if err != nil {
		return err
	}
	if err := owner.json(http.MethodPost, "/api/v1/me/mfa/totp/confirm", map[string]string{"code": code}, nil); err != nil {
		return fmt.Errorf("confirm TOTP: %w", err)
	}
	if err := owner.json(http.MethodGet, "/api/v1/me", nil, &me); err != nil {
		return err
	}
	owner.csrf = me.CSRF
	var tok struct {
		Token string `json:"token"`
	}
	if err := owner.json(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "tf-acc", "installation_role": "admin",
		"expires_in_days": 30}, &tok); err != nil {
		return fmt.Errorf("admin token: %w", err)
	}
	env.AdminToken = tok.Token
	// A second local user through an invite link (POST /users fails at the pinned Deed commit: its audit record carries
	// the detail key "with_password", which the audit writer refuses as a sensitive key).
	var invite struct {
		Link string `json:"setup_link"`
	}
	if err := owner.json(http.MethodPost, "/api/v1/users/invites", map[string]any{"email": "member@example.com"}, &invite); err != nil {
		return fmt.Errorf("invite member: %w", err)
	}
	inviteToken := regexp.MustCompile(`deed_setup_[A-Za-z0-9_-]+`).FindString(invite.Link)
	member, err := newBrowser(origin)
	if err != nil {
		return err
	}
	var memberMe struct {
		UserID string `json:"user_id"`
	}
	if err := member.json(http.MethodPost, "/api/v1/auth/setup/complete", map[string]string{"token": inviteToken,
		"display_name": "Acceptance Member", "password": "acceptance-member-password"}, &memberMe); err != nil {
		return fmt.Errorf("accept invite: %w", err)
	}
	env.MemberID = memberMe.UserID

	admin, err := client.New(client.Config{Endpoint: origin, Token: env.AdminToken, UserAgent: "tfacc-harness"})
	if err != nil {
		return err
	}
	ctx := context.Background()
	env.LimitedProject = "acc-limited"
	if _, err := admin.CreateProject(ctx, client.ProjectInput{Name: env.LimitedProject}); err != nil {
		return err
	}
	if err := admin.SetMember(ctx, env.LimitedProject, env.OwnerID, "admin"); err != nil {
		return err
	}
	if err := owner.json(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "tf-acc-editor", "project_role": "editor",
		"projects": []string{env.LimitedProject}, "expires_in_days": 30}, &tok); err != nil {
		return fmt.Errorf("editor token: %w", err)
	}
	env.EditorToken = tok.Token
	return nil
}

// totpCode computes an RFC 6238 code (HMAC-SHA1, 30 s, 6 digits) from an unpadded base32 secret.
func totpCode(secret string, now time.Time) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimRight(secret, "=")))
	if err != nil {
		return "", fmt.Errorf("TOTP secret: %w", err)
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(now.Unix()/30)) //nolint:gosec // positive Unix time
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", v%1_000_000), nil
}

// browser is a minimal cookie-aware client acting like the web UI (Origin + CSRF on unsafe requests).
type browser struct {
	origin string
	csrf   string
	hc     *http.Client
}

func newBrowser(origin string) (*browser, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &browser{origin: origin, hc: &http.Client{Jar: jar, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (b *browser) json(method, p string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, b.origin+p, rdr) //nolint:noctx // test
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Origin", b.origin)
	if b.csrf != "" {
		req.Header.Set("X-Deed-CSRF", b.csrf)
	}
	resp, err := b.hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %d %s", method, p, resp.StatusCode, data)
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// testAccPreCheck guards acceptance tests.
func testAccPreCheck(t *testing.T) {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance tests need TF_ACC=1")
	}
	if acc == nil {
		t.Fatal("acceptance environment not initialised (TF_ACC must be set)")
	}
}

// adminClient is an out-of-band API client for drift and destroy checks.
func adminClient(t *testing.T) *client.Client {
	t.Helper()
	c, err := client.New(client.Config{Endpoint: acc.Endpoint, Token: acc.AdminToken, UserAgent: "tfacc-oob"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
