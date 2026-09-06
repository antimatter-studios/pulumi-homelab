import { shellQuote } from '../ssh.ts';

/**
 * Asking the cluster whether Flux actually did the thing.
 *
 * `FluxApp` creates two objects and `pulumi up` returns the moment they exist — which is not the
 * moment the application is deployed, and may never be that moment at all. Flux does not report a
 * bad `sourceRef`, a path that is not in the repository, a branch that does not exist or a private
 * repository it cannot read as an *error*. It writes a condition on the object and carries on, and
 * the symptom is an application that never appears with nothing anywhere saying why.
 *
 * So the pointer and the effect are separate questions, exactly as they are for a boot line and a
 * running kernel. `FluxApp` asserts the pointer; this asks the cluster what came of it, and belongs
 * in a `Precondition` so that a deployment which depends on the application stops with an
 * instruction rather than building on something that was never reconciled.
 *
 * ```ts
 * const app = new FluxApp('app', { url, branch: 'main', path: './deploy/flux' }, { provider });
 *
 * new Precondition('app-reconciled', host, {
 *   check: fluxReady('app'),
 *   root: true,
 *   message: `Flux has not reconciled app. Ask it why:\n  ${fluxReason('app')}`,
 * }, { dependsOn: [app] });
 * ```
 *
 * It runs on the machine over ssh rather than through the Kubernetes API, because that is where a
 * kubeconfig already exists and because `Precondition` is already the shape for "stop and tell
 * somebody". `root: true` is usually needed: k3s writes its kubeconfig `0600` and owned by root.
 */
export interface FluxCheckArgs {
  /** Which Flux object, in the namespace below. Defaults to a `Kustomization`. */
  kind?: string;
  namespace?: string;
  /** How long to give Flux before calling it a failure. */
  timeout?: string;
  /** Where the kubeconfig lives. The k3s default. */
  kubeconfig?: string;
}

const DEFAULTS = {
  kind: 'kustomization',
  namespace: 'flux-system',
  timeout: '120s',
  kubeconfig: '/etc/rancher/k3s/k3s.yaml',
};

/**
 * A command that exits zero only when Flux says the object is Ready.
 *
 * `kubectl wait` rather than a `get` and a comparison: it blocks until the condition holds or the
 * timeout expires, so a deployment run seconds after the objects were created does not fail merely
 * for being early. Reconciliation takes as long as a clone and an apply take.
 */
export function fluxReady(name: string, args: FluxCheckArgs = {}): string {
  const kubeconfig = args.kubeconfig ?? DEFAULTS.kubeconfig;
  const kind = args.kind ?? DEFAULTS.kind;
  const namespace = args.namespace ?? DEFAULTS.namespace;
  const timeout = args.timeout ?? DEFAULTS.timeout;
  return `KUBECONFIG=${shellQuote(kubeconfig)} kubectl -n ${shellQuote(namespace)} ` +
    `wait ${shellQuote(`${kind}/${name}`)} --for=condition=Ready --timeout=${shellQuote(timeout)} >/dev/null 2>&1`;
}

/**
 * The command that prints Flux's own reason, for putting in the message.
 *
 * The message is the whole product of a `Precondition`, and "Flux has not reconciled" on its own
 * sends somebody looking. Flux does write down what went wrong — a missing path, a branch that does
 * not exist, a repository it cannot read — in the Ready condition's message. This is how to read it,
 * and it belongs in the failure text rather than being run by the check, because a check that
 * printed it would still leave the operator without it once the deployment stopped.
 */
export function fluxReason(name: string, args: FluxCheckArgs = {}): string {
  const kubeconfig = args.kubeconfig ?? DEFAULTS.kubeconfig;
  const kind = args.kind ?? DEFAULTS.kind;
  const namespace = args.namespace ?? DEFAULTS.namespace;
  return `sudo KUBECONFIG=${kubeconfig} kubectl -n ${namespace} get ${kind} ${name} ` +
    `-o jsonpath='{.status.conditions[?(@.type=="Ready")].message}'`;
}
