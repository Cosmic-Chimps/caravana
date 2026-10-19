package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

const cliPassword = "correct horse battery"

var cliTokenFormat = regexp.MustCompile(`^cvat_[A-Za-z0-9_-]{43}$`)

// admin runs `caravana admin <args>` against dir with stdin as the piped
// password, and returns the exit code and what it printed.
func admin(t *testing.T, dir, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	args = append(append([]string{}, args...), "-data-dir", dir)
	code = run(context.Background(), append([]string{"admin"}, args...), noEnv, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

// tokenIsActive opens the store in dir (nothing else may hold it) and asks
// whether the API token still authenticates.
func tokenIsActive(t *testing.T, dir, token string) bool {
	t.Helper()
	st, err := store.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	auth := service.NewAuth(st, slog.New(slog.DiscardHandler), service.AuthOptions{})
	_, err = auth.CheckAPIToken(context.Background(), token)
	return err == nil
}

func TestAdminInitStatusReset(t *testing.T) {
	dir := t.TempDir()
	var allOutput strings.Builder
	record := func(stdout, stderr string) { allOutput.WriteString(stdout + stderr) }

	code, stdout, stderr := admin(t, dir, "", "status")
	record(stdout, stderr)
	if code != 3 || !strings.Contains(stdout, "caravana admin init") {
		t.Errorf("status with no admin = %d, %q; want 3 naming init", code, stdout)
	}

	code, stdout, stderr = admin(t, dir, cliPassword+"\n", "init", "--email", "You@Example.com")
	record(stdout, stderr)
	if code != 0 || !strings.Contains(stdout, "you@example.com") {
		t.Fatalf("init = %d, stdout %q, stderr %q; want 0 naming the admin", code, stdout, stderr)
	}

	code, stdout, stderr = admin(t, dir, cliPassword+"\n", "init", "--email", "other@example.com")
	record(stdout, stderr)
	if code != 3 || !strings.Contains(stderr, "reset-password") {
		t.Errorf("second init = %d, stderr %q; want 3 naming reset-password", code, stderr)
	}

	code, stdout, stderr = admin(t, dir, "", "status")
	record(stdout, stderr)
	if code != 0 || strings.TrimSpace(stdout) != "admin: you@example.com" {
		t.Errorf("status = %d, %q; want 0 and the email", code, stdout)
	}

	before := adminHash(t, dir)
	code, stdout, stderr = admin(t, dir, "a brand new password\n", "reset-password")
	record(stdout, stderr)
	if code != 0 || !strings.Contains(stdout, "Password changed") {
		t.Errorf("reset-password = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if after := adminHash(t, dir); after == before {
		t.Error("reset-password left the password hash unchanged")
	}

	for _, secret := range []string{cliPassword, "a brand new password"} {
		if strings.Contains(allOutput.String(), secret) {
			t.Errorf("a password reached the output:\n%s", allOutput.String())
		}
	}
}

func adminHash(t *testing.T, dir string) string {
	t.Helper()
	st, err := store.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	a, err := st.GetAdmin(context.Background())
	if err != nil {
		t.Fatalf("GetAdmin: %v", err)
	}
	return a.PasswordHash
}

func TestAdminWeakPasswordAndNoPassword(t *testing.T) {
	dir := t.TempDir()
	code, _, stderr := admin(t, dir, "short\n", "init", "--email", "you@example.com")
	if code != 1 || !strings.Contains(stderr, "password") || strings.Contains(stderr, "short\n") {
		t.Errorf("init with a weak password = %d, %q; want 1 naming the password", code, stderr)
	}
	code, _, stderr = admin(t, dir, "", "init", "--email", "you@example.com")
	if code != 1 || !strings.Contains(stderr, "standard input") {
		t.Errorf("init with nothing on stdin = %d, %q; want 1 saying so", code, stderr)
	}
	if code, _, _ := admin(t, dir, "", "status"); code != 3 {
		t.Errorf("an admin exists after refused inits (status exit %d)", code)
	}
}

func TestAdminResetPasswordWithoutAdmin(t *testing.T) {
	code, _, stderr := admin(t, t.TempDir(), cliPassword+"\n", "reset-password")
	if code != 3 || !strings.Contains(stderr, "admin init") {
		t.Errorf("reset-password without admin = %d, %q; want 3 naming init", code, stderr)
	}
}

func TestAdminUsage(t *testing.T) {
	dir := t.TempDir()
	for name, args := range map[string][]string{
		"no command":            {},
		"unknown command":       {"frobnicate"},
		"init without --email":  {"init"},
		"unknown flag":          {"status", "--nope"},
		"stray argument":        {"status", "extra"},
		"create-token no name":  {"create-token"},
		"revoke-token no name":  {"revoke-token"},
		"bad --expires-in":      {"create-token", "--name", "ci", "--expires-in", "soon"},
		"negative --expires-in": {"create-token", "--name", "ci", "--expires-in", "-1h"},
	} {
		code, _, stderr := admin(t, dir, cliPassword+"\n", args...)
		if len(args) == 0 {
			// admin() appends -data-dir, which is itself a usage error here.
			code, _, stderr = runAdminDirect(t)
		}
		if code != 2 || stderr == "" {
			t.Errorf("%s: exit %d, stderr %q; want 2 with a message", name, code, stderr)
		}
	}
}

func runAdminDirect(t *testing.T) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"admin"}, noEnv, strings.NewReader(""), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestAdminTokens(t *testing.T) {
	dir := t.TempDir()
	if code, _, _ := admin(t, dir, "", "create-token", "--name", "ci"); code != 3 {
		t.Fatalf("create-token without an admin = exit %d, want 3", code)
	}
	if code, _, stderr := admin(t, dir, cliPassword+"\n", "init", "--email", "you@example.com"); code != 0 {
		t.Fatalf("init: %d %s", code, stderr)
	}

	code, stdout, stderr := admin(t, dir, "", "create-token", "--name", "ci")
	token := strings.TrimSpace(stdout)
	if code != 0 || !cliTokenFormat.MatchString(token) || stdout != token+"\n" {
		t.Fatalf("create-token = %d, stdout %q; want only the token", code, stdout)
	}
	if !strings.Contains(stderr, "Token ci created") || strings.Contains(stderr, token) {
		t.Errorf("create-token notice = %q; want a notice without the token", stderr)
	}
	if !tokenIsActive(t, dir, token) {
		t.Error("the new token does not authenticate")
	}

	if code, _, stderr := admin(t, dir, "", "create-token", "--name", "ci"); code != 3 || !strings.Contains(stderr, "name") {
		t.Errorf("create-token with a taken name = %d, %q; want 3", code, stderr)
	}
	if code, _, stderr := admin(t, dir, "", "create-token", "--name", "bad name"); code != 2 || !strings.Contains(stderr, "name") {
		t.Errorf("create-token with a bad name = %d, %q; want 2 naming the field", code, stderr)
	}

	code, stdout, stderr = admin(t, dir, "", "create-token", "--name", "expiring", "--expires-in", "720h")
	if code != 0 || !cliTokenFormat.MatchString(strings.TrimSpace(stdout)) {
		t.Fatalf("create-token --expires-in = %d, %q", code, stdout)
	}
	want := time.Now().Add(720 * time.Hour).UTC().Format("2006-01-02")
	if !strings.Contains(stderr, "expires "+want) && !strings.Contains(stderr, "expires "+time.Now().Add(720*time.Hour+time.Minute).UTC().Format("2006-01-02")) {
		t.Errorf("notice %q should give the expiry date around %s", stderr, want)
	}

	if code, stdout, stderr := admin(t, dir, "", "revoke-token", "--name", "ci"); code != 0 || !strings.Contains(stdout, "revoked") {
		t.Errorf("revoke-token = %d, %q, %q", code, stdout, stderr)
	}
	if tokenIsActive(t, dir, token) {
		t.Error("the revoked token still authenticates")
	}
	if code, _, stderr := admin(t, dir, "", "revoke-token", "--name", "ci"); code != 1 || !strings.Contains(stderr, `"ci"`) {
		t.Errorf("revoke-token of a revoked name = %d, %q; want 1", code, stderr)
	}
	if code, _, _ := admin(t, dir, "", "revoke-token", "--name", "nope"); code != 1 {
		t.Errorf("revoke-token of an unknown name = exit %d, want 1", code)
	}
}

// TestAdminWhileServerRuns is the host workflow: the server is up, the owner
// runs `caravana admin init` and `create-token` beside it, and the server
// accepts the new token without a restart (research R4).
func TestAdminWhileServerRuns(t *testing.T) {
	addr, dir := freeAddr(t), t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	var stderr syncBuffer
	exit := make(chan int, 1)
	go func() { exit <- runServer(ctx, []string{"-listen", addr, "-data-dir", dir}, noEnv, &stderr) }()
	t.Cleanup(func() {
		cancel()
		<-exit
	})
	waitHealthy(t, "http://"+addr)

	get := func(token string) int {
		req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/v1/apps/x", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := get(""); got != http.StatusUnauthorized {
		t.Fatalf("no token before init = %d, want 401", got)
	}

	if code, _, errOut := admin(t, dir, cliPassword+"\n", "init", "--email", "you@example.com"); code != 0 {
		t.Fatalf("init while the server runs = %d, %s", code, errOut)
	}
	code, stdout, errOut := admin(t, dir, "", "create-token", "--name", "ci")
	if code != 0 {
		t.Fatalf("create-token while the server runs = %d, %s", code, errOut)
	}
	token := strings.TrimSpace(stdout)

	if got := get(token); got != http.StatusNotFound { // past auth: the app doesn't exist
		t.Errorf("GET with the CLI's token = %d, want 404 (authenticated)", got)
	}
	if code, _, errOut := admin(t, dir, "", "revoke-token", "--name", "ci"); code != 0 {
		t.Fatalf("revoke-token while the server runs = %d, %s", code, errOut)
	}
	if got := get(token); got != http.StatusUnauthorized {
		t.Errorf("GET with the revoked token = %d, want 401", got)
	}

	if strings.Contains(stderr.String(), cliPassword) || strings.Contains(stderr.String(), token) {
		t.Errorf("a secret reached the server log:\n%s", stderr.String())
	}
}

// TestGateAPIStep runs the gate's first step, api-needs-token, against an
// in-process server. The gate itself needs the lab or a VM (~12 minutes);
// this proves its token plumbing works with nothing but the CLI and the API.
func TestGateAPIStep(t *testing.T) {
	addr, dir := freeAddr(t), t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	var stderr syncBuffer
	exit := make(chan int, 1)
	go func() { exit <- runServer(ctx, []string{"-listen", addr, "-data-dir", dir}, noEnv, &stderr) }()
	t.Cleanup(func() {
		cancel()
		<-exit
	})
	waitHealthy(t, "http://"+addr)

	token := labAPIToken(t, ctx, dir)
	g := &gate{target: gateTarget{API: "http://" + addr, APIToken: token}}
	if !t.Run("api-needs-token", g.apiNeedsToken) {
		t.Fatal("the gate's api-needs-token step failed against a correct server")
	}

	// The helper authenticates every API call it makes.
	resp := g.api(t, http.MethodGet, "/v1/tokens", "", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("g.api GET /v1/tokens = %d, want 200", resp.StatusCode)
	}
}
