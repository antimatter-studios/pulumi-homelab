import { describe, expect, it } from 'vitest';
import { parseStat } from './directory.ts';

/**
 * Reading state back is where both of this repo's real bugs have been, and both were the machine
 * phrasing an answer differently from the code that declared it.
 */
describe('reading what stat says about a path', () => {
  it('pads the mode into the shape the code writes it in', () => {
    // '755' compared against '0755' as strings is drift reported for ever on a directory nobody
    // has touched — the same trap the file resource already fell into once
    expect(parseStat('directory|755|root|root').mode).toBe('0755');
  });

  it('leaves a mode that is already four digits alone', () => {
    expect(parseStat('directory|0700|admin|admin').mode).toBe('0700');
  });

  it('keeps a setgid mode, which is four digits for a different reason', () => {
    expect(parseStat('directory|2775|root|staff').mode).toBe('2775');
  });

  it('reports the kind, so a file where a directory belongs can be told from an empty path', () => {
    // `mkdir` on this would fail with something far less useful than saying what is actually there
    expect(parseStat('regular file|644|root|root').kind).toBe('regular file');
    expect(parseStat('symbolic link|777|root|root').kind).toBe('symbolic link');
  });

  it('reads the owner and group', () => {
    const found = parseStat('directory|0750|aiworld|aiworld');
    expect([found.owner, found.group]).toEqual(['aiworld', 'aiworld']);
  });
});
