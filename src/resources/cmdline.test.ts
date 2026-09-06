import { describe, expect, it } from 'vitest';
import { bootedWith, merge } from './cmdline.ts';

/**
 * The file this edits is the one that decides whether the machine boots at all. Everything here is
 * about changing the smallest possible part of it: a regenerated `cmdline.txt` that gets
 * `root=PARTUUID=…` wrong is a Pi that cannot be fixed over ssh, only by moving the card to another
 * computer.
 */
const REAL = 'console=serial0,115200 console=tty1 root=PARTUUID=00112233-02 rootfstype=ext4 fsck.repair=yes rootwait';

describe('putting parameters on the boot line', () => {
  it('appends what is missing and leaves everything else exactly as it was', () => {
    const merged = merge(REAL, ['cgroup_memory=1', 'cgroup_enable=memory']);
    expect(merged).toBe(`${REAL} cgroup_memory=1 cgroup_enable=memory`);
  });

  it('changes nothing when the parameters are already there', () => {
    // the ordinary case on every deployment after the first, and it has to come out byte for byte
    // the same or every run writes the file and reports a change
    const already = `${REAL} cgroup_memory=1`;
    expect(merge(already, ['cgroup_memory=1'])).toBe(already);
  });

  it('corrects a parameter that is present with the wrong value', () => {
    // the bug worth having a test for: a machine that says cgroup_memory=0 is precisely the one
    // somebody is fixing. Treating the key as present writes nothing, while the check on
    // /proc/cmdline goes on failing — a deployment asking for a reboot that can never satisfy it
    expect(merge(`${REAL} cgroup_memory=0`, ['cgroup_memory=1']))
      .toBe(`${REAL} cgroup_memory=1`);
  });

  it('corrects in place, so the ordering the image was written with survives', () => {
    expect(merge('cgroup_memory=0 root=PARTUUID=00112233-02 rootwait', ['cgroup_memory=1']))
      .toBe('cgroup_memory=1 root=PARTUUID=00112233-02 rootwait');
  });

  it('gives the same line whether it runs once or twice', () => {
    const once = merge(`${REAL} cgroup_memory=0`, ['cgroup_memory=1', 'cgroup_enable=memory']);
    expect(merge(once, ['cgroup_memory=1', 'cgroup_enable=memory'])).toBe(once);
  });

  it('handles a bare parameter, which has no value to compare', () => {
    expect(merge('quiet rootwait', ['quiet'])).toBe('quiet rootwait');
    expect(merge('rootwait', ['quiet'])).toBe('rootwait quiet');
  });

  it('does not mistake one parameter for another that starts the same way', () => {
    // cgroup_enable and cgroup_enable_memory would collide under any prefix comparison
    expect(merge('cgroup_enable=cpuset', ['cgroup_enable_memory=1']))
      .toBe('cgroup_enable=cpuset cgroup_enable_memory=1');
  });

  it('collapses the whitespace a hand edit leaves behind, without dropping anything', () => {
    expect(merge('  quiet   rootwait  ', ['splash'])).toBe('quiet rootwait splash');
  });
});

describe('asking the running kernel what it actually booted with', () => {
  it('anchors on both sides, so cgroup_memory=0 does not satisfy cgroup_memory=1', () => {
    const check = bootedWith(['cgroup_memory=1']);
    expect(check).toBe(`grep -qE '(^| )cgroup_memory=1( |$)' /proc/cmdline`);
  });

  it('joins several parameters so all of them have to hold', () => {
    // `;` between them would report success on the last one alone, which is the kind of check that
    // passes on a machine that is still wrong
    expect(bootedWith(['a=1', 'b=2'])).toContain(' && ');
  });

  it('treats a parameter as text rather than as a pattern', () => {
    // an unescaped . matches any character, so `rootfstype=ext4` would be satisfied by `ext4x`, and
    // a parameter carrying a + or a ( would make grep fail outright rather than answer
    expect(bootedWith(['a.b=1'])).toContain('a\\.b=1');
    expect(bootedWith(['a+b'])).toContain('a\\+b');
  });
});
