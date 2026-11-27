package homelab

import (
	"strings"
	"testing"
)

const (
	gitSHA   = "9f2c1b7e4a3d8065f1c2b3a4d5e6f708192a3b4c"
	gitOther = "0123456789abcdef0123456789abcdef01234567"
	gitURL   = "https://github.com/example/widget.git"
)

func strp(s string) *string { return &s }

// Ported from src/resources/git.test.ts: what counts as a commit.
func TestLooksLikeCommit(t *testing.T) {
	check := func(t *testing.T, ref string, want bool) {
		t.Helper()
		if got := looksLikeCommit(ref); got != want {
			t.Errorf("looksLikeCommit(%q) = %v, want %v", ref, got, want)
		}
	}
	t.Run("accepts a full sha and an abbreviated one", func(t *testing.T) {
		check(t, gitSHA, true)
		check(t, "9f2c1b7", true)
		check(t, strings.ToUpper(gitSHA), true)
	})
	t.Run("refuses anything shorter than git abbreviates to", func(t *testing.T) {
		check(t, "abc", false)
		check(t, "", false)
	})
	t.Run("refuses tags and branches, however version-shaped", func(t *testing.T) {
		for _, ref := range []string{"v1.2.3", "main", "release/1.2", "deadbeefs"} {
			check(t, ref, false)
		}
	})
	t.Run("refuses something longer than a sha", func(t *testing.T) {
		check(t, gitSHA+"0", false)
	})
}

// Ported from src/resources/git.test.ts: refusing a ref that is not a commit.
func TestCommitRefusal(t *testing.T) {
	t.Run("says nothing about a sha", func(t *testing.T) {
		if got := commitRefusal(gitURL, gitSHA); got != "" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("tells whoever wrote a tag exactly how to turn it into a sha", func(t *testing.T) {
		refusal := commitRefusal(gitURL, "v1.2.3")
		for _, part := range []string{"git ls-remote", gitURL, "v1.2.3"} {
			if !strings.Contains(refusal, part) {
				t.Errorf("%q does not contain %q", refusal, part)
			}
		}
	})
	t.Run("says why, not merely that", func(t *testing.T) {
		if got := commitRefusal(gitURL, "main"); !strings.Contains(got, "move") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("is exactly the TypeScript text", func(t *testing.T) {
		want := "main is not a commit. This resource pins a sha and nothing else, because a tag or a branch can move and a moved ref cannot be told apart from drift. Run `git ls-remote " + gitURL + " main` to get the sha, and keep main beside it in a comment."
		if got := commitRefusal(gitURL, "main"); got != want {
			t.Fatalf("got %q", got)
		}
	})
}

// Ported from src/resources/git.test.ts: comparing what the machine said against what was declared.
func TestSameCommit(t *testing.T) {
	check := func(t *testing.T, head, commit string, want bool) {
		t.Helper()
		if got := sameCommit(head, commit); got != want {
			t.Errorf("sameCommit(%q, %q) = %v, want %v", head, commit, got, want)
		}
	}
	t.Run("matches a full sha against itself", func(t *testing.T) { check(t, gitSHA, gitSHA, true) })
	t.Run("lets an abbreviated declaration match the full answer", func(t *testing.T) {
		check(t, gitSHA, "9f2c1b7", true)
		check(t, gitSHA, gitSHA[:12], true)
	})
	t.Run("ignores case, which git does not preserve for us", func(t *testing.T) {
		check(t, gitSHA, strings.ToUpper(gitSHA[:10]), true)
	})
	t.Run("matches in one direction only", func(t *testing.T) { check(t, "9f2c1b7", gitSHA, false) })
	t.Run("sees a different commit as different", func(t *testing.T) {
		check(t, gitSHA, gitOther, false)
		check(t, gitSHA, "0123456", false)
	})
	t.Run("never calls nothing a match", func(t *testing.T) {
		check(t, "", "", false)
		check(t, "", gitSHA, false)
		check(t, gitSHA, "", false)
	})
}

// Ported from src/resources/git.test.ts: deciding what to do to a checkout.
func TestCheckoutAct(t *testing.T) {
	t.Run("clones what is not there", func(t *testing.T) {
		if got := checkoutAct(nil, gitSHA); got != actClone {
			t.Fatalf("got %s", got)
		}
	})
	t.Run("does nothing to a tree already at the commit", func(t *testing.T) {
		if got := checkoutAct(strp(gitSHA), gitSHA); got != actStay {
			t.Fatalf("got %s", got)
		}
		if got := checkoutAct(strp(gitSHA), "9f2c1b7"); got != actStay {
			t.Fatalf("got %s", got)
		}
	})
	t.Run("checks out a tree that is somewhere else", func(t *testing.T) {
		if got := checkoutAct(strp(gitOther), gitSHA); got != actCheckout {
			t.Fatalf("got %s", got)
		}
	})
}

func command(t *testing.T, act checkoutActKind, url, path, commit string) string {
	t.Helper()
	c, ok := checkoutCommand(act, url, path, commit)
	if !ok {
		t.Fatalf("no command for %s", act)
	}
	return c
}

// Ported from src/resources/git.test.ts: composing the commands.
func TestCheckoutCommand(t *testing.T) {
	t.Run("has nothing to run when there is nothing to do", func(t *testing.T) {
		if c, ok := checkoutCommand(actStay, gitURL, "/srv/widget", gitSHA); ok || c != "" {
			t.Fatalf("got %q", c)
		}
	})
	t.Run("makes the parent before cloning into it", func(t *testing.T) {
		c := command(t, actClone, gitURL, "/srv/widget", gitSHA)
		if i := strings.Index(c, "mkdir -p"); i < 0 || i >= strings.Index(c, "git clone") {
			t.Fatal(c)
		}
	})
	t.Run("clones and then checks out, because clone lands on the default branch", func(t *testing.T) {
		c := command(t, actClone, gitURL, "/srv/widget", gitSHA)
		if strings.Index(c, "git clone") >= strings.Index(c, "checkout") {
			t.Fatal(c)
		}
	})
	t.Run("fetches before checking out an existing tree", func(t *testing.T) {
		c := command(t, actCheckout, gitURL, "/srv/widget", gitSHA)
		if i := strings.Index(c, "fetch"); i < 0 || i >= strings.Index(c, "checkout") {
			t.Fatal(c)
		}
	})
	t.Run("fetches tags too, which is the commonest reason a valid sha is missing", func(t *testing.T) {
		if c := command(t, actCheckout, gitURL, "/srv/widget", gitSHA); !strings.Contains(c, "--tags") {
			t.Fatal(c)
		}
	})
	t.Run("does not re-clone over an existing tree", func(t *testing.T) {
		if c := command(t, actCheckout, gitURL, "/srv/widget", gitSHA); strings.Contains(c, "git clone") {
			t.Fatal(c)
		}
	})
	t.Run("detaches, so nothing later moves the checkout off the pin", func(t *testing.T) {
		for _, act := range []checkoutActKind{actClone, actCheckout} {
			if c := command(t, act, gitURL, "/srv/widget", gitSHA); !strings.Contains(c, "--detach") {
				t.Fatal(c)
			}
		}
	})
	t.Run("stops the whole command when a step fails", func(t *testing.T) {
		c := command(t, actClone, gitURL, "/srv/widget", gitSHA)
		if strings.Contains(c, ";") || !strings.Contains(c, "&&") {
			t.Fatal(c)
		}
	})
	t.Run("quotes the url, the path and the commit", func(t *testing.T) {
		c := command(t, actClone, "https://x/a.git?b=1&c=2", "/srv/it's", gitSHA)
		for _, part := range []string{"'https://x/a.git?b=1&c=2'", `'/srv/it'\''s'`, "'" + gitSHA + "'"} {
			if !strings.Contains(c, part) {
				t.Errorf("%q does not contain %q", c, part)
			}
		}
	})
	t.Run("is the same string every time, so a refresh does not look like a change", func(t *testing.T) {
		if command(t, actClone, gitURL, "/srv/widget", gitSHA) != command(t, actClone, gitURL, "/srv/widget", gitSHA) {
			t.Fatal("differs")
		}
	})
	t.Run("is exactly the TypeScript command", func(t *testing.T) {
		clone := `mkdir -p "$(dirname '/srv/w')" && git clone --quiet 'u' '/srv/w' && git -C '/srv/w' checkout --quiet --detach 'abc1234'`
		if c := command(t, actClone, "u", "/srv/w", "abc1234"); c != clone {
			t.Errorf("clone %s", c)
		}
		checkout := `git -C '/srv/w' fetch --quiet --tags origin && git -C '/srv/w' checkout --quiet --detach 'abc1234'`
		if c := command(t, actCheckout, "u", "/srv/w", "abc1234"); c != checkout {
			t.Errorf("checkout %s", c)
		}
	})
}

// Ported from src/resources/git.test.ts: what counts as changed.
func TestCheckoutChanged(t *testing.T) {
	t.Run("is quiet when the machine is where the program says it should be", func(t *testing.T) {
		if checkoutChanged(gitSHA, gitSHA, gitURL, gitSHA, gitURL) {
			t.Fatal("changed")
		}
	})
	t.Run("reports a checkout somebody moved by hand", func(t *testing.T) {
		if !checkoutChanged(gitOther, gitSHA, gitURL, gitSHA, gitURL) {
			t.Fatal("not changed")
		}
	})
	t.Run("reports a checkout that is gone", func(t *testing.T) {
		if !checkoutChanged("", gitSHA, gitURL, gitSHA, gitURL) {
			t.Fatal("not changed")
		}
	})
	t.Run("reports a commit the program moved", func(t *testing.T) {
		if !checkoutChanged(gitSHA, gitSHA, gitURL, gitOther, gitURL) {
			t.Fatal("not changed")
		}
	})
	t.Run("reports a different origin, which is a different repository at the same path", func(t *testing.T) {
		if !checkoutChanged(gitSHA, gitSHA, gitURL, gitSHA, "https://github.com/someone-else/widget.git") {
			t.Fatal("not changed")
		}
	})
	t.Run("stays quiet when the declaration abbreviates the commit the machine reported", func(t *testing.T) {
		if checkoutChanged(gitSHA, "9f2c1b7", gitURL, "9f2c1b7", gitURL) {
			t.Fatal("changed")
		}
	})
}
