package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/secrets"
)

// filesSentinel is a secret file's value that no store row, record, job, export or log may hold. The one
// place it belongs is the service's Nomad Variable, which is where the file's template reads it.
const filesSentinel = "s3cret-sentinel-060-key"

func withFiles() app.App {
	a := withSecrets(set("TLS_KEY", "-----BEGIN KEY-----\n"+filesSentinel+"\n-----END KEY-----\n"), set("API_KEY", "k1"))
	a.Services[0].Files = []app.File{
		{Path: "/etc/app/app.yml", Content: "public: true\n{{ not a template }} ${HOME}\n", Mode: "0640"},
		{Path: "/certs/node.key", Secret: "TLS_KEY", Mode: "0400"},
	}
	return a
}

// A secret a file holds is a secret like any other: its value is written to the service's Nomad Variable and
// nowhere else, and the job reads it from there into the file, never into the environment.
func TestSecretFileValueOnlyInTheVariable(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	logs := &lockedBuffer{}
	e.svc = startServiceLogging(t, e.store, e.svc.deployer, e.secrets, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))

	rec := deployHealthy(t, e, withFiles())
	want := secrets.Sets{"web": {"TLS_KEY": "-----BEGIN KEY-----\n" + filesSentinel + "\n-----END KEY-----\n", "API_KEY": "k1"}}
	if got := e.secrets.Live("whoami"); !got.Equal(want) {
		t.Fatalf("live = %v, want the typed values in the variable (the file's secret too)", got)
	}

	// The job: a template per file, a mount per file, and an environment template that skips the secret file's item.
	jobJSON, _ := json.Marshal(e.nomad.Job("whoami"))
	for _, need := range []string{`secrets/caravana-files/1`, `local/caravana-files/0`, `"readonly":true`, `{{ if not (eq .K \"TLS_KEY\") }}`, `{{ if eq .K \"TLS_KEY\" }}`} {
		if !strings.Contains(string(jobJSON), need) {
			t.Errorf("the job lacks %s:\n%s", need, jobJSON)
		}
	}

	stored, err := e.svc.GetApp(ctx, "whoami")
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.App.Services[0].Files; len(got) != 2 || got[1].Secret != "TLS_KEY" || got[1].Content != "" {
		t.Errorf("stored files = %+v", got)
	}
	_, hcl, err := e.svc.Export(ctx, "whoami", rec.Deployment.ID)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	list, _ := e.store.ListDeployments(ctx, "whoami", 0)
	docs, _ := json.Marshal(list)
	for what, text := range map[string]string{"stored app": mustJSON(stored), "deployments": string(docs), "Nomad job": string(jobJSON),
		"export": string(hcl), "record error": rec.Error, "log": logs.String(), "SQLite file and every row in it": rawStore(t, e.dir)} {
		if strings.Contains(text, filesSentinel) {
			t.Errorf("the %s holds the secret file's value", what)
		}
	}
	if !strings.Contains(string(hcl), `{{ not a template }} $${HOME}`) {
		t.Errorf("the export lacks the content file, escaped for HCL:\n%s", hcl)
	}
}

// A file that names a secret the service doesn't have is refused before anything is deployed.
func TestFileNamingAnUnknownSecretIsRefused(t *testing.T) {
	e := newEnv(t)
	a := withFiles()
	a.Services[0].Files[1].Secret = "NOPE"
	_, err := e.svc.Deploy(context.Background(), a, app.TriggerManual)
	var fe *app.FieldError
	if !errors.As(err, &fe) || fe.Field != "services[0].files[1].secret" {
		t.Errorf("Deploy = %v, want a field error on services[0].files[1].secret", err)
	}
}

// Copy to keeps the files, and the secret a file holds is one of the secrets the copy needs a value for.
func TestCopyKeepsFiles(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	if _, err := e.svc.CreateProject(ctx, "shop", "Shop"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateEnvironment(ctx, "shop", "staging"); err != nil {
		t.Fatal(err)
	}
	a := withFiles()
	a.ID = "shop--production--web"
	deployHealthy(t, e, a)
	copied, missing, err := e.svc.CopyApp(ctx, a.ID, "staging")
	if err != nil {
		t.Fatalf("CopyApp: %v", err)
	}
	if got := copied.App.Services[0].Files; len(got) != 2 || got[0].Path != "/etc/app/app.yml" || got[1].Secret != "TLS_KEY" {
		t.Errorf("copied files = %+v", got)
	}
	var names []string
	for _, m := range missing {
		names = append(names, m.Name)
	}
	if !strings.Contains(strings.Join(names, ","), "TLS_KEY") {
		t.Errorf("missing secrets = %v, want TLS_KEY among them", missing)
	}
}
