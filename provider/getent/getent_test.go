package getent

import (
	"reflect"
	"testing"
)

func TestParseUserRead(t *testing.T) {
	t.Run("takes the uid from passwd and leaves the primary group out of the memberships", func(t *testing.T) {
		read := ParseUserRead("smbguest", "smbguest:x:999:100::/home/smbguest:/usr/sbin/nologin\nusers rogroup\nusers\n")
		if read.UID == nil || *read.UID != 999 {
			t.Errorf("uid = %v, want 999", read.UID)
		}
		if read.Group != "users" {
			t.Errorf("group = %q, want users", read.Group)
		}
		if want := []string{"rogroup"}; !reflect.DeepEqual(read.Groups, want) {
			t.Errorf("groups = %q, want %q", read.Groups, want)
		}
	})
	t.Run("trims and splits on JavaScript's whitespace, so a NEL is text", func(t *testing.T) {
		read := ParseUserRead("a", "a:x:1000:1000::/home/a:/bin/sh\nb a c\u0085d\n\u0085")
		if read.Group != "\u0085" {
			t.Errorf("group = %q, want NEL", read.Group)
		}
		if want := []string{"a", "b", "c\u0085d"}; !reflect.DeepEqual(read.Groups, want) {
			t.Errorf("groups = %q, want %q", read.Groups, want)
		}
		read = ParseUserRead("a", "\ufeffa:x:0x3e8:1000::/home/a:/bin/sh\nb\u00a0c\u3000users\nusers\ufeff")
		if read.UID == nil || *read.UID != 1000 || read.Group != "users" || !reflect.DeepEqual(read.Groups, []string{"b", "c"}) {
			t.Errorf("BOM, NBSP, U+3000 and a hex uid read as %+v", read)
		}
	})
	t.Run("sorts the groups by UTF-16 code units, as Array.prototype.sort does", func(t *testing.T) {
		read := ParseUserRead("a", "a:x:1:1::/:/bin/sh\n\uffff \U0001F600 b\na")
		if want := []string{"b", "\U0001F600", "\uffff"}; !reflect.DeepEqual(read.Groups, want) {
			t.Errorf("groups = %q, want %q", read.Groups, want)
		}
	})
}
