import { describe, expect, it } from 'vitest';
import { installScript } from './deb.ts';

const args = {
  name: 'x1200',
  url: 'https://github.com/antimatter-studios/geekworm-x1200-ups-cli/releases/download/v0.1.0/x1200_0.1.0_linux_arm64.deb',
  sha256: 'a'.repeat(64),
};

/**
 * The ordering here is the entire safety property of the resource. A verification that happens
 * after the install, or a download into a path someone else can write, is a checksum that proves
 * nothing while looking exactly like one that does.
 */
describe('composing the install', () => {
  it('verifies before it installs', () => {
    const script = installScript(args);
    expect(script.indexOf('sha256sum -c')).toBeLessThan(script.indexOf('apt-get install'));
  });

  it('stops the whole command when verification fails', () => {
    // Without set -e the failing checksum is just a non-zero exit in the middle of a list, and the
    // install on the next line runs anyway.
    expect(installScript(args).startsWith('set -e')).toBe(true);
  });

  it('downloads into a private directory rather than a predictable path', () => {
    const script = installScript(args);
    expect(script).toContain('mktemp -d');
    expect(script).not.toMatch(/-o ['"]?\/tmp\/[a-z]/);
  });

  it('removes the file whether or not it was accepted', () => {
    // A rejected download left on disk is one somebody finds later and assumes was fine.
    expect(installScript(args)).toContain('trap');
  });

  it('installs through apt rather than dpkg, so dependencies resolve', () => {
    const script = installScript(args);
    expect(script).toContain('apt-get install');
    expect(script).not.toContain('dpkg -i');
  });

  it('never prompts', () => {
    expect(installScript(args)).toContain('DEBIAN_FRONTEND=noninteractive');
  });

  it('quotes the url, so a query string cannot become shell syntax', () => {
    const script = installScript({ ...args, url: 'https://example.com/p.deb?a=1&b=2' });
    expect(script).toContain("'https://example.com/p.deb?a=1&b=2'");
    // Unquoted, the & would background curl and the install would race a download that has not
    // happened yet.
    expect(script).not.toMatch(/curl [^']*&b=2/);
  });

  it('quotes the checksum', () => {
    expect(installScript(args)).toContain(`'${'a'.repeat(64)}'`);
  });

  it('is the same string every time, so a refresh does not look like a change', () => {
    expect(installScript(args)).toBe(installScript(args));
  });
});
