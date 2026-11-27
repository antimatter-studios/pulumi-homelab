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
}
