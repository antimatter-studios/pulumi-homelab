import * as pulumi from '@pulumi/pulumi';
import { escalate, ask, must, shellQuote, type Target, describe } from '../ssh.ts';
import { stamped, transportChanged, withLegacyAlias } from '../upgrade.ts';

/**
 * A group, and — the actual point — its gid.
 *
 * A group looks like the most cosmetic thing a machine has until you rebuild one. Filesystem
 * permissions do not record group *names*, they record numbers: a directory owned by `storage` is
 * on disk as `1002`, and the name is only ever a lookup. So when a card dies and the array survives,
 * `groupadd storage` on the new system hands out whatever number is next free — and if that is 1003,
 * two and a bit terabytes of files are now owned by a group that does not exist.
 *
 * That failure is worse than losing the array, because it looks exactly like success. Every command
 * exits zero, the group is there, the files are there, and nothing can read them.
 *
 * So the gid is declarable, read back, and a mismatch is drift rather than something to shrug at.
 * Changing an existing group's gid is the dangerous direction and is refused by default: `groupmod
 * -g` renumbers the group and does **not** touch the files that reference the old number, which
 * orphans every one of them in the same silent way. `renumber: true` says you know, and the error
 * without it says what you would have to do afterwards.
 */
export interface GroupArgs {
  name: string;
  /**
   * The numeric id, which is what the filesystem actually stores.
   *
   * Optional, because a group whose files nothing owns does not need one pinned. Give it for any
   * group that appears in an `ls -l` you care about — that is the whole reason this resource exists.
   */
  gid?: number;
  /**
   * Permission to change the gid of a group that already exists.
   *
   * Off by default. Renumbering does not chown anything, so every file owned by the old number is
   * orphaned by a command that reports success.
   */
  renumber?: boolean;
}

interface GroupState {
  name: string;
  gid: number;
  renumber: boolean;
  /** Who is in it, as `getent` reports them. Read, never managed — membership belongs to `User`. */
  members: string[];
}

/** `storage:x:1002:admin,player` */
export function parseGroupEntry(line: string): { name: string; gid: number; members: string[] } | null {
  const fields = line.trim().split(':');
  if (fields.length < 3) return null;
  const gid = Number(fields[2]);
  if (!Number.isFinite(gid)) return null;
  return {
    name: fields[0] ?? '',
    gid,
    members: (fields[3] ?? '').split(',').filter((member) => member.length > 0),
  };
}

/** What the machine says about the group, or null where there is no such group. */
export async function readGroup(host: Target, name: string): Promise<Omit<GroupState, 'renumber'> | null> {
  const asked = await ask(host, `getent group ${shellQuote(name)}`);
  if (asked.code !== 0) return null;
  const found = parseGroupEntry(asked.out);
  return found ? { name: found.name, gid: found.gid, members: found.members } : null;
}

function providerFor(host: Target): pulumi.dynamic.ResourceProvider<GroupArgs, GroupState> {
  const settle = async (args: GroupArgs): Promise<GroupState> => {
    const existing = await readGroup(host, args.name);

    if (!existing) {
      const gid = args.gid !== undefined ? `-g ${args.gid} ` : '';
      await must(host, escalate(host, `groupadd ${gid}${shellQuote(args.name)}`));
    } else if (args.gid !== undefined && args.gid !== existing.gid) {
      if (!args.renumber) {
        throw new Error(
          `${args.name} exists with gid ${existing.gid} and the code says ${args.gid}. ` +
          `Renumbering does not chown anything, so every file owned by ${existing.gid} would be orphaned by a ` +
          `command that reports success. Set renumber: true if you mean it, and afterwards run ` +
          `\`find / -xdev -gid ${existing.gid} -exec chgrp ${args.gid} {} +\` on every filesystem that matters.`,
        );
      }
      await must(host, escalate(host, `groupmod -g ${args.gid} ${shellQuote(args.name)}`));
    }

    const actual = await readGroup(host, args.name);
    if (!actual) throw new Error(`created ${args.name} but getent cannot find it`);
    return { ...actual, renumber: args.renumber ?? false };
  };

  return {
    async create(args) {
      return { id: args.name, outs: await settle(args) };
    },

    async read(id, state) {
      const actual = await readGroup(host, id);
      // gone from the machine: Pulumi forgets it, and the next up puts it back — with its gid,
      // which is the only reason any of this is worth describing
      if (!actual) return { id: undefined, props: undefined };
      return { id, props: { renumber: state?.renumber ?? false, ...state, ...actual } };
    },

    async update(id, _old, args) {
      return { outs: await settle({ ...args, name: id }) };
    },

    async diff(_id, old, args) {
      return {
        changes: transportChanged(old)
          || old.name !== args.name || (args.gid !== undefined && old.gid !== args.gid),
        replaces: old.name !== args.name ? ['name'] : [],
        stables: [],
        deleteBeforeReplace: true,
      };
    },

    async delete() {
      // nothing. `groupdel` refuses to remove a group that is somebody's primary group, and happily
      // removes one that merely owns a filesystem — which is the case worth protecting. Forgetting
      // to describe a group is not a reason to orphan the files that reference it.
    },
  };
}

/** A group whose number is the part that matters. */
export class Group extends pulumi.dynamic.Resource {
  declare readonly name: pulumi.Output<string>;
  declare readonly gid: pulumi.Output<number>;
  declare readonly members: pulumi.Output<string[]>;

  constructor(name: string, host: Target, args: GroupArgs, opts?: pulumi.CustomResourceOptions) {
    super(stamped(providerFor(host)), name, { gid: undefined, members: undefined, renumber: false, ...args }, withLegacyAlias(opts), 'homelab', 'Group');
  }
}
