package resolved

import (
	"reflect"
	"strings"
	"testing"
)

type m = map[string]string

func TestDisagreeing(t *testing.T) {
	check := func(t *testing.T, got, want []string) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %#v, want %#v", got, want)
		}
	}
	t.Run("names a declared key the system resolved differently", func(t *testing.T) {
		check(t, Disagreeing(m{"Storage": "volatile"}, m{"Storage": "persistent"}, nil), []string{"Storage"})
	})
	t.Run("says nothing when the system agrees", func(t *testing.T) {
		check(t, Disagreeing(m{"Storage": "volatile"}, m{"Storage": "volatile"}, nil), []string{})
	})
	t.Run("ignores a key the system does not mention at all", func(t *testing.T) {
		check(t, Disagreeing(m{"Storage": "volatile"}, m{}, nil), []string{})
	})
	t.Run("maps a declared name into the alphabet the system answers in", func(t *testing.T) {
		check(t, Disagreeing(m{"PasswordAuthentication": "yes"}, m{"passwordauthentication": "no"}, strings.ToLower),
			[]string{"PasswordAuthentication"})
	})
	t.Run("agrees through that mapping too", func(t *testing.T) {
		check(t, Disagreeing(m{"PasswordAuthentication": "no"}, m{"passwordauthentication": "no"}, strings.ToLower), []string{})
	})
	t.Run("sorts, so a message reads the same every time", func(t *testing.T) {
		check(t, Disagreeing(m{"b": "1", "a": "1"}, m{"b": "2", "a": "2"}, nil), []string{"a", "b"})
	})
	t.Run("has nothing to say about nothing declared", func(t *testing.T) {
		check(t, Disagreeing(m{}, m{"Storage": "volatile"}, nil), []string{})
	})
}
