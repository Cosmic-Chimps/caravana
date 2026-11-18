package deploy

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/hashicorp/nomad/api"
)

// EventBatch is what changed in Nomad, as job IDs (sorted, distinct).
type EventBatch struct {
	Copies      []string // jobs whose allocations changed (Allocation topic: payload Allocation.JobID)
	Deployments []string // jobs whose rollouts changed (Deployment topic: payload Deployment.JobID)
	Rollouts    []string // of those, jobs whose rollout may have ended or been replaced (see rolloutMoved)
	Jobs        []string // jobs registered, changed or deregistered (Job topic: event Key, or payload Job.ID)
	Nodes       bool     // a node changed (Node topic)
	Resync      bool     // the stream (re)connected: events may have been missed, treat everything as changed
}

// Empty says the batch holds nothing to act on.
func (b EventBatch) Empty() bool {
	return len(b.Copies) == 0 && len(b.Deployments) == 0 && len(b.Rollouts) == 0 && len(b.Jobs) == 0 && !b.Nodes && !b.Resync
}

// errStreamClosed is what a stream that ended with no error says.
var errStreamClosed = errors.New("the event stream was closed")

// eventTopics is what FollowEvents subscribes to: everything Caravana reads
// from Nomad that moves on its own. Each topic takes every key.
var eventTopics = map[api.Topic][]string{
	api.TopicJob:        {"*"},
	api.TopicAllocation: {"*"},
	api.TopicDeployment: {"*"},
	api.TopicNode:       {"*"},
}

// batchOf turns one answer of the stream into a batch. It reads the job IDs
// straight out of each payload: an allocation's payload is the whole
// allocation, and decoding it just for an ID would be most of the work. An
// event whose ID can't be read is skipped (the safety-net polls cover it).
func batchOf(events []api.Event) EventBatch {
	var copies, deployments, rollouts, jobs []string
	var b EventBatch
	for _, e := range events {
		switch e.Topic {
		case api.TopicAllocation:
			if id := payloadString(e.Payload, "Allocation", "JobID"); id != "" {
				copies = append(copies, id)
			}
		case api.TopicDeployment:
			if id := payloadString(e.Payload, "Deployment", "JobID"); id != "" {
				deployments = append(deployments, id)
				if rolloutMoved(e) {
					rollouts = append(rollouts, id)
				}
			}
		case api.TopicJob:
			id := e.Key
			if id == "" {
				id = payloadString(e.Payload, "Job", "ID")
			}
			if id != "" {
				jobs = append(jobs, id)
			}
		case api.TopicNode:
			b.Nodes = true
		}
	}
	b.Copies, b.Deployments, b.Jobs = sortedDistinct(copies), sortedDistinct(deployments), sortedDistinct(jobs)
	b.Rollouts = sortedDistinct(rollouts)
	return b
}

// rolloutMoved says a Deployment event may have ended a rollout or started a
// newer one, which is all a rollout's watcher waits for. Most events of a rollout
// are Nomad counting one more healthy copy (type AllocationUpdated, lab, observed):
// those only move its progress, unless they carry a final status.
func rolloutMoved(e api.Event) bool {
	if e.Type != "AllocationUpdated" {
		return true
	}
	switch payloadString(e.Payload, "Deployment", "Status") {
	case api.DeploymentStatusSuccessful, api.DeploymentStatusFailed, api.DeploymentStatusCancelled:
		return true
	}
	return false
}

// payloadString reads payload[object][field] when both are what they should
// be, else "". (A type assertion with two results, v, ok := x.(T), reports a
// mismatch instead of panicking; C#'s `as`.)
func payloadString(payload map[string]any, object, field string) string {
	inner, ok := payload[object].(map[string]any)
	if !ok {
		return ""
	}
	s, _ := inner[field].(string)
	return s
}

func sortedDistinct(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

// merge returns a and b as one batch.
func merge(a, b EventBatch) EventBatch {
	return EventBatch{
		Copies:      sortedDistinct(slices.Concat(a.Copies, b.Copies)),
		Deployments: sortedDistinct(slices.Concat(a.Deployments, b.Deployments)),
		Rollouts:    sortedDistinct(slices.Concat(a.Rollouts, b.Rollouts)),
		Jobs:        sortedDistinct(slices.Concat(a.Jobs, b.Jobs)),
		Nodes:       a.Nodes || b.Nodes,
		Resync:      a.Resync || b.Resync,
	}
}

// FollowEvents reads Nomad's event stream until ctx ends and calls handle
// with what changed (research: one subscription replaces the fast polls).
//
// The stream is a hint, never the truth: events can be missed while it is
// down, so the first batch after any connect that wasn't a planned renewal
// has Resync set, and handle must then look at everything. After a failure
// FollowEvents calls failed with the error, waits (Timing.BackoffStart,
// doubling up to BackoffMax) and connects again from the last index it saw, so
// Nomad replays what it still holds. Each connection is renewed after
// Timing.StreamLifetime without a Resync: it bounds a connection that died
// without telling anyone, since the client swallows Nomad's heartbeats.
//
// Events that arrive together are handed to handle as one batch. handle and
// failed run on this goroutine and must not block for long.
func (d *Deployer) FollowEvents(ctx context.Context, handle func(EventBatch), failed func(error)) {
	timing := d.Timing.withDefaults()
	var last uint64 // the newest index handed out
	backoff := timing.BackoffStart
	resync := true // the first connect, like any after a failure, may have missed events

	for ctx.Err() == nil {
		connected, planned, err := d.followStream(ctx, timing.StreamLifetime, &last, resync, handle)
		if ctx.Err() != nil {
			return
		}
		if connected {
			backoff = timing.BackoffStart
		}
		if planned {
			resync = false // the next connect resumes from last: nothing was missed
			continue
		}
		resync = true
		failed(err)
		if !sleep(ctx, backoff) {
			return
		}
		backoff = min(2*backoff, timing.BackoffMax)
	}
}

// followStream is one connection of FollowEvents. It reports whether it
// connected, whether it ended because its lifetime was up (planned) and, if
// not, why it ended. last is read to resume and updated as events are handed out.
func (d *Deployer) followStream(ctx context.Context, lifetime time.Duration, last *uint64, resync bool, handle func(EventBatch)) (connected, planned bool, err error) {
	connCtx, cancel := context.WithTimeout(ctx, lifetime)
	defer cancel()

	// 0 on the first connect: Nomad then replays what its buffer still holds (lab,
	// observed), which lands in the Resync batch and costs nothing more.
	var from uint64
	if *last > 0 {
		from = *last + 1
	}
	stream, err := d.nomad.EventStream().Stream(connCtx, eventTopics, from, nil)
	if err != nil {
		return false, ended(ctx, connCtx), fmt.Errorf("connect to Nomad's event stream: %w", err)
	}
	if resync {
		handle(EventBatch{Resync: true})
	}

	for {
		select {
		case <-connCtx.Done():
			return true, ended(ctx, connCtx), connCtx.Err()
		case first, ok := <-stream:
			if !ok {
				return true, ended(ctx, connCtx), errStreamClosed
			}
			batch, streamErr := takeEvents(first, stream, last)
			if !batch.Empty() {
				handle(batch)
			}
			if streamErr != nil {
				return true, ended(ctx, connCtx), fmt.Errorf("read Nomad's event stream: %w", streamErr)
			}
		}
	}
}

// ended says a connection ended because its lifetime was up, not because the
// caller is stopping or something broke.
func ended(ctx, connCtx context.Context) bool {
	return ctx.Err() == nil && connCtx.Err() != nil
}

// takeEvents turns first into a batch, then takes whatever else is already
// waiting in the channel without blocking and merges it in, so a burst of
// changes is one call to handle. It moves last forward. The error is the
// first one the stream reported; the stream is over after it.
func takeEvents(first *api.Events, stream <-chan *api.Events, last *uint64) (EventBatch, error) {
	batch := EventBatch{}
	for next := first; ; {
		if next.Err != nil {
			return batch, next.Err
		}
		if next.Index > *last {
			*last = next.Index
		}
		batch = merge(batch, batchOf(next.Events))
		select {
		case more, ok := <-stream:
			if !ok {
				return batch, nil // the caller sees the closed channel on its next read
			}
			next = more
		default:
			return batch, nil
		}
	}
}
