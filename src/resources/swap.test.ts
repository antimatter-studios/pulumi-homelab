import { describe, expect, it } from 'vitest';
import { parseFstabSwap, parseProcSwaps } from './swap.ts';

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
