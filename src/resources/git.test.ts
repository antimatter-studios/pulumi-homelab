import { describe, expect, it } from 'vitest';
import {
  checkoutAct,
  checkoutChanged,
  checkoutCommand,
  commitRefusal,
  looksLikeCommit,
  parseCheckout,
  sameCommit,
} from './git.ts';

const SHA = '9f2c1b7e4a3d8065f1c2b3a4d5e6f708192a3b4c';
const OTHER = '0123456789abcdef0123456789abcdef01234567';
const URL = 'https://github.com/example/widget.git';

/**
 * The one design decision in this resource. `git rev-parse HEAD` answers with a sha, so a sha is the
 * only declaration that can be compared against it — and taking a ref instead would mean resolving
 * it every run and then deciding whether a changed answer is an upgrade or somebody moving a tag
 * under us, a question with no good answer.
 */
describe('what counts as a commit', () => {
  it('accepts a full sha and an abbreviated one', () => {
    expect(looksLikeCommit(SHA)).toBe(true);
    expect(looksLikeCommit('9f2c1b7')).toBe(true);
    expect(looksLikeCommit(SHA.toUpperCase())).toBe(true);
  });

  it('refuses anything shorter than git abbreviates to', () => {
    // `abc` is a tag far more often than a commit, and taking it for one would pin something that
    // can never resolve
    expect(looksLikeCommit('abc')).toBe(false);
    expect(looksLikeCommit('')).toBe(false);
  });

  it('refuses tags and branches, however version-shaped', () => {
    expect(looksLikeCommit('v1.2.3')).toBe(false);
    expect(looksLikeCommit('main')).toBe(false);
    expect(looksLikeCommit('release/1.2')).toBe(false);
    // `deadbeef` is hex and is a commit; `deadbeefs` is not hex and is not
    expect(looksLikeCommit('deadbeefs')).toBe(false);
  });

  it('refuses something longer than a sha', () => {
    expect(looksLikeCommit(SHA + '0')).toBe(false);
  });
});

describe('refusing a ref that is not a commit', () => {
  it('says nothing about a sha', () => {
    expect(commitRefusal(URL, SHA)).toBeNull();
  });

  it('tells whoever wrote a tag exactly how to turn it into a sha', () => {
    // the message is most of the value here: being told `v1.2.3 is invalid` and nothing else leaves
    // somebody to work out on their own what this resource wants instead
    const refusal = commitRefusal(URL, 'v1.2.3');
    expect(refusal).toContain('git ls-remote');
    expect(refusal).toContain(URL);
    expect(refusal).toContain('v1.2.3');
  });

  it('says why, not merely that', () => {
    expect(commitRefusal(URL, 'main')).toMatch(/move/);
  });
});

describe('comparing what the machine said against what was declared', () => {
  it('matches a full sha against itself', () => {
    expect(sameCommit(SHA, SHA)).toBe(true);
  });

  it('lets an abbreviated declaration match the full answer', () => {
    // rev-parse always answers with forty characters, so an abbreviated declaration compared for
    // equality would read as drift on every single refresh, for ever
    expect(sameCommit(SHA, '9f2c1b7')).toBe(true);
    expect(sameCommit(SHA, SHA.slice(0, 12))).toBe(true);
  });

  it('ignores case, which git does not preserve for us', () => {
    expect(sameCommit(SHA, SHA.slice(0, 10).toUpperCase())).toBe(true);
  });

  it('matches in one direction only', () => {
    // a declaration longer than the answer is not a shorter form of it, and treating it as one
    // would make a truncated read look correct
    expect(sameCommit('9f2c1b7', SHA)).toBe(false);
  });

  it('sees a different commit as different', () => {
    expect(sameCommit(SHA, OTHER)).toBe(false);
    expect(sameCommit(SHA, '0123456')).toBe(false);
  });

  it('never calls nothing a match', () => {
    // a machine with no checkout answers with the empty string, and an empty declaration is a
    // mistake; `''.startsWith('')` is true, and that would report both as correct
    expect(sameCommit('', '')).toBe(false);
    expect(sameCommit('', SHA)).toBe(false);
    expect(sameCommit(SHA, '')).toBe(false);
  });
});

describe('deciding what to do to a checkout', () => {
  it('clones what is not there', () => {
    expect(checkoutAct(null, SHA)).toBe('clone');
  });

  it('does nothing to a tree already at the commit', () => {
    // a fetch costs a round trip to the remote on every deployment, and re-cloning would throw away
    // whatever sits beside the checkout — build output, a submodule, an untracked config
    expect(checkoutAct(SHA, SHA)).toBe('nothing');
    expect(checkoutAct(SHA, '9f2c1b7')).toBe('nothing');
  });

  it('checks out a tree that is somewhere else', () => {
    expect(checkoutAct(OTHER, SHA)).toBe('checkout');
  });
});

describe('composing the commands', () => {
  it('has nothing to run when there is nothing to do', () => {
    expect(checkoutCommand('nothing', URL, '/srv/widget', SHA)).toBeNull();
  });

  it('makes the parent before cloning into it', () => {
    const command = checkoutCommand('clone', URL, '/srv/widget', SHA) ?? '';
    expect(command.indexOf('mkdir -p')).toBeLessThan(command.indexOf('git clone'));
  });

  it('clones and then checks out, because clone lands on the default branch', () => {
    const command = checkoutCommand('clone', URL, '/srv/widget', SHA) ?? '';
    expect(command.indexOf('git clone')).toBeLessThan(command.indexOf('checkout'));
  });

  it('fetches before checking out an existing tree', () => {
    // a commit pushed since the clone is not in the local object store, and checkout would fail
    // with a message about an unknown revision rather than about a stale remote
    const command = checkoutCommand('checkout', URL, '/srv/widget', SHA) ?? '';
    expect(command.indexOf('fetch')).toBeLessThan(command.indexOf('checkout'));
  });

  it('fetches tags too, which is the commonest reason a valid sha is missing', () => {
    expect(checkoutCommand('checkout', URL, '/srv/widget', SHA)).toContain('--tags');
  });

  it('does not re-clone over an existing tree', () => {
    expect(checkoutCommand('checkout', URL, '/srv/widget', SHA)).not.toContain('git clone');
  });

  it('detaches, so nothing later moves the checkout off the pin', () => {
    // attached to a local branch, the next `git pull` moves HEAD and the read starts describing the
    // branch rather than the commit that was declared
    expect(checkoutCommand('clone', URL, '/srv/widget', SHA)).toContain('--detach');
    expect(checkoutCommand('checkout', URL, '/srv/widget', SHA)).toContain('--detach');
  });

  it('stops the whole command when a step fails', () => {
    // without &&, a failed clone is followed by a checkout in a directory that does not exist, and
    // the error that surfaces is about the wrong thing
    const command = checkoutCommand('clone', URL, '/srv/widget', SHA) ?? '';
    expect(command).not.toContain(';');
    expect(command).toContain('&&');
  });

  it('quotes the url, the path and the commit', () => {
    const command = checkoutCommand('clone', 'https://x/a.git?b=1&c=2', "/srv/it's", SHA) ?? '';
    expect(command).toContain("'https://x/a.git?b=1&c=2'");
    expect(command).toContain(String.raw`'/srv/it'\''s'`);
    expect(command).toContain(`'${SHA}'`);
  });

  it('is the same string every time, so a refresh does not look like a change', () => {
    expect(checkoutCommand('clone', URL, '/srv/widget', SHA)).toBe(checkoutCommand('clone', URL, '/srv/widget', SHA));
  });
});

describe('what counts as changed', () => {
  const old = { head: SHA, commit: SHA, url: URL };

  it('is quiet when the machine is where the program says it should be', () => {
    expect(checkoutChanged(old, { commit: SHA, url: URL })).toBe(false);
  });

  it('reports a checkout somebody moved by hand', () => {
    // the read is the point of this resource: a build that left the tree on another commit, or a
    // `git checkout` somebody ran last Tuesday, is drift rather than something to ignore
    expect(checkoutChanged({ ...old, head: OTHER }, { commit: SHA, url: URL })).toBe(true);
  });

  it('reports a checkout that is gone', () => {
    expect(checkoutChanged({ ...old, head: '' }, { commit: SHA, url: URL })).toBe(true);
  });

  it('reports a commit the program moved', () => {
    expect(checkoutChanged(old, { commit: OTHER, url: URL })).toBe(true);
  });

  it('reports a different origin, which is a different repository at the same path', () => {
    expect(checkoutChanged(old, { commit: SHA, url: 'https://github.com/someone-else/widget.git' })).toBe(true);
  });

  it('stays quiet when the declaration abbreviates the commit the machine reported', () => {
    expect(checkoutChanged({ ...old, commit: '9f2c1b7' }, { commit: '9f2c1b7', url: URL })).toBe(false);
  });
});

describe('reading the checkout back', () => {
  it('splits the two answers apart', () => {
    expect(parseCheckout(`${SHA}\n#pulumi-homelab#origin\n${URL}\n`)).toEqual({ head: SHA, url: URL });
  });

  it('answers with an empty origin when git could not name one', () => {
    // a clone from a path, or a remote somebody renamed, still has a HEAD worth comparing
    expect(parseCheckout(`${SHA}\n#pulumi-homelab#origin\n`)).toEqual({ head: SHA, url: '' });
  });

  it('does not fold the marker into either answer', () => {
    const parsed = parseCheckout(`${SHA}\n#pulumi-homelab#origin\n${URL}\n`);
    expect(parsed.head).not.toContain('pulumi-homelab');
    expect(parsed.url).not.toContain('pulumi-homelab');
  });
});
