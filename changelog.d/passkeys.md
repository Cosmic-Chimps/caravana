### Added
- **Sign in with a passkey.** Settings › Sign-in lists the passkeys you have, adds one (after you
  confirm it's you with your password or a passkey you already have, or by having just signed in),
  renames and removes them. On the sign-in page, "Sign in with a passkey" asks for your fingerprint,
  face or device PIN and signs you in without typing an email. A passkey belongs to the domain it was
  made at, so it works only there: it is offered at the console's https domain and at `localhost`,
  never at an IP address. Caravana keeps only the passkey's public key and a counter.
- **Passkeys only.** Once a passkey exists for the console's domain, you can turn password sign-in
  off. The password form then refuses without checking the password, and the last passkey of a domain
  can't be removed while it is off. If the console's domain changes, the password works again at the
  new domain until you add a passkey there, so a move never locks you out.
- **On the host.** `caravana admin passkeys list` and `caravana admin passkeys remove --id <id>`.
  `caravana admin reset-password` now also turns password sign-in back on (passkeys are kept), and
  `caravana admin status` says how you sign in.
- Failed passkey sign-ins count against the same per-address limit as wrong passwords (5 in 15
  minutes).
- API 0.36.0: `listPasskeys`, `updatePasskey`, `deletePasskey`, `getSignIn` and `putSignIn` under
  `/v1/account/`. Adding a passkey and signing in with one are console routes only: a passkey is
  bound to the console's own origin and to a person's device unlock, which an API client has neither.

### Upgrade
- Nothing to do: a new migration (0026) adds the passkeys table and two columns. Sessions that were
  open before the upgrade have not confirmed anything yet, so adding a passkey from one asks you to
  confirm it's you first.
