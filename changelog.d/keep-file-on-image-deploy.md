### Fixed
- **Changing only an image no longer drops an app's Compose file.** On an app made from a Compose
  file, deploying with only a new image (the Deployments tab's Deploy form, CI, or the MCP tool) used to
  throw the kept file away, and Configure then asked you to paste it again. The new image is now
  written into the kept file, with its comments and layout untouched. If the image can't be
  rewritten (it comes from a `${VARIABLE}`, say) or anything else changes, the file is still dropped,
  with the same warning as before.
