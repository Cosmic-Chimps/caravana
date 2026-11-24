package ui

import (
	"errors"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/httpx"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// People (feature 053, D8 and D10): Settings › People lists everyone and invites, a person's page
// changes their roles, asks for a new invite link and removes them; /invite is where a person who was
// invited takes their password. The pages are admin routes (access.go); the service holds an admin to
// members and an owner to anyone, so a refusal of what the page offered is drawn as one sentence.
// A link and the token in it are drawn once, in the answer that made them (no-store), and no page
// holds a password, a hash or a stored token.

// The words a role has on the pages.
var (
	orgRoleWords = map[service.OrgRole]string{service.OrgOwner: "Owner", service.OrgAdmin: "Admin", service.OrgMember: "Member"}
	roleWords    = map[service.Role]string{service.RoleNone: "no access", service.RoleView: "view", service.RoleDeploy: "deploy", service.RoleMaintain: "maintain"}
)

// choice is one option of a select.
type choice struct{ Value, Label string }

// roleChoices are what a project's role may be; the empty value gives the person no role in it.
var roleChoices = []choice{{"", "No access"}, {"view", "View"}, {"deploy", "Deploy"}, {"maintain", "Maintain"}}

// overrideChoices are what an environment may say about a person's role in its project.
var overrideChoices = []choice{{"", "As the project"}, {"none", "No access"}, {"view", "View"}, {"deploy", "Deploy"}, {"maintain", "Maintain"}}

// personRow is a person as the list and their page say it.
type personRow struct {
	ID, Email string
	Role      string // owner, admin or member
	RoleWord  string // Owner, Admin, Member
	State     string // invited or active
	StateText string
	Self      bool
	Ends      when // the person's own end date; Text is "" for none
	EndsText  string
	Projects  []string // one line per project role
	Href      string
}

// envForm is one environment's override in the person form.
type envForm struct{ Name, Override string }

// projectForm is one project's block of the person form: the role, its end date and an override per
// environment (previews take the project's role, so they have none).
type projectForm struct {
	Project, Display string
	Role, EndsAt     string
	Envs             []envForm
}

// personForm is the form of an invite and of a change, with what was typed.
type personForm struct {
	Action      string
	Email       string
	EmailLocked bool // changing a person: the email never changes
	OrgRole     string
	EndsAt      string
	Projects    []projectForm
	Submit      string
}

// invitedView is the link just made, drawn once.
type invitedView struct {
	Email string
	URL   string
	Until when
}

// peopleData is Settings › People. View says which of its four pages: list, new, person or remove.
type peopleData struct {
	View    string
	Rows    []personRow
	Row     *personRow
	Form    *personForm
	Invited *invitedView
	Notice  *notice
	Errors  map[string]string // a sentence per field, by the field's name
	// Owner says the viewer is an owner: only an owner picks Owner or Admin.
	Owner bool
	// ReadOnly says the viewer may not change this person (an admin and an owner or another admin).
	ReadOnly bool
	// OrgChoices are the organization roles the viewer may give.
	OrgChoices []choice
	// RoleChoices and OverrideChoices fill the project blocks' selects.
	RoleChoices, OverrideChoices []choice
}

// Err is the sentence about a field, "" for none.
func (d peopleData) Err(field string) string { return d.Errors[field] }

// peopleSection is what Settings › People draws: the rows are the people the service lists.
func (s *server) peopleSection(r *http.Request, base peopleData) (peopleData, error) {
	who, _ := service.PrincipalFrom(r.Context())
	base.Owner = who.Owner()
	base.OrgChoices = []choice{{string(service.OrgMember), "Member"}}
	if who.Owner() {
		base.OrgChoices = []choice{{string(service.OrgMember), "Member"}, {string(service.OrgAdmin), "Admin"}, {string(service.OrgOwner), "Owner"}}
	}
	base.RoleChoices, base.OverrideChoices = roleChoices, overrideChoices
	if base.View == "list" {
		people, err := s.svc.People(r.Context())
		if err != nil {
			return base, err
		}
		for _, p := range people {
			base.Rows = append(base.Rows, s.personRowOf(r, p))
		}
	}
	return base, nil
}

// personRowOf says a person in the words of the pages.
func (s *server) personRowOf(r *http.Request, p service.Person) personRow {
	now := s.now()
	row := personRow{ID: p.ID, Email: p.Email, Role: string(p.OrgRole), RoleWord: orgRoleWords[p.OrgRole], State: p.State,
		Self: p.ID == callerID(r.Context()), Href: "/settings/people/" + url.PathEscape(p.ID)}
	switch {
	case p.State == "invited" && p.InviteExpiresAt != nil && !now.Before(*p.InviteExpiresAt):
		row.StateText = "Invited · the link ran out"
	case p.State == "invited":
		row.StateText = "Invited"
	default:
		row.StateText = "Active"
	}
	if p.EndsAt != nil {
		row.Ends = whenOf(now, p.EndsAt, "")
		if p.EndsAt.After(now) {
			row.EndsText = "ends " + row.Ends.Text
		} else {
			row.EndsText = "ended " + row.Ends.Text
		}
	}
	for _, pr := range p.Projects {
		line := pr.Project + ": " + roleWords[pr.Role]
		var envs []string
		for _, env := range slices.Sorted(maps.Keys(pr.Environments)) {
			envs = append(envs, env+" "+roleWords[pr.Environments[env]])
		}
		if len(envs) > 0 {
			line += " (" + strings.Join(envs, ", ") + ")"
		}
		if pr.EndsAt != nil {
			line += " until " + pr.EndsAt.UTC().Format("Jan 2")
		}
		row.Projects = append(row.Projects, line)
	}
	return row
}

// formFor builds the person form with the project blocks of every project; person, when set, fills it.
func (s *server) formFor(r *http.Request, action, submit string, person *service.Person) (*personForm, error) {
	projects, err := s.svc.Projects(r.Context())
	if err != nil {
		return nil, err
	}
	f := &personForm{Action: action, Submit: submit, OrgRole: string(service.OrgMember)}
	var held map[string]service.ProjectRole
	if person != nil {
		f.Email, f.EmailLocked, f.OrgRole = person.Email, true, string(person.OrgRole)
		if person.EndsAt != nil {
			f.EndsAt = person.EndsAt.UTC().Format("2006-01-02")
		}
		held = map[string]service.ProjectRole{}
		for _, pr := range person.Projects {
			held[pr.Project] = pr
		}
	}
	for _, p := range projects {
		block := projectForm{Project: p.ID, Display: p.DisplayName}
		pr, has := held[p.ID]
		if has {
			block.Role = string(pr.Role)
			if pr.EndsAt != nil {
				block.EndsAt = pr.EndsAt.UTC().Format("2006-01-02")
			}
		}
		for _, e := range p.Environments {
			if e.Preview != nil || app.IsPreviewEnvironment(e.Name) {
				continue
			}
			block.Envs = append(block.Envs, envForm{Name: e.Name, Override: string(pr.Environments[e.Name])})
		}
		f.Projects = append(f.Projects, block)
	}
	return f, nil
}

// endOfDay is a date typed in a form as the end of that day in UTC, as the API token's expiry is.
func endOfDay(value string) (*time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, true
	}
	day, err := time.Parse("2006-01-02", value)
	if err != nil {
		return nil, false
	}
	return new(day.Add(24*time.Hour - time.Second)), true
}

// readPersonForm reads what the person form posted into the form (to draw again) and the input (to
// send). A date that isn't one is a sentence at its field, and the input is then not made.
func readPersonForm(r *http.Request, f *personForm) (service.PersonInput, map[string]string) {
	errs := map[string]string{}
	post := r.PostForm
	if !f.EmailLocked {
		f.Email = strings.TrimSpace(post.Get("email"))
	}
	f.OrgRole = post.Get("orgRole")
	f.EndsAt = strings.TrimSpace(post.Get("endsAt"))
	in := service.PersonInput{Email: f.Email, OrgRole: service.OrgRole(f.OrgRole)}
	var ok bool
	if in.EndsAt, ok = endOfDay(f.EndsAt); !ok {
		errs["endsAt"] = "The end date must be a date."
	}
	for i := range f.Projects {
		b := &f.Projects[i]
		b.Role = post.Get("role." + b.Project)
		b.EndsAt = strings.TrimSpace(post.Get("ends." + b.Project))
		for j := range b.Envs {
			b.Envs[j].Override = post.Get("env." + b.Project + "/" + b.Envs[j].Name)
		}
		// Only a member has project roles; an owner and an admin have every role everywhere.
		if in.OrgRole != service.OrgMember || b.Role == "" {
			continue
		}
		pr := service.ProjectRole{Project: b.Project, Role: service.Role(b.Role)}
		if pr.EndsAt, ok = endOfDay(b.EndsAt); !ok {
			errs["ends."+b.Project] = "The end date must be a date."
		}
		for _, e := range b.Envs {
			if e.Override != "" {
				if pr.Environments == nil {
					pr.Environments = map[string]service.Role{}
				}
				pr.Environments[e.Name] = service.Role(e.Override)
			}
		}
		in.Projects = append(in.Projects, pr)
	}
	return in, errs
}

// peopleRefused maps the service's refusals of a person form to the field they are about, or to the
// page's notice. It returns false for an error that is not a refusal of the input.
func peopleRefused(err error, data *peopleData) bool {
	var fe *app.FieldError
	var pe *service.PlaceError
	switch {
	case errors.Is(err, service.ErrEmailTaken):
		data.Errors["email"] = "Someone with this email is already here."
	case errors.Is(err, service.ErrLastOwner):
		data.Notice = &notice{Kind: "failed", Text: "This is the last owner. Make another person an owner first."}
	case errors.Is(err, service.ErrPersonNotFound):
		data.Notice = &notice{Kind: "failed", Text: "That person isn't here any more."}
	case errors.As(err, &fe):
		field := fe.Field
		switch {
		case field == "email" || field == "orgRole" || field == "endsAt":
		case strings.HasPrefix(field, "projects"):
			field = "projects"
		default:
			data.Notice = &notice{Kind: "failed", Text: "The " + field + " " + fe.Message + "."}
			return true
		}
		data.Errors[field] = "The " + fieldWords(field) + " " + fe.Message + "."
	case errors.As(err, &pe):
		data.Errors["projects"] = pe.Error() + "."
	default:
		return false
	}
	return true
}

func fieldWords(field string) string {
	switch field {
	case "endsAt":
		return "end date"
	case "orgRole":
		return "role"
	}
	return field
}

// peoplePage draws Settings › People with the given status.
func (s *server) peoplePage(w http.ResponseWriter, r *http.Request, status int, data peopleData) {
	section, err := s.peopleSection(r, data)
	if err != nil {
		s.serverError(w, r, "read people", err)
		return
	}
	s.renderSettings(w, r, entryPeople, settingsData{People: section, Status: status})
}

func newPeopleData(view string) peopleData {
	return peopleData{View: view, Errors: map[string]string{}}
}

// showPeople answers GET /settings/people.
func (s *server) showPeople(w http.ResponseWriter, r *http.Request, session string) {
	d := newPeopleData("list")
	switch r.URL.Query().Get("did") {
	case "removed":
		d.Notice = &notice{Kind: "healthy", Text: "Removed. Their sessions, tokens and passkeys ended with it."}
	}
	s.peoplePage(w, r, http.StatusOK, d)
}

// showNewPerson answers GET /settings/people/new.
func (s *server) showNewPerson(w http.ResponseWriter, r *http.Request, session string) {
	f, err := s.formFor(r, "/settings/people", "Invite", nil)
	if err != nil {
		s.serverError(w, r, "list projects", err)
		return
	}
	d := newPeopleData("new")
	d.Form = f
	s.peoplePage(w, r, http.StatusOK, d)
}

// invitedOf is the link of an invite, drawn once.
func (s *server) invitedOf(r *http.Request, in service.Invited) *invitedView {
	base := s.svc.PublicURL()
	if base == "" {
		base = s.origin(r)
	}
	return &invitedView{Email: in.Person.Email, URL: service.InviteURL(base, in.Token), Until: whenOf(s.now(), in.Person.InviteExpiresAt, "")}
}

// invitePerson answers POST /settings/people: the person is made and the answer (200, never cached)
// shows the link once. A refusal draws the form again at the field.
func (s *server) invitePerson(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	f, err := s.formFor(r, "/settings/people", "Invite", nil)
	if err != nil {
		s.serverError(w, r, "list projects", err)
		return
	}
	d := newPeopleData("new")
	d.Form = f
	in, errs := readPersonForm(r, f)
	d.Errors = errs
	if len(errs) > 0 {
		s.peoplePage(w, r, http.StatusUnprocessableEntity, d)
		return
	}
	invited, err := s.svc.InvitePerson(r.Context(), in)
	switch {
	case err == nil:
		list := newPeopleData("list")
		list.Invited = s.invitedOf(r, invited)
		w.Header().Set("Cache-Control", "no-store")
		s.peoplePage(w, r, http.StatusOK, list)
	case peopleRefused(err, &d):
		s.peoplePage(w, r, http.StatusUnprocessableEntity, d)
	default:
		s.serverError(w, r, "invite a person", err)
	}
}

// personPage draws one person's page. extra fills what the answer adds (a notice, a link, the refused
// form's sentences) and may change the status; it answers true when it already answered the request
// (a redirect), and then nothing is drawn.
func (s *server) personPage(w http.ResponseWriter, r *http.Request, status int, id string, extra func(*peopleData, *personForm, *int) (answered bool)) {
	p, err := s.svc.Person(r.Context(), id)
	if errors.Is(err, service.ErrPersonNotFound) {
		s.message(w, r, http.StatusNotFound, "Not found", "That person isn't here.", "They may have been removed. Go back to People.")
		return
	} else if err != nil {
		s.serverError(w, r, "read person", err)
		return
	}
	f, err := s.formFor(r, "/settings/people/"+url.PathEscape(id), "Save", &p)
	if err != nil {
		s.serverError(w, r, "list projects", err)
		return
	}
	row := s.personRowOf(r, p)
	d := newPeopleData("person")
	d.Row, d.Form = &row, f
	who, _ := service.PrincipalFrom(r.Context())
	d.ReadOnly = !who.Owner() && p.OrgRole != service.OrgMember
	if extra != nil && extra(&d, f, &status) {
		return
	}
	s.peoplePage(w, r, status, d)
}

// showPerson answers GET /settings/people/{id}.
func (s *server) showPerson(w http.ResponseWriter, r *http.Request, session string) {
	s.personPage(w, r, http.StatusOK, r.PathValue("id"), func(d *peopleData, _ *personForm, _ *int) bool {
		if r.URL.Query().Get("saved") == "1" {
			d.Notice = &notice{Kind: "healthy", Text: "Saved. Their next request uses the new roles."}
		}
		return false
	})
}

// updatePerson answers POST /settings/people/{id}: the form replaces the person's roles. 303 back to
// their page on success; a refusal draws the page again at the field.
func (s *server) updatePerson(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	id := r.PathValue("id")
	s.personPage(w, r, http.StatusOK, id, func(d *peopleData, f *personForm, status *int) bool {
		in, errs := readPersonForm(r, f)
		d.Errors = errs
		if len(errs) == 0 {
			_, err := s.svc.UpdatePerson(r.Context(), id, in)
			switch {
			case err == nil:
				http.Redirect(w, r, "/settings/people/"+url.PathEscape(id)+"?saved=1", http.StatusSeeOther)
				return true
			case !peopleRefused(err, d):
				s.serverError(w, r, "update a person", err)
				return true
			}
		}
		*status = http.StatusUnprocessableEntity
		return false
	})
}

// reinvitePerson answers POST /settings/people/{id}/invite: a new link, drawn once (200, never cached).
func (s *server) reinvitePerson(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("id")
	invited, err := s.svc.ReinvitePerson(r.Context(), id)
	switch {
	case err == nil:
		w.Header().Set("Cache-Control", "no-store")
		s.personPage(w, r, http.StatusOK, id, func(d *peopleData, _ *personForm, _ *int) bool { d.Invited = s.invitedOf(r, invited); return false })
	case errors.Is(err, service.ErrNotInvited):
		s.personPage(w, r, http.StatusConflict, id, func(d *peopleData, _ *personForm, _ *int) bool {
			d.Notice = &notice{Kind: "warning", Text: "This person has accepted their invite already."}
			return false
		})
	case errors.Is(err, service.ErrPersonNotFound):
		s.message(w, r, http.StatusNotFound, "Not found", "That person isn't here.", "They may have been removed. Go back to People.")
	default:
		s.serverError(w, r, "invite again", err)
	}
}

// showRemovePerson answers GET /settings/people/{id}/remove: the page that says what removing does.
func (s *server) showRemovePerson(w http.ResponseWriter, r *http.Request, session string) {
	s.personPage(w, r, http.StatusOK, r.PathValue("id"), func(d *peopleData, _ *personForm, _ *int) bool { d.View = "remove"; return false })
}

// removePerson answers POST /settings/people/{id}/remove: their sessions, tokens and passkeys end with
// the request. 303 to the list.
func (s *server) removePerson(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("id")
	err := s.svc.RemovePerson(r.Context(), id)
	switch {
	case err == nil:
		http.Redirect(w, r, "/settings/people?did=removed", http.StatusSeeOther)
	case errors.Is(err, service.ErrLastOwner):
		s.personPage(w, r, http.StatusConflict, id, func(d *peopleData, _ *personForm, _ *int) bool {
			d.View = "remove"
			d.Notice = &notice{Kind: "failed", Text: "This is the last owner. Make another person an owner first."}
			return false
		})
	case errors.Is(err, service.ErrPersonNotFound):
		http.Redirect(w, r, "/settings/people", http.StatusSeeOther)
	default:
		s.serverError(w, r, "remove a person", err)
	}
}

// ---- /invite ---------------------------------------------------------------------------------

// inviteData is the page of an invite link: the email it is for, and the password to choose.
type inviteData struct {
	pageData
	Host    string
	Token   string
	Email   string
	Valid   bool
	Notice  *notice
	PwdErr  string
	ConfErr string
}

const invalidInvite = "This invite link isn't valid any more. Ask for a new one."

func (s *server) invitePageOf(r *http.Request, token string) inviteData {
	return inviteData{pageData: s.page(r, "Join"), Host: r.Host, Token: token}
}

// inviteFailure draws the page for a link that can't be used: not valid any more, or the address
// locked for too many wrong tries. It answers true when err was one of them.
func (s *server) inviteFailure(w http.ResponseWriter, r *http.Request, d inviteData, err error) bool {
	var limited *service.SigninLimitError
	switch {
	case errors.Is(err, service.ErrInviteInvalid):
		d.Notice = &notice{Kind: "failed", Text: invalidInvite}
		s.render(w, http.StatusNotFound, "invite", d)
	case errors.As(err, &limited):
		d.Notice = &notice{Kind: "failed", Text: limitedSentence(limited.RetryAfter.Minutes())}
		s.render(w, http.StatusTooManyRequests, "invite", d)
	default:
		return false
	}
	return true
}

// showInvite answers GET /invite?token=…: the person's email and the form for their password. It needs
// no session: the link is the credential, and a wrong one counts against the address like a wrong
// password.
func (s *server) showInvite(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	d := s.invitePageOf(r, token)
	p, err := s.svc.InviteFor(r.Context(), token, s.proxies.ClientAddress(r))
	switch {
	case err == nil:
		d.Valid, d.Email = true, p.Email
		s.render(w, http.StatusOK, "invite", d)
	case !s.inviteFailure(w, r, d, err):
		s.serverError(w, r, "read invite", err)
	}
}

// acceptInvite answers POST /invite: the password is set through the service, a session starts for the
// person and the browser goes to Apps. A bad token or a refused password draws the page again.
func (s *server) acceptInvite(w http.ResponseWriter, r *http.Request) {
	if !s.readForm(w, r) {
		return
	}
	token, password, confirm := r.PostFormValue("token"), r.PostFormValue("password"), r.PostFormValue("confirm")
	d := s.invitePageOf(r, token)
	address := s.proxies.ClientAddress(r)
	if password != confirm {
		// Costs nothing and says nothing of the token: look the email up so the page can name them.
		p, err := s.svc.InviteFor(r.Context(), token, address)
		if err != nil {
			if !s.inviteFailure(w, r, d, err) {
				s.serverError(w, r, "read invite", err)
			}
			return
		}
		d.Valid, d.Email, d.ConfErr = true, p.Email, "The two passwords don't match."
		s.render(w, http.StatusUnprocessableEntity, "invite", d)
		return
	}
	p, err := s.svc.AcceptInvite(r.Context(), token, password, address)
	var fe *app.FieldError
	switch {
	case err == nil:
		session, err := s.auth.StartSessionFor(r.Context(), p.ID)
		if err != nil {
			s.serverError(w, r, "start a session", err)
			return
		}
		httpx.SetSessionCookie(w, session.Value, session.ExpiresAt, s.proxies.HTTPS(r))
		http.Redirect(w, r, "/apps", http.StatusSeeOther)
	case errors.As(err, &fe) && fe.Field == "password":
		// The token was fine: say whose page this is again.
		if who, e := s.svc.InviteFor(r.Context(), token, address); e == nil {
			d.Email = who.Email
		}
		d.Valid, d.PwdErr = true, "The password "+fe.Message+"."
		s.render(w, http.StatusUnprocessableEntity, "invite", d)
	case !s.inviteFailure(w, r, d, err):
		s.serverError(w, r, "accept an invite", err)
	}
}
