package metrics

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	// MaxBody is the most a scrape may hold. Traefik's is about half a
	// megabyte for 20 services.
	MaxBody = 8 << 20
	// ScrapeTimeout is the time one call may take, body included.
	ScrapeTimeout = 5 * time.Second
)

// ErrTooLarge is returned by reading a scrape's body past MaxBody.
var ErrTooLarge = errors.New("metrics: the scrape is larger than 8 MiB")

// HTTPSource reads Traefik's Prometheus endpoint. This is the only file of the
// package that uses the network.
type HTTPSource struct {
	URL string
	// Client is optional; the default has a 5 s timeout. Go's client asks for
	// gzip and inflates the body itself.
	Client *http.Client
}

var defaultClient = &http.Client{Timeout: ScrapeTimeout}

// Scrape asks for the text format only (never OpenMetrics or protobuf). A 404
// is ErrOff; a connection that can't be made or times out is ErrUnreachable;
// any other status is an error naming it. The URL is not put in an error.
func (s HTTPSource) Scrape(ctx context.Context) (io.ReadCloser, error) {
	client := s.Client
	if client == nil {
		client = defaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return nil, errors.New("metrics: the scrape address is not a URL")
	}
	req.Header.Set("Accept", "text/plain;version=0.0.4")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w", ErrUnreachable)
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		resp.Body.Close()
		return nil, ErrOff
	case resp.StatusCode != http.StatusOK:
		resp.Body.Close()
		return nil, fmt.Errorf("metrics: the source answered %d", resp.StatusCode)
	}
	return &limitedBody{rc: resp.Body, left: MaxBody}, nil
}

// limitedBody fails, rather than quietly stops, when the body is over the
// limit: half a scrape would be a wrong count.
type limitedBody struct {
	rc   io.ReadCloser
	left int64
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.left < 0 {
		return 0, ErrTooLarge
	}
	// Read one byte more than is left, to tell "exactly the limit" from "over".
	if int64(len(p)) > b.left+1 {
		p = p[:b.left+1]
	}
	n, err := b.rc.Read(p)
	b.left -= int64(n)
	if b.left < 0 {
		return n - int(-b.left), ErrTooLarge
	}
	return n, err
}

func (b *limitedBody) Close() error { return b.rc.Close() }
