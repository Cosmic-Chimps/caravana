package templates

import (
	"encoding/json"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
)

// goodTemplate is a small valid template: a web service with a domain and a
// password shared with its database.
func goodTemplate() Template {
	secret := func(s string) *string { return &s }
	return Template{
		Name:        "demo",
		Title:       "Demo",
		Version:     "1.0.0-1",
		Description: "A demo app.",
		Tags:        []string{"demo"},
		Upstream:    "https://example.com/demo",
		AppName:     "demo",
		Notes:       "Open https://{{domain}} now.",
		Inputs: []Input{
			{Name: "domain", Label: "Domain", Type: TypeDomain, Example: "demo.example.com"},
			{Name: "admin", Label: "Admin email", Type: TypeEmail, Example: "you@example.com"},
			{Name: "bucket", Label: "Bucket", Type: TypeText, Pattern: `[a-z0-9-]{3,20}`, Default: "demo-bucket"},
			{Name: "api_key", Label: "API key", Type: TypeSecret, Example: "example-key"},
		},
		Generated: []Generated{{Name: "db_password", Length: 32, Alphabet: AlphabetAlnum}},
		Services: map[string]compose.Overlay{
			"web": {
				Domains: []compose.Domain{{Host: "{{domain}}", Port: 8080}},
				Health:  &compose.Health{Path: "/healthz"},
				Secrets: []app.Secret{
					{Name: "DATABASE_URL", Value: secret("postgres://demo:{{db_password}}@db:5432/demo")},
					{Name: "API_KEY", Value: secret("{{api_key}}")},
				},
			},
			"db": {
				Secrets: []app.Secret{{Name: "POSTGRES_PASSWORD", Value: secret("{{db_password}}")}},
				Backup:  &app.Backup{Engine: app.BackupPostgres},
			},
		},
		Compose: `services:
  web:
    image: example/demo:1.0.0
    expose: ["8080"]
    environment:
      ADMIN_EMAIL: "{{admin}}"
      BUCKET: "{{bucket}}"
      PUBLIC_URL: "https://{{domain}}"
  db:
    image: postgres:17
    expose: ["5432"]
    environment:
      POSTGRES_USER: demo
      POSTGRES_DB: demo
    volumes:
      - data:/var/lib/postgresql/data
volumes:
  data: {}
`,
	}
}

// bundleOf makes a bundle holding the templates.
func bundleOf(t *testing.T, version string, ts ...Template) []byte {
	t.Helper()
	b, err := json.Marshal(Catalog{Format: Format, Version: version, Templates: ts})
	if err != nil {
		t.Fatal(err)
	}
	return b
}
