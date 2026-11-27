# Rules for every Go resource

Every resource in this provider follows these rules. They come from the reviews of #96 to #100
(2026-11-27) and are kept on the tracking issue, in
[#31's rules comment](https://github.com/antimatter-studios/pulumi-homelab/issues/31#issuecomment-7225605455). Every resource pull request says that it was
checked against them.

- **R1 Ownership.** Only Create and Update set what a later Delete may remove: the digest applied,
  the lines, ACL entries and accounts written. Read, refresh and import never set or widen that
  baseline. An imported or adopted object is kept on delete, with a warning.
- **R2 Failures fail.** A failed install, write, stop, swapon or validation returns an error and
  leaves the previous state. Nothing reports success after a failed step, and nothing restarts or
  reloads after a failed write.
- **R3 No fallbacks.** An invalid enum or mode is an error, not a default. A missing verifier
  (testparm, sshd, systemd-analyze) is an error, not a skipped check.
- **R4 Secrets over stdin only.** A secret never appears in argv, errors, diagnostics or logs, and
  tests assert it.
- **R5 Destructive steps through [`safeops`](safeops/safeops.go).** The parent is pinned once, the
  entry captured with rename(2) into a fresh private directory, and verified there. A match is
  removed; anything else is restored with link(2), which never replaces. Anything kept is reported
  by its exact path. `mv` is never used for no-clobber: Debian 13's `--update=none-fail` can fall
  back to a plain rename.
- **Delete never fails on something unexpected.** It keeps it and warns in Pulumi's output, so a
  destroy or replace never gets stuck. Faults that are not findings (no perl, an unreachable
  machine) are still errors.
- **Input maps** that reached the TypeScript provider through protobuf are rendered in sorted key
  order (`jscompat.SortedKeys`), so files on machines are not rewritten at the switch.
- **Ordering-sensitive names** (such as ACL user and group names) outside the supported ASCII set
  are a hard error naming them, not best-effort ordering.
