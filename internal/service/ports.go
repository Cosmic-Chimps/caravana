package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// ErrPortTaken is a deploy that publishes a port another app already
// publishes (spec 041). The error is a *PortTakenError, which names the field,
// the port and the other app.
var ErrPortTaken = errors.New("another app publishes this port")

// PortTakenError is the refusal of a deploy that publishes a port another
// app has. Field is the port's field in the request
// ("services[0].ports[1].published"), App the other app's ID.
type PortTakenError struct {
	Field string
	Port  int
	App   string
}

func (e *PortTakenError) Error() string {
	return fmt.Sprintf("%s: port %d is published by app %s; unpublish it there first", e.Field, e.Port, e.App)
}

// Unwrap makes errors.Is(err, ErrPortTaken) true.
func (e *PortTakenError) Unwrap() error { return ErrPortTaken }

// checkPortsTaken refuses a deploy of a that publishes a port another stored
// app publishes. Every other stored app claims its ports, a stopped one
// included: starting it again must not fail. The same app's own ports are not
// checked, so a redeploy keeps them. Ports inside a itself are the model's
// check (app.ValidateApp).
func (s *Service) checkPortsTaken(ctx context.Context, a app.App) error {
	mine := false
	for _, svc := range a.Services {
		if svc.Publishes() {
			mine = true
			break
		}
	}
	if !mine {
		return nil
	}
	recs, err := s.store.ListApps(ctx)
	if err != nil {
		return err
	}
	claims := map[int]string{} // port → the first other app that publishes it
	for _, rec := range recs {
		if rec.App.ID == a.ID {
			continue
		}
		for _, svc := range rec.App.Services {
			for _, p := range svc.Ports {
				if _, ok := claims[p.Published]; ok || !p.IsPublished() {
					continue
				}
				claims[p.Published] = rec.App.ID
			}
		}
	}
	for i, svc := range a.Services {
		for j, p := range svc.Ports {
			if other, ok := claims[p.Published]; ok && p.IsPublished() {
				return &PortTakenError{Field: fmt.Sprintf("services[%d].ports[%d].published", i, j), Port: p.Published, App: other}
			}
		}
	}
	return nil
}
