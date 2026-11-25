package service

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// People (feature 053, D8): inviting, accepting an invite, changing roles and removing. The
// rights are the plan's: an owner manages anyone; an admin manages members only and can't make
// anyone an owner or an admin; the last owner can't be removed or demoted. Caravana itself (a
// context with no principal: the host CLI) is allowed everything.

// What people management refuses with. Callers match them with errors.Is.
var (
	// ErrInviteInvalid is an invite token that is unknown, expired or already used. It never says
	// which, and a try counts against the address like a failed sign-in.
	ErrInviteInvalid = errors.New("this invite link is not valid: ask for a new one")
	// ErrLastOwner is removing or demoting the only owner.
	ErrLastOwner = errors.New("this is the last owner: make another person an owner first")
	// ErrEmailTaken is inviting an email someone already has.
	ErrEmailTaken = errors.New("someone with this email is already here")
	// ErrNotInvited is a new invite link for a person who already accepted theirs.
	ErrNotInvited = errors.New("this person has accepted their invite already")
	// ErrPersonNotFound is an ID that names no person.
	ErrPersonNotFound = errors.New("person not found")
)

const (
	// inviteTokenPrefix marks an invite token, so a leaked one is recognized (webhook tokens use
	// cvwh_, API tokens cvat_).
	inviteTokenPrefix = "cvin_"
	inviteTokenBytes  = 32                 // 256 bits of randomness
	inviteLife        = 7 * 24 * time.Hour // how long an invite link works
)

// SetAuth gives the service the Auth that holds the sign-in limit and the password hashing, which
// accepting an invite uses. cmd/caravana calls it once at start; a service without one can't
// accept invites.
func (s *Service) SetAuth(a *Auth) { s.auth = a }

// Person is a person as the people pages and the API show them: never a hash.
type Person struct {
	ID    string
	Email string
	// OrgRole is owner, admin or member; State is "invited" or "active".
	OrgRole OrgRole
	State   string
	// EndsAt is when the person's access ends: nil for no end (a guest has one).
	EndsAt *time.Time
	// InviteExpiresAt is when the invite link stops working, while State is "invited".
	InviteExpiresAt *time.Time
	InvitedBy       string // the inviter's email
	CreatedAt       time.Time
	// Projects are a member's roles, by project with their environment overrides. Owners and
	// admins have none.
	Projects []ProjectRole
}

// PersonInput is what an invite or a change says about a person. Email is read only by an invite
// (a person's email never changes).
type PersonInput struct {
	Email   string
	OrgRole OrgRole
	EndsAt  *time.Time
	// Projects are a member's roles; an owner or an admin gets none (a field error on "projects").
	Projects []ProjectRole
}

// Invited is a new invite: the person as stored, and the token, which exists only here.
type Invited struct {
	Person Person
	Token  string
}

// InviteURL is the link to give a person: <base>/invite?token=<token>. base is the console's
// address (CARAVANA_PUBLIC_URL, else where the request came from); with none the link is a path.
func InviteURL(base, token string) string {
	return strings.TrimRight(base, "/") + "/invite?token=" + url.QueryEscape(token)
}

// newInviteToken returns a fresh token: the prefix, then 32 random bytes in lower-case base32.
func newInviteToken() (string, error) {
	b := make([]byte, inviteTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate invite token: %w", err)
	}
	return inviteTokenPrefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)), nil
}

func validOrgRole(r OrgRole) bool { return r == OrgOwner || r == OrgAdmin || r == OrgMember }

// mayManage says the caller may make, change or remove a person who is (or is to become) `role`:
// an owner anyone, an admin members only. A caller with no principal is Caravana itself.
func mayManage(ctx context.Context, role OrgRole) error {
	p, ok := PrincipalFrom(ctx)
	if !ok || p.Owner() {
		return nil
	}
	if !p.Admin() {
		return &NotAllowedError{Org: "admin"}
	}
	if role != OrgMember {
		return &NotAllowedError{Org: "owner"}
	}
	return nil
}

// personOf is a stored person with their roles, as the pages show them.
func (s *Service) personOf(ctx context.Context, rec store.PersonRecord) (Person, error) {
	out := Person{ID: rec.ID, Email: rec.Email, OrgRole: OrgRole(rec.OrgRole), State: rec.State, EndsAt: rec.EndsAt,
		InviteExpiresAt: rec.InviteExpiresAt, InvitedBy: rec.InvitedBy, CreatedAt: rec.CreatedAt}
	if out.OrgRole == OrgMember {
		roles, err := s.store.PersonRoles(ctx, rec.ID)
		if err != nil {
			return Person{}, fmt.Errorf("person %q: %w", rec.ID, err)
		}
		out.Projects = projectRolesOf(roles)
	}
	return out, nil
}

// projectRolesOf is stored roles as ProjectRole values.
func projectRolesOf(recs []store.ProjectRoleRecord) []ProjectRole {
	var out []ProjectRole
	for _, r := range recs {
		pr := ProjectRole{Project: r.Project, Role: Role(r.Role), EndsAt: r.EndsAt}
		if len(r.Environments) > 0 {
			pr.Environments = make(map[string]Role, len(r.Environments))
			for env, role := range r.Environments {
				pr.Environments[env] = Role(role)
			}
		}
		out = append(out, pr)
	}
	return out
}

// checkPerson validates what an invite or a change says and returns the roles to store. Field
// errors name the field (orgRole, endsAt, projects, projects[i].role, projects[i].endsAt); a
// project or environment that doesn't exist is a *PlaceError on projects[i].project or
// projects[i].environments.<env>.
func (s *Service) checkPerson(ctx context.Context, in PersonInput) ([]store.ProjectRoleRecord, error) {
	if !validOrgRole(in.OrgRole) {
		return nil, app.NewFieldError("orgRole", "must be owner, admin or member")
	}
	now := s.clock.Now()
	if in.EndsAt != nil && !in.EndsAt.After(now) {
		return nil, app.NewFieldError("endsAt", "must be in the future")
	}
	if in.OrgRole != OrgMember && len(in.Projects) > 0 {
		return nil, app.NewFieldError("projects", "owners and admins have every role in every project: give roles to members only")
	}
	var out []store.ProjectRoleRecord
	seen := map[string]bool{}
	for i, pr := range in.Projects {
		field := fmt.Sprintf("projects[%d]", i)
		if pr.Role != RoleView && pr.Role != RoleDeploy && pr.Role != RoleMaintain {
			return nil, app.NewFieldError(field+".role", "must be view, deploy or maintain")
		}
		if seen[pr.Project] {
			return nil, app.NewFieldError(field+".project", "%q is given twice", pr.Project)
		}
		seen[pr.Project] = true
		if pr.EndsAt != nil && !pr.EndsAt.After(now) {
			return nil, app.NewFieldError(field+".endsAt", "must be in the future")
		}
		if err := s.requireProject(ctx, pr.Project); err != nil {
			return nil, &PlaceError{Field: field + ".project", Err: err}
		}
		rec := store.ProjectRoleRecord{Project: pr.Project, Role: string(pr.Role), EndsAt: pr.EndsAt}
		envs := make([]string, 0, len(pr.Environments))
		for env := range pr.Environments {
			envs = append(envs, env)
		}
		slices.Sort(envs) // the same first refusal every time
		for _, env := range envs {
			role := pr.Environments[env]
			efield := field + ".environments." + env
			if role != RoleNone && role != RoleView && role != RoleDeploy && role != RoleMaintain {
				return nil, app.NewFieldError(efield, "must be none, view, deploy or maintain")
			}
			if err := s.requireEnvironment(ctx, pr.Project, env); err != nil {
				return nil, &PlaceError{Field: efield, Err: err}
			}
			if rec.Environments == nil {
				rec.Environments = map[string]string{}
			}
			rec.Environments[env] = string(role)
		}
		out = append(out, rec)
	}
	return out, nil
}

// InvitePerson makes an invited person with their roles and a one-time token, valid 7 days, that
// the caller hands over as a link (InviteURL). Owners invite anyone; admins invite members.
// Errors: ErrEmailTaken, the field errors of checkPerson, a *NotAllowedError.
func (s *Service) InvitePerson(ctx context.Context, in PersonInput) (Invited, error) {
	if err := s.Allow(ctx, NeedAdmin(), Target{}); err != nil {
		return Invited{}, err
	}
	email, err := normalizeEmail(in.Email)
	if err != nil {
		return Invited{}, err
	}
	if validOrgRole(in.OrgRole) {
		if err := mayManage(ctx, in.OrgRole); err != nil {
			return Invited{}, err
		}
	}
	s.peopleMu.Lock()
	defer s.peopleMu.Unlock()
	roles, err := s.checkPerson(ctx, in)
	if err != nil {
		return Invited{}, err
	}
	token, err := newInviteToken()
	if err != nil {
		return Invited{}, err
	}
	now := s.clock.Now().UTC()
	expires := now.Add(inviteLife)
	rec := store.PersonRecord{ID: newPersonID(), Email: email, OrgRole: string(in.OrgRole), State: personInvited,
		PasswordSignIn: true, InviteHash: hashToken(token), InviteExpiresAt: &expires, InvitedBy: callerEmail(ctx),
		EndsAt: utcPtr(in.EndsAt), CreatedAt: now}
	if err := s.store.CreatePerson(ctx, rec, roles); errors.Is(err, store.ErrAlreadyExists) {
		return Invited{}, fmt.Errorf("%w: %s", ErrEmailTaken, email)
	} else if err != nil {
		return Invited{}, fmt.Errorf("invite %s: %w", email, err)
	}
	person, err := s.personOf(ctx, rec)
	if err != nil {
		return Invited{}, err
	}
	// The entry says who was invited and with what role; the invite token never appears.
	s.audit(ctx, audit.Entry{Action: audit.ActionInvited, Resource: personResource(rec.ID, email),
		Changes: audit.Diff(nil, personAuditView(person))})
	s.log.Info("person invited", "person", rec.ID, "role", rec.OrgRole, "by", rec.InvitedBy)
	return Invited{Person: person, Token: token}, nil
}

// ReinvitePerson gives an invited person a new link; the old one stops working. A person who
// accepted already is ErrNotInvited, an unknown one ErrPersonNotFound.
func (s *Service) ReinvitePerson(ctx context.Context, id string) (Invited, error) {
	if err := s.Allow(ctx, NeedAdmin(), Target{}); err != nil {
		return Invited{}, err
	}
	s.peopleMu.Lock()
	defer s.peopleMu.Unlock()
	rec, err := s.store.GetPerson(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return Invited{}, ErrPersonNotFound
	} else if err != nil {
		return Invited{}, fmt.Errorf("reinvite: %w", err)
	}
	if err := mayManage(ctx, OrgRole(rec.OrgRole)); err != nil {
		return Invited{}, err
	}
	if rec.State != personInvited {
		return Invited{}, fmt.Errorf("%w: %s", ErrNotInvited, rec.Email)
	}
	token, err := newInviteToken()
	if err != nil {
		return Invited{}, err
	}
	expires := s.clock.Now().UTC().Add(inviteLife)
	if err := s.store.SetInvite(ctx, id, hashToken(token), expires); err != nil {
		return Invited{}, fmt.Errorf("reinvite %s: %w", rec.Email, err)
	}
	rec.InviteExpiresAt = &expires
	person, err := s.personOf(ctx, rec)
	if err != nil {
		return Invited{}, err
	}
	s.audit(ctx, audit.Entry{Action: audit.ActionInvited, Resource: personResource(rec.ID, rec.Email),
		Changes: []audit.Change{{Path: "inviteExpiresAt", Op: audit.OpChanged, To: expires.Format(time.RFC3339)}}})
	return Invited{Person: person, Token: token}, nil
}

// AcceptInvite sets the invited person's password, makes them active and voids the token. A token
// that is unknown, expired or used is ErrInviteInvalid and counts against the address's sign-in
// limit like a wrong password does; a locked address is refused before the token is looked at (a
// *SigninLimitError). A password under 12 characters is an *app.FieldError and costs nothing.
// The caller (the console) then starts a session for the person.
func (s *Service) AcceptInvite(ctx context.Context, token, password, address string) (Person, error) {
	if s.auth == nil {
		return Person{}, errors.New("accept invite: the service has no Auth (SetAuth)")
	}
	a := s.auth
	at, err := a.beginAttempt(ctx, address)
	if err != nil {
		var limited *SigninLimitError
		if errors.As(err, &limited) {
			a.event("signin_limited", "address", address)
		}
		return Person{}, err
	}
	defer at.release() // a no-op once the failure is recorded

	invalid := func() (Person, error) { return Person{}, s.inviteFailed(ctx, at, address) }
	rec, hash, ok, err := s.liveInvite(ctx, token)
	if err != nil {
		return Person{}, err
	}
	if !ok {
		return invalid()
	}
	now := s.clock.Now().UTC()
	if err := checkPasswordRule("password", password); err != nil {
		return Person{}, err
	}
	pw, err := a.hash(password)
	if err != nil {
		return Person{}, err
	}
	// The store accepts only while the invite is still this token: of two accepts at once, one wins.
	if err := s.store.AcceptInvite(ctx, rec.ID, hash, pw, now); errors.Is(err, store.ErrNotFound) {
		return invalid()
	} else if err != nil {
		return Person{}, fmt.Errorf("accept invite: %w", err)
	}
	a.event("invite_accepted", "person", rec.ID)
	// The person who accepted is the actor: nobody is signed in yet.
	s.audit(ctx, audit.Entry{Action: audit.ActionAcceptedInvite, Resource: personResource(rec.ID, rec.Email), Address: address,
		Actor: audit.Actor{Kind: audit.ActorPerson, Email: rec.Email, PersonID: rec.ID}})
	done, err := s.store.GetPerson(ctx, rec.ID)
	if err != nil {
		return Person{}, fmt.Errorf("accept invite: %w", err)
	}
	return s.personOf(ctx, done)
}

// inviteFailed counts a wrong invite token against the address and says what the caller answers:
// ErrInviteInvalid, or the *SigninLimitError the fifth failure makes.
func (s *Service) inviteFailed(ctx context.Context, at *attempt, address string) error {
	s.auth.event("invite_failed", "address", address)
	// A wrong invite token is a failed sign-in of nobody: no email, and the typed token is never kept.
	s.audit(ctx, audit.Entry{Action: audit.ActionSignInFailed, Resource: audit.Resource{Kind: audit.KindAccount},
		Address: address, Actor: audit.Actor{Kind: audit.ActorAnonymous}})
	if _, err := s.auth.recordFailure(ctx, at); err != nil {
		return err
	}
	return ErrInviteInvalid
}

// liveInvite finds the person an invite token is for. ok is false for a token that is unknown,
// expired or already used, or whose person's end date has passed.
func (s *Service) liveInvite(ctx context.Context, token string) (rec store.PersonRecord, hash string, ok bool, err error) {
	if !strings.HasPrefix(token, inviteTokenPrefix) {
		return rec, "", false, nil
	}
	hash = hashToken(token)
	rec, err = s.store.GetPersonByInvite(ctx, hash)
	if errors.Is(err, store.ErrNotFound) {
		return rec, hash, false, nil
	} else if err != nil {
		return rec, hash, false, fmt.Errorf("find invite: %w", err)
	}
	now := s.clock.Now().UTC()
	if rec.State != personInvited || rec.InviteExpiresAt == nil || !now.Before(*rec.InviteExpiresAt) ||
		(rec.EndsAt != nil && !now.Before(*rec.EndsAt)) {
		return rec, hash, false, nil
	}
	return rec, hash, true, nil
}

// InviteFor is who an invite link is for, so the page that takes the password can name them. It is
// as careful as AcceptInvite: a token that is not valid counts against the address's sign-in limit
// (a guess is a guess), and a locked address is refused before the token is looked at.
func (s *Service) InviteFor(ctx context.Context, token, address string) (Person, error) {
	if s.auth == nil {
		return Person{}, errors.New("read invite: the service has no Auth (SetAuth)")
	}
	at, err := s.auth.beginAttempt(ctx, address)
	if err != nil {
		var limited *SigninLimitError
		if errors.As(err, &limited) {
			s.auth.event("signin_limited", "address", address)
		}
		return Person{}, err
	}
	defer at.release()
	rec, _, ok, err := s.liveInvite(ctx, token)
	if err != nil {
		return Person{}, err
	}
	if !ok {
		return Person{}, s.inviteFailed(ctx, at, address)
	}
	return s.personOf(ctx, rec)
}

// UpdatePerson replaces a person's organization role, end date and roles. An admin changes members
// only and can't make anyone an owner or an admin; the last owner can't be demoted (ErrLastOwner).
// The person's next request sees the change.
func (s *Service) UpdatePerson(ctx context.Context, id string, in PersonInput) (Person, error) {
	if err := s.Allow(ctx, NeedAdmin(), Target{}); err != nil {
		return Person{}, err
	}
	s.peopleMu.Lock()
	defer s.peopleMu.Unlock()
	rec, err := s.store.GetPerson(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return Person{}, ErrPersonNotFound
	} else if err != nil {
		return Person{}, fmt.Errorf("update person: %w", err)
	}
	// Both what they are and what they become must be within the caller's rights.
	if err := mayManage(ctx, OrgRole(rec.OrgRole)); err != nil {
		return Person{}, err
	}
	if validOrgRole(in.OrgRole) {
		if err := mayManage(ctx, in.OrgRole); err != nil {
			return Person{}, err
		}
	}
	roles, err := s.checkPerson(ctx, in)
	if err != nil {
		return Person{}, err
	}
	was, _ := s.personOf(ctx, rec)
	if rec.OrgRole == string(OrgOwner) && in.OrgRole != OrgOwner {
		if err := s.keepAnOwner(ctx); err != nil {
			return Person{}, err
		}
	}
	if err := s.store.ReplacePerson(ctx, id, string(in.OrgRole), utcPtr(in.EndsAt), roles); errors.Is(err, store.ErrNotFound) {
		return Person{}, ErrPersonNotFound
	} else if err != nil {
		return Person{}, fmt.Errorf("update person %s: %w", rec.Email, err)
	}
	done, err := s.store.GetPerson(ctx, id)
	if err != nil {
		return Person{}, fmt.Errorf("update person: %w", err)
	}
	updated, err := s.personOf(ctx, done)
	if err != nil {
		return Person{}, err
	}
	if changes := audit.Diff(personAuditView(was), personAuditView(updated)); len(changes) > 0 {
		s.audit(ctx, audit.Entry{Action: audit.ActionUpdated, Resource: personResource(id, rec.Email), Changes: changes})
	} else {
		s.noChange(ctx)
	}
	return updated, nil
}

// RemovePerson deletes a person with their sessions, API tokens, passkeys and roles, in one
// transaction: their next call is refused. The records they made keep their email. Rights are
// UpdatePerson's; the last owner can't be removed (ErrLastOwner).
func (s *Service) RemovePerson(ctx context.Context, id string) error {
	if err := s.Allow(ctx, NeedAdmin(), Target{}); err != nil {
		return err
	}
	s.peopleMu.Lock()
	defer s.peopleMu.Unlock()
	rec, err := s.store.GetPerson(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return ErrPersonNotFound
	} else if err != nil {
		return fmt.Errorf("remove person: %w", err)
	}
	if err := mayManage(ctx, OrgRole(rec.OrgRole)); err != nil {
		return err
	}
	if rec.OrgRole == string(OrgOwner) {
		if err := s.keepAnOwner(ctx); err != nil {
			return err
		}
	}
	if err := s.store.DeletePerson(ctx, id); errors.Is(err, store.ErrNotFound) {
		return ErrPersonNotFound
	} else if err != nil {
		return fmt.Errorf("remove person %s: %w", rec.Email, err)
	}
	s.audit(ctx, audit.Entry{Action: audit.ActionRemoved, Resource: personResource(id, rec.Email)})
	s.log.Info("person removed", "person", id, "by", callerEmail(ctx))
	return nil
}

// keepAnOwner is ErrLastOwner when there is only one owner left to lose. The caller holds
// peopleMu, so two owners can't each demote the other.
func (s *Service) keepAnOwner(ctx context.Context) error {
	n, err := s.store.CountOwners(ctx)
	if err != nil {
		return err
	}
	if n <= 1 {
		return ErrLastOwner
	}
	return nil
}

// People lists everyone, the oldest first. It is for owners and admins.
func (s *Service) People(ctx context.Context) ([]Person, error) {
	if err := s.Allow(ctx, NeedAdmin(), Target{}); err != nil {
		return nil, err
	}
	recs, err := s.store.ListPeople(ctx)
	if err != nil {
		return nil, fmt.Errorf("list people: %w", err)
	}
	out := make([]Person, 0, len(recs))
	for _, rec := range recs {
		p, err := s.personOf(ctx, rec)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// Person returns one person, or ErrPersonNotFound. It is for owners and admins.
func (s *Service) Person(ctx context.Context, id string) (Person, error) {
	if err := s.Allow(ctx, NeedAdmin(), Target{}); err != nil {
		return Person{}, err
	}
	rec, err := s.store.GetPerson(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return Person{}, ErrPersonNotFound
	} else if err != nil {
		return Person{}, fmt.Errorf("get person: %w", err)
	}
	return s.personOf(ctx, rec)
}

// MeView is who a credential is: the person, and for an API token its name and the scope that
// limits it further.
type MeView struct {
	Person    Person
	TokenName string      // "" for a session
	Scope     *TokenScope // set for a deploy token
}

// Me says who the request acts as. A context without a principal (Caravana itself) is
// ErrNotAuthenticated: there is no person to describe.
func (s *Service) Me(ctx context.Context) (MeView, error) {
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return MeView{}, ErrNotAuthenticated
	}
	rec, err := s.store.GetPerson(ctx, p.PersonID)
	if errors.Is(err, store.ErrNotFound) {
		return MeView{}, ErrNotAuthenticated
	} else if err != nil {
		return MeView{}, fmt.Errorf("me: %w", err)
	}
	person, err := s.personOf(ctx, rec)
	if err != nil {
		return MeView{}, err
	}
	return MeView{Person: person, TokenName: p.TokenName, Scope: p.Scope}, nil
}

// callerEmail is the email of the person behind the request, "" for Caravana itself.
func callerEmail(ctx context.Context) string {
	p, _ := PrincipalFrom(ctx)
	return p.Email
}

// utcPtr is t in UTC, nil for nil.
func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	return new(t.UTC())
}
