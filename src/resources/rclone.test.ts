import { describe, expect, it } from 'vitest';
import { configPairs, parseDump } from './rclone.ts';

/**
 * The trap this resource exists to avoid: rclone stores passwords obscured, and `rclone obscure` is
 * not deterministic — it encrypts with a random initialisation vector, so obscuring the same
 * password twice gives two different strings. Comparing a stored value against a freshly obscured
 * one reports drift on every refresh, for ever, on a remote nobody has touched. The comparison
 * happens in plaintext instead, via `rclone reveal`.
 */
const DUMP = JSON.stringify({
  archive: { type: 'sftp', host: 'far.example', user: 'admin', pass: 'obscured-Ab3xQ' },
  backup: { type: 's3', provider: 'Wasabi' },
});

describe('reading rclone’s own dump', () => {
  it('finds a remote and everything stored for it', () => {
    expect(parseDump(DUMP, 'archive')).toEqual({
      type: 'sftp', host: 'far.example', user: 'admin', pass: 'obscured-Ab3xQ',
    });
  });

  it('says nothing rather than something wrong about a remote that is not there', () => {
    expect(parseDump(DUMP, 'nothing')).toBeNull();
  });

  it('survives output that is not JSON at all', () => {
    // an rclone too old for `config dump`, or a config file it could not read: neither is a remote
    // that exists, and neither should throw out of a read
    expect(parseDump('', 'archive')).toBeNull();
    expect(parseDump('Usage:\n  rclone [flags]', 'archive')).toBeNull();
  });

  it('handles a config with no remotes in it', () => {
    expect(parseDump('{}', 'archive')).toBeNull();
  });
});

describe('building the command', () => {
  it('writes key and value as separate arguments, as config create takes them', () => {
    expect(configPairs({ host: 'far.example', user: 'admin' }))
      .toEqual(['host', 'far.example', 'user', 'admin']);
  });

  it('sorts, so a rearranged settings map is not a different command', () => {
    // object key order is source order, and a resource that rewrote a config file because somebody
    // moved a line in the program would be reporting churn as change
    expect(configPairs({ user: 'admin', host: 'far.example' }))
      .toEqual(configPairs({ host: 'far.example', user: 'admin' }));
  });

  it('keeps an empty value rather than dropping the key', () => {
    expect(configPairs({ pass: '' })).toEqual(['pass', '']);
  });

  it('has nothing to say about an empty map', () => {
    expect(configPairs({})).toEqual([]);
  });
});
