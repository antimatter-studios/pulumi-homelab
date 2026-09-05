import * as pulumi from '@pulumi/pulumi';
import { asRoot, ask, must, shellQuote, type Host } from '../ssh';

/**
 * A package that should be installed.
 *
 * Deliberately not a wrapper around `apt-get install`. The difference matters: a command resource
 * records that a command was once run, and knows nothing afterwards. This one asks dpkg what is
 * actually installed, so removing a package by hand shows up as drift on the next refresh instead
 * of being invisible for ever.
 *
 * It manages presence and nothing else. Pinning a version is left out on purpose: on a machine that
 * runs `apt upgrade` on its own schedule, a pinned version turns every security update into drift
 * and every refresh into a false alarm. If a version ever has to be held, that is `apt-mark hold`
 * and a different resource with different semantics, not an argument here.
 */
export interface AptPackageArgs {
  /** What apt calls it. */
  name: string;
  /**
   * Whether to refresh the package lists before installing.
   *
   * Off by default because it is slow and shared: every package doing it makes a deployment crawl,
   * and on a Pi over a slow link it dominates everything else. Set it on one resource that the
   * others depend on, or run it yourself.
   */
  update?: boolean;
}

interface AptPackageState {
  name: string;
  update: boolean;
  /** What is actually installed, which is how a version bump shows up in a diff without being asked for. */
  version: string;
}

/** What dpkg says about it, or null when it is not installed. */
export async function readPackage(host: Host, name: string): Promise<string | null> {
  // dpkg-query exits non-zero for a package it has never heard of, which is an answer and not a
  // fault. It also reports packages that are known but removed, hence the status check.
  const asked = await ask(host, `dpkg-query -W -f='\${db:Status-Status} \${Version}' ${shellQuote(name)} 2>/dev/null`);
  if (asked.code !== 0) return null;
  const [status, version] = asked.out.trim().split(/\s+/);
  return status === 'installed' ? (version ?? '') : null;
}

function providerFor(host: Host): pulumi.dynamic.ResourceProvider<AptPackageArgs, AptPackageState> {
  const install = async (args: AptPackageArgs): Promise<string> => {
    const refresh = args.update ? 'apt-get update -qq && ' : '';
    // noninteractive or a package with a config prompt hangs the deployment for ever behind a
    // dialogue nobody can see, let alone answer
    await must(host, asRoot(
      `${refresh}DEBIAN_FRONTEND=noninteractive apt-get install -y -qq ${shellQuote(args.name)}`,
    ));
    const version = await readPackage(host, args.name);
    if (version === null) throw new Error(`apt said it installed ${args.name}, but dpkg cannot find it`);
    return version;
  };

  return {
    async create(args) {
      const version = await install(args);
      return { id: args.name, outs: { name: args.name, update: args.update ?? false, version } };
    },

    async read(id, state) {
      const version = await readPackage(host, id);
      // somebody removed it by hand: Pulumi forgets it, and the next up puts it back
      if (version === null) return { id: undefined, props: undefined };
      // `state` is absent when a resource is being imported rather than refreshed, so every field
      // has to stand on its own here rather than leaning on what Pulumi already knew
      return { id, props: { update: state?.update ?? false, ...state, name: id, version } };
    },

    async update(id, old, args) {
      // the only thing that can change in place is whether the lists are refreshed first; the name
      // is the identity and a different name is a different package
      const version = (await readPackage(host, id)) ?? (await install(args));
      return { outs: { ...old, update: args.update ?? false, version } };
    },

    async diff(_id, old, args) {
      return {
        changes: old.name !== args.name || old.update !== (args.update ?? false),
        replaces: old.name !== args.name ? ['name'] : [],
        stables: [],
        deleteBeforeReplace: false,
      };
    },

    async delete(id) {
      // purge rather than remove, so configuration does not linger to surprise a later install.
      // Autoremove is deliberately not run: it reaches beyond this resource and could take out a
      // dependency something undeclared on the machine still needs.
      await must(host, asRoot(`DEBIAN_FRONTEND=noninteractive apt-get purge -y -qq ${shellQuote(id)}`));
    },
  };
}

/** A package the machine should have, checked against dpkg rather than remembered. */
export class AptPackage extends pulumi.dynamic.Resource {
  declare readonly name: pulumi.Output<string>;
  declare readonly version: pulumi.Output<string>;

  constructor(name: string, host: Host, args: AptPackageArgs, opts?: pulumi.CustomResourceOptions) {
    super(providerFor(host), name, { version: undefined, update: false, ...args }, opts);
  }
}
