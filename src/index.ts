/**
 * homelab — Pulumi resources for a Linux machine you own.
 *
 * There is a gap between the tools that provision infrastructure and the tools that configure it.
 * Pulumi is excellent at the first and has nothing of its own for the second: `remote.Command` runs
 * shell over SSH and records that it ran, which is not the same as knowing what the machine is.
 * Ansible fills the gap and is a YAML dialect; NixOS closes it entirely and is a project in itself.
 *
 * This is the small answer. Desired state on a machine you own, over SSH, in TypeScript, with one
 * rule that decides everything else:
 *
 *   **Every resource implements a real `read`.**
 *
 * Not "it ran once", but "here is what the machine says right now". That is what makes
 * `pulumi up --refresh` mean something: a file somebody edited by hand, a service somebody stopped
 * last Tuesday, a package somebody removed — all of it comes back as drift you can see, instead of
 * the code and the machine parting company in silence.
 *
 * What it deliberately does not do is claim the whole machine. Anything not declared is left
 * exactly as it is, which is what makes it safe to point at a server that already works and has
 * been hand-tended for years. The cost of that is honest and worth stating: a machine managed this
 * way is reproducible in the parts you modelled and no further.
 */

export { ask, must, asRoot, shellQuote, heredoc, type Host, type Ran } from './ssh';
export { ManagedFile, readFile, writeFile, type FileArgs } from './resources/file';
export { AptPackage, readPackage, type AptPackageArgs } from './resources/apt';
export { SystemdUnit, readUnit, type SystemdUnitArgs } from './resources/systemd';
export { User, readUser, type UserArgs } from './resources/user';
