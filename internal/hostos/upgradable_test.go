package hostos

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestParseUpgradable(t *testing.T) {
	const warning = "WARNING: apt does not have a stable CLI interface. Use with caution in scripts.\n\n"
	tests := []struct {
		name            string
		out             string
		total, security int
	}{
		{"empty output", "", 0, 0},
		{"nothing waiting", "Listing...\n", 0, 0},
		{"debian 12", warning + "Listing...\n" +
			"curl/bookworm-security 7.88.1-10+deb12u8 amd64 [upgradable from: 7.88.1-10+deb12u5]\n" +
			"libc6/bookworm,bookworm-security 2.36-9+deb12u9 arm64 [upgradable from: 2.36-9+deb12u7]\n" +
			"tzdata/bookworm-updates 2024b-0+deb12u1 all [upgradable from: 2024a-0+deb12u1]\n" +
			"vim/stable-security,stable 2:9.0.1378-2+deb12u2 amd64 [upgradable from: 2:9.0.1378-2]\n", 4, 3},
		{"debian 13", "Listing... Done\n" +
			"openssl/trixie-security 3.5.1-1+deb13u1 amd64 [upgradable from: 3.5.0-1]\n" +
			"bash/stable 5.2.37-2+b3 amd64 [upgradable from: 5.2.37-2+b2]\n", 2, 1},
		{"ubuntu 24.04", "Listing...\n" +
			"libssl3t64/noble-updates,noble-security 3.0.13-0ubuntu3.5 amd64 [upgradable from: 3.0.13-0ubuntu3.4]\n" +
			"linux-image-generic/noble-updates 6.8.0-50.51 amd64 [upgradable from: 6.8.0-49.49]\n" +
			"snapd/noble-updates 2.66.1+24.04 amd64 [upgradable from: 2.65.3+24.04]\n", 3, 1},
		{"notes and blank lines are not packages", "\nListing...\nN: There is 1 additional version.\n\nE: odd\n", 0, 0},
		{"a line that is not a package", "garbage\nname/ only\n", 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseUpgradable([]byte(tc.out))
			if !got.Known || got.Total != tc.total || got.Security != tc.security {
				t.Errorf("got %+v, want known with %d total and %d security", got, tc.total, tc.security)
			}
		})
	}
}

func TestParseUpgradableReadsAtMostTenThousandLines(t *testing.T) {
	line := "pkg/bookworm-security 1 amd64 [upgradable from: 0]\n"
	got := ParseUpgradable([]byte(strings.Repeat(line, 12000)))
	if got.Total != maxUpgradableLines {
		t.Errorf("Total = %d, want %d", got.Total, maxUpgradableLines)
	}
}

func TestListsRefreshedAtAndAptStamp(t *testing.T) {
	stamp := time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)
	lists := time.Date(2026, 11, 9, 6, 0, 0, 0, time.UTC)
	dpkg := time.Date(2026, 11, 8, 6, 0, 0, 0, time.UTC)

	empty := fstest.MapFS{}
	if got := ListsRefreshedAt(empty); !got.IsZero() {
		t.Errorf("empty host: %v", got)
	}
	if got := AptStamp(empty); got != "none|none|none" {
		t.Errorf("empty host stamp = %q", got)
	}

	onlyLists := fstest.MapFS{"var/lib/apt/lists": {Mode: os.ModeDir, ModTime: lists}}
	if got := ListsRefreshedAt(onlyLists); !got.Equal(lists) {
		t.Errorf("lists only: %v, want %v", got, lists)
	}

	full := fstest.MapFS{
		"var/lib/apt/periodic/update-success-stamp": {ModTime: stamp},
		"var/lib/apt/lists/x":                       {ModTime: lists},
		"var/lib/dpkg/status":                       {ModTime: dpkg},
	}
	// MapFS makes the lists directory from its file; give it its own time.
	full["var/lib/apt/lists"] = &fstest.MapFile{Mode: os.ModeDir, ModTime: lists}
	if got := ListsRefreshedAt(full); !got.Equal(stamp) {
		t.Errorf("full host: %v, want the stamp %v", got, stamp)
	}
	before := AptStamp(full)
	full["var/lib/dpkg/status"] = &fstest.MapFile{ModTime: dpkg.Add(time.Hour)}
	if after := AptStamp(full); after == before {
		t.Error("the stamp did not change with dpkg's status")
	}
}

// fakeApt puts a program called apt first on PATH that runs script.
func fakeApt(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "apt"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestAptRunMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := (Apt{}).Run(context.Background()); !errors.Is(err, ErrAptUnavailable) {
		t.Errorf("err = %v, want ErrAptUnavailable", err)
	}
}

func TestAptRun(t *testing.T) {
	t.Run("its output, with the arguments and the C locale", func(t *testing.T) {
		fakeApt(t, `[ "$1 $2" = "list --upgradable" ] || exit 9
echo "WARNING: unstable CLI" >&2
printf 'Listing...\ncurl/bookworm-security 1 amd64 [upgradable from: 0]\nlocale=%s\n' "$LC_ALL"`)
		out, err := Apt{}.Run(context.Background())
		if err != nil || !strings.Contains(string(out), "locale=C.UTF-8") || strings.Contains(string(out), "WARNING") {
			t.Errorf("out = %q, err = %v", out, err)
		}
	})
	t.Run("a failure never carries its words", func(t *testing.T) {
		fakeApt(t, "echo SENTINEL-OUT; echo SENTINEL-ERR >&2; exit 3")
		_, err := Apt{}.Run(context.Background())
		if err == nil || strings.Contains(err.Error(), "SENTINEL") || !strings.Contains(err.Error(), "code 3") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a cancelled run", func(t *testing.T) {
		fakeApt(t, "sleep 30")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := (Apt{}).Run(ctx); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want Canceled", err)
		}
	})
}
