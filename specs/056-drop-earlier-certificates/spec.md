# Feature 056: Drop the earlier certificates an upload covers, from the console

From CodeAlta task `69pt` (2026-11-24), a follow-up of feature 034 (research R5). Stacked on 034
(`alta/quick-baker-rvch`, with `alta/silver-sonnet-krdg`'s install-test checks).

## Why

Traefik keeps every certificate in its store and renews each one, and an ACME certificate for an exact name beats
an uploaded wildcard (034 research R5). So after an upload, a host that had a certificate from the ACME issuer
keeps presenting it. Settings › Certificates lists such hosts under "Still on their earlier certificate" and tells
the owner to run the installer again, whose `drop_covered_acme_certificates` removes from the store every ACME
certificate whose every name an upload covers. That means SSH and root on the server for something the console
already knows is needed.

Caravana can't do it itself: the store is the `traefik-acme` host volume, which Caravana's Nomad policy denies on
purpose, and the console never gets root.

## User story (P1)

As the owner, after I upload a certificate and Settings › Certificates says some hosts are still on their earlier
certificate, I press **Drop the earlier certificates** and, a few seconds later (Traefik restarts once), those
hosts present the uploaded one. I never open a shell.

Acceptance:

1. With an upload that covers `a.example.com` and an ACME certificate for `a.example.com` in Traefik's store, the
   action removes that ACME certificate from the store, Traefik restarts once, and `a.example.com` presents the
   upload. An ACME certificate with a name no upload covers stays, byte for byte.
2. The page shows the drop's progress (waiting for the server, dropping, restarting Traefik) and its end in one
   sentence: what was dropped, or that nothing was (every certificate still names a host no upload covers), or why
   it failed.
3. Without a server agent that holds Traefik's store (the lab's unmanaged node, an agent older than this feature,
   one that hasn't reported lately) the action is refused with what to do: run the installer on the server.
4. Without any uploaded certificate there is nothing to drop: refused.
5. One drop at a time; a second request while one is open is refused.
6. The same works through the API (an admin's token), and the installer keeps dropping on a rerun with the same
   rule, implemented once.
7. No certificate or key from the store reaches the console, its database, a log line or an answer: only the
   names of what was dropped.

## Out of scope

- Dropping a certificate that also names a host no upload covers (Traefik would order it again: 034 R4's limit).
- Choosing which certificates to drop; the rule is the installer's.
- Any other change to Traefik's store, and any store on a server that isn't the ingress.
- Restarting Traefik in any other circumstance.
