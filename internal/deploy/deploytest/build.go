package deploytest

import (
	"encoding/base64"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/nomadjob"
)

// Builds (feature 050). A build is a batch job with a prestart task `source`
// (the clone) and a main task `build`. A test plays Git and BuildKit with a
// BuildPlayer: it finds each build job Caravana registers and ends it as the
// test says, with the files and the output a real one leaves.

// BuildPlay says how a build job ends.
type BuildPlay struct {
	// Commit, Author and Subject are what the clone read: the source file's
	// contents. A commit "" means the clone didn't get that far, and no file
	// is written.
	Commit, Author, Subject string
	// Digest is the manifest digest the push reports (build.json). "" with no
	// Fail means the build ends without writing one.
	Digest string
	// SourceOut and BuildOut are the stdout of the two tasks, SourceErr and
	// BuildErr their stderr. A failed build's marker line goes to the failing
	// task's stderr after these.
	SourceOut, SourceErr, BuildOut, BuildErr string
	// Fail is the reason a build fails with: one of nomadjob's BuildCloneFailed,
	// BuildDockerfileMissing..., printed after nomadjob.BuildMarker on the stderr
	// of the failing task and ending the job with exit code 1. "" means the
	// build succeeds. The clone reasons fail the source task, the others the
	// build task.
	Fail string
	// Hold leaves the job running: the test ends it itself.
	Hold bool
}

// PlayedBuild is a build job the player saw.
type PlayedBuild struct {
	Job *api.Job
	ID  string
	// Variable is the job's own variable as it was when the job started: what
	// Caravana wrote for its templates to read.
	Variable map[string]string
	// SourceEnv and BuildEnv are the tasks' environments.
	SourceEnv, BuildEnv map[string]string
}

// BuildPlayer plays every build job of a Nomad.
type BuildPlayer struct {
	nomad *Nomad
	mu    sync.Mutex
	seen  map[string]bool
	list  []PlayedBuild
	play  func(PlayedBuild) BuildPlay
}

// PlayBuilds starts a player that ends every build job the test's Caravana
// registers with what play says. It stops when the test ends.
func PlayBuilds(t testing.TB, n *Nomad, play func(PlayedBuild) BuildPlay) *BuildPlayer {
	t.Helper()
	p := &BuildPlayer{nomad: n, seen: map[string]bool{}, play: play}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
				p.step()
			}
		}
	}()
	t.Cleanup(func() { close(stop); <-done })
	return p
}

func (p *BuildPlayer) step() {
	for _, id := range p.nomad.JobIDs("") {
		if !strings.Contains(id, "_build-") {
			continue
		}
		p.mu.Lock()
		if p.seen[id] {
			p.mu.Unlock()
			continue
		}
		p.seen[id] = true
		p.mu.Unlock()
		job := p.nomad.Job(id)
		if job == nil || len(job.TaskGroups) == 0 {
			continue // not a build job of Caravana's making
		}
		tasks := job.TaskGroups[0].Tasks
		pb := PlayedBuild{Job: job, ID: id, Variable: p.nomad.Variable(nomadjob.BuildVariablePath(id))}
		for _, task := range tasks {
			switch task.Name {
			case nomadjob.BuildSourceTask:
				pb.SourceEnv = task.Env
			case nomadjob.BuildTask:
				pb.BuildEnv = task.Env
			}
		}
		p.mu.Lock()
		p.list = append(p.list, pb)
		p.mu.Unlock()
		p.finish(pb, p.play(pb))
	}
}

func (p *BuildPlayer) finish(pb PlayedBuild, how BuildPlay) {
	id := pb.ID
	alloc := id + "-alloc"
	p.nomad.StartRun(id)
	sourceErr, buildErr := how.SourceErr, how.BuildErr
	exit := 0
	if how.Fail != "" {
		exit = 1
		marker := nomadjob.BuildMarker + " " + how.Fail + "\n"
		if how.Fail == nomadjob.BuildCloneFailed || strings.HasPrefix(how.Fail, nomadjob.BuildDockerfileMissing) ||
			strings.HasPrefix(how.Fail, nomadjob.BuildFolderMissing) {
			sourceErr += marker
		} else {
			buildErr += marker
		}
	}
	p.nomad.SetTaskLogs(alloc, nomadjob.BuildSourceTask, []byte(how.SourceOut), []byte(sourceErr))
	p.nomad.SetTaskLogs(alloc, nomadjob.BuildTask, []byte(how.BuildOut), []byte(buildErr))
	if how.Commit != "" {
		enc := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
		p.nomad.WriteAllocFile(alloc, nomadjob.BuildSourceFile,
			"COMMIT="+enc(how.Commit)+"\nAUTHOR="+enc(how.Author)+"\nSUBJECT="+enc(how.Subject)+"\n")
	}
	if how.Digest != "" && how.Fail == "" {
		p.nomad.WriteAllocFile(alloc, nomadjob.BuildResultFile, `{"containerimage.digest":"`+how.Digest+`"}`)
	}
	if how.Hold {
		return
	}
	p.nomad.FinishRunExit(id, exit)
}

// Played returns the build jobs seen so far, in order.
func (p *BuildPlayer) Played() []PlayedBuild {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]PlayedBuild(nil), p.list...)
}

// Finish ends a held build job as how says (its Hold is ignored).
func (p *BuildPlayer) Finish(job string, how BuildPlay) {
	how.Hold = false
	p.finish(PlayedBuild{ID: job}, how)
}
