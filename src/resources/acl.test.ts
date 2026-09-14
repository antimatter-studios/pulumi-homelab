import { describe, expect, it } from 'vitest';
import {
  abandoned,
  entryKey,
  entryRefusal,
  entrySatisfied,
  findEntry,
  formatEntry,
  formatRemoval,
  normalisePerms,
  parseGetfacl,
  resolveEntries,
  setfaclCommand,
  unsatisfied,
  type AclEntry,
} from './acl.ts';

/**
 * Taken from a storage pool with two access tiers granted by named groups, which is the layout this
 * resource exists for: a service account needs one directory in it, and neither group membership
 * nor ownership can say that without granting far more.
 */
const POOL = [
  'user::rwx',
  'user:svc:rwx',
  'group::rwx',
  'group:rwgroup:rwx',
  'group:rogroup:r-x',
  'mask::rwx',
  'other::r-x',
  'default:user::rwx',
  'default:user:svc:rwx',
  'default:group::rwx',
  'default:group:rwgroup:rwx',
  'default:mask::rwx',
  'default:other::r-x',
].join('\n');

describe('the ways people write permissions', () => {
  it('settles every spelling on the three characters getfacl prints', () => {
    // comparing what somebody typed against what the machine printed would report drift between
    // two spellings of one grant, for ever
    expect(normalisePerms('rwx')).toBe('rwx');
    expect(normalisePerms('rw')).toBe('rw-');
    expect(normalisePerms('r-x')).toBe('r-x');
    expect(normalisePerms('x')).toBe('--x');
    expect(normalisePerms('-')).toBe('---');
  });

  it('takes an octal digit, which is how a mode is usually thought about', () => {
    expect(normalisePerms('7')).toBe('rwx');
    expect(normalisePerms('5')).toBe('r-x');
    expect(normalisePerms('6')).toBe('rw-');
    expect(normalisePerms('0')).toBe('---');
  });

  it('refuses the conditional X, which can only ever read as drift', () => {
    // setfacl resolves X per file according to what is already executable, and getfacl never prints
    // it back — so a declaration containing one can never match what the machine reports
    expect(() => normalisePerms('rwX')).toThrow(/drift/);
  });

  it('refuses anything that is not a permission set', () => {
    expect(() => normalisePerms('rwxr')).toThrow();
    expect(() => normalisePerms('8')).toThrow();
    expect(() => normalisePerms('')).toThrow();
  });
});

/**
 * The base entries are the mode bits wearing another name. A resource that set them here and a
 * `Directory` that set the mode would overwrite each other on alternate deployments, and neither
 * would ever report anything wrong.
 */
describe('what may be declared', () => {
  it('allows a named user or group, which is the whole point of the resource', () => {
    expect(entryRefusal({ type: 'user', name: 'svc', perms: 'rwx' }, 'access')).toBeNull();
    expect(entryRefusal({ type: 'group', name: 'rogroup', perms: 'r-x' }, 'access')).toBeNull();
  });

  it('refuses the base entries as access entries, and names the resource that owns them', () => {
    for (const type of ['user', 'group', 'other'] as const) {
      const refusal = entryRefusal({ type, perms: 'rwx' }, 'access');
      expect(refusal).toContain('mode');
      expect(refusal).toContain('Directory');
    }
  });

  it('allows the same base entries as defaults, because no mode sets those', () => {
    // `default:other::---` is an ordinary declaration: it governs children that do not exist yet,
    // which no chmod on this directory can express
    expect(entryRefusal({ type: 'user', perms: 'rwx' }, 'default')).toBeNull();
    expect(entryRefusal({ type: 'other', perms: '---' }, 'default')).toBeNull();
  });

  it('refuses a mask in either scope', () => {
    // setfacl recomputes it from the entries around it, so a declared mask would be overwritten by
    // the next unrelated change and report drift ever after
    expect(entryRefusal({ type: 'mask', perms: 'rwx' }, 'access')).toContain('computed');
    expect(entryRefusal({ type: 'mask', perms: 'rwx' }, 'default')).toContain('computed');
  });

  it('refuses an other entry that names somebody', () => {
    expect(entryRefusal({ type: 'other', name: 'svc', perms: 'rwx' }, 'access')).toContain('no name');
  });
});

describe('reading what the machine says', () => {
  it('reads both scopes apart', () => {
    const { access, defaults } = parseGetfacl(POOL);
    expect(access.map(entryKey)).toContain('user:svc');
    expect(defaults.map(entryKey)).toContain('user:svc');
    // a default entry grants nothing to the directory itself and an access entry grants nothing to
    // its children; folding them together is the mistake this resource exists to make hard
    expect(access.map(entryKey)).not.toContain('group:rogroup:default');
    expect(defaults.some((e) => entryKey(e) === 'group:rogroup')).toBe(false);
  });

  it('keeps the owning entries distinguishable from the named ones', () => {
    const { access } = parseGetfacl(POOL);
    expect(access.find((e) => e.type === 'user' && e.name === '')?.perms).toBe('rwx');
    expect(access.find((e) => e.type === 'user' && e.name === 'svc')?.perms).toBe('rwx');
  });

  it('reports what a mask actually leaves, not what the entry claims', () => {
    // this is the failure a chmod causes: the entry is still listed at rwx and grants nothing, and
    // reading the first half of the line would call the machine correct while the account cannot write
    const suppressed = 'user:svc:rwx\t#effective:r--\nmask::r--\n';
    const entry = parseGetfacl(suppressed).access[0] as AclEntry;
    expect(entry.perms).toBe('rwx');
    expect(entry.effective).toBe('r--');
  });

  it('treats an entry with no effective comment as granting what it says', () => {
    expect(parseGetfacl('user:svc:rwx\n').access[0]?.effective).toBe('rwx');
  });

  it('skips the comment header without skipping the entries', () => {
    const withHeader = `# file: mnt/pool\n# owner: root\n# group: rwgroup\n# flags: -s-\n${POOL}`;
    expect(parseGetfacl(withHeader).access.length).toBe(parseGetfacl(POOL).access.length);
  });

  it('survives the blank lines getfacl puts between paths', () => {
    expect(parseGetfacl(`user:svc:rwx\n\n\nuser:other:r-x\n`).access).toHaveLength(2);
  });
});

describe('an entry the machine has nothing to say about', () => {
  const actual = parseGetfacl(POOL).access;

  it('comes back absent rather than missing', () => {
    const found = findEntry(actual, { type: 'user', name: 'nobody', perms: 'rwx' });
    expect(found.perms).toBe('');
    expect(found.effective).toBe('');
  });

  it('is not the same as an entry granting nothing', () => {
    // `---` is a decision somebody made and `` is an entry that was never set; conflating them
    // would make a revoked grant look like one that was never asked for
    expect(findEntry(actual, { type: 'user', name: 'nobody', perms: 'rwx' }).perms)
      .not.toBe(normalisePerms('---'));
  });

  it('is never satisfied', () => {
    expect(entrySatisfied(findEntry(actual, { type: 'user', name: 'nobody', perms: 'rwx' }),
      { type: 'user', name: 'nobody', perms: 'rwx' })).toBe(false);
  });
});

describe('deciding whether an entry is satisfied', () => {
  const wanted = { type: 'user' as const, name: 'svc', perms: 'rwx' };

  it('is satisfied when both what it says and what it grants match', () => {
    expect(entrySatisfied({ type: 'user', name: 'svc', perms: 'rwx', effective: 'rwx' }, wanted)).toBe(true);
  });

  it('is not satisfied when a mask has suppressed it', () => {
    // the entry reads exactly as declared and the account still cannot write
    expect(entrySatisfied({ type: 'user', name: 'svc', perms: 'rwx', effective: 'r-x' }, wanted)).toBe(false);
  });

  it('is not satisfied when the entry says more than was asked for', () => {
    // effective alone would accept this, and the extra grant would start applying the moment some
    // unrelated change raised the mask
    expect(entrySatisfied({ type: 'user', name: 'svc', perms: 'rwx', effective: 'r-x' },
      { ...wanted, perms: 'r-x' })).toBe(false);
  });

  it('compares the settled spelling, not the one that was typed', () => {
    expect(entrySatisfied({ type: 'user', name: 'svc', perms: 'rw-', effective: 'rw-' },
      { ...wanted, perms: 'rw' })).toBe(true);
    expect(entrySatisfied({ type: 'user', name: 'svc', perms: 'rwx', effective: 'rwx' },
      { ...wanted, perms: '7' })).toBe(true);
  });
});

describe('working out what to change', () => {
  const actual = parseGetfacl(POOL);

  it('leaves alone what the machine already agrees about', () => {
    expect(unsatisfied(actual.access, [{ type: 'user', name: 'svc', perms: 'rwx' }], 'access')).toEqual([]);
  });

  it('picks out the entry that is missing', () => {
    const todo = unsatisfied(actual.access, [
      { type: 'user', name: 'svc', perms: 'rwx' },
      { type: 'user', name: 'backup', perms: 'r-x' },
    ], 'access');
    expect(todo.map((t) => t.entry.name)).toEqual(['backup']);
  });

  it('picks out the entry whose permissions moved', () => {
    const todo = unsatisfied(actual.access, [{ type: 'user', name: 'svc', perms: 'r-x' }], 'access');
    expect(todo).toHaveLength(1);
  });

  it('takes away only what this resource used to declare', () => {
    // the pool root already carries the entries that make the whole scheme work, and a resource
    // that removed what it did not declare would take the estate apart
    const previous = [{ type: 'user' as const, name: 'svc', perms: 'rwx' },
      { type: 'user' as const, name: 'old', perms: 'r-x' }];
    expect(abandoned(previous, [{ type: 'user', name: 'svc', perms: 'rwx' }]).map((e) => e.name)).toEqual(['old']);
  });

  it('does not abandon an entry whose permissions merely changed', () => {
    const previous = [{ type: 'user' as const, name: 'svc', perms: 'rwx' }];
    expect(abandoned(previous, [{ type: 'user', name: 'svc', perms: 'r-x' }])).toEqual([]);
  });
});

describe('composing setfacl', () => {
  const svc = { type: 'user' as const, name: 'svc', perms: 'rwx' };
  const old = { type: 'user' as const, name: 'old', perms: 'r-x' };

  it('has nothing to run when nothing needs changing', () => {
    expect(setfaclCommand('/mnt/pool/p', [], [])).toBeNull();
  });

  it('writes an access entry without the default prefix, and a default with it', () => {
    expect(setfaclCommand('/p', [{ entry: svc, scope: 'access' }], [])).toContain('user:svc:rwx');
    expect(setfaclCommand('/p', [{ entry: svc, scope: 'default' }], [])).toContain('default:user:svc:rwx');
  });

  it('removes before it sets', () => {
    // an entry can move between scopes in one declaration, and removing after setting would take
    // away the grant that was just made
    const command = setfaclCommand('/p', [{ entry: svc, scope: 'default' }], [{ entry: svc, scope: 'access' }]) ?? '';
    expect(command.indexOf('-x')).toBeLessThan(command.indexOf('-m'));
  });

  it('gives removals no permissions, which is the form setfacl wants', () => {
    expect(setfaclCommand('/p', [], [{ entry: old, scope: 'access' }])).toContain("'user:old'");
    expect(setfaclCommand('/p', [], [{ entry: old, scope: 'access' }])).not.toContain('r-x');
  });

  it('sets everything in one call rather than one call per entry', () => {
    const command = setfaclCommand('/p', [
      { entry: svc, scope: 'access' }, { entry: svc, scope: 'default' },
    ], []) ?? '';
    expect(command.match(/setfacl/g)).toHaveLength(1);
    expect(command).toContain('user:svc:rwx,default:user:svc:rwx');
  });

  it('stops when a step fails rather than running the next one', () => {
    const command = setfaclCommand('/p', [{ entry: svc, scope: 'access' }], [{ entry: old, scope: 'access' }]) ?? '';
    expect(command).toContain('&&');
    expect(command).not.toContain(';');
  });

  it('quotes the path and the entries', () => {
    const command = setfaclCommand("/mnt/it's", [{ entry: svc, scope: 'access' }], []) ?? '';
    expect(command).toContain(String.raw`'/mnt/it'\''s'`);
    expect(command).toContain("'user:svc:rwx'");
  });

  it('never reaches for -b or --set, which would remove entries it does not own', () => {
    // a pool root already carries the entries that make the whole scheme work. Setting the ACL
    // wholesale would take them off, and the declaration would look exactly the same either way
    for (const command of [
      setfaclCommand('/p', [{ entry: svc, scope: 'access' }], []),
      setfaclCommand('/p', [], [{ entry: old, scope: 'access' }]),
      setfaclCommand('/p', [{ entry: svc, scope: 'access' }], [{ entry: old, scope: 'access' }]),
    ]) {
      expect(command).not.toMatch(/(^|\s)-{1,2}b\b/);
      expect(command).not.toContain('--set');
      expect(command).not.toContain('--remove-all');
    }
  });

  it('is the same string every time, so a refresh does not look like a change', () => {
    const once = setfaclCommand('/p', [{ entry: svc, scope: 'access' }], []);
    expect(setfaclCommand('/p', [{ entry: svc, scope: 'access' }], [])).toBe(once);
  });
});

describe('formatting one entry', () => {
  it('writes the owning entries with an empty name, as getfacl does', () => {
    expect(formatEntry({ type: 'user', perms: 'rwx' }, 'default')).toBe('default:user::rwx');
    expect(formatRemoval({ type: 'other' }, 'default')).toBe('default:other:');
  });

  it('settles the permissions on the way out', () => {
    expect(formatEntry({ type: 'group', name: 'ro', perms: '5' }, 'access')).toBe('group:ro:r-x');
  });
});

describe('settling the declaration', () => {
  const args = {
    path: '/mnt/pool/project',
    entries: [{ type: 'user' as const, name: 'svc', perms: 'rw' }],
    defaultEntries: [{ type: 'user' as const, name: 'svc', perms: '7' }],
  };

  it('settles the permissions once rather than at each comparison', () => {
    const settled = resolveEntries(args);
    expect(settled.entries[0]?.perms).toBe('rw-');
    expect(settled.defaultEntries[0]?.perms).toBe('rwx');
  });

  it('fills in an absent name so two spellings of the owning entry compare equal', () => {
    expect(resolveEntries({ path: '/p', defaultEntries: [{ type: 'other', perms: '---' }] })
      .defaultEntries[0]?.name).toBe('');
  });

  it('sorts, so reordering a declaration is not a change', () => {
    const forwards = resolveEntries({ path: '/p', entries: [
      { type: 'user', name: 'b', perms: 'rwx' }, { type: 'user', name: 'a', perms: 'r-x' },
    ] });
    const backwards = resolveEntries({ path: '/p', entries: [
      { type: 'user', name: 'a', perms: 'r-x' }, { type: 'user', name: 'b', perms: 'rwx' },
    ] });
    expect(forwards).toEqual(backwards);
  });

  it('raises a refusal with the path in it, since a declaration names several', () => {
    expect(() => resolveEntries({ path: '/mnt/pool/project', entries: [{ type: 'mask', perms: 'rwx' }] }))
      .toThrow(/\/mnt\/pool\/project/);
  });

  it('treats an empty declaration as empty rather than as nothing', () => {
    expect(resolveEntries({ path: '/p' })).toEqual({ entries: [], defaultEntries: [] });
  });
});
