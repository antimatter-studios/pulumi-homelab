import * as pulumi from '@pulumi/pulumi';
import { escalate, ask, heredocInto, must, shellQuote, type Target, describe } from '../ssh.ts';
import { stamped, transportChanged, withLegacyAlias } from '../upgrade.ts';
import { parseShow } from './systemd.ts';

/** One port published on the far end, carried back to a port here. */
export interface Forward {
  /** The port to open on the bastion. */
  remote: number;
  /** The port here that it reaches. */
  local: number;
  /**
   * What the bastion binds to, if not its own loopback.
   *
   * **Whether this is honoured is the bastion's decision, not this resource's.** `sshd` ignores a
   * bind address on a reverse forward unless `GatewayPorts` allows it, and with
   * `GatewayPorts yes` set globally a forward you expected on loopback is on the public internet
   * instead. Nothing on this side can see which, so nothing here pretends to: the value is passed
   * through and the bastion decides. Check `sshd -T | grep gatewayports` there before assuming.
   */
  bind?: string;
  /** What it is for. Becomes a comment in the unit, for whoever reads it in a year. */
  what?: string;
}

/**
 * A machine behind NAT publishing ports on a bastion it can reach.
 *
 * **One connection carrying every forward, and that is not an optimisation.** Each connection is a
 * login, and a bastion running fail2ban counts them: several tunnels reconnecting together look
 * exactly like a brute-force attempt, and a ban takes out every tunnel *and* the route needed to fix
 * them. It has happened twice on one real machine. So a declaration with three forwards produces one
 * unit and one ssh process with three `-R` flags — a resource that generated a unit per forward
 * would reproduce the ban.
 *
 * **The read has three rungs and the third is why this is a resource rather than a module.** The
 * unit file compares as text, the service reports whether it is active and how many times it has
 * restarted, and then the forwards are read from the **running process** — `-R` flags out of
 * `/proc/<pid>/cmdline`. That last one catches the failure the first two cannot: a unit whose file
 * says one thing while the process still carries the previous forwards, because nobody restarted it
 * after an edit. `systemctl cat` agrees with the declaration and the machine is doing something
 * else.
 *
 * **What it cannot read is whether the far end accepted the forward.** With
 * `ExitOnForwardFailure=yes` the process exits instead of sitting connected and forwarding nothing,
 * so a rising restart count is the only signal — reported as an output and never compared, because a
 * restart is not drift. A tunnel that flaps is neither up nor down, and a resource that called it
 * healthy would be the most useful kind of lie.
 *
 * **Changing a forward restarts the tunnel, and for the ssh forward that is the route the deployment
 * is standing on.** Nothing here can solve that. Bring the new port up as a second forward, prove
 * it, then take the old one out of the declaration — two deployments, and the first one is the one
 * that keeps you connected.
 *
 * There is deliberately no choice between autossh and plain ssh. autossh with `ServerAliveInterval`
 * is what makes this a tunnel rather than a connection that dies quietly on the first network blip,
 * and an option there would be an option to build the broken one.
 */
export interface SshTunnelArgs {
  /** `user@bastion`. */
  to: string;
  /** The bastion's ssh port, when it is not 22. */
  port?: number;
  /** The private key on *this* machine, readable by `runAs`. */
  identity: string;
  /** The account the tunnel runs as. Its `known_hosts` is what must trust the bastion. */
  runAs: string;
  /** Every port to publish, in one connection. */
  forwards: Forward[];
  /**
   * Seconds before systemd restarts a tunnel that exited. Ten by default, and under five is refused.
   *
   * **`ExitOnForwardFailure=yes` and a short `RestartSec` combine into a self-inflicted ban.** A port
   * still held by a previous connection makes ssh exit rather than forward nothing, which is
   * correct; systemd then restarts it, and a tight loop is a login attempt every second or two until
   * fail2ban notices. Ten seconds is long enough for the far end to release the port.
   */
  restartSec?: number;
  /** The unit's name, without `.service`. Derived from the destination when not given. */
  unit?: string;
  /**
   * Where unit files live.
   *
   * An argument rather than a constant because this package should not decide a machine's layout.
   */
  directory?: string;
}

interface SshTunnelState {
  to: string;
  port: number;
  identity: string;
  runAs: string;
  forwards: Forward[];
  restartSec: number;
  unit: string;
  directory: string;
  /** The unit file as it now stands. */
  file: string;
  /** What systemd says the service is doing. */
  active: boolean;
  /** The `-R` flags on the *running* process, which is the rung the other two cannot see. */
  running: Forward[];
  /**
   * How many times systemd has restarted it.
   *
   * Reported, never compared. A restart is not drift, and a rising count is the only signal that the
   * far end is refusing a forward — the one thing this resource cannot read directly.
   */
  restarts: number;
}

const UNITS = '/etc/systemd/system';
const DEFAULTS = { port: 22, restartSec: 10, directory: UNITS };

/** The shortest `RestartSec` that is not a self-inflicted ban. */
export const RESTART_FLOOR = 5;

/** Why a restart interval is refused, or null when it is not. */
export function restartRefusal(seconds: number): string | null {
  if (seconds >= RESTART_FLOOR) return null;
  return `RestartSec of ${seconds}s is too short. ExitOnForwardFailure makes ssh exit when a port is `
    + `still held by the previous connection, systemd restarts it, and a tight loop is a login `
    + `attempt every ${seconds} seconds until fail2ban bans this address — which takes out every `
    + `tunnel and the route needed to fix them. Use ${DEFAULTS.restartSec}s, or at least ${RESTART_FLOOR}s.`;
}

/** `-R [bind:]remote:localhost:local`, as ssh spells a reverse forward. */
export function forwardFlag(forward: Forward): string {
  const bind = forward.bind !== undefined && forward.bind !== '' ? `${forward.bind}:` : '';
  // localhost rather than 127.0.0.1: the far end resolves it on *this* machine, and a host with no
  // IPv4 loopback route is a thing that exists
  return `${bind}${forward.remote}:localhost:${forward.local}`;
}

/** Why a set of forwards cannot work, or null when it can. */
export function forwardsRefusal(forwards: Forward[]): string | null {
  if (forwards.length === 0) return `a tunnel with no forwards is a connection, not a tunnel`;
  const remote = forwards.map((forward) => forward.remote);
  const twice = remote.find((port, at) => remote.indexOf(port) !== at);
  if (twice !== undefined) {
    return `two forwards both want port ${twice} on the far end. The second would be refused, and `
      + `with ExitOnForwardFailure that means the whole tunnel exits — including the forwards that `
      + `were fine.`;
  }
  const bad = forwards.find((forward) => !inRange(forward.remote) || !inRange(forward.local));
  if (bad !== undefined) return `${bad.remote}:${bad.local} is not a pair of ports`;
  return null;
}

const inRange = (port: number) => Number.isInteger(port) && port > 0 && port < 65536;

/**
 * Why a unit name is refused, or null when it is usable.
 *
 * systemd takes a limited alphabet, and this name also reaches a shell as part of a path — so the
 * check is both a correctness one and the reason a declared name cannot become shell syntax.
 */
export function unitRefusal(unit: string): string | null {
  if (/^[A-Za-z0-9_@.-]+$/.test(unit) && !unit.endsWith('.service')) return null;
  if (unit.endsWith('.service')) {
    return `unit '${unit}' should not carry the .service suffix — it is added.`;
  }
  return `unit '${unit}' is not a systemd unit name: letters, digits, and _ @ . - only.`;
}

/** A unit name derived from where the tunnel goes, when one was not given. */
export function unitNameFor(to: string, port: number): string {
  const host = to.includes('@') ? to.slice(to.indexOf('@') + 1) : to;
  // systemd unit names take a limited alphabet, and a dot in one reads as a type suffix
  return `autossh-${host.replace(/[^A-Za-z0-9_-]/g, '-')}-${port}`;
}

/**
 * The `ExecStart` line, which is the whole behaviour of the tunnel in one place.
 *
 * `-M 0` turns autossh's own monitoring off on purpose: it works by opening *another* forwarded port
 * pair on the far end, which is a second thing to collide and a second thing for the bastion to
 * refuse. `ServerAliveInterval` does the same job inside the connection that already exists.
 *
 * `AUTOSSH_GATETIME=0` because autossh otherwise gives up for good if its first connection dies
 * inside thirty seconds — which is exactly what happens when a machine boots before its network is
 * up, and it turns a transient failure into a tunnel that never returns.
 */
export function execStart(args: {
  to: string;
  port: number;
  identity: string;
  forwards: Forward[];
}): string {
  return [
    '/usr/bin/autossh',
    '-M', '0',
    // no shell, no command, no tty: this connection exists to carry ports and nothing else
    '-N',
    '-o', 'ExitOnForwardFailure=yes',
    '-o', 'ServerAliveInterval=30',
    '-o', 'ServerAliveCountMax=3',
    // a tunnel cannot answer a prompt, so it must fail instead of waiting for one nobody will see
    '-o', 'BatchMode=yes',
    '-o', 'IdentitiesOnly=yes',
    '-i', args.identity,
    '-p', String(args.port),
    ...args.forwards.flatMap((forward) => ['-R', forwardFlag(forward)]),
    args.to,
  ].join(' ');
}

/** The unit file. */
export function tunnelUnit(args: {
  to: string;
  port: number;
  identity: string;
  runAs: string;
  forwards: Forward[];
  restartSec: number;
}): string {
  const described = args.forwards
    .map((forward) => `# ${forward.remote} -> ${forward.local}${forward.what ? ` (${forward.what})` : ''}`);
  return [
    '[Unit]',
    `Description=Reverse tunnel to ${args.to}`,
    ...described,
    // network-online rather than network: a tunnel started before there is a route spends its
    // AUTOSSH_GATETIME allowance failing, and Wants is what actually pulls the target in
    'After=network-online.target',
    'Wants=network-online.target',
    '',
    '[Service]',
    `User=${args.runAs}`,
    'Environment=AUTOSSH_GATETIME=0',
    `ExecStart=${execStart(args)}`,
    'Restart=always',
    `RestartSec=${args.restartSec}`,
    '',
    '[Install]',
    'WantedBy=multi-user.target',
    '',
  ].join('\n');
}

/**
 * The forwards a running process is actually carrying.
 *
 * From `/proc/<pid>/cmdline`, whose arguments are separated by NUL and are read here one per line.
 * This is the rung that catches a unit file nobody restarted: the file and the process disagree, and
 * only the process is doing anything.
 */
export function parseForwards(cmdline: string): Forward[] {
  const args = cmdline.split('\n').map((arg) => arg.trim()).filter((arg) => arg.length > 0);
  const found: Forward[] = [];
  args.forEach((arg, at) => {
    if (arg !== '-R') return;
    const spec = args[at + 1];
    if (spec === undefined) return;
    const parts = spec.split(':');
    // [bind:]remote:host:local — four parts when a bind address is present, three when not
    const [bind, remote, , local] = parts.length === 4 ? parts : [undefined, ...parts];
    const pair = { remote: Number(remote), local: Number(local) };
    if (!inRange(pair.remote) || !inRange(pair.local)) return;
    found.push(bind === undefined || bind === '' ? pair : { ...pair, bind });
  });
  return found;
}

/** Two sets of forwards compared on what they publish, in an order neither side chose. */
export function sameForwards(a: Forward[], b: Forward[]): boolean {
  const shape = (forwards: Forward[]) => forwards
    .map((forward) => forwardFlag(forward))
    .sort()
    .join(' ');
  return shape(a) === shape(b);
}

const CMDLINE_MARKER = '#pulumi-homelab#cmdline';

/** What systemd and `/proc` say, split apart again. */
export function parseTunnel(out: string): { active: boolean; restarts: number; running: Forward[] } {
  const [shown = '', cmdline = ''] = out.split(`${CMDLINE_MARKER}\n`);
  const properties = parseShow(shown);
  return {
    active: properties.ActiveState === 'active',
    restarts: Number(properties.NRestarts ?? '0') || 0,
    running: parseForwards(cmdline),
  };
}

/** Everything the arguments settle to, with the defaults applied once. */
export function resolveTunnel(args: SshTunnelArgs): Omit<SshTunnelState, 'file' | 'active' | 'running' | 'restarts'> {
  const port = args.port ?? DEFAULTS.port;
  const restartSec = args.restartSec ?? DEFAULTS.restartSec;
  const unit = args.unit ?? unitNameFor(args.to, port);
  const refusal = restartRefusal(restartSec)
    ?? forwardsRefusal(args.forwards ?? [])
    ?? unitRefusal(unit);
  if (refusal !== null) throw new Error(refusal);
  return {
    to: args.to,
    port,
    identity: args.identity,
    runAs: args.runAs,
    forwards: args.forwards,
    restartSec,
    unit,
    directory: args.directory ?? DEFAULTS.directory,
  };
}

/** The path the unit file goes to. */
export const unitPath = (unit: string, directory = UNITS) => `${directory}/${unit}.service`;

/**
 * The command that writes the unit, and does not install one systemd cannot read.
 *
 * **Staged, verified, then installed — the same order `SudoRule` uses with `visudo -c`, and for the
 * same reason.** A unit file that is syntactically wrong does not fail at the write; it fails at
 * every start attempt, with `Missing '=', ignoring line` in the journal and a service that never
 * runs. Checking a candidate first turns that into a failed deployment with systemd's own complaint
 * attached.
 *
 * Everything after the write is chained on the heredoc's **command line**, before the body. A
 * heredoc's terminator must stand alone on its line, so appending ` && …` to a finished heredoc puts
 * `PULUMI_EOF && chmod …` on one line and the rest of the command becomes file content. That
 * shipped once and produced exactly the broken unit described above.
 *
 * `systemd-analyze verify` is required where it exists and skipped where it does not, rather than
 * being made optional: `! command -v … || verify` passes on a machine without it and demands success
 * on a machine with it.
 */
export function writeCommand(unit: string, path: string, file: string): string {
  const service = `${unit}.service`;
  const staged = `"$dir/${service}"`;
  return `dir=$(mktemp -d) && trap 'rm -rf "$dir"' EXIT && `
    + heredocInto(staged, file, [
      // braced, because `&&` and `||` have equal precedence and associate left to right: ungrouped,
      // a failed `cat` would fall through the `||` into the verify rather than stopping the chain
      `{ ! command -v systemd-analyze >/dev/null || systemd-analyze verify ${staged}; }`,
      `install -m 0644 -o root -g root ${staged} ${shellQuote(path)}`,
      'systemctl daemon-reload',
    ]);
}

/** What the machine says about the tunnel, or null when the unit is not there. */
export async function readTunnel(
  host: Target,
  unit: string,
  directory = UNITS,
): Promise<{ file: string; active: boolean; restarts: number; running: Forward[] } | null> {
  const service = shellQuote(`${unit}.service`);
  const asked = await ask(host, escalate(host,
    `test -f ${shellQuote(unitPath(unit, directory))} || exit 9; `
    + `cat ${shellQuote(unitPath(unit, directory))}; echo '${CMDLINE_MARKER.replace('cmdline', 'file')}'; `
    // Key=value rather than --value: systemd returns properties in its own order, not the order
    // they were asked for, and this package has already had every unit read as stopped that way
    + `systemctl show -p ActiveState -p NRestarts -p MainPID ${service}; echo '${CMDLINE_MARKER}'; `
    // one property, so --value has no order to get wrong
    + `pid=$(systemctl show -p MainPID --value ${service} 2>/dev/null); `
    + `[ -n "$pid" ] && [ "$pid" != 0 ] && tr '\\0' '\\n' < "/proc/$pid/cmdline" 2>/dev/null || true`,
  ));
  if (asked.code === 9) return null;
  if (asked.code !== 0) throw new Error(`could not read the tunnel ${unit}: ${asked.err.trim()}`);
  const [file = '', rest = ''] = asked.out.split(`${CMDLINE_MARKER.replace('cmdline', 'file')}\n`);
  return { file, ...parseTunnel(rest) };
}

/**
 * Why the tunnel cannot come up, or null when nothing is in the way.
 *
 * Both of these fail *silently* otherwise: the unit starts, ssh exits, systemd restarts it for ever,
 * and the only trace is a rising restart count and a journal nobody is reading. Turning that into a
 * refusal with the command that fixes it is most of what this resource is for.
 */
export function preconditionRefusal(
  probe: { identity: boolean; knownHost: boolean },
  args: { to: string; port: number; identity: string; runAs: string },
): string | null {
  if (!probe.identity) {
    return `${args.runAs} cannot read ${args.identity} on this machine, so the tunnel would exit on `
      + `every start. Check the file exists and its mode and owner let ${args.runAs} read it.`;
  }
  if (!probe.knownHost) {
    const host = args.to.includes('@') ? args.to.slice(args.to.indexOf('@') + 1) : args.to;
    return `${args.runAs} does not trust ${host}'s host key, so ssh would refuse under BatchMode and `
      + `the tunnel would restart for ever without ever connecting. Add it first: `
      + `sudo -u ${args.runAs} ssh-keyscan -p ${args.port} ${host} >> ~${args.runAs}/.ssh/known_hosts`;
  }
  return null;
}

/** Whether `runAs` can read the key, and whether it already trusts the bastion. */
async function probe(
  host: Target,
  args: { to: string; port: number; identity: string; runAs: string },
): Promise<{ identity: boolean; knownHost: boolean }> {
  const far = args.to.includes('@') ? args.to.slice(args.to.indexOf('@') + 1) : args.to;
  const as = (command: string) => `sudo -n -u ${shellQuote(args.runAs)} sh -c ${shellQuote(command)}`;
  const asked = await ask(host, escalate(host,
    `${as(`test -r ${shellQuote(args.identity)}`)} && echo identity || true; `
    // -F searches known_hosts for the host, including a hashed file, which grepping cannot
    + `${as(`ssh-keygen -F ${shellQuote(args.port === 22 ? far : `[${far}]:${args.port}`)} -q`)} && echo known || true`,
  ));
  return { identity: asked.out.includes('identity'), knownHost: asked.out.includes('known') };
}

function providerFor(host: Target): pulumi.dynamic.ResourceProvider<SshTunnelArgs, SshTunnelState> {
  const settle = async (args: SshTunnelArgs): Promise<SshTunnelState> => {
    const wanted = resolveTunnel(args);
    const refusal = preconditionRefusal(await probe(host, wanted), wanted);
    if (refusal !== null) throw new Error(refusal);

    const file = tunnelUnit(wanted);
    const path = unitPath(wanted.unit, wanted.directory);
    const before = await readTunnel(host, wanted.unit, wanted.directory);
    const service = shellQuote(`${wanted.unit}.service`);

    const rewrite = before === null || before.file !== file;
    // restarted when the file changed, and also when the process is carrying forwards the file no
    // longer describes — which is the whole reason the third rung is read
    const bounce = rewrite || !before.active || !sameForwards(before.running, wanted.forwards);
    if (rewrite) {
      const wrote = await ask(host, escalate(host, writeCommand(wanted.unit, path, file)));
      if (wrote.code !== 0) {
        throw new Error(
          `could not install ${path} on ${describe(host)}: ${(wrote.err || wrote.out).trim()}\n`
          + `Nothing was installed — the unit was checked in a temporary directory first, so the `
          + `machine still has whatever it had before.`,
        );
      }
    }
    // separate from the write on purpose: enable and restart cannot be chained after a heredoc's
    // terminator, and a failed write must not be followed by a restart of the old unit
    if (rewrite || bounce) {
      await must(host, escalate(host,
        `systemctl enable ${service}${bounce ? ` && systemctl restart ${service}` : ''}`,
      ));
    }

    const after = await readTunnel(host, wanted.unit, wanted.directory);
    if (after === null) throw new Error(`wrote ${path} on ${describe(host)} but it is not there`);
    // the file is read back and compared, not assumed: a write that corrupted the file is invisible
    // to the code that composed it, and every other check here would pass on the version it meant
    if (after.file !== file) {
      throw new Error(
        `wrote ${path} on ${describe(host)} and it does not read back as written. `
        + `Something else is editing the same file, or the write itself was malformed.`,
      );
    }
    if (!sameForwards(after.running, wanted.forwards)) {
      throw new Error(
        `started ${wanted.unit} on ${describe(host)} but the running process carries `
        + `${after.running.map(forwardFlag).join(' ') || 'no forwards'} rather than `
        + `${wanted.forwards.map(forwardFlag).join(' ')}. ${after.restarts > 0
          ? `It has restarted ${after.restarts} times, which is what a forward the far end refuses looks like — `
            + `check whether something already holds those ports on ${wanted.to}.`
          : 'Check the journal for the unit.'}`,
      );
    }
    return { ...wanted, file: after.file, active: after.active, running: after.running, restarts: after.restarts };
  };

  return {
    async check(_olds, news) {
      const bad = restartRefusal(news.restartSec ?? DEFAULTS.restartSec)
        ?? forwardsRefusal(news.forwards ?? []);
      // wrong in the declaration rather than on the machine, so it belongs in a preview
      return { inputs: news, failures: bad === null ? [] : [{ property: 'forwards', reason: bad }] };
    },

    async create(args) {
      const state = await settle(args);
      return { id: state.unit, outs: state };
    },

    async read(id, state) {
      const directory = state?.directory ?? UNITS;
      const found = await readTunnel(host, id, directory);
      // no unit file: Pulumi forgets it and the next up writes it back
      if (found === null) return { id: undefined, props: undefined };
      return {
        id,
        props: {
          to: state?.to ?? '',
          port: state?.port ?? DEFAULTS.port,
          identity: state?.identity ?? '',
          runAs: state?.runAs ?? '',
          forwards: state?.forwards ?? found.running,
          restartSec: state?.restartSec ?? DEFAULTS.restartSec,
          ...state,
          unit: id,
          directory,
          // the four that come from the machine rather than from what was remembered
          file: found.file,
          active: found.active,
          running: found.running,
          restarts: found.restarts,
        },
      };
    },

    async update(id, _old, args) {
      return { outs: await settle({ ...args, unit: id }) };
    },

    async diff(_id, old, args) {
      const wanted = resolveTunnel({ ...args, unit: old.unit });
      return {
        changes: transportChanged(old)
          || old.file !== tunnelUnit(wanted)
          || old.active === false
          // the rung the other two cannot see: a unit nobody restarted after an edit
          || !sameForwards(old.running ?? [], wanted.forwards),
        // a different destination is a different tunnel, and the old unit has to go rather than be
        // left running and forwarding ports nothing describes
        replaces: old.to !== wanted.to || old.unit !== wanted.unit ? ['to', 'unit'] : [],
        stables: [],
        deleteBeforeReplace: true,
      };
    },

    async delete(id, state) {
      const path = unitPath(id, state.directory ?? UNITS);
      await must(host, escalate(host,
        `systemctl disable --now ${shellQuote(`${id}.service`)} || true; `
        + `rm -f ${shellQuote(path)} && systemctl daemon-reload`,
      ));
    },
  };
}

/** A reverse tunnel, checked against the forwards the running process is actually carrying. */
export class SshTunnel extends pulumi.dynamic.Resource {
  declare readonly unit: pulumi.Output<string>;
  /** The `-R` flags on the running process. */
  declare readonly running: pulumi.Output<Forward[]>;
  declare readonly active: pulumi.Output<boolean>;
  /** Restarts so far. Reported, never compared — a rising count is a forward the far end refuses. */
  declare readonly restarts: pulumi.Output<number>;

  constructor(name: string, host: Target, args: SshTunnelArgs, opts?: pulumi.CustomResourceOptions) {
    super(stamped(providerFor(host)), name, {
      port: DEFAULTS.port,
      restartSec: DEFAULTS.restartSec,
      directory: DEFAULTS.directory,
      unit: undefined,
      file: undefined,
      active: undefined,
      running: undefined,
      restarts: undefined,
      ...args,
    }, withLegacyAlias(opts), 'homelab', 'SshTunnel');
  }
}
