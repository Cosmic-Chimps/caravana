package httpx

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAttachmentHeader(t *testing.T) {
	for name, want := range map[string]string{
		"export.csv":      `attachment; filename=export.csv`,
		"my file.txt":     `attachment; filename="my file.txt"`,
		`a"b.txt`:         `attachment; filename="a\"b.txt"`,
		"a/b\\c.txt":      `attachment; filename=abc.txt`,
		"line\nbreak.txt": `attachment; filename=linebreak.txt`,
		"":                `attachment; filename=download`,
		"..":              `attachment; filename=download`,
	} {
		if got := attachmentHeader(name); got != want {
			t.Errorf("attachmentHeader(%q) = %q, want %q", name, got, want)
		}
	}
	// A non-ASCII name is encoded, not sent raw.
	got := attachmentHeader("café.txt")
	if !strings.Contains(got, "filename*=utf-8''caf%C3%A9.txt") {
		t.Errorf("attachmentHeader(café.txt) = %q", got)
	}
}

func TestDownload(t *testing.T) {
	rec := httptest.NewRecorder()
	err := Download(rec, "a.bin", 5, func(w io.Writer) error { _, err := w.Write([]byte("hello")); return err })
	if err != nil {
		t.Fatal(err)
	}
	h := rec.Header()
	for k, want := range map[string]string{
		"Content-Type": "application/octet-stream", "Content-Length": "5", "X-Content-Type-Options": "nosniff",
		"Cache-Control": "no-store", "Content-Disposition": "attachment; filename=a.bin",
	} {
		if h.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, h.Get(k), want)
		}
	}
	if rec.Body.String() != "hello" || rec.Code != http.StatusOK {
		t.Errorf("answer = %d %q", rec.Code, rec.Body.String())
	}

	// An empty file still gets its headers.
	rec = httptest.NewRecorder()
	if err := Download(rec, "e", 0, func(io.Writer) error { return nil }); err != nil || rec.Header().Get("Content-Length") != "0" {
		t.Errorf("empty = %v, %v", err, rec.Header())
	}

	// A failure before the first byte is the caller's to answer, with no headers set.
	rec = httptest.NewRecorder()
	boom := errors.New("boom")
	if err := Download(rec, "x", 3, func(io.Writer) error { return boom }); !errors.Is(err, boom) || rec.Header().Get("Content-Disposition") != "" {
		t.Errorf("early failure = %v, headers %v", err, rec.Header())
	}
	// After the first byte there is nothing more to say.
	rec = httptest.NewRecorder()
	if err := Download(rec, "x", 9, func(w io.Writer) error { _, _ = w.Write([]byte("abc")); return boom }); err != nil {
		t.Errorf("late failure = %v, want nil", err)
	}
}
