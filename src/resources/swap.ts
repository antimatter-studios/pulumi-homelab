import * as pulumi from '@pulumi/pulumi';
import { escalate, ask, must, shellQuote, type Target, describe } from '../ssh.ts';
import { stamped, transportChanged, withLegacyAlias } from '../upgrade.ts';

/**
 * Whether the machine has swap, and where it comes from.
 *
 * **The design is decided by one fact: what is active now and what will be active after a reboot
 * are different questions.** `swapoff -a` empties `/proc/swaps` immediately and changes nothing
 * about the next boot, so a resource that reads only the running state reports itself correct on
 * the afternoon somebody ran it and drifted every morning afterwards — and a report that is wrong
 * every morning is one nobody reads by the second week. So the `read` answers both halves: what is
 * mounted, and what is configured to mount.
 *
 * Turning swap off therefore has to mean all of it, or it means nothing:
 *
 * - `swapoff -a` for what is running;
 * - `dphys-swapfile` disabled **and masked**, because on Raspberry Pi OS that is what provides swap,
 *   and an `apt upgrade` of the package re-enables a merely-disabled unit;
 * - swap lines in `/etc/fstab` commented rather than deleted, so the machine keeps the record of
 *   what it used to do and somebody reading the file later can see the decision rather than a gap.
 *
 * zram is **reported and not managed**. A machine using it has made a different choice about the
 * whole question, and half-managing that choice is worse than describing it accurately and leaving
 * it to whoever made it.
 */
export interface SwapArgs {
  /**
   * Whether the machine should have swap at all.
   *
   * Required rather than defaulted. `false` is the ordinary answer on a machine with enough RAM and
   * a card it would rather not wear out, and it is exactly the case that fails silently when a
   * resource asserts without reading — so it should be written down rather than arrived at.
   */
  enabled: boolean;
  /** How big, in MB, when it is on. */
  sizeMb?: number;
  /** Where the swapfile lives. */
  path?: string;
  /**
   * The unit that provides swap on this distribution.
   *
   * `dphys-swapfile` on Raspberry Pi OS and Debian derivatives. An argument because the name is a
   * fact about a distribution rather than about swap, and a machine that provides it another way
   * should be able to say so.
   */
  unit?: string;
  /** Which fstab to edit. */
  fstab?: string;
}

interface ActiveSwap {
  path: string;
  /** `file` or `partition`, as `/proc/swaps` spells it. */
  kind: string;
  sizeMb: number;
  priority: number;
}

interface SwapState {
  enabled: boolean;
  sizeMb: number | null;
  path: string;
  unit: string;
  fstabFile: string;
  /** What is swapping right now. */
  active: ActiveSwap[];
  /** What `/etc/fstab` will bring up at the next boot: the uncommented swap lines. */
  fstab: string[];
  /** `enabled`, `disabled`, `masked`, or null where the unit does not exist on this machine. */
  dphys: string | null;
  /** Reported so a machine using it is described honestly, never managed. */
  zram: boolean;
}

const DEFAULTS = { path: '/var/swap' } as const;
const DPHYS = 'dphys-swapfile';
const FSTAB = '/etc/fstab';

/** `/proc/swaps`, whose sizes are in kilobytes and whose first line is a header. */
export function parseProcSwaps(out: string): ActiveSwap[] {
  return out
    .split('\n')
    .slice(1)
    .map((line) => line.trim())
    .filter((line) => line.length > 0)
    .map((line) => {
      const [path = '', kind = '', size = '', , priority = '0'] = line.split(/\s+/);
      return {
        path,
        kind,
        // kB in the file, MB everywhere a person talks about it. Rounded rather than floored: a
        // 2048 MB swapfile reports 2097148 kB, four kilobytes short of the round number, and
        // reporting that as 2047 would be drift on a file written to the requested size
        sizeMb: Math.round(Number(size) / 1024),
        priority: Number(priority),
      };
    })
    .filter((swap) => swap.path.length > 0 && Number.isFinite(swap.sizeMb));
}

/**
 * The swap lines in `/etc/fstab` that are actually in force.
 *
 * Commented lines are skipped by looking at the first character rather than by trusting the field
 * layout: `#/var/swap none swap sw 0 0` still has `swap` in the third column, so a naive `$3 ==
 * "swap"` reports a line that has been switched off as one that is on — and this resource would
 * then try to comment out a comment, for ever.
 */
export function parseFstabSwap(out: string): string[] {
  return out
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => line.length > 0 && !line.startsWith('#'))
    .filter((line) => (line.split(/\s+/)[2] ?? '') === 'swap');
}

/** What the machine says about swap, in both tenses. */
export async function readSwap(
  host: Target,
  path: string,
  unit = DPHYS,
  fstab = FSTAB,
): Promise<Omit<SwapState, 'enabled' | 'sizeMb' | 'unit' | 'fstabFile'>> {
  const asked = await ask(host, escalate(host,
    `cat /proc/swaps; echo '#pulumi-homelab#fstab'; ` +
    `cat ${shellQuote(fstab)} 2>/dev/null; echo '#pulumi-homelab#dphys'; ` +
    // is-enabled exits non-zero for disabled and masked alike but prints the word either way, and
    // the word is the answer
    `systemctl is-enabled ${shellQuote(unit)} 2>/dev/null || true; echo '#pulumi-homelab#unit'; ` +
    `systemctl list-unit-files ${shellQuote(`${unit}.service`)} --no-legend --no-pager 2>/dev/null || true; ` +
    `echo '#pulumi-homelab#zram'; ` +
    `{ test -e /dev/zram0 || test -f /etc/systemd/zram-generator.conf; } && echo yes || echo no`,
  ));
  if (asked.code !== 0) throw new Error(`could not read swap state: ${asked.err.trim() || asked.out.trim()}`);

  const sections = asked.out.split(/^#pulumi-homelab#\w+$/m);
  const [swaps = '', mounts = '', enabled = '', known = '', zram = ''] = sections;
  return {
    path,
    active: parseProcSwaps(swaps),
    fstab: parseFstabSwap(mounts),
    // a machine that has never had the package says nothing here, and null is a different answer
    // from 'disabled': one is a choice somebody made, the other is a package that was never installed
    dphys: known.trim().length > 0 ? (enabled.trim() || 'unknown') : null,
    zram: zram.trim() === 'yes',
  };
}

/** Turn all of it off, in both tenses. */
async function disable(host: Target, state: Pick<SwapState, 'dphys'>, unit = DPHYS, fstab = FSTAB): Promise<void> {
  const steps = [
    'swapoff -a || true',
    // comment the swap lines rather than remove them: the machine keeps the record of what it used
    // to do, and a person reading fstab later sees a decision instead of an absence
    `awk 'BEGIN{OFS=FS=" "} /^[[:space:]]*#/ {print; next} $3 == "swap" {print "#" $0; next} {print}' ` +
    `${shellQuote(fstab)} > ${shellQuote(`${fstab}.pulumi`)} && mv ${shellQuote(`${fstab}.pulumi`)} ${shellQuote(fstab)}`,
  ];
  if (state.dphys !== null) {
    // masked, not merely disabled: an apt upgrade of the package re-enables a disabled unit, and
    // swap comes back on a machine whose code says it should not have any
    steps.push(`systemctl disable --now ${shellQuote(unit)} || true`, `systemctl mask ${shellQuote(unit)} || true`);
  }
  await must(host, escalate(host, steps.join('; ')));
}

/** Put a swapfile there and make it survive a reboot. */
async function enable(
  host: Target,
  path: string,
  sizeMb: number,
  dphys: string | null,
  unit = DPHYS,
  fstab = FSTAB,
): Promise<void> {
  const file = shellQuote(path);
  const directory = shellQuote(path.replace(/\/[^/]*$/, '') || '/');
  // btrfs needs the file created with copy-on-write disabled, before a single byte is written to it,
  // and compression off — and a swapfile that gets any of that wrong corrupts rather than fails.
  // Refusing it plainly is more honest than half-supporting it. Its own statement, before the chain,
  // because a guard joined with && is a guard that passes by being false
  const guard = `if [ "$(stat -f -c %T ${directory})" = btrfs ]; then ` +
    `echo "a swapfile on btrfs needs chattr +C on an empty file, which this resource does not do" >&2; exit 1; fi`;

  const steps = [
    `swapoff ${file} 2>/dev/null || true`,
    // fallocate is instant where the filesystem allows it; dd is the fallback that always works and
    // is slow enough to notice, which is why it is not the first choice
    `{ fallocate -l ${sizeMb}M ${file} || dd if=/dev/zero of=${file} bs=1M count=${sizeMb} status=none; }`,
    // before mkswap, not after: a swapfile readable by anyone is every secret the kernel paged out
    `chmod 0600 ${file}`,
    `mkswap ${file} >/dev/null`,
    `swapon ${file}`,
    // and the half that survives a reboot
    `grep -q ${file} ${shellQuote(fstab)} || printf '%s none swap sw 0 0\\n' ${file} >> ${shellQuote(fstab)}`,
  ];
  if (dphys !== null) {
    // two mechanisms both providing swap is how a machine ends up with swap nothing describes;
    // this resource owns the swapfile, so the package's version of the same job is switched off
    steps.push(`systemctl disable --now ${shellQuote(unit)} || true`, `systemctl mask ${shellQuote(unit)} || true`);
  }
  await must(host, escalate(host, `${guard}; ${steps.join(' && ')}`));
}

function providerFor(host: Target): pulumi.dynamic.ResourceProvider<SwapArgs, SwapState> {
  const settle = async (args: SwapArgs): Promise<SwapState> => {
    const path = args.path ?? DEFAULTS.path;
    const unit = args.unit ?? DPHYS;
    const fstab = args.fstab ?? FSTAB;
    const before = await readSwap(host, path, unit, fstab);
    // an update that changes nothing must do nothing. Without this, a deployment caused only by
    // this package being upgraded runs `swapoff -a` and rewrites /etc/fstab on a machine that was
    // already exactly as the code describes it
    const alreadyOff = before.active.length === 0 && before.fstab.length === 0
      && (before.dphys === null || before.dphys === 'masked');
    const alreadyOn = before.active.some((swap) => swap.path === path)
      && before.fstab.length > 0
      && (args.sizeMb === undefined || before.active.some((swap) => swap.path === path && swap.sizeMb === args.sizeMb));

    if (args.enabled && !alreadyOn) {
      if (args.sizeMb === undefined) throw new Error('swap that is enabled needs a sizeMb');
      await enable(host, path, args.sizeMb, before.dphys, unit, fstab);
    } else if (!args.enabled && !alreadyOff) {
      await disable(host, before, unit, fstab);
    }
    const after = await readSwap(host, path, unit, fstab);
    return { ...after, unit, fstabFile: fstab, enabled: args.enabled, sizeMb: args.sizeMb ?? null };
  };

  return {
    async create(args) {
      const state = await settle(args);
      return { id: state.path, outs: state };
    },

    async read(id, state) {
      const actual = await readSwap(host, id, state?.unit ?? DPHYS, state?.fstabFile ?? FSTAB);
      // both tenses, and they have to agree before this reports itself satisfied: swap that is off
      // now but configured to come back at the next boot is not swap that is off
      const off = actual.active.length === 0 && actual.fstab.length === 0;
      return {
        id,
        props: {
          enabled: state?.enabled ?? !off,
          sizeMb: state?.sizeMb ?? (actual.active[0]?.sizeMb ?? null),
          ...actual,
          unit: state?.unit ?? DPHYS,
          fstabFile: state?.fstabFile ?? FSTAB,
        },
      };
    },

    async update(id, _old, args) {
      return { outs: await settle({ ...args, path: args.path ?? id }) };
    },

    async diff(_id, old, args) {
      const path = args.path ?? DEFAULTS.path;
      const activeNow = old.active.length > 0;
      const comingBack = old.fstab.length > 0 || old.dphys === 'enabled';
      // the comparison is against the machine in both tenses rather than against the last arguments:
      // this is the resource where 'what I asked for last time' is the least useful thing to know
      const satisfied = args.enabled
        ? activeNow && old.active.some((swap) => swap.path === path)
        : !activeNow && !comingBack;
      return {
        changes: transportChanged(old)
          || !satisfied || old.enabled !== args.enabled || old.path !== path
          || (args.enabled && args.sizeMb !== undefined && old.sizeMb !== args.sizeMb),
        replaces: old.path !== path ? ['path'] : [],
        stables: [],
        deleteBeforeReplace: false,
      };
    },

    async delete(id) {
      // deleting the resource means this program stops describing swap, not that the machine should
      // lose the swap it is using. The file stays and so does the fstab line; unmasking the package
      // is the one thing worth undoing, since masking it was this resource's doing and would
      // otherwise outlive the code that explains it
      await must(host, escalate(host, `systemctl unmask ${DPHYS} 2>/dev/null || true; test -n ${shellQuote(id)}`));
    },
  };
}

/** Whether the machine swaps, checked in both tenses rather than remembered. */
export class Swap extends pulumi.dynamic.Resource {
  declare readonly active: pulumi.Output<ActiveSwap[]>;
  declare readonly fstab: pulumi.Output<string[]>;
  declare readonly zram: pulumi.Output<boolean>;

  constructor(name: string, host: Target, args: SwapArgs, opts?: pulumi.CustomResourceOptions) {
    super(stamped(providerFor(host)), name, {
      active: undefined,
      fstab: undefined,
      dphys: undefined,
      zram: undefined,
      sizeMb: null,
      path: DEFAULTS.path,
      unit: DPHYS,
      fstabFile: FSTAB,
      ...args,
    }, withLegacyAlias(opts), 'homelab', 'Swap');
  }
}
