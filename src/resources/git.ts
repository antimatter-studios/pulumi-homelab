import * as pulumi from '@pulumi/pulumi';
import { escalate, ask, must, shellQuote, type Target, describe } from '../ssh.ts';
import { stamped, transportChanged, withLegacyAlias } from '../upgrade.ts';

/**
 * A repository cloned and checked out at a commit.
 *
 * **A commit and nothing else.** A branch moves by design and a tag moves whenever somebody
 * force-pushes one, so neither can be read back and compared: `git rev-parse HEAD` answers with a
 * sha, and the only declaration that can be checked against a sha is a sha. Taking a ref instead
 * would mean resolving it on every run and then deciding whether a change of answer is an upgrade
 * or somebody moving a tag under us — a question with no good answer, avoided entirely by never
 * asking it. Equal is clean, different is drift, and both are unambiguous.
 *
 * The ergonomic cost is real and is the caller's to pay: nobody reads release notes in sha form, so
 * a declaration should carry the sha and a comment beside it saying which tag it was.
 * `git ls-remote <url> v1.2.3` turns one into the other.
 *
 * **It clones and checks out, and it does not build.** A clone at a fixed commit is declarative —
 * the read has a real answer and a rebuild reproduces it. Running a build or an installer afterwards
 * is not: what lands on disk is unverifiable, differs on a different day, and on slow hardware takes
 * hours to produce something nobody can check. A resource that cloned a repository and ran its
 * setup script would be a command with no readable state, which is the thing this package exists to
 * avoid. If a build step is unavoidable, build it once, put the result somewhere durable with a
 * checksum, and install that with `Archive` — which turns an unrepeatable afternoon into a verified
 * download.
 */
export interface GitCheckoutArgs {
  /** Where to clone from. */
  url: string;
  /** Where the working tree goes. This is the resource's identity. */
  path: string;
  /**
   * The commit to check out, as a full or abbreviated sha.
   *
   * A tag or a branch name is refused rather than resolved — see the note on this resource. Get the
   * sha with `git ls-remote <url> <tag>` and keep the tag beside it in a comment.
   */
  commit: string;
}

interface GitCheckoutState {
  url: string;
  path: string;
  commit: string;
  /** What the checkout is at now — the read, and where a hand `git checkout` shows up. */
  head: string;
}

/** Whether a ref is written the way a commit is. */
export function looksLikeCommit(ref: string): boolean {
  // seven is git's own shortest default abbreviation; under it, `abc` is a tag far more often than
  // it is a commit, and taking it for one would pin something that can never resolve
  return /^[0-9a-f]{7,40}$/i.test(ref);
}

/**
 * Why a commit argument is refused, or null when it is a sha.
 *
 * A separate function from the check because the message is most of the value: somebody who wrote
 * `v1.2.3` needs to be told how to turn it into a sha, not merely that it was rejected.
 */
export function commitRefusal(url: string, commit: string): string | null {
  if (looksLikeCommit(commit)) return null;
  return `${commit} is not a commit. This resource pins a sha and nothing else, because a tag or a `
    + `branch can move and a moved ref cannot be told apart from drift. Run `
    + `\`git ls-remote ${url} ${commit}\` to get the sha, and keep ${commit} beside it in a comment.`;
}

/**
 * Whether an abbreviated declaration names the commit the checkout is at.
 *
 * `rev-parse HEAD` always answers with the full forty characters, so an abbreviated declaration
 * would otherwise read as permanent drift. Prefix rather than equality, case-insensitively, and
 * only in that direction: a declaration is allowed to be shorter than the answer and never longer.
 */
export function sameCommit(head: string, commit: string): boolean {
  if (head === '' || commit === '') return false;
  return head.toLowerCase().startsWith(commit.toLowerCase());
}

/** What to do to a checkout to get it to the declared commit. */
export type CheckoutAct = 'clone' | 'checkout' | 'nothing';

/**
 * Which of the three acts a checkout needs.
 *
 * "Nothing" is the one that matters. A tree already at the commit is not worth a fetch, and
 * re-cloning would throw away whatever sits beside the checkout — build output, an untracked config
 * somebody put there, a submodule.
 */
export function checkoutAct(head: string | null, commit: string): CheckoutAct {
  if (head === null) return 'clone';
  return sameCommit(head, commit) ? 'nothing' : 'checkout';
}

/** The commands that put a checkout at a commit, or null when it is already there. */
export function checkoutCommand(act: CheckoutAct, url: string, path: string, commit: string): string | null {
  if (act === 'nothing') return null;
  const at = `git -C ${shellQuote(path)}`;
  // --detach because that is what a pinned checkout is: attaching to a local branch would invite
  // the next `git pull` to move it, and HEAD would then describe the branch rather than the pin
  const checkout = `${at} checkout --quiet --detach ${shellQuote(commit)}`;
  if (act === 'clone') {
    return `mkdir -p "$(dirname ${shellQuote(path)})" && `
      + `git clone --quiet ${shellQuote(url)} ${shellQuote(path)} && ${checkout}`;
  }
  // --tags as well, because a commit reachable only from a tag created since the clone is not
  // fetched by default, and that is the commonest reason a checkout of a valid sha fails
  return `${at} fetch --quiet --tags origin && ${checkout}`;
}

/**
 * Whether anything about this checkout needs doing.
 *
 * `head` against `commit` is the drift that matters and the reason the read exists: somebody ran
 * `git checkout` by hand, or a build left the tree on another commit.
 */
export function checkoutChanged(
  old: { head: string; commit: string; url: string },
  wanted: { commit: string; url: string },
): boolean {
  return !sameCommit(old.head, old.commit)
    || old.commit !== wanted.commit
    || old.url !== wanted.url;
}

const HEAD_MARKER = '#pulumi-homelab#origin';

/** `rev-parse` and `remote get-url`, split apart again. */
export function parseCheckout(out: string): { head: string; url: string } {
  const [head = '', url = ''] = out.split(`${HEAD_MARKER}\n`);
  return { head: head.trim(), url: url.trim() };
}

/** What the checkout is at, or null when there is no checkout there. */
export async function readCheckout(host: Target, path: string): Promise<{ head: string; url: string } | null> {
  const asked = await ask(host, escalate(host,
    // a missing checkout is an answer rather than a fault, and 9 is not a code git itself returns
    `test -d ${shellQuote(`${path}/.git`)} || exit 9; `
    + `git -C ${shellQuote(path)} rev-parse HEAD; echo '${HEAD_MARKER}'; `
    + `git -C ${shellQuote(path)} remote get-url origin 2>/dev/null || true`,
  ));
  if (asked.code === 9) return null;
  if (asked.code !== 0) throw new Error(`could not read the checkout at ${path}: ${asked.err.trim()}`);
  return parseCheckout(asked.out);
}

function providerFor(host: Target): pulumi.dynamic.ResourceProvider<GitCheckoutArgs, GitCheckoutState> {
  const settle = async (args: GitCheckoutArgs): Promise<GitCheckoutState> => {
    const refusal = commitRefusal(args.url, args.commit);
    if (refusal !== null) throw new Error(refusal);

    const before = await readCheckout(host, args.path);
    const command = checkoutCommand(checkoutAct(before?.head ?? null, args.commit), args.url, args.path, args.commit);
    if (command !== null) await must(host, escalate(host, command));

    const actual = await readCheckout(host, args.path);
    if (actual === null || !sameCommit(actual.head, args.commit)) {
      throw new Error(
        `checked out ${args.commit} at ${args.path} on ${describe(host)} `
        + `but HEAD is ${actual?.head || 'nothing'}`,
      );
    }
    return { url: args.url, path: args.path, commit: args.commit, head: actual.head };
  };

  return {
    async check(_olds, news) {
      const refusal = commitRefusal(news.url, news.commit);
      // the argument is wrong rather than the machine: worth saying at preview, before anything runs
      return { inputs: news, failures: refusal === null ? [] : [{ property: 'commit', reason: refusal }] };
    },

    async create(args) {
      return { id: args.path, outs: await settle(args) };
    },

    async read(id, state) {
      const actual = await readCheckout(host, id);
      // no checkout there any more: Pulumi forgets it, and the next up clones it back
      if (actual === null) return { id: undefined, props: undefined };
      return {
        id,
        props: {
          url: state?.url ?? actual.url,
          commit: state?.commit ?? actual.head,
          ...state,
          path: id,
          // the one field that always comes from the machine rather than from what was remembered
          head: actual.head,
        },
      };
    },

    async update(id, _old, args) {
      return { outs: await settle({ ...args, path: id }) };
    },

    async diff(_id, old, args) {
      return {
        changes: transportChanged(old) || checkoutChanged(old, args),
        // a different path or a different origin is a different checkout, and changing either in
        // place would leave a working tree that nothing declares
        replaces: old.path !== args.path || old.url !== args.url ? ['path', 'url'] : [],
        stables: [],
        deleteBeforeReplace: true,
      };
    },

    async delete(id, state) {
      // the working tree goes and nothing beside it does: the parent may be somebody else's
      // directory, and `rm -rf` on that would take their things with it
      await must(host, escalate(host, `rm -rf ${shellQuote(state.path || id)}`));
    },
  };
}

/** A repository at a pinned commit, read back with `git rev-parse HEAD`. */
export class GitCheckout extends pulumi.dynamic.Resource {
  declare readonly path: pulumi.Output<string>;
  /** What the checkout is at now, which is where a hand `git checkout` shows up. */
  declare readonly head: pulumi.Output<string>;

  constructor(name: string, host: Target, args: GitCheckoutArgs, opts?: pulumi.CustomResourceOptions) {
    super(stamped(providerFor(host)), name, { head: undefined, ...args }, withLegacyAlias(opts), 'homelab', 'GitCheckout');
  }
}
