package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
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
// Traefik's file provider reads it: a router's own key, beside its rule.
func TestTraefikConsoleRouteHasTopPriority(t *testing.T) {
	b, err := os.ReadFile("../../deploy/install/traefik.nomad.hcl")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	for i, line := range lines {
		rule, ok := strings.CutPrefix(strings.TrimLeft(line, " "), "rule: ")
		if !ok || !strings.Contains(rule, "${var.console_domain}") {
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
