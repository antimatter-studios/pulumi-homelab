import { describe, expect, it } from 'vitest';
import { disagreeing } from './resolved.ts';

/**
 * The package's central idea, named: what a resource wrote is not what the system does. Something
 * else can always win — a drop-in that sorts later, a `Match` block, a `config.txt` section that
 * never matches the board — and none of those are errors, and all of them are silent.
 */
describe('finding where the system disagrees', () => {
  it('names a declared key the system resolved differently', () => {
    expect(disagreeing({ Storage: 'volatile' }, { Storage: 'persistent' })).toEqual(['Storage']);
  });

  it('says nothing when the system agrees', () => {
    expect(disagreeing({ Storage: 'volatile' }, { Storage: 'volatile' })).toEqual([]);
  });

  it('ignores a key the system does not mention at all', () => {
    // several of these readers are partial by nature: vcgencmd does not report dtoverlay, and a
    // keyword absent from `sshd -T` may be one that version does not know. Reporting those would
    // fill the field with things nobody can act on, and a report that is mostly noise is one
    // nobody reads — which is the failure this whole package is arranged against
    expect(disagreeing({ Storage: 'volatile' }, {})).toEqual([]);
  });

  it('maps a declared name into the alphabet the system answers in', () => {
    // sshd -T lowercases every keyword, so PasswordAuthentication has to be *asked for* as
    // passwordauthentication or it looks absent and is never compared at all — the comparison
    // silently never happens rather than failing
    expect(disagreeing(
      { PasswordAuthentication: 'yes' },
      { passwordauthentication: 'no' },
      (key) => key.toLowerCase(),
    )).toEqual(['PasswordAuthentication']);
  });

  it('agrees through that mapping too', () => {
    expect(disagreeing(
      { PasswordAuthentication: 'no' },
      { passwordauthentication: 'no' },
      (key) => key.toLowerCase(),
    )).toEqual([]);
  });

  it('sorts, so a message reads the same every time', () => {
    const found = disagreeing({ b: '1', a: '1' }, { b: '2', a: '2' });
    expect(found).toEqual(['a', 'b']);
  });

  it('has nothing to say about nothing declared', () => {
    expect(disagreeing({}, { Storage: 'volatile' })).toEqual([]);
  });
});
