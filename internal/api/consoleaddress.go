package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The console's address (feature 051, specs/051-console-address/contracts/api.md): read the current
// address and what depends on it, check a new domain, follow the change, cancel it before the switch and
// switch. A session or a full API token may call them; a deploy token is refused by the scope
// middleware's default deny, since none of these routes is in scopeRules. Every answer, an error too, is
// `no-store`: a change moves by the minute.

// The codes only these operations give. domain_taken, dns_provider_refused, dns_provider_unavailable,
// no_agent and invalid_field are the API's own.
const (
	codeDomainNotHere            = "domain_not_here"
	codeConsoleChangeOpen        = "console_address_change_open"
	codeConsoleUnsupported       = "console_address_unsupported"
	codeConsoleChangeNotFound    = "console_address_change_not_found"
	codeConsoleNotCancellable    = "console_address_not_cancellable"
	codeConsoleNotReady          = "console_address_not_ready"
	codeConsoleAddressCheckFails = "console_address_check_failed"
)

// consoleAddressError maps the refusals of the console address operations; mapError calls it before the
// space errors, so a switch without an agent is a 409 no_agent that names no field.
func consoleAddressError(err error) *Error {
	var notHere *service.DomainNotHereError
	var check *service.ConsoleCheckError
	var noAgent service.ConsoleNoAgentError
	switch {
	case errors.As(err, &notHere):
		return &Error{Status: http.StatusUnprocessableEntity, Code: codeDomainNotHere, Field: notHere.Field, Message: err.Error()}
	case errors.As(err, &check):
		return &Error{Status: http.StatusUnprocessableEntity, Code: codeConsoleAddressCheckFails, Field: check.Field, Message: err.Error()}
	case errors.Is(err, service.ErrConsoleChangeOpen):
		return &Error{Status: http.StatusConflict, Code: codeConsoleChangeOpen, Message: err.Error()}
	case errors.Is(err, service.ErrConsoleAddressUnsupported):
		return &Error{Status: http.StatusConflict, Code: codeConsoleUnsupported, Message: err.Error()}
	case errors.Is(err, service.ErrConsoleChangeNotFound):
		return &Error{Status: http.StatusNotFound, Code: codeConsoleChangeNotFound, Message: err.Error()}
	case errors.Is(err, service.ErrConsoleChangeNotCancellable):
		return &Error{Status: http.StatusConflict, Code: codeConsoleNotCancellable, Message: err.Error()}
	case errors.Is(err, service.ErrConsoleChangeNotReady):
		return &Error{Status: http.StatusConflict, Code: codeConsoleNotReady, Message: err.Error()}
	case errors.As(err, &noAgent):
		// Only the switch's own refusal: the cleanup routes' no_agent names the server (spaceError).
		return &Error{Status: http.StatusConflict, Code: codeNoAgent, Message: err.Error()}
	}
	return nil
}

// consoleAddressJSON is schema ConsoleAddress. Change is null before any change was made.
type consoleAddressJSON struct {
	Domain       string                 `json:"domain"`
	PublicURL    string                 `json:"publicUrl"`
	Issuer       string                 `json:"issuer"`
	SSLIPHint    string                 `json:"sslipHint"`
	CanChange    bool                   `json:"canChange"`
	CannotChange string                 `json:"cannotChange"`
	Agent        bool                   `json:"agent"`
	OldDomains   []consoleOldDomainJSON `json:"oldDomains"`
	Change       *consoleChangeJSON     `json:"change"`
	Dependents   consoleDependentsJSON  `json:"dependents"`
}

// consoleOldDomainJSON is an old domain still routed and redirected until Until.
type consoleOldDomainJSON struct {
	Domain string    `json:"domain"`
	Until  time.Time `json:"until"`
}

// consoleDependentsJSON is schema ConsoleAddressDependents.
type consoleDependentsJSON struct {
	Passkeys     int `json:"passkeys"`
	Webhooks     int `json:"webhooks"`
	DeployTokens int `json:"deployTokens"`
	APITokens    int `json:"apiTokens"`
	Servers      int `json:"servers"`
}

// consoleChangeJSON is schema ConsoleAddressChange. readyAt, switchAt, settledAt and redirectUntil are
// null until reached.
type consoleChangeJSON struct {
	ID               string             `json:"id"`
	Domain           string             `json:"domain"`
	OldDomain        string             `json:"oldDomain"`
	RedirectDays     int                `json:"redirectDays"`
	Status           string             `json:"status"`
	Checks           []consoleCheckJSON `json:"checks"`
	Reason           string             `json:"reason"`
	DNSRecordCreated bool               `json:"dnsRecordCreated"`
	CreatedAt        time.Time          `json:"createdAt"`
	ReadyAt          *time.Time         `json:"readyAt"`
	SwitchAt         *time.Time         `json:"switchAt"`
	SettledAt        *time.Time         `json:"settledAt"`
	RedirectUntil    *time.Time         `json:"redirectUntil"`
}

// consoleCheckJSON is one check of a change (schema ConsoleAddressCheck).
type consoleCheckJSON struct {
	Name    string `json:"name"`
	State   string `json:"state"`
	Message string `json:"message"`
}

func toConsoleChangeJSON(c store.ConsoleAddressChange) consoleChangeJSON {
	out := consoleChangeJSON{
		ID: c.ID, Domain: c.Domain, OldDomain: c.OldDomain, RedirectDays: c.RedirectDays, Status: c.Status,
		Checks: make([]consoleCheckJSON, 0, len(c.Checks)), Reason: c.Reason, DNSRecordCreated: c.DNSRecordCreated,
		CreatedAt: c.CreatedAt.UTC(), ReadyAt: timePtr(c.ReadyAt), SwitchAt: timePtr(c.SwitchAt),
		SettledAt: timePtr(c.SettledAt), RedirectUntil: timePtr(c.RedirectUntil),
	}
	for _, k := range c.Checks {
		out.Checks = append(out.Checks, consoleCheckJSON{Name: k.Name, State: k.State, Message: k.Message})
	}
	return out
}

func toConsoleAddressJSON(v service.ConsoleAddressView) consoleAddressJSON {
	out := consoleAddressJSON{
		Domain: v.Domain, PublicURL: v.PublicURL, Issuer: v.Issuer, SSLIPHint: v.SSLIPHint,
		CanChange: v.CanChange, CannotChange: v.CannotChange, Agent: v.Agent,
		OldDomains: make([]consoleOldDomainJSON, 0, len(v.OldDomains)),
		Dependents: consoleDependentsJSON{Passkeys: v.Dependents.Passkeys, Webhooks: v.Dependents.Webhooks,
			DeployTokens: v.Dependents.DeployTokens, APITokens: v.Dependents.APITokens, Servers: v.Dependents.Servers},
	}
	for _, o := range v.OldDomains {
		out.OldDomains = append(out.OldDomains, consoleOldDomainJSON{Domain: o.Domain, Until: o.Until.UTC()})
	}
	if v.Change != nil {
		c := toConsoleChangeJSON(*v.Change)
		out.Change = &c
	}
	return out
}

// getConsoleAddress answers GET /v1/console-address.
func (s *server) getConsoleAddress(w http.ResponseWriter, r *http.Request) error {
	noStore(w)
	v, err := s.svc.ConsoleAddress(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toConsoleAddressJSON(v))
	return nil
}

// startConsoleAddressChange answers POST /v1/console-address/changes with 202 and the change, checking:
// the name and its DNS were checked before anything was recorded, and the route and the certificate are
// followed in the background (getConsoleAddressChange). redirectDays is 30 when it is left out, since 0 is
// a choice of its own ("don't keep the old address").
func (s *server) startConsoleAddressChange(w http.ResponseWriter, r *http.Request) error {
	noStore(w)
	var in struct {
		Domain          string `json:"domain"`
		RedirectDays    *int   `json:"redirectDays"`
		CreateDNSRecord bool   `json:"createDNSRecord"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	days := service.DefaultConsoleRedirectDays
	if in.RedirectDays != nil {
		days = *in.RedirectDays
	}
	c, err := s.svc.StartConsoleAddressChange(r.Context(), in.Domain, days, in.CreateDNSRecord)
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/v1/console-address/changes/"+c.ID)
	writeJSON(w, http.StatusAccepted, toConsoleChangeJSON(c))
	return nil
}

// getConsoleAddressChange answers GET /v1/console-address/changes/{id}.
func (s *server) getConsoleAddressChange(w http.ResponseWriter, r *http.Request) error {
	noStore(w)
	c, err := s.svc.ConsoleAddressChange(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toConsoleChangeJSON(c))
	return nil
}

// cancelConsoleAddressChange answers DELETE /v1/console-address/changes/{id} with the cancelled change.
func (s *server) cancelConsoleAddressChange(w http.ResponseWriter, r *http.Request) error {
	noStore(w)
	c, err := s.svc.CancelConsoleAddressChange(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toConsoleChangeJSON(c))
	return nil
}

// switchConsoleAddress answers POST /v1/console-address/changes/{id}/switch with 202 and the change,
// switching: the server agent applies it at its next sync (within about a minute).
func (s *server) switchConsoleAddress(w http.ResponseWriter, r *http.Request) error {
	noStore(w)
	c, err := s.svc.SwitchConsoleAddress(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusAccepted, toConsoleChangeJSON(c))
	return nil
}
