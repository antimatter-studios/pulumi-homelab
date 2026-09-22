import * as pulumi from '@pulumi/pulumi';
import { Directory } from '../resources/directory.ts';
import { Symlink } from '../resources/symlink.ts';
import type { Target } from '../ssh.ts';

/**
 * A second Claude Code login on one account, sharing the work and not the identity.
 *
 * Claude Code keeps everything about itself in one directory, chosen per process by
 * `CLAUDE_CONFIG_DIR`. Point it somewhere else and you get a wholly separate installation: separate
 * credentials, and separate history, projects and plugins too. The first is the point; the second is
 * usually not wanted, because the work does not belong to the account that paid for it.
 *
 * So a profile is a real directory whose *shared* entries are symlinks back into the default one:
 *
 * ```
 * ~/.claude/                 the shared backend, and also the default profile
 *   projects/  sessions/  session-env/  plugins/  settings.json      real
 *
 * ~/.claude-work/
 *   projects      -> ~/.claude/projects
 *   sessions      -> ~/.claude/sessions
 *   ...
 *   .credentials.json  .claude.json  history.jsonl  cache/           real, and private
 * ```
 *
 * Work product is shared; identity is not. Two accounts drive one working history, and which one is
 * in use is a per-process decision — an alias interactively, `Environment=` in a unit — never a
 * global one.
 *
 * **What this deliberately does not do.** It does not fold an existing profile into the shared tree.
 * That is a one-time migration, not a desired state, and it moves data: the right tool is a person
 * with `rsync` who can see what they are merging. If a real directory sits where a link belongs,
 * `Symlink`'s read reports it and nothing is moved.
 *
 * **And it owns no content.** `settings.json` gets a link; what Claude writes through it is Claude's.
 * `.credentials.json` and `.claude.json` are not declared at all — the first because it holds a
 * token the program rotates (see `ProtectedFile`'s `enforce: 'once'` for the resource that can carry
 * one), the second because it is per-account state that is meaningless anywhere else.
 */

/**
 * One entry shared between the default profile and every other one.
 *
 * The mode is here rather than as one setting for all of them because Claude does not use one:
 * `projects` and `sessions` are `0700` and hold conversation content, while `session-env` and
 * `plugins` are `0775`. Declaring a single mode would mean changing permissions the program chose,
 * on a directory this module did not create.
 */
export interface SharedEntry {
  name: string;
  /**
   * Whether the shared side is a directory this should make, or a file Claude writes for itself.
   *
   * A file is linked and never created: an empty `settings.json` is not the same as no
   * `settings.json`, and writing one would be this module having an opinion about the program's
   * defaults.
   */
  kind: 'directory' | 'file';
  mode?: string;
}

/**
 * What is shared by default, and it is smaller than it looks like it should be.
 *
 * Taken from a working setup rather than from what seemed reasonable. A hand-written script that
 * preceded this listed `skills`, `hooks` and `workgroups` as well — and linked none of them, because
 * it only linked entries that already existed on the shared side and those three never did. A
 * default that creates directories the program does not use is a default that has to be explained
 * later.
 */
export const SHARED_DEFAULTS: SharedEntry[] = [
  { name: 'projects', kind: 'directory', mode: '0700' },
  { name: 'sessions', kind: 'directory', mode: '0700' },
  { name: 'session-env', kind: 'directory', mode: '0775' },
  { name: 'plugins', kind: 'directory', mode: '0775' },
  { name: 'settings.json', kind: 'file' },
];

export interface ClaudeSharedArgs {
  account: string;
  /** The shared backend — `~/.claude`, which is also the default profile. */
  path: string;
  /** Defaults to {@link SHARED_DEFAULTS}. */
  entries?: SharedEntry[];
  group?: string;
  mode?: string;
}

export interface ClaudeShared {
  path: string;
  entries: SharedEntry[];
  /** Everything a profile has to exist after, passed to `claudeProfile` as `shared`. */
  resources: pulumi.Resource[];
}

export interface ClaudeProfileArgs {
  account: string;
  /** The backend this profile links into, from {@link claudeShared}. */
  shared: ClaudeShared;
  /** This profile's directory — `~/.claude-work`. What `CLAUDE_CONFIG_DIR` is set to. */
  dir: string;
  group?: string;
  /** The profile directory's own mode. */
  mode?: string;
}

export interface ClaudeProfile {
  dir: string;
  /** What `CLAUDE_CONFIG_DIR` has to be for this profile, which is the same thing said usefully. */
  configDir: string;
  links: Symlink[];
}

/**
 * The link's target, spelled absolutely.
 *
 * Relative would be shorter and is wrong here: these directories are read by a program that resolves
 * paths from wherever it happens to be running, and one of them — `plugins` — is walked by tooling
 * that follows the link and reports what it found. An absolute target says the same thing from
 * every working directory.
 */
export function sharedTarget(shared: string, entry: string): string {
  return `${shared.replace(/\/+$/, '')}/${entry}`;
}

/**
 * The refusal for a profile that would link a directory to itself.
 *
 * A profile whose `dir` is the backend produces, for each entry, a symlink whose target is the path
 * the symlink is at. The loop that creates them succeeds. What follows is a program that opens
 * `projects`, follows a link to `projects`, and gets ELOOP — reported as a corrupt installation
 * rather than as a description that asked for something impossible.
 */
export function profileRefusal(args: { shared: { path: string }; dir: string }): string | undefined {
  const shared = args.shared.path.replace(/\/+$/, '');
  const dir = args.dir.replace(/\/+$/, '');
  if (shared === dir) return `claudeProfile: dir and shared are the same directory (${dir}); a profile has to be somewhere else`;
  return undefined;
}

/**
 * The backend every profile links into, declared once.
 *
 * Separate from `claudeProfile` for one reason, and it is the failure this package keeps meeting:
 * two profiles each declaring `~/.claude` would be two resources owning one path, re-applying
 * different answers on alternate runs with both reporting success. The backend has one owner and
 * the profiles depend on it.
 *
 * It is also the default profile. Nothing here is specific to being *shared* — a machine with one
 * login has exactly this and no `claudeProfile` at all.
 */
export function claudeShared(host: Target, name: string, args: ClaudeSharedArgs, opts?: pulumi.CustomResourceOptions): ClaudeShared {
  const entries = args.entries ?? SHARED_DEFAULTS;
  const group = args.group ?? args.account;

  const root = new Directory(name, host, {
    path: args.path, owner: args.account, group, mode: args.mode ?? '0775',
  }, opts);

  /**
   * The entry directories, declared so a link cannot dangle.
   *
   * A symlink to a directory that does not exist is created happily and fails on first use, and the
   * failure names the profile rather than the missing target. Claude makes these itself on first
   * run — but only for whichever profile runs first, and every other one is broken until then.
   *
   * A `file` entry is not created: an empty `settings.json` is not the same as no `settings.json`,
   * and writing one would be this module having an opinion about the program's defaults.
   */
  const made = entries
    .filter((entry) => entry.kind === 'directory')
    .map((entry) => new Directory(`${name}-${entry.name}`, host, {
      path: sharedTarget(args.path, entry.name),
      owner: args.account,
      group,
      mode: entry.mode ?? '0700',
    }, { ...opts, dependsOn: [root] }));

  return { path: args.path, entries, resources: [root, ...made] };
}

/** A Claude Code profile: its own credentials, the shared account's work. */
export function claudeProfile(host: Target, name: string, args: ClaudeProfileArgs, opts?: pulumi.CustomResourceOptions): ClaudeProfile {
  const refusal = profileRefusal(args);
  if (refusal) throw new Error(refusal);

  const group = args.group ?? args.account;

  const dir = new Directory(name, host, {
    path: args.dir, owner: args.account, group, mode: args.mode ?? '0775',
  }, opts);

  const links = args.shared.entries.map((entry) => new Symlink(`${name}-${entry.name}`, host, {
    path: sharedTarget(args.dir, entry.name),
    target: sharedTarget(args.shared.path, entry.name),
  }, { ...opts, dependsOn: [dir, ...args.shared.resources] }));

  return { dir: args.dir, configDir: args.dir, links };
}
