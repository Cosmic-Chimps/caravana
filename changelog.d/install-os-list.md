### Added
- **Caravana installs on Ubuntu 26.04 LTS, Debian 12 and Debian 13**, besides Ubuntu 24.04 LTS, on amd64 and
  arm64: the same installer and the same packages, for the first server and for one that joins. Any other
  system is refused before anything changes, naming the four supported ones. Each of them is tested by
  `deploy/dev/install-test.sh` (`IMAGE=debian-12`); `deploy/dev/install-test-all.sh` runs all four.

### Changed
- **Updates on a Debian server**: the Operator › Updates page counts the waiting updates with
  `apt list --upgradable` (read inside Caravana's sandbox, never applied) on a host that has no
  `updates-available` file, and names the server's system in its words ("Debian updates"). Package names on
  demand still come from Ubuntu's `pro`, so a Debian server says it can't list them.
