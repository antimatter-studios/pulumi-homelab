import * as pulumi from '@pulumi/pulumi';
import { normaliseMode } from '../mode.ts';
import { escalate, ask, heredocInto, must, shellQuote, type Target, describe } from '../ssh.ts';
import { providerChanged, withLegacyAlias } from '../upgrade.ts';

/**
 * A sudo privilege, as a file in `/etc/sudoers.d`.
 *
 * **This cannot bootstrap itself, and that is worth understanding before reaching for it.** Every
 * resource here runs its work through `sudo -n`, so writing a sudoers file already requires the
 * privilege a sudoers file grants. What this manages is sudo for *other* accounts — a service that
 * needs one specific command, a second administrator, a group — and the recording of an arrangement
 * that already exists so it stops being folklore. The first passwordless sudo on a machine is a
 * person with a password, and nothing declarative can be the thing that creates it.
 *
 * It is separate from `User` on purpose. A privilege is not a property of an account: it can name a
 * group rather than a user, it can be granted to somebody this program does not manage, and it lives
 * in a different file with entirely different rules about mode and ownership. One resource owning
 * both would be two resources sharing a name.
 *
 * Three things about `/etc/sudoers.d` that this exists to get right, all of which fail silently or
 * catastrophically when done by hand:
 *
 * - **A bad file breaks sudo for everybody**, including the connection managing the machine. So the
 *   content is validated with `visudo -c` in a temporary file and only installed if it parses. A
 *   locked-out machine is not drift; it is a trip to wherever the machine physically is.
 * - **sudo ignores any file whose name contains a dot or a tilde.** `admin.conf` in that directory
 *   is not a rule, it is a file nobody reads and no error anywhere. The name is checked rather than
 *   trusted.
 * - **sudo rejects the file unless it is mode 0440 and owned by root**, so the mode is set *and*
 *   read back rather than applied and forgotten.
 */
export interface SudoRuleArgs {
  /** The account this is about. Exactly one of `user` or `group`. */
  user?: string;
  /** A group instead, written `%wheel` in sudoers and given here without the `%`. */
  group?: string;
  /**
   * Whether the commands run without a password.
   *
   * Deliberately required rather than defaulted. This is the argument that decides whether the rule
   * is a convenience or a standing grant of root to anything that can reach the account, and a
   * default would let it be chosen by not thinking about it.
   */
  passwordless: boolean;
  /** What may be run, as sudoers spells it. Full paths, or `['ALL']`. */
  commands?: string[];
  /** Who the commands may be run as. */
  runAs?: string;
  /** The file's name in the drop-in directory. Defaults to the resource's own name. */
  file?: string;
  /** Which drop-in directory, for a machine that does not keep it at `/etc/sudoers.d`. */
  directory?: string;
}

interface SudoRuleState {
  file: string;
  directory: string;
  content: string;
  mode: string;
  owner: string;
}

const DIRECTORY = '/etc/sudoers.d';
/** sudo insists on this, and refuses the file with an error at every invocation if it differs. */
const MODE = '0440';
const DEFAULTS = { commands: ['ALL'], runAs: 'ALL' } as const;

const pathOf = (file: string, directory = DIRECTORY) => `${directory}/${file}`;

/**
 * Check a drop-in file name, because sudo will not.
 *
 * `#includedir` skips any file whose name contains a `.` or ends with `~`, on the reasoning that
 * those are editor backups and package manager leftovers. It does this silently: no warning, no
 * error, and a rule that simply never applies. Somebody then spends an afternoon on why their
 * NOPASSWD line does nothing.
 */
export function sudoersFileName(name: string): string {
  if (name.length === 0) throw new Error('a sudoers drop-in needs a name');
  if (name.includes('.') || name.includes('~')) {
    throw new Error(`sudo ignores files in ${DIRECTORY} whose name contains '.' or '~', so '${name}' would never apply`);
  }
  if (name.includes('/')) throw new Error(`'${name}' is a path, and a sudoers drop-in is a single file name`);
  return name;
}

/**
 * The rule itself.
 *
 * One line, in the form sudoers has used for decades: who, on which hosts, as whom, and what. The
 * `NOPASSWD:` tag applies to the command list that follows it, which is why it goes there and not
 * next to the user.
 */
export function sudoersLine(args: SudoRuleArgs): string {
  if ((args.user === undefined) === (args.group === undefined)) {
    throw new Error('a sudo rule names exactly one of a user or a group');
  }
  const who = args.user ?? `%${args.group}`;
  const commands = (args.commands ?? DEFAULTS.commands).join(', ');
  const tag = args.passwordless ? 'NOPASSWD: ' : '';
  return `${who} ALL=(${args.runAs ?? DEFAULTS.runAs}) ${tag}${commands}`;
}

/** The whole file, with the note that explains itself to whoever finds it. */
export function sudoersFile(args: SudoRuleArgs): string {
  return `# Managed by Pulumi. Hand edits show up as drift on the next \`pulumi up --refresh\`.\n${sudoersLine(args)}\n`;
}

/** What the machine has there now, or null when there is no such rule. */
export async function readSudoRule(host: Target, file: string, directory = DIRECTORY): Promise<SudoRuleState | null> {
  const path = pathOf(file, directory);
  const asked = await ask(host, escalate(host,
    `test -f ${shellQuote(path)} || exit 9; ` +
    `stat -c '%a %U' ${shellQuote(path)} && cat ${shellQuote(path)}`,
  ));
  if (asked.code === 9) return null;
  if (asked.code !== 0) throw new Error(`could not read ${path}: ${asked.err.trim()}`);

  const split = asked.out.indexOf('\n');
  const [mode = '', owner = ''] = asked.out.slice(0, split).trim().split(/\s+/);
  return { file, directory, content: asked.out.slice(split + 1), mode: normaliseMode(mode), owner };
}

/**
 * Write it, but only once sudo itself agrees it is a sudoers file.
 *
 * The validation is the whole reason this is not a `ManagedFile` with careful content. A syntax
 * error in `/etc/sudoers.d` does not break the rule it is in — it breaks sudo, for every user, on
 * a machine whose only management path is sudo over ssh. Writing to a temporary file first means
 * the worst outcome of a mistake is a deployment that fails, which is the correct worst outcome.
 */
async function apply(host: Target, file: string, content: string, directory = DIRECTORY): Promise<void> {
  const path = pathOf(file, directory);
  await must(host, escalate(host,
    // umask first: the temporary file holds a privilege grant, however briefly
    `umask 077 && staging=$(mktemp) && ` +
    `${heredocInto('"$staging"', content)}\n` +
    `visudo -cqf "$staging" || { rm -f "$staging"; echo "sudoers content did not parse" >&2; exit 1; }; ` +
    // install rather than mv and chmod: one step, and the file is never briefly present with the
    // wrong mode, which sudo would reject if it read it in between
    `install -m ${MODE} -o root -g root "$staging" ${shellQuote(path)} && rm -f "$staging"`,
  ));
}

function providerFor(host: Target): pulumi.dynamic.ResourceProvider<SudoRuleArgs, SudoRuleState> {
  return {
    async create(args) {
      const file = sudoersFileName(args.file ?? '');
      const directory = args.directory ?? DIRECTORY;
      const content = sudoersFile(args);
      await apply(host, file, content, directory);
      return { id: file, outs: { file, directory, content, mode: MODE, owner: 'root' } };
    },

    async read(id, state) {
      const actual = await readSudoRule(host, id, state?.directory ?? DIRECTORY);
      // gone from the machine: Pulumi forgets it, and the next up puts it back
      if (!actual) return { id: undefined, props: undefined };
      return { id, props: { ...state, ...actual } };
    },

    async update(id, _old, args) {
      const content = sudoersFile(args);
      const directory = args.directory ?? DIRECTORY;
      const current = await readSudoRule(host, id, directory);
      if (!current || current.content !== content || current.mode !== MODE) {
        await apply(host, id, content, directory);
      }
      return { outs: { file: id, directory, content, mode: MODE, owner: 'root' } };
    },

    async diff(_id, old, args) {
      const content = sudoersFile(args);
      const file = args.file ?? old.file;
      return {
        changes: providerChanged(old, args)
          || old.content !== content || old.mode !== MODE || old.owner !== 'root' || old.file !== file,
        // a rule in a different file is a different rule, and leaving the old file behind would
        // leave a privilege granted that nothing describes any more
        replaces: old.file !== file ? ['file'] : [],
        stables: [],
        deleteBeforeReplace: true,
      };
    },

    async delete(id) {
      await must(host, escalate(host, `rm -f ${shellQuote(pathOf(id))}`));
    },
  };
}

/**
 * A sudo privilege the machine should grant, checked against the file rather than remembered.
 *
 * Whether it is *effective* is a separate question from whether the file is right, and belongs in a
 * `Precondition` — `sudo -n -u <user> true` for a rule that should need no password. The file says
 * what was asked for; only running it says what sudo actually does with the whole of its
 * configuration, which includes everything else in `/etc/sudoers` and every other drop-in.
 */
export class SudoRule extends pulumi.dynamic.Resource {
  declare readonly file: pulumi.Output<string>;
  declare readonly content: pulumi.Output<string>;

  constructor(name: string, host: Target, args: SudoRuleArgs, opts?: pulumi.CustomResourceOptions) {
    // the resource's own name is the obvious file name, and is checked rather than trusted: a
    // Pulumi name with a dot in it is perfectly legal and would produce a rule sudo silently ignores
    super(providerFor(host), name, { directory: DIRECTORY, ...args, file: sudoersFileName(args.file ?? name) }, withLegacyAlias(opts), 'homelab', 'SudoRule');
  }
}
