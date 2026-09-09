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

/**
 * The install is not the only thing that has to be conditional.
 *
 * Every `diff` in this package reports a change when the serialised provider differs, so an update
 * happens whenever `pulumi-homelab` itself is edited — a comment included. A resource that
 * reinstalled unconditionally would re-download a `.deb` on every machine it manages because
 * somebody fixed a typo, which is the same failure `SystemdUnit` had when it restarted every
 * service.
 */
describe('what an update actually does', () => {
  it('downloads into a private directory rather than a predictable path', () => {
    // a fixed name in a world-writable directory is a file another user can swap between the
    // checksum passing and apt reading it
    expect(installScript(args)).toContain('mktemp -d');
    expect(installScript(args)).not.toContain('/tmp/pkg.deb');
  });

  it('cleans up even when verification fails', () => {
    // a rejected file left behind is one somebody finds later and trusts
    expect(installScript(args)).toContain('trap');
  });

  it('uses apt rather than dpkg, so dependencies are resolved', () => {
    // dpkg -i on a package with an absent dependency leaves it unpacked but unconfigured, which
    // breaks the next unrelated apt run and gives no hint why
    expect(installScript(args)).toContain('apt-get install');
    expect(installScript(args)).not.toContain('dpkg -i');
  });
});
