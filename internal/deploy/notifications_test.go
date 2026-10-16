package deploy

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// notificationSecret is a value no error may contain.
const notificationSecret = "https://hooks.slack.com/services/SENTINEL-notify-secret"

func TestNotificationVariablePath(t *testing.T) {
	got := NotificationVariablePath("nch_0123456789ab")
	if got != "caravana/notifications/nch_0123456789ab" {
		t.Errorf("NotificationVariablePath() = %q", got)
	}
	if !nomadPath.MatchString(got) {
		t.Errorf("%q is not a legal Nomad variable path", got)
	}
}

func TestNotificationSecrets(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	const id = "nch_0123456789ab"

	t.Run("a channel with none has an empty set", func(t *testing.T) {
		got, err := d.NotificationSecret(ctx, id)
		if err != nil || len(got) != 0 {
			t.Errorf("NotificationSecret() before a save = %v, %v; want empty, nil", got, err)
		}
	})

	t.Run("save, read, replace", func(t *testing.T) {
		if err := d.SaveNotificationSecret(ctx, id, map[string]string{"url": notificationSecret}); err != nil {
			t.Fatalf("SaveNotificationSecret() = %v", err)
		}
		if got := nomad.VariablePaths(); len(got) != 1 || got[0] != "caravana/notifications/"+id {
			t.Errorf("variables = %v, want the one channel variable", got)
		}
		got, err := d.NotificationSecret(ctx, id)
		if err != nil || !reflect.DeepEqual(got, map[string]string{"url": notificationSecret}) {
			t.Errorf("NotificationSecret() = %v, %v", got, err)
		}

		// A second save replaces the items, it does not merge them.
		if err := d.SaveNotificationSecret(ctx, id, map[string]string{"token": "tk_new"}); err != nil {
			t.Fatal(err)
		}
		got, _ = d.NotificationSecret(ctx, id)
		if !reflect.DeepEqual(got, map[string]string{"token": "tk_new"}) {
			t.Errorf("NotificationSecret() after a second save = %v, want only the token", got)
		}
	})

	t.Run("saving nothing deletes the variable", func(t *testing.T) {
		if err := d.SaveNotificationSecret(ctx, id, map[string]string{"token": ""}); err != nil {
			t.Fatal(err)
		}
		if got := nomad.VariablePaths(); len(got) != 0 {
			t.Errorf("variables = %v, want none", got)
		}
		if err := d.SaveNotificationSecret(ctx, id, nil); err != nil {
			t.Errorf("SaveNotificationSecret(nil) with no variable = %v, want nil", err)
		}
	})

	t.Run("delete, also of a channel with none", func(t *testing.T) {
		if err := d.SaveNotificationSecret(ctx, id, map[string]string{"signing_secret": "abc"}); err != nil {
			t.Fatal(err)
		}
		if err := d.DeleteNotificationSecret(ctx, id); err != nil {
			t.Fatalf("DeleteNotificationSecret() = %v", err)
		}
		if got, _ := d.NotificationSecret(ctx, id); len(got) != 0 {
			t.Errorf("secret after a delete = %v", got)
		}
		if err := d.DeleteNotificationSecret(ctx, id); err != nil {
			t.Errorf("DeleteNotificationSecret() of a channel with none = %v, want nil", err)
		}
	})

	t.Run("an ID that is not a variable name is refused", func(t *testing.T) {
		for _, bad := range []string{"", "../registries/ghcr~io", "nch/x", "nch x"} {
			if _, err := d.NotificationSecret(ctx, bad); err == nil {
				t.Errorf("NotificationSecret(%q) succeeded", bad)
			}
			if err := d.SaveNotificationSecret(ctx, bad, map[string]string{"url": "x"}); err == nil {
				t.Errorf("SaveNotificationSecret(%q) succeeded", bad)
			}
			if err := d.DeleteNotificationSecret(ctx, bad); err == nil {
				t.Errorf("DeleteNotificationSecret(%q) succeeded", bad)
			}
		}
	})

	t.Run("a refused token is an error and names no value", func(t *testing.T) {
		nomad.FailVariables(403)
		defer nomad.FailVariables(0)
		if _, err := d.NotificationSecret(ctx, id); !errors.Is(err, ErrNomadUnavailable) {
			t.Errorf("NotificationSecret() = %v, want ErrNomadUnavailable", err)
		}
		err := d.SaveNotificationSecret(ctx, id, map[string]string{"url": notificationSecret})
		if !errors.Is(err, ErrNomadUnavailable) {
			t.Fatalf("SaveNotificationSecret() = %v, want ErrNomadUnavailable", err)
		}
		if strings.Contains(err.Error(), notificationSecret) {
			t.Errorf("the error holds the secret: %v", err)
		}
	})
}
