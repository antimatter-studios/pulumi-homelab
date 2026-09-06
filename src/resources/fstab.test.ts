import { describe, expect, it } from 'vitest';
import { findEntry, fstabLine, markedTargets, removeFromFstab, targetOf, upsertFstab } from './fstab.ts';

/**
 * This file decides whether the machine boots. Everything here is about changing one line of it and
 * touching nothing else: a regenerated fstab that gets the root filesystem's entry wrong is a
 * machine that cannot be fixed over ssh, only by moving the card to another computer.
 *
 * The sample is the real one off the Pi, prose comments and all — those two lines are somebody's
 * note to the next person about how swap works on this distribution, and a resource that tidied
 * them away would be destroying the only documentation the machine has.
 */
const REAL = `proc            /proc           proc    defaults          0       0
PARTUUID=00112233-01  /boot/firmware  vfat    defaults          0       2
PARTUUID=00112233-02  /               ext4    defaults,noatime  0       1
# a swapfile is not a swap partition, no line here
#   use  dphys-swapfile swap[on|off]  for that
UUID=00000000-0000-0000-0000-000000000000  /mnt/data  btrfs  defaults,noatime,compress=zstd,autodefrag,nofail  0  0
`;

describe('finding the entry for a mount point', () => {
  it('finds one by its target', () => {
    expect(targetOf('UUID=00000000-0000-0000-0000-000000000000  /mnt/data  btrfs  defaults  0  0')).toBe('/mnt/data');
    expect(findEntry(REAL, '/mnt/data')).toContain('btrfs');
  });

  it('does not read a comment as an entry', () => {
    // `#/dev/sda1 /mnt ext4 ...` has a plausible target in field two, so the comment check is by
    // first character and comes first
    expect(targetOf('#/dev/sda1 /mnt ext4 defaults 0 0')).toBeNull();
    expect(targetOf('#   use  dphys-swapfile swap[on|off]  for that')).toBeNull();
    expect(findEntry(REAL, '/mnt')).toBeNull();
  });

  it('ignores blank lines and anything too short to be an entry', () => {
    expect(targetOf('')).toBeNull();
    expect(targetOf('   ')).toBeNull();
    expect(targetOf('/dev/sda1')).toBeNull();
  });
});

describe('putting a line in without disturbing the file', () => {
  it('appends an entry the file does not have, keeping every other byte', () => {
    const line = fstabLine({ source: '/mnt/data/k8s/podlogs', target: '/var/log/pods', type: 'none', options: ['bind', 'nofail'], dump: 0, pass: 0 });
    const updated = upsertFstab(REAL, '/var/log/pods', line);
    expect(updated.startsWith(REAL.replace(/\n$/, ''))).toBe(true);
    expect(updated).toContain('/mnt/data/k8s/podlogs /var/log/pods none bind,nofail 0 0');
  });

  it('keeps the prose comments, which are the only documentation the machine has', () => {
    const updated = upsertFstab(REAL, '/var/log/pods', 'a /var/log/pods none bind 0 0');
    expect(updated).toContain('# a swapfile is not a swap partition, no line here');
    expect(updated).toContain('#   use  dphys-swapfile swap[on|off]  for that');
  });

  it('replaces in place rather than moving the entry to the end', () => {
    // fstab order is frequently deliberate, and an entry that migrates to the bottom on every edit
    // makes a diff of the file useless for seeing what actually changed
    const updated = upsertFstab(REAL, '/mnt/data', 'UUID=new  /mnt/data  btrfs  defaults  0  0');
    const lines = updated.split('\n');
    // where the entry was, with its marker taking the line above rather than the entry moving
    expect(lines[5]).toBe('# pulumi-homelab');
    expect(lines[6]).toBe('UUID=new  /mnt/data  btrfs  defaults  0  0');
    expect(lines).toHaveLength(REAL.split('\n').length + 1);
  });

  it('never touches the root filesystem entry while editing another', () => {
    const updated = upsertFstab(REAL, '/var/log/pods', 'a /var/log/pods none bind 0 0');
    expect(updated).toContain('PARTUUID=00112233-02  /               ext4    defaults,noatime  0       1');
  });

  it('gives the same file whether it runs once or twice', () => {
    const line = 'a /var/log/pods none bind 0 0';
    const once = upsertFstab(REAL, '/var/log/pods', line);
    expect(upsertFstab(once, '/var/log/pods', line)).toBe(once);
  });

  it('ends with exactly one newline however the file arrived', () => {
    expect(upsertFstab('a / ext4 defaults 0 0\n\n\n', '/x', 'b /x ext4 defaults 0 0'))
      .toBe('a / ext4 defaults 0 0\n# pulumi-homelab\nb /x ext4 defaults 0 0\n');
  });
});

describe('taking a line out', () => {
  it('removes the entry and nothing else', () => {
    const without = removeFromFstab(REAL, '/mnt/data');
    expect(without).not.toContain('00000000');
    expect(without).toContain('# a swapfile is not a swap partition, no line here');
    expect(without).toContain('PARTUUID=00112233-02');
  });

  it('leaves a commented entry for the same target alone', () => {
    // it was already switched off by somebody, and removing their comment destroys the record of it
    expect(removeFromFstab('#/dev/sda1 /mnt ext4 defaults 0 0\n', '/mnt')).toContain('#/dev/sda1');
  });
});

describe('writing the line', () => {
  it('writes the six fields in the order fstab has always had them', () => {
    expect(fstabLine({ source: 'UUID=x', target: '/mnt/x', type: 'ext4', options: ['noatime'], dump: 0, pass: 2 }))
      .toBe('UUID=x /mnt/x ext4 noatime 0 2');
  });

  it('writes defaults rather than an empty options field, which fstab cannot parse', () => {
    expect(fstabLine({ source: 'a', target: '/b', type: 'none', options: [], dump: 0, pass: 0 }))
      .toBe('a /b none defaults 0 0');
  });
});

describe('marking the lines this provider owns', () => {
  it('writes a marker above the entry, never on it', () => {
    // fstab(5) documents comments as whole lines and says nothing about trailing ones; a seventh
    // field on the line that decides whether the machine boots is not worth finding out about
    const updated = upsertFstab(REAL, '/var/log/pods', 'a /var/log/pods none bind 0 0');
    expect(updated).toContain('# pulumi-homelab\na /var/log/pods none bind 0 0');
  });

  it('keeps one marker rather than growing another on every run', () => {
    const line = 'a /var/log/pods none bind 0 0';
    const once = upsertFstab(REAL, '/var/log/pods', line);
    const twice = upsertFstab(once, '/var/log/pods', line);
    expect(twice).toBe(once);
    expect(twice.match(/# pulumi-homelab/g)).toHaveLength(1);
  });

  it('takes the marker away with the line', () => {
    const added = upsertFstab(REAL, '/var/log/pods', 'a /var/log/pods none bind 0 0');
    expect(removeFromFstab(added, '/var/log/pods')).not.toContain('# pulumi-homelab');
  });

  it('finds the mount points this provider claims', () => {
    const added = upsertFstab(REAL, '/var/log/pods', 'a /var/log/pods none bind 0 0');
    expect(markedTargets(added)).toEqual(['/var/log/pods']);
  });

  it('does not claim a line somebody else wrote a comment above', () => {
    // the marker is an annotation and is never matched on for identity, but the audit does read it,
    // so it has to mean only what this provider wrote
    expect(markedTargets(REAL)).toEqual([]);
    expect(markedTargets('# something else\na /x ext4 defaults 0 0')).toEqual([]);
  });

  it('adopting a hand-written line marks it rather than duplicating it', () => {
    // the visible cost of the idea: a hand-written line gains a comment on the first up. It stays
    // one line, in its original position, which is the part that matters
    const adopted = upsertFstab(REAL, '/mnt/data', 'UUID=00000000-0000-0000-0000-000000000000  /mnt/data  btrfs  defaults  0  0');
    expect(adopted.match(/\/mnt\/data/g)).toHaveLength(1);
    expect(markedTargets(adopted)).toEqual(['/mnt/data']);
  });
});
