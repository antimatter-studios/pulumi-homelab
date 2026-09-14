# Changelog

Every release is recorded here, newest first, and **this file is the single source of truth for
release notes**. `.githooks/pre-push.d/git-changelog.sh notes v0.1.0` prints a version's section, so
a release body cannot drift from the changelog — and a version tag whose section is missing is
refused at push time rather than discovered afterwards. Headings are `## vX.Y.Z` because that is
what the extractor reads.

Versions follow [semantic versioning](https://semver.org/). Until 1.0.0 the minor number is where
breaking changes live.

## Unreleased

### Added

- **`Archive`** — software installed once from a checksum-verified tarball or zip and thereafter
  left alone. It asks whether the thing is installed and nothing else: no version is declared,
  stored as a pin, or compared, because software that ships its own updater would otherwise sit in
  permanent drift the moment it updated itself. It fetches when the install is absent, or when the
  url or checksum in your program change. The read has three rungs — the directory exists, `binary`
  is executable, `healthCommand` exits zero — and only the last tells a working install from a
  half-unpacked tree or a binary built for the wrong architecture. `version` is reported for
  `pulumi stack` and is never compared; failing to obtain it is not an error. The unpack goes to a
  staging directory and is moved into place with `link` repointed last, so a failed fetch cannot
  replace a working install with a broken one.
- **`GitCheckout`** — a repository at a commit, read back with `git rev-parse HEAD`. A tag or a
  branch is refused rather than resolved, with a message naming the `git ls-remote` that turns one
  into a sha: rev-parse answers with a sha, so a sha is the only declaration that can be compared
  against it, and resolving a ref each run would mean deciding whether a changed answer is an
  upgrade or a moved tag. An abbreviated commit matches the full answer, so a short sha is not
  permanent drift. Checkouts are detached, and a tree already at the commit costs no fetch.
- **`PosixAcl`** — ACL entries on a path, read back with `getfacl -pc` and compared on **effective**
  permissions rather than nominal ones, so a `chmod` that recomputed the mask and quietly suppressed
  a named entry reads as drift instead of as a mystery. Access entries and default entries are two
  arguments rather than a flag, because declaring one and meaning both is the mistake people
  actually make. It owns the entries it names and leaves the rest of the ACL alone — `setfacl -b`
  and `--set` appear nowhere. Base entries are refused as access entries, where they are the mode
  bits under another name and would fight `Directory`, and allowed as defaults, where no mode sets
  them; a `mask` is refused in both, since setfacl computes it. There is deliberately no `recursive`
  argument: applying an ACL across files that already exist cannot be read back without walking the
  tree on every refresh.

### Fixed

- **Seven `delete` methods read their layout path from state rather than from the module default.**
  A resource created with a non-default `directory`, `file` or `config` was deleted from the
  default path, which left the real file behind and reported success. The package checks now refuse
  a `delete` that reaches for a layout constant while its state carries the declared one.

## v0.1.0

First working version. It has been used to describe a real machine end to end — packages, users and
groups, systemd units and template instances, fstab entries, a symlinked journal, swap disabled at
both tenses, Samba shares, sudo rules, the boot line and the kernel command line — reporting no
drift across a few dozen resources.

### Resources

**Files and paths** — `ManagedFile`, `Directory`, `Symlink`, `FstabEntry`.

**Packages** — `AptPackage` for one package as a decision, `AptPackages` for a list with `present`
and `absent` both describable and removals refused when apt would take anything not named, and
`DebPackage` for a checksum-verified `.deb` from a URL.

**Services** — `SystemdUnit`, and `SystemdInstance` for template units, which have no file of their
own and so are asked about in both tenses.

**Accounts and access** — `User`, `Group`, `SudoRule`, `AuthorizedKey`, `SshKey`, `SshdConfig`.

**Identity** — `Hostname`, which owns the name in both files because a machine whose two names
disagree fails in ways that mention neither.

**Subsystems** — `Swap`, `Journald`, `KernelCmdline`, `BootConfig`, `SambaShare`, `SambaSetting`,
`SambaUser`, `RcloneRemote`.

**Gates and composition** — `Precondition`, and `FluxApp` with `fluxReady`/`fluxReason`.

`audit(host, declared)` reports what is on a machine that no code mentions: manually installed
packages sorted by when dpkg last wrote them, units in `/etc/systemd/system`, login accounts above
the machine's own `UID_MIN`, and fstab entries this provider marked that nothing declares any more.

### Transport

Plain `ssh`, so it inherits the agent, `known_hosts` and `~/.ssh/config` that already work from a
terminal. `Transport` is an interface with an ssh implementation and a local one, so the same
resources can describe a container, a chroot, or the machine the program is running on.

`Host.become` chooses how a command reaches root — `sudo -n` by default, `'none'` for a root
connection, or `{ password }` for `sudo -S` with the password on ssh's stdin rather than the command
line, where every user on the machine could read it. Connections are multiplexed, so a parallel
refresh costs one handshake rather than one per resource.

### Bugs found by running it against a real machine

Every one of these was invisible to the tests and obvious on hardware, and each is now a test.

- **`systemctl show --value` returns properties in systemd's own order**, not the order they were
  asked for, so every running enabled unit read as stopped and disabled. On a machine where
  everything is stopped and disabled the wrong answer and the right one are identical, which is why
  every fixture agreed with the bug.
- **`promisify(execFile)` made every resource unserialisable.** Pulumi serialises a dynamic
  provider's closure into the state file, and `promisify` reaches `Promise.withResolvers`, which is
  native code. `pulumi preview` died before opening a connection.
- **A module-scope `Set` serialised into a plain object** and threw only when something called
  `.has()` on it, from inside a `diff`, aborting every preview partway through. It serialised
  *successfully* into the wrong thing, which is the failure mode the existing checks could not see.
- **Comparing the serialised closure could not work.** An update does not re-persist `__provider`,
  so a resource whose stored text differed reported an update for ever without the difference ever
  resolving — and a comment added to a doc block made every resource in every stack report one.
  Replaced by a transport version carried in each resource's own state.
- **A doubled split marker** meant `Hostname` read the hosts file as empty on every machine, and
  because that check throws rather than reporting drift, the whole deployment aborted.
- **`testparm -s` omits a setting that equals its default**, so a resource that successfully made a
  setting match the default became permanently unable to observe that it had.
- **Twenty-one resources refreshing in parallel tripped sshd's `MaxStartups`**, which arrives as
  `kex_exchange_identification: read: Connection reset by peer` and reads as a network fault.
- **A merge that tested for a parameter's presence rather than its value** would silently refuse to
  fix a machine already carrying `cgroup_memory=0` — the exact machine somebody would be fixing —
  while the check on `/proc/cmdline` correctly went on failing.

### Verification

`pnpm test` runs the unit tests and then a set of package checks under plain Node, each with a
demonstrated failure mode: the package loads through Node's own loader, a provider closure
serialises, a serialised provider still works when read back, an unserialisable closure is still
rejected, no provider local shadows a global or a module-scope binding, nothing prototypal is
captured at module scope, every `diff` notices a transport upgrade and every provider stamps one,
and every resource declares a type and carries its legacy alias.

CI runs both on Ubuntu, where six tests that exercise resources against a real filesystem — and skip
on macOS, because BSD `stat` rejects `-c` — actually run.
