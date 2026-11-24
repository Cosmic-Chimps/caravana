### Added
- **Several backup destinations, and a second copy of every backup.** Settings › Backup storage keeps any number of
  named S3-compatible destinations: add, edit (leave the key empty to keep it), **Test** (lists, writes and removes a
  small object from the servers), **Make default** and Remove (refused for the default and for one a service or
  project uses, naming who). A service's backups go to its policy's `destination`, else its project's default
  (project settings › Backups go to), else the installation's default, and may be copied to up to two others with
  `copyTo` and `copyKeep`, for every engine. The Backups tab has a **Where backups go…** form and says on each backup
  where it is ("in main · copied to offsite", "copying to offsite…", "deleted from main, kept in offsite"). A failed
  copy is its own needs-you item and notification. See `docs/backup-destinations.md`.
- **Restore from a place.** A restore reads the backup from its own destination or from any copy that still has it;
  **Restore from** chooses (Automatic tries them in order), so a bucket you lost doesn't lose the backup. API 0.58.0:
  `listBackupDestinations`, `putBackupDestination`, `deleteBackupDestination`, `testBackupDestination`, the policy's
  `destination`, `copyTo` and `copyKeep`, `Backup.destination` and `copies`, `Restore.destination`, the restore's
  `{from}` body and `Project.backupDestination`.

### Upgrade
- The destination you have becomes `main`, the default, with its key where it was: nothing to do, and every backup
  and restore recorded so far says `main`.
