import { describe, expect, it } from 'vitest';
import { sameJson } from './same.ts';

describe('sameJson', () => {
  it('ignores the order of object keys, which Pulumi sorts when it stores state', () => {
    expect(sameJson({ AllowTcpForwarding: 'yes', GatewayPorts: 'no' }, { GatewayPorts: 'no', AllowTcpForwarding: 'yes' })).toBe(true);
    expect(sameJson({ a: { y: 1, x: 2 } }, { a: { x: 2, y: 1 } })).toBe(true);
  });

  it('still sees a changed, added or removed value', () => {
    expect(sameJson({ a: 'yes' }, { a: 'no' })).toBe(false);
    expect(sameJson({ a: 'yes' }, { a: 'yes', b: 'no' })).toBe(false);
    expect(sameJson({ a: 'yes', b: 'no' }, { a: 'yes' })).toBe(false);
  });

  it('keeps array order, which carries meaning', () => {
    expect(sameJson(['a', 'b'], ['b', 'a'])).toBe(false);
  });

  it('treats a missing object like undefined, not like an empty one', () => {
    expect(sameJson(undefined, {})).toBe(false);
    expect(sameJson(undefined, undefined)).toBe(true);
  });
});
