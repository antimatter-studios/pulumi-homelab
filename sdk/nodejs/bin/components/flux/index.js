"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
exports.FluxApp = exports.ignoreRules = void 0;
const pulumi = require("@pulumi/pulumi");
const k8s = require("@pulumi/kubernetes");
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
const DEFAULTS = {
    branch: 'main',
    path: './',
    namespace: DEFAULT_NAMESPACE,
    interval: '1m',
    reconcileInterval: '5m',
    prune: true,
};
/**
 * `['/chart', '/deploy']` → the gitignore rules that reconcile on those and nothing else.
 *
 * Everything ignored first, then the wanted paths negated back in. Order matters to gitignore
 * semantics: a later rule wins, so the `/*` has to come first or the negations have nothing to
 * undo.
 */
function ignoreRules(include) {
    return ['/*', ...include.map((path) => `!${path.startsWith('/') ? path : `/${path}`}`)].join('\n');
}
exports.ignoreRules = ignoreRules;
class FluxApp extends pulumi.ComponentResource {
    constructor(name, args, opts) {
        var _a, _b, _c, _d, _e, _f;
        super('homelab:flux:FluxApp', name, {}, opts);
        const namespace = (_a = args.namespace) !== null && _a !== void 0 ? _a : DEFAULTS.namespace;
        if (args.include && args.ignore !== undefined) {
            throw new Error(`${name}: give either include or ignore, not both — two answers to one question`);
        }
        const ignore = args.include ? ignoreRules(args.include) : args.ignore;
        this.source = new k8s.apiextensions.CustomResource(`${name}-source`, {
            apiVersion: SOURCE_API,
            kind: 'GitRepository',
            metadata: { name, namespace },
            spec: Object.assign(Object.assign({ url: args.url, ref: { branch: (_b = args.branch) !== null && _b !== void 0 ? _b : DEFAULTS.branch }, interval: (_c = args.interval) !== null && _c !== void 0 ? _c : DEFAULTS.interval }, (ignore !== undefined ? { ignore } : {})), (args.secretRef ? { secretRef: { name: args.secretRef } } : {})),
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
                    name: this.source.metadata.apply((metadata) => { var _a; return (_a = metadata.name) !== null && _a !== void 0 ? _a : name; }),
                    namespace,
                },
                path: (_d = args.path) !== null && _d !== void 0 ? _d : DEFAULTS.path,
                interval: (_e = args.reconcileInterval) !== null && _e !== void 0 ? _e : DEFAULTS.reconcileInterval,
                prune: (_f = args.prune) !== null && _f !== void 0 ? _f : DEFAULTS.prune,
            },
        }, { parent: this, dependsOn: [this.source] });
        this.registerOutputs({ source: this.source, kustomization: this.kustomization });
    }
}
exports.FluxApp = FluxApp;
//# sourceMappingURL=index.js.map