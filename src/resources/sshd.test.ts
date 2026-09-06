import { describe, expect, it } from 'vitest';
import { hasInclude, keywordFor, normaliseSshdValue, parseSshdT, renderSshd, sshdDropIn } from './sshd.ts';

/**
 * `sshd -T` does not print what you write, and comparing the two directly is permanent false drift
 * on a machine nobody has touched — the same family as `stat` answering `644` where the code says
 * `0644`. All three differences below were observed on a real machine.
 */
describe('reading what sshd resolved', () => {
  const REAL = `passwordauthentication no
pubkeyauthentication yes
permitrootlogin without-password
maxauthtries 6
maxstartups 10:30:100
gatewayports no`;

  it('reads the keywords sshd prints, which are lowercased', () => {
    expect(parseSshdT(REAL).passwordauthentication).toBe('no');
    expect(parseSshdT(REAL).maxauthtries).toBe('6');
  });

  it('turns without-password into the spelling anybody would write', () => {
    // this is the whole reason the function exists: `PermitRootLogin prohibit-password` is what a
    // person writes and `without-password` is what sshd prints, and they are the same setting.
    // Compared directly, that is drift on every refresh and a "correction" on every deployment
    expect(parseSshdT(REAL).permitrootlogin).toBe('prohibit-password');
    expect(normaliseSshdValue('without-password')).toBe('prohibit-password');
  });

  it('leaves every other value exactly as sshd said it', () => {
    expect(normaliseSshdValue('prohibit-password')).toBe('prohibit-password');
    expect(parseSshdT(REAL).maxstartups).toBe('10:30:100');
  });

  it('keeps a value containing spaces whole', () => {
    expect(parseSshdT('subsystem sftp /usr/lib/openssh/sftp-server').subsystem)
      .toBe('sftp /usr/lib/openssh/sftp-server');
  });

  it('says nothing when sshd could not answer', () => {
    // -T needs a full parse, so it fails on a machine whose config is already broken
    expect(parseSshdT('')).toEqual({});
  });
});

describe('writing sshd’s own keywords', () => {
  it('capitalises a property into the keyword sshd uses', () => {
    expect(keywordFor('passwordAuthentication')).toBe('PasswordAuthentication');
    expect(keywordFor('maxAuthTries')).toBe('MaxAuthTries');
  });

  it('knows the ones that are not simply capitalised', () => {
    // UsePAM, UseDNS, X11Forwarding and MACs are spelled the way sshd spells them, not the way a
    // capitalisation rule would
    expect(keywordFor('usePAM')).toBe('UsePAM');
    expect(keywordFor('useDNS')).toBe('UseDNS');
    expect(keywordFor('x11Forwarding')).toBe('X11Forwarding');
    expect(keywordFor('macs')).toBe('MACs');
  });

  it('writes booleans as yes and no, which sshd requires', () => {
    // sshd *rejects* `false` rather than ignoring it, so this one fails at start — and the machine
    // is reached only through sshd
    expect(renderSshd({ passwordAuthentication: false, pubkeyAuthentication: true }))
      .toEqual({ PasswordAuthentication: 'no', PubkeyAuthentication: 'yes' });
  });

  it('writes lists comma-separated', () => {
    expect(renderSshd({ allowUsers: ['admin', 'deploy'] })).toEqual({ AllowUsers: 'admin,deploy' });
  });

  it('writes numbers as themselves', () => {
    expect(renderSshd({ maxAuthTries: 3, port: 22 })).toEqual({ MaxAuthTries: '3', Port: '22' });
  });

  it('leaves out what was never set', () => {
    expect(renderSshd({})).toEqual({});
  });

  it('lets unchecked reach a keyword the type does not name', () => {
    expect(renderSshd({}, { RekeyLimit: '1G 1h' })).toEqual({ RekeyLimit: '1G 1h' });
  });
});

describe('scoping a change to a Match block', () => {
  it('writes a plain file when there is no match', () => {
    const file = sshdDropIn({ PasswordAuthentication: 'no' });
    expect(file).toContain('PasswordAuthentication no');
    expect(file).not.toContain('Match');
  });

  it('puts the settings inside the block, indented', () => {
    // everything after a Match runs to the next Match or the end of the file, which is why this
    // resource writes its own file rather than appending to somebody else's
    const file = sshdDropIn({ PasswordAuthentication: 'yes' }, { Address: '10.0.0.0/24' });
    expect(file).toContain('Match Address 10.0.0.0/24');
    expect(file).toContain('    PasswordAuthentication yes');
  });

  it('joins several criteria into one Match line', () => {
    const file = sshdDropIn({ X11Forwarding: 'no' }, { User: 'deploy', Address: '10.0.0.0/24' });
    expect(file).toContain('Match User deploy Address 10.0.0.0/24');
  });

  it('says who wrote it, for whoever finds it', () => {
    expect(sshdDropIn({}).startsWith('# Managed by Pulumi.')).toBe(true);
  });
});

describe('checking the drop-in is read at all', () => {
  it('finds the Include that makes a drop-in mean anything', () => {
    expect(hasInclude('Include /etc/ssh/sshd_config.d/*.conf\nPort 22\n')).toBe(true);
  });

  it('is not case-sensitive about the keyword, since sshd is not', () => {
    expect(hasInclude('include /etc/ssh/sshd_config.d/*.conf\n')).toBe(true);
  });

  it('reports its absence, which is the silent failure this guards', () => {
    // without it the file is written, looks correct, and is read by nothing — the same shape as a
    // config.txt setting under a filter that never matches
    expect(hasInclude('Port 22\nPermitRootLogin no\n')).toBe(false);
  });

  it('does not accept an Include of somewhere else', () => {
    expect(hasInclude('Include /etc/ssh/other.d/*.conf\n')).toBe(false);
  });
});
