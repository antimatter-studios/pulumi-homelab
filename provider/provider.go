// Package homelab is the homelab Pulumi provider: desired state for a Linux machine you own, kept
// over a remote connection the project configures explicitly.
//
// Every resource token is homelab:index:<Type>. The TypeScript dynamic providers under src/ stay
// in service while the resources move here one type at a time (tracked on #31).
package homelab

import (
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
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
	return infer.Provider(options())
}

func options() infer.Options {
	return infer.NewProviderBuilder().
		WithDisplayName("homelab").
		WithDescription("Desired state for a Linux machine you own.").
		WithNamespace("antimatter-studios").
		WithRepository("https://github.com/antimatter-studios/pulumi-homelab").
		WithLicense("MIT").
		WithPluginDownloadURL(PluginDownloadURL).
		WithKeywords("homelab", "linux", "ssh", "category/infrastructure").
		BuildOptions()
}
