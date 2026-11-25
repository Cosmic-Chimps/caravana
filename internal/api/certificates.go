package api

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// Bring your own certificates (feature 034): the ACME issuer of the installation and the certificates
// an owner uploads. Everything here is an administrator's. No answer or error holds a private key or the
// EAB HMAC key: the types below have no property for one.

// certificatesError maps the certificate refusals to their codes; mapError calls it.
func certificatesError(err error) *Error {
	var noAgent *service.DropNoAgentError
	var re *service.RefusalError
	switch {
	case errors.As(err, &noAgent):
		// The drop's own refusal: it names the ingress server in its sentence and no field (spaceError's
		// no_agent names the server's route parameter).
		return &Error{Status: http.StatusConflict, Code: codeNoAgent, Message: err.Error()}
	case errors.Is(err, service.ErrNoUploadedCertificates):
		return &Error{Status: http.StatusConflict, Code: codeNoUploadedCertificates, Message: err.Error()}
	case errors.Is(err, service.ErrCertificateDropRunning):
		return &Error{Status: http.StatusConflict, Code: codeCertificateDropRunning, Message: err.Error()}
	case errors.Is(err, service.ErrCertificateDropNotFound):
		field := "id"
		if errors.As(err, &re) {
			field = re.Field
		}
		return &Error{Status: http.StatusNotFound, Code: codeCertificateDropNotFound, Field: field, Message: err.Error()}
	case errors.Is(err, service.ErrIssuerUnavailable):
		return &Error{Status: http.StatusConflict, Code: codeIssuerUnavailable, Message: err.Error()}
	case errors.Is(err, service.ErrCertificatesUnavailable):
		return &Error{Status: http.StatusConflict, Code: codeCertificatesUnavailable, Message: err.Error()}
	case errors.Is(err, service.ErrTooManyCertificates):
		return &Error{Status: http.StatusConflict, Code: codeTooManyCertificates, Message: err.Error()}
	case errors.Is(err, service.ErrCertificateNotFound):
		return &Error{Status: http.StatusNotFound, Code: codeCertificateNotFound, Message: err.Error()}
	}
	return nil
}

// certificateInvalid is the 422 of a field of these requests that can't be accepted, like
// putCertificateSettings'. Other errors pass through for mapError.
func certificateInvalid(err error) error {
	var fe *app.FieldError
	if errors.As(err, &fe) {
		return &Error{Status: http.StatusUnprocessableEntity, Code: codeInvalidField, Field: fe.Field, Message: fe.Message}
	}
	return err
}

// certificateIssuerRequest is schema CertificateIssuerRequest. A missing eabHmacKey or caCertificate
// keeps what is stored; an empty caCertificate removes it.
type certificateIssuerRequest struct {
	Server        string  `json:"server"`
	Email         string  `json:"email"`
	EABKeyID      string  `json:"eabKeyId"`
	EABHMACKey    *string `json:"eabHmacKey"`
	CACertificate *string `json:"caCertificate"`
}

// putCertificateIssuer answers PUT /v1/settings/certificates/issuer: the custom ACME issuer, and the
// settings as they are now. Traefik restarts its task once to take it.
func (s *server) putCertificateIssuer(w http.ResponseWriter, r *http.Request) error {
	var in certificateIssuerRequest
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	cs, err := s.svc.SetCertificateIssuer(r.Context(), service.CertificateIssuerInput{Server: in.Server, Email: in.Email,
		EABKeyID: in.EABKeyID, EABHMACKey: in.EABHMACKey, CACertificate: in.CACertificate})
	if err != nil {
		return certificateInvalid(err)
	}
	writeJSON(w, http.StatusOK, cs)
	return nil
}

// deleteCertificateIssuer answers DELETE /v1/settings/certificates/issuer: the installer's issuer is
// back. It is 200 also when none was set.
func (s *server) deleteCertificateIssuer(w http.ResponseWriter, r *http.Request) error {
	cs, err := s.svc.ResetCertificateIssuer(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, cs)
	return nil
}

// listUploadedCertificates answers GET /v1/certificates/uploaded.
func (s *server) listUploadedCertificates(w http.ResponseWriter, r *http.Request) error {
	list, err := s.svc.UploadedCertificates(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, struct {
		Certificates []service.UploadedCertificateView `json:"certificates"`
	}{Certificates: list})
	return nil
}

// certificateUploadRequest is schema CertificateUpload.
type certificateUploadRequest struct {
	Certificate string `json:"certificate"`
	Key         string `json:"key"`
}

// uploadCertificate answers POST /v1/certificates/uploaded: 201 with the certificate (and `replaced`
// when its names were an earlier upload's) and its Location. A refusal is 422 on certificate or key.
func (s *server) uploadCertificate(w http.ResponseWriter, r *http.Request) error {
	var in certificateUploadRequest
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	v, err := s.svc.UploadCertificate(r.Context(), in.Certificate, in.Key)
	if err != nil {
		return certificateInvalid(err)
	}
	w.Header().Set("Location", "/v1/certificates/uploaded/"+url.PathEscape(v.ID))
	writeJSON(w, http.StatusCreated, v)
	return nil
}

// getUploadedCertificate answers GET /v1/certificates/uploaded/{id}.
func (s *server) getUploadedCertificate(w http.ResponseWriter, r *http.Request) error {
	v, err := s.svc.UploadedCertificate(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, v)
	return nil
}

// deleteUploadedCertificate answers DELETE /v1/certificates/uploaded/{id} with 204.
func (s *server) deleteUploadedCertificate(w http.ResponseWriter, r *http.Request) error {
	if err := s.svc.DeleteUploadedCertificate(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
