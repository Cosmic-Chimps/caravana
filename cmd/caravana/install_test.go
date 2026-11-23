package main

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"text/template"
)

// The installer (deploy/install/install.sh) and the lab (deploy/dev/setup.sh)
// install the same Nomad and CNI plugins: the lab is where every feature is
// tested, so a host must run what the lab runs. Both check what they download.
func TestInstallerMatchesLab(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	installer, lab := read("../../deploy/install/install.sh"), read("../../deploy/dev/setup.sh")

	for _, name := range []string{"NOMAD_VERSION", "CNI_VERSION"} {
		pin := regexp.MustCompile(`(?m)^` + name + `=(\S+)$`)
		got, want := pin.FindStringSubmatch(installer), pin.FindStringSubmatch(lab)
		if got == nil || want == nil {
			t.Fatalf("%s is pinned in neither or only one of the scripts", name)
		}
		if got[1] != want[1] {
			t.Errorf("%s: the installer pins %s, the lab %s", name, got[1], want[1])
		}
	}

	// Every download is followed by a checksum check of what it fetched.
	if downloads, checks := strings.Count(installer, "curl -fsSL -o"), strings.Count(installer, "sha256sum"); downloads == 0 || checks < 3 {
		t.Errorf("the installer downloads %d files but checks checksums %d times; want a check for Nomad, CNI and the release", downloads, checks)
	}
}

// The console's route is the one static route in Traefik, and it must beat any
// app's router for the same host (an app that was given the console's domain
// once made the console answer 502). Traefik's default priority is the rule's
// length, so the router sets a priority far above it. The file's template is
// YAML inside HCL, which nothing here parses, so this checks the key is where
// Traefik's file provider reads it: a router's own key, beside its rule. Since
// feature 051 the routers are one per host of the console's hosts variable, so
// the rule names the template's {{ $host }}.
func TestTraefikConsoleRouteHasTopPriority(t *testing.T) {
	b, err := os.ReadFile("../../deploy/install/traefik.nomad.hcl")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	for i, line := range lines {
		rule, ok := strings.CutPrefix(strings.TrimLeft(line, " "), "rule: ")
		if !ok || !strings.Contains(rule, "{{ $host }}") {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " "))]
		if i+1 >= len(lines) {
			break
		}
		priority, ok := strings.CutPrefix(lines[i+1], indent+"priority: ")
		if !ok {
			break
		}
		if len(priority) < 4 || strings.Trim(priority, "0123456789") != "" {
			t.Errorf("priority = %q, want a whole number of at least 1000, far above a rule's length", priority)
		}
		return
	}
	t.Fatal("traefik.nomad.hcl: the console's router has no priority right after its rule")
}

// consoleRouteTemplate is the data of Traefik's caravana.yml template with the HCL interpolations
// done as Nomad does when the job is registered.
func consoleRouteTemplate(t *testing.T, consoleDomain string) string {
	t.Helper()
	b, err := os.ReadFile("../../deploy/install/traefik.nomad.hcl")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, `destination = "local/dynamic/caravana.yml"`)
	if i < 0 {
		t.Fatal("traefik.nomad.hcl: no caravana.yml template")
	}
	src = src[i:]
	start := strings.Index(src, "<<-YAML\n")
	end := strings.Index(src, "\n          YAML")
	if start < 0 || end < 0 {
		t.Fatal("traefik.nomad.hcl: the caravana.yml template's heredoc isn't where the test reads it")
	}
	if !strings.Contains(src[:start], `change_mode = "noop"`) {
		t.Error("the console's route template must be noop, so a change of address restarts nothing")
	}
	lines := strings.Split(src[start+len("<<-YAML\n"):end], "\n")
	for j, l := range lines {
		lines[j] = strings.TrimPrefix(l, "          ") // the heredoc's indent, as <<- strips it
	}
	data := strings.Join(lines, "\n")
	data = strings.ReplaceAll(data, "${var.console_domain}", consoleDomain)
	return strings.ReplaceAll(data, "${local.resolver}", "{certResolver: letsencrypt}")
}

// renderConsoleRoute renders the template the way Traefik's task does, with fakes of the two
// functions of Nomad's template engine it uses: nomadVarExists and nomadVar read variable (nil is
// no variable), and split takes the separator first, as consul-template's does.
func renderConsoleRoute(t *testing.T, consoleDomain string, variable map[string]string) string {
	t.Helper()
	tmpl, err := template.New("caravana.yml").Funcs(template.FuncMap{
		"nomadVarExists": func(path string) bool { return path == "nomad/jobs/traefik/traefik" && variable != nil },
		"nomadVar": func(path string) (map[string]string, error) {
			if path != "nomad/jobs/traefik/traefik" || variable == nil {
				return nil, fmt.Errorf("no variable %s", path)
			}
			return variable, nil
		},
		"split": func(sep, s string) []string { return strings.Split(s, sep) },
	}).Parse(consoleRouteTemplate(t, consoleDomain))
	if err != nil {
		t.Fatalf("the console's route template doesn't parse: %v", err)
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, nil); err != nil {
		t.Fatalf("the console's route template doesn't render: %v", err)
	}
	return out.String()
}

// The console's route (feature 051) is made from the variable's hosts, falls back to the job's
// console domain, and is `{}` when there is neither: Traefik refuses a file with `http:` and no
// router, and a template that fails to render kills the task.
func TestTraefikConsoleRouteRenders(t *testing.T) {
	routers := func(out string) []string {
		var names []string
		routerPart, _, _ := strings.Cut(out, "\n  services:")
		for _, l := range strings.Split(routerPart, "\n") {
			if strings.HasPrefix(l, "    caravana") {
				names = append(names, strings.TrimSpace(strings.TrimSuffix(l, ":")))
			}
		}
		return names
	}
	tests := []struct {
		name        string
		domain      string
		variable    map[string]string
		wantRouters []string
		wantRules   []string
		wantEmpty   bool
	}{
		{name: "no variable: the job's own domain", domain: "console.test", wantRouters: []string{"caravana"}, wantRules: []string{"Host(`console.test`)"}},
		{name: "a variable with other items only", domain: "console.test", variable: map[string]string{"CARAVANA_TEAM": "none"},
			wantRouters: []string{"caravana"}, wantRules: []string{"Host(`console.test`)"}},
		{name: "an empty hosts item", domain: "console.test", variable: map[string]string{"hosts": ""},
			wantRouters: []string{"caravana"}, wantRules: []string{"Host(`console.test`)"}},
		{name: "one host from the variable wins over the job's", domain: "console.test", variable: map[string]string{"hosts": "new.test"},
			wantRouters: []string{"caravana"}, wantRules: []string{"Host(`new.test`)"}},
		{name: "three hosts, a router each", domain: "old.test", variable: map[string]string{"hosts": "a.test b.test c.test"},
			wantRouters: []string{"caravana", "caravana-1", "caravana-2"},
			wantRules:   []string{"Host(`a.test`)", "Host(`b.test`)", "Host(`c.test`)"}},
		{name: "no variable and no domain", domain: "", wantEmpty: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := renderConsoleRoute(t, tt.domain, tt.variable)
			if tt.wantEmpty {
				if strings.TrimSpace(out) != "{}" {
					t.Errorf("rendered %q, want {}", out)
				}
				return
			}
			if got := routers(out); !slices.Equal(got, tt.wantRouters) {
				t.Errorf("routers = %v, want %v\n%s", got, tt.wantRouters, out)
			}
			for _, r := range tt.wantRules {
				if !strings.Contains(out, `rule: "`+r+`"`) {
					t.Errorf("no rule %s in\n%s", r, out)
				}
			}
			if n := strings.Count(out, "priority: 10000"); n != len(tt.wantRouters) {
				t.Errorf("%d routers have the top priority, want %d", n, len(tt.wantRouters))
			}
			if n := strings.Count(out, "tls: {certResolver: letsencrypt}"); n != len(tt.wantRouters) {
				t.Errorf("%d routers have their own certificate resolver, want %d", n, len(tt.wantRouters))
			}
			if strings.Count(out, "  services:\n    caravana:\n") != 1 || !strings.Contains(out, `- url: "http://127.0.0.1:7070"`) {
				t.Errorf("want the one service caravana to Caravana's loopback listener in\n%s", out)
			}
		})
	}
}
