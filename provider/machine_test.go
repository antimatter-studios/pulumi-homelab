package homelab

import (
	"testing"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote"
)

// Errors name the machine and the route, as the TypeScript's describe does.
func TestDescribeNamesTheRoute(t *testing.T) {
	for _, tc := range []struct {
		cfg  remote.Config
		want string
	}{
		{remote.Config{User: "chris", Address: "192.168.0.47", Port: 22}, "chris@192.168.0.47"},
		{remote.Config{User: "chris", Address: "127.0.0.1", Port: 20022,
			Jumps: []remote.Jump{{User: "admin", Address: "s2.antimatter-studios.com", Port: 22}}},
			"chris@127.0.0.1:20022 via admin@s2.antimatter-studios.com"},
		{remote.Config{User: "chris", Address: "10.0.0.5", Port: 22,
			Jumps: []remote.Jump{{User: "a", Address: "one", Port: 2222}, {User: "b", Address: "two", Port: 22}}},
			"chris@10.0.0.5 via a@one:2222,b@two"},
	} {
		if got := (&machine{cfg: tc.cfg}).describe(); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}
