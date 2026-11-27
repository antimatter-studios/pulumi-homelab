#!/usr/bin/env bash
# Release helper, shared by every repository that keeps a CHANGELOG.md in this shape.
#
#   release.sh next               print the next version (X.Y.Z) or nothing when no release is due
#   release.sh changelog X.Y.Z    move "## Unreleased" to "## vX.Y.Z" in CHANGELOG.md
#   release.sh notes X.Y.Z        print the body of the "## vX.Y.Z" section
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

changelog() {
  local v="$1"
  grep -q '^## Unreleased$' "$changelog" || { echo "no '## Unreleased' in $changelog" >&2; exit 1; }
  # An existing heading for this version (written by hand before the first tag) is merged into the
  # one the Unreleased section becomes, so a version is never listed twice.
  awk -v h="## v$v" '$0 == h { next } $0 == "## Unreleased" { print h; next } { print }' \
    "$changelog" > "$changelog.tmp" && mv "$changelog.tmp" "$changelog"
}

notes() {
  awk -v h="## v$1" '$0 == h { on = 1; next } /^## / && on { exit } on' "$changelog" \
    | sed -e '/./,$!d'
}

cmd="${1:-}"; shift || true
case "$cmd" in
  next) next ;;
  changelog) changelog "$1" ;;
  notes) notes "$1" ;;
  *) sed -n '2,10p' "$0" >&2; exit 2 ;;
esac
