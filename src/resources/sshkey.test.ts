import { describe, expect, it } from 'vitest';
import { parseFingerprint, parseHostKeys } from './sshkey.ts';

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
