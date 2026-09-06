/**
 * Prove a provider built on this transport survives the trip into the state file.
 *
 * The layer of verification this package was missing, and it cost a real failure to find. A
 * typecheck proves the code is well typed; the unit tests prove the parsing and the command
 * building are right; importing the package proves it loads. None of the three exercises the one
 * thing Pulumi does that nothing else does: it **serialises a dynamic provider's whole closure into
 * the state file**, so everything a provider function can reach has to be serialisable.
 *
 * What that missed was `promisify(execFile)` — perfectly typed, fully tested, imported without
 * complaint, and on current Node built on `Promise.withResolvers`, which is native code the
 * serialiser cannot capture. Every resource here goes through `ask`, so every resource failed, and
 * the first `pulumi preview` died before opening a connection with an error mentioning
 * `bound withResolvers` and nothing at all about ssh.
 *
 * **This deliberately does not run under vitest.** Vite's SSR transform rewrites both static and
 * dynamic imports into captured variables (`__vite_ssr_dynamic_import__`), which is exactly the
 * shape the serialiser rejects — so a vitest version of this check fails on correct code and proves
 * nothing about the real thing. It has to run the way Pulumi runs it: plain node, real modules.
 */
import * as pulumi from '@pulumi/pulumi';
import { ask, asRoot, escalate, must, shellQuote, heredoc, heredocInto } from '../src/ssh.ts';
import { normaliseMode } from '../src/mode.ts';
import { mountedAt } from '../src/checks.ts';
import { audit } from '../src/audit.ts';

const host = { address: '198.51.100.1', user: 'nobody' };

// shaped like the real thing: a closure over a provider whose methods reach the transport, which is
// exactly what every resource in this package hands to pulumi.dynamic.Resource
const provider: pulumi.dynamic.ResourceProvider<{ path: string }, { path: string }> = {
  async create(args) {
    const asked = await ask(host, asRoot(`test -f ${shellQuote(args.path)}`));
    return { id: args.path, outs: { path: `${asked.code}` } };
  },
  async read(id) {
    await must(host, heredoc(id, 'x'));
    return { id, props: { path: id } };
  },
};

// every helper a resource might reach, so one acquiring a native dependency in a later refactor
// fails here rather than on somebody's first deployment
const reachable = { ask, must, asRoot, shellQuote, heredoc, heredocInto, normaliseMode, mountedAt, audit };

const checks: [string, () => unknown][] = [
  ['a provider using the transport', () => provider],
  ['every exported helper', () => reachable],
];

let failed = false;

/**
 * Serialise a closure, write it out, load it back, and **call it**.
 *
 * Serialising successfully is not the same as surviving serialisation, and the k3s session found
 * the difference the hard way: two of its providers serialised without complaint and then failed
 * against a real machine with `Failed to parse URL from [object Object]`. The candidate cause was a
 * local helper named `fetch` inside the provider factory — when the closure is evaluated again from
 * the state file, a binding that did not survive does not raise, it resolves to the *global* of the
 * same name, which answers with a message leading nowhere near the resource.
 *
 * So the useful assertion is that the revived closure reaches ssh. The address is unroutable, so
 * "cannot reach" is the success condition: it proves every binding on the path from the provider
 * method down through `escalate`, `shellQuote` and `ask` survived the trip. Anything else — a
 * ReferenceError, a global answering in place of a local — fails here rather than on a machine.
 */
async function survivesRoundTrip(what: string, closure: () => unknown): Promise<boolean> {
  const { writeFile, mkdtemp } = await import('node:fs/promises');
  const { tmpdir } = await import('node:os');
  const { join } = await import('node:path');
  const { createRequire } = await import('node:module');

  const serialised = await pulumi.runtime.serializeFunction(closure);
  const directory = await mkdtemp(join(tmpdir(), 'pulumi-homelab-'));
  // .cjs because Pulumi writes a CommonJS module body, assigning to `exports`
  const file = join(directory, 'revived.cjs');
  await writeFile(file, serialised.text);

  const revived = createRequire(import.meta.url)(file)[serialised.exportName]() as {
    create: (args: { path: string }) => Promise<unknown>;
  };

  try {
    await revived.create({ path: '/tmp/pulumi-homelab-does-not-exist' });
    console.error(`  FAIL ${what}: the revived provider reached a machine it should not have`);
    return false;
  } catch (error) {
    const said = (error as Error).message;
    // 192.0.2.0/24 is TEST-NET-1 and is guaranteed not to route anywhere
    if (said.includes('cannot reach')) {
      console.log(`  ok   ${what}`);
      return true;
    }
    console.error(`  FAIL ${what}: revived, but did not reach ssh — ${said}`);
    return false;
  }
}

/**
 * The package's own entry point, loaded through Node's real ESM loader.
 *
 * Cheap, and it catches a whole class on its own: a relative import that resolves happily under
 * both `tsc --noEmit` and vitest, and that Node then refuses — leaving a package where every check
 * is green and Pulumi cannot run the program at all. The k3s provider shipped exactly that and a
 * consumer found it. The rest of this file imports individual modules, so it would miss a bad path
 * in one nothing here happens to reach; importing the index reaches everything the package exports.
 */
try {
  const surface = await import('../src/index.ts');
  console.log(`  ok   the package loads through node's own loader (${Object.keys(surface).length} exports)`);
} catch (error) {
  failed = true;
  console.error(`  FAIL the package does not load: ${(error as Error).message}`);
}
for (const [what, closure] of checks) {
  try {
    const serialised = await pulumi.runtime.serializeFunction(closure);
    console.log(`  ok   ${what} (${serialised.text.length} bytes)`);
  } catch (error) {
    failed = true;
    console.error(`  FAIL ${what}\n${(error as Error).message}`);
  }
}
// the round trip, on a provider shaped like the real ones
const unroutable = { address: '192.0.2.1', user: 'nobody', timeout: 1 };
const roundTripped: pulumi.dynamic.ResourceProvider<{ path: string }, { path: string }> = {
  async create(args) {
    // the whole path a real resource takes: escalate, quote, ask
    await must(unroutable, escalate(unroutable, `test -f ${shellQuote(args.path)}`));
    return { id: args.path, outs: { path: args.path } };
  },
};
if (!(await survivesRoundTrip('a provider still works after being read back', () => roundTripped))) failed = true;

/**
 * And the guard on the guard: a check that cannot fail proves nothing.
 *
 * This provider reaches a native bound function, which is the shape that broke every resource in
 * this package once already. If serialisation stops rejecting it, the rest of this file has stopped
 * meaning anything and would go on printing `ok` for ever.
 */
const nativeCapture = Promise.withResolvers.bind(Promise);
const broken = { async create() { nativeCapture(); return { id: '1', outs: {} }; } };
try {
  await pulumi.runtime.serializeFunction(() => broken);
  failed = true;
  console.error('  FAIL the check no longer detects an unserialisable closure');
} catch {
  console.log('  ok   an unserialisable closure is still rejected');
}

/**
 * No local inside a provider factory may shadow a name that outlives it.
 *
 * This is the check the round trip cannot be relied on to make, and it guards a failure that is
 * invisible until a real deployment. When a provider object is built by a factory and the *result*
 * is captured — `super(providerFor(host), …)`, which is how every resource in this package is
 * written — the factory's local bindings do not survive revival. **A lost binding does not raise.**
 * The name resolves to whatever else is in scope by then, and there are two of those:
 *
 * - a **global** of the same name — a helper called `fetch` becomes *the* `fetch`, answering
 *   `Failed to parse URL from /etc/fstab` with nothing to lead anybody back to the resource;
 * - a **module-scope binding** of the same name, which *is* captured and therefore survives — so a
 *   local `const FSTAB` shadowing the module's `FSTAB` silently swaps one value for another, and
 *   the resource goes on working against the wrong path.
 *
 * The second is the quieter of the two and was found by adding path arguments to six resources:
 * three of them wanted a parameter named after a module constant. TypeScript caught those as
 * duplicate identifiers because they collided in the same scope; a *nested* one would compile.
 *
 * Demonstrated in this environment, and shape-dependent rather than version-dependent: serialising
 * `() => providerFactory()` keeps the locals, while serialising a captured provider object loses
 * them. This package uses the second shape everywhere.
 *
 * Indentation stands in for scope, which is crude and right often enough: a declaration at column
 * zero is module scope and survives, one inside a function does not.
 */
const globals = new Set(Object.getOwnPropertyNames(globalThis));
const shadows: string[] = [];
{
  const { readdir, readFile } = await import('node:fs/promises');
  const { join } = await import('node:path');
  const directory = new URL('../src/resources/', import.meta.url).pathname;
  for (const entry of await readdir(directory)) {
    if (!entry.endsWith('.ts') || entry.endsWith('.test.ts')) continue;
    const source = await readFile(join(directory, entry), 'utf8');
    const lines = source.split('\n');

    // module scope is column zero: these survive revival, so a local of the same name is a value
    // silently replaced rather than a name that goes missing
    const moduleScope = new Set<string>();
    for (const line of lines) {
      const top = line.match(/^(?:export\s+)?(?:const|let|var|function|async function|class)\s+([A-Za-z_$][\w$]*)/);
      if (top?.[1]) moduleScope.add(top[1]);
    }

    lines.forEach((line, at) => {
      const declared = line.match(/^\s+(?:const|let|var|function|async function)\s+([A-Za-z_$][\w$]*)/);
      const name = declared?.[1];
      if (!name) return;
      if (globals.has(name)) {
        shadows.push(`${entry}:${at + 1} declares '${name}', which is also a global`);
      } else if (moduleScope.has(name)) {
        shadows.push(`${entry}:${at + 1} declares '${name}', which also exists at module scope`);
      }
    });
  }
}
if (shadows.length > 0) {
  failed = true;
  console.error(`  FAIL a provider local shadows something that outlives it:\n    ${shadows.join('\n    ')}`);
} else {
  console.log('  ok   no provider local shadows a global or a module binding');
}

/**
 * Every `diff` must consider whether the provider itself changed.
 *
 * A resource that forgets this is one whose existing instances never receive another transport fix
 * — not loudly, but by reporting `changes: false` because the machine matches, which is correct
 * about the resource and wrong about the code running it. There is no way to notice from the
 * outside: the stack simply keeps an old `ssh.ts` for ever.
 *
 * So it is checked rather than remembered, because the next resource added to this package will be
 * written by copying one of the existing ones and the line is easy to lose.
 */
const forgotten: string[] = [];
{
  const { readdir, readFile } = await import('node:fs/promises');
  const { join } = await import('node:path');
  const directory = new URL('../src/resources/', import.meta.url).pathname;
  for (const entry of await readdir(directory)) {
    if (!entry.endsWith('.ts') || entry.endsWith('.test.ts')) continue;
    const source = await readFile(join(directory, entry), 'utf8');
    const diffs = (source.match(/async diff\(/g) ?? []).length;
    const checks = (source.match(/providerChanged\(old, args\)/g) ?? []).length;
    if (diffs > checks) forgotten.push(`${entry} has ${diffs} diff(s) and ${checks} provider check(s)`);
  }
}
if (forgotten.length > 0) {
  failed = true;
  console.error(`  FAIL a diff would swallow a provider upgrade:\n    ${forgotten.join('\n    ')}`);
} else {
  console.log('  ok   every diff notices a provider upgrade');
}

/**
 * Every resource class must declare its own type, and must carry the legacy alias with it.
 *
 * The type is what makes a URN distinct: without it every resource here was
 * `pulumi-nodejs:dynamic:Resource` and the name was the only discriminator, so a `User` and a
 * `SystemdUnit` that happened to share a name collided. The alias is what stops adding the type
 * from reading as delete-and-create — which for this provider purges packages and removes unit
 * files. A resource that has one without the other is worse than one that has neither.
 */
const untyped: string[] = [];
{
  const { readdir, readFile } = await import('node:fs/promises');
  const { join } = await import('node:path');
  const directory = new URL('../src/resources/', import.meta.url).pathname;
  for (const entry of await readdir(directory)) {
    if (!entry.endsWith('.ts') || entry.endsWith('.test.ts')) continue;
    const source = await readFile(join(directory, entry), 'utf8');
    const supers = (source.match(/super\((?:provider|userProvider)For\(host\)/g) ?? []).length;
    const typed = (source.match(/withLegacyAlias\([\s\S]*?'homelab', '[A-Za-z]+'\)/g) ?? []).length;
    if (supers > typed) untyped.push(`${entry}: ${supers} resource class(es), ${typed} typed with an alias`);
  }
}
if (untyped.length > 0) {
  failed = true;
  console.error(`  FAIL a resource has no type, or a type with no alias:\n    ${untyped.join('\n    ')}`);
} else {
  console.log('  ok   every resource declares a type and carries the legacy alias');
}

console.log(failed ? 'package checks: FAILED' : 'package checks: 8 passed');
process.exit(failed ? 1 : 0);
