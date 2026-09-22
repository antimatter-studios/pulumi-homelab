import { describe, expect, it } from 'vitest';
import { needsMaterial, sourceRefusal, substituteArgv, SEEDED, type Source } from './protected.ts';

describe('substituting the temporary file into an argv', () => {
  it('replaces every occurrence, not only the first', () => {
    expect(substituteArgv(['tool', '--out', '{}', '--also', '{}'], '/tmp/blob'))
      .toEqual(['tool', '--out', '/tmp/blob', '--also', '/tmp/blob']);
  });

  it('substitutes inside an argument rather than only as a whole one', () => {
    // `--out={}` is one argv entry, and a replace that only matched whole entries would miss it
    expect(substituteArgv(['tool', '--out={}'], '/tmp/blob')).toEqual(['tool', '--out=/tmp/blob']);
  });

  it('leaves an argv with nothing to substitute exactly as it was', () => {
    expect(substituteArgv(['pass', 'show', 'secret'], '/tmp/blob')).toEqual(['pass', 'show', 'secret']);
  });

  it('does not quote or escape, because there is no shell to protect from', () => {
    // the point of an argv rather than a command string: a path with a space is one argument
    expect(substituteArgv(['tool', '{}'], '/tmp/a dir/blob')).toEqual(['tool', '/tmp/a dir/blob']);
  });
});

describe('refusing a source that cannot work', () => {
  it('accepts a file source with a path', () => {
    expect(sourceRefusal({ kind: 'file', path: 'secrets/token.json' })).toBeUndefined();
  });

  it('refuses a file source with no path', () => {
    expect(sourceRefusal({ kind: 'file', path: '' })).toBe('source.path is empty');
  });

  it('accepts an exec source', () => {
    expect(sourceRefusal({ kind: 'exec', argv: ['pass', 'show', 'token'] })).toBeUndefined();
  });

  it('refuses an empty argv, which execFileSync reports as a missing program', () => {
    expect(sourceRefusal({ kind: 'exec', argv: [] })).toBe("source.argv is empty for kind 'exec'");
  });

  it('accepts an execFile source that says where to write', () => {
    expect(sourceRefusal({ kind: 'execFile', argv: ['trove', 'get', 'file', '--out', '{}', 'entry'] }))
      .toBeUndefined();
  });

  /**
   * The failure this refusal exists for: the command runs, succeeds, writes its material somewhere
   * else entirely, and the provider reads the empty temporary file it made. An empty secret,
   * written successfully, reported as created.
   */
  it('refuses an execFile source with no place to write it', () => {
    expect(sourceRefusal({ kind: 'execFile', argv: ['trove', 'get', 'file', 'entry'] }))
      .toBe("source.argv for kind 'execFile' has no '{}' for the provider to substitute");
  });

  it('accepts `{}` embedded in a longer argument', () => {
    expect(sourceRefusal({ kind: 'execFile', argv: ['tool', '--out={}'] })).toBeUndefined();
  });
});

describe('whether the material has to be fetched at all', () => {
  it('fetches under always, which is what a digest is for', () => {
    expect(needsMaterial('always')).toBe(true);
  });

  /**
   * Not an optimisation. Fetching would unlock a vault, run a subprocess and hold a secret in
   * memory to compute an answer that is then discarded — during `pulumi preview`, which is the
   * command people run precisely because it touches nothing.
   */
  it('does not fetch under once', () => {
    expect(needsMaterial('once')).toBe(false);
  });

  it('marks unfetched state with a word rather than an empty digest', () => {
    // an empty string would read as "digest not computed yet" and invite a comparison against one
    expect(SEEDED).toBe('seeded');
  });
});

describe('the source union is data', () => {
  it('describes every source without holding any material', () => {
    const sources: Source[] = [
      { kind: 'file', path: 'secrets/claude-p.credentials.json' },
      { kind: 'exec', argv: ['op', 'read', 'op://vault/item/field'] },
      { kind: 'execFile', argv: ['trove', 'get', 'file', '--name', 'key', '--out', '{}', 'entry'] },
    ];
    // JSON round-trips, which is the property a resource input has to have and a closure does not
    expect(JSON.parse(JSON.stringify(sources))).toEqual(sources);
  });
});
