import { describe, expect, it } from 'vitest';
import { hostsName, hostsNames, hostsNamesHost, parseHostnameOutput, setHostsName } from './hostname.ts';

/**
 * A machine whose two names disagree fails in ways that never mention either file: `sudo` becomes
 * slow because it resolves the hostname and waits, and some daemons bind to the wrong name. That is
 * why this is one resource — and why `/etc/hosts` is edited in place rather than regenerated, since
 * it collects hand-added entries nobody remembers making.
 */
const REAL = `127.0.0.1\tlocalhost
::1\t\tlocalhost ip6-localhost ip6-loopback
ff02::1\t\tip6-allnodes
ff02::2\t\tip6-allrouters
127.0.1.1\toldname
10.0.0.5\tprinter.lan printer
`;

describe('reading the name out of /etc/hosts', () => {
  it('finds the name on the 127.0.1.1 line', () => {
    expect(hostsName(REAL)).toBe('oldname');
  });

  it('takes the short name when the line is qualified', () => {
    // Debian writes `127.0.1.1 host.domain host`, and the short name is the one `hostname` has to
    // agree with
    expect(hostsName('127.0.1.1\thomelab.lan homelab\n')).toBe('homelab');
  });

  it('says nothing when the file has no such line', () => {
    expect(hostsName('127.0.0.1\tlocalhost\n')).toBeNull();
  });

  it('is not fooled by the localhost line above it', () => {
    expect(hostsName(REAL)).not.toBe('localhost');
  });
});

describe('writing the name into /etc/hosts', () => {
  it('replaces the name in place, leaving every other entry alone', () => {
    const updated = setHostsName(REAL, 'newname');
    expect(hostsName(updated)).toBe('newname');
    // the entry nobody remembers making is the one that must survive
    expect(updated).toContain('10.0.0.5\tprinter.lan printer');
    expect(updated).toContain('ff02::2');
    expect(updated).not.toContain('oldname');
  });

  it('keeps the line in its original position', () => {
    const updated = setHostsName(REAL, 'newname').split('\n');
    expect(updated[4]?.startsWith('127.0.1.1')).toBe(true);
    expect(updated).toHaveLength(REAL.split('\n').length);
  });

  it('writes the qualified name before the short one, as Debian does', () => {
    expect(setHostsName(REAL, 'homelab', 'lan')).toContain('127.0.1.1\thomelab.lan homelab');
  });

  it('adds the line after 127.0.0.1 when the file has none', () => {
    // at the end of a file with a block of static entries it would read as unrelated to the
    // loopback names it belongs with
    const added = setHostsName('127.0.0.1\tlocalhost\n10.0.0.5\tprinter\n', 'homelab').split('\n');
    expect(added[1]).toBe('127.0.1.1\thomelab');
    expect(added[2]).toBe('10.0.0.5\tprinter');
  });

  it('gives the same file whether it runs once or twice', () => {
    const once = setHostsName(REAL, 'homelab', 'lan');
    expect(setHostsName(once, 'homelab', 'lan')).toBe(once);
  });
});

/**
 * The bug this file did not catch the first time.
 *
 * `Hostname` reported an update on every deployment, on a machine that was correct, because the
 * short name was read as *the last word* on the line. Which position holds it is a convention, not
 * a rule: Debian's installer writes `127.0.1.1 host.domain host`, and plenty of machines have it
 * the other way round. On one of those, every run reported drift for ever — and a resource that
 * always reports a change trains people to stop reading the report, which is how a real conflict
 * elsewhere went unnoticed for hours.
 */
describe('reading the name whichever order it was written in', () => {
  it('finds the short name when the qualified one comes first', () => {
    expect(hostsName('127.0.1.1\thomelab.lan homelab\n')).toBe('homelab');
  });

  it('finds it when the qualified one comes second', () => {
    // the case that reported drift for ever
    expect(hostsName('127.0.1.1\thomelab homelab.lan\n')).toBe('homelab');
  });

  it('finds it when there is only one name', () => {
    expect(hostsName('127.0.1.1\thomelab\n')).toBe('homelab');
  });

  it('lists every name on the line', () => {
    expect(hostsNames('127.0.1.1\thomelab homelab.lan alias\n')).toEqual(['homelab', 'homelab.lan', 'alias']);
  });

  it('answers whether the line names the host, in any position', () => {
    for (const line of [
      '127.0.1.1\thomelab\n',
      '127.0.1.1\thomelab.lan homelab\n',
      '127.0.1.1\thomelab homelab.lan\n',
      '127.0.1.1\tHomelab\n',
    ]) {
      expect(hostsNamesHost(line, 'homelab'), line).toBe(true);
    }
  });

  it('is case-insensitive, because hostnames are', () => {
    // treating `Homelab` and `homelab` as two names is drift nobody can fix by editing the file
    expect(hostsNamesHost('127.0.1.1\tHOMELAB\n', 'homelab')).toBe(true);
  });

  it('does not accept a line that names something else', () => {
    expect(hostsNamesHost('127.0.1.1\toldname\n', 'homelab')).toBe(false);
    expect(hostsNamesHost('127.0.0.1\thomelab\n', 'homelab')).toBe(false);
  });

  it('is not fooled by a name that merely starts the same way', () => {
    expect(hostsNamesHost('127.0.1.1\thomelab-old\n', 'homelab')).toBe(false);
  });
});

/**
 * The bug that took a stack from "cannot report itself clean" to "cannot finish".
 *
 * The read used **one marker twice** and then destructured two parts out of the three that `split`
 * produces — so the hosts file was silently always the empty string, on every machine. The check
 * that the `127.0.1.1` line names the host then failed on a machine where the line was perfectly
 * correct, and because that check throws rather than reporting drift, the whole deployment aborted
 * partway.
 *
 * The reply below is the real one, tab-separated, because that is what Debian's own installer
 * writes — and the tab was the first thing suspected and was never the problem.
 */
const REPLY = [
  'homelab',
  '#pulumi-homelab#now',
  'homelab',
  '#pulumi-homelab#hosts',
  '127.0.0.1\tlocalhost',
  '127.0.1.1\thomelab',
  '',
].join('\n');

describe('splitting one reply into three answers', () => {
  it('reads all three, and the third is not empty', () => {
    const found = parseHostnameOutput(REPLY);
    expect(found.static).toBe('homelab');
    expect(found.transient).toBe('homelab');
    expect(found.hostsFile).toContain('127.0.1.1\thomelab');
  });

  it('finds the host on a tab-separated line, which is what Debian writes', () => {
    expect(hostsNamesHost(parseHostnameOutput(REPLY).hostsFile, 'homelab')).toBe(true);
  });

  it('does not lose the hosts file to a marker appearing twice', () => {
    // the whole bug: `split` divides at every occurrence, so one marker used twice yields three
    // parts, and taking two of them drops the last silently
    expect(parseHostnameOutput(REPLY).hostsFile.length).toBeGreaterThan(0);
  });

  it('reads a static and transient name that differ, which DHCP can cause', () => {
    const reply = ['homelab', '#pulumi-homelab#now', 'dhcp-given', '#pulumi-homelab#hosts', ''].join('\n');
    const found = parseHostnameOutput(reply);
    expect([found.static, found.transient]).toEqual(['homelab', 'dhcp-given']);
  });

  it('answers empty rather than throwing when the machine said nothing', () => {
    expect(parseHostnameOutput('')).toEqual({ static: '', transient: '', hostsFile: '' });
  });
});
