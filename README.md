# pulumi-homelab

Pulumi resources for a Linux machine you own. Desired state over SSH, in TypeScript, with real
drift detection.

Point it at a Raspberry Pi, a NAS or a server that has been hand-tended for years, describe the
parts you care about, and leave the rest alone. Every resource asks the machine what it actually
says rather than trusting Pulumi's memory of what it once did, so a file somebody edited, a
service somebody stopped, or a package somebody removed comes back as drift you can see.

## Why

There is a gap between the tools that *provision* infrastructure and the tools that *configure* it.
Pulumi is excellent at the first and has nothing of its own for the second — `remote.Command` runs
shell over SSH and records that it ran, which is not the same as knowing what the machine is.
Ansible fills the gap and is a YAML dialect. NixOS closes it entirely and is a project in itself.

This is the small answer, and it has one rule:

> **Every resource implements a real `read`.**

Not "this command ran once", but "here is what the machine says right now". That is what makes
`pulumi up --refresh` mean something. A file somebody edited by hand, a service somebody stopped
last Tuesday, a package somebody removed — all of it comes back as drift you can see, rather than
the code and the machine parting company in silence.

The rule has a corollary that is easier to break: **do not assert state that nothing reads back.**
A write with no matching read is applied for ever and checked never — it can't show up as drift, and
the day something else declares the same thing, the two re-apply different answers on alternate runs
with both reporting success. Both bugs this project has had were the two ends of the same rule: a
read that normalised wrongly and so reported drift for ever, and a write with no read at all. Every
write wants a read, and the two have to agree on how the value is spelled.

There is one resource here that the rule reads differently for, and it is worth saying which.
`FluxApp` is a `ComponentResource` — a pairing of two Kubernetes objects rather than something that
talks to a machine over ssh — and it implements no `read` because `@pulumi/kubernetes` already reads
those objects back from the API server properly. The rule is about a resource being able to say what
is actually there, and for that one something else already can. Everything else in this package
answers for itself.

There is a third rule in the same family as the first two, and between them they cover every bug
this project has had:

- a read that normalises wrongly reports drift for ever, on something nobody touched;
- a write with no read reports nothing for ever, then fights the first resource that does read it;
- **a check on the input proves the request, not the result** — where the effect can be observed,
  check the effect. `/proc/cmdline` says what the kernel was asked for;
  `/sys/fs/cgroup/cgroup.controllers` says what it actually did, and on a Raspberry Pi 5 whose
  firmware contributes a contradicting parameter those are not the same answer.

The shape underneath all three is the same: code that tests for presence answers a question nobody
asked. What matters is the value, and whether the thing that wrote it and the thing that reads it
agree about what it means.

The third one keeps recurring because **the thing you control and the thing you care about are
usually different objects**:

| The cheap question | What you actually care about |
|---|---|
| the boot line in `cmdline.txt` | `/proc/cmdline`, after the firmware has had its say — and `/sys/fs/cgroup/cgroup.controllers`, after the kernel has |
| the drop-in you wrote | `systemd-analyze cat-config`, which is what systemd merged |
| the mount unit you asked for | the mount point, which is what happened |
| `swapoff`, an action | `/proc/swaps`, a state |
| `uname -m`, which has three possible answers on one machine | `dpkg --print-architecture`, the userland's, which has one — see below |

Asserting the first and reading the second is the only combination that tells the truth, which is
the one rule again applied to the check rather than to the resource. And the reason it keeps being
missed is that **the cheap question is usually right** — which is exactly what makes the exception
expensive when it arrives.

The last row is the sharpest of them, because there the cheap question does not merely differ from
the one that matters: it has no single answer at all. Raspberry Pi OS 32-bit runs a 64-bit kernel by
default on a Pi 4 and necessarily on a Pi 5, whose A76 has no aarch32 at EL1 — a wholly armhf
userland on an arm64 kernel is the standard configuration. What `uname -m` reports there depends on
who is asking: a compat process is told whatever the kernel's `COMPAT_UTS_MACHINE` holds, which is
`armv8l` on an arm64 kernel and `armv7l` for the same userland under an armhf one, while a process
requesting the `PER_LINUX` personality is told `aarch64`. Three answers, one machine, and none of
them is the question — which is whether a binary will run. `dpkg --print-architecture` answers that
one and nothing else.

## What it does not do

It does not claim the whole machine. Anything you do not declare is left exactly as it is, which is
what makes it safe to point at a server that already works and has been hand-tended for years, and
what makes adopting one incremental rather than a rebuild.

**Adoption is by convergence, not by import.** Pulumi cannot import resources belonging to a dynamic
provider — neither `pulumi import` nor the resource-level `import` option, both of which fail inside
Pulumi's own dynamic-provider service. So describing something that already exists means declaring
it and letting the first `up` agree with the machine, which in turn means the declaration has to be
byte-exact against what is there: a `ManagedFile` whose content differs by a trailing newline will
rewrite the file rather than adopt it. The `read` is what makes that survivable — run
`pulumi refresh` first and the diff tells you where the code and the machine disagree before
anything is written.

The cost is worth stating plainly: a machine managed this way is reproducible **in the parts you
modelled and no further**. If you need "rebuild from bare metal and get the same box", you need to
model everything — and at that point you are describing NixOS, which already does it.

## Only data crosses into a provider, never behaviour

The rule that decides what is possible here, and it is invisible from the outside. Pulumi serialises
a dynamic provider's whole closure into the state file and evaluates it again later, so **a value
survives that trip and a running thing does not**. A consumer can hand a resource
`{ address, user }`; it cannot hand it an object with methods, however well typed.

Inside the package that constraint lifts, because the implementations travel with the closure. So
`Transport` is an interface here and could not be one across the boundary:

```ts
export interface Transport {
  ask(command: string): Promise<Ran>;
  escalate(command: string): string;
  describe(): string;
}
```

Every resource takes a `Target`, which is either the `Host` struct or a transport. `localTransport()`
is the other implementation, and it exists because **every bug this package has had was found by a
real machine and invisible to its tests** — a fixture written by the same person who wrote the bug
agrees with it. Pointing `ManagedFile` at a temporary directory lets the real `stat` answer.

It found something on its first run: these reads use `stat -c`, which is GNU coreutils, and BSD
`stat` rejects it. Not a bug — this package describes Linux machines — but an assumption nothing had
written down until a shell was asked.

The same rule is why a resource cannot take a *reference* to a secret and fetch it.
`{ vault, entry, attr }` would survive serialisation, but only code that already understood those
words could act on it, so the package would either learn about one particular vault or grow a
plug-in interface with exactly one implementation. It is also why three separate failures in one day
were one lesson: a helper named `fetch` losing its binding and landing on the global, a module-scope
import captured by reference, and `promisify` reaching native code.

### Declared, effective, overridden

The package's central idea was implemented five times before it was named. `BootConfig` asks
`vcgencmd`, `Journald` asks `systemd-analyze cat-config`, `SambaShare` asks `testparm`, `SshdConfig`
asks `sshd -T` — each with its own field names for **what this resource wrote is not what the system
does**.

`Resolved<T>` names it, and `overridden` — which one resource had and four needed — is now on all of
them: the declared keys the system does not agree with, because something else won. A drop-in that
sorts later, a `Match` block, a `config.txt` section that never matches the board. None of them are
errors and all of them are silent. **Nothing reconciles it**: a resource that rewrote its own file to
win would lose the same argument on the next run.

## Nothing about a layout is hardcoded

Every path this package touches is an argument with a default, not a constant. `/etc/fstab`,
`/etc/samba/smb.conf`, `/etc/sudoers.d`, `/etc/systemd/system`, `/etc/systemd/journald.conf.d`,
`/var/log/journal`, `/var/lib/dpkg/info`, `/etc/login.defs`, the boot partition's `cmdline.txt`,
the `dphys-swapfile` unit, rclone's config file and the kubeconfig — all of them are decisions a
distribution made, and a provider that treats one as a fact is a provider that is confidently wrong
about a machine it has never met.

The defaults are what Debian and its derivatives do. Anything else says so at the call site:

```ts
new SambaShare('media', host, { share: 'media', path: '/srv/media', config: '/usr/local/etc/smb.conf' });
new Swap('none', host, { enabled: false, unit: 'swapfile-generator' });
```

The same goes for anything identifying. Nothing in this repository names a real host, address,
UUID, account or service — the examples and fixtures use documentation addresses and placeholder
names, because a provider describes a *kind* of machine and the specifics belong in the program that
calls it.

## Using it

**A note on install time.** `@pulumi/kubernetes` is a dependency because `FluxApp` is in here, and
it is a large one: 22MB of SDK, plus a `postinstall` that downloads a 178MB resource plugin into
`~/.pulumi/plugins`. On a cold store that install has taken close to three minutes; where the plugin
is already cached it is seconds, which is why two people can report wildly different numbers for the
same command. It is install
time and not runtime — Pulumi runs a program through Node rather than bundling it, so a module
nobody imports is never loaded — but it is long enough to be mistaken for a hang, which is the only
reason it is worth mentioning here.

**Consumers need one compiler option.** This ships sources rather than built output, so a `link:`
or `file:` dependency means *your* compiler reads these files under *your* options. Relative imports
here carry their `.ts` extension — which is what lets plain `node --experimental-strip-types` load
the package with no build step, and is how `audit()` runs from a two-line script rather than only
inside a Pulumi program. Set this, or you get `TS5097` pointing at files in this repo from a
typecheck of yours:

```json
{ "compilerOptions": { "allowImportingTsExtensions": true } }
```

Nothing here emits, so it costs a consumer nothing but the line.


```ts
import { AptPackage, SystemdUnit, type Host } from 'pulumi-homelab';

const host: Host = { address: '198.51.100.10', user: 'admin' };

const node = new AptPackage('nodejs', host, { name: 'nodejs', update: true });
```

Always run with `--refresh`. A bare `pulumi up` compares your code against Pulumi's *memory* of the
machine rather than the machine itself, which is the one way to make all of this pointless.

## The resources

Every one of these reads the machine, compares, and does only what differs. The
"reads from" column is the command that answers the question — not a record of what
Pulumi once did.

### Files and paths

| | Arguments | Reads from |
|---|---|---|
| **`ManagedFile`** | `path` `content` `mode?` `owner?` `group?` `reloadSystemd?` | `stat` and `cat` |
| **`Directory`** | `path` `mode?` `owner?` `group?` | `stat` |
| **`Symlink`** | `path` `target` | `readlink`, then `stat` |
| **`FstabEntry`** | `source` `target` `type` `options?` `dump?` `pass?` `mount?` | the line in `/etc/fstab`, plus `findmnt` |

`Directory` makes parents on the way up and removes only the leaf on the way down, with
`rmdir` rather than `rm -rf` — a directory with something still in it is a machine saying the
code's picture of it is incomplete, and failing loudly is worth more than a tidy teardown.

`Symlink` tells four states apart: a link pointing where the code says, a link pointing
elsewhere, **something real at that path**, and nothing. The third throws rather than
replacing, because `/var/log/journal` being a real directory instead of a link means journald
writes to the SD card silently and for ever.

`FstabEntry` is keyed on the mount point, never regenerates the file, and writes the line
without mounting it — mounting over a directory that has contents hides them, and a deployment
is the worst moment to discover that. It marks its lines with a `# pulumi-homelab` comment
which is *never matched on*; that is what lets `audit()` find orphans.

### Packages

| | Arguments | Reads from |
|---|---|---|
| **`AptPackage`** | `name` `update?` | `dpkg-query` |
| **`AptPackages`** | `present?` `absent?` `update?` | `dpkg-query`, whole list in one round trip |

### Services

| | Arguments | Reads from |
|---|---|---|
| **`SystemdUnit`** | `name` `unit` `enabled?` `started?` `mode?` | `systemctl show` and the unit file |
| **`SystemdInstance`** | `template` `instance` `enabled?` `started?` `suffix?` | `systemctl is-enabled` and `is-active` |

`SystemdUnit` holds the unit file and the running state together because they are one thought.
`SystemdInstance` is for template units — `avahi-alias@photos.example.local` — which have no
file of their own, and asks both questions separately because an instance can be enabled and
not running. A `static` unit (no `[Install]` section) is refused with an explanation rather
than reported as permanent drift.

### Accounts

| | Arguments | Reads from |
|---|---|---|
| **`User`** | `name` `shell` `home?` `createHome?` `groups?` `allowGroupRemoval?` | `getent passwd` and `id -nG` |
| **`Group`** | `name` `gid?` `renumber?` | `getent group` |
| **`SudoRule`** | `user?`/`group?` `passwordless` `commands?` `runAs?` `file?` | the drop-in in `/etc/sudoers.d`, and its mode |

`User.shell` is required and `groups` is the **whole list**. Both defaults were removed because
each one could take the machine away from you: `nologin` on your own account, or `usermod -G`
silently dropping the groups you did not mention, `sudo` among them. Removing a
membership needs `allowGroupRemoval`.

`Group` exists for the gid. Filesystem permissions store numbers, not names, so a rebuild where
`groupadd` hands out 1003 instead of 1002 leaves every file owned by a group that does not
exist — and every command exits zero.

### Subsystems

| | Arguments | Reads from |
|---|---|---|
| **`Swap`** | `enabled` `sizeMb?` `path?` | `/proc/swaps`, `/etc/fstab`, the `dphys-swapfile` unit |
| **`Journald`** | `settings` `file?` | `systemd-analyze cat-config` |
| **`KernelCmdline`** | `flags` `path?` | the boot partition's `cmdline.txt` |
| **`SambaShare`** | `share` `path` `settings?` | `testparm -s` |
| **`SambaUser`** | `name` `password` | `pdbedit -L` — existence only |
| **`RcloneRemote`** | `remote` `type` `settings?` `secrets?` `config?` | `rclone config dump`, credentials revealed |

### Gates and composition

| | Arguments | Reads from |
|---|---|---|
| **`Precondition`** | `check` `message` `root?` | whatever the check exits with |
| **`FluxApp`** | `url` `branch?` `path?` `include?`/`ignore?` `prune?` `secretRef?` … | the Kubernetes API |

`Precondition` builds nothing. It models something that must be true before the resources
depending on it run — a reboot taken, a disk mounted, a kernel flag in effect — and fails with
the message you wrote. Because its `read` asks again, a machine that stops qualifying comes back
as a resource that has gone. **One condition per resource, not one compound check**: a reader
who has to work out which half failed is doing the diagnosis the resource existed to do.

Check builders to pair with it: `bootedWith(flags)`, `mountedAt(path)`, `fluxReady(name)`, and
`fluxReason(name)` for the message.

### Helpers

`ask` and `must` (a non-zero exit is an *answer* to a question and a *fault* when doing),
`escalate` / `asRoot`, `shellQuote`, `heredoc` / `heredocInto`, `sshArgs`, `normaliseMode`,
`audit`, and `providerChanged` / `withLegacyAlias`.

`BootConfig` is `KernelCmdline`'s sibling and the section is part of its identity. A Raspberry Pi's
`config.txt` has conditional filter sections — `[all]`, `[pi5]`, `[cm5]` — and **a setting under a
section that does not match the board is not an error**: the firmware reads it, decides it does not
apply, and carries on. A line enabling a PCIe lane once sat under `[cm5]`, which matches a Compute
Module and never a Pi 5 Model B, so it silently never applied and the symptom was a missing NVMe
drive rather than anything mentioning boot configuration.

```ts
new BootConfig('pcie', host, { section: 'all', overlays: ['dtparam=pciex1'] });
```

`settings` is typed **only where a value set is genuinely closed** — the 0/1 options as booleans,
`hdmi_group` as `0 | 1 | 2`, `display_rotate` as quarter turns. `gpu_mem`, `hdmi_mode` and
`sdram_freq` are plain numbers, because their valid ranges depend on the board and on each other
(`hdmi_mode` means different things under different `hdmi_group` values), so a union would be wrong
somewhere and right nowhere in particular.

The asymmetry that decides all of this: an unknown **key** is inert — the firmware ignores what it
does not recognise, so `arm_bosot=1` does nothing at all. Only a known key with a wrong **value**
is dangerous, and only some of those have a set small enough to write down.

`arm_freq`, `over_voltage`, `kernel` and `initramfs` are absent from the type deliberately, and
reachable only through `unchecked`. Not because a type could validate them — it could not — but
because a wrong value there means no boot, no message, and a recovery that involves a card reader
and another computer. The escape hatch is spelled to make somebody think.

**Overlay names are checked against the machine, not against a list in this source.** The complete
legal set is a directory of `.dtbo` files, so `dtoverlay=dwc3` — one letter from `dwc2` — fails at
deploy time with the directory to look in and `dtoverlay -h <name>` to read. A hand-maintained union
would be wrong the moment a firmware package updated, and wrong silently. Same argument as reading
`cgroup.controllers` rather than `/proc/cmdline`: ask what is there, not what should be.

`overlays` are whole lines that may legitimately repeat, because `dtoverlay=vc4-kms-v3d` and
`dtoverlay=dwc2,dr_mode=host` are two pieces of hardware rather than a contradiction, and a
key-based upsert would collapse them and remove one. `dtparam` lives there too, for the same reason.

It reports `effective` from `vcgencmd get_config`, which is the firmware's own answer — and says
plainly that it does not cover overlays, which the device tree consumes rather than keeping as
config integers.

## Two tenses

`Swap` is the resource that shows why a real `read` is harder than it sounds. `swapoff -a` empties
`/proc/swaps` immediately and changes nothing about the next boot, so a resource reading only the
running state reports itself correct on the afternoon somebody ran it and drifted every morning
afterwards. Its `read` answers both questions — what is active, and what is configured to become
active — and `enabled: false` is only satisfied when both say no:

```ts
new Swap('none', host, { enabled: false });
```

Which is why turning swap off means `swapoff -a`, *and* masking `dphys-swapfile` rather than merely
disabling it (an `apt upgrade` re-enables a disabled unit), *and* commenting the `fstab` lines
rather than deleting them, so the machine keeps the record of what it used to do.

`Journald` has the same shape from the other end. It writes a drop-in under
`journald.conf.d/`, but a drop-in sorting later wins — so it reports `effective`, parsed from
systemd's own merged view, alongside the file it wrote, and names any setting something else
overrides. It does not try to win that argument by rewriting its own file, because it would lose it
again on the next run.

`SystemdInstance` is the same idea for a template unit — `avahi-alias@photos.example.local` — which
has no file of its own to read. It asks both questions rather than inferring one from the other,
because an instance can be enabled and not running, and on a real machine one was:

```
example.local          enabled  active     (started by hand, before any of this owned it)
photos.example.local   enabled  inactive   (declared correctly, and not resolving on the network)
```

The second line is what this whole project exists to prevent, arriving from the other side: the
resource reported success, the state file agreed, and the thing that was asked for did not work.

## Sudo, and what it cannot do

`SudoRule` writes a file in `/etc/sudoers.d`, validated with `visudo -c` before it is installed —
a syntax error there does not break the rule, it breaks sudo for every user on a machine whose only
management path is sudo over ssh.

```ts
new SudoRule('backup-restart', host, {
  user: 'backup',
  passwordless: true,
  commands: ['/usr/bin/systemctl restart borg'],
  runAs: 'root',
});
```

**It cannot bootstrap itself.** Everything here runs through `sudo -n`, so writing a sudoers file
already requires the privilege a sudoers file grants. It manages sudo for *other* accounts, and
records an arrangement that already exists so it stops being folklore. The first passwordless sudo
on a machine is a person with a password, and nothing declarative can be the thing that creates it.

`passwordless` has no default on purpose: it is the argument that decides whether a rule is a
convenience or a standing grant of root, and a default would let it be chosen by not thinking
about it.

It models one rule and cannot express `Cmnd_Alias` or `Defaults` lines. Inlining the commands is
usually identical in meaning; a sudoers file that leans on aliases wants a `ManagedFile` instead,
and loses the `visudo` validation in exchange.

## SSH

Three resources, and the reason they are three rather than fields on `User`: a real
`authorized_keys` file has more than one owner. One machine's carries a laptop's key and an RSA key
labelled for the far end of a reverse tunnel, put there by somebody else entirely. A list on `User`
would have to be the whole truth — as `groups` is — so declaring one key would silently remove the
other. A resource per key composes instead, and a different stack can authorise a key here without
touching the account's declaration.

```ts
const key = new SshKey('deploy', host, { path: '/root/.ssh/deploy_ed25519' });

new AuthorizedKey('deploy-on-build', buildHost, {
  user: 'ci',
  key: key.publicKey,
  options: ['from="10.0.0.0/24"', 'command="/usr/bin/deploy"'],
});
```

Generate on one machine, authorise on another, and no human copies a secret. `SshKey` makes the
public half and the fingerprint outputs and deliberately does **not** output the private key —
unlike rclone's obscured passwords there is nothing to compare it against, so it has no reason to be
in a state file. `delete` refuses without `allowDelete`, because a key may be trusted somewhere this
stack has never heard of.

`AuthorizedKey` matches on the **key body**, never the comment — people rename laptops, and a
resource keyed on the line would add a duplicate of a key already present. It sets and reads back
both the mode and the owner: sshd refuses a key file that is group-writable *or* owned by somebody
else, and tells the client `Permission denied (publickey)` either way, which is indistinguishable
from a wrong key.

`SshdConfig` writes a drop-in, validates it with `sshd -t` before installing, and **fails if
`sshd_config` has no `Include`** for the drop-in directory — the same silent shape as a `config.txt`
setting under a filter that never matches. It reloads rather than restarts, since one of the
connections a restart drops is the one applying the change.

Its `read` is `sshd -T`, and that needed a normalisation layer of its own:
`PermitRootLogin prohibit-password` comes back as **`without-password`**, the deprecated spelling of
the same thing. Compared directly that is drift on every refresh and a correction on every
deployment — the `0644` bug again, in a different alphabet.

Host keys are deliberately not managed; `hostKeys(host)` reports their fingerprints instead. A
machine presenting new ones has been reinstalled, which is worth being told loudly and is not worth
rotating on purpose.

## The one where the obvious comparison is wrong

`RcloneRemote` is worth reading before writing anything similar. rclone stores passwords *obscured*,
and `rclone obscure` is **not deterministic** — it encrypts with a random initialisation vector, so
obscuring the same password twice gives two different strings. A resource that obscured the desired
password and compared it against what is stored would report drift on every refresh, for ever, on a
remote nobody had touched. It is the `644` versus `0644` bug with a cryptographic hat on.

So the comparison is done in plaintext: `rclone reveal` turns the stored value back into what it
was, and that is what is compared. The cost is that the plaintext ends up in the Pulumi state file,
marked as a secret output, because there is nowhere else for it to be if drift is to be detected at
all.

Worth knowing either way: obscuring is obfuscation, not encryption. The key is a constant in
rclone's own source, so a remote's credentials are protected by the config file's mode and by
nothing else.

## The one place the rule cannot be met

`SambaUser` can read that an account exists and nothing about its password, which lives hashed in
Samba's own database. Setting it in the program works; changing it on the machine will never come
back as drift, because there is nothing to compare against. That is stated here rather than papered
over — a resource that cannot be fully reconciled should say so, and it is the only one in the
package.

`SambaShare` has no such problem, because `testparm -s` prints the configuration as smbd itself
resolves it — defaults applied, includes followed, syntax validated. Sections are edited in place
and the file is never regenerated, so a share nothing here declares survives untouched. It reloads
rather than restarts: a restart drops every open connection, which on a machine somebody watches
films from means their film stops.

## A package, and a list of packages

`AptPackage` is one package as a decision — nodejs because a service needs it, with a dependency
edge to the service. `AptPackages` is the other case: the list of things somebody installed because
they wanted them on the machine.

```ts
new AptPackages('tools', host, {
  present: ['btop', 'duf', 'fd-find', 'gdu', 'ripgrep'],
  absent: ['nano'],
  update: true,
});
```

The difference is round trips and resolution. Twenty single resources ask dpkg twenty times over
twenty ssh connections, and apt resolves each against a different intermediate state; this asks once
and installs the missing ones together.

**Both conditions are describable, and that matters more than it looks.** Without `absent`, "not
installed" is something the audit can report and the code cannot state — a package somebody removed
on purpose three years ago is indistinguishable from one nobody ever considered. Drift is read in
both directions: something declared present that has gone, and something declared absent that has
come back.

**Removal is guarded rather than trusted.** `apt-get purge` removes everything depending on what it
removes, and it does so with a success exit code, because from apt's point of view that is exactly
what was asked. So a removal is simulated first with `apt-get -s purge`, and if apt would take
anything not named in `absent`, the deployment fails and names the collateral. Declaring a package
absent is a statement about that package, not permission to remove what depends on it.

A package named in both lists throws rather than resolving, because that is not a machine that can
exist, and picking a winner would be the resource deciding which half of the code to believe.

`delete` does nothing in either direction: dropping the description of a machine's packages is
neither a request to strip it nor to reinstall what was declared absent. What that leaves behind is
what the audit reports.

## Flux

The one component rather than provider, and the one thing here that is about a cluster:

```ts
new FluxApp('app', {
  url: 'https://github.com/example/app',
  branch: 'main',
  path: './deploy/flux',
}, { provider: cluster });
```

It creates a `GitRepository` and a `Kustomization` in `flux-system` and pairs them. **After it
exists, Pulumi is no longer in charge of the application** — push to the repository and the cluster
changes, with no `pulumi up` involved and nothing in a preview to see. Pulumi owns the pointer;
Flux owns everything the pointer reaches.

The pairing earns its place on one detail. A `Kustomization` names its source in
`spec.sourceRef.name`, and when that name does not match a `GitRepository` that exists, **Flux does
not report an error** — it simply never reconciles, and the symptom is an application that never
deploys with nothing anywhere saying why. So the name comes from the object that exists rather than
from the string it was asked for, which is the failure Pulumi's autonaming would otherwise produce.

### Reading the effect, not just the pointer

That fixes one cause of silence. It does not fix the silence, and the difference matters: a path
that is not in the repository, a branch that does not exist, a private repository Flux cannot read —
none of those are errors either. Flux writes a condition on the object and carries on, and
`pulumi up` returns the moment the two objects exist, which is not the moment anything is deployed.

Which is the third rule again — the pointer is what you control, the reconciliation is what you care
about — so they are asked separately:

```ts
const app = new FluxApp('app', { url, branch: 'main', path: './deploy/flux' }, { provider });

new Precondition('app-reconciled', host, {
  check: fluxReady('app'),
  root: true,                      // k3s writes its kubeconfig 0600, owned by root
  message: `Flux has not reconciled app. Ask it why:\n  ${fluxReason('app')}`,
}, { dependsOn: [app] });
```

`fluxReady` is a `kubectl wait` rather than a read-and-compare, so a deployment running seconds
after the objects were created is not failed merely for being early. `fluxReason` is the command
that prints Flux's own explanation out of the Ready condition, and it belongs in the *message*
rather than in the check: a check that printed it would still leave the operator without it once the
deployment had stopped.

## The audit

Leaving undeclared things alone is what makes this safe to point at a machine that already works.
The price is invisible rot: a package installed by hand two years ago, a unit left over from a
service that was replaced, an account belonging to somebody who has moved on. None of it appears in
a diff, because a diff can only talk about resources the program contains.

```ts
import { audit } from 'pulumi-homelab';

const findings = await audit(host, { packages: ['nodejs'], units: ['aiworld'], users: ['aiworld'] });
```

It asks the machine what it has, subtracts what you declared, and reports the rest: manually
installed packages (newest first, since a Debian base install marks several hundred as manual and
they all share one timestamp), units in `/etc/systemd/system`, and accounts above the machine's own
`UID_MIN`. It is a plain function rather than a resource — it builds nothing, and there is no
desired state for it to hold.

Managing a machine fully means describing every resource with no gaps; anything short of that means
accepting the differences in the gaps. This is what turns accepting them into knowing their size.

## Upgrading this provider is a real deployment

Pulumi serialises a dynamic provider's whole closure — the transport, the quoting, every resource
method — into the state file, and `read`, `diff` and `update` all run **the stored closure rather
than the current source**. A resource created last month is still running last month's `ssh.ts`.

Every `diff` here therefore reports a change when the serialised provider differs, which means the
first deployment after upgrading this package **updates every resource**. For `SystemdUnit` that
restarts every managed service. That is deliberate: the alternative, measured on a real stack after
connection multiplexing was added, was four resources on the new transport and eighteen still on the
old one — silently, with no way to tell from the outside and no way for them to recover, since
`--replace` would fix the transport by purging packages and deleting unit files.

So a `pulumi up` after a version bump is worth previewing rather than waving through — though it
should be uneventful, because of the rule below.

### An update that changes nothing does nothing

Every resource here reads the machine before it writes to it, and skips the write when the machine
already says what the code says. That is what keeps the paragraph above from being a problem: a
deployment caused only by this package changing finds everything already in place and touches
nothing.

It matters most for `SystemdUnit`, which used to `systemctl restart` on every update. Combined with
the provider check that would have meant every managed service restarting whenever anything in this
package was edited — including a comment, since a serialised closure carries the source text of what
it captures. A film stopping because somebody fixed a typo.

The one act that is not conditional on its own field is the restart of a service whose *unit file*
changed, even when it was already running: systemd goes on executing the definition the process was
started with, which is the classic "why has my edit not taken effect" afternoon.

## Changelog

### 0.1.0 — unreleased

First working version. Nineteen resources, a machine audit, and a transport that
multiplexes its ssh connections.

Full history in [CHANGELOG.md](CHANGELOG.md).

## Status

Early, and no longer only theoretical. It has been used to describe a real machine end to end —
packages, users and groups, systemd units and template instances, fstab entries, a symlinked
journal, swap disabled at both tenses, Samba shares, sudo rules and the boot line — and reports no
drift across a few dozen resources.

The resources exercised there are `AptPackages`, `ManagedFile`, `Directory`, `Symlink`,
`FstabEntry`, `User`, `SystemdUnit`, `SystemdInstance`, `SudoRule`, `Swap`, `Journald`,
`SambaShare`, `KernelCmdline`, `Precondition` and the audit. What that machine has not yet touched:
`Group`, `SambaUser`, `AptPackage` (the singular one), the refusals in `Swap` for btrfs and in
`Group` for renumbering a gid, and `SystemdInstance`'s error for a template that does not exist.

One refusal has been exercised deliberately rather than only reasoned about: an overlay name that
does not exist was declared on purpose, and `BootConfig` refused **before writing** — the file came
back byte-identical. On the file where being wrong means a card reader and another computer, the
ordering is the point: refuse, then write, never write and then report.

Four bugs in this repository were found by that machine and could not have been found without it.
The worst read every running, enabled unit as stopped and disabled, because `systemctl show
--value` returns properties in systemd's own order rather than the order they were asked for — and
on a machine where everything is stopped and disabled, the wrong answer and the right one are
identical, so every fixture agreed with the bug.

Everything else here is tested against parsing and command composition, which is where this
project's bugs have been, and proves nothing about what a machine does when it answers.

## Licence

MIT. See [LICENSE](LICENSE).
