import { describe, expect, it } from 'vitest';
import {
  asRoot,
  controlPath,
  describe as describeTarget,
  escalate,
  heredoc,
  identityArgs,
  jumpArgs,
  multiHopRefusal,
  parseJump,
  proxyCommand,
  shellQuote,
  sshArgs,
} from './ssh.ts';

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
    // a home directory plus a long hostname can exceed it, and the error nobody reads correctly.
    // The route is hashed in rather than spelled out for the same reason — see the socket-keying
    // tests below for why the route has to be in there at all
    const path = sshArgs(host, 'true').find((arg) => arg.startsWith('ControlPath=')) ?? '';
    expect(path.startsWith('ControlPath=/tmp/pulumi-homelab-')).toBe(true);
    expect(path.endsWith('-%C')).toBe(true);
    expect(path.length).toBeLessThan(60);
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

/**
 * The same machine is not always reachable the same way. On a LAN it is a direct connection; from
 * elsewhere the only route may be a tunnel on a bastion — same machine, same stack, different
 * route. Without this the second case is unexpressible and Pulumi cannot run at all.
 */
describe('reaching a machine that is not directly reachable', () => {
  const direct = { address: '198.51.100.10', user: 'admin' };
  const jumped = { address: '127.0.0.1', user: 'admin', port: 2222, proxyJump: 'root@203.0.113.5:10022' };

  it('says nothing about a port when it is the usual one', () => {
    expect(sshArgs(direct, 'true')).not.toContain('-p');
  });

  it('passes a port through when there is one', () => {
    const args = sshArgs(jumped, 'true');
    expect(args[args.indexOf('-p') + 1]).toBe('2222');
  });

  it('passes the jump through as ProxyJump, which checks known_hosts for each hop', () => {
    // ProxyCommand piping through netcat would silently drop host verification for the far end,
    // which is the property this transport exists to inherit
    expect(sshArgs(jumped, 'true')).toContain('ProxyJump=root@203.0.113.5:10022');
  });

  it('puts every option before the destination', () => {
    // ssh reads options in order and does not apply one that comes after the host name
    const args = sshArgs(jumped, 'true');
    expect(args.indexOf('ProxyJump=root@203.0.113.5:10022')).toBeLessThan(args.indexOf('admin@127.0.0.1'));
    expect(args.indexOf('-p')).toBeLessThan(args.indexOf('admin@127.0.0.1'));
  });

  it('still puts the command last', () => {
    const args = sshArgs(jumped, 'systemctl show x');
    expect(args[args.length - 1]).toBe('systemctl show x');
    expect(args[args.length - 2]).toBe('admin@127.0.0.1');
  });
});

/**
 * `%C` is ssh's own hash of the local host, the remote host, the port and the user — and **not the
 * ProxyJump**. So one address reachable two ways would share a control socket: the second
 * connection silently reuses the first one's route, and a deployment aimed at a tunnel goes wherever
 * the master happened to be established. Whichever route was tried first wins for the next sixty
 * seconds, which looks exactly like a network fault.
 */
describe('keying the control socket on the route, not just the destination', () => {
  const path = (host: Parameters<typeof sshArgs>[0]) =>
    sshArgs(host, 'true').find((arg) => arg.startsWith('ControlPath=')) ?? '';

  it('gives a direct connection and a jumped one different sockets', () => {
    const same = { address: '198.51.100.10', user: 'admin' };
    expect(path(same)).not.toBe(path({ ...same, proxyJump: 'root@203.0.113.5' }));
  });

  it('gives two different jumps different sockets', () => {
    const same = { address: '127.0.0.1', user: 'admin', port: 2222 };
    expect(path({ ...same, proxyJump: 'root@203.0.113.5' }))
      .not.toBe(path({ ...same, proxyJump: 'root@203.0.113.6' }));
  });

  it('gives the same route the same socket every time, or multiplexing buys nothing', () => {
    const host = { address: '127.0.0.1', user: 'admin', port: 2222, proxyJump: 'root@203.0.113.5' };
    expect(path(host)).toBe(path({ ...host }));
  });

  it('keeps the socket path short, since a unix path has about a hundred characters', () => {
    expect(path({ address: '127.0.0.1', user: 'admin', proxyJump: 'root@203.0.113.5:10022' }).length)
      .toBeLessThan(60);
  });
});

describe('naming a machine in an error', () => {
  it('names the route, because the same machine reached two ways fails differently', () => {
    // "cannot reach admin@127.0.0.1:2222" without the jump names a destination nobody recognises
    expect(describeTarget({ address: '127.0.0.1', user: 'admin', port: 2222, proxyJump: 'root@203.0.113.5' }))
      .toBe('admin@127.0.0.1:2222 via root@203.0.113.5');
  });

  it('stays short for the ordinary case', () => {
    expect(describeTarget({ address: '198.51.100.10', user: 'admin' })).toBe('admin@198.51.100.10');
  });
});

/**
 * The failure this exists for points at the wrong thing. sshd's MaxAuthTries is 6; an agent holding
 * nine keys offers them in its own order; and if the key a host accepts is eighth, the connection
 * closes with `Too many authentication failures` before it is reached. Measured on a real bastion,
 * with the same key at position five earlier in the day and eight later, because the agent's order
 * changed when the vault was re-unlocked. So an unchanged stack works at three and fails at five,
 * and the obvious response — relaxing the server's limits — treats a symptom that was never the
 * cause.
 */
describe('offering one key rather than every key', () => {
  const host = { address: '10.0.0.9', user: 'root', identityFile: '/home/me/.ssh/s1.pub' };

  it('offers nothing in particular when no identity is declared', () => {
    expect(identityArgs(undefined)).toEqual([]);
    expect(sshArgs({ address: '10.0.0.9', user: 'root' }, 'true')).not.toContain('-i');
  });

  it('restricts rather than adds', () => {
    // without IdentitiesOnly the file joins the list the agent already offers instead of replacing
    // it, so the offer that trips MaxAuthTries still happens and nothing is fixed
    expect(identityArgs('/k/id.pub')).toEqual(['-o', 'IdentitiesOnly=yes', '-i', '/k/id.pub']);
  });

  it('puts the identity before the destination', () => {
    // ssh applies options in order and ignores one that comes after the host name
    const args = sshArgs(host, 'true');
    expect(args.indexOf('-i')).toBeLessThan(args.indexOf('root@10.0.0.9'));
  });

  it('takes a .pub path, so nothing secret goes near a program', () => {
    // ssh matches the public key against the agent and offers only that one; the private half never
    // leaves the agent
    expect(sshArgs(host, 'true')).toContain('/home/me/.ssh/s1.pub');
  });
});

describe('reaching a machine through another with an identity pinned', () => {
  const direct = { address: '10.0.0.9', user: 'root', proxyJump: 'jump@bastion:10022' };
  const pinned = { ...direct, identityFile: '/k/id.pub' };

  it('keeps plain -J when no identity is declared, so nothing existing changes', () => {
    expect(sshArgs(direct, 'true')).toContain('ProxyJump=jump@bastion:10022');
    expect(sshArgs(direct, 'true').join(' ')).not.toContain('ProxyCommand');
  });

  it('switches to a ProxyCommand when one is, because -J cannot carry options', () => {
    // measured: -J with the identity pinned still failed at the bastion, since ProxyJump does not
    // pass options to the ssh it spawns — so the bastion was still offered every key in the agent
    const args = sshArgs(pinned, 'true');
    expect(args.join(' ')).not.toContain('ProxyJump=');
    expect(args.join(' ')).toContain('ProxyCommand=');
  });

  it('pins the identity on the jump as well as the target', () => {
    // the bastion is usually the machine with the limit, being the one every key is offered to
    expect(proxyCommand(pinned)).toContain('IdentitiesOnly=yes');
    expect(proxyCommand(pinned)).toContain("-i '/k/id.pub'");
    expect(sshArgs(pinned, 'true')).toContain('-i');
  });

  it('forwards with -W rather than netcat, so the far end is still verified', () => {
    // the reason ProxyJump was chosen over a ProxyCommand in the first place was host verification
    // for the far end; -W keeps ssh doing the forwarding, so this is not a return to that
    const command = proxyCommand(pinned);
    expect(command).toContain('-W %h:%p');
    expect(command).not.toMatch(/\b(nc|netcat|socat)\b/);
  });

  it('carries the jump host, its port and its user', () => {
    const command = proxyCommand(pinned);
    expect(command).toContain("-p 10022");
    expect(command).toContain("-l 'jump'");
    expect(command).toContain("'bastion'");
  });

  it('fails rather than waiting for a password on the jump', () => {
    expect(proxyCommand(pinned)).toContain('BatchMode=yes');
  });

  it('gives the jump the same timeout as the connection', () => {
    expect(proxyCommand({ ...pinned, timeout: 7 })).toContain('ConnectTimeout=7');
  });

  it('quotes what goes into the command, since ssh runs it through a shell', () => {
    // ssh hands a ProxyCommand to /bin/sh, so an unquoted apostrophe in a user name or a path is
    // somebody else's words becoming shell syntax on the machine in between
    expect(proxyCommand({ ...pinned, proxyJump: "it's@host" })).toContain(`-l ${shellQuote("it's")}`);
    expect(proxyCommand({ ...pinned, identityFile: '/k/a b.pub' })).toContain(`-i ${shellQuote('/k/a b.pub')}`);
  });

  it('quotes a path that would otherwise read as a flag', () => {
    // `-i -oProxyCommand=...` would be ssh reading the filename as an option
    expect(proxyCommand({ ...pinned, identityFile: '-nasty' })).toContain(`-i ${shellQuote('-nasty')}`);
  });

  it('does not quote the option names, which are not data', () => {
    expect(proxyCommand(pinned)).toContain('-o IdentitiesOnly=yes');
  });

  it('hands both hops the same literal path, tilde and all', () => {
    // ssh expands `~` in IdentityFile itself, so quoting it for /bin/sh is not a bug: the inner ssh
    // receives exactly the string the outer one gets as argv, and both resolve it the same way.
    // The tempting "fix" is dropping the quotes to let the shell expand it, which breaks every path
    // containing a space
    const tilde = { ...pinned, identityFile: '~/.ssh/that-one.pub' };
    expect(sshArgs(tilde, 'true')).toContain('~/.ssh/that-one.pub');
    expect(proxyCommand(tilde)).toContain(`-i ${shellQuote('~/.ssh/that-one.pub')}`);
  });

  it('reads the local ssh configuration and writes nothing to it', () => {
    // every option is on the command line. The agent, known_hosts and ~/.ssh/config are inherited
    // exactly as they work from a terminal, and nothing here edits any of them
    const args = sshArgs(pinned, 'true');
    expect(args).not.toContain('-F');
    expect(args.join(' ')).not.toContain('UserKnownHostsFile');
    expect(args.join(' ')).not.toContain('StrictHostKeyChecking');
  });

  it('refuses a multi-hop jump rather than nesting shells it cannot test', () => {
    // a nested ProxyCommand built wrong does not fail cleanly: it connects somewhere unintended or
    // hangs, which is worse than being told no
    expect(multiHopRefusal({ ...pinned, proxyJump: 'a@one,b@two' })).toMatch(/multi-hop/);
    expect(() => sshArgs({ ...pinned, proxyJump: 'a@one,b@two' }, 'true')).toThrow(/multi-hop/);
  });

  it('allows a multi-hop jump when no identity is pinned', () => {
    expect(multiHopRefusal({ ...direct, proxyJump: 'a@one,b@two' })).toBeNull();
  });

  it('names the way out in the refusal', () => {
    expect(multiHopRefusal({ ...pinned, proxyJump: 'a@one,b@two' })).toContain('ssh/config');
  });
});

describe('taking a jump specification apart', () => {
  it('reads every combination of the three parts', () => {
    expect(parseJump('host')).toEqual({ user: undefined, host: 'host' });
    expect(parseJump('me@host')).toEqual({ user: 'me', host: 'host' });
    expect(parseJump('host:2222')).toEqual({ user: undefined, host: 'host', port: 2222 });
    expect(parseJump('me@host:2222')).toEqual({ user: 'me', host: 'host', port: 2222 });
  });

  it('does not take an IPv6 address apart in the middle', () => {
    // splitting on the first colon would make `fe80::1` a host of `fe80` on a port of nothing
    expect(parseJump('[fe80::1]:22')).toEqual({ user: undefined, host: 'fe80::1', port: 22 });
    expect(parseJump('me@[fe80::1]')).toEqual({ user: 'me', host: 'fe80::1' });
  });

  it('does not mistake a hostname containing a colon-like suffix for a port', () => {
    expect(parseJump('host:name')).toEqual({ user: undefined, host: 'host:name' });
  });

  it('ignores the whitespace somebody left in a config value', () => {
    expect(parseJump('  me@host  ')).toEqual({ user: 'me', host: 'host' });
  });
});

describe('the control socket', () => {
  const base = { address: '10.0.0.9', user: 'root' };

  it('is the same for the same route, or multiplexing buys nothing', () => {
    expect(controlPath(base)).toBe(controlPath({ ...base }));
  });

  it('separates a route through a jump from a direct one', () => {
    expect(controlPath(base)).not.toBe(controlPath({ ...base, proxyJump: 'bastion' }));
  });

  it('separates two identities on the same route', () => {
    // a socket opened offering one key would otherwise be reused for a declaration asking for
    // another, and the second would silently inherit the first's authentication
    expect(controlPath({ ...base, identityFile: '/k/a.pub' }))
      .not.toBe(controlPath({ ...base, identityFile: '/k/b.pub' }));
    expect(controlPath(base)).not.toBe(controlPath({ ...base, identityFile: '/k/a.pub' }));
  });

  it('stays short enough to be a unix socket path', () => {
    // a socket path has about a hundred characters, and an error about exceeding it is one nobody
    // reads correctly
    const long = { address: 'a'.repeat(60), user: 'root', identityFile: `/home/${'b'.repeat(60)}/k.pub` };
    expect(controlPath(long).length).toBeLessThan(60);
  });
});
