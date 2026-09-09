# Changelog

All notable changes to this project are recorded here. Versions follow
[semantic versioning](https://semver.org/), and until 1.0.0 the minor number is where breaking
changes live.

## 0.1.0 — unreleased

First working version, used to describe a real machine end to end.

### Resources

Files and paths — `ManagedFile`, `Directory`, `Symlink`, `FstabEntry`.
Identity — `Hostname`, which owns the name in both files because a machine whose two names
disagree fails in ways that mention neither.
Packages — `AptPackage` for one as a decision, `AptPackages` for a list, `DebPackage` for a
checksum-verified `.deb` from a URL, with `present` and
`absent` both describable and removals refused when apt would take anything not named.
Services — `SystemdUnit`, `SystemdInstance` for template units.
Accounts and access — `User`, `Group`, `SudoRule`, `AuthorizedKey`, `SshKey`, `SshdConfig`.
Subsystems — `Swap`, `Journald`, `KernelCmdline`, `BootConfig`, `SambaShare`, `SambaUser`,
`RcloneRemote`.
Gates and composition — `Precondition`, `FluxApp`.

`audit(host, declared)` reports what is on a machine that no code mentions: manually installed
packages sorted by when dpkg last wrote them, units in `/etc/systemd/system`, login accounts, and
fstab entries this provider marked but nothing declares any more.

### Transport

Plain `ssh`, so it inherits the agent, `known_hosts` and `~/.ssh/config` that already work from a
terminal. `Host.become` chooses how a command reaches root — `sudo -n` by default, `'none'` for a
root connection, or `{ password }` for `sudo -S` with the password on ssh's stdin rather than the
command line. Connections are multiplexed, so a parallel refresh costs one handshake rather than
one per resource.

### Bugs found by running it against a real machine

- **`systemctl show --value` returns properties in systemd's own order**, not the order they were
  asked for, so every running enabled unit read as stopped and disabled. On a machine where
  everything is stopped and disabled the wrong answer and the right one are identical, which is why
  every fixture agreed with the bug.
- **`promisify(execFile)` made every resource unserialisable.** Pulumi serialises a dynamic
  provider's closure into the state file, and `promisify` reaches `Promise.withResolvers`, which is
  native code. `pulumi preview` died before opening a connection.
- **Pulumi runs the stored closure, not the current source**, so a fix to the transport reached only
  resources created after it — four on the new transport and eighteen on the old, with no way to
  tell from the outside. Every `diff` now reports a change when the serialised provider differs.
- **Twenty-one resources refreshing in parallel tripped sshd's `MaxStartups`**, which arrives as
  `kex_exchange_identification: read: Connection reset by peer` and reads as a network fault.
- **A merge that tested for a parameter's presence rather than its value** would silently refuse to
  fix a Pi already carrying `cgroup_memory=0` — the exact machine somebody would be fixing — while
  the check on `/proc/cmdline` correctly went on failing.

### Verification

`pnpm test` runs the unit tests and then a set of package checks under plain Node, each with a
demonstrated failure mode: the package loads through Node's own loader, a provider closure
serialises, a serialised provider still works when read back, an unserialisable closure is still
rejected, no provider local shadows a global **or a module-scope binding**, every `diff` notices a
provider upgrade, and every resource declares a type and carries its legacy alias.

The module-scope half of that scan catches what the compiler cannot. A factory's locals do not
survive closure revival, and a lost binding does not raise — the name resolves to whatever else is
in scope, which is either a global of the same name or a module constant that *was* captured. The
second silently swaps one value for another and the resource goes on working against the wrong path.
A local shadowing a module constant in the same scope is a duplicate identifier and `tsc` rejects
it; nested one function deeper, it compiles.
