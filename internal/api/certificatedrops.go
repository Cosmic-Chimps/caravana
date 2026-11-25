package api

import (
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/store"
)

// Dropping the earlier certificates an upload covers (feature 056). Everything here is an administrator's,
// and no answer holds more than the names of what was dropped and one sentence: never a certificate, a
// key or an account. A deploy token is refused by the scope middleware's default deny.

// certificateDropJSON is schema CertificateDrop. startedAt and finishedAt are null until they happen;
// dropped lists each certificate removed as its names joined with "+", and is empty while none is known.
type certificateDropJSON struct {
	ID          string     `json:"id"`
	Server      string     `json:"server"`
	Status      string     `json:"status"`
	Dropped     []string   `json:"dropped"`
	Error       string     `json:"error"`
	Actor       string     `json:"actor"`
	RequestedAt time.Time  `json:"requestedAt"`
	StartedAt   *time.Time `json:"startedAt"`
	FinishedAt  *time.Time `json:"finishedAt"`
}

func toCertificateDropJSON(d store.CertificateDrop) certificateDropJSON {
	dropped := d.Dropped
	if dropped == nil {
		dropped = []string{}
	}
	return certificateDropJSON{ID: d.ID, Server: d.ServerName, Status: d.Status, Dropped: dropped, Error: d.Error, Actor: d.Actor,
		RequestedAt: d.RequestedAt.UTC(), StartedAt: timePtr(d.StartedAt), FinishedAt: timePtr(d.FinishedAt)}
}

// dropEarlierCertificates answers POST /v1/certificates/drops with 202 and the drop's Location: the ingress
// server's agent removes the certificates the uploads cover at its next sync, and Traefik restarts once.
func (s *server) dropEarlierCertificates(w http.ResponseWriter, r *http.Request) error {
	noStore(w)
	d, err := s.svc.RequestCertificateDrop(r.Context())
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/v1/certificates/drops/"+d.ID)
	writeJSON(w, http.StatusAccepted, toCertificateDropJSON(d))
	return nil
}

// listCertificateDrops answers GET /v1/certificates/drops: the newest 20, newest first.
func (s *server) listCertificateDrops(w http.ResponseWriter, r *http.Request) error {
	noStore(w)
	list, err := s.svc.CertificateDrops(r.Context())
	if err != nil {
		return err
	}
	out := struct {
		Drops []certificateDropJSON `json:"drops"`
	}{Drops: make([]certificateDropJSON, 0, len(list))}
	for _, d := range list {
		out.Drops = append(out.Drops, toCertificateDropJSON(d))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// getCertificateDrop answers GET /v1/certificates/drops/{id}.
func (s *server) getCertificateDrop(w http.ResponseWriter, r *http.Request) error {
	noStore(w)
	d, err := s.svc.CertificateDrop(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toCertificateDropJSON(d))
	return nil
}
