import * as pulumi from '@pulumi/pulumi';
import { escalate, ask, heredoc, must, shellQuote, type Target, describe } from '../ssh.ts';
import { stamped, transportChanged, withLegacyAlias } from '../upgrade.ts';

/**
 * One line in `/etc/fstab`, keyed on the mount point.
 *
 * **The file is never regenerated.** It carries the root filesystem's entry, and an fstab that gets
 * `PARTUUID=` wrong is a machine that does not boot and cannot be fixed over ssh — the same
 * argument as `KernelCmdline` with a worse blast radius. Every other line comes back byte for byte,
 * including the comments: this machine's fstab has two prose notes explaining that
 * `dphys-swapfile` is the mechanism on Raspberry Pi OS, and those are somebody's message to the
 * next person rather than noise to be tidied away.
 *
 * The target is the identity because a mount point is what is unique in that file. Two entries for
 * one target is a broken machine, and the target is what somebody looks up when they want to know
 * what is mounted where.
 *
 * **It writes the line and does not mount.** Those are different acts with different risks: mounting
 * something over a directory that has contents hides them, silently and immediately, and a
 * deployment is the worst possible moment to discover that. So `read` reports both the line and
 * whether the target is actually mounted, the difference shows up as drift, and a person decides.
 * `mount: true` is available for the cases where that is genuinely wanted.
 */
export interface FstabEntryArgs {
  /** Device, UUID, label, or the source path of a bind mount. */
  source: string;
  /** The mount point. This is the resource's identity. */
  target: string;
  /** `ext4`, `btrfs`, or `none` for a bind mount. */
  type: string;
  options?: string[];
  /** The last two fields. `0 0` is right for everything this should be managing. */
  dump?: number;
  pass?: number;
  /**
   * Whether to actually mount it after writing the line.
   *
   * Off by default, and worth leaving off. Mounting over a directory with contents in it hides them
   * rather than failing, so the safe order is: write the line, look at what is there, mount it
   * yourself.
   */
  mount?: boolean;
  /**
   * Which file to edit.
   *
   * An argument rather than a constant because this package should not decide where a machine keeps
   * its mounts. `/etc/fstab` is where every distribution this is likely to meet puts it, and that
   * is a default rather than a fact.
   */
  file?: string;
}

interface FstabEntryState {
  source: string;
  target: string;
  type: string;
  options: string[];
  dump: number;
  pass: number;
  mount: boolean;
  /** The line as it now stands in the file. */
  line: string;
  /** What is mounted there right now, or null when nothing is — the other tense. */
  mounted: string | null;
  file: string;
}

const FSTAB = '/etc/fstab';


/**
 * The line written above a managed entry, so somebody reading the file knows which lines are owned.
 *
 * **It is an annotation and is never matched on.** The identity stays the mount point, because the
 * operating system already guarantees that is unique — two entries for one target is a broken
 * machine. Keying on a comment would replace a strong key with one a person can edit or delete, and
 * losing it would mean `upsert` could not find its own line and appended instead, producing exactly
 * the duplicate the design exists to prevent. Because nothing matches on it, losing it degrades to
 * "unlabelled" rather than "duplicated", which is no new failure mode at all.
 *
 * What it buys is the case target-keying genuinely cannot catch: an **orphan**. Pulumi only removes
 * what it still remembers creating, so a line left behind by lost or rebuilt state is invisible to
 * every resource — but a marked line that nothing declares is exactly what `audit` can find.
 *
 * Its own line rather than trailing the entry. `fstab(5)` documents comments as lines beginning
 * with `#` and says nothing about trailing ones; libmount's tolerance for a seventh field is not
 * something worth discovering on the file that decides whether the machine boots.
 */
export const MARKER = '# pulumi-homelab';

/** Whether a line is one of our annotations. */
const isMarker = (line: string) => line.trim().startsWith(MARKER);
// not `as const`: the options array is spread into resource state and has to stay mutable, and a
// readonly tuple here produces four type errors two files away that say nothing about the cause
const DEFAULTS: { options: string[]; dump: number; pass: number; mount: boolean } =
  { options: ['defaults'], dump: 0, pass: 0, mount: false };

/** The six fields, in the order fstab has had them since before any of this. */
export function fstabLine(args: Omit<FstabEntryState, 'line' | 'mounted' | 'mount' | 'file'>): string {
  const options = args.options.length > 0 ? args.options.join(',') : 'defaults';
  return `${args.source} ${args.target} ${args.type} ${options} ${args.dump} ${args.pass}`;
}

/** Whether a line is a comment, by its first non-whitespace character rather than by its shape. */
const isComment = (line: string) => line.trim().startsWith('#');

/** The mount point a line describes, or null for a comment or anything without six-ish fields. */
export function targetOf(line: string): string | null {
  if (isComment(line) || line.trim().length === 0) return null;
  const fields = line.trim().split(/\s+/);
  // a commented entry still has a plausible target in field two, which is why the comment check
  // comes first and is by character rather than by field
  return fields.length >= 3 ? (fields[1] ?? null) : null;
}

/** The uncommented entry for that mount point, or null when the file has none. */
export function findEntry(text: string, target: string): string | null {
  return text.split('\n').find((line) => targetOf(line) === target) ?? null;
}

/**
 * Put the line in, leaving every other byte of the file alone.
 *
 * Replace in place where the target is already described, append otherwise. Replacing in place
 * rather than removing and appending keeps the file in whatever order its author chose, which for
 * fstab is frequently deliberate — and keeps the result stable, so writing twice gives the same
 * file as writing once.
 */
export function upsertFstab(text: string, target: string, line: string): string {
  const lines = text.split('\n');
  const at = lines.findIndex((existing) => targetOf(existing) === target);
  if (at >= 0) {
    // an entry already marked keeps one marker rather than growing another on every run
    const from = at > 0 && isMarker(lines[at - 1] ?? '') ? at - 1 : at;
    return [...lines.slice(0, from), MARKER, line, ...lines.slice(at + 1)].join('\n');
  }
  // keep exactly one trailing newline, whatever the file arrived with
  const body = text.replace(/\n+$/, '');
  return `${body}\n${MARKER}\n${line}\n`;
}

/** Take the line out, and nothing else with it. */
export function removeFromFstab(text: string, target: string): string {
  const lines = text.split('\n');
  const at = lines.findIndex((line) => targetOf(line) === target);
  if (at < 0) return text;
  // the marker above it goes too, or the file keeps a label for a line that is no longer there
  const from = at > 0 && isMarker(lines[at - 1] ?? '') ? at - 1 : at;
  return [...lines.slice(0, from), ...lines.slice(at + 1)].join('\n');
}

/**
 * The mount points of every marked entry in the file.
 *
 * For the audit: a marked line whose target nothing declares any more is an orphan, left by state
 * that was lost or rebuilt. It is the one kind of stale entry no resource can find, because Pulumi
 * only removes what it still remembers creating.
 */
export function markedTargets(text: string): string[] {
  const lines = text.split('\n');
  const targets: string[] = [];
  lines.forEach((line, at) => {
    if (!isMarker(line)) return;
    const next = targetOf(lines[at + 1] ?? '');
    if (next !== null) targets.push(next);
  });
  return targets;
}

/** The line, and what is mounted there now — the two tenses this resource exists to keep apart. */
export async function readFstabEntry(host: Target, target: string, file = FSTAB): Promise<{ line: string | null; mounted: string | null }> {
  const asked = await ask(host, escalate(host,
    `cat ${shellQuote(file)}; echo '#pulumi-homelab#mounted'; ` +
    // findmnt answers about the mount point itself; a non-zero exit is the answer 'nothing is
    // mounted there', which is an answer and not a fault
    `findmnt -no SOURCE,FSTYPE,OPTIONS ${shellQuote(target)} 2>/dev/null || true`,
  ));
  if (asked.code !== 0) throw new Error(`could not read ${file}: ${asked.err.trim()}`);
  const [contents = '', mounted = ''] = asked.out.split('#pulumi-homelab#mounted\n');
  return { line: findEntry(contents, target), mounted: mounted.trim() || null };
}

function providerFor(host: Target): pulumi.dynamic.ResourceProvider<FstabEntryArgs, FstabEntryState> {
  const settle = async (args: FstabEntryArgs): Promise<FstabEntryState> => {
    const wanted = { ...DEFAULTS, ...args };
    const file = args.file ?? FSTAB;
    const line = fstabLine(wanted);
    const current = await must(host, escalate(host, `cat ${shellQuote(file)}`));
    const updated = upsertFstab(current, wanted.target, line);
    if (updated !== current) {
      // written through a here-document in one command: an fstab that is briefly empty because a
      // redirect truncated it before the write finished is a machine that will not boot
      await must(host, escalate(host, heredoc(file, updated)));
    }
    if (wanted.mount) {
      await must(host, escalate(host, `mountpoint -q ${shellQuote(wanted.target)} || mount ${shellQuote(wanted.target)}`));
    }
    const actual = await readFstabEntry(host, wanted.target, file);
    return { ...wanted, file, line: actual.line ?? line, mounted: actual.mounted };
  };

  return {
    async create(args) {
      const state = await settle(args);
      return { id: args.target, outs: state };
    },

    async read(id, state) {
      const file = state?.file ?? FSTAB;
      const actual = await readFstabEntry(host, id, file);
      // the line being gone is the resource being gone; something else being mounted there is drift
      // this resource should report rather than a resource that no longer exists
      if (!actual.line) return { id: undefined, props: undefined };
      return {
        id,
        props: {
          source: state?.source ?? '',
          type: state?.type ?? '',
          options: state?.options ?? [],
          dump: state?.dump ?? DEFAULTS.dump,
          pass: state?.pass ?? DEFAULTS.pass,
          mount: state?.mount ?? DEFAULTS.mount,
          file,
          ...state,
          target: id,
          line: actual.line,
          mounted: actual.mounted,
        },
      };
    },

    async update(id, _old, args) {
      return { outs: await settle({ ...args, target: id }) };
    },

    async diff(_id, old, args) {
      const wanted = { ...DEFAULTS, ...args };
      return {
        // the line is compared against the file rather than against the last arguments, so a hand
        // edit shows up; `mounted` is reported and never reconciled, because mounting is the
        // operator's act
        changes: transportChanged(old)
          || old.line !== fstabLine(wanted) || old.target !== wanted.target,
        replaces: old.target !== wanted.target ? ['target'] : [],
        stables: [],
        deleteBeforeReplace: true,
      };
    },

    async delete(id) {
      // the line goes; nothing is unmounted. Forgetting to describe a mount is not the same as
      // wanting it gone from a machine that is using it
      const current = await must(host, escalate(host, `cat ${shellQuote(FSTAB)}`));
      await must(host, escalate(host, heredoc(FSTAB, removeFromFstab(current, id))));
    },
  };
}

/** A line in fstab, and an honest account of whether anything is mounted there. */
export class FstabEntry extends pulumi.dynamic.Resource {
  declare readonly target: pulumi.Output<string>;
  declare readonly line: pulumi.Output<string>;
  declare readonly mounted: pulumi.Output<string | null>;

  constructor(name: string, host: Target, args: FstabEntryArgs, opts?: pulumi.CustomResourceOptions) {
    super(stamped(providerFor(host)), name, { ...DEFAULTS, file: FSTAB, line: undefined, mounted: undefined, ...args }, withLegacyAlias(opts), 'homelab', 'FstabEntry');
  }
}
