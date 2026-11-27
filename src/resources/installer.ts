import * as pulumi from '@pulumi/pulumi';
import { escalate, ask, must, shellQuote, type Target } from '../ssh.ts';
import { stamped, transportChanged, withLegacyAlias } from '../upgrade.ts';

/**
 * A tool installed by its vendor's own installer, into an account, and then left to update itself.
 *
 * For software that ships weekly and updates itself: the coding agents (Claude Code, Codex,
 * opencode). Pinning those to a checksummed tarball, as `Archive` does, sounds safer and works out
 * worse. The pin is out of date within days, the harness that drives them starts refusing old
 * versions ("This provider version has limited compatibility… Use >=2.1.301"), and the tool's own
 * `update` cannot help, because the copy it would replace belongs to the deployment.
 *
 * So this declares only that the tool is installed, the vendor's way, as the account that uses
 * it. If the executable is missing, the installer runs. If it is there, at whatever version, that
 * is success. The version is the tool's business from then on, which is the whole point.
 *
 * The trade is stated rather than hidden: the installer is fetched from the vendor at install time
 * and is not checksummed here, so this trusts the vendor's domain over https. That is the same trust
 * the vendor's own instructions ask for, and it is why this is for these tools and not for anything
 * the machine needs in order to boot or be reached.
 */
export interface OfficialInstallArgs {
  /** The account it is installed for, and run as. */
  account: string;
  /** That account's home, which the installers write under. */
  home: string;
  /**
   * The vendor's install command, run by bash as the account with HOME set: for example
   * `curl -fsSL https://claude.ai/install.sh | bash`.
   */
  installer: string;
  /** The executable the installer leaves behind. Its presence is what "installed" means. */
  bin: string;
}

interface OfficialInstallState extends OfficialInstallArgs {
  /** What the tool said its version was when last read. Reported, never enforced. */
  version: string;
}

/** The command that runs something as the account, with a login-shaped environment. */
export function asAccount(account: string, home: string, command: string): string {
  // `cd` first: runuser keeps the caller's working directory, usually a home the account cannot
  // enter, and Codex's installer then fails in `find` ("Failed to restore initial working directory")
  return `runuser -u ${shellQuote(account)} -- env HOME=${shellQuote(home)} `
    + `PATH=${shellQuote(`${home}/.local/bin:/usr/local/bin:/usr/bin:/bin`)} bash -c ${shellQuote(`cd "$HOME" && ${command}`)}`;
}

/**
 * The version the tool reports, or null when it is not installed **or does not run**.
 *
 * Running it is the check, not `test -x`: a home carried over from another machine can hold a
 * binary for the wrong architecture (the Pi's arm64 opencode, on x86), which is executable and
 * useless. Anything that cannot answer `--version` is treated as missing and installed again.
 */
async function readVersion(host: Target, args: OfficialInstallArgs): Promise<string | null> {
  const asked = await ask(host, escalate(host,
    `test -x ${shellQuote(args.bin)} || exit 9; ` + asAccount(args.account, args.home, `${shellQuote(args.bin)} --version 2>/dev/null`),
  ));
  if (asked.code !== 0) return null;
  return asked.out.trim().split('\n')[0] || 'installed';
}

function providerFor(host: Target): pulumi.dynamic.ResourceProvider<OfficialInstallArgs, OfficialInstallState> {
  const ensure = async (args: OfficialInstallArgs): Promise<OfficialInstallState> => {
    let version = await readVersion(host, args);
    if (version === null) {
      await must(host, escalate(host, asAccount(args.account, args.home, args.installer)));
      version = await readVersion(host, args);
      if (version === null) {
        throw new Error(`the installer ran for ${args.account} but ${args.bin} is still not there: ${args.installer}`);
      }
    }
    return { ...args, version };
  };

  return {
    async create(args) {
      return { id: `${args.account}:${args.bin}`, outs: await ensure(args) };
    },

    async read(id, state) {
      if (!state) return { id, props: state };
      const version = await readVersion(host, state);
      // gone from the machine: drop it from the state, and the next deployment installs it again
      if (version === null) return { id: undefined, props: undefined };
      return { id, props: { ...state, version } };
    },

    async update(_id, _old, args) {
      return { outs: await ensure(args) };
    },

    async diff(_id, old, args) {
      // the version is deliberately not compared: updating is the tool's job, not the deployment's
      return {
        changes: transportChanged(old) || old.installer !== args.installer || old.bin !== args.bin
          || old.account !== args.account || old.home !== args.home,
        replaces: old.account !== args.account || old.bin !== args.bin ? ['account', 'bin'] : [],
        stables: [],
        deleteBeforeReplace: false,
      };
    },

    async delete() {
      // left in place: an installed tool holds the account's own settings and sessions, and a
      // deployment that stops declaring it is not a reason to take those away
    },
  };
}

/** A self-updating tool, installed the vendor's way and then left alone. */
export class OfficialInstall extends pulumi.dynamic.Resource {
  declare readonly version: pulumi.Output<string>;

  constructor(name: string, host: Target, args: OfficialInstallArgs, opts?: pulumi.CustomResourceOptions) {
    super(stamped(providerFor(host)), name, { version: undefined, ...args }, withLegacyAlias(opts), 'homelab', 'OfficialInstall');
  }
}
