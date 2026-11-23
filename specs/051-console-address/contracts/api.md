# API contract — console address (openapi 0.57.0)

All routes need a full API token or a signed-in session; deploy tokens are denied by the default deny (add the
operations to the scope walk's denied list). Errors are `{"error":{"code","message","field"}}`.

## Schemas

```yaml
ConsoleAddress:
  domain: string            # host of the public URL, "" when Caravana has none
  publicUrl: string
  issuer: string            # 038's cluster issuer, "" for none
  sslipHint: string         # "<a-b-c-d>.sslip.io" of this server's IPv4, "" when unknown
  canChange: boolean
  cannotChange: string      # one sentence when canChange is false: no public https URL, Traefik's job predates it
                            # ("run the installer again"), or no agent beside Caravana (switch only, see below)
  agent: boolean            # an agent with runsConsole reported in the last 10 minutes
  oldDomains: [{domain, until: date-time}]
  change: ConsoleAddressChange | null   # the open one, else the newest settled one
  dependents:
    passkeys: integer       # passkeys registered on the current domain
    webhooks: integer       # apps with a webhook token
    deployTokens: integer   # active scoped API tokens
    apiTokens: integer      # active full API tokens
    servers: integer        # joined servers (they follow by themselves)

ConsoleAddressChange:
  id: string                # 32 hex
  domain: string
  oldDomain: string
  redirectDays: integer     # 0, 7, 30 or 90
  status: checking | ready | check_failed | switching | applied | failed | refused | lost | cancelled
  checks: [{name: hostname|dns|route|certificate, state: ok|failed|waiting|skipped, message: string}]
  reason: string            # one line for check_failed, failed, refused, lost
  dnsRecordCreated: boolean
  createdAt, readyAt?, switchAt?, settledAt?, redirectUntil?: date-time
```

## Operations

- `GET /v1/console-address` → 200 `ConsoleAddress` (`getConsoleAddress`).
- `POST /v1/console-address/changes` (`startConsoleAddressChange`) body `{domain, redirectDays?=30,
  createDNSRecord?=false}` →
  - 400 `invalid_field` `domain` (not a host name, an IP, the current domain) or `redirectDays`;
  - 409 `domain_taken` `domain` (an app's);
  - 422 `domain_not_here` `domain`: DNS doesn't point here; the message says what it saw, and when a connected zone
    covers the domain and there is no record, "send createDNSRecord to let Caravana create it in <zone>";
  - 422 `dns_provider_refused` / 502 `dns_provider_unavailable` (048's) when creating the record;
  - 409 `console_address_change_open`; 409 `console_address_unsupported` (with the sentence);
  - 202 `ConsoleAddressChange` (`checking`), `Location: /v1/console-address/changes/{id}`.
- `GET /v1/console-address/changes/{id}` → 200 / 404 `console_address_change_not_found` (`getConsoleAddressChange`).
- `DELETE /v1/console-address/changes/{id}` (`cancelConsoleAddressChange`) → 200 change `cancelled`; 409
  `console_address_not_cancellable` once it is switching or settled.
- `POST /v1/console-address/changes/{id}/switch` (`switchConsoleAddress`) → 202 change `switching`; 409
  `console_address_not_ready` (not `ready`); 409 `no_agent`; 422 `domain_not_here` / `console_address_check_failed`
  `domain` when the fresh check fails (the change stays `ready`, its checks updated).

## Old domains

For a host in `oldDomains`, Caravana's outer handler serves `/v1/…` and `/.well-known/…` and answers anything else with
308 `Location: <publicUrl><path>?<query>`. Any other host is served as today.
