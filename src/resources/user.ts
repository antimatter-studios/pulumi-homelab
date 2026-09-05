import * as pulumi from '@pulumi/pulumi';
import { asRoot, ask, must, shellQuote, type Host } from '../ssh';

/**
 * A system account for something that runs.
 *
 * Only the parts worth describing: whether it exists, what shell it has, which groups it is in, and
 * whether it owns a home directory. Not the password, not the uid — a password belongs in a secret
 * store rather than a config file, and pinning a uid turns every machine into a special case for a
 * number nobody reads.
 *
 * Deleting one deliberately leaves the home directory behind. A service account's home is where its
 * data lives, and a deployment that removes a user should not be the thing that quietly destroys
 * the worlds it was keeping.
 */
export interface UserArgs {
  name: string;
  /** A login shell for a person; `/usr/sbin/nologin` for something that only ever runs. */
  shell?: string;
  /** Where its home is, and whether one is made at all. */
  home?: string;
  createHome?: boolean;
  /** Groups it belongs to besides its own. */
  groups?: string[];
}

interface UserState {
  name: string;
  shell: string;
  home: string;
  createHome: boolean;
  groups: string[];
}

const DEFAULTS = { shell: '/usr/sbin/nologin', createHome: true } as const;

/** What the machine says about the account, or null when there is no such user. */
export async function readUser(host: Host, name: string): Promise<Omit<UserState, 'createHome'> | null> {
  const asked = await ask(host, `getent passwd ${shellQuote(name)} && id -nG ${shellQuote(name)}`);
  if (asked.code !== 0) return null;

  const [passwd = '', memberships = ''] = asked.out.trim().split('\n');
  // name:x:uid:gid:gecos:home:shell — the two fields worth describing are the last two
  const fields = passwd.split(':');
  return {
    name,
    home: fields[5] ?? '',
    shell: fields[6] ?? '',
    // `id -nG` includes the user's own primary group, which nobody writes in a groups list and
    // which would otherwise show up as drift on the very first refresh
    groups: memberships.split(/\s+/).filter((g) => g.length > 0 && g !== name).sort(),
  };
}

function providerFor(host: Host): pulumi.dynamic.ResourceProvider<UserArgs, UserState> {
  const settle = async (args: UserState): Promise<void> => {
    const name = shellQuote(args.name);
    const groups = args.groups.length > 0 ? `-G ${shellQuote(args.groups.join(','))}` : '';
    const home = args.home ? `-d ${shellQuote(args.home)}` : '';
    // useradd for a user that exists fails; usermod for one that does not fails too. Asking first
    // is the only way to write this once and have it be safe to run again, which every resource
    // here has to be.
    await must(host, asRoot(
      `if getent passwd ${name} >/dev/null; then ` +
      `usermod -s ${shellQuote(args.shell)} ${home} ${groups} ${name}; ` +
      `else ` +
      `useradd --system ${args.createHome ? '-m' : '-M'} -s ${shellQuote(args.shell)} ${home} ${groups} ${name}; ` +
      `fi`,
    ));
  };

  return {
    async create(args) {
      const wanted: UserState = { ...DEFAULTS, home: `/home/${args.name}`, groups: [], ...args };
      await settle(wanted);
      const actual = await readUser(host, args.name);
      return { id: args.name, outs: { ...wanted, ...actual } };
    },

    async read(id, state) {
      const actual = await readUser(host, id);
      if (!actual) return { id: undefined, props: undefined };
      return { id, props: { createHome: state?.createHome ?? true, ...state, ...actual } };
    },

    async update(id, old, args) {
      const wanted: UserState = { ...old, ...args, name: id };
      await settle(wanted);
      const actual = await readUser(host, id);
      return { outs: { ...wanted, ...actual } };
    },

    async diff(_id, old, args) {
      const groups = [...(args.groups ?? [])].sort();
      const changed = old.shell !== (args.shell ?? DEFAULTS.shell)
        || (args.home !== undefined && old.home !== args.home)
        || old.groups.join(',') !== groups.join(',');
      return {
        changes: changed || old.name !== args.name,
        replaces: old.name !== args.name ? ['name'] : [],
        stables: [],
        deleteBeforeReplace: true,
      };
    },

    async delete(id) {
      // no --remove: the home directory is where a service account's data lives, and a deployment
      // is not the right moment to discover that removing a user also deleted the worlds
      await must(host, asRoot(`userdel ${shellQuote(id)} || true`));
    },
  };
}

/** An account the machine should have, checked against passwd rather than remembered. */
export class User extends pulumi.dynamic.Resource {
  declare readonly name: pulumi.Output<string>;
  declare readonly home: pulumi.Output<string>;

  constructor(name: string, host: Host, args: UserArgs, opts?: pulumi.CustomResourceOptions) {
    super(providerFor(host), name, { shell: DEFAULTS.shell, home: undefined, groups: [], createHome: true, ...args }, opts);
  }
}
