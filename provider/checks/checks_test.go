package checks

import (
	"strings"
	"testing"
)

func TestMountedAt(t *testing.T) {
	t.Run("asks mountpoint first, since that is the tool for the question", func(t *testing.T) {
		if got := MountedAt("/mnt/data"); !strings.HasPrefix(got, "mountpoint -q '/mnt/data'") {
			t.Errorf("got %q", got)
		}
	})
	t.Run("falls back to findmnt for an image that has one and not the other", func(t *testing.T) {
		if got := MountedAt("/mnt/data"); !strings.Contains(got, "findmnt -rno TARGET") {
			t.Errorf("got %q", got)
		}
	})
	t.Run("quotes the path, which may contain anything a filesystem allows", func(t *testing.T) {
		if got := MountedAt("/mnt/it's here"); !strings.Contains(got, `'/mnt/it'\''s here'`) {
			t.Errorf("got %q", got)
		}
	})
}
