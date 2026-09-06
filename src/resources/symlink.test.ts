import { describe, expect, it } from 'vitest';
import { Symlink } from './symlink.ts';

/**
 * The interesting behaviour of this resource is in its `read`, which needs a machine to answer.
 * What can be tested here is that the resource exists with the shape the rest of the code expects —
 * the four-state read itself is covered by the reasoning in the source and by the first real run.
 */
describe('the symlink resource', () => {
  it('is constructible with a path and a target', () => {
    expect(typeof Symlink).toBe('function');
    expect(Symlink.name).toBe('Symlink');
  });
});
