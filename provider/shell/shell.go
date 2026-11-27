// Package shell holds the pure shell-quoting helpers from src/ssh.ts.
package shell

import "strings"

// Quote quotes a string so a POSIX shell treats it as one argument, whatever is in it. Single
// quotes are literal; a single quote inside is closed, escaped and reopened.
func Quote(text string) string {
	return "'" + strings.ReplaceAll(text, "'", `'\''`) + "'"
}
