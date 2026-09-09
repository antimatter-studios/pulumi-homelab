import { describe, expect, it } from 'vitest';
import { hostsName, setHostsName } from './hostname.ts';

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
