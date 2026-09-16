import * as pulumi from '@pulumi/pulumi';
import { escalate, ask, must, shellQuote, type Target, describe } from '../ssh.ts';
import { stamped, transportChanged, withLegacyAlias } from '../upgrade.ts';
import { markManualCommand, parseManual } from '../aptmark.ts';

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
  /**
   * Whether apt holds it as manually installed rather than as somebody else's dependency.
   *
   * This resource mostly gets it for free: `apt-get install` on an already-installed package prints
   * *"set to manually installed"* and does exactly that. What it does not cover is the package that
   * was already there when the resource adopted it and never needed installing — present, agreed
   * with, and still removable by an `apt autoremove` aimed at something else. So it is read rather
   * than assumed. See `aptmark.ts`.
   */
  manual: boolean;
}

/**
 * `installed 1.2.3` → the version, or null for anything else.
 *
 * Its own function because the status word is the whole decision and was previously buried in the
 * read where nothing could test it. dpkg keeps a record of a package it has removed, so a line
 * coming back is not the same as a package being present — `deinstall` and `config-files` are both
 * answers meaning "not installed" that arrive looking exactly like an answer meaning it is.
 */
export function parseDpkgStatus(out: string): string | null {
  const [status, version] = out.trim().split(/\s+/);
  return status === 'installed' ? (version ?? '') : null;
}

/** What dpkg says about it, or null when it is not installed. */
export async function readPackage(host: Target, name: string): Promise<string | null> {
  // dpkg-query exits non-zero for a package it has never heard of, which is an answer and not a
  // fault. It also reports packages that are known but removed, hence the status check.
  const asked = await ask(host, `dpkg-query -W -f='\${db:Status-Status} \${Version}' ${shellQuote(name)} 2>/dev/null`);
  if (asked.code !== 0) return null;
  return parseDpkgStatus(asked.out);
}

/** Whether apt holds this one as manually installed. */
export async function readManual(host: Target, name: string): Promise<boolean> {
  const asked = await ask(host, `apt-mark showmanual ${shellQuote(name)} 2>/dev/null || true`);
  return parseManual(asked.out).includes(name.split(':')[0] ?? name);
}

function providerFor(host: Target): pulumi.dynamic.ResourceProvider<AptPackageArgs, AptPackageState> {
  const install = async (args: AptPackageArgs): Promise<string> => {
    const refresh = args.update ? 'apt-get update -qq && ' : '';
    // noninteractive or a package with a config prompt hangs the deployment for ever behind a
    // dialogue nobody can see, let alone answer
    await must(host, escalate(host,
      `${refresh}DEBIAN_FRONTEND=noninteractive apt-get install -y -qq ${shellQuote(args.name)}`,
    ));
    const version = await readPackage(host, args.name);
    if (version === null) throw new Error(`apt said it installed ${args.name}, but dpkg cannot find it`);
    return version;
  };

  /** Make apt hold it as asked for rather than as somebody else's dependency. */
  const markManual = async (name: string): Promise<boolean> => {
    if (await readManual(host, name)) return true;
    const mark = markManualCommand([name]);
    if (mark !== null) await must(host, escalate(host, mark));
    return readManual(host, name);
  };

  return {
    async create(args) {
      const version = await install(args);
      return { id: args.name, outs: { name: args.name, update: args.update ?? false, version, manual: await markManual(args.name) } };
    },

    async read(id, state) {
      const version = await readPackage(host, id);
      // somebody removed it by hand: Pulumi forgets it, and the next up puts it back
      if (version === null) return { id: undefined, props: undefined };
      // `state` is absent when a resource is being imported rather than refreshed, so every field
      // has to stand on its own here rather than leaning on what Pulumi already knew
      return {
        id,
        props: {
          update: state?.update ?? false,
          ...state,
          name: id,
          version,
          // from the machine rather than from what was remembered: an `apt-mark auto` somebody ran,
          // or a package this adopted without ever installing, both show up only by asking
          manual: await readManual(host, id),
        },
      };
    },

    async update(id, old, args) {
      // the only thing that can change in place is whether the lists are refreshed first; the name
      // is the identity and a different name is a different package
      const version = (await readPackage(host, id)) ?? (await install(args));
      return { outs: { ...old, update: args.update ?? false, version, manual: await markManual(id) } };
    },

    async diff(_id, old, args) {
      return {
        changes: transportChanged(old)
          // auto-marked is present and still removable by an autoremove aimed at something else,
          // so the declaration is weaker than it reads until it is fixed
          || old.manual === false
          || old.name !== args.name || old.update !== (args.update ?? false),
        replaces: old.name !== args.name ? ['name'] : [],
        stables: [],
        deleteBeforeReplace: false,
      };
    },

    async delete(id) {
      // purge rather than remove, so configuration does not linger to surprise a later install.
      // Autoremove is deliberately not run: it reaches beyond this resource and could take out a
      // dependency something undeclared on the machine still needs.
      await must(host, escalate(host, `DEBIAN_FRONTEND=noninteractive apt-get purge -y -qq ${shellQuote(id)}`));
    },
  };
}

/** A package the machine should have, checked against dpkg rather than remembered. */
export class AptPackage extends pulumi.dynamic.Resource {
  declare readonly name: pulumi.Output<string>;
  declare readonly version: pulumi.Output<string>;
  /** Whether apt holds it as asked for rather than as somebody else's dependency. */
  declare readonly manual: pulumi.Output<boolean>;

  constructor(name: string, host: Target, args: AptPackageArgs, opts?: pulumi.CustomResourceOptions) {
    super(stamped(providerFor(host)), name, { version: undefined, manual: undefined, update: false, ...args }, withLegacyAlias(opts), 'homelab', 'AptPackage');
  }
}
