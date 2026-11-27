import * as pulumi from '@pulumi/pulumi';
import * as k8s from '@pulumi/kubernetes';

/**
 * An application Flux deploys from a git repository, as one thing rather than two.
 *
 * **This is the one thing here that is composition rather than a provider, and the one rule still
 * holds.** Everything else in this package talks to a machine over ssh, where "what is actually
 * there" has to be asked for explicitly or nothing knows it. This is a pair of Kubernetes custom
 * resources, and `@pulumi/kubernetes` already reads those back from the API server properly — so
 * `pulumi up --refresh` means exactly what it means everywhere else, and there is nothing a `read`
 * here could do that is not already being done. What this adds is the pairing.
 *
 * `@pulumi/kubernetes` is an ordinary dependency rather than an optional peer behind a subpath. The
 * machinery to avoid it costs more than it saves: Pulumi runs a program through Node rather than
 * bundling it, so a module nobody imports is simply never loaded, and a consumer describing only a
 * machine pays install size and nothing else.
 *
 * **After this exists, Pulumi is no longer in charge of the application.** Push to the repository
 * and the cluster changes, with no `pulumi up` involved and nothing in a `pulumi preview` to see.
 * That is the entire point of Flux and it is also the thing that will confuse whoever next wonders
 * why previewing shows no change after they altered a manifest. Pulumi owns the *pointer* — which
 * repository, which branch, which path, how often — and Flux owns everything the pointer reaches.
 *
 * The composition earns its place on one detail in particular. A `Kustomization` names its source
 * in `spec.sourceRef.name`, and if that name does not match a `GitRepository` that exists, Flux
 * does not report an error. It simply never reconciles, and the symptom is an application that
 * never deploys with nothing anywhere saying why. Here the name cannot disagree, because the
 * `Kustomization` is given the name the `GitRepository` actually got rather than the one it was
 * asked for.
 */

/**
 * The API groups, in one place.
 *
 * Both of these have moved — `source.toolkit.fluxcd.io/v1beta2` became `v1`, and
 * `kustomize.toolkit.fluxcd.io` did the same — and a version string that lives in every consumer's
 * stack is one that gets upgraded in some of them.
 */
const SOURCE_API = 'source.toolkit.fluxcd.io/v1';
const KUSTOMIZE_API = 'kustomize.toolkit.fluxcd.io/v1';

/** Where the Flux controllers watch by default, and where nobody should have to think about it. */
const DEFAULT_NAMESPACE = 'flux-system';

export interface FluxAppArgs {
  /** The repository to watch: 'https://github.com/example/app'. */
  url: pulumi.Input<string>;
  /** Which branch. */
  branch?: pulumi.Input<string>;
  /** Where in the repository the manifests are: './deploy/flux'. */
  path?: pulumi.Input<string>;
  /** Which namespace the Flux objects live in, not where the application is deployed. */
  namespace?: pulumi.Input<string>;
  /** How often Flux looks for new commits. */
  interval?: pulumi.Input<string>;
  /** How often Flux re-applies what it found, whether or not the commit changed. */
  reconcileInterval?: pulumi.Input<string>;
  /**
   * Delete objects the repository stops describing.
   *
   * On by default, because a deployment that only ever adds is not describing a desired state. It
   * is an argument rather than a constant because of what it does: removing a manifest from a
   * commit deletes the thing on the cluster, and that deserves to be visible in the source rather
   * than discovered.
   */
  prune?: pulumi.Input<boolean>;
  /**
   * Only reconcile when these paths change, as gitignore rules.
   *
   * `include: ['/chart', '/deploy']` becomes `/*` then `!/chart` then `!/deploy` — everything
   * ignored, then those put back. Without it Flux reconciles on every commit to the repository,
   * which for one that also holds an application means a texture change redeploying the cluster.
   */
  include?: string[];
  /**
   * The same thing written out, for rules the shorthand above cannot express.
   *
   * Exactly one of `include` and `ignore`; giving both is a description with two answers.
   */
  ignore?: pulumi.Input<string>;
  /**
   * The name of a Kubernetes secret holding credentials for a private repository.
   *
   * A public repository needs none. A repository on a self-hosted Gitea needs a deploy key, and the
   * secret has to already exist in the same namespace — Flux reads it, this does not create it.
   */
  secretRef?: pulumi.Input<string>;
}

const DEFAULTS = {
  branch: 'main',
  path: './',
  namespace: DEFAULT_NAMESPACE,
  interval: '1m',
  reconcileInterval: '5m',
  prune: true,
} as const;

/**
 * `['/chart', '/deploy']` → the gitignore rules that reconcile on those and nothing else.
 *
 * Everything ignored first, then the wanted paths negated back in. Order matters to gitignore
 * semantics: a later rule wins, so the `/*` has to come first or the negations have nothing to
 * undo.
 */
export function ignoreRules(include: string[]): string {
  return ['/*', ...include.map((path) => `!${path.startsWith('/') ? path : `/${path}`}`)].join('\n');
}

export class FluxApp extends pulumi.ComponentResource {
  /** What Flux watches. */
  readonly source: k8s.apiextensions.CustomResource;
  /** What Flux applies. */
  readonly kustomization: k8s.apiextensions.CustomResource;

  constructor(name: string, args: FluxAppArgs, opts?: pulumi.ComponentResourceOptions) {
    super('homelab:flux:FluxApp', name, {}, opts);

    const namespace = args.namespace ?? DEFAULTS.namespace;

    if (args.include && args.ignore !== undefined) {
      throw new Error(`${name}: give either include or ignore, not both — two answers to one question`);
    }
    const ignore = args.include ? ignoreRules(args.include) : args.ignore;

    this.source = new k8s.apiextensions.CustomResource(`${name}-source`, {
      apiVersion: SOURCE_API,
      kind: 'GitRepository',
      metadata: { name, namespace },
      spec: {
        url: args.url,
        ref: { branch: args.branch ?? DEFAULTS.branch },
        interval: args.interval ?? DEFAULTS.interval,
        ...(ignore !== undefined ? { ignore } : {}),
        ...(args.secretRef ? { secretRef: { name: args.secretRef } } : {}),
      },
    }, { parent: this });

    this.kustomization = new k8s.apiextensions.CustomResource(`${name}-kustomization`, {
      apiVersion: KUSTOMIZE_API,
      kind: 'Kustomization',
      metadata: { name, namespace },
      spec: {
        sourceRef: {
          kind: 'GitRepository',
          // Taken from the object that exists rather than from the name that was asked for. Pulumi
          // may name a resource something other than what it was given, and a sourceRef pointing at
          // a GitRepository that is not there fails silently: Flux never reconciles and says
          // nothing at all.
          name: this.source.metadata.apply((metadata) => metadata.name ?? name),
          namespace,
        },
        path: args.path ?? DEFAULTS.path,
        interval: args.reconcileInterval ?? DEFAULTS.reconcileInterval,
        prune: args.prune ?? DEFAULTS.prune,
      },
    }, { parent: this, dependsOn: [this.source] });

    this.registerOutputs({ source: this.source, kustomization: this.kustomization });
  }
}
