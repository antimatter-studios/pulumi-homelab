import * as pulumi from '@pulumi/pulumi';
import { escalate, ask, must, shellQuote, type Target, describe } from '../ssh.ts';
import { providerChanged, withLegacyAlias } from '../upgrade.ts';

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
  /**
   * The login shell. **Required, with no default, and the reason is worth reading.**
   *
   * It used to default to `/usr/sbin/nologin`, which is right for the service accounts this was
   * written for and catastrophic for a person's account. `new User('admin', host, { name: 'admin',
   * groups: [...] })` reads as "describe this account" and behaved as "make it a service account" —
   * setting the login shell of the operator's own user to nologin, over the very ssh connection
   * that would be needed to undo it.
   *
   * A default is a decision made on somebody's behalf, and this is the field where the wrong one is
   * unrecoverable without physical access to the machine. So it has to be written down. Use
   * `/usr/sbin/nologin` for something that only ever runs, and the person's real shell otherwise.
   */
  shell: string;
  /** Where its home is, and whether one is made at all. */
  home?: string;
  createHome?: boolean;
  /**
   * Groups it belongs to besides its own. **This is the whole list, not additions to it.**
   *
   * `usermod -G` replaces the supplementary groups rather than adding to them, so a list of three
   * on an account that is in a dozen removes nine. A desktop or administrator account is routinely
   * in ten or more, and one of them is `sudo` — so a short list severs the connection this provider
   * works over and reports success, and the fix needs a keyboard attached to the machine.
   *
   * `-aG` is not the answer either: it makes removal impossible, so a group taken out of the list
   * stays on the machine and `read` reports drift that nothing can resolve.
   *
   * So the list is the whole truth, and anything on the machine that is not in it is refused rather
   * than removed — with the memberships named. `allowGroupRemoval` is how you say you meant it.
   */
  groups?: string[];
  /**
   * Permission to remove memberships the arguments do not mention.
   *
   * Off by default, because the failure it prevents takes the machine with it.
   */
  allowGroupRemoval?: boolean;
}

interface UserState {
  name: string;
  shell: string;
  home: string;
  createHome: boolean;
  groups: string[];
  allowGroupRemoval: boolean;
}

const DEFAULTS = { createHome: true, allowGroupRemoval: false } as const;

/**
 * Shells that mean "this account cannot log in".
 *
 * Named rather than pattern-matched: these are the four in practical use, and a list that is
 * slightly wrong is better than a regular expression that is confidently wrong about a shell
 * somebody compiled themselves.
 */
const NO_LOGIN = ['/usr/sbin/nologin', '/sbin/nologin', '/bin/false', '/usr/bin/false'];

/** Memberships the machine has that the arguments do not mention. */
export function groupsToLose(actual: string[], declared: string[]): string[] {
  return actual.filter((group) => !declared.includes(group)).sort();
}

/** What the machine says about the account, or null when there is no such user. */
export async function readUser(host: Target, name: string): Promise<Omit<UserState, 'createHome' | 'allowGroupRemoval'> | null> {
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

function providerFor(host: Target): pulumi.dynamic.ResourceProvider<UserArgs, UserState> {
  /**
   * Refuse the two changes that cannot be undone over the connection making them.
   *
   * Both are cases where the command succeeds, the resource reports success, and the machine is
   * then unreachable — so there is no later run in which to notice.
   */
  const refuseLockout = async (args: UserState): Promise<void> => {
    // the guard only applies where the connection is an ssh login as that account: a transport that
    // is not ssh has no login to take away, and asking it for one would be inventing a fact
    const connectsAs = 'user' in host ? host.user : null;
    if (args.name === connectsAs && NO_LOGIN.includes(args.shell)) {
      throw new Error(
        `${args.name} is the account this provider connects as, and the code gives it ${args.shell}. ` +
        `That would take away the login being used to apply it. Give it a real shell, or manage a different account.`,
      );
    }
    const actual = await readUser(host, args.name);
    const losing = actual ? groupsToLose(actual.groups, args.groups) : [];
    if (losing.length > 0 && !args.allowGroupRemoval) {
      throw new Error(
        `${args.name} is in ${losing.join(', ')}, which the code does not mention. ` +
        `\`groups\` is the whole list rather than additions to it, so applying this would remove ${losing.length} ` +
        `membership${losing.length === 1 ? '' : 's'}${losing.includes('sudo') ? ', including sudo' : ''}. ` +
        `Add them to the list, or set allowGroupRemoval: true if losing them is what you meant.`,
      );
    }
  };

  const settle = async (args: UserState): Promise<void> => {
    await refuseLockout(args);
    const name = shellQuote(args.name);
    const groups = args.groups.length > 0 ? `-G ${shellQuote(args.groups.join(','))}` : '';
    const home = args.home ? `-d ${shellQuote(args.home)}` : '';
    // useradd for a user that exists fails; usermod for one that does not fails too. Asking first
    // is the only way to write this once and have it be safe to run again, which every resource
    // here has to be.
    await must(host, escalate(host,
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
      return {
        id,
        props: {
          createHome: state?.createHome ?? true,
          ...state,
          allowGroupRemoval: state?.allowGroupRemoval ?? false,
          ...actual,
        },
      };
    },

    async update(id, old, args) {
      const wanted: UserState = { ...old, ...args, name: id };
      await settle(wanted);
      const actual = await readUser(host, id);
      return { outs: { ...wanted, ...actual } };
    },

    async diff(_id, old, args) {
      const groups = [...(args.groups ?? [])].sort();
      const changed = old.shell !== args.shell
        || (args.home !== undefined && old.home !== args.home)
        || old.groups.join(',') !== groups.join(',');
      return {
        changes: providerChanged(old, args)
          || changed || old.name !== args.name,
        replaces: old.name !== args.name ? ['name'] : [],
        stables: [],
        deleteBeforeReplace: true,
      };
    },

    async delete(id) {
      // no --remove: the home directory is where a service account's data lives, and a deployment
      // is not the right moment to discover that removing a user also deleted the worlds
      await must(host, escalate(host, `userdel ${shellQuote(id)} || true`));
    },
  };
}

/** An account the machine should have, checked against passwd rather than remembered. */
export class User extends pulumi.dynamic.Resource {
  declare readonly name: pulumi.Output<string>;
  declare readonly home: pulumi.Output<string>;

  constructor(name: string, host: Target, args: UserArgs, opts?: pulumi.CustomResourceOptions) {
    super(providerFor(host), name, {
      home: undefined,
      groups: [],
      createHome: true,
      allowGroupRemoval: false,
      ...args,
    }, withLegacyAlias(opts), 'homelab', 'User');
  }
}
