# Quickstart: Domains (023)

How to see the feature work end to end. The lab's Traefik has no ACME, so its certificate state
is "Traefik's own"; a real host shows "live" with Let's Encrypt.

## Prerequisites

- The lab up with its Traefik re-registered after the file-provider change (`deploy/dev/lab.sh
  up`), `eval "$(deploy/dev/lab.sh env)"`, Caravana running with
  `CARAVANA_EDGE_ADDRESS=127.0.0.1:8443`, the admin, a full token in `$TOKEN`.
- A busybox app `site` serving on 8080 with the domain `site.localhost` (healthy).

## 1. The page and the states

`GET /v1/domains` lists `site.localhost` with `state: self_signed`, `dns.status: ok` (a
`.localhost` name resolves to loopback), `certificate.status: self`, `issuer: "Traefik's own"`.
On `/domains` the row reads "Traefik's own certificate" (○) with "Goes to `site` :8080 ·
production", and the sidebar has Domains. Stop `site`: the row reads "app stopped"
(`not_served`); start it again.

With the lab's injected resolver (`startLab` maps `elsewhere.test` to `203.0.113.9` and this
host to `203.0.113.1`), add `elsewhere.test` to `site`: within a minute the row reads "DNS
points elsewhere" with "now A · 203.0.113.9 / should be A · 203.0.113.1", a banner names it,
`/apps` has the needs-you item "elsewhere.test points at 203.0.113.9, which isn't this host"
with "Open domain", and a webhook channel receives one `problem.opened` with
`kind: domain_dns_elsewhere` and `addressSeen`. `nothere.invalid` reads "no DNS record".

## 2. Options

Open `site.localhost`'s row. Force HTTPS is on and locked ("On for every domain."). Turn **Team
only** on, Save and deploy: the answer shows the password once (user `team`); then

```bash
curl -sk --resolve site.localhost:8443:127.0.0.1 https://site.localhost:8443/        # 401
curl -sk -u team:$PW --resolve site.localhost:8443:127.0.0.1 https://site.localhost:8443/   # 200
```

The job, the export (`GET /v1/apps/site/export`), Traefik's dashboard and every log hold only
`site-web-<hash>-auth@file`, never the hash or the password; `nomad var get
nomad/jobs/traefik` (the lab's management token) shows the hash. **New password**: the old one
gets 401 at once, the new one 200.

Add `www.site.localhost` with "Redirect to `site.localhost`": `curl -skI --resolve
www.site.localhost:8443:127.0.0.1 https://www.site.localhost:8443/x` answers a permanent
redirect (308 in Traefik 3.7; the test accepts 301 too) with `Location:
https://site.localhost/x`; the `www.` row reads "redirects to site.localhost".

Set the path `/api` on `site.localhost`: `/api/x` reaches the app with the path kept,
`/other` answers Traefik's 404.

Remove `elsewhere.test`: the item resolves after the resolve delay.

## 3. Add a domain

`/domains#add`: type `new.localhost`, pick `site`; the record box reads "A · @ or `new` · <this
host's IP>" (or "this host's public IP address" in the lab without `CARAVANA_PUBLIC_URL`);
**Check now** shows the DNS line ● "points at caravana" and the certificate line ○ "issued
after you add it". Add domain and deploy: the row appears. Adding `site.localhost` to another
app is refused naming `site`.

## 4. Settings › Certificates

`/settings/certificates`: the table lists every domain with issuer "Traefik's own"; the rows
Issuer "none" / Contact email read-only with "Set when caravana was installed; run the installer
again to change it."; Edit → Warn when 7 d → Save; `GET /v1/settings/certificates` answers
`warnBeforeDays: 7`. A unit test with a fake leaf 5 days from expiry gives `cert_expiring`, the
banner, the ◆ nav mark and the `domain_cert_expiring` item.

## 5. A real host (the gate, `host.sh prove`)

`site.<host>` shows `live`, issuer "Let's Encrypt", `notAfter` about 90 days out, `renewsAt` 30
days before; Team only, the redirect and the path behave as in the lab; the installer's rerun
restarted Traefik once and wrote `CARAVANA_ACME=production` and the email into Caravana's env,
which the Settings page shows.

## 6. The lab steps

- `TestConsoleLab` step `console-domains-states`: §1 (the injected resolver), §2 through the
  forms (the shown-once panel, curl 401/200, New password, the redirect's Location, the path),
  §3 with Check now, §4, and no password or hash on any page, answer, export, job or log.
- The gate step `domains`: §1–§3 through the API with the gate's token, a deploy token refused
  on the seven routes, `teamPasswords` on the deploy answer once and absent from every later
  answer, `listCertificates`.

## Checks before calling it done

The CI list, `go test -race` on ui/service/store/edge, `TestConsoleLab`, the installer on a
throwaway VM (`deploy/dev/install-test.sh`: the Traefik job change and the env lines), and
`host.sh prove` once before the release.
