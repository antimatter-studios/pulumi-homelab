package git

import (
	"strings"
	"testing"
)

const (
	sha = "9f2c1b7e4a3d8065f1c2b3a4d5e6f708192a3b4c"
	url = "https://github.com/example/widget.git"
)

func TestParseCheckout(t *testing.T) {
	t.Run("splits the two answers apart", func(t *testing.T) {
		got := ParseCheckout(sha + "\n#pulumi-homelab#origin\n" + url + "\n")
		if want := (Checkout{Head: sha, URL: url}); got != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
	t.Run("answers with an empty origin when git could not name one", func(t *testing.T) {
		got := ParseCheckout(sha + "\n#pulumi-homelab#origin\n")
		if want := (Checkout{Head: sha, URL: ""}); got != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
	t.Run("does not fold the marker into either answer", func(t *testing.T) {
		got := ParseCheckout(sha + "\n#pulumi-homelab#origin\n" + url + "\n")
		if strings.Contains(got.Head, "pulumi-homelab") || strings.Contains(got.URL, "pulumi-homelab") {
			t.Errorf("marker folded in: %+v", got)
		}
	})
}
