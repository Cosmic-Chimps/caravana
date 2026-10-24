package ui

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// The critique's ruling 9: without Let's Encrypt, Warn when is drawn disabled with "Applies once
// certificates are issued." and the form posts nothing; the post is still taken (as the API takes it).
func TestCertificatesWarnWhenWithoutLetsEncrypt(t *testing.T) {
	editOf := func(body string) string {
		return regexp.MustCompile(`(?s)<details class="cv-confirm cv-certedit".*?</details>`).FindString(body)
	}
	e := newConsoleEnv(t)
	e.Svc.SetACME("none", "")
	body := e.pageOf(t, "/settings/certificates")
	if !strings.Contains(text(body), "This caravana was installed without Let's Encrypt: Traefik serves its own certificate.") {
		t.Errorf("the lede:\n%s", text(body))
	}
	edit := editOf(body)
	for _, want := range []string{`name="warnBeforeDays" disabled aria-describedby="cert-warn-hint"`, `id="cert-warn-hint">Applies once certificates are issued.</span>`,
		`<button type="submit" class="cv-btn cv-btn-primary cv-btn-sm" disabled>Save</button>`} {
		if !strings.Contains(edit, want) {
			t.Errorf("the Edit form lacks %s:\n%s", want, edit)
		}
	}
	if !strings.Contains(text(body), "Applies once certificates are issued. Sends to the channels set up in Notifications.") {
		t.Errorf("the Warn when row:\n%s", text(body))
	}
	// The post is still taken.
	if resp, _ := e.postForm(t, "/settings/certificates", url.Values{"warnBeforeDays": {"30"}}); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("a post with no issuer = %d, want 303", resp.StatusCode)
	}
	cs, err := e.Svc.CertificateSettings(t.Context())
	if err != nil || cs.WarnBeforeDays != 30 {
		t.Errorf("Warn when = %d, %v; want 30", cs.WarnBeforeDays, err)
	}

	// With Let's Encrypt it is a field like any other.
	e.Svc.SetACME("production", "ops@example.com")
	edit = editOf(e.pageOf(t, "/settings/certificates"))
	if strings.Contains(edit, "disabled") || strings.Contains(edit, "Applies once") {
		t.Errorf("with Let's Encrypt the form is locked:\n%s", edit)
	}
}
