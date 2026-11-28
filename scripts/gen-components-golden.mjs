// Run after building the Node SDK. CI also checks the build against the TypeScript sources.
import { readFileSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
const pulumi = require('@pulumi/pulumi');
const { getMonitor, waitForRPCs } = require('@pulumi/pulumi/runtime/settings');
const { Provider: KubernetesProvider } = require('@pulumi/kubernetes');
const sdk = require('../sdk/nodejs');

const cases = JSON.parse(readFileSync(new URL('../sdk/go/testdata/components-cases.json', import.meta.url), 'utf8'));
const destination = new URL('../sdk/go/testdata/components-golden.json', import.meta.url);
const ignoredNames = new Set(['project-stack', 'machine', 'cluster', 'group-t3code', 'group-kvm']);

async function capture(register) {
  await pulumi.runtime.setMocks({
    newResource({ type, name, inputs }) {
      const state = type === 'kubernetes:source.toolkit.fluxcd.io/v1:GitRepository'
        ? { ...inputs, metadata: { ...inputs.metadata, name: name + '-7f3a91' } }
        : inputs;
      return { id: name + '-id', state };
    },
    call() { return {}; },
  }, 'project', 'stack', false);
  const monitor = getMonitor();
  const original = monitor.registerResource.bind(monitor);
  const resources = [];
  monitor.registerResource = (request, ...args) => {
    const name = request.getName();
    if (!ignoredNames.has(name)) {
      const parent = request.getParent();
      resources.push({
        name, type: request.getType(), inputs: request.getObject()?.toJavaScript() ?? {},
        parent: parent.includes('::pulumi:pulumi:Stack::') ? '' : parent,
        provider: request.getProvider(), dependencies: request.getDependenciesList().slice().sort(),
        version: request.getVersion(), importId: request.getImportid(),
        ignoreChanges: request.getIgnorechangesList(), protect: request.getProtect(),
      });
    }
    return original(request, ...args);
  };
  let error = null;
  try {
    register();
  } catch (failure) {
    error = failure.message;
  }
  await waitForRPCs();
  resources.sort((a, b) => a.name < b.name ? -1 : a.name === b.name ? 0 : 1);
  return { error, resources };
}

function machine() {
  return new sdk.Provider('machine', { address: 'test.invalid', user: 'deploy' });
}

const helpers = {
  fluxReady: ({ name, options }) => sdk.fluxReady(name, options),
  fluxReason: ({ name, options }) => sdk.fluxReason(name, options),
  ignoreRules: ({ paths }) => sdk.ignoreRules(paths),
  sharedTarget: ({ shared, entry }) => sdk.sharedTarget(shared, entry),
  profileRefusal: ({ shared, dir }) => sdk.profileRefusal({ shared: { path: shared }, dir }) ?? null,
  sharedDefaults: () => sdk.SHARED_DEFAULTS,
  t3Default: ({ version = sdk.T3_DEFAULT.version }) => ({
    version: sdk.T3_DEFAULT.version, sha256: sdk.T3_DEFAULT.sha256, url: sdk.T3_DEFAULT.url(version),
  }),
  agentDefaults: () => sdk.AGENT_DEFAULTS,
  unitResourceLines: ({ options }) => {
    try {
      return { value: sdk.unitResourceLines(options) };
    } catch (error) {
      return { error: error.message };
    }
  },
  fluxApp: ({ name, options, patch }) => capture(() => {
    const opts = patch === undefined ? undefined : {
      provider: new KubernetesProvider('cluster'),
      transformations: [({ type, props, opts }) => {
        if (type !== 'kubernetes:kustomize.toolkit.fluxcd.io/v1:Kustomization') return undefined;
        return {
          props: { ...props, spec: { ...props.spec, patches: [{ target: { kind: 'HelmRelease', name }, patch: pulumi.output(patch) }] } },
          opts,
        };
      }],
    };
    new sdk.FluxApp(name, options, opts);
  }),
  claudeResources: ({ backend, profile }) => capture(() => {
    const provider = machine();
    const shared = sdk.claudeSharedResource(provider, 'claude-backend', backend, { protect: true });
    sdk.claudeProfileResource(provider, 'claude-work', { ...profile, shared }, { protect: true });
  }),
  t3codeResource: ({ options, groups }) => capture(() => {
    const provider = machine();
    if (groups) {
      options = {
        ...options,
        primaryGroup: new sdk.Group('group-t3code', { name: 't3code', gid: options.uid }, { provider }),
        accountDependsOn: [new sdk.Group('group-kvm', { name: 'kvm' }, { provider })],
      };
    }
    sdk.t3codeResource(provider, options);
  }),
};

const snapshots = {};
for (const { name, helper, args } of cases) {
  if (Object.hasOwn(snapshots, name)) throw new Error('Duplicate golden case ' + name);
  if (!Object.hasOwn(helpers, helper)) throw new Error('Unknown helper ' + helper);
  snapshots[name] = await helpers[helper](args);
}
const json = JSON.stringify(snapshots, null, 2) + '\n';
if (process.argv[2] === '--check') {
  if (readFileSync(destination, 'utf8') !== json) {
    throw new Error('The Node component golden file is stale. Run node scripts/gen-components-golden.mjs after rebuilding the Node SDK.');
  }
  console.log('Checked %d Node component golden entries', cases.length);
} else {
  writeFileSync(destination, json);
  console.log('Wrote %d Node component golden entries', cases.length);
}
