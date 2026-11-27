// Package rclone holds the pure parsing for the RcloneRemote resource, ported from
// src/resources/rclone.ts.
package rclone

import "encoding/json"

// ParseDump finds a remote's settings in the output of `rclone config dump`, or nil when the remote
// is not there or the output is not a dump at all.
func ParseDump(out string, remote string) map[string]string {
	var all map[string]map[string]string
	if err := json.Unmarshal([]byte(out), &all); err != nil {
		return nil
	}
	settings, ok := all[remote]
	if !ok {
		return nil
	}
	return settings
}
