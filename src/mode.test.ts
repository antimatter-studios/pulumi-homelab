import { describe, expect, it } from 'vitest';
import { normaliseMode } from './mode.ts';

/**
 * The first bug this repo ever had, and the reason it is one function rather than three copies.
 * A resource that stores what `stat` said and diffs it against what the program declared reports
 * drift on every refresh for ever, on a file nobody has touched — and a report that always says
 * something is wrong teaches people to stop reading it.
 */
describe('putting a mode into the shape the code writes it in', () => {
  it('gives stat’s three digits the leading zero everybody writes', () => {
    expect(normaliseMode('644')).toBe('0644');
    expect(normaliseMode('755')).toBe('0755');
  });

  it('leaves a mode that already has four alone', () => {
    expect(normaliseMode('0644')).toBe('0644');
  });

  it('does not mistake a setgid bit for a leading zero', () => {
    // 2775 is four digits because of the setgid bit, not because it was already normalised, and
    // rewriting it would silently change the permissions the code asks for
    expect(normaliseMode('2775')).toBe('2775');
    expect(normaliseMode('4755')).toBe('4755');
    expect(normaliseMode('1777')).toBe('1777');
  });

  it('ignores the newline stat prints after it', () => {
    expect(normaliseMode('644\n')).toBe('0644');
  });
});
