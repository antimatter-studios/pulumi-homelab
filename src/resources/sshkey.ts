import * as pulumi from '@pulumi/pulumi';
import { normaliseMode } from '../mode.ts';
import { escalate, ask, must, shellQuote, type Target, describe } from '../ssh.ts';
import { stamped, transportChanged, withLegacyAlias } from '../upgrade.ts';

/**
 * A key pair generated on the machine, whose public half is an output.
 *
 * The composition is the point: generate the key here, authorise it there — as an `AuthorizedKey`
 * on another machine, or a deploy key on a git host — and **no human ever copies a secret**. The
 * private half never leaves the machine it was made on and never appears in the state file.
 *
 * That is the difference from `RcloneRemote`, which does keep its secrets in state: there, drift
 * detection needs the plaintext to compare against. Here there is nothing to compare — the
 * fingerprint answers "is this the same key" completely — so the private key has no reason to be
 * anywhere but on disk, and is not made an output.
 *
 * **`delete` refuses by default.** A key this stack generated may be authorised somewhere it does
 * not know about — another machine's `authorized_keys`, a git host's deploy keys, a backup target —
 * and removing the private half locks that out with no way to tell in advance. Forgetting to
 * describe a key is not a reason to revoke it.
 *
 * **The two halves are owned and permissioned by the resource, and they are not treated alike.**
 * `ssh-keygen` runs under escalation, so without `owner` the pair lands root-owned and the account
 * that has to authenticate with it cannot read its own private key. The mode is not safe to leave
 * to `ssh-keygen` either: it creates `0600`, but a **default ACL on the parent directory is
 * inherited by the new file** and can widen what actually lands — `-rw-r--r--+` is what one
 * ACL-managed pool produced — and ssh refuses a private key that anyone but its owner can read. The
 * failure arrives at use time, as an authentication that does not work, with nothing wrong at
 * install time to look at. So both modes are asserted and read back: `0600` private, `0644` public,
 * because the public half is the one somebody has to copy somewhere.
 *
 * Asserting `0600` is also what disarms an inherited ACL rather than merely narrowing the mode:
 * `chmod` recomputes the mask from the group bits, and a group bit of zero suppresses every
 * inherited named entry to `#effective:---`. The entries stay listed and grant nothing, which is
 * the same mechanism a `0700` directory uses to confine what is inside it.
 */
export interface SshKeyArgs {
  /** Where the private key goes; the public key is the same path with `.pub`. */
  path: string;
  /** `ed25519` unless something at the far end cannot read it. */
  type?: 'ed25519' | 'rsa' | 'ecdsa';
  /** Bits, for the types that take them. Ignored by `ed25519`, which has one size. */
  bits?: number;
  /** What goes in the key's comment field. Defaults to the resource's name. */
  comment?: string;
  /**
   * Who owns both halves of the pair.
   *
   * **The field that decides whether the account can authenticate.** `ssh-keygen` runs with
   * escalation, so without this the key lands root-owned and the service account it was made for
   * cannot read it. Defaults to root, so nothing declared before this existed changes.
   */
  owner?: string;
  /** The group on both halves. Defaults to root. */
  group?: string;
  /** Permission to delete the private key when the resource goes. Off, and worth leaving off. */
  allowDelete?: boolean;
}

/**
 * What the private half must be, and it is not an argument.
 *
 * ssh refuses a private key that anyone but its owner can read, so every other value is one that
 * produces a key the machine will not use. A knob here would only ever be set wrong.
 */
export const PRIVATE_MODE = '0600';

/** What the public half should be. It is public, and it is the half somebody has to copy. */
export const PUBLIC_MODE = '0644';

interface SshKeyState {
  path: string;
  type: string;
  comment: string;
  owner: string;
  group: string;
  /** What the machine says the private half is, which is the half ssh is strict about. */
  privateMode: string;
  publicMode: string;
  allowDelete: boolean;
  /** The public key line, for handing to whatever should trust it. */
  publicKey: string;
  /** `SHA256:…`, which is the whole identity of a key pair and is safe to print anywhere. */
  fingerprint: string;
}

const DEFAULTS = { type: 'ed25519' as const, owner: 'root', group: 'root', allowDelete: false };

/** `256 SHA256:abc… comment (ED25519)` → the fingerprint alone. */
export function parseFingerprint(out: string): string {
  return out.trim().split(/\s+/)[1] ?? '';
}

/** What a key pair's files are, beyond the key itself. */
export interface KeyFiles {
  privateMode: string;
  publicMode: string;
  owner: string;
  group: string;
}

/** Everything the machine can say about a key pair. */
export interface KeyOnDisk extends KeyFiles {
  publicKey: string;
  fingerprint: string;
}

const FINGERPRINT_MARKER = '#pulumi-homelab#fingerprint';
const STAT_MARKER = '#pulumi-homelab#stat';

/**
 * `600|svc|svc` for each half, as `stat` prints them.
 *
 * The private half first, because that is the order the command asks in and the half that decides
 * whether ssh will use the key at all. A missing second line leaves the public fields empty rather
 * than borrowing the private one's, so an absent `.pub` reads as unknown instead of as correct.
 */
export function parseKeyFiles(out: string): KeyFiles {
  const [first = '', second = ''] = out.trim().split('\n');
  const [privateMode = '', owner = '', group = ''] = first.trim().split('|');
  const [publicMode = ''] = second.trim().split('|');
  return {
    privateMode: privateMode === '' ? '' : normaliseMode(privateMode),
    publicMode: publicMode === '' ? '' : normaliseMode(publicMode),
    owner,
    group,
  };
}

/**
 * Whether the files need correcting.
 *
 * Empty fields are skipped rather than reported: `stat` answering nothing about a half is the
 * machine declining to say, and asking for a `chmod` on a path that is not there would be a fix
 * that fails every time it is attempted.
 */
export function permissionsWrong(actual: KeyFiles, wanted: { owner: string; group: string }): boolean {
  if (actual.privateMode !== '' && actual.privateMode !== PRIVATE_MODE) return true;
  if (actual.publicMode !== '' && actual.publicMode !== PUBLIC_MODE) return true;
  if (actual.owner !== '' && actual.owner !== wanted.owner) return true;
  return actual.group !== '' && actual.group !== wanted.group;
}

/**
 * The command that makes both halves what they should be.
 *
 * The private half is narrowed **before** ownership moves. Between the two acts the file is briefly
 * described by one of them and not the other, and of the two orders this is the one where the
 * intermediate state is a key nobody new can read yet rather than one the new owner can read while
 * it is still group-readable.
 */
export function permissionsCommand(path: string, owner: string, group: string): string {
  const privateKey = shellQuote(path);
  const publicKey = shellQuote(`${path}.pub`);
  return `chmod ${PRIVATE_MODE} ${privateKey} && chmod ${PUBLIC_MODE} ${publicKey} && `
    + `chown ${shellQuote(`${owner}:${group}`)} ${privateKey} ${publicKey}`;
}

/** What is on disk, or null when there is no such key. */
export async function readSshKey(host: Target, path: string): Promise<KeyOnDisk | null> {
  const asked = await ask(host, escalate(host,
    // the private key is what decides whether the pair exists; a stray .pub without it is not a
    // key anybody can use
    `test -f ${shellQuote(path)} || exit 9; `
    + `cat ${shellQuote(`${path}.pub`)}; echo '${FINGERPRINT_MARKER}'; `
    + `ssh-keygen -lf ${shellQuote(`${path}.pub`)} 2>/dev/null || true; echo '${STAT_MARKER}'; `
    // one stat for both halves, in that order. A mode is not something ssh reports on and not
    // something a fingerprint covers, so without this the resource cannot see the failure at all
    + `stat -c '%a|%U|%G' ${shellQuote(path)} ${shellQuote(`${path}.pub`)} 2>/dev/null || true`,
  ));
  if (asked.code === 9) return null;
  if (asked.code !== 0) throw new Error(`could not read the key at ${path}: ${asked.err.trim()}`);

  const [publicKey = '', rest = ''] = asked.out.split(`${FINGERPRINT_MARKER}\n`);
  const [printed = '', stat = ''] = rest.split(`${STAT_MARKER}\n`);
  return {
    publicKey: publicKey.trim(),
    fingerprint: parseFingerprint(printed),
    ...parseKeyFiles(stat),
  };
}

function providerFor(host: Target): pulumi.dynamic.ResourceProvider<SshKeyArgs, SshKeyState> {
  const settle = async (args: SshKeyArgs, name: string): Promise<SshKeyState> => {
    const type = args.type ?? DEFAULTS.type;
    const comment = args.comment ?? name;
    const owner = args.owner ?? DEFAULTS.owner;
    const group = args.group ?? DEFAULTS.group;
    const existing = await readSshKey(host, args.path);

    if (!existing) {
      const directory = args.path.replace(/\/[^/]*$/, '') || '/';
      const bits = args.bits !== undefined && type !== 'ed25519' ? `-b ${args.bits} ` : '';
      await must(host, escalate(host,
        `mkdir -p ${shellQuote(directory)} && chmod 0700 ${shellQuote(directory)} && ` +
        // -N '' for no passphrase: a key a deployment has to unlock is a key a deployment cannot
        // use, and the protection here is the file's mode and the machine's own security
        `ssh-keygen -t ${shellQuote(type)} ${bits}-N '' -C ${shellQuote(comment)} -f ${shellQuote(args.path)} >/dev/null`,
      ));
    }

    const generated = await readSshKey(host, args.path);
    if (!generated) throw new Error(`generated a key at ${args.path} but it is not there`);
    // always after generating, and only when something is wrong otherwise: ssh-keygen's own 0600 is
    // not to be trusted where a default ACL on the parent is inherited by the new file
    if (existing === null || permissionsWrong(generated, { owner, group })) {
      await must(host, escalate(host, permissionsCommand(args.path, owner, group)));
    }

    const actual = await readSshKey(host, args.path);
    if (!actual) throw new Error(`generated a key at ${args.path} but it is not there`);
    if (permissionsWrong(actual, { owner, group })) {
      throw new Error(
        `the key at ${args.path} on ${describe(host)} is ${actual.privateMode} ${actual.owner}:${actual.group} `
        + `rather than ${PRIVATE_MODE} ${owner}:${group} — check that both the owner and the group exist`,
      );
    }
    return {
      path: args.path,
      type,
      comment,
      owner: actual.owner,
      group: actual.group,
      privateMode: actual.privateMode,
      publicMode: actual.publicMode,
      allowDelete: args.allowDelete ?? DEFAULTS.allowDelete,
      publicKey: actual.publicKey,
      fingerprint: actual.fingerprint,
    };
  };

  return {
    async create(args) {
      // the resource's own name is not available here, so the comment falls back to the path — a
      // key with no comment is one nobody can identify in an authorized_keys file a year later
      const state = await settle(args, args.path);
      return { id: args.path, outs: state };
    },

    async read(id, state) {
      const actual = await readSshKey(host, id);
      // gone from the machine: Pulumi forgets it and the next up generates a *new* key, which is
      // the honest outcome — the old one cannot be recovered and anything trusting it must be told
      if (!actual) return { id: undefined, props: undefined };
      return {
        id,
        props: {
          type: state?.type ?? DEFAULTS.type,
          comment: state?.comment ?? '',
          allowDelete: state?.allowDelete ?? DEFAULTS.allowDelete,
          ...state,
          path: id,
          publicKey: actual.publicKey,
          fingerprint: actual.fingerprint,
          // the four that always come from the machine rather than from what was remembered
          owner: actual.owner,
          group: actual.group,
          privateMode: actual.privateMode,
          publicMode: actual.publicMode,
        },
      };
    },

    async update(id, old, args) {
      // the type and comment are only ever applied at generation, so an existing key keeps them:
      // changing them would mean a new key, which is a replacement rather than an update
      const state = await settle({ ...args, path: id }, old.comment);
      return { outs: state };
    },

    async diff(_id, old, args) {
      const type = args.type ?? DEFAULTS.type;
      const wanted = { owner: args.owner ?? DEFAULTS.owner, group: args.group ?? DEFAULTS.group };
      return {
        changes: transportChanged(old)
          || old.path !== args.path
          || old.type !== type
          // a mode an inherited ACL widened is drift the fingerprint cannot see, and the symptom is
          // an authentication that fails rather than anything wrong at the path
          || permissionsWrong(old, wanted)
          || old.allowDelete !== (args.allowDelete ?? DEFAULTS.allowDelete),
        // a different path or algorithm is a different key pair, and everything trusting the old
        // one stops working — which is a replacement somebody should see in a preview
        replaces: old.path !== args.path || old.type !== type ? ['path', 'type'] : [],
        stables: [],
        deleteBeforeReplace: false,
      };
    },

    async delete(id, state) {
      if (!state.allowDelete) {
        throw new Error(
          `refusing to delete the key at ${id}: it may be authorised somewhere this stack does not ` +
          `know about — another machine, a git host, a backup target — and removing it locks that ` +
          `out with no way to tell in advance. Set allowDelete: true if you have checked, or remove ` +
          `the resource from the program and leave the key on the machine.`,
        );
      }
      await must(host, escalate(host, `rm -f ${shellQuote(id)} ${shellQuote(`${id}.pub`)}`));
    },
  };
}

/** A key pair the machine should have, whose public half can be handed to whatever should trust it. */
export class SshKey extends pulumi.dynamic.Resource {
  declare readonly publicKey: pulumi.Output<string>;
  declare readonly fingerprint: pulumi.Output<string>;

  constructor(name: string, host: Target, args: SshKeyArgs, opts?: pulumi.CustomResourceOptions) {
    super(stamped(providerFor(host)), name, {
      type: DEFAULTS.type,
      comment: name,
      owner: DEFAULTS.owner,
      group: DEFAULTS.group,
      allowDelete: DEFAULTS.allowDelete,
      publicKey: undefined,
      fingerprint: undefined,
      privateMode: undefined,
      publicMode: undefined,
      ...args,
    }, withLegacyAlias(opts), 'homelab', 'SshKey');
  }
}

/**
 * The machine's own host key fingerprints, as a report rather than a resource.
 *
 * Host keys are deliberately **not** managed. A host key is the machine's identity to every client
 * that has ever connected, and rotating one produces exactly the warning that teaches people to
 * click through host key warnings. But knowing them is worth a great deal: a machine that has been
 * reinstalled presents new ones, and that is a thing to be told about loudly rather than to
 * discover from a client that refuses to connect.
 */
export function parseHostKeys(out: string): Record<string, string> {
  const found: Record<string, string> = {};
  for (const line of out.split('\n')) {
    const [file = '', ...rest] = line.trim().split(/\s+/);
    if (file.length === 0) continue;
    const type = file.replace(/^.*ssh_host_/, '').replace(/_key\.pub$/, '');
    // `<path> 256 SHA256:… comment (ED25519)` — the fingerprint is the second word of ssh-keygen's
    // own output, which starts after the path this loop printed
    const fingerprint = rest[1] ?? '';
    if (fingerprint.length > 0) found[type] = fingerprint;
  }
  return found;
}

export async function hostKeys(host: Target): Promise<Record<string, string>> {
  const asked = await ask(host, escalate(host,
    `for pub in /etc/ssh/ssh_host_*_key.pub; do ` +
    `test -f "$pub" || continue; printf '%s ' "$pub"; ssh-keygen -lf "$pub" 2>/dev/null || echo; done`,
  ));
  if (asked.code !== 0) return {};
  return parseHostKeys(asked.out);
}
