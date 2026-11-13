package compose

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// An overlay's backup.restore (feature 036) reaches the app, round-trips
// through JSON, and is checked like the rest of the policy, with the errors
// naming the service's path in the request.
func TestOverlayBackupRestore(t *testing.T) {
	file := "services:\n  kuma:\n    image: traefik/whoami:latest\n    volumes: [\"data:/data\"]\nvolumes:\n  data: {}\n"
	overlay := func(r *app.RestoreSchedule) Request {
		return Request{Compose: file, Services: map[string]Overlay{"kuma": {Backup: &app.Backup{Engine: app.BackupVolume, Restore: r}}}}
	}

	good := &app.RestoreSchedule{Schedule: "0 4 * * *", From: "golden"}
	a, _, err := Translate("shop", overlay(good))
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if got := a.Services[0].Backup.Restore; !reflect.DeepEqual(got, good) {
		t.Errorf("restore = %+v, want %+v", got, good)
	}

	raw, err := json.Marshal(overlay(good).Services["kuma"])
	if err != nil {
		t.Fatal(err)
	}
	var back Overlay
	if err := json.Unmarshal(raw, &back); err != nil || !reflect.DeepEqual(back.Backup.Restore, good) {
		t.Errorf("JSON round trip = %+v (%v) of %s", back.Backup, err, raw)
	}

	for _, tt := range []struct {
		name    string
		restore *app.RestoreSchedule
		field   string
	}{
		{"a schedule that isn't cron", &app.RestoreSchedule{Schedule: "nightly", From: "golden"}, "services.kuma.backup.restore.schedule"},
		{"no schedule", &app.RestoreSchedule{From: "golden"}, "services.kuma.backup.restore.schedule"},
		{"a label in upper case", &app.RestoreSchedule{Schedule: "0 4 * * *", From: "Golden"}, "services.kuma.backup.restore.from"},
		{"no label", &app.RestoreSchedule{Schedule: "0 4 * * *"}, "services.kuma.backup.restore.from"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := Translate("shop", overlay(tt.restore))
			fe, ok := err.(*app.FieldError)
			if !ok || fe.Field != tt.field {
				t.Errorf("Translate = %v, want a field error on %s", err, tt.field)
			}
		})
	}
}
