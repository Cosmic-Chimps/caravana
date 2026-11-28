package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/registry/registrytest"
	"github.com/cosmic-chimps/caravana/internal/s3/s3test"
	"github.com/cosmic-chimps/caravana/internal/secrets/secretstest"
	"github.com/cosmic-chimps/caravana/internal/selfbackup"
	"github.com/cosmic-chimps/caravana/internal/store"
	"github.com/cosmic-chimps/caravana/internal/store/storetest"
)

// Secrets no answer, error, log line, row, audit entry or file may hold (plan D16).
const (
	selfVariableSentinel = "s3cret-sentinel-063-variable"
	selfDestinationKey   = "s3cret-sentinel-063-destination-key"
)

var (
	selfOwner = Principal{PersonID: "p1", Email: "owner@example.com", OrgRole: OrgOwner}
	selfAdmin = Principal{PersonID: "p2", Email: "admin@example.com", OrgRole: OrgAdmin}
)

type selfEnv struct {
	*env
	s3   *s3test.Server
	logs *lockedBuffer
	own  context.Context // as the owner
	adm  context.Context // as an admin
}

// newSelfEnv is an environment whose service logs into a buffer, with a fake bucket as the default backup
// destination (prefix "caravana").
func newSelfEnv(t *testing.T, withDestination bool) *selfEnv {
	t.Helper()
	dir := storetest.Dir(t)
	st, err := store.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{store: st, dir: dir, registry: registrytest.New(t), nomad: deploytest.New(t), secrets: secretstest.New()}
	e.nomad.Succeed()
	logs := &lockedBuffer{}
	e.svc = startServiceLogging(t, st, newDeployer(t, e.registry, e.nomad), e.secrets, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	srv := s3test.New()
	srv.SecretAccessKey = selfDestinationKey
	t.Cleanup(srv.Close)
	se := &selfEnv{env: e, s3: srv, logs: logs,
		own: WithPrincipal(context.Background(), selfOwner), adm: WithPrincipal(context.Background(), selfAdmin)}
	if withDestination {
		se.addDestination(t)
	}
	return se
}

func (e *selfEnv) addDestination(t *testing.T) {
	t.Helper()
	secret := e.s3.SecretAccessKey
	if _, err := e.svc.SaveBackupDestination(context.Background(), "main", DestinationInput{Endpoint: e.s3.URL, Region: e.s3.Region,
		Bucket: e.s3.Bucket, Prefix: "caravana", AccessKeyID: e.s3.AccessKeyID, SecretAccessKey: &secret}); err != nil {
		t.Fatalf("SaveBackupDestination: %v", err)
	}
}

// makeKey makes the recovery key as the owner and waits for the backup that follows it, if any.
func (e *selfEnv) makeKey(t *testing.T) selfbackup.RecoveryKey {
	t.Helper()
	res, err := e.svc.CreateRecoveryKey(e.own, false)
	if err != nil {
		t.Fatalf("CreateRecoveryKey: %v", err)
	}
	key, err := selfbackup.ParseKey(res.Key)
	if err != nil {
		t.Fatalf("the key it made doesn't parse: %v", err)
	}
	e.waitIdle(t)
	return key
}

// waitIdle waits until no backup is running.
func (e *selfEnv) waitIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, running, err := e.store.OpenSelfBackup(context.Background()); err != nil {
			t.Fatal(err)
		} else if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a backup of Caravana is still running")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// backupNow asks for a backup as an admin, waits for it and returns the run as settled.
func (e *selfEnv) backupNow(t *testing.T) store.SelfBackup {
	t.Helper()
	run, err := e.svc.RequestSelfBackup(e.adm)
	if err != nil {
		t.Fatalf("RequestSelfBackup: %v", err)
	}
	e.waitIdle(t)
	got, err := e.store.GetSelfBackup(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func (e *selfEnv) variables() {
	e.nomad.SetVariable("caravana/acme", map[string]string{"email": "owner@example.com", "eab_hmac": selfVariableSentinel})
	e.nomad.SetVariable("caravana/cluster/ca", map[string]string{"key": "cluster-ca-key"})
	e.nomad.SetVariable("nomad/jobs/web/web", map[string]string{"PASSWORD": selfVariableSentinel})
	e.nomad.SetVariable("nomad/jobs/web/web/web", map[string]string{"CARAVANA_REGISTRY_PASSWORD": "copy"})
	e.nomad.SetVariable("nomad/jobs/traefik/traefik", map[string]string{"hosts": "x"})
}

func notAllowed(err error) bool { return errors.Is(err, ErrNotAllowed) }

func TestSelfBackupIsUnavailableWithoutDestinationOrKey(t *testing.T) {
	e := newSelfEnv(t, false)
	var un *SelfBackupUnavailableError
	if _, err := e.svc.RequestSelfBackup(e.adm); !errors.As(err, &un) || un.Reason != SelfBackupNoDestination {
		t.Fatalf("with nothing: %v", err)
	}
	st, err := e.svc.SelfBackups(e.adm)
	if err != nil || st.Unavailable != SelfBackupNoDestination || st.Destination != "" || st.KeyFingerprint != "" || !st.Settings.Enabled || st.Settings.Hour != 3 || st.Settings.Keep != 14 {
		t.Fatalf("status = %+v, %v", st, err)
	}
	if att, _ := e.svc.Attention(context.Background()); hasSelfKind(att, AttentionSelfBackupNoKey) {
		t.Error("an item was raised with no destination at all")
	}
	e.addDestination(t)
	if _, err := e.svc.RequestSelfBackup(e.adm); !errors.As(err, &un) || un.Reason != SelfBackupNoKey {
		t.Fatalf("with a destination and no key: %v", err)
	}
	if st, _ := e.svc.SelfBackups(e.adm); st.Unavailable != SelfBackupNoKey || st.Destination != "main" {
		t.Errorf("status = %+v", st)
	}
	att, _ := e.svc.Attention(context.Background())
	item, ok := findKind(att, AttentionSelfBackupNoKey)
	if !ok || item.Level != "warning" || item.AppID != "" || !strings.Contains(item.Detail, "recovery key") {
		t.Errorf("item = %+v, %v", item, ok)
	}
	if runs, _ := e.svc.SelfBackupRuns(e.adm); len(runs) != 0 {
		t.Errorf("a refused backup left %d runs", len(runs))
	}
}

func hasSelfKind(a Attention, k AttentionKind) bool { _, ok := findKind(a, k); return ok }

func findKind(a Attention, k AttentionKind) (AttentionItem, bool) {
	for _, it := range a.Items {
		if it.Kind == k {
			return it, true
		}
	}
	return AttentionItem{}, false
}

func TestOnlyAnOwnerMakesTheKeyAndChangesSettings(t *testing.T) {
	e := newSelfEnv(t, true)
	hour := 5
	if _, err := e.svc.CreateRecoveryKey(e.adm, false); !notAllowed(err) {
		t.Errorf("an admin made the key: %v", err)
	}
	if _, err := e.svc.SetSelfBackupSettings(e.adm, SelfBackupSettingsInput{Hour: &hour}); !notAllowed(err) {
		t.Errorf("an admin changed the settings: %v", err)
	}
	member := WithPrincipal(context.Background(), Principal{PersonID: "p3", Email: "m@example.com", OrgRole: OrgMember})
	for name, call := range map[string]func() error{
		"read":     func() error { _, err := e.svc.SelfBackups(member); return err },
		"request":  func() error { _, err := e.svc.RequestSelfBackup(member); return err },
		"list":     func() error { _, err := e.svc.SelfBackupRuns(member); return err },
		"one run":  func() error { _, err := e.svc.SelfBackupRun(member, strings.Repeat("a", 32)); return err },
		"owner op": func() error { _, err := e.svc.CreateRecoveryKey(member, false); return err },
	} {
		if err := call(); !notAllowed(err) {
			t.Errorf("a member: %s = %v, want not allowed", name, err)
		}
	}
	// An admin reads and requests (once there is a key).
	if _, err := e.svc.SelfBackups(e.adm); err != nil {
		t.Errorf("an admin read: %v", err)
	}
}

func TestCreateRecoveryKey(t *testing.T) {
	e := newSelfEnv(t, true)
	res, err := e.svc.CreateRecoveryKey(e.own, false)
	if err != nil {
		t.Fatal(err)
	}
	key, err := selfbackup.ParseKey(res.Key)
	if err != nil || res.Fingerprint != key.Fingerprint() || len(res.Fingerprint) != 16 {
		t.Fatalf("result = %+v, %v", res, err)
	}
	e.waitIdle(t)
	st, _ := e.svc.SelfBackups(e.adm)
	if st.KeyFingerprint != res.Fingerprint || st.KeyCreatedAt.IsZero() || st.Unavailable != "" {
		t.Errorf("status = %+v", st)
	}
	// The key made a backup at once, triggered by the key.
	runs, _ := e.svc.SelfBackupRuns(e.adm)
	if len(runs) != 1 || runs[0].Trigger != store.SelfBackupByKey || runs[0].Status != store.SelfBackupSucceeded || runs[0].Actor != "owner@example.com" {
		t.Fatalf("runs = %+v", runs)
	}
	// A second key needs replace.
	if _, err := e.svc.CreateRecoveryKey(e.own, false); !errors.Is(err, ErrRecoveryKeyExists) {
		t.Errorf("a second key without replace: %v", err)
	}
	first := res.Fingerprint
	res2, err := e.svc.CreateRecoveryKey(e.own, true)
	if err != nil || res2.Fingerprint == first || res2.Key == res.Key {
		t.Fatalf("replace: %+v, %v", res2, err)
	}
	e.waitIdle(t)
	// Each backup names the key that made it; the old backup still opens with the old key only.
	runs, _ = e.svc.SelfBackupRuns(e.adm)
	if len(runs) != 2 || runs[0].KeyFingerprint != res2.Fingerprint || runs[1].KeyFingerprint != first {
		t.Fatalf("runs = %+v", runs)
	}
	newKey, _ := selfbackup.ParseKey(res2.Key)
	for _, c := range []struct {
		run store.SelfBackup
		key selfbackup.RecoveryKey
		ok  bool
	}{{runs[0], newKey, true}, {runs[0], key, false}, {runs[1], key, true}, {runs[1], newKey, false}} {
		data, _ := e.s3.Object(c.run.Object)
		_, _, _, _, err := selfbackup.ReadAll(bytes.NewReader(data), c.key, 1<<30)
		if (err == nil) != c.ok {
			t.Errorf("backup %s with key %s: err %v, want ok=%v", c.run.KeyFingerprint, c.key.Fingerprint(), err, c.ok)
		}
	}
}

func TestSelfBackupRoundTrip(t *testing.T) {
	e := newSelfEnv(t, true)
	e.variables()
	key := e.makeKey(t)
	// The key's own backup ran with these variables; ask for one more and read it.
	run := e.backupNow(t)
	if run.Status != store.SelfBackupSucceeded || run.Error != "" || run.Trigger != store.SelfBackupByOwner || run.Actor != "admin@example.com" ||
		run.Destination != "main" || run.KeyFingerprint != key.Fingerprint() || run.Bytes == 0 || run.Schema < 49 {
		t.Fatalf("run = %+v", run)
	}
	wantKey := selfbackup.ObjectKey("caravana", run.StartedAt.Unix())
	if run.Object != wantKey || !strings.HasPrefix(run.Object, "caravana/_caravana/") {
		t.Errorf("object = %q, want %q", run.Object, wantKey)
	}
	data, ok := e.s3.Object(run.Object)
	if !ok || int64(len(data)) != run.Bytes {
		t.Fatalf("the bucket holds %d bytes (found %v), the run says %d", len(data), ok, run.Bytes)
	}
	// Nothing readable: not a variable's secret, not the destination's key, not the recovery key.
	for _, s := range []string{selfVariableSentinel, e.s3.SecretAccessKey, "nomad/jobs/web/web", "CREATE TABLE"} {
		if bytes.Contains(data, []byte(s)) {
			t.Errorf("the backup holds %q in the clear", s)
		}
	}
	hdr, err := selfbackup.ReadHeader(bytes.NewReader(data))
	if err != nil || hdr.KeyFingerprint != key.Fingerprint() || hdr.Schema != run.Schema {
		t.Fatalf("header = %+v, %v", hdr, err)
	}
	h, man, vars, db, err := selfbackup.ReadAll(bytes.NewReader(data), key, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	_ = h
	var paths []string
	for _, v := range vars {
		paths = append(paths, v.Path)
	}
	// The destination's own secret key is a variable under caravana/: a restored Caravana needs it to reach
	// the backups of its apps. The cluster's, a registry copy and Traefik's are not in it.
	if strings.Join(paths, ",") != "caravana/acme,caravana/backup-destinations/main,nomad/jobs/web/web" || vars[2].Items["PASSWORD"] != selfVariableSentinel ||
		vars[1].Items["secretAccessKey"] != e.s3.SecretAccessKey {
		t.Errorf("variables = %v", paths)
	}
	if man.Variables != 3 || man.Apps != 0 {
		t.Errorf("manifest = %+v", man)
	}
	// The database in it is a sound database with the destination in it.
	copyPath := filepath.Join(t.TempDir(), "restored.db")
	if err := os.WriteFile(copyPath, db, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.QuickCheckFile(context.Background(), copyPath); err != nil {
		t.Errorf("the database in the backup: %v", err)
	}
	// No temporary file is left in the data folder.
	entries, _ := os.ReadDir(e.dir)
	for _, en := range entries {
		if strings.HasPrefix(en.Name(), "self-backup-") {
			t.Errorf("left behind: %s", en.Name())
		}
	}
	// The run is in the list and readable by ID, and the latest is the one.
	got, err := e.svc.SelfBackupRun(e.adm, run.ID)
	if err != nil || got.ID != run.ID {
		t.Errorf("SelfBackupRun = %+v, %v", got, err)
	}
	if _, err := e.svc.SelfBackupRun(e.adm, strings.Repeat("0", 32)); !errors.Is(err, ErrSelfBackupNotFound) {
		t.Errorf("an unknown run: %v", err)
	}
	st, _ := e.svc.SelfBackups(e.adm)
	if st.Last == nil || st.Last.ID != run.ID || st.LastSucceeded == nil || st.Running != nil {
		t.Errorf("status = %+v", st)
	}
}

func TestSelfBackupsOfOneSecondDoNotCollide(t *testing.T) {
	e := newSelfEnv(t, true)
	e.makeKey(t)
	a := e.backupNow(t)
	b := e.backupNow(t) // the fake clock didn't move
	if a.Object == b.Object || b.Status != store.SelfBackupSucceeded || !b.StartedAt.After(a.StartedAt) {
		t.Errorf("a %q at %v, b %q at %v", a.Object, a.StartedAt, b.Object, b.StartedAt)
	}
	if got := len(e.s3.Keys()); got != 3 { // the key's backup and these two
		t.Errorf("the bucket holds %d objects: %v", got, e.s3.Keys())
	}
}

func TestSelfBackupRetention(t *testing.T) {
	e := newSelfEnv(t, true)
	keep := 3
	if _, err := e.svc.SetSelfBackupSettings(e.own, SelfBackupSettingsInput{Keep: &keep}); err != nil {
		t.Fatal(err)
	}
	// Older backups, one that doesn't read as a backup's key, and an app's file beside them.
	for _, ts := range []int64{100, 200, 300, 400, 500} {
		e.s3.Put(selfbackup.ObjectKey("caravana", ts), []byte("old"))
	}
	for _, other := range []string{"caravana/_caravana/notes.txt", "caravana/_caravana/9/caravana-8.cvbackup", "caravana/web/db/1.dump", "other/_caravana/1/caravana-1.cvbackup"} {
		e.s3.Put(other, []byte("not ours to delete"))
	}
	e.makeKey(t) // its backup is the newest; keep 3 leaves it and the two before it
	var left []string
	for _, k := range e.s3.Keys() {
		if ts, ok := selfbackup.ParseObjectKey("caravana", k); ok {
			left = append(left, k)
			if ts != 400 && ts != 500 && ts < 1_000_000 {
				t.Errorf("the old backup %d should be gone", ts)
			}
		}
	}
	if len(left) != 3 {
		t.Errorf("%d backups left: %v", len(left), left)
	}
	for _, other := range []string{"caravana/_caravana/notes.txt", "caravana/_caravana/9/caravana-8.cvbackup", "caravana/web/db/1.dump", "other/_caravana/1/caravana-1.cvbackup"} {
		if _, ok := e.s3.Object(other); !ok {
			t.Errorf("retention deleted %s", other)
		}
	}

	// A delete that fails is a warning on a run that succeeded; so is a listing that fails.
	e.s3.Fail("DELETE", 403)
	run := e.backupNow(t)
	if run.Status != store.SelfBackupSucceeded || !strings.Contains(run.Warning, "could not be deleted") {
		t.Errorf("a failed delete: %+v", run)
	}
	e.s3.Fail("DELETE", 0)
	e.s3.Fail("LIST", 500)
	run = e.backupNow(t)
	if run.Status != store.SelfBackupSucceeded || !strings.Contains(run.Warning, "could not be listed") {
		t.Errorf("a failed listing: %+v", run)
	}
}

func TestSelfBackupFailureSaysWhyAndGoesWhenFixed(t *testing.T) {
	e := newSelfEnv(t, true)
	e.makeKey(t)

	e.s3.Fail("PUT", 403)
	run := e.backupNow(t)
	if run.Status != store.SelfBackupFailed || !strings.Contains(run.Error, "403") || !strings.Contains(run.Error, "AccessDenied") || run.Object != "" {
		t.Fatalf("a refused upload: %+v", run)
	}
	for _, leak := range []string{e.s3.URL, e.s3.SecretAccessKey, e.s3.AccessKeyID, "must not repeat"} {
		if strings.Contains(run.Error, leak) {
			t.Errorf("the reason says %q: %s", leak, run.Error)
		}
	}
	att, _ := e.svc.Attention(context.Background())
	item, ok := findKind(att, AttentionSelfBackupFailed)
	if !ok || item.Level != "failed" || item.Detail != run.Error {
		t.Fatalf("item = %+v, %v", item, ok)
	}
	if sentence := plainText(ProblemParts(item, func(s string) string { return s })); !strings.HasPrefix(sentence, "The latest backup of Caravana itself failed: ") {
		t.Errorf("sentence = %q", sentence)
	}
	entries, _ := os.ReadDir(e.dir)
	for _, en := range entries {
		if strings.HasPrefix(en.Name(), "self-backup-") {
			t.Errorf("a failed backup left %s", en.Name())
		}
	}

	// Nomad's variables unreadable: the run says a step failed and points at the log; Nomad's words are not in it.
	e.s3.Fail("PUT", 0)
	e.nomad.FailVariables(500)
	run = e.backupNow(t)
	if run.Status != store.SelfBackupFailed || !strings.HasSuffix(run.Error, "(see Caravana's log).") || strings.Contains(run.Error, "500") {
		t.Errorf("variables unreadable: %+v", run)
	}
	e.nomad.FailVariables(0)

	// A good backup makes the item go.
	if run = e.backupNow(t); run.Status != store.SelfBackupSucceeded {
		t.Fatalf("the good backup: %+v", run)
	}
	if att, _ = e.svc.Attention(context.Background()); hasSelfKind(att, AttentionSelfBackupFailed) {
		t.Error("the failed item stayed after a good backup")
	}
}

func TestOnlyOneSelfBackupRunsAtATime(t *testing.T) {
	e := newSelfEnv(t, true)
	e.makeKey(t)
	// A run already running (a process left it, or it is in flight) refuses the next.
	if _, err := e.store.InsertSelfBackup(context.Background(), store.SelfBackup{ID: strings.Repeat("e", 32), Trigger: store.SelfBackupByOwner,
		StartedAt: e.svc.clock.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RequestSelfBackup(e.adm); !errors.Is(err, ErrSelfBackupRunning) {
		t.Errorf("a second backup: %v", err)
	}
	// A restart settles it: failed, in words.
	if err := e.svc.failInterruptedSelfBackups(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := e.store.GetSelfBackup(context.Background(), strings.Repeat("e", 32))
	if got.Status != store.SelfBackupFailed || got.Error != "Caravana restarted during the backup." {
		t.Errorf("settled = %+v", got)
	}
}

func TestDailyBackupIsDueOncePerDayAtItsHour(t *testing.T) {
	e := newSelfEnv(t, true)
	e.makeKey(t)
	clock := e.svc.clock.(*fakeClock)
	day := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	due := func(at time.Time) bool {
		t.Helper()
		clock.Set(at)
		d, err := e.svc.selfBackupDue(context.Background(), at)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	if due(day.Add(2*time.Hour + 59*time.Minute)) {
		t.Error("due before the hour")
	}
	if !due(day.Add(3 * time.Hour)) {
		t.Fatal("not due at the hour")
	}
	// The tick starts it: trigger schedule, nobody as actor.
	clock.Set(day.Add(3 * time.Hour))
	e.svc.selfBackupTick(context.Background())
	e.waitIdle(t)
	runs, _ := e.svc.SelfBackupRuns(e.adm)
	var scheduled []store.SelfBackup
	for _, r := range runs {
		if r.Trigger == store.SelfBackupBySchedule {
			scheduled = append(scheduled, r)
		}
	}
	if len(scheduled) != 1 || scheduled[0].Actor != "" || scheduled[0].Status != store.SelfBackupSucceeded {
		t.Fatalf("scheduled runs = %+v", scheduled)
	}
	if due(day.Add(12 * time.Hour)) {
		t.Error("due again the same day")
	}
	if !due(day.Add(24*time.Hour + 3*time.Hour)) {
		t.Error("not due the next day")
	}
	// A failed run spends the slot too (no retry the same day).
	e.s3.Fail("PUT", 500)
	clock.Set(day.Add(27 * time.Hour))
	e.svc.selfBackupTick(context.Background())
	e.waitIdle(t)
	if due(day.Add(28 * time.Hour)) {
		t.Error("a failed scheduled run was tried again the same day")
	}
	e.s3.Fail("PUT", 0)

	// Off, or no key, or another hour: not due.
	off := false
	if _, err := e.svc.SetSelfBackupSettings(e.own, SelfBackupSettingsInput{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if due(day.Add(48*time.Hour + 5*time.Hour)) {
		t.Error("due while turned off")
	}
	on, hour := true, 20
	e.svc.SetSelfBackupSettings(e.own, SelfBackupSettingsInput{Enabled: &on, Hour: &hour})
	if due(day.Add(48*time.Hour+5*time.Hour)) || !due(day.Add(48*time.Hour+20*time.Hour)) {
		t.Error("the hour setting isn't followed")
	}
}

func TestSelfBackupOldItem(t *testing.T) {
	e := newSelfEnv(t, true)
	ctx := context.Background()
	clock := e.svc.clock.(*fakeClock)
	now := clock.Now()
	// A key made 49 hours ago and no backup since: out of date, none ever.
	if err := e.store.SetSelfBackupKey(ctx, bytes.Repeat([]byte{1}, 32), "00112233445566ff", now.Add(-49*time.Hour)); err != nil {
		t.Fatal(err)
	}
	items := func() []AttentionItem {
		t.Helper()
		att, err := e.svc.Attention(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []AttentionItem
		for _, it := range att.Items {
			if it.Kind == AttentionSelfBackupOld {
				out = append(out, it)
			}
		}
		return out
	}
	got := items()
	if len(got) != 1 || got[0].SelfBackupHours != 0 || got[0].Level != "warning" || !strings.Contains(got[0].Detail, "has not backed itself up") {
		t.Fatalf("none ever: %+v", got)
	}
	// 47 hours: not yet.
	if err := e.store.SetSelfBackupKey(ctx, bytes.Repeat([]byte{1}, 32), "00112233445566ff", now.Add(-47*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := items(); len(got) != 0 {
		t.Errorf("47 hours: %+v", got)
	}
	// A good backup 50 hours ago: out of date, in hours, then in days.
	good := func(at time.Time) {
		t.Helper()
		id := strings.Repeat("a", 31) + string(rune('0'+at.Hour()%10))
		if _, err := e.store.InsertSelfBackup(ctx, store.SelfBackup{ID: id, Trigger: store.SelfBackupBySchedule, StartedAt: at}); err != nil {
			t.Fatal(err)
		}
		if err := e.store.FinishSelfBackup(ctx, id, store.SelfBackupSucceeded, at.Add(time.Minute), store.SelfBackupResult{}); err != nil {
			t.Fatal(err)
		}
	}
	good(now.Add(-50 * time.Hour))
	got = items()
	if len(got) != 1 || got[0].SelfBackupHours != 49 && got[0].SelfBackupHours != 50 || !strings.Contains(got[0].Detail, "hours ago") {
		t.Fatalf("50 hours: %+v", got)
	}
	clock.Set(now.Add(5 * 24 * time.Hour))
	got = items()
	if len(got) != 1 || !strings.Contains(got[0].Detail, "days ago") {
		t.Fatalf("a week: %+v", got)
	}
	// Turned off is the owner's choice: no item.
	off := false
	if _, err := e.svc.SetSelfBackupSettings(e.own, SelfBackupSettingsInput{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if got := items(); len(got) != 0 {
		t.Errorf("turned off: %+v", got)
	}
}

func TestSelfBackupSettingsAreValidatedAndAudited(t *testing.T) {
	e := newSelfEnv(t, true)
	for field, in := range map[string]SelfBackupSettingsInput{
		"hour": {Hour: new(24)}, "keep": {Keep: new(0)},
	} {
		_, err := e.svc.SetSelfBackupSettings(e.own, in)
		var fe *app.FieldError
		if !errors.As(err, &fe) || fe.Field != field {
			t.Errorf("%s: err = %v", field, err)
		}
	}
	if _, err := e.svc.SetSelfBackupSettings(e.own, SelfBackupSettingsInput{Keep: new(91)}); err == nil {
		t.Error("keep 91 accepted")
	}
	before := len(auditEntries(t, e.store))
	st, err := e.svc.SetSelfBackupSettings(e.own, SelfBackupSettingsInput{Hour: new(22), Keep: new(7), Enabled: new(false)})
	if err != nil || st.Settings != (SelfBackupSettings{Enabled: false, Hour: 22, Keep: 7}) {
		t.Fatalf("settings = %+v, %v", st, err)
	}
	entries := auditEntries(t, e.store)
	if len(entries) != before+1 || entries[0].Resource.Kind != audit.KindSettings || entries[0].Resource.ID != "self-backup" || entries[0].Action != audit.ActionUpdated || len(entries[0].Changes) != 3 {
		t.Fatalf("entries = %+v", entries)
	}
	// The same values again change nothing and note nothing.
	if _, err := e.svc.SetSelfBackupSettings(e.own, SelfBackupSettingsInput{Hour: new(22)}); err != nil {
		t.Fatal(err)
	}
	if got := len(auditEntries(t, e.store)); got != before+1 {
		t.Errorf("an unchanged save was noted: %d entries", got)
	}
}

func auditEntries(t *testing.T, st *store.Store) []audit.Event {
	t.Helper()
	got, _, err := st.ListAuditEvents(context.Background(), store.AuditFilter{Limit: store.MaxAuditLimit})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestSelfBackupAuditNotesTheKeyAndTheRequestNotTheClock(t *testing.T) {
	e := newSelfEnv(t, true)
	res, err := e.svc.CreateRecoveryKey(e.own, false)
	if err != nil {
		t.Fatal(err)
	}
	e.waitIdle(t)
	run := e.backupNow(t)
	var key, requested *audit.Event
	for _, en := range auditEntries(t, e.store) {
		en := en
		if en.Resource.Kind == audit.KindSettings && en.Resource.ID == "self-backup" {
			switch en.Action {
			case audit.ActionCreated:
				key = &en
			case audit.ActionRequested:
				requested = &en
			}
		}
	}
	if key == nil || len(key.Changes) != 1 || key.Changes[0].Path != "recoveryKey" || !strings.Contains(key.Changes[0].To, res.Fingerprint) || key.Actor.Email != "owner@example.com" {
		t.Fatalf("the key's entry = %+v", key)
	}
	if requested == nil || requested.Record == nil || requested.Record.Kind != audit.RecordSelfBackup || requested.Record.ID != run.ID || requested.Actor.Email != "admin@example.com" {
		t.Fatalf("the request's entry = %+v", requested)
	}
	// A scheduled run adds no entry.
	clock := e.svc.clock.(*fakeClock)
	n := len(auditEntries(t, e.store))
	clock.Set(time.Date(2026, 10, 17, 4, 0, 0, 0, time.UTC))
	e.svc.selfBackupTick(context.Background())
	e.waitIdle(t)
	if got := len(auditEntries(t, e.store)); got != n {
		t.Errorf("the clock's backup was noted: %d entries, was %d", got, n)
	}
}

// No secret reaches an answer, a row, an audit entry, a file of the data folder or the log (plan D16).
func TestSelfBackupSentinels(t *testing.T) {
	e := newSelfEnv(t, true)
	e.variables()
	res, err := e.svc.CreateRecoveryKey(e.own, false)
	if err != nil {
		t.Fatal(err)
	}
	e.waitIdle(t)
	e.s3.Fail("PUT", 500)
	failed := e.backupNow(t) // a failed run's words are searched too
	e.s3.Fail("PUT", 0)
	ok := e.backupNow(t)

	secrets := []string{res.Key, strings.TrimPrefix(res.Key, "cvrk_"), selfVariableSentinel, e.s3.SecretAccessKey}
	var haystacks = map[string]string{"log": e.logs.String()}
	for _, r := range []store.SelfBackup{failed, ok} {
		haystacks["run "+r.ID] = strings.Join([]string{r.Error, r.Warning, r.Object, r.Destination, r.Actor}, "\n")
	}
	st, _ := e.svc.SelfBackups(e.adm)
	haystacks["status"] = st.KeyFingerprint + st.Destination
	for _, en := range auditEntries(t, e.store) {
		b, _ := json.Marshal(en)
		haystacks["audit"] += string(b)
	}
	files, _ := filepath.Glob(filepath.Join(e.dir, "caravana.db*"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		haystacks[filepath.Base(f)] = string(b)
	}
	// The destination's secret key is in a Nomad Variable by design, never in SQLite; the others are nowhere.
	for name, hay := range haystacks {
		for _, s := range secrets {
			if strings.Contains(hay, s) {
				t.Errorf("%s holds %q", name, s)
			}
		}
	}
	att, _ := e.svc.Attention(context.Background())
	for _, it := range att.Items {
		for _, s := range secrets {
			if strings.Contains(it.Detail, s) {
				t.Errorf("an attention item holds %q", s)
			}
		}
	}
}
