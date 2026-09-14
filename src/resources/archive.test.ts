import { describe, expect, it } from 'vitest';
import {
  archiveAct,
  binaryPath,
  chownCommand,
  extractCommand,
  formatOf,
  installNeeded,
  installScript,
  ownershipChanged,
  parseProbe,
  parseVersion,
  probeCommand,
  resolveArgs,
  substitute,
  sourceChanged,
  whereOf,
  type ArchiveSource,
} from './archive.ts';

const source: ArchiveSource = {
  url: 'https://example.com/widget/v1.4.2/widget-linux-arm64.tar.gz',
  sha256: 'a'.repeat(64),
  prefix: '/opt/widget/1.4.2',
  link: '/opt/widget/current',
  strip: 1,
  format: 'tar.gz',
};

describe('telling what kind of archive a url names', () => {
  it('knows the four it can unpack, under both spellings', () => {
    expect(formatOf('https://x/a.tar.gz')).toBe('tar.gz');
    expect(formatOf('https://x/a.tgz')).toBe('tar.gz');
    expect(formatOf('https://x/a.tar.xz')).toBe('tar.xz');
    expect(formatOf('https://x/a.txz')).toBe('tar.xz');
    expect(formatOf('https://x/a.tar.bz2')).toBe('tar.bz2');
    expect(formatOf('https://x/a.tbz')).toBe('tar.bz2');
    expect(formatOf('https://x/a.zip')).toBe('zip');
  });

  it('looks past the query string, which release hosts fill with signatures', () => {
    // a presigned S3 url carries the expiry and signature after the name, and matching the whole
    // string would leave every one of them unrecognised
    expect(formatOf('https://x/a.tar.gz?X-Amz-Expires=300&X-Amz-Signature=deadbeef')).toBe('tar.gz');
  });

  it('says so rather than guessing when the name carries no extension', () => {
    // guessing tar.gz here would produce `tar -z` against a zip and an error about a bad magic
    // number, which says nothing about the actual mistake
    expect(formatOf('https://api.example.com/artifacts/12345/download')).toBeNull();
  });

  it('does not mistake a version that contains one of the extensions', () => {
    expect(formatOf('https://x/widget-1.0.zip.sha256')).toBeNull();
  });
});

describe('unpacking', () => {
  it('uses the decompressor that matches the format', () => {
    expect(extractCommand('tar.gz', '/t/a', '/t/u')).toContain('tar -z');
    expect(extractCommand('tar.xz', '/t/a', '/t/u')).toContain('tar -J');
    expect(extractCommand('tar.bz2', '/t/a', '/t/u')).toContain('tar -j');
    expect(extractCommand('zip', '/t/a', '/t/u')).toContain('unzip');
  });

  it('strips only when asked to', () => {
    expect(extractCommand('tar.gz', '/t/a', '/t/u', 0)).not.toContain('--strip-components');
    expect(extractCommand('tar.gz', '/t/a', '/t/u', 1)).toContain('--strip-components=1');
  });

  it('refuses a strip on a zip rather than ignoring one', () => {
    // unzip has no equivalent flag. Ignoring it would put every path one level deeper than whatever
    // refers to them, and the failure would arrive much later as a binary that is not where it
    // should be rather than as the bad argument it is
    expect(() => extractCommand('zip', '/t/a', '/t/u', 1)).toThrow(/strip-components/);
  });

  it('quotes both paths', () => {
    const command = extractCommand('tar.gz', "/t/it's", '/t/u');
    expect(command).toContain(String.raw`'/t/it'\''s'`);
  });
});

/**
 * The ordering here is the whole safety property. A verification that happens after the unpack, or
 * an unpack straight into the path everything refers to, is a checksum that proves nothing while
 * looking exactly like one that does.
 */
describe('composing the install', () => {
  it('verifies before it unpacks', () => {
    const script = installScript(source);
    expect(script.indexOf('sha256sum -c')).toBeLessThan(script.indexOf('tar '));
  });

  it('stops the whole command when verification fails', () => {
    // without set -e the failing checksum is a non-zero exit in the middle of a list, and the unpack
    // on the next line runs anyway
    expect(installScript(source).startsWith('set -e')).toBe(true);
  });

  it('downloads into a private directory rather than a predictable path', () => {
    const script = installScript(source);
    expect(script).toContain('mktemp -d');
    expect(script).not.toMatch(/-o ['"]?\/tmp\/[a-z]/);
  });

  it('removes the download whether or not it was accepted', () => {
    // a rejected archive left on disk is one somebody finds later and assumes was fine
    expect(installScript(source)).toContain('trap');
  });

  it('unpacks to a staging directory and moves it, so a half-extracted tree is never visible', () => {
    const script = installScript(source);
    expect(script.indexOf('-C "$dir/unpacked"')).toBeLessThan(script.indexOf('mv "$dir/unpacked"'));
    // unpacking straight into the prefix would leave a partial tree at the path everything refers
    // to for as long as extraction takes, and for ever if it fails halfway
    expect(script).not.toContain("-C '/opt/widget/1.4.2'");
  });

  it('moves the link last, because that is the step anything else can see', () => {
    const script = installScript(source);
    expect(script.indexOf('mv "$dir/unpacked"')).toBeLessThan(script.indexOf('ln -sfn'));
  });

  it('relinks with -n, so a link at a directory is replaced rather than written inside it', () => {
    // ln -sf onto an existing symlink-to-directory creates the new link *under* the target, and the
    // old version stays live while a link appears somewhere nobody looks
    expect(installScript(source)).toContain('ln -sfn');
  });

  it('leaves the link step out when there is no link', () => {
    expect(installScript({ ...source, link: '' })).not.toContain('ln -s');
    expect(installScript({ ...source, link: undefined })).not.toContain('ln -s');
  });

  it('passes the staging paths as shell variables rather than as quoted literals', () => {
    // $dir comes from mktemp at run time; single-quoting it would unpack into a directory literally
    // called $dir in the working directory
    const script = installScript(source);
    expect(script).toContain('-C "$dir/unpacked"');
    expect(script).not.toContain("'$dir");
  });

  it('quotes the url, so a query string cannot become shell syntax', () => {
    const script = installScript({ ...source, url: 'https://example.com/a.tar.gz?x=1&y=2' });
    expect(script).toContain("'https://example.com/a.tar.gz?x=1&y=2'");
    // unquoted, the & backgrounds curl and the checksum runs against a file that has not arrived
    expect(script).not.toMatch(/curl [^']*&y=2/);
  });

  it('quotes the checksum and the prefix', () => {
    const script = installScript({ ...source, prefix: "/opt/it's" });
    expect(script).toContain(`'${'a'.repeat(64)}'`);
    expect(script).toContain(String.raw`'/opt/it'\''s'`);
  });

  it('is the same string every time, so a refresh does not look like a change', () => {
    expect(installScript(source)).toBe(installScript(source));
  });
});

describe('where the install is looked for', () => {
  it('prefers the stable link, which is what outlives an upgrade', () => {
    expect(whereOf({ prefix: '/opt/w/1.4.2', link: '/opt/w/current' })).toBe('/opt/w/current');
  });

  it('falls back to the prefix when there is no link', () => {
    expect(whereOf({ prefix: '/opt/w/1.4.2', link: '' })).toBe('/opt/w/1.4.2');
    expect(whereOf({ prefix: '/opt/w/1.4.2' })).toBe('/opt/w/1.4.2');
  });

  it('takes a relative binary from there and leaves an absolute one alone', () => {
    expect(binaryPath('/opt/w/current', 'bin/widget')).toBe('/opt/w/current/bin/widget');
    expect(binaryPath('/opt/w/current', './bin/widget')).toBe('/opt/w/current/bin/widget');
    expect(binaryPath('/opt/w/current/', 'bin/widget')).toBe('/opt/w/current/bin/widget');
    expect(binaryPath('/opt/w/current', '/usr/local/bin/widget')).toBe('/usr/local/bin/widget');
    expect(binaryPath('/opt/w/current', '')).toBe('');
  });
});

describe('asking the machine whether it is installed', () => {
  it('answers on the directory alone when nothing stronger was asked for', () => {
    const command = probeCommand({ where: '/opt/w/current', prefix: '/opt/w/current' });
    expect(command).toContain("test -d '/opt/w/current'");
    expect(command).not.toContain('test -x');
  });

  it('requires the binary to be executable when one was named', () => {
    // the directory being there cannot tell a half-extracted tree from a good one, which is the
    // failure a wrong `strip` produces and the one worth catching
    expect(probeCommand({ where: '/opt/w/current', prefix: '/opt/w/current', binary: '/opt/w/current/bin/widget' }))
      .toContain("test -x '/opt/w/current/bin/widget'");
  });

  it('requires the thing to run when a health command was given', () => {
    // the strongest rung, and the only one that asks about the effect: a binary for the wrong
    // architecture and a tree missing its interpreter both pass -d and -x, and both are what a
    // corrupted download looks like
    expect(probeCommand({ where: '/opt/w/current', prefix: '/opt/w/current', healthCommand: '{}/bin/widget --version' }))
      .toContain('/opt/w/current/bin/widget --version');
  });

  it('discards what the health command printed, because only the exit code is the answer', () => {
    // a program that greets stdout or warns on stderr and exits zero is a working program, and its
    // chatter must not end up where the presence marker is read from
    const command = probeCommand({ where: '/opt/w/current', prefix: '/opt/w/current', healthCommand: '{}/bin/widget --version' });
    expect(command).toContain('>/dev/null 2>&1');
  });

  it('requires every rung that was asked for, not any of them', () => {
    const command = probeCommand({ where: '/opt/w/current', prefix: '/opt/w/current', binary: '/opt/w/current/bin/widget', healthCommand: '{}/bin/widget --version' });
    expect(command).toContain('test -d');
    expect(command).toContain('test -x');
    expect(command).toContain('--version');
    // `||` between them would let a bare directory answer for a binary that cannot run
    expect(command).not.toContain('||  ');
    expect(command.split('&&').length).toBeGreaterThan(3);
  });

  it('never fails the command over an absent install, because absence is an answer', () => {
    // `must` would throw on a non-zero exit, and "it is not installed" would arrive as a broken
    // deployment rather than as the thing that causes an install
    expect(probeCommand({ where: '/opt/w/current', prefix: '/opt/w/current' })).toContain('|| true');
  });

  it('substitutes the install path into both commands, everywhere it appears', () => {
    const command = probeCommand({ where: '/opt/w/current', prefix: '/opt/w/current', healthCommand: '{}/bin/widget check', versionCommand: '{}/bin/widget --version # {}' });
    expect(command).toContain('/opt/w/current/bin/widget check');
    expect(command).toContain('/opt/w/current/bin/widget --version');
    expect(command).not.toContain('{}');
  });

  it('asks for the version in the same round trip, and never over ssh twice', () => {
    const command = probeCommand({ where: '/opt/w/current', prefix: '/opt/w/current', versionCommand: '{}/bin/widget --version' });
    expect(command.indexOf('test -d')).toBeLessThan(command.indexOf('--version'));
  });

  it('leaves the version out entirely when nothing was given to ask with', () => {
    expect(probeCommand({ where: '/opt/w/current', prefix: '/opt/w/current' }))
      .toMatch(/#pulumi-homelab#version'$/);
  });

  it('asks who owns the prefix, not who owns the link', () => {
    // stat follows a symlink, so asking through `link` would answer about whichever version it
    // currently points at rather than the one being described
    const command = probeCommand({ where: '/opt/w/current', prefix: '/opt/w/1.4.2' });
    expect(command).toContain("stat -c '%U:%G' '/opt/w/1.4.2'");
    expect(command).not.toContain("stat -c '%U:%G' '/opt/w/current'");
  });

  it('never fails over a prefix it cannot stat', () => {
    expect(probeCommand({ where: '/opt/w', prefix: '/opt/w' })).toContain('2>/dev/null || true');
  });

  it('does not let a version command that exits non-zero look like absence', () => {
    // a binary built for another architecture exits 126, and a program with no --version exits 1;
    // asked as the version, both are installed, and neither should cause a reinstall
    const out = "present\n#pulumi-homelab#owner\nroot:root\n#pulumi-homelab#version\nExec format error\n";
    expect(parseProbe(out).installed).toBe(true);
    expect(parseProbe(out).version).toBe('');
  });

  it('reads both halves back apart', () => {
    const out = 'present\n#pulumi-homelab#owner\nsvc:svc\n#pulumi-homelab#version\nwidget 1.4.2 (abc1234)\n';
    expect(parseProbe(out)).toEqual({ installed: true, owner: 'svc', group: 'svc', version: '1.4.2' });
  });

  it('reports absent when the test printed nothing', () => {
    expect(parseProbe('\n#pulumi-homelab#owner\n\n#pulumi-homelab#version\n'))
      .toEqual({ installed: false, owner: '', group: '', version: '' });
  });
});

describe('standing in for where the install ended up', () => {
  it('replaces every occurrence, because a pipeline names the path more than once', () => {
    expect(substitute('{}/bin/w --version | grep -q "$({}/bin/w id)"', '/opt/w'))
      .toBe('/opt/w/bin/w --version | grep -q "$(/opt/w/bin/w id)"');
  });

  it('leaves a command that names no path alone', () => {
    expect(substitute('widget --version', '/opt/w')).toBe('widget --version');
  });
});

/**
 * The field that decides whether self-updating software can update itself. The fetch runs with
 * escalation, so without it the tree lands root-owned and every updater fails quietly against its
 * own install directory — the read stays green and the software never updates again.
 */
describe('who owns the unpacked tree', () => {
  it('chowns recursively, because an updater rewrites files inside the tree', () => {
    expect(chownCommand('/home/svc/.local/w', 'svc', 'svc')).toBe("chown -R 'svc:svc' '/home/svc/.local/w'");
  });

  it('chowns after the move, so the tree is owned before anything points at it', () => {
    const script = installScript({ ...source, owner: 'svc', group: 'svc' });
    expect(script.indexOf('mv "$dir/unpacked"')).toBeLessThan(script.indexOf('chown -R'));
    expect(script.indexOf('chown -R')).toBeLessThan(script.indexOf('ln -sfn'));
  });

  it('chowns the link itself rather than what it points at', () => {
    // -h, or the chown follows the link to the prefix that was just chowned anyway — and to the
    // wrong prefix entirely once an upgrade has repointed it
    const script = installScript({ ...source, owner: 'svc', group: 'svc' });
    expect(script).toContain("chown -h 'svc:svc' '/opt/widget/current'");
  });

  it('defaults to root, so nothing declared before the field existed changes', () => {
    expect(installScript(source)).toContain("chown -R 'root:root'");
  });

  it('does not let the archive choose who owns files on the machine', () => {
    // tar run as root restores the uids recorded in the archive by default, which makes ownership
    // depend on how somebody else packed the tarball rather than on what was declared
    expect(extractCommand('tar.gz', '/t/a', '/t/u')).toContain('--no-same-owner');
    expect(installScript(source)).toContain('--no-same-owner');
  });

  it('reports a prefix owned by somebody else as changed', () => {
    expect(ownershipChanged({ owner: 'root', group: 'root' }, { owner: 'svc', group: 'svc' })).toBe(true);
    expect(ownershipChanged({ owner: 'svc', group: 'root' }, { owner: 'svc', group: 'svc' })).toBe(true);
    expect(ownershipChanged({ owner: 'svc', group: 'svc' }, { owner: 'svc', group: 'svc' })).toBe(false);
  });

  it('says nothing about a prefix that is not there', () => {
    // absence is what the presence check answers; reporting it here too would ask for a chown on a
    // path that does not exist
    expect(ownershipChanged({ owner: '', group: '' }, { owner: 'svc', group: 'svc' })).toBe(false);
  });

  it('is not part of the source, so a changed owner does not refetch the archive', () => {
    // re-downloading would replace whatever the software installed for itself since, to fix
    // something one chown fixes
    const args = { name: 'w', url: 'https://x/a.tar.gz', sha256: 'a', prefix: '/opt/w' };
    const asRoot = resolveArgs(args);
    const asService = resolveArgs({ ...args, owner: 'svc', group: 'svc' });
    expect(sourceChanged(asRoot, asService)).toBe(false);
    expect(installNeeded(true, asRoot, asService)).toBe(false);
    // and it is still noticed, by the cheap thing rather than the expensive one
    expect(ownershipChanged(asRoot, asService)).toBe(true);
  });

  it('chowns rather than fetching when only the owner is wrong', () => {
    const args = { name: 'w', url: 'https://x/a.tar.gz', sha256: 'a', prefix: '/opt/w' };
    const wanted = resolveArgs({ ...args, owner: 'svc', group: 'svc' });
    expect(archiveAct({ installed: true, owner: 'root', group: 'root' }, resolveArgs(args), wanted))
      .toBe('chown');
  });

  it('still fetches for an absent install, whoever is meant to own it', () => {
    const args = { name: 'w', url: 'https://x/a.tar.gz', sha256: 'a', prefix: '/opt/w', owner: 'svc' };
    expect(installNeeded(false, resolveArgs(args), resolveArgs(args))).toBe(true);
  });

  it('settles to root when nothing was declared', () => {
    const settled = resolveArgs({ name: 'w', url: 'https://x/a.tar.gz', sha256: 'a', prefix: '/opt/w' });
    expect([settled.owner, settled.group]).toEqual(['root', 'root']);
  });

  it('carries a declared owner through', () => {
    const settled = resolveArgs({
      name: 'w', url: 'https://x/a.tar.gz', sha256: 'a', prefix: '/opt/w', owner: 'svc', group: 'agents',
    });
    expect([settled.owner, settled.group]).toEqual(['svc', 'agents']);
  });
});

describe('reading a version out of whatever the software printed', () => {
  it('finds the usual shape without being told', () => {
    expect(parseVersion('widget version 1.4.2')).toBe('1.4.2');
    expect(parseVersion('1.4')).toBe('1.4');
    expect(parseVersion('widget 1.4.2-rc.1+build7')).toBe('1.4.2-rc.1+build7');
  });

  it('takes a pattern for the programs the default gets wrong', () => {
    // a banner with a year in it answers 2024 to the default pattern, which is a plausible-looking
    // wrong answer rather than a visible failure
    expect(parseVersion('widget (c) 2024.1 Acme\nwidget/9.9.9', 'widget/([0-9.]+)')).toBe('9.9.9');
  });

  it('answers nothing rather than throwing when the program said nothing useful', () => {
    expect(parseVersion('command not found')).toBe('');
  });

  it('answers nothing rather than throwing when the pattern does not compile', () => {
    // a bad pattern is a mistake in the declaration, and it must not turn into a failed deployment
    // for a field that nothing compares anyway
    expect(parseVersion('widget 1.4.2', '([0-9')).toBe('');
  });
});

/**
 * This is the decision the whole resource turns on. Comparing versions here is what would make a
 * self-updating program permanently drifted, and reinstalling on a first apply is what would undo
 * the updates it had already installed for itself.
 */
describe('deciding what an install needs', () => {
  const args = { name: 'w', url: 'https://x/a.tar.gz', sha256: 'a', prefix: '/opt/w', owner: 'svc', group: 'svc' };
  const wanted = resolveArgs(args);
  const right = { installed: true, owner: 'svc', group: 'svc' };

  it('does nothing to an install that is there and owned correctly', () => {
    expect(archiveAct(right, wanted, wanted)).toBe('nothing');
    expect(archiveAct(right, null, wanted)).toBe('nothing');
  });

  it('installs what is absent, whatever it would be owned by', () => {
    expect(archiveAct({ installed: false, owner: '', group: '' }, wanted, wanted)).toBe('install');
    expect(archiveAct({ installed: false, owner: '', group: '' }, null, wanted)).toBe('install');
  });

  it('installs when the source moved, and does not merely chown', () => {
    expect(archiveAct(right, resolveArgs({ ...args, sha256: 'b' }), wanted)).toBe('install');
  });

  it('prefers the chown when both could be said to apply', () => {
    // fetching replaces what is on the machine and a chown does not, so where the cheap act is
    // enough the expensive one would roll a self-updated tool back to the bootstrap
    expect(archiveAct({ installed: true, owner: 'root', group: 'root' }, wanted, wanted)).toBe('chown');
  });
});

describe('deciding whether to fetch', () => {
  it('installs what is absent', () => {
    expect(installNeeded(false, null, source)).toBe(true);
    expect(installNeeded(false, source, source)).toBe(true);
  });

  it('leaves alone what is already there and still described the same way', () => {
    expect(installNeeded(true, source, source)).toBe(false);
  });

  it('does not overwrite something already installed on a first apply', () => {
    // no previous description means nothing says the machine is holding the wrong thing, and the
    // software has very likely updated itself past the bootstrap since it was put there
    expect(installNeeded(true, null, source)).toBe(false);
  });

  it('installs again when the url or the checksum move', () => {
    expect(installNeeded(true, source, { ...source, url: 'https://example.com/w/v2/w.tar.gz' })).toBe(true);
    expect(installNeeded(true, source, { ...source, sha256: 'b'.repeat(64) })).toBe(true);
  });

  it('installs again when where it goes or how it unpacks moves', () => {
    expect(sourceChanged(source, { ...source, prefix: '/opt/widget/2.0.0' })).toBe(true);
    expect(sourceChanged(source, { ...source, link: '/usr/local/widget' })).toBe(true);
    expect(sourceChanged(source, { ...source, strip: 0 })).toBe(true);
    expect(sourceChanged(source, { ...source, format: 'tar.xz' })).toBe(true);
  });

  it('has no version in it to change', () => {
    // the point of the resource: a program that upgraded itself between deployments changes nothing
    // here, so nothing reinstalls and nothing reports drift
    expect(Object.keys(source)).not.toContain('version');
    expect(sourceChanged(source, { ...source })).toBe(false);
  });
});

describe('settling the arguments', () => {
  it('infers the format from the url', () => {
    expect(resolveArgs({ name: 'w', url: 'https://x/a.tar.xz', sha256: 'a', prefix: '/opt/w' }).format)
      .toBe('tar.xz');
  });

  it('takes a given format over the url, for a download that carries no name', () => {
    expect(resolveArgs({
      name: 'w', url: 'https://api.x/artifacts/1/download', sha256: 'a', prefix: '/opt/w', format: 'zip',
    }).format).toBe('zip');
  });

  it('refuses a url it cannot read and names the way out', () => {
    expect(() => resolveArgs({ name: 'w', url: 'https://api.x/artifacts/1', sha256: 'a', prefix: '/opt/w' }))
      .toThrow(/give format/);
  });

  it('resolves the binary against the link rather than the versioned prefix', () => {
    // resolving against the prefix would pin the read to one version, and the next upgrade would
    // report the old path missing rather than the new one present
    expect(resolveArgs({
      name: 'w',
      url: 'https://x/a.tar.gz',
      sha256: 'a',
      prefix: '/opt/w/1.4.2',
      link: '/opt/w/current',
      binary: 'bin/widget',
    }).binary).toBe('/opt/w/current/bin/widget');
  });

  it('keeps the health command as given, since it is a command rather than a path', () => {
    expect(resolveArgs({
      name: 'w', url: 'https://x/a.tar.gz', sha256: 'a', prefix: '/opt/w', healthCommand: '{}/bin/w --version',
    }).healthCommand).toBe('{}/bin/w --version');
  });

  it('is the same answer every time, so nothing here can look like a change', () => {
    const args = { name: 'w', url: 'https://x/a.tar.gz', sha256: 'a', prefix: '/opt/w' };
    expect(resolveArgs(args)).toEqual(resolveArgs(args));
  });
});
