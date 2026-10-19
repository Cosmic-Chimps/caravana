package service

import (
	"context"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/notify"
)

// originKey is the context key of WithOrigin. A type of our own keeps other
// packages' keys from ever colliding with it.
type originKey struct{}

// WithOrigin returns a context that says where the deploys made under it came
// from (feature 020: a CI run's commit, branch, person and run). Whatever
// deploy the call starts (a deploy, a Compose deploy, a promotion, the webhook's
// redeploy) records it on its deployment. It travels in the context, like a
// request ID would, because it is about the request and not about the app, and
// every deploy entry point would otherwise need one more argument.
//
// o should have been checked with Validate; a zero origin changes nothing.
func WithOrigin(ctx context.Context, o app.Origin) context.Context {
	o = o.Normalized()
	if o.IsZero() {
		return ctx
	}
	return context.WithValue(ctx, originKey{}, o)
}

// originFrom is the origin WithOrigin put in ctx, or nil.
func originFrom(ctx context.Context) *app.Origin {
	if o, ok := ctx.Value(originKey{}).(app.Origin); ok {
		return &o
	}
	return nil
}

// notifyOrigin is o as a notification event carries it, or nil for none.
func notifyOrigin(o *app.Origin) *notify.Origin {
	if o == nil {
		return nil
	}
	return &notify.Origin{Commit: o.Commit, Ref: o.Ref, Actor: o.Actor, Run: o.Run, Repository: o.Repository}
}
