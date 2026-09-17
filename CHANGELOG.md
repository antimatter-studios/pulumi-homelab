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

- **`SshTunnel`** — a machine behind NAT publishing ports on a bastion it can reach, declared rather
  than assembled from a unit file by hand. Its read has three rungs: the unit file as text,
  `systemctl show` for active state and restart count, and the `-R` flags on the **running process**
  from `/proc/<pid>/cmdline`. The third catches what the others cannot — a unit whose file matches
  the declaration while the process still carries the previous forwards because nobody restarted it.
  One connection carries every forward rather than one unit per forward: each connection is a login,
  and several tunnels reconnecting look like a brute-force attempt to a bastion running fail2ban,
  where a ban takes out every tunnel and the route needed to fix them. `restartSec` under five
  seconds is refused for the same reason — `ExitOnForwardFailure=yes` is right, but a port still held
  by the previous connection then makes ssh exit and a tight restart loop is a self-inflicted ban.
  Two preconditions are checked before the unit is written, since both otherwise fail silently:
  `runAs` must be able to read `identity`, and must already trust the bastion's host key, with the
  refusal carrying the `ssh-keyscan` that fixes it. `restarts` is reported and never compared, since
  a restart is not drift and a rising count is the only available signal that the far end is refusing
  a forward. There is deliberately no autossh-versus-ssh option, and no `GatewayPorts` field — whether
  a forward binds loopback or every interface is the bastion's configuration, so a `bind` address is
  passed through and the far end decides.

- **`ManagedLine`** — one line in a file this stack does not own. `ManagedFile` owns whole files, and
  the case that keeps coming up is a file that must not be owned: a shell rc file, a packaged default
  that takes local additions. Owning one to add a single line means the next hand edit gets reverted,
  which is the two-writer failure that has already broken one machine twice through the same file.
  The marker is written into the line as a trailing comment and *is* matched on, unlike
  `FstabEntry`'s annotation, because a line in a `.bashrc` has no natural key and matching the
  literal text makes a hand-edited line a second line rather than the same one. Position is part of
  the requirement and is read back: `before`/`after` take an `anchor`, a missing anchor is refused
  rather than appended to the end, and a line somebody moved past its anchor reads as drift — a line
  after Debian's `case $- in` early return is present, correct and never executed. A line above its
  anchor but not adjacent to it is left where somebody put it, since it works there. Refuses a file
  that does not exist rather than creating one, and `delete` removes its own line and nothing else.

- **`Archive`** — software installed once from a checksum-verified tarball or zip and thereafter
  left alone. It asks whether the thing is installed and nothing else: no version is declared,
  stored as a pin, or compared, because software that ships its own updater would otherwise sit in
  permanent drift the moment it updated itself. It fetches when the install is absent, or when the
  url or checksum in your program change. The read has three rungs — the directory exists, `binary`
  is executable, `healthCommand` exits zero — and only the last tells a working install from a
  half-unpacked tree or a binary built for the wrong architecture. `version` is reported for
  `pulumi stack` and is never compared; failing to obtain it is not an error. The unpack goes to a
  staging directory and is moved into place with `link` repointed last, so a failed fetch cannot
  replace a working install with a broken one. `owner` and `group` set who owns the unpacked tree,
  defaulting to root: the fetch runs with escalation, so without them the tree lands root-owned and
  a tool that ships its own updater can never rewrite its own install directory — it fails quietly,
  the read stays green, and the software never updates again. Ownership is applied recursively at
  install and read only at the prefix, because software that updates itself legitimately rewrites
  files underneath and a recursive check would report drift on every update. A changed owner is a
  `chown` rather than a refetch, so fixing ownership cannot roll a self-updated tool back to the
  bootstrap. Extraction now passes `--no-same-owner`, so whoever packed the archive does not choose
  who owns files on the machine.
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

### Changed

- **`Directory` takes `setgid` and `sticky` as fields** rather than leaving them to a leading digit
  on `mode`. `2775` is correct the day it is written and quietly wrong the first time
  somebody edits the mode without knowing why there were four digits, and the bit that goes is the
  one holding a shared area together. `sticky` belongs beside any write grant on a shared directory,
  group or ACL: write permission on a directory is what permits deleting the entries in it. A
  four-digit `mode` still means what it always did; writing a non-zero leading digit *and* a flag is
  refused at preview rather than resolved by a precedence nobody would remember. `mode` is now also
  checked for being three or four octal digits, so a symbolic mode fails at the declaration rather
  than applying cleanly and reading back as permanent drift. There is deliberately no `setuid`
  field: `S_ISUID` has no defined meaning on a directory on Linux, so it would set cleanly, read
  back cleanly, and change nothing — a declaration the resource would report success for and which
  has no effect. A directory already carrying the bit is still adopted faithfully with `mode:
  '4755'`.

### Fixed

- **The control socket's name includes `SSH_AUTH_SOCK`.** `ControlPath` already hashed the route and
  the declared identity, for the same reason in both cases: a master opened one way must not be
  reused by a declaration asking for another, or the second silently inherits the first's
  authentication. The agent was the remaining hole — with `ControlPersist=60s`, swapping
  `SSH_AUTH_SOCK` between two runs inside that window reuses a live master authenticated by whichever
  agent opened it. Read at call time rather than captured, because a captured value would be
  serialised into state and revived on a machine where that socket never existed. Folded in as
  revision 3 before revision 2 had been deployed anywhere, so consumers take one update cycle rather
  than two.
- **`TRANSPORT` bumped, and a check so it cannot be forgotten again.** Pulumi serialises a
  dynamic provider's whole closure into the state file, so `read`, `diff` and `update` all run the
  *stored* code rather than the current source — a resource created last month still runs last
  month's `read`, and only a `TRANSPORT` bump makes it pick up a new one. Three merged changes
  altered what a resource reads (Samba reading the file instead of `testparm`, `SshKey` asserting
  the mode on both halves, `AptPackage`/`AptPackages` marking what they declare as manual) and none
  of them bumped it, because none of them touched the transport. All three were correct, merged,
  tested, and inert on every machine that already had those resources; the apt-mark fix was reported
  back as "merged and correct, and on my machine it has never executed". The constant's name cannot
  change — the field in state is `transport`, and renaming it would make every stamped resource read
  as unstamped — so its documentation now says plainly that it governs anything in the closure, not
  only the transport. `scripts/revision.ts` hashes every non-test source under `src/` and records it
  beside the revision it belongs to, so a source change with no bump fails in CI; the way to satisfy
  it is `pnpm run revision:record` after bumping, or `--no-bump` to record that the change need not
  reach existing resources. Unstamped state still answers `false` and still waits for something else
  about it to change, which is unaltered and deliberate.

- **`AptPackage` and `AptPackages` mark what they declare as manually installed.** apt records
  whether a package is present because somebody asked for it (*manual*) or because something else
  required it (*auto*), and `apt autoremove` is entitled to take an auto package once nothing
  requires it. A package already present as a dependency needs no installing, so `AptPackages`
  correctly computed nothing missing, installed nothing, and reported success — while the machine's
  position was "present because something else wants it", which an unrelated `autoremove` is allowed
  to undo. Found on a machine where three declared UEFI firmware packages were all auto-marked,
  having arrived as recommends of `qemu-system-arm`. Both resources now read `apt-mark showmanual`
  alongside dpkg in the same round trip and mark only the difference, so nothing is written on a
  deployment where nothing changed; a declared package held as auto is reported in `auto` (or
  `manual: false`) and reads as drift. Neither ever marks anything *auto* — handing a package to
  `autoremove` is removal by a slower route. `--no-install-recommends` is deliberately not set:
  `qemu-system-arm` has its UEFI firmware as a Recommends, so turning recommends off installs
  cleanly and leaves no arm guest able to boot, which reads as a broken VM rather than a missing
  package. `AptPackages`'s install/mark ordering and its drift decision are extracted as
  `presentCommands` and `packagesChanged`.
- **`Host.identityFile` offers one key rather than everything in the agent.** sshd's `MaxAuthTries`
  is 6; an agent holding nine keys offers them in its own order; and a host that accepts the eighth
  closes the connection with `Received disconnect: Too many authentication failures` before
  reaching it. That reads as the server rejecting you, so the obvious responses — relaxing the
  limit, unbanning an address — treat a symptom that was never the cause. And the agent's order is
  not stable: the same key was measured at position five and then at eight, the order having changed
  when the vault was re-unlocked, so an unchanged stack deploys in the morning and fails in the
  afternoon. A `.pub` path is enough, and better: ssh matches it against the agent and offers only
  that one, so nothing secret goes near a program. Unset, nothing changes. When set, the
  `proxyJump` becomes a `ProxyCommand` using `-W`, because `-J` does not pass options to the ssh it
  spawns and so cannot pin the jump's identity — which is where the limit is usually hit. `-W` keeps
  ssh doing the forwarding and `known_hosts` checked for the far end, so it is not the
  netcat-style `ProxyCommand` that `ProxyJump` was chosen over. A multi-hop jump with an identity is
  refused rather than nested through two layers of shell quoting. The identity is hashed into the
  control socket's name, so a socket opened with one key is not reused for a declaration asking for
  another.
- **`SambaShare` and `SambaSetting` read the file, and ask `testparm` only whether it parses.**
  Both compared against `testparm` output, which reports Samba's resolution rather than what was
  written — so both rewrote their sections on every deployment to correct a difference that did not
  exist. Measured against two real shares of twenty-five settings each, `testparm` reported
  thirteen: a share setting matching `[global]` is not repeated, a value equal to Samba's default is
  omitted, spellings are normalised (`2775` reads back `02775`), and synonyms are collapsed
  (`writeable = yes` *is* `read only = no`, and only the canonical one is printed). Closing that
  semantically would mean carrying Samba's synonym table, its per-version default table, and enough
  of its resolution order to tell the two kinds of absence apart. The comparison is now against the
  section in the file, which is what the resource wrote and round-trips exactly, while `testparm`
  keeps the job only it can do — a `parses` output, because a configuration Samba cannot read does
  not break the share it is in, it stops smbd reloading, and the share quietly does not exist.
  `SambaShare.effective` and `.overridden` are replaced by `.actual` and `.parses`;
  `SambaSetting` keeps `.effective` as information, reported and never compared, and gains
  `.actual`. What is given up is "is this setting in force", a question about Samba's resolution
  order rather than about whether the machine matches the declaration.
- **Seven `delete` methods read their layout path from state rather than from the module default.**
  A resource created with a non-default `directory`, `file` or `config` was deleted from the
  default path, which left the real file behind and reported success. The package checks now refuse
  a `delete` that reaches for a layout constant while its state carries the declared one.
- **`SshKey` takes `owner` and `group`, and asserts the mode on both halves.** `ssh-keygen` runs
  under escalation, so the pair landed root-owned and a service account could not read its own
  private key. The mode was not safe to leave to `ssh-keygen` either: it creates `0600`, but a
  default ACL on the parent directory is inherited by the new file and can widen what lands, and ssh
  then refuses the key — at use time, as an authentication that fails, with nothing wrong at the
  path to look at and neither the public key nor the fingerprint able to see it. Both modes are now
  set and read back, `0600` private and `0644` public, and neither is an argument because every
  other value produces a key ssh will not use. Ownership defaults to root, so nothing declared
  before this changes. Asserting `0600` also disarms an inherited ACL rather than merely narrowing
  the mode: `chmod` recomputes the mask from the group bits, and a group bit of zero suppresses
  every inherited named entry to `#effective:---`.

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
