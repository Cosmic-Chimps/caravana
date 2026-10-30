package hostos

import (
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestReadHistoryFixtures(t *testing.T) {
	root := os.DirFS("testdata/host")
	runs, err := ReadHistory(root, 20)
	if err != nil {
		t.Fatal(err)
	}
	// Four runs in the current log, two in the rotated one, newest first.
	wantStarts := []string{
		"2026-10-28 06:12:44", "2026-10-27 07:30:00", "2026-10-26 09:14:02", "2026-10-24 06:06:23",
		"2026-09-20 06:05:10", "2026-09-02 11:00:00",
	}
	if len(runs) != len(wantStarts) {
		t.Fatalf("got %d runs, want %d: %+v", len(runs), len(wantStarts), runs)
	}
	for i, w := range wantStarts {
		if got := runs[i].Start.Format("2006-01-02 15:04:05"); got != w {
			t.Errorf("run %d starts %s, want %s", i, got, w)
		}
	}

	t.Run("an automatic run", func(t *testing.T) {
		r := runs[3]
		if !r.Automatic || r.Command != "/usr/bin/unattended-upgrade" {
			t.Errorf("automatic = %v, command %q", r.Automatic, r.Command)
		}
		if r.Packages() != 4 || len(r.Installed) != 0 {
			t.Errorf("packages = %d, installed %d", r.Packages(), len(r.Installed))
		}
		if r.End.Sub(r.Start) != 18*time.Second {
			t.Errorf("took %v", r.End.Sub(r.Start))
		}
		p := r.Upgraded[0]
		if p.Name != "libc6" || p.Arch != "amd64" || p.Old != "2.39-0ubuntu8.5" || p.New != "2.39-0ubuntu8.6" {
			t.Errorf("first package = %+v", p)
		}
	})
	t.Run("a manual run", func(t *testing.T) {
		r := runs[2]
		if r.Automatic || r.Command != "apt install htop" {
			t.Errorf("automatic = %v, command %q", r.Automatic, r.Command)
		}
		if len(r.Installed) != 2 || r.Packages() != 2 {
			t.Fatalf("installed = %+v", r.Installed)
		}
		if p := r.Installed[0]; p.Name != "htop" || p.Old != "" || p.New != "3.3.0-4build1" {
			t.Errorf("htop = %+v", p)
		}
		// "automatic" marks a dependency, it is not a version.
		if p := r.Installed[1]; p.Name != "libnl-genl-3-200" || p.New != "3.7.0-0.3build1.1" {
			t.Errorf("dependency = %+v", p)
		}
	})
	t.Run("a removal updates no package", func(t *testing.T) {
		r := runs[1]
		if r.Packages() != 0 || len(r.Removed) != 2 || r.Removed[0].Name != "linux-image-6.8.0-141-generic" {
			t.Errorf("removed run = %+v", r)
		}
	})
	t.Run("a kernel run", func(t *testing.T) {
		r := runs[0]
		if !r.Automatic || r.Packages() != 4 {
			t.Fatalf("run = %+v", r)
		}
		if p := r.Upgraded[0]; p.Name != "linux-image-6.8.0-149-generic" || p.New != "6.8.0-149.149" {
			t.Errorf("kernel = %+v", p)
		}
	})
	t.Run("the rotated file", func(t *testing.T) {
		if r := runs[4]; !r.Automatic || r.Packages() != 2 || r.Upgraded[0].Name != "tzdata" || r.Upgraded[0].Arch != "all" {
			t.Errorf("rotated automatic = %+v", r)
		}
		if r := runs[5]; r.Automatic || r.Command != "apt upgrade" {
			t.Errorf("rotated manual = %+v", r)
		}
	})
}

func TestReadHistoryLimit(t *testing.T) {
	root := os.DirFS("testdata/host")
	for _, tc := range []struct {
		n       int
		want    int
		oldest  string
		message string
	}{
		{1, 1, "2026-10-28", "newest only"},
		{4, 4, "2026-10-24", "all of the current file"},
		{5, 5, "2026-09-20", "one from the rotated file"},
		{0, 0, "", "nothing asked"},
	} {
		runs, err := ReadHistory(root, tc.n)
		if err != nil || len(runs) != tc.want {
			t.Errorf("%s: %d runs, err %v", tc.message, len(runs), err)
			continue
		}
		if tc.want > 0 && runs[len(runs)-1].Start.Format("2006-01-02") != tc.oldest {
			t.Errorf("%s: oldest %s, want %s", tc.message, runs[len(runs)-1].Start, tc.oldest)
		}
	}
}

func TestReadHistoryMissingAndDamaged(t *testing.T) {
	const log = "\nStart-Date: 2026-10-24  06:06:23\nCommandline: /usr/bin/unattended-upgrade\nUpgrade: a:amd64 (1, 2)\nEnd-Date: 2026-10-24  06:06:41\n"
	tests := []struct {
		name     string
		fs       fstest.MapFS
		wantRuns int
		wantErr  bool
	}{
		{"no files at all is no history and no error", fstest.MapFS{}, 0, false},
		{"only the current file", fstest.MapFS{historyFile: {Data: []byte(log)}}, 1, false},
		{"a damaged rotated file is an error beside what was read",
			fstest.MapFS{historyFile: {Data: []byte(log)}, historyRotated: {Data: []byte("not gzip at all")}}, 1, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Asking for more than the current file has reaches the rotated one.
			runs, err := ReadHistory(tc.fs, 5)
			if len(runs) != tc.wantRuns || (err != nil) != tc.wantErr {
				t.Errorf("got %d runs, err %v; want %d runs, error %v", len(runs), err, tc.wantRuns, tc.wantErr)
			}
		})
	}
}

func TestParseHistoryBlocks(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"a block without a start date is dropped", "Commandline: apt install x\nInstall: x:amd64 (1)\n\n", 0},
		{"a start date that isn't one is dropped", "Start-Date: yesterday\nCommandline: apt\n\n", 0},
		{"two blocks without a blank line between", "Start-Date: 2026-10-24  06:06:23\nCommandline: apt\nStart-Date: 2026-10-24  07:00:00\nCommandline: apt\n", 2},
		{"unknown keys, windows line ends and a missing end", "Start-Date: 2026-10-24  06:06:23\r\nRequested-By: x (1)\r\nError: boom\r\nCommandline: apt\r\n", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runs, err := parseHistory(strings.NewReader(tc.in))
			if err != nil || len(runs) != tc.want {
				t.Errorf("got %d runs, err %v; want %d", len(runs), err, tc.want)
			}
		})
	}
}

func TestParsePackages(t *testing.T) {
	tests := []struct {
		name string
		in   string
		kind lineKind
		want []Package
	}{
		{"an upgrade", "a:amd64 (1.0, 1.1), b:all (2, 3)", kindUpgrade,
			[]Package{{"a", "amd64", "1.0", "1.1"}, {"b", "all", "2", "3"}}},
		{"an install and its dependency", "a:amd64 (1.0), b:amd64 (2.0, automatic)", kindInstall,
			[]Package{{"a", "amd64", "", "1.0"}, {"b", "amd64", "", "2.0"}}},
		{"a removal", "a:amd64 (1.0)", kindRemove, []Package{{"a", "amd64", "1.0", ""}}},
		{"no architecture", "a (1, 2)", kindUpgrade, []Package{{"a", "", "1", "2"}}},
		{"a damaged tail keeps what came before it", "a:amd64 (1, 2), b:amd64 (3", kindUpgrade, []Package{{"a", "amd64", "1", "2"}}},
		{"nothing", "", kindUpgrade, nil},
		{"epochs and plus signs stay in the version", "vim:amd64 (2:9.1.0016-1ubuntu7.5, 2:9.1.0016-1ubuntu7.6)", kindUpgrade,
			[]Package{{"vim", "amd64", "2:9.1.0016-1ubuntu7.5", "2:9.1.0016-1ubuntu7.6"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parsePackages(tc.in, tc.kind)
			if len(got) != len(tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("package %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}
