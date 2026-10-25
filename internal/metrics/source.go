package metrics

import (
	"context"
	"errors"
	"io"
)

// A Source is where a scrape comes from. HTTPSource is the real one;
// metricstest has a fake.
type Source interface {
	// Scrape returns the body of one scrape. The caller closes it.
	Scrape(ctx context.Context) (io.ReadCloser, error)
}

var (
	// ErrOff means the source answers but has no metrics: Traefik's
	// exporter is off (an old host that needs the installer run again).
	ErrOff = errors.New("metrics: the source has no metrics")
	// ErrUnreachable means nothing answered: a refused connection, a timeout.
	ErrUnreachable = errors.New("metrics: the source can't be reached")
)

// Read scrapes src and keeps what Caravana reads of it.
func Read(ctx context.Context, src Source) (Snapshot, error) {
	body, err := src.Scrape(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	defer body.Close()
	return ReadSnapshot(body)
}
