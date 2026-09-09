import { describe, expect, it } from 'vitest';
import {
  effectiveShare, parseSambaUsers, parseSections, parseShareSettings, removeSection, removeSetting,
  shareSection, upsertSection, upsertSetting,
} from './samba.ts';

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

/**
 * `[global]` on a hand-tuned machine is dozens of settings nobody can reproduce from memory, so
 * declaring the section means owning all of them. Refusing to declare it is right; being unable to
 * change one key in it is not.
 */
describe('editing one setting in a section', () => {
  it('replaces a key that is already there', () => {
    const updated = upsertSetting(SMB_CONF, 'global', 'workgroup', 'HOMELAB');
    expect(parseShareSettings(parseSections(updated).get('global') ?? []).workgroup).toBe('HOMELAB');
  });

  it('leaves every other setting in the section alone', () => {
    // the whole reason this resource exists rather than SambaShare owning [global]
    const updated = upsertSetting(SMB_CONF, 'global', 'netbios name', 'HOMELAB');
    expect(updated).toContain('server min protocol = SMB2');
    expect(updated).toContain('   # player needs this');
    expect(parseSections(updated).has('public')).toBe(true);
  });

  it('adds a key the section does not have', () => {
    const updated = upsertSetting(SMB_CONF, 'global', 'netbios name', 'HOMELAB');
    expect(parseShareSettings(parseSections(updated).get('global') ?? [])['netbios name']).toBe('HOMELAB');
  });

  it('matches a key however it was spaced or capitalised', () => {
    // smb.conf keys contain spaces and are written with whatever alignment somebody liked
    const spaced = '[global]\n   Netbios   Name   =   OLD\n';
    const updated = upsertSetting(spaced, 'global', 'netbios name', 'NEW');
    expect(updated).toContain('netbios name = NEW');
    expect(updated).not.toContain('OLD');
  });

  it('does not touch a commented-out setting of the same name', () => {
    const commented = '[global]\n   # netbios name = OLD\n';
    expect(upsertSetting(commented, 'global', 'netbios name', 'NEW')).toContain('# netbios name = OLD');
  });

  it('gives the same file whether it runs once or twice', () => {
    const once = upsertSetting(SMB_CONF, 'global', 'netbios name', 'HOMELAB');
    expect(upsertSetting(once, 'global', 'netbios name', 'HOMELAB')).toBe(once);
  });

  it('removes one key and leaves the section standing', () => {
    const without = removeSetting(SMB_CONF, 'global', 'workgroup');
    expect(parseSections(without).has('global')).toBe(true);
    expect(without).toContain('server min protocol = SMB2');
    expect(without).not.toContain('workgroup');
  });
});
