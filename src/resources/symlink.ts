import * as pulumi from '@pulumi/pulumi';
import { escalate, ask, must, shellQuote, type Target, describe } from '../ssh.ts';
import { stamped, transportChanged, withLegacyAlias } from '../upgrade.ts';

/**
 * A symbolic link, and a `read` that can tell four things apart.
 *
 * The four states are the whole reason this is a resource rather than a one-line command: a link
 * pointing where the code says, a link pointing somewhere else, **something real at that path**, and
 * nothing at all. A resource that treats "the path exists" as success gets the third one wrong, and
 * the third one is the dangerous one.
 *
 * The case that produced it: `/var/log/journal` is meant to be a link onto an array, and if it is a
 * real directory instead then journald writes the system journal to the SD card — silently, for
 * ever, on a machine whose configuration all reads as correct. Nothing reports an error at any
 * point, and the only symptom is a card wearing out a year early.
 *
 * So a real directory or file where a link belongs **throws**, naming what is actually there. It
 * would be easy to replace it and it must not: whatever is in that directory is somebody's data,
 * and a deployment is not the moment to discover that a symlink resource deletes things.
 */
export interface SymlinkArgs {
  /** Where the link itself lives. */
  path: string;
  /** What it points at. Not required to exist — a link to a mount point that is not up yet is normal. */
  target: string;
}

interface SymlinkState {
  path: string;
  target: string;
}

/**
 * What an answer from the machine means, as a value rather than as control flow.
 *
 * The four states are the substance of this resource, and they were previously expressed only as
 * `if` statements inside a function that needs a machine — so the one thing worth testing could not
 * be. `9` is nothing there, `8` is something real in the way, `0` is a link and its target.
 */
export function interpretSymlink(
  code: number,
  out: string,
): { state: 'link'; target: string } | { state: 'absent' } | { state: 'occupied'; kind: string } {
  if (code === 9) return { state: 'absent' };
  if (code === 8) return { state: 'occupied', kind: out.trim() || 'something else' };
  return { state: 'link', target: out };
}

/**
 * What is at that path: the link's target, or null where there is nothing.
 *
 * Throws where something real is in the way, because that is a machine in a state the code did not
 * anticipate and every possible automatic answer to it is worse than stopping.
 */
export async function readSymlink(host: Target, path: string): Promise<SymlinkState | null> {
  const quoted = shellQuote(path);
  const asked = await ask(host, escalate(host,
    // readlink succeeds for a broken link too, which is right: a link pointing at a filesystem that
    // is not mounted yet is the ordinary case here, not an error
    `if pointsAt=$(readlink ${quoted} 2>/dev/null); then printf '%s' "$pointsAt"; exit 0; fi; ` +
    `if [ -e ${quoted} ] || [ -L ${quoted} ]; then stat -c '%F' ${quoted}; exit 8; fi; ` +
    `exit 9`,
  ));
  if (asked.code !== 0 && asked.code !== 8 && asked.code !== 9) {
    throw new Error(`could not read ${path}: ${asked.err.trim()}`);
  }
  const found = interpretSymlink(asked.code, asked.out);
  if (found.state === 'absent') return null;
  if (found.state === 'occupied') {
    throw new Error(
      `${path} on ${describe(host)} is a ${found.kind}, not a symlink. ` +
      `Refusing to replace it: whatever is in there is data this resource did not put there.`,
    );
  }
  return { path, target: found.target };
}

function providerFor(host: Target): pulumi.dynamic.ResourceProvider<SymlinkArgs, SymlinkState> {
  const link = async (args: SymlinkState): Promise<void> => {
    // readSymlink throws on anything real in the way, which is the check that has to happen before
    // -f is allowed anywhere near the path
    const current = await readSymlink(host, args.path);
    // already pointing where it should: an update caused by a provider upgrade does nothing
    if (current && current.target === args.target) return;
    const parent = args.path.replace(/\/[^/]*$/, '') || '/';
    await must(host, escalate(host,
      `mkdir -p ${shellQuote(parent)} && ` +
      // -n as well as -f: without it, relinking a link that points at a directory creates the new
      // link *inside* that directory instead of replacing it, which is the classic way to end up
      // with /var/log/journal/journal
      `ln -sfn ${shellQuote(args.target)} ${shellQuote(args.path)}`,
    ));
  };

  return {
    async create(args) {
      await link(args);
      return { id: args.path, outs: args };
    },

    async read(id, state) {
      const actual = await readSymlink(host, id);
      if (!actual) return { id: undefined, props: undefined };
      return { id, props: { ...state, ...actual } };
    },

    async update(id, _old, args) {
      const wanted = { ...args, path: id };
      await link(wanted);
      return { outs: wanted };
    },

    async diff(_id, old, args) {
      return {
        changes: transportChanged(old)
          || old.target !== args.target || old.path !== args.path,
        replaces: old.path !== args.path ? ['path'] : [],
        stables: [],
        deleteBeforeReplace: true,
      };
    },

    async delete(id) {
      // -h, and only when it is still a link: if something replaced it with a real directory since
      // the last deployment, removing the resource must not be the thing that deletes it
      await must(host, escalate(host, `test -L ${shellQuote(id)} && rm -f ${shellQuote(id)} || true`));
    },
  };
}

/** A symlink that is checked for what it actually is, not merely whether the path exists. */
export class Symlink extends pulumi.dynamic.Resource {
  declare readonly path: pulumi.Output<string>;
  declare readonly target: pulumi.Output<string>;

  constructor(name: string, host: Target, args: SymlinkArgs, opts?: pulumi.CustomResourceOptions) {
    super(stamped(providerFor(host)), name, args, withLegacyAlias(opts), 'homelab', 'Symlink');
  }
}
