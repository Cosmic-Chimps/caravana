# Contract: 027 page 3, Updates (one host)

Written by the caravana_designer session on 2026-10-29, before the spec. The values come from the
handoff's `UpdatesScreen.jsx` (`UP_ITEMS`, the Available panel, What happens, Update window, Past
runs), `SettingsScreen.jsx`'s `StField` and `theme/caravana.css`, rendered from the prototype
(`design/console/_shots/pg-updates.png`). Where this list and the handoff disagree, this list
wins. Pages 1 and 2 and the 021–025 contracts still apply (the time rule, the No-Dim-Text Rule,
the console never says "ship"). It takes over the backlog item "tell the owner when a new
Caravana version is out".

The case this page exists for: on 2026-10-28 the maintainer learnt from outside the console that
their server ran kernel 6.8.0-147 while 6.8.0-149 (the fix for a kernel CVE) was installed and
waiting for a reboot. This page, the needs-you list and a notification must have said so.

## What page 3 is (the calls the backend follows)

- **It reads; it doesn't apply.** Nothing is installed, upgraded or rebooted from the console in
  this feature. The page says what is waiting and gives the exact command to run on the server.
  The handoff's Run now, Run in next window, the update window and the rolling plan across
  servers aren't built (Phase 3, where applying updates belongs with draining).
- **A newer caravana:** Caravana asks GitHub's releases API for the latest release of
  `Cosmic-Chimps/caravana` once a day with jitter (and at start), with the stored ETag, sending
  nothing but a `User-Agent: caravana/<version>`. A setting kept in SQLite turns it off (below;
  no environment variable). **Check now** runs it on demand, at most once per 10 minutes. A release counts when its version is
  higher than the running one and it isn't a pre-release. Its notes are the release's body
  (which `tools/release.sh notes` writes from CHANGELOG.md), first 6 bullet lines.
- **Nomad and Traefik** ride with caravana's installer pins: "update" means rerunning a newer
  release's installer. When the newer release changes a pin (the research reads the pins from
  the release, e.g. its `install.sh` or a versions file published with it), the release row
  says so in its notes ("Updates Nomad 1.9.4 → 1.10.2 and Traefik 3.7.1 → 3.8.0."). There is no
  separate Nomad row: nothing updates Nomad apart from the installer.
- **The server's OS**, only from files on the host Caravana runs on (no root, no running `apt`
  as root):
  - security and other updates waiting: `/var/lib/update-notifier/updates-available` (mode 644,
    the motd's "N updates can be applied immediately. M of these are standard security
    updates."): the counts, as fresh as the server's last `apt update` (the file's mtime);
  - **package names only on demand** (ruled 2026-10-29): `pro security-status --format json` is
    the one unprivileged source, and it takes 4 s on an idle host and up to 69 s on a starved
    one, so it never runs on a timer. The Ubuntu updates row offers **Check package names**
    (below); the names are kept for an hour. A kernel waiting in `-updates` is not called a
    security fix: only `pro`'s answer says what is security, and once a kernel is installed the
    reboot row covers it;
  - a reboot needed: `/var/run/reboot-required` and `/var/run/reboot-required.pkgs` (the
    packages that asked for it), with the running kernel (Nomad's `kernel.version`, i.e.
    `uname -r`) and the newest installed kernel (the `linux-image-*` in `.pkgs`, or
    `/boot/vmlinuz-*`'s highest version);
  - how the host updates itself: unattended-upgrades on or off and automatic reboot, from
    `/etc/apt/apt.conf.d/20auto-upgrades` and `50unattended-upgrades` if readable;
  - when Ubuntu last checked: the files' mtimes. **Staleness** (ruled 2026-10-29): the counts are
    shown with their age ("checked by Ubuntu 3 h ago"); older than 48 hours they still show, in
    fg-3, with "Ubuntu's list is 3 days old. Run `sudo apt update` on the server to refresh it."
    and no `os_security_updates` item is opened from them (stale counts aren't a fact to alarm
    on); the reboot file has no staleness (it is written by the install itself).
  - A host that isn't Ubuntu or Debian (no such files): the OS rows say "caravana can't read
    this server's pending updates (not Ubuntu or Debian)." with ○, nothing else.
- **No CVE names**: Caravana has no vulnerability feed, so it never names a CVE. It says what is
  true: security updates are waiting, or a fix is installed and the old kernel still runs.
- **Needs-you items** (and so notifications, through the existing problem events):
  - `os_reboot_required` ■: "This server needs a reboot: kernel 6.8.0-149 is installed, but
    6.8.0-147 is still running." (or, for other packages, "…: `libc6` was updated and needs a
    reboot."), action **Open updates**;
  - `os_security_updates` ◆: "14 updates are waiting, 9 of them security updates.", action
    **Open updates**; opened once the count is above zero and, with unattended-upgrades on, only
    after 24 hours (it installs them by itself overnight; an item before then would be noise);
  - resolved when the file says zero / the reboot file is gone (025's resolve rule).
- **A newer caravana is not a needs-you item** (nothing is wrong): it is a notification
  `caravana.release` sent once per release ("caravana 0.14.0 is out. You run 0.13.0.") to every
  channel, and the Updates nav count (page 1).
- **History**: Caravana records its own version at each start (a row when it changes), the
  host's boots (from the boot time, page 2), and package runs from `/var/log/apt/history.log`
  (confirmed readable; page 2's rule: by Ubuntu's automatic updates or by hand, the package
  count, the versions in the notes).

## Where it lives

`/ops/updates`, the Operator sidebar's Platform › **Updates** (page 1), crumb `Updates`. The nav
mark: ■ reboot needed, ◆ security updates waiting, else the count of available updates.

## The page (`/ops/updates`)

`main`: max-width 1440, padding 36px 26px 64px, column gap 28.

### Head

- "Updates" 30px; the sentence 15px fg-2: **"What's waiting for caravana and this server's OS,
  and how to apply it."** (the handoff's "Servers update one at a time so apps keep running" is a
  rolling update caravana doesn't do yet).
- The needs-you items for this page, as 023's problem banners, first (■ reboot before ◆ updates):
  the item's sentence 15px/1.5 fg-1, a second line 14px fg-2 with the consequence ("Until it
  reboots, the server keeps running the old kernel."), and the action **How to apply** (a link
  to `#apply`).

### Available

- `SectionHead` **"Available"** with meta "3 updates" / "Nothing waiting. Checked 3 h ago." when
  empty (the oldest of the two checks' times).
- `.cv-panel` of rows, the handoff's grid `14px 240px 200px minmax(0, 1fr)`, gap 24, padding
  16px 20px, 1px line-1 between rows, align start:
  - a 9px mark (■ reboot needed, ◆ security updates, ○ a caravana release);
  - the name 15px fg-1 and under it 400 12px/1.3 mono fg-3: **caravana** "released Oct 28" ·
    **Ubuntu updates** "checked by Ubuntu 3 h ago" · **Reboot** "since 2026-10-27 06:12 UTC"
    (the reboot file's mtime);
  - the change, 400 13px/1.4 mono fg-1: "0.13.0 → 0.14.0" · "14 packages · 9 security" ·
    "kernel 6.8.0-147 → 6.8.0-149";
  - the notes as a list (14px/1.45 fg-2, 4px gap): the release's bullets and its pin changes;
    for Ubuntu updates, the security package names when checked (comma-separated, mono, at most
    12, "and N more", "checked 20 min ago"), else **Check package names** (secondary sm, a post
    that starts `pro security-status` in the background and answers 303 to the page; while it
    runs the row shows ◐ "Checking which packages… this can take a minute." and the live notice
    redraws it when done; without script, reload; refused with a sentence within 10 minutes of the
    last check); the caravana row's **Check now** (secondary sm) works the same way; for the
    reboot, "Installed by Ubuntu's automatic updates. Rebooting stops every app for about a
    minute; Nomad starts them again when the server is back." (one host: the truth).
  - Sorted ■, ◆, ○.

### How to apply (`id="apply"`)

The handoff's "What happens", for one host: what to run, not a plan the console runs.

- `SectionHead` **"How to apply"** with meta "run these on the server, over SSH".
- `.cv-panel`, rows like the handoff's steps (grid `24px minmax(0, 1fr)`, gap 18, padding
  14px 20px, 1px line-1 between), only the rows that apply, in order:
  1. **OS updates** (when waiting): "Install the waiting updates:" and the command box
     (`.cv-cmd`, select-all, the console's Copy button hidden without script):
     `sudo apt update && sudo apt upgrade`;
  2. **caravana** (when a release is out): "Run the installer of 0.14.0. It upgrades caravana,
     and Nomad and Traefik when their versions change; your apps keep running, and it puts
     0.13.0 back if 0.14.0 fails its health check." and the release's install command as the
     release publishes it;
  3. **Reboot** (when needed, last): "Reboot the server:" `sudo reboot`, and the consequence
     line 13px fg-1 with the warn ◆ (the handoff's step note shape): "Every app stops for about
     a minute. The console comes back when the server does."
  - The step number 400 12px mono fg-3; each step's title 14px fg-2.
- Under the panel, 13px fg-3: "caravana doesn't apply updates from the console yet."

### How this server updates

The handoff's "Update window" panel becomes what the host actually does, with the one setting
caravana owns. `SectionHead` **"How this server updates"** over a `.cv-panel` of `StField` rows
(label and hint in a 220px column, the value beside):

- **Ubuntu's automatic updates**: "On: security updates install by themselves." / "Off" (from
  `20auto-upgrades`), hint "Set on the server, in unattended-upgrades." (read-only);
- **Automatic reboot**: "Off: reboots wait for you." / "On, at 02:00" (from
  `50unattended-upgrades`'s `Automatic-Reboot` and its time), same hint (read-only); either row
  reads "caravana can't read this setting." when the file isn't readable;
- **New caravana versions**: a form with the checkbox **"Check GitHub for new releases once a
  day"** (checked by default) and **Save** (secondary sm), hint "Only the release list is
  fetched; nothing is sent about this server." The last check's time after it, 12px mono fg-3:
  "checked 14 h ago" / "off".

### Past updates

- `SectionHead` **"Past updates"**, then the handoff's rows (grid `14px 80px minmax(0, 1fr)
  minmax(0, 1fr)`, gap 18, min-height 46, padding 8px 20px): mark 8px, the date 400 12px mono
  fg-3 ("Oct 22"), the sentence 14px fg-1 ("caravana 0.12.0 → 0.13.0", "Ubuntu updates · 14
  packages", "The server restarted"), the detail 400 12px mono fg-3 ("no reboot", "kernel
  6.8.0-149 since then"). The last 10. Empty: "Nothing yet." fg-3.

### Errors

- GitHub unreachable: the caravana row is left out and the meta says "Couldn't check GitHub
  (last checked 3 d ago)." in fg-3; no item.
- The OS files unreadable: the OS rows show the sentence above with ○.

## Narrow (390px)

- The banners stack their action under the text (023's rule).
- Available rows fold: mark, name and change on the first lines, the notes under them.
- The command boxes scroll sideways inside themselves (the one place a horizontal scroll is
  right: a command must not wrap).

## May differ (server-rendered, no JavaScript)

- No Run now, no window, no rolling plan, no automatic security switch caravana would own.
- Copy buttons stay hidden without script; the commands are selectable.

## API (names only, for the spec)

`getUpdates` (`GET /v1/updates` → `caravana{current, latest?, releasedAt?, notes[], url?,
pins?{nomad, traefik}, checkedAt?, checkEnabled, checkError?}`, `os{readable, distro,
updates{total, security, packages?[], checkedAt}, reboot{required, since?, packages[],
runningKernel, installedKernel?}, unattended{enabled?, autoReboot?, autoRebootTime?}}`,
`history[]{at, kind, text, detail}`); `putUpdateSettings` (`checkEnabled`); `checkUpdates` (`POST /v1/updates/check?what=release|packages`, 202, at most once per 10 minutes each; `packages` runs `pro security-status`); attention kinds
`os_reboot_required`, `os_security_updates`; notification event `caravana.release`. Times RFC
3339 UTC.

## Review

The designer checks at 127.0.0.1:7070 against `pg-updates.png`:
- a lab VM with a pending kernel (install a newer `linux-image` without rebooting): the ■ banner,
  the needs-you item and its notification, the Reboot row with both kernels, the steps;
- security updates waiting (the motd file), before and after 24 hours with unattended-upgrades
  on; Check package names (the ◐ state, the names after); a stale file (older than 48 h: fg-3,
  the apt update sentence, no item);
- a newer caravana (a fake release served to the check in the lab, or the table test), the
  `caravana.release` notification, the nav count;
- the release check turned off; GitHub unreachable; a non-Ubuntu host (the OS sentence);
- Past updates after a caravana upgrade and a reboot;
- the nav mark on each state; 1440, 640 and 390 widths; without script.
