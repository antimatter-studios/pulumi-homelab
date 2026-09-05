import { execFile } from 'node:child_process';
import { promisify } from 'node:util';

const run = promisify(execFile);

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
  /** Seconds before a silent host is called dead, rather than hanging a deployment for ever. */
  timeout?: number;
}

export interface Ran {
  code: number;
  out: string;
  err: string;
}

const CONNECT_SECONDS = 10;

/**
 * Run a command and hand back what happened, including a non-zero exit.
 *
 * A failing command is not automatically an error here, and that is deliberate: half of what this
 * code does is ask questions — is this package installed, does this file say what we think — and
 * the answer "no" arrives as exit code 1. Only the caller knows which failures are answers and
 * which are faults, so the decision belongs to it.
 */
export async function ask(host: Host, command: string): Promise<Ran> {
  const args = [
    '-o', 'BatchMode=yes',
    '-o', `ConnectTimeout=${host.timeout ?? CONNECT_SECONDS}`,
    `${host.user}@${host.address}`,
    command,
  ];
  try {
    const { stdout, stderr } = await run('ssh', args, { maxBuffer: 16 * 1024 * 1024 });
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
export async function must(host: Host, command: string): Promise<string> {
  const ran = await ask(host, command);
  if (ran.code !== 0) {
    throw new Error(`\`${command}\` failed on ${host.address} (exit ${ran.code}): ${ran.err.trim() || ran.out.trim()}`);
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
  const edge = 'PULUMI_EOF';
  const body = content.endsWith('\n') ? content : `${content}\n`;
  return `cat > ${shellQuote(path)} <<'${edge}'\n${body}${edge}`;
}
