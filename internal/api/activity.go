package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
)

// activityEntryJSON is one entry of the feed (schema ActivityEntry). The
// message is Caravana's own sentence: it never holds a command's output or
// arguments, or a secret.
type activityEntryJSON struct {
	ID      string             `json:"id"`
	Time    time.Time          `json:"time"`
	Kind    string             `json:"kind"`
	Status  string             `json:"status"`
	Message string             `json:"message"`
	Parts   []activityPartJSON `json:"parts"`
	App     string             `json:"app,omitempty"`
	Place   *activityPlaceJSON `json:"place,omitempty"`
}

// activityPartJSON is a piece of the message (schema ActivityPart): the names in it
// (program, app, service, schedule, version) have mono set.
type activityPartJSON struct {
	Text string `json:"text"`
	Mono bool   `json:"mono"`
}

type activityPlaceJSON struct {
	Project     string `json:"project"`
	Environment string `json:"environment"`
}

func toActivityEntryJSON(e service.ActivityEntry) activityEntryJSON {
	out := activityEntryJSON{ID: e.ID, Time: e.Time.UTC(), Kind: e.Kind, Status: e.Status, Message: e.Sentence, App: e.AppName,
		Parts: make([]activityPartJSON, 0, len(e.Parts))}
	for _, p := range e.Parts {
		out.Parts = append(out.Parts, activityPartJSON{Text: p.Text, Mono: p.Mono})
	}
	if e.Place.Project != "" {
		out.Place = &activityPlaceJSON{Project: e.Place.Project, Environment: e.Place.Environment}
	}
	return out
}

// listActivity answers GET /v1/activity?limit=&before=&server=: the newest
// entries of what happened on the host and to its apps, from Caravana's
// records and the node's own events.
func (s *server) listActivity(w http.ResponseWriter, r *http.Request) error {
	q := service.ActivityQuery{Limit: service.DefaultActivityLimit}
	v := r.URL.Query()
	if raw := v.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > service.MaxActivityLimit {
			return invalidActivityField("limit", "must be a whole number from 1 to 50")
		}
		q.Limit = n
	}
	if raw := v.Get("before"); raw != "" {
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return invalidActivityField("before", "must be a time like 2026-10-30T13:58:04Z")
		}
		q.Before = t.UTC()
	}
	if raw := v.Get("server"); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return invalidActivityField("server", "must be true or false")
		}
		q.Server = b
	}
	entries, err := s.svc.Activity(r.Context(), q)
	if err != nil {
		return err
	}
	out := make([]activityEntryJSON, 0, len(entries))
	for _, e := range entries {
		out = append(out, toActivityEntryJSON(e))
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, struct {
		Entries []activityEntryJSON `json:"entries"`
	}{out})
	return nil
}

func invalidActivityField(field, message string) *Error {
	return &Error{Status: http.StatusUnprocessableEntity, Code: codeInvalidField, Field: field, Message: message}
}
