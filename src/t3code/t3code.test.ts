import { describe, expect, it } from 'vitest';
import { AGENT_DEFAULTS, T3_DEFAULT, unitResourceLines } from './index.ts';

describe('the harness release this package was tested against', () => {
  it('pins a version and the checksum for it together', () => {
    // a version bumped with the hash left behind is an install that fails on a mismatch
    expect(T3_DEFAULT.version).toMatch(/^\d+\.\d+\.\d+-nightly\.\d+\.\d+$/);
    expect(T3_DEFAULT.sha256).toMatch(/^[0-9a-f]{64}$/);
  });

  it('builds the url from the version, so the two cannot disagree', () => {
    expect(T3_DEFAULT.url('1.2.3-nightly.4.5')).toContain('/v1.2.3-nightly.4.5/');
    expect(T3_DEFAULT.url('1.2.3-nightly.4.5')).toContain('t3-1.2.3-nightly.4.5-linux-arm64.tar.gz');
  });

  /**
   * Nightly is forced rather than chosen: preview builds are never offered as updates, so a preview
   * install sits on its bootstrap build for ever, and stable publishes no linux-arm64 asset at all.
   */
  it('is a nightly', () => {
    expect(T3_DEFAULT.version).toContain('nightly');
  });
});

describe('the agent CLIs installed by default', () => {
  it('installs gh, and the three providers t3 spawns', () => {
    expect(AGENT_DEFAULTS.map((agent) => agent.key)).toEqual(['gh', 'claude', 'codex', 'opencode']);
  });

  it('pins every one of them by checksum', () => {
    for (const agent of AGENT_DEFAULTS) {
      expect(agent.sha256, agent.key).toMatch(/^[0-9a-f]{64}$/);
    }
  });

  it('names the executable inside each archive, never bare', () => {
    // `--version` cannot tell a complete install from an incomplete one, so the path is the check
    for (const agent of AGENT_DEFAULTS) {
      expect(agent.bin, agent.key).toBeTruthy();
      expect(agent.bin.startsWith('/'), agent.key).toBe(false);
    }
  });

  /**
   * codex ships both a lone executable and a package, and only the second is a working install: the
   * bare binary runs and reports its version and then fails at the first session with
   * `codex-code-mode-host: No such file or directory`. The package has no top-level directory, which
   * is why its strip is 0 where everything else is 1.
   */
  it('takes the codex package rather than the bare binary', () => {
    const codex = AGENT_DEFAULTS.find((agent) => agent.key === 'codex');
    expect(codex?.url).toContain('codex-package');
    expect(codex?.strip).toBe(0);
    expect(codex?.bin).toBe('bin/codex');
  });

  it('strips the wrapping directory for the archives that have one', () => {
    for (const agent of AGENT_DEFAULTS.filter((a) => a.key !== 'codex')) {
      expect(agent.strip, agent.key).toBe(1);
    }
  });
});

describe('what the agents may take of the machine', () => {
  it('adds nothing when nothing is asked for, so existing units are unchanged', () => {
    expect(unitResourceLines()).toBe('');
    expect(unitResourceLines({ name: 't3code', limits: {} })).toBe('');
  });

  it('writes each limit and TMPDIR as a [Service] line', () => {
    const lines = unitResourceLines({
      limits: { memoryHigh: '6G', memoryMax: '8G', cpuWeight: 20, ioWeight: 20 },
      tmpDir: '/mnt/storage/t3code/tmp',
    });
    expect(lines).toContain('\nMemoryHigh=6G\n');
    expect(lines).toContain('\nMemoryMax=8G\n');
    expect(lines).toContain('\nCPUWeight=20\n');
    expect(lines).toContain('\nIOWeight=20\n');
    expect(lines).toContain('\nEnvironment=TMPDIR=/mnt/storage/t3code/tmp\n');
  });

  it('refuses a weight systemd would reject', () => {
    // systemd ignores an invalid weight with a log line, so the unit starts with no weight at all
    expect(() => unitResourceLines({ limits: { cpuWeight: 0 } })).toThrow(/CPUWeight/);
    expect(() => unitResourceLines({ limits: { ioWeight: 20000 } })).toThrow(/IOWeight/);
    expect(() => unitResourceLines({ limits: { cpuWeight: 2.5 } })).toThrow(/CPUWeight/);
  });
});
