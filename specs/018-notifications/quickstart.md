# Quickstart: validate notifications

Shapes and copy: [contracts/api.md](./contracts/api.md), [contracts/console.md](./contracts/console.md).

## Automated

```bash
go test ./internal/notify ./internal/store ./internal/service ./internal/api ./internal/ui
eval "$(deploy/dev/lab.sh env)"
CARAVANA_LAB=1 go test ./cmd/caravana -run TestConsoleLab -v   # step console-notifications
```

## By hand with ntfy (SC-001, SC-003)

1. Install the ntfy app on your phone, and subscribe to a hard-to-guess topic on ntfy.sh, or on
   your own ntfy.
2. Settings › Notifications: add an ntfy channel with `https://ntfy.sh/<topic>`, then "Send a
   test". Expected: the phone buzzes within seconds, and the row shows "sent".
3. Deploy an app with a health check path that 404s. Expected: a "failed deploy" message and a
   "needs you" problem message, each once.
4. Restart Caravana (`sudo systemctl restart caravana`). Expected: nothing more arrives.
5. Fix the health check and deploy. Expected: a "deployed" message (if "Successful deploys" is
   on), then about 5 minutes later the problem's "resolved" message.
6. Add a Slack or Discord incoming webhook the same way, and send a test.

## Webhook receivers

Check the signature: HMAC-SHA256 with the signing secret over `"<t>.<body>"`, compared with `v1`.
Reject a `t` older than 5 minutes. `internal/notify.Verify` is the reference implementation.
