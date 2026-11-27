// Package rclone holds the pure parsing for the RcloneRemote resource, ported from
// src/resources/rclone.ts.
package rclone

import (
	"bytes"
	"encoding/json"

	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
)

// ParseDump finds a remote's settings in the output of `rclone config dump`, or nil when the remote
// is not there or the output is not a dump at all.
func ParseDump(out string, remote string) map[string]string {
	settings, _ := ParseDumpOrdered(out, remote)
	return settings
}

// ParseDumpOrdered also returns the JavaScript enumeration order of the remote's fields.
func ParseDumpOrdered(out, remote string) (map[string]string, []string) {
	var all map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &all); err != nil {
		return nil, nil
	}
	section := bytes.TrimSpace(all[remote])
	if len(section) == 0 || section[0] != '{' {
		return nil, nil
	}
	settings := map[string]string{}
	keys := []string{}
	dec := json.NewDecoder(bytes.NewReader(section))
	if _, err := dec.Token(); err != nil {
		return nil, nil
	}
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return nil, nil
		}
		key, ok := token.(string)
		if !ok {
			return nil, nil
		}
		var value string
		if err := dec.Decode(&value); err != nil {
			return nil, nil
		}
		if _, exists := settings[key]; !exists {
			keys = append(keys, key)
		}
		settings[key] = value
	}
	return settings, jscompat.KeyOrder(keys)
}
