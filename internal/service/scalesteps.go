package service

import (
	"context"
	"fmt"
	"time"

	"github.com/cosmic-chimps/caravana/internal/store"
)

// recentScaleSteps is how many scale steps Monitoring lists.
const recentScaleSteps = 10

// ScaleStep is one scale step of a service as Monitoring lists it (feature
// 043): the counts it went between and why, or why it failed. It holds numbers
// and Caravana's own words only.
type ScaleStep struct {
	AppID, AppName, Service string
	// From and To are the counts; nil when the step didn't record them.
	From, To *int
	// Reason is why the step was taken. Error is why it failed, "" when it
	// worked.
	Reason, Error string
	StartedAt     time.Time
}

// Failed reports whether the step didn't happen.
func (st ScaleStep) Failed() bool { return st.Error != "" }

// scaleSteps reads the newest limit scale steps of the given apps, newest first.
func (s *Service) scaleSteps(ctx context.Context, recs []store.AppRecord, limit int) ([]ScaleStep, error) {
	ids := make([]string, len(recs))
	names := map[string]string{}
	for i, r := range recs {
		ids[i] = r.App.ID
		names[r.App.ID] = r.Place.Name
	}
	rows, err := s.store.RecentScales(ctx, ids, limit)
	if err != nil {
		return nil, fmt.Errorf("read metrics: %w", err)
	}
	out := make([]ScaleStep, 0, len(rows))
	for _, a := range rows {
		st := ScaleStep{AppID: a.AppID, AppName: names[a.AppID], Service: a.Service, From: a.FromCount, To: a.ToCount,
			Reason: a.Reason, Error: a.Error, StartedAt: a.StartedAt}
		if a.Status == store.ActionFailed && st.Error == "" {
			st.Error = "Nomad refused it"
		}
		out = append(out, st)
	}
	return out, nil
}

// ServiceCounts returns the count each scaled service of the app has now, by
// service name; a scaled service with no stored count yet is left out. An
// unknown app is store.ErrNotFound.
func (s *Service) ServiceCounts(ctx context.Context, appID string) (map[string]int, error) {
	if _, err := s.store.GetApp(ctx, appID); err != nil {
		return nil, err
	}
	all, err := s.store.ServiceCounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("read counts of %q: %w", appID, err)
	}
	return all[appID], nil
}
