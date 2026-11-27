import { createRequire } from 'node:module';
import * as pulumi from '@pulumi/pulumi';
import type { RegisterResourceRequest } from '@pulumi/pulumi/proto/resource_pb';
import { getMonitor } from '@pulumi/pulumi/runtime/settings';
import { beforeAll, describe, expect, it, vi } from 'vitest';
import type * as SDK from '../index';

const {
  Group, Provider, claudeProfileResource, claudeSharedResource, SHARED_DEFAULTS, t3codeResource, T3_DEFAULT,
}: typeof SDK = createRequire(import.meta.url)('..');

const registrations = new Map<string, RegisterResourceRequest>();
const inputs = new Map<string, pulumi.Inputs>();
const publicKey = 'ssh-ed25519 test-key';
const read = <T>(output: pulumi.Output<T>) => new Promise<T>((resolve) => output.apply((value) => {
  resolve(value);
  return value;
}));

function registration(name: string): RegisterResourceRequest {
  const request = registrations.get(name);
  if (!request) throw new Error(`Missing registration for ${name}`);
  return request;
}

function dependencies(name: string): string[] {
  return registration(name).getDependenciesList().map((urn) => urn.split('::').at(-1)!).sort();
}

let provider: SDK.Provider;
let providerRef: string;
beforeAll(async () => {
  await pulumi.runtime.setMocks({
    newResource(args) {
      inputs.set(args.name, args.inputs);
      return {
        id: `${args.name}-id`,
        state: args.type === 'homelab:index:SshKey' ? { ...args.inputs, publicKey } : args.inputs,
      };
    },
    call() { return {}; },
  }, 'project', 'stack', false);

  // Inspect registration options as well as the inputs exposed by Pulumi's resource mocks.
  const monitor = getMonitor()!;
  const register = monitor.registerResource.bind(monitor);
  vi.spyOn(monitor, 'registerResource').mockImplementation((request, ...args) => {
    registrations.set(request.getName(), request);
    return register(request, ...args);
  });
  provider = new Provider('machine', { address: 'test.invalid', user: 'deploy' });
  providerRef = `${await read(provider.urn)}::${await read(provider.id)}`;
});

describe('Claude resource wiring', () => {
  it('creates the shared directories and links each profile entry to the shared backend', async () => {
    const backend = claudeSharedResource(provider, 'claude-backend', {
      account: 'dev', path: '/home/dev/.claude/',
    }, { protect: true });
    const profile = claudeProfileResource(provider, 'claude-work', {
      account: 'dev', shared: backend, dir: '/home/dev/.claude-work', group: 'work', mode: '0700',
    }, { protect: true });
    await Promise.all([...backend.resources, ...profile.links].map((resource) => read(resource.urn)));

    expect(backend.path).toBe('/home/dev/.claude/');
    expect(backend.entries).toBe(SHARED_DEFAULTS);
    expect(backend.resources).toHaveLength(5);
    expect(profile.dir).toBe('/home/dev/.claude-work');
    expect(profile.configDir).toBe(profile.dir);
    expect(profile.links).toHaveLength(5);
    expect(inputs.get('claude-backend')).toMatchObject({
      path: backend.path, owner: 'dev', group: 'dev', mode: '0775',
    });
    expect(inputs.get('claude-work')).toMatchObject({
      path: profile.dir, owner: 'dev', group: 'work', mode: '0700',
    });
    const backendNames = ['claude-backend'];
    for (const entry of SHARED_DEFAULTS) {
      const name = `claude-backend-${entry.name}`;
      if (entry.kind === 'directory') {
        backendNames.push(name);
        expect(registration(name).getType()).toBe('homelab:index:Directory');
        expect(inputs.get(name)).toMatchObject({
          path: `/home/dev/.claude/${entry.name}`, owner: 'dev', group: 'dev', mode: entry.mode,
        });
        expect(dependencies(name)).toEqual(['claude-backend']);
      } else {
        expect(registrations.has(name)).toBe(false);
      }
    }
    for (const entry of SHARED_DEFAULTS) {
      const name = `claude-work-${entry.name}`;
      expect(registration(name).getType()).toBe('homelab:index:Symlink');
      expect(inputs.get(name)).toMatchObject({
        path: `${profile.dir}/${entry.name}`, target: `/home/dev/.claude/${entry.name}`,
      });
      expect(dependencies(name)).toEqual([...backendNames, 'claude-work'].sort());
    }
    for (const name of [...backendNames, 'claude-work', ...SHARED_DEFAULTS.map((entry) => `claude-work-${entry.name}`)]) {
      expect(registration(name).getProvider()).toBe(providerRef);
      expect(registration(name).getProtect()).toBe(true);
    }
  });
});

describe('T3 Code resource wiring', () => {
  it('registers the default installation and preserves its launch and dependency options', async () => {
    const group = new Group('group-t3code', { name: 't3code', gid: 1100 }, { provider });
    const extraGroup = new Group('group-kvm', { name: 'kvm' }, { provider });
    const home = '/srv/t3code';
    const workspace = '/srv/projects';
    const t3 = t3codeResource(provider, {
      home, workspace, groups: ['kvm'], uid: 1100, primaryGroup: group, accountDependsOn: [extraGroup],
      git: { config: '[user]\n    name = Test\n' },
    });
    expect(t3).toMatchObject({
      account: 't3code', home, workspace, bin: `${home}/.local/bin`, opt: `${home}/.local/opt`, unitName: 't3code',
    });
    expect(await read(t3.user.urn)).toMatch(/::user-t3code$/);
    expect(await read(t3.binDirectory.urn)).toMatch(/::dir-t3code-bin$/);
    expect(await read(t3.publicKey)).toBe(publicKey);

    const graph: Record<string, [string, ...string[]]> = {
      'user-t3code': ['User', 'group-kvm', 'group-t3code'],
      'dir-t3code-home': ['Directory', 'user-t3code'],
      'pkg-t3code-tools': ['AptPackages'],
      'dir-t3code-dotlocal': ['Directory', 'user-t3code'],
      'dir-t3code-dott3': ['Directory', 'user-t3code'],
      'dir-t3code-runtime': ['Directory', 'dir-t3code-dott3'],
      'dir-t3code-versions': ['Directory', 'dir-t3code-runtime'],
      'dir-t3code-bin': ['Directory', 'dir-t3code-dotlocal'],
      'dir-t3code-opt': ['Directory', 'dir-t3code-dotlocal'],
      'archive-t3': ['Archive', 'user-t3code', 'dir-t3code-versions'],
      'file-t3-install-complete': ['ManagedFile', 'archive-t3'],
      'link-t3': ['Symlink', 'archive-t3', 'dir-t3code-bin'],
      'archive-gh': ['Archive', 'user-t3code', 'dir-t3code-opt'],
      'link-gh': ['Symlink', 'archive-gh', 'dir-t3code-bin'],
      'install-claude': ['OfficialInstall', 'user-t3code', 'dir-t3code-bin'],
      'install-codex': ['OfficialInstall', 'user-t3code', 'dir-t3code-bin'],
      'install-opencode': ['OfficialInstall', 'user-t3code', 'dir-t3code-bin'],
      'link-opencode': ['Symlink', 'install-opencode'],
      'dir-t3code-dotssh': ['Directory', 'user-t3code'],
      'sshkey-t3code-git': ['SshKey', 'dir-t3code-dotssh'],
      'file-t3code-gitconfig': ['ManagedFile', 'user-t3code'],
      'sudo-t3code-apt': ['SudoRule', 'user-t3code'],
      'unit-t3code': ['SystemdUnit', 'archive-t3', 'dir-t3code-versions', 'dir-t3code-bin'],
    };
    await vi.waitFor(() => {
      for (const name of Object.keys(graph)) expect(inputs.has(name)).toBe(true);
    });
    const otherNames = ['project-stack', 'machine', 'group-t3code', 'group-kvm'];
    expect([...registrations.keys()].filter((name) => !name.startsWith('claude-') && !otherNames.includes(name)).sort())
      .toEqual(Object.keys(graph).sort());
    for (const [name, [type, ...deps]] of Object.entries(graph)) {
      expect(registration(name).getType()).toBe(`homelab:index:${type}`);
      expect(registration(name).getProvider()).toBe(providerRef);
      expect(dependencies(name)).toEqual(deps.sort());
    }
    expect(inputs.get('user-t3code')).toMatchObject({
      name: 't3code', home, shell: '/bin/bash', createHome: true, group: 't3code', groups: ['kvm'], uid: 1100,
    });
    expect(inputs.get('pkg-t3code-tools')).toMatchObject({ names: ['git', 'curl', 'ca-certificates', 'unzip'] });
    const prefix = `${home}/.t3/runtime/versions/${T3_DEFAULT.version}`;
    expect(inputs.get('archive-t3')).toMatchObject({
      prefix, owner: 't3code', group: 't3code', sha256: T3_DEFAULT.sha256, url: T3_DEFAULT.url(T3_DEFAULT.version),
    });
    expect(inputs.get('file-t3-install-complete')).toMatchObject({
      path: `${prefix}/.install-complete`, content: `${T3_DEFAULT.version}\n`,
    });
    expect(inputs.get('link-t3')).toMatchObject({
      path: `${t3.bin}/t3`, target: `${prefix}/t3`, acceptLauncherScript: true,
    });
    expect(registration('link-t3').getIgnorechangesList()).toEqual(['target']);
    expect(inputs.get('unit-t3code')).toMatchObject({ enabled: true, started: true });
    expect(inputs.get('unit-t3code')?.unit).toContain(`ExecStart=${t3.bin}/t3 serve`);
  });
});
