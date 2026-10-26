# Feature Specification: Logs

**Feature Branch**: `024-logs` (stacked on `023-domains`)

**Created**: 2026-10-24

**Status**: Draft

**Input**:
- **The maintainer, 2026-10-23:** "after you finish continue with other page": 024 follows 023
  with no pause.
- **docs/plan.md, row 024 (Phase 2.6, the handoff's `LogsScreen`, Developer mode):** logs across
  an environment, newest first, with levels read from the line, a search box, a time range, a
  histogram with deploy markers, repeated lines folded, "only this service", "lines around
  this", Download, and deploys as rows in the stream; backed by Nomad's log API as today,
  retention stays Nomad's, and sending them somewhere longer is a Settings connection for later.
- **The designer's contract:** `contracts/logs-screen.md` (2026-10-24), with the research's
  three rulings (`research.md`, decided by the designer the same day): an unmatched line is
  info on either stream, "lines around this" is addressed by a line id, an estimated time is
  drawn with a leading "~" and Download is oldest first.

## How this feature is built

As for 022 and 023: the designer's contract and the research, then the pure line reading
(ANSI, time, level, folding) and the reads in the service layer and the API, then the page on
real data, the designer's review, the critique pass, rulings and the last look.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - The output of an environment, newest first (Priority: P1)

The owner opens Logs and reads the output of every copy of every service in one project ›
environment, newest first, for the last 15 minutes (or an hour, a day, a week). Each line has
its time, its level (error, warn, info, debug), the service and copy it came from, and its text
with terminal color codes removed. Error lines are tinted; consecutive repeats of a line from
one copy fold into one row with a count. A rail lists the services, grouped by app, each with
its copy count and its own hue, and "all services" first. A histogram over the range shows how
many lines came in each slice and how many were errors. The page reads what Nomad still keeps:
nothing is stored by caravana, and the footer says so.

**Why this priority**: when something is wrong, the first question is "what did it print?";
today the console shows one app's raw tail at a time with no levels, no time range and no way
to see two services together.

**Independent Test**: in the lab, two apps whose services print lines with `ERROR`, `[warn]`, a
JSON line with `"level":"error"`, color-coded lines and a repeated line: the page lists them
newest first with the right level and time, the colors gone, the repeat folded with its count,
the rail with both apps' services and copy counts, and the histogram with the error share.

**Acceptance Scenarios**:

1. **Given** two apps with running copies in an environment, **When** the owner opens Logs,
   **Then** every copy's recent lines appear in one stream, newest first, each with its time
   (UTC), level, service, copy and text.
2. **Given** a line that starts with a time (`2026-10-25T00:26:49Z ERR …`, `2026/10/25 00:16:42
   …`, Postgres's `… UTC [1] LOG: …`), **Then** that time is the line's time and the level is
   read from the words after it, not from the time.
3. **Given** a JSON line with a `level`, `lvl` or `severity` field, **Then** its level comes from
   that field and its text from `msg` or `message`, and the open row offers the raw line.
4. **Given** a line with color codes, **Then** the page shows the text without them and reads the
   time and the level from the stripped text.
5. **Given** a line with no level word on either stream, **Then** it is info; a continuation
   line (a stack trace's second line) takes the level of the line above it.
6. **Given** a line with no time of its own, **Then** it takes the time of the older line before
   it in the same stream, drawn with a leading "~" and a title saying it is estimated; an old
   line is never shown as "now".
7. **Given** ten consecutive equal lines from one copy, **Then** they are one row with "×10" at
   the newest line's time; the same text from another copy is its own row.
8. **Given** the range and the lines read, **Then** the histogram's slices show the counts and
   the error share, and slices older than what was read are drawn as "not read", never as zero.
9. **Given** no lines in the range, **Then** the stream says "No output yet. New lines appear
   here." (no filter) or "No lines match. Try a wider time range or turn on more levels."

---

### User Story 2 - Narrow it down (Priority: P1)

The owner narrows the stream: a project and an environment (only when there is more than one),
one service (from the rail, or "Only this service" on a row), one copy, a search word (a
request id, a status code, any text), a time range, and the levels to show (error, warn and
info on by default, debug off; error and warn carry their counts). Opening a row shows the whole
text, the stream it came from, "Lines around this", "Only this service", the row's link, the
raw line for JSON, and for an error row how many times its text was seen in the range and when
first. "Lines around this" shows 50 lines before and after that line, from that copy only,
every level, with the line highlighted and a way back. Paging back ("Show older") reaches what
Nomad still has; the page says when older lines were dropped.

**Why this priority**: the stream is useful only once it can be cut to one service, one request
or one minute; "lines around this" is how a stack trace is read in context.

**Independent Test**: with the lab's two apps, each filter cuts the stream to exactly the lines
it names, the counts on the level chips match the lines, and "Lines around this" on an error
line shows that copy's neighbours with the line highlighted.

**Acceptance Scenarios**:

1. **Given** the environment has several apps, **When** a service is chosen in the rail,
   **Then** only its copies' lines are shown, its copies are listed under it, and the histogram
   and the counts follow the filter.
2. **Given** a search word, **Then** only lines whose text contains it (case-insensitive) are
   shown, with "N lines" beside the field.
3. **Given** debug is turned on or error off, **Then** the chips' states and the stream change
   together, and the filters stay in the page's address so a reload or a shared link shows the
   same.
4. **Given** a row is opened, **Then** its text wraps in full, the stream (stdout or stderr) is
   named, and the actions above are offered.
5. **Given** "Lines around this" on a line, **Then** the page shows 50 lines before and 50 after
   it from that copy, every level, the line highlighted, with "Around 14:03:39 UTC in copy
   `9a4d` of `supplier-gw`. Every level, this copy only." and "Back to the stream".
6. **Given** the read reached the oldest line Nomad keeps for a copy, **Then** the stream says
   "Older lines were dropped."; **Given** more exists, **Then** "Show older" reads further back.
7. **Given** a copy that has ended but whose lines Nomad still keeps, **Then** its lines stay in
   the stream and its rail entry reads "(gone)".

---

### User Story 3 - Deploys in the stream and live follow (Priority: P2)

Deploys of the environment's apps appear in the stream as rows at their start and end, with
the words the deployment history uses and a link to the entry, and as markers on the histogram.
With Live on, new lines arrive as they are written and the newest are briefly tinted; Paused
shows how many lines arrived since. Without JavaScript the page is a snapshot that says so.

**Why this priority**: the first question after a deploy is "what changed in the output?"; a
deploy row puts the before and after on one screen.

**Independent Test**: deploy an app while the page is open: a "started rolling out" row and a
marker appear at that time, then "is live" when it finishes (or "failed its health checks ·
rolled back" when it doesn't); Live shows a new line within seconds.

**Acceptance Scenarios**:

1. **Given** a deployment that started or ended within the range, **Then** a deploy row is
   drawn at that time ("`supplier-gw` v24 started rolling out · pushed to `main` by ana ·
   `4e0a77`", "`supplier-gw` v24 is live · rollout finished", "v24 failed its health checks ·
   rolled back to v23"), linking to the deployment's entry, and the histogram has a marker.
2. **Given** Live is on and a copy writes a line, **Then** it appears at the top within a few
   seconds, briefly tinted; **Given** Paused, **Then** "paused · N new lines since" with "Show".
3. **Given** no JavaScript, **Then** the page says "Following needs JavaScript. Reload the page
   to see newer lines." and every filter still works as a form.

---

### User Story 4 - Take the lines with you (Priority: P2)

The owner downloads the current view as a text file, oldest first, one line per row with the
time in UTC, the service, the copy, the level and the text, repeats unfolded and deploy rows as
comment lines, under a name that says the project, the environment and the range. The same
read is available through the API, with the level and the time read the same way the console
reads them, so a client can match the page.

**Why this priority**: a support ticket or a teammate needs the lines as a file; the API is
constitution V.

**Independent Test**: Download with a search and a service filter gives a file whose lines are
exactly the page's rows, oldest first, with a first line saying so; the API answers the same
lines with the same levels.

**Acceptance Scenarios**:

1. **Given** the page's filter, **When** Download is pressed, **Then** a text file named
   `caravana-logs-<project>-<environment>-<from>-<to>.txt` holds the matching lines oldest first,
   its first line `# newest last`, each line `<RFC 3339 UTC time> <service> <copy> <level> <text>`,
   folded rows written out, deploy rows as `# deploy …`, within a size limit the file names when
   it was reached.
2. **Given** an API client, **When** it reads the environment's logs with the same filters,
   **Then** it gets the same lines, newest first, each with its level, time (and whether it was
   estimated), service, copy, stream, text, raw line for JSON and repeat count, plus the deploy
   rows, the histogram and the copies; the app's own log read keeps its shape and gains the
   level and the repeat count.
3. **Given** a deploy token, **Then** every log read is refused.

---

### User Story 5 - The app's Logs tab and the sidebar (Priority: P3)

The app's Logs tab becomes the same page filtered to the app (its services in the rail, its
deploys as rows) with "All logs" linking to the environment's page, and the sidebar's Workspace
group gains Logs after Domains.

**Why this priority**: one page, one set of words; the tab is the entry most owners already use.

**Independent Test**: the app's Logs tab shows the app's lines with the new columns and the
rail, and "All logs" opens `/logs` for the app's project and environment.

**Acceptance Scenarios**:

1. **Given** an app, **When** its Logs tab is opened, **Then** it is the Logs page filtered to
   that app, with "All logs" in its head.
2. **Given** the sidebar, **Then** Logs is listed after Domains and is active on `/logs`.

---

### Edge Cases

- Nomad can't be reached: the stream shows "Nomad can't be reached right now, so logs can't be
  read." and the histogram its empty bars; nothing else on the page breaks.
- A copy's log file rotated between two reads: the page reads what the new file has and says
  older lines were dropped if the oldest line read isn't the oldest Nomad keeps.
- A line longer than a screen: ellipsized in the row, wrapped in full when opened.
- A line with a time far outside the copy's life (a clock in the past, a wrong zone): the time
  isn't trusted and the line takes an estimated time.
- A container writing in a non-UTC zone without naming it: read as UTC (documented).
- A copy with more output than the read budget: the newest budget's worth is read, "Show older"
  reads further, and the histogram's older slices are "not read".
- Thousands of copies' worth of lines in 7 days: the read budget bounds the page; the histogram
  covers what was read and says from when.
- A search word that matches nothing: the empty sentence with the filter named.
- "Lines around this" on a line whose copy or file Nomad no longer has: "This copy's lines are
  gone." with "Back to the stream".
- The environment has one app and one service: the rail still shows it (no chooser is hidden
  but the project and environment bars).
- A deploy of an app in another environment: not a row here.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The console MUST have a Logs page for one project › environment listing the
  output of every running copy of every service there, newest first, each line with its time
  (UTC), level, service, copy, stream and text; the project and environment choosers are drawn
  only when there is more than one of each.
- **FR-002**: Every read MUST go to Nomad's log API at the time of the view; caravana MUST NOT
  store log lines. The page MUST say that lines are kept as long as Nomad keeps them and that
  sending them elsewhere is a later Settings connection.
- **FR-003**: Terminal control sequences (colors, cursor moves, OSC, DCS and C0/C1 controls
  but tab; the text after a mid-line carriage return) MUST be removed from a line before anything
  else is read from it, by one pure function.
- **FR-004**: A line's time MUST be read from fixed places (the start, after a short prefix such
  as a pid or a bracket, the access-log bracket, `time=`/`ts=` pairs, JSON time keys), a time
  without a zone read as UTC, and a time outside the copy's life not trusted; a line with no time
  MUST take the time of the older line before it in the same stream, be marked estimated, and
  be drawn with a leading "~" and an explanation; an old line MUST never be shown as "now".
- **FR-005**: A line's level MUST be read after the time is removed: a JSON line's `level`,
  `lvl` or `severity` (error, err, fatal, panic → error; warn, warning → warn; info → info;
  debug, trace → debug) with `msg`/`message` as the text and the raw line kept; else the first
  few words (`ERROR`, `[warn]`, `WARN:`, `level=info`, glog's `E0124`, klog, Rails and .NET
  prefixes); a continuation line takes the level of the line above; an unmatched line is info
  on either stream. The rule MUST be one pure function with a table test, and documented in
  the API.
- **FR-006**: Consecutive lines from one copy with the same text (the time span removed, wherever
  it was) MUST fold into one row with "×N" at the newest line's time, per read.
- **FR-007**: The filters MUST be project, environment, app, service, copy, a case-insensitive
  substring search, a range of 15 min, 1 h, 24 h or 7 d, and level toggles (error, warn, info on
  by default, debug off, with counts on error and warn); all of them MUST be in the page's
  address and work as a plain form without JavaScript; the service and copy filters MUST be
  applied before reading so the chosen one gets the whole budget.
- **FR-008**: The rail MUST list "all services", then each app's services (grouped under the
  app's name when the environment has more than one app) with the service's copy count and a
  stable identity hue from the handoff's six low-chroma hues; a chosen service lists its copies;
  a copy that ended but whose lines Nomad still keeps MUST be listed as "(gone)".
- **FR-009**: Opening a row MUST show the full text, name its stream, and offer "Lines around
  this", "Only `<service>`", the row's link (selectable text without JavaScript), "Raw" for a JSON
  line, and on an error row "First seen <time>, N times since." when the text repeats in the
  range.
- **FR-010**: "Lines around this" MUST show 50 lines before and 50 after the line, from that copy
  only, every level, the line highlighted with a stable anchor, with the notice sentence and
  "Back to the stream"; a line MUST be addressed by a stable id of its stream, file and byte
  offset, not by its time.
- **FR-011**: The histogram MUST divide the range into 30 equal slices, drawn from the same read
  as the stream, with each slice's count and its error share, deploy markers at their time, the
  range's start, "now" and the deploy's name as labels, and slices older than the oldest line read
  drawn as not read, never as zero; it MUST be drawn without JavaScript and without inline styles.
- **FR-012**: Deployments of the environment's apps whose start or end falls in the range MUST
  be rows in the stream at those times, with the deployment history's words and who-words and a
  link to the entry, and markers on the histogram.
- **FR-013**: The read MUST be bounded: 1 MiB per stream, 16 MiB per page, at most two reads at
  once; "Older lines were dropped." MUST be said when the oldest line read is the oldest Nomad
  keeps, and "Show older" MUST read further back when more exists.
- **FR-014**: With Live on, new lines MUST arrive through the console's live updates while the
  page is open, briefly tinted; Paused MUST say how many arrived since; without JavaScript the
  page MUST say following needs it.
- **FR-015**: Download MUST deliver the current filter as `text/plain`, oldest first, first line
  `# newest last`, one line per row as `<RFC 3339 UTC time> <service> <copy> <level> <text>`,
  folded rows written out, deploy rows as `# deploy …`, named
  `caravana-logs-<project>-<environment>-<from>-<to>.txt`, within a size budget named in the file
  when reached, and never cached.
- **FR-016**: The API (0.26.0) MUST offer the environment read with the same filters answering
  lines newest first (id, time, whether estimated, app, service, copy, stream, level, text, raw
  for JSON, repeat), the deploy rows, the histogram (with from when it was read) and the copies;
  the lines around a line; the download; and the app's log read MUST keep its shape and gain
  the level, the id, whether the time is estimated and the stripped text. Deploy tokens MUST be
  refused. The level and stripping rules MUST be documented in the API.
- **FR-017**: When Nomad can't be reached the page MUST say so in the stream, draw the empty
  histogram, and otherwise work; a copy or file that is gone MUST be said in words, never an
  error page.
- **FR-018**: The app's Logs tab MUST be the same page filtered to the app with "All logs"
  linking to the environment's page; the sidebar MUST list Logs after Domains.
- **FR-019**: No log line, nor any secret a line may contain, MUST be written to caravana's
  store, its log or any notice; the page and the API answer them only to a signed-in owner or a
  full API token.

### Key Entities

- **Line**: one line of a copy's output: its id (stream, file, byte offset), time and whether
  it was estimated, app, service, copy, stream, level, text, the raw JSON when the line was
  JSON, and the repeat count when folded. Read, never stored.
- **Read**: one view's bounded read over the environment's copies: the range, the filters, the
  lines, from when the oldest line was read, whether older lines were dropped.
- **Histogram**: 30 slices of the range, each with a count and an error count, and whether it
  was read.
- **Deploy row**: a deployment's start or end in the range, with its words and its entry.
- **Copy**: a running or recently ended copy of a service, with its short id and whether it is
  gone.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: The page for an environment with 20 copies draws within 2 seconds for the 15-minute
  range on the lab's VM.
- **SC-002**: In the lab test, every one of the sample lines (a leading `ERROR`, `[warn]`, a JSON
  error, a color-coded line, a Postgres `LOG:` line, a Go `log` line with no level) gets the level
  and time the rule table says, and the repeated line folds to one row with its count.
- **SC-003**: Every filter is a link or a form that works without JavaScript, and reloading the
  page's address shows the same view.
- **SC-004**: A deploy made while the page is open appears as a row and a marker within one
  refresh after it starts and after it ends.
- **SC-005**: The Download file's lines are exactly the page's rows for the same filter, oldest
  first, and the API's answer carries the same lines and levels.
- **SC-006**: A search of the store, the server log and every page for a sentinel a copy prints
  finds it only in the Logs page, the download and the API answer for that copy.
- **SC-007**: The designer approves the page against the contract after the review cycle.

## Assumptions

- Nomad's log retention is what it is today (its default of 10 files of 10 MiB per stream per
  task); caravana reads what is there and says when it has read to the end.
- Ended copies stay readable in Nomad for a while after they end (observed in the lab); how long
  is Nomad's call, and the page handles a gone copy in words.
- The identity hue per service is picked by a stable hash of the app and service names from the
  handoff's six hues; it is the console's one color by identity, recorded in DESIGN.md.
- The histogram is the console's first inline SVG, by attributes, recorded in DESIGN.md.
- No regular expressions in search, no rendering of colors, no log store or counts buffer
  (the research shows the live read is fast enough), no sending logs elsewhere and no Operator
  sources (Phase 3).
