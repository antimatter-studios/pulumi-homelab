// Package resolved finds where what a resource declared and what the system effectively does
// disagree. Ported from the pure part of src/resolved.ts.
package resolved

import "sort"

// Disagreeing returns the declared keys the system resolved differently, sorted so a message reads
// the same every time. A key the system does not mention at all is not counted: several readers
// are partial by nature. key maps a declared name into the alphabet the system answers in (sshd -T
// lowercases every keyword); nil means the identity.
func Disagreeing(declared, effective map[string]string, key func(string) string) []string {
	if key == nil {
		key = func(name string) string { return name }
	}
	found := []string{}
	for name, value := range declared {
		answered, ok := effective[key(name)]
		if ok && answered != value {
			found = append(found, name)
		}
	}
	// Array.prototype.sort with no comparator orders by UTF-16 code units; for the keys these
	// readers produce (ASCII config keywords) that is the same as Go's byte order
	sort.Strings(found)
	return found
}
