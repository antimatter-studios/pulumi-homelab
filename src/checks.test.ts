import { describe, expect, it } from 'vitest';
import { mountedAt } from './checks.ts';

/**
 * The failure this guards against is silent in both directions: writing to an unmounted mount point
 * succeeds, and the disk arriving later hides what was written rather than reporting a conflict.
 */
describe('asking whether a path is really a mount point', () => {
  it('asks mountpoint first, since that is the tool for the question', () => {
    expect(mountedAt('/mnt/data').startsWith("mountpoint -q '/mnt/data'")).toBe(true);
  });

  it('falls back to findmnt for an image that has one and not the other', () => {
    expect(mountedAt('/mnt/data')).toContain('findmnt -rno TARGET');
  });

  it('quotes the path, which may contain anything a filesystem allows', () => {
    // a directory with a space in it would otherwise be two arguments, and mountpoint would answer
    // a question about a path nobody asked about
    expect(mountedAt("/mnt/it's here")).toContain("'/mnt/it'\\''s here'");
  });
});
