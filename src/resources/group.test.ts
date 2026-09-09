import { describe, expect, it } from 'vitest';
import { parseGroupEntry, renumberRefusal } from './group.ts';

/**
 * Filesystem permissions do not record group names, they record numbers. A directory owned by
 * `storage` is on disk as `1002`, so a rebuild where `groupadd` hands out 1003 leaves two terabytes
 * of files owned by a group that does not exist — a failure that looks exactly like success.
 */
describe('reading a group out of getent', () => {
  it('reads the name, the number, and who is in it', () => {
    expect(parseGroupEntry('storage:x:1002:admin,player')).toEqual({
      name: 'storage', gid: 1002, members: ['admin', 'player'],
    });
  });

  it('reads a group with nobody in it, which is most of them', () => {
    // an empty member field is an empty list, not a list containing one empty name
    expect(parseGroupEntry('readonly:x:1003:')).toEqual({ name: 'readonly', gid: 1003, members: [] });
  });

  it('reads gid 0, which is falsy and would be lost by a lazier check', () => {
    expect(parseGroupEntry('root:x:0:')?.gid).toBe(0);
  });

  it('says nothing rather than something wrong about a line that is not a group', () => {
    expect(parseGroupEntry('')).toBeNull();
    expect(parseGroupEntry('getent: command not found')).toBeNull();
    expect(parseGroupEntry('storage:x:notanumber:')).toBeNull();
  });
});

/**
 * A guard nobody has seen fire is a guard nobody knows the wording of — and this one cannot be
 * exercised against a machine without orphaning the files it exists to protect.
 *
 * `groupmod -g` renumbers the group and does **not** chown anything, so every file owned by the old
 * number is orphaned by a command that reports success. Filesystem permissions store numbers, not
 * names.
 */
describe('refusing to renumber a group', () => {
  it('refuses a gid change, and says what would be orphaned', () => {
    const refusal = renumberRefusal({ name: 'storage', gid: 1003 }, { gid: 1002 });
    expect(refusal).toContain('exists with gid 1002 and the code says 1003');
    expect(refusal).toContain('orphaned');
  });

  it('says what to run afterwards, because the message is the product', () => {
    expect(renumberRefusal({ name: 'storage', gid: 1003 }, { gid: 1002 }))
      .toContain('find / -xdev -gid 1002 -exec chgrp 1003');
  });

  it('permits it when somebody said they meant it', () => {
    expect(renumberRefusal({ name: 'storage', gid: 1003, renumber: true }, { gid: 1002 })).toBeNull();
  });

  it('has nothing to refuse when the gid already matches', () => {
    expect(renumberRefusal({ name: 'storage', gid: 1002 }, { gid: 1002 })).toBeNull();
  });

  it('has nothing to refuse when no gid was declared', () => {
    // a group whose files nothing owns does not need one pinned
    expect(renumberRefusal({ name: 'storage' }, { gid: 1002 })).toBeNull();
  });

  it('has nothing to refuse for a group that does not exist yet', () => {
    expect(renumberRefusal({ name: 'storage', gid: 1002 }, null)).toBeNull();
  });
});
