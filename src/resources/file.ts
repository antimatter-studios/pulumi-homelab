import * as pulumi from '@pulumi/pulumi';
import { asRoot, ask, heredoc, must, shellQuote, type Host } from '../ssh';

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
}

interface FileState extends FileArgs {
  path: string;
  content: string;
  mode: string;
  owner: string;
  group: string;
}

const DEFAULTS = { mode: '0644', owner: 'root', group: 'root' } as const;

/** What the machine says is there now, or null where there is no such file. */
export async function readFile(host: Host, path: string): Promise<FileState | null> {
  // one round trip for all four questions: an ssh handshake costs far more than the work
  const asked = await ask(host, asRoot(
    `test -f ${shellQuote(path)} || exit 9; ` +
    `stat -c '%a %U %G' ${shellQuote(path)} && cat ${shellQuote(path)}`,
  ));
  if (asked.code === 9) return null;
  if (asked.code !== 0) throw new Error(`could not read ${path}: ${asked.err.trim()}`);

  const split = asked.out.indexOf('\n');
  const [mode = '', owner = '', group = ''] = asked.out.slice(0, split).trim().split(/\s+/);
  return {
    path,
    // stat gives '644' where we write '0644'; comparing those as strings would report drift on
    // every refresh for ever, on a file nobody had touched
    mode: mode.length === 3 ? `0${mode}` : mode,
    owner,
    group,
    content: asked.out.slice(split + 1),
  };
}

/** Put it there, exactly as described. */
export async function writeFile(host: Host, args: FileState): Promise<void> {
  const parent = args.path.replace(/\/[^/]*$/, '') || '/';
  await must(host, asRoot(
    `mkdir -p ${shellQuote(parent)} && ` +
    `${heredoc(args.path, args.content)}\n` +
    `chmod ${args.mode} ${shellQuote(args.path)} && ` +
    `chown ${args.owner}:${args.group} ${shellQuote(args.path)}`,
  ));
}

/**
 * The provider itself.
 *
 * `diff` is spelled out rather than left to Pulumi's structural comparison so that the reason for a
 * replacement is legible in a preview: moving a file is a different act from editing one, and only
 * the first needs the old one deleted.
 */
function providerFor(host: Host): pulumi.dynamic.ResourceProvider<FileArgs, FileState> {
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
      return { id, props: { ...state, ...actual } };
    },

    async update(id, _old, args) {
      const wanted = { ...DEFAULTS, ...args };
      await writeFile(host, wanted);
      return { outs: wanted };
    },

    async diff(_id, old, args) {
      const wanted = { ...DEFAULTS, ...args };
      const changed: string[] = [];
      if (old.content !== wanted.content) changed.push('content');
      if (old.mode !== wanted.mode) changed.push('mode');
      if (old.owner !== wanted.owner) changed.push('owner');
      if (old.group !== wanted.group) changed.push('group');
      return {
        changes: changed.length > 0 || old.path !== wanted.path,
        // a file at a new path is a new file; editing one in place is not
        replaces: old.path !== wanted.path ? ['path'] : [],
        stables: [],
        deleteBeforeReplace: false,
      };
    },

    async delete(id) {
      await must(host, asRoot(`rm -f ${shellQuote(id)}`));
    },
  };
}

/** A file that is kept as the code says it should be, and noticed when it is not. */
export class ManagedFile extends pulumi.dynamic.Resource {
  declare readonly path: pulumi.Output<string>;
  declare readonly content: pulumi.Output<string>;

  constructor(name: string, host: Host, args: FileArgs, opts?: pulumi.CustomResourceOptions) {
    super(providerFor(host), name, { mode: DEFAULTS.mode, owner: DEFAULTS.owner, group: DEFAULTS.group, ...args }, opts);
  }
}
