import * as pulumi from '@pulumi/pulumi';
import { beforeAll, describe, expect, it } from 'vitest';

/**
 * The detail that justifies wrapping this at all.
 *
 * A `Kustomization` names its source in `spec.sourceRef.name`, and when that name does not match a
 * `GitRepository` that exists, **Flux does not report an error**. It simply never reconciles, and
 * the symptom is an application that never deploys with nothing anywhere saying why. So the name is
 * taken from the object that exists rather than from the string it was asked for — Pulumi may
 * autoname a resource something other than what it was given, and this is the failure that produces.
 */
let sourceName: string;
let kustomizationRef: { kind: string; name: string; namespace: string };
let prune: boolean;

beforeAll(async () => {
  pulumi.runtime.setMocks({
    newResource(args: pulumi.runtime.MockResourceArgs) {
      // stand in for Pulumi's autonaming: the object that exists is not called what it was asked to
      // be called, which is exactly the case the sourceRef has to survive
      const named = { ...args.inputs, metadata: { ...args.inputs.metadata, name: `${args.name}-7f3a91` } };
      return { id: `${args.name}-id`, state: named };
    },
    call() { return {}; },
  }, 'project', 'stack', false);

  const { FluxApp } = await import('./index.ts');
  const app = new FluxApp('app', {
    url: 'https://github.com/example/app',
    branch: 'main',
    path: './deploy/flux',
  });

  const read = <T>(output: pulumi.Output<T>) => new Promise<T>((resolve) => output.apply((value) => { resolve(value); return value; }));
  sourceName = (await read(app.source.metadata)).name ?? '';
  // a CustomResource's spec is untyped by construction — it is whatever the CRD says
  const spec = await read((app.kustomization as unknown as {
    spec: pulumi.Output<{ sourceRef: typeof kustomizationRef; prune: boolean }>;
  }).spec);
  kustomizationRef = spec.sourceRef;
  prune = spec.prune;
});

describe('pointing a Kustomization at its source', () => {
  it('uses the name the GitRepository actually got, not the one it was asked for', () => {
    // a sourceRef pointing at a GitRepository that is not there fails silently, for ever
    expect(sourceName).toBe('app-source-7f3a91');
    expect(kustomizationRef.name).toBe(sourceName);
  });

  it('keeps both objects in the same namespace, since Flux resolves the ref within one', () => {
    expect(kustomizationRef.namespace).toBe('flux-system');
  });

  it('names the kind, which Flux needs to know what it is looking for', () => {
    expect(kustomizationRef.kind).toBe('GitRepository');
  });

  it('prunes by default, because a deployment that only adds is not a desired state', () => {
    expect(prune).toBe(true);
  });
});

describe('reconciling on only some of a repository', () => {
  it('ignores everything, then puts back what was asked for', async () => {
    // order matters to gitignore semantics: a later rule wins, so the /* has to come first or the
    // negations have nothing to undo
    const { ignoreRules } = await import('./index.ts');
    expect(ignoreRules(['/chart', '/deploy'])).toBe('/*\n!/chart\n!/deploy');
  });

  it('adds the leading slash somebody will forget', async () => {
    const { ignoreRules } = await import('./index.ts');
    expect(ignoreRules(['chart'])).toBe('/*\n!/chart');
  });
});
