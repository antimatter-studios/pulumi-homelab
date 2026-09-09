import { describe, expect, it } from 'vitest';
import { parseDpkgStatus } from './apt.ts';

/**
 * dpkg keeps a record of a package it has removed, so a line coming back is not the same as a
 * package being present. `deinstall` and `config-files` are answers meaning "not installed" that
 * arrive looking exactly like an answer meaning it is — and a resource that read the presence of a
 * line as the presence of a package would decide there was nothing to do on a machine missing it.
 */
describe('reading dpkg’s status word', () => {
  it('gives the version of a package that is installed', () => {
    expect(parseDpkgStatus('installed 20.11.1-1')).toBe('20.11.1-1');
  });

  it('reports nothing for a package dpkg remembers and has removed', () => {
    expect(parseDpkgStatus('deinstall 2:20.2+dfsg-1')).toBeNull();
    expect(parseDpkgStatus('config-files 1.0-1')).toBeNull();
  });

  it('reports nothing for a package dpkg has never heard of', () => {
    expect(parseDpkgStatus('')).toBeNull();
  });

  it('gives an empty version rather than null when dpkg answers without one', () => {
    // installed and versionless is still installed: reporting null would make the resource
    // reinstall it on every deployment
    expect(parseDpkgStatus('installed')).toBe('');
  });

  it('is unmoved by the whitespace dpkg pads its output with', () => {
    expect(parseDpkgStatus('  installed   1.0-1  \n')).toBe('1.0-1');
  });
});
