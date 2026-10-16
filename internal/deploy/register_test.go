package deploy

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/app/apptest"
)

// deployment returns a deployment of whoami with the given ID.
func deployment(id string) app.Deployment {
	return app.Deployment{ID: id, Trigger: app.TriggerManual, Digests: map[string]string{"web": apptest.WhoamiDigest}}
}

// ownsIDs recognizes exactly the given deployment IDs.
func ownsIDs(ids ...string) func(context.Context, string) (bool, error) {
	return func(_ context.Context, id string) (bool, error) {
		for _, known := range ids {
			if id == known {
				return true, nil
			}
		}
		return false, nil
	}
}

func TestRegister(t *testing.T) {
	ctx := context.Background()

	t.Run("new job", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		reg, err := d.Register(ctx, whoami(), deployment("d1"), ownsIDs(), nil)
		if err != nil {
			t.Fatalf("Register() = %v", err)
		}
		if reg.JobVersion == nil || *reg.JobVersion != 0 {
			t.Errorf("JobVersion = %v, want 0", reg.JobVersion)
		}
		job := nomad.Job("whoami")
		if job == nil || job.Meta["caravana.deployment"] != "d1" {
			t.Fatalf("job = %+v, want one carrying deployment d1", job)
		}
		image := job.TaskGroups[0].Tasks[0].Config["image"]
		if image != apptest.WhoamiDigest {
			t.Errorf("image = %v, want the digest %s, never the tag", image, apptest.WhoamiDigest)
		}
	})

	t.Run("new version of our own job", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		if _, err := d.Register(ctx, whoami(), deployment("d1"), ownsIDs(), nil); err != nil {
			t.Fatalf("first Register() = %v", err)
		}
		reg, err := d.Register(ctx, whoami(), deployment("d2"), ownsIDs("d1"), nil)
		if err != nil {
			t.Fatalf("second Register() = %v", err)
		}
		if reg.JobVersion == nil || *reg.JobVersion != 1 {
			t.Errorf("JobVersion = %v, want 1", reg.JobVersion)
		}
		if nomad.Registrations() != 2 {
			t.Errorf("registrations = %d, want 2", nomad.Registrations())
		}
	})

	t.Run("nomad warnings", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.RegisterWarnings("1 warning: group web uses a deprecated field\n")
		reg, err := d.Register(ctx, whoami(), deployment("d1"), ownsIDs(), nil)
		if err != nil {
			t.Fatalf("Register() = %v", err)
		}
		if len(reg.Warnings) != 1 || reg.Warnings[0].Field != "" || !strings.HasPrefix(reg.Warnings[0].Message, "nomad: 1 warning") {
			t.Errorf("warnings = %+v, want one Nomad warning", reg.Warnings)
		}
	})

	t.Run("version can't be read back", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.FailVersions(3)
		reg, err := d.Register(ctx, whoami(), deployment("d1"), ownsIDs(), nil)
		if err != nil {
			t.Fatalf("Register() = %v, want no error: Nomad accepted the job", err)
		}
		if reg.JobVersion != nil {
			t.Errorf("JobVersion = %v, want nil", *reg.JobVersion)
		}
		if nomad.Registrations() != 1 {
			t.Errorf("registrations = %d, want 1", nomad.Registrations())
		}
	})

	t.Run("find version", func(t *testing.T) {
		d, _, _ := newTestDeployer(t)
		if v, found, err := d.FindVersion(ctx, "whoami", "d1"); err != nil || found {
			t.Errorf("FindVersion before any job = %d, %v, %v; want not found", v, found, err)
		}
		for _, id := range []string{"d1", "d2"} {
			if _, err := d.Register(ctx, whoami(), deployment(id), ownsIDs("d1"), nil); err != nil {
				t.Fatalf("Register(%s) = %v", id, err)
			}
		}
		if v, found, err := d.FindVersion(ctx, "whoami", "d1"); err != nil || !found || v != 0 {
			t.Errorf("FindVersion(d1) = %d, %v, %v; want 0", v, found, err)
		}
		if v, found, err := d.FindVersion(ctx, "whoami", "d2"); err != nil || !found || v != 1 {
			t.Errorf("FindVersion(d2) = %d, %v, %v; want 1", v, found, err)
		}
		if _, found, err := d.FindVersion(ctx, "whoami", "d9"); err != nil || found {
			t.Errorf("FindVersion(d9) = %v, %v; want not found", found, err)
		}
	})
}

// labJob is a hand-registered job like deploy/dev/whoami.json: it carries
// Caravana's meta, but with a deployment ID no store has.
func labJob(stopped bool) *api.Job {
	return &api.Job{
		ID:   new("whoami"),
		Name: new("whoami"),
		Stop: new(stopped),
		Meta: map[string]string{"caravana.app": "whoami", "caravana.deployment": "lab-manual"},
	}
}

func TestRegisterErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("foreign job", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.AddForeignJob(labJob(false))
		_, err := d.Register(ctx, whoami(), deployment("d1"), ownsIDs(), nil)
		if !errors.Is(err, ErrJobConflict) || !strings.Contains(err.Error(), `"whoami"`) {
			t.Errorf("Register() = %v, want ErrJobConflict naming the job", err)
		}
		if nomad.Registrations() != 0 || nomad.Job("whoami").Meta["caravana.deployment"] != "lab-manual" {
			t.Errorf("the foreign job was touched")
		}
	})

	t.Run("stopped foreign job is registered over", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.AddForeignJob(labJob(true))
		reg, err := d.Register(ctx, whoami(), deployment("d1"), ownsIDs(), nil)
		if err != nil {
			t.Fatalf("Register() = %v", err)
		}
		if reg.JobVersion == nil || *reg.JobVersion != 1 || nomad.Registrations() != 1 {
			t.Errorf("JobVersion = %v, registrations %d; want version 1 and 1 registration", reg.JobVersion, nomad.Registrations())
		}
	})

	t.Run("job changed during the deploy", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		// owns runs between reading the job and registering: change the
		// job right then, as another client would.
		if _, err := d.Register(ctx, whoami(), deployment("d1"), ownsIDs(), nil); err != nil {
			t.Fatalf("first Register() = %v", err)
		}
		owns := func(ctx context.Context, id string) (bool, error) {
			j := *nomad.Job("whoami")
			nomad.AddForeignJob(&j)
			return true, nil
		}
		_, err := d.Register(ctx, whoami(), deployment("d2"), owns, nil)
		if !errors.Is(err, ErrJobConflict) || !strings.Contains(err.Error(), "changed while deploying") {
			t.Errorf("Register() = %v, want ErrJobConflict (changed while deploying)", err)
		}
	})

	t.Run("nomad rejects the job", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.RejectNext(http.StatusBadRequest, "1 error occurred: task group web: missing driver")
		_, err := d.Register(ctx, whoami(), deployment("d1"), ownsIDs(), nil)
		if !errors.Is(err, ErrNomadRejected) || !strings.Contains(err.Error(), "task group web: missing driver") {
			t.Errorf("Register() = %v, want ErrNomadRejected with Nomad's text", err)
		}
	})

	t.Run("token refused", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.RequireToken("right-token")
		client, err := NewNomadClient(nomad.Addr(), "wrong-s3cr3t")
		if err != nil {
			t.Fatal(err)
		}
		d.nomad = client
		_, err = d.Register(ctx, whoami(), deployment("d1"), ownsIDs(), nil)
		if !errors.Is(err, ErrNomadUnavailable) || !strings.Contains(err.Error(), "refused the token") {
			t.Errorf("Register() = %v, want ErrNomadUnavailable (token refused)", err)
		}
		if strings.Contains(err.Error(), "wrong-s3cr3t") {
			t.Errorf("error %q contains the token", err)
		}
	})

	t.Run("nomad down", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.Down()
		_, err := d.Register(ctx, whoami(), deployment("d1"), ownsIDs(), nil)
		if !errors.Is(err, ErrNomadUnavailable) || !strings.Contains(err.Error(), nomad.Addr()) {
			t.Errorf("Register() = %v, want ErrNomadUnavailable naming the address", err)
		}
	})
}
