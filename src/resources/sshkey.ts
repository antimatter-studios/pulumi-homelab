import * as pulumi from '@pulumi/pulumi';
import { escalate, ask, must, shellQuote, type Target, describe } from '../ssh.ts';
import { providerChanged, withLegacyAlias } from '../upgrade.ts';

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
  /** Permission to delete the private key when the resource goes. Off, and worth leaving off. */
  allowDelete?: boolean;
}

interface SshKeyState {
  path: string;
  type: string;
  comment: string;
  allowDelete: boolean;
  /** The public key line, for handing to whatever should trust it. */
  publicKey: string;
  /** `SHA256:…`, which is the whole identity of a key pair and is safe to print anywhere. */
  fingerprint: string;
}

const DEFAULTS = { type: 'ed25519' as const, allowDelete: false };

/** `256 SHA256:abc… comment (ED25519)` → the fingerprint alone. */
export function parseFingerprint(out: string): string {
  return out.trim().split(/\s+/)[1] ?? '';
}

/** What is on disk, or null when there is no such key. */
export async function readSshKey(
  host: Target,
  path: string,
): Promise<{ publicKey: string; fingerprint: string } | null> {
  const asked = await ask(host, escalate(host,
    // the private key is what decides whether the pair exists; a stray .pub without it is not a
    // key anybody can use
    `test -f ${shellQuote(path)} || exit 9; ` +
    `cat ${shellQuote(`${path}.pub`)}; echo '#pulumi-homelab#'; ` +
    `ssh-keygen -lf ${shellQuote(`${path}.pub`)} 2>/dev/null || true`,
  ));
  if (asked.code === 9) return null;
  if (asked.code !== 0) throw new Error(`could not read the key at ${path}: ${asked.err.trim()}`);

  const [publicKey = '', printed = ''] = asked.out.split('#pulumi-homelab#\n');
  return { publicKey: publicKey.trim(), fingerprint: parseFingerprint(printed) };
}

function providerFor(host: Target): pulumi.dynamic.ResourceProvider<SshKeyArgs, SshKeyState> {
  const settle = async (args: SshKeyArgs, name: string): Promise<SshKeyState> => {
    const type = args.type ?? DEFAULTS.type;
    const comment = args.comment ?? name;
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

    const actual = await readSshKey(host, args.path);
    if (!actual) throw new Error(`generated a key at ${args.path} but it is not there`);
    return {
      path: args.path,
      type,
      comment,
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
      return {
        changes: providerChanged(old, args)
          || old.path !== args.path
          || old.type !== type
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
    super(providerFor(host), name, {
      type: DEFAULTS.type,
      comment: name,
      allowDelete: DEFAULTS.allowDelete,
      publicKey: undefined,
      fingerprint: undefined,
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
export async function hostKeys(host: Target): Promise<Record<string, string>> {
  const asked = await ask(host, escalate(host,
    `for pub in /etc/ssh/ssh_host_*_key.pub; do ` +
    `test -f "$pub" || continue; printf '%s ' "$pub"; ssh-keygen -lf "$pub" 2>/dev/null || echo; done`,
  ));
  if (asked.code !== 0) return {};
  const found: Record<string, string> = {};
  for (const line of asked.out.split('\n')) {
    const [file = '', ...rest] = line.trim().split(/\s+/);
    if (file.length === 0) continue;
    const type = file.replace(/^.*ssh_host_/, '').replace(/_key\.pub$/, '');
    const fingerprint = rest[1] ?? '';
    if (fingerprint.length > 0) found[type] = fingerprint;
  }
  return found;
}
