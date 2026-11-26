### Fixed
- **A first deploy that failed lost its secret values.** When a new app (or a template) deployed for the first time and
  failed, for example because its image is private and Caravana has no registry credentials for it yet, the values of its
  secrets were thrown away, and "deploy again" was refused because the app only keeps the names. Now a service that had no
  secret values before keeps the values of the failed deploy, so after fixing the cause you can deploy again. A template's
  generated passwords survive the same way. A service that already had values still gets them back when a deploy fails.
