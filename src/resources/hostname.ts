import * as pulumi from '@pulumi/pulumi';
import { escalate, ask, heredoc, must, shellQuote, type Target, describe } from '../ssh.ts';
import { stamped, transportChanged, withLegacyAlias } from '../upgrade.ts';

/**
 * The machine's name, in both places it is written.
 *
 * **One resource rather than two, because a machine whose two names disagree fails in ways that
 * never mention either file.** `sudo` becomes slow, because it resolves the hostname and waits for
 * a lookup that will not answer. Some daemons bind to the wrong name. The symptoms read as DNS
 * faults, and nothing in them points at `/etc/hosts`. Splitting this into a `Hostname` and a
 * `ManagedFile` would let a deployment leave a machine in exactly that state, with both resources
 * reporting success.
 *
 * So it owns:
 *
 * - the static hostname, set through `hostnamectl` — the systemd route, which writes
 *   `/etc/hostname` *and* announces the change on the bus, so anything listening finds out;
 * - the `127.0.1.1` line in `/etc/hosts`, edited **in place**. Debian's convention is the name on
 *   that line, and the file is never regenerated: it collects hand-added entries nobody remembers
 *   making, and losing one is a machine that stops finding something it has always found.
 *
 * **mDNS is the part that surprises people.** avahi publishes `<hostname>.local`, so the old name
 * keeps answering on the network until avahi re-reads it — and the observable somebody actually
 * checks is whether `oldname.local` has stopped resolving. Whether avahi notices a bus announcement
 * varies, so this restarts it rather than hoping, and only when the name really changed. Restart
 * rather than reload: NetBIOS-style registrations are made at start, and a reload can leave the old
 * name published while every command reports success.
 */
export interface HostnameArgs {
  /** The name, without a domain: `homelab`, not `homelab.local`. */
  name: string;
  /**
   * Also written to the `127.0.1.1` line, before the short name — `homelab.lan homelab`.
   *
   * Debian's own installer writes the fully-qualified name there when it knows one, and some
   * software resolves the hostname expecting to get a FQDN back.
   */
  domain?: string;
  /**
   * Restart avahi when the name changes, so the old `<name>.local` stops answering.
   *
   * On by default, because the usual reason for renaming a machine is that the old name should stop
   * working. `try-restart`, so a machine without avahi is untouched rather than failing.
   */
  restartAvahi?: boolean;
  /** Where the hosts file is. */
  hosts?: string;
}

interface HostnameState {
  name: string;
  domain: string;
  restartAvahi: boolean;
  hosts: string;
  /**
   * The three answers, which can all differ.
   *
   * `static` is what the machine is called across reboots; `transient` is what it is called now,
   * which DHCP can change underneath you; `hostsLine` is what `/etc/hosts` says. The reason this
   * resource exists is that those disagreeing is a real state with confusing symptoms, so all
   * three are read rather than one being assumed from another.
   */
  effective: {
    static: string;
    transient: string;
    hostsLine: string;
    /**
     * Whether the `127.0.1.1` line names this host at all.
     *
     * The question the diff needs answered, and not the same as "what is the short name on that
     * line". A line may carry an alias shorter than the hostname, or the qualified name in either
     * position, and extracting one name from it to compare made a correct machine report drift on
     * every single run.
     */
    namesHost: boolean;
  };
}

const HOSTS = '/etc/hosts';
const LOOPBACK = '127.0.1.1';
const DEFAULTS = { restartAvahi: true, domain: '' };
const MARK = '#pulumi-homelab#';

/**
 * Every name the `127.0.1.1` line assigns, or an empty list when the file has no such line.
 *
 * All of them rather than one, because **which position holds the short name is a convention, not a
 * rule**. Debian's installer writes `127.0.1.1 host.domain host`, and plenty of machines have
 * `127.0.1.1 host` or the two the other way round. Taking the last word made a file written in the
 * other order report drift on every single run, on a machine that was correct.
 */
export function hostsNames(text: string): string[] {
  for (const line of text.split('\n')) {
    const words = line.trim().split(/\s+/);
    if (words[0] !== LOOPBACK) continue;
    return words.slice(1);
  }
  return [];
}

/**
 * Whether that line names this host.
 *
 * Case-insensitively, because hostnames are: `Homelab` and `homelab` are one name to every resolver
 * that will read this, and treating them as two is drift nobody can fix by editing the file.
 */
export function hostsNamesHost(text: string, name: string): boolean {
  const wanted = name.trim().toLowerCase();
  return hostsNames(text).some((each) => {
    const found = each.toLowerCase();
    // the qualified form counts: `homelab.lan` names the host `homelab`
    return found === wanted || found.startsWith(`${wanted}.`);
  });
}

/** The short name a `127.0.1.1` line assigns, for reporting. */
export function hostsName(text: string): string | null {
  const names = hostsNames(text);
  // the shortest is the unqualified one, whichever order they were written in
  return names.length === 0 ? null : [...names].sort((a, b) => a.length - b.length)[0] ?? null;
}

/**
 * Put the name on the `127.0.1.1` line, leaving every other line alone.
 *
 * Replaced in place where the line exists, appended after the `127.0.0.1` line where it does not —
 * which is where Debian puts it, and putting it at the end of a file that has a block of static
 * entries would read as unrelated to the loopback names above it.
 */
export function setHostsName(text: string, name: string, domain = ''): string {
  const wanted = domain.length > 0 ? `${LOOPBACK}\t${name}.${domain} ${name}` : `${LOOPBACK}\t${name}`;
  const lines = text.split('\n');
  const at = lines.findIndex((line) => line.trim().split(/\s+/)[0] === LOOPBACK);
  if (at >= 0) {
    lines[at] = wanted;
    return lines.join('\n');
  }
  const after = lines.findIndex((line) => line.trim().split(/\s+/)[0] === '127.0.0.1');
  const insertAt = after >= 0 ? after + 1 : lines.length;
  return [...lines.slice(0, insertAt), wanted, ...lines.slice(insertAt)].join('\n');
}

/**
 * The three answers, out of one reply.
 *
 * Its own function because the previous version of this was three lines inside the read and got the
 * splitting wrong in a way no test could see: it used **one marker twice** and then destructured two
 * parts out of the three that `split` produces, so the hosts file was silently always the empty
 * string. Every machine then failed the check that the `127.0.1.1` line names the host — on a
 * machine where the line was perfectly correct — and, because that check throws, the whole
 * deployment aborted rather than merely reporting drift.
 *
 * Distinct markers now, so each split finds exactly one and there is nothing to miscount.
 */
export function parseHostnameOutput(out: string): { static: string; transient: string; hostsFile: string } {
  const [fixed = '', rest = ''] = out.split(`${MARK}now\n`);
  const [now = '', file = ''] = rest.split(`${MARK}hosts\n`);
  return { static: fixed.trim(), transient: now.trim(), hostsFile: file };
}

/** What the machine currently calls itself, in all three places. */
export async function readHostname(
  host: Target,
  hosts = HOSTS,
): Promise<{ static: string; transient: string; hostsLine: string; hostsFile: string }> {
  const asked = await ask(host, escalate(host,
    // --static and plain `hostname` are different questions: DHCP can set a transient name that
    // outlives nothing and explains a machine answering to something nobody configured
    `hostnamectl --static 2>/dev/null || cat /etc/hostname 2>/dev/null || true; ` +
    `echo ${shellQuote(`${MARK}now`)}; hostname 2>/dev/null || true; ` +
    `echo ${shellQuote(`${MARK}hosts`)}; cat ${shellQuote(hosts)} 2>/dev/null || true`,
  ));
  if (asked.code !== 0) throw new Error(`could not read the hostname on ${describe(host)}: ${asked.err.trim()}`);
  const found = parseHostnameOutput(asked.out);
  return {
    ...found,
    hostsLine: hostsName(found.hostsFile) ?? '',
  };
}

function providerFor(host: Target): pulumi.dynamic.ResourceProvider<HostnameArgs, HostnameState> {
  const settle = async (args: HostnameArgs): Promise<HostnameState> => {
    const hosts = args.hosts ?? HOSTS;
    const domain = args.domain ?? DEFAULTS.domain;
    const restartAvahi = args.restartAvahi ?? DEFAULTS.restartAvahi;

    const before = await readHostname(host, hosts);
    const renaming = before.static !== args.name;
    const current = await must(host, escalate(host, `cat ${shellQuote(hosts)} 2>/dev/null || true`));
    const updated = setHostsName(current, args.name, domain);

    if (renaming || updated !== current) {
      const steps = [
        // hostnamectl rather than writing /etc/hostname: it writes the file *and* announces the
        // change, which is what gives anything listening a chance to notice
        ...(renaming ? [`hostnamectl set-hostname ${shellQuote(args.name)}`] : []),
        ...(updated !== current ? [heredoc(hosts, updated)] : []),
      ];
      await must(host, escalate(host, steps.join(' && ')));

      if (renaming && restartAvahi) {
        // try-restart, so a machine without avahi is untouched. Restart rather than reload: a
        // reload can leave the previous name published, and the thing somebody checks is whether
        // the old <name>.local has stopped answering
        await must(host, escalate(host, 'systemctl try-restart avahi-daemon 2>/dev/null || true'));
      }
    }

    const found = await readHostname(host, hosts);
    const effective = {
      static: found.static,
      transient: found.transient,
      hostsLine: found.hostsLine,
      namesHost: hostsNamesHost(found.hostsFile, args.name),
    };
    if (effective.static !== args.name) {
      throw new Error(
        `set the hostname to ${args.name} on ${describe(host)} but it reports ${effective.static || 'nothing'}`,
      );
    }
    // and the other half, which is the whole reason the two live in one resource: a static name
    // that is right while the hosts line names something else is the state with confusing symptoms
    if (!effective.namesHost) {
      throw new Error(
        `set the hostname to ${args.name} on ${describe(host)} but ${hosts} does not name it on the ` +
        `${LOOPBACK} line — the two disagreeing is what makes sudo slow and daemons bind wrong`,
      );
    }
    return { name: args.name, domain, restartAvahi, hosts, effective };
  };

  return {
    async create(args) {
      return { id: args.hosts ?? HOSTS, outs: await settle(args) };
    },

    async read(id, state) {
      const found = await readHostname(host, id);
      const named = state?.name ?? found.static;
      const effective = {
        static: found.static,
        transient: found.transient,
        hostsLine: found.hostsLine,
        namesHost: hostsNamesHost(found.hostsFile, named),
      };
      // there is always a hostname, so this never reports the resource gone — a machine called
      // something else is drift rather than a resource that has stopped existing
      return {
        id,
        props: {
          name: state?.name ?? effective.static,
          domain: state?.domain ?? DEFAULTS.domain,
          restartAvahi: state?.restartAvahi ?? DEFAULTS.restartAvahi,
          ...state,
          hosts: id,
          effective,
        },
      };
    },

    async update(id, _old, args) {
      return { outs: await settle({ ...args, hosts: id }) };
    },

    async diff(_id, old, args) {
      const domain = args.domain ?? DEFAULTS.domain;
      return {
        // compared against what the machine says rather than against the last arguments, and
        // against both halves: the two files disagreeing is the state this resource exists to stop
        // hostnames are case-insensitive, and the hosts line is checked for *naming* the host
        // rather than for holding it in a particular position — taking the last word on the line
        // made a file written `127.0.1.1 host host.domain` report drift on every run for ever
        changes: transportChanged(old)
          || old.effective?.static?.toLowerCase() !== args.name.toLowerCase()
          // whether the line *names* the host, not which name was extracted from it
          || old.effective?.namesHost !== true
          || old.name !== args.name
          || old.domain !== domain,
        replaces: [],
        stables: [],
        deleteBeforeReplace: false,
      };
    },

    async delete() {
      // nothing. A machine has a name whether or not a program describes it, and reverting to some
      // earlier one would be inventing a name rather than removing a resource.
    },
  };
}

/** The machine's name, in `/etc/hostname` and `/etc/hosts`, which have to agree. */
export class Hostname extends pulumi.dynamic.Resource {
  declare readonly name: pulumi.Output<string>;
  /** The static name, the transient name, and what `/etc/hosts` says — which can all differ. */
  declare readonly effective: pulumi.Output<{ static: string; transient: string; hostsLine: string }>;

  constructor(name: string, host: Target, args: HostnameArgs, opts?: pulumi.CustomResourceOptions) {
    super(stamped(providerFor(host)), name, {
      domain: DEFAULTS.domain,
      restartAvahi: DEFAULTS.restartAvahi,
      hosts: HOSTS,
      effective: undefined,
      ...args,
    }, withLegacyAlias(opts), 'homelab', 'Hostname');
  }
}
