package main

// What the lab gives the in-process Caravana for the Updates page (feature 027 page 3): a stand-in
// for GitHub's releases API, a stand-in for the host's pro command, and a helper that writes a
// Ubuntu host's files (a pending kernel, waiting updates) into the host root and takes them away.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// labCurrentVersion is the version the lab's Caravana says it runs. A development build makes no
// release request, so the lab gives it a release's number; the release stand-in starts at the same
// one, so that nothing is newer until a step says so.
const labCurrentVersion = "0.15.0"

// labReleaseGap is how often the lab's Caravana asks the release stand-in (and how soon after it
// starts): short, so a release a step announces is seen within a minute, and a check turned off
// is shown to make no request over a few gaps. Check now is therefore always refused in the lab
// (the last check is under 10 minutes old), which is the 429 the steps look for.
const labReleaseGap = 30 * time.Second

// labRelease is a stand-in for GitHub's releases API. It answers the latest release with the tag
// Set gave it and an ETag (304 for the same one), counts the requests, and answers 404 to anything
// else (the installer and the Traefik job that are read for a release's pins).
type labRelease struct {
	*httptest.Server
	mu       sync.Mutex
	tag      string
	requests int
}

func newLabRelease(t *testing.T) *labRelease {
	t.Helper()
	r := &labRelease{tag: "v" + labCurrentVersion}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !strings.HasSuffix(req.URL.Path, "/releases/latest") {
			http.NotFound(w, req)
			return
		}
		r.mu.Lock()
		r.requests++
		tag := r.tag
		r.mu.Unlock()
		etag := `"` + tag + `"`
		if req.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		fmt.Fprintf(w, `{"tag_name":%q,"html_url":"https://github.com/Cosmic-Chimps/caravana/releases/tag/%s",`+
			`"published_at":"2026-10-28T10:00:00Z","body":"Highlights\n\n- Operator view.\n- Updates page.\n","draft":false,"prerelease":false}`, tag, tag)
	}))
	t.Cleanup(r.Close)
	return r
}

// Set makes tag ("v0.16.0") the latest release.
func (r *labRelease) Set(tag string) {
	r.mu.Lock()
	r.tag = tag
	r.mu.Unlock()
}

// Requests is how many times Caravana has asked for the latest release.
func (r *labRelease) Requests() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requests
}

// labPro is the host's pro command for the lab, where Caravana runs on the Mac and there is none:
// after a second it gives a recorded answer (internal/hostos's fixture), naming the security
// packages.
type labPro struct{}

func (labPro) Available() bool { return true }

func (labPro) Run(ctx context.Context) ([]byte, error) {
	select {
	case <-time.After(time.Second):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return os.ReadFile("../../internal/hostos/testdata/pro-security-status.json")
}

// labInstalledKernel is the kernel the fixture says is installed: newer than any the lab's VM runs.
const labInstalledKernel = "9.9.0-999-generic"

// hostFixture writes the files of an Ubuntu host into the lab's host root, and takes them away
// again, so that the steps after it see a host that can't be read.
type hostFixture struct {
	t     *testing.T
	root  string
	files map[string]bool
}

func newHostFixture(t *testing.T, root string) *hostFixture {
	t.Helper()
	f := &hostFixture{t: t, root: root, files: map[string]bool{}}
	t.Cleanup(f.removeAll)
	return f
}

// write puts a file in the root, with a modification time age ago.
func (f *hostFixture) write(name, body string, age time.Duration) {
	f.t.Helper()
	path := filepath.Join(f.root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
	at := time.Now().Add(-age)
	if err := os.Chtimes(path, at, at); err != nil {
		f.t.Fatal(err)
	}
	f.files[name] = true
}

func (f *hostFixture) remove(names ...string) {
	for _, name := range names {
		_ = os.Remove(filepath.Join(f.root, name))
		delete(f.files, name)
	}
}

func (f *hostFixture) removeAll() {
	for name := range f.files {
		_ = os.Remove(filepath.Join(f.root, name))
	}
	f.files = map[string]bool{}
}

// ubuntu is a Ubuntu 24.04 host with 14 updates waiting, 9 of them security updates (Ubuntu's list
// is 3 hours old), Ubuntu's automatic updates off (so the security updates are a needs-you item at
// once) and automatic reboot off, and a kernel installed that the running one is behind.
func (f *hostFixture) ubuntu() {
	f.write("etc/os-release", "NAME=\"Ubuntu\"\nVERSION_ID=\"24.04\"\nID=ubuntu\nID_LIKE=debian\n", 0)
	f.write("var/lib/update-notifier/updates-available",
		"\n14 updates can be applied immediately.\n9 of these updates are standard security updates.\nTo see these additional updates run: apt list --upgradable\n\n", 3*time.Hour)
	f.write("etc/apt/apt.conf.d/20auto-upgrades", "APT::Periodic::Update-Package-Lists \"1\";\nAPT::Periodic::Unattended-Upgrade \"0\";\n", 0)
	f.write("etc/apt/apt.conf.d/50unattended-upgrades", "Unattended-Upgrade::Automatic-Reboot \"false\";\n", 0)
	f.write("boot/vmlinuz-"+labInstalledKernel, "", 0)
}

// rebootNeeded is the marker Ubuntu writes after installing a kernel, naming its package, 30 hours old.
func (f *hostFixture) rebootNeeded() {
	f.write("run/reboot-required", "*** System restart required ***\n", 30*time.Hour)
	f.write("run/reboot-required.pkgs", "linux-image-"+labInstalledKernel+"\n", 30*time.Hour)
}

func (f *hostFixture) rebootCleared() { f.remove("run/reboot-required", "run/reboot-required.pkgs") }
