package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestWebhookTokens(t *testing.T) {
	ctx := context.Background()
	hashA, hashB := strings.Repeat("a", 64), strings.Repeat("b", 64)

	t.Run("set and get", func(t *testing.T) {
		s := storeWithApp(t)
		set, err := s.SetWebhookToken(ctx, "whoami", hashA)
		if err != nil {
			t.Fatalf("SetWebhookToken: %v", err)
		}
		got, err := s.GetWebhookToken(ctx, "whoami")
		if err != nil || got != set || got.Hash != hashA || got.AppID != "whoami" || got.CreatedAt.IsZero() {
			t.Errorf("GetWebhookToken = %+v, %v; want %+v", got, err, set)
		}
	})

	t.Run("set again replaces it", func(t *testing.T) {
		s := storeWithApp(t)
		first, _ := s.SetWebhookToken(ctx, "whoami", hashA)
		second, err := s.SetWebhookToken(ctx, "whoami", hashB)
		if err != nil {
			t.Fatalf("second SetWebhookToken: %v", err)
		}
		if second.Hash != hashB || !second.CreatedAt.After(first.CreatedAt) {
			t.Errorf("after replace = %+v, want hash B and a later CreatedAt than %v", second, first.CreatedAt)
		}
	})

	t.Run("delete", func(t *testing.T) {
		s := storeWithApp(t)
		if _, err := s.SetWebhookToken(ctx, "whoami", hashA); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteWebhookToken(ctx, "whoami"); err != nil {
			t.Fatalf("DeleteWebhookToken: %v", err)
		}
		if _, err := s.GetWebhookToken(ctx, "whoami"); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetWebhookToken after delete = %v, want ErrNotFound", err)
		}
		if err := s.DeleteWebhookToken(ctx, "whoami"); err != nil {
			t.Errorf("second DeleteWebhookToken = %v, want nil", err)
		}
	})

	t.Run("none yet", func(t *testing.T) {
		s := storeWithApp(t)
		if _, err := s.GetWebhookToken(ctx, "whoami"); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetWebhookToken = %v, want ErrNotFound", err)
		}
	})

	t.Run("unknown app", func(t *testing.T) {
		s := storeWithApp(t)
		if _, err := s.SetWebhookToken(ctx, "nope", hashA); !errors.Is(err, ErrNotFound) {
			t.Errorf("SetWebhookToken(nope) = %v, want ErrNotFound", err)
		}
		if _, err := s.GetWebhookToken(ctx, "nope"); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetWebhookToken(nope) = %v, want ErrNotFound", err)
		}
		if err := s.DeleteWebhookToken(ctx, "nope"); !errors.Is(err, ErrNotFound) {
			t.Errorf("DeleteWebhookToken(nope) = %v, want ErrNotFound", err)
		}
	})
}
