import * as pulumi from '@pulumi/pulumi';
import { escalate, ask, must, shellQuote, type Target } from '../ssh.ts';
import { providerChanged, withLegacyAlias } from '../upgrade.ts';
import { readPackage } from './apt.ts';

/**
 * A Debian package installed from a URL rather than from a repository.
 *
 * `AptPackage` covers everything in Debian's archives, which is almost everything. This is for the
 * remainder: something built elsewhere and published as a `.deb`, where there is no repository to
 * add and adding one would mean trusting a whole archive in order to install a single file.
 *
 * It is still a package rather than a downloaded binary, and that distinction is the point. A
 * binary copied into /usr/local/bin is invisible to everything that inventories a machine: dpkg
 * does not know it exists, nothing can say where it came from, and removing it cleanly is guesswork.
 * Installed as a package it appears in `dpkg -l` with a version, which is what makes an audit of
 * this machine able to account for the file a year from now.
 *
 * Like `AptPackage`, this asks dpkg what is actually installed rather than remembering what it once
 * did, so a package removed by hand shows up as drift on the next refresh.
 */
export interface DebPackageArgs {
  /** The name dpkg knows it by, which is not necessarily the filename. */
  name: string;
  /** Where to fetch the `.deb` from. */
  url: string;
  /**
   * SHA-256 of the file, and not optional.
   *
   * HTTPS authenticates the server, not the artefact. A release asset can be replaced in place, a
   * tag can be moved, and a mirror can serve something else entirely — none of which the transport
   * notices. This is the only thing here that makes an install reproducible rather than merely
   * repeatable, so it is a required argument: a checksum nobody set is a checksum nobody checked.
   */
  sha256: string;
}

interface DebPackageState {
  name: string;
  url: string;
  sha256: string;
  /** What dpkg reports after the install, so a version change is visible in a diff. */
  version: string;
}

/**
 * The install, as one shell command.
 *
 * Pure and exported so it can be tested without a machine, and so the ordering below is pinned by
 * something other than hope. The order is the whole substance of it:
 *
 *   1. Download to a private temporary directory. Not /tmp directly — a predictable path in a
 *      world-writable directory is a file another user can swap between the checksum passing and
 *      apt reading it.
 *   2. Verify before installing. `sha256sum -c` fails the command, and `set -e` stops there, so a
 *      file that does not match is never handed to apt.
 *   3. Install with `apt-get`, not `dpkg -i`. dpkg does not resolve dependencies: given a package
 *      that needs something absent it leaves it unpacked but unconfigured, which is a state that
 *      breaks the *next* unrelated apt run and gives no hint why. apt pulls the dependencies in.
 *   4. Clean up regardless, with a trap, so a failed verification does not leave the rejected file
 *      behind to be found later and trusted.
 */
export function installScript(args: DebPackageArgs): string {
  const url = shellQuote(args.url);
  const sha = shellQuote(args.sha256);
  return [
    'set -e',
    'dir=$(mktemp -d)',
    'trap "rm -rf \\"$dir\\"" EXIT',
    `curl -fsSL --retry 3 -o "$dir/pkg.deb" ${url}`,
    `echo ${sha}"  $dir/pkg.deb" | sha256sum -c - >/dev/null`,
    // noninteractive for the same reason as AptPackage: a config prompt nobody can see is a
    // deployment that hangs for ever.
    'DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$dir/pkg.deb"',
  ].join('; ');
}

function providerFor(host: Target): pulumi.dynamic.ResourceProvider<DebPackageArgs, DebPackageState> {
  const install = async (args: DebPackageArgs): Promise<string> => {
    await must(host, escalate(host, installScript(args)));
    const version = await readPackage(host, args.name);
    if (version === null) {
      // apt reported success and dpkg has never heard of it, which means the `.deb` provides a
      // package under a different name than the one declared. Worth saying plainly, because the
      // alternative is a resource that succeeds and a `read` that reports it missing for ever.
      throw new Error(
        `installed ${args.url} but dpkg has no package named ${args.name} — ` +
        `check the name against 'dpkg-deb -f <file> Package'`,
      );
    }
    return version;
  };

  return {
    async create(args) {
      const version = await install(args);
      return { id: args.name, outs: { ...args, version } };
    },

    async read(id, state) {
      const version = await readPackage(host, id);
      // Removed by hand: Pulumi forgets it, and the next up puts it back.
      if (version === null) return { id: undefined, props: undefined };
      // `state` is absent during an import rather than a refresh, so nothing here may assume it.
      return {
        id,
        props: { url: state?.url ?? '', sha256: state?.sha256 ?? '', ...state, name: id, version },
      };
    },

    async update(id, old, args) {
      // A changed url or checksum means a different file, so this reinstalls rather than checking
      // whether the version happens to match. apt handles the downgrade case too, which a
      // conditional install would not.
      const version = await install({ ...args, name: id });
      return { outs: { ...old, ...args, name: id, version } };
    },

    async diff(_id, old, args) {
      return {
        changes: providerChanged(old, args)
          || old.name !== args.name
          || old.url !== args.url
          || old.sha256 !== args.sha256,
        // The name is the identity: a different package is a different resource.
        replaces: old.name !== args.name ? ['name'] : [],
        stables: [],
        deleteBeforeReplace: false,
      };
    },

    async delete(id) {
      // Purge rather than remove, matching AptPackage, so configuration does not linger to
      // surprise a later install. Autoremove is deliberately not run: it reaches beyond this
      // resource and could take out something another resource is relying on.
      await must(host, escalate(host,
        `DEBIAN_FRONTEND=noninteractive apt-get purge -y -qq ${shellQuote(id)}`,
      ));
    },
  };
}

/** A `.deb` fetched from a URL, verified, and installed as a package dpkg knows about. */
export class DebPackage extends pulumi.dynamic.Resource {
  declare readonly name: pulumi.Output<string>;
  declare readonly version: pulumi.Output<string>;

  constructor(name: string, host: Target, args: DebPackageArgs, opts?: pulumi.CustomResourceOptions) {
    super(providerFor(host), name, { ...args, version: undefined }, withLegacyAlias(opts), 'homelab', 'DebPackage');
  }
}
