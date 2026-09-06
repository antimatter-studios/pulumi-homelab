import { describe, expect, it } from 'vitest';
import { providerChanged, withLegacyAlias } from './upgrade.ts';

/**
 * Pulumi runs the provider closure stored in the state file, not the current source, so a fix to
 * the transport reaches only resources created after it. Measured on a real stack after connection
 * multiplexing was added: four resources carried the new transport and eighteen carried the old one.
 */
describe('noticing the provider itself has changed', () => {
  it('reports a change when the serialised provider differs', () => {
    expect(providerChanged({ __provider: 'old text' }, { __provider: 'new text' })).toBe(true);
  });

  it('reports nothing when it is the same provider', () => {
    // the ordinary case, on every deployment that did not change this package
    expect(providerChanged({ __provider: 'same' }, { __provider: 'same' })).toBe(false);
  });

  it('reports nothing when either side is unknown', () => {
    // guessing here would update every resource on the machine — for SystemdUnit, restarting every
    // managed service — on the strength of a field the framework did not hand over
    expect(providerChanged({}, { __provider: 'new' })).toBe(false);
    expect(providerChanged({ __provider: 'old' }, {})).toBe(false);
    expect(providerChanged(undefined, undefined)).toBe(false);
  });

  it('ignores a value that is not a string', () => {
    expect(providerChanged({ __provider: 1 }, { __provider: 2 })).toBe(false);
  });
});

/**
 * Changing a resource's type changes its URN, and Pulumi reads that as the old resource being gone
 * and a new one appearing: delete, then create. For this provider a delete purges packages, removes
 * unit files and unmasks `dphys-swapfile`. On a stack with thirty-six live resources, shipping the
 * type without the alias would strip the machine.
 */
describe('surviving the change of type', () => {
  it('claims the type every resource used to have', () => {
    expect(withLegacyAlias().aliases).toEqual([{ type: 'pulumi-nodejs:dynamic:Resource' }]);
  });

  it('keeps the caller’s own options', () => {
    const opts = withLegacyAlias({ deleteBeforeReplace: true, protect: true });
    expect(opts.deleteBeforeReplace).toBe(true);
    expect(opts.protect).toBe(true);
  });

  it('keeps the caller’s own aliases, after ours', () => {
    // a stack somebody has already renamed by hand keeps whatever it was relying on
    const opts = withLegacyAlias({ aliases: [{ name: 'old-name' }] });
    expect(opts.aliases).toEqual([{ type: 'pulumi-nodejs:dynamic:Resource' }, { name: 'old-name' }]);
  });

  it('works when given nothing at all', () => {
    expect(withLegacyAlias(undefined).aliases).toHaveLength(1);
  });
});
