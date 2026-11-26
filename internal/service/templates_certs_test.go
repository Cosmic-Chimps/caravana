package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/templates"
)

// certsTemplate asks for a certificate authority and two certificates, keeps them in secret files, and keeps
// a password's bcrypt hash in another.
func certsLabTemplate() templates.Template {
	secret := func(s string) *string { return &s }
	t := whoamiTemplate()
	t.Requires = "0.24.0"
	t.Inputs = []templates.Input{
		{Name: "domain", Label: "Domain", Type: templates.TypeDomain, Example: "demo.example.com"},
		{Name: "admin_password", Label: "Admin password", Type: templates.TypeSecret, Example: "example-password"},
	}
	t.Generated = nil
	subject := func(cn string) []templates.DNPart {
		return []templates.DNPart{{Type: "CN", Value: cn}, {Type: "OU", Value: "Demo"}, {Type: "O", Value: "Demo"}, {Type: "C", Value: "US"}}
	}
	t.Certificates = &templates.Certificates{
		CA: templates.CertAuthority{Subject: subject("Demo CA")},
		Leaves: []templates.CertLeaf{
			{Name: "node", Subject: subject("node"), DNSNames: []string{"web"}},
			{Name: "admin", Subject: subject("admin")},
		},
	}
	t.Services = map[string]compose.Overlay{"web": {
		Domains: []compose.Domain{{Host: "{{domain}}", Port: 80}},
		Health:  &compose.Health{Path: "/"},
		Secrets: []app.Secret{
			{Name: "ADMIN_PASSWORD", Value: secret("{{admin_password}}")},
			{Name: "CA_CERT", Value: secret("{{ca_cert}}")},
			{Name: "NODE_CERT", Value: secret("{{node_cert}}")},
			{Name: "NODE_KEY", Value: secret("{{node_key}}")},
			{Name: "ADMIN_CERT", Value: secret("{{admin_cert}}")},
			{Name: "ADMIN_KEY", Value: secret("{{admin_key}}")},
			{Name: "USERS", Value: secret("admin:\n  hash: \"{{admin_password|bcrypt}}\"\n")},
		},
		Files: []app.File{
			{Path: "/certs/ca.pem", Secret: "CA_CERT", Mode: "0444"},
			{Path: "/certs/node.pem", Secret: "NODE_CERT", Mode: "0444"},
			{Path: "/certs/node.key", Secret: "NODE_KEY", Mode: "0400"},
			{Path: "/certs/admin.pem", Secret: "ADMIN_CERT", Mode: "0444"},
			{Path: "/certs/admin.key", Secret: "ADMIN_KEY", Mode: "0400"},
			{Path: "/etc/users.yml", Secret: "USERS", Mode: "0400"},
		},
	}}
	return t
}

// keyBody is a line of a PEM's base64 that no other text has: the first of the body.
func keyBody(t *testing.T, pem string) string {
	t.Helper()
	lines := strings.Split(pem, "\n")
	if len(lines) < 3 || len(lines[1]) < 40 {
		t.Fatalf("not a PEM:\n%.80s", pem)
	}
	return lines[1]
}

// Spec 060: the certificates and keys a template makes, and the hashes it writes, reach the secret store and
// nothing else: no store row, kept file, deployment record, answer, Nomad job, error or log line.
func TestInstallTemplateKeepsCertificatesOut(t *testing.T) {
	ctx := context.Background()
	l := newTemplateLab(t)
	const typed = "s3cret-sentinel-060-typed"
	l.useCatalog(t, futureCatVer, certsLabTemplate())

	inputs := map[string]string{"domain": "demo.example.com", "admin_password": typed}
	preview, err := l.svc.PreviewTemplate(ctx, "demo", "demo", inputs, TemplateInstallOptions{})
	if err != nil {
		t.Fatalf("PreviewTemplate: %v", err)
	}
	res, err := l.svc.InstallTemplate(ctx, "demo", "demo", inputs, TemplateInstallOptions{}, app.TriggerManual)
	if err != nil {
		t.Fatalf("InstallTemplate: %v", err)
	}
	final := installed(t, l, res)
	waitFor(t, "the secrets to be committed", func() bool { owner, _ := l.secrets.Kept("demo"); return owner == "" })

	// The secret store has them.
	live := l.secrets.Live("demo")["web"]
	nodeKey, adminKey := live["NODE_KEY"], live["ADMIN_KEY"]
	if !strings.HasPrefix(nodeKey, "-----BEGIN PRIVATE KEY-----\n") || !strings.HasPrefix(live["NODE_CERT"], "-----BEGIN CERTIFICATE-----\n") ||
		!strings.HasPrefix(live["CA_CERT"], "-----BEGIN CERTIFICATE-----\n") || live["ADMIN_PASSWORD"] != typed {
		t.Fatalf("the secret store has %d names, the key starts %.30q", len(live), nodeKey)
	}
	hash := strings.Trim(strings.TrimSpace(strings.TrimPrefix(strings.Split(live["USERS"], "\n")[1], "  hash:")), `"`)
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(typed)); err != nil || strings.Contains(live["USERS"], typed) {
		t.Errorf("USERS = %q: not the hash of the typed password (%v)", live["USERS"], err)
	}

	// Nothing else does.
	rec, _ := l.svc.GetApp(ctx, "demo")
	answers := map[string]any{
		"the preview":          preview,
		"the install's answer": res,
		"the final record":     final,
		"the stored app":       rec,
		"the Nomad job":        l.nomad.Job("demo"),
		"the deployments":      mustList(t, l),
	}
	needles := []string{keyBody(t, nodeKey), keyBody(t, adminKey), keyBody(t, live["NODE_CERT"]), keyBody(t, live["CA_CERT"]), typed, hash}
	for what, v := range answers {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		for _, n := range needles {
			if strings.Contains(string(b), n) {
				t.Errorf("%s holds %.40q", what, n)
			}
		}
	}
	raw, logs := rawStore(t, l.dir), l.logs.String()
	for _, n := range needles {
		if strings.Contains(raw, n) {
			t.Errorf("the SQLite file holds %.40q", n)
		}
		if strings.Contains(logs, n) {
			t.Errorf("the log holds %.40q", n)
		}
	}
	// The kept Compose file and its overlay have names and paths only; the files name secrets and hold no text.
	web := rec.Compose.Services["web"]
	for _, s := range web.Secrets {
		if s.Value != nil {
			t.Errorf("the kept overlay has a value for %s", s.Name)
		}
	}
	for _, f := range web.Files {
		if f.Secret == "" || f.Content != "" {
			t.Errorf("kept file %+v holds content", f)
		}
	}
	if strings.Contains(rec.Compose.Compose, "BEGIN") {
		t.Error("the kept Compose text holds a PEM")
	}

	// A redeploy keeps the certificates it made: nothing is made twice.
	again, err := l.svc.Redeploy(ctx, "demo", app.TriggerManual)
	if err != nil {
		t.Fatalf("Redeploy: %v", err)
	}
	if final := waitFinal(t, l.store, again.Deployment.ID); final.Status != "healthy" {
		t.Fatalf("redeploy = %s (%s)", final.Status, final.Error)
	}
	if got := l.secrets.Live("demo")["web"]["NODE_KEY"]; got != nodeKey {
		t.Error("the key changed across the redeploy")
	}
}
