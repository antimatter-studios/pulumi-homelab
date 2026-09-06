import { describe, expect, it } from 'vitest';
import { groupsToLose } from './user.ts';

/**
 * `usermod -G` replaces the supplementary group list rather than adding to it. On the machine this
 * distribution puts an administrator account in a dozen or more groups; declaring three would
 * remove the rest, including `sudo` — which is how this provider connects at all. The resource
 * would sever its own connection and report success, and the fix would need a keyboard attached to
 * the machine.
 */
const REAL = [
  'adm', 'dialout', 'cdrom', 'sudo', 'audio', 'video', 'plugdev', 'games',
  'users', 'input', 'render', 'netdev', 'spi', 'i2c', 'gpio', 'storage', 'player',
];

describe('working out what a declaration would take away', () => {
  it('names every membership the code does not mention', () => {
    const losing = groupsToLose(REAL, ['audio', 'video', 'storage']);
    expect(losing).toContain('sudo');
    expect(losing).toHaveLength(14);
  });

  it('says nothing when the declaration is the whole truth', () => {
    expect(groupsToLose(REAL, REAL)).toEqual([]);
  });

  it('does not complain about a group being added', () => {
    // adding is safe and is what the resource is for; only removal is unrecoverable
    expect(groupsToLose(['audio'], ['audio', 'video', 'newgroup'])).toEqual([]);
  });

  it('sorts, so the message reads the same every time', () => {
    expect(groupsToLose(['video', 'audio'], [])).toEqual(['audio', 'video']);
  });

  it('handles an account in no groups at all', () => {
    expect(groupsToLose([], ['audio'])).toEqual([]);
  });
});
