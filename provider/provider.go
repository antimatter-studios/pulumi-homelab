// Package homelab is the homelab Pulumi provider: desired state for a Linux machine you own, kept
// over a remote connection the project configures explicitly.
//
// Every resource token is homelab:index:<Type>. The TypeScript dynamic providers under src/ stay
// in service while the resources move here one type at a time (tracked on #31).
package homelab

import (
	"context"
	"slices"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote"
)

// Name is the package name: the plugin binary is pulumi-resource-homelab and every token starts
// with homelab:.
const Name = "homelab"

// Version is the provider's version. Release builds set it with
// -ldflags "-X github.com/antimatter-studios/pulumi-homelab/provider.Version=<version>".
var Version = "0.0.0-dev"

// PluginDownloadURL is where Pulumi fetches the plugin: the assets of this repository's GitHub
// releases, which .github/workflows/release.yml attaches.
const PluginDownloadURL = "github://api.github.com/antimatter-studios"

// Provider builds the provider the plugin serves.
func Provider() p.Provider {
	return markSecretsOnRead(keepEngineVersion(infer.Provider(options(nil))))
}

// ProviderWith builds the provider with an injected Runner factory, for tests.
func ProviderWith(factory remote.Factory) p.Provider {
	return markSecretsOnRead(keepEngineVersion(infer.Provider(options(factory))))
}

// keepEngineVersion puts the engine's "version" back into the checked configuration. A provider's
// state is its checked inputs, and the library diffs the state's version against the next
// program's; but checking decodes the inputs into Config, which has no version, so without this
// the state never has one and every later preview shows the provider changing (+version) — no
// switch could ever show zero changes. The value is the engine's own, passed through untouched.
func keepEngineVersion(prov p.Provider) p.Provider {
	check := prov.CheckConfig
	prov.CheckConfig = func(ctx context.Context, req p.CheckRequest) (p.CheckResponse, error) {
		resp, err := check(ctx, req)
		if err != nil {
			return resp, err
		}
		if v, ok := req.Inputs.GetOk("version"); ok {
			resp.Inputs = resp.Inputs.Set("version", v)
		}
		return resp, nil
	}
	return prov
}

// secretsOnRead names, per resource type, the fields that hold credentials. infer marks such a field
// secret when it builds the value from declared inputs, but a Read that starts from an id alone (an
// import) encodes what it found on the machine as plain values, and the engine stores them so.
var secretsOnRead = map[string][]string{"homelab:index:RcloneRemote": {"secrets"}}

// secretsOnImport names fields that may hold credentials the provider cannot pick out when a Read
// starts from an id alone: an S3 remote keeps its keys in settings, in the clear, because rclone
// obscures whatever is under secrets and an obscured S3 key does not work. The whole field is marked
// secret on such a Read; a later Read keeps whatever marking the state then has.
var secretsOnImport = map[string][]string{"homelab:index:RcloneRemote": {"settings"}}

func markSecretsOnRead(prov p.Provider) p.Provider {
	read := prov.Read
	prov.Read = func(ctx context.Context, req p.ReadRequest) (p.ReadResponse, error) {
		resp, err := read(ctx, req)
		if err != nil {
			return resp, err
		}
		keys := secretsOnRead[string(req.Urn.Type())]
		if req.Properties.Len() == 0 && req.Inputs.Len() == 0 {
			keys = append(slices.Clone(keys), secretsOnImport[string(req.Urn.Type())]...)
		}
		for _, key := range keys {
			if v, ok := resp.Properties.GetOk(key); ok {
				resp.Properties = resp.Properties.Set(key, v.WithSecret(true))
			}
			if v, ok := resp.Inputs.GetOk(key); ok {
				resp.Inputs = resp.Inputs.Set(key, v.WithSecret(true))
			}
		}
		return resp, nil
	}
	return prov
}

func options(factory remote.Factory) infer.Options {
	return infer.NewProviderBuilder().
		WithConfig(infer.Config(&Config{factory: factory})).
		WithResources(infer.Resource(Group{}), infer.Resource(AptPackage{}), infer.Resource(AptPackages{}), infer.Resource(DebPackage{}), infer.Resource(Archive{}), infer.Resource(OfficialInstall{}), infer.Resource(GitCheckout{}), infer.Resource(RcloneRemote{}), infer.Resource(PosixAcl{}), infer.Resource(SambaShare{}), infer.Resource(SambaSetting{}), infer.Resource(SambaUser{}), infer.Resource(SshTunnel{}), infer.Resource(Symlink{}), infer.Resource(Directory{}), infer.Resource(Precondition{}), infer.Resource(User{}), infer.Resource(SudoRule{}), infer.Resource(AuthorizedKey{}), infer.Resource(SshKey{}), infer.Resource(ManagedLine{}), infer.Resource(KernelCmdline{}), infer.Resource(SystemdUnit{}), infer.Resource(SystemdInstance{}), infer.Resource(Journald{}), infer.Resource(Hostname{}), infer.Resource(FstabEntry{}), infer.Resource(Swap{}), infer.Resource(SshdConfig{}), infer.Resource(BootConfig{}), infer.Resource(ManagedFile{}), infer.Resource(ProtectedFile{})).
		WithDisplayName("homelab").
		WithDescription("Desired state for a Linux machine you own.").
		WithNamespace("antimatter-studios").
		WithRepository("https://github.com/antimatter-studios/pulumi-homelab").
		WithLicense("MIT").
		WithPluginDownloadURL(PluginDownloadURL).
		// The npm name the generated SDK carries. Left unset, Pulumi names it @pulumi/homelab, which
		// claims Pulumi's own scope for a package they do not publish.
		WithLanguageMap(map[string]any{"nodejs": map[string]any{"packageName": "@antimatter-studios/homelab"}}).
		WithKeywords("homelab", "linux", "ssh", "category/infrastructure").
		BuildOptions()
}
