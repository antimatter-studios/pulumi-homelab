import * as pulumi from '@pulumi/pulumi';
import * as k8s from '@pulumi/kubernetes';
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
/**
 * `['/chart', '/deploy']` → the gitignore rules that reconcile on those and nothing else.
 *
 * Everything ignored first, then the wanted paths negated back in. Order matters to gitignore
 * semantics: a later rule wins, so the `/*` has to come first or the negations have nothing to
 * undo.
 */
export declare function ignoreRules(include: string[]): string;
export declare class FluxApp extends pulumi.ComponentResource {
    /** What Flux watches. */
    readonly source: k8s.apiextensions.CustomResource;
    /** What Flux applies. */
    readonly kustomization: k8s.apiextensions.CustomResource;
    constructor(name: string, args: FluxAppArgs, opts?: pulumi.ComponentResourceOptions);
}
