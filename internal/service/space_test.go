package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// dg is the digest made of one hex digit: dg("a") is sha256:aaaa….
func dg(c string) string { return "sha256:" + strings.Repeat(c, 64) }

// pin is a pinned image reference for the digest dg(c).
func pin(repo, c string) string { return repo + "@" + dg(c) }

func appOf(id string, images ...string) store.AppRecord {
	a := app.App{ID: id}
	for i, image := range images {
		a.Services = append(a.Services, app.Service{Name: fmt.Sprintf("svc%d", i), Image: image})
	}
	return store.AppRecord{App: a}
}

func depOf(app string, status store.Status, withDefinition bool, images ...string) store.DeploymentRecord {
	d := store.DeploymentRecord{AppID: app, Status: status, Deployment: deploymentOf(images)}
	if withDefinition {
		def := appOf(app, images...).App
		d.Definition = &def
	}
	return d
}

func deploymentOf(images []string) app.Deployment {
	d := app.Deployment{Digests: map[string]string{}}
	for i, image := range images {
		d.Digests[fmt.Sprintf("svc%d", i)] = image
	}
	return d
}

func TestKeepFrom(t *testing.T) {
	helper := pin("busybox", "9")
	tests := []struct {
		name    string
		apps    []store.AppRecord
		deps    map[string][]store.DeploymentRecord
		helpers []string
		n       int
		want    []string
	}{
		{"nothing", nil, nil, nil, 3, []string{}},
		{"helpers only, a tag is no digest", nil, nil, []string{helper, "busybox:latest"}, 3, []string{dg("9")}},
		{
			"the newest n healthy of an app",
			[]store.AppRecord{appOf("web", "web:latest")},
			map[string][]store.DeploymentRecord{"web": {
				depOf("web", store.StatusHealthy, true, pin("web", "1")),
				depOf("web", store.StatusHealthy, true, pin("web", "2")),
				depOf("web", store.StatusHealthy, true, pin("web", "3")),
				depOf("web", store.StatusHealthy, true, pin("web", "4")),
			}},
			nil, 3, []string{dg("1"), dg("2"), dg("3")},
		},
		{
			"failed, reverted and superseded deployments keep nothing; a healthy one with no definition can't be rolled back to",
			[]store.AppRecord{appOf("web")},
			map[string][]store.DeploymentRecord{"web": {
				depOf("web", store.StatusFailed, true, pin("web", "1")),
				depOf("web", store.StatusReverted, true, pin("web", "2")),
				depOf("web", store.StatusSuperseded, true, pin("web", "3")),
				depOf("web", store.StatusHealthy, false, pin("web", "4")),
				depOf("web", store.StatusHealthy, true, pin("web", "5")),
			}},
			nil, 3, []string{dg("5")},
		},
		{
			"a deployment under way is kept past the count",
			[]store.AppRecord{appOf("web")},
			map[string][]store.DeploymentRecord{"web": {
				depOf("web", store.StatusPending, false, pin("web", "1")),
				depOf("web", store.StatusRunning, true, pin("web", "2")),
				depOf("web", store.StatusHealthy, true, pin("web", "3")),
			}},
			nil, 1, []string{dg("1"), dg("2"), dg("3")},
		},
		{
			"a stored app's current definition counts even with no deployment (a stopped or never deployed app)",
			[]store.AppRecord{appOf("old", pin("old", "7"), "other:tag")},
			nil, nil, 3, []string{dg("7")},
		},
		{
			"two services of a deployment, the same digest twice, two apps, sorted once",
			[]store.AppRecord{appOf("a", pin("x", "2")), appOf("b")},
			map[string][]store.DeploymentRecord{
				"a": {depOf("a", store.StatusHealthy, true, pin("x", "2"), pin("y", "1"))},
				"b": {depOf("b", store.StatusHealthy, true, pin("z", "2"))},
			},
			[]string{pin("busybox", "0"), pin("busybox", "0")}, 3, []string{dg("0"), dg("1"), dg("2")},
		},
		{
			"the deployments of another app aren't read",
			[]store.AppRecord{appOf("a")},
			map[string][]store.DeploymentRecord{"gone": {depOf("gone", store.StatusHealthy, true, pin("x", "1"))}},
			nil, 3, []string{},
		},
		{
			"the definition's images count beside the recorded digests",
			[]store.AppRecord{appOf("a")},
			map[string][]store.DeploymentRecord{"a": {func() store.DeploymentRecord {
				d := depOf("a", store.StatusHealthy, true, pin("x", "1"))
				def := appOf("a", pin("x", "2")).App
				d.Definition = &def
				return d
			}()}},
			nil, 3, []string{dg("1"), dg("2")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := keepFrom(tt.apps, tt.deps, tt.helpers, tt.n)
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("keep = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestKeepFromCap(t *testing.T) {
	var images []string
	for i := range maxKeepDigests + 1 {
		images = append(images, fmt.Sprintf("x@sha256:%064x", i))
	}
	got, err := keepFrom(nil, nil, images[:maxKeepDigests], 3)
	if err != nil || len(got) != maxKeepDigests {
		t.Fatalf("at the cap: %d, %v", len(got), err)
	}
	if _, err := keepFrom(nil, nil, images, 3); !errors.Is(err, ErrKeepListTooLong) {
		t.Errorf("over the cap = %v, want ErrKeepListTooLong", err)
	}
}

func TestDigestOf(t *testing.T) {
	for in, want := range map[string]string{
		pin("traefik/whoami", "c"):              dg("c"),
		"ghcr.io/acme/web@" + dg("a"):           dg("a"),
		"web:latest":                            "",
		"web@sha256:abc":                        "",
		"web@sha256:" + strings.Repeat("A", 64): "",
		"":                                      "",
	} {
		got, ok := digestOf(in)
		if got != want || ok != (want != "") {
			t.Errorf("digestOf(%q) = %q, %v", in, got, ok)
		}
	}
}

// The image scanner's image (feature 044) runs only during a scan, so no task holds it between scans:
// the keep list names it while the add-on is installed, and not before or after.
func TestKeepDigestsKeepsTheScannerWhileInstalled(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	scanner, ok := digestOf(e.svc.deployer.ScanImage())
	if !ok {
		t.Fatalf("the scanner image %q isn't pinned by digest", e.svc.deployer.ScanImage())
	}
	keeps := func() bool {
		t.Helper()
		keep, err := e.svc.keepDigests(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return slices.Contains(keep, scanner)
	}
	if keeps() {
		t.Error("the scanner's image is kept while the add-on isn't installed")
	}
	if err := e.store.SaveAddon(ctx, store.Addon{Name: store.AddonImageScanner, State: store.AddonInstalled, Version: "0.117.0"}); err != nil {
		t.Fatal(err)
	}
	if !keeps() {
		t.Error("the scanner's image isn't kept while the add-on is installed")
	}
}
