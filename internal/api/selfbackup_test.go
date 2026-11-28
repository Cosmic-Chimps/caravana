package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/s3/s3test"
	"github.com/cosmic-chimps/caravana/internal/selfbackup"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// Feature 063: Caravana's own backup through the API. The test walks the whole flow as the owner (the
// state with nothing set up, the settings, the key made once, a backup now and the runs), checks every
// JSON against the contract, and searches everything the API said, its log and its data folder for the
// recovery key and the secrets the backup holds.

type selfBackupsAnswer struct {
	Settings struct {
		Enabled bool `json:"enabled"`
		Hour    int  `json:"hour"`
		Keep    int  `json:"keep"`
	} `json:"settings"`
	Key *struct {
		Fingerprint string    `json:"fingerprint"`
		CreatedAt   time.Time `json:"createdAt"`
	} `json:"key"`
	Destination *string          `json:"destination"`
	Unavailable *string          `json:"unavailable"`
	Running     *json.RawMessage `json:"running"`
	Last        *selfRunAnswer   `json:"last"`
}

type selfRunAnswer struct {
	ID             string     `json:"id"`
	Status         string     `json:"status"`
	Trigger        string     `json:"trigger"`
	Actor          string     `json:"actor"`
	StartedAt      time.Time  `json:"startedAt"`
	EndedAt        *time.Time `json:"endedAt"`
	Destination    string     `json:"destination"`
	Object         string     `json:"object"`
	Bytes          int64      `json:"bytes"`
	KeyFingerprint string     `json:"keyFingerprint"`
	Error          string     `json:"error"`
}

func TestSelfBackupThroughTheAPI(t *testing.T) {
	ctx := context.Background()
	e := newTestEnvWith(t, "", func(svc *service.Service) { svc.SetSelfBackupPassGap(time.Hour) })
	doc := loadContract(t)
	srv := s3test.New()
	srv.SecretAccessKey = "s3cret-sentinel-063-api-destination"
	t.Cleanup(srv.Close)
	e.Nomad.SetVariable("nomad/jobs/web/web", map[string]string{"PASSWORD": "s3cret-sentinel-063-api-variable"})

	said := []string{}
	do := func(method, path, body string) (int, http.Header, []byte) {
		t.Helper()
		status, hdr, raw := call(t, method, e.URL+path, "application/json", body)
		said = append(said, string(raw))
		if hdr.Get("Cache-Control") != "no-store" && status < 400 {
			t.Errorf("%s %s: Cache-Control = %q, want no-store", method, path, hdr.Get("Cache-Control"))
		}
		return status, hdr, raw
	}
	state := func() selfBackupsAnswer {
		t.Helper()
		status, _, raw := do(http.MethodGet, "/v1/self-backups", "")
		if status != http.StatusOK {
			t.Fatalf("getSelfBackups = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "SelfBackups")
		return decodeAs[selfBackupsAnswer](t, raw)
	}
	waitIdle := func() selfBackupsAnswer {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			st := state()
			if st.Running == nil || string(*st.Running) == "null" {
				return st
			}
			if time.Now().After(deadline) {
				t.Fatal("the backup never ended")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	// A new installation: nothing set up.
	st := state()
	if st.Settings.Hour != 3 || st.Settings.Keep != 14 || !st.Settings.Enabled || st.Key != nil || st.Destination != nil || st.Unavailable == nil || *st.Unavailable != "no_destination" || st.Last != nil {
		t.Fatalf("a new installation: %+v", st)
	}
	status, _, raw := do(http.MethodPost, "/v1/self-backups/runs", "")
	wantError(t, "a backup with no destination", status, raw, http.StatusConflict, codeSelfBackupUnavailable, "")

	// The settings: refusals name their field, a change answers the state.
	status, _, raw = do(http.MethodPut, "/v1/self-backups/settings", `{"hour":24}`)
	wantError(t, "hour 24", status, raw, http.StatusUnprocessableEntity, codeInvalidField, "hour")
	status, _, raw = do(http.MethodPut, "/v1/self-backups/settings", `{"keep":91}`)
	wantError(t, "keep 91", status, raw, http.StatusUnprocessableEntity, codeInvalidField, "keep")
	status, _, raw = do(http.MethodPut, "/v1/self-backups/settings", `{"nope":1}`)
	wantError(t, "an unknown property", status, raw, http.StatusBadRequest, codeUnknownField, "nope")
	status, _, raw = do(http.MethodPut, "/v1/self-backups/settings", `{"hour":5,"keep":7,"enabled":true}`)
	if status != http.StatusOK {
		t.Fatalf("putSelfBackupSettings = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "SelfBackups")
	if got := decodeAs[selfBackupsAnswer](t, raw); got.Settings.Hour != 5 || got.Settings.Keep != 7 {
		t.Errorf("settings = %+v", got.Settings)
	}

	// A destination, and still no key.
	secret := srv.SecretAccessKey
	if _, err := e.Service.SaveBackupDestination(ctx, "main", service.DestinationInput{Endpoint: srv.URL, Region: srv.Region, Bucket: srv.Bucket,
		Prefix: "caravana", AccessKeyID: srv.AccessKeyID, SecretAccessKey: &secret}); err != nil {
		t.Fatal(err)
	}
	st = state()
	if st.Destination == nil || *st.Destination != "main" || st.Unavailable == nil || *st.Unavailable != "no_key" {
		t.Fatalf("with a destination: %+v", st)
	}
	status, _, raw = do(http.MethodPost, "/v1/self-backups/runs", "")
	if body := wantError(t, "a backup with no key", status, raw, http.StatusConflict, codeSelfBackupUnavailable, ""); !strings.Contains(body.Error.Message, "recovery key") {
		t.Errorf("message = %q", body.Error.Message)
	}

	// The key: once, in a no-store answer; a second needs replace.
	status, hdr, raw := do(http.MethodPost, "/v1/self-backups/recovery-key", "")
	if status != http.StatusCreated || hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("createRecoveryKey = %d %s %v", status, raw, hdr)
	}
	var made struct {
		RecoveryKey string `json:"recoveryKey"`
		Fingerprint string `json:"fingerprint"`
	}
	if err := json.Unmarshal(raw, &made); err != nil || !regexp.MustCompile(`^cvrk_[a-z2-7]{58}$`).MatchString(made.RecoveryKey) || len(made.Fingerprint) != 16 {
		t.Fatalf("the answer = %s", raw)
	}
	key, err := selfbackup.ParseKey(made.RecoveryKey)
	if err != nil || key.Fingerprint() != made.Fingerprint {
		t.Fatalf("the key doesn't parse: %v", err)
	}
	keyAnswer := said[len(said)-1]
	status, _, raw = do(http.MethodPost, "/v1/self-backups/recovery-key", "")
	wantError(t, "a second key", status, raw, http.StatusConflict, codeRecoveryKeyExists, "")
	status, _, raw = do(http.MethodPost, "/v1/self-backups/recovery-key", `{"replace":"yes"}`)
	if status != http.StatusBadRequest {
		t.Errorf("replace as a string = %d %s", status, raw)
	}
	st = waitIdle() // the key's own backup
	if st.Key == nil || st.Key.Fingerprint != made.Fingerprint || st.Unavailable != nil || st.Last == nil || st.Last.Status != "succeeded" || st.Last.Trigger != "key" {
		t.Fatalf("after the key: %+v", st)
	}

	// Back up now: 202, the Location, the run read by its ID, and in the list.
	status, hdr, raw = do(http.MethodPost, "/v1/self-backups/runs", "")
	if status != http.StatusAccepted {
		t.Fatalf("startSelfBackup = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "SelfBackupRun")
	run := decodeAs[selfRunAnswer](t, raw)
	if run.Status != "running" || run.Trigger != "owner" || run.Actor != testAdminEmail || run.EndedAt != nil || hdr.Get("Location") != "/v1/self-backups/runs/"+run.ID {
		t.Errorf("run = %+v, Location %q", run, hdr.Get("Location"))
	}
	st = waitIdle()
	status, _, raw = do(http.MethodGet, "/v1/self-backups/runs/"+run.ID, "")
	if status != http.StatusOK {
		t.Fatalf("getSelfBackupRun = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "SelfBackupRun")
	done := decodeAs[selfRunAnswer](t, raw)
	if done.Status != "succeeded" || done.EndedAt == nil || done.Bytes == 0 || done.KeyFingerprint != made.Fingerprint || !strings.HasPrefix(done.Object, "caravana/_caravana/") || done.Error != "" {
		t.Fatalf("the run = %+v", done)
	}
	if _, ok := srv.Object(done.Object); !ok {
		t.Errorf("the bucket has no %s: %v", done.Object, srv.Keys())
	}
	status, _, raw = do(http.MethodGet, "/v1/self-backups/runs", "")
	if status != http.StatusOK {
		t.Fatalf("listSelfBackupRuns = %d %s", status, raw)
	}
	var list struct {
		Runs []json.RawMessage `json:"runs"`
	}
	if err := json.Unmarshal(raw, &list); err != nil || len(list.Runs) != 2 {
		t.Fatalf("runs = %s", raw)
	}
	for _, r := range list.Runs {
		checkSchema(t, doc, r, "SelfBackupRun")
	}
	status, _, raw = do(http.MethodGet, "/v1/self-backups/runs/"+strings.Repeat("0", 32), "")
	wantError(t, "an unknown run", status, raw, http.StatusNotFound, codeSelfBackupNotFound, "id")

	// A replacement key: the old backup names the old key, the new one the new.
	status, _, raw = do(http.MethodPost, "/v1/self-backups/recovery-key", `{"replace":true}`)
	if status != http.StatusCreated {
		t.Fatalf("replace = %d %s", status, raw)
	}
	waitIdle()

	// The key is in the two answers that made it and nowhere else: not another answer, the log or a file.
	for i, text := range said {
		if text != keyAnswer && strings.Contains(text, made.RecoveryKey) {
			t.Errorf("answer %d holds the first key: %.200s", i, text)
		}
		for _, secret := range []string{"s3cret-sentinel-063-api-destination", "s3cret-sentinel-063-api-variable", strings.TrimPrefix(made.RecoveryKey, "cvrk_")} {
			if text != keyAnswer && strings.Contains(text, secret) {
				t.Errorf("answer %d holds %q", i, secret)
			}
		}
	}
	for _, secret := range []string{made.RecoveryKey, "s3cret-sentinel-063-api-destination", "s3cret-sentinel-063-api-variable"} {
		if strings.Contains(e.Logs.String(), secret) {
			t.Errorf("the log holds %q", secret)
		}
		files, _ := filepath.Glob(filepath.Join(e.Dir, "caravana.db*"))
		for _, f := range files {
			if b, _ := os.ReadFile(f); strings.Contains(string(b), secret) {
				t.Errorf("%s holds %q", filepath.Base(f), secret)
			}
		}
	}
}

// Every route needs a credential.
func TestSelfBackupRoutesNeedCredentials(t *testing.T) {
	e := newTestEnv(t, "")
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/v1/self-backups"},
		{http.MethodPut, "/v1/self-backups/settings"},
		{http.MethodPost, "/v1/self-backups/recovery-key"},
		{http.MethodPost, "/v1/self-backups/runs"},
		{http.MethodGet, "/v1/self-backups/runs"},
		{http.MethodGet, "/v1/self-backups/runs/" + strings.Repeat("a", 32)},
	} {
		status, _, raw := call(t, c.method, e.RawURL+c.path, "application/json", `{}`)
		wantError(t, c.method+" "+c.path+" without credentials", status, raw, http.StatusUnauthorized, codeUnauthorized, "")
	}
}
