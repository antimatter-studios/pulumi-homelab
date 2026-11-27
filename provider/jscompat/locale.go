package jscompat

import (
	"sync"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// collators are English collators with the options node's default Intl.Collator resolves to (locale
// en-US, usage sort, sensitivity variant, no numeric ordering, no case-first override, punctuation
// not ignored): CLDR root order, which is what en uses, at tertiary strength. A Collator keeps
// buffers and is not safe for concurrent use, hence the pool.
var collators = sync.Pool{New: func() any { return collate.New(language.English) }}

// LocaleCompare is a.localeCompare(b) as node runs it with no locale given: -1, 0 or 1. Canonically
// equivalent spellings (a precomposed é and e followed by U+0301) compare equal, as in node, so a
// stable sort keeps them in input order. Differential-tested against node in locale_node_test.go.
func LocaleCompare(a, b string) int {
	c := collators.Get().(*collate.Collator)
	defer collators.Put(c)
	return c.CompareString(a, b)
}
