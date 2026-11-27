// Command pulumi-resource-homelab is the homelab provider plugin. Pulumi starts it by name; it
// serves the provider over gRPC on the port it prints, and exits when the engine is done with it.
package main

import (
	"context"
	"fmt"
	"os"

	homelab "github.com/antimatter-studios/pulumi-homelab/provider"
)

func main() {
	if err := homelab.Provider().Run(context.Background(), homelab.Name, homelab.Version); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}
