import { describe, expect, it } from 'vitest';
import { PRIVATE_MODE, PUBLIC_MODE, parseFingerprint, parseHostKeys, parseKeyFiles, permissionsCommand, permissionsWrong } from './sshkey.ts';

/**
 * A fingerprint is the whole identity of a key pair and is safe to print anywhere, which is why it
 * is what this package stores rather than the key. Host keys are read and never managed: a machine
 * presenting new ones has been reinstalled, and that is worth being told loudly rather than
 * discovering from a client that refuses to connect.
 */
describe('reading a fingerprint', () => {
  it('takes the fingerprint out of ssh-keygen’s line', () => {
    expect(parseFingerprint('256 SHA256:8Zc1qgJ+EXAMPLEfingerprintEXAMPLEabcdef admin@host (ED25519)'))
      .toBe('SHA256:8Zc1qgJ+EXAMPLEfingerprintEXAMPLEabcdef');
  });

  it('is unmoved by a comment containing spaces', () => {
    expect(parseFingerprint('3072 SHA256:abc123 a comment with spaces (RSA)')).toBe('SHA256:abc123');
  });

  it('answers empty rather than throwing when there is nothing to read', () => {
    expect(parseFingerprint('')).toBe('');
  });
});

describe('reading the machine’s host keys', () => {
  const REAL = [
    '/etc/ssh/ssh_host_ecdsa_key.pub 256 SHA256:aaaEXAMPLEecdsa root@host (ECDSA)',
    '/etc/ssh/ssh_host_ed25519_key.pub 256 SHA256:bbbEXAMPLEed25519 root@host (ED25519)',
    '/etc/ssh/ssh_host_rsa_key.pub 3072 SHA256:cccEXAMPLErsa root@host (RSA)',
  ].join('\n');

  it('names each key by its algorithm rather than by its path', () => {
    expect(Object.keys(parseHostKeys(REAL)).sort()).toEqual(['ecdsa', 'ed25519', 'rsa']);
  });

  it('reads the fingerprint of each', () => {
    expect(parseHostKeys(REAL).ed25519).toBe('SHA256:bbbEXAMPLEed25519');
  });

  it('skips a key ssh-keygen could not read rather than recording an empty one', () => {
    // an unreadable key would otherwise be reported as a key whose fingerprint is the empty string,
    // which reads as a machine whose identity changed
    expect(parseHostKeys('/etc/ssh/ssh_host_dsa_key.pub\n')).toEqual({});
  });

  it('says nothing on a machine with no host keys at all', () => {
    expect(parseHostKeys('')).toEqual({});
  });
});

/**
 * ssh-keygen runs under escalation, so without an owner the pair lands root-owned and the account
 * it was made for cannot read its own private key. The mode is not safe to leave to ssh-keygen
 * either: a default ACL on the parent is inherited by the new file and can widen what lands, and
 * ssh then refuses the key at use time with nothing wrong at the path to look at.
 */
describe('the files the pair lives in', () => {
  const wanted = { owner: 'svc', group: 'svc' };
  const right = { privateMode: '0600', publicMode: '0644', owner: 'svc', group: 'svc' };

  it('reads both halves, private first', () => {
    expect(parseKeyFiles('600|svc|svc\n644|svc|svc\n')).toEqual(right);
  });

  it('puts the mode in the shape the code writes it in', () => {
    // stat says 600 and everybody writes 0600; comparing the two as strings is how a key nobody
    // has touched reports drift on every refresh
    expect(parseKeyFiles('600|root|root\n644|root|root\n').privateMode).toBe('0600');
  });

  it('leaves the public fields empty when stat said nothing about that half', () => {
    // borrowing the private half's answer would report a missing .pub as correctly permissioned
    expect(parseKeyFiles('600|svc|svc\n').publicMode).toBe('');
    expect(parseKeyFiles('600|svc|svc\n').privateMode).toBe('0600');
  });

  it('answers empty rather than throwing when stat said nothing at all', () => {
    expect(parseKeyFiles('')).toEqual({ privateMode: '', publicMode: '', owner: '', group: '' });
  });

  it('is content when both halves are what they should be', () => {
    expect(permissionsWrong(right, wanted)).toBe(false);
  });

  it('reports a private key anyone can read, which is what an inherited ACL produces', () => {
    // -rw-r--r--+ is what one ACL-managed pool actually produced, and ssh refuses it at use time
    expect(permissionsWrong({ ...right, privateMode: '0644' }, wanted)).toBe(true);
  });

  it('reports a key the account does not own', () => {
    expect(permissionsWrong({ ...right, owner: 'root' }, wanted)).toBe(true);
    expect(permissionsWrong({ ...right, group: 'root' }, wanted)).toBe(true);
  });

  it('reports a public half that is not readable, since it is the half people copy', () => {
    expect(permissionsWrong({ ...right, publicMode: '0600' }, wanted)).toBe(true);
  });

  it('says nothing about a half the machine declined to describe', () => {
    // asking for a chmod on a path that is not there is a fix that fails every time it is tried
    expect(permissionsWrong({ ...right, publicMode: '' }, wanted)).toBe(false);
    expect(permissionsWrong({ privateMode: '', publicMode: '', owner: '', group: '' }, wanted)).toBe(false);
  });

  it('narrows the private half to what ssh will accept', () => {
    expect(permissionsCommand('/home/svc/.ssh/id_ed25519', 'svc', 'svc'))
      .toContain("chmod 0600 '/home/svc/.ssh/id_ed25519'");
  });

  it('leaves the public half readable', () => {
    expect(permissionsCommand('/k/id', 'svc', 'svc')).toContain("chmod 0644 '/k/id.pub'");
  });

  it('narrows before it hands the key over', () => {
    // of the two orders, this is the one whose intermediate state is a key nobody new can read yet
    // rather than one the new owner can read while it is still group-readable
    const command = permissionsCommand('/k/id', 'svc', 'svc');
    expect(command.indexOf('chmod 0600')).toBeLessThan(command.indexOf('chown'));
  });

  it('owns both halves', () => {
    const command = permissionsCommand('/k/id', 'svc', 'agents');
    expect(command).toContain("chown 'svc:agents' '/k/id' '/k/id.pub'");
  });

  it('stops when a step fails rather than running the next one', () => {
    expect(permissionsCommand('/k/id', 'svc', 'svc')).toContain('&&');
    expect(permissionsCommand('/k/id', 'svc', 'svc')).not.toContain(';');
  });

  it('quotes the path, which may be anywhere a service account lives', () => {
    expect(permissionsCommand("/home/it's/id", 'svc', 'svc')).toContain(String.raw`'/home/it'\''s/id'`);
  });

  it('is the same string every time, so a refresh does not look like a change', () => {
    expect(permissionsCommand('/k/id', 'svc', 'svc')).toBe(permissionsCommand('/k/id', 'svc', 'svc'));
  });

  it('asks for a mode that ssh actually accepts', () => {
    // the whole point: any other value produces a key the machine will not use, which is why this
    // is a constant rather than an argument
    expect(PRIVATE_MODE).toBe('0600');
    expect(PUBLIC_MODE).toBe('0644');
  });
});
