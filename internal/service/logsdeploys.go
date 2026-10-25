package service

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Deploy rows of the logs stream (feature 024, research R6): where a deploy
// started and ended, between the lines, and the markers on the histogram. They
// come from the store, never from Nomad, and are merged into the stream by the
// caller by time; they are never filtered by level or search, only by app.

// DeployKind says what a deploy row marks.
type DeployKind string

// The five kinds: one start row, and one end row for a deployment that ended.
const (
	DeployStarted    DeployKind = "started"    // the deploy began rolling out (CreatedAt)
	DeployLive       DeployKind = "live"       // it went healthy
	DeployFailed     DeployKind = "failed"     // it failed
	DeployReverted   DeployKind = "reverted"   // it failed and Nomad went back to an older version
	DeploySuperseded DeployKind = "superseded" // a newer deploy replaced it mid-rollout
)

// DeployRow is one deploy marker.
type DeployRow struct {
	DeploymentID string
	App          string
	// Service is empty: a deployment is the whole app's.
	Service string
	// At is the deployment's CreatedAt for a started row and, for an end row,
	// its UpdatedAt (a final status has no later change).
	At   time.Time
	Kind DeployKind
	// JobVersion is the Nomad job version the deployment registered; 0 when it
	// never got one.
	JobVersion int
	// RevertedTo is the job version Nomad went back to, on a reverted row: the
	// one its message names, else the last healthy deployment's before it, else
	// 0 (unknown).
	RevertedTo int
	// Who is page 4's who-words, lowercase: "pushed to main by ana", "promoted
	// from staging", "through the API by ci-token", "by the webhook", "from the
	// console".
	Who string
}

// LogDeploys lists the deploy rows of the environment's deployments that
// started or ended in [from, to] (inclusive), oldest first. An empty project
// or environment means Default and production. onlyApp, when not empty, keeps
// that app's rows. A deployment still pending or running has only its started
// row.
func (s *Service) LogDeploys(ctx context.Context, project, environment string, from, to time.Time, onlyApp string) ([]DeployRow, error) {
	if project == "" {
		project = app.DefaultProject
	}
	if environment == "" {
		environment = app.DefaultEnvironment
	}
	recs, err := s.store.ListDeploymentsIn(ctx, project, environment, from, to)
	if err != nil {
		return nil, fmt.Errorf("read deploys of %s/%s: %w", project, environment, err)
	}

	var rows []DeployRow
	earlier := map[string][]store.DeploymentRecord{} // an app's deployments, for a revert nothing named
	inRange := func(t time.Time) bool { return !t.Before(from) && !t.After(to) }
	for _, rec := range recs {
		if onlyApp != "" && rec.AppID != onlyApp {
			continue
		}
		row := DeployRow{DeploymentID: rec.Deployment.ID, App: rec.AppID, Who: deployWhoWords(rec)}
		if rec.JobVersion != nil {
			row.JobVersion = int(*rec.JobVersion)
		}
		if inRange(rec.CreatedAt) {
			started := row
			started.At, started.Kind = rec.CreatedAt, DeployStarted
			rows = append(rows, started)
		}
		kind, ended := endKind(rec.Status)
		if !ended || !inRange(rec.UpdatedAt) {
			continue
		}
		row.At, row.Kind = rec.UpdatedAt, kind
		if kind == DeployReverted {
			if n, ok := revertedToVersion(rec.Error); ok {
				row.RevertedTo = n
			} else {
				if _, seen := earlier[rec.AppID]; !seen {
					list, err := s.store.ListDeployments(ctx, rec.AppID, 0)
					if err != nil {
						return nil, fmt.Errorf("read deploys of %s/%s: %w", project, environment, err)
					}
					earlier[rec.AppID] = list
				}
				row.RevertedTo = previousHealthyVersion(earlier[rec.AppID], rec)
			}
		}
		rows = append(rows, row)
	}
	slices.SortStableFunc(rows, func(a, b DeployRow) int { return a.At.Compare(b.At) })
	return rows, nil
}

// endKind is the row kind of a final status; ended is false for pending and
// running.
func endKind(st store.Status) (kind DeployKind, ended bool) {
	switch st {
	case store.StatusHealthy:
		return DeployLive, true
	case store.StatusFailed:
		return DeployFailed, true
	case store.StatusReverted:
		return DeployReverted, true
	case store.StatusSuperseded:
		return DeploySuperseded, true
	}
	return "", false
}

var rolledBackToVersion = regexp.MustCompile(`rolling back to job version (\d+)`)

// revertedToVersion reads the version Nomad's message names ("... - rolling
// back to job version 3").
func revertedToVersion(errText string) (int, bool) {
	m := rolledBackToVersion.FindStringSubmatch(errText)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

// previousHealthyVersion is the job version of the newest deployment of the
// list (an app's, newest first) that went healthy before rec was created, or 0.
func previousHealthyVersion(list []store.DeploymentRecord, rec store.DeploymentRecord) int {
	for _, d := range list {
		if d.Status == store.StatusHealthy && d.JobVersion != nil && d.CreatedAt.Before(rec.CreatedAt) {
			return int(*d.JobVersion)
		}
	}
	return 0
}

// deployWhoWords says who deployed, as the console's page 4 words it (ui's
// deployWho, as plain text and without the commit): "promoted from staging",
// "pushed to main by ana", "by the webhook", "through the API" (and " by
// <token>" when the deployment recorded the token), "from the console".
func deployWhoWords(rec store.DeploymentRecord) string {
	if rec.SourceApp != "" {
		return "promoted from " + rec.SourceEnvironment
	}
	if o := rec.Deployment.Origin; o != nil {
		branch := o.BranchOrTag()
		if branch != "" || o.Actor != "" {
			out := "pushed"
			if branch != "" {
				out += " to " + branch
			}
			if o.Actor != "" {
				out += " by " + o.Actor
			}
			return out
		}
		if o.ShortCommit() != "" {
			return "pushed"
		}
	}
	switch rec.Deployment.Trigger {
	case app.TriggerWebhook:
		return "by the webhook"
	case app.TriggerManual:
		if name := rec.Deployment.TokenName; name != "" {
			return "through the API by " + name
		}
		return "through the API"
	case app.TriggerConsole:
		return "from the console"
	}
	return "by " + string(rec.Deployment.Trigger)
}
