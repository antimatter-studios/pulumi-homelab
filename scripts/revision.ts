/**
 * Whether the provider's revision has kept up with the provider.
 *
 * **This exists because the alternative was somebody remembering, and they did not.** Pulumi
 * serialises a dynamic provider's whole closure into the state file, so `read`, `diff` and `update`
 * all run the *stored* code rather than the current source. A resource created last month is still
 * running last month's `read`, and the only thing that makes it pick up a new one is `TRANSPORT`
 * moving.
 *
 * Three merged changes altered what a resource reads — Samba reading the file instead of `testparm`,
 * `SshKey` asserting the mode on both halves, `AptPackages` marking what it declares as manual — and
 * none of them bumped it, because none of them touched the transport. All three were correct,
 * merged, tested, and inert on every machine that already had those resources. It was reported back
 * as "merged and correct, and on my machine it has never executed".
 *
 * So the question is asked mechanically. Every non-test source under `src/` is hashed, and the hash
 * is recorded beside the revision it belongs to. A source change with no bump fails, and the way to
 * satisfy it is to make the decision rather than to silence it:
 *
 * - **bump `TRANSPORT`**, then `pnpm run revision:record` — the change must reach existing resources;
 * - **`pnpm run revision:record --no-bump`** — it must not, and here is a commit saying so.
 *
 * `--no-bump` is the honest escape for a doc comment or a test-only helper. It is deliberately not
 * the default, because the failure it guards against is silent on every machine and obvious on none.
 */
import { createHash } from 'node:crypto';
import { readFile, readdir, writeFile } from 'node:fs/promises';
import { join } from 'node:path';

const RECORD = 'provider-revision.json';

/** Every source that ends up inside the serialised closure, in a stable order. */
async function sources(directory: string): Promise<string[]> {
  const found: string[] = [];
  for (const entry of (await readdir(directory, { withFileTypes: true })).sort((a, b) => a.name.localeCompare(b.name))) {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) found.push(...await sources(path));
    // tests are not serialised, so a test-only change is not a reason to bump anything
    else if (entry.name.endsWith('.ts') && !entry.name.endsWith('.test.ts')) found.push(path);
  }
  return found;
}

/**
 * One hash over every source, path included.
 *
 * The path is hashed as well as the contents, so renaming a file or moving a resource between files
 * counts as a change — which it is, to the closure.
 */
export async function hashSources(directory = 'src'): Promise<string> {
  const digest = createHash('sha256');
  for (const path of await sources(directory)) {
    digest.update(path);
    digest.update(await readFile(path));
  }
  return digest.digest('hex');
}

export async function recorded(): Promise<{ transport: number; sources: string }> {
  return JSON.parse(await readFile(RECORD, 'utf8')) as { transport: number; sources: string };
}

/** Why the record is out of date, or null when it is not. */
export function staleness(
  transport: number,
  hash: string,
  record: { transport: number; sources: string },
): string | null {
  if (record.sources === hash && record.transport === transport) return null;
  if (record.transport !== transport) {
    return `TRANSPORT is ${transport} and ${RECORD} still says ${record.transport}. `
      + `Run \`pnpm run revision:record\`.`;
  }
  return `source under src/ has changed and TRANSPORT is still ${transport}, so the change cannot `
    + `reach any resource that already exists — its state keeps running the stored closure. Either `
    + `bump TRANSPORT in src/upgrade.ts and run \`pnpm run revision:record\`, or, if the change `
    + `genuinely need not reach them, run \`pnpm run revision:record --no-bump\`.`;
}

if (process.argv[1]?.endsWith('revision.ts')) {
  const { TRANSPORT } = await import('../src/upgrade.ts');
  const hash = await hashSources();
  const record = await recorded();
  const recording = process.argv.includes('--record');

  if (!recording) {
    const stale = staleness(TRANSPORT, hash, record);
    if (stale !== null) {
      console.error(`  FAIL provider revision is out of date:\n    ${stale}`);
      process.exit(1);
    }
    console.log(`  ok   provider revision ${TRANSPORT} matches the source it was recorded against`);
    process.exit(0);
  }

  if (record.sources !== hash && record.transport === TRANSPORT && !process.argv.includes('--no-bump')) {
    console.error(
      `  FAIL source changed but TRANSPORT is still ${TRANSPORT}.\n`
      + `    A resource that already exists runs the closure in its own state, so the change reaches\n`
      + `    nothing until the revision moves. Bump TRANSPORT in src/upgrade.ts, or pass --no-bump if\n`
      + `    the change genuinely need not reach existing resources.`,
    );
    process.exit(1);
  }
  await writeFile(RECORD, `${JSON.stringify({ transport: TRANSPORT, sources: hash }, null, 2)}\n`);
  console.log(`  ok   recorded revision ${TRANSPORT}`);
}
