import { describe, expect, it } from 'vitest';
import { parseStat, resolveMode } from './directory.ts';

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

/**
 * A shared directory's sticky bit is the one that pairs with a write grant: write permission on a
 * directory is what permits deleting the entries in it, so an account given write there can remove
 * work it cannot even read into.
 */
describe('settling a mode and the bits beside it', () => {
  it('defaults to 0755 when nothing is said', () => {
    expect(resolveMode({})).toBe('0755');
  });

  it('folds the flags into the mode, so one string is written, compared and read back', () => {
    expect(resolveMode({ mode: '0775', setgid: true })).toBe('2775');
    expect(resolveMode({ mode: '0777', sticky: true })).toBe('1777');
    expect(resolveMode({ mode: '0775', setgid: true, sticky: true })).toBe('3775');
  });

  it('keeps a four-digit mode meaning what it always has', () => {
    expect(resolveMode({ mode: '2775' })).toBe('2775');
  });

  it('refuses a mode and a flag that both answer the same question', () => {
    expect(() => resolveMode({ mode: '2775', sticky: true })).toThrow(/one or the other/);
  });

  it('refuses a mode that is not a mode, at the declaration rather than at the machine', () => {
    expect(() => resolveMode({ mode: 'u+rwx' })).toThrow();
    expect(() => resolveMode({ mode: '789' })).toThrow();
  });

  it('is the same answer every time, so nothing here can look like a change', () => {
    expect(resolveMode({ mode: '775', setgid: true })).toBe(resolveMode({ mode: '775', setgid: true }));
  });

  it('answers in the shape stat reports, for a mode with no special bits', () => {
    // stat says `755` and the declaration says `0755`; if these two ever differ the directory
    // reports drift on every refresh
    expect(resolveMode({ mode: '755' })).toBe(parseStat('directory|755|root|root').mode);
    expect(resolveMode({ mode: '0775', setgid: true })).toBe(parseStat('directory|2775|root|root').mode);
  });
});
