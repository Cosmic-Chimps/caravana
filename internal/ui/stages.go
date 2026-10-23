package ui

import (
	"fmt"
	"strconv"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// This file is the deployment entry's stages strip and the wording of a
// rollback (feature 021, page 4). Everything here is pure: it reads stored
// deployment records and never asks Nomad, so the page can say it while it
// renders.

// stageView is one cell of the stages strip: Queued, Roll out, Live. Mark is the
// status kind of the mark before the name (healthy: done, rolling: active, idle:
// still to come, failed). Active underlines the cell. Title, when set, is the
// exact time for the hover.
type stageView struct {
	Name   string
	Mark   string
	Value  string
	Active bool
	Title  string
	// Clock, when set, is Value as a clock element (the Live stage's time,
	// feature 022), so the viewer's local time can be added beside it.
	Clock *clockTime
}

// ToCome says the stage hasn't happened, and may never: its name is set quieter.
func (st stageView) ToCome() bool { return st.Mark == "idle" }

// Word says what the cell's mark means in words, for a reader who doesn't see it.
func (st stageView) Word() string {
	switch st.Mark {
	case "healthy":
		return "done"
	case "rolling":
		return "in progress"
	case "failed":
		return "failed"
	}
	return "not reached"
}

const noValue = "—"

// stagesOf says where a deployment stands in its three stages. p is the
// rollout's progress, read only for a running deployment (nil when Nomad can't
// say). The stage after a failed or ended one reads "—" and a ○ mark.
//
//   - Queued: waiting until Nomad accepts the job, then done with the time that
//     took; failed ("didn't start") when the deploy failed before it was
//     registered (a missing image, a refused registry, a validation error),
//     and "superseded" when a newer deploy won before it registered.
//   - Roll out: active while it runs ("1 of 2 new copies healthy"), done with its
//     duration, failed when it failed or was reverted.
//   - Live: done with the time the deployment ended healthy.
func stagesOf(rec store.DeploymentRecord, p *service.Progress) [3]stageView {
	queued := stageView{Name: "Queued", Mark: "idle", Value: noValue}
	roll := stageView{Name: "Roll out", Mark: "idle", Value: noValue}
	live := stageView{Name: "Live", Mark: "idle", Value: noValue}

	registered := rec.JobVersion != nil
	if registered {
		queued.Mark, queued.Value = "healthy", spanText(rec.CreatedAt, rec.RegisteredAt)
	}
	switch rec.Status {
	case store.StatusPending:
		queued.Mark, queued.Value, queued.Active = "rolling", "waiting", true
	case store.StatusRunning:
		roll.Mark, roll.Active = "rolling", true
		roll.Value = "waiting"
		if p != nil && (p.Wanted > 0 || p.Healthy > 0) {
			roll.Value = newCopiesHealthy(*p)
		}
	case store.StatusHealthy:
		roll.Mark, roll.Value = "healthy", noValue
		if rec.RegisteredAt != nil {
			roll.Value = spanText(*rec.RegisteredAt, &rec.UpdatedAt)
		}
		live.Mark, live.Value = "healthy", rec.UpdatedAt.UTC().Format("15:04:05 UTC")
		live.Title = rec.UpdatedAt.UTC().Format("2006-01-02 15:04:05 UTC")
		live.Clock = new(clockAt(rec.UpdatedAt, live.Value, localSeconds))
	case store.StatusReverted:
		roll.Mark, roll.Value = "failed", "failed"
	case store.StatusFailed:
		if registered {
			roll.Mark, roll.Value = "failed", "failed"
		} else {
			queued.Mark, queued.Value = "failed", "didn't start"
		}
	case store.StatusSuperseded:
		if registered {
			roll.Value = "superseded"
		} else {
			queued.Value = "superseded"
		}
	}
	return [3]stageView{queued, roll, live}
}

// newCopiesHealthy is how far a rollout has got, in the one wording the strip and
// the card share: "1 of 2 new copies healthy", "0 of 1 new copy healthy".
func newCopiesHealthy(p service.Progress) string {
	return fmt.Sprintf("%d of %d new %s healthy", p.Healthy, p.Wanted, copyNoun(p.Wanted))
}

// copyNoun is "copy" for one and "copies" for more, for "1 of 1 new copy healthy".
func copyNoun(n int) string {
	if n == 1 {
		return "copy"
	}
	return "copies"
}

// fullStrip says a history entry draws its three cells: one still in progress, the
// app's newest, and any that failed or was reverted, whose failed cell is what
// explains it. The older healthy and superseded ones get stageLine instead.
func fullStrip(rec store.DeploymentRecord, current bool) bool {
	switch rec.Status {
	case store.StatusPending, store.StatusRunning, store.StatusFailed, store.StatusReverted:
		return true
	}
	return current
}

// spanText is the time from one moment to the next as the strip writes it, or
// "—" when either is missing (a deployment recorded before Caravana kept when
// the job was accepted).
func spanText(from time.Time, to *time.Time) string {
	if to == nil || to.Before(from) {
		return noValue
	}
	return durationText(to.Sub(from))
}

// revertTarget is the job version Nomad names in a rollback message ("... -
// rolling back to job version 3"), or nil when the message doesn't.
func revertTarget(errText string) *uint64 {
	m := rolledBackTo.FindStringSubmatch(errText)
	if m == nil {
		return nil
	}
	n, err := strconv.ParseUint(m[1], 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

// repeatedVersion says which deployment the owner made that a rollback put back,
// as its version ("v0 again"). After a revert Nomad registers a new job version
// that is a copy of the last version that went healthy, so the version that runs
// (v2) has no deployment of its own: it repeats an earlier one (v0).
//
// failed is the reverted deployment and older the deployments before it, newest
// first. The version Nomad went back to is its last stable one, which is the
// newest of them that ended healthy, with a recorded definition and its images
// pinned; when Nomad's message names a version that a healthy deployment of the
// history has, that one is meant. Nil when the history has none to match, and
// then the pages say what they said before.
func repeatedVersion(failed store.DeploymentRecord, older []store.DeploymentRecord) *uint64 {
	if failed.Status != store.StatusReverted {
		return nil
	}
	target := revertTarget(failed.Error)
	var newest *uint64
	for _, d := range older {
		if d.Status != store.StatusHealthy || d.JobVersion == nil || d.Definition == nil || len(d.Deployment.Digests) == 0 {
			continue
		}
		if target != nil && *d.JobVersion == *target {
			return d.JobVersion
		}
		if newest == nil {
			newest = d.JobVersion
		}
	}
	return newest
}

// againWords is " (v0 again)" for a version of that deployment's, as a sentence
// to follow the version that runs. Nothing when again is nil or is the version
// itself (it is then the deployment the owner made, not a copy of one).
func againWords(shown, again *uint64) sentence {
	if again == nil || (shown != nil && *shown == *again) {
		return nil
	}
	return sentence{plain(" ("), mono(versionLabel(again)), plain(" again)")}
}
