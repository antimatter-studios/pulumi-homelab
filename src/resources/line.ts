import * as pulumi from '@pulumi/pulumi';
import { escalate, ask, heredoc, must, shellQuote, type Target, describe } from '../ssh.ts';
import { stamped, transportChanged, withLegacyAlias } from '../upgrade.ts';

/** Where a managed line goes, relative to what is already in the file. */
export type LinePosition = 'prepend' | 'append' | 'before' | 'after';

/**
 * One line in a file that belongs to somebody else.
 *
 * **`ManagedFile` owns whole files, and the interesting case is a file that must not be owned.** A
 * shell rc file, a packaged default that takes local additions, a config somebody has tuned by hand
 * — reproducing one of those in a program to add a single line means the program now owns it, and
 * the next time a person edits it the deployment reverts them. That is the two-writer failure, and
 * it has already bitten a real machine twice through the same file: `gh auth setup-git` writing a
 * credential helper into a file Pulumi owned, and an agent adding a second helper to it. Both
 * presented as intermittent authentication breakage with nothing pointing at the cause.
 *
 * So this owns a line and nothing else. Every other byte of the file, including the comments, comes
 * back untouched, and `delete` takes away its own line and leaves the rest — the same stance
 * `PosixAcl` takes about entries it did not grant.
 *
 * **The marker is matched on, unlike `FstabEntry`'s.** An fstab entry has a natural key, the mount
 * point, so its comment is an annotation that can be lost without consequence. A line in a `.bashrc`
 * has no key at all: matching the literal text would make a hand-edited line a *second* line rather
 * than the same one, and appending without recognising its own previous line grows the file on every
 * deployment. So the marker is written into the line as a trailing comment and is how it is found
 * ever after. Lose it and the line becomes an orphan this resource can no longer see — which is why
 * it is worth choosing a marker that means something a year later.
 *
 * **Position is part of the requirement, not an accident, and it is read back.** The case this was
 * built for is a line that has to precede Debian's early return in `.bashrc`:
 *
 * ```
 * # If not running interactively, don't do anything
 * case $- in
 *     *i*) ;;
 *       *) return;;
 * esac
 * ```
 *
 * A line after that is present, correct, and dead — `ssh host 'cmd'` never reaches it. So `read`
 * reports whether the line is still where it was asked to be, and a line somebody moved past its
 * anchor is drift rather than a resource that is fine. A resource that checked only for presence
 * would report success for a file that does not work, which is the failure this package exists to
 * avoid.
 *
 * **It refuses a file that does not exist rather than creating one.** If the file is absent,
 * `ManagedFile` is the resource that should own it, and quietly creating it here would hide the
 * mistake behind a deployment that appeared to work.
 */
export interface ManagedLineArgs {
  /** The file. This resource neither creates nor owns it. */
  path: string;
  /** The line, without the marker — that is added. */
  line: string;
  /**
   * How this line is recognised across edits.
   *
   * Written into the line as a trailing comment, and the only thing that finds it afterwards. Worth
   * making it say what the line is for, because somebody reading the file in a year is the other
   * audience.
   */
  marker: string;
  /**
   * Where it goes.
   *
   * `'before'` and `'after'` need `anchor`, and are the reason this is not simply an append: a line
   * whose whole purpose is to precede something has to be placed relative to it.
   */
  position?: LinePosition;
  /**
   * The text `before` and `after` are relative to, matched as a literal substring.
   *
   * A substring rather than a pattern because the anchor is usually a line somebody else wrote and
   * will recognise — `case $- in` — and a regex over a file you do not own is a way to match
   * something you did not mean.
   */
  anchor?: string;
  /**
   * The comment syntax, for the marker.
   *
   * `#` covers shell, sysctl, and most of what has packaged defaults. A file whose comments are `//`
   * or `;` needs to say so.
   */
  comment?: string;
}

interface ManagedLineState {
  path: string;
  line: string;
  marker: string;
  position: LinePosition;
  anchor: string;
  comment: string;
  /** The line as the file now has it, marker and all, or the empty string when it is absent. */
  actual: string;
  /**
   * Whether the line is where it was asked to be.
   *
   * False for a line somebody moved past its anchor — present, correct, and inert.
   */
  placed: boolean;
}

const DEFAULTS = { position: 'append' as LinePosition, anchor: '', comment: '#' };

/** The line as it is written into the file, with the marker that finds it again. */
export function markLine(line: string, marker: string, comment = DEFAULTS.comment): string {
  return `${line} ${comment} pulumi-homelab:${marker}`;
}

/** What identifies this resource's line in the file. */
export function markerOf(marker: string): string {
  return `pulumi-homelab:${marker}`;
}

/** Where the marked line is, or -1 when the file does not have it. */
export function findMarked(text: string, marker: string): number {
  const needle = markerOf(marker);
  return text.split('\n').findIndex((line) => line.includes(needle));
}

/**
 * Where the anchor is, or -1.
 *
 * The *first* occurrence for `before` and the *last* for `after`, which is what the words mean when
 * the anchor appears more than once: before the interactive guard means before it starts, and after
 * a block means after all of it.
 */
export function findAnchor(text: string, anchor: string, position: LinePosition): number {
  if (anchor === '') return -1;
  const lines = text.split('\n');
  if (position === 'after') {
    for (let at = lines.length - 1; at >= 0; at -= 1) if (lines[at]?.includes(anchor)) return at;
    return -1;
  }
  return lines.findIndex((line) => line.includes(anchor));
}

/**
 * Why a line cannot be placed, or null when it can.
 *
 * A missing anchor is refused rather than fallen back on. The position is the requirement — a line
 * that was meant to precede an early return and got appended instead is present, correct and dead,
 * and a deployment that reports success for that is worse than one that fails.
 */
export function placementRefusal(
  text: string,
  args: { path: string; position: LinePosition; anchor: string },
): string | null {
  if (args.position !== 'before' && args.position !== 'after') return null;
  if (args.anchor === '') {
    return `position: '${args.position}' needs an anchor to be relative to.`;
  }
  if (findAnchor(text, args.anchor, args.position) < 0) {
    return `${args.path} does not contain ${JSON.stringify(args.anchor)}, so the line cannot be `
      + `placed ${args.position} it. Appending instead would put it somewhere it may never run, `
      + `which is why this refuses rather than guessing.`;
  }
  return null;
}

/** Where a new line goes. */
export function insertionPoint(
  text: string,
  args: { position: LinePosition; anchor: string },
): number {
  const lines = text.split('\n');
  if (args.position === 'prepend') return 0;
  if (args.position === 'append') return lines.length;
  const at = findAnchor(text, args.anchor, args.position);
  return args.position === 'before' ? at : at + 1;
}

/**
 * Whether the line sits where it was asked to.
 *
 * Absent is not misplaced — that is a different answer, and conflating them would report a file with
 * no line at all as one whose line is in the wrong place.
 */
export function correctlyPlaced(
  text: string,
  args: { marker: string; position: LinePosition; anchor: string },
): boolean {
  const at = findMarked(text, args.marker);
  if (at < 0) return false;
  if (args.position === 'prepend' || args.position === 'append') return true;
  const anchor = findAnchor(text, args.anchor, args.position);
  if (anchor < 0) return false;
  return args.position === 'before' ? at < anchor : at > anchor;
}

/**
 * Put the line in, leaving every other byte alone.
 *
 * Replaced in place where the marker is already there, inserted at the requested point otherwise.
 * Replacing rather than removing and re-inserting keeps a line somebody moved *deliberately* where
 * they put it, unless it has crossed its anchor — at which point it is moved back, because the
 * position was the requirement.
 */
export function upsertLine(
  text: string,
  args: { line: string; marker: string; position: LinePosition; anchor: string; comment: string },
): string {
  const wanted = markLine(args.line, args.marker, args.comment);
  const lines = text.split('\n');
  const at = findMarked(text, args.marker);

  if (at >= 0 && correctlyPlaced(text, args)) {
    return [...lines.slice(0, at), wanted, ...lines.slice(at + 1)].join('\n');
  }
  // somewhere it should not be: take it out first, then place it, so the file never briefly holds
  // two copies and the insertion point is computed against the file without it
  const without = at >= 0 ? [...lines.slice(0, at), ...lines.slice(at + 1)] : lines;
  const point = insertionPoint(without.join('\n'), args);
  return [...without.slice(0, point), wanted, ...without.slice(point)].join('\n');
}

/** Take the line out, and nothing else with it. */
export function removeLine(text: string, marker: string): string {
  const needle = markerOf(marker);
  const lines = text.split('\n');
  const kept = lines.filter((line) => !line.includes(needle));
  return kept.length === lines.length ? text : kept.join('\n');
}

/** What the file says about this line. */
export function readLine(
  text: string,
  args: { marker: string; position: LinePosition; anchor: string },
): { actual: string; placed: boolean } {
  const at = findMarked(text, args.marker);
  return {
    actual: at < 0 ? '' : (text.split('\n')[at] ?? ''),
    placed: correctlyPlaced(text, args),
  };
}

/** The file, or null when there is nothing there. */
export async function readFileLine(
  host: Target,
  path: string,
  args: { marker: string; position: LinePosition; anchor: string },
): Promise<{ actual: string; placed: boolean } | null> {
  const asked = await ask(host, escalate(host,
    `test -f ${shellQuote(path)} || exit 9; cat ${shellQuote(path)}`,
  ));
  if (asked.code === 9) return null;
  if (asked.code !== 0) throw new Error(`could not read ${path}: ${asked.err.trim()}`);
  return readLine(asked.out, args);
}

/** Everything the arguments settle to, with the defaults applied once. */
export function resolveLine(args: ManagedLineArgs): ManagedLineState & { id: string } {
  const settled = {
    path: args.path,
    line: args.line,
    marker: args.marker,
    position: args.position ?? DEFAULTS.position,
    anchor: args.anchor ?? DEFAULTS.anchor,
    comment: args.comment ?? DEFAULTS.comment,
  };
  if (settled.marker.trim() === '') throw new Error(`${args.path}: a marker is what finds this line again, so it cannot be empty`);
  return { ...settled, actual: '', placed: false, id: `${settled.path}#${settled.marker}` };
}

function providerFor(host: Target): pulumi.dynamic.ResourceProvider<ManagedLineArgs, ManagedLineState> {
  const settle = async (args: ManagedLineArgs): Promise<ManagedLineState> => {
    const wanted = resolveLine(args);
    const current = await ask(host, escalate(host, `test -f ${shellQuote(wanted.path)} || exit 9; cat ${shellQuote(wanted.path)}`));
    if (current.code === 9) {
      throw new Error(
        `${wanted.path} does not exist on ${describe(host)}. This resource owns a line in a file `
        + `somebody else owns and will not create one — if the file should be this stack's, declare `
        + `it with ManagedFile instead.`,
      );
    }
    if (current.code !== 0) throw new Error(`could not read ${wanted.path}: ${current.err.trim()}`);

    const refusal = placementRefusal(current.out, wanted);
    if (refusal !== null) throw new Error(refusal);

    const updated = upsertLine(current.out, wanted);
    // only when the file would actually differ: rewriting somebody's shell configuration on every
    // deployment is how a resource that adds one line becomes a resource nobody trusts
    if (updated !== current.out) await must(host, escalate(host, heredoc(wanted.path, updated)));

    const after = await readFileLine(host, wanted.path, wanted);
    if (after === null || after.actual === '') {
      throw new Error(`wrote the line into ${wanted.path} on ${describe(host)} but it is not there`);
    }
    if (!after.placed) {
      throw new Error(
        `wrote the line into ${wanted.path} on ${describe(host)} but it is not ${wanted.position} `
        + `${JSON.stringify(wanted.anchor)} — a line in the wrong place is present, correct and inert`,
      );
    }
    return { ...wanted, actual: after.actual, placed: after.placed };
  };

  return {
    async create(args) {
      return { id: resolveLine(args).id, outs: await settle(args) };
    },

    async read(id, state) {
      const path = state?.path ?? id.split('#')[0] ?? '';
      const marker = state?.marker ?? id.split('#').slice(1).join('#');
      const args = {
        marker,
        position: state?.position ?? DEFAULTS.position,
        anchor: state?.anchor ?? DEFAULTS.anchor,
      };
      const found = await readFileLine(host, path, args);
      // the file is gone: so is the line, and this resource does not create files
      if (found === null) return { id: undefined, props: undefined };
      return {
        id,
        props: {
          ...state,
          line: state?.line ?? '',
          comment: state?.comment ?? DEFAULTS.comment,
          path,
          marker,
          // `state` is absent when a resource is imported rather than refreshed, so every field has
          // to stand on its own here rather than leaning on what Pulumi already knew
          position: args.position,
          anchor: args.anchor,
          // the two that come from the machine rather than from what was remembered
          actual: found.actual,
          placed: found.placed,
        },
      };
    },

    async update(id, _old, args) {
      return { outs: await settle(args) };
    },

    async diff(_id, old, args) {
      const wanted = resolveLine(args);
      return {
        changes: transportChanged(old)
          // the line as the file has it, against what it should be — so a hand edit is drift
          || old.actual !== markLine(wanted.line, wanted.marker, wanted.comment)
          // and a line somebody moved past its anchor, which is the failure presence cannot see
          || old.placed === false
          || old.position !== wanted.position
          || old.anchor !== wanted.anchor,
        // a different file or marker is a different line; this one is removed from where it was
        replaces: old.path !== wanted.path || old.marker !== wanted.marker ? ['path', 'marker'] : [],
        stables: [],
        deleteBeforeReplace: true,
      };
    },

    async delete(id, state) {
      const path = state.path || id.split('#')[0] || '';
      const marker = state.marker || id.split('#').slice(1).join('#');
      const current = await ask(host, escalate(host, `test -f ${shellQuote(path)} || exit 9; cat ${shellQuote(path)}`));
      // the file going before the line is not a failure: there is nothing left to take out
      if (current.code === 9) return;
      if (current.code !== 0) throw new Error(`could not read ${path}: ${current.err.trim()}`);
      const updated = removeLine(current.out, marker);
      if (updated !== current.out) await must(host, escalate(host, heredoc(path, updated)));
    },
  };
}

/** One line in a file this stack does not own, found again by its marker and checked for position. */
export class ManagedLine extends pulumi.dynamic.Resource {
  /** The line as the file has it, marker and all. */
  declare readonly actual: pulumi.Output<string>;
  /** Whether it is still where it was asked to be. */
  declare readonly placed: pulumi.Output<boolean>;

  constructor(name: string, host: Target, args: ManagedLineArgs, opts?: pulumi.CustomResourceOptions) {
    super(stamped(providerFor(host)), name, {
      position: DEFAULTS.position,
      anchor: DEFAULTS.anchor,
      comment: DEFAULTS.comment,
      actual: undefined,
      placed: undefined,
      ...args,
    }, withLegacyAlias(opts), 'homelab', 'ManagedLine');
  }
}
