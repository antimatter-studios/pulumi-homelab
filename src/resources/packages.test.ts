import { describe, expect, it } from 'vitest';
import { missingFrom, parseInstalled, parseSimulatedRemovals } from './packages.ts';

/**
 * The answer to "is this installed" is the absence of a line as much as the presence of one, and
 * both have to be read. dpkg says nothing at all about a package it has never heard of, and says
 * something misleading about one that is known but removed.
 */
describe('reading dpkg’s answer about several packages', () => {
  const OUT = [
    'btop installed 1.2.13-1',
    'ripgrep installed 13.0.0-4+b2',
    'player deinstall 2:20.2+dfsg-1',
    'libc6:arm64 installed 2.36-9+rpt2+deb12u7',
  ].join('\n');

  it('reads the installed ones with their versions', () => {
    expect(parseInstalled(OUT)).toMatchObject({ btop: '1.2.13-1', ripgrep: '13.0.0-4+b2' });
  });

  it('does not count a package that is known but removed', () => {
    // `deinstall` means dpkg still has a record of it and the files are gone — reporting that as
    // installed is how a resource decides there is nothing to do on a machine missing the package
    expect(parseInstalled(OUT).player).toBeUndefined();
  });

  it('says nothing about a package dpkg has never heard of', () => {
    expect(parseInstalled('')).toEqual({});
  });
});

describe('working out what still needs installing', () => {
  it('names the ones dpkg does not have, in the order they were declared', () => {
    expect(missingFrom(['btop', 'gdu', 'duf'], { btop: '1.2.13-1' })).toEqual(['gdu', 'duf']);
  });

  it('counts a multi-arch package as present under its bare name', () => {
    // dpkg reports `libc6:arm64`; nobody writes the architecture in a package list, and treating
    // those as different names would reinstall it on every single deployment
    expect(missingFrom(['libc6'], { 'libc6:arm64': '2.36-9' })).toEqual([]);
  });

  it('has nothing to do on a machine that already matches', () => {
    expect(missingFrom(['btop'], { btop: '1.2.13-1' })).toEqual([]);
  });

  it('asks for everything on a machine that has none of it', () => {
    expect(missingFrom(['btop', 'gdu'], {})).toEqual(['btop', 'gdu']);
  });
});

/**
 * Removal is the direction that can damage a machine, and apt does it with a success exit code:
 * from its point of view, taking the dependents of what you asked for is exactly what you asked
 * for. Asking first is what turns that into a refusal rather than a discovery.
 */
describe('reading what apt says a purge would take', () => {
  const SIMULATION = `NOTE: This is only a simulation!
Reading package lists...
Building dependency tree...
The following packages will be REMOVED:
  player player-data player-plugin
Remv player [2:20.2+dfsg-1]
Remv player-data [2:20.2+dfsg-1]
Remv player-plugin [20.3.7-1]
`;

  it('names every package apt would remove', () => {
    expect(parseSimulatedRemovals(SIMULATION))
      .toEqual(['player', 'player-data', 'player-plugin']);
  });

  it('does not read the prose above it as packages', () => {
    // "The following packages will be REMOVED:" and the indented list under it say the same thing
    // in a shape that is much harder to parse, and reading both would double every name
    expect(parseSimulatedRemovals(SIMULATION)).not.toContain('The');
    expect(parseSimulatedRemovals(SIMULATION)).toHaveLength(3);
  });

  it('says nothing when apt would remove nothing', () => {
    expect(parseSimulatedRemovals('NOTE: This is only a simulation!\nReading package lists...\n')).toEqual([]);
  });

  it('reads a multi-arch name, which is what gets compared against the request', () => {
    expect(parseSimulatedRemovals('Remv libc6:arm64 [2.36-9]')).toEqual(['libc6:arm64']);
  });
});
