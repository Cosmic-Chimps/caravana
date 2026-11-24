### Added
- **Another ACME issuer.** Settings › Certificates can now get every domain's certificate from an ACME issuer other
  than Let's Encrypt: Google Trust Services, ZeroSSL, DigiCert (presets fill their address) or any ACME server, with
  the External Account Binding key ID and HMAC key those issuers give you. Saving restarts Traefik once; renewal stays
  automatic. The HMAC key is kept in Nomad's variables and never shown again. Going back to Let's Encrypt finds its
  certificates where they were.
- **Upload a certificate.** A certificate you bought, or your company's, with its intermediates and its private key: a
  single name, several, or a wildcard. Caravana checks that the key matches, the chain is complete, the certificate
  isn't expired and which names it covers, then Traefik serves it for those names at once, with no restart, and asks the
  ACME issuer for none of them. Nothing renews an uploaded certificate: its expiry shows on Domains and in Settings, and
  a needs-you item and a notification come before it ends. Uploading one with the same names replaces it; removing it
  hands its names back to the ACME issuer. The console's own domain can use one too. See `docs/certificates.md`.
- **Domains say who issued each certificate**: Let's Encrypt, the other issuer, or "uploaded: <subject>".
- API 0.61.0: `putCertificateIssuer`, `deleteCertificateIssuer`, `listUploadedCertificates`, `uploadCertificate`,
  `getUploadedCertificate`, `deleteUploadedCertificate`, the issuer on `getCertificateSettings`, the uploaded certificate
  on a domain's certificate, the needs-you kind `certificate_expiring`, and the codes `issuer_unavailable`,
  `certificates_unavailable`, `too_many_certificates` and `certificate_not_found`. Admins only; deploy tokens are refused.

### Upgrade
- Rerun the installer once on an existing host: Traefik's static configuration is now a file its job renders, so
  Traefik restarts once (a few seconds without HTTP). Until then Settings › Certificates asks for the rerun. A later run
  also drops the certificates Let's Encrypt issued for names an uploaded certificate covers, since Traefik would keep
  serving those.
