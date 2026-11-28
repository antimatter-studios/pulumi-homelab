package homelab

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

type componentGoldenCase struct {
	Name   string
	Helper string
	Args   struct {
		Name, Shared, Entry, Dir, Version string
		Paths                             []string
		Options                           json.RawMessage
		Patch                             *string
		Backend                           *ClaudeSharedArgs
		Profile                           *ClaudeProfileArgs
		Groups                            bool
	}
}

func decodeGolden[T any](t *testing.T, data []byte) T {
	t.Helper()
	var value T
	if len(data) != 0 {
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
	}
	return value
}

func readGolden[T any](t *testing.T, path string) T {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return decodeGolden[T](t, data)
}

func TestComponentGolden(t *testing.T) {
	cases := readGolden[[]componentGoldenCase](t, "testdata/components-cases.json")
	golden := readGolden[map[string]json.RawMessage](t, "testdata/components-golden.json")
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			want, ok := golden[tc.Name]
			if !ok {
				t.Fatal("missing Node golden entry")
			}
			delete(golden, tc.Name)
			got, err := json.MarshalIndent(runGoldenCase(t, tc), "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(decodeGolden[any](t, got), decodeGolden[any](t, want)) {
				t.Errorf("Go result differs from Node golden\nGo:\n%s\nNode:\n%s", got, want)
			}
		})
	}
	if len(golden) != 0 {
		t.Errorf("%d Node golden entries have no Go case", len(golden))
	}
}

func runGoldenCase(t *testing.T, tc componentGoldenCase) any {
	t.Helper()
	a := tc.Args
	switch tc.Helper {
	case "fluxReady":
		return FluxReady(a.Name, decodeGolden[*FluxCheckArgs](t, a.Options))
	case "fluxReason":
		return FluxReason(a.Name, decodeGolden[*FluxCheckArgs](t, a.Options))
	case "ignoreRules":
		return IgnoreRules(a.Paths)
	case "sharedTarget":
		return SharedTarget(a.Shared, a.Entry)
	case "profileRefusal":
		if refusal := ProfileRefusal(a.Shared, a.Dir); refusal != nil {
			return refusal.Error()
		}
		return nil
	case "sharedDefaults":
		entries := make([]map[string]any, 0, len(SharedDefaults))
		for _, entry := range SharedDefaults {
			props := map[string]any{"name": entry.Name, "kind": entry.Kind}
			if entry.Mode != nil {
				props["mode"] = *entry.Mode
			}
			entries = append(entries, props)
		}
		return entries
	case "t3Default":
		version := a.Version
		if version == "" {
			version = T3Default.Version
		}
		return map[string]any{"version": T3Default.Version, "sha256": T3Default.SHA256, "url": T3Default.URL(version)}
	case "agentDefaults":
		agents := make([]map[string]any, 0, len(AgentDefaults))
		for _, agent := range AgentDefaults {
			switch spec := agent.(type) {
			case ArchiveAgentSpec:
				agents = append(agents, map[string]any{"key": spec.Key, "bin": spec.Bin, "url": spec.URL,
					"sha256": spec.SHA256, "strip": spec.Strip})
			case OfficialAgentSpec:
				props := map[string]any{"key": spec.Key, "bin": spec.Bin, "installer": spec.Installer}
				if spec.Link {
					props["link"] = true
				}
				agents = append(agents, props)
			default:
				t.Fatalf("unexpected agent type %T", agent)
			}
		}
		return agents
	case "unitResourceLines":
		lines, err := UnitResourceLines(decodeGolden[*UnitArgs](t, a.Options))
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		return map[string]any{"value": lines}
	case "fluxApp":
		return captureGoldenResources(t, func(ctx *pulumi.Context) error {
			return registerGoldenFlux(ctx, a.Name, decodeGolden[goldenFluxOptions](t, a.Options), a.Patch)
		})
	case "claudeResources":
		return captureGoldenResources(t, func(ctx *pulumi.Context) error {
			provider, err := mockMachine(ctx)
			if err != nil {
				return err
			}
			shared, err := ClaudeSharedResource(ctx, provider, "claude-backend", a.Backend, pulumi.Protect(true))
			if err != nil {
				return err
			}
			a.Profile.Shared = shared
			_, err = ClaudeProfileResource(ctx, provider, "claude-work", a.Profile, pulumi.Protect(true))
			return err
		})
	case "t3codeResource":
		type t3Options struct {
			T3CodeArgs
			Sudo *bool
		}
		options := decodeGolden[t3Options](t, a.Options)
		options.DisableSudo = options.Sudo != nil && !*options.Sudo
		return captureGoldenResources(t, func(ctx *pulumi.Context) error {
			return registerGoldenT3(ctx, &options.T3CodeArgs, a.Groups)
		})
	default:
		t.Fatalf("unknown helper %q", tc.Helper)
		return nil
	}
}

type goldenFluxOptions struct {
	URL                                                                     string
	Branch, Path, Namespace, Interval, ReconcileInterval, Ignore, SecretRef *string
	Prune                                                                   *bool
	Include                                                                 []string
}

func registerGoldenFlux(ctx *pulumi.Context, name string, options goldenFluxOptions, patch *string) error {
	input := func(s *string) pulumi.StringInput {
		if s == nil {
			return nil
		}
		return pulumi.String(*s)
	}
	args := &FluxAppArgs{
		URL: pulumi.String(options.URL), Branch: input(options.Branch), Path: input(options.Path),
		Namespace: input(options.Namespace), Interval: input(options.Interval), ReconcileInterval: input(options.ReconcileInterval),
		Include: options.Include, Ignore: input(options.Ignore), SecretRef: input(options.SecretRef),
	}
	if options.Prune != nil {
		args.Prune = pulumi.Bool(*options.Prune)
	}
	var opts []pulumi.ResourceOption
	if patch != nil {
		cluster := &mockProvider{}
		if err := ctx.RegisterResource("pulumi:providers:kubernetes", "cluster", pulumi.Map{}, cluster); err != nil {
			return err
		}
		opts = append(opts, pulumi.Provider(cluster), pulumi.Transformations([]pulumi.ResourceTransformation{
			func(args *pulumi.ResourceTransformationArgs) *pulumi.ResourceTransformationResult {
				if args.Type != "kubernetes:kustomize.toolkit.fluxcd.io/v1:Kustomization" {
					return nil
				}
				props := args.Props.(kubernetes.UntypedArgs)
				props["spec"].(pulumi.Map)["patches"] = pulumi.Array{pulumi.Map{
					"target": pulumi.Map{"kind": pulumi.String("HelmRelease"), "name": pulumi.String(name)},
					"patch":  pulumi.String(*patch).ToStringOutput(),
				}}
				return &pulumi.ResourceTransformationResult{Props: props, Opts: args.Opts}
			},
		}))
	}
	_, err := NewFluxApp(ctx, name, args, opts...)
	return err
}

func registerGoldenT3(ctx *pulumi.Context, args *T3CodeArgs, hasGroups bool) error {
	provider, err := mockMachine(ctx)
	if err != nil {
		return err
	}
	if hasGroups {
		args.PrimaryGroup, err = NewGroup(ctx, "group-t3code", &GroupArgs{
			Name: pulumi.String("t3code"), Gid: pulumi.Int(*args.UID),
		}, pulumi.Provider(provider))
		if err != nil {
			return err
		}
		group, err := NewGroup(ctx, "group-kvm", &GroupArgs{Name: pulumi.String("kvm")}, pulumi.Provider(provider))
		if err != nil {
			return err
		}
		args.AccountDependsOn = []pulumi.Resource{group}
	}
	_, err = T3CodeResource(ctx, provider, args)
	return err
}

func captureGoldenResources(t *testing.T, register pulumi.RunFunc) any {
	t.Helper()
	var failure any
	m := runComponent(t, func(ctx *pulumi.Context) error {
		if err := register(ctx); err != nil {
			failure = err.Error()
		}
		return nil
	})
	resources := []map[string]any{}
	for _, name := range m.namesExcept("project-stack", "machine", "cluster", "group-t3code", "group-kvm") {
		reg := m.registration(t, name)
		rpc := reg.RegisterRPC
		deps := append([]string{}, rpc.GetDependencies()...)
		slices.Sort(deps)
		resources = append(resources, map[string]any{
			"name": name, "type": reg.TypeToken, "inputs": reg.Inputs.Mappable(),
			"parent": parentPath(rpc.GetParent()), "provider": rpc.GetProvider(), "dependencies": deps,
			"version": rpc.GetVersion(), "importId": rpc.GetImportId(),
			"ignoreChanges": append([]string{}, rpc.GetIgnoreChanges()...), "protect": rpc.GetProtect(),
		})
	}
	return map[string]any{"error": failure, "resources": resources}
}
