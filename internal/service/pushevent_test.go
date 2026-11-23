package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

const (
	pushAfter  = "4e0a77c1b2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7"
	pushBefore = "1111111111111111111111111111111111111111"
	zeroSHA    = "0000000000000000000000000000000000000000"
)

// These bodies are the shape of each provider's real push (only the fields Caravana
// reads are given their real values; the rest of a body is left out).
var (
	githubPush = `{"ref":"refs/heads/main","before":"` + pushBefore + `","after":"` + pushAfter + `","created":false,"deleted":false,"forced":false,
		"head_commit":{"id":"` + pushAfter + `","message":"Retry supplier timeouts","author":{"name":"Ana"}},
		"repository":{"full_name":"acme/web","clone_url":"https://github.com/acme/web.git"},"pusher":{"name":"ana"}}`
	githubDelete = `{"ref":"refs/heads/feature","before":"` + pushAfter + `","after":"` + zeroSHA + `","created":false,"deleted":true,"head_commit":null}`
	githubPing   = `{"zen":"Design for failure.","hook_id":1,"hook":{"type":"Repository","events":["push"]},"repository":{"full_name":"acme/web"}}`
	giteaPush    = `{"ref":"refs/heads/main","before":"` + pushBefore + `","after":"` + pushAfter + `","compare_url":"https://git.example.com/acme/web/compare/a...b",
		"commits":[{"id":"` + pushAfter + `"}],"repository":{"full_name":"acme/web"}}`
	gitlabPush = `{"object_kind":"push","event_name":"push","before":"` + pushBefore + `","after":"` + pushAfter + `","ref":"refs/heads/main",
		"checkout_sha":"` + pushAfter + `","user_name":"Ana","project":{"path_with_namespace":"acme/web"},"commits":[]}`
	gitlabDelete = `{"object_kind":"push","before":"` + pushAfter + `","after":"` + zeroSHA + `","ref":"refs/heads/feature","checkout_sha":null}`
	gitlabTag    = `{"object_kind":"tag_push","ref":"refs/tags/v1","checkout_sha":"` + pushAfter + `","after":"` + pushAfter + `"}`
)

func headerOf(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

func TestPushEventReadsEachProvider(t *testing.T) {
	tests := []struct {
		name   string
		header http.Header
		body   string
		want   Push
		skip   string // the reason for an app that builds main; "" builds
	}{
		{"github push", headerOf("X-GitHub-Event", "push"), githubPush, Push{Event: "push", Ref: "refs/heads/main", Commit: pushAfter}, ""},
		{"github push to another branch", headerOf("X-GitHub-Event", "push"), strings.Replace(githubPush, "refs/heads/main", "refs/heads/dev", 1),
			Push{Event: "push", Ref: "refs/heads/dev", Commit: pushAfter}, "a push to refs/heads/dev, not main"},
		{"github tag", headerOf("X-GitHub-Event", "push"), strings.Replace(githubPush, "refs/heads/main", "refs/tags/v1", 1),
			Push{Event: "push", Ref: "refs/tags/v1", Commit: pushAfter}, "a push to refs/tags/v1, not main"},
		{"github deleted branch", headerOf("X-GitHub-Event", "push"), githubDelete, Push{Event: "push", Ref: "refs/heads/feature", Deleted: true}, "the branch was deleted"},
		{"github ping", headerOf("X-GitHub-Event", "ping"), githubPing, Push{Event: "ping"}, "a ping"},
		{"github another event", headerOf("X-GitHub-Event", "issues"), `{"action":"opened"}`, Push{Event: "other", Name: "issues"}, "an event that is not a push (issues)"},
		{"gitea push", headerOf("X-Gitea-Event", "push"), giteaPush, Push{Event: "push", Ref: "refs/heads/main", Commit: pushAfter}, ""},
		{"forgejo push", headerOf("X-Forgejo-Event", "push"), giteaPush, Push{Event: "push", Ref: "refs/heads/main", Commit: pushAfter}, ""},
		{"gitlab push", headerOf("X-Gitlab-Event", "Push Hook"), gitlabPush, Push{Event: "push", Ref: "refs/heads/main", Commit: pushAfter}, ""},
		{"gitlab deleted branch", headerOf("X-Gitlab-Event", "Push Hook"), gitlabDelete, Push{Event: "push", Ref: "refs/heads/feature", Deleted: true}, "the branch was deleted"},
		{"gitlab tag push", headerOf("X-Gitlab-Event", "Tag Push Hook"), gitlabTag, Push{Event: "other", Name: "Tag Push Hook"}, "an event that is not a push (Tag Push Hook)"},
		{"a push whose body isn't JSON still builds", headerOf("X-GitHub-Event", "push"), "not json", Push{Event: "push"}, ""},
		{"an upper-case commit is lower-cased", headerOf("X-GitHub-Event", "push"), `{"ref":"refs/heads/main","after":"` + strings.ToUpper(pushAfter) + `"}`,
			Push{Event: "push", Ref: "refs/heads/main", Commit: pushAfter}, ""},
		{"a short or odd commit is no commit", headerOf("X-GitHub-Event", "push"), `{"ref":"refs/heads/main","after":"xyz"}`, Push{Event: "push", Ref: "refs/heads/main"}, ""},
		{"no provider header: the body is not read", http.Header{}, githubDelete, Push{}, ""},
		{"no body at all", http.Header{}, "", Push{}, ""},
		{"an event name with control characters is cleaned", headerOf("X-GitHub-Event", "bad\tname"), `{}`, Push{Event: "other", Name: "badname"}, "an event that is not a push (badname)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := PushEvent(tc.header, []byte(tc.body))
			if got != tc.want {
				t.Errorf("PushEvent = %+v, want %+v", got, tc.want)
			}
			if reason := got.SkipReason("main"); reason != tc.skip {
				t.Errorf("SkipReason = %q, want %q", reason, tc.skip)
			}
		})
	}
}

// builtWebhookLab is a lab with a built app deployed once (commit pushAfter) and a
// webhook token, which the tests call as a push webhook would.
func builtWebhookLab(t *testing.T, play func(deploytest.PlayedBuild) deploytest.BuildPlay) (*buildLab, store.DeploymentRecord) {
	t.Helper()
	l := newBuildLab(t, play)
	rec := l.deploy(t, builtApp())
	final := waitFinal(t, l.store, rec.Deployment.ID)
	if final.Status != store.StatusHealthy {
		t.Fatalf("first deployment = %s %q", final.Status, final.Error)
	}
	waitFor(t, "the deploy's lock", func() bool {
		st := l.svc.stateOf("shop")
		if !st.mu.TryLock() {
			return false
		}
		st.mu.Unlock()
		return true
	})
	return l, final
}

func TestAPushToABuiltAppsBranchBuildsTheNewestCommit(t *testing.T) {
	ctx := context.Background()
	l, first := builtWebhookLab(t, func(deploytest.PlayedBuild) deploytest.BuildPlay {
		return succeedingBuild()
	})

	// The commit the first deployment was built from is live: a push of it builds nothing.
	res, err := l.svc.WebhookPush(ctx, "shop", headerOf("X-GitHub-Event", "push"), []byte(githubPush))
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed || res.Skipped != "" || res.Deployment.Deployment.ID != first.Deployment.ID {
		t.Errorf("push of the live commit = %+v, want unchanged with the live deployment", res)
	}

	// Skipped calls answer with the app's newest deployment and a sentence.
	for name, c := range map[string]struct {
		header http.Header
		body   string
		why    string
	}{
		"a push to another branch": {headerOf("X-GitHub-Event", "push"), strings.Replace(githubPush, "refs/heads/main", "refs/heads/dev", 1), "a push to refs/heads/dev, not main"},
		"a deleted branch":         {headerOf("X-Gitlab-Event", "Push Hook"), gitlabDelete, "the branch was deleted"},
		"a ping":                   {headerOf("X-Gitea-Event", "ping"), `{}`, "a ping"},
	} {
		res, err := l.svc.WebhookPush(ctx, "shop", c.header, []byte(c.body))
		if err != nil || res.Changed || res.Skipped != c.why || res.Deployment.Deployment.ID != first.Deployment.ID {
			t.Errorf("%s = %+v, %v; want skipped %q", name, res, err, c.why)
		}
	}
	if got := len(l.player.Played()); got != 1 {
		t.Fatalf("builds = %d after calls that build nothing, want 1", got)
	}

	// A push of a new commit builds the branch's newest and deploys it.
	newer := strings.Replace(githubPush, pushAfter, "9999999999999999999999999999999999999999", 1)
	res, err = l.svc.WebhookPush(ctx, "shop", headerOf("X-GitHub-Event", "push"), []byte(newer))
	if err != nil || !res.Changed || res.Deployment.Build == nil || res.Deployment.Deployment.Trigger != app.TriggerWebhook {
		t.Fatalf("push of a new commit = %+v, %v", res, err)
	}
	if got := waitFinal(t, l.store, res.Deployment.Deployment.ID); got.Status != store.StatusHealthy {
		t.Errorf("deployment = %s %q", got.Status, got.Error)
	}

	// A body no provider sent (the old webhook call) builds the newest commit too.
	res, err = l.svc.WebhookPush(ctx, "shop", http.Header{}, nil)
	if err != nil || !res.Changed || res.Deployment.Build == nil {
		t.Fatalf("a plain call = %+v, %v", res, err)
	}
	waitFinal(t, l.store, res.Deployment.Deployment.ID)
}

func TestAPushToAnAppWithNothingDeployedYetIsSkippedWithoutADeployment(t *testing.T) {
	ctx := context.Background()
	l := newBuildLab(t, nil)
	if _, err := l.svc.CreateAppOnly(ctx, builtApp()); err != nil {
		t.Fatal(err)
	}
	res, err := l.svc.WebhookPush(ctx, "shop", headerOf("X-GitHub-Event", "ping"), []byte(githubPing))
	if err != nil || res.Changed || res.Skipped != "a ping" || res.Deployment.Deployment.ID != "" {
		t.Errorf("ping = %+v, %v", res, err)
	}
}

func TestAnAppWithoutABuiltServiceIgnoresThePushBody(t *testing.T) {
	ctx := context.Background()
	l := newBuildLab(t, nil)
	l.registry.AddIndex("traefik/whoami", "latest", whoamiDigest)
	rec := l.deploy(t, whoami())
	waitFinal(t, l.store, rec.Deployment.ID)
	waitFor(t, "the lock", func() bool {
		st := l.svc.stateOf("whoami")
		if !st.mu.TryLock() {
			return false
		}
		st.mu.Unlock()
		return true
	})
	// A ping would be skipped for a built app; here the body is ignored: nothing changed, nothing skipped.
	res, err := l.svc.WebhookPush(ctx, "whoami", headerOf("X-GitHub-Event", "ping"), []byte(githubPing))
	if err != nil || res.Changed || res.Skipped != "" || res.Deployment.Deployment.ID != rec.Deployment.ID {
		t.Errorf("= %+v, %v; want the unchanged answer of the plain webhook", res, err)
	}
	// A moved tag redeploys, as before.
	l.registry.AddIndex("traefik/whoami", "latest", movedDigest)
	res, err = l.svc.WebhookPush(ctx, "whoami", http.Header{}, nil)
	if err != nil || !res.Changed {
		t.Errorf("a moved tag = %+v, %v", res, err)
	}
	waitFinal(t, l.store, res.Deployment.Deployment.ID)
}

func TestAStoppedBuiltAppRefusesAPush(t *testing.T) {
	ctx := context.Background()
	l, _ := builtWebhookLab(t, nil)
	if _, err := l.svc.StopApp(ctx, "shop", app.TriggerManual); err != nil {
		t.Fatal(err)
	}
	if _, err := l.svc.WebhookPush(ctx, "shop", headerOf("X-GitHub-Event", "ping"), []byte(githubPing)); !errors.Is(err, ErrAppStopped) {
		t.Errorf("push to a stopped app = %v, want ErrAppStopped", err)
	}
}
