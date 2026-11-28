// Package rclone holds the pure parsing for the RcloneRemote resource, ported from
// src/resources/rclone.ts.
package rclone

import (
	"bytes"
	"encoding/json"
	"errors"

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
	settings, keys, _ := ParseDumpChecked(out, remote)
	return settings, keys
}

// ParseDumpChecked distinguishes a missing remote from an invalid dump. Errors
// omit the dump content because it can contain credentials.
func ParseDumpChecked(out, remote string) (map[string]string, []string, error) {
	var all map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &all); err != nil || all == nil {
		return nil, nil, errors.New("invalid rclone config dump")
	}
	raw, found := all[remote]
	if !found {
		return nil, nil, nil
	}
	section := bytes.TrimSpace(raw)
	if len(section) == 0 || section[0] != '{' {
		return nil, nil, errors.New("invalid remote in rclone config dump")
	}
	settings := map[string]string{}
	keys := []string{}
	dec := json.NewDecoder(bytes.NewReader(section))
	if _, err := dec.Token(); err != nil {
		return nil, nil, errors.New("invalid remote in rclone config dump")
	}
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return nil, nil, errors.New("invalid remote in rclone config dump")
		}
		key, ok := token.(string)
		if !ok {
			return nil, nil, errors.New("invalid remote in rclone config dump")
		}
		var value string
		if err := dec.Decode(&value); err != nil {
			return nil, nil, errors.New("invalid remote in rclone config dump")
		}
		if _, exists := settings[key]; !exists {
			keys = append(keys, key)
		}
		settings[key] = value
	}
	return settings, jscompat.KeyOrder(keys), nil
}
