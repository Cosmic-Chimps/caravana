package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The node's own facts and the host's history of boots and Caravana versions
// (feature 027). The sampler keeps the newest facts in memory, like its Traefik
// snapshot: nothing here is stored but the events, and a restart of Caravana
// has no facts until its first tick.

// HostNode is what Nomad says about the node (name, state, OS, kernel,
// versions, cores, address, events) and when it started.
type HostNode struct {
	deploy.NodeFacts
	// BootTime is when the node started, to the second; zero when unknown.
	BootTime time.Time
}

// bootMoved is how far the boot time must move between two readings for it to
// be a new boot. The time is worked out from an uptime and the moment it was
// read, which differ by a second or two from one tick to the next.
const bootMoved = time.Minute

// hostNode is a copy of the newest node facts, nil before the first reading.
func (s *Service) hostNode() *HostNode {
	st := &s.sampler
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.node == nil {
		return nil
	}
	n := *st.node
	n.Events = append([]deploy.NodeEvent(nil), n.Events...)
	return &n
}

// noteHost keeps what a tick learned about the node and records a boot. A tick
// that couldn't read the node leaves the old facts in place, except that a node
// Nomad lists as down is shown as down: its facts come from the node record
// alone, and its boot time and Traefik version stay as they were.
func (s *Service) noteHost(ctx context.Context, r *samplerReads) {
	st := &s.sampler
	st.mu.Lock()
	prev := st.node
	st.mu.Unlock()

	var node HostNode
	switch {
	case r.hostErr == nil:
		node = HostNode{NodeFacts: r.host.Node, BootTime: r.host.BootTime}
		switch {
		case r.imageErr == nil:
			node.TraefikVersion = deploy.ImageVersion(r.traefikImage)
		case prev != nil:
			node.TraefikVersion = prev.TraefikVersion // not readable now: the last known one
		}
		s.noteBoot(ctx, node.BootTime)
	case r.fallbackTried && r.fallbackErr == nil:
		node = HostNode{NodeFacts: r.fallback}
		if prev != nil {
			node.BootTime, node.TraefikVersion = prev.BootTime, prev.TraefikVersion
		}
	default:
		return
	}
	st.mu.Lock()
	st.node = &node
	st.mu.Unlock()
}

// noteBoot records a boot as a host event when the boot time is not the one
// last recorded within a minute. The reference is the last boot this process
// recorded, else the newest stored boot event, else none: the first boot ever
// seen is recorded too, so the history always has the current one. A store that
// can't be read skips the tick rather than guess.
func (s *Service) noteBoot(ctx context.Context, boot time.Time) {
	if boot.IsZero() {
		return
	}
	st := &s.sampler
	ref := st.bootRef
	if ref.IsZero() {
		events, err := s.store.HostEvents(ctx, store.HostEventBoot, 1)
		if err != nil {
			s.log.Warn("read the host's last boot", "error", err)
			return
		}
		if len(events) > 0 {
			ref = events[0].At
		}
	}
	if !ref.IsZero() && boot.Sub(ref).Abs() <= bootMoved {
		st.bootRef = ref
		return
	}
	s.appendHostEvent(ctx, store.HostEvent{
		Kind: store.HostEventBoot, At: boot, Detail: "{}", Key: "boot|" + boot.Format(time.RFC3339),
	})
	st.bootRef = boot
}

// recordVersion writes a version host event when the running version isn't the
// one the newest version event says. The key carries the start time, so a
// rollback to a version seen before is recorded too.
func (s *Service) recordVersion(ctx context.Context) {
	if s.runningVersion == "" {
		return
	}
	events, err := s.store.HostEvents(ctx, store.HostEventVersion, 1)
	if err != nil {
		s.log.Warn("read the host's version history", "error", err)
		return
	}
	from := ""
	if len(events) > 0 {
		var d struct{ To string }
		if err := json.Unmarshal([]byte(events[0].Detail), &d); err == nil {
			from = d.To
		}
		if from == s.runningVersion {
			return
		}
	}
	detail, err := json.Marshal(map[string]string{"from": from, "to": s.runningVersion})
	if err != nil {
		return
	}
	now := s.clock.Now().UTC()
	s.appendHostEvent(ctx, store.HostEvent{
		Kind: store.HostEventVersion, At: now, Detail: string(detail),
		Key: "version|" + s.runningVersion + "|" + now.Format(time.RFC3339),
	})
}

// appendHostEvent stores a host event and, when it is new, tells open pages. A
// failure is logged: the history is not worth failing a tick or a start for.
func (s *Service) appendHostEvent(ctx context.Context, e store.HostEvent) {
	added, err := s.store.AppendHostEvent(ctx, e)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			s.log.Warn("record a host event", "kind", e.Kind, "error", err)
		}
		return
	}
	if added {
		s.publish(Change{What: ChangeHost})
	}
}
