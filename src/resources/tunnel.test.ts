import { describe, expect, it } from 'vitest';
import {
  RESTART_FLOOR,
  execStart,
  forwardFlag,
  forwardsRefusal,
  parseForwards,
  parseTunnel,
  preconditionRefusal,
  resolveTunnel,
  restartRefusal,
  sameForwards,
  tunnelUnit,
  unitNameFor,
  unitPath,
  unitRefusal,
  writeCommand,
} from './tunnel.ts';

const tunnel = {
  to: 'tunnel@bastion.example.com',
  port: 10022,
  identity: '/home/chris/.ssh/id_ed25519',
  runAs: 'chris',
  forwards: [
    { remote: 20022, local: 22, what: 'ssh' },
    { remote: 20080, local: 80, what: 'ingress' },
    { remote: 20443, local: 443, what: 'ingress, tls' },
  ],
  restartSec: 10,
  unit: 'autossh-bastion-example-com-10022',
  directory: '/etc/systemd/system',
};

describe('spelling a reverse forward', () => {
  it('publishes a far port and reaches a near one', () => {
    expect(forwardFlag({ remote: 20022, local: 22 })).toBe('20022:localhost:22');
  });

  it('passes a bind address through for the far end to accept or ignore', () => {
    expect(forwardFlag({ remote: 20080, local: 80, bind: '0.0.0.0' })).toBe('0.0.0.0:20080:localhost:80');
  });

  it('uses localhost rather than an address, which the far end resolves here', () => {
    // a host with no IPv4 loopback route exists, and 127.0.0.1 would be the one thing that fails
    expect(forwardFlag({ remote: 1, local: 2 })).toContain('localhost');
  });
});

/**
 * Every one of these has bitten a real machine in the last two days, and they share a shape: a
 * tunnel that exits is restarted, a restart is a login, and enough logins is a ban that takes out
 * every tunnel plus the route needed to fix them.
 */
describe('refusing a configuration that bans you', () => {
  it('accepts ten seconds and anything above the floor', () => {
    expect(restartRefusal(10)).toBeNull();
    expect(restartRefusal(RESTART_FLOOR)).toBeNull();
  });

  it('refuses a tight restart loop, and says why rather than just no', () => {
    const refusal = restartRefusal(1) ?? '';
    expect(refusal).toContain('fail2ban');
    expect(refusal).toContain('ExitOnForwardFailure');
  });

  it('refuses two forwards wanting the same far port', () => {
    // the second is refused, and with ExitOnForwardFailure the whole tunnel exits — including the
    // forwards that were fine
    const refusal = forwardsRefusal([{ remote: 20022, local: 22 }, { remote: 20022, local: 2222 }]) ?? '';
    expect(refusal).toContain('20022');
    expect(refusal).toContain('ExitOnForwardFailure');
  });

  it('refuses a tunnel with no forwards', () => {
    expect(forwardsRefusal([])).toMatch(/not a tunnel/);
  });

  it('refuses something that is not a port', () => {
    expect(forwardsRefusal([{ remote: 0, local: 22 }])).not.toBeNull();
    expect(forwardsRefusal([{ remote: 22, local: 70000 }])).not.toBeNull();
  });

  it('allows the same near port published twice on different far ports', () => {
    expect(forwardsRefusal([{ remote: 20022, local: 22 }, { remote: 30022, local: 22 }])).toBeNull();
  });
});

describe('composing the command', () => {
  it('carries every forward on one connection', () => {
    // one unit, one process, three -R flags. A unit per forward is three logins and a ban
    const command = execStart(tunnel);
    expect(command.match(/-R /g)).toHaveLength(3);
    expect(command.match(/autossh/g)).toHaveLength(1);
  });

  it('exits rather than forwarding nothing', () => {
    // the failure that looks healthiest: connected, and carrying no ports at all
    expect(execStart(tunnel)).toContain('ExitOnForwardFailure=yes');
  });

  it('turns autossh monitoring off and keeps the connection alive from inside it', () => {
    // -M opens another forwarded port pair on the far end: a second thing to collide and a second
    // thing for the bastion to refuse
    expect(execStart(tunnel)).toContain('-M 0');
    expect(execStart(tunnel)).toContain('ServerAliveInterval=30');
  });

  it('never waits for a prompt nobody will see', () => {
    expect(execStart(tunnel)).toContain('BatchMode=yes');
  });

  it('offers only the declared key', () => {
    // an agent is not available to a system unit, and offering a directory of keys to a bastion
    // with MaxAuthTries is the other way to get banned
    expect(execStart(tunnel)).toContain('IdentitiesOnly=yes');
    expect(execStart(tunnel)).toContain('-i /home/chris/.ssh/id_ed25519');
  });

  it('asks for no shell, no command and no tty', () => {
    expect(execStart(tunnel)).toContain('-N');
  });

  it('is the same string every time, so a refresh does not look like a change', () => {
    expect(execStart(tunnel)).toBe(execStart(tunnel));
  });
});

describe('the unit', () => {
  it('survives a boot that beats its own network', () => {
    // autossh gives up for good if its first connection dies inside thirty seconds, which turns a
    // transient boot-order failure into a tunnel that never returns
    const unit = tunnelUnit(tunnel);
    expect(unit).toContain('AUTOSSH_GATETIME=0');
    expect(unit).toContain('Wants=network-online.target');
    expect(unit).toContain('After=network-online.target');
  });

  it('runs as the account whose known_hosts trusts the far end', () => {
    expect(tunnelUnit(tunnel)).toContain('User=chris');
  });

  it('restarts always, and not immediately', () => {
    expect(tunnelUnit(tunnel)).toContain('Restart=always');
    expect(tunnelUnit(tunnel)).toContain('RestartSec=10');
  });

  it('records what each forward is for, since somebody reads this in a year', () => {
    const unit = tunnelUnit(tunnel);
    expect(unit).toContain('# 20022 -> 22 (ssh)');
    expect(unit).toContain('# 20443 -> 443 (ingress, tls)');
  });

  it('is the same file every time', () => {
    expect(tunnelUnit(tunnel)).toBe(tunnelUnit(tunnel));
  });
});

describe('naming the unit', () => {
  it('derives a name from where the tunnel goes', () => {
    expect(unitNameFor('tunnel@bastion.example.com', 10022)).toBe('autossh-bastion-example-com-10022');
  });

  it('keeps the port, so two tunnels to one host are two units', () => {
    expect(unitNameFor('a@h', 22)).not.toBe(unitNameFor('a@h', 2222));
  });

  it('leaves nothing systemd would read as a type suffix', () => {
    expect(unitNameFor('tunnel@h.example.com', 22)).not.toContain('.');
  });

  it('copes with a destination that names no user', () => {
    expect(unitNameFor('bastion.example.com', 22)).toBe('autossh-bastion-example-com-22');
  });

  it('puts the unit where the layout says, not where a constant says', () => {
    expect(unitPath('x', '/run/systemd/transient')).toBe('/run/systemd/transient/x.service');
  });
});

/**
 * The rung the other two cannot see. A unit whose file says one thing while the process carries the
 * previous forwards is a machine where `systemctl cat` agrees with the declaration and nothing else
 * does.
 */
describe('reading the forwards off the running process', () => {
  const CMDLINE = [
    '/usr/bin/autossh', '-M', '0', '-N',
    '-o', 'ExitOnForwardFailure=yes',
    '-i', '/home/chris/.ssh/id_ed25519',
    '-p', '10022',
    '-R', '20022:localhost:22',
    '-R', '20080:localhost:80',
    'tunnel@bastion.example.com',
  ].join('\n');

  it('reads every forward the process is carrying', () => {
    expect(parseForwards(CMDLINE)).toEqual([
      { remote: 20022, local: 22 },
      { remote: 20080, local: 80 },
    ]);
  });

  it('reads a bind address when the process has one', () => {
    expect(parseForwards('-R\n0.0.0.0:20080:localhost:80')).toEqual([
      { remote: 20080, local: 80, bind: '0.0.0.0' },
    ]);
  });

  it('answers nothing for a process that is not there', () => {
    expect(parseForwards('')).toEqual([]);
  });

  it('ignores a -R with nothing after it', () => {
    expect(parseForwards('/usr/bin/autossh\n-R')).toEqual([]);
  });

  it('ignores anything that is not a pair of ports', () => {
    expect(parseForwards('-R\nnonsense')).toEqual([]);
  });

  it('does not mistake a local forward for a reverse one', () => {
    expect(parseForwards('-L\n8080:localhost:80')).toEqual([]);
  });
});

describe('comparing what is declared against what is running', () => {
  it('agrees regardless of the order either side lists them in', () => {
    expect(sameForwards(
      [{ remote: 20080, local: 80 }, { remote: 20022, local: 22 }],
      [{ remote: 20022, local: 22 }, { remote: 20080, local: 80 }],
    )).toBe(true);
  });

  it('ignores the comment, which is documentation rather than behaviour', () => {
    expect(sameForwards([{ remote: 1, local: 2 }], [{ remote: 1, local: 2, what: 'anything' }])).toBe(true);
  });

  it('reports a forward the process is not carrying', () => {
    // the unit file was edited and nobody restarted it: this is the whole point of the third rung
    expect(sameForwards([{ remote: 20022, local: 22 }], tunnel.forwards)).toBe(false);
  });

  it('reports a forward that reaches a different near port', () => {
    expect(sameForwards([{ remote: 20022, local: 22 }], [{ remote: 20022, local: 2222 }])).toBe(false);
  });

  it('does not ignore a bind address, since the far end may honour it', () => {
    expect(sameForwards([{ remote: 1, local: 2 }], [{ remote: 1, local: 2, bind: '0.0.0.0' }])).toBe(false);
  });
});

describe('what systemd and proc say together', () => {
  const OUT = [
    'ActiveState=active',
    'NRestarts=3',
    'MainPID=1234',
    '#pulumi-homelab#cmdline',
    '-R', '20022:localhost:22',
  ].join('\n');

  it('reads the state, the restarts and the forwards apart', () => {
    expect(parseTunnel(OUT)).toEqual({
      active: true,
      restarts: 3,
      running: [{ remote: 20022, local: 22 }],
    });
  });

  it('reads properties by key rather than by position', () => {
    // systemd returns them in its own order, and this package has already had every unit read as
    // stopped by trusting the order they were asked in
    const reordered = 'NRestarts=3\nMainPID=1234\nActiveState=active\n#pulumi-homelab#cmdline\n';
    expect(parseTunnel(reordered).active).toBe(true);
    expect(parseTunnel(reordered).restarts).toBe(3);
  });

  it('reads a stopped tunnel as stopped, not as absent', () => {
    const stopped = 'ActiveState=failed\nNRestarts=97\nMainPID=0\n#pulumi-homelab#cmdline\n';
    expect(parseTunnel(stopped)).toEqual({ active: false, restarts: 97, running: [] });
  });

  it('answers zero restarts when systemd said nothing about them', () => {
    expect(parseTunnel('ActiveState=active\n#pulumi-homelab#cmdline\n').restarts).toBe(0);
  });
});

/**
 * Both of these fail silently: the unit starts, ssh exits, systemd restarts it for ever, and the
 * only trace is a rising count and a journal nobody is reading.
 */
describe('refusing to start a tunnel that cannot connect', () => {
  it('is quiet when the key is readable and the far end is trusted', () => {
    expect(preconditionRefusal({ identity: true, knownHost: true }, tunnel)).toBeNull();
  });

  it('names the account when it cannot read the key', () => {
    const refusal = preconditionRefusal({ identity: false, knownHost: true }, tunnel) ?? '';
    expect(refusal).toContain('chris');
    expect(refusal).toContain('/home/chris/.ssh/id_ed25519');
  });

  it('gives the command that fixes an untrusted host key', () => {
    // BatchMode means ssh refuses rather than prompting, so this is a tunnel that restarts for ever
    // without ever connecting
    const refusal = preconditionRefusal({ identity: true, knownHost: false }, tunnel) ?? '';
    expect(refusal).toContain('ssh-keyscan');
    expect(refusal).toContain('bastion.example.com');
    expect(refusal).toContain('-p 10022');
  });

  it('reports the key before the host key, since one is checked first', () => {
    expect(preconditionRefusal({ identity: false, knownHost: false }, tunnel)).toMatch(/cannot read/);
  });
});

describe('settling the declaration', () => {
  const minimal = { to: 'a@b', identity: '/k', runAs: 'u', forwards: [{ remote: 1, local: 2 }] };

  it('defaults the port, the restart interval and the layout', () => {
    const settled = resolveTunnel(minimal);
    expect([settled.port, settled.restartSec, settled.directory])
      .toEqual([22, 10, '/etc/systemd/system']);
  });

  it('derives the unit name when none is given, and keeps one that is', () => {
    expect(resolveTunnel(minimal).unit).toBe('autossh-b-22');
    expect(resolveTunnel({ ...minimal, unit: 'mine' }).unit).toBe('mine');
  });

  it('raises the refusals once rather than at each use', () => {
    expect(() => resolveTunnel({ ...minimal, restartSec: 1 })).toThrow(/fail2ban/);
    expect(() => resolveTunnel({ ...minimal, forwards: [] })).toThrow(/not a tunnel/);
  });

  it('is the same answer every time', () => {
    expect(resolveTunnel(minimal)).toEqual(resolveTunnel(minimal));
  });
});

/**
 * The defect that shipped in the first version: the heredoc terminator and the rest of the shell
 * command landed inside the unit file, systemd said `Missing '=', ignoring line` four times per start
 * attempt, and the tunnel never ran. The third rung caught it — the process carried no forwards —
 * but the write should not have been able to produce it.
 */
describe('installing the unit without installing a broken one', () => {
  const file = tunnelUnit(tunnel);
  const command = writeCommand('autossh-b-22', '/etc/systemd/system/autossh-b-22.service', file);

  it('never leaves the terminator sharing a line with a command', () => {
    const lines = command.split('\n');
    expect(lines.some((line) => line === 'PULUMI_EOF')).toBe(true);
    expect(lines.filter((line) => line.startsWith('PULUMI_EOF') && line !== 'PULUMI_EOF')).toEqual([]);
  });

  it('writes the unit body verbatim, with nothing of the command in it', () => {
    const body = command.split('\n').slice(1, -2).join('\n');
    expect(`${body}\n`).toBe(file);
    expect(body).not.toContain('systemctl');
    expect(body).not.toContain('install -m');
  });

  it('stages, verifies, installs, then reloads, in that order', () => {
    // the same order SudoRule uses with visudo -c: a unit systemd cannot read fails at every start
    // rather than at the write, so a candidate is checked before anything is installed
    const at = (text: string) => command.indexOf(text);
    expect(at('mktemp -d')).toBeLessThan(at('systemd-analyze verify'));
    expect(at('systemd-analyze verify')).toBeLessThan(at('install -m 0644'));
    expect(at('install -m 0644')).toBeLessThan(at('systemctl daemon-reload'));
  });

  it('writes to a private staging directory rather than over the live unit', () => {
    expect(command).toContain('mktemp -d');
    expect(command).toContain('cat > "$dir/autossh-b-22.service"');
    // the live path appears only as install's destination
    expect(command.match(/\/etc\/systemd\/system/g)).toHaveLength(1);
  });

  it('cleans the staging directory up whether or not it succeeded', () => {
    expect(command).toContain("trap 'rm -rf \"$dir\"' EXIT");
  });

  it('groups the verify, since && and || associate left to right', () => {
    // ungrouped, a failed `cat` falls through the `||` into the verify instead of stopping the chain
    expect(command).toContain('{ ! command -v systemd-analyze >/dev/null || systemd-analyze verify');
    expect(command).toContain('; }');
  });

  it('requires the verify where systemd-analyze exists and skips it where it does not', () => {
    // optional-and-silent would be no check at all; mandatory-everywhere would break a machine
    // without the tooling
    expect(command).toContain('! command -v systemd-analyze');
    expect(command).toContain('systemd-analyze verify');
  });

  it('does not enable or restart anything, which cannot follow a terminator', () => {
    expect(command).not.toContain('systemctl enable');
    expect(command).not.toContain('systemctl restart');
  });

  it('quotes the destination path', () => {
    expect(writeCommand('u', "/etc/it's.service", 'a\n')).toContain(String.raw`'/etc/it'\''s.service'`);
  });

  it('is the same string every time', () => {
    expect(writeCommand('u', '/p', 'a\n')).toBe(writeCommand('u', '/p', 'a\n'));
  });
});

describe('refusing a unit name that is not one', () => {
  it('accepts what systemd accepts', () => {
    expect(unitRefusal('autossh-b-22')).toBeNull();
    expect(unitRefusal('tunnel@host')).toBeNull();
  });

  it('refuses the suffix, which is added', () => {
    expect(unitRefusal('x.service')).toMatch(/should not carry/);
  });

  it('refuses anything that would become shell syntax in a path', () => {
    // the name reaches a shell as part of the staging path, so this is a correctness check and the
    // reason a declared name cannot inject
    for (const bad of ['a b', 'a;rm -rf /', 'a$(id)', "a'b", 'a/b', 'a`id`']) {
      expect(unitRefusal(bad), bad).not.toBeNull();
    }
  });

  it('is raised when the declaration settles, not at each use', () => {
    expect(() => resolveTunnel({
      to: 'a@b', identity: '/k', runAs: 'u', forwards: [{ remote: 1, local: 2 }], unit: 'a b',
    })).toThrow(/systemd unit name/);
  });
});
