package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// originHeaders are the headers a deploy may carry its origin in (feature 020),
// in the order of app.Origin's fields.
var originHeaders = [...]string{
	app.HeaderOriginCommit, app.HeaderOriginRef, app.HeaderOriginActor,
	app.HeaderOriginRun, app.HeaderOriginRepository,
}

// originFromHeaders reads the optional Caravana-Origin-* headers of a deploy.
// None is required; an empty value is the same as an absent one. A header that
// breaks its rule (app.Origin.Validate), or is sent more than once, is 422
// invalid_field naming it, and nothing is deployed.
func originFromHeaders(r *http.Request) (app.Origin, error) {
	var parts [len(originHeaders)]string
	for i, name := range originHeaders {
		values := r.Header.Values(name)
		if len(values) > 1 {
			return app.Origin{}, invalidHeader(name, "send it once")
		}
		if len(values) == 1 {
			parts[i] = values[0]
		}
	}
	o := app.Origin{Commit: parts[0], Ref: parts[1], Actor: parts[2], Run: parts[3], Repository: parts[4]}.Normalized()
	if err := o.Validate(); err != nil {
		var fe *app.FieldError
		if errors.As(err, &fe) {
			return app.Origin{}, invalidHeader(fe.Field, fe.Message)
		}
		return app.Origin{}, err
	}
	return o, nil
}

// invalidHeader is the 422 for a header that breaks its rule.
func invalidHeader(header, message string) *Error {
	return &Error{Status: http.StatusUnprocessableEntity, Code: codeInvalidField, Field: header,
		Message: strings.TrimSpace(message)}
}

// withOrigin returns r's context with the origin its headers give, so the deploy
// the handler starts records it. It is the first thing a deploy handler does:
// a bad header refuses the request before any work.
func withOrigin(r *http.Request) (context.Context, error) {
	o, err := originFromHeaders(r)
	if err != nil {
		return nil, err
	}
	return service.WithOrigin(r.Context(), o), nil
}

// originJSON is where a deploy came from (schema Origin).
type originJSON struct {
	Commit     string `json:"commit,omitempty"`
	Ref        string `json:"ref,omitempty"`
	Actor      string `json:"actor,omitempty"`
	Run        string `json:"run,omitempty"`
	Repository string `json:"repository,omitempty"`
}

func toOriginJSON(o *app.Origin) *originJSON {
	if o == nil || o.IsZero() {
		return nil
	}
	return &originJSON{Commit: o.Commit, Ref: o.Ref, Actor: o.Actor, Run: o.Run, Repository: o.Repository}
}
