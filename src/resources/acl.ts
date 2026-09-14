import * as pulumi from '@pulumi/pulumi';
import { escalate, ask, must, shellQuote, type Target, describe } from '../ssh.ts';
import { stamped, transportChanged, withLegacyAlias } from '../upgrade.ts';

/** The four kinds of entry a POSIX ACL holds. */
export type AclEntryType = 'user' | 'group' | 'mask' | 'other';

/** Whether an entry governs this directory, or the children created inside it later. */
export type AclScope = 'access' | 'default';

/**
 * A POSIX ACL entry, as declared.
 *
 * `perms` may be written `rwx`, `r-x`, `rw`, or a single octal digit; all of them settle to the
 * three-character form `getfacl` prints, so the comparison is between like and like.
 */
export interface AclEntryArgs {
  type: AclEntryType;
  /** The account or group. Left out it means the owning one, which is only declarable as a default. */
  name?: string;
  perms: string;
}

/**
 * Access control on one path, expressed as ACL entries rather than as mode bits.
 *
 * **What this is for is the grant that ownership and group membership cannot express.** A service
 * account that needs write access to one directory inside a shared pool has no good answer in the
 * two levers a Unix mode offers: adding it to the group that owns the pool grants it everything in
 * the pool, and chowning the directory takes it away from whoever owns the things inside it. A
 * named-user ACL entry says exactly the intended thing, changes no owner, and affects nothing else
 * on the volume.
 *
 * **Access entries and default entries are different things, and confusing them is the usual
 * mistake.** An access entry grants nothing to children; a default entry grants nothing to the
 * directory itself. Most real grants want both, which is why they are two arguments here rather
 * than a flag — half the job done is the failure people actually hit, and it fails as a permission
 * denied somewhere far from the declaration.
 *
 * **It owns the entries it names and nothing else in the ACL.** A pool root typically already
 * carries the entries that make the whole scheme work, and a resource that set the ACL wholesale
 * would remove them. So this adds and updates what it declares, removes what it used to declare,
 * and leaves every other entry exactly as it found it — the same bargain the rest of this package
 * makes with the machines it is pointed at.
 *
 * **A mode changes the ACL, and the mode wins.** `chmod` recomputes the mask from the group bits,
 * and the mask can suppress a named entry that is still listed: `getfacl` goes on printing
 * `user:svc:rwx` while what it actually grants is nothing, which is why it also prints
 * `#effective:`. This resource reads the effective permission rather than the nominal one, so a
 * mask that has quietly suppressed a grant shows up as drift instead of as a mystery. If a
 * `Directory` sets the mode on the same path, it must run first — declare the ACL `dependsOn` it.
 *
 * **There is no recursive argument, and leaving it out is the design.** Applying an ACL across
 * files that already exist cannot be read back: verifying it means walking the whole tree on every
 * refresh, which on a storage pool is not a refresh anybody would run. A resource that wrote
 * without reading would be a command that reports success, which is the thing this package exists
 * to avoid. `defaultEntries` is the declarative half — every child created from now on inherits it
 * and the read can prove it. For files that are already there, do it once by hand:
 * `sudo setfacl -R -m u:svc:rwX /path`, with the capital `X` so existing files do not all become
 * executable.
 */
export interface PosixAclArgs {
  /** The path the entries are on. This is the resource's identity. */
  path: string;
  /** Entries governing this path itself. */
  entries?: AclEntryArgs[];
  /** Entries inherited by children created inside it from now on. Directories only. */
  defaultEntries?: AclEntryArgs[];
}

/** An entry as the machine reports it. */
export interface AclEntry {
  type: AclEntryType;
  name: string;
  /** What the entry says. */
  perms: string;
  /**
   * What it grants once the mask is applied.
   *
   * The one that matters, and the one a `chmod` changes without touching `perms`.
   */
  effective: string;
}

interface PosixAclState {
  path: string;
  entries: AclEntryArgs[];
  defaultEntries: AclEntryArgs[];
  /** Every declared entry as the machine reports it, including the ones that are not there. */
  actual: AclEntry[];
  defaultActual: AclEntry[];
}

/**
 * The three-character form `getfacl` prints, from any of the ways people write permissions.
 *
 * `rw` and `7` and `rw-` are the same grant, and comparing the string somebody typed against the
 * string the machine printed would report drift between two spellings of one thing.
 */
export function normalisePerms(perms: string): string {
  const text = perms.trim();
  if (/^[0-7]$/.test(text)) {
    const bits = Number(text);
    return `${bits & 4 ? 'r' : '-'}${bits & 2 ? 'w' : '-'}${bits & 1 ? 'x' : '-'}`;
  }
  if (/X/.test(text)) {
    // setfacl takes X as "execute only where it is already set", resolves it per file, and getfacl
    // never prints it back. Declared here it would read as drift on every refresh, for ever
    throw new Error(
      `${perms} uses setfacl's conditional X, which resolves differently per file and is never `
      + `reported back, so it can only ever read as drift. Write x or - for what this path should have.`,
    );
  }
  if (!/^[rwx-]{1,3}$/.test(text)) throw new Error(`${perms} is not a permission set: write rwx, r-x, rw or an octal digit`);
  return `${text.includes('r') ? 'r' : '-'}${text.includes('w') ? 'w' : '-'}${text.includes('x') ? 'x' : '-'}`;
}

/**
 * Why an entry cannot be declared here, or null when it can.
 *
 * The base entries — `user::`, `group::`, `other::` — *are* the mode bits, and `mask::` is computed
 * by setfacl from the entries around it. Declaring any of them in `entries` would put this resource
 * and `Directory`'s `mode` in a fight over the same three numbers, which one of them would lose
 * silently on every deployment. As *defaults* they are a different thing entirely: no mode sets
 * them, and `default:other::---` is an ordinary and useful declaration.
 */
export function entryRefusal(entry: AclEntryArgs, scope: AclScope): string | null {
  const named = (entry.name ?? '') !== '';
  if (entry.type === 'mask') {
    return `a mask is computed by setfacl from the entries around it, and declaring one would be `
      + `overwritten the next time any entry changed. Declare the entries you want and let the mask follow.`;
  }
  if (entry.type === 'other' && named) return `an other entry names nobody, so it takes no name`;
  if (!named && scope === 'access') {
    const bits = { user: 'owner', group: 'group', other: 'other' }[entry.type];
    return `${entry.type}:: is the ${bits} mode bits under another name, and setting it here would `
      + `fight Directory's mode over the same three numbers. Set the mode there. As a defaultEntry it `
      + `is a different thing and is allowed, because no mode sets that.`;
  }
  return null;
}

/** An entry reduced to what identifies it, so two spellings of one entry compare equal. */
export function entryKey(entry: { type: AclEntryType; name?: string }): string {
  return `${entry.type}:${entry.name ?? ''}`;
}

/** `default:user:svc:rwx`, the form both setfacl and getfacl use. */
export function formatEntry(entry: AclEntryArgs, scope: AclScope): string {
  return `${scope === 'default' ? 'default:' : ''}${entry.type}:${entry.name ?? ''}:${normalisePerms(entry.perms)}`;
}

/** The same entry with no permissions, which is how setfacl is told to remove one. */
export function formatRemoval(entry: { type: AclEntryType; name?: string }, scope: AclScope): string {
  return `${scope === 'default' ? 'default:' : ''}${entry.type}:${entry.name ?? ''}`;
}

/**
 * The ACL as `getfacl` reports it.
 *
 * Effective permissions rather than nominal ones: a line reading `user:svc:rwx  #effective:r-x` is a
 * grant the mask has suppressed, and reading the first half of it would report a machine as correct
 * while the account it names could not write.
 */
export function parseGetfacl(out: string): { access: AclEntry[]; defaults: AclEntry[] } {
  const access: AclEntry[] = [];
  const defaults: AclEntry[] = [];
  for (const raw of out.split('\n')) {
    const line = raw.trim();
    // the header lines are comments; `#effective:` is a trailing comment on an entry and is handled
    // below, never at the start of a line
    if (line === '' || line.startsWith('#')) continue;
    const [body = '', comment = ''] = line.split('#');
    const fields = body.trim().split(':');
    const isDefault = fields[0] === 'default';
    const [type, name, perms] = isDefault ? fields.slice(1) : fields;
    if (type === undefined || name === undefined || perms === undefined) continue;
    if (type !== 'user' && type !== 'group' && type !== 'mask' && type !== 'other') continue;
    const effective = comment.startsWith('effective:') ? comment.slice('effective:'.length).trim() : perms.trim();
    (isDefault ? defaults : access).push({ type, name, perms: perms.trim(), effective });
  }
  return { access, defaults };
}

/** What the machine says about one declared entry, or an absent one when it has nothing to say. */
export function findEntry(actual: AclEntry[], wanted: AclEntryArgs): AclEntry {
  const found = actual.find((entry) => entryKey(entry) === entryKey(wanted));
  // absent and present-with-nothing are different states, and `---` would conflate them
  return found ?? { type: wanted.type, name: wanted.name ?? '', perms: '', effective: '' };
}

/**
 * Whether an entry is satisfied.
 *
 * Both halves, and that is the point: nominal alone misses a mask that has suppressed the grant,
 * and effective alone accepts an entry that says more than it was asked to and will start granting
 * it the moment some unrelated change raises the mask.
 */
export function entrySatisfied(actual: AclEntry, wanted: AclEntryArgs): boolean {
  const perms = normalisePerms(wanted.perms);
  return actual.perms === perms && actual.effective === perms;
}

/** Every declared entry the machine does not already agree about. */
export function unsatisfied(
  actual: AclEntry[],
  wanted: AclEntryArgs[],
  scope: AclScope,
): { entry: AclEntryArgs; scope: AclScope; found: AclEntry }[] {
  return wanted
    .map((entry) => ({ entry, scope, found: findEntry(actual, entry) }))
    .filter(({ entry, found }) => !entrySatisfied(found, entry));
}

/** Entries this resource used to declare and no longer does, which are its to take away. */
export function abandoned(previous: AclEntryArgs[], wanted: AclEntryArgs[]): AclEntryArgs[] {
  const keys = wanted.map(entryKey);
  return previous.filter((entry) => !keys.includes(entryKey(entry)));
}

/**
 * One `setfacl` for everything that needs changing, or null when nothing does.
 *
 * Removals come first. Re-declaring an entry under a permission it already holds is a no-op, but
 * removing one *after* setting it would take away the thing just granted, and the two lists can
 * overlap when a declaration moves an entry between scopes.
 */
export function setfaclCommand(
  path: string,
  set: { entry: AclEntryArgs; scope: AclScope }[],
  remove: { entry: AclEntryArgs; scope: AclScope }[],
): string | null {
  const commands: string[] = [];
  if (remove.length > 0) {
    commands.push(`setfacl -x ${shellQuote(remove.map(({ entry, scope }) => formatRemoval(entry, scope)).join(','))} ${shellQuote(path)}`);
  }
  if (set.length > 0) {
    commands.push(`setfacl -m ${shellQuote(set.map(({ entry, scope }) => formatEntry(entry, scope)).join(','))} ${shellQuote(path)}`);
  }
  return commands.length === 0 ? null : commands.join(' && ');
}

/** Everything declared, with the refusals raised once rather than at each use. */
export function resolveEntries(args: PosixAclArgs): { entries: AclEntryArgs[]; defaultEntries: AclEntryArgs[] } {
  const settle = (entries: AclEntryArgs[], scope: AclScope) => entries
    .map((entry) => {
      const refusal = entryRefusal(entry, scope);
      if (refusal !== null) throw new Error(`${formatRemoval(entry, scope)} on ${args.path}: ${refusal}`);
      return { type: entry.type, name: entry.name ?? '', perms: normalisePerms(entry.perms) };
    })
    // sorted so that reordering a declaration is not a change, and state does not churn
    .sort((a, b) => entryKey(a).localeCompare(entryKey(b)));
  return {
    entries: settle(args.entries ?? [], 'access'),
    defaultEntries: settle(args.defaultEntries ?? [], 'default'),
  };
}

/** The ACL on a path, or null when there is nothing at that path. */
export async function readAcl(host: Target, path: string): Promise<{ access: AclEntry[]; defaults: AclEntry[] } | null> {
  const asked = await ask(host, escalate(host,
    // a path that is not there is an answer rather than a fault, and 9 is not a code getfacl uses
    `test -e ${shellQuote(path)} || exit 9; getfacl -pc ${shellQuote(path)}`,
  ));
  if (asked.code === 9) return null;
  if (asked.code === 127) {
    throw new Error(
      `getfacl is not installed on ${describe(host)} — it is in the acl package, which you can `
      + `declare with new AptPackage('acl', host, { name: 'acl' })`,
    );
  }
  if (asked.code !== 0) throw new Error(`could not read the ACL on ${path}: ${asked.err.trim()}`);
  return parseGetfacl(asked.out);
}

function providerFor(host: Target): pulumi.dynamic.ResourceProvider<PosixAclArgs, PosixAclState> {
  const settle = async (args: PosixAclArgs, previous: PosixAclState | null): Promise<PosixAclState> => {
    const wanted = resolveEntries(args);
    const before = await readAcl(host, args.path);
    if (before === null) {
      throw new Error(`nothing exists at ${args.path} on ${describe(host)}, so there is no ACL to set on it`);
    }

    const set = [
      ...unsatisfied(before.access, wanted.entries, 'access'),
      ...unsatisfied(before.defaults, wanted.defaultEntries, 'default'),
    ].map(({ entry, scope }) => ({ entry, scope }));
    const remove = [
      ...abandoned(previous?.entries ?? [], wanted.entries).map((entry) => ({ entry, scope: 'access' as const })),
      ...abandoned(previous?.defaultEntries ?? [], wanted.defaultEntries).map((entry) => ({ entry, scope: 'default' as const })),
    ];

    const command = setfaclCommand(args.path, set, remove);
    if (command !== null) await must(host, escalate(host, command));

    const after = await readAcl(host, args.path);
    if (after === null) throw new Error(`${args.path} on ${describe(host)} disappeared while its ACL was being set`);
    const actual = wanted.entries.map((entry) => findEntry(after.access, entry));
    const defaultActual = wanted.defaultEntries.map((entry) => findEntry(after.defaults, entry));

    const stillWrong = [
      ...wanted.entries.filter((entry, at) => !entrySatisfied(actual[at]!, entry)).map((e) => formatEntry(e, 'access')),
      ...wanted.defaultEntries.filter((entry, at) => !entrySatisfied(defaultActual[at]!, entry)).map((e) => formatEntry(e, 'default')),
    ];
    if (stillWrong.length > 0) {
      throw new Error(
        `set ${stillWrong.join(', ')} on ${args.path} at ${describe(host)} and the ACL still does not `
        + `grant it — a mask is suppressing it, which a chmod after this would explain`,
      );
    }
    return { path: args.path, ...wanted, actual, defaultActual };
  };

  return {
    async check(_olds, news) {
      const failures: pulumi.dynamic.CheckFailure[] = [];
      for (const [scope, entries] of [['access', news.entries], ['default', news.defaultEntries]] as const) {
        for (const entry of entries ?? []) {
          const refusal = entryRefusal(entry, scope);
          if (refusal !== null) {
            failures.push({ property: scope === 'access' ? 'entries' : 'defaultEntries', reason: `${formatRemoval(entry, scope)}: ${refusal}` });
          }
        }
      }
      // the declaration is wrong rather than the machine, so it is worth saying at preview
      return { inputs: news, failures };
    },

    async create(args) {
      return { id: args.path, outs: await settle(args, null) };
    },

    async read(id, state) {
      const actual = await readAcl(host, id);
      // no path there any more: Pulumi forgets it rather than reporting an ACL on nothing
      if (actual === null) return { id: undefined, props: undefined };
      const entries = state?.entries ?? [];
      const defaultEntries = state?.defaultEntries ?? [];
      return {
        id,
        props: {
          ...state,
          path: id,
          entries,
          defaultEntries,
          // the two that come from the machine rather than from what was remembered
          actual: entries.map((entry) => findEntry(actual.access, entry)),
          defaultActual: defaultEntries.map((entry) => findEntry(actual.defaults, entry)),
        },
      };
    },

    async update(id, old, args) {
      return { outs: await settle({ ...args, path: id }, old) };
    },

    async diff(_id, old, args) {
      const wanted = resolveEntries({ ...args, path: old.path });
      const declared = (entries: AclEntryArgs[]) => entries.map((e) => formatEntry(e, 'access')).join(',');
      return {
        changes: transportChanged(old)
          || declared(old.entries ?? []) !== declared(wanted.entries)
          || declared(old.defaultEntries ?? []) !== declared(wanted.defaultEntries)
          // what the machine grants, against what was asked for. This is where a chmod that
          // recomputed the mask and suppressed a named entry shows up
          || unsatisfied(old.actual ?? [], old.entries ?? [], 'access').length > 0
          || unsatisfied(old.defaultActual ?? [], old.defaultEntries ?? [], 'default').length > 0,
        replaces: old.path !== args.path ? ['path'] : [],
        stables: [],
        deleteBeforeReplace: true,
      };
    },

    async delete(id, state) {
      const path = state.path || id;
      const remove = [
        ...(state.entries ?? []).map((entry) => ({ entry, scope: 'access' as const })),
        ...(state.defaultEntries ?? []).map((entry) => ({ entry, scope: 'default' as const })),
      ];
      const command = setfaclCommand(path, [], remove);
      // only what this resource declared, never `setfacl -b`: the entries somebody else put on a
      // shared pool are not this resource's to take away
      if (command !== null) await must(host, escalate(host, command));
    },
  };
}

/** ACL entries on a path, read back with `getfacl` and compared on what they actually grant. */
export class PosixAcl extends pulumi.dynamic.Resource {
  declare readonly path: pulumi.Output<string>;
  /** Each declared access entry as the machine reports it, effective permissions and all. */
  declare readonly actual: pulumi.Output<AclEntry[]>;
  declare readonly defaultActual: pulumi.Output<AclEntry[]>;

  constructor(name: string, host: Target, args: PosixAclArgs, opts?: pulumi.CustomResourceOptions) {
    super(stamped(providerFor(host)), name, {
      entries: [],
      defaultEntries: [],
      actual: undefined,
      defaultActual: undefined,
      ...args,
    }, withLegacyAlias(opts), 'homelab', 'PosixAcl');
  }
}
