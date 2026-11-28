package homelab

import (
	"context"
	"fmt"

	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
)

// deleteCheckout checks the owner's working tree and local refs before removing the checkout.
func deleteCheckout(ctx context.Context, m *machine, path string) error {
	probe := "p=" + shellQuote(path) + `
set -e
if [ ! -e "$p" ] && [ ! -L "$p" ]; then exit 0; fi
uid=$(stat -Lc %u -- "$p")
as_owner() {
 if [ "$(id -u)" = "$uid" ]; then
  env GIT_OPTIONAL_LOCKS=0 git -C "$p" "$@"
 else
  runuser -u "$(stat -Lc %U -- "$p")" -- env GIT_OPTIONAL_LOCKS=0 git -C "$p" "$@"
 fi
}
changes=$(as_owner status --porcelain --untracked-files=all)
stashes=$(as_owner stash list)
commits=$(as_owner log HEAD --branches --not --remotes --oneline)
if [ -n "$changes" ]; then printf 'Working tree changes:\n%s\n' "$changes"; fi
if [ -n "$stashes" ]; then printf 'Stashes:\n%s\n' "$stashes"; fi
if [ -n "$commits" ]; then printf 'Unpushed commits:\n%s\n' "$commits"; fi
`
	output, err := m.must(ctx, m.escalate(probe))
	if err != nil {
		return fmt.Errorf("refusing to delete the checkout at %s on %s: %w", path, m.describe(), err)
	}
	if findings := jscompat.Trim(output); findings != "" {
		return fmt.Errorf("refusing to delete the checkout at %s on %s: it holds work that exists nowhere else, "+
			"and deleting it would lose that. Found:\n%s\n"+
			"Nothing was removed. To proceed, commit the work and push it to the remote, then delete again; or, "+
			"if the work should go, remove the checkout at %s by hand and delete again.", path, m.describe(), findings, path)
	}
	_, err = m.must(ctx, m.escalate("rm -rf "+shellQuote(path)))
	return err
}
