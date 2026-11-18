package service

import (
	"context"
	"fmt"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// scale_at_max (feature 043): a scaled service runs its maximum count and its
// value over the last 10 minutes is still above its target, so scaling can't
// help any more. Derived on every read from the stored counts and minute rows,
// so it goes by itself when the load drops or the maximum is raised. A warning;
// a stopped app is not judged.
//
// The key is scale_at_max|<app>|<service>|.

// scaleItems is the kind's items for the stored apps.
func (s *Service) scaleItems(ctx context.Context, apps []store.AppRecord, now time.Time) ([]AttentionItem, error) {
	want := false
	for _, rec := range apps {
		want = want || (hasScale(rec.App) && rec.StoppedAt == nil)
	}
	if !want {
		return nil, nil
	}
	counts, err := s.store.ServiceCounts(ctx)
	if err != nil {
		return nil, err
	}
	layouts, err := s.store.MetricsLayouts(ctx)
	if err != nil {
		return nil, err
	}
	var items []AttentionItem
	for _, rec := range apps {
		if rec.StoppedAt != nil {
			continue
		}
		for _, svc := range rec.App.Services {
			count, ok := counts[rec.App.ID][svc.Name]
			if svc.Scale == nil || !ok || count < svc.Scale.Max {
				continue
			}
			series, err := s.store.EnsureSeries(ctx, rec.App.ID, svc.Name)
			if err != nil {
				return nil, err
			}
			samples, err := s.store.MinuteRows(ctx, series, now.Add(-(metrics.WindowMinutes+1)*time.Minute), now.Add(time.Minute))
			if err != nil {
				return nil, err
			}
			rows := make([]metrics.Row, 0, len(samples))
			for _, m := range samples {
				rows = append(rows, samplerRow(m, layouts))
			}
			if _, over := metrics.OverTargetAtMax(metrics.ScaleInput{Rule: scaleRule(*svc.Scale), Count: count, Rows: rows, Now: now}); !over {
				continue
			}
			it := AttentionItem{Kind: AttentionScaleAtMax, AppID: rec.App.ID, Service: svc.Name, Count: count,
				Level: metrics.DiskWarning, When: now,
				Project: rec.Place.Project, Environment: rec.Place.Environment, AppName: rec.Place.Name}
			it.Detail = plainText(scaleAtMaxParts(it, placeParts(func(s string) string { return s }, it, slashed(it.Service))))
			items = append(items, it)
		}
	}
	return items, nil
}

// scaleAtMaxParts is the item's one wording. where is the service's place.
func scaleAtMaxParts(it AttentionItem, where []TextPart) []TextPart {
	out := append([]TextPart{}, where...)
	return append(out, txt(fmt.Sprintf(": at its maximum of %s and still over its target. Raise the maximum, give it more CPU, or add a server.", copyCount(it.Count))))
}
