package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/edge"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// Domains (feature 023): every address the apps answer on, with what
// Caravana's own checks found (DNS against this host, the certificate Traefik
// presents), a fresh check, and a Team-only domain's new password.
//
// A Team-only password is in two answers only, each sent once and with
// Cache-Control: no-store: the deploy that turned the option on
// (deploymentJSON.TeamPasswords) and rotateTeamPassword's. No other answer,
// error or log line holds a password or its hash.

// The codes only the domains give.
const (
	codeDomainNotFound = "domain_not_found"
	codeDomainTaken    = "domain_taken"
	codeNotTeamOnly    = "not_team_only"
)

// domainError maps the domain errors to their codes; mapError calls it.
func domainError(err error) *Error {
	var taken *service.DomainTakenError
	switch {
	case errors.As(err, &taken):
		return &Error{Status: http.StatusConflict, Code: codeDomainTaken, Field: taken.Field, Message: err.Error()}
	case errors.Is(err, service.ErrDomainNotFound):
		return &Error{Status: http.StatusNotFound, Code: codeDomainNotFound, Message: err.Error()}
	case errors.Is(err, service.ErrNotTeamOnly):
		return &Error{Status: http.StatusConflict, Code: codeNotTeamOnly, Message: err.Error()}
	}
	return nil
}

// dnsJSON is what the DNS check found (schema DomainDNS).
type dnsJSON struct {
	Status   string   `json:"status"`
	Seen     []string `json:"seen"`
	Expected []string `json:"expected"`
}

// certificateJSON is the certificate the edge presented (schema
// DomainCertificate). The dates are null when there are none.
type certificateJSON struct {
	Status   string     `json:"status"`
	Issuer   string     `json:"issuer"`
	NotAfter *time.Time `json:"notAfter"`
	RenewsAt *time.Time `json:"renewsAt"`
}

// domainJSON is one domain (schema Domain).
type domainJSON struct {
	Host        string `json:"host"`
	App         string `json:"app"`
	Project     string `json:"project"`
	Environment string `json:"environment"`
	AppName     string `json:"appName"`
	Service     string `json:"service"`
	Port        string `json:"port"`
	Path        string `json:"path"`
	WWWRedirect bool   `json:"wwwRedirect"`
	TeamOnly    bool   `json:"teamOnly"`
	Router      string `json:"router"`
	// RedirectOf is set on a redirect row: the host it redirects to.
	RedirectOf  string          `json:"redirectOf,omitempty"`
	State       string          `json:"state"`
	DNS         dnsJSON         `json:"dns"`
	Certificate certificateJSON `json:"certificate"`
	CheckedAt   *time.Time      `json:"checkedAt"`
}

func toDNSJSON(d edge.DNS) dnsJSON {
	return dnsJSON{Status: d.Status, Seen: addrList(d.Seen), Expected: addrList(d.Expected)}
}

func addrList(in []netip.Addr) []string {
	out := make([]string, len(in)) // never null in the JSON
	for i, a := range in {
		out[i] = a.String()
	}
	return out
}

func toCertificateJSON(c edge.Certificate) certificateJSON {
	j := certificateJSON{Status: c.Status, Issuer: c.Issuer}
	if !c.NotAfter.IsZero() {
		j.NotAfter = new(c.NotAfter.UTC())
	}
	if !c.RenewsAt.IsZero() {
		j.RenewsAt = new(c.RenewsAt.UTC())
	}
	return j
}

func toDomainJSON(v service.DomainView) domainJSON {
	return domainJSON{
		Host: v.Host, App: v.App, Project: v.Project, Environment: v.Environment, AppName: v.AppName,
		Service: v.Service, Port: v.Port, Path: v.Path, WWWRedirect: v.WWWRedirect, TeamOnly: v.TeamOnly,
		Router: v.Router, RedirectOf: v.RedirectOf, State: string(v.State),
		DNS: toDNSJSON(v.DNS), Certificate: toCertificateJSON(v.Certificate), CheckedAt: v.CheckedAt,
	}
}

// listDomains answers GET /v1/domains?app=&project=&fresh=: every domain,
// problems first. fresh=true checks them now, as the watcher would, before
// answering; otherwise the answer is the last checks'.
func (s *server) listDomains(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	f := service.DomainFilter{App: q.Get("app"), Project: q.Get("project")}
	fresh := false
	switch q.Get("fresh") {
	case "", "false":
	case "true":
		fresh = true
	default:
		return &Error{Status: http.StatusBadRequest, Code: codeInvalidField, Field: "fresh",
			Message: fmt.Sprintf("must be true or false, got %q", q.Get("fresh"))}
	}
	if fresh {
		if err := s.svc.RefreshDomains(r.Context(), f); err != nil {
			return err
		}
	}
	views, err := s.svc.Domains(r.Context(), f)
	if err != nil {
		return err
	}
	out := struct {
		Domains []domainJSON `json:"domains"`
	}{Domains: make([]domainJSON, 0, len(views))}
	for _, v := range views {
		out.Domains = append(out.Domains, toDomainJSON(v))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// getDomain answers GET /v1/domains/{host}: one domain, as listDomains has
// it, or 404 domain_not_found.
func (s *server) getDomain(w http.ResponseWriter, r *http.Request) error {
	v, err := s.svc.Domain(r.Context(), r.PathValue("host"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toDomainJSON(v))
	return nil
}

// checkDomainRequest is schema CheckDomainRequest.
type checkDomainRequest struct {
	Host string `json:"host"`
	App  string `json:"app,omitempty"`
}

// dnsRecordJSON is a record to create (schema DNSRecord).
type dnsRecordJSON struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

// domainCheckJSON is a fresh check (schema DomainCheck).
type domainCheckJSON struct {
	Host        string          `json:"host"`
	Record      []dnsRecordJSON `json:"record"`
	DNS         dnsJSON         `json:"dns"`
	Certificate certificateJSON `json:"certificate"`
	CheckedAt   time.Time       `json:"checkedAt"`
}

// checkDomain answers POST /v1/domains/check: the host checked now, not from
// the cache, with the records that point it here. A host that is no app's yet
// (the Add form) gets DNS only. A host Caravana can't take is 422
// invalid_field on "host"; an unknown app 404. Sent with no-store: it is a
// moment's facts.
func (s *server) checkDomain(w http.ResponseWriter, r *http.Request) error {
	var in checkDomainRequest
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	c, err := s.svc.CheckDomain(r.Context(), in.Host, in.App)
	if err != nil {
		var fe *app.FieldError
		if errors.As(err, &fe) {
			return &Error{Status: http.StatusUnprocessableEntity, Code: codeInvalidField, Field: fe.Field, Message: fe.Message}
		}
		return err
	}
	out := domainCheckJSON{Host: c.Host, Record: make([]dnsRecordJSON, 0, len(c.Records)),
		DNS: toDNSJSON(c.DNS), Certificate: toCertificateJSON(c.Certificate), CheckedAt: c.CheckedAt}
	for _, rec := range c.Records {
		out.Record = append(out.Record, dnsRecordJSON(rec))
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
	return nil
}

// teamPasswordJSON is a Team-only domain's new password (schema
// TeamPassword), in this one answer.
type teamPasswordJSON struct {
	Host         string `json:"host"`
	User         string `json:"user"`
	TeamPassword string `json:"teamPassword"`
}

// rotateTeamPassword answers POST /v1/apps/{id}/domains/{host}/team-password:
// a new password for a Team-only domain, shown here once (no-store). The old
// one stops working once Traefik reads the change, without a deploy.
func (s *server) rotateTeamPassword(w http.ResponseWriter, r *http.Request) error {
	host := r.PathValue("host")
	password, err := s.svc.RotateTeamPassword(r.Context(), r.PathValue("id"), host)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, teamPasswordJSON{Host: host, User: "team", TeamPassword: password})
	return nil
}

// certificateSettingsRequest is schema CertificateSettingsRequest.
type certificateSettingsRequest struct {
	WarnBeforeDays int `json:"warnBeforeDays"`
}

// getCertificateSettings answers GET /v1/settings/certificates.
func (s *server) getCertificateSettings(w http.ResponseWriter, r *http.Request) error {
	cs, err := s.svc.CertificateSettings(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, cs)
	return nil
}

// putCertificateSettings answers PUT /v1/settings/certificates: the warning
// window (7, 14 or 30 days; 422 otherwise), and the settings as they are now.
func (s *server) putCertificateSettings(w http.ResponseWriter, r *http.Request) error {
	var in certificateSettingsRequest
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if err := s.svc.SetWarnBeforeDays(r.Context(), in.WarnBeforeDays); err != nil {
		var fe *app.FieldError
		if errors.As(err, &fe) {
			return &Error{Status: http.StatusUnprocessableEntity, Code: codeInvalidField, Field: fe.Field, Message: fe.Message}
		}
		return err
	}
	return s.getCertificateSettings(w, r)
}

// listCertificates answers GET /v1/certificates: the certificate of every
// served domain, as the last checks found it, problems first.
func (s *server) listCertificates(w http.ResponseWriter, r *http.Request) error {
	list, err := s.svc.Certificates(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, struct {
		Certificates []service.CertificateView `json:"certificates"`
	}{Certificates: list})
	return nil
}
