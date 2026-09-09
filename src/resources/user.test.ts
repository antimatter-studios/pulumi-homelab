import { describe, expect, it } from 'vitest';
import { groupsToLose, lockoutRefusal } from './user.ts';

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

/**
 * Two refusals that cannot be exercised against a machine without taking that machine away, which
 * is precisely why they are pure functions. Both are cases where the command succeeds, the resource
 * reports success, and there is no later run in which to notice.
 */
describe('refusing to lock somebody out', () => {
  const base = { name: 'admin', shell: '/bin/bash', groups: REAL, allowGroupRemoval: false };

  it('refuses to give the connecting account a shell it cannot log in with', () => {
    const refusal = lockoutRefusal({ ...base, shell: '/usr/sbin/nologin' }, 'admin', { groups: REAL });
    expect(refusal).toContain('the account this provider connects as');
    expect(refusal).toContain('take away the login being used to apply it');
  });

  it('allows nologin on an account that is not the one connecting', () => {
    // which is the ordinary case: a service account should not have a shell
    expect(lockoutRefusal({ ...base, name: 'backup', shell: '/usr/sbin/nologin' }, 'admin', { groups: [] }))
      .toBeNull();
  });

  it('allows it when the transport has no login to take away', () => {
    // a container or a chroot is reached without logging in as anybody, and inventing a login to
    // compare against would be the guard being confidently wrong
    expect(lockoutRefusal({ ...base, shell: '/usr/sbin/nologin' }, null, { groups: [] })).toBeNull();
  });

  it('recognises every shell that means no login', () => {
    for (const shell of ['/usr/sbin/nologin', '/sbin/nologin', '/bin/false', '/usr/bin/false']) {
      expect(lockoutRefusal({ ...base, shell }, 'admin', { groups: REAL }), shell).not.toBeNull();
    }
  });

  it('refuses to drop memberships the code does not mention, and counts them', () => {
    const refusal = lockoutRefusal({ ...base, groups: ['audio', 'video'] }, 'admin', { groups: REAL });
    expect(refusal).toContain(`remove ${REAL.length - 2} memberships`);
  });

  it('names sudo specially, because that is the one that ends the connection', () => {
    expect(lockoutRefusal({ ...base, groups: ['audio'] }, 'admin', { groups: REAL }))
      .toContain('including sudo');
  });

  it('says membership rather than memberships when only one would go', () => {
    expect(lockoutRefusal({ ...base, groups: [] }, 'admin', { groups: ['audio'] }))
      .toContain('remove 1 membership.');
  });

  it('permits removal when somebody said they meant it', () => {
    expect(lockoutRefusal({ ...base, groups: ['audio'], allowGroupRemoval: true }, 'admin', { groups: REAL }))
      .toBeNull();
  });

  it('has nothing to refuse when the declaration is the whole truth', () => {
    expect(lockoutRefusal(base, 'admin', { groups: REAL })).toBeNull();
  });

  it('has nothing to refuse for an account that does not exist yet', () => {
    expect(lockoutRefusal({ ...base, groups: ['audio'] }, 'admin', null)).toBeNull();
  });
});
