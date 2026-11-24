package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/store"
)

// addPerson stores a person with testPassword (an invited one has none) and returns them. The
// store is used directly: inviting people is the service's job, not Auth's.
func (e *authEnv) addPerson(t *testing.T, email, orgRole, state string, change ...func(*store.PersonRecord)) store.PersonRecord {
	t.Helper()
	hash := ""
	if state == personActive {
		var err error
		if hash, err = hashPassword(testPassword, 1000); err != nil {
			t.Fatal(err)
		}
	}
	p := store.PersonRecord{ID: newPersonID(), Email: email, OrgRole: orgRole, State: state, PasswordHash: hash,
		PasswordSignIn: true, CreatedAt: e.clock.Now()}
	for _, f := range change {
		f(&p)
	}
	if err := e.store.CreatePerson(context.Background(), p, nil); err != nil {
		t.Fatalf("CreatePerson(%s): %v", email, err)
	}
	return p
}

// signInAs signs in as email with testPassword and returns the session value.
func (e *authEnv) signInAs(t *testing.T, email string) string {
	t.Helper()
	s, err := e.auth.SignIn(context.Background(), email, testPassword, "", addrA)
	if err != nil {
		t.Fatalf("SignIn(%s): %v", email, err)
	}
	return s.Value
}

func TestSignInAmongPeople(t *testing.T) {
	ctx := context.Background()
	e := newAuthEnvWithAdmin(t)
	ana := e.addPerson(t, "ana@example.com", "member", personActive)

	// Each person signs in with their own email and gets their own session.
	ownerSession := e.signInAs(t, testEmail)
	anaSession := e.signInAs(t, "  Ana@Example.com ")
	who, err := e.auth.CheckSession(ctx, anaSession)
	if err != nil || who.PersonID != ana.ID || who.Email != "ana@example.com" || who.OrgRole != OrgMember || who.Admin() {
		t.Errorf("Ana's session = %+v, %v; want Ana, a member", who, err)
	}
	who, err = e.auth.CheckSession(ctx, ownerSession)
	if err != nil || who.PersonID != e.owner || !who.Owner() {
		t.Errorf("the owner's session = %+v, %v; want the owner", who, err)
	}

	// Her password is hers: the owner's email with her password (they are the same here, so
	// change hers) doesn't sign in.
	if err := e.auth.ResetPassword(ctx, "ana@example.com", "another long password"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.auth.SignIn(ctx, testEmail, "another long password", "", addrA); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("the owner with Ana's password = %v, want ErrBadCredentials", err)
	}
	if _, err := e.auth.SignIn(ctx, "ana@example.com", testPassword, "", addrA); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("Ana with her old password = %v, want ErrBadCredentials", err)
	}
	if _, err := e.auth.SignIn(ctx, "ana@example.com", "another long password", "", addrA); err != nil {
		t.Errorf("Ana with her new password: %v", err)
	}
}

func TestSignInRefusalsLookAlike(t *testing.T) {
	ctx := context.Background()
	e := newAuthEnvWithAdmin(t)
	past := e.clock.Now().Add(-time.Hour)
	e.addPerson(t, "invited@example.com", "member", "invited")
	e.addPerson(t, "ended@example.com", "member", personActive, func(p *store.PersonRecord) { p.EndsAt = &past })
	e.addPerson(t, "ana@example.com", "member", personActive)

	// An unknown, invited and ended email, and a wrong password, answer the same and cost the
	// same: one password check each, so the answer doesn't say which emails are people.
	for i, tt := range []struct{ name, email, password string }{
		{"unknown email", "nobody@example.com", testPassword},
		{"invited", "invited@example.com", testPassword},
		{"invited, with no password", "invited@example.com", ""},
		{"ended, with the right password", "ended@example.com", testPassword},
		{"wrong password", "ana@example.com", "not the password"},
	} {
		e.hashes = 0
		_, err := e.auth.SignIn(ctx, tt.email, tt.password, "", fmt.Sprintf("198.51.100.%d", i+10)) // one address each: the limit counts per address
		var failure *SigninFailure
		if !errors.As(err, &failure) || !errors.Is(err, ErrBadCredentials) {
			t.Errorf("%s: SignIn = %v, want a *SigninFailure", tt.name, err)
		}
		if e.hashes != 1 {
			t.Errorf("%s: %d password checks, want 1", tt.name, e.hashes)
		}
	}
}

func TestSignInEndDate(t *testing.T) {
	ctx := context.Background()
	e := newAuthEnvWithAdmin(t)
	end := e.clock.Now().Add(10 * time.Hour) // inside the 12 hours a session may idle
	ana := e.addPerson(t, "guest@example.com", "member", personActive, func(p *store.PersonRecord) { p.EndsAt = &end })

	session := e.signInAs(t, "guest@example.com")
	other := e.signInAs(t, "guest@example.com")
	ownerSession := e.signInAs(t, testEmail)
	if _, err := e.auth.CheckSession(ctx, session); err != nil {
		t.Fatalf("a guest before their end date: %v", err)
	}
	token, err := e.auth.CreateAPIToken(ctx, ana.ID, "ci", nil)
	if err != nil {
		t.Fatal(err)
	}

	// After the end date nothing works, and nobody had to act.
	e.clock.Advance(10 * time.Hour)
	if _, err := e.auth.CheckSession(ctx, session); !errors.Is(err, ErrNotAuthenticated) {
		t.Errorf("CheckSession after the end = %v, want ErrNotAuthenticated", err)
	}
	if _, err := e.auth.CheckAPIToken(ctx, token.Token); !errors.Is(err, ErrNotAuthenticated) {
		t.Errorf("CheckAPIToken after the end = %v, want ErrNotAuthenticated", err)
	}
	if _, err := e.auth.SignIn(ctx, "guest@example.com", testPassword, "", addrB); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("SignIn after the end = %v, want ErrBadCredentials", err)
	}
	// Their sessions were deleted by the first check, not just refused; the owner's stay.
	for _, v := range []string{session, other} {
		if _, err := e.store.GetSession(ctx, hashToken(v)); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("a session of the ended guest is still stored (%v)", err)
		}
	}
	if _, err := e.auth.CheckSession(ctx, ownerSession); err != nil {
		t.Errorf("the owner's session after the guest's end: %v", err)
	}
}

func TestPrincipalIsLoadedFresh(t *testing.T) {
	ctx := context.Background()
	e := newAuthEnvWithAdmin(t)
	if _, err := e.store.InsertProject(ctx, "shop", "Shop"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.InsertEnvironment(ctx, "shop", "staging"); err != nil {
		t.Fatal(err)
	}
	ana := e.addPerson(t, "ana@example.com", "member", personActive)
	session := e.signInAs(t, "ana@example.com")

	if who, err := e.auth.CheckSession(ctx, session); err != nil || len(who.Roles) != 0 {
		t.Fatalf("a member with no roles = %+v, %v", who, err)
	}

	end := e.clock.Now().Add(time.Hour)
	err := e.store.SetPersonRoles(ctx, ana.ID, []store.ProjectRoleRecord{
		{Project: "shop", Role: "view", EndsAt: &end, Environments: map[string]string{"staging": "deploy", "production": "none"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	who, err := e.auth.CheckSession(ctx, session) // the same session sees the new roles at once
	if err != nil || len(who.Roles) != 1 {
		t.Fatalf("after granting a role = %+v, %v", who, err)
	}
	r := who.Roles[0]
	if r.Project != "shop" || r.Role != RoleView || r.EndsAt == nil || !r.EndsAt.Equal(end) ||
		r.Environments["staging"] != RoleDeploy || r.Environments["production"] != RoleNone {
		t.Errorf("role = %+v", r)
	}
	if got := who.LevelAt("shop", "staging", e.clock.Now()); got != LevelDeploy {
		t.Errorf("LevelAt(shop, staging) = %v, want deploy", got)
	}
	// Quiet checks load it too.
	if q, err := e.auth.CheckSessionQuiet(ctx, session); err != nil || len(q.Roles) != 1 {
		t.Errorf("CheckSessionQuiet = %+v, %v", q, err)
	}

	// A promotion to admin applies to the next request, and an admin carries no project roles.
	if err := e.store.UpdatePerson(ctx, ana.ID, "admin", nil); err != nil {
		t.Fatal(err)
	}
	if who, err := e.auth.CheckSession(ctx, session); err != nil || !who.Admin() || len(who.Roles) != 0 {
		t.Errorf("after promoting = %+v, %v; want an admin with no roles loaded", who, err)
	}
}

func TestRemovedPersonIsRefusedAtOnce(t *testing.T) {
	ctx := context.Background()
	e := newAuthEnvWithAdmin(t)
	ana := e.addPerson(t, "ana@example.com", "member", personActive)
	session := e.signInAs(t, "ana@example.com")
	token, err := e.auth.CreateAPIToken(ctx, ana.ID, "ci", nil)
	if err != nil {
		t.Fatal(err)
	}
	ownerSession := e.signInAs(t, testEmail)
	if _, err := e.auth.CheckAPIToken(ctx, token.Token); err != nil {
		t.Fatal(err)
	}

	if err := e.store.DeletePerson(ctx, ana.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.auth.CheckSession(ctx, session); !errors.Is(err, ErrNotAuthenticated) {
		t.Errorf("CheckSession of a removed person = %v, want ErrNotAuthenticated", err)
	}
	if _, err := e.auth.CheckAPIToken(ctx, token.Token); !errors.Is(err, ErrNotAuthenticated) {
		t.Errorf("CheckAPIToken of a removed person = %v, want ErrNotAuthenticated", err)
	}
	if _, err := e.auth.SignIn(ctx, "ana@example.com", testPassword, "", addrB); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("SignIn of a removed person = %v, want ErrBadCredentials", err)
	}
	if _, err := e.auth.CheckSession(ctx, ownerSession); err != nil {
		t.Errorf("the owner after removing Ana: %v", err)
	}
}

func TestTokensBelongToAPerson(t *testing.T) {
	ctx := context.Background()
	e := newAuthEnvWithAdmin(t)
	if _, err := e.store.InsertProject(ctx, "shop", "Shop"); err != nil {
		t.Fatal(err)
	}
	ana := e.addPerson(t, "ana@example.com", "member", personActive)
	invited := e.addPerson(t, "new@example.com", "member", "invited")

	// The same name for two people is fine; it must be unique among one person's active tokens.
	mine, err := e.auth.CreateAPIToken(ctx, e.owner, "ci", nil)
	if err != nil {
		t.Fatal(err)
	}
	hers, err := e.auth.CreateScopedAPIToken(ctx, ana.ID, "ci", nil, &TokenScope{Project: "shop", App: "web"})
	if err != nil {
		t.Fatalf("Ana's token ci next to the owner's: %v", err)
	}
	if _, err := e.auth.CreateAPIToken(ctx, ana.ID, "ci", nil); !errors.Is(err, ErrTokenNameTaken) {
		t.Errorf("a second ci for Ana = %v, want ErrTokenNameTaken", err)
	}
	if _, err := e.auth.CreateAPIToken(ctx, invited.ID, "ci", nil); !errors.Is(err, ErrNoSuchPerson) {
		t.Errorf("a token for an invited person = %v, want ErrNoSuchPerson", err)
	}
	if _, err := e.auth.CreateAPIToken(ctx, "nobody", "ci", nil); !errors.Is(err, ErrNoSuchPerson) {
		t.Errorf("a token for a person who doesn't exist = %v, want ErrNoSuchPerson", err)
	}

	// A token acts as its person, and its name and scope are in the principal.
	who, err := e.auth.CheckAPIToken(ctx, hers.Token)
	if err != nil || who.PersonID != ana.ID || who.Email != "ana@example.com" || who.OrgRole != OrgMember ||
		who.TokenName != "ci" || who.Scope == nil || who.Scope.App != "web" {
		t.Errorf("Ana's token = %+v, %v", who, err)
	}
	who, err = e.auth.CheckAPIToken(ctx, mine.Token)
	if err != nil || who.PersonID != e.owner || who.Scope != nil || !who.Owner() {
		t.Errorf("the owner's token = %+v, %v", who, err)
	}

	// Lists show a person's own, and nobody can revoke another's.
	list, err := e.auth.ListAPITokens(ctx, ana.ID)
	if err != nil || len(list) != 1 || list[0].ID != hers.Info.ID {
		t.Errorf("Ana's list = %+v, %v; want only hers", list, err)
	}
	if err := e.auth.RevokeAPIToken(ctx, ana.ID, mine.Info.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Ana revoking the owner's token = %v, want store.ErrNotFound", err)
	}
	if _, err := e.auth.CheckAPIToken(ctx, mine.Token); err != nil {
		t.Errorf("the owner's token after Ana's attempt: %v", err)
	}
	if err := e.auth.RevokeAPITokenByName(ctx, ana.ID, "ci"); err != nil {
		t.Errorf("Ana revoking her own ci by name: %v", err)
	}
	if _, err := e.auth.CheckAPIToken(ctx, hers.Token); !errors.Is(err, ErrNotAuthenticated) {
		t.Errorf("Ana's revoked token = %v, want ErrNotAuthenticated", err)
	}
	if _, err := e.auth.CheckAPIToken(ctx, mine.Token); err != nil {
		t.Errorf("the owner's ci after Ana revoked hers by name: %v", err)
	}
}

func TestPasswordsEndOnlyTheirOwnersSessions(t *testing.T) {
	ctx := context.Background()
	e := newAuthEnvWithAdmin(t)
	ana := e.addPerson(t, "ana@example.com", "member", personActive)
	ownerSession := e.signInAs(t, testEmail)
	anaOne, anaTwo := e.signInAs(t, "ana@example.com"), e.signInAs(t, "ana@example.com")

	// Ana changes her password from one session: her other one ends, the owner's doesn't.
	if err := e.auth.ChangePassword(ctx, ana.ID, testPassword, "a brand new password", anaOne); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	for v, want := range map[string]bool{anaOne: true, anaTwo: false, ownerSession: true} {
		if _, err := e.auth.CheckSession(ctx, v); (err == nil) != want {
			t.Errorf("a session alive = %v after Ana changed her password, want %v", err == nil, want)
		}
	}
	// The current password must be hers.
	if err := e.auth.ChangePassword(ctx, ana.ID, testPassword, "yet another password", anaOne); !errors.Is(err, ErrWrongPassword) {
		t.Errorf("ChangePassword with the old password = %v, want ErrWrongPassword", err)
	}

	// A reset at the host ends that person's sessions and no one else's.
	if err := e.auth.ResetPassword(ctx, "ANA@example.com", "reset by the owner"); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	if _, err := e.auth.CheckSession(ctx, anaOne); !errors.Is(err, ErrNotAuthenticated) {
		t.Errorf("Ana's session after the reset = %v, want ErrNotAuthenticated", err)
	}
	if _, err := e.auth.CheckSession(ctx, ownerSession); err != nil {
		t.Errorf("the owner's session after Ana's reset: %v", err)
	}
}

func TestHostCLIFindsPeople(t *testing.T) {
	ctx := context.Background()
	e := newAuthEnv(t)
	if _, err := e.auth.ResolvePerson(ctx, ""); !errors.Is(err, ErrNoAdmin) {
		t.Errorf("ResolvePerson before anyone = %v, want ErrNoAdmin", err)
	}
	if _, err := e.auth.ResolvePerson(ctx, "x@example.com"); !errors.Is(err, ErrNoAdmin) {
		t.Errorf("ResolvePerson(email) before anyone = %v, want ErrNoAdmin", err)
	}
	if err := e.auth.ResetPassword(ctx, "", "long enough password"); !errors.Is(err, ErrNoAdmin) {
		t.Errorf("ResetPassword before anyone = %v, want ErrNoAdmin", err)
	}
	if _, err := e.auth.FirstOwnerEmail(ctx); !errors.Is(err, ErrNoAdmin) {
		t.Errorf("FirstOwnerEmail before anyone = %v, want ErrNoAdmin", err)
	}

	if err := e.auth.InitAdmin(ctx, testEmail, testPassword); err != nil {
		t.Fatal(err)
	}
	// Once anyone exists, init is refused, whatever the email.
	if err := e.auth.InitAdmin(ctx, "another@example.com", testPassword); !errors.Is(err, ErrAdminExists) {
		t.Errorf("a second InitAdmin = %v, want ErrAdminExists", err)
	}
	ana := e.addPerson(t, "ana@example.com", "member", personActive)
	e.addPerson(t, "new@example.com", "member", "invited")

	for email, want := range map[string]string{"": "you@example.com", " ANA@example.com ": "ana@example.com"} {
		if got, err := e.auth.ResolvePerson(ctx, email); err != nil || got.Email != want {
			t.Errorf("ResolvePerson(%q) = %+v, %v; want %s", email, got, err, want)
		}
	}
	if got, _ := e.auth.ResolvePerson(ctx, "ana@example.com"); got.ID != ana.ID {
		t.Errorf("ResolvePerson ID = %q, want Ana's %q", got.ID, ana.ID)
	}
	for _, email := range []string{"nobody@example.com", "new@example.com"} {
		if _, err := e.auth.ResolvePerson(ctx, email); !errors.Is(err, ErrNoSuchPerson) {
			t.Errorf("ResolvePerson(%q) = %v, want ErrNoSuchPerson (unknown and invited alike)", email, err)
		}
		if err := e.auth.ResetPassword(ctx, email, "long enough password"); !errors.Is(err, ErrNoSuchPerson) {
			t.Errorf("ResetPassword(%q) = %v, want ErrNoSuchPerson", email, err)
		}
	}

	people, err := e.auth.ListPeople(ctx)
	if err != nil || len(people) != 3 || people[0].Email != "you@example.com" || people[0].OrgRole != "owner" ||
		people[1].Email != "ana@example.com" || people[2].State != "invited" {
		t.Errorf("ListPeople = %+v, %v", people, err)
	}
}

func TestPasswordSignInIsPerPerson(t *testing.T) {
	ctx := context.Background()
	e := newAuthEnvWithAdmin(t)
	ana := e.addPerson(t, "ana@example.com", "member", personActive, func(p *store.PersonRecord) { p.PasswordSignIn = false })
	if err := e.store.InsertPasskey(ctx, store.PasskeyRecord{ID: "pk1", PersonID: ana.ID, UserHandle: []byte("h"), CredentialID: []byte("c1"),
		RPID: "console.example.com", PublicKey: []byte("k"), AttestationType: "none", AttestationFormat: "none",
		AAGUID: []byte{}, Name: "Mac", CreatedAt: e.clock.Now()}); err != nil {
		t.Fatal(err)
	}

	// Ana's password is off and she has a passkey there; the owner's is on.
	if _, err := e.auth.SignIn(ctx, "ana@example.com", testPassword, "console.example.com", addrA); !errors.Is(err, ErrPasswordSignInOff) {
		t.Errorf("Ana at the passkey's domain = %v, want ErrPasswordSignInOff", err)
	}
	if _, err := e.auth.SignIn(ctx, testEmail, testPassword, "console.example.com", addrA); err != nil {
		t.Errorf("the owner at the same domain: %v", err)
	}
	// Where she has no passkey her password still works: a domain change never locks her out.
	if _, err := e.auth.SignIn(ctx, "ana@example.com", testPassword, "new.example.com", addrA); err != nil {
		t.Errorf("Ana at a domain with no passkey of hers: %v", err)
	}
	if ok, err := e.auth.PasswordSignInAllowed(ctx, ana.ID, "console.example.com"); err != nil || ok {
		t.Errorf("PasswordSignInAllowed(Ana) = %v, %v; want false", ok, err)
	}
	if ok, err := e.auth.PasswordSignInAllowed(ctx, e.owner, "console.example.com"); err != nil || !ok {
		t.Errorf("PasswordSignInAllowed(owner) = %v, %v; want true", ok, err)
	}
	st, err := e.auth.SignInSettings(ctx, ana.ID)
	if err != nil || st.PasswordSignIn || st.Passkeys != 1 {
		t.Errorf("Ana's settings = %+v, %v", st, err)
	}
	if st, _ := e.auth.SignInSettings(ctx, e.owner); !st.PasswordSignIn || st.Passkeys != 0 {
		t.Errorf("the owner's settings = %+v, want password on and no passkeys", st)
	}

	// The sign-in page draws the password form while anyone can use it here.
	if ok, err := e.auth.AnyPasswordSignIn(ctx, "console.example.com"); err != nil || !ok {
		t.Errorf("AnyPasswordSignIn with the owner's password on = %v, %v; want true", ok, err)
	}
	if err := e.store.InsertPasskey(ctx, store.PasskeyRecord{ID: "pk2", PersonID: e.owner, UserHandle: []byte("h2"), CredentialID: []byte("c2"),
		RPID: "console.example.com", PublicKey: []byte("k"), AttestationType: "none", AttestationFormat: "none",
		AAGUID: []byte{}, Name: "Key", CreatedAt: e.clock.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetPasswordSignIn(ctx, e.owner, false); err != nil {
		t.Fatal(err)
	}
	if ok, err := e.auth.AnyPasswordSignIn(ctx, "console.example.com"); err != nil || ok {
		t.Errorf("AnyPasswordSignIn with everyone passkey-only = %v, %v; want false", ok, err)
	}
	if ok, err := e.auth.AnyPasswordSignIn(ctx, "new.example.com"); err != nil || !ok {
		t.Errorf("AnyPasswordSignIn at a domain with no passkeys = %v, %v; want true", ok, err)
	}
	if _, err := newAuthEnv(t).auth.AnyPasswordSignIn(ctx, ""); !errors.Is(err, ErrNoAdmin) {
		t.Errorf("AnyPasswordSignIn before anyone = %v, want ErrNoAdmin", err)
	}
}

func TestPasskeysBelongToAPerson(t *testing.T) {
	ctx := context.Background()
	e := newAuthEnvWithAdmin(t)
	ana := e.addPerson(t, "ana@example.com", "member", personActive)
	ownerSession, anaSession := e.signInAs(t, testEmail), e.signInAs(t, "ana@example.com")

	// Each person adds a passkey from their own session; each gets their own WebAuthn handle.
	ownerKey := addPasskey(t, e, ownerSession, rpMain, "Owner's Mac")
	anaKey := addPasskey(t, e, anaSession, rpMain, "Ana's phone")
	ownerRec, _ := e.store.GetPerson(ctx, e.owner)
	anaRec, _ := e.store.GetPerson(ctx, ana.ID)
	if len(ownerRec.WebAuthnHandle) == 0 || len(anaRec.WebAuthnHandle) == 0 || string(ownerRec.WebAuthnHandle) == string(anaRec.WebAuthnHandle) {
		t.Errorf("handles: owner %x, Ana %x; want two different ones", ownerRec.WebAuthnHandle, anaRec.WebAuthnHandle)
	}

	// A passkey signs in the person who owns it, found by the credential.
	s, err := passkeySignIn(t, e, rpMain, anaKey, addrA)
	if err != nil {
		t.Fatalf("Ana's passkey sign-in: %v", err)
	}
	if who, err := e.auth.CheckSession(ctx, s.Value); err != nil || who.PersonID != ana.ID {
		t.Errorf("the session of Ana's passkey = %+v, %v; want Ana", who, err)
	}
	s, err = passkeySignIn(t, e, rpMain, ownerKey, addrA)
	if err != nil {
		t.Fatalf("the owner's passkey sign-in: %v", err)
	}
	if who, err := e.auth.CheckSession(ctx, s.Value); err != nil || who.PersonID != e.owner {
		t.Errorf("the session of the owner's passkey = %+v, %v; want the owner", who, err)
	}

	// Lists, renames and removals reach only the caller's own.
	list, err := e.auth.ListPasskeys(ctx, ana.ID)
	if err != nil || len(list) != 1 || list[0].Name != "Ana's phone" || list[0].PersonID != ana.ID {
		t.Fatalf("Ana's passkeys = %+v, %v", list, err)
	}
	ownersID := ""
	if l, _ := e.auth.ListPasskeys(ctx, e.owner); len(l) == 1 {
		ownersID = l[0].ID
	}
	if _, err := e.auth.RenamePasskey(ctx, ana.ID, ownersID, "mine now"); !errors.Is(err, ErrPasskeyNotFound) {
		t.Errorf("Ana renaming the owner's passkey = %v, want ErrPasskeyNotFound", err)
	}
	if err := e.auth.RemovePasskey(ctx, ana.ID, ownersID); !errors.Is(err, ErrPasskeyNotFound) {
		t.Errorf("Ana removing the owner's passkey = %v, want ErrPasskeyNotFound", err)
	}
	if all, err := e.auth.ListAllPasskeys(ctx); err != nil || len(all) != 2 {
		t.Errorf("ListAllPasskeys = %+v, %v; want both people's", all, err)
	}

	// Removing a person who has one ends it: their key signs in nothing.
	if err := e.store.DeletePerson(ctx, ana.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := passkeySignIn(t, e, rpMain, anaKey, addrB); !errors.Is(err, ErrPasskeyRefused) {
		t.Errorf("a removed person's passkey = %v, want ErrPasskeyRefused", err)
	}
}

func TestPasskeyOfAnEndedPersonSignsNoOneIn(t *testing.T) {
	e := newAuthEnvWithAdmin(t)
	end := e.clock.Now().Add(time.Hour)
	e.addPerson(t, "guest@example.com", "member", personActive, func(p *store.PersonRecord) { p.EndsAt = &end })
	key := addPasskey(t, e, e.signInAs(t, "guest@example.com"), rpMain, "Guest phone")
	if _, err := passkeySignIn(t, e, rpMain, key, addrA); err != nil {
		t.Fatalf("a guest's passkey before the end: %v", err)
	}
	e.clock.Advance(time.Hour)
	if _, err := passkeySignIn(t, e, rpMain, key, addrB); !errors.Is(err, ErrPasskeyRefused) {
		t.Errorf("a guest's passkey after the end = %v, want ErrPasskeyRefused", err)
	}
}
