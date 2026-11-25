-- Feature 034: bring your own certificates. The ACME issuer becomes a setting
-- (address, contact, the EAB key ID, the issuer's own CA certificate: all
-- public; the EAB HMAC key lives only in the Nomad Variable caravana/acme), a
-- domain's check remembers the fingerprint of the leaf it saw, and uploaded
-- certificates are listed here by what is public about them. The chain and the
-- private key are only in the Nomad Variable caravana/certificates/<id>.

-- Empty acme_server means the installer's issuer.
ALTER TABLE certificate_settings ADD COLUMN acme_server     TEXT NOT NULL DEFAULT '';
ALTER TABLE certificate_settings ADD COLUMN acme_email      TEXT NOT NULL DEFAULT '';
ALTER TABLE certificate_settings ADD COLUMN acme_eab_kid    TEXT NOT NULL DEFAULT '';
ALTER TABLE certificate_settings ADD COLUMN acme_eab_set    INTEGER NOT NULL DEFAULT 0;  -- a key is stored in the variable
ALTER TABLE certificate_settings ADD COLUMN acme_ca         TEXT NOT NULL DEFAULT '';    -- PEM, public
ALTER TABLE certificate_settings ADD COLUMN acme_updated_at TEXT;

-- SHA-256 of the presented leaf, lowercase hex; '' when there was none.
ALTER TABLE domain_checks ADD COLUMN cert_fingerprint TEXT NOT NULL DEFAULT '';

CREATE TABLE uploaded_certificates (
  id          TEXT PRIMARY KEY,                     -- crt_ and 12 lowercase hex
  names       TEXT NOT NULL,                        -- JSON array of DNS names, lower case, sorted
  subject     TEXT NOT NULL,
  issuer      TEXT NOT NULL,
  not_before  TEXT NOT NULL,                        -- RFC 3339, UTC, nine fraction digits
  not_after   TEXT NOT NULL,
  fingerprint TEXT NOT NULL,                        -- SHA-256 of the leaf, lowercase hex
  key_type    TEXT NOT NULL,                        -- "RSA 2048", "ECDSA P-256"
  trusted     INTEGER NOT NULL,                     -- chains to a root this server trusts
  uploaded_at TEXT NOT NULL
) STRICT;
