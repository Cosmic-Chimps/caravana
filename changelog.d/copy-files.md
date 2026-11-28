### Added
- **Browse and download the files inside a running copy.** Each copy on the Running tab has a **Files** link next
  to Terminal. It lists the folders Nomad keeps for the copy (`alloc` with `data`, `logs` and `tmp`, and one
  folder per task with `local` and `tmp`, where a service's own files are), shows a text file up to 512 KB on the
  page and downloads any file. The container's own files and its volumes are not in that directory: the page says
  so and points to the Terminal. A task's `secrets` and `private` folders are never listed or read, and links are
  never followed (Nomad itself follows a relative link into `secrets`). Viewing or
  downloading a file is recorded in the audit log (`read a file`, with the copy, the path and whether it was a view or a download, never the content; a listing isn't, nor the page of a file too large to show). It
  needs the maintain role, like Run a command. The API has `listCopyFiles` and `downloadCopyFile` (0.69.0). It
  needs no policy change: the policy has had `read-fs` since volume backups.
