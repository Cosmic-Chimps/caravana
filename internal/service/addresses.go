package service

import (
	"context"
	"fmt"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// AppAddresses says where each service of rec answers by name (task 2xg2).
// The other apps that claim names are the stored apps of rec's project and
// environment that are defined and not stopped, which is what Caravana
// registers: an undefined app has no services, and a stopped app is
// deregistered. An app that isn't defined has no addresses itself.
//
// It reads the store only; the names a copy resolves are decided live by its
// sidecar (nomadjob), by the same rule (app.Addresses).
func (s *Service) AppAddresses(ctx context.Context, rec store.AppRecord) ([]app.Address, error) {
	if !rec.App.Defined() {
		return nil, nil
	}
	recs, err := s.store.ListAppsIn(ctx, rec.Place.Project, rec.Place.Environment)
	if err != nil {
		return nil, fmt.Errorf("list the apps of %s › %s: %w", rec.Place.Project, rec.Place.Environment, err)
	}
	var others []app.App
	for _, o := range recs {
		if o.App.ID == rec.App.ID || !o.App.Defined() || o.StoppedAt != nil {
			continue
		}
		others = append(others, o.App)
	}
	return app.Addresses(rec.App, others), nil
}
