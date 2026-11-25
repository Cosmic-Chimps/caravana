package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"text/template"
)

// Feature 034: Traefik's static configuration and the uploaded certificates are files its task
// renders from Nomad Variables. These tests render the two jobs' templates the way Nomad does, with
// fakes of the template functions they use, because a template that fails to render kills the task
// and a wrong line in the static configuration takes HTTP down. That Traefik reads the result was
// checked in the lab (research R1-R6).

// traefikTemplate is the data of the template of path whose destination is dest, with the HCL
// interpolations done as Nomad does when the job is registered (vars), and the heredoc's indent
// removed as <<- does.
func traefikTemplate(t *testing.T, path, dest string, vars map[string]string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, `destination = "`+dest+`"`)
	if i < 0 {
		t.Fatalf("%s: no template for %s", path, dest)
	}
	src = src[i:]
	open := strings.Index(src, "<<-")
	nl := strings.Index(src[open:], "\n")
	word := strings.TrimSpace(src[open+3 : open+nl])
	body := src[open+nl+1:]
	end := strings.Index(body, "\n          "+word)
	if end < 0 {
		t.Fatalf("%s: the %s heredoc isn't where the test reads it", path, dest)
	}
	lines := strings.Split(body[:end], "\n")
	for j, l := range lines {
		lines[j] = strings.TrimPrefix(l, "          ")
	}
	data := strings.Join(lines, "\n")
	for k, v := range vars {
		data = strings.ReplaceAll(data, "${"+k+"}", v)
	}
	if strings.Contains(data, "${") {
		t.Fatalf("%s: %s has an HCL interpolation the test doesn't set: %s", path, dest, data[strings.Index(data, "${"):][:40])
	}
	return data
}

// renderTraefik renders data with variables standing for Nomad's: each path's items (an item is a
// struct that prints as its value, as consul-template's is; see varItem).
func renderTraefik(t *testing.T, data string, variables map[string]map[string]string) string {
	t.Helper()
	items := func(path string) (map[string]varItem, error) {
		v, ok := variables[path]
		if !ok {
			return nil, fmt.Errorf("no variable %s", path)
		}
		out := make(map[string]varItem, len(v))
		for k, val := range v {
			out[k] = varItem{Key: k, Value: val}
		}
		return out, nil
	}
	type listed struct{ Path string }
	tmpl, err := template.New("t").Funcs(template.FuncMap{
		"nomadVarExists": func(path string) bool { _, ok := variables[path]; return ok },
		"nomadVar":       items,
		"nomadVarList": func(prefix string) []listed {
			var out []listed
			for p := range variables {
				if strings.HasPrefix(p, prefix+"/") {
					out = append(out, listed{Path: p})
				}
			}
			return out
		},
		"toJSON": func(v any) (string, error) {
			s := fmt.Sprint(v)
			return fmt.Sprintf("%q", s), nil
		},
		"regexMatch": func(string, string) bool { return false },
	}).Parse(data)
	if err != nil {
		t.Fatalf("the template doesn't parse: %v", err)
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, nil); err != nil {
		t.Fatalf("the template doesn't render: %v", err)
	}
	return out.String()
}

var traefikJobs = []struct {
	name, path string
	vars       map[string]string
	storageDir string
}{
	{"host", "../../deploy/install/traefik.nomad.hcl", map[string]string{
		"local.acme_ca": "https://acme-v02.api.letsencrypt.org/directory", "var.acme_email": "owner@example.com",
	}, "/acme/"},
	{"lab", "../../deploy/dev/traefik.nomad.hcl", nil, "/local/"},
}

func TestTraefikStaticConfigRenders(t *testing.T) {
	pebble := map[string]string{
		"server": "https://localhost:14000/dir", "email": "ops@example.com", "storage": "acme-0123456789ab.json",
		"eab_kid": "kid-1", "eab_hmac": "s3cret-hmac-034", "ca": "-----BEGIN CERTIFICATE-----\nX\n-----END CERTIFICATE-----\n",
	}
	noEAB := map[string]string{
		"server": "https://acme.example/dir", "email": "ops@example.com", "storage": "acme-aaaaaaaaaaaa.json",
		"eab_kid": "", "eab_hmac": "", "ca": "",
	}
	for _, job := range traefikJobs {
		data := traefikTemplate(t, job.path, "secrets/traefik.yml", job.vars)
		t.Run(job.name+"/installer's issuer", func(t *testing.T) {
			out := renderTraefik(t, data, nil)
			for _, want := range []string{"directory: /secrets/dynamic", "storage: " + job.storageDir + "acme.json", "entryPoint: web", "format: json"} {
				if !strings.Contains(out, want) {
					t.Errorf("no %q in\n%s", want, out)
				}
			}
			for _, not := range []string{"eab:", "caCertificates"} {
				if strings.Contains(out, not) {
					t.Errorf("%q without a custom issuer:\n%s", not, out)
				}
			}
			if job.name == "host" && !strings.Contains(out, `caServer: "https://acme-v02.api.letsencrypt.org/directory"`) {
				t.Errorf("the installer's issuer isn't the caServer:\n%s", out)
			}
		})
		t.Run(job.name+"/custom issuer with EAB and its CA", func(t *testing.T) {
			out := renderTraefik(t, data, map[string]map[string]string{"caravana/acme": pebble})
			for _, want := range []string{
				`caServer: "https://localhost:14000/dir"`, `email: "ops@example.com"`,
				`storage: "` + job.storageDir + `acme-0123456789ab.json"`,
				"eab:\n        kid: \"kid-1\"\n        hmacEncoded: \"s3cret-hmac-034\"",
				"caCertificates: [/secrets/acme-ca.pem]",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("no %q in\n%s", want, out)
				}
			}
		})
		t.Run(job.name+"/custom issuer without EAB", func(t *testing.T) {
			out := renderTraefik(t, data, map[string]map[string]string{"caravana/acme": noEAB})
			if !strings.Contains(out, `caServer: "https://acme.example/dir"`) || strings.Contains(out, "eab:") || strings.Contains(out, "caCertificates") {
				t.Errorf("want the server alone:\n%s", out)
			}
		})
	}
}

func TestTraefikUploadedCertificatesRender(t *testing.T) {
	for _, job := range traefikJobs {
		data := traefikTemplate(t, job.path, "secrets/dynamic/certificates.yml", job.vars)
		t.Run(job.name+"/none", func(t *testing.T) {
			if out := renderTraefik(t, data, nil); strings.TrimSpace(out) != "{}" {
				t.Errorf("rendered %q, want {}: Traefik refuses a tls: with nothing under it", out)
			}
		})
		t.Run(job.name+"/two", func(t *testing.T) {
			out := renderTraefik(t, data, map[string]map[string]string{
				"caravana/certificates/crt_000000000001": {"certificate": "CHAIN-1\n", "key": "KEY-1\n", "names": "*.a.test"},
				"caravana/certificates/crt_000000000002": {"certificate": "CHAIN-2\n", "key": "KEY-2\n", "names": "b.test"},
				"caravana/acme":                          {"server": "x"},
			})
			if !strings.HasPrefix(strings.TrimSpace(out), "tls:\n  certificates:") || strings.Count(out, "- certFile:") != 2 {
				t.Errorf("want two certificates under tls.certificates:\n%s", out)
			}
			for _, want := range []string{`certFile: "CHAIN-1\n"`, `keyFile: "KEY-2\n"`} {
				if !strings.Contains(out, want) {
					t.Errorf("no %q in\n%s", want, out)
				}
			}
		})
	}
}

// The lab's job is the host's apart from what its header names; the templates of feature 034 must not
// drift apart.
func TestTraefikJobsShareTheCertificateTemplates(t *testing.T) {
	host := traefikTemplate(t, traefikJobs[0].path, "secrets/dynamic/certificates.yml", traefikJobs[0].vars)
	lab := traefikTemplate(t, traefikJobs[1].path, "secrets/dynamic/certificates.yml", nil)
	if host != lab {
		t.Error("the uploaded certificates template differs between deploy/install and deploy/dev")
	}
	for _, job := range traefikJobs {
		b, err := os.ReadFile(job.path)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, want := range []string{`caravana_certificates = "1"`, `args = ["--configFile=/secrets/traefik.yml"]`, `destination = "secrets/dynamic/team.yml"`} {
			if !strings.Contains(src, want) {
				t.Errorf("%s has no %s", job.path, want)
			}
		}
	}
}
