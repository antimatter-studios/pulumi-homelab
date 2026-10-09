import { describe, expect, it } from 'vitest';
import { fstabLinesFor, parseFstabSwap, parseProcSwaps, swapAction } from './swap.ts';

/**
 * The whole design of this resource is that what is active now and what will be active after a
 * reboot are different questions. These are the two answers, and getting either wrong produces a
 * resource that reports itself correct on the afternoon it ran and drifted every morning after.
 */
describe('reading what is swapping now', () => {
  const PROC = [
    'Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority',
    '/var/swap                               file            2097148 0       -2',
    '/dev/mmcblk0p3                          partition       1048572 512     -3',
  ].join('\n');

  it('skips the header and reads both kinds', () => {
    const swaps = parseProcSwaps(PROC);
    expect(swaps.map((s) => s.path)).toEqual(['/var/swap', '/dev/mmcblk0p3']);
    expect(swaps.map((s) => s.kind)).toEqual(['file', 'partition']);
  });

  it('rounds kilobytes to the megabytes the size was asked for in', () => {
    // a 2048 MB swapfile reports 2097148 kB, four short of the round number: flooring it would
    // report 2047 and diff against a request for 2048 on every refresh, for ever
    expect(parseProcSwaps(PROC)[0]?.sizeMb).toBe(2048);
    expect(parseProcSwaps(PROC)[1]?.sizeMb).toBe(1024);
  });

  it('reads a negative priority, which is what the kernel gives an ordinary swapfile', () => {
    expect(parseProcSwaps(PROC)[0]?.priority).toBe(-2);
  });

  it('says nothing rather than something wrong on a machine with no swap at all', () => {
    // the header is always there; a machine with swap off has that line and nothing else
    expect(parseProcSwaps('Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n')).toEqual([]);
    expect(parseProcSwaps('')).toEqual([]);
  });
});

describe('reading what will come back at the next boot', () => {
  const FSTAB = [
    'proc            /proc           proc    defaults          0       0',
    'PARTUUID=00112233-01  /boot/firmware  vfat    defaults          0       2',
    '/var/swap none swap sw 0 0',
  ].join('\n');

  it('finds an fstab swap line', () => {
    expect(parseFstabSwap(FSTAB)).toEqual(['/var/swap none swap sw 0 0']);
  });

  it('does not read a commented line as one that is in force', () => {
    // `#/var/swap none swap sw 0 0` still has `swap` in the third column, so a naive $3 == "swap"
    // reports a line that was switched off — and this resource would then comment out a comment,
    // every run, for ever
    expect(parseFstabSwap('#/var/swap none swap sw 0 0')).toEqual([]);
    expect(parseFstabSwap('  # /var/swap none swap sw 0 0')).toEqual([]);
  });

  it('ignores every other kind of mount', () => {
    expect(parseFstabSwap('PARTUUID=00112233-02  /  ext4  defaults,noatime  0  1')).toEqual([]);
  });

  it('finds a partition as readily as a file', () => {
    expect(parseFstabSwap('UUID=abcd-1234 none swap sw 0 0')).toHaveLength(1);
  });
});

/**
 * An update that changes nothing must do nothing, and on this resource that matters more than most:
 * the disable path runs `swapoff -a` and rewrites `/etc/fstab`. Doing that on a machine already
 * exactly as described is a write to the boot configuration for no reason at all.
 */
describe('deciding whether anything needs doing', () => {
  const off = { active: [], fstab: [], dphys: 'masked' };
  const on = { active: [{ path: '/var/swap', sizeMb: 2048 }], fstab: ['/var/swap none swap sw 0 0'], dphys: 'masked' };

  it('does nothing when swap is already off in both tenses', () => {
    expect(swapAction(off, { enabled: false, path: '/var/swap' })).toBe('nothing');
  });

  it('does nothing when swap is already on at the size asked for', () => {
    expect(swapAction(on, { enabled: true, sizeMb: 2048, path: '/var/swap' })).toBe('nothing');
  });

  it('disables when something is swapping now', () => {
    expect(swapAction(on, { enabled: false, path: '/var/swap' })).toBe('disable');
  });

  it('disables when nothing is swapping now but fstab will bring it back', () => {
    // off now and configured to return is not off: reading only the running state is what made this
    // resource report itself correct on the afternoon it ran and drifted every morning after
    expect(swapAction({ active: [], fstab: ['/var/swap none swap sw 0 0'], dphys: null }, { enabled: false, path: '/var/swap' }))
      .toBe('disable');
  });

  it('disables when the unit is merely disabled rather than masked', () => {
    // an apt upgrade of the package re-enables a disabled unit, so disabled is not off
    expect(swapAction({ active: [], fstab: [], dphys: 'disabled' }, { enabled: false, path: '/var/swap' }))
      .toBe('disable');
  });

  it('does nothing when the machine never had the package at all', () => {
    expect(swapAction({ active: [], fstab: [], dphys: null }, { enabled: false, path: '/var/swap' })).toBe('nothing');
  });

  it('enables when the size differs from what is mounted', () => {
    expect(swapAction(on, { enabled: true, sizeMb: 4096, path: '/var/swap' })).toBe('enable');
  });

  it('enables when swap is on but nothing will bring it back', () => {
    expect(swapAction({ ...on, fstab: [] }, { enabled: true, sizeMb: 2048, path: '/var/swap' })).toBe('enable');
  });

  it('enables when something else is swapping but not the declared file', () => {
    expect(swapAction({ active: [{ path: '/other', sizeMb: 2048 }], fstab: ['x'], dphys: null },
      { enabled: true, sizeMb: 2048, path: '/var/swap' })).toBe('enable');
  });

  it('accepts any size when none was asked for', () => {
    expect(swapAction(on, { enabled: true, path: '/var/swap' })).toBe('nothing');
  });
});

/**
 * A machine that boots from an SD card and swaps to zram and NVMe: off has to mean "never on the
 * card", not "never at all". Reading the other swap as drift would `swapoff` it on the first
 * refresh, which on a machine under memory pressure pulls gigabytes back into RAM at once.
 */
describe('leaving swap declared elsewhere alone', () => {
  const elsewhere = {
    active: [{ path: '/dev/zram0', sizeMb: 8192 }, { path: '/mnt/storage/swap/swap-1', sizeMb: 1024 }],
    fstab: ['/mnt/storage/swap/swap-1 none swap pri=10,nofail 0 0'],
    dphys: 'masked',
  };
  const allow = { enabled: false, path: '/var/swap', otherSwap: 'allow' as const };

  it('does nothing when only other swap is running and the unit is masked', () => {
    expect(swapAction(elsewhere, allow)).toBe('nothing');
  });

  it('still reads the same machine as swap to switch off without the option', () => {
    // the default is unchanged: an existing declaration keeps meaning no swap of any kind
    expect(swapAction(elsewhere, { enabled: false, path: '/var/swap' })).toBe('disable');
  });

  it('disables when the declared path is swapping', () => {
    expect(swapAction({ ...elsewhere, active: [...elsewhere.active, { path: '/var/swap', sizeMb: 100 }] }, allow))
      .toBe('disable');
  });

  it('disables when fstab will bring the declared path back', () => {
    expect(swapAction({ ...elsewhere, fstab: [...elsewhere.fstab, '/var/swap none swap sw 0 0'] }, allow))
      .toBe('disable');
  });

  it('disables when the unit is only disabled, so an upgrade could put swap on the card', () => {
    expect(swapAction({ ...elsewhere, dphys: 'disabled' }, allow)).toBe('disable');
  });

  it('is ignored when swap is enabled', () => {
    expect(swapAction({ ...elsewhere, active: [{ path: '/var/swap', sizeMb: 2048 }], fstab: ['/var/swap none swap sw 0 0'] },
      { enabled: true, sizeMb: 2048, path: '/var/swap', otherSwap: 'allow' })).toBe('nothing');
  });
});

describe('finding the fstab lines for one path', () => {
  const lines = ['/var/swap none swap sw 0 0', '/var/swap2 none swap sw 0 0', 'UUID=abcd none swap sw 0 0'];

  it('matches the source field exactly, not as a prefix', () => {
    expect(fstabLinesFor(lines, '/var/swap')).toEqual(['/var/swap none swap sw 0 0']);
  });

  it('finds nothing for a path fstab does not mention', () => {
    expect(fstabLinesFor(lines, '/swapfile')).toEqual([]);
  });
});
