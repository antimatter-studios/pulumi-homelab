import * as pulumi from '@pulumi/pulumi';
import { asRoot, escalate, ask, type Target, describe } from '../ssh.ts';
import { providerChanged, withLegacyAlias } from '../upgrade.ts';

/**
 * A condition the machine has to satisfy before anything downstream is allowed to run.
 *
 * The case that produced it: a Raspberry Pi ships with the memory cgroup controller switched off,
 * and k3s will not start without it. Turning it on is a line in the boot configuration and a
 * reboot — and a deployment that reboots the machine underneath itself is a worse idea than one
 * that stops and says what to do. So the resource that changes the boot line is one thing, and this
 * is the other half: it asks the running kernel whether the change has actually taken, and refuses
 * to let the cluster be installed on a machine that will not run it.
 *
 * The general shape is worth having on its own. Every machine has some fact that is true only after
 * a human does something — a disk mounted, a key installed, firmware updated, a reboot taken — and
 * the alternative to modelling it is a deployment that half-succeeds and leaves a failure three
 * resources further on, pointing at the wrong thing.
 *
 * **One condition per resource, not one compound check.** The temptation is to `&&` several
 * questions together and explain all the causes in one message, and it is worth resisting: a reader
 * who has to work out which half of the check failed is doing the diagnosis the resource existed to
 * do for them. The cgroup case ends up as two — one asking whether the machine *booted* with the
 * parameters, whose fix is a reboot, and one asking whether the controller is *actually on*, whose
 * fix is the firmware and not the file. Chained with `dependsOn`, they fail in the order somebody
 * would investigate, and each says the one thing to do about it.
 *
 * What makes it a resource rather than an assertion in the program is the `read`. When the
 * condition stops being true — the machine is rebooted back without the flags, somebody unmounts
 * the disk — a refresh reports the resource as gone, and the next `up` runs the check again and
 * fails with the message. An assertion at the top of the program cannot do that, because it runs
 * against Pulumi's memory of the world rather than the world.
 */
export interface PreconditionArgs {
  /**
   * A command that exits zero when the condition holds.
   *
   * It must only ask, never change anything: it runs on every create, every update and every
   * refresh, so anything with a side effect here happens at times nobody chose.
   */
  check: string;
  /**
   * What to tell whoever is running the deployment when the check fails.
   *
   * The message is the whole value of the resource, so it should say what to do rather than what
   * went wrong — 'run `sudo reboot` and deploy again', not 'cgroups missing'. It is the last thing
   * a person reads before they have to work out the fix themselves.
   */
  message: string;
  /** Whether the check needs root. Off by default: a question that needs root usually wants rewriting. */
  root?: boolean;
}

interface PreconditionState {
  check: string;
  message: string;
  root: boolean;
}

const DEFAULTS = { root: false } as const;

/**
 * The command as it is actually sent.
 *
 * Both `create` and `read` go through here, which is the point: a check that is written one way
 * when it gates a deployment and another way when it refreshes would be a resource that passes and
 * then reports itself missing, for ever.
 *
 * The host is optional so that composing a check string without one still works, which is how the
 * exported helpers are usually used. Passing it is what lets a machine that connects as root, or
 * carries a sudo password, run a check that needs privilege.
 */
export function checkCommand(args: Pick<PreconditionState, 'check' | 'root'>, host?: Target): string {
  if (!args.root) return args.check;
  return host ? escalate(host, args.check) : asRoot(args.check);
}

/** Whether the machine currently satisfies it. */
export async function readPrecondition(host: Target, args: Pick<PreconditionState, 'check' | 'root'>): Promise<boolean> {
  // a non-zero exit is the answer 'no', not a fault: `ask` rather than `must`. ssh's own 255 still
  // throws from in there, because 'I could not reach the machine' is not the same answer as 'the
  // machine says no' and must not be reported as one
  const asked = await ask(host, checkCommand(args, host));
  return asked.code === 0;
}

function providerFor(host: Target): pulumi.dynamic.ResourceProvider<PreconditionArgs, PreconditionState> {
  const insist = async (args: PreconditionState): Promise<void> => {
    if (!(await readPrecondition(host, args))) throw new Error(args.message);
  };

  return {
    async create(args) {
      const wanted = { ...DEFAULTS, ...args };
      await insist(wanted);
      // the check itself is the id, so two preconditions asking the same question of the same
      // machine are the same resource however they are named in the program
      return { id: wanted.check, outs: wanted };
    },

    async read(id, state) {
      const wanted: PreconditionState = { check: id, message: state?.message ?? id, root: state?.root ?? DEFAULTS.root };
      // no longer true: Pulumi drops it, and the next up runs the check again and fails with the
      // message rather than building on a machine that has quietly stopped qualifying
      if (!(await readPrecondition(host, wanted))) return { id: undefined, props: undefined };
      return { id, props: wanted };
    },

    async update(id, _old, args) {
      const wanted = { ...DEFAULTS, ...args, check: id };
      await insist(wanted);
      return { outs: wanted };
    },

    async diff(_id, old, args) {
      const wanted = { ...DEFAULTS, ...args };
      return {
        changes: providerChanged(old, args)
          || old.check !== wanted.check || old.message !== wanted.message || old.root !== wanted.root,
        // a different question is a different precondition, and has to be asked before whatever
        // depends on it is touched rather than after
        replaces: old.check !== wanted.check ? ['check'] : [],
        stables: [],
        deleteBeforeReplace: true,
      };
    },

    async delete() {
      // nothing was created, so there is nothing to undo. Deleting a precondition means the program
      // stopped caring about the condition, not that the machine should stop satisfying it.
    },
  };
}

/** Something that must be true of the machine before the resources that depend on it are built. */
export class Precondition extends pulumi.dynamic.Resource {
  declare readonly check: pulumi.Output<string>;

  constructor(name: string, host: Target, args: PreconditionArgs, opts?: pulumi.CustomResourceOptions) {
    super(providerFor(host), name, { ...DEFAULTS, ...args }, withLegacyAlias(opts), 'homelab', 'Precondition');
  }
}
