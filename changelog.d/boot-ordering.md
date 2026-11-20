### Fixed
- **A server with cloud-init boots cleanly again.** Caravana's private network unit no longer forms an
  ordering loop with cloud-init at boot, which made systemd drop a job to break it (on some boots ssh,
  leaving the server unreachable) and left the system degraded. Rerun the installer to get the new unit;
  it takes effect at the next boot.
