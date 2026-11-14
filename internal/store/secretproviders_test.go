package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestSecretProviders(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 13, 9, 0, 0, 123456789, time.UTC)
	bella := func(name string) SecretProvider {
		return SecretProvider{
			Name: name, Kind: SecretProviderBella,
			URL: "https://api.bella-baxter.io", Project: "shop", Environment: "production",
			Allowed: []string{"shop", "shop/production"}, CreatedAt: t0, UpdatedAt: t0,
		}
	}

	t.Run("a new store has none", func(t *testing.T) {
		s := openTestStore(t)
		got, err := s.ListSecretProviders(ctx)
		if err != nil || len(got) != 0 {
			t.Errorf("ListSecretProviders() = %v, %v; want none", got, err)
		}
		if _, err := s.GetSecretProvider(ctx, "bella-prod"); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetSecretProvider() error = %v, want ErrNotFound", err)
		}
	})

	t.Run("save and read back", func(t *testing.T) {
		s := openTestStore(t)
		want := bella("bella-prod")
		if err := s.SaveSecretProvider(ctx, want); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetSecretProvider(ctx, "bella-prod")
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("GetSecretProvider() = %+v, %v; want %+v", got, err, want)
		}
		if got.LastTest != nil {
			t.Errorf("LastTest = %+v on an untested provider, want nil", got.LastTest)
		}
	})

	t.Run("no allowed places reads back empty", func(t *testing.T) {
		s := openTestStore(t)
		p := bella("open")
		p.Allowed = nil
		if err := s.SaveSecretProvider(ctx, p); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetSecretProvider(ctx, "open")
		if err != nil || len(got.Allowed) != 0 {
			t.Errorf("Allowed = %v, %v; want none", got.Allowed, err)
		}
	})

	t.Run("saving again keeps created_at and the last test, changes the rest", func(t *testing.T) {
		s := openTestStore(t)
		if err := s.SaveSecretProvider(ctx, bella("bella-prod")); err != nil {
			t.Fatal(err)
		}
		test := SecretProviderTest{At: t0.Add(time.Minute), OK: true, Message: "12 secrets readable", Count: 12}
		if err := s.SaveSecretProviderTest(ctx, "bella-prod", test); err != nil {
			t.Fatal(err)
		}
		t1 := t0.Add(time.Hour)
		next := bella("bella-prod")
		next.Environment, next.Allowed, next.CreatedAt, next.UpdatedAt = "staging", nil, t1, t1
		if err := s.SaveSecretProvider(ctx, next); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetSecretProvider(ctx, "bella-prod")
		if err != nil {
			t.Fatal(err)
		}
		if got.Environment != "staging" || len(got.Allowed) != 0 || !got.CreatedAt.Equal(t0) || !got.UpdatedAt.Equal(t1) {
			t.Errorf("after the second save: %+v", got)
		}
		if got.LastTest == nil || *got.LastTest != test {
			t.Errorf("LastTest = %+v, want %+v kept", got.LastTest, test)
		}
	})

	t.Run("list is ordered by name", func(t *testing.T) {
		s := openTestStore(t)
		for _, n := range []string{"b", "c", "a"} {
			if err := s.SaveSecretProvider(ctx, bella(n)); err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.ListSecretProviders(ctx)
		if err != nil || len(got) != 3 || got[0].Name != "a" || got[1].Name != "b" || got[2].Name != "c" {
			t.Errorf("ListSecretProviders() = %+v, %v; want a, b, c", got, err)
		}
	})

	t.Run("a test is recorded, a failed one too, and cleared", func(t *testing.T) {
		s := openTestStore(t)
		if err := s.SaveSecretProvider(ctx, bella("bella-prod")); err != nil {
			t.Fatal(err)
		}
		fail := SecretProviderTest{At: t0, Message: "Bella refused the API key"}
		if err := s.SaveSecretProviderTest(ctx, "bella-prod", fail); err != nil {
			t.Fatal(err)
		}
		got, _ := s.GetSecretProvider(ctx, "bella-prod")
		if got.LastTest == nil || *got.LastTest != fail {
			t.Errorf("LastTest = %+v, want %+v", got.LastTest, fail)
		}
		if err := s.ClearSecretProviderTest(ctx, "bella-prod"); err != nil {
			t.Fatal(err)
		}
		got, _ = s.GetSecretProvider(ctx, "bella-prod")
		if got.LastTest != nil {
			t.Errorf("LastTest = %+v after Clear, want nil", got.LastTest)
		}
	})

	t.Run("an unknown provider is ErrNotFound", func(t *testing.T) {
		s := openTestStore(t)
		if err := s.SaveSecretProviderTest(ctx, "nope", SecretProviderTest{At: t0}); !errors.Is(err, ErrNotFound) {
			t.Errorf("SaveSecretProviderTest error = %v, want ErrNotFound", err)
		}
		if err := s.ClearSecretProviderTest(ctx, "nope"); !errors.Is(err, ErrNotFound) {
			t.Errorf("ClearSecretProviderTest error = %v, want ErrNotFound", err)
		}
		if err := s.DeleteSecretProvider(ctx, "nope"); !errors.Is(err, ErrNotFound) {
			t.Errorf("DeleteSecretProvider error = %v, want ErrNotFound", err)
		}
	})

	t.Run("delete", func(t *testing.T) {
		s := openTestStore(t)
		if err := s.SaveSecretProvider(ctx, bella("bella-prod")); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteSecretProvider(ctx, "bella-prod"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetSecretProvider(ctx, "bella-prod"); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetSecretProvider after delete: %v, want ErrNotFound", err)
		}
	})

	t.Run("only bella is a kind", func(t *testing.T) {
		s := openTestStore(t)
		p := bella("x")
		p.Kind = "vault"
		if err := s.SaveSecretProvider(ctx, p); err == nil {
			t.Error("SaveSecretProvider accepted the kind vault")
		}
	})
}

func TestDeploymentRefresh(t *testing.T) {
	ctx := context.Background()
	s := storeWithApp(t)

	plain, err := s.CreateDeployment(ctx, "whoami", newDeployment("d1"), nil, nil, "")
	if err != nil || plain.Deployment.Refresh {
		t.Fatalf("plain deployment refresh = %v, %v; want false", plain.Deployment.Refresh, err)
	}
	d := newDeployment("d2")
	d.Refresh = true
	rec, err := s.CreateDeployment(ctx, "whoami", d, nil, nil, "d1")
	if err != nil || !rec.Deployment.Refresh || rec.Source != "d1" {
		t.Fatalf("created refresh = %v, source %q, %v", rec.Deployment.Refresh, rec.Source, err)
	}
	if _, err := s.UpdateDeployment(ctx, "d2", DeploymentUpdate{Status: StatusRunning}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetDeployment(ctx, "d2")
	if err != nil || !got.Deployment.Refresh {
		t.Errorf("GetDeployment refresh = %v, %v", got.Deployment.Refresh, err)
	}
	list, err := s.ListDeployments(ctx, "whoami", 0)
	if err != nil || len(list) != 2 || !list[0].Deployment.Refresh || list[1].Deployment.Refresh {
		t.Errorf("ListDeployments refresh flags wrong: %+v, %v", list, err)
	}
}
