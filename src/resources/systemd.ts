import * as pulumi from '@pulumi/pulumi';
import { normaliseMode } from '../mode.ts';
import { escalate, ask, heredoc, must, shellQuote, type Target, describe } from '../ssh.ts';
import { providerChanged, withLegacyAlias } from '../upgrade.ts';

/**
 * A systemd service: its unit file, and whether it is enabled and running.
 *
 * The unit file and the service's state are one resource rather than two because they are one
 * thought. Writing a unit without starting it leaves a machine that is configured and does nothing,
 * and the two-resource version of that is a dependency edge somebody eventually forgets to draw.
 *
 * `systemctl show` answers both halves in one call, which is what makes a real `read` possible:
 * a unit edited by hand, or a service somebody stopped last Tuesday and forgot about, both come
 * back as drift instead of sitting there invisibly.
 */
export interface SystemdUnitArgs {
  /** Without the suffix: 'aiworld', not 'aiworld.service'. */
  name: string;
  /** The whole unit file, exactly as it should appear on disk. */
  unit: string;
  /** Start at boot. */
  enabled?: boolean;
  /** Running now. */
  started?: boolean;
  /**
   * The unit file's own permissions. Octal, as everywhere else.
   *
   * Here because it used to be applied and never read: the write set `0644` on every deployment and
   * nothing ever asked what the mode actually was, so a hand-edited permission could never appear
   * as drift, and anything else that declared the same path would have re-applied a different
   * answer on alternate runs with both sides reporting success. Every write wants a read, and the
   * two have to agree on how the value is spelled.
   */
  mode?: string;
  /**
   * Which directory the unit file goes in.
   *
   * `/etc/systemd/system` is where a machine's own units belong — anything under `/lib` arrived
   * with a package and is not this provider's to edit. An argument all the same, because a layout
   * is a packaging decision and hardcoding one is how a provider becomes wrong about a machine it
   * has never met.
   */
  directory?: string;
}

interface SystemdUnitState {
  name: string;
  unit: string;
  enabled: boolean;
  started: boolean;
  mode: string;
  /**
   * What `systemctl` actually calls it, kept raw alongside the boolean.
   *
   * `enabled` collapses several answers into one, which is right for deciding what to do and wrong
   * for explaining why. `static` is the case that needs the distinction: a unit with no `[Install]`
   * section cannot be enabled at all, so a resource asking for `enabled: true` against one is not
   * drifting, it is describing something impossible — and without the raw value it would report
   * drift on every refresh for ever and try to fix it on every up.
   */
  unitFileState: string;
  directory: string;
}

const DEFAULTS = { enabled: true, started: true, mode: '0644' } as const;

const UNITS = '/etc/systemd/system';

const pathOf = (name: string, directory = UNITS) => `${directory}/${name}.service`;

/**
 * `Key=value` lines from `systemctl show`, in whatever order systemd feels like.
 *
 * Order is the whole reason this exists. `--value` prints the values alone, and systemd returns
 * them in its own order rather than the order they were requested — so `--property=A,B` can answer
 * `B`'s value first, and a positional read silently swaps them. On a machine where every unit is
 * stopped and disabled the swapped answer is identical to the right one, which is why every fixture
 * agreed with the bug and a running service was needed to expose it.
 */
export function parseShow(out: string): Record<string, string> {
  const shown: Record<string, string> = {};
  for (const line of out.split('\n')) {
    const equals = line.indexOf('=');
    if (equals <= 0) continue;
    shown[line.slice(0, equals).trim()] = line.slice(equals + 1).trim();
  }
  return shown;
}

/** What systemd and the file system say about it now, or null when there is no such unit. */
export async function readUnit(host: Target, name: string, directory = UNITS): Promise<Omit<SystemdUnitState, 'name'> | null> {
  const file = pathOf(name, directory);
  const asked = await ask(host, escalate(host,
    `test -f ${shellQuote(file)} || exit 9; ` +
    // a single line of answers first, then the file, so one round trip covers everything. An ssh
    // handshake costs far more than any of this work.
    // `Key=value`, never `--value`. systemd returns properties in its own order rather than the
    // order they were asked for, so a positional parse reads ActiveState as UnitFileState and gets
    // both answers wrong — on a machine where the unit is enabled and running, which is the only
    // machine where the two answers differ, so every fixture agrees with the bug. It also keeps an
    // empty value attached to its own key: UnitFileState is empty for a transient or generated
    // unit, and with `--value` that blank line makes the parse eat the line after it
    `systemctl show ${shellQuote(name)} --property=UnitFileState,ActiveState | tr '\\n' ' ' && ` +
    `stat -c '%a' ${shellQuote(file)} && cat ${shellQuote(file)}`,
  ));
  if (asked.code === 9) return null;
  if (asked.code !== 0) throw new Error(`could not read unit ${name}: ${asked.err.trim()}`);

  const split = asked.out.indexOf('\n');
  // `stat` prints its own newline, so the answers share the first line:
  // 'ActiveState=active UnitFileState=enabled 644'
  const first = asked.out.slice(0, split).trim().split(/\s+/);
  const shown = parseShow(first.filter((word) => word.includes('=')).join('\n'));
  const mode = first[first.length - 1] ?? '';
  const fileState = shown.UnitFileState ?? '';
  const activeState = shown.ActiveState ?? '';
  return {
    directory,
    unitFileState: fileState,
    mode: normaliseMode(mode),
    // `enabled-runtime` and `alias` are enabled for our purposes: the question is whether it comes
    // back after a reboot, and all of them do
    enabled: fileState.startsWith('enabled'),
    // `activating` counts as started, or a service still coming up would read as drift and be
    // restarted underneath itself on every deployment
    started: activeState === 'active' || activeState === 'activating',
    unit: asked.out.slice(split + 1),
  };
}

/**
 * Put the unit where it belongs and make systemd's world match the arguments — doing only what is
 * actually different.
 *
 * **An update that changes nothing must do nothing to the machine**, and for this resource that is
 * not a nicety. Every `diff` here reports a change when the serialised provider differs, so the
 * first deployment after any edit to this package updates every resource — including a comment in a
 * doc block, since the closure carries the source text of what it captures. An `apply` that always
 * restarted would turn that into every managed service restarting: on the machine this was written
 * for, a cluster dropping and a media player stopping a film somebody was watching, because somebody
 * fixed a typo.
 *
 * So each of the three acts is conditional on its own difference. The one that is not obvious is
 * the restart: a changed unit file needs one even when the service was already running, because
 * systemd would otherwise go on running the old command — which is the classic "why has my edit not
 * taken effect" afternoon.
 */
async function apply(
  host: Target,
  args: Omit<SystemdUnitState, 'unitFileState'>,
  current: Omit<SystemdUnitState, 'name'> | null,
): Promise<void> {
  const file = pathOf(args.name, args.directory);
  const unit = shellQuote(args.name);

  const rewrite = current === null || current.unit !== args.unit || current.mode !== args.mode;
  const relabel = current === null || current.enabled !== args.enabled;
  // a rewritten unit has to be restarted even when it was already running, or the process keeps
  // executing the definition it was started with
  const bounce = rewrite || current === null || current.started !== args.started;
  if (!rewrite && !relabel && !bounce) return;

  const steps = [
    ...(rewrite
      ? [
          `${heredoc(file, args.unit)}\nchmod ${args.mode} ${shellQuote(file)}`,
          // systemd caches unit files, so a changed one it has not re-read is not in effect
          'systemctl daemon-reload',
        ]
      : []),
    ...(relabel ? [`systemctl ${args.enabled ? 'enable' : 'disable'} ${unit}`] : []),
    ...(bounce ? [`systemctl ${args.started ? 'restart' : 'stop'} ${unit}`] : []),
  ];
  await must(host, escalate(host, steps.join(' && ')));
}

function providerFor(host: Target): pulumi.dynamic.ResourceProvider<SystemdUnitArgs, SystemdUnitState> {
  return {
    async create(args) {
      const wanted = { ...DEFAULTS, directory: UNITS, ...args };
      await apply(host, wanted, await readUnit(host, args.name, wanted.directory));
      const actual = await readUnit(host, args.name, wanted.directory);
      refuseStatic(args.name, wanted.enabled, actual?.unitFileState ?? '');
      // both branches spelled out rather than spreading a possibly-null read: the covering spread
      // has to supply every required field, and `...actual` cannot promise that
      const outs = actual
        ? { ...wanted, ...actual, name: args.name }
        : { ...wanted, name: args.name, unitFileState: '' };
      return { id: args.name, outs };
    },

    async read(id, state) {
      const actual = await readUnit(host, id, state?.directory ?? UNITS);
      if (!actual) return { id: undefined, props: undefined };
      return { id, props: { ...state, name: id, ...actual } };
    },

    async update(id, _old, args) {
      const wanted = { ...DEFAULTS, directory: UNITS, ...args, name: id };
      // read before writing: an update caused only by this package being upgraded finds everything
      // already as it should be and touches nothing, which is what keeps a provider bump from
      // restarting every service on the machine
      await apply(host, wanted, await readUnit(host, id, wanted.directory));
      const actual = await readUnit(host, id, wanted.directory);
      refuseStatic(id, wanted.enabled, actual?.unitFileState ?? '');
      const outs = actual
        ? { ...wanted, ...actual, name: id }
        : { ...wanted, name: id, unitFileState: '' };
      return { outs };
    },

    async diff(_id, old, args) {
      const wanted = { ...DEFAULTS, directory: UNITS, ...args };
      const changed = old.unit !== wanted.unit
        || old.enabled !== wanted.enabled
        || old.started !== wanted.started
        || old.mode !== wanted.mode;
      return {
        changes: providerChanged(old, args)
          || changed || old.name !== wanted.name,
        // a renamed service is a different service: the old unit has to be stopped and removed
        // rather than left running under a name nothing describes any more
        replaces: old.name !== wanted.name ? ['name'] : [],
        stables: [],
        deleteBeforeReplace: true,
      };
    },

    async delete(id) {
      const unit = shellQuote(id);
      // `|| true` on the stop: a unit that is already dead is not a failure to delete, and a
      // deployment that cannot tidy up after a service that crashed is worse than useless
      await must(host, escalate(host,
        `systemctl disable --now ${unit} || true; ` +
        `rm -f ${shellQuote(pathOf(id))} && systemctl daemon-reload`,
      ));
    },
  };
}

/**
 * Stop rather than report drift for ever on a unit that cannot be enabled.
 *
 * `static` means the unit file has no `[Install]` section, so `systemctl enable` has nothing to link
 * and the unit can only ever be pulled in by something else's dependencies. Asking for
 * `enabled: true` against one is a description of a machine that cannot exist. Silently reporting it
 * as drift would mean a refresh that always disagrees and an up that always tries and always fails.
 */
function refuseStatic(name: string, wantEnabled: boolean, unitFileState: string): void {
  if (wantEnabled && unitFileState === 'static') {
    throw new Error(
      `${name} is a static unit: it has no [Install] section, so it cannot be enabled. ` +
      `Describe the unit that pulls it in, or set enabled: false. If you only need the file managed, ` +
      `a ManagedFile at ${pathOf(name)} with reloadSystemd: true does that and ` +
      `still tells systemd about the change.`,
    );
  }
}

/** A service that should exist, be enabled and be running — and say so honestly when it is not. */
export class SystemdUnit extends pulumi.dynamic.Resource {
  declare readonly name: pulumi.Output<string>;
  declare readonly enabled: pulumi.Output<boolean>;
  declare readonly started: pulumi.Output<boolean>;
  declare readonly mode: pulumi.Output<string>;

  constructor(name: string, host: Target, args: SystemdUnitArgs, opts?: pulumi.CustomResourceOptions) {
    super(providerFor(host), name, { ...DEFAULTS, directory: UNITS, ...args }, withLegacyAlias(opts), 'homelab', 'SystemdUnit');
  }
}
