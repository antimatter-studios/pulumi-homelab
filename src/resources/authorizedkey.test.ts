import { describe, expect, it } from 'vitest';
import { authorizedLine, keyBody, keyComment, removeAuthorized, upsertAuthorized } from './authorizedkey.ts';

/**
 * The comment on a key is the part that changes — people rename laptops — so a resource keyed on
 * the whole line adds a second copy of a key that is already there, and an account accumulates one
 * entry per rename. The base64 body is what identifies a key.
 */
const KEY = 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIH0k9tBqZ3rEXAMPLEexampleEXAMPLEkey0 someone@laptop';
const BODY = 'AAAAC3NzaC1lZDI1NTE5AAAAIH0k9tBqZ3rEXAMPLEexampleEXAMPLEkey0';
const OTHER = 'ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDfoREXAMPLEotherkeyEXAMPLE1 build@ci';

describe('identifying a key', () => {
  it('finds the body of a plain key line', () => {
    expect(keyBody(KEY)).toBe(BODY);
  });

  it('finds it past the options, which may contain spaces inside quotes', () => {
    // `from="10.0.0.0/24,10.1.0.0/24" command="/usr/bin/thing --flag"` is one field with spaces in
    // it, so the body cannot be found by counting columns
    expect(keyBody(`from="10.0.0.0/24" command="/usr/bin/x --y" ${KEY}`)).toBe(BODY);
  });

  it('is not fooled by a renamed laptop', () => {
    expect(keyBody(KEY)).toBe(keyBody(KEY.replace('someone@laptop', 'someone@newlaptop')));
  });

  it('says nothing about a comment or a blank line', () => {
    expect(keyBody('# an old key, removed 2024')).toBeNull();
    expect(keyBody('')).toBeNull();
    expect(keyBody('not a key at all')).toBeNull();
  });
});

describe('writing the line', () => {
  it('puts the options before the key, comma-separated', () => {
    expect(authorizedLine(KEY, ['from="10.0.0.0/24"', 'restrict']))
      .toBe(`from="10.0.0.0/24",restrict ${KEY}`);
  });

  it('writes a bare key when there are no options', () => {
    expect(authorizedLine(KEY)).toBe(KEY);
  });
});

describe('putting a key in the file', () => {
  it('adds a key the file does not have, keeping the others', () => {
    const updated = upsertAuthorized(`${OTHER}\n`, KEY, authorizedLine(KEY));
    expect(updated).toContain(OTHER);
    expect(keyBody(updated.split('\n')[1] ?? '')).toBe(BODY);
  });

  it('replaces the line for a key already there rather than adding a second', () => {
    const once = upsertAuthorized('', KEY, authorizedLine(KEY));
    const twice = upsertAuthorized(once, KEY, authorizedLine(KEY));
    expect(twice).toBe(once);
  });

  it('tightens options in place, leaving no unrestricted copy underneath', () => {
    // a change that appears to restrict a key and leaves the open version below it would be worse
    // than no change at all
    const open = upsertAuthorized('', KEY, authorizedLine(KEY));
    const restricted = upsertAuthorized(open, KEY, authorizedLine(KEY, ['from="10.0.0.0/24"']));
    expect(restricted.split('\n').filter((line) => keyBody(line) === BODY)).toHaveLength(1);
    expect(restricted).toContain('from="10.0.0.0/24"');
  });

  it('matches on the body even when the comment changed', () => {
    const existing = `${KEY.replace('someone@laptop', 'someone@oldlaptop')}\n`;
    const updated = upsertAuthorized(existing, KEY, authorizedLine(KEY));
    expect(updated.split('\n').filter((line) => keyBody(line) === BODY)).toHaveLength(1);
    expect(updated).toContain('someone@laptop');
  });
});

describe('taking a key out', () => {
  it('removes that key and leaves everybody else’s alone', () => {
    // an authorized_keys file usually has more than one owner
    const both = `${OTHER}\n${KEY}\n`;
    const without = removeAuthorized(both, KEY);
    expect(without).toContain(OTHER);
    expect(keyBody(without)).toBe(keyBody(OTHER));
  });

  it('leaves a file alone when the key is not in it', () => {
    expect(removeAuthorized(`${OTHER}\n`, KEY)).toContain(OTHER);
  });
});

/**
 * A real authorized_keys file has more than one owner. On one machine, `chris` carries a laptop's
 * ed25519 key and an RSA key commented `root@id_rsa` — the far end of a reverse tunnel, put there
 * by somebody else entirely. Neither owner knows about the other's key, which is why this is a
 * resource per key rather than a list on `User`: a declared list would have to be the whole truth,
 * and declaring one would silently remove the other.
 */
describe('telling one key from another', () => {
  it('reports the comment, which is the only human-readable part', () => {
    expect(keyComment(KEY)).toBe('someone@laptop');
    expect(keyComment(OTHER)).toBe('build@ci');
  });

  it('reads the comment past the options', () => {
    expect(keyComment(`from="10.0.0.0/24" ${KEY}`)).toBe('someone@laptop');
  });

  it('says nothing rather than guessing for a key with no comment', () => {
    const bare = KEY.replace(' someone@laptop', '');
    expect(keyComment(bare)).toBe('');
  });

  it('handles a comment containing spaces, which ssh-keygen allows', () => {
    expect(keyComment(`${KEY} and more words`)).toBe('someone@laptop and more words');
  });

  it('leaves the other owner’s key alone when this one is removed', () => {
    // the failure this shape avoids: a list on User would make the declared set the whole truth,
    // and declaring the laptop key would silently drop the tunnel's
    const both = `${KEY}\n${OTHER}\n`;
    expect(keyComment(removeAuthorized(both, KEY).trim())).toBe('build@ci');
  });
});
