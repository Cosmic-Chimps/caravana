package hostos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"time"
)

// This is the one file of the package that touches the real host. Root only
// ever hands out a read-only view (an fs.FS has no way to write), and RunPro is
// the one program the package runs.

// Root is the host's own file system: /var/log/apt/history.log is
// "var/log/apt/history.log" in it.
func Root() fs.FS { return os.DirFS("/") }

// Dir is the file system under dir, for development and tests that read a
// folder of fixtures in place of the host's files. An empty dir is the host's
// own (Root).
func Dir(dir string) fs.FS {
	if dir == "" {
		return Root()
	}
	return os.DirFS(dir)
}

// ProTimeout is how long `pro security-status` may run: 4 s on an idle host and
// up to 69 s on a starved one (feature 027, research R3).
const ProTimeout = 120 * time.Second

// maxProOutput is the most of pro's answer that is read. A real answer is a few
// hundred kilobytes at the most.
const maxProOutput = 8 << 20

// RunPro runs `pro security-status --format json` and returns what it wrote to
// standard output. home is a directory Caravana's user can write: pro keeps its
// cache under $HOME, and the service's own home is not writable. It needs no
// privilege. A host without pro gives ErrProUnavailable. Pro's output and its
// standard error are data: they are never put in an error, so a caller may log
// the error.
func RunPro(ctx context.Context, home string) ([]byte, error) {
	path, err := exec.LookPath("pro")
	if err != nil {
		return nil, ErrProUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, ProTimeout)
	defer cancel()

	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, path, "security-status", "--format", "json")
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C.UTF-8"}
	cmd.Stdout = &limitedWriter{w: &out, left: maxProOutput}
	// Standard error is dropped: it is the program's own words, not ours.
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return nil, fmt.Errorf("pro security-status: %w", context.DeadlineExceeded)
		case ctx.Err() != nil:
			return nil, fmt.Errorf("pro security-status: %w", ctx.Err())
		case errors.As(err, &exit):
			return nil, fmt.Errorf("pro security-status exited with code %d", exit.ExitCode())
		}
		return nil, errors.New("pro security-status could not run")
	}
	return out.Bytes(), nil
}

// limitedWriter writes at most left bytes and then fails, so a program that
// never stops writing is cut off.
type limitedWriter struct {
	w    io.Writer
	left int
}

var errTooMuchOutput = errors.New("too much output")

func (l *limitedWriter) Write(p []byte) (int, error) {
	if len(p) > l.left {
		return 0, errTooMuchOutput
	}
	l.left -= len(p)
	return l.w.Write(p)
}
