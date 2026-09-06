import { describe, expect, it } from 'vitest';
import { parseGroupEntry } from './group.ts';

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
