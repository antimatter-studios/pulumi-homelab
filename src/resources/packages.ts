import * as pulumi from '@pulumi/pulumi';
import { escalate, ask, must, shellQuote, type Target, describe } from '../ssh.ts';
import { stamped, transportChanged, withLegacyAlias } from '../upgrade.ts';

/**
 * The packages a machine should have beyond what the image came with.
 *
 * `AptPackage` models one package and is the right shape when a package is a decision — nodejs
 * because a service needs it, with a dependency edge to the service. This is the other case: the
 * list of things a person installed because they wanted them on the machine, which is an inventory
 * rather than twenty separate decisions.
 *
 * The practical difference is round trips. Twenty `AptPackage` resources ask dpkg twenty times and
 * open twenty ssh connections to do it, and on a Pi over a slow link the handshakes dominate
 * everything else. This asks once for all of them and installs the missing ones in one `apt-get`,
 * which is also what apt is good at — it resolves the whole set together rather than twenty times
 * against twenty different intermediate states.
 *
 * **It adds and it never removes.** Taking a name out of the list leaves the package installed, and
 * that is deliberate rather than lazy: `apt-get purge` removes anything depending on what it
 * removes, so purging an inventory can cascade into packages nothing here mentions, and a
 * deployment that quietly uninstalls things is a worse failure than one that leaves something
 * behind. What it leaves behind is exactly what `audit()` reports — a manually installed package
 * that nothing declares — so the machine still tells you, and a person decides.
 *
 * That is a deliberate divergence from `AptPackage`, which does purge on delete. One package is a
 * decision you can reason about; twenty is a list, and the blast radius of getting it wrong is not
 * the same.
 */
export interface AptPackagesArgs {
  /** Packages that should be installed. */
  present?: string[];
  /**
   * Packages that should **not** be installed.
   *
   * The other half of a description, and the half that was missing: without it, "not installed" is
   * something the audit can report and the code cannot state. A package somebody removed on purpose
   * three years ago is indistinguishable from one nobody ever thought about.
   *
   * Removal is the dangerous direction, so it is guarded rather than trusted. `apt-get purge` takes
   * everything depending on what it removes, so this simulates the purge first and **refuses** if
   * apt would take anything not named here, listing what it would have taken. Naming a package
   * absent is a statement about that package, not permission to remove whatever is attached to it.
   */
  absent?: string[];
  /** Accepted as a synonym for `present`, which is what it was called first. */
  names?: string[];
  /**
   * Whether to refresh the package lists first.
   *
   * Worth having on here where it is off on `AptPackage`: this resource is usually the one place a
   * machine's package list is described, so it is the natural owner of the one `apt-get update`
   * that everything else would otherwise duplicate.
   */
  update?: boolean;
}

interface AptPackagesState {
  present: string[];
  absent: string[];
  names: string[];
  update: boolean;
  /** What dpkg says is installed, name to version, for the names asked about. */
  installed: Record<string, string>;
  /** Names declared present that dpkg does not have. Empty on a machine that matches the code. */
  missing: string[];
  /** Names declared absent that dpkg still has. Empty on a machine that matches the code. */
  lingering: string[];
}

const DEFAULTS = { update: false } as const;

/**
 * `dpkg-query` output for several packages at once.
 *
 * One line per package it knows, and nothing at all for one it has never heard of — so the answer
 * to "is it installed" is the absence of a line as much as the presence of one, and both have to be
 * read. A package that is known but removed still gets a line, with a status that is not
 * `installed`, which is why the status is checked rather than assumed from the name appearing.
 */
export function parseInstalled(out: string): Record<string, string> {
  const installed: Record<string, string> = {};
  for (const line of out.split('\n')) {
    const [name = '', status = '', version = ''] = line.trim().split(/\s+/);
    if (name.length === 0) continue;
    if (status === 'installed') installed[name] = version;
  }
  return installed;
}

/** What the machine has, of the packages asked about. */
export async function readPackages(host: Target, names: string[]): Promise<Record<string, string>> {
  if (names.length === 0) return {};
  const asked = await ask(host,
    // dpkg-query exits non-zero when any name is unknown, which is an answer about that name rather
    // than a failure of the question — the packages it does know are still on stdout
    `dpkg-query -W -f='\${binary:Package} \${db:Status-Status} \${Version}\\n' ` +
    `${names.map(shellQuote).join(' ')} 2>/dev/null || true`,
  );
  return parseInstalled(asked.out);
}

/**
 * The packages `apt-get -s purge` says it would remove.
 *
 * `Remv <name> [version]`, one per line, among a great deal of other output. This is the guard on
 * the only operation here that can damage a machine: apt removes the dependents of anything it
 * removes, so a request to purge one package can quietly take a desktop environment with it. Asking
 * apt what it would do, before letting it do it, is the difference between a refusal and a
 * discovery.
 */
export function parseSimulatedRemovals(out: string): string[] {
  return out
    .split('\n')
    .map((line) => line.trim().match(/^Remv\s+(\S+)/))
    .flatMap((found) => (found?.[1] ? [found[1]] : []));
}

/** The names dpkg does not report as installed, in the order they were declared. */
export function missingFrom(names: string[], installed: Record<string, string>): string[] {
  // dpkg reports a multi-arch package as `name:arch`, and the declared name has no architecture on
  // it — so a package present only under a qualified name still counts as present
  const present = new Set(Object.keys(installed).map((name) => name.split(':')[0] ?? name));
  return names.filter((name) => !present.has(name));
}

/** Both lists, however they were spelled, and the contradiction check that has to come first. */
function wanted(args: AptPackagesArgs): { present: string[]; absent: string[]; update: boolean } {
  const present = args.present ?? args.names ?? [];
  const absent = args.absent ?? [];
  // a package in both lists is not a state the machine can be in, and resolving it either way would
  // be this resource choosing which half of the code to believe
  const both = present.filter((name) => absent.includes(name));
  if (both.length > 0) {
    throw new Error(`declared both present and absent, which is not a machine that can exist: ${both.join(', ')}`);
  }
  return { present, absent, update: args.update ?? DEFAULTS.update };
}

function providerFor(host: Target): pulumi.dynamic.ResourceProvider<AptPackagesArgs, AptPackagesState> {
  /**
   * Take the named packages off the machine, or refuse and say what apt would have taken with them.
   *
   * The simulation is the whole safety of this direction. apt removes the dependents of what it
   * removes, so `purge player` on a machine like this one could take the media stack with it — and it
   * would do so with a success exit code, because from apt's point of view it did exactly what was
   * asked. Asking first turns that into a failed deployment naming the collateral.
   */
  const purge = async (names: string[]): Promise<void> => {
    if (names.length === 0) return;
    const quoted = names.map(shellQuote).join(' ');
    const simulated = await must(host, escalate(host, `apt-get -s purge ${quoted} 2>/dev/null`));
    const collateral = parseSimulatedRemovals(simulated)
      .map((name) => name.split(':')[0] ?? name)
      .filter((name) => !names.includes(name));
    if (collateral.length > 0) {
      throw new Error(
        `refusing to purge ${names.join(', ')}: apt would also remove ${collateral.join(', ')}. ` +
        `Declaring a package absent is a statement about that package, not permission to remove what depends on it.`,
      );
    }
    await must(host, escalate(host, `DEBIAN_FRONTEND=noninteractive apt-get purge -y -qq ${quoted}`));
  };

  const settle = async (args: AptPackagesArgs): Promise<AptPackagesState> => {
    const { present, absent, update } = wanted(args);
    const before = await readPackages(host, [...present, ...absent]);
    const missing = missingFrom(present, before);
    const lingering = absent.filter((name) => !missingFrom([name], before).includes(name));

    if (missing.length > 0 || update) {
      const refresh = update ? 'apt-get update -qq && ' : '';
      // one apt-get for the whole set: apt resolves them together rather than against twenty
      // different intermediate states, and noninteractive because a package with a configuration
      // prompt otherwise hangs the deployment behind a dialogue nobody can see
      const install = missing.length > 0
        ? `DEBIAN_FRONTEND=noninteractive apt-get install -y -qq ${missing.map(shellQuote).join(' ')}`
        : 'true';
      await must(host, escalate(host, `${refresh}${install}`));
    }
    // installs first, then removals: if one list wants a package the other's removal would take as a
    // dependent, the refusal happens with the machine in the state the code asked for
    await purge(lingering);

    const installed = await readPackages(host, [...present, ...absent]);
    const stillMissing = missingFrom(present, installed);
    if (stillMissing.length > 0) {
      throw new Error(`apt said it installed them, but dpkg cannot find: ${stillMissing.join(', ')}`);
    }
    const stillThere = absent.filter((name) => !missingFrom([name], installed).includes(name));
    if (stillThere.length > 0) {
      throw new Error(`apt said it removed them, but dpkg still has: ${stillThere.join(', ')}`);
    }
    return { present, absent, names: present, update, installed, missing: [], lingering: [] };
  };

  return {
    async create(args) {
      // the id is fixed rather than derived from the names: this resource is "the list", and a list
      // that changed its identity every time somebody added a package would replace itself, which
      // for packages means purging and reinstalling the lot
      return { id: 'apt-packages', outs: await settle(args) };
    },

    async read(id, state) {
      const present = state?.present ?? state?.names ?? [];
      const absent = state?.absent ?? [];
      const installed = await readPackages(host, [...present, ...absent]);
      return {
        id,
        props: {
          update: state?.update ?? DEFAULTS.update,
          ...state,
          present,
          absent,
          names: present,
          installed,
          // a package removed by hand, or installed by hand against a declaration that it should not
          // be, comes back as drift here rather than as a resource that has gone: the list still
          // exists, it has just stopped being true
          missing: missingFrom(present, installed),
          lingering: absent.filter((name) => !missingFrom([name], installed).includes(name)),
        },
      };
    },

    async update(id, _old, args) {
      return { outs: await settle(args) };
    },

    async diff(_id, old, args) {
      const next = wanted(args);
      return {
        // drift in either direction counts: something declared present that is missing, and
        // something declared absent that is back. A name dropped from `present` is still a change to
        // the declaration, so the state stops claiming a package it no longer describes
        changes: transportChanged(old)
          || (old.missing?.length ?? 0) > 0
          || (old.lingering?.length ?? 0) > 0
          || (old.present ?? old.names ?? []).join(',') !== next.present.join(',')
          || (old.absent ?? []).join(',') !== next.absent.join(',')
          || old.update !== next.update,
        replaces: [],
        stables: [],
        deleteBeforeReplace: false,
      };
    },

    async delete() {
      // nothing, in either direction. Removing the description of a machine's package list is not a
      // request to strip the machine, and it is not a request to reinstall what was declared absent
      // either. `audit()` is what reports the packages nothing declares any more.
    },
  };
}

/** The list of packages a machine should have, checked against dpkg rather than remembered. */
export class AptPackages extends pulumi.dynamic.Resource {
  declare readonly installed: pulumi.Output<Record<string, string>>;
  declare readonly missing: pulumi.Output<string[]>;

  declare readonly lingering: pulumi.Output<string[]>;

  constructor(name: string, host: Target, args: AptPackagesArgs, opts?: pulumi.CustomResourceOptions) {
    super(stamped(providerFor(host)), name, {
      installed: undefined,
      missing: undefined,
      lingering: undefined,
      update: false,
      present: args.present ?? args.names ?? [],
      absent: args.absent ?? [],
      ...args,
    }, withLegacyAlias(opts), 'homelab', 'AptPackages');
  }
}
