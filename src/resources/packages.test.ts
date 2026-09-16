import { describe, expect, it } from 'vitest';
import { autoMarked } from '../aptmark.ts';
import { missingFrom, packagesChanged, parseInstalled, parsePackageState, parseSimulatedRemovals, presentCommands } from './packages.ts';

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

/**
 * The read had to grow for this, because "installed" and "installed because somebody asked" are
 * different states of the machine and only one of them matches a declaration.
 */
describe('reading dpkg and apt in one round trip', () => {
  const OUT = [
    'ovmf installed 2024.02',
    'qemu-efi-arm installed 2024.02',
    '#pulumi-homelab#manual',
    'qemu-efi-arm',
  ].join('\n');

  it('splits the two answers apart', () => {
    expect(parsePackageState(OUT)).toEqual({
      installed: { ovmf: '2024.02', 'qemu-efi-arm': '2024.02' },
      manual: ['qemu-efi-arm'],
    });
  });

  it('does not fold the marker into either answer', () => {
    const state = parsePackageState(OUT);
    expect(Object.keys(state.installed)).not.toContain('#pulumi-homelab#manual');
    expect(state.manual).not.toContain('#pulumi-homelab#manual');
  });

  it('reads a machine that holds none of them as manual', () => {
    expect(parsePackageState('ovmf installed 1\n#pulumi-homelab#manual\n').manual).toEqual([]);
  });

  it('still reads dpkg when apt-mark said nothing at all', () => {
    // apt-mark is not installed on every image, and a missing marking is not a reason to lose the
    // answer to the question this resource was always asking
    expect(parsePackageState('ovmf installed 1\n').installed).toEqual({ ovmf: '1' });
  });

  it('finds the drift the old read could not see', () => {
    // declared, installed, and held as auto — so apt autoremove is entitled to take it
    const { installed, manual } = parsePackageState(OUT);
    expect(autoMarked(['ovmf', 'qemu-efi-arm'], installed, manual)).toEqual(['ovmf']);
  });
});

describe('composing what brings the present half into line', () => {
  it('does nothing when the machine already matches', () => {
    expect(presentCommands({ missing: [], auto: [], update: false })).toEqual([]);
  });

  it('installs what is missing, in one apt-get', () => {
    const commands = presentCommands({ missing: ['ovmf', 'nginx'], auto: [], update: false });
    expect(commands).toHaveLength(1);
    expect(commands[0]).toContain("'ovmf' 'nginx'");
  });

  it('marks what apt holds as auto, even with nothing to install', () => {
    // the case the resource used to miss entirely: nothing missing, nothing installed, success
    // reported, and three declared packages still removable by an autoremove
    const commands = presentCommands({ missing: [], auto: ['ovmf'], update: false });
    expect(commands).toHaveLength(1);
    expect(commands[0]).toContain('apt-mark manual');
  });

  it('installs before it marks', () => {
    // a package apt has just installed on request is already manual, so a mark computed from the
    // earlier reading asks for work apt has already done
    const commands = presentCommands({ missing: ['nginx'], auto: ['ovmf'], update: false });
    expect(commands.findIndex((c) => c.includes('apt-get install')))
      .toBeLessThan(commands.findIndex((c) => c.includes('apt-mark')));
  });

  it('refreshes the lists first when asked', () => {
    const commands = presentCommands({ missing: ['nginx'], auto: [], update: true });
    expect(commands[0]).toBe('apt-get update -qq');
  });

  it('refreshes nothing when not asked', () => {
    expect(presentCommands({ missing: ['nginx'], auto: [], update: false }).join(' '))
      .not.toContain('apt-get update');
  });

  it('never prompts', () => {
    expect(presentCommands({ missing: ['nginx'], auto: [], update: false })[0])
      .toContain('DEBIAN_FRONTEND=noninteractive');
  });

  it('never removes or autoremoves anything', () => {
    // this resource adds and does not remove: apt-get purge takes the dependents of what it takes,
    // and autoremove reaches past anything declared here
    const commands = presentCommands({ missing: ['nginx'], auto: ['ovmf'], update: true }).join(' ');
    expect(commands).not.toContain('autoremove');
    expect(commands).not.toContain('purge');
    expect(commands).not.toContain('apt-mark auto');
  });

  it('is the same list every time, so a refresh does not look like a change', () => {
    const args = { missing: ['nginx'], auto: ['ovmf'], update: true };
    expect(presentCommands(args)).toEqual(presentCommands(args));
  });
});

describe('deciding whether the list has stopped being true', () => {
  const next = { present: ['ovmf'], absent: ['telnet'], update: false };
  const matching = { present: ['ovmf'], absent: ['telnet'], update: false, missing: [], lingering: [], auto: [] };

  it('is quiet when the machine matches the declaration', () => {
    expect(packagesChanged(matching, next)).toBe(false);
  });

  it('reports a declared package apt holds as auto', () => {
    // installed, agreed with by every other read, and still removable by an autoremove aimed at
    // something else — so the declaration is weaker than it reads
    expect(packagesChanged({ ...matching, auto: ['ovmf'] }, next)).toBe(true);
  });

  it('reports a package that is missing, and one that is back', () => {
    expect(packagesChanged({ ...matching, missing: ['ovmf'] }, next)).toBe(true);
    expect(packagesChanged({ ...matching, lingering: ['telnet'] }, next)).toBe(true);
  });

  it('reports a change to either list', () => {
    expect(packagesChanged(matching, { ...next, present: ['ovmf', 'nginx'] })).toBe(true);
    expect(packagesChanged(matching, { ...next, absent: [] })).toBe(true);
    expect(packagesChanged(matching, { ...next, update: true })).toBe(true);
  });

  it('reads the older spelling of the present list', () => {
    expect(packagesChanged({ ...matching, present: undefined, names: ['ovmf'] }, next)).toBe(false);
  });

  it('reports a change when the state never recorded whether lists are refreshed', () => {
    // state written before `update` existed has no answer for it, and treating "unrecorded" as
    // "agrees with whatever the code says" would let a field arrive silently and never be applied
    const { update: _dropped, ...withoutUpdate } = matching;
    expect(packagesChanged(withoutUpdate, next)).toBe(true);
    expect(packagesChanged(matching, next)).toBe(false);
  });

  it('treats a state with nothing recorded as changed rather than as matching', () => {
    // an imported resource has no reading behind it, and calling that agreement would be a machine
    // nobody ever looks at
    expect(packagesChanged({}, next)).toBe(true);
  });
});
