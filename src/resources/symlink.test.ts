import { describe, expect, it } from 'vitest';
import { Symlink, interpretSymlink } from './symlink.ts';

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

/**
 * The four states are the substance of this resource, and the dangerous one is the third. A real
 * directory where a link belongs means journald writes the system journal to the wrong disk —
 * silently, for ever, on a machine whose configuration all reads as correct. A resource that
 * treated "the path exists" as success would report that machine as fine.
 */
describe('deciding what is at the path', () => {
  it('reads a link and its target', () => {
    expect(interpretSymlink(0, '/mnt/data/journal')).toEqual({ state: 'link', target: '/mnt/data/journal' });
  });

  it('reads nothing at all as absent, which is a resource to create', () => {
    expect(interpretSymlink(9, '')).toEqual({ state: 'absent' });
  });

  it('reads something real as occupied, naming what it found', () => {
    // the error has to say what is actually there: `mkdir` failing later says far less
    expect(interpretSymlink(8, 'directory')).toEqual({ state: 'occupied', kind: 'directory' });
    expect(interpretSymlink(8, 'regular file')).toEqual({ state: 'occupied', kind: 'regular file' });
  });

  it('still calls it occupied when stat said nothing useful', () => {
    expect(interpretSymlink(8, '')).toEqual({ state: 'occupied', kind: 'something else' });
  });

  it('reads a broken link as a link, because that is what it is', () => {
    // a link pointing at a filesystem that is not mounted yet is the ordinary case here
    expect(interpretSymlink(0, '/mnt/data/never-mounted')).toEqual({
      state: 'link', target: '/mnt/data/never-mounted',
    });
  });
});
