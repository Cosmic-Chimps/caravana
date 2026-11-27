### Fixed
- **Following an app's logs no longer shows its output again when the follow starts.**
  `GET /v1/apps/{id}/logs?follow=true` sent the copy's whole newest log file a second time, as new lines,
  right after the tail; now only what the copy writes after that arrives. For the same reason, turning
  Live on in Logs and Requests no longer reads each copy's (and Traefik's) newest log file through first.
