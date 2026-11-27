// Package tunnel holds the pure parsers from src/resources/tunnel.ts: the reverse forwards read
// off the running process's command line, and that read together with systemd's properties.
package tunnel

import (
	"math"
	"strings"

	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
	"github.com/antimatter-studios/pulumi-homelab/provider/systemd"
)

// Forward is one reverse forward. Bind is "" when the forward carries no bind address, where the
// TypeScript leaves the property off.
type Forward struct {
	Remote int
	Local  int
	Bind   string
}

// port is the field as a port number; ok is false unless it is an integer in 1..65535. A missing
// field is NaN, as Number(undefined) is.
func port(parts []string, at int) (int, bool) {
	if at >= len(parts) {
		return 0, false
	}
	n := jscompat.Number(parts[at])
	if n != math.Trunc(n) || n <= 0 || n >= 65536 {
		return 0, false
	}
	return int(n), true
}

// ParseForwards reads the -R forwards off a NUL-to-newline command line, one argument per line:
// `[bind:]remote:host:local`, kept only when both ports are in range.
func ParseForwards(cmdline string) []Forward {
	args := []string{}
	for _, arg := range strings.Split(cmdline, "\n") {
		if arg = jscompat.Trim(arg); arg != "" {
			args = append(args, arg)
		}
	}
	found := []Forward{}
	for at, arg := range args {
		if arg != "-R" || at+1 >= len(args) {
			continue
		}
		parts := strings.Split(args[at+1], ":")
		bind := ""
		// four parts when a bind address is present; otherwise the parts are read as if a missing
		// bind came first, so remote is part 0 and local is part 2
		if len(parts) == 4 {
			bind = parts[0]
		} else {
			parts = append([]string{""}, parts...)
		}
		remote, okRemote := port(parts, 1)
		local, okLocal := port(parts, 3)
		if !okRemote || !okLocal {
			continue
		}
		found = append(found, Forward{Remote: remote, Local: local, Bind: bind})
	}
	return found
}

const cmdlineMarker = "#pulumi-homelab#cmdline"

// Tunnel is what systemd and /proc say about a running tunnel.
type Tunnel struct {
	Active   bool
	Restarts int
	Running  []Forward
}

// ParseTunnel splits the reply at the marker into `systemctl show` properties, read by key, and
// the process's command line. Restarts is 0 when systemd gave nothing, or nothing numeric.
func ParseTunnel(out string) Tunnel {
	parts := strings.Split(out, cmdlineMarker+"\n")
	shown, cmdline := parts[0], ""
	if len(parts) > 1 {
		cmdline = parts[1]
	}
	properties := systemd.ParseShow(shown)
	restarts := "0"
	if value, ok := properties["NRestarts"]; ok {
		restarts = value
	}
	n := jscompat.Number(restarts)
	if math.IsNaN(n) {
		n = 0
	}
	return Tunnel{
		Active:   properties["ActiveState"] == "active",
		Restarts: int(n),
		Running:  ParseForwards(cmdline),
	}
}
