#!/usr/bin/env bash
# Release helper, shared by every repository that keeps a CHANGELOG.md in this shape.
#
#   release.sh next     print the next version (X.Y.Z) or nothing when no release is due
#   release.sh notes    print the release notes: the entries under "## Unreleased" that were not
#                       there at the last v* tag (with no tag yet, the whole section, plus any
#                       section written by hand for the first version)
#
# Nothing here writes to the repository. main is protected (a pull request and a green CI for every
# change, admins included), so a release is only a tag on a commit main already has, and its notes
# are what the changelog gained since the previous tag.
#
# The bump comes from the conventional-commit subjects since the last v* tag: fix: is a patch,
# feat: a minor, and a breaking change (type! or BREAKING CHANGE) a minor below 1.0.0, a major
# after. No feat or fix means no release. With no tag yet the first release is 0.1.0.
set -euo pipefail
changelog="${CHANGELOG:-CHANGELOG.md}"

next() {
  local last range log bump=0
  last="$(git describe --tags --abbrev=0 --match 'v[0-9]*.[0-9]*.[0-9]*' 2>/dev/null || true)"
  if [ -z "$last" ]; then
    log="$(git log --format='%s')"
    grep -qE '^(feat|fix)(\([^)]*\))?!?:' <<<"$log" && echo 0.1.0
    return 0
  fi
  range="$last..HEAD"
  log="$(git log --format='%s%n%b%n--end--' "$range")"
  grep -qE '^fix(\([^)]*\))?:' <<<"$log" && bump=1
  grep -qE '^feat(\([^)]*\))?:' <<<"$log" && bump=2
  grep -qE '^(feat|fix)(\([^)]*\))?!:|^BREAKING[ -]CHANGE:' <<<"$log" && bump=3
  [ "$bump" -eq 0 ] && return 0
  IFS=. read -r ma mi pa <<<"${last#v}"
  case "$bump" in
    1) pa=$((pa + 1)) ;;
    2) mi=$((mi + 1)); pa=0 ;;
    3) if [ "$ma" -eq 0 ]; then mi=$((mi + 1)); pa=0; else ma=$((ma + 1)); mi=0; pa=0; fi ;;
  esac
  echo "$ma.$mi.$pa"
}

notes() {
  local last old
  last="$(git describe --tags --abbrev=0 --match 'v[0-9]*.[0-9]*.[0-9]*' 2>/dev/null || true)"
  if [ -n "$last" ]; then
    old="$(git show "$last:$changelog" 2>/dev/null || true)"
  else
    old=""
  fi
  python3 - "$changelog" "$last" "$(next)" 3<<<"$old" <<'PY'
import re, sys

def unreleased(text):
    """The body of the "## Unreleased" section."""
    m = re.search(r"^## Unreleased\n(.*?)(?=^## |\Z)", text, re.S | re.M)
    return m.group(1) if m else ""

def section(text, heading):
    m = re.search(r"^" + re.escape(heading) + r"[^\n]*\n(.*?)(?=^## |\Z)", text, re.S | re.M)
    return m.group(1) if m else ""

def blocks(body):
    """Split a section into (subheading, entry) pairs; an entry is a bullet and its continuation."""
    out, heading, entry = [], "", []
    for line in body.splitlines():
        if line.startswith("### "):
            if entry: out.append((heading, "\n".join(entry).rstrip())); entry = []
            heading = line
        elif line.startswith("- "):
            if entry: out.append((heading, "\n".join(entry).rstrip()))
            entry = [line]
        elif entry:
            entry.append(line)
    if entry: out.append((heading, "\n".join(entry).rstrip()))
    return out

path, last, version = sys.argv[1], sys.argv[2], sys.argv[3]
new = open(path).read()
old = open(3).read() if last else ""
seen = {e for _, e in blocks(unreleased(old))}
body = unreleased(new)
if not last and version:
    # a section written by hand for the first version, before any tag existed
    body += "\n" + section(new, "## v" + version)
fresh = [(h, e) for h, e in blocks(body) if e not in seen]
order, grouped = [], {}
for h, e in fresh:
    if h not in grouped: order.append(h); grouped[h] = []
    if e not in grouped[h]: grouped[h].append(e)
parts = []
for h in order:
    parts.append((h + "\n\n" if h else "") + "\n".join(grouped[h]))
print("\n\n".join(parts) if parts else "No changelog entries since the last release.")
PY
}

cmd="${1:-}"; shift || true
case "$cmd" in
  next) next ;;
  notes) notes ;;
  *) sed -n '2,14p' "$0" >&2; exit 2 ;;
esac
