import * as pulumi from '@pulumi/pulumi';
import { normaliseMode } from '../mode.ts';
import { escalate, ask, heredoc, must, shellQuote, type Target, describe } from '../ssh.ts';
import { providerChanged, withLegacyAlias } from '../upgrade.ts';

/**
 * A file on the managed machine, with its content, owner and mode.
 *
 * This is the resource that makes the case for writing our own rather than shelling out through
 * `command.remote.Command`. A command has no state to read back: Pulumi knows it ran the command
 * once and nothing else, so if somebody edits the file on the box afterwards, nothing ever notices.
 *
 * A resource with a real `read` is a different thing. `pulumi refresh` calls it, it goes and looks
 * at the actual file, and the next `preview` shows the drift as a diff. That is the whole reason
 * this exists: on a machine you keep for years, the question worth answering is not "did I once
 * deploy this" but "does the machine still say what the code says".
 */
export interface FileArgs {
  path: string;
  content: string;
  /** Octal, as it is written everywhere else: '0644'. */
  mode?: string;
  owner?: string;
  group?: string;
  /**
   * Run `systemctl daemon-reload` after writing.
   *
   * For the case `SystemdUnit` refuses: a **static** unit, one with no `[Install]` section, cannot
   * be enabled and so cannot be described by that resource at all. Writing it as a file works and
   * reads back correctly, but systemd caches unit files and would go on running the old one —
   * the file managed, and the daemon never told.
   *
   * Off by default because most files are not units, and a reload on every deployment that touches
   * an unrelated file is noise.
   */
  reloadSystemd?: boolean;
}

interface FileState extends FileArgs {
  path: string;
  content: string;
  mode: string;
  owner: string;
  group: string;
  reloadSystemd: boolean;
}

const DEFAULTS = { mode: '0644', owner: 'root', group: 'root', reloadSystemd: false } as const;

/**
 * `644 root root` → the three answers, with the mode in the shape the code writes it in.
 *
 * Extracted for the same reason `Directory` extracted its own: reading state back is where this
 * package's bugs have been, and a parse buried in a read is one nothing can test.
 */
export function parseFileStat(out: string): { mode: string; owner: string; group: string } {
  const [mode = '', owner = '', group = ''] = out.trim().split(/\s+/);
  return { mode: normaliseMode(mode), owner, group };
}

/** What the machine says is there now, or null where there is no such file. */
export async function readFile(host: Target, path: string): Promise<Omit<FileState, 'reloadSystemd'> | null> {
  // one round trip for all four questions: an ssh handshake costs far more than the work
  const asked = await ask(host, escalate(host,
    `test -f ${shellQuote(path)} || exit 9; ` +
    `stat -c '%a %U %G' ${shellQuote(path)} && cat ${shellQuote(path)}`,
  ));
  if (asked.code === 9) return null;
  if (asked.code !== 0) throw new Error(`could not read ${path}: ${asked.err.trim()}`);

  const split = asked.out.indexOf('\n');
  return {
    path,
    ...parseFileStat(asked.out.slice(0, split)),
    content: asked.out.slice(split + 1),
  };
}

/** Put it there, exactly as described. */
export async function writeFile(host: Target, args: Omit<FileState, 'reloadSystemd'> & { reloadSystemd?: boolean }): Promise<void> {
  const parent = args.path.replace(/\/[^/]*$/, '') || '/';
  await must(host, escalate(host,
    `mkdir -p ${shellQuote(parent)} && ` +
    `${heredoc(args.path, args.content)}\n` +
    `chmod ${args.mode} ${shellQuote(args.path)} && ` +
    `chown ${args.owner}:${args.group} ${shellQuote(args.path)}` +
    (args.reloadSystemd ? ' && systemctl daemon-reload' : ''),
  ));
}

/**
 * The provider itself.
 *
 * `diff` is spelled out rather than left to Pulumi's structural comparison so that the reason for a
 * replacement is legible in a preview: moving a file is a different act from editing one, and only
 * the first needs the old one deleted.
 */
function providerFor(host: Target): pulumi.dynamic.ResourceProvider<FileArgs, FileState> {
  return {
    async create(args) {
      const wanted = { ...DEFAULTS, ...args };
      await writeFile(host, wanted);
      return { id: args.path, outs: wanted };
    },

    async read(id, state) {
      const actual = await readFile(host, id);
      // gone from the machine entirely: Pulumi drops it from the state and the next up recreates it
      if (!actual) return { id: undefined, props: undefined };
      // reloadSystemd first: it is a behaviour rather than a fact about the file, so the read
      // cannot supply it and the covering spread has to come from somewhere
      return { id, props: { reloadSystemd: state?.reloadSystemd ?? false, ...state, ...actual } };
    },

    async update(id, _old, args) {
      const wanted = { ...DEFAULTS, ...args };
      const current = await readFile(host, id);
      // an update that changes nothing must do nothing. Rewriting identical content is not harmless
      // here: it moves the mtime, and with reloadSystemd it reloads the daemon for no reason
      const same = current
        && current.content === wanted.content
        && current.mode === wanted.mode
        && current.owner === wanted.owner
        && current.group === wanted.group;
      if (!same) await writeFile(host, wanted);
      return { outs: wanted };
    },

    async diff(_id, old, args) {
      const wanted = { ...DEFAULTS, ...args };
      const changed: string[] = [];
      if (old.content !== wanted.content) changed.push('content');
      if (old.mode !== wanted.mode) changed.push('mode');
      if (old.owner !== wanted.owner) changed.push('owner');
      if (old.group !== wanted.group) changed.push('group');
      if (old.reloadSystemd !== wanted.reloadSystemd) changed.push('reloadSystemd');
      return {
        changes: providerChanged(old, args)
          || changed.length > 0 || old.path !== wanted.path,
        // a file at a new path is a new file; editing one in place is not
        replaces: old.path !== wanted.path ? ['path'] : [],
        stables: [],
        deleteBeforeReplace: false,
      };
    },

    async delete(id) {
      await must(host, escalate(host, `rm -f ${shellQuote(id)}`));
    },
  };
}

/** A file that is kept as the code says it should be, and noticed when it is not. */
export class ManagedFile extends pulumi.dynamic.Resource {
  declare readonly path: pulumi.Output<string>;
  declare readonly content: pulumi.Output<string>;

  constructor(name: string, host: Target, args: FileArgs, opts?: pulumi.CustomResourceOptions) {
    super(providerFor(host), name, { ...DEFAULTS, ...args }, withLegacyAlias(opts), 'homelab', 'ManagedFile');
  }
}
