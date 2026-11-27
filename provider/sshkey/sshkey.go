// Package sshkey holds the pure parsing for the SshKey resource and host-key reading, ported from
// src/resources/sshkey.ts.
package sshkey

import (
	"strings"
	"unicode"
)

// isJSSpace is what JavaScript's \s and String.prototype.trim treat as whitespace.
func isJSSpace(r rune) bool { return unicode.IsSpace(r) || r == 0xFEFF }

// words is JavaScript's s.trim().split(/\s+/), except that an empty input gives no words where
// JavaScript gives one empty one; callers here treat both the same.
func words(s string) []string { return strings.FieldsFunc(s, isJSSpace) }

// ParseFingerprint takes the fingerprint (the second word) out of `ssh-keygen -lf` output, or ""
// when there is nothing to read.
func ParseFingerprint(out string) string {
	w := words(out)
	if len(w) < 2 {
		return ""
	}
	return w[1]
}

// ParseHostKeys reads lines of `<path> <ssh-keygen -lf output>` into fingerprints keyed by
// algorithm. A key ssh-keygen could not read is skipped rather than recorded as empty.
func ParseHostKeys(out string) map[string]string {
	found := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		w := words(line)
		if len(w) == 0 || w[0] == "" {
			continue
		}
		file := w[0]
		// file.replace(/^.*ssh_host_/, '') is greedy, so it cuts through the last occurrence
		if i := strings.LastIndex(file, "ssh_host_"); i >= 0 {
			file = file[i+len("ssh_host_"):]
		}
		keyType := strings.TrimSuffix(file, "_key.pub")
		fingerprint := ""
		if len(w) > 2 {
			fingerprint = w[2]
		}
		if fingerprint != "" {
			found[keyType] = fingerprint
		}
	}
	return found
}
