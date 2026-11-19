package ui

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// Server labels in the console (feature 042): the Labels row of the server page and its form. A label is
// a name and a value ("ssd=true"); the form is one text input holding them all, "ssd=true, region=eu",
// where a bare word means "=true". The checks of names and values are the service's (a field error on
// labels or labels.<name>), so the console and the API refuse the same things in the same words.

// srvLabels is the Labels row: the labels as chips, the form's input with what it holds, and what to
// say about it. Error is drawn above the form (never inside the part the live refresh keeps), Note is
// the quiet line that says Nomad hasn't taken the labels yet.
type srvLabels struct {
	Chips  []string
	Text   string
	Error  sentence
	Note   sentence
	Action string
}

// labelChips lists labels as "name=value", in name order.
func labelChips(labels map[string]string) []string {
	var out []string
	for _, name := range sortedKeys(labels) {
		out = append(out, name+"="+labels[name])
	}
	return out
}

// labelsText is the text of the input for these labels: "ssd=true, region=eu" in name order.
func labelsText(labels map[string]string) string { return strings.Join(labelChips(labels), ", ") }

// sortedKeys is the keys of m in order.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// parsePairs reads "ssd=true, region=eu" into labels: pairs are separated by commas or line breaks, a
// bare word means "=true". A name set twice or a pair with no name is an error in the owner's words. The
// validity of a name or a value is the service's to judge.
func parsePairs(text string) (map[string]string, error) {
	labels := map[string]string{}
	for _, item := range splitItems(text) {
		name, value, hasValue := strings.Cut(item, "=")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		switch {
		case name == "":
			return nil, fmt.Errorf("%q has no name before the \"=\"", item)
		case !hasValue:
			value = "true"
		}
		if _, dup := labels[name]; dup {
			return nil, fmt.Errorf("%s is written twice", name)
		}
		labels[name] = value
	}
	return labels, nil
}

// splitItems splits a list typed on one line or several: commas and line breaks separate, blanks go.
func splitItems(text string) []string {
	var out []string
	for _, item := range strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' }) {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// labelsFormOf builds the Labels row of a server. typed, when not nil, is what the owner typed in a
// refused post: it replaces the stored text, with the error beside it.
func labelsFormOf(v service.ServerView, typed *string, problem sentence) *srvLabels {
	l := &srvLabels{Chips: labelChips(v.Labels), Text: labelsText(v.Labels), Error: problem,
		Action: serverHref(v.Name) + "/labels"}
	if typed != nil {
		l.Text = *typed
	}
	if !v.LabelsApplied && v.State != "down" && v.State != "disconnected" {
		l.Note = plainS("Nomad hasn't taken them yet. Caravana keeps trying, and a deploy that needs a label waits for it.")
	}
	return l
}

// saveServerLabels answers POST /ops/servers/{name}/labels: the labels typed replace the server's. Success
// goes back to the server page (303); a refusal draws the page with the sentence at the form.
func (s *server) saveServerLabels(w http.ResponseWriter, r *http.Request, session string) {
	name := r.PathValue("name")
	if !s.readForm(w, r) {
		return
	}
	text := r.PostForm.Get("labels")
	refuse := func(status int, problem sentence) {
		s.drawOpsServerWith(w, r, name, status, srvExtras{LabelsText: &text, LabelsError: problem})
	}
	labels, err := parsePairs(text)
	if err != nil {
		refuse(http.StatusUnprocessableEntity, plainS(refusalWords(err)))
		return
	}
	_, err = s.svc.SetServerLabels(r.Context(), name, labels)
	var fieldErr *app.FieldError
	switch {
	case err == nil:
		http.Redirect(w, r, serverHref(name), http.StatusSeeOther)
	case errors.As(err, &fieldErr):
		refuse(http.StatusUnprocessableEntity, labelFieldSentence(fieldErr))
	case errors.Is(err, service.ErrServerNotFound):
		s.drawOpsServerWith(w, r, name, http.StatusNotFound, srvExtras{})
	case errors.Is(err, service.ErrServerNotReady):
		refuse(http.StatusConflict, plainS(serverRefusalWords(err)))
	case errors.Is(err, deploy.ErrNomadUnavailable):
		refuse(http.StatusBadGateway, plainS(refusalWords(err)))
	default:
		s.serverError(w, r, "set server labels", err)
	}
}

// labelFieldSentence is a label field error as a sentence at the form: a value's error names its label.
func labelFieldSentence(e *app.FieldError) sentence {
	if label, ok := strings.CutPrefix(e.Field, "labels."); ok {
		return sentence{mono(label), plain(": " + e.Message)}
	}
	return plainS(refusalWords(errors.New(e.Message)))
}
