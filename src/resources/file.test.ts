import { execFileSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync, statSync } from 'node:fs';
import { userInfo, tmpdir } from 'node:os';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { ask } from '../ssh.ts';
import { parseFileStat, writeCommands } from './file.ts';

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

/**
 * A 183 KB page once stopped every deployment of a machine: the content went into one ssh argument,
 * Linux refused it, and the error was lost, so `pulumi up` waited for ever.
 */
describe('writing a file too big for one command', () => {
  const me = userInfo().username;
  const group = execFileSync('id', ['-gn'], { encoding: 'utf8' }).trim();

  function written(content: string): { text: string; mode: number; commands: string[] } {
    const dir = mkdtempSync(join(tmpdir(), 'managed-file-'));
    try {
      const path = join(dir, 'sub', 'page.html');
      const commands = writeCommands({ path, content, mode: '0640', owner: me, group });
      for (const command of commands) execFileSync('sh', ['-c', command]);
      return { text: readFileSync(path, 'utf8'), mode: statSync(path).mode & 0o777, commands };
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  }

  it('arrives whole, in commands that each fit', () => {
    const content = `<p>'quotes' $HOME \\ é 🇪🇸</p>\n`.repeat(9000);
    const result = written(content);
    expect(result.text).toBe(content);
    expect(result.mode).toBe(0o640);
    expect(result.commands.length).toBeGreaterThan(2);
    for (const command of result.commands) expect(Buffer.byteLength(command)).toBeLessThan(128 * 1024);
  });

  it('ends a big file with a newline, as a small one does', () => {
    expect(written('x'.repeat(100_000)).text).toBe(`${'x'.repeat(100_000)}\n`);
    expect(written('x').text).toBe('x\n');
  });

  it('sends a small file in one command', () => {
    expect(written('hello\n').commands).toHaveLength(1);
  });

  it('fails when ssh cannot be started, rather than never answering', async () => {
    // over the limit on 4 KiB pages (128 KiB) and on 16 KiB pages (512 KiB) alike
    await expect(ask({ address: '198.51.100.10', user: 'admin' }, 'x'.repeat(1_000_000)))
      .rejects.toThrow(/could not run ssh.*E2BIG/);
  });
});
