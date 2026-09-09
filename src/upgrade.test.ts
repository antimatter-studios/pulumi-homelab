import { describe, expect, it } from 'vitest';
import { TRANSPORT, providerChanged, stamped, transportChanged, withLegacyAlias } from './upgrade.ts';

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

/**
 * The mechanism that replaced comparing the serialised closure, and why it had to.
 *
 * `providerChanged` compared `__provider` in state against the program's. An update does not
 * re-persist `__provider`, so once those differed they differed for ever: the resource reported an
 * update on every deployment, the stored value never moved, and only resources whose state was
 * written at an older version were affected. On one real stack that was ten resources writing to
 * the machine on every run — a Samba reload and an avahi restart among them — while `pulumi up`
 * could never answer "nothing to do".
 *
 * A number carried in the resource's own state fixes both halves: an update persists it, so the
 * upgrade completes; and it only moves when somebody decides it should, so an edit that changes no
 * behaviour changes nothing.
 */
describe('noticing the transport has moved on', () => {
  it('reports a change when the stamp is older than the current transport', () => {
    expect(transportChanged({ transport: TRANSPORT - 1 })).toBe(true);
  });

  it('reports nothing when the stamp is current', () => {
    expect(transportChanged({ transport: TRANSPORT })).toBe(false);
  });

  it('reports nothing for state written before stamps existed', () => {
    // guessing here would update every resource on every machine once, which is the failure this
    // replaces; those resources pick the stamp up when anything else about them genuinely changes
    expect(transportChanged({})).toBe(false);
    expect(transportChanged(undefined)).toBe(false);
  });

  it('ignores a stamp that is not a number', () => {
    expect(transportChanged({ transport: 'v1' })).toBe(false);
  });
});

describe('stamping everything a provider stores', () => {
  const provider = {
    create: async () => ({ id: 'x', outs: { path: '/etc/x' } }),
    read: async (id: string) => ({ id, props: { path: id } }),
    update: async () => ({ outs: { path: '/etc/x' } }),
  };

  it('stamps what create stores', async () => {
    const made = await stamped(provider).create({});
    expect(made.outs).toEqual({ path: '/etc/x', transport: TRANSPORT });
  });

  it('stamps what update stores, which is the half the old mechanism never did', async () => {
    // a resource that stamped on create and not on update would never complete the upgrade it was
    // meant to enable — exactly how the previous mechanism failed
    const done = await stamped(provider).update?.('x', { path: '/etc/x' }, {});
    expect(done?.outs).toEqual({ path: '/etc/x', transport: TRANSPORT });
  });

  it('stamps what read reports, so a refresh does not undo it', async () => {
    const found = await stamped(provider).read?.('/etc/x');
    expect(found?.props).toEqual({ path: '/etc/x', transport: TRANSPORT });
  });

  it('leaves a read that found nothing alone', async () => {
    // `props: undefined` is how a provider says the resource is gone, and stamping it would turn
    // that into a resource that exists and has nothing but a version
    const gone = { ...provider, read: async () => ({ id: undefined, props: undefined }) };
    const found = await stamped(gone).read?.('/etc/x');
    expect(found?.props).toBeUndefined();
  });

  it('keeps the rest of the provider, including the methods it does not wrap', async () => {
    const withDiff = { ...provider, diff: async () => ({ changes: true }) };
    expect(await stamped(withDiff).diff?.('x', { path: '' }, {})).toEqual({ changes: true });
  });

  it('does not invent a read or an update the provider does not have', () => {
    const bare = { create: provider.create };
    expect(stamped(bare).read).toBeUndefined();
    expect(stamped(bare).update).toBeUndefined();
  });
});
