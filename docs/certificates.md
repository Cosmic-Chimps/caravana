# Certificates

Every domain of an app gets an HTTPS certificate. By default it comes from Let's Encrypt, which the
installer sets up (`--acme production`), and Traefik renews it 30 days before it ends. Settings ›
Certificates offers two other ways, for an organization that must use its own certificate authority:

- **Another ACME issuer**: the same automatic certificates, from another issuer.
- **Upload a certificate**: a certificate you bought, or your company's, that you renew yourself.

Both need the installer to have run once since Caravana 034 (Traefik restarts once). Until then the page
says so.

## Another ACME issuer

Choose **Change issuer › Another ACME issuer**, then give:

| Field | |
|---|---|
| ACME directory URL | The issuer's directory. The presets fill it: Google Trust Services `https://dv.acme-v02.api.pki.goog/directory`, ZeroSSL `https://acme.zerossl.com/v2/DV90`, DigiCert `https://acme.digicert.com/v2/acme/directory/`. |
| Contact email | The issuer sends expiry notices there. |
| EAB key ID and HMAC key | The External Account Binding pair from the issuer's console. Google Trust Services, ZeroSSL and DigiCert all need one. Caravana refuses a save without it when the directory says it's required. |
| The issuer's own CA certificate | Only for an issuer whose ACME server has a certificate from a private CA (your own step-ca, for example). |

Caravana reads the directory before saving (it never registers an account: some EAB keys can be used only
once). On save, Traefik restarts once, in place: a few seconds without HTTP, this console included. Every
domain then gets a new certificate from the new issuer as it is asked for. Renewal is automatic, as with
Let's Encrypt.

The HMAC key is kept in the Nomad Variable `caravana/acme`, which only Traefik's job and Caravana read. No
page, API answer or log shows it; leave the field empty when you edit the issuer to keep it.

Each issuer keeps its account and certificates in a file of its own in Traefik's volume. **Let's Encrypt
(what the installer set)** goes back to the installer's issuer, and the certificates it had are served again
while they are valid.

### Google Cloud

Google Certificate Manager certificates can't be used: they work only on Google's load balancers and their
keys can't be exported. Google's public CA, Google Trust Services, has an ACME API: create an EAB key with
`gcloud publicca external-account-keys create`, and give its `keyId` and `b64MacKey`.

## Upload a certificate

Paste the certificate with its intermediate certificates, and its private key, in PEM. Caravana checks:

- the key is the certificate's, RSA of 2048 bits or more or ECDSA, and not encrypted
  (`openssl pkey -in key.pem -out plain.pem` decrypts one);
- the chain is complete: every intermediate up to a root is there (a private CA's root may be included);
- the certificate is valid now and has DNS names.

It answers with the names it covers, who issued it, when it ends, and whether browsers trust it (a private
CA's certificate is accepted and marked "not trusted by browsers").

Traefik serves an uploaded certificate for every name it covers within about ten seconds, with no restart,
and asks the ACME issuer for none of those names. A wildcard `*.example.com` covers `a.example.com`, not
`example.com` nor `a.b.example.com`. The console's own domain is served with an uploaded certificate that
covers it, like any other.

**Nothing renews an uploaded certificate.** Its expiry is on Domains and in Settings, and when it is inside
the warning window (Settings › Certificates › Warn when, 14 days by default) or has ended, the needs-you list
has an item for it and your notification channels get a message. Upload its replacement: a certificate with
exactly the same names replaces the earlier one. Remove a certificate to hand its names back to the ACME
issuer (or Traefik's own certificate without one).

The key is kept in a Nomad Variable under `caravana/certificates/`, which only Traefik's job and Caravana
read, and rendered into a folder of Traefik's task that Nomad's file API can't read. No page, API answer,
log or export shows it. At most 30 certificates can be uploaded.

### When a domain still shows its earlier certificate

Traefik keeps a certificate it already has. If a domain had a Let's Encrypt certificate before you uploaded
one that covers it (or was added in the few seconds before Traefik picked the upload up: upload first, then
add the domains), Traefik goes on serving and renewing the earlier one, and the uploaded certificate's row
says "Still on their earlier certificate". Press **Drop the earlier certificates** under the uploaded
certificates on Settings › Certificates. The agent on the first server removes from Traefik's store every ACME
certificate whose every name an uploaded certificate covers, and Caravana then restarts Traefik once: a few
seconds without HTTPS. The page follows it (waiting for the agent, dropping, restarting) and ends in one
sentence: the names that were dropped, or that every earlier certificate also names a host no upload covers, or
why it failed. Only names travel between the agent and Caravana, never a certificate or a key. It needs the
first server's agent to be up to date and to have reported lately; if it isn't, the page says to run the
installer on that server. The API is `POST /v1/certificates/drops` (an administrator's, 202; read it with
`GET /v1/certificates/drops/{id}`).

If Traefik obtains or renews a certificate in the seconds between the rewrite and its restart, it writes its old
store back and the drop doesn't hold: the row still lists the host after the check that follows, so press the
button again. Running the installer again on the server does the same drop and says which certificates it
dropped.

One case dropping can't fix: a service whose domains are partly covered by the upload. Traefik asks the
ACME issuer for all the names of a route together, so its certificate names the covered one too, and wins.
The uploaded certificate's row names the domain and the other names of its route. Upload a certificate that
covers every domain of the service (a bought wildcard usually also names the apex:
`example.com, *.example.com`).

## Several servers

Traefik runs on the first server only, and so do its certificates. Nothing changes on joined servers.
