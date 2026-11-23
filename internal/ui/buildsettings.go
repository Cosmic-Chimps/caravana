package ui

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// Settings › Builds (feature 050, plan D8): the limits of the builds of the whole installation. A
// change reaches the next build; one that is going keeps what it started with. The ranges are the
// service's (service.SetBuildSettings), which is the one judge: the form only reads whole numbers.

// buildLimit is one input of the page: the posted name, what it is called, its unit and its range.
type buildLimit struct {
	Name, Label, Suffix, Hint string
}

// buildLimits are the five limits, in the order they show.
var buildLimits = []buildLimit{
	{"memoryMB", "Memory", "MB", "What each build reserves, 512 to 16384. A build that needs more is stopped with a message that says so."},
	{"cpuMHz", "CPU", "MHz", "What each build reserves, 500 to 32000."},
	{"timeoutMinutes", "Timeout", "minutes", "A build that takes longer is stopped and fails its deployment, 5 to 180."},
	{"concurrency", "Builds at once", "", "How many builds run at the same time in the whole installation, 1 to 4. The others wait, queued."},
	{"cacheMB", "Build cache", "MB", "The size an app's cache may grow to before it is emptied, 0 to 102400. 0 means no cache."},
}

// buildsData is the Builds page.
type buildsData struct {
	// Values holds each input's text by name; Errors the message of each refused one.
	Values map[string]string
	Errors map[string]string
	// Notice is what a saved form says.
	Notice sentence
	// Inputs are the five rows, ready to draw.
	Inputs []buildInput
}

// buildInput is one row of the form.
type buildInput struct {
	buildLimit
	ID, Value, Error string
}

// buildsSection fills in the page: the saved limits, or what a refused post typed.
func (s *server) buildsSection(ctx context.Context, form buildsData) (buildsData, error) {
	if form.Values == nil {
		set, err := s.svc.BuildSettings(ctx)
		if err != nil {
			return form, err
		}
		form.Values = map[string]string{"memoryMB": strconv.Itoa(set.MemoryMB), "cpuMHz": strconv.Itoa(set.CPUMHz),
			"timeoutMinutes": strconv.Itoa(set.TimeoutMinutes), "concurrency": strconv.Itoa(set.Concurrency), "cacheMB": strconv.Itoa(set.CacheMB)}
	}
	form.Inputs = nil
	for _, l := range buildLimits {
		form.Inputs = append(form.Inputs, buildInput{buildLimit: l, ID: "builds-" + l.Name, Value: form.Values[l.Name], Error: form.Errors[l.Name]})
	}
	return form, nil
}

// showBuilds answers GET /settings/builds.
func (s *server) showBuilds(w http.ResponseWriter, r *http.Request, session string) {
	data := settingsData{}
	if r.URL.Query().Get("saved") == "1" {
		data.Builds.Notice = plainS("Saved. The next build uses these limits; one that is going keeps its own.")
	}
	s.renderSettings(w, r, entryBuilds, data)
}

// saveBuilds answers POST /settings/builds. A value that isn't a whole number, or that the service
// refuses, draws the page again at 422 with what was typed and the message at its input.
func (s *server) saveBuilds(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	form := buildsData{Values: map[string]string{}, Errors: map[string]string{}}
	nums := map[string]int{}
	for _, l := range buildLimits {
		text := strings.TrimSpace(r.PostForm.Get(l.Name))
		form.Values[l.Name] = text
		n, err := strconv.Atoi(text)
		if err != nil {
			form.Errors[l.Name] = "Enter a whole number."
			continue
		}
		nums[l.Name] = n
	}
	if len(form.Errors) == 0 {
		_, err := s.svc.SetBuildSettings(r.Context(), service.BuildSettings{MemoryMB: nums["memoryMB"], CPUMHz: nums["cpuMHz"],
			TimeoutMinutes: nums["timeoutMinutes"], Concurrency: nums["concurrency"], CacheMB: nums["cacheMB"]})
		var fieldErr *app.FieldError
		switch {
		case err == nil:
			http.Redirect(w, r, "/settings/builds?saved=1", http.StatusSeeOther)
			return
		case errors.As(err, &fieldErr):
			form.Errors[fieldErr.Field] = capitalize(fieldErr.Message) + "."
		default:
			s.serverError(w, r, "save the build settings", err)
			return
		}
	}
	s.renderSettings(w, r, entryBuilds, settingsData{Builds: form})
}
