"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
exports.fluxReason = exports.fluxReady = void 0;
function shellQuote(text) {
    return `'${text.replace(/'/g, `'\\''`)}'`;
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
function fluxReady(name, args = {}) {
    var _a, _b, _c, _d;
    const kubeconfig = (_a = args.kubeconfig) !== null && _a !== void 0 ? _a : DEFAULTS.kubeconfig;
    const kind = (_b = args.kind) !== null && _b !== void 0 ? _b : DEFAULTS.kind;
    const namespace = (_c = args.namespace) !== null && _c !== void 0 ? _c : DEFAULTS.namespace;
    const timeout = (_d = args.timeout) !== null && _d !== void 0 ? _d : DEFAULTS.timeout;
    return `KUBECONFIG=${shellQuote(kubeconfig)} kubectl -n ${shellQuote(namespace)} ` +
        `wait ${shellQuote(`${kind}/${name}`)} --for=condition=Ready --timeout=${shellQuote(timeout)} >/dev/null 2>&1`;
}
exports.fluxReady = fluxReady;
/**
 * The command that prints Flux's own reason, for putting in the message.
 *
 * The message is the whole product of a `Precondition`, and "Flux has not reconciled" on its own
 * sends somebody looking. Flux does write down what went wrong — a missing path, a branch that does
 * not exist, a repository it cannot read — in the Ready condition's message. This is how to read it,
 * and it belongs in the failure text rather than being run by the check, because a check that
 * printed it would still leave the operator without it once the deployment stopped.
 */
function fluxReason(name, args = {}) {
    var _a, _b, _c;
    const kubeconfig = (_a = args.kubeconfig) !== null && _a !== void 0 ? _a : DEFAULTS.kubeconfig;
    const kind = (_b = args.kind) !== null && _b !== void 0 ? _b : DEFAULTS.kind;
    const namespace = (_c = args.namespace) !== null && _c !== void 0 ? _c : DEFAULTS.namespace;
    return `sudo KUBECONFIG=${kubeconfig} kubectl -n ${namespace} get ${kind} ${name} ` +
        `-o jsonpath='{.status.conditions[?(@.type=="Ready")].message}'`;
}
exports.fluxReason = fluxReason;
//# sourceMappingURL=ready.js.map