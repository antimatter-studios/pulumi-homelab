package homelab

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/internals"
)

const testPublicKey = "ssh-ed25519 test-key"
const testURNPrefix = "urn:pulumi:stack::project::"
const testProviderRef = testURNPrefix + "pulumi:providers:homelab::machine::machine-id"

type componentMocks struct {
	mu            sync.Mutex
	registrations map[string]pulumi.MockResourceArgs
}

func (*componentMocks) Call(pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return nil, nil
}

func (m *componentMocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	m.mu.Lock()
	m.registrations[args.Name] = args
	m.mu.Unlock()

	state := args.Inputs.Copy()
	switch args.TypeToken {
	case "homelab:index:SshKey":
		state["publicKey"] = resource.NewProperty(testPublicKey)
	case "kubernetes:source.toolkit.fluxcd.io/v1:GitRepository":
		metadata := args.Inputs["metadata"].ObjectValue().Copy()
		metadata["name"] = resource.NewProperty(args.Name + "-7f3a91")
		state["metadata"] = resource.NewProperty(metadata)
	}
	return args.Name + "-id", state, nil
}

func runComponent(t *testing.T, program pulumi.RunFunc) *componentMocks {
	t.Helper()
	m := &componentMocks{registrations: map[string]pulumi.MockResourceArgs{}}
	if err := pulumi.RunErr(program, pulumi.WithMocks("project", "stack", m)); err != nil {
		t.Fatal(err)
	}
	return m
}

func mockMachine(ctx *pulumi.Context) (*Provider, error) {
	return NewProvider(ctx, "machine", &ProviderArgs{
		Address: pulumi.String("test.invalid"), User: pulumi.String("deploy"),
	})
}

func (m *componentMocks) registration(t *testing.T, name string) pulumi.MockResourceArgs {
	t.Helper()
	r, ok := m.registrations[name]
	if !ok || r.RegisterRPC == nil {
		t.Fatalf("missing registration for %s", name)
	}
	return r
}

func (m *componentMocks) namesExcept(ignored ...string) []string {
	names := []string{}
	for name := range m.registrations {
		if !slices.Contains(ignored, name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

func (m *componentMocks) check(t *testing.T, name, token, parent, provider string, inputs map[string]any, deps ...string) {
	t.Helper()
	reg := m.registration(t, name)
	equal(t, reg.TypeToken, token)
	equal(t, reg.Provider, provider)
	equal(t, parentPath(reg.RegisterRPC.GetParent()), parent)
	deepEqual(t, reg.Inputs.Mappable(), inputs)
	got := resourceNames(reg.RegisterRPC.GetDependencies())
	slices.Sort(deps)
	if !slices.Equal(got, deps) {
		t.Errorf("%s dependencies = %v, want %v", name, got, deps)
	}
}

func resourceNames(urns []string) []string {
	names := make([]string, 0, len(urns))
	for _, urn := range urns {
		names = append(names, resource.URN(urn).Name())
	}
	slices.Sort(names)
	return names
}

func parentPath(urn string) string {
	// Both SDKs use the stack for resources without an explicit parent. Some
	// runtimes leave this implicit in the registration request.
	if urn == "" || resource.URN(urn).QualifiedType() == resource.RootStackType {
		return ""
	}
	return urn
}

type mockComponent struct{ pulumi.ResourceState }
type mockProvider struct{ pulumi.ProviderResourceState }

func TestFluxAppRegistrations(t *testing.T) {
	m := runComponent(t, func(ctx *pulumi.Context) error {
		_, err := NewFluxApp(ctx, "app", &FluxAppArgs{URL: pulumi.String("https://github.com/example/app")})
		return err
	})
	deepEqual(t, m.namesExcept("project-stack"), []string{"app", "app-kustomization", "app-source"})
	const parent = testURNPrefix + "homelab:flux:FluxApp::app"
	m.check(t, "app", "homelab:flux:FluxApp", "", "", map[string]any{})
	metadata := map[string]any{"name": "app", "namespace": "flux-system"}
	const sourceToken = "kubernetes:source.toolkit.fluxcd.io/v1:GitRepository"
	m.check(t, "app-source", sourceToken, parent, "", map[string]any{
		"metadata": metadata, "apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "GitRepository",
		"spec": map[string]any{"url": "https://github.com/example/app", "ref": map[string]any{"branch": "main"}, "interval": "1m"},
	})
	const kustomizationToken = "kubernetes:kustomize.toolkit.fluxcd.io/v1:Kustomization"
	k := m.registration(t, "app-kustomization")
	spec := k.Inputs["spec"].ObjectValue()
	ref := spec["sourceRef"].ObjectValue()
	t.Run("refers to the repository name returned by Kubernetes", func(t *testing.T) {
		equal(t, ref["name"].StringValue(), "app-source-7f3a91")
	})
	t.Run("uses the same namespace for both resources and the source reference", func(t *testing.T) {
		equal(t, ref["namespace"].StringValue(), "flux-system")
		equal(t, k.Inputs["metadata"].ObjectValue()["namespace"].StringValue(), ref["namespace"].StringValue())
	})
	t.Run("names the source kind", func(t *testing.T) {
		equal(t, ref["kind"].StringValue(), "GitRepository")
	})
	t.Run("prunes by default", func(t *testing.T) {
		equal(t, spec["prune"].BoolValue(), true)
	})
	m.check(t, "app-kustomization", kustomizationToken, parent, "", map[string]any{
		"metadata": metadata, "apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"spec": map[string]any{
			"sourceRef": map[string]any{"kind": "GitRepository", "name": "app-source-7f3a91", "namespace": "flux-system"},
			"path":      "./", "interval": "5m", "prune": true,
		},
	}, "app-source")
	deepEqual(t, resourceNames(k.RegisterRPC.GetPropertyDependencies()["spec"].GetUrns()), []string{"app-source"})
	for _, name := range []string{"app-source", "app-kustomization"} {
		equal(t, m.registration(t, name).RegisterRPC.GetVersion(), "4.34.0")
	}
}

func TestFluxAppKustomizationPatch(t *testing.T) {
	const patch = `[{"op":"add","path":"/spec/priorityClassName","value":"critical"}]`
	const clusterRef = testURNPrefix + "pulumi:providers:kubernetes::cluster::cluster-id"
	m := runComponent(t, func(ctx *pulumi.Context) error {
		cluster := &mockProvider{}
		if err := ctx.RegisterResource("pulumi:providers:kubernetes", "cluster", pulumi.Map{}, cluster); err != nil {
			return err
		}
		transform := func(args *pulumi.ResourceTransformationArgs) *pulumi.ResourceTransformationResult {
			if args.Type != "kubernetes:kustomize.toolkit.fluxcd.io/v1:Kustomization" {
				return nil
			}
			props := args.Props.(kubernetes.UntypedArgs)
			props["spec"].(pulumi.Map)["patches"] = pulumi.Array{pulumi.Map{
				"target": pulumi.Map{"kind": pulumi.String("HelmRelease"), "name": pulumi.String("app")},
				"patch":  pulumi.String(patch).ToStringOutput(),
			}}
			return &pulumi.ResourceTransformationResult{Props: props, Opts: args.Opts}
		}
		_, err := NewFluxApp(ctx, "patched", &FluxAppArgs{
			URL: pulumi.String("https://github.com/example/app"), Branch: pulumi.String("work"),
			Namespace: pulumi.String("ops"), Path: pulumi.String("./deploy"), Interval: pulumi.String("2m"),
			ReconcileInterval: pulumi.String("10m"), Prune: pulumi.Bool(false),
			Include: []string{"deploy"}, SecretRef: pulumi.String("deploy-key"),
		}, pulumi.Provider(cluster), pulumi.Transformations([]pulumi.ResourceTransformation{transform}),
			pulumi.Aliases([]pulumi.Alias{{Name: pulumi.String("legacy")}}))
		return err
	})
	const parent = testURNPrefix + "homelab:flux:FluxApp::patched"
	metadata := map[string]any{"name": "patched", "namespace": "ops"}
	m.check(t, "patched-source", "kubernetes:source.toolkit.fluxcd.io/v1:GitRepository", parent, clusterRef, map[string]any{
		"metadata": metadata, "apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "GitRepository",
		"spec": map[string]any{
			"url": "https://github.com/example/app", "ref": map[string]any{"branch": "work"},
			"interval": "2m", "ignore": "/*\n!/deploy", "secretRef": map[string]any{"name": "deploy-key"},
		},
	})
	m.check(t, "patched-kustomization", "kubernetes:kustomize.toolkit.fluxcd.io/v1:Kustomization", parent, clusterRef, map[string]any{
		"metadata": metadata, "apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"spec": map[string]any{
			"sourceRef": map[string]any{"kind": "GitRepository", "name": "patched-source-7f3a91", "namespace": "ops"},
			"path":      "./deploy", "interval": "10m", "prune": false,
			"patches": []any{map[string]any{"target": map[string]any{"kind": "HelmRelease", "name": "app"}, "patch": patch}},
		},
	}, "patched-source")
	app := m.registration(t, "patched").RegisterRPC
	equal(t, len(app.GetAliases())+len(app.GetAliasURNs()), 1)
}

func TestFluxAppConflictingIgnoreRules(t *testing.T) {
	runComponent(t, func(ctx *pulumi.Context) error {
		_, err := NewFluxApp(ctx, "app", &FluxAppArgs{
			URL: pulumi.String("https://github.com/example/app"), Include: []string{}, Ignore: pulumi.String(""),
		})
		if err == nil {
			t.Error("accepted both include and ignore")
		} else {
			contains(t, err.Error(), "give either include or ignore, not both")
		}
		return nil
	})
}

func directoryInputs(path, owner, group, mode string) map[string]any {
	return map[string]any{"path": path, "owner": owner, "group": group, "mode": mode}
}

func TestClaudeResourceWiring(t *testing.T) {
	const path = "/home/dev/.claude/"
	const profileDir = "/home/dev/.claude-work"
	m := runComponent(t, func(ctx *pulumi.Context) error {
		p, err := mockMachine(ctx)
		if err != nil {
			return err
		}
		backend, err := ClaudeSharedResource(ctx, p, "claude-backend", &ClaudeSharedArgs{
			Account: "dev", Path: path,
		}, pulumi.Protect(true))
		if err != nil {
			return err
		}
		profile, err := ClaudeProfileResource(ctx, p, "claude-work", &ClaudeProfileArgs{
			Account: "dev", Shared: backend, Dir: profileDir, Group: new("work"), Mode: new("0700"),
		}, pulumi.Protect(true))
		if err != nil {
			return err
		}
		equal(t, backend.Path, path)
		deepEqual(t, backend.Entries, SharedDefaults)
		equal(t, len(backend.Resources), 5)
		equal(t, profile.Dir, profileDir)
		equal(t, profile.ConfigDir, profileDir)
		equal(t, len(profile.Links), 5)
		return nil
	})
	const dirType = "homelab:index:Directory"
	const linkType = "homelab:index:Symlink"
	m.check(t, "claude-backend", dirType, "", testProviderRef, directoryInputs(path, "dev", "dev", "0775"))
	m.check(t, "claude-work", dirType, "", testProviderRef, directoryInputs(profileDir, "dev", "work", "0700"))
	backendNames := []string{"claude-backend"}
	for _, entry := range SharedDefaults {
		name := "claude-backend-" + entry.Name
		if entry.Kind == "directory" {
			backendNames = append(backendNames, name)
			m.check(t, name, dirType, "", testProviderRef,
				directoryInputs("/home/dev/.claude/"+entry.Name, "dev", "dev", *entry.Mode), "claude-backend")
		}
	}
	names := append(slices.Clone(backendNames), "claude-work")
	for _, entry := range SharedDefaults {
		name := "claude-work-" + entry.Name
		names = append(names, name)
		m.check(t, name, linkType, "", testProviderRef, map[string]any{
			"path": profileDir + "/" + entry.Name, "target": "/home/dev/.claude/" + entry.Name,
		}, append([]string{"claude-work"}, backendNames...)...)
	}
	for _, name := range names {
		equal(t, m.registration(t, name).RegisterRPC.GetProtect(), true)
	}
	slices.Sort(names)
	deepEqual(t, m.namesExcept("project-stack", "machine"), names)
}

func TestClaudeResourceOptions(t *testing.T) {
	m := runComponent(t, func(ctx *pulumi.Context) error {
		p, err := mockMachine(ctx)
		if err != nil {
			return err
		}
		parent := &mockComponent{}
		if err := ctx.RegisterComponentResource("test:index:Parent", "parent", parent); err != nil {
			return err
		}
		// Use a custom dependency so Pulumi includes it in the registration.
		group, err := NewGroup(ctx, "group-work", &GroupArgs{Name: pulumi.String("work")}, pulumi.Provider(p))
		if err != nil {
			return err
		}
		opts := []pulumi.ResourceOption{pulumi.Parent(parent), pulumi.DependsOn([]pulumi.Resource{group}),
			pulumi.Aliases([]pulumi.Alias{{Type: pulumi.String("legacy:index:Entry")}}), pulumi.Import(pulumi.ID("adopt"))}
		shared, err := ClaudeSharedResource(ctx, p, "backend", &ClaudeSharedArgs{
			Account: "dev", Path: "/shared", Entries: []SharedEntry{{Name: "projects", Kind: "directory"}},
		}, opts...)
		if err != nil {
			return err
		}
		_, err = ClaudeProfileResource(ctx, p, "profile", &ClaudeProfileArgs{
			Account: "dev", Dir: "/profile", Shared: shared,
		}, opts...)
		return err
	})
	for name, deps := range map[string][]string{
		"backend": {"group-work"}, "backend-projects": {"backend"},
		"profile": {"group-work"}, "profile-projects": {"backend", "backend-projects", "profile"},
	} {
		reg := m.registration(t, name)
		equal(t, reg.RegisterRPC.GetParent(), testURNPrefix+"test:index:Parent::parent")
		equal(t, reg.RegisterRPC.GetProvider(), testProviderRef)
		equal(t, reg.RegisterRPC.GetImportId(), "adopt")
		equal(t, len(reg.RegisterRPC.GetAliases())+len(reg.RegisterRPC.GetAliasURNs()), 1)
		deepEqual(t, resourceNames(reg.RegisterRPC.GetDependencies()), deps)
	}
}

func TestT3CodeResourceWiring(t *testing.T) {
	const home, workspace = "/srv/t3code", "/srv/projects"
	const bin, opt = home + "/.local/bin", home + "/.local/opt"
	const gitConfig = "[user]\n    name = Test\n"
	m := runComponent(t, func(ctx *pulumi.Context) error {
		p, err := mockMachine(ctx)
		if err != nil {
			return err
		}
		group, err := NewGroup(ctx, "group-t3code", &GroupArgs{Name: pulumi.String("t3code"), Gid: pulumi.Int(1100)}, pulumi.Provider(p))
		if err != nil {
			return err
		}
		extraGroup, err := NewGroup(ctx, "group-kvm", &GroupArgs{Name: pulumi.String("kvm")}, pulumi.Provider(p))
		if err != nil {
			return err
		}
		t3, err := T3CodeResource(ctx, p, &T3CodeArgs{
			Home: home, Workspace: workspace, Groups: []string{"kvm"}, UID: new(1100), PrimaryGroup: group,
			AccountDependsOn: []pulumi.Resource{extraGroup}, Git: T3GitArgs{Config: gitConfig},
		})
		if err != nil {
			return err
		}
		deepEqual(t, []string{t3.Account, t3.Home, t3.Workspace, t3.Bin, t3.Opt, t3.UnitName},
			[]string{"t3code", home, workspace, bin, opt, "t3code"})
		for _, check := range []struct {
			out  pulumi.Output
			want any
		}{
			{t3.User.URN(), pulumi.URN(testURNPrefix + "homelab:index:User::user-t3code")},
			{t3.BinDirectory.URN(), pulumi.URN(testURNPrefix + "homelab:index:Directory::dir-t3code-bin")},
			{t3.PublicKey, testPublicKey},
		} {
			got, err := internals.UnsafeAwaitOutput(ctx.Context(), check.out)
			if err != nil {
				return err
			}
			equal(t, got.Known, true)
			deepEqual(t, got.Value, check.want)
		}
		return nil
	})

	const owner = "t3code"
	prefix := home + "/.t3/runtime/versions/" + T3Default.Version
	dir := func(path, mode string) map[string]any { return directoryInputs(path, owner, owner, mode) }
	link := func(path, target string) map[string]any { return map[string]any{"path": path, "target": target} }
	archive := func(name, prefix, bin, url, sha256 string) map[string]any {
		return map[string]any{"name": name, "prefix": prefix, "binary": bin, "url": url, "sha256": sha256,
			"strip": float64(1), "owner": owner, "group": owner, "healthCommand": "{}/" + bin + " --version", "versionCommand": "{}/" + bin + " --version"}
	}
	file := func(path, content string) map[string]any {
		props := dir(path, "0644")
		props["content"] = content
		props["reloadSystemd"] = false
		return props
	}
	official := func(bin, installer string) map[string]any {
		return map[string]any{"account": owner, "home": home, "bin": home + "/" + bin, "installer": installer}
	}
	unit := m.registration(t, "unit-t3code").Inputs["unit"].StringValue()
	contains(t, unit, "ExecStart="+bin+"/t3 serve "+workspace)
	// Exact bytes captured from the Node helper for these paths and default limits.
	equal(t, fmt.Sprintf("%x", sha256.Sum256([]byte(unit))), "1938029feaac0827ce18c4b17a1031504e99b39705929769e509f5bcc8064c4c")
	graph := map[string]struct {
		token  string
		inputs map[string]any
		deps   []string
	}{
		"user-t3code": {"User", map[string]any{"name": owner, "home": home, "shell": "/bin/bash",
			"createHome": true, "groups": []any{"kvm"}, "group": owner, "uid": float64(1100)}, []string{"group-kvm", "group-t3code"}},
		"dir-t3code-home":          {"Directory", dir(home, "0700"), []string{"user-t3code"}},
		"pkg-t3code-tools":         {"AptPackages", map[string]any{"names": []any{"git", "curl", "ca-certificates", "unzip"}}, nil},
		"dir-t3code-dotlocal":      {"Directory", dir(home+"/.local", "0755"), []string{"user-t3code"}},
		"dir-t3code-dott3":         {"Directory", dir(home+"/.t3", "0700"), []string{"user-t3code"}},
		"dir-t3code-runtime":       {"Directory", dir(home+"/.t3/runtime", "0755"), []string{"dir-t3code-dott3"}},
		"dir-t3code-versions":      {"Directory", dir(home+"/.t3/runtime/versions", "0755"), []string{"dir-t3code-runtime"}},
		"dir-t3code-bin":           {"Directory", dir(bin, "0755"), []string{"dir-t3code-dotlocal"}},
		"dir-t3code-opt":           {"Directory", dir(opt, "0755"), []string{"dir-t3code-dotlocal"}},
		"archive-t3":               {"Archive", archive("t3", prefix, "t3", T3Default.URL(T3Default.Version), T3Default.SHA256), []string{"user-t3code", "dir-t3code-versions"}},
		"file-t3-install-complete": {"ManagedFile", file(prefix+"/.install-complete", T3Default.Version+"\n"), []string{"archive-t3"}},
		"link-t3":                  {"Symlink", map[string]any{"path": bin + "/t3", "target": prefix + "/t3", "acceptLauncherScript": true}, []string{"archive-t3", "dir-t3code-bin"}},
		"archive-gh": {"Archive", archive("gh", opt+"/gh", "bin/gh", "https://github.com/cli/cli/releases/download/v2.106.0/gh_2.106.0_linux_amd64.tar.gz",
			"a476449fa9625413c54a3734eded62e9e480e49f421ff08e7f3b3879c059a033"), []string{"user-t3code", "dir-t3code-opt"}},
		"link-gh":           {"Symlink", link(bin+"/gh", opt+"/gh/bin/gh"), []string{"archive-gh", "dir-t3code-bin"}},
		"install-claude":    {"OfficialInstall", official(".local/bin/claude", "curl -fsSL https://claude.ai/install.sh | bash"), []string{"user-t3code", "dir-t3code-bin"}},
		"install-codex":     {"OfficialInstall", official(".local/bin/codex", "curl -fsSL https://chatgpt.com/codex/install.sh | CODEX_NON_INTERACTIVE=1 sh"), []string{"user-t3code", "dir-t3code-bin"}},
		"install-opencode":  {"OfficialInstall", official(".opencode/bin/opencode", "curl -fsSL https://opencode.ai/install | bash -s -- --no-modify-path"), []string{"user-t3code", "dir-t3code-bin"}},
		"link-opencode":     {"Symlink", link(bin+"/opencode", home+"/.opencode/bin/opencode"), []string{"install-opencode"}},
		"dir-t3code-dotssh": {"Directory", dir(home+"/.ssh", "0700"), []string{"user-t3code"}},
		"sshkey-t3code-git": {"SshKey", map[string]any{"path": home + "/.ssh/id_ed25519", "type": "ed25519", "comment": "t3code@homelab",
			"owner": owner, "group": owner}, []string{"dir-t3code-dotssh"}},
		"file-t3code-gitconfig": {"ManagedFile", file(home+"/.gitconfig", gitConfig), []string{"user-t3code"}},
		"sudo-t3code-apt":       {"SudoRule", map[string]any{"file": "030_t3code", "user": owner, "runAs": "root", "passwordless": true}, []string{"user-t3code"}},
		"unit-t3code":           {"SystemdUnit", map[string]any{"name": owner, "enabled": true, "started": true, "unit": unit}, []string{"archive-t3", "dir-t3code-versions", "dir-t3code-bin"}},
	}
	names := make([]string, 0, len(graph))
	for name, entry := range graph {
		names = append(names, name)
		t.Run(name, func(t *testing.T) {
			m.check(t, name, "homelab:index:"+entry.token, "", testProviderRef, entry.inputs, entry.deps...)
			equal(t, m.registration(t, name).RegisterRPC.GetImportId(), "")
		})
	}
	slices.Sort(names)
	deepEqual(t, m.namesExcept("project-stack", "machine", "group-t3code", "group-kvm"), names)
	deepEqual(t, m.registration(t, "link-t3").RegisterRPC.GetIgnoreChanges(), []string{"target"})
}

func TestT3CodeSecondaryAccountAndImportTransformation(t *testing.T) {
	m := runComponent(t, func(ctx *pulumi.Context) error {
		p, err := mockMachine(ctx)
		if err != nil {
			return err
		}
		err = ctx.RegisterStackTransformation(func(args *pulumi.ResourceTransformationArgs) *pulumi.ResourceTransformationResult {
			if args.Name != "other-link-t3" {
				return nil
			}
			props := args.Props.(*SymlinkArgs)
			return &pulumi.ResourceTransformationResult{
				Props: props, Opts: append(args.Opts, pulumi.Import(
					pulumi.Sprintf("%s@launcher", props.Path).ApplyT(func(path string) pulumi.ID { return pulumi.ID(path) }).(pulumi.IDOutput))),
			}
		})
		if err != nil {
			return err
		}
		_, err = T3CodeResource(ctx, p, &T3CodeArgs{
			Account: new("other"), Home: "/srv/other", Workspace: "/work",
			Agents: []AgentSpec{}, DisableSudo: true,
			T3: &T3Release{Version: "custom", SHA256: "test", URL: func(version string) string {
				return "https://example.invalid/" + version
			}},
			Unit: &UnitArgs{Name: new("t3-other"), Limits: UnitLimits{CPUWeight: new(25.0)}},
		})
		return err
	})
	for _, name := range []string{"sudo-other-apt", "other-archive-gh", "other-install-codex", "link-t3"} {
		_, found := m.registrations[name]
		equal(t, found, false)
	}
	equal(t, m.registration(t, "other-link-t3").RegisterRPC.GetImportId(), "/srv/other/.local/bin/t3@launcher")
	equal(t, m.registration(t, "other-archive-t3").Inputs["url"].StringValue(), "https://example.invalid/custom")
	equal(t, m.registration(t, "sshkey-other-git").Inputs["comment"].StringValue(), "other@t3-other")
	deepEqual(t, m.registration(t, "user-other").Inputs["groups"].Mappable(), []any{})
	unit := m.registration(t, "unit-other").Inputs["unit"].StringValue()
	contains(t, unit, "/etc/systemd/system/t3-other.service")
	contains(t, unit, "\nCPUWeight=25\n")
}
