import { describe, expect, it } from 'vitest';
import { sudoersFile, sudoersFileName, sudoersLine } from './sudo.ts';

/**
 * This resource grants privilege, so its two failure modes are a rule that does nothing and a rule
 * that does more than anybody intended. Both are quiet.
 */
describe('naming a drop-in file', () => {
  it('accepts an ordinary name', () => {
    expect(sudoersFileName('admin-nopasswd')).toBe('admin-nopasswd');
  });

  it('refuses a name with a dot, which sudo would ignore without saying so', () => {
    // `#includedir` skips anything with a . or a ~ on the assumption it is an editor backup. No
    // warning, no error, just a NOPASSWD line that never applies and an afternoon spent on why
    expect(() => sudoersFileName('admin.conf')).toThrow(/would never apply/);
    expect(() => sudoersFileName('admin~')).toThrow(/would never apply/);
  });

  it('refuses a path, since a drop-in is one file in one directory', () => {
    expect(() => sudoersFileName('../sudoers')).toThrow();
    expect(() => sudoersFileName('')).toThrow();
  });
});

describe('writing the rule', () => {
  it('grants a user everything without a password when asked to', () => {
    expect(sudoersLine({ user: 'admin', passwordless: true }))
      .toBe('admin ALL=(ALL) NOPASSWD: ALL');
  });

  it('leaves the password requirement in place when not asked to remove it', () => {
    // the difference between these two lines is the whole security posture of the machine, which
    // is why `passwordless` has no default and has to be written down
    expect(sudoersLine({ user: 'admin', passwordless: false }))
      .toBe('admin ALL=(ALL) ALL');
  });

  it('writes a group with the % sudoers wants, so callers do not have to know', () => {
    expect(sudoersLine({ group: 'sudo', passwordless: false }))
      .toBe('%sudo ALL=(ALL) ALL');
  });

  it('narrows to specific commands, which is the form worth encouraging', () => {
    expect(sudoersLine({
      user: 'backup',
      passwordless: true,
      commands: ['/usr/bin/systemctl restart borg', '/usr/bin/borg'],
      runAs: 'root',
    })).toBe('backup ALL=(root) NOPASSWD: /usr/bin/systemctl restart borg, /usr/bin/borg');
  });

  it('refuses a rule that names both a user and a group, or neither', () => {
    // silently picking one would grant a privilege to somebody the code did not name
    expect(() => sudoersLine({ user: 'admin', group: 'sudo', passwordless: true })).toThrow(/exactly one/);
    expect(() => sudoersLine({ passwordless: true })).toThrow(/exactly one/);
  });

  it('says who wrote the file, for whoever finds it in a year', () => {
    const file = sudoersFile({ user: 'admin', passwordless: true });
    expect(file.startsWith('# Managed by Pulumi.')).toBe(true);
    expect(file.endsWith('\n')).toBe(true);
  });
});
