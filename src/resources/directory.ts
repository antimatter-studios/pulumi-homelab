import * as pulumi from '@pulumi/pulumi';
import { normaliseMode } from '../mode.ts';
import { escalate, ask, must, shellQuote, type Target, describe } from '../ssh.ts';
import { providerChanged, withLegacyAlias } from '../upgrade.ts';

/**
 * A directory that should exist, with its mode and ownership.
 *
 * It earns a resource of its own rather than being left to `ManagedFile`'s `mkdir -p`. A directory
 * that exists only as a side effect of writing a file into it has no state anybody reads: nothing
 * knows its mode, nothing notices when it is removed, and the code never said it should be there.
 * That is exactly the "it ran once" problem this provider exists to avoid, arrived at sideways.
 *
 * The asymmetry between creating and deleting is deliberate and is the whole design:
 *
 * - **Creating makes parents.** `/etc/rancher/k3s` needs `/etc/rancher`, and nobody should have to
 *   declare a path component that is an implementation detail of where something keeps its files.
 * - **Deleting removes only the leaf, and only when it is empty.** `rmdir`, never `rm -rf`. A
 *   directory with something in it is a machine saying that the code's picture of it is incomplete,
 *   and failing loudly is worth more than a tidy teardown that takes data with it. The parents made
 *   on the way up are left alone, because this resource never claimed them.
 */
export interface DirectoryArgs {
  path: string;
  /** Octal, as it is written everywhere else: '0755'. */
  mode?: string;
  owner?: string;
  group?: string;
}

interface DirectoryState extends DirectoryArgs {
  path: string;
  mode: string;
  owner: string;
  group: string;
}

const DEFAULTS = { mode: '0755', owner: 'root', group: 'root' } as const;

/**
 * What the machine says is at that path, or null where there is nothing.
 *
 * It asks what *kind* of thing is there rather than merely whether something is, because the two
 * failures need different answers. Nothing at all is a resource to create. A regular file sitting
 * where a directory is declared is a machine in a state the code did not anticipate — reporting
 * that as absent would send the next `up` into a `mkdir` that fails with something far less
 * useful than saying what is actually there.
 */
export async function readDirectory(host: Target, path: string): Promise<DirectoryState | null> {
  // -e is false for a broken symlink, which is still something in the way and has to be told apart
  // from an empty path
  const asked = await ask(host, escalate(host,
    `{ test -e ${shellQuote(path)} || test -L ${shellQuote(path)}; } || exit 9; ` +
    `stat -c '%F|%a|%U|%G' ${shellQuote(path)}`,
  ));
  if (asked.code === 9) return null;
  if (asked.code !== 0) throw new Error(`could not read ${path}: ${asked.err.trim()}`);

  const found = parseStat(asked.out);
  if (found.kind !== 'directory') {
    throw new Error(
      `${path} is a ${found.kind || 'something else'} on ${describe(host)}, not a directory: ` +
      `refusing to guess what to do with it`,
    );
  }
  return { path, mode: found.mode, owner: found.owner, group: found.group };
}

/**
 * `directory|755|root|root` → what it says, with the mode in the shape the code is written in.
 *
 * Separated out because reading state back is where this repo's bugs have been, and both of them
 * were a machine phrasing an answer differently from the code that declared it.
 */
export function parseStat(out: string): { kind: string; mode: string; owner: string; group: string } {
  const [kind = '', mode = '', owner = '', group = ''] = out.trim().split('|');
  return { kind, mode: normaliseMode(mode), owner, group };
}

/** Make it, and make it match. */
async function apply(host: Target, args: DirectoryState): Promise<void> {
  const path = shellQuote(args.path);
  // mkdir -p is also what makes this safe to run again: an existing directory is not an error, and
  // the chmod and chown after it are what actually settle the state either way
  await must(host, escalate(host,
    `mkdir -p ${path} && chmod ${args.mode} ${path} && chown ${args.owner}:${args.group} ${path}`,
  ));
}

function providerFor(host: Target): pulumi.dynamic.ResourceProvider<DirectoryArgs, DirectoryState> {
  return {
    async create(args) {
      const wanted = { ...DEFAULTS, ...args };
      await apply(host, wanted);
      return { id: args.path, outs: wanted };
    },

    async read(id, state) {
      const actual = await readDirectory(host, id);
      // gone from the machine: Pulumi drops it, and the next up puts it back
      if (!actual) return { id: undefined, props: undefined };
      return { id, props: { ...state, ...actual } };
    },

    async update(id, _old, args) {
      const wanted = { ...DEFAULTS, ...args, path: id };
      const current = await readDirectory(host, id);
      const same = current
        && current.mode === wanted.mode
        && current.owner === wanted.owner
        && current.group === wanted.group;
      if (!same) await apply(host, wanted);
      return { outs: wanted };
    },

    async diff(_id, old, args) {
      const wanted = { ...DEFAULTS, ...args };
      return {
        changes: providerChanged(old, args)
          || old.mode !== wanted.mode
          || old.owner !== wanted.owner
          || old.group !== wanted.group
          || old.path !== wanted.path,
        // a directory at a new path is a new directory; the old one is emptied by whatever declared
        // its contents and then removed
        replaces: old.path !== wanted.path ? ['path'] : [],
        stables: [],
        deleteBeforeReplace: false,
      };
    },

    async delete(id) {
      // rmdir, and no -p: only the leaf, and only when it is empty. Anything still in there is the
      // machine saying the code's picture of it is incomplete, which is worth a failed deployment
      await must(host, escalate(host, `rmdir ${shellQuote(id)}`));
    },
  };
}

/** A directory the machine should have, checked against `stat` rather than remembered. */
export class Directory extends pulumi.dynamic.Resource {
  declare readonly path: pulumi.Output<string>;
  declare readonly mode: pulumi.Output<string>;

  constructor(name: string, host: Target, args: DirectoryArgs, opts?: pulumi.CustomResourceOptions) {
    super(providerFor(host), name, { ...DEFAULTS, ...args }, withLegacyAlias(opts), 'homelab', 'Directory');
  }
}
