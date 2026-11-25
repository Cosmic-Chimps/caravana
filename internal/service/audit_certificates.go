package service

import (
	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Audit entries for certificates (features 034 and 056, merged after 057): the issuer is part of
// the certificate settings, an upload is a certificate of its own, and a drop is a request with its
// record. Nothing here holds a secret: the issuer's view has the EAB key's ID, never the HMAC key,
// whose leaf is named so that audit.Redacted hides it (it only says that a key was set or changed);
// an upload's view has its names, subject, issuer and dates, never the private key or the PEM.

// issuerView is what the audit log compares for the custom ACME issuer: none when ok is false. A new
// HMAC key in this request (newKey) shows as a changed redacted leaf even when the key ID stays.
func issuerView(i store.ACMEIssuer, ok, newKey bool) map[string]any {
	if !ok {
		return map[string]any{}
	}
	v := map[string]any{"issuer": i.Server, "email": i.Email}
	if i.EABKeyID != "" {
		v["eabKeyId"] = i.EABKeyID
	}
	if i.EABKeySet {
		v["eabHmacSecret"] = "stored"
		if newKey {
			v["eabHmacSecret"] = "new"
		}
	}
	if i.CA != "" {
		v["caCertificate"] = "set"
	}
	return v
}

// certificateResource names an uploaded certificate: its ID, and its subject as the name.
func certificateResource(id, subject string) audit.Resource {
	return namedResource(audit.KindCertificate, id, subject)
}

// uploadView is what the audit log lists for an uploaded certificate.
func uploadView(c store.UploadedCertificate) map[string]any {
	return map[string]any{
		"names": c.Names, "subject": c.Subject, "issuer": c.Issuer, "fingerprint": c.Fingerprint,
		"notAfter": c.NotAfter.UTC().Format("2006-01-02"), "trusted": c.Trusted,
	}
}
