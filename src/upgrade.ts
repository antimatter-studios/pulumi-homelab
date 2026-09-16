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
/**
 * The provider's revision, bumped deliberately.
 *
 * **It is not only about the transport, whatever its name says.** The field in state is `transport`
 * and cannot be renamed without every stamped resource reading as unstamped, so the name is stuck —
 * but the thing it governs is wider: *anything* a resource does after connecting is in the
 * serialised closure too. Its `read`, its `diff`, the shape of its state. Change one of those and
 * existing resources go on running the old one until this number moves.
 *
 * **That has already been got wrong once, which is why the rule below is blunt.** Three merged
 * changes altered what a resource reads — Samba reading the file instead of `testparm`, `SshKey`
 * asserting the mode on both halves, `AptPackages` marking what it declares as manual — and none of
 * them bumped this, because none of them touched the transport. All three were correct, merged,
 * tested, and inert on every machine that already had those resources. The apt-mark fix was
 * reported as "merged and correct, and on my machine it has never executed".
 *
 * So: **bump it for any change that must reach resources that already exist.** A quoting fix, a
 * connection option, a security fix, a different `read`, a new state field, a corrected `diff`. The
 * question is never "did I touch the transport" but "would a resource created last month still be
 * wrong". `scripts/serialise-check.ts` asks it mechanically now rather than relying on somebody
 * remembering.
 *
 * **This replaces comparing the serialised closure, which could not work.** The first attempt at
 * this compared `__provider` in state against the program's — and `update` does not persist a new
 * `__provider`, so once the two differed they differed for ever: the resource updated on every
 * deployment, the stored value never moved, and only the resources whose state happened to be
 * written at an older version were affected. Ten of them, on one real stack, writing to the machine
 * on every run — a Samba reload and an avahi restart among them — while `pulumi up` could never
 * answer "nothing to do".
 *
 * It also fired on edits that changed nothing about behaviour. The closure carries the source text
 * of everything it captures, so a comment added to a doc block made every resource in every stack
 * report an update.
 *
 * A number is better on both counts. It is data, so it survives serialisation; it is carried in each
 * resource's own state, so an update persists it and the upgrade completes; and it moves only when
 * somebody decides it should.
 *
 * ## Revisions
 *
 * - **1** — the mechanism itself, replacing the closure comparison.
 * - **2** — Samba reads the file rather than `testparm`; `SshKey` owns both halves' mode and
 *   ownership; `AptPackage` and `AptPackages` mark what they declare as manual; seven `delete`
 *   methods read their layout path from state; `Directory` takes `setgid` and `sticky`.
 * - **3** — the control socket's name includes `SSH_AUTH_SOCK`, so swapping agents between runs
 *   cannot reuse a master authenticated by the previous one. Folded in before revision 2 had been
 *   deployed anywhere, so it costs one update cycle rather than two.
 */
export const TRANSPORT = 3;

/**
 * Whether this resource's state was written before the current transport.
 *
 * Unstamped state answers **false**: a resource created before this mechanism existed has nothing to
 * compare against, and guessing would mean updating every resource on every machine once, which is
 * the failure this replaces. Those resources pick up the stamp the first time anything else about
 * them genuinely changes.
 */
export function transportChanged(old: unknown): boolean {
  const stamp = (old as { transport?: unknown } | undefined)?.transport;
  if (typeof stamp !== 'number') return false;
  return stamp !== TRANSPORT;
}

/**
 * Wrap a provider so everything it stores carries the transport's version.
 *
 * One place rather than in every `create`, `read` and `update` return, because the stamp is only
 * useful if it is on *all* of them: a resource that stamps on create and not on update never
 * completes the upgrade it was meant to enable, which is exactly how the previous mechanism failed.
 */
export function stamped<Inputs, Outputs>(
  provider: pulumi.dynamic.ResourceProvider<Inputs, Outputs>,
): pulumi.dynamic.ResourceProvider<Inputs, Outputs> {
  const stamp = (value: unknown): Outputs =>
    (value === undefined ? value : { ...(value as object), transport: TRANSPORT }) as Outputs;

  return {
    ...provider,
    create: async (inputs) => {
      const made = await provider.create(inputs);
      return { ...made, outs: stamp(made.outs) };
    },
    ...(provider.read
      ? {
          read: async (id: string, props?: Outputs) => {
            const found = await provider.read!(id, props);
            return found.props === undefined ? found : { ...found, props: stamp(found.props) };
          },
        }
      : {}),
    ...(provider.update
      ? {
          update: async (id: string, olds: Outputs, news: Inputs) => {
            const done = await provider.update!(id, olds, news);
            return { ...done, outs: stamp(done.outs) };
          },
        }
      : {}),
  };
}

/**
 * Kept so a consumer's own dynamic resources can ask the same question, and deprecated.
 *
 * @deprecated Compare `transportChanged(old)` instead. This compared the serialised closure, which
 * an update does not re-persist, so a resource whose stored text differed reported an update for
 * ever without the difference ever being resolved.
 */
export function providerChanged(old: unknown, args: unknown): boolean {
  const before = (old as { __provider?: unknown } | undefined)?.__provider;
  const after = (args as { __provider?: unknown } | undefined)?.__provider;
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
