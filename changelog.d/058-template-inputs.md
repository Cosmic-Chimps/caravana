### Added
- **Templates can ask a choice.** An app template may now have a *choice* between a few answers (shown as radios, or a
  select for more than four), inputs that are optional, up to 20 inputs (it was 12), and inputs, Compose lines and
  deploy settings that only one answer needs. The page of a template groups those under "Only for GitHub" and the like,
  hides the groups another choice doesn't ask for (with script; without it every input is shown and only the asked ones
  are read), and its Preview lists the secrets and the Compose file the answers make. What is typed for an input a choice
  doesn't ask for, a password included, is ignored and kept nowhere.
- **Codemagic Patch is one template again:** pick GitHub, Bitbucket Cloud or GitLab at install (in the template catalog).
- `caravana template check` renders every combination of a template's choices, with its optional inputs empty and filled.
- API 0.65.0: the template's inputs have `required`, `choices` and `when`, the type `choice`; its services' list items may
  have `when`.

### Upgrade
- Older Caravana versions keep reading the catalog's `catalog.json`, which stays in the format they know, so their
  template list keeps updating; templates that need the new format are offered to this version and newer ones, and a
  template made for a newer version than yours is listed as unavailable instead of stopping the catalog.
