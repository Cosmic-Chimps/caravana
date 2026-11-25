package service

import (
	"context"
	"log/slog"
	"net/mail"
	"sync"
	"time"

	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The audit log's recorder (feature 057). The service notes a change with s.audit (Auth with a.audit)
// once the change is stored; a note never fails the change, and a method that failed before changing
// anything notes nothing.
//
// One request, one entry (plan D2): the API (every write route) and the console (every form post) open
// an AuditScope in the request's context. Notes inside a scope are collected: the first is the entry,
// later notes about the same resource add their changes, notes about other resources are listed in
// related. Finish writes the entry when the handler returns. Outside a scope (a watcher, the reaper,
// the CLI, a GET stream) a note is written at once.

type (
	auditScopeKey struct{}
	auditDoorKey  struct{}
)

// auditWriter is what writes an entry: the store, a log for a write that failed, and a clock. Service
// and Auth each have one, and both go through the same functions.
type auditWriter struct {
	store *store.Store
	log   *slog.Logger
	now   func() time.Time
}

func (w auditWriter) logger() *slog.Logger {
	if w.log == nil {
		return slog.Default()
	}
	return w.log
}

// write stores e and logs a failure: an audit entry that couldn't be written never fails the change it
// is about, and never carries a secret into the log (an entry holds none).
func (w auditWriter) write(ctx context.Context, at time.Time, e audit.Entry) {
	if !audit.ValidAction(e.Action) || !audit.ValidKind(e.Resource.Kind) {
		w.logger().Error("audit: an entry with an action or a resource kind outside the closed lists",
			"action", string(e.Action), "kind", string(e.Resource.Kind), "operation", e.Operation)
	}
	// A cancelled request must not lose the entry of a change it already made.
	if _, err := w.store.InsertAuditEvent(context.WithoutCancel(ctx), at, e); err != nil {
		w.logger().Error("audit: an entry was not written", "action", string(e.Action),
			"kind", string(e.Resource.Kind), "operation", e.Operation, "error", err)
	}
}

// AuditScope collects the notes of one request into one entry.
type AuditScope struct {
	door      audit.Door
	address   string
	operation string

	mu        sync.Mutex
	principal *Principal
	fallback  audit.Resource
	w         *auditWriter
	at        time.Time
	entry     *audit.Entry
	finished  bool
	// unchanged is set by NoChange: the request succeeded and changed nothing, so it needs no fallback.
	unchanged bool
}

// WithAuditScope returns a context with a scope for one request, and the scope. The door is how the
// request came (console, api, mcp, webhook), address the client's, operation "METHOD /pattern". The
// caller ends it with Finish. Service.BeginAudit does the same and binds the scope to the service, so
// Finish can write an entry no method noted.
func WithAuditScope(ctx context.Context, door audit.Door, address, operation string) (context.Context, *AuditScope) {
	sc := &AuditScope{door: door, address: clip(address, 64), operation: clip(operation, 200)}
	return context.WithValue(ctx, auditScopeKey{}, sc), sc
}

// BeginAudit is WithAuditScope for a request the service will record.
func (s *Service) BeginAudit(ctx context.Context, door audit.Door, address, operation string) (context.Context, *AuditScope) {
	ctx, sc := WithAuditScope(ctx, door, address, operation)
	sc.w = &auditWriter{store: s.store, log: s.log, now: s.clock.Now}
	return ctx, sc
}

// AuditScopeFrom is the scope WithAuditScope put in ctx, or nil.
func AuditScopeFrom(ctx context.Context) *AuditScope {
	sc, _ := ctx.Value(auditScopeKey{}).(*AuditScope)
	return sc
}

// WithAuditDoor marks a context that has no scope with the door it acts through: the host's CLI says
// "host". A note outside a scope uses it.
func WithAuditDoor(ctx context.Context, door audit.Door) context.Context {
	return context.WithValue(ctx, auditDoorKey{}, door)
}

// SetActor tells the scope who the request acts as: the API's authenticate calls it once the
// credentials are known, so even a request nothing noted has its actor.
func (sc *AuditScope) SetActor(p Principal) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.principal = &p
}

// SetFallback tells the scope what the request is about, for the entry Finish writes when nothing was
// noted: the API takes it from the route's access rule.
func (sc *AuditScope) SetFallback(r audit.Resource) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.fallback = r
}

// NoChange tells the scope the request succeeded and changed nothing (a webhook whose images are the
// ones already live, an unpin of a backup that holds no label), so Finish writes no fallback entry for it.
func (sc *AuditScope) NoChange() {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.unchanged = true
}

// Finish ends the scope once the handler returned with the status it wrote, and writes the entry. A
// request that changed something and then failed keeps its entry; one that noted nothing and ended below
// 400 gets a fallback entry (action changed, the resource given here or by SetFallback, installation
// when there is none) and a warning, since a write route that notes nothing is a gap; one that failed
// and noted nothing leaves no entry. A note after Finish (a goroutine that outlived the request) is
// written at once.
func (sc *AuditScope) Finish(status int, fallback audit.Resource) {
	sc.mu.Lock()
	if sc.finished {
		sc.mu.Unlock()
		return
	}
	sc.finished = true
	w, entry, at := sc.w, sc.entry, sc.at
	if entry == nil && status < 400 && !sc.unchanged {
		res := fallback
		if res.Kind == "" {
			res = sc.fallback
		}
		if res.Kind == "" {
			res = audit.Resource{Kind: audit.KindInstallation}
		}
		e := audit.Entry{Action: audit.ActionChanged, Resource: res, Fallback: true}
		sc.fill(context.Background(), &e)
		entry = &e
		if w != nil {
			at = w.now()
			w.logger().Warn("audit: a write request noted nothing, so a fallback entry was written", "operation", sc.operation)
		}
	}
	sc.mu.Unlock()
	if entry == nil || w == nil {
		return
	}
	w.write(context.Background(), at, *entry)
}

// fill completes e with the scope's door, address, operation and actor. Called with sc.mu held.
func (sc *AuditScope) fill(ctx context.Context, e *audit.Entry) {
	if e.Door == "" {
		e.Door = sc.door
	}
	if e.Address == "" {
		e.Address = sc.address
	}
	if e.Operation == "" {
		e.Operation = sc.operation
	}
	if e.Actor.Kind == "" {
		p := sc.principal
		if p == nil {
			if pr, ok := PrincipalFrom(ctx); ok {
				p = &pr
			}
		}
		e.Actor = actorFor(p, e.Door, e.Resource)
	}
	clipEntry(e)
}

// note adds e to the scope. It reports false when the scope has finished, and the caller writes e itself.
func (sc *AuditScope) note(w auditWriter, ctx context.Context, e audit.Entry) bool {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.w == nil {
		sc.w = &w
	}
	sc.fill(ctx, &e)
	if sc.finished {
		return false
	}
	if sc.entry == nil {
		sc.entry, sc.at = &e, w.now()
		return true
	}
	first := sc.entry
	if first.Resource.Kind == e.Resource.Kind && first.Resource.ID == e.Resource.ID {
		first.Changes = append(first.Changes, e.Changes...)
		if first.Record == nil {
			first.Record = e.Record
		}
		return true
	}
	rel := audit.Related{Kind: e.Resource.Kind, ID: e.Resource.ID, Action: e.Action}
	for _, have := range first.Related {
		if have == rel {
			return true
		}
	}
	first.Related = append(first.Related, rel)
	return true
}

// audit notes a change that was stored: who (from ctx), what was done and to what, and the changes. It
// never fails the change; a failed write is logged.
func (s *Service) audit(ctx context.Context, e audit.Entry) {
	s.writer().note(ctx, e)
}

// audit is Service.audit for the accounts side, which has its own store.
func (a *Auth) audit(ctx context.Context, e audit.Entry) {
	auditWriter{store: a.store, log: a.log, now: a.now}.note(ctx, e)
}

// noChange tells the request's audit scope, if there is one, that the call changed nothing.
func (s *Service) noChange(ctx context.Context) {
	if sc := AuditScopeFrom(ctx); sc != nil {
		sc.NoChange()
	}
}

func (s *Service) writer() auditWriter {
	return auditWriter{store: s.store, log: s.log, now: s.clock.Now}
}

// note is the one function Service and Auth share.
func (w auditWriter) note(ctx context.Context, e audit.Entry) {
	if sc := AuditScopeFrom(ctx); sc != nil && sc.note(w, ctx, e) {
		return
	}
	// Outside a scope, or after it finished: written at once.
	scope := AuditScopeFrom(ctx)
	if scope != nil {
		scope.mu.Lock()
		scope.fill(ctx, &e)
		scope.mu.Unlock()
	} else {
		fillWithoutScope(ctx, &e)
	}
	w.write(ctx, w.now(), e)
}

func fillWithoutScope(ctx context.Context, e *audit.Entry) {
	if e.Door == "" {
		e.Door = doorOf(ctx)
	}
	if e.Actor.Kind == "" {
		var p *Principal
		if pr, ok := PrincipalFrom(ctx); ok {
			p = &pr
		}
		e.Actor = actorFor(p, e.Door, e.Resource)
	}
	clipEntry(e)
}

// doorOf is the door of a context with no scope: the one WithAuditDoor set; a request's own context (a
// principal and nothing else, such as a GET stream) is the API's; with neither, Caravana itself.
func doorOf(ctx context.Context) audit.Door {
	if d, ok := ctx.Value(auditDoorKey{}).(audit.Door); ok {
		return d
	}
	if _, ok := PrincipalFrom(ctx); ok {
		return audit.DoorAPI
	}
	return audit.DoorCaravana
}

// actorFor is who acts (plan D4): the person behind the request, through a token when it came with one;
// with no one, the app's webhook at the webhook door, the CLI at the host door, else Caravana.
func actorFor(p *Principal, door audit.Door, res audit.Resource) audit.Actor {
	if p != nil {
		a := audit.Actor{Kind: audit.ActorPerson, Email: p.Email, PersonID: p.PersonID}
		if p.TokenName != "" || p.Scope != nil {
			a.Kind, a.Token = audit.ActorToken, p.TokenName
			if p.Scope != nil {
				a.Kind = audit.ActorDeployToken
			}
		}
		return a
	}
	switch door {
	case audit.DoorWebhook:
		a := audit.Actor{Kind: audit.ActorWebhook}
		if res.Kind == audit.KindApp {
			a.Token = res.ID
		}
		return a
	case audit.DoorHost:
		return audit.Actor{Kind: audit.ActorHost}
	}
	return audit.Actor{Kind: audit.ActorCaravana}
}

// AnonymousActor is the actor of a failed sign-in: nobody, with the typed email only when it reads as one
// plain address of at most 254 bytes (so a password typed in the wrong box is never logged).
func AnonymousActor(typed string) audit.Actor {
	a := audit.Actor{Kind: audit.ActorAnonymous}
	if len(typed) <= 254 {
		if parsed, err := mail.ParseAddress(typed); err == nil && parsed.Address == typed {
			a.Email = typed
		}
	}
	return a
}

// The limits that keep one entry small whatever a request carried.
const (
	maxAuditText = 500
	maxAuditName = 200
)

func clipEntry(e *audit.Entry) {
	e.Actor.Email = clip(e.Actor.Email, 254)
	e.Actor.Token = clip(e.Actor.Token, maxAuditName)
	e.Actor.PersonID = clip(e.Actor.PersonID, 64)
	e.Address = clip(e.Address, 64)
	e.Operation = clip(e.Operation, 200)
	e.Resource.ID = clip(e.Resource.ID, maxAuditText)
	e.Resource.Name = clip(e.Resource.Name, maxAuditName)
	e.Resource.Project = clip(e.Resource.Project, maxAuditName)
	e.Resource.Environment = clip(e.Resource.Environment, maxAuditName)
	if e.Record != nil {
		e.Record.ID = clip(e.Record.ID, maxAuditText)
	}
}

// clip cuts s to n bytes on a character boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
