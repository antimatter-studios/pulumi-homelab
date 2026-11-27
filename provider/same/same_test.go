package same

import "testing"

type m = map[string]any

func TestSameJSON(t *testing.T) {
	check := func(t *testing.T, a, b any, want bool) {
		t.Helper()
		if got := SameJSON(a, b); got != want {
			t.Errorf("SameJSON(%v, %v) = %v, want %v", a, b, got, want)
		}
	}
	t.Run("ignores the order of object keys, which Pulumi sorts when it stores state", func(t *testing.T) {
		check(t, m{"AllowTcpForwarding": "yes", "GatewayPorts": "no"}, m{"GatewayPorts": "no", "AllowTcpForwarding": "yes"}, true)
		check(t, m{"a": m{"y": 1, "x": 2}}, m{"a": m{"x": 2, "y": 1}}, true)
	})
	t.Run("still sees a changed, added or removed value", func(t *testing.T) {
		check(t, m{"a": "yes"}, m{"a": "no"}, false)
		check(t, m{"a": "yes"}, m{"a": "yes", "b": "no"}, false)
		check(t, m{"a": "yes", "b": "no"}, m{"a": "yes"}, false)
	})
	t.Run("keeps array order, which carries meaning", func(t *testing.T) {
		check(t, []any{"a", "b"}, []any{"b", "a"}, false)
	})
	t.Run("treats a missing object like undefined, not like an empty one", func(t *testing.T) {
		check(t, nil, m{}, false)
		check(t, nil, nil, true)
	})
}
