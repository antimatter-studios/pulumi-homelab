// Package git holds the pure parsing for the GitCheckout resource, ported from src/resources/git.ts.
package git

import "strings"

// HeadMarker separates the two answers the read command prints: HEAD, then origin's URL.
const HeadMarker = "#pulumi-homelab#origin"

// Checkout is what a checkout is at.
type Checkout struct {
	Head string
	URL  string
}

// ParseCheckout splits the read command's output into HEAD and origin's URL. An origin git could
// not name comes back as the empty string.
func ParseCheckout(out string) Checkout {
	parts := strings.Split(out, HeadMarker+"\n")
	head, url := "", ""
	if len(parts) > 0 {
		head = parts[0]
	}
	if len(parts) > 1 {
		url = parts[1]
	}
	return Checkout{Head: strings.TrimSpace(head), URL: strings.TrimSpace(url)}
}
