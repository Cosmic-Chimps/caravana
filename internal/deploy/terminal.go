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
// every resize after it. A nil channel means the size never changes.
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
	nomadSizes := make(chan api.TerminalSize)
	over := make(chan struct{})
	defer close(over)
	go func() {
		defer close(nomadSizes)
		for {
			select {
			case <-over:
				return
			case <-ctx.Done():
				return
			case s, ok := <-sizes:
				if !ok {
					return
				}
				select {
				case nomadSizes <- api.TerminalSize{Width: s.Cols, Height: s.Rows}:
				case <-over:
					return
				case <-ctx.Done():
					return
				}
			}
		}
	}()

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
