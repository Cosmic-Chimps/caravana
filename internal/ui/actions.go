package ui

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// App actions in the console (feature 020.5): Restart, Stop and Start in the
// app's header, "Run a command" on the Running tab, and the list of recent
// actions. The service decides everything; these pages say it in words.
//
// A command's output is shown on the one answer to the form that ran it, and
// nowhere else: not in a redirect, a notice, a live event, a log line or a
// page that is fetched later. The answer is sent no-store.

// ---- Reading a command line ---------------------------------------------------------

// splitCommand turns the line typed in the form into the program and its
// arguments, the way a shell reads words, and no further: words are separated
// by spaces; 'single quotes' keep everything inside literally; "double quotes"
// keep everything but \" and \\, which are the quote and the backslash; outside
// quotes a backslash makes the next character an ordinary one; quoted and
// unquoted pieces next to each other are one word, and "" is an empty word.
// Pipes, redirects, variables and globs are not understood: the command runs
// without a shell, so they reach the program as plain text (use sh -c '...').
// The error says what is wrong in lower case and without a full stop; the page
// capitalizes it.
func splitCommand(line string) ([]string, error) {
	var words []string
	var word strings.Builder
	inWord := false // a word has started, even an empty one ("")
	flush := func() {
		if inWord {
			words = append(words, word.String())
			word.Reset()
			inWord = false
		}
	}

	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		switch r := rs[i]; r {
		case ' ', '\t', '\r', '\n':
			flush()
		case '\\':
			if i+1 == len(rs) {
				return nil, errors.New("the line ends with a backslash: add the character it escapes, or remove it")
			}
			i++
			word.WriteRune(rs[i])
			inWord = true
		case '\'':
			end := indexRune(rs, i+1, '\'')
			if end < 0 {
				return nil, errors.New("a ' quote isn't closed: add the closing '")
			}
			word.WriteString(string(rs[i+1 : end]))
			inWord, i = true, end
		case '"':
			inWord = true
			closed := false
			for i++; i < len(rs); i++ {
				c := rs[i]
				if c == '"' {
					closed = true
					break
				}
				if c == '\\' && i+1 < len(rs) && (rs[i+1] == '"' || rs[i+1] == '\\') {
					i++
					c = rs[i]
				}
				word.WriteRune(c)
			}
			if !closed {
				return nil, errors.New(`a " quote isn't closed: add the closing "`)
			}
		default:
			word.WriteRune(r)
			inWord = true
		}
	}
	flush()
	if len(words) == 0 {
		return nil, errors.New("type a command: the program, then its arguments")
	}
	return words, nil
}

// capitalize upper-cases the first letter of s.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

// indexRune is the index of the first r in rs at or after from, or -1.
func indexRune(rs []rune, from int, r rune) int {
	for i := from; i < len(rs); i++ {
		if rs[i] == r {
			return i
		}
	}
	return -1
}

// ---- What the pages draw -------------------------------------------------------------

// headerActions are the buttons of the app header: Restart and Stop for a
// running app, Start for a stopped one. An app with nothing deployed has none.
type headerActions struct {
	Stopped  bool
	Restart  string // where Restart posts
	StopHref string // the confirmation page
	Start    string // where Start posts
}

// actionsOf says what the header offers an app, nil when nothing is deployed.
func actionsOf(sum service.AppSummary) *headerActions {
	if !sum.App.Defined() || sum.LastDeployment == nil {
		return nil
	}
	base := "/apps/" + url.PathEscape(sum.App.ID)
	if sum.StoppedAt != nil {
		return &headerActions{Stopped: true, Start: base + "/start"}
	}
	return &headerActions{Restart: base + "/restart", StopHref: base + "/stop"}
}

// runForm is one copy's "Run a command" form. Value and Error are what was
// typed and what is wrong with it when a post was refused.
type runForm struct {
	Action  string
	ID      string // the input's element ID
	CopyID  string
	Short   string
	Service string
	Value   string
	Error   sentence
}

// runResult is the answer to one command: what it printed and how it ended.
// It is drawn on that answer only.
type runResult struct {
	Command   string
	Short     string
	CopyID    string
	Service   string
	Outcome   sentence // "Exit code 0."; "It didn't finish in time and was stopped."
	Kind      string   // healthy for exit 0, warning for another exit code, failed for the rest
	Stdout    string
	Stderr    string
	StdoutCut bool
	StderrCut bool
	// Started is false when the program couldn't be started: Stderr then holds
	// Nomad's reason, and Stdout is empty.
	Started bool
}

// runSection is the Running tab's "Run a command" part: a form per live copy,
// and, on the answer to a post, its result or why it was refused. It sits
// outside the live regions, so a refresh never replaces what is being typed or
// the result being read.
type runSection struct {
	Forms   []runForm
	Result  *runResult
	Refusal sentence
}

// Show says whether there is anything to draw.
func (r *runSection) Show() bool {
	return r != nil && (len(r.Forms) > 0 || r.Result != nil || len(r.Refusal) > 0)
}

// actionRow is one entry of the recent actions: what was done, how it ended,
// and who asked. It never has a command's output or arguments.
type actionRow struct {
	Label   string // "Restart", "Stop", "Start", "Run"
	Program string // a run's first word; "" for the others
	Copy    string // a run's copy, short
	CopyID  string
	Status  statusView
	Error   string // why it failed, in Caravana's words
	Trigger string // "from the console", "through the API"
	When    when
}

// actionsRegion is what the "actions" region draws: the newest actions, or
// Gone when the app was deleted.
type actionsRegion struct {
	Rows        []actionRow
	RefreshHref string
	Gone        sentence
}

// recentActions is how many actions the Running tab lists.
const recentActions = 10

// loadActions reads the app's newest actions. An unknown app is Gone.
func (s *server) loadActions(r *http.Request, id string) (actionsRegion, error) {
	region := actionsRegion{RefreshHref: "/parts/apps/" + id + "/actions"}
	recs, err := s.svc.ListActions(r.Context(), id, recentActions)
	if errors.Is(err, store.ErrNotFound) {
		region.Gone = sentence{mono(id), plain(" was deleted.")}
		return region, nil
	}
	if err != nil {
		return actionsRegion{}, err
	}
	now := s.now()
	for _, a := range recs {
		region.Rows = append(region.Rows, actionRowOf(now, a))
	}
	return region, nil
}

// actionRowOf turns a record into a row.
func actionRowOf(now time.Time, a store.ActionRecord) actionRow {
	row := actionRow{Program: a.Program, CopyID: a.CopyID, Copy: shortID(a.CopyID), Error: a.Error,
		Trigger: triggerWords(app.Trigger(a.Trigger)), When: whenOf(now, &a.StartedAt, "")}
	switch a.Action {
	case store.ActionStop:
		row.Label = "Stop"
	case store.ActionStart:
		row.Label = "Start"
	case store.ActionRestart:
		row.Label = "Restart"
	case store.ActionRun:
		row.Label = "Run"
	default:
		row.Label = a.Action
	}
	switch {
	case a.Status == store.ActionRunning:
		row.Status = statusView{"rolling", "running"}
	case a.Status == store.ActionFailed:
		row.Status = statusView{"failed", "failed"}
	case a.ExitCode != nil && *a.ExitCode != 0:
		row.Status = statusView{"warning", fmt.Sprintf("exit %d", *a.ExitCode)}
	case a.ExitCode != nil:
		row.Status = statusView{"healthy", "exit 0"}
	default:
		row.Status = statusView{"healthy", "done"}
	}
	return row
}

// partActions answers GET /parts/apps/{id}/actions: the region alone.
func (s *server) partActions(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("id")
	region, err := s.loadActions(r, id)
	if err != nil {
		s.log.Error("list actions", "app", id, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPart(w, "app", "actions-region", region)
}

// ---- Stop, Start, Restart --------------------------------------------------------------

// didNotice is the sentence after an action succeeded, for ?did=. It says
// something only while it is still true, so a reload later says nothing.
func didNotice(did string, sum service.AppSummary) sentence {
	switch did {
	case "restart":
		return sentence{plain("Restarting: its copies restart one at a time, so the app keeps answering. Recent actions shows how it goes.")}
	case "stop":
		if sum.StoppedAt != nil {
			return sentence{plain("Stopped. Nothing was deleted: Start runs it again exactly as it was.")}
		}
	case "start":
		if sum.StoppedAt == nil {
			return sentence{plain("Started. It runs the same images and settings as before it stopped.")}
		}
	}
	return nil
}

// actionRefusal says in words why an action was refused, with the status the
// API gives it. ok is false for errors the pages have no words for.
func actionRefusal(err error, verb string) (status int, text sentence, ok bool) {
	switch {
	case errors.Is(err, service.ErrAppStopped):
		if verb == "stop" {
			return http.StatusConflict, sentence{plain("The app is stopped already.")}, true
		}
		return http.StatusConflict, sentence{plain(appStoppedText)}, true
	case errors.Is(err, service.ErrAppNotStopped):
		return http.StatusConflict, sentence{plain("The app isn't stopped, so there is nothing to start.")}, true
	case errors.Is(err, service.ErrAppBusy):
		return http.StatusConflict, sentence{plain("A deploy, a volume backup or another action of this app is running. Wait for it to finish, then try again.")}, true
	case errors.Is(err, service.ErrAppNotDefined), errors.Is(err, service.ErrNoLiveDeployment):
		return http.StatusConflict, sentence{plain("Nothing is deployed yet, so there is nothing to " + verb + ". Deploy the app first.")}, true
	case errors.Is(err, service.ErrNoRunningCopy):
		return http.StatusConflict, sentence{plain("No copy of the app is running, so there is nothing to restart. Deploy it, or start it if it is stopped.")}, true
	case errors.Is(err, deploy.ErrNomadUnavailable), errors.Is(err, deploy.ErrNomadRejected):
		return http.StatusBadGateway, sentence{plain("Nomad couldn't be reached or refused it, so the app may not have changed. The attempt is in Recent actions.")}, true
	}
	return 0, nil, false
}

// appStoppedText is the refusal of anything that would deploy a stopped app.
const appStoppedText = "The app is stopped: start it first."

// stopView is the Stop confirmation page.
type stopView struct {
	AppID, Name string
	Headline    sentence
	Copies      string // "2 copies"
	Backups     bool   // a service has a backup policy
	Action      string
	Back        string
	// Refusal replaces the button: why it can't be stopped. Start is set when
	// the reason is that the app is stopped already.
	Refusal sentence
	Start   string
}

// stopPage is the whole Stop page.
type stopPage struct {
	pageData
	stopView
}

// showStop answers GET /apps/{id}/stop: what stopping the app does, and the
// button that does it.
func (s *server) showStop(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("id")
	sum, err := s.svc.AppSummary(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.appNotFound(w, r, id)
		return
	}
	if err != nil {
		s.serverError(w, "read app", err)
		return
	}
	name := cmpName(sum)
	v := stopView{AppID: id, Name: name, Action: "/apps/" + url.PathEscape(id) + "/stop",
		Back: "/apps/" + url.PathEscape(id), Copies: copyCount(sum.Status.WantedCopies)}
	v.Headline = sentence{plain("Stop "), mono(name), plain("?")}
	for _, svc := range sum.App.Services {
		v.Backups = v.Backups || svc.Backup != nil
	}
	status := http.StatusOK
	switch {
	case sum.StoppedAt != nil:
		status = http.StatusConflict
		v.Refusal = sentence{plain("This app is stopped already.")}
		v.Start = "/apps/" + url.PathEscape(id) + "/start"
	case actionsOf(sum) == nil:
		status = http.StatusConflict
		v.Refusal = sentence{plain("Nothing is deployed yet, so there is nothing to stop.")}
	}
	head, err := s.shellPage(r, "Stop "+name, "apps", crumb{Label: "Apps", Href: "/apps"},
		crumb{Label: name, Href: v.Back, Mono: true}, crumb{Label: "Stop"})
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	s.render(w, status, "stop", stopPage{pageData: head, stopView: v})
}

// stopApp answers POST /apps/{id}/stop: the app stops, and the browser goes
// back to its page, which says so.
func (s *server) stopApp(w http.ResponseWriter, r *http.Request, session string) {
	s.doAction(w, r, "stop", func(id string) error {
		_, err := s.svc.StopApp(r.Context(), id, app.TriggerConsole)
		return err
	})
}

// startApp answers POST /apps/{id}/start.
func (s *server) startApp(w http.ResponseWriter, r *http.Request, session string) {
	s.doAction(w, r, "start", func(id string) error {
		_, err := s.svc.StartApp(r.Context(), id, app.TriggerConsole)
		return err
	})
}

// restartApp answers POST /apps/{id}/restart: the restart goes on in the
// background; the browser goes back to the app's page.
func (s *server) restartApp(w http.ResponseWriter, r *http.Request, session string) {
	s.doAction(w, r, "restart", func(id string) error {
		_, err := s.svc.RestartApp(r.Context(), id, app.TriggerConsole)
		return err
	})
}

// doAction runs one of the three actions and answers: 303 to the app's page
// with ?did= on success; on a refusal, the Running tab again (never a
// redirect) with the reason and the status the API gives it.
func (s *server) doAction(w http.ResponseWriter, r *http.Request, verb string, do func(id string) error) {
	id := r.PathValue("id")
	err := do(id)
	if err == nil {
		http.Redirect(w, r, "/apps/"+url.PathEscape(id)+"?did="+verb, http.StatusSeeOther)
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		s.appNotFound(w, r, id)
		return
	}
	status, text, ok := actionRefusal(err, verb)
	if !ok {
		s.serverError(w, verb+" the app", err)
		return
	}
	if status == http.StatusBadGateway {
		s.log.Warn("can't "+verb+" the app", "app", id, "error", err)
	}
	s.runningPage(w, r, status, id, runningExtras{Refusal: text})
}

// ---- Run a command -------------------------------------------------------------------

// runCommand answers POST /apps/{id}/copies/{copyId}/run: it runs the typed
// line in that copy and answers 200 with the Running tab and, above the copies,
// the result. That answer is the only place the output ever shows: it is sent
// no-store, nothing here logs or keeps it, and a later page has none of it. A
// refusal draws the tab again with the reason by the copy's form.
func (s *server) runCommand(w http.ResponseWriter, r *http.Request, session string) {
	w.Header().Set("Cache-Control", "no-store")
	id, copyID := r.PathValue("id"), r.PathValue("copyId")
	if !s.readForm(w, r) {
		return
	}
	line := r.PostForm.Get("command")
	typed := &typedCommand{CopyID: copyID, Line: line}

	words, err := splitCommand(line)
	if err != nil {
		typed.Error = sentence{plain(capitalize(err.Error()) + ".")}
		s.runningPage(w, r, http.StatusUnprocessableEntity, id, runningExtras{Typed: typed})
		return
	}
	res, err := s.svc.RunCommand(r.Context(), id, copyID, words, app.TriggerConsole)
	if err == nil {
		s.runningPage(w, r, http.StatusOK, id, runningExtras{Result: resultOf(line, copyID, res, s.copyService(r, id, copyID))})
		return
	}

	var fe *app.FieldError
	switch {
	case errors.Is(err, store.ErrNotFound):
		if _, getErr := s.svc.GetApp(r.Context(), id); errors.Is(getErr, store.ErrNotFound) {
			s.appNotFound(w, r, id)
			return
		}
		s.runningPage(w, r, http.StatusNotFound, id, runningExtras{
			Refusal: sentence{plain("That copy isn't part of the app any more. The copies below are the ones it has now.")}})
	case errors.As(err, &fe):
		typed.Error = sentence{plain("Can't run that: " + fe.Message + ".")}
		s.runningPage(w, r, http.StatusUnprocessableEntity, id, runningExtras{Typed: typed})
	case errors.Is(err, service.ErrCopyNotRunning):
		s.runningPage(w, r, http.StatusConflict, id, runningExtras{
			Refusal: sentence{plain("That copy isn't running any more, so nothing was run. The copies below are the ones it has now.")}})
	case errors.Is(err, deploy.ErrNomadUnavailable), errors.Is(err, deploy.ErrNomadRejected):
		s.log.Warn("can't run a command", "app", id, "copy", copyID, "error", err)
		text := "Nomad couldn't be reached or refused the command."
		if strings.Contains(err.Error(), "alloc-exec") {
			text = "Nomad refused the command: Caravana's token can't run commands yet. Run the installer again on the host to update its policy."
		}
		s.runningPage(w, r, http.StatusBadGateway, id, runningExtras{Refusal: sentence{plain(text)}, Typed: typed})
	default:
		s.serverError(w, "run a command", err)
	}
}

// typedCommand is a refused post's line, drawn back in the copy's form.
type typedCommand struct {
	CopyID string
	Line   string
	Error  sentence
}

// copyService is the service of an app's copy, "" when it can't be found. It
// only labels the result, so a failure to read it says nothing.
func (s *server) copyService(r *http.Request, appID, copyID string) string {
	copies, err := s.svc.Copies(r.Context(), appID)
	if err != nil {
		return ""
	}
	for _, c := range copies {
		if c.ID == copyID {
			return c.Service
		}
	}
	return ""
}

// resultOf turns what the service answered into what the page draws.
func resultOf(line, copyID string, res service.CommandResult, svcName string) *runResult {
	out := &runResult{Command: line, CopyID: copyID, Short: shortID(copyID), Service: svcName,
		Stdout: res.Stdout, Stderr: res.Stderr, StdoutCut: res.StdoutTruncated, StderrCut: res.StderrTruncated,
		Started: !res.FailedToStart}
	switch {
	case res.FailedToStart:
		out.Kind = "failed"
		out.Outcome = sentence{plain("It couldn't be started. This is what Nomad said:")}
	case res.TimedOut:
		out.Kind = "failed"
		out.Outcome = sentence{plain("It didn't finish in time, so it was stopped. What it printed until then is below.")}
	case res.ExitCode != nil && *res.ExitCode == 0:
		out.Kind = "healthy"
		out.Outcome = sentence{plain("It finished with exit code "), mono("0"), plain(".")}
	case res.ExitCode != nil:
		out.Kind = "warning"
		out.Outcome = sentence{plain("It finished with exit code "), mono(fmt.Sprint(*res.ExitCode)), plain(".")}
	default:
		out.Kind = "failed"
		out.Outcome = sentence{plain("It ended without an exit code.")}
	}
	return out
}
