import { describe, expect, it } from 'vitest';
import { asRoot, heredoc, shellQuote } from './ssh';

/**
 * Everything sent to the machine goes through quoting: file contents, unit definitions, package
 * names read off a config file. A missed quote here is not a bug that gives a wrong answer, it is
 * one that runs somebody else's words as a command on a box you care about.
 */
describe('putting a string safely into a shell', () => {
  it('wraps a plain word', () => {
    expect(shellQuote('hello')).toBe("'hello'");
  });

  it('leaves the shell nothing to expand', () => {
    // inside single quotes a POSIX shell expands nothing at all, which is the whole point
    for (const nasty of ['$HOME', '`id`', '$(whoami)', 'a && rm -rf /', 'a; reboot', '*']) {
      expect(shellQuote(nasty)).toBe(`'${nasty}'`);
    }
  });

  it('survives the one character that cannot live inside single quotes', () => {
    // close, escape, reopen — the standard trick, and the one thing worth testing properly
    expect(shellQuote("it's")).toBe("'it'\\''s'");
    expect(shellQuote("'; rm -rf /; '")).toContain("'\\''");
  });

  it('never leaves an unbalanced quote, whatever it is given', () => {
    for (const text of ["a'b", "''", "'", "a'b'c", '']) {
      const quoted = shellQuote(text);
      expect(quoted.startsWith("'"), text).toBe(true);
      expect(quoted.endsWith("'"), text).toBe(true);
    }
  });
});

describe('writing a file through a here-document', () => {
  it('quotes the delimiter, so nothing in the body is expanded on the way in', () => {
    // a $ in a systemd unit would otherwise arrive as the empty string: subtly wrong rather than
    // obviously broken, which is the worst way for a deployment to fail
    const doc = heredoc('/etc/x.conf', 'Exec=/bin/thing $MAINPID\n');
    expect(doc).toContain("<<'PULUMI_EOF'");
    expect(doc).toContain('$MAINPID');
  });

  it('ends the body with a newline, or the closing delimiter is not on its own line', () => {
    expect(heredoc('/etc/x', 'no trailing newline')).toContain('no trailing newline\nPULUMI_EOF');
  });

  it('does not add a second newline to a body that already has one', () => {
    expect(heredoc('/etc/x', 'has one\n')).toContain('has one\nPULUMI_EOF');
  });
});

describe('running as root', () => {
  it('passes the whole command through as one argument', () => {
    const rooted = asRoot('rm -f /tmp/a && systemctl restart x');
    expect(rooted.startsWith('sudo -n sh -c ')).toBe(true);
    // the && belongs to the inner shell, not to sudo's own command line
    expect(rooted).toContain("'rm -f /tmp/a && systemctl restart x'");
  });

  it('refuses to sit and wait for a password, because a deployment cannot answer one', () => {
    expect(asRoot('true')).toContain('-n');
  });
});
