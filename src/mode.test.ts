import { describe, expect, it } from 'vitest';
import { modeRefusal, normaliseMode, specialBitsOf, specialBitsRefusal, withSpecialBits } from './mode.ts';

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

/**
 * The high digit is not a permission and is the one most often lost. `2775` is correct the day it
 * is written and quietly wrong the first time somebody edits the mode without knowing why there
 * were four digits — and the bit that goes is the one holding a shared area together.
 */
describe('the special bits', () => {
  it('reads what the leading digit says', () => {
    expect(specialBitsOf('2775')).toEqual({ setuid: false, setgid: true, sticky: false });
    expect(specialBitsOf('1777')).toEqual({ setuid: false, setgid: false, sticky: true });
    expect(specialBitsOf('4755')).toEqual({ setuid: true, setgid: false, sticky: false });
    expect(specialBitsOf('3775')).toEqual({ setuid: false, setgid: true, sticky: true });
  });

  it('reads a three-digit mode as having none, which is what stat means by one', () => {
    expect(specialBitsOf('755')).toEqual({ setuid: false, setgid: false, sticky: false });
    expect(specialBitsOf('0755')).toEqual({ setuid: false, setgid: false, sticky: false });
  });

  it('folds a flag into the digit', () => {
    expect(withSpecialBits('0775', { setgid: true })).toBe('2775');
    expect(withSpecialBits('1777', {})).toBe('1777');
    expect(withSpecialBits('775', { sticky: true, setgid: true })).toBe('3775');
  });

  it('always answers with four digits, so the comparison is against what stat reports', () => {
    // stat says `755`, everybody writes `0755`, and comparing the two as strings is how a directory
    // nobody has touched reports drift on every refresh for ever
    expect(withSpecialBits('755', {})).toBe('0755');
    expect(withSpecialBits('0755', {})).toBe('0755');
  });

  it('takes a flag set to false as taking the bit off', () => {
    // a declaration that drops `sticky: true` has to actually remove the bit, or the resource can
    // grant something and never take it back
    expect(withSpecialBits('0775', { setgid: false })).toBe('0775');
    expect(withSpecialBits('775', { sticky: false })).toBe('0775');
  });

  it('leaves a bit alone when nothing was said about it', () => {
    // undefined is nobody having written a flag, and it must not overwrite what the mode said
    expect(withSpecialBits('2775', { sticky: undefined })).toBe('2775');
    expect(withSpecialBits('2775', { sticky: true })).toBe('3775');
  });

  it('does not touch the permission digits', () => {
    expect(withSpecialBits('0640', { setgid: true })).toBe('2640');
  });
});

describe('refusing two answers to one question', () => {
  it('is quiet when only the mode says anything', () => {
    expect(specialBitsRefusal('2775', {})).toBeNull();
    expect(specialBitsRefusal('2775', { sticky: undefined })).toBeNull();
  });

  it('is quiet when only the flags do', () => {
    // `0755` is how everybody writes plain `755`, so a leading zero is not a statement about the
    // special bits and a flag beside it simply fills the digit in
    expect(specialBitsRefusal('0755', { sticky: true })).toBeNull();
    expect(specialBitsRefusal('755', { setgid: true })).toBeNull();
  });

  it('refuses a non-zero leading digit alongside a flag, rather than inventing a precedence', () => {
    const refusal = specialBitsRefusal('2775', { sticky: true });
    expect(refusal).toContain('setgid or sticky'.split(' or ')[1]);
    // the message has to carry the way out, or somebody is left to guess which spelling is wanted
    expect(refusal).toContain("'0775'");
  });

  it('refuses a flag that outright contradicts the digit', () => {
    // `2775` says setgid and `setgid: false` says the opposite. Whichever won silently, the other
    // reading is somebody's intention being discarded without a word
    expect(specialBitsRefusal('2775', { setgid: false })).not.toBeNull();
  });

  it('refuses a false flag beside a digit even when the two agree', () => {
    // the rule is one spelling or the other, not a precedence to work out: `2775` already answers
    // what sticky is, and a flag beside it is a second answer whichever way it points
    expect(specialBitsRefusal('2775', { sticky: false })).not.toBeNull();
  });

  it('names every flag that was given', () => {
    const refusal = specialBitsRefusal('1775', { setgid: true, setuid: true }) ?? '';
    expect(refusal).toContain('setuid');
    expect(refusal).toContain('setgid');
  });
});

describe('what counts as a mode', () => {
  it('accepts three and four octal digits', () => {
    expect(modeRefusal('755')).toBeNull();
    expect(modeRefusal('0755')).toBeNull();
    expect(modeRefusal('2775')).toBeNull();
  });

  it('refuses a digit that is not octal', () => {
    expect(modeRefusal('798')).toContain('octal');
  });

  it('refuses a symbolic mode, which chmod takes and stat never reports', () => {
    // `u+rwx` would apply cleanly and then read back as something else entirely, which is drift
    // that can never be resolved
    expect(modeRefusal('u+rwx')).not.toBeNull();
  });

  it('refuses the wrong number of digits', () => {
    expect(modeRefusal('75')).not.toBeNull();
    expect(modeRefusal('07555')).not.toBeNull();
    expect(modeRefusal('')).not.toBeNull();
  });
});
