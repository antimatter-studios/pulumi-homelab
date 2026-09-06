import { describe, expect, it } from 'vitest';
import { parseShow } from './systemd.ts';

/**
 * The bug this exists to prevent was found on a real machine and could not have been found on a
 * fixture: `systemctl show --property=A,B --value` returns the values in systemd's own order, not
 * the order they were asked for, so a positional read swaps them. On a machine where every unit is
 * stopped and disabled the swapped answer is identical to the right one — so every unit anybody had
 * exercised agreed with the bug, and it took a unit that was actually running to expose it.
 *
 * What it produced was worse than a wrong value: permanent phantom drift on the three units that
 * were up, each reporting `enabled: true => false, started: true => false` on every refresh, on
 * units nobody had touched — and an `up` that "corrected" it by restarting k3s and the media player every
 * deployment.
 */
describe('reading systemctl show', () => {
  it('reads values by key, whatever order systemd returns them in', () => {
    // this is the real output for a unit that is enabled and running: ActiveState comes first,
    // though UnitFileState was asked for first
    expect(parseShow('ActiveState=active\nUnitFileState=enabled')).toEqual({
      ActiveState: 'active', UnitFileState: 'enabled',
    });
  });

  it('gives the same answer when the order is reversed', () => {
    const one = parseShow('ActiveState=active\nUnitFileState=enabled');
    const other = parseShow('UnitFileState=enabled\nActiveState=active');
    expect(one).toEqual(other);
  });

  it('keeps an empty value attached to its own key', () => {
    // UnitFileState is empty for a transient or generated unit. With --value that is a blank line,
    // and the positional parse then reads whatever follows — the file contents — as the active state
    expect(parseShow('UnitFileState=\nActiveState=active')).toEqual({
      UnitFileState: '', ActiveState: 'active',
    });
  });

  it('omits a property systemd does not recognise rather than shifting the rest', () => {
    expect(parseShow('ActiveState=active').UnitFileState).toBeUndefined();
  });

  it('ignores anything that is not a key=value line', () => {
    expect(parseShow('some prose\n\nActiveState=active')).toEqual({ ActiveState: 'active' });
  });

  it('keeps an = that appears inside a value', () => {
    expect(parseShow('ExecStart=/usr/bin/node --env=x server.mjs').ExecStart)
      .toBe('/usr/bin/node --env=x server.mjs');
  });
});

/**
 * An update that changes nothing must do nothing to the machine.
 *
 * Every `diff` in this package reports a change when the serialised provider differs, so the first
 * deployment after any edit here — including a comment, since the closure carries the source text
 * of what it captures — updates every resource. If `apply` restarted unconditionally, that would
 * mean every managed service restarting because somebody fixed a typo: k3s dropping its cluster and
 * the media player stopping a film.
 *
 * `apply` is not exported, so what is asserted here is the decision it makes, in the same form.
 */
function actsNeeded(
  current: { unit: string; mode: string; enabled: boolean; started: boolean } | null,
  wanted: { unit: string; mode: string; enabled: boolean; started: boolean },
): { rewrite: boolean; relabel: boolean; bounce: boolean } {
  const rewrite = current === null || current.unit !== wanted.unit || current.mode !== wanted.mode;
  const relabel = current === null || current.enabled !== wanted.enabled;
  return { rewrite, relabel, bounce: rewrite || current === null || current.started !== wanted.started };
}

describe('doing only what is different', () => {
  const running = { unit: '[Unit]\nDescription=x\n', mode: '0644', enabled: true, started: true };

  it('does nothing at all when the machine already matches', () => {
    expect(actsNeeded(running, running)).toEqual({ rewrite: false, relabel: false, bounce: false });
  });

  it('restarts a service whose unit file changed, even though it was already running', () => {
    // systemd would otherwise go on executing the definition the process was started with, which is
    // the classic "why has my edit not taken effect" afternoon
    const edited = { ...running, unit: '[Unit]\nDescription=y\n' };
    expect(actsNeeded(running, edited)).toEqual({ rewrite: true, relabel: false, bounce: true });
  });

  it('enables without restarting when only that differs', () => {
    const disabled = { ...running, enabled: false };
    expect(actsNeeded(disabled, running)).toEqual({ rewrite: false, relabel: true, bounce: false });
  });

  it('starts a stopped service without rewriting the file', () => {
    const stopped = { ...running, started: false };
    expect(actsNeeded(stopped, running)).toEqual({ rewrite: false, relabel: false, bounce: true });
  });

  it('does everything for a unit that is not there yet', () => {
    expect(actsNeeded(null, running)).toEqual({ rewrite: true, relabel: true, bounce: true });
  });

  it('rewrites when only the mode differs, since that is asserted state too', () => {
    expect(actsNeeded({ ...running, mode: '0600' }, running).rewrite).toBe(true);
  });
});
