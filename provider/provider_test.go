package homelab

import (
	"encoding/json"
	"testing"

	"github.com/blang/semver"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/integration"
)

// The plugin starts and describes itself as the homelab package.
func TestSchemaNamesThePackage(t *testing.T) {
	server, err := integration.NewServer(t.Context(), Name, semver.MustParse("0.0.1"), integration.WithProvider(Provider()))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.GetSchema(p.GetSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(resp.Schema), &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Name != Name {
		t.Fatalf("schema name = %q, want %q", schema.Name, Name)
	}
}
