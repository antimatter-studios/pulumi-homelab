- **Package types in the Go provider:** AptPackage, AptPackages, DebPackage, Archive, OfficialInstall,
  GitCheckout and RcloneRemote, ported with the TypeScript's behaviour. Archive checks that
  versionPattern compiles under RE2, and GitCheckout refuses to delete a checkout with local work.
