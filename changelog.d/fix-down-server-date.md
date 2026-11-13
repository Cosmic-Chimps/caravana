### Fixed
- **A down server's times on Operator › Overview** no longer add a date to a time from today. The
  page compared the UTC time with the date in the server's local time zone, so in the hour after
  local midnight east of UTC it said "Nov 12, 23:16 UTC" for a time from the same UTC day.
