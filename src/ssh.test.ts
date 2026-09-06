import { describe, expect, it } from 'vitest';
import { asRoot, escalate, heredoc, shellQuote, sshArgs } from './ssh.ts';

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

/**
 * Authentication and privilege escalation are separate steps, and treating them as one made a
 * transport decision look like a law: with `sudo -n` as the only option, this provider could adopt
 * a machine that already had passwordless sudo and could never bootstrap a fresh one — which is
 * exactly the machine you have after an SD card dies.
 */
describe('becoming root, on a machine that says how', () => {
  const host = { address: '198.51.100.10', user: 'admin' };

  it('uses sudo -n by default, which fails rather than waiting for a password', () => {
    expect(escalate(host, 'true')).toBe(asRoot('true'));
    expect(escalate(host, 'true')).toContain('-n');
  });

  it('prepends nothing at all when the connection is already root', () => {
    // and does not wrap it in sh -c either: there is nothing to escalate, so there is nothing to
    // quote, and an extra shell is one more thing between the code and the machine
    expect(escalate({ ...host, user: 'root', become: 'none' }, 'rm -f /x && echo done'))
      .toBe('rm -f /x && echo done');
  });

  it('uses sudo -S with a password, and keeps the password out of the command', () => {
    const command = escalate({ ...host, become: { password: 'hunter2' } }, 'true');
    expect(command).toContain('sudo -S');
    // the password goes down ssh's stdin: anything on the command line is readable by every user on
    // the machine through `ps`, which is worse than the problem it solves
    expect(command).not.toContain('hunter2');
  });

  it('silences sudo’s prompt, which would otherwise be quoted back inside an error', () => {
    expect(escalate({ ...host, become: { password: 'x' } }, 'true')).toContain("-p ''");
  });

  it('still passes the whole command as one argument, however it escalates', () => {
    for (const become of ['sudo', { password: 'x' }] as const) {
      expect(escalate({ ...host, become }, 'a && b')).toContain("'a && b'");
    }
  });
});

/**
 * Pulumi refreshes resources in parallel, so a stack of any size opens its connections within a
 * second or two of each other. Twenty-one of them trips sshd's default `MaxStartups 10:30:100` and
 * most of the run dies with `kex_exchange_identification: read: Connection reset by peer`, which
 * reads as a network fault rather than a limit. It happened on the first real refresh.
 */
describe('not opening a connection per question', () => {
  const host = { address: '198.51.100.10', user: 'admin' };

  it('reuses one connection for every command', () => {
    const args = sshArgs(host, 'true');
    expect(args).toContain('ControlMaster=auto');
    expect(args).toContain('ControlPersist=60s');
  });

  it('keeps the control socket short, since a unix path has about a hundred characters', () => {
    // a home directory plus a long hostname can exceed it, and the error nobody reads correctly
    const path = sshArgs(host, 'true').find((arg) => arg.startsWith('ControlPath='));
    expect(path).toBe('ControlPath=/tmp/pulumi-homelab-%C');
  });

  it('still refuses to wait for a password nobody can type', () => {
    expect(sshArgs(host, 'true')).toContain('BatchMode=yes');
  });

  it('puts the command last, after the destination', () => {
    const args = sshArgs(host, 'systemctl show player');
    expect(args[args.length - 2]).toBe('admin@198.51.100.10');
    expect(args[args.length - 1]).toBe('systemctl show player');
  });

  it('takes the timeout from the host, so a slow link can say so', () => {
    expect(sshArgs({ ...host, timeout: 30 }, 'true')).toContain('ConnectTimeout=30');
  });
});
