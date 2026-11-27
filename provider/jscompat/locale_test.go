package jscompat

import (
	"slices"
	"testing"
)

// The order node gives with keys.sort((a, b) => a.localeCompare(b)), pinned.
func TestLocaleCompare(t *testing.T) {
	want := []string{"group:x", "user:", "user:$x", "user:a", "user:A", "user:a_b", "user:a-b", "user:a:b", "user:a.b", "user:a1", "user:b", "user:B", "user:z"}
	got := []string{"user:a_b", "user:a-b", "user:a.b", "user:a1", "user:A", "user:a", "user:B", "user:b", "group:x", "user:", "user:a:b", "user:$x", "user:z"}
	slices.SortStableFunc(got, LocaleCompare)
	if !slices.Equal(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if LocaleCompare("a", "a") != 0 {
		t.Fatal("a string is not equal to itself")
	}
}
