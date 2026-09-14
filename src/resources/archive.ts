import * as pulumi from '@pulumi/pulumi';
import { escalate, ask, must, shellQuote, type Target, describe } from '../ssh.ts';
import { stamped, transportChanged, withLegacyAlias } from '../upgrade.ts';

/** The kinds of archive this knows how to unpack. */
export type ArchiveFormat = 'tar.gz' | 'tar.xz' | 'tar.bz2' | 'zip';

/**
 * Software installed once from a verified archive, and thereafter left alone.
 *
 * `DebPackage` covers anything published as a `.deb`, and dpkg answers what is installed. This is
 * for the rest: a tarball of binaries, a release asset, a build captured once and kept somewhere
 * durable.
 *
 * **The url is a bootstrap, not a version policy.** This resource asks one question — is it
 * installed — and fetches only when the answer is no, or when the url or checksum in the program
 * change. It does not know or compare versions. That is the shape `AptPackage` has, for the same
 * reason: software that updates itself would otherwise put the resource in permanent drift the
 * moment it did its job, and every self-update would read as damage.
 *
 * **Pin what is expensive to get wrong; install and forget what maintains itself.** A cluster, or
 * anything holding data, should be pinned to a version and compared, so that it upgrades at a
 * moment somebody chose — but that pinning belongs in the declaration that installs it, and
 * `Precondition` is the resource that asserts it. What belongs here is the other kind: a tool that
 * ships its own updater, where the archive is how it reaches a bare machine the first time and
 * nothing after that is this resource's business.
 *
 * **The checksum is required, and is not about versions.** HTTPS authenticates the server, not the
 * artefact: a release asset can be replaced in place, a tag can be moved, a mirror can serve
 * something else, and the transport notices none of it. For a self-contained executable fetched
 * over the internet, knowing exactly what was put on the machine is the whole risk. A vendor's own
 * `SHA256SUMS` is the best case. Where none is published, one computed from the asset that was
 * actually fetched pins what was got rather than what they meant — a weaker claim, and still enough
 * to catch an asset swapped under a fixed url, which is the realistic threat.
 *
 * The line this does not cross: **an archive with a checksum is reproducible and a downloaded
 * installer is not.** If software only ships as a script, run it once, capture what it produced,
 * put that somewhere durable with a checksum, and install *that* here — which turns an
 * unrepeatable afternoon of compiling into a verified download. Running the installer would be a
 * command with no readable state, which is the thing this package exists to avoid.
 */
export interface ArchiveArgs {
  /** What it is, for the id and for messages. */
  name: string;
  /** Where the archive is. */
  url: string;
  /**
   * SHA-256 of the archive, and not optional.
   *
   * A checksum nobody set is a checksum nobody checked.
   */
  sha256: string;
  /**
   * Where it unpacks.
   *
   * A versioned path — `/opt/thing/1.4.2` — keeps a failed install from replacing a working one and
   * makes a rollback a matter of repointing `link`. A plain path is right for software that manages
   * its own versions underneath, which is most of what belongs here.
   */
  prefix: string;
  /** A stable path symlinked at `prefix`, so everything else refers to one place across upgrades. */
  link?: string;
  /**
   * Something under the install that has to exist and be executable for it to count as installed.
   *
   * The middle rung of the read. Without it, installed means the directory is there, which cannot
   * tell a half-extracted tree from a good one. A relative path is taken from `link` when there is
   * one and `prefix` otherwise.
   */
  binary?: string;
  /**
   * Something that exits zero when the install works, whose output is discarded.
   *
   * The strongest rung, and the only one that asks about the *effect* rather than about the input.
   * A path test and an executable bit both pass for a half-unpacked tree, a binary built for
   * another architecture, and a missing interpreter — which is exactly what a corrupted download
   * looks like. Running the thing tells those apart, and unlike a version comparison it stays true
   * across every update the software makes to itself.
   *
   * `{}` expands to the link if there is one and the prefix otherwise, so `{}/bin/widget --version`
   * is the usual shape. A non-zero exit is an **answer** — installed but broken, so install it
   * again — rather than a fault, which is why this rides in the same `ask` as everything else here.
   * It only has to exit zero when the program is healthy: much weaker than having to print a
   * version in a shape something can parse.
   */
  healthCommand?: string;
  /**
   * How to ask the software what version it is, purely so `pulumi stack` can show it.
   *
   * **Reported, never compared, and never a reason to do anything.** Failing to obtain it is not an
   * error: the software may not answer, may not run on this architecture, may have no such flag.
   * `{}` is replaced by the link if there is one and the prefix otherwise.
   */
  versionCommand?: string;
  /**
   * A pattern with one capture group, matched against what `versionCommand` prints.
   *
   * Defaults to the first version-shaped token, which is right for the usual `thing 1.4.2 (abc123)`
   * and wrong often enough to be worth overriding.
   */
  versionPattern?: string;
  /** Leading path components to strip. Some archives carry a top-level directory and some do not. */
  strip?: number;
  /** `tar.gz`, `tar.xz`, `tar.bz2` or `zip`. Inferred from the url when not given. */
  format?: ArchiveFormat;
}

interface ArchiveState {
  name: string;
  url: string;
  sha256: string;
  prefix: string;
  link: string;
  binary: string;
  healthCommand: string;
  strip: number;
  format: ArchiveFormat;
  versionCommand: string;
  versionPattern: string;
  /** Whether it is there — the read, and the only thing this resource acts on. */
  installed: boolean;
  /**
   * What the software says it is, or the empty string when it could not be asked.
   *
   * Information for whoever is reading the stack. Nothing compares it, and nothing is reinstalled
   * because of it.
   */
  version: string;
}

const DEFAULTS = { strip: 0, versionPattern: '([0-9]+\\.[0-9]+(?:\\.[0-9]+)?[^\\s]*)' };

/** What kind of archive a url names, by its extension. */
export function formatOf(url: string): ArchiveFormat | null {
  // the query string carries signatures and expiry on most release hosts, and `.tar.gz?X=Y` is not
  // a name this would otherwise recognise
  const path = url.split('?')[0] ?? url;
  if (/\.(tar\.gz|tgz)$/i.test(path)) return 'tar.gz';
  if (/\.(tar\.xz|txz)$/i.test(path)) return 'tar.xz';
  if (/\.(tar\.bz2|tbz2?)$/i.test(path)) return 'tar.bz2';
  if (/\.zip$/i.test(path)) return 'zip';
  return null;
}

/**
 * The command that unpacks one archive into a directory.
 *
 * `zip` is the odd one: `unzip` has no `--strip-components`, so a zip carrying a top-level directory
 * has to be unpacked and then moved. Rather than pretend otherwise, a non-zero strip on a zip is
 * refused — silently ignoring it would put everything one level below the paths that reference it,
 * and the failure would surface as a missing binary rather than as a bad argument.
 */
export function extractCommand(format: ArchiveFormat, file: string, into: string, strip = 0): string {
  const source = shellQuote(file);
  const target = shellQuote(into);
  if (format === 'zip') {
    if (strip !== 0) {
      throw new Error(`unzip has no --strip-components, so strip: ${strip} cannot be honoured for a zip`);
    }
    return `unzip -q ${source} -d ${target}`;
  }
  const flag = { 'tar.gz': '-z', 'tar.xz': '-J', 'tar.bz2': '-j' }[format];
  const stripping = strip > 0 ? ` --strip-components=${strip}` : '';
  return `tar ${flag} -xf ${source} -C ${target}${stripping}`;
}

/**
 * Download, verify, unpack, and only then move the link.
 *
 * The order is the substance:
 *
 *  1. into a private `mktemp -d`, because a predictable path in a world-writable directory is a
 *     file somebody else can swap between the checksum passing and the unpack reading it;
 *  2. verify **before** unpacking, so a mismatch never reaches the filesystem;
 *  3. unpack into a *staging* directory and move it into place, so a half-extracted tree is never
 *     visible at the path things refer to;
 *  4. repoint the link last — the one step that changes what anything else sees;
 *  5. clean up through a trap, so a rejected download is not left behind to be found later and
 *     trusted.
 */
export function installScript(args: {
  url: string;
  sha256: string;
  prefix: string;
  link?: string;
  strip?: number;
  format: ArchiveFormat;
}): string {
  const prefix = shellQuote(args.prefix);
  const steps = [
    'set -e',
    'dir=$(mktemp -d)',
    'trap "rm -rf \\"$dir\\"" EXIT',
    `curl -fsSL --retry 3 -o "$dir/archive" ${shellQuote(args.url)}`,
    `echo ${shellQuote(args.sha256)}"  $dir/archive" | sha256sum -c - >/dev/null`,
    'mkdir -p "$dir/unpacked"',
    // the paths inside the staging directory are shell variables and must not be quoted as literals
    extractCommand(args.format, '$dir/archive', '$dir/unpacked', args.strip).replace(/'(\$dir[^']*)'/g, '"$1"'),
    // the move is what makes the install atomic: everything above happens where nothing looks
    `rm -rf ${prefix}`,
    `mkdir -p "$(dirname ${prefix})"`,
    `mv "$dir/unpacked" ${prefix}`,
  ];
  if (args.link !== undefined && args.link !== '') {
    // -n as well as -f: without it, relinking a link that points at a directory creates the new link
    // *inside* that directory rather than replacing it
    steps.push(`ln -sfn ${prefix} ${shellQuote(args.link)}`);
  }
  return steps.join('; ');
}

/** Where the install is looked for: the stable link when there is one, the prefix otherwise. */
export function whereOf(args: { prefix: string; link?: string }): string {
  return args.link !== undefined && args.link !== '' ? args.link : args.prefix;
}

/** An absolute path for `binary`, which is allowed to be given relative to the install. */
export function binaryPath(where: string, binary: string): string {
  if (binary === '') return '';
  if (binary.startsWith('/')) return binary;
  return `${where.replace(/\/+$/, '')}/${binary.replace(/^\.?\/+/, '')}`;
}

const VERSION_MARKER = '#pulumi-homelab#version';

/**
 * `{}` stands for wherever the install ended up.
 *
 * A command here almost always has to name an absolute path — what was just unpacked is very often
 * not on `PATH` when the read runs, and asking by bare name would answer about a different copy of
 * the program or about nothing at all.
 */
export function substitute(command: string, where: string): string {
  return command.replace(/\{\}/g, where);
}

/**
 * One command that answers both halves of the read.
 *
 * The version half rides along in the same round trip rather than being asked separately, because
 * it is optional information and not worth a second connection. `|| true` throughout: a binary that
 * will not run on this architecture, a program with no version flag, and a program that prints its
 * version to stderr are all ordinary, and none of them is a fault.
 */
export function probeCommand(
  where: string,
  binary: string,
  healthCommand: string,
  versionCommand: string,
): string {
  const tests = [`test -d ${shellQuote(where)}`];
  if (binary !== '') tests.push(`test -x ${shellQuote(binary)}`);
  // both streams discarded: the question is whether it runs, and a program that greets stdout or
  // warns on stderr while exiting zero is a working program
  if (healthCommand !== '') tests.push(`{ ${substitute(healthCommand, where)} ; } >/dev/null 2>&1`);
  const version = versionCommand === '' ? '' : `; ${substitute(versionCommand, where)} 2>&1 || true`;
  return `{ ${tests.join(' && ')} && echo present || true; }; echo '${VERSION_MARKER}'${version}`;
}

/** The version out of whatever the software printed, or the empty string. */
export function parseVersion(out: string, pattern = DEFAULTS.versionPattern): string {
  try {
    return out.match(new RegExp(pattern))?.[1] ?? '';
  } catch {
    // a pattern that does not compile is a mistake in the program rather than an answer about the
    // machine, and reporting no version keeps it out of the way of everything that matters
    return '';
  }
}

/** What the probe said, split back into the two things it answers. */
export function parseProbe(out: string, pattern = DEFAULTS.versionPattern): { installed: boolean; version: string } {
  const [presence = '', printed = ''] = out.split(`${VERSION_MARKER}\n`);
  return { installed: presence.trim() === 'present', version: parseVersion(printed, pattern) };
}

/** What to fetch and where to put it. A change to any of it is the one reason to install again. */
export interface ArchiveSource {
  url: string;
  sha256: string;
  prefix: string;
  link: string;
  strip: number;
  format: ArchiveFormat;
}

/**
 * Whether the declared source changed.
 *
 * Version is not in it and cannot be: these are the fields that say *what to fetch and where to put
 * it*, so a change to one means the machine holds something the program no longer describes. A
 * newer version the software installed for itself changes none of them.
 */
export function sourceChanged(old: ArchiveSource, wanted: ArchiveSource): boolean {
  return old.url !== wanted.url
    || old.sha256 !== wanted.sha256
    || old.prefix !== wanted.prefix
    || old.link !== wanted.link
    || old.strip !== wanted.strip
    || old.format !== wanted.format;
}

/**
 * Whether to fetch and unpack.
 *
 * Absent is always yes. Present is yes only when the program's own description of the source
 * changed — on a first apply there is no previous description, so something already installed is
 * left exactly as it is rather than overwritten with the bootstrap it has since moved past.
 */
export function installNeeded(installed: boolean, old: ArchiveSource | null, wanted: ArchiveSource): boolean {
  if (!installed) return true;
  if (old === null) return false;
  return sourceChanged(old, wanted);
}

/** Is it there, and what does it say it is. */
export async function readArchive(
  host: Target,
  where: string,
  binary = '',
  healthCommand = '',
  versionCommand = '',
  versionPattern = DEFAULTS.versionPattern,
): Promise<{ installed: boolean; version: string }> {
  // `ask`, not `must`: every way this can fail is an answer about the machine rather than a fault
  const asked = await ask(host, escalate(host, probeCommand(where, binary, healthCommand, versionCommand)));
  return parseProbe(asked.out, versionPattern);
}

/** Everything the arguments settle to, with the defaults applied exactly once. */
export function resolveArgs(args: ArchiveArgs): ArchiveSource & {
  name: string;
  binary: string;
  healthCommand: string;
  versionCommand: string;
  versionPattern: string;
  where: string;
} {
  const format = args.format ?? formatOf(args.url);
  if (format === null) {
    throw new Error(
      `cannot tell what kind of archive ${args.url} is from its name — `
      + `give format: 'tar.gz' | 'tar.xz' | 'tar.bz2' | 'zip'`,
    );
  }
  const link = args.link ?? '';
  const where = whereOf({ prefix: args.prefix, link });
  return {
    name: args.name,
    url: args.url,
    sha256: args.sha256,
    prefix: args.prefix,
    link,
    strip: args.strip ?? DEFAULTS.strip,
    format,
    binary: binaryPath(where, args.binary ?? ''),
    healthCommand: args.healthCommand ?? '',
    versionCommand: args.versionCommand ?? '',
    versionPattern: args.versionPattern ?? DEFAULTS.versionPattern,
    where,
  };
}

function providerFor(host: Target): pulumi.dynamic.ResourceProvider<ArchiveArgs, ArchiveState> {
  const settle = async (args: ArchiveArgs, previous: ArchiveSource | null): Promise<ArchiveState> => {
    const wanted = resolveArgs(args);
    const before = await readArchive(
      host, wanted.where, wanted.binary, wanted.healthCommand, wanted.versionCommand, wanted.versionPattern,
    );

    if (installNeeded(before.installed, previous, wanted)) {
      await must(host, escalate(host, installScript(wanted)));
      const after = await readArchive(
      host, wanted.where, wanted.binary, wanted.healthCommand, wanted.versionCommand, wanted.versionPattern,
    );
      if (!after.installed) {
        throw new Error(
          `unpacked ${args.url} to ${wanted.prefix} on ${describe(host)} but ${wanted.binary || wanted.where} `
          + `is still not there — check strip, or whether the archive carries a top-level directory`,
        );
      }
      return { ...wanted, installed: true, version: after.version };
    }
    return { ...wanted, installed: before.installed, version: before.version };
  };

  return {
    async create(args) {
      return { id: args.name, outs: await settle(args, null) };
    },

    async read(id, state) {
      const where = state?.link || state?.prefix || '';
      const actual = await readArchive(
        host, where, state?.binary ?? '', state?.healthCommand ?? '',
        state?.versionCommand ?? '', state?.versionPattern,
      );
      // nothing there: Pulumi forgets it, and the next up installs it back
      if (!actual.installed) return { id: undefined, props: undefined };
      return {
        id,
        props: {
          url: state?.url ?? '',
          sha256: state?.sha256 ?? '',
          prefix: state?.prefix ?? '',
          link: state?.link ?? '',
          binary: state?.binary ?? '',
          healthCommand: state?.healthCommand ?? '',
          strip: state?.strip ?? DEFAULTS.strip,
          format: state?.format ?? 'tar.gz',
          versionCommand: state?.versionCommand ?? '',
          versionPattern: state?.versionPattern ?? DEFAULTS.versionPattern,
          ...state,
          name: id,
          // the two that always come from the machine rather than from what was remembered
          installed: true,
          version: actual.version,
        },
      };
    },

    async update(id, old, args) {
      return { outs: await settle({ ...args, name: id }, old) };
    },

    async diff(_id, old, args) {
      const wanted = resolveArgs(args);
      return {
        changes: transportChanged(old)
          // gone is the drift this resource acts on. A version that has moved on is not drift at
          // all, which is why nothing here looks at one
          || !old.installed
          || sourceChanged(old, wanted)
          // a stricter read is a stricter question, and the answer to it has not been asked yet
          || old.binary !== wanted.binary
          || old.healthCommand !== wanted.healthCommand,
        // a different prefix is a different install rather than an upgrade of this one, and leaving
        // the old tree behind with nothing pointing at it is worse than replacing it
        replaces: old.prefix !== wanted.prefix ? ['prefix'] : [],
        stables: [],
        deleteBeforeReplace: false,
      };
    },

    async delete(id, state) {
      const prefix = state.prefix || '';
      if (prefix === '') throw new Error(`cannot delete ${id}: state carries no prefix to remove`);
      // the link goes only if it still points here: a replacement repoints it at the new prefix
      // first, and deleting the old resource must not take the new one's link with it
      const unlink = state.link
        ? `; [ "$(readlink ${shellQuote(state.link)} 2>/dev/null)" = ${shellQuote(prefix)} ] `
          + `&& rm -f ${shellQuote(state.link)} || true`
        : '';
      await must(host, escalate(host, `rm -rf ${shellQuote(prefix)}${unlink}`));
    },
  };
}

/** Software from a verified archive, installed once and thereafter left to manage itself. */
export class Archive extends pulumi.dynamic.Resource {
  /** Whether it is there. The read, and the only thing this resource acts on. */
  declare readonly installed: pulumi.Output<boolean>;
  /** What the software says it is, when it can be asked. Reported and never compared. */
  declare readonly version: pulumi.Output<string>;

  constructor(name: string, host: Target, args: ArchiveArgs, opts?: pulumi.CustomResourceOptions) {
    super(stamped(providerFor(host)), name, {
      link: '',
      binary: '',
      healthCommand: '',
      strip: DEFAULTS.strip,
      versionCommand: '',
      versionPattern: DEFAULTS.versionPattern,
      format: undefined,
      installed: undefined,
      version: undefined,
      ...args,
    }, withLegacyAlias(opts), 'homelab', 'Archive');
  }
}
