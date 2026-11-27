// Package same compares JSON-shaped values regardless of the order of object keys. Ported from
// src/same.ts.
package same

import (
	"bytes"
	"encoding/json"
)

// SameJSON reports whether two JSON-shaped values are the same, ignoring the order of object keys
// (Pulumi stores object inputs and outputs with their keys sorted) but keeping array order, which
// carries meaning.
//
// A nil argument stands for TypeScript's undefined, a missing value: two missing values are the same,
// and a missing value is never the same as an empty object. Go's encoding/json decodes JSON null to
// nil as well, so unlike the TypeScript, nil here also covers null.
func SameJSON(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ea, errA := canonical(a)
	eb, errB := canonical(b)
	if errA != nil || errB != nil {
		return false
	}
	return bytes.Equal(ea, eb)
}

// canonical encodes a value with every map's keys sorted, which encoding/json does for maps.
func canonical(value any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
