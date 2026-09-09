import * as pulumi from '@pulumi/pulumi';
import { escalate, ask, heredoc, must, shellQuote, type Target, describe } from '../ssh.ts';
import { normaliseMode } from '../mode.ts';
import { stamped, transportChanged, withLegacyAlias } from '../upgrade.ts';

/**
 * One public key, authorised for one account.
 *
 * **The key body is the identity, not the line and not the comment.** An authorized_keys line is
 * `[options] type base64 comment`, and the comment is the part people change — a laptop gets
 * renamed, a key is copied with a new label. A resource keyed on the whole line would add a second
 * copy of a key that is already there, and an account would accumulate one entry per rename.
 *
 * **The modes are load-bearing and fail silently**, which is the reason this is a resource rather
 * than a `ManagedFile` with careful content. sshd refuses to read `~/.ssh/authorized_keys` if the
 * file or the directory is group- or world-writable, and says nothing about it: the client is told
 * `Permission denied (publickey)`, which is indistinguishable from the key being wrong. So `0700`
 * on the directory and `0600` on the file are set, and read back.
 *
 * `options` is where most of the value is. A key restricted with `from="10.0.0.0/24"` and
 * `command="…"` is a different security posture from one that can do anything, and it is one field
 * at the call site.
 */
export interface AuthorizedKeyArgs {
  /** The account whose `authorized_keys` this is. It must already exist. */
  user: string;
  /** The whole public key line as ssh-keygen prints it: `ssh-ed25519 AAAA… someone@somewhere`. */
  key: string;
  /**
   * Restrictions, written before the key.
   *
   * `from="10.0.0.0/24"`, `command="…"`, `restrict`, `no-agent-forwarding`. A deploy key that can
   * only run one command from one subnet is worth the line it takes.
   */
  options?: string[];
  /** Where the file is, for an account whose home is not where `getent` says. */
  path?: string;
}

interface AuthorizedKeyState {
  user: string;
  key: string;
  options: string[];
  path: string;
  /** `0600`, read back rather than assumed — a wrong one looks exactly like a wrong key. */
  mode: string;
  /** `0700` on `~/.ssh`, which fails the same silent way. */
  directoryMode: string;
  /**
   * Who owns the file, read back because ownership fails exactly as loudly as mode does — which is
   * to say not at all. A file that is `0600` but owned by root in somebody's home is refused by
   * sshd, and the client is told `Permission denied (publickey)` either way.
   */
  owner: string;
  /** The comment on the key as it sits in the file: how a person tells one key from another. */
  comment: string;
}

const FILE_MODE = '0600';
const DIRECTORY_MODE = '0700';

/**
 * The base64 body of a public key, which is the only part that identifies it.
 *
 * `[options] type base64 comment` — options may contain spaces inside quotes, so the body is found
 * by shape rather than by position: it is the long base64 field, and there is exactly one.
 */
export function keyBody(line: string): string | null {
  const text = line.trim();
  if (text.length === 0 || text.startsWith('#')) return null;
  const found = text.match(/(?:^|\s)(AAAA[0-9A-Za-z+/=]{20,})(?:\s|$)/);
  return found?.[1] ?? null;
}

/**
 * The comment at the end of a key line, which is how a person tells one key from another.
 *
 * Never matched on — the body is the identity — but reported, because "one key present" is accurate
 * and useless when the file holds a laptop's key and a tunnel's key side by side.
 */
export function keyComment(line: string): string {
  const body = keyBody(line);
  if (body === null) return '';
  const after = line.trim().slice(line.trim().indexOf(body) + body.length);
  return after.trim();
}

/** The line as it should appear, options first. */
export function authorizedLine(key: string, options: string[] = []): string {
  const restrictions = options.length > 0 ? `${options.join(',')} ` : '';
  return `${restrictions}${key.trim()}`;
}

/**
 * Put the line in, replacing whatever line currently carries the same key body.
 *
 * Replaced rather than appended, so changing the options on a key that is already authorised does
 * not leave the unrestricted version of it in the file underneath — which would be a change that
 * appeared to tighten access and did not.
 */
export function upsertAuthorized(text: string, key: string, line: string): string {
  const body = keyBody(key);
  const lines = text.split('\n');
  const at = body === null ? -1 : lines.findIndex((existing) => keyBody(existing) === body);
  if (at >= 0) {
    lines[at] = line;
    return lines.join('\n');
  }
  const kept = text.replace(/\n+$/, '');
  return kept.length > 0 ? `${kept}\n${line}\n` : `${line}\n`;
}

/** Take out the line carrying this key, and leave every other line alone. */
export function removeAuthorized(text: string, key: string): string {
  const body = keyBody(key);
  if (body === null) return text;
  // an authorized_keys file usually has more than one owner, and the others' keys are not this
  // resource's to tidy
  return text.split('\n').filter((line) => keyBody(line) !== body).join('\n');
}

/** Where the account's keys live, according to the machine. */
async function homeOf(host: Target, user: string): Promise<string> {
  const asked = await ask(host, `getent passwd ${shellQuote(user)}`);
  if (asked.code !== 0) throw new Error(`there is no account called ${user} on ${describe(host)}`);
  const home = asked.out.trim().split(':')[5] ?? '';
  if (home.length === 0) throw new Error(`${user} has no home directory, so it has nowhere to keep a key`);
  return `${home}/.ssh/authorized_keys`;
}

/** The line for this key, and the modes that decide whether sshd will read it at all. */
export async function readAuthorizedKey(
  host: Target,
  path: string,
  key: string,
): Promise<{ line: string | null; mode: string; directoryMode: string; owner: string } | null> {
  const directory = path.replace(/\/[^/]*$/, '') || '/';
  const asked = await ask(host, escalate(host,
    // the directory and the file are separate existence questions: an account that has never had a
    // key has neither, and an account that has had one tidied away has the directory and no file
    `test -d ${shellQuote(directory)} || exit 9; ` +
    `stat -c '%a' ${shellQuote(directory)}; ` +
    `if [ -f ${shellQuote(path)} ]; then stat -c '%a %U' ${shellQuote(path)}; echo '#pulumi-homelab#'; ` +
    `cat ${shellQuote(path)}; else echo 'none none'; echo '#pulumi-homelab#'; fi`,
  ));
  if (asked.code === 9) return null;
  if (asked.code !== 0) throw new Error(`could not read ${path}: ${asked.err.trim()}`);

  const [head = '', contents = ''] = asked.out.split('#pulumi-homelab#\n');
  const [directoryMode = '', fileLine = ''] = head.trim().split('\n');
  const [fileMode = '', fileOwner = ''] = fileLine.trim().split(/\s+/);
  const body = keyBody(key);
  const line = body === null
    ? null
    : contents.split('\n').find((existing) => keyBody(existing) === body) ?? null;
  return {
    line,
    mode: fileMode === 'none' ? '' : normaliseMode(fileMode),
    directoryMode: normaliseMode(directoryMode),
    owner: fileOwner === 'none' ? '' : fileOwner,
  };
}

function providerFor(host: Target): pulumi.dynamic.ResourceProvider<AuthorizedKeyArgs, AuthorizedKeyState> {
  const settle = async (args: AuthorizedKeyArgs): Promise<AuthorizedKeyState> => {
    if (keyBody(args.key) === null) {
      throw new Error(`that does not look like a public key: expected '<type> AAAA… <comment>', got '${args.key.slice(0, 40)}'`);
    }
    const path = args.path ?? (await homeOf(host, args.user));
    const options = args.options ?? [];
    const line = authorizedLine(args.key, options);
    const directory = path.replace(/\/[^/]*$/, '') || '/';

    const before = await readAuthorizedKey(host, path, args.key);
    const settled = before !== null
      && before.line === line
      && before.mode === FILE_MODE
      && before.directoryMode === DIRECTORY_MODE
      && before.owner === args.user;

    if (!settled) {
      const current = await must(host, escalate(host, `cat ${shellQuote(path)} 2>/dev/null || true`));
      await must(host, escalate(host,
        `mkdir -p ${shellQuote(directory)} && ` +
        `${heredoc(path, upsertAuthorized(current, args.key, line))}\n` +
        // both modes, every time: sshd refuses the file silently if either is loose, and the
        // symptom is `Permission denied (publickey)` with nothing about permissions
        `chmod ${DIRECTORY_MODE} ${shellQuote(directory)} && chmod ${FILE_MODE} ${shellQuote(path)} && ` +
        `chown -R ${shellQuote(args.user)} ${shellQuote(directory)}`,
      ));
    }

    const actual = await readAuthorizedKey(host, path, args.key);
    if (!actual || actual.line === null) throw new Error(`wrote the key for ${args.user} but it is not in ${path}`);
    return {
      user: args.user,
      key: args.key,
      options,
      path,
      mode: actual.mode,
      directoryMode: actual.directoryMode,
      owner: actual.owner,
      comment: keyComment(actual.line ?? ''),
    };
  };

  return {
    async create(args) {
      const state = await settle(args);
      return { id: `${state.path}#${keyBody(state.key)}`, outs: state };
    },

    async read(id, state) {
      const path = state?.path ?? id.split('#')[0] ?? '';
      const key = state?.key ?? '';
      const actual = await readAuthorizedKey(host, path, key);
      // the line is gone, or the whole .ssh directory is: either way nothing here is authorised any
      // more, and the next up puts it back
      if (!actual || actual.line === null) return { id: undefined, props: undefined };
      return {
        id,
        props: {
          user: state?.user ?? '',
          options: state?.options ?? [],
          ...state,
          key,
          path,
          mode: actual.mode,
          directoryMode: actual.directoryMode,
          owner: actual.owner,
          comment: keyComment(actual.line ?? ''),
        },
      };
    },

    async update(_id, _old, args) {
      return { outs: await settle(args) };
    },

    async diff(_id, old, args) {
      const options = args.options ?? [];
      return {
        changes: transportChanged(old)
          || keyBody(old.key) !== keyBody(args.key)
          || old.options.join(',') !== options.join(',')
          || old.user !== args.user
          || old.mode !== FILE_MODE
          || old.directoryMode !== DIRECTORY_MODE
          // ownership fails as silently as mode: 0600 owned by root in somebody's home is refused
          || old.owner !== args.user,
        // a different key, or the same key for a different account, is a different authorisation
        replaces: keyBody(old.key) !== keyBody(args.key) || old.user !== args.user ? ['key', 'user'] : [],
        stables: [],
        deleteBeforeReplace: true,
      };
    },

    async delete(id, state) {
      const [path = ''] = id.split('#');
      const current = await must(host, escalate(host, `cat ${shellQuote(path)} 2>/dev/null || true`));
      await must(host, escalate(host, heredoc(path, removeAuthorized(current, state.key))));
    },
  };
}

/** A key that should be authorised, identified by the key itself rather than by what it is called. */
export class AuthorizedKey extends pulumi.dynamic.Resource {
  declare readonly user: pulumi.Output<string>;
  declare readonly path: pulumi.Output<string>;
  declare readonly mode: pulumi.Output<string>;
  /** How a person tells this key from the others in the same file. */
  declare readonly comment: pulumi.Output<string>;

  constructor(name: string, host: Target, args: AuthorizedKeyArgs, opts?: pulumi.CustomResourceOptions) {
    super(stamped(providerFor(host)), name, {
      options: [],
      path: undefined,
      mode: undefined,
      directoryMode: undefined,
      owner: undefined,
      comment: undefined,
      ...args,
    }, withLegacyAlias(opts), 'homelab', 'AuthorizedKey');
  }
}
