### Changed
- **Image findings are quiet, so apps and outages stay in view.** The needs-you list no longer draws a row for
  every vulnerable package. One **Security** group closes the list, after every other item: "3 apps have fixable
  vulnerabilities: 14 critical packages, 2 known exploited." Open it for one line per app with **Open findings**.
  An app with findings shows a small "■ 9 critical" or "◆ 4 high" under its status in the Apps home, the project
  page and the Operator Overview, and nothing when it has none. Notifications and the API are unchanged.
- **The Apps headline counts outages, not findings, and names apps by name.** An app whose only items are findings
  counts as healthy, and a last sentence says "3 apps have fixable critical vulnerabilities." An app outside Default ›
  production is named by its name, not by its ID (`banana`, not `banana--production--banana`); two apps with one
  name say their environment.
