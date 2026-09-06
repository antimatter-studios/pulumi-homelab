import { describe, expect, it } from 'vitest';
import { effectiveShare, parseSambaUsers, parseSections, removeSection, shareSection, upsertSection } from './samba.ts';

/**
 * The sample is the real configuration off the Pi, as `testparm -s` prints it: two shares over one
 * path, one guest-readable and one writable by a named user.
 */
const TESTPARM = `[global]
	log file = /var/log/samba/log.%m
	server role = STANDALONE SERVER

[public]
	guest ok = Yes
	path = /mnt/data

[admin]
	path = /mnt/data
	read only = No
	valid users = admin
`;

/** A hand-written smb.conf with the comments a person leaves in one. */
const SMB_CONF = `# Sambaconfig, hand-edited 2024
[global]
   workgroup = WORKGROUP
   # player needs this
   server min protocol = SMB2

[public]
   path = /mnt/data
   guest ok = yes

[admin]
   path = /mnt/data
   read only = no
   valid users = admin
`;

describe('reading what samba makes of its own configuration', () => {
  it('finds a share and its settings', () => {
    expect(effectiveShare(TESTPARM, 'admin')).toEqual({
      path: '/mnt/data', 'read only': 'No', 'valid users': 'admin',
    });
  });

  it('keeps keys that have spaces in them, which most samba keys do', () => {
    expect(effectiveShare(TESTPARM, 'public')?.['guest ok']).toBe('Yes');
  });

  it('says nothing rather than something wrong about a share that is not there', () => {
    expect(effectiveShare(TESTPARM, 'media')).toBeNull();
  });

  it('reads [global] as a section like any other', () => {
    expect(parseSections(TESTPARM).has('global')).toBe(true);
  });
});

describe('editing one section of the file', () => {
  it('replaces a section in place, leaving the rest byte for byte', () => {
    const updated = upsertSection(SMB_CONF, 'public', shareSection('public', '/mnt/data/public', { 'guest ok': 'no' }));
    expect(updated).toContain('[public]\n   path = /mnt/data/public\n   guest ok = no');
    // the hand-written parts survive: the file comment, the note about player, and the other share
    expect(updated).toContain('# Sambaconfig, hand-edited 2024');
    expect(updated).toContain('   # player needs this');
    expect(updated).toContain('valid users = admin');
  });

  it('does not swallow the section that follows the one it edits', () => {
    // the end of a section is the next heading, and getting that wrong eats every share below it
    const updated = upsertSection(SMB_CONF, 'public', shareSection('public', '/x', {}));
    expect(parseSections(updated).has('admin')).toBe(true);
    expect(parseSections(updated).has('global')).toBe(true);
  });

  it('appends a share the file does not have', () => {
    const updated = upsertSection(SMB_CONF, 'media', shareSection('media', '/mnt/data/media', { 'read only': 'yes' }));
    expect(parseSections(updated).has('media')).toBe(true);
    expect(parseSections(updated).has('public')).toBe(true);
  });

  it('gives the same file whether it runs once or twice', () => {
    const section = shareSection('public', '/mnt/data', { 'guest ok': 'yes' });
    const once = upsertSection(SMB_CONF, 'public', section);
    expect(upsertSection(once, 'public', section)).toBe(once);
  });

  it('puts path first, since it is the line anybody looks for', () => {
    expect(shareSection('x', '/p', { 'read only': 'no' })).toBe('[x]\n   path = /p\n   read only = no');
  });
});

describe('removing a section', () => {
  it('takes out the share and nothing else', () => {
    const without = removeSection(SMB_CONF, 'public');
    expect(parseSections(without).has('public')).toBe(false);
    expect(parseSections(without).has('admin')).toBe(true);
    expect(without).toContain('# player needs this');
  });

  it('leaves a file alone when the share is not in it', () => {
    expect(removeSection(SMB_CONF, 'media')).toBe(SMB_CONF);
  });
});

describe('reading samba accounts', () => {
  it('takes the name out of pdbedit output', () => {
    expect(parseSambaUsers('admin:1000:\n')).toEqual(['admin']);
  });

  it('reads several, and ignores the blank line at the end', () => {
    expect(parseSambaUsers('admin:1000:\nmedia:1001:\n\n')).toEqual(['admin', 'media']);
  });

  it('says nothing on a machine with no samba accounts', () => {
    expect(parseSambaUsers('')).toEqual([]);
  });
});
