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
	version4 = uint64(4)
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

// fromCI is a deploy event of a deploy that a CI run started.
var fromCI = Event{
	Type:        EventDeploySucceeded,
	AppID:       "shop--staging--web",
	Project:     "shop",
	Environment: "staging",
	AppName:     "web",
	Title:       "shop--staging--web: deployed v4",
	Text:        "shop--staging--web's deploy v4 is healthy, started by the API.",
	Deployment:  "dep_5c4d3e",
	Version:     &version4,
	Outcome:     "healthy",
	Trigger:     "manual",
	Origin: &Origin{Commit: "abc1234def5678", Ref: "refs/heads/main", Actor: "ana",
		Run: "https://github.com/acme/shop/actions/runs/212", Repository: "acme/shop"},
	URL: "https://caravana.example.com/apps/shop--staging--web/deployments#deployment-dep_5c4d3e",
	At:  fixedNow,
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

// scheduleFailed is a problem about a schedule (feature 022): its name and
// the run's exit code, never its output.
var scheduleFailed = Event{
	Type:        EventProblemOpened,
	Kind:        "schedule_failed",
	AppID:       "booking",
	Project:     "default",
	Environment: "production",
	AppName:     "booking",
	Service:     "web",
	Title:       "booking: rates-sync failed (exit 3)",
	Text:        "The latest run of the schedule rates-sync, on booking/web, failed.",
	Detail:      "Exited with code 3.",
	Schedule:    "rates-sync",
	ExitCode:    new(3),
	URL:         "https://caravana.example.com/schedules#schedule-3f9a1c2b4d5e",
	At:          fixedNow,
}

// domainElsewhere and certExpiring are problems about a domain (feature 023):
// the host, the address its DNS gave and when its certificate ends.
var domainElsewhere = Event{
	Type:        EventProblemOpened,
	Kind:        "domain_dns_elsewhere",
	AppID:       "shop",
	Project:     "default",
	Environment: "production",
	AppName:     "shop",
	Service:     "web",
	Title:       "shop: partners.example.com points elsewhere",
	Text:        "partners.example.com points at 185.199.108.153, which isn't this host.",
	Domain:      "partners.example.com",
	AddressSeen: "185.199.108.153",
	URL:         "https://caravana.example.com/domains?open=partners.example.com#domain-partners.example.com",
	At:          fixedNow,
}

var certExpiring = Event{
	Type:        EventProblemOpened,
	Kind:        "domain_cert_expiring",
	AppID:       "shop",
	Project:     "default",
	Environment: "production",
	AppName:     "shop",
	Service:     "web",
	Title:       "shop: the certificate for bi.example.com is expiring",
	Text:        "The certificate for bi.example.com expires in 6 days and hasn't been renewed.",
	Domain:      "bi.example.com",
	ExpiresAt:   new(fixedNow.Add(6 * 24 * time.Hour)),
	URL:         "https://caravana.example.com/domains?open=bi.example.com#domain-bi.example.com",
	At:          fixedNow,
}

// diskNearlyFull is a problem about the host's disk (feature 025): no app, the
// mount, what is on it, how full and how much is left.
var diskNearlyFull = Event{
	Type:        EventProblemOpened,
	Kind:        "disk_nearly_full",
	Title:       "Disk nearly full: / is 86% full",
	Text:        "The disk at / is 86% full: 11 GB left.",
	Mount:       "/",
	Holds:       []string{"nomad", "docker"},
	UsedPercent: 86.4,
	FreeBytes:   new(int64(11) << 30),
	Level:       "warning",
	URL:         "https://caravana.example.com/monitoring#host",
	At:          fixedNow,
}

func TestRender(t *testing.T) {
	tests := []struct {
		name string
		ch   Channel
		ev   Event
	}{
		{"webhook", Channel{KindWebhook, "https://hooks.example.com/caravana?x=1", "whsec_test"}, deployFailed},
		{"webhook-promoted", Channel{KindWebhook, "https://hooks.example.com/caravana", "whsec_test"}, promoted},
		{"webhook-origin", Channel{KindWebhook, "https://hooks.example.com/caravana", "whsec_test"}, fromCI},
		{"slack-origin", Channel{KindSlack, "https://hooks.slack.com/services/T000/B000/XXXX", ""}, fromCI},
		{"ntfy-origin", Channel{KindNtfy, "https://ntfy.sh/my-topic", ""}, fromCI},
		{"webhook-problem", Channel{KindWebhook, "https://hooks.example.com/caravana", "whsec_test"}, backupFailed},
		{"slack", Channel{KindSlack, "https://hooks.slack.com/services/T000/B000/XXXX", ""}, backupFailed},
		{"slack-no-link", Channel{KindSlack, "https://hooks.slack.com/services/T000/B000/XXXX", ""}, resolvedNoLink},
		{"discord", Channel{KindDiscord, "https://discord.com/api/webhooks/123/abc", ""}, deployFailed},
		{"ntfy-token", Channel{KindNtfy, "https://ntfy.sh/my-topic", "tk_secret"}, backupFailed},
		{"ntfy-resolved", Channel{KindNtfy, "https://ntfy.example.com/alerts", ""}, resolvedNoLink},
		{"ntfy-deploy-failed", Channel{KindNtfy, "https://ntfy.sh/my-topic", ""}, deployFailed},
		{"webhook-schedule", Channel{KindWebhook, "https://hooks.example.com/caravana", "whsec_test"}, scheduleFailed},
		{"slack-schedule", Channel{KindSlack, "https://hooks.slack.com/services/T000/B000/XXXX", ""}, scheduleFailed},
		{"discord-schedule", Channel{KindDiscord, "https://discord.com/api/webhooks/123/abc", ""}, scheduleFailed},
		{"ntfy-schedule", Channel{KindNtfy, "https://ntfy.sh/my-topic", ""}, scheduleFailed},
		{"webhook-domain", Channel{KindWebhook, "https://hooks.example.com/caravana", "whsec_test"}, domainElsewhere},
		{"slack-domain", Channel{KindSlack, "https://hooks.slack.com/services/T000/B000/XXXX", ""}, domainElsewhere},
		{"discord-domain", Channel{KindDiscord, "https://discord.com/api/webhooks/123/abc", ""}, certExpiring},
		{"ntfy-domain", Channel{KindNtfy, "https://ntfy.sh/my-topic", ""}, certExpiring},
		{"webhook-cert", Channel{KindWebhook, "https://hooks.example.com/caravana", "whsec_test"}, certExpiring},
		{"webhook-disk", Channel{KindWebhook, "https://hooks.example.com/caravana", "whsec_test"}, diskNearlyFull},
		{"slack-disk", Channel{KindSlack, "https://hooks.slack.com/services/T000/B000/XXXX", ""}, diskNearlyFull},
		{"discord-disk", Channel{KindDiscord, "https://discord.com/api/webhooks/123/abc", ""}, diskNearlyFull},
		{"ntfy-disk", Channel{KindNtfy, "https://ntfy.sh/my-topic", ""}, diskNearlyFull},
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

func TestDiskLine(t *testing.T) {
	tests := []struct {
		name string
		ev   Event
		want string
	}{
		{"not a disk", Event{Type: EventProblemOpened}, ""},
		{"the example", diskNearlyFull, "Disk: / is 86% full, 11 GB left"},
		{"cut down, so 94.6 is not 95, where the item turns failed; under 10 GB left", Event{Mount: "/var/lib/docker", UsedPercent: 94.6, FreeBytes: new(int64(3584) << 20)}, "Disk: /var/lib/docker is 94% full, 3.5 GB left"},
		{"no space left", Event{Mount: "/", UsedPercent: 100, FreeBytes: new(int64(0))}, "Disk: / is 100% full, 0 B left"},
		{"free space not known", Event{Mount: "/", UsedPercent: 90}, "Disk: / is 90% full"},
		{"a mount with a line break stays one line", Event{Mount: "/a\nb", UsedPercent: 90}, "Disk: /a b is 90% full"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := diskLine(tc.ev); got != tc.want {
				t.Errorf("diskLine = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSize(t *testing.T) {
	for n, want := range map[int64]string{
		-5: "0 B", 0: "0 B", 1023: "1023 B", 1024: "1 KB", 1 << 20: "1 MB", 620 << 20: "620 MB",
		1<<30 - 1: "1024 MB", 1 << 30: "1.0 GB", 3584 << 20: "3.5 GB", 10 << 30: "10 GB", 22528 << 20: "22 GB",
	} {
		if got := Size(n); got != want {
			t.Errorf("Size(%d) = %q, want %q", n, got, want)
		}
	}
}

// The one wording of a nearly full disk, which the console, the API's detail and the notifications
// share: the percent is cut down and the space left is Size's.
func TestDiskSentence(t *testing.T) {
	for _, tc := range []struct {
		mount string
		pct   float64
		free  int64
		want  string
	}{
		{"/var/lib/docker", 87.2, 1126 << 20, "The disk at /var/lib/docker is 87% full: 1.1 GB left."},
		{"/", 86.9, 11 << 30, "The disk at / is 86% full: 11 GB left."},
		{"/data", 94.6, 600 << 20, "The disk at /data is 94% full: 600 MB left."},
		{"/", 100, 0, "The disk at / is 100% full: 0 B left."},
	} {
		if got := DiskSentence(tc.mount, tc.pct, tc.free); got != tc.want {
			t.Errorf("DiskSentence(%q, %v, %d) = %q, want %q", tc.mount, tc.pct, tc.free, got, tc.want)
		}
	}
}
