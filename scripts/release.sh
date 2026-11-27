#!/usr/bin/env bash
# Release helper, shared by every repository that keeps a CHANGELOG.md in this shape.
#
#   release.sh next     print the next version (X.Y.Z) or nothing when no release is due
#   release.sh notes    print the release notes: the changelog.d fragments added since the last
#                       v* tag, plus the entries under CHANGELOG.md's "## Unreleased" that were not
#                       there at that tag (with no tag yet, every fragment, the whole section, and
#                       any section written by hand for the first version), grouped by kind
#
# Each change brings its own file in changelog.d/ (see changelog.d/README.md), so pull requests
# made side by side never conflict over one shared Unreleased section. LAST=vX.Y.Z overrides "the
# last tag", for notes of a release whose own tag already exists.
#
# Nothing here writes to the repository. main is protected (a pull request and a green CI for every
# change, admins included), so a release is only a tag on a commit main already has, and its notes
# are what the changelog gained since the previous tag.
#
# The bump comes from the conventional-commit subjects since the last v* tag: fix: is a patch,
# feat: a minor, and a breaking change (type! or BREAKING CHANGE) a minor below 1.0.0, a major
# after. The changelog.d fragments added since that tag count the same way, so a change whose
# commits carry no prefix still releases: fixed or security is a patch, added, changed or
# deprecated a minor, removed a breaking change. Neither means no release. With no tag yet the first
# release is 0.1.0.
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
  local kind
  while IFS= read -r kind; do
    case "$kind" in
      fixed | security) [ "$bump" -lt 1 ] && bump=1 ;;
      added | changed | deprecated) [ "$bump" -lt 2 ] && bump=2 ;;
      removed) bump=3 ;;
    esac
  done < <(git diff --name-only --diff-filter=A "$last" HEAD -- changelog.d | grep -v '/README.md$' | sed -E 's#.*\.([a-z]+)\.md$#\1#')
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
  last="${LAST-$(git describe --tags --abbrev=0 --match 'v[0-9]*.[0-9]*.[0-9]*' 2>/dev/null || true)}"
  if [ -n "$last" ]; then
    old="$(git show "$last:$changelog" 2>/dev/null || true)"
    fragments="$(git diff --name-only --diff-filter=A "$last" HEAD -- changelog.d | grep -v '/README.md$' || true)"
  else
    old=""
    fragments="$(git ls-files changelog.d | grep -v '/README.md$' || true)"
  fi
  python3 - "$changelog" "$last" "$(next)" "$fragments" 3<<<"$old" <<'PY'
import os, re, sys

KINDS = ["added", "changed", "deprecated", "removed", "fixed", "security"]

def unreleased(text):
    """The body of the "## Unreleased" section."""
    m = re.search(r"^## Unreleased\n(.*?)(?=^## |\Z)", text, re.S | re.M)
    return m.group(1) if m else ""

def section(text, heading):
    m = re.search(r"^" + re.escape(heading) + r"[^\n]*\n(.*?)(?=^## |\Z)", text, re.S | re.M)
    return m.group(1) if m else ""

def blocks(body, heading=""):
    """Split a section into (subheading, entry) pairs; an entry is a bullet and its continuation."""
    out, entry = [], []
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

path, last, version, fragment_list = sys.argv[1:5]
new = open(path).read()
old = open(3).read() if last else ""
seen = {e for _, e in blocks(unreleased(old))}
body = unreleased(new)
if not last and version:
    # a section written by hand for the first version, before any tag existed
    body += "\n" + section(new, "## v" + version)
fresh = [(h, e) for h, e in blocks(body) if e not in seen]

# changelog.d/<name>.<kind>.md: the kind names the subheading
for name in sorted(f for f in fragment_list.split("\n") if f):
    base = os.path.basename(name)
    kind = base.rsplit(".", 2)[-2].lower() if base.count(".") >= 2 else ""
    if kind not in KINDS:
        sys.exit(f"{name}: name it <anything>.<kind>.md, kind one of {', '.join(KINDS)}")
    entries = blocks(open(name).read(), "### " + kind.capitalize())
    if not entries:
        sys.exit(f"{name}: holds no '- ' bullet, so it would add nothing to the release notes")
    fresh += entries

order, grouped = [], {}
for h, e in fresh:
    if h not in grouped: order.append(h); grouped[h] = []
    if e not in grouped[h]: grouped[h].append(e)
rank = {"### " + k.capitalize(): i for i, k in enumerate(KINDS)}
order.sort(key=lambda h: rank.get(h, len(KINDS)))
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
