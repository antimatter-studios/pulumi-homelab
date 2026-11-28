package homelab

import (
	"fmt"
	"strings"

	"github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes"
	"github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/apiextensions"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

const (
	kubernetesPluginVersion = "4.34.0"
	fluxSourceAPI           = "source.toolkit.fluxcd.io/v1"
	fluxKustomizeAPI        = "kustomize.toolkit.fluxcd.io/v1"
	fluxNamespace           = "flux-system"
	fluxKubeconfig          = "/etc/rancher/k3s/k3s.yaml"
)

// FluxAppArgs describes the repository Flux watches and which manifests it applies.
type FluxAppArgs struct {
	URL               pulumi.StringInput
	Branch            pulumi.StringInput
	Path              pulumi.StringInput
	Namespace         pulumi.StringInput
	Interval          pulumi.StringInput
	ReconcileInterval pulumi.StringInput
	Prune             pulumi.BoolInput
	// Include is shorthand for Ignore. A non-nil list and Ignore are mutually exclusive.
	Include []string
	Ignore  pulumi.StringInput
	// SecretRef names an existing credentials secret in the Flux namespace.
	SecretRef pulumi.StringInput
}

// FluxApp pairs a GitRepository with a Kustomization that applies its manifests.
type FluxApp struct {
	pulumi.ResourceState
	Source        *apiextensions.CustomResource
	Kustomization *apiextensions.CustomResource
}

// IgnoreRules turns a list of included paths into a Flux gitignore expression.
func IgnoreRules(include []string) string {
	rules := []string{"/*"}
	for _, path := range include {
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		rules = append(rules, "!"+path)
	}
	return strings.Join(rules, "\n")
}

// NewFluxApp registers the component and its two Kubernetes resources. Component
// transformations can set spec.patches on the Kustomization's kubernetes.UntypedArgs.
func NewFluxApp(ctx *pulumi.Context, name string, args *FluxAppArgs, opts ...pulumi.ResourceOption) (*FluxApp, error) {
	if args == nil || args.URL == nil {
		return nil, fmt.Errorf("%s: FluxApp requires a repository URL", name)
	}
	app := &FluxApp{}
	if err := ctx.RegisterComponentResource("homelab:flux:FluxApp", name, app, opts...); err != nil {
		return nil, err
	}
	if args.Include != nil && args.Ignore != nil {
		return nil, fmt.Errorf("%s: give either include or ignore, not both — two answers to one question", name)
	}

	namespace := fluxStringInput(args.Namespace, fluxNamespace)
	metadata := metav1.ObjectMetaArgs{Name: pulumi.String(name), Namespace: namespace}
	sourceSpec := pulumi.Map{
		"url":      args.URL,
		"ref":      pulumi.Map{"branch": fluxStringInput(args.Branch, "main")},
		"interval": fluxStringInput(args.Interval, "1m"),
	}
	if args.Include != nil {
		sourceSpec["ignore"] = pulumi.String(IgnoreRules(args.Include))
	} else if args.Ignore != nil {
		sourceSpec["ignore"] = args.Ignore
	}
	// Node omits a literal empty string but accepts an Output that resolves to one.
	if args.SecretRef != nil && args.SecretRef != pulumi.String("") {
		sourceSpec["secretRef"] = pulumi.Map{"name": args.SecretRef}
	}

	source, err := apiextensions.NewCustomResource(ctx, name+"-source", &apiextensions.CustomResourceArgs{
		ApiVersion:  pulumi.String(fluxSourceAPI),
		Kind:        pulumi.String("GitRepository"),
		Metadata:    metadata,
		OtherFields: kubernetes.UntypedArgs{"spec": sourceSpec},
	}, pulumi.Parent(app), pulumi.Version(kubernetesPluginVersion))
	if err != nil {
		return nil, err
	}
	app.Source = source

	sourceName := source.Metadata.ApplyT(func(meta *metav1.ObjectMeta) string {
		if meta != nil && meta.Name != nil {
			return *meta.Name
		}
		return name
	}).(pulumi.StringOutput)
	prune := args.Prune
	if prune == nil {
		prune = pulumi.Bool(true)
	}
	app.Kustomization, err = apiextensions.NewCustomResource(ctx, name+"-kustomization", &apiextensions.CustomResourceArgs{
		ApiVersion: pulumi.String(fluxKustomizeAPI),
		Kind:       pulumi.String("Kustomization"),
		Metadata:   metadata,
		OtherFields: kubernetes.UntypedArgs{"spec": pulumi.Map{
			"sourceRef": pulumi.Map{
				"kind":      pulumi.String("GitRepository"),
				"name":      sourceName,
				"namespace": namespace,
			},
			"path":     fluxStringInput(args.Path, "./"),
			"interval": fluxStringInput(args.ReconcileInterval, "5m"),
			"prune":    prune,
		}},
	}, pulumi.Parent(app), pulumi.Version(kubernetesPluginVersion), pulumi.DependsOn([]pulumi.Resource{source}))
	if err != nil {
		return nil, err
	}
	if err := ctx.RegisterResourceOutputs(app, pulumi.Map{"source": pulumi.NewResourceInput(source), "kustomization": pulumi.NewResourceInput(app.Kustomization)}); err != nil {
		return nil, err
	}
	return app, nil
}

func fluxStringInput(input pulumi.StringInput, fallback string) pulumi.StringInput {
	if input != nil {
		return input
	}
	return pulumi.String(fallback)
}

// FluxCheckArgs overrides values used in the Flux readiness and diagnostic commands.
type FluxCheckArgs struct {
	Kind       *string
	Namespace  *string
	Timeout    *string
	Kubeconfig *string
}

// FluxReady returns a kubectl wait command for use in a Precondition.
func FluxReady(name string, args *FluxCheckArgs) string {
	if args == nil {
		args = &FluxCheckArgs{}
	}
	return fmt.Sprintf("KUBECONFIG=%s kubectl -n %s wait %s --for=condition=Ready --timeout=%s >/dev/null 2>&1",
		fluxShellQuote(stringOr(args.Kubeconfig, fluxKubeconfig)),
		fluxShellQuote(stringOr(args.Namespace, fluxNamespace)),
		fluxShellQuote(stringOr(args.Kind, "kustomization")+"/"+name),
		fluxShellQuote(stringOr(args.Timeout, "120s")))
}

// FluxReason returns the diagnostic command to show in a failed Precondition's message.
func FluxReason(name string, args *FluxCheckArgs) string {
	if args == nil {
		args = &FluxCheckArgs{}
	}
	return fmt.Sprintf(`sudo KUBECONFIG=%s kubectl -n %s get %s %s -o jsonpath='{.status.conditions[?(@.type=="Ready")].message}'`,
		stringOr(args.Kubeconfig, fluxKubeconfig), stringOr(args.Namespace, fluxNamespace),
		stringOr(args.Kind, "kustomization"), name)
}

func fluxShellQuote(text string) string {
	return "'" + strings.ReplaceAll(text, "'", `'\''`) + "'"
}

func stringOr(value *string, fallback string) string {
	if value != nil {
		return *value
	}
	return fallback
}
