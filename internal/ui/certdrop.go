package ui

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Dropping the earlier certificates an upload covers (feature 056, plan D11). Settings › Certificates
// has one button under the uploaded certificates, shown while a host is still on its earlier
// certificate. It is a plain post that answers 303 to the page, whose panel (#drop) follows the drop:
// the panel is read again from /parts/settings/certificates/drop every 3 s while the drop is open, and the
// part answers 286 once it isn't so htmx stops; without script a <noscript> refresh reloads the page. A
// refusal draws the page again with its sentence at the form (409), never a redirect. The panel holds
// what the record holds: the names of what was dropped and one sentence, never more.

const (
	// certDropShownFor is how long after a drop ended the panel still tells how it went.
	certDropShownFor = time.Hour
	// certDropRefreshSeconds is how often a page without script reloads itself while a drop is open.
	certDropRefreshSeconds = 5
)

// certDropView is the panel of one drop.
type certDropView struct {
	ID       string
	Open     bool
	Status   statusView
	Summary  sentence
	Actor    string
	Asked    clockTime
	Ended    clockTime
	HasEnded bool
	// PartHref is where the panel reads itself again from while it is open.
	PartHref string
}

// certDropStatus is the pill of a drop's status.
func certDropStatus(status string) statusView {
	switch status {
	case store.DropRequested:
		return statusView{Kind: "rolling", Word: "Waiting"}
	case store.DropRunning:
		return statusView{Kind: "rolling", Word: "Dropping"}
	case store.DropRestarting:
		return statusView{Kind: "rolling", Word: "Restarting"}
	case store.DropDone:
		return statusView{Kind: "healthy", Word: "Done"}
	case store.DropLost:
		return statusView{Kind: "warning", Word: "No answer"}
	}
	return statusView{Kind: "failed", Word: "Failed"}
}

// certDropSummary is the one sentence of a drop. A done drop whose store was never rewritten (no
// rewritten_at) is one the agent found nothing to drop for; one that was rewritten and names nothing is
// a run found interrupted, which restarted Traefik all the same.
func certDropSummary(d store.CertificateDrop) sentence {
	switch d.Status {
	case store.DropRequested:
		return join(plainS("Waiting for server "), sentence{mono(d.ServerName)}, plainS("'s agent."))
	case store.DropRunning:
		return plainS("Dropping…")
	case store.DropRestarting:
		return plainS("Restarting Traefik…")
	case store.DropDone:
		switch {
		case d.RewrittenAt.IsZero():
			return plainS("Nothing to drop: every earlier certificate also names a host no upload covers.")
		case len(d.Dropped) == 0:
			return plainS("Traefik was restarted to read its certificate store again.")
		}
		return join(plainS("Dropped the earlier certificates of "), sentence{mono(strings.Join(d.Dropped, ", "))}, plainS("."))
	}
	if d.Error != "" {
		return plainS(d.Error)
	}
	return plainS("The drop didn't end well.")
}

// certDropOf is the panel of one stored drop.
func certDropOf(now time.Time, d store.CertificateDrop) *certDropView {
	v := &certDropView{ID: d.ID, Open: store.CertificateDropOpen(d.Status), Status: certDropStatus(d.Status),
		Summary: certDropSummary(d), Actor: d.Actor, Asked: clockOf(now, d.RequestedAt),
		PartHref: "/parts/settings/certificates/drop?id=" + url.QueryEscape(d.ID)}
	if !d.FinishedAt.IsZero() {
		v.Ended, v.HasEnded = clockOf(now, d.FinishedAt), true
	}
	return v
}

// certDropPanel picks the drop the panel tells about: the one the address names, else the newest while it
// is open or ended within the last hour. Nil for none.
func certDropPanel(now time.Time, drops []store.CertificateDrop, asked string) *certDropView {
	for _, d := range drops {
		if asked != "" && d.ID == asked {
			return certDropOf(now, d)
		}
	}
	if len(drops) == 0 {
		return nil
	}
	d := drops[0]
	if store.CertificateDropOpen(d.Status) || now.Sub(d.FinishedAt) <= certDropShownFor {
		return certDropOf(now, d)
	}
	return nil
}

// dropRefusalWords is a refusal of the drop as the sentence at the form: the service's own words, with
// a capital and an end.
func dropRefusalWords(err error) sentence {
	r := []rune(err.Error())
	if len(r) == 0 {
		return nil
	}
	r[0] = unicode.ToUpper(r[0])
	text := string(r)
	if !strings.HasSuffix(text, ".") {
		text += "."
	}
	return plainS(text)
}

// dropCertificates answers POST /settings/certificates/drop: the drop is requested and the page follows
// it; a refusal (no agent holding Traefik's store, nothing uploaded, a drop under way) draws the page
// again with 409 and its sentence at the form.
func (s *server) dropCertificates(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	d, err := s.svc.RequestCertificateDrop(r.Context())
	switch {
	case err == nil:
		http.Redirect(w, r, "/settings/certificates?drop="+url.QueryEscape(d.ID)+"#drop", http.StatusSeeOther)
	case errors.Is(err, service.ErrNoAgent), errors.Is(err, service.ErrNoUploadedCertificates), errors.Is(err, service.ErrCertificateDropRunning):
		s.renderSettings(w, r, entryCertificates, settingsData{Certificates: certificatesData{DropRefusal: dropRefusalWords(err)}, Status: http.StatusConflict})
	default:
		s.serverError(w, r, "drop the earlier certificates", err)
	}
}

// partCertificateDrop answers GET /parts/settings/certificates/drop?id=: the drop's panel alone. Once the drop
// is no longer open it answers 286, which htmx takes as "stop polling" and still swaps in.
func (s *server) partCertificateDrop(w http.ResponseWriter, r *http.Request, session string) {
	d, err := s.svc.CertificateDrop(r.Context(), r.URL.Query().Get("id"))
	var view *certDropView
	switch {
	case err == nil:
		view = certDropOf(s.now().UTC(), d)
	case errors.Is(err, service.ErrCertificateDropNotFound):
		view = &certDropView{Summary: plainS("This drop is gone."), Status: statusView{Kind: "idle", Word: "Gone"}}
	case errors.Is(err, service.ErrNotAllowed):
		s.refuse(w, r, err)
		return
	default:
		s.log.Error("read the certificate drop", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if view.Open {
		s.renderPart(w, "settings", "cert-drop", view)
		return
	}
	var buf strings.Builder
	if err := s.pages["settings"].ExecuteTemplate(&buf, "cert-drop", view); err != nil {
		s.log.Error("render part", "part", "cert-drop", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(286)
	_, _ = w.Write([]byte(buf.String()))
}
