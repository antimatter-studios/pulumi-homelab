import type { Ran, Transport } from './ssh.ts';

/**
 * A transport that runs commands on the machine the program is running on.
 *
 * The reason it exists is not "sometimes you manage localhost" — it is that **every bug this
 * package has had was found by a real machine and invisible to its tests**. `stat` answering `644`
 * where the code says `0644`; `systemctl show --value` returning properties in systemd's own order;
 * `sshd -T` printing `without-password` for `prohibit-password`; `rclone obscure` never returning
 * the same string twice. Each was a fixture agreeing with the bug, because the fixture was written
 * by the same person who wrote the bug.
 *
 * With this, a test can point `ManagedFile` at a temporary directory and let the *real* `stat` and
 * the *real* shell answer. That does not reach systemd or apt on a developer's laptop, so it is not
 * a substitute for a machine — but it closes the gap for everything that is only files and
 * commands, which is most of the parsing this package does.
 *
 * **It is a Linux transport, and its first run proved it.** These resources read with
 * `stat -c '%a %U %G'`, which is GNU coreutils; BSD `stat` on macOS takes `-f` and rejects `-c`.
 * That is not a bug — this package describes Linux machines — but it was an assumption nothing had
 * written down until a real shell was asked, which is the whole argument for this transport
 * existing.
 *
 * `escalate` is a no-op by default rather than shelling out to sudo. A test suite that asked for
 * root would either prompt or fail, and neither is a test; a caller that genuinely wants privilege
 * locally can say so.
 */
export interface LocalOptions {
  /** Prefix every command with this, for the case where local work does need root. */
  escalateWith?: string;
  /** Run commands from here, so a test can point resources at a temporary directory. */
  cwd?: string;
}

export function localTransport(options: LocalOptions = {}): Transport {
  return {
    async ask(command: string): Promise<Ran> {
      const { execFile } = await import('node:child_process');
      return new Promise<Ran>((resolve, reject) => {
        // `sh -c` rather than running the command directly: everything this package builds is a
        // shell command, with pipes, redirections and `||` in it, and a transport that only ran
        // executables would answer a different question from the ssh one
        execFile('sh', ['-c', command], { maxBuffer: 16 * 1024 * 1024, cwd: options.cwd }, (error, stdout, stderr) => {
          if (!error) return resolve({ code: 0, out: stdout, err: stderr });
          const failure = error as { code?: number; message?: string };
          // a shell that could not be started at all is a fault; a command that exited non-zero is
          // an answer, exactly as it is over ssh
          if (typeof failure.code !== 'number') return reject(new Error(`cannot run locally: ${failure.message ?? ''}`));
          resolve({ code: failure.code, out: stdout, err: stderr });
        });
      });
    },

    escalate(command: string): string {
      return options.escalateWith ? `${options.escalateWith} ${command}` : command;
    },

    describe(): string {
      return options.cwd ? `local:${options.cwd}` : 'local';
    },
  };
}
