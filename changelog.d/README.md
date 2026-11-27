# changelog.d

Every pull request that changes what this package does adds one file here instead of editing
CHANGELOG.md, so pull requests made side by side never conflict over a shared Unreleased section.

- **Name:** `<anything>.<kind>.md`, where kind is one of `added`, `changed`, `deprecated`,
  `removed`, `fixed` or `security`; for example `posixacl-key-order.fixed.md`.
- **Content:** one or more bullets, written as they should read in the release notes.

When CI passes on main, the release workflow gathers the fragments added since the last tag, groups
them by kind, and publishes them as that release's notes (`scripts/release.sh notes`). Fragments stay
here afterwards; the tag marks which ones a release has already covered.
