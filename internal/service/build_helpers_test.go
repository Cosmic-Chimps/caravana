package service

import (
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
)

func TestBuildFailureReason(t *testing.T) {
	marker := nomadjob.BuildMarker + " "
	tests := []struct {
		name string
		log  string
		st   deploy.RunState
		want string
	}{
		{"the clone failed", "cloning\n" + marker + nomadjob.BuildCloneFailed + "\n", deploy.RunState{}, "the repository could not be read"},
		{"no Dockerfile", marker + nomadjob.BuildDockerfileMissing + "deploy/Dockerfile\n", deploy.RunState{}, "no Dockerfile at deploy/Dockerfile"},
		{"no folder", marker + nomadjob.BuildFolderMissing + "api\n", deploy.RunState{}, "no folder in the repository at api"},
		{"the build failed", marker + "building x\nstep failed\n" + marker + nomadjob.BuildFailed + "\n", deploy.RunState{}, "the build failed"},
		{"the push failed", "error: failed to solve: failed to push ghcr.io/acme/web:git-1: 401 Unauthorized\n" + marker + nomadjob.BuildFailed + "\n", deploy.RunState{}, "the image could not be pushed"},
		{"a say line is not a reason", marker + "cloned commit abc\n", deploy.RunState{}, "the build failed"},
		{"the last reason wins", marker + nomadjob.BuildCloneFailed + "\n" + marker + nomadjob.BuildFailed + "\n", deploy.RunState{}, "the build failed"},
		{"killed for memory", "#4 RUN make\n", deploy.RunState{OOMKilled: true}, buildReasonMemory},
		{"the job could not start", "", deploy.RunState{StartFailed: true}, buildReasonNoStart},
		{"nothing is known", "", deploy.RunState{}, "the build failed"},
		{"a marker inside a line is not one", "echo caravana-build: " + nomadjob.BuildCloneFailed + "\n", deploy.RunState{}, "the build failed"},
	}
	for _, tc := range tests {
		if got := buildFailureReason(tc.log, tc.st); got != tc.want {
			t.Errorf("%s: reason = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestMaskBuildLog(t *testing.T) {
	log := "git clone https://ana:ghp_TOKEN123@github.com/acme/web.git\npush with hunter2 ok, and TOKEN123 again\nnothing here\n"
	got := maskBuildLog(log, []string{"TOKEN123", "hunter2", ""})
	for _, leaked := range []string{"TOKEN123", "hunter2", "ana:"} {
		if strings.Contains(got, leaked) {
			t.Errorf("%q is still in the log:\n%s", leaked, got)
		}
	}
	if !strings.Contains(got, "https://***@github.com/acme/web.git") || !strings.Contains(got, "nothing here") {
		t.Errorf("the mask took too much or too little:\n%s", got)
	}
	// An address with a port and a path is not a credential.
	if kept := maskBuildLog("see http://127.0.0.1:8098/app.git\n", nil); kept != "see http://127.0.0.1:8098/app.git\n" {
		t.Errorf("a plain address was changed: %q", kept)
	}
}

func TestLastLines(t *testing.T) {
	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, "line "+strings.Repeat("x", i%3)+"\n", "\n")
	}
	got := lastLines(strings.Join(lines, ""), 20)
	if len(got) != 20 || got[19] != strings.TrimSpace("line "+strings.Repeat("x", 30%3)) {
		t.Errorf("lastLines = %d lines, last %q", len(got), got[len(got)-1])
	}
	if long := lastLines(strings.Repeat("é", 1000), 5); len(long) != 1 || len(long[0]) > 410 || !strings.HasSuffix(long[0], "…") {
		t.Errorf("a long line is cut to %d bytes: %q", len(long[0]), long[0][len(long[0])-10:])
	}
}

func TestBuildErrorText(t *testing.T) {
	e := &BuildError{Reason: "the build failed", Tail: []string{"one", "two"}}
	if e.Error() != "build failed: the build failed\none\ntwo" {
		t.Errorf("text = %q", e.Error())
	}
	if (&BuildError{Reason: "x"}).Error() != "build failed: x" {
		t.Error("a reason alone has a trailing newline")
	}
}

func TestBuildOriginFitsWhatAnOriginAccepts(t *testing.T) {
	b := app.Build{Repository: "git@github.com:acme/web.git", Branch: "release/1.2"}
	got := buildOrigin(b, deploy.BuildResult{Commit: buildCommit, Author: "Ana\x00 Ñandú\n" + strings.Repeat("x", 300)})
	if got.Commit != buildCommit || got.Ref != "refs/heads/release/1.2" || got.Repository != "github.com/acme/web" {
		t.Errorf("origin = %+v", got)
	}
	if got.Validate() != nil || len(got.Actor) > 100 || strings.ContainsAny(got.Actor, "\x00\n") {
		t.Errorf("actor = %q, validate = %v", got.Actor, got.Validate())
	}
	if (buildOrigin(app.Build{Repository: "https://git.example.com/a/b"}, deploy.BuildResult{Commit: "nothex"})).Commit != "" {
		t.Error("an invalid commit was kept")
	}
	if o := buildOrigin(app.Build{Repository: "https://git.example.com/a/b"}, deploy.BuildResult{Commit: buildCommit}); o.Repository != "git.example.com/a/b" {
		t.Errorf("repository = %q", o.Repository)
	}
}

func TestNewDeployKeyIsAnOpenSSHKeyWithItsPublicHalf(t *testing.T) {
	private, public, err := newDeployKey(rand.Reader, "shop", "web")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(private, "-----BEGIN OPENSSH PRIVATE KEY-----\n") || !strings.HasSuffix(private, "-----END OPENSSH PRIVATE KEY-----\n") {
		t.Errorf("private key = %q…", private[:40])
	}
	signer, err := ssh.ParsePrivateKey([]byte(private))
	if err != nil {
		t.Fatalf("OpenSSH's own parser can't read the key: %v", err)
	}
	pub, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(public))
	if err != nil || comment != "caravana shop/web" {
		t.Fatalf("public = %q: %v (comment %q)", public, err, comment)
	}
	if string(pub.Marshal()) != string(signer.PublicKey().Marshal()) || pub.Type() != "ssh-ed25519" {
		t.Error("the public half is not the private key's")
	}
	if strings.Contains(public, "\n") {
		t.Error("the public key line has a line break")
	}
	// Two keys differ.
	other, _, _ := newDeployKey(rand.Reader, "shop", "web")
	if other == private {
		t.Error("two keys are the same")
	}
	if err := checkGitToken("x", strings.Repeat("a", maxGitToken)); err != nil {
		t.Errorf("a token of the largest size: %v", err)
	}
	var fe *app.FieldError
	if err := checkGitToken("x", "bad token"); !errors.As(err, &fe) || strings.Contains(err.Error(), "bad token") {
		t.Errorf("a token with a space = %v", err)
	}
}
