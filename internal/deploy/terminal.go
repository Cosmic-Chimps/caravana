package deploy

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/hashicorp/nomad/api"
)

// Terminal (feature 020.6): an interactive shell in a copy, over the same Nomad
// exec call Run a command uses, with a terminal. What the user types goes in
// through stdin and what the program prints comes out through out; both pass
// through this package and are never logged, put in an error or kept, as for
// Exec.

// TerminalSize is the size of a terminal in characters.
type TerminalSize struct {
	Cols, Rows int
}

// OpenTerminal runs command in the service's task of the copy with a terminal
// and returns the program's exit code when it ends. stdin is what the user
// types: the program sees end of input when it returns io.EOF, and Nomad reads
// it until then, so the caller closes it when the session is over. Everything
// the program prints (a terminal merges stderr into stdout) is written to out.
// sizes carries the terminal's size: send the first one before the call, and
// every resize after it; stdin isn't read until Nomad has the first one. A nil
// channel means the size never changes (Nomad's default, 80x24).
//
// It blocks until the program ends or ctx ends; in that case the error is the
// context's. A copy Nomad doesn't have is ErrCopyNotFound and one that isn't
// running is ErrCopyNotRunning. When Nomad can't start the program the error
// wraps ErrExecFailed.
func (d *Deployer) OpenTerminal(ctx context.Context, copyID, task string, command []string,
	stdin io.Reader, out io.Writer, sizes <-chan TerminalSize) (int, error) {
	q := (&api.QueryOptions{}).WithContext(ctx)
	alloc, _, err := d.nomad.Allocations().Info(copyID, q)
	if err != nil {
		if statusOf(err) == http.StatusNotFound {
			return 0, fmt.Errorf("%w: %s", ErrCopyNotFound, copyID)
		}
		return 0, d.callError("read copy "+copyID, err)
	}
	if alloc.ClientStatus != api.AllocClientStatusRunning {
		return 0, fmt.Errorf("%w: %s is %s", ErrCopyNotRunning, copyID, alloc.ClientStatus)
	}

	// Nomad's client wants its own size type; a goroutine converts. It ends
	// with the call, or earlier when the caller closes sizes.
	//
	// Nomad's client sends the sizes and what is typed from two goroutines, so
	// a line typed at once could reach the shell before the starting size and
	// run at 80x24. What is typed is therefore not read until Nomad's client
	// has sent the starting size: the first size is handed over twice, and the
	// second handover only completes once Nomad's sender is back for the next
	// size, after it wrote the first. The same size again changes nothing (a
	// terminal only signals a size that differs).
	nomadSizes := make(chan api.TerminalSize)
	sized := make(chan struct{}) // closed once the starting size is sent
	over := make(chan struct{})
	defer close(over)
	if sizes == nil {
		close(sized)
	}
	go func() {
		defer close(nomadSizes)
		hand := func(s TerminalSize) bool {
			select {
			case nomadSizes <- api.TerminalSize{Width: s.Cols, Height: s.Rows}:
				return true
			case <-over:
				return false
			case <-ctx.Done():
				return false
			}
		}
		first := sizes != nil
		for {
			select {
			case <-over:
				return
			case <-ctx.Done():
				return
			case s, ok := <-sizes:
				if !ok {
					if first {
						close(sized)
					}
					return
				}
				if !hand(s) {
					return
				}
				if first {
					if !hand(s) {
						return
					}
					first = false
					close(sized)
				}
			}
		}
	}()
	stdin = afterSize{r: stdin, sized: sized, over: over}

	code, err := d.nomad.Allocations().Exec(ctx, alloc, task, true, command, stdin, out, out,
		nomadSizes, (&api.QueryOptions{}).WithContext(ctx))
	switch {
	case err == nil:
		return code, nil
	case ctx.Err() != nil:
		return 0, ctx.Err()
	}
	return 0, d.execError(copyID, err)
}

// afterSize holds back every read of r until the starting size is sent
// (sized closed). Once the terminal is over it reads nothing more: Nomad's
// client may still be waiting in a read when the call has returned.
type afterSize struct {
	r           io.Reader
	sized, over <-chan struct{}
}

func (a afterSize) Read(p []byte) (int, error) {
	select {
	case <-a.sized:
		return a.r.Read(p)
	case <-a.over:
		return 0, io.EOF
	}
}
