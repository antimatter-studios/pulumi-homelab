import * as pulumi from '@pulumi/pulumi';
import { escalate, ask, must, shellQuote, type Target, describe } from '../ssh.ts';
import { stamped, transportChanged, withLegacyAlias } from '../upgrade.ts';

/**
 * An instance of a systemd template unit — `avahi-alias@photos.example.local`.
 *
 * The first resource here that owns no file, and the reason it cannot be a `SystemdUnit`: an
 * instance has no unit file of its own. `avahi-alias@.service` is the only file on disk, and
 * `avahi-alias@photos.example.local` exists as a name systemd resolves against that template, plus
 * a symlink in `multi-user.target.wants` if it is enabled. `SystemdUnit` writes a file and reads it
 * back, so there is nothing for it to hold.
 *
 * Expressing this with a `Symlink` works and describes the mechanism instead of the intent — and
 * more importantly it can only say half of it. **A symlink is a statement about the next boot, not
 * about now.** An instance can be enabled and not running, which on a real machine looked like
 * this:
 *
 * ```
 * example.local          enabled  active     (started by hand, before any of this owned it)
 * photos.example.local   enabled  inactive   (declared correctly, and not resolving on the network)
 * ```
 *
 * The second line is the failure this whole repository exists to prevent, arriving from the other
 * direction: the resource reported success, the state file agreed, and the thing that was asked for
 * did not work. So both questions are asked — `is-enabled` for the next boot and `is-active` for
 * now — and neither is inferred from the other.
 */
export interface SystemdInstanceArgs {
  /** The unit before the `@`: `avahi-alias`, not `avahi-alias@.service`. */
  template: string;
  /** What `%i` becomes. Given raw — escaping is done here, so pass `mnt/data`, not `mnt-data`. */
  instance: string;
  /** Start at boot. */
  enabled?: boolean;
  /** Running now. */
  started?: boolean;
  /** `.service` unless it is a template of some other kind. */
  suffix?: string;
}

interface SystemdInstanceState {
  template: string;
  instance: string;
  enabled: boolean;
  started: boolean;
  suffix: string;
  /** The full name systemd knows it by, which is also the id. */
  unit: string;
}

const DEFAULTS = { enabled: true, started: true, suffix: 'service' } as const;

/**
 * An instance name, escaped the way `systemd-escape` escapes it.
 *
 * Done here rather than by calling `systemd-escape` on the machine, so the name is known before a
 * connection is opened and the same string is produced by every part of this resource. The rules
 * are systemd's: alphanumerics and `:_.` survive, `/` becomes `-` (which is why a path-like
 * instance cannot simply be passed through), everything else becomes `\\xNN`, and a leading dot is
 * escaped because a unit file starting with one would be hidden.
 *
 * Pass the raw value. `photos.example.local` comes through unchanged, so the common case costs
 * nothing, but an already-escaped name would be escaped again — the backslash is not a survivor.
 */
export function escapeInstance(instance: string): string {
  const escaped = [...instance]
    .map((character) => {
      if (/[A-Za-z0-9:_.]/.test(character)) return character;
      if (character === '/') return '-';
      return [...new TextEncoder().encode(character)]
        .map((byte) => `\\x${byte.toString(16).padStart(2, '0')}`)
        .join('');
    })
    .join('');
  // a unit whose name begins with a dot is a hidden file, which systemd will not load
  return escaped.startsWith('.') ? `\\x2e${escaped.slice(1)}` : escaped;
}

/** The name systemd knows the instance by. */
export function instanceUnit(template: string, instance: string, suffix: string = DEFAULTS.suffix): string {
  return `${template}@${escapeInstance(instance)}.${suffix}`;
}

/** What systemd says about the instance, or null when the template is not there at all. */
export async function readInstance(
  host: Target,
  template: string,
  unit: string,
  suffix: string = DEFAULTS.suffix,
): Promise<{ enabled: boolean; started: boolean } | null> {
  const asked = await ask(host, escalate(host,
    // the template first, and separately: `is-enabled` on an instance of a template that does not
    // exist says `Failed to get unit file state … No such file or directory`, which is a different
    // problem from being disabled and deserves a different message
    `systemctl cat ${shellQuote(`${template}@.${suffix}`)} >/dev/null 2>&1 || exit 9; ` +
    // both answers, and neither inferred from the other: an instance can be enabled and stopped,
    // which is a machine that will work after a reboot and does not work now
    `systemctl is-enabled ${shellQuote(unit)} 2>/dev/null || true; echo '#pulumi-homelab#'; ` +
    `systemctl is-active ${shellQuote(unit)} 2>/dev/null || true`,
  ));
  if (asked.code === 9) return null;
  if (asked.code !== 0) throw new Error(`could not read ${unit}: ${asked.err.trim()}`);

  const [enabled = '', active = ''] = asked.out.split('#pulumi-homelab#');
  return {
    // `enabled-runtime` and `alias` count, on the same reasoning as SystemdUnit: the question is
    // whether it comes back after a reboot
    enabled: enabled.trim().startsWith('enabled'),
    // `activating` counts as started, or a service still coming up reads as drift and gets
    // restarted underneath itself on every deployment
    started: active.trim() === 'active' || active.trim() === 'activating',
  };
}

function providerFor(host: Target): pulumi.dynamic.ResourceProvider<SystemdInstanceArgs, SystemdInstanceState> {
  const settle = async (args: SystemdInstanceArgs): Promise<SystemdInstanceState> => {
    const suffix = args.suffix ?? DEFAULTS.suffix;
    const unit = instanceUnit(args.template, args.instance, suffix);
    const wanted = {
      enabled: args.enabled ?? DEFAULTS.enabled,
      started: args.started ?? DEFAULTS.started,
    };

    const current = await readInstance(host, args.template, unit, suffix);
    if (!current) {
      throw new Error(
        `there is no template ${args.template}@.${suffix} on ${describe(host)}, so ${unit} cannot exist. ` +
        `Declare the file that defines it — a ManagedFile with reloadSystemd, or a SystemdUnit — and depend on it.`,
      );
    }

    // an update that changes nothing does nothing: this resource updates whenever the provider is
    // upgraded, and enabling something already enabled is noise at best
    const steps = [
      ...(current.enabled !== wanted.enabled
        ? [`systemctl ${wanted.enabled ? 'enable' : 'disable'} ${shellQuote(unit)}`] : []),
      ...(current.started !== wanted.started
        ? [`systemctl ${wanted.started ? 'start' : 'stop'} ${shellQuote(unit)}`] : []),
    ];
    if (steps.length > 0) await must(host, escalate(host, steps.join(' && ')));

    const actual = await readInstance(host, args.template, unit, suffix);
    if (!actual) throw new Error(`${unit} vanished while it was being configured`);
    return { template: args.template, instance: args.instance, suffix, unit, ...actual };
  };

  return {
    async create(args) {
      const state = await settle(args);
      return { id: state.unit, outs: state };
    },

    async read(id, state) {
      const template = state?.template ?? id.split('@')[0] ?? '';
      const suffix = state?.suffix ?? DEFAULTS.suffix;
      const actual = await readInstance(host, template, id, suffix);
      // the template is gone, or somebody disabled and stopped it: either way there is nothing on
      // the machine that this describes, so Pulumi forgets it and the next up puts it back
      if (!actual || (!actual.enabled && !actual.started)) return { id: undefined, props: undefined };
      return {
        id,
        props: {
          template,
          instance: state?.instance ?? '',
          suffix,
          ...state,
          unit: id,
          ...actual,
        },
      };
    },

    async update(_id, _old, args) {
      return { outs: await settle(args) };
    },

    async diff(_id, old, args) {
      const unit = instanceUnit(args.template, args.instance, args.suffix ?? DEFAULTS.suffix);
      return {
        changes: transportChanged(old)
          || old.unit !== unit
          || old.enabled !== (args.enabled ?? DEFAULTS.enabled)
          || old.started !== (args.started ?? DEFAULTS.started),
        // a different template or instance is a different unit entirely; nothing about the old one
        // carries over, and leaving it enabled would leave a service nothing describes
        replaces: old.unit !== unit ? ['template', 'instance'] : [],
        stables: [],
        deleteBeforeReplace: true,
      };
    },

    async delete(id) {
      // one of the few deletes here that is genuinely complete: an instance leaves nothing behind
      // once it is disabled and stopped, because it never had a file of its own
      await must(host, escalate(host, `systemctl disable --now ${shellQuote(id)} || true`));
    },
  };
}

/** An instance of a template unit, asked about in both tenses. */
export class SystemdInstance extends pulumi.dynamic.Resource {
  declare readonly unit: pulumi.Output<string>;
  declare readonly enabled: pulumi.Output<boolean>;
  declare readonly started: pulumi.Output<boolean>;

  constructor(name: string, host: Target, args: SystemdInstanceArgs, opts?: pulumi.CustomResourceOptions) {
    super(stamped(providerFor(host)), name, { unit: undefined, ...DEFAULTS, ...args }, withLegacyAlias(opts), 'homelab', 'SystemdInstance');
  }
}
