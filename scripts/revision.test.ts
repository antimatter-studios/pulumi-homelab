import { describe, expect, it } from 'vitest';
import { staleness } from './revision.ts';

/**
 * The failure this guards is silent on every machine and obvious on none: a `read` that changed,
 * merged, tested, and then never ran because every existing resource keeps executing the closure
 * stored in its own state. It happened three times before this check existed.
 */
describe('whether the recorded revision has kept up', () => {
  const record = { transport: 2, sources: 'abc' };

  it('is quiet when both the revision and the source match', () => {
    expect(staleness(2, 'abc', record)).toBeNull();
  });

  it('reports a bump that was never recorded', () => {
    expect(staleness(3, 'abc', record)).toMatch(/still says 2/);
  });

  it('reports changed source with no bump, which is the real bug', () => {
    // the change is correct, merged and inert — so the message has to say why, not just that
    const stale = staleness(2, 'def', record) ?? '';
    expect(stale).toMatch(/cannot reach/);
    expect(stale).toMatch(/already exists/);
  });

  it('names both ways out, since one of them is right and neither is obvious', () => {
    const stale = staleness(2, 'def', record) ?? '';
    expect(stale).toContain('bump TRANSPORT');
    expect(stale).toContain('--no-bump');
  });

  it('does not accept a matching revision with stale source', () => {
    // the pair is the record: a revision that matches while the hash does not means somebody
    // changed a read and left the number alone
    expect(staleness(2, 'different', record)).not.toBeNull();
  });

  it('does not accept matching source with a moved revision', () => {
    // harmless in itself, but it means the record no longer says which source revision 3 covers
    expect(staleness(3, 'abc', record)).not.toBeNull();
  });
});
