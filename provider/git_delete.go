package homelab

import (
	"context"
	"fmt"

	p "github.com/pulumi/pulumi-go-provider"

	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
)

// deleteCheckout checks the owner's working tree and compares local commits with refreshed remote refs.
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
changes=$(as_owner status --porcelain)
stashes=$(as_owner stash list)
if [ -n "$changes" ]; then printf 'Working tree changes:\n%s\n' "$changes"; fi
if [ -n "$stashes" ]; then printf 'Stashes:\n%s\n' "$stashes"; fi
if ! GIT_TERMINAL_PROMPT=0 as_owner fetch --all --prune --quiet >/dev/null; then
 printf 'Git fetch --prune failed, so remote-tracking refs could not be verified.\n'
 exit 0
fi
commits=$(as_owner log HEAD --branches --not --remotes --oneline)
if [ -n "$commits" ]; then printf 'Unpushed commits:\n%s\n' "$commits"; fi
`
	output, err := m.must(ctx, m.escalate(probe))
	if err != nil {
		return fmt.Errorf("refusing to delete the checkout at %s on %s: %w", path, m.describe(), err)
	}
	if findings := jscompat.Trim(output); findings != "" {
		p.GetLogger(ctx).Warningf("Leaving %s in place because the checkout could not be safely removed.\n%s", path, findings)
		return nil
	}
	_, err = m.must(ctx, m.escalate("rm -rf "+shellQuote(path)))
	return err
}
