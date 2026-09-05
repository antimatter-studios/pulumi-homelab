import * as pulumi from '@pulumi/pulumi';
import { asRoot, ask, heredoc, must, shellQuote, type Host } from '../ssh';

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
}

interface SystemdUnitState {
  name: string;
  unit: string;
  enabled: boolean;
  started: boolean;
}

const DEFAULTS = { enabled: true, started: true } as const;

const pathOf = (name: string) => `/etc/systemd/system/${name}.service`;

/** What systemd and the file system say about it now, or null when there is no such unit. */
export async function readUnit(host: Host, name: string): Promise<Omit<SystemdUnitState, 'name'> | null> {
  const file = pathOf(name);
  const asked = await ask(host, asRoot(
    `test -f ${shellQuote(file)} || exit 9; ` +
    // a single line of answers first, then the file, so one round trip covers everything. An ssh
    // handshake costs far more than any of this work.
    `systemctl show ${shellQuote(name)} --property=UnitFileState,ActiveState --value | tr '\\n' ' ' && echo && cat ${shellQuote(file)}`,
  ));
  if (asked.code === 9) return null;
  if (asked.code !== 0) throw new Error(`could not read unit ${name}: ${asked.err.trim()}`);

  const split = asked.out.indexOf('\n');
  const [fileState = '', activeState = ''] = asked.out.slice(0, split).trim().split(/\s+/);
  return {
    // `enabled-runtime` and `alias` are enabled for our purposes: the question is whether it comes
    // back after a reboot, and all of them do
    enabled: fileState.startsWith('enabled'),
    // `activating` counts as started, or a service still coming up would read as drift and be
    // restarted underneath itself on every deployment
    started: activeState === 'active' || activeState === 'activating',
    unit: asked.out.slice(split + 1),
  };
}

/** Put the unit where it belongs and make systemd's world match the arguments. */
async function apply(host: Host, args: SystemdUnitState): Promise<void> {
  const file = pathOf(args.name);
  const unit = shellQuote(args.name);
  await must(host, asRoot(
    `${heredoc(file, args.unit)}\n` +
    `chmod 0644 ${shellQuote(file)} && ` +
    // always reload: systemd caches unit files, and a changed one it has not re-read is the classic
    // "why is it still running the old command" afternoon
    `systemctl daemon-reload && ` +
    `systemctl ${args.enabled ? 'enable' : 'disable'} ${unit} && ` +
    // restart rather than start, so an edited unit actually takes effect rather than being written
    // to disk and ignored by the process already running
    `systemctl ${args.started ? 'restart' : 'stop'} ${unit}`,
  ));
}

function providerFor(host: Host): pulumi.dynamic.ResourceProvider<SystemdUnitArgs, SystemdUnitState> {
  return {
    async create(args) {
      const wanted = { ...DEFAULTS, ...args };
      await apply(host, wanted);
      return { id: args.name, outs: wanted };
    },

    async read(id, state) {
      const actual = await readUnit(host, id);
      if (!actual) return { id: undefined, props: undefined };
      return { id, props: { ...state, name: id, ...actual } };
    },

    async update(id, _old, args) {
      const wanted = { ...DEFAULTS, ...args, name: id };
      await apply(host, wanted);
      return { outs: wanted };
    },

    async diff(_id, old, args) {
      const wanted = { ...DEFAULTS, ...args };
      const changed = old.unit !== wanted.unit
        || old.enabled !== wanted.enabled
        || old.started !== wanted.started;
      return {
        changes: changed || old.name !== wanted.name,
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
      await must(host, asRoot(
        `systemctl disable --now ${unit} || true; ` +
        `rm -f ${shellQuote(pathOf(id))} && systemctl daemon-reload`,
      ));
    },
  };
}

/** A service that should exist, be enabled and be running — and say so honestly when it is not. */
export class SystemdUnit extends pulumi.dynamic.Resource {
  declare readonly name: pulumi.Output<string>;
  declare readonly enabled: pulumi.Output<boolean>;
  declare readonly started: pulumi.Output<boolean>;

  constructor(name: string, host: Host, args: SystemdUnitArgs, opts?: pulumi.CustomResourceOptions) {
    super(providerFor(host), name, { ...DEFAULTS, ...args }, opts);
  }
}
