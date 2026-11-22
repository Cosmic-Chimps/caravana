### Added
- **Email and Telegram notification channels.** Settings › Notifications can now send what needs you, and how deploys
  end, by email (through any SMTP server, with STARTTLS, TLS or, for a mail server on the same host, no encryption) and to
  a Telegram chat (through a bot you make with BotFather). They have the same events, Send a test, Pause, Edit and
  Remove as the other channels, and the same retries (after 30 seconds, then 2 minutes). The SMTP password and the bot
  token are write-only: they live only in the cluster's secret store and are never shown or logged. The API takes
  them as `email` and `password`, and `telegram` and `token`, on the notification channel calls (API 0.52.0).
