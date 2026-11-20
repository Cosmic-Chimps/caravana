package notify

import (
	"bytes"
	"context"
	"mime"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/notify/smtptest"
)

// emailChannel is an email channel to two people; the password is the sentinel
// the leak tests look for.
func emailChannel(srv *smtptest.Server, security string) Channel {
	return Channel{
		Kind:   KindEmail,
		Secret: "s3cret-sentinel-mail-pass",
		SMTP: &SMTP{
			Host: srv.Host(), Port: srv.Port(), Security: security, Username: "caravana",
			From: "Caravana <ops@example.com>", To: []string{"ana@example.com", "Ben Díaz <ben@example.com>"},
		},
	}
}

func TestRenderMail(t *testing.T) {
	ch := Channel{Kind: KindEmail, SMTP: &SMTP{Host: "smtp.example.com", Port: 587, Security: SecuritySTARTTLS,
		From: "Caravana <ops@example.com>", To: []string{"ana@example.com", "Ben Díaz <ben@example.com>"}}}
	tests := []struct {
		name string
		ev   Event
	}{
		{"test", testMessage},
		{"problem", backupFailed}, // detail with "<", ">" and "&": the HTML part escapes them
		{"origin", fromCI},
		{"release", newRelease},
		{"no-link", resolvedNoLink},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, err := RenderMail(ch, tc.ev, "ndl_3f9a1c2b4d5e", fixedNow)
			if err != nil {
				t.Fatalf("RenderMail: %v", err)
			}
			// Every line ends in CRLF; the golden file shows them as LF.
			if bytes.Contains(bytes.ReplaceAll(m.Data, []byte("\r\n"), nil), []byte("\n")) {
				t.Error("the message has a bare LF")
			}
			got := "From: " + m.From + "\nTo: " + strings.Join(m.To, ", ") + "\n\n" + strings.ReplaceAll(string(m.Data), "\r\n", "\n")

			path := filepath.Join("testdata", "email-"+tc.name+".golden.txt")
			if *update {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden (run with -update to create it): %v", err)
			}
			if got != string(want) {
				t.Errorf("message differs from %s; if the change is intended, run with -update and review the diff.\ngot:\n%s", path, got)
			}
			// The message parses: it is the structure a mail client reads.
			parsed, err := mail.ReadMessage(bytes.NewReader(m.Data))
			if err != nil {
				t.Fatalf("the message does not parse: %v", err)
			}
			if parsed.Header.Get("Subject") == "" || parsed.Header.Get("Message-Id") != "<ndl_3f9a1c2b4d5e@caravana>" {
				t.Errorf("headers = %v", parsed.Header)
			}
		})
	}
}

func TestRenderMailEncodesTheSubject(t *testing.T) {
	ch := Channel{Kind: KindEmail, SMTP: &SMTP{Host: "h", Port: 25, Security: SecurityNone, From: "ops@example.com", To: []string{"a@example.com"}}}
	ev := testMessage
	ev.Title = "café: the deploy\nfailed"
	m, err := RenderMail(ch, ev, "ndl_1", fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(m.Data))
	if err != nil {
		t.Fatal(err)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
	if err != nil || subject != "café: the deploy failed" {
		t.Errorf("subject = %q, %v", subject, err)
	}
}

// A subject that needs encoding all the way is folded, one encoded word per
// line, so no header line passes SMTP's limit of 998 characters, and it still
// reads back whole.
func TestRenderMailFoldsALongSubject(t *testing.T) {
	ch := Channel{Kind: KindEmail, SMTP: &SMTP{Host: "h", Port: 25, Security: SecurityNone, From: "ops@example.com", To: []string{"a@example.com"}}}
	ev := testMessage
	ev.Title = strings.Repeat("é", 300)
	m, err := RenderMail(ch, ev, "ndl_1", fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(m.Data), "\r\n") {
		if len(line) > 998 {
			t.Fatalf("a line of %d characters: %.80q…", len(line), line)
		}
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(m.Data))
	if err != nil {
		t.Fatal(err)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
	if want := strings.Repeat("é", 249) + "…"; err != nil || subject != want {
		t.Errorf("subject = %q (%d runes), %v; want 249 é and an ellipsis", subject, len([]rune(subject)), err)
	}
}

func TestRenderMailRefuses(t *testing.T) {
	good := SMTP{Host: "h", Port: 25, Security: SecurityNone, From: "ops@example.com", To: []string{"a@example.com"}}
	tests := []struct {
		name string
		edit func(*SMTP)
	}{
		{"line break in from", func(s *SMTP) { s.From = "ops@example.com\r\nBcc: evil@example.com" }},
		{"line break in a recipient", func(s *SMTP) { s.To = []string{"a@example.com\nBcc: evil@example.com"} }},
		{"not an address", func(s *SMTP) { s.To = []string{"nobody"} }},
		{"no recipient", func(s *SMTP) { s.To = nil }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := good
			tc.edit(&s)
			if _, err := RenderMail(Channel{Kind: KindEmail, SMTP: &s}, testMessage, "ndl_1", fixedNow); err == nil {
				t.Error("RenderMail succeeded, want an error")
			}
		})
	}
	if _, err := RenderMail(Channel{Kind: KindEmail}, testMessage, "ndl_1", fixedNow); err == nil {
		t.Error("RenderMail without settings succeeded")
	}
	if _, err := Render(Channel{Kind: KindEmail, SMTP: &good}, testMessage, "ndl_1", fixedNow); err == nil || !strings.Contains(err.Error(), "RenderMail") {
		t.Errorf("Render of an email channel = %v, want a pointer to RenderMail", err)
	}
}

func rendered(t *testing.T, ch Channel) Mail {
	t.Helper()
	m, err := RenderMail(ch, backupFailed, "ndl_1", fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSendMailNone(t *testing.T) {
	srv := smtptest.Start(t, smtptest.Options{AuthUser: "caravana", AuthPassword: "s3cret-sentinel-mail-pass", AllowPlainAuth: true})
	ch := emailChannel(srv, SecurityNone)
	if err := SendMail(context.Background(), ch, rendered(t, ch)); err != nil {
		t.Fatalf("SendMail: %v", err)
	}
	got := srv.Messages()
	if len(got) != 1 {
		t.Fatalf("server got %d messages", len(got))
	}
	m := got[0]
	if m.From != "ops@example.com" || strings.Join(m.To, ",") != "ana@example.com,ben@example.com" || m.TLS || m.AuthUser != "caravana" {
		t.Errorf("message = from %q to %v tls %v auth %q", m.From, m.To, m.TLS, m.AuthUser)
	}
	if !strings.Contains(string(m.Data), "the backup of uptime-kuma failed") {
		t.Errorf("data = %s", m.Data)
	}
}

func TestSendMailSTARTTLS(t *testing.T) {
	// AUTH is offered only after TLS, so a login that works proves TLS came first.
	srv := smtptest.Start(t, smtptest.Options{StartTLS: true, AuthUser: "caravana", AuthPassword: "s3cret-sentinel-mail-pass"})
	ch := emailChannel(srv, SecuritySTARTTLS)
	if err := sendMail(context.Background(), ch, rendered(t, ch), sendTimeout, srv.RootCAs()); err != nil {
		t.Fatalf("sendMail: %v", err)
	}
	got := srv.Messages()
	if len(got) != 1 || !got[0].TLS || got[0].AuthUser != "caravana" {
		t.Fatalf("messages = %+v, want one over TLS with the login", got)
	}
}

func TestSendMailImplicitTLS(t *testing.T) {
	srv := smtptest.Start(t, smtptest.Options{ImplicitTLS: true, AuthUser: "caravana", AuthPassword: "s3cret-sentinel-mail-pass"})
	ch := emailChannel(srv, SecurityTLS)
	if err := sendMail(context.Background(), ch, rendered(t, ch), sendTimeout, srv.RootCAs()); err != nil {
		t.Fatalf("sendMail: %v", err)
	}
	got := srv.Messages()
	if len(got) != 1 || !got[0].TLS {
		t.Fatalf("messages = %+v, want one over TLS", got)
	}
}

// A certificate nobody trusts is refused, so a message and a password never go
// to a server that only looks right.
func TestSendMailUntrustedCertificate(t *testing.T) {
	srv := smtptest.Start(t, smtptest.Options{StartTLS: true, AuthUser: "caravana", AuthPassword: "s3cret-sentinel-mail-pass"})
	ch := emailChannel(srv, SecuritySTARTTLS)
	err := sendMail(context.Background(), ch, rendered(t, ch), sendTimeout, nil) // the system's roots
	if err == nil {
		t.Fatal("sendMail succeeded against an unknown certificate")
	}
	if len(srv.Messages()) != 0 {
		t.Error("a message was delivered")
	}
}

func TestSendMailNoSTARTTLS(t *testing.T) {
	srv := smtptest.Start(t, smtptest.Options{}) // offers nothing
	ch := emailChannel(srv, SecuritySTARTTLS)
	err := sendMail(context.Background(), ch, rendered(t, ch), sendTimeout, srv.RootCAs())
	if err == nil || !strings.Contains(err.Error(), "doesn't offer STARTTLS") {
		t.Fatalf("error = %v, want a refusal naming STARTTLS", err)
	}
	if len(srv.Messages()) != 0 {
		t.Error("a message was delivered in the clear")
	}
}

func TestSendMailRejected(t *testing.T) {
	srv := smtptest.Start(t, smtptest.Options{AuthUser: "caravana", AuthPassword: "s3cret-sentinel-mail-pass", AllowPlainAuth: true, RefuseRcpt: "ben@"})
	ch := emailChannel(srv, SecurityNone)
	err := SendMail(context.Background(), ch, rendered(t, ch))
	if err == nil || !strings.Contains(err.Error(), "550") {
		t.Fatalf("error = %v, want the server's 550", err)
	}
	if strings.Contains(err.Error(), "s3cret-sentinel") || strings.ContainsAny(err.Error(), "\r\n") {
		t.Errorf("error %q holds the password or a line break", err)
	}
	if !strings.Contains(err.Error(), srv.Host()) {
		t.Errorf("error %q does not name the host", err)
	}
}

func TestSendMailWrongPassword(t *testing.T) {
	srv := smtptest.Start(t, smtptest.Options{AuthUser: "caravana", AuthPassword: "another-password", AllowPlainAuth: true})
	ch := emailChannel(srv, SecurityNone)
	err := SendMail(context.Background(), ch, rendered(t, ch))
	if err == nil || !strings.Contains(err.Error(), "535") {
		t.Fatalf("error = %v, want the server's 535", err)
	}
	if strings.Contains(err.Error(), "s3cret-sentinel") {
		t.Errorf("error %q holds the password", err)
	}
}

// An error that echoes the password has it blanked out.
func TestMailErrorBlanksThePassword(t *testing.T) {
	err := mailError("smtp.example.com", errBare("535 bad login for hunter2-pass"), "hunter2-pass")
	if strings.Contains(err.Error(), "hunter2-pass") || !strings.Contains(err.Error(), "535 bad login for …") {
		t.Errorf("error = %q", err)
	}
}

type errBare string

func (e errBare) Error() string { return string(e) }

func TestSendMailTimeout(t *testing.T) {
	srv := smtptest.Start(t, smtptest.Options{Hang: true})
	ch := emailChannel(srv, SecurityNone)
	start := time.Now()
	err := sendMail(context.Background(), ch, rendered(t, ch), 100*time.Millisecond, nil)
	if err == nil {
		t.Fatal("sendMail succeeded against a server that never answers")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("sendMail took %v, want about the timeout", took)
	}
	if strings.Contains(err.Error(), "s3cret-sentinel") {
		t.Errorf("error %q holds the password", err)
	}
}

func TestSendMailUnreachable(t *testing.T) {
	srv := smtptest.Start(t, smtptest.Options{})
	ch := emailChannel(srv, SecurityNone)
	m := rendered(t, ch)
	srv.Close()
	if err := SendMail(context.Background(), ch, m); err == nil {
		t.Error("SendMail to a closed port succeeded")
	}
}

// Deliver sends an email through SMTP and anything else through HTTP.
func TestDeliverEmail(t *testing.T) {
	srv := smtptest.Start(t, smtptest.Options{AuthUser: "caravana", AuthPassword: "s3cret-sentinel-mail-pass", AllowPlainAuth: true})
	ch := emailChannel(srv, SecurityNone)
	if err := Deliver(context.Background(), nil, ch, testMessage, "ndl_1", fixedNow); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if _, ok := srv.WaitFor(func(m smtptest.Message) bool { return strings.Contains(string(m.Data), "Test message from Caravana") }, time.Second); !ok {
		t.Error("the server did not get the test message")
	}
}
