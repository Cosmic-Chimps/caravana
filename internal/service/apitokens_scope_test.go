package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
)

func TestScopedAPIToken(t *testing.T) {
	ctx := context.Background()
	newEnvWithShop := func(t *testing.T) *authEnv {
		t.Helper()
		e := newAuthEnvWithAdmin(t)
		if _, err := e.store.InsertProject(ctx, "shop", "Shop"); err != nil {
			t.Fatal(err)
		}
		return e
	}
	scope := TokenScope{Project: "shop", App: "web"}

	t.Run("create, check and list", func(t *testing.T) {
		e := newEnvWithShop(t)
		created, err := e.auth.CreateScopedAPIToken(ctx, "shop-web-ci", nil, &scope)
		if err != nil {
			t.Fatalf("CreateScopedAPIToken: %v", err)
		}
		if created.Info.Scope == nil || *created.Info.Scope != scope {
			t.Errorf("Info.Scope = %+v, want %+v", created.Info.Scope, scope)
		}
		got, err := e.auth.CheckAPIToken(ctx, created.Token)
		if err != nil || got == nil || *got != scope {
			t.Errorf("CheckAPIToken = %+v, %v; want scope %+v", got, err, scope)
		}
		full, _ := e.auth.CreateAPIToken(ctx, "everything", nil)
		if got, err := e.auth.CheckAPIToken(ctx, full.Token); err != nil || got != nil {
			t.Errorf("CheckAPIToken(full) = %+v, %v; want nil scope", got, err)
		}
		list, err := e.auth.ListAPITokens(ctx)
		if err != nil || len(list) != 2 || list[0].Scope != nil || list[1].Scope == nil || list[1].Scope.App != "web" {
			t.Errorf("ListAPITokens = %+v, %v", list, err)
		}
		if got := scope.Describe(); got != "this token can only deploy app web of project shop" {
			t.Errorf("Describe = %q", got)
		}
	})

	t.Run("the app need not exist, the project must", func(t *testing.T) {
		e := newEnvWithShop(t)
		if _, err := e.auth.CreateScopedAPIToken(ctx, "ok", nil, &TokenScope{Project: "shop", App: "nothing-yet"}); err != nil {
			t.Errorf("an app that doesn't exist = %v, want ok", err)
		}
		tests := []struct {
			name  string
			scope TokenScope
			field string
		}{
			{"unknown project", TokenScope{Project: "nope", App: "web"}, "scope.project"},
			{"bad project name", TokenScope{Project: "Shop", App: "web"}, "scope.project"},
			{"empty project", TokenScope{App: "web"}, "scope.project"},
			{"bad app name", TokenScope{Project: "shop", App: "a--b"}, "scope.app"},
			{"uppercase app", TokenScope{Project: "shop", App: "Web"}, "scope.app"},
			{"empty app", TokenScope{Project: "shop"}, "scope.app"},
		}
		for i, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := e.auth.CreateScopedAPIToken(ctx, "bad-"+string(rune('a'+i)), nil, &tt.scope)
				var fe *app.FieldError
				if !errors.As(err, &fe) || fe.Field != tt.field {
					t.Errorf("error = %v, want a FieldError on %s", err, tt.field)
				}
			})
		}
		if got, _ := e.store.ListAPITokens(ctx); len(got) != 1 {
			t.Errorf("%d tokens stored, want only the one that was fine", len(got))
		}
	})

	t.Run("the log names the scope, never the token", func(t *testing.T) {
		e := newEnvWithShop(t)
		created, err := e.auth.CreateScopedAPIToken(ctx, "shop-web-ci", nil, &scope)
		if err != nil {
			t.Fatal(err)
		}
		logs := e.logs.String()
		if !strings.Contains(logs, `"scopeApp":"web"`) || strings.Contains(logs, created.Token) {
			t.Errorf("log = %s", logs)
		}
	})
}
