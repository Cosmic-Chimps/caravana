package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// peopleEnv is a service with an Auth (as cmd/caravana wires it) on the shop installation, whose Auth
// and service share one clock, and its first owner.
type peopleEnv struct {
	*env
	auth  *Auth
	clock *fakeClock
	owner string // the owner's ID
}

func newPeopleEnv(t *testing.T) *peopleEnv {
	t.Helper()
	ctx := context.Background()
	e := shopEnv(t)
	clock := e.svc.clock.(*fakeClock)
	auth := NewAuth(e.store, slog.New(slog.DiscardHandler), AuthOptions{Now: clock.Now, PasswordIterations: 1000})
	e.svc.SetAuth(auth)
	if err := auth.InitAdmin(ctx, "you@example.com", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	who, err := auth.ResolvePerson(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	return &peopleEnv{env: e, auth: auth, clock: clock, owner: who.ID}
}

// as is the context of a person loaded the way a request loads them.
func (e *peopleEnv) as(t *testing.T, id string) context.Context {
	t.Helper()
	rec, err := e.store.GetPerson(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.auth.principalOf(context.Background(), rec, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	return WithPrincipal(context.Background(), p)
}

// invite invites and returns the person's ID and token.
func (e *peopleEnv) invite(t *testing.T, in PersonInput) (id, token string) {
	t.Helper()
	got, err := e.svc.InvitePerson(context.Background(), in)
	if err != nil {
		t.Fatalf("InvitePerson(%s): %v", in.Email, err)
	}
	return got.Person.ID, got.Token
}

// accept accepts an invite from address and returns the person.
func (e *peopleEnv) accept(t *testing.T, token, address string) Person {
	t.Helper()
	p, err := e.svc.AcceptInvite(context.Background(), token, "a long enough password", address)
	if err != nil {
		t.Fatalf("AcceptInvite: %v", err)
	}
	return p
}

func memberIn(email string, projects ...ProjectRole) PersonInput {
	return PersonInput{Email: email, OrgRole: OrgMember, Projects: projects}
}

func TestInvitePerson(t *testing.T) {
	ctx := context.Background()
	e := newPeopleEnv(t)
	end := e.clock.Now().Add(30 * 24 * time.Hour)
	roleEnd := e.clock.Now().Add(48 * time.Hour)

	got, err := e.svc.InvitePerson(ctx, PersonInput{Email: "  Ana@Example.com ", OrgRole: OrgMember, EndsAt: &end, Projects: []ProjectRole{
		{Project: "shop", Role: RoleView, EndsAt: &roleEnd, Environments: map[string]Role{"staging": RoleDeploy, "production": RoleNone}},
	}})
	if err != nil {
		t.Fatalf("InvitePerson: %v", err)
	}
	p := got.Person
	if p.Email != "ana@example.com" || p.OrgRole != OrgMember || p.State != "invited" || len(p.ID) != 26 || p.EndsAt == nil || !p.EndsAt.Equal(end) {
		t.Errorf("person = %+v", p)
	}
	if want := e.clock.Now().Add(7 * 24 * time.Hour); p.InviteExpiresAt == nil || !p.InviteExpiresAt.Equal(want) {
		t.Errorf("the invite expires %v, want in 7 days (%v)", p.InviteExpiresAt, want)
	}
	if !strings.HasPrefix(got.Token, "cvin_") || len(got.Token) < 50 {
		t.Errorf("token = %q, want cvin_ and 32 random bytes", got.Token)
	}
	if len(p.Projects) != 1 || p.Projects[0].Role != RoleView || p.Projects[0].Environments["staging"] != RoleDeploy ||
		p.Projects[0].Environments["production"] != RoleNone || p.Projects[0].EndsAt == nil || !p.Projects[0].EndsAt.Equal(roleEnd) {
		t.Errorf("roles = %+v", p.Projects)
	}
	rec, _ := e.store.GetPerson(ctx, p.ID)
	if rec.InviteHash != hashToken(got.Token) || rec.InviteHash == got.Token || rec.PasswordHash != "" || rec.InvitedBy != "" {
		t.Errorf("stored = %+v; want only the token's hash, no password, no inviter for the host", rec)
	}
	if strings.Contains(rec.InviteHash, "cvin_") {
		t.Error("the stored invite holds the token")
	}

	// Two invites are two tokens.
	other, _ := e.svc.InvitePerson(ctx, memberIn("ben@example.com"))
	if other.Token == got.Token {
		t.Error("two invites have the same token")
	}

	t.Run("the link", func(t *testing.T) {
		for base, want := range map[string]string{
			"https://console.example.com":  "https://console.example.com/invite?token=" + got.Token,
			"https://console.example.com/": "https://console.example.com/invite?token=" + got.Token,
			"":                             "/invite?token=" + got.Token,
		} {
			if link := InviteURL(base, got.Token); link != want {
				t.Errorf("InviteURL(%q) = %q, want %q", base, link, want)
			}
		}
	})

	t.Run("who invited", func(t *testing.T) {
		inviter := Principal{PersonID: e.owner, Email: "you@example.com", OrgRole: OrgOwner}
		got, err := e.svc.InvitePerson(WithPrincipal(ctx, inviter), memberIn("cat@example.com"))
		if err != nil || got.Person.InvitedBy != "you@example.com" {
			t.Errorf("InvitedBy = %q, %v; want the owner's email", got.Person.InvitedBy, err)
		}
	})
}

func TestInvitePersonRefusals(t *testing.T) {
	ctx := context.Background()
	e := newPeopleEnv(t)
	e.invite(t, memberIn("taken@example.com"))
	past := e.clock.Now().Add(-time.Hour)
	ownerCtx := e.as(t, e.owner)

	tests := []struct {
		name  string
		in    PersonInput
		field string
		is    error
	}{
		{"a bad email", PersonInput{Email: "nope", OrgRole: OrgMember}, "email", nil},
		{"no org role", PersonInput{Email: "a@example.com"}, "orgRole", nil},
		{"a role that doesn't exist", PersonInput{Email: "a@example.com", OrgRole: "boss"}, "orgRole", nil},
		{"an end in the past", PersonInput{Email: "a@example.com", OrgRole: OrgMember, EndsAt: &past}, "endsAt", nil},
		{"roles for an admin", PersonInput{Email: "a@example.com", OrgRole: OrgAdmin, Projects: []ProjectRole{{Project: "shop", Role: RoleView}}}, "projects", nil},
		{"roles for an owner", PersonInput{Email: "a@example.com", OrgRole: OrgOwner, Projects: []ProjectRole{{Project: "shop", Role: RoleView}}}, "projects", nil},
		{"a project role that doesn't exist", memberIn("a@example.com", ProjectRole{Project: "shop", Role: "boss"}), "projects[0].role", nil},
		{"no project role at all", memberIn("a@example.com", ProjectRole{Project: "shop"}), "projects[0].role", nil},
		{"none for a whole project", memberIn("a@example.com", ProjectRole{Project: "shop", Role: RoleNone}), "projects[0].role", nil},
		{"a project twice", memberIn("a@example.com", ProjectRole{Project: "shop", Role: RoleView}, ProjectRole{Project: "shop", Role: RoleDeploy}), "projects[1].project", nil},
		{"a role that ended already", memberIn("a@example.com", ProjectRole{Project: "shop", Role: RoleView, EndsAt: &past}), "projects[0].endsAt", nil},
		{"an environment role that doesn't exist", memberIn("a@example.com", ProjectRole{Project: "shop", Role: RoleView, Environments: map[string]Role{"staging": "boss"}}),
			"projects[0].environments.staging", nil},
		{"a project that doesn't exist", memberIn("a@example.com", ProjectRole{Project: "nowhere", Role: RoleView}), "projects[0].project", ErrProjectNotFound},
		{"the second project that doesn't exist", memberIn("a@example.com", ProjectRole{Project: "shop", Role: RoleView}, ProjectRole{Project: "nowhere", Role: RoleView}),
			"projects[1].project", ErrProjectNotFound},
		{"an environment that doesn't exist", memberIn("a@example.com", ProjectRole{Project: "shop", Role: RoleView, Environments: map[string]Role{"qa": RoleDeploy}}),
			"projects[0].environments.qa", ErrEnvironmentNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := e.svc.InvitePerson(ownerCtx, tt.in)
			var fe *app.FieldError
			var pe *PlaceError
			switch {
			case tt.is != nil:
				if !errors.As(err, &pe) || pe.Field != tt.field || !errors.Is(err, tt.is) {
					t.Fatalf("InvitePerson = %v, want a *PlaceError on %s wrapping %v", err, tt.field, tt.is)
				}
			case !errors.As(err, &fe) || fe.Field != tt.field:
				t.Fatalf("InvitePerson = %v, want a field error on %s", err, tt.field)
			}
		})
	}

	t.Run("an email someone has", func(t *testing.T) {
		for _, email := range []string{"taken@example.com", " TAKEN@example.com", "you@example.com"} {
			if _, err := e.svc.InvitePerson(ctx, memberIn(email)); !errors.Is(err, ErrEmailTaken) {
				t.Errorf("InvitePerson(%q) = %v, want ErrEmailTaken", email, err)
			}
		}
	})

	t.Run("a refused invite leaves nobody", func(t *testing.T) {
		before, _ := e.store.ListPeople(ctx)
		e.svc.InvitePerson(ownerCtx, memberIn("ghost@example.com", ProjectRole{Project: "shop", Role: RoleView}, ProjectRole{Project: "nowhere", Role: RoleView}))
		after, _ := e.store.ListPeople(ctx)
		if len(after) != len(before) {
			t.Errorf("%d people after a refused invite, was %d", len(after), len(before))
		}
	})
}

func TestWhoMayInvite(t *testing.T) {
	ctx := context.Background()
	e := newPeopleEnv(t)
	adminID, _ := e.invite(t, PersonInput{Email: "admin@example.com", OrgRole: OrgAdmin})
	e.accept(t, mustToken(t, e, adminID), addrA)
	memberID, _ := e.invite(t, memberIn("member@example.com"))
	e.accept(t, mustToken(t, e, memberID), addrA)

	adminCtx, memberCtx, ownerCtx := e.as(t, adminID), e.as(t, memberID), e.as(t, e.owner)

	if _, err := e.svc.InvitePerson(adminCtx, memberIn("m2@example.com", ProjectRole{Project: "shop", Role: RoleView})); err != nil {
		t.Errorf("an admin inviting a member = %v, want allowed", err)
	}
	for _, role := range []OrgRole{OrgAdmin, OrgOwner} {
		_, err := e.svc.InvitePerson(adminCtx, PersonInput{Email: "x-" + string(role) + "@example.com", OrgRole: role})
		var na *NotAllowedError
		if !errors.As(err, &na) || na.Org != "owner" || err.Error() != "this needs an owner" {
			t.Errorf("an admin inviting an %s = %v, want a refusal that needs an owner", role, err)
		}
	}
	for _, role := range []OrgRole{OrgAdmin, OrgOwner, OrgMember} {
		_, err := e.svc.InvitePerson(memberCtx, PersonInput{Email: "y-" + string(role) + "@example.com", OrgRole: role})
		var na *NotAllowedError
		if !errors.As(err, &na) || na.Org != "admin" || err.Error() != "this needs an owner or an admin" {
			t.Errorf("a member inviting an %s = %v, want a refusal that needs an owner or an admin", role, err)
		}
	}
	for _, role := range []OrgRole{OrgAdmin, OrgOwner} {
		if _, err := e.svc.InvitePerson(ownerCtx, PersonInput{Email: "z-" + string(role) + "@example.com", OrgRole: role}); err != nil {
			t.Errorf("an owner inviting an %s = %v, want allowed", role, err)
		}
	}
	if _, err := e.svc.InvitePerson(ctx, PersonInput{Email: "host@example.com", OrgRole: OrgOwner}); err != nil {
		t.Errorf("the host (no principal) inviting an owner = %v, want allowed", err)
	}
	// Nobody refused was made.
	if _, err := e.store.GetPersonByEmail(ctx, "x-admin@example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Error("a refused invite made a person")
	}
}

// mustToken gives an invited person a new link and returns its token, for tests that start from an ID.
func mustToken(t *testing.T, e *peopleEnv, id string) string {
	t.Helper()
	got, err := e.svc.ReinvitePerson(context.Background(), id)
	if err != nil {
		t.Fatalf("ReinvitePerson: %v", err)
	}
	return got.Token
}

func TestAcceptInvite(t *testing.T) {
	ctx := context.Background()
	e := newPeopleEnv(t)
	id, token := e.invite(t, memberIn("ana@example.com", ProjectRole{Project: "shop", Role: RoleDeploy}))

	// A short password costs nothing, and the invite still works.
	for i := range 7 {
		_, err := e.svc.AcceptInvite(ctx, token, "short", addrA)
		var fe *app.FieldError
		if !errors.As(err, &fe) || fe.Field != "password" {
			t.Fatalf("try %d with a short password = %v, want a field error on password", i, err)
		}
	}
	rec, _ := e.store.GetPerson(ctx, id)
	if rec.State != "invited" || rec.InviteHash == "" {
		t.Fatalf("a refused accept changed the person: %+v", rec)
	}

	p := e.accept(t, token, addrA)
	if p.ID != id || p.State != "active" || p.InviteExpiresAt != nil || len(p.Projects) != 1 {
		t.Errorf("person = %+v", p)
	}
	rec, _ = e.store.GetPerson(ctx, id)
	if rec.InviteHash != "" || rec.InviteExpiresAt != nil || rec.PasswordHash == "" || strings.Contains(rec.PasswordHash, "a long enough password") ||
		rec.PasswordChangedAt == nil {
		t.Errorf("stored = %+v; want the invite cleared and a password hash", rec)
	}
	// The person signs in with the password they chose, and not before.
	if _, err := e.auth.SignIn(ctx, "ana@example.com", "a long enough password", "", "198.51.100.1"); err != nil {
		t.Errorf("SignIn after accepting: %v", err)
	}

	t.Run("a used token", func(t *testing.T) {
		if _, err := e.svc.AcceptInvite(ctx, token, "another long password", "198.51.100.2"); !errors.Is(err, ErrInviteInvalid) {
			t.Errorf("a second accept = %v, want ErrInviteInvalid", err)
		}
		if _, err := e.auth.SignIn(ctx, "ana@example.com", "a long enough password", "", "198.51.100.3"); err != nil {
			t.Errorf("the second accept changed the password: %v", err)
		}
	})
}

func TestAcceptInviteRefusals(t *testing.T) {
	ctx := context.Background()
	e := newPeopleEnv(t)

	t.Run("unknown tokens count against the address until it is locked", func(t *testing.T) {
		for i := 1; i <= 4; i++ {
			if _, err := e.svc.AcceptInvite(ctx, "cvin_nope"+strings.Repeat("x", i), "a long enough password", addrB); !errors.Is(err, ErrInviteInvalid) {
				t.Fatalf("try %d = %v, want ErrInviteInvalid", i, err)
			}
		}
		_, err := e.svc.AcceptInvite(ctx, "cvin_nopeyy", "a long enough password", addrB) // the fifth failure locks it
		var limited *SigninLimitError
		if !errors.As(err, &limited) {
			t.Fatalf("the fifth failure = %v, want a *SigninLimitError", err)
		}

		// A locked address is refused before the token is looked at: even a good one.
		_, token := e.invite(t, memberIn("late@example.com"))
		if _, err := e.svc.AcceptInvite(ctx, token, "a long enough password", addrB); !errors.As(err, &limited) {
			t.Errorf("a good token from a locked address = %v, want a *SigninLimitError", err)
		}
		if rec, _ := e.store.GetPersonByEmail(ctx, "late@example.com"); rec.State != "invited" {
			t.Error("a locked address accepted an invite")
		}
		// The same limit stops sign-in from that address.
		if _, err := e.auth.SignIn(ctx, "you@example.com", "correct horse battery", "", addrB); !errors.As(err, &limited) {
			t.Errorf("SignIn from the address locked by invites = %v, want a *SigninLimitError", err)
		}
		// Another address is untouched.
		if _, err := e.svc.AcceptInvite(ctx, token, "a long enough password", "198.51.100.77"); err != nil {
			t.Errorf("another address = %v, want allowed", err)
		}
	})

	t.Run("the wrong kinds of value", func(t *testing.T) {
		for i, token := range []string{"", "cvwh_" + strings.Repeat("a", 40), "cvat_" + strings.Repeat("a", 40), strings.Repeat("a", 52)} {
			if _, err := e.svc.AcceptInvite(ctx, token, "a long enough password", "198.51.100."+string(rune('1'+i))); !errors.Is(err, ErrInviteInvalid) {
				t.Errorf("token %q = %v, want ErrInviteInvalid", token, err)
			}
		}
	})

	t.Run("an expired invite", func(t *testing.T) {
		_, token := e.invite(t, memberIn("slow@example.com"))
		e.clock.Advance(7*24*time.Hour + time.Second)
		if _, err := e.svc.AcceptInvite(ctx, token, "a long enough password", "198.51.100.20"); !errors.Is(err, ErrInviteInvalid) {
			t.Errorf("an expired invite = %v, want ErrInviteInvalid", err)
		}
		rec, _ := e.store.GetPersonByEmail(ctx, "slow@example.com")
		if rec.State != "invited" {
			t.Error("an expired invite was accepted")
		}
		// A new link works.
		got, err := e.svc.ReinvitePerson(ctx, rec.ID)
		if err != nil {
			t.Fatal(err)
		}
		e.accept(t, got.Token, "198.51.100.21")
	})

	t.Run("a person whose access ended before they accepted", func(t *testing.T) {
		end := e.clock.Now().Add(time.Hour)
		_, token := e.invite(t, PersonInput{Email: "guest@example.com", OrgRole: OrgMember, EndsAt: &end})
		e.clock.Advance(2 * time.Hour)
		if _, err := e.svc.AcceptInvite(ctx, token, "a long enough password", "198.51.100.30"); !errors.Is(err, ErrInviteInvalid) {
			t.Errorf("an invite of someone whose end passed = %v, want ErrInviteInvalid", err)
		}
	})

	t.Run("a service without an Auth", func(t *testing.T) {
		bare := newEnv(t)
		if _, err := bare.svc.AcceptInvite(ctx, "cvin_x", "a long enough password", addrA); err == nil || errors.Is(err, ErrInviteInvalid) {
			t.Errorf("AcceptInvite without SetAuth = %v, want an error that says so", err)
		}
	})
}

func TestAcceptRacesOnce(t *testing.T) {
	ctx := context.Background()
	e := newPeopleEnv(t)
	_, token := e.invite(t, memberIn("race@example.com"))
	results := make(chan error, 2)
	for i := range 2 {
		go func() {
			_, err := e.svc.AcceptInvite(ctx, token, "password number "+string(rune('a'+i))+" long", "198.51.100."+string(rune('4'+i)))
			results <- err
		}()
	}
	var ok, invalid int
	for range 2 {
		switch err := <-results; {
		case err == nil:
			ok++
		case errors.Is(err, ErrInviteInvalid):
			invalid++
		default:
			t.Errorf("accept = %v", err)
		}
	}
	if ok != 1 || invalid != 1 {
		t.Errorf("two accepts of one token: %d won, %d refused; want 1 and 1", ok, invalid)
	}
}

func TestReinvitePerson(t *testing.T) {
	ctx := context.Background()
	e := newPeopleEnv(t)
	id, first := e.invite(t, memberIn("ana@example.com"))

	got, err := e.svc.ReinvitePerson(ctx, id)
	if err != nil {
		t.Fatalf("ReinvitePerson: %v", err)
	}
	if got.Token == first || !strings.HasPrefix(got.Token, "cvin_") || got.Person.State != "invited" {
		t.Errorf("reinvite = %+v", got)
	}
	// The old link is void, the new one works.
	if _, err := e.svc.AcceptInvite(ctx, first, "a long enough password", addrA); !errors.Is(err, ErrInviteInvalid) {
		t.Errorf("the old link = %v, want ErrInviteInvalid", err)
	}
	e.accept(t, got.Token, "198.51.100.40")

	// An active person has nothing to be invited to.
	if _, err := e.svc.ReinvitePerson(ctx, id); !errors.Is(err, ErrNotInvited) {
		t.Errorf("reinvite of an active person = %v, want ErrNotInvited", err)
	}
	if _, err := e.svc.ReinvitePerson(ctx, e.owner); !errors.Is(err, ErrNotInvited) {
		t.Errorf("reinvite of the owner = %v, want ErrNotInvited", err)
	}
	if _, err := e.svc.ReinvitePerson(ctx, "nobody"); !errors.Is(err, ErrPersonNotFound) {
		t.Errorf("reinvite of nobody = %v, want ErrPersonNotFound", err)
	}

	t.Run("who may", func(t *testing.T) {
		adminID, adminToken := e.invite(t, PersonInput{Email: "admin@example.com", OrgRole: OrgAdmin})
		e.accept(t, adminToken, "198.51.100.41")
		_, pending := e.invite(t, PersonInput{Email: "pending-admin@example.com", OrgRole: OrgAdmin})
		_ = pending
		pendingAdmin, _ := e.store.GetPersonByEmail(ctx, "pending-admin@example.com")
		memberID, _ := e.invite(t, memberIn("pending-member@example.com"))

		adminCtx := e.as(t, adminID)
		if _, err := e.svc.ReinvitePerson(adminCtx, memberID); err != nil {
			t.Errorf("an admin reinviting a member = %v, want allowed", err)
		}
		var na *NotAllowedError
		if _, err := e.svc.ReinvitePerson(adminCtx, pendingAdmin.ID); !errors.As(err, &na) || na.Org != "owner" {
			t.Errorf("an admin reinviting an admin = %v, want a refusal that needs an owner", err)
		}
		if _, err := e.svc.ReinvitePerson(e.as(t, e.owner), pendingAdmin.ID); err != nil {
			t.Errorf("an owner reinviting an admin = %v, want allowed", err)
		}
		memberCtx := WithPrincipal(ctx, Principal{PersonID: "m", OrgRole: OrgMember})
		if _, err := e.svc.ReinvitePerson(memberCtx, memberID); !errors.As(err, &na) || na.Org != "admin" {
			t.Errorf("a member reinviting = %v, want a refusal that needs an owner or an admin", err)
		}
	})
}

func TestUpdatePerson(t *testing.T) {
	ctx := context.Background()
	e := newPeopleEnv(t)
	id, token := e.invite(t, memberIn("ana@example.com", ProjectRole{Project: "shop", Role: RoleView}))
	e.accept(t, token, addrA)
	session, err := e.auth.SignIn(ctx, "ana@example.com", "a long enough password", "", "198.51.100.50")
	if err != nil {
		t.Fatal(err)
	}
	end := e.clock.Now().Add(24 * time.Hour)
	ownerCtx := e.as(t, e.owner)

	// Roles, end date, replaced as a whole; the next request of the person sees it.
	got, err := e.svc.UpdatePerson(ownerCtx, id, PersonInput{OrgRole: OrgMember, EndsAt: &end, Projects: []ProjectRole{
		{Project: "shop", Role: RoleDeploy, Environments: map[string]Role{"production": RoleView}},
		{Project: "default", Role: RoleView},
	}})
	if err != nil {
		t.Fatalf("UpdatePerson: %v", err)
	}
	if got.EndsAt == nil || !got.EndsAt.Equal(end) || len(got.Projects) != 2 || got.Email != "ana@example.com" {
		t.Errorf("person = %+v", got)
	}
	who, err := e.auth.CheckSession(ctx, session.Value)
	if err != nil || who.LevelAt("shop", "production", e.clock.Now()) != LevelView || who.LevelAt("shop", "staging", e.clock.Now()) != LevelDeploy {
		t.Errorf("the person's next request = %+v, %v; want the new roles", who.Roles, err)
	}
	// Taking everything away is allowed: a member with no roles sees nothing.
	if got, err = e.svc.UpdatePerson(ownerCtx, id, PersonInput{OrgRole: OrgMember}); err != nil || len(got.Projects) != 0 || got.EndsAt != nil {
		t.Errorf("clearing roles and end = %+v, %v", got, err)
	}

	t.Run("a promotion drops the roles an admin doesn't need", func(t *testing.T) {
		if _, err := e.svc.UpdatePerson(ownerCtx, id, memberIn("", ProjectRole{Project: "shop", Role: RoleView})); err != nil {
			t.Fatal(err)
		}
		got, err := e.svc.UpdatePerson(ownerCtx, id, PersonInput{OrgRole: OrgAdmin})
		if err != nil || got.OrgRole != OrgAdmin || len(got.Projects) != 0 {
			t.Errorf("promoting to admin = %+v, %v", got, err)
		}
		if roles, _ := e.store.PersonRoles(ctx, id); len(roles) != 0 {
			t.Errorf("the admin kept %d stored roles", len(roles))
		}
		if _, err := e.svc.UpdatePerson(ownerCtx, id, PersonInput{OrgRole: OrgAdmin, Projects: []ProjectRole{{Project: "shop", Role: RoleView}}}); err == nil {
			t.Error("roles for an admin were accepted")
		}
		if _, err := e.svc.UpdatePerson(ownerCtx, id, PersonInput{OrgRole: OrgMember}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		var fe *app.FieldError
		var pe *PlaceError
		if _, err := e.svc.UpdatePerson(ownerCtx, id, PersonInput{OrgRole: "boss"}); !errors.As(err, &fe) || fe.Field != "orgRole" {
			t.Errorf("a bad role = %v, want a field error on orgRole", err)
		}
		if _, err := e.svc.UpdatePerson(ownerCtx, id, memberIn("", ProjectRole{Project: "nowhere", Role: RoleView})); !errors.As(err, &pe) || pe.Field != "projects[0].project" {
			t.Errorf("an unknown project = %v, want a place error on projects[0].project", err)
		}
		if _, err := e.svc.UpdatePerson(ownerCtx, "nobody", memberIn("")); !errors.Is(err, ErrPersonNotFound) {
			t.Errorf("an unknown person = %v, want ErrPersonNotFound", err)
		}
		// A refused change changes nothing.
		if roles, _ := e.store.PersonRoles(ctx, id); len(roles) != 0 {
			t.Errorf("a refused update left %d roles", len(roles))
		}
	})

	t.Run("admins change members only, and make no one an admin or owner", func(t *testing.T) {
		adminID, adminToken := e.invite(t, PersonInput{Email: "admin@example.com", OrgRole: OrgAdmin})
		e.accept(t, adminToken, "198.51.100.51")
		otherAdminID, otherToken := e.invite(t, PersonInput{Email: "admin2@example.com", OrgRole: OrgAdmin})
		e.accept(t, otherToken, "198.51.100.52")
		adminCtx := e.as(t, adminID)
		var na *NotAllowedError

		if _, err := e.svc.UpdatePerson(adminCtx, id, memberIn("", ProjectRole{Project: "shop", Role: RoleMaintain})); err != nil {
			t.Errorf("an admin changing a member's roles = %v, want allowed", err)
		}
		for _, role := range []OrgRole{OrgAdmin, OrgOwner} {
			if _, err := e.svc.UpdatePerson(adminCtx, id, PersonInput{OrgRole: role}); !errors.As(err, &na) || na.Org != "owner" {
				t.Errorf("an admin making a member an %s = %v, want a refusal that needs an owner", role, err)
			}
		}
		if _, err := e.svc.UpdatePerson(adminCtx, otherAdminID, memberIn("")); !errors.As(err, &na) || na.Org != "owner" {
			t.Errorf("an admin demoting an admin = %v, want a refusal that needs an owner", err)
		}
		if _, err := e.svc.UpdatePerson(adminCtx, e.owner, memberIn("")); !errors.As(err, &na) || na.Org != "owner" {
			t.Errorf("an admin demoting the owner = %v, want a refusal that needs an owner", err)
		}
		if _, err := e.svc.UpdatePerson(adminCtx, adminID, PersonInput{OrgRole: OrgOwner}); !errors.As(err, &na) {
			t.Errorf("an admin making themselves an owner = %v, want a refusal", err)
		}
		if rec, _ := e.store.GetPerson(ctx, e.owner); rec.OrgRole != "owner" {
			t.Error("the owner was demoted by an admin")
		}
		memberCtx := WithPrincipal(ctx, Principal{PersonID: "m", OrgRole: OrgMember})
		if _, err := e.svc.UpdatePerson(memberCtx, id, memberIn("")); !errors.As(err, &na) || na.Org != "admin" {
			t.Errorf("a member updating = %v, want a refusal that needs an owner or an admin", err)
		}
		if _, err := e.svc.UpdatePerson(ownerCtx, otherAdminID, memberIn("")); err != nil {
			t.Errorf("an owner demoting an admin = %v, want allowed", err)
		}
	})
}

func TestTheLastOwnerStays(t *testing.T) {
	ctx := context.Background()
	e := newPeopleEnv(t)
	ownerCtx := e.as(t, e.owner)

	if _, err := e.svc.UpdatePerson(ownerCtx, e.owner, PersonInput{OrgRole: OrgAdmin}); !errors.Is(err, ErrLastOwner) {
		t.Errorf("demoting the last owner = %v, want ErrLastOwner", err)
	}
	if _, err := e.svc.UpdatePerson(ownerCtx, e.owner, memberIn("")); !errors.Is(err, ErrLastOwner) {
		t.Errorf("making the last owner a member = %v, want ErrLastOwner", err)
	}
	if err := e.svc.RemovePerson(ownerCtx, e.owner); !errors.Is(err, ErrLastOwner) {
		t.Errorf("removing the last owner = %v, want ErrLastOwner", err)
	}
	if err := e.svc.RemovePerson(ctx, e.owner); !errors.Is(err, ErrLastOwner) {
		t.Errorf("the host removing the last owner = %v, want ErrLastOwner too", err)
	}
	// Changing the last owner without demoting them is fine (their end date, say, or nothing).
	if _, err := e.svc.UpdatePerson(ownerCtx, e.owner, PersonInput{OrgRole: OrgOwner}); err != nil {
		t.Errorf("keeping the last owner an owner = %v", err)
	}

	// With a second owner, either may go; then the other is the last.
	secondID, token := e.invite(t, PersonInput{Email: "second@example.com", OrgRole: OrgOwner})
	e.accept(t, token, addrA)
	if _, err := e.svc.UpdatePerson(ownerCtx, secondID, PersonInput{OrgRole: OrgAdmin}); err != nil {
		t.Errorf("demoting one of two owners = %v, want allowed", err)
	}
	if _, err := e.svc.UpdatePerson(ownerCtx, secondID, PersonInput{OrgRole: OrgOwner}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RemovePerson(e.as(t, secondID), e.owner); err != nil {
		t.Errorf("one owner removing the other = %v, want allowed", err)
	}
	if err := e.svc.RemovePerson(e.as(t, secondID), secondID); !errors.Is(err, ErrLastOwner) {
		t.Errorf("the remaining owner removing themselves = %v, want ErrLastOwner", err)
	}
	if n, _ := e.store.CountOwners(ctx); n != 1 {
		t.Errorf("%d owners, want 1", n)
	}
}

func TestOwnersDemotingEachOtherAtOnce(t *testing.T) {
	ctx := context.Background()
	e := newPeopleEnv(t)
	secondID, token := e.invite(t, PersonInput{Email: "second@example.com", OrgRole: OrgOwner})
	e.accept(t, token, addrA)
	a, b := e.as(t, e.owner), e.as(t, secondID)

	results := make(chan error, 2)
	go func() { _, err := e.svc.UpdatePerson(a, secondID, PersonInput{OrgRole: OrgMember}); results <- err }()
	go func() { _, err := e.svc.UpdatePerson(b, e.owner, PersonInput{OrgRole: OrgMember}); results <- err }()
	for range 2 {
		if err := <-results; err != nil && !errors.Is(err, ErrLastOwner) {
			t.Errorf("update = %v", err)
		}
	}
	if n, _ := e.store.CountOwners(ctx); n != 1 {
		t.Errorf("%d owners after two demotions at once, want exactly 1", n)
	}
}

func TestRemovePerson(t *testing.T) {
	ctx := context.Background()
	e := newPeopleEnv(t)
	id, token := e.invite(t, memberIn("ana@example.com", ProjectRole{Project: "shop", Role: RoleDeploy}))
	e.accept(t, token, addrA)
	session, err := e.auth.SignIn(ctx, "ana@example.com", "a long enough password", "", "198.51.100.60")
	if err != nil {
		t.Fatal(err)
	}
	api, err := e.auth.CreateAPIToken(ctx, id, "ci", nil)
	if err != nil {
		t.Fatal(err)
	}
	var na *NotAllowedError

	// An admin removes a member; an owner anyone.
	adminID, adminToken := e.invite(t, PersonInput{Email: "admin@example.com", OrgRole: OrgAdmin})
	e.accept(t, adminToken, "198.51.100.61")
	adminCtx := e.as(t, adminID)
	if err := e.svc.RemovePerson(adminCtx, e.owner); !errors.As(err, &na) || na.Org != "owner" {
		t.Errorf("an admin removing the owner = %v, want a refusal that needs an owner", err)
	}
	if err := e.svc.RemovePerson(WithPrincipal(ctx, Principal{PersonID: "m", OrgRole: OrgMember}), id); !errors.As(err, &na) || na.Org != "admin" {
		t.Errorf("a member removing = %v, want a refusal that needs an owner or an admin", err)
	}
	if _, err := e.auth.CheckSession(ctx, session.Value); err != nil {
		t.Fatalf("refused removals ended a session: %v", err)
	}

	if err := e.svc.RemovePerson(adminCtx, id); err != nil {
		t.Fatalf("an admin removing a member = %v, want allowed", err)
	}
	// Their next call is refused, whatever credential it comes with.
	if _, err := e.auth.CheckSession(ctx, session.Value); !errors.Is(err, ErrNotAuthenticated) {
		t.Errorf("the removed person's session = %v, want ErrNotAuthenticated", err)
	}
	if _, err := e.auth.CheckAPIToken(ctx, api.Token); !errors.Is(err, ErrNotAuthenticated) {
		t.Errorf("the removed person's token = %v, want ErrNotAuthenticated", err)
	}
	if _, err := e.auth.SignIn(ctx, "ana@example.com", "a long enough password", "", "198.51.100.62"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("the removed person signing in = %v, want ErrBadCredentials", err)
	}
	for _, table := range []string{"api_tokens", "project_roles"} { // their sessions are checked above, through CheckSession
		if n := countRows(t, e.store, table, id); n != 0 {
			t.Errorf("%s still has %d rows of the removed person", table, n)
		}
	}
	if err := e.svc.RemovePerson(adminCtx, id); !errors.Is(err, ErrPersonNotFound) {
		t.Errorf("removing twice = %v, want ErrPersonNotFound", err)
	}

	t.Run("an admin removing an admin, an owner removing one", func(t *testing.T) {
		otherID, otherToken := e.invite(t, PersonInput{Email: "admin2@example.com", OrgRole: OrgAdmin})
		e.accept(t, otherToken, "198.51.100.63")
		if err := e.svc.RemovePerson(adminCtx, otherID); !errors.As(err, &na) || na.Org != "owner" {
			t.Errorf("an admin removing an admin = %v, want a refusal that needs an owner", err)
		}
		if err := e.svc.RemovePerson(e.as(t, e.owner), otherID); err != nil {
			t.Errorf("an owner removing an admin = %v, want allowed", err)
		}
	})

	t.Run("the invited can be removed too", func(t *testing.T) {
		pendingID, _ := e.invite(t, memberIn("pending@example.com"))
		if err := e.svc.RemovePerson(adminCtx, pendingID); err != nil {
			t.Errorf("removing someone who never accepted = %v", err)
		}
	})
}

// countRows counts a person's rows in a table the store can list per person.
func countRows(t *testing.T, st *store.Store, table, personID string) int {
	t.Helper()
	switch table {
	case "api_tokens":
		toks, err := st.ListAPITokens(context.Background(), personID)
		if err != nil {
			t.Fatal(err)
		}
		return len(toks)
	case "project_roles":
		roles, err := st.PersonRoles(context.Background(), personID)
		if err != nil {
			t.Fatal(err)
		}
		return len(roles)
	}
	t.Fatalf("countRows: no way to list %s", table)
	return 0
}

func TestReadingPeople(t *testing.T) {
	ctx := context.Background()
	e := newPeopleEnv(t)
	id, _ := e.invite(t, memberIn("ana@example.com", ProjectRole{Project: "shop", Role: RoleView}))
	ownerCtx := e.as(t, e.owner)

	people, err := e.svc.People(ownerCtx)
	if err != nil || len(people) != 2 || people[0].Email != "you@example.com" || people[0].OrgRole != OrgOwner || people[1].State != "invited" ||
		len(people[1].Projects) != 1 {
		t.Errorf("People = %+v, %v", people, err)
	}
	p, err := e.svc.Person(ownerCtx, id)
	if err != nil || p.Email != "ana@example.com" || len(p.Projects) != 1 {
		t.Errorf("Person = %+v, %v", p, err)
	}
	if _, err := e.svc.Person(ownerCtx, "nobody"); !errors.Is(err, ErrPersonNotFound) {
		t.Errorf("Person(nobody) = %v, want ErrPersonNotFound", err)
	}
	// Admin only.
	member := WithPrincipal(ctx, Principal{PersonID: id, Email: "ana@example.com", OrgRole: OrgMember})
	if _, err := e.svc.People(member); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("People as a member = %v, want ErrNotAllowed", err)
	}
	if _, err := e.svc.Person(member, e.owner); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("Person as a member = %v, want ErrNotAllowed", err)
	}

	t.Run("me", func(t *testing.T) {
		me, err := e.svc.Me(WithPrincipal(ctx, Principal{PersonID: e.owner, Email: "you@example.com", OrgRole: OrgOwner, TokenName: "ci",
			Scope: &TokenScope{Project: "shop", App: "web"}}))
		if err != nil || me.Person.Email != "you@example.com" || me.Person.OrgRole != OrgOwner || me.TokenName != "ci" || me.Scope == nil || me.Scope.App != "web" {
			t.Errorf("Me = %+v, %v", me, err)
		}
		if me, err := e.svc.Me(member); err != nil || me.Person.ID != id || len(me.Person.Projects) != 1 {
			t.Errorf("Me as a member = %+v, %v; want their own roles", me, err)
		}
		if _, err := e.svc.Me(ctx); !errors.Is(err, ErrNotAuthenticated) {
			t.Errorf("Me with no principal = %v, want ErrNotAuthenticated", err)
		}
		if _, err := e.svc.Me(WithPrincipal(ctx, Principal{PersonID: "gone"})); !errors.Is(err, ErrNotAuthenticated) {
			t.Errorf("Me of a person who is gone = %v, want ErrNotAuthenticated", err)
		}
	})

	t.Run("no secret in what is read", func(t *testing.T) {
		got, err := e.svc.InvitePerson(ctx, memberIn("secret@example.com"))
		if err != nil {
			t.Fatal(err)
		}
		all, _ := e.svc.People(ctx)
		if text := strings.Join(personWords(all), "\n"); strings.Contains(text, got.Token) || strings.Contains(text, hashToken(got.Token)) || strings.Contains(text, "pbkdf2") {
			t.Errorf("a token or hash is in what People shows:\n%s", text)
		}
	})
}

// personWords is every string of the people, for a search for a secret.
func personWords(ps []Person) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.ID, p.Email, string(p.OrgRole), p.State, p.InvitedBy)
	}
	return out
}

func TestScopedTokenNeedsDeployRights(t *testing.T) {
	ctx := context.Background()
	e := newPeopleEnv(t)
	scope := &TokenScope{Project: "shop", App: "web"}

	mk := func(email string, org OrgRole, projects ...ProjectRole) string {
		t.Helper()
		id, token := e.invite(t, PersonInput{Email: email, OrgRole: org, Projects: projects})
		e.accept(t, token, "198.51.100."+string(rune('1'+len(email)%8)))
		return id
	}
	viewer := mk("viewer@example.com", OrgMember, ProjectRole{Project: "shop", Role: RoleView})
	outsider := mk("outsider@example.com", OrgMember, ProjectRole{Project: "default", Role: RoleMaintain})
	stagingDeployer := mk("stager@example.com", OrgMember, ProjectRole{Project: "shop", Role: RoleView, Environments: map[string]Role{"staging": RoleDeploy}})
	deployer := mk("deployer@example.com", OrgMember, ProjectRole{Project: "shop", Role: RoleDeploy})
	ended := mk("ended@example.com", OrgMember, ProjectRole{Project: "shop", Role: RoleDeploy})
	admin := mk("admin@example.com", OrgAdmin)
	norole := mk("none@example.com", OrgMember)

	refused := func(who, name string, have Level) {
		t.Helper()
		_, err := e.auth.CreateScopedAPIToken(ctx, who, name, nil, scope)
		var na *NotAllowedError
		if !errors.As(err, &na) || !errors.Is(err, ErrNotAllowed) || na.Have != have || na.Need != LevelDeploy || na.Project != "shop" {
			t.Errorf("token for %s = %v, want a refusal: have %s, need deploy in shop", name, err, have)
		}
	}
	refused(viewer, "viewer", LevelView)
	refused(outsider, "outsider", LevelNone)
	refused(norole, "norole", LevelNone)
	for who, name := range map[string]string{stagingDeployer: "stager", deployer: "deployer", admin: "admin", e.owner: "owner"} {
		if _, err := e.auth.CreateScopedAPIToken(ctx, who, name, nil, scope); err != nil {
			t.Errorf("token for %s = %v, want allowed", name, err)
		}
	}
	// A full token has no scope to judge: any active person may make one for themselves.
	if _, err := e.auth.CreateAPIToken(ctx, viewer, "full", nil); err != nil {
		t.Errorf("a full token for a viewer = %v, want allowed (it acts with their rights)", err)
	}
	// A role that has ended counts as none.
	past := e.clock.Now().Add(time.Hour)
	if _, err := e.svc.UpdatePerson(ctx, ended, memberIn("", ProjectRole{Project: "shop", Role: RoleDeploy, EndsAt: &past})); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(2 * time.Hour)
	refused(ended, "ended", LevelNone)
}
