import { mkdtemp, readFile, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { beforeAll, describe, expect, it } from 'vitest';
import { localTransport } from './local.ts';
import { readFile as readManagedFile, writeFile as writeManagedFile } from './resources/file.ts';
import { readDirectory } from './resources/directory.ts';
import { readSymlink } from './resources/symlink.ts';

/**
 * The gap every real bug came through.
 *
 * `stat` answering `644` where the code says `0644`; `systemctl show --value` returning properties
 * in systemd's own order; `sshd -T` printing `without-password`. Each was a fixture agreeing with
 * the bug, because the same person wrote both. Here the real `stat` and the real shell answer, so
 * the reads are tested against the tools rather than against somebody's memory of them.
 */
let where: string;
const local = localTransport();

/**
 * These reads want GNU coreutils, and this transport found that out on its first run.
 *
 * `stat -c '%a %U %G'` is GNU; BSD `stat` on macOS takes `-f` and rejects `-c` outright. That is
 * not a bug — this package describes Linux machines and `stat -c` is right there — but it is a real
 * assumption that nothing had written down, and it is exactly the kind of thing running against a
 * shell finds and a fixture cannot.
 *
 * So the reads that shell out to `stat` run where GNU coreutils exist, and the rest run everywhere.
 * Skipping is honest here; making the resources speak BSD would be adding an untested platform to
 * a package that has never been pointed at one.
 */
const gnuStat = await localTransport().ask("stat -c '%a' . >/dev/null 2>&1").then((ran) => ran.code === 0);

beforeAll(async () => {
  where = await mkdtemp(join(tmpdir(), 'pulumi-homelab-'));
});

describe.skipIf(!gnuStat)('reading a file the machine actually has', () => {
  it('reads back what was written, with the mode in the shape the code writes it', async () => {
    const path = join(where, 'written.conf');
    await writeManagedFile(local, { path, content: 'a=b\n', mode: '0640', owner: '', group: '' });
    const found = await readManagedFile(local, path);
    expect(found?.content).toBe('a=b\n');
    // the original bug: stat says 644 where the code says 0644, and comparing those as strings is
    // drift on every refresh for ever
    expect(found?.mode).toBe('0640');
  });

  it('says nothing rather than something wrong about a file that is not there', async () => {
    expect(await readManagedFile(local, join(where, 'absent'))).toBeNull();
  });

  it('keeps a dollar sign, which a here-document would otherwise expand away', async () => {
    // a $ in a systemd unit arriving as the empty string is subtly wrong rather than obviously
    // broken, which is the worst way for a deployment to fail
    const path = join(where, 'unit.conf');
    await writeManagedFile(local, { path, content: 'ExecStart=/bin/x $MAINPID\n', mode: '0644', owner: '', group: '' });
    expect((await readManagedFile(local, path))?.content).toContain('$MAINPID');
  });
});

describe.skipIf(!gnuStat)('telling a directory from a file', () => {
  it('reads a directory and its mode', async () => {
    const found = await readDirectory(local, where);
    expect(found?.mode).toMatch(/^0\d{3}$/);
  });

  it('refuses a path that is a file rather than reporting it absent', async () => {
    // reporting it absent would send the next up into a mkdir that fails with something far less
    // useful than saying what is actually there
    const path = join(where, 'not-a-directory');
    await writeFile(path, 'x');
    await expect(readDirectory(local, path)).rejects.toThrow(/not a directory/);
  });

  it('reports nothing for a path with nothing at it', async () => {
    expect(await readDirectory(local, join(where, 'nowhere'))).toBeNull();
  });
});

describe('telling a symlink from what it points at', () => {
  it('reads a link’s target rather than following it', async () => {
    const { symlink } = await import('node:fs/promises');
    const link = join(where, 'link');
    await symlink('/somewhere/else', link);
    expect((await readSymlink(local, link))?.target).toBe('/somewhere/else');
  });

  it('refuses a real directory where a link belongs', async () => {
    // the failure this prevents is silent: journald writing the system journal to an SD card
    // because /var/log/journal was a directory rather than a link, on a machine that reads correct
    await expect(readSymlink(local, where)).rejects.toThrow(/not a symlink/);
  });

  it('reports a broken link as a link, because that is what it is', async () => {
    const { symlink } = await import('node:fs/promises');
    const dangling = join(where, 'dangling');
    await symlink(join(where, 'never-existed'), dangling);
    expect((await readSymlink(local, dangling))?.target).toContain('never-existed');
  });
});

describe('the transport itself', () => {
  it('treats a non-zero exit as an answer, not a fault', async () => {
    // half of what this package does is ask questions whose answer is exit 1
    expect((await local.ask('exit 3')).code).toBe(3);
  });

  it('runs a shell, since everything this package builds is a shell command', async () => {
    expect((await local.ask('echo one && echo two')).out.trim().split('\n')).toEqual(['one', 'two']);
  });

  it('does not reach for sudo unless asked', async () => {
    expect(local.escalate('id')).toBe('id');
    expect(localTransport({ escalateWith: 'sudo -n' }).escalate('id')).toBe('sudo -n id');
  });

  it('says what it is, for an error message', async () => {
    expect(local.describe()).toBe('local');
    expect(localTransport({ cwd: '/tmp' }).describe()).toBe('local:/tmp');
  });
});
