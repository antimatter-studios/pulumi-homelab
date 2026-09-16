import { describe, expect, it } from 'vitest';
import { autoMarked, markManualCommand, parseManual } from './aptmark.ts';

/**
 * The gap this closes is quiet. A package already present as a dependency needs no installing, so a
 * resource that computes what is missing correctly computes nothing, installs nothing, and reports
 * success — while apt still records it as auto and `apt autoremove` is entitled to take it the
 * moment the thing that wanted it changes. Measured on a real machine: three UEFI firmware packages
 * declared and present, all three auto-marked because they arrived as recommends of qemu-system-arm.
 */
describe('reading what apt holds as manual', () => {
  it('takes one name per line', () => {
    expect(parseManual('ovmf\nqemu-efi-arm\nqemu-system-arm\n')).toEqual(['ovmf', 'qemu-efi-arm', 'qemu-system-arm']);
  });

  it('answers empty for a machine that holds none of them', () => {
    expect(parseManual('')).toEqual([]);
    expect(parseManual('\n\n')).toEqual([]);
  });

  it('strips the architecture, since a declaration carries none', () => {
    // dpkg reports a multi-arch package as `name:arch`, so comparing the qualified name against a
    // declared bare one would report a package as auto that apt holds as manual
    expect(parseManual('libc6:arm64\n')).toEqual(['libc6']);
  });

  it('ignores commentary apt may print', () => {
    expect(parseManual('# comment\novmf\n')).toEqual(['ovmf']);
  });
});

describe('deciding what needs marking', () => {
  const installed = { ovmf: '2024.02', 'qemu-efi-arm': '2024.02', 'qemu-system-arm': '8.2' };

  it('is content when everything declared is held as manual', () => {
    expect(autoMarked(['ovmf'], installed, ['ovmf', 'other'])).toEqual([]);
  });

  it('names a declared package apt holds as auto', () => {
    // installed, agreed with by every other read, and still removable by an autoremove somebody
    // runs for unrelated reasons
    expect(autoMarked(['ovmf', 'qemu-efi-arm'], installed, ['qemu-efi-arm'])).toEqual(['ovmf']);
  });

  it('says nothing about a package that is not installed at all', () => {
    // absence has no marking to be wrong, and reporting it here would mean two resources describing
    // one problem in two vocabularies
    expect(autoMarked(['not-there'], installed, [])).toEqual([]);
  });

  it('says nothing about a package nothing declared', () => {
    expect(autoMarked([], installed, [])).toEqual([]);
  });

  it('matches a declared name against a multi-arch installed one', () => {
    expect(autoMarked(['libc6'], { 'libc6:arm64': '2.36' }, [])).toEqual(['libc6']);
    expect(autoMarked(['libc6'], { 'libc6:arm64': '2.36' }, ['libc6'])).toEqual([]);
  });

  it('keeps the declared order, so a message reads the way the code does', () => {
    expect(autoMarked(['qemu-system-arm', 'ovmf'], installed, [])).toEqual(['qemu-system-arm', 'ovmf']);
  });
});

describe('composing the mark', () => {
  it('writes nothing when there is nothing to mark', () => {
    // apt-mark manual is idempotent and harmless to repeat, which is exactly why running it
    // unconditionally would be easy and wrong: a write on every deployment is a line in every log
    // for a machine nobody touched
    expect(markManualCommand([])).toBeNull();
  });

  it('marks the whole set in one command', () => {
    const command = markManualCommand(['ovmf', 'qemu-efi-arm']) ?? '';
    expect(command.match(/apt-mark/g)).toHaveLength(1);
    expect(command).toContain("'ovmf'");
    expect(command).toContain("'qemu-efi-arm'");
  });

  it('never marks anything auto', () => {
    // handing a package to autoremove is removal by a slower route, and this package does not remove
    // what it did not install — the same reasoning as PosixAcl never reaching for setfacl -b
    expect(markManualCommand(['ovmf'])).not.toContain('auto');
  });

  it('quotes the names, which come out of a declaration', () => {
    expect(markManualCommand(["it's"])).toContain(String.raw`'it'\''s'`);
  });

  it('is the same string for the same set, however it was ordered', () => {
    expect(markManualCommand(['b', 'a'])).toBe(markManualCommand(['a', 'b']));
  });

  it('says nothing on stdout, since the answer comes from the read that follows', () => {
    expect(markManualCommand(['ovmf'])).toContain('>/dev/null');
  });
});
