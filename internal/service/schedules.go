package service

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/cronwords"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Schedules (feature 022): a command run on a timer in a fresh copy of one
// service of an app, from the app's live version, with no ports and no
// traffic. This file is the schedules themselves: their checks, their reads
// and the owner's changes. schedulerun.go starts and stops runs,
// scheduleclock.go is the timer and schedulewatch.go follows each run to its
// end and records it.
//
// Every time is UTC. A run's output is kept with the run (the last 64 KiB of
// each stream, secret values masked) and is only ever returned by
// GetScheduleRunOutput: never in a Schedule, a ScheduleRun, an error, a log
// line or a notice.

// Errors the API maps to their own codes.
var (
	ErrScheduleNotFound = errors.New("no such schedule")
	ErrRunNotFound      = errors.New("no such run of this schedule")
	ErrScheduleExists   = errors.New("the app already has a schedule with this name")
	// ErrScheduleBuiltin is a change to a built-in schedule: a service's
	// backup, which is changed in the app's Backups.
	ErrScheduleBuiltin = errors.New("this schedule is a service's backup: change it in the app's Backups")
	// ErrRunGoing is Run now while a run is going and the schedule skips
	// overlapping runs, or while 3 runs are going.
	ErrRunGoing = errors.New("a run of this schedule is going")
	// ErrRunNotRunning is Stop this run on a run that has ended.
	ErrRunNotRunning = errors.New("the run has ended")
	// ErrOutputNotKept is a read of output that isn't kept: the run never
	// ran, its output couldn't be read, or it is a backup's run.
	ErrOutputNotKept = errors.New("this run's output isn't kept")
)

// The choices a schedule has (contracts/api.md).
var (
	stopAfterChoices = []int{300, 1800, 3600, 7200}
	memoryChoices    = []int{256, 512, 1024, 2048}
)

const (
	defaultStopAfterSeconds = 1800
	maxScheduleNameLen      = 31
	// maxOpenRuns is how many runs of one schedule may go at once when it
	// allows overlap.
	maxOpenRuns = 3
	// keptRuns is how many runs of a schedule are kept.
	keptRuns = 100
	// usualRuns is how many of the newest runs the usual duration is the
	// median of (the ok ones among them).
	usualRuns = 14
	// defaultRunsLimit and maxRunsLimit bound ListScheduleRuns.
	defaultRunsLimit = 14
	maxRunsLimit     = 100
)

// builtinPrefix starts the ID of a built-in schedule: backup:<app>:<service>.
const builtinPrefix = "backup:"

// The triggers of a run.
const (
	RunTriggerTimer = "timer"
	RunTriggerOwner = "owner"
)

// The reasons a run is stopped, written before its job is (research R3).
const (
	stopOwner           = "owner"
	stopLimit           = "limit"
	stopAppStopped      = "app_stopped"
	stopScheduleRemoved = "schedule_removed"
)

// NewSchedule is what an owner sends to create a schedule, or, whole, to
// change one.
type NewSchedule struct {
	// App is the app's ID.
	App string `json:"app"`
	// Service is the service whose image, env and secrets a run uses; empty
	// means the app's first.
	Service string `json:"service,omitempty"`
	// Name is a DNS label of at most 31 characters, unique in the app.
	Name string `json:"name"`
	// Command is the program, then its arguments; no shell is implied.
	Command []string `json:"command"`
	// Cron is five fields, read as UTC.
	Cron string `json:"cron"`
	// StopAfterSeconds is 300, 1800, 3600 or 7200; 0 means 1800.
	StopAfterSeconds int `json:"stopAfterSeconds,omitempty"`
	// MemoryMB is 256, 512, 1024 or 2048; 0 means the service's reservation
	// rounded up to one of those.
	MemoryMB int `json:"memoryMB,omitempty"`
	// SkipIfRunning skips a slot while a run is going; nil means true. False
	// lets up to 3 runs go at once.
	SkipIfRunning *bool `json:"skipIfRunning,omitempty"`
}

// Schedule is a schedule as the API and the console show it: what was stored,
// and what is derived on every read. A built-in schedule (a service's backup)
// has Builtin set, an ID of backup:<app>:<service>, no command and no limits.
type Schedule struct {
	ID               string   `json:"id"`
	App              string   `json:"app"`
	Service          string   `json:"service"`
	Name             string   `json:"name"`
	Command          []string `json:"command"`
	Cron             string   `json:"cron"`
	StopAfterSeconds int      `json:"stopAfterSeconds"`
	MemoryMB         int      `json:"memoryMB"`
	SkipIfRunning    bool     `json:"skipIfRunning"`
	// Words is the cron line in words, "Every day at 03:00 UTC".
	Words    string     `json:"words"`
	Paused   bool       `json:"paused"`
	PausedAt *time.Time `json:"pausedAt"`
	Builtin  bool       `json:"builtin"`
	// NextRunAt is nil while paused, and for a built-in whose app is stopped.
	NextRunAt *time.Time `json:"nextRunAt"`
	// LastRun is the newest run that wasn't skipped; nil for none.
	LastRun *ScheduleRun `json:"lastRun"`
	// RunningNow is how many runs are going.
	RunningNow int `json:"runningNow"`
	// UsualSeconds is the median duration of the ok runs among the newest 14;
	// nil when there are none.
	UsualSeconds *int   `json:"usualSeconds"`
	Project      string `json:"project"`
	Environment  string `json:"environment"`
	AppName      string `json:"appName"`

	// ArmedAt is the clock's base; not shown.
	ArmedAt time.Time `json:"-"`
	// Engine is a built-in's backup engine (postgres or volume); "" for the
	// others. Not in the API: the console draws "pg_dump" or "volumes" from it.
	Engine string `json:"-"`
}

// ScheduleRun is one run of a schedule (or one skipped slot) without its
// output, which only GetScheduleRunOutput returns.
type ScheduleRun struct {
	ID         string     `json:"id"`
	ScheduleID string     `json:"scheduleId"`
	Trigger    string     `json:"trigger"`
	SlotAt     *time.Time `json:"slotAt"`
	// Status is starting, running, ok, failed, stopped_at_limit, stopped,
	// skipped or could_not_start.
	Status   string `json:"status"`
	ExitCode *int   `json:"exitCode"`
	// Error is why, in Caravana's words; empty when ok. Never the output.
	Error         string     `json:"error"`
	StartedAt     time.Time  `json:"startedAt"`
	TaskStartedAt *time.Time `json:"taskStartedAt"`
	FinishedAt    *time.Time `json:"finishedAt"`
	// DurationSeconds is from the task's start (the run's, when the task's
	// isn't known) to the finish; nil while it goes and for a skipped slot.
	DurationSeconds *int     `json:"durationSeconds"`
	Command         []string `json:"command"`
	DeploymentID    *string  `json:"deploymentId"`
	JobVersion      *int     `json:"jobVersion"`
	OutputKept      bool     `json:"outputKept"`
	StdoutBytes     int      `json:"stdoutBytes"`
	StderrBytes     int      `json:"stderrBytes"`
	StdoutCut       bool     `json:"stdoutCut"`
	StderrCut       bool     `json:"stderrCut"`
}

// ScheduleFilter narrows ListSchedules: an app ID, a project, or neither.
type ScheduleFilter struct {
	App, Project string
}

// schedulesChanged tells open pages that a schedule or its runs changed. It
// carries the app's ID and nothing else.
func (s *Service) schedulesChanged(appID string) {
	s.publish(Change{AppID: appID, What: ChangeSchedules})
}

// --- checks ---

// checkedSchedule is a NewSchedule that passed its checks, with the defaults
// applied.
type checkedSchedule struct {
	app           store.AppRecord
	service       string
	name          string
	command       []string
	cron          string
	stopAfter     time.Duration
	memoryMB      int
	skipIfRunning bool
}

// checkSchedule applies a schedule's rules. An unknown app is
// store.ErrNotFound; everything else is a *app.FieldError naming the field.
func (s *Service) checkSchedule(ctx context.Context, in NewSchedule) (checkedSchedule, error) {
	if strings.TrimSpace(in.App) == "" {
		return checkedSchedule{}, app.NewFieldError("app", "is required")
	}
	rec, err := s.store.GetApp(ctx, in.App)
	if err != nil {
		return checkedSchedule{}, err
	}
	if !rec.App.Defined() {
		return checkedSchedule{}, app.NewFieldError("app", "the app has no definition yet: define it and deploy it first")
	}
	if err := app.ValidName("name", in.Name); err != nil {
		return checkedSchedule{}, err
	}
	if len(in.Name) > maxScheduleNameLen {
		return checkedSchedule{}, app.NewFieldError("name", "%q is longer than %d characters", in.Name, maxScheduleNameLen)
	}
	// A service's backup is listed as the built-in schedule "<service>-backup":
	// a stored schedule of that name would be a second row with the same name.
	for _, svc := range rec.App.Services {
		if svc.Backup != nil && in.Name == svc.Name+"-backup" {
			return checkedSchedule{}, app.NewFieldError("name", "%q is the built-in backup schedule of service %q; choose another name", in.Name, svc.Name)
		}
	}
	if err := checkCommand(in.Command); err != nil {
		return checkedSchedule{}, err
	}
	cron := strings.Join(strings.Fields(in.Cron), " ")
	if cron == "" {
		return checkedSchedule{}, app.NewFieldError("cron", "is required: five fields, in UTC")
	}
	if err := cronwords.Validate(cron); err != nil {
		return checkedSchedule{}, app.NewFieldError("cron", "%s", err.Error())
	}

	name := in.Service
	if name == "" {
		name = rec.App.Services[0].Name
	}
	i := slices.IndexFunc(rec.App.Services, func(svc app.Service) bool { return svc.Name == name })
	if i < 0 {
		return checkedSchedule{}, app.NewFieldError("service", "the app has no service %q", name)
	}
	svc := rec.App.Services[i]
	if len(svc.Volumes) > 0 {
		return checkedSchedule{}, app.NewFieldError("service", "the service mounts a volume; a schedule needs a service without volumes")
	}

	stopAfter := in.StopAfterSeconds
	if stopAfter == 0 {
		stopAfter = defaultStopAfterSeconds
	}
	if !slices.Contains(stopAfterChoices, stopAfter) {
		return checkedSchedule{}, app.NewFieldError("stopAfterSeconds", "must be 300, 1800, 3600 or 7200")
	}
	memory := in.MemoryMB
	if memory == 0 {
		_, reserved, _ := s.deployer.Reserved(svc.Resources)
		memory = roundedMemory(reserved)
	}
	if !slices.Contains(memoryChoices, memory) {
		return checkedSchedule{}, app.NewFieldError("memoryMB", "must be 256, 512, 1024 or 2048")
	}
	skip := true
	if in.SkipIfRunning != nil {
		skip = *in.SkipIfRunning
	}
	return checkedSchedule{app: rec, service: name, name: in.Name, command: slices.Clone(in.Command), cron: cron,
		stopAfter: time.Duration(stopAfter) * time.Second, memoryMB: memory, skipIfRunning: skip}, nil
}

// roundedMemory is a reservation rounded up to the nearest memory choice, the
// largest for one above them all.
func roundedMemory(mb int) int {
	for _, c := range memoryChoices {
		if mb <= c {
			return c
		}
	}
	return memoryChoices[len(memoryChoices)-1]
}

// --- changes ---

// CreateSchedule checks and stores a new schedule. Its clock starts now: the
// first run is the first slot after this moment. Errors: an unknown app is
// store.ErrNotFound, a name the app has is ErrScheduleExists, anything else
// the input got wrong is a *app.FieldError.
func (s *Service) CreateSchedule(ctx context.Context, in NewSchedule) (Schedule, error) {
	c, err := s.checkSchedule(ctx, in)
	if err != nil {
		return Schedule{}, err
	}
	id, err := newID("")
	if err != nil {
		return Schedule{}, err
	}
	now := s.clock.Now().UTC()
	rec, err := s.store.InsertSchedule(ctx, store.ScheduleRecord{ID: id, AppID: c.app.App.ID, Service: c.service,
		Name: c.name, Command: c.command, Cron: c.cron, StopAfter: c.stopAfter, MemoryMB: c.memoryMB,
		SkipIfRunning: c.skipIfRunning, ArmedAt: now, CreatedAt: now})
	if errors.Is(err, store.ErrAlreadyExists) {
		return Schedule{}, fmt.Errorf("schedule %q of %q: %w", c.name, c.app.App.ID, ErrScheduleExists)
	}
	if err != nil {
		return Schedule{}, err
	}
	s.schedulesChanged(rec.AppID)
	return s.scheduleOf(ctx, rec, &c.app)
}

// UpdateSchedule replaces what an owner can edit (in is the whole schedule).
// A schedule can't move to another app. A new cron line takes effect from
// now: slots the old line owed are not run. The pause and the runs stay.
// Errors are CreateSchedule's, plus ErrScheduleNotFound and
// ErrScheduleBuiltin.
func (s *Service) UpdateSchedule(ctx context.Context, id string, in NewSchedule) (Schedule, error) {
	old, err := s.storedSchedule(ctx, id)
	if err != nil {
		return Schedule{}, err
	}
	if in.App == "" {
		in.App = old.AppID
	}
	if in.App != old.AppID {
		return Schedule{}, app.NewFieldError("app", "a schedule stays in its app; create a new one in %q", in.App)
	}
	c, err := s.checkSchedule(ctx, in)
	if err != nil {
		return Schedule{}, err
	}
	rec, err := s.store.UpdateSchedule(ctx, store.ScheduleRecord{ID: id, AppID: old.AppID, Service: c.service,
		Name: c.name, Command: c.command, Cron: c.cron, StopAfter: c.stopAfter, MemoryMB: c.memoryMB,
		SkipIfRunning: c.skipIfRunning})
	switch {
	case errors.Is(err, store.ErrAlreadyExists):
		return Schedule{}, fmt.Errorf("schedule %q of %q: %w", c.name, old.AppID, ErrScheduleExists)
	case errors.Is(err, store.ErrNotFound):
		return Schedule{}, fmt.Errorf("schedule %q: %w", id, ErrScheduleNotFound)
	case err != nil:
		return Schedule{}, err
	}
	if rec.Cron != old.Cron {
		if err := s.store.ArmSchedule(ctx, id, s.clock.Now().UTC()); err != nil {
			return Schedule{}, err
		}
		if rec, err = s.store.GetSchedule(ctx, id); err != nil {
			return Schedule{}, err
		}
	}
	s.schedulesChanged(rec.AppID)
	return s.scheduleOf(ctx, rec, &c.app)
}

// DeleteSchedule removes a schedule and its runs. A run that is going is
// ended first: its stop reason is written (schedule_removed) and its job is
// erased with its variable, since no row is left for the watcher to settle
// and purge it by. When Nomad can't do that the schedule stays, and the error
// says why.
func (s *Service) DeleteSchedule(ctx context.Context, id string) error {
	rec, err := s.storedSchedule(ctx, id)
	if err != nil {
		return err
	}
	open, err := s.store.OpenRuns(ctx, id)
	if err != nil {
		return err
	}
	now := s.clock.Now().UTC()
	for _, r := range open {
		if err := s.store.SetRunStopIntent(ctx, r.ID, stopScheduleRemoved, now); err != nil && !errors.Is(err, store.ErrInvalidTransition) {
			return err
		}
		if err := s.deployer.PurgeScheduleRun(ctx, runJobID(r)); err != nil {
			return fmt.Errorf("stop run %s of schedule %q: %w", r.ID, rec.Name, err)
		}
	}
	if err := s.store.DeleteSchedule(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("schedule %q: %w", id, ErrScheduleNotFound)
		}
		return err
	}
	s.schedulesChanged(rec.AppID)
	s.publish(Change{What: ChangeAttention})
	return nil
}

// PauseSchedule stops the schedule's clock; runs going go on, and Run now
// still works. Pausing a paused schedule changes nothing.
func (s *Service) PauseSchedule(ctx context.Context, id string) (Schedule, error) {
	rec, err := s.storedSchedule(ctx, id)
	if err != nil {
		return Schedule{}, err
	}
	if rec.PausedAt == nil {
		if err := s.store.SetSchedulePaused(ctx, id, new(s.clock.Now().UTC())); err != nil {
			return Schedule{}, err
		}
		s.schedulesChanged(rec.AppID)
	}
	return s.GetSchedule(ctx, id)
}

// ResumeSchedule starts the clock again from now: the slots that passed while
// it was paused are not run, the next one is. Resuming a schedule that isn't
// paused changes nothing.
func (s *Service) ResumeSchedule(ctx context.Context, id string) (Schedule, error) {
	rec, err := s.storedSchedule(ctx, id)
	if err != nil {
		return Schedule{}, err
	}
	if rec.PausedAt != nil {
		if err := s.store.ArmSchedule(ctx, id, s.clock.Now().UTC()); err != nil {
			return Schedule{}, err
		}
		if err := s.store.SetSchedulePaused(ctx, id, nil); err != nil {
			return Schedule{}, err
		}
		s.schedulesChanged(rec.AppID)
	}
	return s.GetSchedule(ctx, id)
}

// storedSchedule is the stored schedule id for a change: a built-in one is
// ErrScheduleBuiltin, a missing one ErrScheduleNotFound.
func (s *Service) storedSchedule(ctx context.Context, id string) (store.ScheduleRecord, error) {
	if strings.HasPrefix(id, builtinPrefix) {
		if _, _, err := s.builtinOf(ctx, id); err != nil {
			return store.ScheduleRecord{}, err
		}
		return store.ScheduleRecord{}, fmt.Errorf("schedule %q: %w", id, ErrScheduleBuiltin)
	}
	rec, err := s.store.GetSchedule(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.ScheduleRecord{}, fmt.Errorf("schedule %q: %w", id, ErrScheduleNotFound)
	}
	return rec, err
}

// --- reads ---

// GetSchedule returns one schedule, stored or built-in, or ErrScheduleNotFound.
func (s *Service) GetSchedule(ctx context.Context, id string) (Schedule, error) {
	if strings.HasPrefix(id, builtinPrefix) {
		rec, svc, err := s.builtinOf(ctx, id)
		if err != nil {
			return Schedule{}, err
		}
		return s.builtinSchedule(ctx, rec, svc)
	}
	rec, err := s.storedSchedule(ctx, id)
	if err != nil {
		return Schedule{}, err
	}
	return s.scheduleOf(ctx, rec, nil)
}

// ListSchedules returns the schedules of every app, or of one app or project,
// with the built-in ones (one per service with a backup), sorted: those whose
// last run failed first, then those running, then by next run, the paused
// ones last. An app or project that doesn't exist has none.
func (s *Service) ListSchedules(ctx context.Context, f ScheduleFilter) ([]Schedule, error) {
	apps, err := s.store.ListApps(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*store.AppRecord, len(apps))
	for i := range apps {
		byID[apps[i].App.ID] = &apps[i]
	}
	keep := func(rec *store.AppRecord) bool {
		return rec != nil && (f.App == "" || rec.App.ID == f.App) && (f.Project == "" || placeOf(*rec).Project == f.Project)
	}

	stored, err := s.store.ListSchedules(ctx, f.App)
	if err != nil {
		return nil, err
	}
	out := []Schedule{}
	for _, rec := range stored {
		a := byID[rec.AppID]
		if !keep(a) {
			continue
		}
		sc, err := s.scheduleOf(ctx, rec, a)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	for _, a := range apps {
		if !keep(&a) {
			continue
		}
		for _, svc := range a.App.Services {
			if svc.Backup == nil {
				continue
			}
			sc, err := s.builtinSchedule(ctx, a, svc)
			if err != nil {
				return nil, err
			}
			out = append(out, sc)
		}
	}
	sortSchedules(out)
	return out, nil
}

// sortSchedules orders schedules as the list shows them.
func sortSchedules(list []Schedule) {
	rank := func(sc Schedule) int {
		switch {
		case sc.Paused:
			return 3
		case sc.LastRun != nil && runFailed(sc.LastRun.Status):
			return 0
		case sc.RunningNow > 0:
			return 1
		}
		return 2
	}
	slices.SortStableFunc(list, func(a, b Schedule) int {
		if c := cmp.Compare(rank(a), rank(b)); c != 0 {
			return c
		}
		switch {
		case a.NextRunAt != nil && b.NextRunAt == nil:
			return -1
		case a.NextRunAt == nil && b.NextRunAt != nil:
			return 1
		case a.NextRunAt != nil:
			if c := a.NextRunAt.Compare(*b.NextRunAt); c != 0 {
				return c
			}
		}
		return cmp.Or(cmp.Compare(a.App, b.App), cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
	})
}

// runFailed says a run's status counts as a failure: what the needs-you list
// and the list's order call a failed last run.
func runFailed(status string) bool {
	return status == store.RunFailed || status == store.RunStoppedAtLimit || status == store.RunCouldNotStart
}

// ListScheduleRuns returns a schedule's newest runs, newest first: 14 when
// limit is 0, at most 100. A built-in schedule's runs are its service's
// backups.
func (s *Service) ListScheduleRuns(ctx context.Context, id string, limit int) ([]ScheduleRun, error) {
	if limit <= 0 {
		limit = defaultRunsLimit
	}
	limit = min(limit, maxRunsLimit)
	if strings.HasPrefix(id, builtinPrefix) {
		rec, svc, err := s.builtinOf(ctx, id)
		if err != nil {
			return nil, err
		}
		runs, err := s.backupRuns(ctx, rec, svc)
		if err != nil {
			return nil, err
		}
		return runs[:min(limit, len(runs))], nil
	}
	if _, err := s.storedSchedule(ctx, id); err != nil {
		return nil, err
	}
	recs, err := s.store.ListRuns(ctx, id, limit)
	if err != nil {
		return nil, err
	}
	out := make([]ScheduleRun, 0, len(recs))
	for _, r := range recs {
		out = append(out, runOf(r))
	}
	return out, nil
}

// GetScheduleRun returns one run of a schedule, without its output.
// ErrScheduleNotFound or ErrRunNotFound when there is no such thing.
func (s *Service) GetScheduleRun(ctx context.Context, scheduleID, runID string) (ScheduleRun, error) {
	if strings.HasPrefix(scheduleID, builtinPrefix) {
		rec, svc, err := s.builtinOf(ctx, scheduleID)
		if err != nil {
			return ScheduleRun{}, err
		}
		runs, err := s.backupRuns(ctx, rec, svc)
		if err != nil {
			return ScheduleRun{}, err
		}
		if i := slices.IndexFunc(runs, func(r ScheduleRun) bool { return r.ID == runID }); i >= 0 {
			return runs[i], nil
		}
		return ScheduleRun{}, fmt.Errorf("run %q of %q: %w", runID, scheduleID, ErrRunNotFound)
	}
	r, err := s.storedRun(ctx, scheduleID, runID)
	if err != nil {
		return ScheduleRun{}, err
	}
	return runOf(r), nil
}

// storedRun is run runID of the stored schedule scheduleID, output included.
func (s *Service) storedRun(ctx context.Context, scheduleID, runID string) (store.ScheduleRunRecord, error) {
	if _, err := s.storedSchedule(ctx, scheduleID); err != nil {
		return store.ScheduleRunRecord{}, err
	}
	r, err := s.store.GetRun(ctx, runID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && r.ScheduleID != scheduleID) {
		return store.ScheduleRunRecord{}, fmt.Errorf("run %q of %q: %w", runID, scheduleID, ErrRunNotFound)
	}
	return r, err
}

// GetScheduleRunOutput returns what a run printed on stream ("stdout" or
// "stderr"): the last 64 KiB, secret values masked, and whether the start was
// cut. It is the only read that returns output. ErrOutputNotKept when there
// is none to give: the run never ran, its output couldn't be read, or it is a
// backup's run.
func (s *Service) GetScheduleRunOutput(ctx context.Context, scheduleID, runID, stream string) ([]byte, bool, error) {
	if stream != "stdout" && stream != "stderr" {
		return nil, false, app.NewFieldError("stream", "must be stdout or stderr")
	}
	if strings.HasPrefix(scheduleID, builtinPrefix) {
		if _, err := s.GetScheduleRun(ctx, scheduleID, runID); err != nil {
			return nil, false, err
		}
		return nil, false, fmt.Errorf("run %q of %q: %w", runID, scheduleID, ErrOutputNotKept)
	}
	r, err := s.storedRun(ctx, scheduleID, runID)
	if err != nil {
		return nil, false, err
	}
	if !r.OutputKept {
		return nil, false, fmt.Errorf("run %q of %q: %w", runID, scheduleID, ErrOutputNotKept)
	}
	if stream == "stdout" {
		return []byte(r.Stdout), r.StdoutCut, nil
	}
	return []byte(r.Stderr), r.StderrCut, nil
}

// scheduleOf is a stored schedule with what is derived from its runs and its
// app. a is the schedule's app when the caller has it, else nil.
func (s *Service) scheduleOf(ctx context.Context, rec store.ScheduleRecord, a *store.AppRecord) (Schedule, error) {
	if a == nil {
		got, err := s.store.GetApp(ctx, rec.AppID)
		if err != nil {
			return Schedule{}, err
		}
		a = &got
	}
	runs, err := s.store.ListRuns(ctx, rec.ID, 0)
	if err != nil {
		return Schedule{}, err
	}
	place := placeOf(*a)
	sc := Schedule{ID: rec.ID, App: rec.AppID, Service: rec.Service, Name: rec.Name, Command: rec.Command,
		Cron: rec.Cron, StopAfterSeconds: int(rec.StopAfter / time.Second), MemoryMB: rec.MemoryMB,
		SkipIfRunning: rec.SkipIfRunning, Words: cronwords.Words(rec.Cron), Paused: rec.PausedAt != nil,
		PausedAt: rec.PausedAt, Project: place.Project, Environment: place.Environment, AppName: place.Name,
		ArmedAt: rec.ArmedAt}
	if rec.PausedAt == nil {
		from := s.clock.Now().UTC()
		if rec.ArmedAt.After(from) {
			from = rec.ArmedAt
		}
		if next := cronwords.Next(rec.Cron, from, 1); len(next) == 1 {
			sc.NextRunAt = &next[0]
		}
	}
	all := make([]ScheduleRun, 0, len(runs))
	for _, r := range runs {
		all = append(all, runOf(r))
	}
	fillRuns(&sc, all)
	return sc, nil
}

// fillRuns sets what a schedule derives from its runs, newest first:
// LastRun, RunningNow and UsualSeconds.
func fillRuns(sc *Schedule, runs []ScheduleRun) {
	var durations []int
	for i, r := range runs {
		if r.Status == store.RunStarting || r.Status == store.RunRunning {
			sc.RunningNow++
		}
		if sc.LastRun == nil && r.Status != store.RunSkipped {
			sc.LastRun = &runs[i]
		}
		if i < usualRuns && r.Status == store.RunOK && r.DurationSeconds != nil {
			durations = append(durations, *r.DurationSeconds)
		}
	}
	if m, ok := median(durations); ok {
		sc.UsualSeconds = &m
	}
}

// median is the middle of xs (the lower of the two middles for an even
// count, so it is always a duration that happened); false for none.
func median(xs []int) (int, bool) {
	if len(xs) == 0 {
		return 0, false
	}
	sorted := slices.Sorted(slices.Values(xs))
	return sorted[(len(sorted)-1)/2], true
}

// runOf is a stored run as the API shows it, without its output.
func runOf(r store.ScheduleRunRecord) ScheduleRun {
	out := ScheduleRun{ID: r.ID, ScheduleID: r.ScheduleID, Trigger: r.Trigger, SlotAt: r.SlotAt, Status: r.Status,
		ExitCode: r.ExitCode, Error: r.Error, StartedAt: r.StartedAt, TaskStartedAt: r.TaskStartedAt,
		FinishedAt: r.FinishedAt, Command: r.Command, JobVersion: r.JobVersion, OutputKept: r.OutputKept,
		StdoutBytes: r.StdoutBytes, StderrBytes: r.StderrBytes, StdoutCut: r.StdoutCut, StderrCut: r.StderrCut}
	if r.DeploymentID != "" {
		out.DeploymentID = new(r.DeploymentID)
	}
	if r.Status != store.RunSkipped {
		out.DurationSeconds = durationOf(r.StartedAt, r.TaskStartedAt, r.FinishedAt)
	}
	return out
}

// durationOf is how long a run took, in whole seconds, from the task's start
// (or the run's, when the task's isn't known) to the finish; nil while it
// goes.
func durationOf(started time.Time, taskStarted, finished *time.Time) *int {
	if finished == nil {
		return nil
	}
	from := started
	if taskStarted != nil {
		from = *taskStarted
	}
	return new(max(0, int(finished.Sub(from).Round(time.Second)/time.Second)))
}

// placeOf is where an app is: the stored place, or the one its ID says for a
// record that has none.
func placeOf(rec store.AppRecord) app.Place {
	if rec.Place.Name != "" {
		return rec.Place
	}
	if p, err := app.PlaceOf(rec.App.ID); err == nil {
		return p
	}
	return app.Place{Project: app.DefaultProject, Environment: app.DefaultEnvironment, Name: rec.App.ID}
}

// runJobID is the Nomad job of a run.
func runJobID(r store.ScheduleRunRecord) string { return deploy.ScheduleRunJobID(r.AppID, r.ID) }

// --- built-in schedules ---

// builtinID is the ID of the built-in schedule of a service's backup.
func builtinID(appID, service string) string { return builtinPrefix + appID + ":" + service }

// builtinOf finds the app and the service of a built-in schedule's ID, or
// ErrScheduleNotFound.
func (s *Service) builtinOf(ctx context.Context, id string) (store.AppRecord, app.Service, error) {
	parts := strings.Split(strings.TrimPrefix(id, builtinPrefix), ":")
	notFound := fmt.Errorf("schedule %q: %w", id, ErrScheduleNotFound)
	if len(parts) != 2 {
		return store.AppRecord{}, app.Service{}, notFound
	}
	rec, err := s.store.GetApp(ctx, parts[0])
	if errors.Is(err, store.ErrNotFound) {
		return store.AppRecord{}, app.Service{}, notFound
	}
	if err != nil {
		return store.AppRecord{}, app.Service{}, err
	}
	for _, svc := range rec.App.Services {
		if svc.Name == parts[1] && svc.Backup != nil {
			return rec, svc, nil
		}
	}
	return store.AppRecord{}, app.Service{}, notFound
}

// builtinSchedule is the schedule a service's backup policy is: its cron, its
// next run (none while the app is stopped), and its runs from the backups.
func (s *Service) builtinSchedule(ctx context.Context, rec store.AppRecord, svc app.Service) (Schedule, error) {
	cron := svc.Backup.ScheduleOrDefault()
	place := placeOf(rec)
	sc := Schedule{ID: builtinID(rec.App.ID, svc.Name), App: rec.App.ID, Service: svc.Name, Name: svc.Name + "-backup",
		Command: []string{}, Cron: cron, SkipIfRunning: true, Words: cronwords.Words(cron), Builtin: true,
		Project: place.Project, Environment: place.Environment, AppName: place.Name, Engine: svc.Backup.Engine}
	if rec.StoppedAt == nil {
		if next := cronwords.Next(cron, s.clock.Now(), 1); len(next) == 1 {
			sc.NextRunAt = &next[0]
		}
	}
	runs, err := s.backupRuns(ctx, rec, svc)
	if err != nil {
		return Schedule{}, err
	}
	fillRuns(&sc, runs)
	return sc, nil
}

// backupRuns are a service's backups as the runs of its built-in schedule,
// newest first: ok, failed or running, with the backup's message as the
// error of a failed one, and never any output.
func (s *Service) backupRuns(ctx context.Context, rec store.AppRecord, svc app.Service) ([]ScheduleRun, error) {
	backups, err := s.store.ListBackups(ctx, rec.App.ID, 0)
	if err != nil {
		return nil, err
	}
	id := builtinID(rec.App.ID, svc.Name)
	out := []ScheduleRun{}
	for _, b := range backups {
		if b.Service != svc.Name {
			continue
		}
		r := ScheduleRun{ID: b.ID, ScheduleID: id, Trigger: RunTriggerTimer, StartedAt: b.StartedAt,
			FinishedAt: b.FinishedAt, Command: []string{}}
		switch b.Status {
		case store.BackupSucceeded:
			r.Status = store.RunOK
		case store.BackupFailed:
			r.Status, r.Error = store.RunFailed, b.Message
		default:
			r.Status = store.RunRunning
		}
		r.DurationSeconds = durationOf(b.StartedAt, nil, b.FinishedAt)
		out = append(out, r)
	}
	return out, nil
}
