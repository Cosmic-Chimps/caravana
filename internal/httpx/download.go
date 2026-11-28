package httpx

import (
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode"
)

// Download answers with a file the caller streams through write: an attachment
// of size bytes named name, never cached, never sniffed. The headers go out with
// the first byte, so if write fails before writing anything Download returns
// that error and nothing has been sent: the caller can answer it as it answers
// any other. If it fails after the first byte the answer is already a download
// that is shorter than its Content-Length, which the client sees as a broken
// transfer; Download returns nil then, since nothing more can be said.
func Download(w http.ResponseWriter, name string, size int64, write func(io.Writer) error) error {
	d := &downloadWriter{w: w, name: name, size: size}
	if err := write(d); err != nil {
		if !d.started {
			return err
		}
		return nil
	}
	d.start() // an empty file writes no byte
	return nil
}

type downloadWriter struct {
	w       http.ResponseWriter
	name    string
	size    int64
	started bool
}

func (d *downloadWriter) start() {
	if d.started {
		return
	}
	d.started = true
	h := d.w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Length", strconv.FormatInt(d.size, 10))
	h.Set("Content-Disposition", attachmentHeader(d.name))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
}

func (d *downloadWriter) Write(p []byte) (int, error) {
	d.start()
	return d.w.Write(p)
}

// attachmentHeader is the Content-Disposition of an attachment called name:
// control characters and slashes are dropped, and what can't be written as a
// plain quoted name (a non-ASCII one) is encoded as RFC 2231 does.
func attachmentHeader(name string) string {
	clean := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '/' || r == '\\' {
			return -1
		}
		return r
	}, name)
	if strings.Trim(clean, ". ") == "" {
		clean = "download"
	}
	if v := mime.FormatMediaType("attachment", map[string]string{"filename": clean}); v != "" {
		return v
	}
	return `attachment; filename="download"`
}
