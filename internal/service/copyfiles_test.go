package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/store"
)

const copyFilesSentinel = "s3cret-sentinel-2g4z"

// copyFilesLab is the action lab with a tree of files in copy c1: the service task
// web, whose secrets and private folders hold the sentinel.
func copyFilesLab(t *testing.T) (*actionLab, *syncBuffer) {
	t.Helper()
	l, logs := newLoggedActionLab(t)
	l.nomad.WriteAllocFile("c1", "web/local/caravana-files/0", "listen 8080;\n")
	l.nomad.WriteAllocFile("c1", "web/local/dump.bin", "PK\x00\x01binary")
	l.nomad.WriteAllocFile("c1", "web/local/latin1.txt", "caf\xe9")
	l.nomad.WriteAllocFile("c1", "alloc/data/export.csv", "a,b\n1,2\n")
	l.nomad.WriteAllocFile("c1", "web/secrets/token", copyFilesSentinel)
	l.nomad.WriteAllocFile("c1", "web/private/key", copyFilesSentinel)
	return l, logs
}

func pathFieldOf(err error) string {
	var fe *app.FieldError
	if errors.As(err, &fe) {
		return fe.Field
	}
	return ""
}

func TestListCopyFiles(t *testing.T) {
	ctx := context.Background()
	l, _ := copyFilesLab(t)

	root, err := l.svc.ListCopyFiles(ctx, "whoami", "c1", "")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range root.Entries {
		got = append(got, e.Name)
	}
	if root.Service != "web" || root.Path != "" || strings.Join(got, ",") != "alloc,names,web" {
		t.Errorf("root = %+v (%v)", root, got)
	}
	task, err := l.svc.ListCopyFiles(ctx, "whoami", "c1", "/web/")
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	for _, e := range task.Entries {
		got = append(got, e.Name)
	}
	if task.Path != "web" || strings.Join(got, ",") != "local,tmp" {
		t.Errorf("task folder = %+v (%v): secrets and private must not be listed", task, got)
	}
	if f, err := l.svc.ListCopyFiles(ctx, "whoami", "c1", "web/local"); err != nil || len(f.Entries) != 3 || !f.Entries[0].Dir {
		t.Errorf("web/local = %+v, %v", f, err)
	}
	// Listing is a read: no audit entry.
	if n := len(noted(t, l.store, func() { _, _ = l.svc.ListCopyFiles(ctx, "whoami", "c1", "alloc") })); n != 0 {
		t.Errorf("a listing left %d audit entries", n)
	}
}

func TestCopyFilesRefusals(t *testing.T) {
	ctx := context.Background()
	l, _ := copyFilesLab(t)

	tests := []struct {
		name         string
		app, copy    string
		path         string
		call         string // list, view or open
		field        string // a field error on this field
		is           error  // or this sentinel
		wantNotFound bool
	}{
		{name: "dot dot", app: "whoami", copy: "c1", path: "web/../x", call: "list", field: "path"},
		{name: "a NUL byte", app: "whoami", copy: "c1", path: "web\x00", call: "view", field: "path"},
		{name: "secrets", app: "whoami", copy: "c1", path: "web/secrets", call: "list", field: "path"},
		{name: "a secret file, viewed", app: "whoami", copy: "c1", path: "web/secrets/token", call: "view", field: "path"},
		{name: "a secret file, opened", app: "whoami", copy: "c1", path: "web/private/key", call: "open", field: "path"},
		{name: "dot dot into secrets", app: "whoami", copy: "c1", path: "web/local/../secrets/token", call: "open", field: "path"},
		{name: "a file listed", app: "whoami", copy: "c1", path: "web/local/caravana-files/0", call: "list", field: "path"},
		{name: "a folder viewed", app: "whoami", copy: "c1", path: "web/local", call: "view", field: "path"},
		{name: "a folder downloaded", app: "whoami", copy: "c1", path: "web/local", call: "open", field: "path"},
		{name: "a missing file", app: "whoami", copy: "c1", path: "web/local/nope", call: "view", is: ErrFileNotFound},
		{name: "a missing folder", app: "whoami", copy: "c1", path: "nope", call: "list", is: ErrFileNotFound},
		{name: "an unknown app", app: "ghost", copy: "c1", path: "", call: "list", is: store.ErrNotFound},
		{name: "a copy that isn't the app's", app: "whoami", copy: "zz", path: "", call: "list", is: store.ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			switch tt.call {
			case "list":
				_, err = l.svc.ListCopyFiles(ctx, tt.app, tt.copy, tt.path)
			case "view":
				_, err = l.svc.CopyFileText(ctx, tt.app, tt.copy, tt.path)
			case "open":
				_, err = l.svc.OpenCopyFile(ctx, tt.app, tt.copy, tt.path)
			}
			if tt.field != "" && pathFieldOf(err) != tt.field {
				t.Errorf("error = %v, want a field error on %q", err, tt.field)
			}
			if tt.is != nil && !errors.Is(err, tt.is) {
				t.Errorf("error = %v, want %v", err, tt.is)
			}
			if err != nil && strings.Contains(err.Error(), copyFilesSentinel) {
				t.Errorf("the error holds the secret: %v", err)
			}
		})
	}

	// A refusal leaves no audit entry: nothing was read.
	if n := len(noted(t, l.store, func() {
		_, _ = l.svc.CopyFileText(ctx, "whoami", "c1", "web/secrets/token")
		_, _ = l.svc.OpenCopyFile(ctx, "whoami", "c1", "web/local")
	})); n != 0 {
		t.Errorf("refusals left %d audit entries", n)
	}

	// A copy that has ended, and a stopped app.
	l.nomad.SetCopies("whoami",
		api.AllocationListStub{ID: "c1", TaskGroup: "web", ClientStatus: "complete", JobVersion: 0},
		api.AllocationListStub{ID: "c2", TaskGroup: "web", ClientStatus: "running", JobVersion: 0})
	if _, err := l.svc.ListCopyFiles(ctx, "whoami", "c1", ""); !errors.Is(err, ErrCopyNotRunning) {
		t.Errorf("an ended copy = %v", err)
	}
	if _, err := l.svc.ListCopyFiles(ctx, "whoami", "c2", ""); err != nil {
		t.Errorf("the running copy = %v", err)
	}
	if _, err := l.svc.StopApp(ctx, "whoami", app.TriggerManual); err != nil {
		t.Fatal(err)
	}
	if _, err := l.svc.ListCopyFiles(ctx, "whoami", "c2", ""); !errors.Is(err, ErrAppStopped) {
		t.Errorf("a stopped app = %v", err)
	}
}

func TestCopyFileText(t *testing.T) {
	ctx := context.Background()
	l, _ := copyFilesLab(t)

	var v CopyFileView
	e := notedOne(t, l.store, func() {
		var err error
		if v, err = l.svc.CopyFileText(ctx, "whoami", "c1", "/web/local/caravana-files/0"); err != nil {
			t.Fatal(err)
		}
	})
	if !v.Shown || v.Text != "listen 8080;\n" || v.Info.Path != "web/local/caravana-files/0" || v.Service != "web" {
		t.Errorf("view = %+v", v)
	}
	wantEntry(t, e, audit.ActionReadFile, audit.KindApp, "whoami")
	for path, want := range map[string]string{"copy": "c1", "path": "web/local/caravana-files/0", "as": "view"} {
		c := change(e.Changes, path)
		if c == nil || c.To != want {
			t.Errorf("change %q = %+v, want %q", path, c, want)
		}
	}
	if strings.Contains(fmt.Sprintf("%+v", e), "listen 8080") {
		t.Error("the audit entry holds the file's content")
	}

	for path, reason := range map[string]string{"web/local/dump.bin": ViewNotText, "web/local/latin1.txt": ViewNotText} {
		v, err := l.svc.CopyFileText(ctx, "whoami", "c1", path)
		if err != nil || v.Shown || v.Text != "" || v.Reason != reason {
			t.Errorf("%s = %+v, %v; want not shown, %s", path, v, err, reason)
		}
	}

	// The limit: a file at it is shown, one byte over isn't read at all.
	l.nomad.WriteAllocFile("c1", "alloc/data/at-limit.txt", strings.Repeat("a", MaxViewBytes))
	l.nomad.WriteAllocFile("c1", "alloc/data/over.txt", strings.Repeat("a", MaxViewBytes+1))
	if v, err := l.svc.CopyFileText(ctx, "whoami", "c1", "alloc/data/at-limit.txt"); err != nil || !v.Shown || len(v.Text) != MaxViewBytes {
		t.Errorf("at the limit: shown %v, %d bytes, %v", v.Shown, len(v.Text), err)
	}
	reads := len(l.nomad.FileRequests())
	var over CopyFileView
	if n := len(noted(t, l.store, func() {
		var err error
		if over, err = l.svc.CopyFileText(ctx, "whoami", "c1", "alloc/data/over.txt"); err != nil {
			t.Fatal(err)
		}
	})); n != 0 {
		t.Errorf("a file that wasn't read left %d audit entries", n)
	}
	if over.Shown || over.Reason != ViewTooLarge || over.Info.Size != MaxViewBytes+1 {
		t.Errorf("over the limit = %+v", over)
	}
	for _, r := range l.nomad.FileRequests()[reads:] {
		if r.Op == "readat" {
			t.Errorf("a file over the limit was read: %+v", r)
		}
	}
}

func TestOpenCopyFile(t *testing.T) {
	ctx := context.Background()
	l, _ := copyFilesLab(t)

	var f *CopyFile
	e := notedOne(t, l.store, func() {
		var err error
		if f, err = l.svc.OpenCopyFile(ctx, "whoami", "c1", "alloc/data/export.csv"); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, e, audit.ActionReadFile, audit.KindApp, "whoami")
	if c := change(e.Changes, "as"); c == nil || c.To != "download" {
		t.Errorf("as = %+v", c)
	}
	if f.Info.Name != "export.csv" || f.Info.Size != 8 || f.Service != "web" {
		t.Errorf("file = %+v", f.Info)
	}
	var buf bytes.Buffer
	if n, err := f.WriteTo(&buf); err != nil || n != 8 || buf.String() != "a,b\n1,2\n" {
		t.Errorf("WriteTo = %d, %v, %q", n, err, buf.String())
	}

	// The file vanished between the check and the read: an error, nothing written.
	l.nomad.RemoveAllocFile("c1", "alloc/data/export.csv")
	buf.Reset()
	f2 := &CopyFile{Info: f.Info, svc: f.svc, copyID: f.copyID, ctx: ctx}
	if _, err := f2.WriteTo(&buf); !errors.Is(err, ErrFileNotFound) || buf.Len() != 0 {
		t.Errorf("a vanished file = %v, wrote %q", err, buf.String())
	}
}

// TestCopyFilesNeverShowSecrets: a task's secret file never comes back through
// any call, answer, error, log line, notice, audit entry or row of the store.
func TestCopyFilesNeverShowSecrets(t *testing.T) {
	ctx := context.Background()
	l, logs := copyFilesLab(t)
	changes, stop := l.svc.Subscribe("")
	defer stop()

	var seen strings.Builder
	record := func(what string, v any, err error) {
		fmt.Fprintf(&seen, "%s: %+v %v\n", what, v, err)
	}
	for _, p := range []string{
		"web/secrets", "web/secrets/token", "web/private", "web/private/key", "/web/secrets/", "web/local/../secrets/token",
		"web/SECRETS/token", "names/secrets", `web\secrets\token`, "web/./secrets",
	} {
		a, err := l.svc.ListCopyFiles(ctx, "whoami", "c1", p)
		record("list "+p, a, err)
		b, err := l.svc.CopyFileText(ctx, "whoami", "c1", p)
		record("view "+p, b, err)
		c, err := l.svc.OpenCopyFile(ctx, "whoami", "c1", p)
		record("open "+p, c, err)
		d, err := l.svc.StatCopyPath(ctx, "whoami", "c1", p)
		record("stat "+p, d, err)
		if c != nil {
			var buf bytes.Buffer
			_, err := c.WriteTo(&buf)
			record("write "+p, buf.String(), err)
		}
	}
	// What is allowed to be read still is, and doesn't mix the secrets in.
	v, err := l.svc.CopyFileText(ctx, "whoami", "c1", "alloc/data/export.csv")
	record("view export", v, err)
	for _, r := range l.nomad.FileRequests() {
		if strings.Contains(r.Path, "secrets") || strings.Contains(strings.ToLower(r.Path), "private") {
			t.Errorf("Nomad was asked for %s %q", r.Op, r.Path)
		}
	}
	for what, text := range map[string]string{
		"the answers":                         seen.String(),
		"the audit entries":                   everyEntry(t, l.store),
		"the SQLite file and every row in it": rawStore(t, l.dir),
		"the log":                             logs.String(),
		"the live notices":                    drainNotices(changes),
	} {
		if strings.Contains(text, copyFilesSentinel) {
			t.Errorf("%s holds the secret file's content", what)
		}
	}
}
