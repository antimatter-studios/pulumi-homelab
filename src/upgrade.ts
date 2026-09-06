import * as pulumi from '@pulumi/pulumi';

/**
 * Noticing that the provider itself has changed.
 *
 * Pulumi serialises a dynamic provider's whole closure — the transport, the quoting, every resource
 * method — into `__provider` in the state file, and `read`, `diff` and `update` all run **the stored
 * closure rather than the current source**. A resource created last month is still running last
 * month's `ssh.ts`.
 *
 * That is fine until something in the transport is fixed. Then the fix reaches new resources and
 * nothing else, because every `diff` here compares only its own fields and reports `changes: false`
 * when the machine matches — which is correct for the resource's own semantics and also, silently,
 * suppresses the upgrade. Measured on a real stack after connection multiplexing was added: four
 * resources carried the new transport and eighteen carried the old one, still opening one connection
 * each and still tripping sshd's startup limit, months of fixes away from the code that describes
 * them.
 *
 * Nothing reports it, and the resources cannot recover on their own. `--replace` would fix the
 * transport by deleting and recreating, which for these resources means purging packages and
 * removing unit files — an absurd trade for a connection option.
 *
 * So every `diff` in this package asks this as well. The cost is real and belongs in a preview
 * rather than in a surprise: the first deployment after a transport change updates every resource,
 * and for `SystemdUnit` that means every managed service restarts.
 */
export function providerChanged(old: unknown, args: unknown): boolean {
  const before = (old as { __provider?: unknown } | undefined)?.__provider;
  const after = (args as { __provider?: unknown } | undefined)?.__provider;
  // only when both are known: a missing value means the framework did not hand it over, and forcing
  // an update on that guess would restart every service on the machine for no reason at all
  if (typeof before !== 'string' || typeof after !== 'string') return false;
  return before !== after;
}

/**
 * The alias that stops a type change from becoming a rebuild.
 *
 * Every resource in this package used to be `pulumi-nodejs:dynamic:Resource`, the default type for
 * a dynamic resource, and now declares its own. That is worth doing — with the type segment
 * constant, the *name* was the only discriminator in a URN, so `new User('player', …)` beside
 * `new SystemdUnit('player', …)` was a hard `Duplicate resource URN` failure between two things with
 * nothing in common, and `pulumi stack` was thirty-six lines that all said the same type.
 *
 * But a URN carries the type, so changing it makes Pulumi see the old resource as gone and the new
 * one as never having existed: delete and create. For this provider a delete purges packages,
 * removes unit files and unmasks `dphys-swapfile`. The alias is what turns that into a rename in
 * the state file and nothing at all on the machine.
 *
 * It is effectively permanent. Removing it later would be a rebuild for anyone who had not deployed
 * in between, which is exactly the person least likely to be watching.
 */
export function withLegacyAlias(opts?: pulumi.CustomResourceOptions): pulumi.CustomResourceOptions {
  return {
    ...opts,
    // the caller's own aliases come after ours, so a stack that has already been renamed by hand
    // keeps whatever it was relying on
    aliases: [{ type: 'pulumi-nodejs:dynamic:Resource' }, ...(opts?.aliases ?? [])],
  };
}
