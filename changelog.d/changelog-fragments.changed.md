- **Changelog entries are files in `changelog.d/`**, one per change, named by kind, instead of
  edits to CHANGELOG.md's Unreleased section, so pull requests made in parallel no longer conflict
  over it. Release notes gather the fragments added since the last tag. A tag pushed by hand
  (`v*`) now also builds and publishes its release.
