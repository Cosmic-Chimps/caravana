package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Autoscaling (feature 043). After each sampler tick, autoscale reads every
// scaled service's stored minute rows, asks metrics.DecideScale, and carries
// out the one step it says with Nomad's scale API, under the app's lock. Each
// step is an app action "scale" with its counts, its reason and the job
// versions that map it to the deployment it scaled.

// autoscaleBudget bounds one pass: the tick's own work has its budget apart.
const autoscaleBudget = 30 * time.Second

// storeCounts keeps the counts a deploy registered for the app's scaled
// services (and drops those of services that are no longer scaled). A failed
// write is logged: the autoscaler reads the counts from Nomad on its next pass.
func (s *Service) storeCounts(appID string, counts map[string]int) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.store.ReplaceServiceCounts(ctx, appID, counts); err != nil {
		s.log.Warn("store the counts of a deployed app; the next autoscale pass reads them from Nomad", "app", appID, "error", err)
	}
}

// versionAliases is the deployer's VersionAliases hook: the job versions that
// scale steps made, by app, with the version of the deployment each scaled. A
// failed read leaves the versions as Nomad has them and is logged once, until
// a read works again.
func (s *Service) versionAliases(ctx context.Context) (map[string]map[uint64]uint64, error) {
	aliases, err := s.store.ScaleVersions(ctx)
	if err != nil {
		if s.aliasFailing.CompareAndSwap(false, true) {
			s.log.Warn("read the versions scale steps made; copies show Nomad's job versions until it works", "error", err)
		}
		return nil, err
	}
	s.aliasFailing.Store(false)
	return aliases, nil
}

// scaleRule is a scaled service's settings as the rule reads them.
func scaleRule(sc app.Scale) metrics.ScaleRule {
	r := metrics.ScaleRule{Min: sc.Min, Max: sc.Max, Kind: sc.Target.Kind(), Cooldown: sc.Cooldown()}
	switch r.Kind {
	case app.ScaleOnCPU:
		r.Target = float64(sc.Target.CPU)
	case app.ScaleOnRequests:
		r.Target = float64(sc.Target.RequestsPerCopy)
	case app.ScaleOnP95:
		r.Target = float64(sc.Target.P95)
	}
	return r
}

// hasScale reports whether any service of the app scales.
func hasScale(a app.App) bool {
	for _, svc := range a.Services {
		if svc.Scale != nil {
			return true
		}
	}
	return false
}

// autoscale is one pass over every app with a scaled service. Errors are
// logged and never fail the sampler's tick.
func (s *Service) autoscale(ctx context.Context, now time.Time) {
	ctx, cancel := context.WithTimeout(ctx, autoscaleBudget)
	defer cancel()
	apps, err := s.store.ListApps(ctx)
	if err != nil {
		s.log.Warn("autoscale: list apps", "error", err)
		return
	}
	var layouts map[int64]string // read when the first scaled app needs it
	for _, rec := range apps {
		if ctx.Err() != nil {
			return
		}
		if rec.StoppedAt != nil || !rec.App.Defined() || !hasScale(rec.App) {
			continue
		}
		if layouts == nil {
			if layouts, err = s.store.MetricsLayouts(ctx); err != nil {
				s.log.Warn("autoscale: read the bucket layouts", "error", err)
				return
			}
		}
		if err := s.autoscaleApp(ctx, rec.App.ID, now, layouts); err != nil {
			s.log.Warn("autoscale", "app", rec.App.ID, "error", err)
		}
	}
}

// autoscaleApp judges the scaled services of one app and makes at most one
// step, so two services of an app never race in Nomad's deployment. An app whose
// lock is held (a deploy, a restart, a volume run), a deployment that is still
// going, and a job whose own Nomad deployment is still placing copies are left
// for a later tick.
func (s *Service) autoscaleApp(ctx context.Context, appID string, now time.Time, layouts map[int64]string) error {
	state, err := s.tryLockApp(appID)
	if err != nil {
		if errors.Is(err, ErrAppBusy) || errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	defer state.mu.Unlock()

	// What was read before the lock may have changed while a deploy held it.
	rec, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return err
	}
	if rec.StoppedAt != nil || !rec.App.Defined() {
		return nil
	}
	deps, err := s.store.ListDeployments(ctx, appID, 1)
	if err != nil {
		return err
	}
	if len(deps) == 0 || deps[0].Status == store.StatusPending || deps[0].Status == store.StatusRunning {
		return nil
	}
	lastChange := deps[0].UpdatedAt // the end of the newest deployment

	st, err := s.deployer.ScaleState(ctx, appID)
	if err != nil {
		return err
	}
	if !st.Exists || st.Stopped || st.Rolling {
		return nil
	}
	// What Nomad has is what the status counts, also after a change made
	// outside Caravana or a revert of a failed scale-up.
	counts := map[string]int{}
	for _, svc := range rec.App.Services {
		if n, ok := st.Counts[svc.Name]; ok && svc.Scale != nil {
			counts[svc.Name] = n
		}
	}
	if err := s.store.ReplaceServiceCounts(ctx, appID, counts); err != nil {
		return err
	}

	for _, svc := range rec.App.Services {
		count, ok := counts[svc.Name]
		if !ok {
			continue
		}
		since := lastChange
		last, found, err := s.store.LastScale(ctx, appID, svc.Name)
		if err != nil {
			return err
		}
		if found {
			at := last.StartedAt
			if last.FinishedAt != nil {
				at = *last.FinishedAt
			}
			if at.After(since) {
				since = at
			}
		}
		rows, err := s.scaleRows(ctx, appID, svc.Name, now, layouts)
		if err != nil {
			return err
		}
		rule := scaleRule(*svc.Scale)
		d := metrics.DecideScale(metrics.ScaleInput{Rule: rule, Count: count, Rows: rows, LastChange: since, Now: now})
		if d.Move == metrics.MoveHold {
			continue
		}
		s.applyScale(ctx, appID, svc.Name, d, rule, count, st, now)
		return nil
	}
	return nil
}

// scaleRows reads a service's newest minute rows: the longest window the
// decision reads, and one more row.
func (s *Service) scaleRows(ctx context.Context, appID, service string, now time.Time, layouts map[int64]string) ([]metrics.Row, error) {
	series, err := s.store.EnsureSeries(ctx, appID, service)
	if err != nil {
		return nil, err
	}
	reach := time.Duration(max(metrics.ScaleDownSamples, metrics.WindowMinutes)+1) * s.metricsGap
	samples, err := s.store.MinuteRows(ctx, series, now.Add(-reach), now.Add(s.metricsGap))
	if err != nil {
		return nil, err
	}
	rows := make([]metrics.Row, 0, len(samples))
	for _, m := range samples {
		rows = append(rows, samplerRow(m, layouts))
	}
	return rows, nil
}

// applyScale makes one step with Nomad and records it, whatever the outcome: a
// refused step counts for the cooldown, so it is not tried again at the next
// tick.
func (s *Service) applyScale(ctx context.Context, appID, service string, d metrics.ScaleDecision, rule metrics.ScaleRule, count int, st deploy.ScaleState, now time.Time) {
	reason := scaleReason(d, rule, count, time.Duration(d.Samples)*s.metricsGap)
	id, err := newID("act_")
	if err != nil {
		s.log.Warn("autoscale: make an action ID", "error", err)
		return
	}
	rec := store.ActionRecord{ID: id, AppID: appID, Action: store.ActionScale, Trigger: string(app.TriggerAutoscale),
		Status: store.ActionSucceeded, Service: service, StartedAt: now, FinishedAt: &now,
		FromCount: new(count), ToCount: new(d.To), Reason: reason}

	version, found, err := s.deployer.Scale(ctx, appID, service, d.To, reason)
	switch {
	case err != nil:
		rec.Status, rec.Error = store.ActionFailed, truncateBytes(err.Error(), 300)
	default:
		if found {
			// The version Nomad made, and the deployment's version it scaled: the
			// one before this step, itself mapped when that was a scale's.
			base := st.Version
			if aliases, aerr := s.store.ScaleVersions(ctx); aerr == nil {
				if b, ok := aliases[appID][base]; ok {
					base = b
				}
			}
			rec.JobVersion, rec.BaseVersion = new(version), new(base)
		}
		if serr := s.store.SetServiceCount(ctx, appID, service, d.To); serr != nil {
			s.log.Warn("autoscale: store the new count", "app", appID, "service", service, "error", serr)
		}
	}
	// The record is written with a context of its own: the step is done or
	// refused, and the cooldown needs it either way.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if ierr := s.store.InsertAction(wctx, rec); ierr != nil {
		s.log.Error("autoscale: record the step", "app", appID, "service", service, "error", ierr)
	}
	if err != nil {
		s.log.Warn("autoscale: Nomad refused the step", "app", appID, "service", service, "from", count, "to", d.To, "error", err)
	}
	s.actionsChanged(appID)
	s.appChanged(appID)
	s.publish(Change{AppID: appID, What: ChangeCopies})
	s.publish(Change{What: ChangeActivity})
	s.publish(Change{What: ChangeMetrics})
}

// scaleReason is the one sentence a step records, in Caravana's words with
// numbers only. span is how long the rows the decision read cover.
func scaleReason(d metrics.ScaleDecision, r metrics.ScaleRule, count int, span time.Duration) string {
	if d.Move == metrics.MoveBound {
		return fmt.Sprintf("the count was outside the scale's %d–%d copies", r.Min, r.Max)
	}
	over := "over the last " + spanWords(span)
	value, target := scaleNumber(d.Value), scaleNumber(r.Target)
	if d.Move == metrics.MoveDown && r.Kind != metrics.ScaleOnP95 && count > 1 {
		value = scaleNumber(d.Value * float64(count) / float64(count-1)) // what one copy fewer would carry
	}
	switch {
	case r.Kind == metrics.ScaleOnRequests && d.Move == metrics.MoveDown:
		return fmt.Sprintf("about %s requests a second per copy on one copy fewer %s, target %s", value, over, target)
	case r.Kind == metrics.ScaleOnRequests:
		return fmt.Sprintf("%s requests a second per copy %s, target %s", value, over, target)
	case r.Kind == metrics.ScaleOnCPU && d.Move == metrics.MoveDown:
		return fmt.Sprintf("about %s %% CPU on one copy fewer %s, target %s %%", value, over, target)
	case r.Kind == metrics.ScaleOnCPU:
		return fmt.Sprintf("CPU at %s %% of its reservation %s, target %s %%", value, over, target)
	case r.Kind == metrics.ScaleOnP95 && d.Move == metrics.MoveDown:
		return fmt.Sprintf("the slowest 5 %% at %s ms %s, under half of the %s ms target", value, over, target)
	case r.Kind == metrics.ScaleOnP95:
		return fmt.Sprintf("the slowest 5 %% at %s ms %s, target %s ms", value, over, target)
	}
	return fmt.Sprintf("scaled from %d to %d copies", count, d.To)
}

// scaleNumber writes a measured value: whole from 10 up, else one decimal,
// without a trailing ".0".
func scaleNumber(v float64) string {
	if v >= 10 {
		return strconv.FormatFloat(float64(int64(v+0.5)), 'f', -1, 64)
	}
	return strconv.FormatFloat(float64(int64(v*10+0.5))/10, 'f', -1, 64)
}

// spanWords says how long the window was: "3 minutes", "1 minute", or seconds
// when the sampler's gap is shorter than a minute (the lab's).
func spanWords(d time.Duration) string {
	switch {
	case d >= time.Minute && d%time.Minute == 0:
		n := int(d / time.Minute)
		if n == 1 {
			return "minute"
		}
		return fmt.Sprintf("%d minutes", n)
	case d >= time.Minute:
		return fmt.Sprintf("%.1f minutes", d.Minutes())
	case d >= time.Second:
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	}
	return "few moments"
}
