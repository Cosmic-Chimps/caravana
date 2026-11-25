package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/webauthntest"
)

// Phase C of the audit log (feature 057), accounts: people, tokens, passkeys, sign-in, the CLI as host.

func TestAuditNotesTheAdminsCreationAsHost(t *testing.T) {
	e := newAuthEnv(t)
	ctx := WithAuditDoor(context.Background(), audit.DoorHost)
	got := notedOne(t, e.store, func() {
		if err := e.auth.InitAdmin(ctx, testEmail, testPassword); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionCreated, audit.KindPerson, e.mustOwner(t))
	if got.Actor.Kind != audit.ActorHost || got.Door != audit.DoorHost {
		t.Errorf("actor = %+v door %s, want the host", got.Actor, got.Door)
	}
	if got.Resource.Name != testEmail {
		t.Errorf("name = %q, want the email", got.Resource.Name)
	}
	if strings.Contains(everyEntry(t, e.store), testPassword) {
		t.Error("the password is in the log")
	}
}

func (e *authEnv) mustOwner(t *testing.T) string {
	t.Helper()
	who, err := e.auth.ResolvePerson(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	return who.ID
}

func TestAuditNotesSignInsAndFailures(t *testing.T) {
	e := newAuthEnvWithAdmin(t)
	ctx := context.Background()

	// A good password: signed in by the person, from the address.
	var sess Session
	got := notedOne(t, e.store, func() {
		var err error
		if sess, err = e.auth.SignIn(ctx, testEmail, testPassword, "", addrA); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionSignedIn, audit.KindAccount, e.owner)
	if got.Actor.Kind != audit.ActorPerson || got.Actor.Email != testEmail || got.Address != addrA {
		t.Errorf("sign-in = actor %+v address %q", got.Actor, got.Address)
	}

	// A wrong password for a real email: anonymous, the typed email, the address.
	got = notedOne(t, e.store, func() {
		if _, err := e.auth.SignIn(ctx, testEmail, "not the password at all", "", addrA); err == nil {
			t.Fatal("a wrong password signed in")
		}
	})
	wantEntry(t, got, audit.ActionSignInFailed, audit.KindAccount, "")
	if got.Actor != (audit.Actor{Kind: audit.ActorAnonymous, Email: testEmail}) || got.Address != addrA {
		t.Errorf("failure = actor %+v address %q", got.Actor, got.Address)
	}

	// A password typed into the email box: the entry keeps no email, and not the password.
	typed := "hunter2-in-the-wrong-box"
	got = notedOne(t, e.store, func() {
		if _, err := e.auth.SignIn(ctx, typed, "whatever", "", addrA); err == nil {
			t.Fatal("signed in")
		}
	})
	if got.Actor != (audit.Actor{Kind: audit.ActorAnonymous}) {
		t.Errorf("actor = %+v, want anonymous with no email", got.Actor)
	}
	if strings.Contains(everyEntry(t, e.store), typed) {
		t.Error("a typed password is in the log")
	}

	// A locked address makes no entry: nothing was checked.
	for i := 0; i < 5; i++ {
		_, _ = e.auth.SignIn(ctx, testEmail, "wrong wrong wrong", "", addrB)
	}
	if n := len(noted(t, e.store, func() { _, _ = e.auth.SignIn(ctx, testEmail, "wrong wrong wrong", "", addrB) })); n != 0 {
		t.Errorf("a locked address wrote %d entries", n)
	}

	// Sign-out: by the person; a session that is gone changes nothing.
	got = notedOne(t, e.store, func() {
		if err := e.auth.SignOut(ctx, sess.Value); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionSignedOut, audit.KindAccount, e.owner)
	if got.Actor.Email != testEmail {
		t.Errorf("sign-out actor = %+v", got.Actor)
	}
	if n := len(noted(t, e.store, func() { _ = e.auth.SignOut(ctx, sess.Value) })); n != 0 {
		t.Errorf("signing out twice wrote %d entries", n)
	}
}

func TestAuditNotesPasskeys(t *testing.T) {
	e := newAuthEnvWithAdmin(t)
	ctx := context.Background()
	session := signedIn(t, e)

	var authn *webauthntest.Authenticator
	var id string
	got := notedOne(t, e.store, func() { authn = addPasskey(t, e, session, rpMain, "Mac") })
	id = got.Resource.ID
	wantEntry(t, got, audit.ActionCreated, audit.KindPasskey, id)
	if got.Resource.Name != "Mac" || change(got.Changes, "domain") == nil {
		t.Errorf("passkey entry = %+v", got)
	}
	if got.Actor.Email != testEmail {
		t.Errorf("actor = %+v, want the person", got.Actor)
	}

	got = notedOne(t, e.store, func() {
		if _, err := e.auth.RenamePasskey(ctx, e.owner, id, "Work Mac"); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindPasskey, id)
	if c := change(got.Changes, "name"); c == nil || c.From != "Mac" || c.To != "Work Mac" {
		t.Errorf("rename changes = %+v", got.Changes)
	}
	if n := len(noted(t, e.store, func() { _, _ = e.auth.RenamePasskey(ctx, e.owner, id, "Work Mac") })); n != 0 {
		t.Errorf("a rename to the same name wrote %d entries", n)
	}

	// Password sign-in off then on: the sign-in settings, with the change.
	got = notedOne(t, e.store, func() {
		if _, err := e.auth.SetPasswordSignIn(ctx, e.owner, false, ""); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindSettings, "sign-in")
	if c := change(got.Changes, "passwordSignIn"); c == nil || c.From != "true" || c.To != "false" {
		t.Errorf("sign-in changes = %+v", got.Changes)
	}
	if n := len(noted(t, e.store, func() { _, _ = e.auth.SetPasswordSignIn(ctx, e.owner, false, "") })); n != 0 {
		t.Errorf("setting what it was wrote %d entries", n)
	}
	if _, err := e.auth.SetPasswordSignIn(ctx, e.owner, true, ""); err != nil {
		t.Fatal(err)
	}

	// A passkey sign-in, and a failed one: no email on the failure.
	got = notedOne(t, e.store, func() {
		if _, err := passkeySignIn(t, e, rpMain, authn, addrA); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionSignedIn, audit.KindAccount, e.owner)
	if c := change(got.Changes, "method"); c == nil || c.To != "passkey" {
		t.Errorf("method = %+v", got.Changes)
	}
	failedAuthn := webauthntest.New(rpMain.ID) // never registered
	got = notedOne(t, e.store, func() {
		if _, err := passkeySignIn(t, e, rpMain, failedAuthn, addrA); err == nil {
			t.Fatal("an unknown passkey signed in")
		}
	})
	wantEntry(t, got, audit.ActionSignInFailed, audit.KindAccount, "")
	if got.Actor != (audit.Actor{Kind: audit.ActorAnonymous}) {
		t.Errorf("passkey failure actor = %+v, want anonymous with no email", got.Actor)
	}

	// Remove: the passkey's entry names it; no credential ID, key or private key anywhere.
	got = notedOne(t, e.store, func() {
		if err := e.auth.RemovePasskey(ctx, e.owner, id); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionRemoved, audit.KindPasskey, id)
	all := everyEntry(t, e.store)
	for _, leak := range authn.PrivateKeySentinel() {
		if strings.Contains(all, leak) {
			t.Errorf("the log holds the authenticator's key (%q)", leak)
		}
	}
}

func TestAuditNotesPasswordChangeAndReset(t *testing.T) {
	e := newAuthEnvWithAdmin(t)
	ctx := context.Background()

	got := notedOne(t, e.store, func() {
		if err := e.auth.ChangePassword(ctx, e.owner, testPassword, "a brand new password!", ""); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindAccount, e.owner)
	if c := change(got.Changes, "password"); c == nil || c.From != "" || c.To != "" {
		t.Errorf("password change = %+v", got.Changes)
	}
	if got.Actor.Email != testEmail {
		t.Errorf("actor = %+v", got.Actor)
	}
	// A wrong current password changes nothing and notes nothing.
	if n := len(noted(t, e.store, func() { _ = e.auth.ChangePassword(ctx, e.owner, "not it, not it, not it", "x-another-new-pass", "") })); n != 0 {
		t.Errorf("a refused change wrote %d entries", n)
	}

	// The CLI's reset: the host is the actor.
	host := WithAuditDoor(ctx, audit.DoorHost)
	got = notedOne(t, e.store, func() {
		if err := e.auth.ResetPassword(host, "", "reset by the host 12345"); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindAccount, e.owner)
	if got.Actor.Kind != audit.ActorHost {
		t.Errorf("reset actor = %+v, want the host", got.Actor)
	}
	for _, p := range []string{testPassword, "a brand new password!", "reset by the host 12345"} {
		if strings.Contains(everyEntry(t, e.store), p) {
			t.Errorf("a password is in the log: %q", p)
		}
	}
}

func TestAuditNotesTokens(t *testing.T) {
	e := newAuthEnvWithAdmin(t)
	ctx := context.Background()
	exp := e.clock.Now().Add(48 * time.Hour)

	var created CreatedAPIToken
	got := notedOne(t, e.store, func() {
		var err error
		if created, err = e.auth.CreateAPIToken(ctx, e.owner, "ci", &exp); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionCreated, audit.KindToken, created.Info.ID)
	if got.Resource.Name != "ci" || change(got.Changes, "expiresAt") == nil || change(got.Changes, "owner") == nil {
		t.Errorf("token entry = %+v", got)
	}
	if strings.Contains(everyEntry(t, e.store), created.Token) {
		t.Error("the token is in the log")
	}
	// A refused creation (a taken name) notes nothing.
	if n := len(noted(t, e.store, func() { _, _ = e.auth.CreateAPIToken(ctx, e.owner, "ci", nil) })); n != 0 {
		t.Errorf("a refused token wrote %d entries", n)
	}

	got = notedOne(t, e.store, func() {
		if err := e.auth.RevokeAPIToken(ctx, e.owner, created.Info.ID); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionRevoked, audit.KindToken, created.Info.ID)
	// Revoking again changes nothing.
	if n := len(noted(t, e.store, func() { _ = e.auth.RevokeAPIToken(ctx, e.owner, created.Info.ID) })); n != 0 {
		t.Errorf("revoking twice wrote %d entries", n)
	}

	// The CLI's token and revoke by name: the host is the actor.
	host := WithAuditDoor(ctx, audit.DoorHost)
	got = notedOne(t, e.store, func() {
		var err error
		if created, err = e.auth.CreateAPIToken(host, e.owner, "cli", nil); err != nil {
			t.Fatal(err)
		}
	})
	if got.Actor.Kind != audit.ActorHost {
		t.Errorf("CLI token actor = %+v", got.Actor)
	}
	got = notedOne(t, e.store, func() {
		if err := e.auth.RevokeAPITokenByName(host, e.owner, "cli"); err != nil {
			t.Fatal(err)
		}
	})
	if got.Actor.Kind != audit.ActorHost || got.Resource.Name != "cli" {
		t.Errorf("CLI revoke = %+v", got)
	}
}

func TestAuditNotesPeople(t *testing.T) {
	e := newPeopleEnv(t)
	owner := e.as(t, e.owner)
	ctx := context.Background()

	var invited Invited
	got := notedOne(t, e.store, func() {
		var err error
		if invited, err = e.svc.InvitePerson(owner, memberIn("ana@example.com", ProjectRole{Project: "shop", Role: RoleDeploy})); err != nil {
			t.Fatal(err)
		}
	})
	id := invited.Person.ID
	wantEntry(t, got, audit.ActionInvited, audit.KindPerson, id)
	if got.Actor.Kind != audit.ActorPerson || got.Actor.Email != "you@example.com" || got.Resource.Name != "ana@example.com" {
		t.Errorf("invite = actor %+v resource %+v", got.Actor, got.Resource)
	}
	if c := change(got.Changes, "projects[shop].role"); c == nil && change(got.Changes, "projects") == nil {
		t.Errorf("the invite lists no role: %v", changePaths(got.Changes))
	}
	if strings.Contains(everyEntry(t, e.store), invited.Token) {
		t.Error("the invite token is in the log")
	}
	// A refused invite (the email is taken) notes nothing.
	if n := len(noted(t, e.store, func() { _, _ = e.svc.InvitePerson(owner, memberIn("ana@example.com")) })); n != 0 {
		t.Errorf("a refused invite wrote %d entries", n)
	}

	got = notedOne(t, e.store, func() {
		if invited, err := e.svc.ReinvitePerson(owner, id); err != nil {
			t.Fatal(err)
		} else if strings.Contains(everyEntry(t, e.store), invited.Token) {
			t.Error("the new invite token is in the log")
		}
	})
	wantEntry(t, got, audit.ActionInvited, audit.KindPerson, id)

	// A wrong invite token is a failed sign-in of nobody.
	got = notedOne(t, e.store, func() {
		if _, err := e.svc.AcceptInvite(ctx, "cvin_wrong-token", "a long enough password", "203.0.113.7"); err == nil {
			t.Fatal("a wrong token was accepted")
		}
	})
	wantEntry(t, got, audit.ActionSignInFailed, audit.KindAccount, "")
	if got.Actor != (audit.Actor{Kind: audit.ActorAnonymous}) || got.Address != "203.0.113.7" {
		t.Errorf("failed invite = actor %+v address %q", got.Actor, got.Address)
	}

	// The person who accepts is the actor.
	var token string
	{
		again, err := e.svc.ReinvitePerson(owner, id)
		if err != nil {
			t.Fatal(err)
		}
		token = again.Token
	}
	got = notedOne(t, e.store, func() { e.accept(t, token, "203.0.113.8") })
	wantEntry(t, got, audit.ActionAcceptedInvite, audit.KindPerson, id)
	if got.Actor != (audit.Actor{Kind: audit.ActorPerson, Email: "ana@example.com", PersonID: id}) || got.Address != "203.0.113.8" {
		t.Errorf("accept = actor %+v address %q", got.Actor, got.Address)
	}

	// An update shows the role diff.
	got = notedOne(t, e.store, func() {
		if _, err := e.svc.UpdatePerson(owner, id, PersonInput{OrgRole: OrgMember, Projects: []ProjectRole{{Project: "shop", Role: RoleMaintain}}}); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindPerson, id)
	if c := change(got.Changes, "projects[shop].role"); c == nil || c.From != "deploy" || c.To != "maintain" {
		t.Errorf("update changes = %v", changePaths(got.Changes))
	}
	// The same again changes nothing.
	if n := len(noted(t, e.store, func() {
		_, _ = e.svc.UpdatePerson(owner, id, PersonInput{OrgRole: OrgMember, Projects: []ProjectRole{{Project: "shop", Role: RoleMaintain}}})
	})); n != 0 {
		t.Errorf("an update that changed nothing wrote %d entries", n)
	}

	got = notedOne(t, e.store, func() {
		if err := e.svc.RemovePerson(owner, id); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionRemoved, audit.KindPerson, id)
	// A refused removal (the last owner) notes nothing.
	if n := len(noted(t, e.store, func() { _ = e.svc.RemovePerson(owner, e.owner) })); n != 0 {
		t.Errorf("a refused removal wrote %d entries", n)
	}
}
