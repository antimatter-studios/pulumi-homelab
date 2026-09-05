# pulumi-homelab

Pulumi resources for a Linux machine you own. Desired state over SSH, in TypeScript, with real
drift detection.

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

## What it does not do

It does not claim the whole machine. Anything you do not declare is left exactly as it is, which is
what makes it safe to point at a server that already works and has been hand-tended for years, and
what makes adopting one incremental rather than a rebuild.

The cost is worth stating plainly: a machine managed this way is reproducible **in the parts you
modelled and no further**. If you need "rebuild from bare metal and get the same box", you need to
model everything — and at that point you are describing NixOS, which already does it.

## Resources

| Resource | Reads state from |
|---|---|
| `ManagedFile` | `stat` and `cat` |
| `AptPackage` | `dpkg-query` |
| `SystemdUnit` | `systemctl show` and the unit file |
| `User` | `getent passwd` and `id -nG` |

## Using it

```ts
import { AptPackage, SystemdUnit, type Host } from 'pulumi-homelab';

const host: Host = { address: '192.168.0.47', user: 'chris' };

const node = new AptPackage('nodejs', host, { name: 'nodejs', update: true });
```

Always run with `--refresh`. A bare `pulumi up` compares your code against Pulumi's *memory* of the
machine rather than the machine itself, which is the one way to make all of this pointless.

## Status

Early. The resources work and are tested, but this has not yet been run against a machine in anger.

## Licence

Apache-2.0
