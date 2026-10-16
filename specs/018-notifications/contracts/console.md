# Contract: Console

Plain HTML forms; nothing needs JavaScript. Copy is exact.

## Settings › Notifications (new section, after Registries, `id="notifications"`)

Lead: "Caravana tells you when something needs you and when a deploy ends, even when nobody has
this page open."

When there are none: "No channels yet. Add ntfy, Slack, Discord or a webhook to hear about
problems."

### List (one row per channel)

| Column | Shows |
| --- | --- |
| Channel | name, then the kind muted: "ntfy", "Slack", "Discord", "Webhook" |
| Sends | "Problems, failed deploys" (whichever are on) |
| Address | the shown address, mono, muted |
| Last delivery | "sent 2 minutes ago" with the healthy mark; "failed 2 minutes ago" with the failed mark and the error muted; "nothing sent yet" |
| Actions | "Send a test" (secondary form); "Edit" (`<details>`); "Pause" or "Resume" (secondary form); "Remove" (`<details>` with "Messages stop at once. Deliveries already listed are removed with it." and a danger "Remove") |

Each row also has a `<details>` "Recent deliveries" listing the last 20: time, the event title,
"sent" or "failed (3 attempts): <error>".

A paused channel shows "paused" after its name.

### Add form

- **Kind**: select with "ntfy", "Slack", "Discord", "Webhook".
- **Name**: text, placeholder "phone".
- **URL**: text. Hint: "ntfy: the topic URL, like https://ntfy.sh/my-topic. Slack and Discord:
  the incoming webhook URL. Webhook: where Caravana posts signed JSON."
- **ntfy access token**: password, optional. Hint: "Only for a protected topic."
- **Send**: checkboxes, "Problems (opened and resolved)" checked, "Failed deploys" checked,
  "Successful deploys" unchecked.
- Button: "Add channel".

After adding a webhook channel, a notice shows the secret once:
"Signing secret for <name>: <secret>. Copy it now: it isn't shown again. Receivers check each
message's Caravana-Signature header with it."

### Notices

- Test sent: "<name> accepted the test message."
- Test failed: "<name> didn't accept the test message: <error>."
- Saved: "Saved."
- Field errors appear at their inputs.

### Edit (`<details>`)

Name, URL (leave empty to keep it for Slack and Discord, since it's the secret), token (empty
keeps it), the event checkboxes, and "Save". A webhook also has "New signing secret" (secondary,
inside its own `<details>` saying "The old secret stops working at once.").
