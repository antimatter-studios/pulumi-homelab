import * as pulumi from '@pulumi/pulumi';
import { escalate, ask, heredoc, must, shellQuote, type Target, describe } from '../ssh.ts';
import { stamped, transportChanged, withLegacyAlias } from '../upgrade.ts';

/**
 * The parameters the machine boots with.
 *
 * The case that produced it: Raspberry Pi OS ships with the memory cgroup controller switched off,
 * and k3s cannot start without it. The failure arrives as a container runtime error some way into
 * the boot, which reads as a k3s problem rather than a kernel one, and costs an afternoon to trace.
 * Turning it on is two parameters on the kernel command line, and they take effect at the next boot
 * and not before.
 *
 * That reboot is the awkward part, and it is why this resource does only half the job. A resource
 * that rebooted the machine would kill the ssh connection in the middle of a deployment and leave
 * Pulumi unable to say what it had and had not finished. So the file is written honestly as a file,
 * and whether the running kernel actually has the parameters is a separate question, asked by
 * `Precondition` with the command `bootedWith` builds. Deploy, reboot when it tells you to, deploy
 * again, and the second run carries straight on.
 *
 * It edits the boot partition's `cmdline.txt`, which is how the Raspberry Pi's bootloader is
 * configured. A machine that boots through GRUB keeps the same idea in an entirely different place
 * and wants its own resource rather than an argument here — the two have nothing in common but the
 * words "kernel command line".
 */

/**
 * Where the file lives, in the order to look.
 *
 * Bookworm moved the boot partition to `/boot/firmware` and left a compatibility symlink for a
 * while; older images have it directly in `/boot`. Editing the wrong one is silent — the write
 * succeeds, the bootloader reads the other file, and nothing changes across as many reboots as you
 * care to try.
 */
export const CANDIDATES = ['/boot/firmware/cmdline.txt', '/boot/cmdline.txt'];

export interface KernelCmdlineArgs {
  /** Whole parameters as the bootloader wants them: `['cgroup_memory=1', 'cgroup_enable=memory']`. */
  flags: string[];
  /** Where the file is, for an image that keeps it somewhere neither usual place would find. */
  path?: string;
}

interface KernelCmdlineState {
  flags: string[];
  path: string;
  /** The whole line as it now stands, so a hand edit anywhere in it shows up as drift. */
  cmdline: string;
}

/** The key half of `name=value`, or the whole word for a bare parameter like `quiet`. */
const keyOf = (parameter: string) => parameter.split('=', 1)[0] ?? parameter;

/**
 * Set the parameters that are asked for, and change nothing else.
 *
 * This deliberately does not rebuild the line from a template. It carries `root=PARTUUID=…`,
 * `rootfstype`, the console and whatever else this particular image was written with; a regenerated
 * `cmdline.txt` that gets any of that wrong is a machine that does not boot and cannot be fixed over
 * ssh, only by moving the card to another computer.
 *
 * A parameter whose key is already on the line is **replaced in place** rather than left alone. That
 * distinction is the whole correctness of this function: a Pi that already says `cgroup_memory=0` is
 * precisely the machine somebody is trying to fix, and a merge that treated the key as present would
 * write nothing, while the check on `/proc/cmdline` — which is anchored tightly enough to reject
 * `cgroup_memory=0` — went on failing. The deployment would then ask for a reboot that could never
 * satisfy it, for ever.
 *
 * Replacing in place rather than appending keeps whatever ordering the image was written with, and
 * keeps the result stable across runs: merging twice gives the same line as merging once.
 */
export function merge(current: string, flags: string[]): string {
  const parameters = current.trim().split(/\s+/).filter(Boolean);
  const wanted = new Map(flags.map((flag) => [keyOf(flag), flag]));
  const replaced = new Set<string>();

  const kept = parameters.map((parameter) => {
    const key = keyOf(parameter);
    const wants = wanted.get(key);
    if (wants === undefined) return parameter;
    replaced.add(key);
    return wants;
  });

  // anything the line never mentioned goes on the end, in the order it was asked for
  const missing = flags.filter((flag) => !replaced.has(keyOf(flag)));
  return [...kept, ...missing].join(' ');
}

/** Find the file and read it, or null when this is not a machine that has one. */
export async function readCmdline(host: Target, path?: string): Promise<{ path: string; cmdline: string } | null> {
  const candidates = path ? [path] : CANDIDATES;
  const tests = candidates.map((candidate) =>
    `test -f ${shellQuote(candidate)} && { echo ${shellQuote(candidate)}; cat ${shellQuote(candidate)}; exit 0; }`);
  const asked = await ask(host, escalate(host, `${tests.join('; ')}; exit 9`));
  if (asked.code === 9) return null;
  if (asked.code !== 0) throw new Error(`could not read the kernel command line file: ${asked.err.trim()}`);
  const split = asked.out.indexOf('\n');
  return { path: asked.out.slice(0, split).trim(), cmdline: asked.out.slice(split + 1).trim() };
}

function providerFor(host: Target): pulumi.dynamic.ResourceProvider<KernelCmdlineArgs, KernelCmdlineState> {
  const apply = async (args: KernelCmdlineArgs): Promise<KernelCmdlineState> => {
    const found = await readCmdline(host, args.path);
    if (!found) {
      const looked = args.path ?? CANDIDATES.join(' or ');
      throw new Error(`no kernel command line file at ${looked} on ${describe(host)}: this is not an image that boots that way`);
    }
    const cmdline = merge(found.cmdline, args.flags);
    if (cmdline !== found.cmdline) {
      // the bootloader reads one line and stops, so the file has to stay one line. It is also on a
      // vfat partition, where the ownership and mode a ManagedFile would set mean nothing — which is
      // half the reason this is its own resource rather than a file with clever content
      await must(host, escalate(host, heredoc(found.path, cmdline)));
    }
    return { flags: args.flags, path: found.path, cmdline };
  };

  return {
    async create(args) {
      const state = await apply(args);
      return { id: state.path, outs: state };
    },

    async read(id, state) {
      const found = await readCmdline(host, id);
      if (!found) return { id: undefined, props: undefined };
      // an import brings no previous state, and the machine cannot say which parameters were ours
      // rather than the image's — so the flags come from the source on the next up, not from here
      return { id, props: { flags: state?.flags ?? [], path: found.path, cmdline: found.cmdline } };
    },

    async update(id, _old, args) {
      return { outs: await apply({ ...args, path: args.path ?? id }) };
    },

    async diff(_id, old, args) {
      // compared against what is actually on the line rather than against the argument list:
      // somebody adding an unrelated parameter by hand is not drift this resource should undo, and
      // reporting it as a change every time would train everybody to ignore the diff
      const wanted = merge(old.cmdline, args.flags);
      return {
        changes: transportChanged(old) || wanted !== old.cmdline,
        replaces: [],
        stables: ['path'],
        deleteBeforeReplace: false,
      };
    },

    async delete() {
      // taking the parameters away again would stop k3s starting at the next boot, on a machine that
      // is by then running a cluster. Deleting this resource means "stop describing the boot line",
      // not "put the machine back the way it was".
    },
  };
}

/** The parameters the machine should boot with, added to whatever it already boots with. */
export class KernelCmdline extends pulumi.dynamic.Resource {
  declare readonly path: pulumi.Output<string>;
  declare readonly cmdline: pulumi.Output<string>;

  constructor(name: string, host: Target, args: KernelCmdlineArgs, opts?: pulumi.CustomResourceOptions) {
    super(stamped(providerFor(host)), name, { path: undefined, cmdline: undefined, ...args }, withLegacyAlias(opts), 'homelab', 'KernelCmdline');
  }
}

/** ERE metacharacters, so a parameter is matched as the text it is rather than as a pattern. */
const escaped = (text: string) => text.replace(/[.[\]{}()*+?^$|\\]/g, '\\$&');

/**
 * A command that exits zero only when the running kernel really has these parameters.
 *
 * `/proc/cmdline` is the kernel reporting what it was actually started with, and it is the only
 * thing that settles the question. The file on the boot partition is a statement of intent for next
 * time, and agrees with reality only after a reboot.
 *
 * Matching is anchored to whole words on both sides. A bare `grep cgroup_memory` is satisfied by
 * `cgroup_memory=0` — the exact setting somebody would be trying to correct — and by any longer
 * parameter that happens to contain the name.
 *
 * Pair it with `Precondition`, which is what turns the answer into a deployment that stops with an
 * instruction rather than one that builds a cluster on a kernel that cannot run it.
 *
 * **What it proves, and what it does not.** This asks what the kernel was *told*, which is not
 * always what the kernel *did*. A Raspberry Pi 5 puts firmware parameters ahead of the ones in
 * `cmdline.txt`, and one observed machine boots with `cgroup_disable=memory` from the firmware and
 * `cgroup_enable=memory` from the file, on the same line. The kernel takes the later parameter, so
 * that machine is correct — but a line with those two the other way round would satisfy this check
 * while the controller stayed off.
 *
 * Where the effect can be observed directly, check the effect instead: for cgroups that is
 * `grep -qw memory /sys/fs/cgroup/cgroup.controllers`, which is the kernel reporting what it
 * actually enabled rather than what it was asked to. That belongs in the caller's `Precondition`
 * rather than here, because the parameter and the thing it switches on have no general relationship
 * this function could know about.
 */
export function bootedWith(flags: string[]): string {
  return flags
    .map((flag) => `grep -qE '(^| )${escaped(flag)}( |$)' /proc/cmdline`)
    .join(' && ');
}
