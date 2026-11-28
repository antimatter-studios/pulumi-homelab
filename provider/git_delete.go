package homelab

import (
	"context"
	"fmt"

	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
)

// deleteCheckout checks the owner's working tree and compares local commits with refreshed remote refs before removing the checkout.
func deleteCheckout(ctx context.Context, m *machine, path string) error {
	probe := "p=" + shellQuote(path) + `
set -e
if [ ! -e "$p" ] && [ ! -L "$p" ]; then exit 9; fi
if [ ! -d "$p" ] || [ -L "$p" ] || [ ! -e "$p/.git" ]; then
    printf 'the path no longer holds a Git checkout.\n'
    exit 0
fi
uid=$(stat -Lc %u -- "$p")
as_owner() {
 if [ "$(id -u)" = "$uid" ]; then
  env GIT_OPTIONAL_LOCKS=0 "$@"
 else
  runuser -u "$(stat -Lc %U -- "$p")" -- env GIT_OPTIONAL_LOCKS=0 "$@"
 fi
}
changes=$(as_owner git -C "$p" status --porcelain --untracked-files=all)
stashes=$(as_owner git -C "$p" stash list)
if [ -n "$changes" ]; then printf 'Working tree changes:\n%s\n' "$changes"; fi
if [ -n "$stashes" ]; then printf 'Stashes:\n%s\n' "$stashes"; fi
if ! as_owner env GIT_TERMINAL_PROMPT=0 GIT_SSH_COMMAND='ssh -o BatchMode=yes' \
    timeout --kill-after=5s 60s git -C "$p" fetch --all --prune --quiet >/dev/null; then
    printf 'Git fetch --prune failed, so remote-tracking refs could not be verified.\n'
    exit 0
fi
commits=$(as_owner git -C "$p" log HEAD --branches --not --remotes --oneline)
if [ -n "$commits" ]; then printf 'Unpushed commits:\n%s\n' "$commits"; fi
`
	command := m.escalate(probe)
	status, err := m.ask(ctx, command)
	if err != nil {
		return err
	}
	if status.code == 9 {
		return nil
	}
	if _, err := m.check(command, status); err != nil {
		return fmt.Errorf("could not read the checkout at %s: %w", path, err)
	}
	if findings := jscompat.Trim(status.out); findings != "" {
		warnRetained(ctx, path, "the checkout could not be safely removed.\n"+findings)
		return nil
	}
	return guardedDelete(ctx, m, path, "rm -rf -- "+shellQuote(path))
}
