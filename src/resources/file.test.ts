import { describe, expect, it } from 'vitest';
import { parseFileStat } from './file.ts';

/**
 * The first bug this package ever had lived in this parse: `stat` says `644` where the code says
 * `0644`, and storing what stat said and diffing it against what the program declared is drift
 * reported on every refresh, for ever, on a file nobody has touched.
 */
describe('reading what stat says about a file', () => {
  it('reads the mode, the owner and the group', () => {
    expect(parseFileStat('644 root root')).toEqual({ mode: '0644', owner: 'root', group: 'root' });
  });

  it('pads the mode into the shape the code writes it in', () => {
    expect(parseFileStat('600 admin admin').mode).toBe('0600');
  });

  it('leaves a mode that already carries four digits alone', () => {
    // 2775 is four digits because of the setgid bit, not because it was normalised
    expect(parseFileStat('2775 root staff').mode).toBe('2775');
  });

  it('reads an owner whose name is not a word stat would pad differently', () => {
    expect(parseFileStat('640 systemd-network systemd-network')).toEqual({
      mode: '0640', owner: 'systemd-network', group: 'systemd-network',
    });
  });

  it('answers with empty strings rather than throwing on nothing', () => {
    // a read that throws here would report a machine as unreachable when the file is merely gone
    expect(parseFileStat('')).toEqual({ mode: '', owner: '', group: '' });
  });
});
