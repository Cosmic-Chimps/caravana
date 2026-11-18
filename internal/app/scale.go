package app

import (
	"time"
)

// Limits of autoscaling (feature 043).
const (
	// MaxScaleCopies is the most copies a scaled service may ask for.
	MaxScaleCopies = 20
	// DefaultScaleCooldown is the wait, in seconds, between two scale steps
	// of a service that sets none.
	DefaultScaleCooldown = 300
	// MinScaleCooldown and MaxScaleCooldown bound a set cooldown, in seconds.
	MinScaleCooldown = 60
	MaxScaleCooldown = 3600

	// Ranges of the three targets.
	MinScaleCPU      = 10
	MaxScaleCPU      = 200
	MinScaleRequests = 1
	MaxScaleRequests = 100000
	MinScaleP95      = 10
	MaxScaleP95      = 60000
)

// The kinds of ScaleTarget.Kind.
const (
	ScaleOnCPU      = "cpu"
	ScaleOnRequests = "requestsPerCopy"
	ScaleOnP95      = "p95"
)

// Scale lets Caravana change how many copies a service runs, from its own
// metrics (feature 043). A scaled service's Replicas is then only the
// starting count: a deploy registers the count the service has now.
type Scale struct {
	// Min and Max bound the count, 1 to 20. A scaled service never runs
	// fewer than Min or more than Max copies.
	Min int `json:"min"`
	Max int `json:"max"`
	// Target is the one value the copies are kept near.
	Target ScaleTarget `json:"target"`
	// CooldownSeconds is how long Caravana waits after a step (or a deploy)
	// before it judges again, 60 to 3600. 0 means DefaultScaleCooldown.
	CooldownSeconds int `json:"cooldownSeconds,omitzero"`
}

// ScaleTarget is the value a scaled service's copies are kept near. Exactly
// one field is set.
type ScaleTarget struct {
	// CPU is the percent of the CPU the copies reserve, 10 to 200.
	CPU int `json:"cpu,omitzero"`
	// RequestsPerCopy is requests a second per copy, 1 to 100000. It is
	// measured at the proxy, so the service needs a domain.
	RequestsPerCopy int `json:"requestsPerCopy,omitzero"`
	// P95 is the time the slowest 5 % of requests may take, in milliseconds,
	// 10 to 60000. It is measured at the proxy, so the service needs a
	// domain.
	P95 int `json:"p95,omitzero"`
}

// Cooldown is the wait between two scale steps: CooldownSeconds, or the
// default when it is not set.
func (s Scale) Cooldown() time.Duration {
	if s.CooldownSeconds == 0 {
		return DefaultScaleCooldown * time.Second
	}
	return time.Duration(s.CooldownSeconds) * time.Second
}

// Kind names the target that is set: "cpu", "requestsPerCopy" or "p95". It is
// "" when none is set, and the first of them when several are (validation
// refuses that).
func (t ScaleTarget) Kind() string {
	switch {
	case t.CPU != 0:
		return ScaleOnCPU
	case t.RequestsPerCopy != 0:
		return ScaleOnRequests
	case t.P95 != 0:
		return ScaleOnP95
	}
	return ""
}

// set counts the target fields that are set.
func (t ScaleTarget) set() int {
	n := 0
	for _, v := range []int{t.CPU, t.RequestsPerCopy, t.P95} {
		if v != 0 {
			n++
		}
	}
	return n
}

// NeedsDomain reports whether the target is measured at the proxy, which only
// sees requests to a domain: requestsPerCopy and p95.
func (t ScaleTarget) NeedsDomain() bool {
	k := t.Kind()
	return k == ScaleOnRequests || k == ScaleOnP95
}

// CountFor is the number of copies a deploy registers for the service.
// current is the count the job runs now, or nil when there is no job to read
// (a first deploy): a scaled service keeps its current count, or starts at
// Replicas (Min when that is unset), always within Min to Max. A service
// without Scale runs Replicas, 1 when unset, and ignores current.
func (svc Service) CountFor(current *int) int {
	if svc.Scale == nil {
		if svc.Replicas == nil {
			return 1
		}
		return *svc.Replicas
	}
	n := svc.Scale.Min
	switch {
	case current != nil:
		n = *current
	case svc.Replicas != nil:
		n = *svc.Replicas
	}
	return min(max(n, svc.Scale.Min), svc.Scale.Max)
}

// validateScale checks a service's scale. field is the service's path, e.g.
// "services[0]".
func validateScale(field string, svc Service) error {
	s := svc.Scale
	if s == nil {
		return nil
	}
	switch {
	case len(svc.Volumes) > 0:
		return NewFieldError(field+".scale", "a service with volumes runs one copy, so it can't scale")
	case svc.Publishes():
		return NewFieldError(field+".scale", "a service with a published port runs one copy, so it can't scale")
	case s.Min < 1:
		return NewFieldError(field+".scale.min", "must be at least 1, got %d", s.Min)
	case s.Max < s.Min:
		return NewFieldError(field+".scale.max", "is %d, less than min (%d)", s.Max, s.Min)
	case s.Max > MaxScaleCopies:
		return NewFieldError(field+".scale.max", "is %d; at most %d copies are allowed", s.Max, MaxScaleCopies)
	}

	t := s.Target
	switch t.set() {
	case 0:
		return NewFieldError(field+".scale.target", "needs one value to keep the copies near: cpu, requestsPerCopy or p95")
	case 1:
	default:
		return NewFieldError(field+".scale.target", "has more than one value set; set exactly one of cpu, requestsPerCopy and p95")
	}
	switch {
	case t.CPU != 0 && (t.CPU < MinScaleCPU || t.CPU > MaxScaleCPU):
		return NewFieldError(field+".scale.target.cpu", "is %d; it must be %d to %d (percent of the CPU the copies reserve)", t.CPU, MinScaleCPU, MaxScaleCPU)
	case t.RequestsPerCopy != 0 && (t.RequestsPerCopy < MinScaleRequests || t.RequestsPerCopy > MaxScaleRequests):
		return NewFieldError(field+".scale.target.requestsPerCopy", "is %d; it must be %d to %d (requests a second per copy)", t.RequestsPerCopy, MinScaleRequests, MaxScaleRequests)
	case t.P95 != 0 && (t.P95 < MinScaleP95 || t.P95 > MaxScaleP95):
		return NewFieldError(field+".scale.target.p95", "is %d; it must be %d to %d (milliseconds)", t.P95, MinScaleP95, MaxScaleP95)
	}

	if s.CooldownSeconds != 0 && (s.CooldownSeconds < MinScaleCooldown || s.CooldownSeconds > MaxScaleCooldown) {
		return NewFieldError(field+".scale.cooldownSeconds", "is %d; it must be %d to %d seconds (leave it out for %d)", s.CooldownSeconds, MinScaleCooldown, MaxScaleCooldown, DefaultScaleCooldown)
	}
	if r := svc.Replicas; r != nil && (*r < s.Min || *r > s.Max) {
		return NewFieldError(field+".replicas", "is %d, outside the scale's %d to %d: it is the starting count", *r, s.Min, s.Max)
	}
	if t.NeedsDomain() && len(svc.Domains) == 0 {
		return NewFieldError(field+".scale.target", "%s is measured at the proxy: give the service a domain, or scale on cpu", t.Kind())
	}
	return nil
}
