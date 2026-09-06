import { describe, expect, it } from 'vitest';
import { checkCommand } from './precondition.ts';

/**
 * The check runs on every create, update and refresh, and the same string has to be sent every
 * time. A check that is composed one way when it gates a deployment and another way when it
 * refreshes is a resource that passes once and then reports itself missing for ever.
 */
describe('composing the check', () => {
  it('sends an ordinary check exactly as it was written', () => {
    expect(checkCommand({ check: 'grep -q cgroup_memory /proc/cmdline', root: false }))
      .toBe('grep -q cgroup_memory /proc/cmdline');
  });

  it('wraps a check that needs root, whole', () => {
    const rooted = checkCommand({ check: 'test -f /root/.ssh/authorized_keys && grep -q x /root/x', root: true });
    expect(rooted.startsWith('sudo -n sh -c ')).toBe(true);
    // the && belongs to the inner shell: sudo must be given one argument, not a command and a
    // second thing the outer shell runs whatever sudo did
    expect(rooted).toContain("'test -f /root/.ssh/authorized_keys && grep -q x /root/x'");
  });

  it('is the same string both times it is asked', () => {
    const args = { check: "awk '$1 == \"memory\"' /proc/cgroups", root: true };
    expect(checkCommand(args)).toBe(checkCommand(args));
  });
});
