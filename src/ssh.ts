/**
 * `execFile` as a promise — written out by hand, and importing child_process *inside* the function.
 *
 * Both halves of that are forced by the same thing, and it is worth writing down because nothing
 * about the code looks like it matters. Pulumi serialises a dynamic provider's entire closure into
 * the state file, so **everything a provider function can reach has to be serialisable**, and the
 * serialiser walks every captured variable to find out.
 *
 * - `promisify(execFile)` fails: on current Node, `promisify` is built on
 *   `Promise.withResolvers`, which is native code and cannot be captured.
 * - Importing `execFile` at the top of this file and calling it here fails too, for the same reason
 *   one level further down — the serialiser follows the captured binding into child_process and
 *   reaches `ArrayPrototypeSlice` inside `normalizeExecFileArgs`.
 *
 * A dynamic `import()` in the body is neither of those. It is syntax rather than a captured
 * variable, so there is nothing for the serialiser to walk into, and it works unchanged under both
 * ESM and CommonJS at runtime — which matters because this package is loaded both ways: by Pulumi,
 * and by anyone running the audit from a plain node script. Node caches the module, so the import
 * costs nothing after the first call.
 *
 * The symptom this prevents is worth recognising: `pulumi preview` failing before it opens a single
 * connection, with an error naming `bound withResolvers` and nothing at all about ssh.
 *
 * Nothing about the contract changes. The rejection carries `stdout` and `stderr` alongside the
 * error's own `code`, because `ask` reads all three — a command that answers "no" on exit 1 still
 * has output worth having, and telling that apart from ssh's own 255 is the whole distinction
 * between an answer and a fault.
 */
function run(
  file: string,
  args: string[],
  options: { maxBuffer: number },
  stdin?: string,
): Promise<{ stdout: string; stderr: string }> {
  return new Promise((resolve, reject) => {
    import('node:child_process').then(({ execFile }) => {
      const child = execFile(file, args, options, (error, stdout, stderr) => {
        if (error) reject(Object.assign(error, { stdout, stderr }));
        else resolve({ stdout, stderr });
      });
      // the password goes down ssh's own stdin to `sudo -S`, so it never appears in the command
      // line and never shows up in `ps` on the machine being managed
      if (stdin !== undefined) child.stdin?.end(stdin);
    }, reject);
  });
}

/**
 * Running a command on the machine being managed.
 *
 * Plain `ssh` rather than a library, because the connection this needs is the one that already
 * works from a terminal: the agent, the known_hosts file, the config in ~/.ssh, all of it. A
 * library would want its own key handling and would then disagree with the shell about whether the
 * host is trusted, which is a bad thing to discover half way through a deployment.
 */
export interface Host {
  /** Where it is. An address rather than a name, because mDNS is the first thing to stop working. */
  address: string;
  user: string;
  /** Which port, when it is not 22 — a tunnel's local end rarely is. */
  port?: number;
  /**
   * A host to reach this one *through*: `-o ProxyJump=…`.
   *
   * `user@jump-host:port`, or several separated by commas for more than one hop. For a machine that
   * is only reachable from somewhere else — behind a reverse tunnel, on a private subnet, through a
   * bastion — which is the same machine and the same stack by a different route.
   *
   * `ProxyJump` rather than `ProxyCommand` because ssh already knows how to chain hops and checks
   * `known_hosts` for *each* one, which is the property this transport exists to inherit: a
   * `ProxyCommand` piping through netcat would silently drop host verification for the far end.
   */
  proxyJump?: string;
  /** Seconds before a silent host is called dead, rather than hanging a deployment for ever. */
  timeout?: number;
  /**
   * How a command becomes root on this machine. Defaults to `'sudo'`.
   *
   * **This exists because the alternative was a lie about what is possible.** With `sudo -n` as the
   * only option, this provider could never write `/etc/sudoers.d` on a machine that did not already
   * have passwordless sudo — so it could adopt a configured machine and never bootstrap a fresh
   * one, which is precisely the case that matters when an SD card has died and the replacement was
   * just flashed. Authentication and privilege escalation are separate steps, and treating them as
   * one made a transport decision look like a law.
   *
   * - `'sudo'` — `sudo -n`, which never prompts and fails instead. The default, and the right
   *   answer on a machine that is already set up: a prompt nobody can answer is a deployment that
   *   hangs for ever, which is the failure `-n` exists to prevent.
   * - `'none'` — the connection is already root, so nothing is prepended. For `root@host`, which
   *   means enabling root ssh login: plenty of people would call that worse than the problem, and
   *   it is offered rather than recommended.
   * - `{ password }` — `sudo -S`, with the password written to ssh's stdin rather than put on the
   *   command line, where every user on the machine could read it out of `ps`. Keep it in Pulumi
   *   config as a secret; it is in the state file either way.
   */
  become?: 'sudo' | 'none' | { password: string };
}

/**
 * How a command reaches a machine.
 *
 * Every resource in this package took a `Host` and therefore assumed ssh. As an interface, the same
 * `ManagedFile`, `SystemdUnit` and `AptPackage` work against a container, a chroot, a host behind a
 * bastion, or the machine the program is running on — without one resource changing.
 *
 * **It is possible only because the implementations ship inside this package.** A provider's
 * closure is serialised into the state file and evaluated again, so only data survives that trip: a
 * consumer can hand a resource `{ address, user }` and cannot hand it an object with methods,
 * however well typed. Inside the package the methods travel with the closure, which is what makes
 * this shape work here and not across the boundary.
 *
 * The immediate use is not hypothetical. A local transport lets this package's own tests exercise
 * resources against a temporary directory rather than against fixtures — and every finding that
 * cost a bug today came through exactly that gap: `sshd -T` printing `without-password`,
 * `systemctl show` answering in its own order, `stat` saying `644`.
 */
export interface Transport {
  ask(command: string): Promise<Ran>;
  /** Wrap a command so it runs as root, however that is done here. */
  escalate(command: string): string;
  /** For error messages: `admin@198.51.100.10`, `local`, `container:abc123`. */
  describe(): string;
}

/** Anything a resource can be pointed at: an ssh host, or a transport of its own. */
export type Target = Host | Transport;

/** Whether this is a transport rather than the ssh host struct. */
const isTransport = (target: Target): target is Transport =>
  typeof (target as Transport).ask === 'function';

/** What to call the machine in an error message. */
export function describe(target: Target): string {
  if (isTransport(target)) return target.describe();
  // the route matters in an error message: the same machine reached two ways fails differently, and
  // "cannot reach admin@127.0.0.1:2222" without the jump names a destination nobody recognises
  const where = target.port !== undefined ? `${target.user}@${target.address}:${target.port}` : `${target.user}@${target.address}`;
  return target.proxyJump !== undefined ? `${where} via ${target.proxyJump}` : where;
}

/**
 * The ssh transport, which is what a plain `Host` becomes.
 *
 * A factory rather than a class: a plain object of module-scope functions serialises into the state
 * file, and a class instance is a thing the serialiser has to reconstruct rather than a value.
 */
export function sshTransport(host: Host): Transport {
  return {
    ask: (command) => askOver(host, command),
    escalate: (command) => escalateOn(host, command),
    describe: () => describe(host),
  };
}

export interface Ran {
  code: number;
  out: string;
  err: string;
}

const CONNECT_SECONDS = 10;

/**
 * Reuse one ssh connection for every command, instead of opening one per question.
 *
 * A refresh asks every resource at once, and Pulumi runs them in parallel — so twenty-one resources
 * open twenty-one connections within a second or two, and sshd's default `MaxStartups 10:30:100`
 * starts refusing them. The failure arrives as `kex_exchange_identification: read: Connection reset
 * by peer` on most of the run, which reads as a network fault rather than as a limit being hit, and
 * the obvious fix — `--parallel 4` — makes every user of this provider slower to work around a
 * setting on the machine.
 *
 * Multiplexing fixes it at the source. The first command opens a master connection and every
 * command after it travels down the same one, so twenty-one resources cost one handshake rather
 * than twenty-one. That also makes a refresh substantially faster, since a handshake costs far more
 * than any of the work this provider asks a machine to do.
 *
 * `ControlPersist=60s` keeps the master alive briefly after the last command, which is what lets a
 * separate `pulumi refresh` and `pulumi up` moments apart share it. The socket lives in /tmp under a
 * hash of the destination, because a unix socket path has about a hundred characters to work with
 * and a home directory plus a long hostname can exceed it — an error nobody ever reads correctly.
 */
/**
 * The control socket's name, which has to distinguish routes and not only destinations.
 *
 * `%C` is ssh's own hash of the local host, the remote host, the port and the user — and **not the
 * ProxyJump**. So one address reachable two ways, directly and through a bastion, would share a
 * socket: the second connection silently reuses the first one's route, and a deployment aimed at a
 * tunnel goes wherever the master happened to be established. Whichever route was tried first wins
 * for the next sixty seconds, which is the kind of failure that looks like a network fault.
 *
 * So the jump is hashed into the name as well. Short, because a unix socket path has about a
 * hundred characters and `/tmp` plus a hash is the only thing that reliably fits.
 */
function controlPath(host: Host): string {
  const route = host.proxyJump ?? 'direct';
  // a small deterministic hash: the same route must always give the same socket, or multiplexing
  // buys nothing at all
  let hash = 0;
  for (let at = 0; at < route.length; at += 1) {
    hash = (hash * 31 + route.charCodeAt(at)) | 0;
  }
  return `/tmp/pulumi-homelab-${(hash >>> 0).toString(36)}-%C`;
}

const multiplexing = (host: Host): string[] => [
  '-o', 'ControlMaster=auto',
  '-o', `ControlPath=${controlPath(host)}`,
  '-o', 'ControlPersist=60s',
];

/**
 * The arguments ssh is actually given.
 *
 * Its own function so the options that matter can be asserted rather than assumed. Two of the four
 * are there because of a specific failure: `BatchMode` so a machine that wants a password fails
 * instead of waiting for one nobody can type, and the multiplexing so a parallel refresh does not
 * open a connection per resource and trip sshd's startup limit.
 */
export function sshArgs(host: Host, command: string): string[] {
  return [
    '-o', 'BatchMode=yes',
    '-o', `ConnectTimeout=${host.timeout ?? CONNECT_SECONDS}`,
    // before the destination, because ssh reads options in order and a later one does not override
    // an earlier: an option after the host name is not applied to that connection at all
    ...(host.port !== undefined ? ['-p', String(host.port)] : []),
    ...(host.proxyJump !== undefined ? ['-o', `ProxyJump=${host.proxyJump}`] : []),
    ...multiplexing(host),
    `${host.user}@${host.address}`,
    command,
  ];
}

/**
 * Run a command and hand back what happened, including a non-zero exit.
 *
 * A failing command is not automatically an error here, and that is deliberate: half of what this
 * code does is ask questions — is this package installed, does this file say what we think — and
 * the answer "no" arrives as exit code 1. Only the caller knows which failures are answers and
 * which are faults, so the decision belongs to it.
 */
async function askOver(host: Host, command: string): Promise<Ran> {
  const secret = typeof host.become === 'object' ? `${host.become.password}\n` : undefined;
  const args = sshArgs(host, command);
  try {
    const { stdout, stderr } = await run('ssh', args, { maxBuffer: 16 * 1024 * 1024 }, secret);
    return { code: 0, out: stdout, err: stderr };
  } catch (thrown) {
    const failure = thrown as { code?: number; stdout?: string; stderr?: string; message?: string };
    // ssh itself failing to connect is never an answer to a question, so it is worth telling apart
    // from the command running and saying no. 255 is ssh's own "I could not do it" code.
    if (failure.code === 255) {
      throw new Error(`cannot reach ${host.user}@${host.address}: ${(failure.stderr ?? failure.message ?? '').trim()}`);
    }
    return { code: failure.code ?? 1, out: failure.stdout ?? '', err: failure.stderr ?? '' };
  }
}

/** Run a command and insist it worked, for the half of the job that is doing rather than asking. */
export async function must(target: Target, command: string): Promise<string> {
  const ran = await ask(target, command);
  if (ran.code !== 0) {
    throw new Error(`\`${command}\` failed on ${describe(target)} (exit ${ran.code}): ${ran.err.trim() || ran.out.trim()}`);
  }
  return ran.out;
}

/**
 * Wrap a command so it runs as root.
 *
 * Kept in one place because the alternative is every resource deciding for itself, and the day one
 * of them forgets is the day a deployment half-succeeds and leaves the machine in a state no part
 * of this code describes.
 */
export function asRoot(command: string): string {
  return `sudo -n sh -c ${shellQuote(command)}`;
}

/**
 * The same, for a machine that says how it escalates.
 *
 * `asRoot` is the no-host form and assumes `sudo -n`, which is right for the common case and wrong
 * for the case this provider could not previously reach at all. Everything inside this package goes
 * through here instead, so a `Host` that connects as root or carries a sudo password works
 * everywhere rather than in the resources somebody remembered to change.
 */
function escalateOn(host: Host, command: string): string {
  if (host.become === 'none') return command;
  if (typeof host.become === 'object') {
    // -S reads the password from stdin, which `ask` supplies; -p '' stops sudo writing a prompt
    // into stderr where it would end up quoted back in an error message
    return `sudo -S -p '' sh -c ${shellQuote(command)}`;
  }
  return asRoot(command);
}

/**
 * Quote a string so a shell treats it as one argument, whatever is in it.
 *
 * Single quotes are literal in every POSIX shell, and the only character that cannot appear inside
 * them is a single quote — which is closed, escaped and reopened. Everything this code sends to a
 * machine goes through here: file contents, unit definitions, package names off a config file.
 * A missed quote is not a bug that shows up as a wrong answer, it is one that runs somebody else's
 * words as a command.
 */
export function shellQuote(text: string): string {
  return `'${text.replace(/'/g, `'\\''`)}'`;
}

/**
 * Write a file on the machine through a here-document.
 *
 * The delimiter is quoted, which stops the shell expanding anything in the body — without that a
 * `$` in a systemd unit or a config file would be replaced by the empty string on the way in, and
 * the file would arrive subtly wrong rather than obviously broken.
 */
export function heredoc(path: string, content: string): string {
  return heredocInto(shellQuote(path), content);
}

/**
 * The same, for a destination that is a shell expression rather than a literal path.
 *
 * `heredoc` quotes what it is given, which is right for a path and wrong for `"$staging"` — a
 * quoted `$staging` is a file called `$staging`, and the write silently goes somewhere nobody meant.
 * Anything that writes to a temporary file it made with `mktemp` needs this one, and takes on the
 * job of quoting the destination itself.
 */
export function heredocInto(target: string, content: string): string {
  const edge = 'PULUMI_EOF';
  const body = content.endsWith('\n') ? content : `${content}\n`;
  return `cat > ${target} <<'${edge}'\n${body}${edge}`;
}


/**
 * Run a command and hand back what happened, whatever the machine is reached through.
 *
 * The `Host` struct is accepted directly so that every existing call site keeps working: it is
 * turned into an ssh transport here rather than at each of the two hundred places that ask a
 * question.
 */
export async function ask(target: Target, command: string): Promise<Ran> {
  return isTransport(target) ? target.ask(command) : askOver(target, command);
}

/** Wrap a command so it runs as root, by whatever means this target has. */
export function escalate(target: Target, command: string): string {
  return isTransport(target) ? target.escalate(command) : escalateOn(target, command);
}
