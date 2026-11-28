package homelab

import (
	"context"
	"fmt"
	"strings"

	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
)

// sshdChangeCommand is the one root shell script that installs (content non-nil) or removes
// (content nil) an sshd drop-in without ever leaving sshd unable to take a connection. In order:
//
//  1. The whole configuration is checked with the change in place before anything live is
//     touched: the main configuration's directory and the drop-in directory are copied into a
//     private temporary tree, every Include there is rewritten to point into the copy, the
//     candidate is put into (or taken out of) the copy, and `sshd -t -f <copy>/sshd_config` runs
//     on it. Then sshd -T resolves the deploying user's settings in the copy to check that
//     public-key login is still permitted. A rejection stops here, with nothing changed.
//  2. sshd must be healthy before the change, or there is no telling afterwards whether the
//     change broke it; if it is not, nothing is changed.
//  3. The previous drop-in, if any, is kept, the new one is written to a temporary file in the
//     drop-in directory (whose name sshd's *.conf glob does not match) and renamed over the target
//     (removal unlinks it), and `sshd -t` runs on the live tree.
//  4. sshd is reloaded, never restarted: `systemctl reload ssh.service`, falling back to
//     sshd.service. A socket-activated sshd with no running service has nothing to reload; the
//     next connection starts it with the new files, which the banner probe below makes happen.
//  5. sshd must then be healthy: the service (or its socket) active in systemd, something
//     listening on every port `sshd -T` reports, and an SSH-2.0- banner read from one of its
//     listen addresses (0.0.0.0 probed as 127.0.0.1, :: as ::1). It is retried for up to 15
//     seconds, since a reloaded sshd re-executes itself and is briefly not listening.
//  6. Any failure in 3 to 5 rolls back: the previous drop-in is restored the same atomic way (or
//     the new one removed), `sshd -t` runs, sshd is reloaded (or started, if the failed change
//     killed it) and checked again, and the error says what failed and how the rollback went.
//
// Two of these on one machine take turns, under flock where it exists.
func sshdChangeCommand(target, directory, config, deployUser string, content *string) string {
	mode := "remove"
	if content != nil {
		mode = "install"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "target=%s; dir=%s; config=%s; mode=%s\n", shellQuote(target), shellQuote(directory), shellQuote(config), mode)
	fmt.Fprintf(&b, "deploy_user=%s\n", shellQuote(deployUser))
	b.WriteString(sshdChangePrelude)
	b.WriteString(sshdDeployCheck)
	if content != nil {
		b.WriteString(heredocInto(`"$work/candidate"`, *content))
		b.WriteString(`[ $? -eq 0 ] || refuse "could not stage the new drop-in"` + "\n")
	}
	b.WriteString(sshdChangeMain)
	return b.String()
}

const sshdChangePrelude = `umask 022
problem=
refuse() { echo "$1; nothing was changed" >&2; exit 1; }
work=$(mktemp -d) || refuse "could not make a working directory"
trap 'rm -rf "$work"' EXIT
if [ -d /run/lock ] && [ -w /run/lock ] && command -v flock >/dev/null 2>&1; then
  exec 9>/run/lock/pulumi-homelab-sshd.lock
  flock -w 300 9 || refuse "another sshd change on this machine held its lock for 5 minutes"
fi
probe='exec 3<>"/dev/tcp/$1/$2" && IFS= read -r line <&3 && case $line in SSH-2.0-*) exit 0;; esac; exit 1'
healthy() {
  unit=
  for u in ssh.service sshd.service ssh.socket sshd.socket; do
    if systemctl is-active --quiet "$u"; then unit=$u; break; fi
  done
  [ -n "$unit" ] || { problem="neither ssh nor sshd is active in systemd"; return 1; }
  listen=$(sshd -T -f "$config" 2>/dev/null | awk '$1 == "listenaddress" { print $2 }')
  [ -n "$listen" ] || { problem="sshd -T reports no listen address"; return 1; }
  sockets=$(ss -ltn) || { problem="ss -ltn failed"; return 1; }
  answered=no
  for l in $listen; do
    port=${l##*:}; host=${l%:*}; host=${host#\[}; host=${host%\]}
    case $host in 0.0.0.0) host=127.0.0.1;; ::) host=::1;; esac
    printf '%s\n' "$sockets" | awk -v p=":$port" '{ if (length($4) >= length(p) && substr($4, length($4) - length(p) + 1) == p) f = 1 } END { exit !f }' ||
      { problem="nothing listens on port $port"; return 1; }
    [ "$answered" = no ] || continue
    if ! command -v bash >/dev/null 2>&1 || ! command -v timeout >/dev/null 2>&1; then
      answered=skipped
    elif timeout 5 bash -c "$probe" _ "$host" "$port"; then
      answered=yes
    fi
  done
  [ "$answered" != no ] || { problem="no listen address answered with an SSH-2.0 banner"; return 1; }
}
settle() {
  tries=0
  until healthy; do
    tries=$((tries + 1))
    [ "$tries" -lt 15 ] || return 1
    sleep 1
  done
}
reload_sshd() {
  for u in ssh.service sshd.service; do
    if systemctl is-active --quiet "$u"; then
      systemctl reload "$u" && return 0
      problem="systemctl reload $u failed"; return 1
    fi
  done
  for u in ssh.socket sshd.socket; do
    systemctl is-active --quiet "$u" && return 0
  done
  problem="neither ssh.service nor sshd.service is active, so there is nothing to reload"; return 1
}
revive() {
  for u in ssh.service sshd.service; do
    if systemctl is-active --quiet "$u"; then systemctl reload "$u"; return; fi
  done
  for u in ssh.socket sshd.socket; do
    systemctl is-active --quiet "$u" && return 0
  done
  systemctl start ssh.service 2>/dev/null || systemctl start sshd.service
}
put() {
  tmp=$(mktemp "$dir/.pulumi-sshd.XXXXXX") || return 1
  if cat "$1" > "$tmp" && chmod 0644 "$tmp" && { [ "$(id -u)" -ne 0 ] || chown root:root "$tmp"; } && mv -f "$tmp" "$target"; then
    return 0
  fi
  rm -f "$tmp"; return 1
}
rollback() {
  if [ "$had" = yes ]; then
    if put "$work/previous"; then restored="restored the previous $target"; else restored="COULD NOT restore the previous $target, which is kept at $work/previous"; rm -rf "$work/root"; trap - EXIT; fi
  else
    if rm -f "$target"; then restored="removed $target"; else restored="COULD NOT remove $target"; fi
  fi
  if ! out=$(sshd -t -f "$config" 2>&1); then after="sshd -t still fails after the rollback: $out"
  elif ! revive; then after="sshd could not be reloaded or started after the rollback"
  elif ! settle; then after="sshd is still unhealthy after the rollback: $problem"
  else after="sshd is healthy again"; fi
  echo "$1. Rolled back: $restored; $after." >&2
  exit 1
}
`

const sshdChangeMain = `had=no
if [ -f "$target" ]; then
  had=yes
  cp -p "$target" "$work/previous" || refuse "could not keep a copy of $target"
fi
[ "$mode" = install ] || [ "$had" = yes ] || exit 0
mirror=$work/root
roots="$(dirname "$config") $dir"
for r in $roots; do
  mkdir -p "$mirror$r" || refuse "could not stage $r to check the change"
  if [ -d "$r" ]; then
    cp -a "$r/." "$mirror$r/" || refuse "could not copy $r to check the change"
  fi
done
find "$mirror" -type f > "$work/mirror-files" || refuse "could not list the mirrored configuration files"
while IFS= read -r f; do
  grep -qi '^[[:space:]]*include[[:space:]]' "$f" || {
    [ "$?" -eq 1 ] && continue
    refuse "could not inspect Include directives in $f"
  }
  awk -v m="$mirror" -v roots="$roots" '
    BEGIN { n = split(roots, r, " ") }
    tolower($1) == "include" {
      out = $1
      for (i = 2; i <= NF; i++) {
        p = $i
        if (substr(p, 1, 1) != "/") p = "/etc/ssh/" p
        for (j = 1; j <= n; j++) if (p == r[j] || index(p, r[j] "/") == 1) { p = m p; break }
        out = out " " p
      }
      print out; next
    }
    { print }' "$f" > "$f.pulumi-include" && cat "$f.pulumi-include" > "$f" && rm -f "$f.pulumi-include" ||
    refuse "could not rewrite Include directives in $f"
done < "$work/mirror-files"
if [ "$mode" = install ]; then
  cp "$work/candidate" "$mirror$target" || refuse "could not stage the new drop-in"
else
  rm -f "$mirror$target"
fi
out=$(sshd -t -f "$mirror$config" 2>&1) || refuse "sshd -t rejects the configuration with this change: $out"
check_deploy_login
settle || refuse "sshd is not healthy before the change ($problem), so whether the change broke it could not be told"
if [ "$mode" = install ]; then
  mkdir -p "$dir" || refuse "could not create $dir"
  put "$work/candidate" || refuse "could not install $target"
else
  rm -f "$target" || refuse "could not remove $target"
fi
out=$(sshd -t -f "$config" 2>&1) || rollback "sshd -t rejects the live configuration: $out"
reload_sshd || rollback "$problem"
settle || rollback "sshd is not healthy after the reload: $problem"
`

// changeSshd runs sshdChangeCommand as root and turns a failure into an error naming the drop-in
// and the machine, without quoting the whole script back.
func changeSshd(ctx context.Context, m *machine, target, directory, config string, content *string) error {
	asked, err := m.ask(ctx, m.escalate(sshdChangeCommand(target, directory, config, m.cfg.User, content)))
	if err != nil {
		return err
	}
	if asked.code == 0 {
		return nil
	}
	detail := jscompat.Trim(asked.err)
	if detail == "" {
		detail = jscompat.Trim(asked.out)
	}
	verb := "install"
	if content == nil {
		verb = "remove"
	}
	return fmt.Errorf("could not %s the sshd drop-in %s on %s: %s", verb, target, m.describe(), detail)
}
