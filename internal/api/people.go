package api

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
)

// People and roles (feature 053, specs/053-people-and-roles/contracts/api.md). Who the caller is
// (getMe), the people (list, invite, read, change, remove, a new invite link) and accepting an
// invite, which is public: the token in the body is the credential, and a wrong one counts against
// the address like a wrong password. No answer holds a password hash or a stored token hash: the
// invite token is in the answer that makes it, once.

// The codes of people and roles.
const (
	codePersonNotFound = "person_not_found"
	codeEmailTaken     = "email_taken"
	codeInviteInvalid  = "invite_invalid"
	codeNotInvited     = "not_invited"
	codeLastOwner      = "last_owner"
)

// accessError maps a refusal of the person's role to 403 not_allowed, with the field that needs
// more (a body deploy that changes more than images). It returns nil for any other error.
func accessError(err error) *Error {
	var na *service.NotAllowedError
	switch {
	case errors.As(err, &na):
		return &Error{Status: http.StatusForbidden, Code: codeNotAllowed, Field: na.Field, Message: na.Error()}
	case errors.Is(err, service.ErrNotAllowed):
		return &Error{Status: http.StatusForbidden, Code: codeNotAllowed, Message: err.Error()}
	}
	return nil
}

// peopleError maps the errors of the people calls. It returns nil for any other error.
func peopleError(err error) *Error {
	var limited *service.SigninLimitError
	switch {
	case errors.Is(err, service.ErrPersonNotFound):
		return &Error{Status: http.StatusNotFound, Code: codePersonNotFound, Message: err.Error()}
	case errors.Is(err, service.ErrEmailTaken):
		return &Error{Status: http.StatusConflict, Code: codeEmailTaken, Field: "email", Message: err.Error()}
	case errors.Is(err, service.ErrInviteInvalid):
		return &Error{Status: http.StatusNotFound, Code: codeInviteInvalid, Message: err.Error()}
	case errors.Is(err, service.ErrNotInvited):
		return &Error{Status: http.StatusConflict, Code: codeNotInvited, Message: err.Error()}
	case errors.Is(err, service.ErrLastOwner):
		return &Error{Status: http.StatusConflict, Code: codeLastOwner, Message: err.Error()}
	case errors.As(err, &limited):
		minutes := max(1, int(math.Ceil(limited.RetryAfter.Minutes())))
		return &Error{Status: http.StatusTooManyRequests, Code: codeTooManyAttempts,
			Message: fmt.Sprintf("Too many failed tries from this address. Try again in %d minutes.", minutes)}
	}
	return nil
}

// projectRoleJSON is schema ProjectRole.
type projectRoleJSON struct {
	Project      string            `json:"project"`
	Role         string            `json:"role"`
	EndsAt       *time.Time        `json:"endsAt,omitempty"`
	Environments map[string]string `json:"environments,omitempty"`
}

func toProjectRolesJSON(roles []service.ProjectRole) []projectRoleJSON {
	out := make([]projectRoleJSON, 0, len(roles))
	for _, r := range roles {
		pr := projectRoleJSON{Project: r.Project, Role: string(r.Role), EndsAt: r.EndsAt}
		if len(r.Environments) > 0 {
			pr.Environments = make(map[string]string, len(r.Environments))
			for env, role := range r.Environments {
				pr.Environments[env] = string(role)
			}
		}
		out = append(out, pr)
	}
	return out
}

func fromProjectRolesJSON(in []projectRoleJSON) []service.ProjectRole {
	var out []service.ProjectRole
	for _, r := range in {
		pr := service.ProjectRole{Project: r.Project, Role: service.Role(r.Role), EndsAt: r.EndsAt}
		if len(r.Environments) > 0 {
			pr.Environments = make(map[string]service.Role, len(r.Environments))
			for env, role := range r.Environments {
				pr.Environments[env] = service.Role(role)
			}
		}
		out = append(out, pr)
	}
	return out
}

// personJSON is schema Person.
type personJSON struct {
	ID              string            `json:"id"`
	Email           string            `json:"email"`
	OrgRole         string            `json:"orgRole"`
	State           string            `json:"state"`
	EndsAt          *time.Time        `json:"endsAt,omitempty"`
	InvitedBy       string            `json:"invitedBy,omitempty"`
	CreatedAt       time.Time         `json:"createdAt"`
	InviteExpiresAt *time.Time        `json:"inviteExpiresAt,omitempty"`
	Projects        []projectRoleJSON `json:"projects"`
}

func toPersonJSON(p service.Person) personJSON {
	return personJSON{ID: p.ID, Email: p.Email, OrgRole: string(p.OrgRole), State: p.State, EndsAt: p.EndsAt,
		InvitedBy: p.InvitedBy, CreatedAt: p.CreatedAt, InviteExpiresAt: p.InviteExpiresAt, Projects: toProjectRolesJSON(p.Projects)}
}

// meJSON is schema Me: the person, and for a call made with a token the token's name and scope.
type meJSON struct {
	personJSON
	Token *meTokenJSON `json:"token,omitempty"`
}

type meTokenJSON struct {
	Name  string          `json:"name"`
	Scope *tokenScopeJSON `json:"scope,omitempty"`
}

// personInputJSON is schema PersonInput. Email is read only when inviting.
type personInputJSON struct {
	Email    string            `json:"email"`
	OrgRole  string            `json:"orgRole"`
	EndsAt   *time.Time        `json:"endsAt"`
	Projects []projectRoleJSON `json:"projects"`
}

func (in personInputJSON) input() service.PersonInput {
	return service.PersonInput{Email: in.Email, OrgRole: service.OrgRole(in.OrgRole), EndsAt: in.EndsAt, Projects: fromProjectRolesJSON(in.Projects)}
}

// invitedJSON is schema Invited: the token is in this answer only.
type invitedJSON struct {
	Person      personJSON `json:"person"`
	InviteToken string     `json:"inviteToken"`
	InviteURL   string     `json:"inviteUrl"`
}

// consoleBase is the address the console is at, for a link in an answer: Caravana's public URL when
// it has one, else the scheme and host the request came with.
func (s *server) consoleBase(r *http.Request) string {
	if u := s.svc.PublicURL(); u != "" {
		return u
	}
	scheme := "http"
	if s.proxies.HTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *server) toInvitedJSON(r *http.Request, in service.Invited) invitedJSON {
	return invitedJSON{Person: toPersonJSON(in.Person), InviteToken: in.Token, InviteURL: service.InviteURL(s.consoleBase(r), in.Token)}
}

// getMe answers GET /v1/me: who the credential is, with the roles it acts with.
func (s *server) getMe(w http.ResponseWriter, r *http.Request) error {
	me, err := s.svc.Me(r.Context())
	if err != nil {
		return err
	}
	out := meJSON{personJSON: toPersonJSON(me.Person)}
	out.InviteExpiresAt = nil
	if me.TokenName != "" {
		out.Token = &meTokenJSON{Name: me.TokenName}
		if me.Scope != nil {
			out.Token.Scope = &tokenScopeJSON{Project: me.Scope.Project, App: me.Scope.App}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
	return nil
}

// listPeople answers GET /v1/people.
func (s *server) listPeople(w http.ResponseWriter, r *http.Request) error {
	people, err := s.svc.People(r.Context())
	if err != nil {
		return err
	}
	out := struct {
		People []personJSON `json:"people"`
	}{People: make([]personJSON, 0, len(people))}
	for _, p := range people {
		out.People = append(out.People, toPersonJSON(p))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// invitePerson answers POST /v1/people: 201 with the person and the invite's token and link, which
// are in this answer only (no-store).
func (s *server) invitePerson(w http.ResponseWriter, r *http.Request) error {
	var in personInputJSON
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	invited, err := s.svc.InvitePerson(r.Context(), in.input())
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Location", "/v1/people/"+invited.Person.ID)
	writeJSON(w, http.StatusCreated, s.toInvitedJSON(r, invited))
	return nil
}

// getPerson answers GET /v1/people/{personId}.
func (s *server) getPerson(w http.ResponseWriter, r *http.Request) error {
	p, err := s.svc.Person(r.Context(), r.PathValue("personId"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toPersonJSON(p))
	return nil
}

// updatePerson answers PUT /v1/people/{personId}: the body replaces the person's organization role,
// end date and roles.
func (s *server) updatePerson(w http.ResponseWriter, r *http.Request) error {
	var in personInputJSON
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	p, err := s.svc.UpdatePerson(r.Context(), r.PathValue("personId"), in.input())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toPersonJSON(p))
	return nil
}

// removePerson answers DELETE /v1/people/{personId}: 204. Their sessions, tokens and passkeys end
// with the request.
func (s *server) removePerson(w http.ResponseWriter, r *http.Request) error {
	if err := s.svc.RemovePerson(r.Context(), r.PathValue("personId")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// reinvitePerson answers POST /v1/people/{personId}/invite: a new link for a person who hasn't
// accepted; the old one stops working. no-store, like invitePerson.
func (s *server) reinvitePerson(w http.ResponseWriter, r *http.Request) error {
	invited, err := s.svc.ReinvitePerson(r.Context(), r.PathValue("personId"))
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, s.toInvitedJSON(r, invited))
	return nil
}

// acceptInvite answers POST /v1/invites/accept: the person sets a password and becomes active. It
// is public (no credentials: the token is the credential); a token that is unknown, expired or used
// is 404 invite_invalid and counts against the address like a failed sign-in, and a locked address
// is 429. It starts no session: the console's page does that, an API client uses a token.
func (s *server) acceptInvite(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Cache-Control", "no-store")
	var in struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	p, err := s.svc.AcceptInvite(r.Context(), in.Token, in.Password, s.proxies.ClientAddress(r))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, struct {
		Email string `json:"email"`
	}{p.Email})
	return nil
}
