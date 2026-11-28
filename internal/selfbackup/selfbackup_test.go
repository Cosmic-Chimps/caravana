package selfbackup

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"go/build"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestKeep(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"caravana/acme", true},
		{"caravana/registries/ghcr~io", true},
		{"caravana/registries/ghcr~io/acme", true},
		{"caravana/backup-destinations/main", true},
		{"caravana/notifications/abc", true},
		{"caravana/secret-providers/bella", true},
		{"caravana/certificates/xyz", true},
		{"caravana/cluster/ca", false},
		{"caravana/cluster/gossip", false},
		{"caravana/cluster", false},
		{"caravana/", false},
		{"caravana", false},
		{"nomad/jobs/web/web", true},
		{"nomad/jobs/shop--staging--api/worker", true},
		{"nomad/jobs/web/caravana", false},            // the deployment's own
		{"nomad/jobs/web/caravana/previous", false},   // three segments
		{"nomad/jobs/web/web/web", false},             // a registry copy
		{"nomad/jobs/web/db/caravana-volumes", false}, // a volume request
		{"nomad/jobs/web_run-123", false},             // a helper job
		{"nomad/jobs/web_backup/x", false},            // helper job with a service
		{"nomad/jobs/traefik", false},
		{"nomad/jobs/traefik/traefik", false},
		{"nomad/jobs/", false},
		{"nomad/jobs//web", false},
		{"nomad/jobs/web/", false},
		{"nomad/other/web/web", false},
		{"other/x", false},
		{"", false},
	} {
		if got := Keep(tc.path); got != tc.want {
			t.Errorf("Keep(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// fixedReader makes a key from bytes that are the same every run.
func fixedReader(seed byte) io.Reader {
	b := make([]byte, 64)
	for i := range b {
		b[i] = seed + byte(i)
	}
	return bytes.NewReader(b)
}

func TestKeyRoundTripAndChecksum(t *testing.T) {
	k, err := NewKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s := k.String()
	if !strings.HasPrefix(s, "cvrk_") || s != strings.ToLower(s) || strings.ContainsAny(s, "= ") {
		t.Fatalf("key = %q", s)
	}
	back, err := ParseKey(s)
	if err != nil || back.String() != s || back.Fingerprint() != k.Fingerprint() {
		t.Fatalf("ParseKey = %v, %v", back, err)
	}
	// Case, spaces and line breaks don't matter.
	spaced := strings.ToUpper(s[:9]) + " " + s[9:20] + "\n" + s[20:]
	if again, err := ParseKey(spaced); err != nil || again.String() != s {
		t.Errorf("ParseKey(spaced) = %v, %v", again, err)
	}
	if len(k.Fingerprint()) != 16 || len(k.Public()) != 32 {
		t.Errorf("fingerprint %q, public key %d bytes", k.Fingerprint(), len(k.Public()))
	}

	// A mistyped key is refused as mistyped, not as another key: change one character.
	runes := []rune(s)
	i := len(runes) - 3
	if runes[i] == 'a' {
		runes[i] = 'b'
	} else {
		runes[i] = 'a'
	}
	if _, err := ParseKey(string(runes)); !errors.Is(err, ErrKeyChecksum) {
		t.Errorf("a typo: err = %v, want ErrKeyChecksum", err)
	}
	for _, bad := range []string{"", "cvrk_", "cvrk_abc", "abcdefghijklmnop", "cvrk_" + strings.Repeat("1", 58), s + "aa"} {
		if _, err := ParseKey(bad); !errors.Is(err, ErrKeyFormat) {
			t.Errorf("ParseKey(%q) = %v, want ErrKeyFormat", bad, err)
		}
	}
}

func TestNewKeyIsDeterministicFromItsReader(t *testing.T) {
	a, _ := NewKey(fixedReader(1))
	b, _ := NewKey(fixedReader(1))
	c, _ := NewKey(fixedReader(2))
	if a.String() != b.String() || a.String() == c.String() {
		t.Errorf("keys: %s %s %s", a, b, c)
	}
	if _, err := NewKey(strings.NewReader("short")); err == nil {
		t.Error("a short random source should fail")
	}
}

func TestStreamRoundTripSizes(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	for _, n := range []int{0, 1, 100, chunkSize - 1, chunkSize, chunkSize + 1, 2 * chunkSize, 2*chunkSize + 17, 5*chunkSize + 3} {
		plain := make([]byte, n)
		rand.Read(plain)
		var sealed bytes.Buffer
		w, err := newSealWriter(&sealed, key)
		if err != nil {
			t.Fatal(err)
		}
		// Write in odd pieces to exercise the buffering.
		for rest := plain; len(rest) > 0; {
			take := min(len(rest), 40001)
			if _, err := w.Write(rest[:take]); err != nil {
				t.Fatal(err)
			}
			rest = rest[take:]
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		chunks := max(1, (n+chunkSize-1)/chunkSize)
		if want := n + chunks*tagSize; sealed.Len() != want {
			t.Errorf("n=%d: sealed %d bytes, want %d", n, sealed.Len(), want)
		}
		r, err := newOpenReader(bytes.NewReader(sealed.Bytes()), key)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(r)
		if err != nil || !bytes.Equal(got, plain) {
			t.Errorf("n=%d: round trip: %d bytes, err %v", n, len(got), err)
		}
	}
}

func TestStreamRefusesTruncationReorderAndGarbage(t *testing.T) {
	key := bytes.Repeat([]byte{9}, 32)
	plain := make([]byte, 3*chunkSize+100)
	rand.Read(plain)
	var buf bytes.Buffer
	w, _ := newSealWriter(&buf, key)
	w.Write(plain)
	w.Close()
	sealed := buf.Bytes()
	full := chunkSize + tagSize

	open := func(b []byte, k []byte) error {
		r, err := newOpenReader(bytes.NewReader(b), k)
		if err != nil {
			return err
		}
		_, err = io.ReadAll(r)
		return err
	}
	if err := open(sealed, key); err != nil {
		t.Fatalf("the whole file: %v", err)
	}
	cases := map[string][]byte{
		"cut at a chunk boundary": sealed[:2*full],
		"cut inside a chunk":      sealed[:2*full+50],
		"cut to nothing":          {},
		"cut inside the last tag": sealed[:len(sealed)-1],
		"a chunk removed":         append(append([]byte{}, sealed[:full]...), sealed[2*full:]...),
		"garbage after the end":   append(append([]byte{}, sealed...), 1),
		"a flipped bit":           flip(sealed, full+5),
		"two chunks swapped":      swap(sealed, full),
		"a chunk repeated":        append(append([]byte{}, sealed[:full]...), sealed...),
	}
	for name, b := range cases {
		if err := open(b, key); !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: err = %v, want ErrCorrupt", name, err)
		}
	}
	if err := open(sealed, bytes.Repeat([]byte{1}, 32)); !errors.Is(err, ErrCorrupt) {
		t.Errorf("another key: err = %v", err)
	}
}

func flip(b []byte, at int) []byte {
	c := append([]byte{}, b...)
	c[at] ^= 1
	return c
}

func swap(b []byte, full int) []byte {
	c := append([]byte{}, b...)
	copy(c[:full], b[full:2*full])
	copy(c[full:2*full], b[:full])
	return c
}

func testMeta(k RecoveryKey) Meta {
	return Meta{CreatedAt: time.Date(2026, 11, 28, 3, 0, 0, 0, time.UTC), Version: "0.30.0", Schema: 49, KeyFingerprint: k.Fingerprint()}
}

func files(db []byte) ([]File, []byte, []byte) {
	vars, _ := json.Marshal([]Variable{{Path: "caravana/acme", Items: map[string]string{"email": "a@b.c"}}, {Path: "nomad/jobs/web/web", Items: map[string]string{"PW": "s3cret-sentinel-063"}}})
	man, _ := json.Marshal(Manifest{Apps: 3, People: 2, Variables: 2})
	return []File{
		{Name: DatabaseFile, Size: int64(len(db)), Body: bytes.NewReader(db)},
		{Name: VariablesFile, Size: int64(len(vars)), Body: bytes.NewReader(vars)},
		{Name: ManifestFile, Size: int64(len(man)), Body: bytes.NewReader(man)},
	}, vars, man
}

func TestBundleRoundTrip(t *testing.T) {
	key, _ := NewKey(rand.Reader)
	// A database of a few chunks that gzip can't shrink to nothing.
	db := make([]byte, 3*chunkSize+999)
	rand.Read(db)
	fs, _, _ := files(db)
	var out bytes.Buffer
	if err := Write(&out, rand.Reader, key.Public(), testMeta(key), fs); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(out.Bytes(), []byte("CARAVANA-BACKUP 1\n{")) {
		t.Fatalf("the file starts with %q", out.Bytes()[:30])
	}
	// Nothing the backup holds is readable in the file.
	if bytes.Contains(out.Bytes(), []byte("s3cret-sentinel-063")) || bytes.Contains(out.Bytes(), []byte("caravana/acme")) {
		t.Error("the file holds its contents in the clear")
	}
	// The header is readable from the first bytes alone, without the key.
	hdr, err := ReadHeader(bytes.NewReader(out.Bytes()[:HeaderMax]))
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Version != "0.30.0" || hdr.Schema != 49 || hdr.KeyFingerprint != key.Fingerprint() || !hdr.CreatedAt.Equal(testMeta(key).CreatedAt) {
		t.Errorf("header = %+v", hdr.Meta)
	}
	h, man, vars, got, err := ReadAll(bytes.NewReader(out.Bytes()), key, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if h.Version != "0.30.0" || man != (Manifest{Apps: 3, People: 2, Variables: 2}) || len(vars) != 2 || vars[1].Items["PW"] != "s3cret-sentinel-063" || !bytes.Equal(got, db) {
		t.Errorf("read back: %+v %+v %d vars, %d db bytes", h.Meta, man, len(vars), len(got))
	}
}

func TestBundleEmptyDatabaseAndNoFiles(t *testing.T) {
	key, _ := NewKey(rand.Reader)
	var out bytes.Buffer
	if err := Write(&out, rand.Reader, key.Public(), testMeta(key), nil); err != nil {
		t.Fatal(err)
	}
	called := 0
	if _, err := Read(bytes.NewReader(out.Bytes()), key, func(string, int64, io.Reader) error { called++; return nil }); err != nil || called != 0 {
		t.Errorf("no files: %d visited, %v", called, err)
	}
}

func TestBundleRefusals(t *testing.T) {
	key, _ := NewKey(rand.Reader)
	other, _ := NewKey(rand.Reader)
	db := bytes.Repeat([]byte("db"), 100)
	fs, _, _ := files(db)
	var out bytes.Buffer
	if err := Write(&out, rand.Reader, key.Public(), testMeta(key), fs); err != nil {
		t.Fatal(err)
	}
	sealed := out.Bytes()
	read := func(b []byte, k RecoveryKey) error {
		_, _, _, _, err := ReadAll(bytes.NewReader(b), k, 1<<20)
		return err
	}

	// The wrong key is named by both fingerprints, before anything is decrypted.
	var mismatch *KeyMismatchError
	err := read(sealed, other)
	if !errors.As(err, &mismatch) || mismatch.Backup != key.Fingerprint() || mismatch.Key != other.Fingerprint() {
		t.Errorf("wrong key: %v", err)
	} else if !strings.Contains(err.Error(), key.Fingerprint()) || !strings.Contains(err.Error(), other.Fingerprint()) {
		t.Errorf("the sentence should name both fingerprints: %v", err)
	}

	// A header whose fingerprint was changed to the other key's still can't be opened by it: the key is
	// derived from the header and from the ephemeral key, so it is corrupt, not opened.
	forged := bytes.Replace(sealed, []byte(key.Fingerprint()), []byte(other.Fingerprint()), 1)
	if err := read(forged, other); !errors.Is(err, ErrCorrupt) {
		t.Errorf("forged header: %v", err)
	}
	// Any change of the header fails to open, even one that keeps it a header.
	changed := bytes.Replace(sealed, []byte(`"version":"0.30.0"`), []byte(`"version":"0.31.0"`), 1)
	if err := read(changed, key); !errors.Is(err, ErrCorrupt) {
		t.Errorf("changed header: %v", err)
	}
	for name, b := range map[string][]byte{
		"cut short":      sealed[:len(sealed)-1],
		"cut at chunk":   sealed[:len(sealed)-20],
		"empty":          {},
		"not a backup":   []byte("hello\nworld\n"),
		"header only":    sealed[:bytes.IndexByte(sealed[bytes.IndexByte(sealed, '\n')+1:], '\n')+bytes.IndexByte(sealed, '\n')+2],
		"trailing byte":  append(append([]byte{}, sealed...), 0),
		"a flipped byte": flip(sealed, len(sealed)-30),
	} {
		if err := read(b, key); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if err := read([]byte("hello\nworld\n"), key); !errors.Is(err, ErrNotABackup) {
		t.Errorf("not a backup: %v", err)
	}

	// Writing for a header that names another key is refused.
	m := testMeta(key)
	m.KeyFingerprint = other.Fingerprint()
	if err := Write(io.Discard, rand.Reader, key.Public(), m, nil); err == nil {
		t.Error("a header naming another key should be refused")
	}
	if err := Write(io.Discard, rand.Reader, []byte("short"), testMeta(key), nil); err == nil {
		t.Error("a bad public key should be refused")
	}
	// A file whose size isn't what was said.
	bad := []File{{Name: "a", Size: 10, Body: strings.NewReader("abc")}}
	if err := Write(io.Discard, rand.Reader, key.Public(), testMeta(key), bad); err == nil {
		t.Error("a short file should be refused")
	}
}

func TestBundlesOfTheSameFilesDiffer(t *testing.T) {
	key, _ := NewKey(rand.Reader)
	fs1, _, _ := files([]byte("x"))
	fs2, _, _ := files([]byte("x"))
	var a, b bytes.Buffer
	Write(&a, rand.Reader, key.Public(), testMeta(key), fs1)
	Write(&b, rand.Reader, key.Public(), testMeta(key), fs2)
	if bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Error("two backups are the same bytes: the ephemeral key isn't fresh")
	}
}

func TestObjectKeys(t *testing.T) {
	if got := ObjectKey("", 1700000000); got != "_caravana/1700000000/caravana-1700000000.cvbackup" {
		t.Errorf("no prefix: %q", got)
	}
	if got := ObjectKey("/team/backups/", 1700000000); got != "team/backups/_caravana/1700000000/caravana-1700000000.cvbackup" {
		t.Errorf("prefix: %q", got)
	}
	if got := Base("a/b"); got != "a/b/_caravana/" {
		t.Errorf("Base = %q", got)
	}
	for _, tc := range []struct {
		prefix, key string
		ts          int64
		ok          bool
	}{
		{"p", "p/_caravana/1700000000/caravana-1700000000.cvbackup", 1700000000, true},
		{"", "_caravana/5/caravana-5.cvbackup", 5, true},
		{"p", "p/_caravana/1700000000/caravana-1700000001.cvbackup", 0, false}, // names differ
		{"p", "p/_caravana/1700000000/other.cvbackup", 0, false},
		{"p", "p/_caravana/1700000000/caravana-1700000000.cvbackup.tmp", 0, false},
		{"p", "p/_caravana/abc/caravana-abc.cvbackup", 0, false},
		{"p", "p/_caravana/-5/caravana--5.cvbackup", 0, false},
		{"p", "p/_caravana/007/caravana-007.cvbackup", 0, false},
		{"p", "p/_caravana/1700000000", 0, false},
		{"p", "q/_caravana/1700000000/caravana-1700000000.cvbackup", 0, false},
		{"p", "p/web/_caravana/1/caravana-1.cvbackup", 0, false},
		{"p", "p/_caravana/1/x/caravana-1.cvbackup", 0, false},
	} {
		ts, ok := ParseObjectKey(tc.prefix, tc.key)
		if ts != tc.ts || ok != tc.ok {
			t.Errorf("ParseObjectKey(%q, %q) = %d, %v", tc.prefix, tc.key, ts, ok)
		}
	}
}

// The package is pure: it reads and writes streams, and imports no network, store, service or Nomad.
func TestPackageIsPure(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	exact := []string{"net", "net/http", "database/sql"}
	parts := []string{"internal/service", "internal/store", "internal/deploy", "internal/s3", "hashicorp/nomad"}
	for _, imp := range append(append([]string{}, pkg.Imports...), pkg.TestImports...) {
		if slices.Contains(exact, imp) {
			t.Errorf("selfbackup imports %q", imp)
		}
		for _, p := range parts {
			if strings.Contains(imp, p) {
				t.Errorf("selfbackup imports %q", imp)
			}
		}
	}
}
