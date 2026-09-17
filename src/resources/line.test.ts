import { describe, expect, it } from 'vitest';
import {
  correctlyPlaced,
  findAnchor,
  findMarked,
  insertionPoint,
  markLine,
  placementRefusal,
  readLine,
  removeLine,
  resolveLine,
  upsertLine,
} from './line.ts';

/**
 * Taken from the case this was built for: a `.bashrc` that must carry one line BEFORE Debian's early
 * return, because `ssh host 'cmd'` sources this file and never reaches anything past it. A line
 * after the guard is present, correct and dead.
 */
const GUARD = [
  '# ~/.bashrc: executed by bash(1) for non-login shells.',
  '',
  '# If not running interactively, don\'t do anything',
  'case $- in',
  '    *i*) ;;',
  '      *) return;;',
  'esac',
  '',
  'HISTSIZE=1000',
  'alias ll=\'ls -alF\'',
].join('\n');

const shims = {
  path: '/home/chris/.bashrc',
  line: 'export PATH="$HOME/.local/share/mise/shims:$PATH"',
  marker: 'mise-shims',
  position: 'before' as const,
  anchor: 'case $- in',
  comment: '#',
};

describe('marking a line so it can be found again', () => {
  it('writes the marker into the line as a trailing comment', () => {
    expect(markLine('export PATH=x', 'mise-shims')).toBe('export PATH=x # pulumi-homelab:mise-shims');
  });

  it('takes the comment syntax of the file it is going into', () => {
    expect(markLine('x=1', 'thing', ';')).toBe('x=1 ; pulumi-homelab:thing');
  });

  it('finds its own line and nothing else', () => {
    const text = `a\n${markLine('export PATH=x', 'mise-shims')}\nb`;
    expect(findMarked(text, 'mise-shims')).toBe(1);
    expect(findMarked(text, 'something-else')).toBe(-1);
  });

  it('is what makes a hand-edited line the same line rather than a second one', () => {
    // matching the literal text would append a duplicate on every deployment, which is the obvious
    // failure mode of a resource that adds a line
    const edited = `a\nexport PATH="/somewhere/else" # pulumi-homelab:mise-shims\nb`;
    expect(findMarked(edited, 'mise-shims')).toBe(1);
  });
});

describe('finding the anchor', () => {
  it('takes the first occurrence for before', () => {
    expect(findAnchor('x\ncase $- in\ny\ncase $- in', 'case $- in', 'before')).toBe(1);
  });

  it('takes the last for after, so after a block means after all of it', () => {
    expect(findAnchor('x\ncase $- in\ny\ncase $- in', 'case $- in', 'after')).toBe(3);
  });

  it('answers nothing when the file does not contain it', () => {
    expect(findAnchor(GUARD, 'no such text', 'before')).toBe(-1);
    expect(findAnchor(GUARD, '', 'before')).toBe(-1);
  });
});

/**
 * The position is the requirement, not a preference. Appending a line that was meant to precede an
 * early return produces a file that looks right and does not work.
 */
describe('refusing to place a line where it would be inert', () => {
  it('is quiet for prepend and append, which need no anchor', () => {
    for (const position of ['prepend', 'append'] as const) {
      expect(placementRefusal(GUARD, { path: '/x', position, anchor: '' })).toBeNull();
    }
  });

  it('is quiet when the anchor is there', () => {
    expect(placementRefusal(GUARD, shims)).toBeNull();
  });

  it('refuses before or after with no anchor given', () => {
    expect(placementRefusal(GUARD, { path: '/x', position: 'before', anchor: '' }))
      .toMatch(/needs an anchor/);
  });

  it('refuses an anchor the file does not contain, rather than appending', () => {
    const refusal = placementRefusal(GUARD, { ...shims, anchor: 'case $x in' }) ?? '';
    expect(refusal).toContain('/home/chris/.bashrc');
    expect(refusal).toContain('may never run');
  });
});

describe('where a new line goes', () => {
  it('goes above the anchor for before', () => {
    expect(insertionPoint(GUARD, shims)).toBe(3);
  });

  it('goes below it for after', () => {
    expect(insertionPoint(GUARD, { ...shims, position: 'after', anchor: 'esac' })).toBe(7);
  });

  it('goes at the top for prepend and the end for append', () => {
    expect(insertionPoint(GUARD, { position: 'prepend', anchor: '' })).toBe(0);
    expect(insertionPoint(GUARD, { position: 'append', anchor: '' })).toBe(GUARD.split('\n').length);
  });
});

describe('putting the line in', () => {
  it('places it before the guard, where it actually runs', () => {
    const lines = upsertLine(GUARD, shims).split('\n');
    const at = lines.findIndex((line) => line.includes('mise-shims'));
    const guard = lines.findIndex((line) => line.includes('case $- in'));
    expect(at).toBeLessThan(guard);
  });

  it('leaves every other byte alone, comments and all', () => {
    const before = GUARD.split('\n');
    const after = upsertLine(GUARD, shims).split('\n').filter((line) => !line.includes('mise-shims'));
    expect(after).toEqual(before);
  });

  it('does not grow the file on a second application', () => {
    // the failure mode of every append-only script that has ever touched a shell rc file
    const once = upsertLine(GUARD, shims);
    expect(upsertLine(once, shims)).toBe(once);
    expect(once.split('\n').filter((l) => l.includes('mise-shims'))).toHaveLength(1);
  });

  it('corrects the text of a line somebody edited, in place', () => {
    const edited = upsertLine(GUARD, shims).replace(shims.line, 'export PATH="/wrong"');
    const fixed = upsertLine(edited, shims);
    expect(fixed).toBe(upsertLine(GUARD, shims));
    expect(fixed.split('\n').filter((l) => l.includes('mise-shims'))).toHaveLength(1);
  });

  it('moves a line that has crossed its anchor back, and takes the old copy with it', () => {
    // moved past the guard it is inert, so position wins over where somebody put it — and the copy
    // left behind has to go, or correcting the position silently doubles the line
    const moved = `${GUARD}\n${markLine(shims.line, shims.marker)}`;
    expect(correctlyPlaced(moved, shims)).toBe(false);
    const fixed = upsertLine(moved, shims);
    expect(correctlyPlaced(fixed, shims)).toBe(true);
    expect(fixed.split('\n').filter((l) => l.includes('mise-shims'))).toHaveLength(1);
  });

  it('leaves a line alone that is above its anchor but not adjacent to it', () => {
    // anywhere above the guard runs, so somebody who put it at the top of the file meant it there.
    // Replacing in place rather than removing and re-inserting is what keeps that
    const lines = GUARD.split('\n');
    const guard = lines.findIndex((line) => line.includes('case $- in'));
    const placedHigh = [
      markLine(shims.line, shims.marker, shims.comment),
      ...lines.slice(0, guard),
      ...lines.slice(guard),
    ].join('\n');
    expect(correctlyPlaced(placedHigh, shims)).toBe(true);
    // the marker is at index 0 and the anchor several lines down, so a re-insert would move it
    expect(findMarked(placedHigh, shims.marker)).toBe(0);
    expect(upsertLine(placedHigh, shims)).toBe(placedHigh);
    expect(findMarked(upsertLine(placedHigh, shims), shims.marker)).toBe(0);
  });

  it('is the same string every time, so a refresh does not look like a change', () => {
    expect(upsertLine(GUARD, shims)).toBe(upsertLine(GUARD, shims));
  });
});

describe('what the file says about the line', () => {
  it('reports it absent, and absent is not misplaced', () => {
    // conflating them would report a file with no line at all as one whose line is in the wrong place
    expect(readLine(GUARD, shims)).toEqual({ actual: '', placed: false });
  });

  it('reports the line as the file has it, marker and all', () => {
    expect(readLine(upsertLine(GUARD, shims), shims).actual)
      .toBe(markLine(shims.line, shims.marker, shims.comment));
  });

  it('reports a line that is present and inert', () => {
    // the failure presence alone cannot see, and the reason this resource is worth building
    const moved = `${GUARD}\n${markLine(shims.line, shims.marker)}`;
    expect(readLine(moved, shims)).toEqual({ actual: markLine(shims.line, shims.marker), placed: false });
  });

  it('reports a correctly placed line as placed', () => {
    expect(readLine(upsertLine(GUARD, shims), shims).placed).toBe(true);
  });

  it('calls an anchorless position placed wherever the line is', () => {
    const appended = upsertLine(GUARD, { ...shims, position: 'append', anchor: '' });
    expect(readLine(appended, { ...shims, position: 'append', anchor: '' }).placed).toBe(true);
  });

  it('reports not-placed when the anchor itself has gone', () => {
    // somebody rewrote the guard, so the requirement can no longer be checked and must not be assumed
    const withLine = upsertLine(GUARD, shims);
    expect(correctlyPlaced(withLine.replace('case $- in', 'case "$-" in'), shims)).toBe(false);
  });
});

describe('taking the line out', () => {
  it('removes its own line and leaves the rest', () => {
    expect(removeLine(upsertLine(GUARD, shims), shims.marker)).toBe(GUARD);
  });

  it('leaves a file it was never in exactly as it found it', () => {
    expect(removeLine(GUARD, shims.marker)).toBe(GUARD);
  });

  it('does not remove another marked line', () => {
    const both = upsertLine(upsertLine(GUARD, shims), { ...shims, marker: 'other', line: 'x=1' });
    expect(removeLine(both, 'other')).toBe(upsertLine(GUARD, shims));
  });
});

describe('settling the arguments', () => {
  it('defaults to appending with no anchor and a hash comment', () => {
    const settled = resolveLine({ path: '/etc/x', line: 'y', marker: 'z' });
    expect([settled.position, settled.anchor, settled.comment]).toEqual(['append', '', '#']);
  });

  it('keys on the file and the marker, since one file can hold two managed lines', () => {
    expect(resolveLine({ path: '/etc/x', line: 'y', marker: 'z' }).id).toBe('/etc/x#z');
  });

  it('refuses an empty marker, which is the thing that finds the line again', () => {
    expect(() => resolveLine({ path: '/etc/x', line: 'y', marker: '  ' })).toThrow(/cannot be empty/);
  });
});
