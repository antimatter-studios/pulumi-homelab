import { describe, expect, it } from 'vitest';
import { fluxReady, fluxReason } from './ready.ts';

/**
 * Flux does not error on a bad sourceRef, a path that is not in the repository, or a branch that
 * does not exist. It writes a condition and carries on, so `pulumi up` succeeds, the state file
 * agrees, and the application never appears.
 */
describe('asking whether Flux actually reconciled', () => {
  it('waits rather than reading once, so a deployment is not failed for being early', () => {
    // reconciliation takes as long as a clone and an apply take, and the check runs seconds after
    // the objects were created
    expect(fluxReady('app')).toContain('--for=condition=Ready');
    expect(fluxReady('app')).toContain('wait');
  });

  it('defaults to a Kustomization in flux-system with the k3s kubeconfig', () => {
    const check = fluxReady('app');
    expect(check).toContain("'kustomization/app'");
    expect(check).toContain("-n 'flux-system'");
    expect(check).toContain("KUBECONFIG='/etc/rancher/k3s/k3s.yaml'");
  });

  it('can ask about the source rather than the kustomization', () => {
    // the two fail differently: a GitRepository that cannot be cloned and a Kustomization whose
    // path is not there are separate problems with separate fixes
    expect(fluxReady('app', { kind: 'gitrepository' })).toContain("'gitrepository/app'");
  });

  it('takes a timeout, since a first clone is slower than a re-apply', () => {
    expect(fluxReady('app', { timeout: '300s' })).toContain('--timeout=');
  });

  it('quotes everything that reaches a shell', () => {
    const check = fluxReady("ai'world");
    expect(check).toContain("'\\''");
  });
});

describe('telling somebody why', () => {
  it('reads the Ready condition’s own message, which is where Flux writes the reason', () => {
    // 'Flux has not reconciled' on its own sends somebody looking; the message is the whole product
    expect(fluxReason('app')).toContain('.status.conditions[?(@.type=="Ready")].message');
  });

  it('is a command a person can paste, sudo and all', () => {
    expect(fluxReason('app').startsWith('sudo KUBECONFIG=')).toBe(true);
  });
});
