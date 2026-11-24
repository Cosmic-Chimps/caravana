package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestPeople(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	person := func(id, email, role string) PersonRecord {
		return PersonRecord{ID: id, Email: email, OrgRole: role, State: "active", PasswordHash: "hash-" + id,
			PasswordSignIn: true, CreatedAt: t0}
	}

	t.Run("create and read back by ID, email and invite", func(t *testing.T) {
		s := openTestStore(t)
		exp := t0.Add(7 * 24 * time.Hour)
		want := PersonRecord{ID: "p2", Email: "ana@example.com", OrgRole: "member", State: "invited",
			InviteHash: "inv-hash", InviteExpiresAt: &exp, InvitedBy: "you@example.com", EndsAt: &exp, CreatedAt: t0}
		if err := s.CreatePerson(ctx, want, nil); err != nil {
			t.Fatalf("CreatePerson: %v", err)
		}
		for name, get := range map[string]func() (PersonRecord, error){
			"id":     func() (PersonRecord, error) { return s.GetPerson(ctx, "p2") },
			"email":  func() (PersonRecord, error) { return s.GetPersonByEmail(ctx, "ana@example.com") },
			"invite": func() (PersonRecord, error) { return s.GetPersonByInvite(ctx, "inv-hash") },
		} {
			got, err := get()
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("by %s = %+v, %v; want %+v", name, got, err, want)
			}
		}
		if _, err := s.GetPerson(ctx, "nope"); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetPerson(nope) = %v, want ErrNotFound", err)
		}
		if _, err := s.GetPersonByInvite(ctx, "nope"); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetPersonByInvite(nope) = %v, want ErrNotFound", err)
		}
	})

	t.Run("an email and an invite token are unique", func(t *testing.T) {
		s := openTestStore(t)
		a := person("p1", "a@example.com", "owner")
		a.InviteHash = "same"
		if err := s.CreatePerson(ctx, a, nil); err != nil {
			t.Fatal(err)
		}
		b := person("p2", "a@example.com", "member")
		if err := s.CreatePerson(ctx, b, nil); !errors.Is(err, ErrAlreadyExists) {
			t.Errorf("same email = %v, want ErrAlreadyExists", err)
		}
		c := person("p3", "c@example.com", "member")
		c.InviteHash = "same"
		if err := s.CreatePerson(ctx, c, nil); !errors.Is(err, ErrAlreadyExists) {
			t.Errorf("same invite hash = %v, want ErrAlreadyExists", err)
		}
	})

	t.Run("owners, in the order they were made", func(t *testing.T) {
		s := openTestStore(t)
		if _, err := s.FirstOwner(ctx); !errors.Is(err, ErrNotFound) {
			t.Fatalf("FirstOwner with nobody = %v, want ErrNotFound", err)
		}
		if n, err := s.CountOwners(ctx); err != nil || n != 0 {
			t.Fatalf("CountOwners = %d, %v; want 0", n, err)
		}
		later := person("p2", "later@example.com", "owner")
		later.CreatedAt = t0.Add(time.Hour)
		for _, p := range []PersonRecord{later, person("p1", "first@example.com", "owner"), person("p3", "m@example.com", "member")} {
			if err := s.CreatePerson(ctx, p, nil); err != nil {
				t.Fatal(err)
			}
		}
		if got, err := s.FirstOwner(ctx); err != nil || got.ID != "p1" {
			t.Errorf("FirstOwner = %+v, %v; want p1", got, err)
		}
		if n, _ := s.CountOwners(ctx); n != 2 {
			t.Errorf("CountOwners = %d, want 2", n)
		}
		list, err := s.ListPeople(ctx)
		if err != nil || len(list) != 3 || list[0].ID != "p1" || list[1].ID != "p3" || list[2].ID != "p2" {
			t.Errorf("ListPeople = %+v, %v; want p1, p3, p2 by creation time", list, err)
		}
	})

	t.Run("roles round trip, with overrides, and are replaced as a whole", func(t *testing.T) {
		s := openTestStore(t)
		if _, err := s.InsertProject(ctx, "shop", "Shop"); err != nil {
			t.Fatal(err)
		}
		// A new project comes with its production.
		if _, err := s.InsertEnvironment(ctx, "shop", "staging"); err != nil {
			t.Fatal(err)
		}
		end := t0.Add(24 * time.Hour)
		roles := []ProjectRoleRecord{
			{Project: "shop", Role: "deploy", EndsAt: &end, Environments: map[string]string{"production": "view", "staging": "maintain"}},
			{Project: "default", Role: "view"},
		}
		if err := s.CreatePerson(ctx, person("p1", "m@example.com", "member"), roles); err != nil {
			t.Fatalf("CreatePerson with roles: %v", err)
		}
		got, err := s.PersonRoles(ctx, "p1")
		want := []ProjectRoleRecord{roles[1], roles[0]} // by project name
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("PersonRoles = %+v, %v; want %+v", got, err, want)
		}

		if err := s.SetPersonRoles(ctx, "p1", []ProjectRoleRecord{{Project: "shop", Role: "view"}}); err != nil {
			t.Fatalf("SetPersonRoles: %v", err)
		}
		got, _ = s.PersonRoles(ctx, "p1")
		if len(got) != 1 || got[0].Project != "shop" || got[0].Role != "view" || len(got[0].Environments) != 0 || got[0].EndsAt != nil {
			t.Errorf("roles after replacing = %+v, want only shop view with no overrides", got)
		}
		var n int
		if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM environment_roles").Scan(&n); err != nil || n != 0 {
			t.Errorf("environment rows after replacing = %d, %v; want 0", n, err)
		}

		// An unknown project, environment or person changes nothing.
		if err := s.SetPersonRoles(ctx, "p1", []ProjectRoleRecord{{Project: "nope", Role: "view"}}); !errors.Is(err, ErrNotFound) {
			t.Errorf("SetPersonRoles with an unknown project = %v, want ErrNotFound", err)
		}
		if err := s.SetPersonRoles(ctx, "p1", []ProjectRoleRecord{{Project: "shop", Role: "view", Environments: map[string]string{"nope": "none"}}}); !errors.Is(err, ErrNotFound) {
			t.Errorf("SetPersonRoles with an unknown environment = %v, want ErrNotFound", err)
		}
		if err := s.SetPersonRoles(ctx, "ghost", nil); !errors.Is(err, ErrNotFound) {
			t.Errorf("SetPersonRoles of an unknown person = %v, want ErrNotFound", err)
		}
		if got, _ := s.PersonRoles(ctx, "p1"); len(got) != 1 || got[0].Role != "view" {
			t.Errorf("roles after the refused changes = %+v, want the earlier ones kept", got)
		}
	})

	t.Run("the role tables' checks", func(t *testing.T) {
		s := openTestStore(t)
		if err := s.CreatePerson(ctx, person("p1", "m@example.com", "member"), nil); err != nil {
			t.Fatal(err)
		}
		if err := s.CreatePerson(ctx, person("p2", "x@example.com", "member"), []ProjectRoleRecord{{Project: "default", Role: "owner"}}); err == nil {
			t.Error("a project role of owner was accepted")
		}
		if err := s.CreatePerson(ctx, person("p3", "y@example.com", "member"), []ProjectRoleRecord{{Project: "default", Role: "view", Environments: map[string]string{"production": "boss"}}}); err == nil {
			t.Error("an environment role of boss was accepted")
		}
		if err := s.CreatePerson(ctx, person("p4", "z@example.com", "wizard"), nil); err == nil {
			t.Error("an organization role of wizard was accepted")
		}
		// 'none' is allowed for an environment, not for a project.
		if err := s.CreatePerson(ctx, person("p5", "n@example.com", "member"), []ProjectRoleRecord{{Project: "default", Role: "view", Environments: map[string]string{"production": "none"}}}); err != nil {
			t.Errorf("an environment role of none = %v, want it accepted", err)
		}
		if err := s.CreatePerson(ctx, person("p6", "q@example.com", "member"), []ProjectRoleRecord{{Project: "default", Role: "none"}}); err == nil {
			t.Error("a project role of none was accepted")
		}
		// A refused create leaves nobody behind.
		for _, id := range []string{"p2", "p3", "p4", "p6"} {
			if _, err := s.GetPerson(ctx, id); !errors.Is(err, ErrNotFound) {
				t.Errorf("GetPerson(%s) after a refused create = %v, want ErrNotFound", id, err)
			}
		}
	})

	t.Run("deleting an environment or a project drops the overrides and roles", func(t *testing.T) {
		s := openTestStore(t)
		for _, p := range []string{"shop", "blog"} {
			if _, err := s.InsertProject(ctx, p, p); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.InsertEnvironment(ctx, "shop", "staging"); err != nil {
			t.Fatal(err)
		}
		roles := []ProjectRoleRecord{
			{Project: "shop", Role: "view", Environments: map[string]string{"staging": "deploy"}},
			{Project: "blog", Role: "view"},
		}
		if err := s.CreatePerson(ctx, person("p1", "m@example.com", "member"), roles); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteEnvironment(ctx, "shop", "staging"); err != nil {
			t.Fatal(err)
		}
		got, _ := s.PersonRoles(ctx, "p1")
		if len(got) != 2 || len(got[1].Environments) != 0 {
			t.Errorf("roles after deleting the environment = %+v; want both projects, no overrides", got)
		}
		if err := s.DeleteProject(ctx, "blog"); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.PersonRoles(ctx, "p1"); len(got) != 1 || got[0].Project != "shop" {
			t.Errorf("roles after deleting a project = %+v; want only shop", got)
		}
		if _, err := s.GetPerson(ctx, "p1"); err != nil {
			t.Errorf("the person was lost with the project: %v", err)
		}
	})

	t.Run("update, invite and accept", func(t *testing.T) {
		s := openTestStore(t)
		exp := t0.Add(time.Hour)
		p := PersonRecord{ID: "p1", Email: "ana@example.com", OrgRole: "member", State: "invited",
			InviteHash: "tok1", InviteExpiresAt: &exp, CreatedAt: t0}
		if err := s.CreatePerson(ctx, p, nil); err != nil {
			t.Fatal(err)
		}
		end := t0.Add(48 * time.Hour)
		if err := s.UpdatePerson(ctx, "p1", "admin", &end); err != nil {
			t.Fatalf("UpdatePerson: %v", err)
		}
		if got, _ := s.GetPerson(ctx, "p1"); got.OrgRole != "admin" || got.EndsAt == nil || !got.EndsAt.Equal(end) {
			t.Errorf("person after UpdatePerson = %+v", got)
		}
		if err := s.UpdatePerson(ctx, "p1", "member", nil); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.GetPerson(ctx, "p1"); got.EndsAt != nil {
			t.Errorf("EndsAt = %v after clearing it", got.EndsAt)
		}
		if err := s.UpdatePerson(ctx, "ghost", "member", nil); !errors.Is(err, ErrNotFound) {
			t.Errorf("UpdatePerson of an unknown person = %v, want ErrNotFound", err)
		}

		// A new link voids the old one.
		exp2 := t0.Add(2 * time.Hour)
		if err := s.SetInvite(ctx, "p1", "tok2", exp2); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetPersonByInvite(ctx, "tok1"); !errors.Is(err, ErrNotFound) {
			t.Errorf("the old invite still finds the person: %v", err)
		}
		// The token that is not the current one accepts nothing.
		if err := s.AcceptInvite(ctx, "p1", "tok1", "pw", t0); !errors.Is(err, ErrNotFound) {
			t.Errorf("AcceptInvite with the old token = %v, want ErrNotFound", err)
		}
		if err := s.AcceptInvite(ctx, "p1", "tok2", "pw-hash", t0.Add(time.Minute)); err != nil {
			t.Fatalf("AcceptInvite: %v", err)
		}
		got, _ := s.GetPerson(ctx, "p1")
		if got.State != "active" || got.PasswordHash != "pw-hash" || got.InviteHash != "" || got.InviteExpiresAt != nil ||
			got.PasswordChangedAt == nil || !got.PasswordChangedAt.Equal(t0.Add(time.Minute)) {
			t.Errorf("person after accepting = %+v", got)
		}
		// One token is used once.
		if err := s.AcceptInvite(ctx, "p1", "tok2", "other", t0); !errors.Is(err, ErrNotFound) {
			t.Errorf("a second AcceptInvite = %v, want ErrNotFound", err)
		}
	})

	t.Run("password, sign-in setting and handle belong to one person", func(t *testing.T) {
		s := openTestStore(t)
		for _, p := range []PersonRecord{person("p1", "a@example.com", "owner"), person("p2", "b@example.com", "owner")} {
			if err := s.CreatePerson(ctx, p, nil); err != nil {
				t.Fatal(err)
			}
		}
		for _, id := range []string{"p1", "p2"} {
			for _, h := range []string{"keep", "drop"} {
				if err := s.CreateSession(ctx, SessionRecord{IDHash: id + h, PersonID: id, CreatedAt: t0, LastSeenAt: t0, ExpiresAt: t0.Add(time.Hour)}); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := s.SetPassword(ctx, "p1", "new-hash", t0.Add(time.Minute), "p1keep"); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.GetPerson(ctx, "p1"); got.PasswordHash != "new-hash" {
			t.Errorf("p1 hash = %q", got.PasswordHash)
		}
		if got, _ := s.GetPerson(ctx, "p2"); got.PasswordHash != "hash-p2" {
			t.Errorf("p2 hash = %q, want it untouched", got.PasswordHash)
		}
		for id, want := range map[string]bool{"p1keep": true, "p1drop": false, "p2keep": true, "p2drop": true} {
			if _, err := s.GetSession(ctx, id); (err == nil) != want {
				t.Errorf("session %s alive = %v, want %v", id, err == nil, want)
			}
		}
		if err := s.SetPassword(ctx, "ghost", "h", t0, ""); !errors.Is(err, ErrNotFound) {
			t.Errorf("SetPassword of an unknown person = %v, want ErrNotFound", err)
		}

		if err := s.SetPasswordSignIn(ctx, "p1", false); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.GetPerson(ctx, "p1"); got.PasswordSignIn {
			t.Error("p1 still signs in with a password")
		}
		if got, _ := s.GetPerson(ctx, "p2"); !got.PasswordSignIn {
			t.Error("p2's setting changed with p1's")
		}
		if err := s.SetPasswordSignIn(ctx, "ghost", true); !errors.Is(err, ErrNotFound) {
			t.Errorf("SetPasswordSignIn of an unknown person = %v, want ErrNotFound", err)
		}

		first, err := s.SetWebAuthnHandle(ctx, "p1", []byte("handle-one"))
		if err != nil || string(first) != "handle-one" {
			t.Fatalf("first handle = %q, %v", first, err)
		}
		again, err := s.SetWebAuthnHandle(ctx, "p1", []byte("handle-two"))
		if err != nil || string(again) != "handle-one" {
			t.Errorf("second handle = %q, %v; want the first kept", again, err)
		}
		if other, _ := s.SetWebAuthnHandle(ctx, "p2", []byte("handle-two")); string(other) != "handle-two" {
			t.Errorf("p2's handle = %q, want its own", other)
		}
		if _, err := s.SetWebAuthnHandle(ctx, "ghost", []byte("x")); !errors.Is(err, ErrNotFound) {
			t.Errorf("SetWebAuthnHandle of an unknown person = %v, want ErrNotFound", err)
		}
	})

	t.Run("delete takes the person's sessions, tokens, passkeys and roles, and no one else's", func(t *testing.T) {
		s := openTestStore(t)
		for _, p := range []PersonRecord{person("p1", "a@example.com", "member"), person("p2", "b@example.com", "member")} {
			if err := s.CreatePerson(ctx, p, []ProjectRoleRecord{{Project: "default", Role: "view", Environments: map[string]string{"production": "deploy"}}}); err != nil {
				t.Fatal(err)
			}
		}
		for _, id := range []string{"p1", "p2"} {
			if err := s.CreateSession(ctx, SessionRecord{IDHash: "s-" + id, PersonID: id, CreatedAt: t0, LastSeenAt: t0, ExpiresAt: t0.Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			if err := s.CreateAPIToken(ctx, APITokenRecord{ID: "t-" + id, PersonID: id, Name: "ci", Hash: "h-" + id, CreatedAt: t0}, t0); err != nil {
				t.Fatal(err)
			}
			pk := testPasskey("pk-"+id, "cred-"+id, "localhost", t0)
			pk.PersonID = id
			if err := s.InsertPasskey(ctx, pk); err != nil {
				t.Fatal(err)
			}
		}

		if err := s.DeletePerson(ctx, "p1"); err != nil {
			t.Fatalf("DeletePerson: %v", err)
		}
		if _, err := s.GetPerson(ctx, "p1"); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetPerson after delete = %v, want ErrNotFound", err)
		}
		if _, err := s.GetSession(ctx, "s-p1"); !errors.Is(err, ErrNotFound) {
			t.Errorf("session after delete = %v, want ErrNotFound", err)
		}
		if _, err := s.GetAPITokenByHash(ctx, "h-p1"); !errors.Is(err, ErrNotFound) {
			t.Errorf("token after delete = %v, want ErrNotFound", err)
		}
		if _, err := s.GetPasskeyByCredential(ctx, []byte("cred-p1")); !errors.Is(err, ErrNotFound) {
			t.Errorf("passkey after delete = %v, want ErrNotFound", err)
		}
		if got, _ := s.PersonRoles(ctx, "p1"); len(got) != 0 {
			t.Errorf("roles after delete = %+v, want none", got)
		}
		var n int
		for _, table := range []string{"environment_roles", "project_roles", "sessions", "api_tokens", "passkeys", "people"} {
			if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil || n != 1 {
				t.Errorf("%s rows after the delete = %d, %v; want only p2's 1", table, n, err)
			}
		}
		if err := s.DeletePerson(ctx, "p1"); !errors.Is(err, ErrNotFound) {
			t.Errorf("DeletePerson twice = %v, want ErrNotFound", err)
		}
	})

	t.Run("tokens and passkeys are listed and changed per person", func(t *testing.T) {
		s := openTestStore(t)
		for _, p := range []PersonRecord{person("p1", "a@example.com", "owner"), person("p2", "b@example.com", "member")} {
			if err := s.CreatePerson(ctx, p, nil); err != nil {
				t.Fatal(err)
			}
		}
		// Two people may each have an active token called ci.
		for _, id := range []string{"p1", "p2"} {
			if err := s.CreateAPIToken(ctx, APITokenRecord{ID: "t-" + id, PersonID: id, Name: "ci", Hash: "h-" + id, CreatedAt: t0}, t0); err != nil {
				t.Fatalf("token ci of %s: %v", id, err)
			}
		}
		if err := s.CreateAPIToken(ctx, APITokenRecord{ID: "t-x", PersonID: "p1", Name: "ci", Hash: "h-x", CreatedAt: t0}, t0); !errors.Is(err, ErrAlreadyExists) {
			t.Errorf("a second ci for p1 = %v, want ErrAlreadyExists", err)
		}
		if got, _ := s.ListAPITokens(ctx, "p2"); len(got) != 1 || got[0].ID != "t-p2" {
			t.Errorf("p2's tokens = %+v", got)
		}
		if all, _ := s.ListAllAPITokens(ctx); len(all) != 2 {
			t.Errorf("all tokens = %d, want 2", len(all))
		}
		if _, err := s.RevokeAPIToken(ctx, "p2", "t-p1", t0); !errors.Is(err, ErrNotFound) {
			t.Errorf("revoking p1's token as p2 = %v, want ErrNotFound", err)
		}
		if got, _ := s.GetAPITokenByHash(ctx, "h-p1"); got.RevokedAt != nil {
			t.Error("p1's token was revoked by p2")
		}

		pk := testPasskey("pk1", "cred-1", "localhost", t0)
		pk.PersonID = "p1"
		if err := s.InsertPasskey(ctx, pk); err != nil {
			t.Fatal(err)
		}
		if err := s.RenamePasskey(ctx, "p2", "pk1", "mine"); !errors.Is(err, ErrNotFound) {
			t.Errorf("renaming p1's passkey as p2 = %v, want ErrNotFound", err)
		}
		if err := s.DeletePasskey(ctx, "p2", "pk1"); !errors.Is(err, ErrNotFound) {
			t.Errorf("deleting p1's passkey as p2 = %v, want ErrNotFound", err)
		}
		if _, err := s.GetPasskey(ctx, "p2", "pk1"); !errors.Is(err, ErrNotFound) {
			t.Errorf("reading p1's passkey as p2 = %v, want ErrNotFound", err)
		}
		if got, _ := s.ListPasskeys(ctx, "p2"); len(got) != 0 {
			t.Errorf("p2's passkeys = %+v, want none", got)
		}
		if n, _ := s.CountPasskeys(ctx, "p2", ""); n != 0 {
			t.Errorf("CountPasskeys(p2) = %d, want 0", n)
		}
		if n, _ := s.CountAllPasskeys(ctx, "localhost"); n != 1 {
			t.Errorf("CountAllPasskeys = %d, want 1", n)
		}
		if all, _ := s.ListAllPasskeys(ctx); len(all) != 1 || all[0].PersonID != "p1" {
			t.Errorf("ListAllPasskeys = %+v", all)
		}
	})
}

// Migration 0043 turns the one admin into the owner and gives them every session, token and
// passkey; with no admin, nobody exists and the tables are empty.
func TestUpgradeTo0043(t *testing.T) {
	ctx := context.Background()
	const at = "2026-11-01T10:00:00.000000000Z"

	t.Run("with an admin", func(t *testing.T) {
		db := testDB(t, t.TempDir())
		if _, err := migrate(ctx, db, migrationsUpTo(t, 42)); err != nil {
			t.Fatal(err)
		}
		mustExec := func(query string, args ...any) {
			t.Helper()
			if _, err := db.ExecContext(ctx, query, args...); err != nil {
				t.Fatalf("%s: %v", query, err)
			}
		}
		mustExec(`INSERT INTO admin (id, email, password_hash, created_at, password_changed_at, password_signin, webauthn_handle)
			VALUES (1, 'you@example.com', 'pbkdf2-sha256$1$c2FsdA$a2V5', ?, ?, 0, x'0102030405')`, at, "2026-11-02T10:00:00.000000000Z")
		mustExec(`INSERT INTO sessions (id_hash, created_at, last_seen_at, expires_at, verified_at) VALUES ('s1', ?, ?, ?, ?), ('s2', ?, ?, ?, NULL)`,
			at, at, at, at, at, at, at)
		mustExec(`INSERT INTO api_tokens (id, name, token_hash, created_at, scope_project, scope_app) VALUES ('t1', 'full', 'h1', ?, NULL, NULL), ('t2', 'ci', 'h2', ?, 'default', 'web')`, at, at)
		mustExec(`INSERT INTO passkeys (id, user_handle, credential_id, rp_id, public_key, attestation_type, attestation_format, aaguid,
			sign_count, transports, user_present, user_verified, backup_eligible, backup_state, name, created_at)
			VALUES ('pk1', x'0102030405', x'aa', 'localhost', x'bb', 'none', 'none', x'', 7, 'internal', 1, 1, 0, 0, 'Mac', ?)`, at)

		if _, err := migrate(ctx, db, testMigrations(t)); err != nil {
			t.Fatal(err)
		}
		s := &Store{db: db, nowFn: time.Now}

		people, err := s.ListPeople(ctx)
		if err != nil || len(people) != 1 {
			t.Fatalf("people = %+v, %v; want exactly the owner", people, err)
		}
		o := people[0]
		if o.Email != "you@example.com" || o.OrgRole != "owner" || o.State != "active" || o.PasswordHash != "pbkdf2-sha256$1$c2FsdA$a2V5" ||
			o.PasswordSignIn || string(o.WebAuthnHandle) != "\x01\x02\x03\x04\x05" || len(o.ID) != 26 ||
			o.PasswordChangedAt == nil || o.PasswordChangedAt.Format("2006-01-02") != "2026-11-02" ||
			o.CreatedAt.Format("2006-01-02") != "2026-11-01" || o.InviteHash != "" || o.EndsAt != nil {
			t.Errorf("the owner = %+v; want the admin's email, hash, setting and handle", o)
		}
		for _, id := range []string{"s1", "s2"} {
			if got, err := s.GetSession(ctx, id); err != nil || got.PersonID != o.ID {
				t.Errorf("session %s = %+v, %v; want the owner's", id, got, err)
			}
		}
		if got, _ := s.GetSession(ctx, "s1"); got.VerifiedAt == nil {
			t.Error("a session lost its verified time")
		}
		tokens, err := s.ListAPITokens(ctx, o.ID)
		if err != nil || len(tokens) != 2 {
			t.Fatalf("owner's tokens = %+v, %v; want both", tokens, err)
		}
		if got, _ := s.GetAPITokenByHash(ctx, "h2"); got.ScopeProject != "default" || got.ScopeApp != "web" || got.Name != "ci" {
			t.Errorf("the deploy token = %+v; want its scope kept", got)
		}
		if got, err := s.GetPasskey(ctx, o.ID, "pk1"); err != nil || got.SignCount != 7 || got.Name != "Mac" || got.RPID != "localhost" {
			t.Errorf("passkey = %+v, %v; want the owner's, whole", got, err)
		}
		var n int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name = 'admin'").Scan(&n); err != nil || n != 0 {
			t.Errorf("the admin table is still there (%d, %v)", n, err)
		}
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name IN ('api_tokens_name', 'passkeys_rp')").Scan(&n); err != nil || n != 2 {
			t.Errorf("indexes back = %d, %v; want 2", n, err)
		}
		for _, table := range []string{"deployments", "app_actions", "backups", "restores"} {
			if _, err := db.ExecContext(ctx, "SELECT actor FROM "+table); err != nil {
				t.Errorf("%s has no actor column: %v", table, err)
			}
		}
		rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
		if err != nil {
			t.Fatal(err)
		}
		if rows.Next() {
			t.Error("foreign_key_check finds a broken reference")
		}
		rows.Close()
	})

	t.Run("without an admin", func(t *testing.T) {
		db := testDB(t, t.TempDir())
		if _, err := migrate(ctx, db, migrationsUpTo(t, 42)); err != nil {
			t.Fatal(err)
		}
		if _, err := migrate(ctx, db, testMigrations(t)); err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"people", "sessions", "api_tokens", "passkeys"} {
			var n int
			if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil || n != 0 {
				t.Errorf("%s has %d rows, %v; want none", table, n, err)
			}
		}
	})
}
