package notify

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// Run `go test ./internal/notify -update` to rewrite the golden files, then
// review the diff before committing.
var update = flag.Bool("update", false, "rewrite golden files")

var (
	fixedNow = time.Date(2026, 10, 17, 3, 0, 41, 0, time.UTC)
	version8 = uint64(8)
	version3 = uint64(3)
)

// backupFailed is a problem event with a link.
var backupFailed = Event{
	Type:    EventProblemOpened,
	Kind:    "backup_failed",
	AppID:   "kuma",
	Service: "uptime-kuma",
	Title:   "kuma: the backup of uptime-kuma failed",
	Text:    "The volume backup at 03:00 UTC failed.",
	Detail:  "upload of data: Failed to rcat: <timeout> & more",
	URL:     "https://caravana.example.com/apps/kuma/backups",
	At:      fixedNow,
}

// deployFailed is a deploy event.
var deployFailed = Event{
	Type:        EventDeployFailed,
	AppID:       "hollo",
	Project:     "default",
	Environment: "production",
	AppName:     "hollo",
	Title:       "hollo: the deploy was reverted to v6",
	Text:        "Version 8 did not become healthy, so Nomad went back to version 6.",
	Detail:      "deadline reached",
	Deployment:  "dep_1a2b3c",
	Version:     &version8,
	Outcome:     "reverted",
	Trigger:     "webhook",
	URL:         "https://caravana.example.com/apps/hollo/deployments#deployment-dep_1a2b3c",
	At:          fixedNow,
}

// promoted is a deploy event of a promotion: the place and where it came from.
var promoted = Event{
	Type:         EventDeploySucceeded,
	AppID:        "shop--production--web",
	Project:      "shop",
	Environment:  "production",
	AppName:      "web",
	Title:        "shop--production--web: deployed v3",
	Text:         "shop--production--web's deploy v3 is healthy, promoted from staging, started from the console.",
	Deployment:   "dep_9z8y7x",
	Version:      &version3,
	Outcome:      "healthy",
	Trigger:      "console",
	PromotedFrom: "staging",
	URL:          "https://caravana.example.com/apps/shop--production--web/deployments#deployment-dep_9z8y7x",
	At:           fixedNow,
}

// resolvedNoLink is a resolved problem on a host without a public address.
var resolvedNoLink = Event{
	Type:  EventProblemResolved,
	Kind:  "backup_failed",
	AppID: "kuma",
	Title: "kuma: the backup of uptime-kuma is working again",
	Text:  "It has not been on the needs-you list for 5 minutes.",
	At:    fixedNow,
}

func TestRender(t *testing.T) {
	tests := []struct {
		name string
		ch   Channel
		ev   Event
	}{
		{"webhook", Channel{KindWebhook, "https://hooks.example.com/caravana?x=1", "whsec_test"}, deployFailed},
		{"webhook-promoted", Channel{KindWebhook, "https://hooks.example.com/caravana", "whsec_test"}, promoted},
		{"webhook-problem", Channel{KindWebhook, "https://hooks.example.com/caravana", "whsec_test"}, backupFailed},
		{"slack", Channel{KindSlack, "https://hooks.slack.com/services/T000/B000/XXXX", ""}, backupFailed},
		{"slack-no-link", Channel{KindSlack, "https://hooks.slack.com/services/T000/B000/XXXX", ""}, resolvedNoLink},
		{"discord", Channel{KindDiscord, "https://discord.com/api/webhooks/123/abc", ""}, deployFailed},
		{"ntfy-token", Channel{KindNtfy, "https://ntfy.sh/my-topic", "tk_secret"}, backupFailed},
		{"ntfy-resolved", Channel{KindNtfy, "https://ntfy.example.com/alerts", ""}, resolvedNoLink},
		{"ntfy-deploy-failed", Channel{KindNtfy, "https://ntfy.sh/my-topic", ""}, deployFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := Render(tc.ch, tc.ev, "ndl_3f9a1c2b4d5e", fixedNow)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			got := dump(t, req)

			path := filepath.Join("testdata", tc.name+".golden.txt")
			if *update {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden (run with -update to create it): %v", err)
			}
			if got != string(want) {
				t.Errorf("request differs from %s; if the change is intended, run with -update and review the diff.\ngot:\n%s", path, got)
			}
		})
	}
}

// dump writes a request as text: the method and URL, the headers sorted, a
// blank line and the body.
func dump(t *testing.T, req *http.Request) string {
	t.Helper()
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for name := range req.Header {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", req.Method, req.URL)
	for _, name := range names {
		fmt.Fprintf(&b, "%s: %s\n", name, strings.Join(req.Header[name], ", "))
	}
	fmt.Fprintf(&b, "\n%s\n", body)
	return b.String()
}

// TestRenderWebhookSignature checks that the signature on a rendered webhook
// verifies against its own body, which is how a receiver would check it.
func TestRenderWebhookSignature(t *testing.T) {
	req, err := Render(Channel{KindWebhook, "https://hooks.example.com/x", "whsec_test"}, deployFailed, "ndl_1", fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(req.Body)
	header := req.Header.Get(SignatureHeader)
	if err := Verify("whsec_test", header, body, fixedNow, 5*time.Minute); err != nil {
		t.Errorf("Verify: %v (header %q)", err, header)
	}
	if err := Verify("other", header, body, fixedNow, 5*time.Minute); !errors.Is(err, ErrSignatureMismatch) {
		t.Errorf("Verify with another secret = %v, want ErrSignatureMismatch", err)
	}
}

func TestRenderErrors(t *testing.T) {
	const sentinel = "SENTINEL_PATH_TOKEN"
	tests := []struct {
		name string
		ch   Channel
	}{
		{"unknown kind", Channel{"carrier-pigeon", "https://example.com/" + sentinel, ""}},
		{"not http", Channel{KindSlack, "ftp://example.com/" + sentinel, ""}},
		{"no host", Channel{KindSlack, "https:///" + sentinel, ""}},
		{"relative", Channel{KindDiscord, "/" + sentinel, ""}},
		{"unparsable", Channel{KindSlack, "https://exa mple.com/%zz" + sentinel, ""}},
		{"webhook without a secret", Channel{KindWebhook, "https://example.com/" + sentinel, ""}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Render(tc.ch, backupFailed, "ndl_1", fixedNow)
			if err == nil {
				t.Fatal("Render succeeded, want an error")
			}
			if strings.Contains(err.Error(), sentinel) {
				t.Errorf("the error holds the address: %v", err)
			}
		})
	}
}

// TestRenderDetail checks the "Detail: " line of the chat kinds (FR-006): there
// when the event has a detail, on one line, cut when long, and absent when not.
func TestRenderDetail(t *testing.T) {
	long := strings.Repeat("x", maxDetail+50)
	tests := []struct {
		name   string
		detail string
		want   string // what follows the text
	}{
		{"none", "", ""},
		{"short", "deadline reached", "\nDetail: deadline reached"},
		{"several lines", "first\n  second\r\nthird", "\nDetail: first second third"},
		{"long", long, "\nDetail: " + strings.Repeat("x", maxDetail) + "…"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := Event{Type: EventDeployFailed, Title: "t", Text: "text", Detail: tc.detail}
			if got, want := withDetail(ev), "text"+tc.want; got != want {
				t.Errorf("withDetail() = %q, want %q", got, want)
			}
			for _, kind := range []string{KindSlack, KindDiscord, KindNtfy} {
				req, err := Render(Channel{kind, "https://example.com/x", ""}, ev, "ndl_1", fixedNow)
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(req.Body)
				if has := strings.Contains(string(body), "Detail: "); has != (tc.detail != "") {
					t.Errorf("%s: body has detail = %v, want %v: %s", kind, has, tc.detail != "", body)
				}
			}
		})
	}
}
