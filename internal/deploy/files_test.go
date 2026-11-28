package deploy

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
)

const filesSentinel = "s3cret-sentinel-2g4z"

func TestCleanFilePath(t *testing.T) {
	var pe *FilePathError
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr error // a sentinel, or nil
		badPath bool  // a *FilePathError
	}{
		{name: "the root", in: "", want: ""},
		{name: "slashes alone are the root", in: "///", want: ""},
		{name: "a leading slash", in: "/alloc/data", want: "alloc/data"},
		{name: "a trailing slash", in: "alloc/data/", want: "alloc/data"},
		{name: "a task's local", in: "web/local/caravana-files/0", want: "web/local/caravana-files/0"},
		{name: "a task folder", in: "web", want: "web"},
		{name: "alloc's own secrets name is fine", in: "alloc/secrets", want: "alloc/secrets"},
		{name: "a NUL byte", in: "alloc/da\x00ta", badPath: true},
		{name: "a backslash", in: `alloc\data`, badPath: true},
		{name: "an empty part", in: "alloc//data", badPath: true},
		{name: "a dot part", in: "alloc/./data", badPath: true},
		{name: "a dot dot part", in: "web/x/../local", badPath: true},
		{name: "dot dot into secrets", in: "web/x/../secrets/token", badPath: true},
		{name: "too long", in: strings.Repeat("a/", 2100), badPath: true},
		{name: "a task's secrets", in: "web/secrets", wantErr: ErrPathNotAllowed},
		{name: "a file in a task's secrets", in: "web/secrets/token", wantErr: ErrPathNotAllowed},
		{name: "secrets in other case", in: "web/Secrets/token", wantErr: ErrPathNotAllowed},
		{name: "a task's private", in: "/web/private/", wantErr: ErrPathNotAllowed},
		{name: "private in other case", in: "names/PRIVATE", wantErr: ErrPathNotAllowed},
		{name: "a task named secrets is fine", in: "secrets", want: "secrets"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CleanFilePath(tt.in)
			switch {
			case tt.badPath:
				if !errors.As(err, &pe) {
					t.Fatalf("CleanFilePath(%q) = %q, %v; want a *FilePathError", tt.in, got, err)
				}
				if pe.Reason == "" {
					t.Error("the reason is empty")
				}
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("CleanFilePath(%q) error = %v; want %v", tt.in, err, tt.wantErr)
				}
			default:
				if err != nil || got != tt.want {
					t.Fatalf("CleanFilePath(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
				}
			}
		})
	}
}

// filesCopy is one running copy c1 of service web with a tree of files, among
// them a secret the fake would serve if the deployer ever asked for it.
func filesCopy(t *testing.T) (*Deployer, *deploytest.Nomad) {
	t.Helper()
	d, nomad := logCopy(t)
	nomad.WriteAllocFile("c1", "web/local/caravana-files/0", "hello\n")
	nomad.WriteAllocFile("c1", "web/local/notes.txt", "a note")
	nomad.WriteAllocFile("c1", "web/tmp/scratch", "")
	nomad.WriteAllocFile("c1", "alloc/data/export/a.csv", "1,2\n")
	nomad.WriteAllocFile("c1", "web/secrets/token", filesSentinel)
	nomad.WriteAllocFile("c1", "web/private/key", filesSentinel)
	nomad.AddLogFile("c1", "stdout", 0, []string{"one"}, time.Now())
	return d, nomad
}

func names(files []FileInfo) []string {
	var out []string
	for _, f := range files {
		n := f.Name
		if f.Dir {
			n += "/"
		}
		out = append(out, n)
	}
	return out
}

func TestListFiles(t *testing.T) {
	d, _ := filesCopy(t)
	ctx := context.Background()
	tests := []struct {
		path string
		want string
	}{
		{"", "alloc/ names/ web/"},
		{"alloc", "data/ logs/ tmp/"},
		{"alloc/data", "export/"},
		{"alloc/data/export", "a.csv"},
		{"alloc/logs", ".web.stdout.fifo names.stdout.0 web.stdout.0"},
		{"web", "local/ tmp/"}, // secrets and private never listed
		{"web/local", "caravana-files/ notes.txt"},
		{"/web/local/caravana-files/", "0"},
		{"names", "local/ tmp/"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			files, err := d.ListFiles(ctx, "c1", tt.path)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(names(files), " "); got != tt.want {
				t.Errorf("ListFiles(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}

	// An entry knows its path, size and kind.
	files, err := d.ListFiles(ctx, "c1", "web/local")
	if err != nil {
		t.Fatal(err)
	}
	want := FileInfo{Name: "notes.txt", Path: "web/local/notes.txt", Size: int64(len("a note")), Mode: "-rw-r--r--"}
	got := files[1]
	got.ModTime = time.Time{}
	if got != want {
		t.Errorf("entry = %+v, want %+v", got, want)
	}
	if files[0].Path != "web/local/caravana-files" || !files[0].Dir {
		t.Errorf("folder entry = %+v", files[0])
	}
}

func TestListFilesErrors(t *testing.T) {
	d, nomad := filesCopy(t)
	ctx := context.Background()
	var pe *FilePathError

	for _, tt := range []struct {
		name, copy, path string
		is               error
	}{
		{"a file", "c1", "web/local/notes.txt", ErrNotAFolder},
		{"missing", "c1", "web/local/nope", ErrFileNotFound},
		{"unknown copy", "ghost", "web", ErrCopyNotFound},
		{"secrets", "c1", "web/secrets", ErrPathNotAllowed},
		{"private file", "c1", "web/private/key", ErrPathNotAllowed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := d.ListFiles(ctx, tt.copy, tt.path); !errors.Is(err, tt.is) {
				t.Errorf("ListFiles(%q, %q) error = %v, want %v", tt.copy, tt.path, err, tt.is)
			}
		})
	}
	if _, err := d.ListFiles(ctx, "c1", "web/../x"); !errors.As(err, &pe) {
		t.Errorf("a .. path = %v, want a *FilePathError", err)
	}

	// A token without read-fs names the capability.
	nomad.FailFS(403)
	_, err := d.ListFiles(ctx, "c1", "web")
	if !errors.Is(err, ErrNomadUnavailable) || !strings.Contains(err.Error(), "read-fs") {
		t.Errorf("403 error = %v, want one naming read-fs", err)
	}
	nomad.FailFS(0)
}

func TestStatFile(t *testing.T) {
	d, _ := filesCopy(t)
	ctx := context.Background()
	f, err := d.StatFile(ctx, "c1", "/web/local/notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	if f.Name != "notes.txt" || f.Path != "web/local/notes.txt" || f.Dir || f.Size != 6 {
		t.Errorf("StatFile = %+v", f)
	}
	dir, err := d.StatFile(ctx, "c1", "web/local")
	if err != nil || !dir.Dir || dir.Name != "local" {
		t.Errorf("StatFile of a folder = %+v, %v", dir, err)
	}
	if _, err := d.StatFile(ctx, "c1", "web/local/nope"); !errors.Is(err, ErrFileNotFound) {
		t.Errorf("missing = %v", err)
	}
	if _, err := d.StatFile(ctx, "c1", "web/secrets/token"); !errors.Is(err, ErrPathNotAllowed) {
		t.Errorf("secret = %v", err)
	}
}

func TestReadFile(t *testing.T) {
	d, nomad := filesCopy(t)
	ctx := context.Background()

	var buf bytes.Buffer
	if err := d.ReadFile(ctx, "c1", "web/local/caravana-files/0", 6, &buf); err != nil || buf.String() != "hello\n" {
		t.Fatalf("ReadFile = %q, %v", buf.String(), err)
	}

	// An empty file writes nothing.
	buf.Reset()
	if err := d.ReadFile(ctx, "c1", "web/tmp/scratch", 0, &buf); err != nil || buf.Len() != 0 {
		t.Errorf("empty file = %q, %v", buf.String(), err)
	}

	// A file over one chunk comes whole, in order.
	big := strings.Repeat("0123456789abcdef", (2<<20+4096)/16)
	nomad.WriteAllocFile("c1", "alloc/data/big.bin", big)
	buf.Reset()
	if err := d.ReadFile(ctx, "c1", "alloc/data/big.bin", int64(len(big)), &buf); err != nil || buf.String() != big {
		t.Fatalf("a big file: err %v, %d bytes of %d, equal %v", err, buf.Len(), len(big), buf.String() == big)
	}
	reads := 0
	for _, r := range nomad.FileRequests() {
		if r.Op == "readat" && strings.HasSuffix(r.Path, "big.bin") {
			reads++
			if r.Limit > fileChunk {
				t.Errorf("a readat asked for %d bytes", r.Limit)
			}
		}
	}
	if reads != 3 {
		t.Errorf("readat calls for the big file = %d, want 3", reads)
	}

	// A folder is not a file, a missing file is not found.
	buf.Reset()
	if err := d.ReadFile(ctx, "c1", "web/local", 0, &buf); !errors.Is(err, ErrNotAFile) {
		t.Errorf("a folder = %v", err)
	}
	if err := d.ReadFile(ctx, "c1", "web/local/nope", 3, &buf); !errors.Is(err, ErrFileNotFound) {
		t.Errorf("missing = %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("failed reads wrote %q", buf.String())
	}

	// The file shrank after the stat: the answer is not the length asked for.
	// The error says how much came, not what it was.
	short := strings.Repeat("x", 100)
	nomad.WriteAllocFile("c1", "alloc/data/short.txt", short)
	err := d.ReadFile(ctx, "c1", "alloc/data/short.txt", 200, &buf)
	if !errors.Is(err, ErrNomadUnavailable) || strings.Contains(err.Error(), "xxxx") {
		t.Errorf("a short answer = %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("a failed first chunk wrote %d bytes", buf.Len())
	}

	// A token without read-fs.
	nomad.FailFS(403)
	err = d.ReadFile(ctx, "c1", "web/local/notes.txt", 6, &buf)
	nomad.FailFS(0)
	if !errors.Is(err, ErrNomadUnavailable) || !strings.Contains(err.Error(), "read-fs") {
		t.Errorf("403 = %v", err)
	}
}

// TestFilesNeverReachSecrets: whatever path a caller writes, the deployer never
// asks Nomad for a task's secrets or private folder, and the fake's secret
// never comes back.
func TestFilesNeverReachSecrets(t *testing.T) {
	d, nomad := filesCopy(t)
	ctx := context.Background()
	var out bytes.Buffer
	for _, p := range []string{
		"web/secrets", "web/secrets/token", "web/private/key", "/web/private/", "web/x/../secrets/token",
		"web/./secrets/token", "web//secrets/token", "web/SECRETS/token", "names/secrets", `web\secrets\token`,
		"web/secrets/../secrets/token",
	} {
		if _, err := d.ListFiles(ctx, "c1", p); err == nil {
			t.Errorf("ListFiles(%q) worked", p)
		}
		if _, err := d.StatFile(ctx, "c1", p); err == nil {
			t.Errorf("StatFile(%q) worked", p)
		}
		if err := d.ReadFile(ctx, "c1", p, int64(len(filesSentinel)), &out); err == nil {
			t.Errorf("ReadFile(%q) worked", p)
		}
	}
	if strings.Contains(out.String(), filesSentinel) {
		t.Error("the secret came back")
	}
	for _, r := range nomad.FileRequests() {
		if strings.Contains(r.Path, "secrets") || strings.Contains(strings.ToLower(r.Path), "private") {
			t.Errorf("the deployer asked Nomad for %s %q", r.Op, r.Path)
		}
	}

	// And a listing of a task's folder doesn't show them, even though the fake
	// would have them if asked.
	files, err := d.ListFiles(ctx, "c1", "web")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Name == "secrets" || f.Name == "private" {
			t.Errorf("the listing shows %s", f.Name)
		}
	}
}

// TestFileMessages: what Nomad's client says as the body of a 200 is mapped,
// and what it doesn't know isn't quoted.
func TestFileMessages(t *testing.T) {
	d := &Deployer{}
	for msg, want := range map[string]error{
		"Reading secret file prohibited: /web/secrets": ErrPathNotAllowed,
		`file "/web/local" is a directory`:             ErrNotAFile,
		"stat /x: no such file or directory":           ErrFileNotFound,
		`Unknown allocation "c9"`:                      ErrCopyNotFound,
		"Permission denied":                            ErrNomadUnavailable,
	} {
		if err := d.fileMessageError("read", "c1", msg); !errors.Is(err, want) {
			t.Errorf("fileMessageError(%q) = %v, want %v", msg, err, want)
		}
	}
	err := d.fileMessageError("read", "c1", "the file's own words "+filesSentinel)
	if !errors.Is(err, ErrNomadUnavailable) || strings.Contains(err.Error(), filesSentinel) {
		t.Errorf("an unknown message = %v", err)
	}
}

// TestFilesNeverFollowLinks: Nomad follows a relative link and checks the
// secrets folder only against the path as written (lab, 2026-11-28), so a
// link an app made into its secrets folder would be read. The fake does the
// same, and no call of ours follows a link: not to a file, not through a
// folder, not an absolute one.
func TestFilesNeverFollowLinks(t *testing.T) {
	d, nomad := filesCopy(t)
	ctx := context.Background()
	nomad.WriteAllocLink("c1", "web/local/peek", "../secrets/token")
	nomad.WriteAllocLink("c1", "web/local/sdir", "../secrets")
	nomad.WriteAllocLink("c1", "web/local/abs", "/secrets/token")
	nomad.WriteAllocLink("c1", "alloc/data/up", "../../web/private")

	// The fake follows them, as Nomad does: the test would be blind otherwise.
	if text, ok := nomad.AllocFile("c1", "web/secrets/token"); !ok || text != filesSentinel {
		t.Fatal("the fake holds no secret to protect")
	}

	var out bytes.Buffer
	for _, p := range []string{"web/local/peek", "web/local/sdir", "web/local/sdir/token", "web/local/abs",
		"alloc/data/up", "alloc/data/up/key"} {
		if _, err := d.ListFiles(ctx, "c1", p); err == nil {
			t.Errorf("ListFiles(%q) worked", p)
		}
		_, err := d.StatFile(ctx, "c1", p)
		if !errors.Is(err, ErrPathIsLink) {
			t.Errorf("StatFile(%q) error = %v; want ErrPathIsLink", p, err)
		}
		if err := d.ReadFile(ctx, "c1", p, int64(len(filesSentinel)), &out); err == nil {
			t.Errorf("ReadFile(%q) worked", p)
		}
	}
	if strings.Contains(out.String(), filesSentinel) {
		t.Error("the secret came back through a link")
	}
	for _, r := range nomad.FileRequests() {
		if r.Op != "ls" && (strings.Contains(r.Path, "peek") || strings.Contains(r.Path, "sdir") ||
			strings.Contains(r.Path, "abs") || strings.Contains(r.Path, "up")) {
			t.Errorf("the deployer asked Nomad for %s %q, which follows the link", r.Op, r.Path)
		}
	}

	// The folder lists the links as links.
	files, err := d.ListFiles(ctx, "c1", "web/local")
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, f := range files {
		kinds[f.Name] = f.Kind()
	}
	for _, n := range []string{"peek", "sdir", "abs"} {
		if kinds[n] != KindLink {
			t.Errorf("%s is %q in the listing; want a link", n, kinds[n])
		}
	}
	if kinds["notes.txt"] != KindFile || kinds["caravana-files"] != KindFolder {
		t.Errorf("kinds = %v", kinds)
	}
}

// TestSpecialFilesAreNeverOpened: logmon's pipes in alloc/logs are listed but
// never read: a reader would take the app's output.
func TestSpecialFilesAreNeverOpened(t *testing.T) {
	d, nomad := filesCopy(t)
	ctx := context.Background()
	files, err := d.ListFiles(ctx, "c1", "alloc/logs")
	if err != nil {
		t.Fatal(err)
	}
	fifo := ""
	for _, f := range files {
		if strings.HasSuffix(f.Name, ".fifo") {
			fifo = f.Path
			if f.Kind() != KindOther {
				t.Errorf("%s is %q; want other", f.Name, f.Kind())
			}
		}
	}
	if fifo == "" {
		t.Fatal("the fake lists no pipe")
	}
	err = d.ReadFile(ctx, "c1", fifo, 10, &bytes.Buffer{})
	if !errors.Is(err, ErrSpecialFile) {
		t.Fatalf("ReadFile(%q) error = %v; want ErrSpecialFile", fifo, err)
	}
	for _, r := range nomad.FileRequests() {
		if r.Op == "readat" && r.Path == fifo {
			t.Error("the pipe was read")
		}
	}
}

func TestFileKind(t *testing.T) {
	for _, tt := range []struct {
		info FileInfo
		want string
	}{
		{FileInfo{Dir: true, Mode: "drwxrwxrwx"}, KindFolder},
		{FileInfo{Mode: "-rw-r--r--"}, KindFile},
		{FileInfo{Mode: "Lrwxrwxrwx"}, KindLink},
		{FileInfo{Mode: "prw-------"}, KindOther},
		{FileInfo{Mode: "Srwxrwxrwx"}, KindOther},
		{FileInfo{Mode: "Dcrw-rw-rw-"}, KindOther},
	} {
		if got := tt.info.Kind(); got != tt.want {
			t.Errorf("Kind of mode %q = %q; want %q", tt.info.Mode, got, tt.want)
		}
	}
}
