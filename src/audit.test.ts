import { describe, expect, it } from 'vitest';
import { parseInstallTimes, parseLoginUsers, parseUidMin, parseUnitNames, splitSections } from './audit.ts';

/**
 * The audit is the one thing here that reads a machine it did not write to, so every answer it
 * gives comes from parsing output somebody else's tool chose the shape of. Both bugs this repo has
 * had so far were in reading state back rather than in doing anything, and these are the same kind
 * of code.
 */
describe('splitting one reply into its answers', () => {
  const reply = [
    '#pulumi-homelab#packages',
    'nodejs',
    'vim',
    '#pulumi-homelab#times',
    '1737480000 /var/lib/dpkg/info/vim.list',
    '#pulumi-homelab#units',
    '/etc/systemd/system/aiworld.service',
    '#pulumi-homelab#passwd',
    'root:x:0:0:root:/root:/bin/bash',
    '#pulumi-homelab#logindefs',
    'UID_MIN\t1000',
  ].join('\n');

  it('puts every line under the marker above it', () => {
    const sections = splitSections(reply);
    expect(sections.packages).toEqual(['nodejs', 'vim']);
    expect(sections.units).toEqual(['/etc/systemd/system/aiworld.service']);
    expect(sections.logindefs).toEqual(['UID_MIN\t1000']);
  });

  it('gives every section an empty list rather than nothing, on a machine that answered none of it', () => {
    // a minimal container has no /etc/login.defs and no units of its own; the audit still has to
    // return findings rather than throw on the first missing field
    const sections = splitSections('#pulumi-homelab#packages\n#pulumi-homelab#units\n');
    expect(sections.passwd).toEqual([]);
    expect(sections.times).toEqual([]);
  });
});

describe('reading install times out of dpkg', () => {
  it('drops the architecture, because apt-mark prints the bare name', () => {
    // a report that says `nodejs` was never installed, while listing `nodejs:arm64`'s timestamp
    // under a name nothing matches, is worse than no timestamp at all
    const times = parseInstallTimes(['1737480000 /var/lib/dpkg/info/nodejs:arm64.list']);
    expect(times.get('nodejs')).toBe(1737480000);
  });

  it('keeps the later of two architectures of the same package', () => {
    const times = parseInstallTimes([
      '1700000000 /var/lib/dpkg/info/libc6:armhf.list',
      '1737480000 /var/lib/dpkg/info/libc6:arm64.list',
    ]);
    expect(times.get('libc6')).toBe(1737480000);
  });

  it('ignores a line it cannot read rather than recording a package installed at NaN', () => {
    expect(parseInstallTimes(['no such thing', '']).size).toBe(0);
  });
});

describe('deciding which units are the machine’s own', () => {
  it('reports them the way SystemdUnit takes them, without the suffix', () => {
    expect(parseUnitNames(['/etc/systemd/system/aiworld.service'])).toEqual(['aiworld']);
  });

  it('ignores anything that is not a service', () => {
    // a timer or a socket is not something SystemdUnit models, so reporting one as an undeclared
    // service would send somebody looking for a resource that could not have declared it
    expect(parseUnitNames(['/etc/systemd/system/backup.timer'])).toEqual([]);
  });

  it('does not report a service called `*` on a machine with no units of its own', () => {
    // a shell that prints an unexpanded pattern back rather than failing quietly
    expect(parseUnitNames(['/etc/systemd/system/*.service'])).toEqual([]);
  });
});

describe('deciding which accounts a person made', () => {
  const passwd = [
    'root:x:0:0:root:/root:/bin/bash',
    'daemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin',
    'aiworld:x:997:997::/opt/app:/usr/sbin/nologin',
    'admin:x:1000:1000::/home/admin:/bin/bash',
    'nobody:x:65534:65534:nobody:/nonexistent:/usr/sbin/nologin',
  ];

  it('keeps the people and leaves the distribution alone', () => {
    expect(parseLoginUsers(passwd, 1000).map((u) => u.name)).toEqual(['admin']);
  });

  it('excludes nobody by uid, so it is not reported on every machine for ever', () => {
    // 65534 is above any UID_MIN, so a name-based rule would be the only thing keeping it out
    expect(parseLoginUsers(passwd, 500).map((u) => u.name)).not.toContain('nobody');
  });

  it('finds a system service account when the machine’s own policy puts the line lower', () => {
    expect(parseLoginUsers(passwd, 900).map((u) => u.name)).toEqual(['aiworld', 'admin']);
  });

  it('skips a line that is not a passwd entry', () => {
    expect(parseLoginUsers(['getent: command not found'], 1000)).toEqual([]);
  });
});

describe('finding where the distribution draws the line', () => {
  it('reads the machine’s own answer', () => {
    expect(parseUidMin(['UID_MIN\t1000'])).toBe(1000);
    expect(parseUidMin(['UID_MIN 500'])).toBe(500);
  });

  it('falls back to the conventional answer when the machine does not say', () => {
    // no /etc/login.defs at all, which is normal in a container
    expect(parseUidMin([])).toBe(1000);
  });
});
