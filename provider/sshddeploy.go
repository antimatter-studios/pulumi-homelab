package homelab

const sshdDeployCheck = `check_deploy_login() {
  connection="user=$deploy_user,host=localhost,addr=127.0.0.1"
  candidate=$(sshd -T -C "$connection" -f "$mirror$config" -ddd 2>"$work/candidate-trace") ||
    refuse "sshd -T could not evaluate the candidate configuration for $deploy_user"
  account=$(getent passwd -- "$deploy_user") || refuse "could not look up the deployment account $deploy_user"
  IFS=: read -r account_name passwd deploy_uid gid gecos deploy_home login_shell <<EOF
$account
EOF
  [ "$account_name" = "$deploy_user" ] && [ -n "$deploy_home" ] ||
    refuse "could not determine the home directory for $deploy_user"
  groups=
  if printf '%s\n' "$candidate" | grep -Eq '^(allow|deny)groups '; then
    group_ids=$(id -G -- "$deploy_user") && group_entries=$(getent group -- $group_ids) ||
      refuse "could not look up the groups for $deploy_user"
    groups=$(printf '%s\n' "$group_entries" | cut -d: -f1) ||
      refuse "could not read the groups for $deploy_user"
  fi
  reason=$(printf '%s\n' "$candidate" | LC_ALL=C DEPLOY_USER="$deploy_user" DEPLOY_GROUPS="$groups" awk '
    # OpenSSH user and group patterns recognize * and ?; the other characters are literal.
    function matches(value, pattern,    re, i, c) {
      re = "^"
      for (i = 1; i <= length(pattern); i++) {
        c = substr(pattern, i, 1)
        if (c == "*") re = re ".*"
        else if (c == "?") re = re "."
        else re = re (index("\\.[]^$()+{}|", c) ? "\\" c : c)
      }
      return value ~ (re "$")
    }
    function loopback_cidr(pattern,    fields, octets, mask, address, block, i) {
      if (split(pattern, fields, "/") != 2 || fields[2] !~ /^[0-9]+$/ || fields[2] > 32 ||
          split(fields[1], octets, /[.]/) != 4) return 0
      mask = fields[2]
      for (i = 1; i <= 4; i++) {
        if (octets[i] !~ /^(0|[1-9][0-9]*)$/ || octets[i] > 255) return 0
        address = address * 256 + octets[i]
      }
      block = 2 ^ (32 - mask)
      return int(address / block) == int(2130706433 / block)  # 127.0.0.1
    }
    function user_matches(pattern,    at, count, hosts, i, negated, host, found) {
      if (!match(pattern, /@[^@]*$/)) return matches(user, pattern)
      at = RSTART
      if (!matches(user, substr(pattern, 1, at - 1))) return 0
      count = split(substr(pattern, at + 1), hosts, ",")
      for (i = 1; i <= count; i++) {
        host = hosts[i]
        negated = substr(host, 1, 1) == "!"
        if (negated) host = substr(host, 2)
        if (matches("localhost", tolower(host)) || matches("127.0.0.1", host) || loopback_cidr(host)) {
          if (negated) return 0
          found = 1
        }
      }
      return found
    }
    function group_matches(pattern,    i) {
      for (i = 1; i <= group_count; i++) if (matches(groups[i], pattern)) return 1
      return 0
    }
    BEGIN { user = ENVIRON["DEPLOY_USER"]; group_count = split(ENVIRON["DEPLOY_GROUPS"], groups, "\n") }
    $1 == "pubkeyauthentication" { pubkey = $2 }
    $1 ~ /^(allow|deny)(users|groups)$/ {
      seen[$1] = 1
      # sshd reports each pattern on its own line, including any literal spaces.
      pattern = substr($0, length($1) + 2)
      if ($1 ~ /users$/ ? user_matches(pattern) : group_matches(pattern)) matched[$1] = 1
    }
    END {
      if (pubkey != "yes") print "PubkeyAuthentication is not yes"
      else if (matched["denyusers"]) print "DenyUsers matches the deployment user"
      else if (seen["allowusers"] && !matched["allowusers"]) print "AllowUsers does not match the deployment user"
      else if (matched["denygroups"]) print "DenyGroups matches a deployment group"
      else if (seen["allowgroups"] && !matched["allowgroups"]) print "AllowGroups does not match any deployment group"
      else exit 0
      exit 1
    }') || refuse "candidate would prevent public-key login for $deploy_user: ${reason:-could not check access rules}"

  live=$(sshd -T -C "$connection" -f "$config" -ddd 2>"$work/live-trace") ||
    refuse "sshd -T could not evaluate the live configuration for $deploy_user"
  check_deploy_key_files
}

deploy_key_paths() {
  printf '%s\n' "$1" | LC_ALL=C DEPLOY_HOME="$deploy_home" DEPLOY_USER="$deploy_user" DEPLOY_UID="$deploy_uid" awk '
    function expand(path,    result, i, c) {
      for (i = 1; i <= length(path); i++) {
        c = substr(path, i, 1)
        if (c == "%") {
          c = substr(path, ++i, 1)
          if (c == "h") c = home
          else if (c == "u") c = ENVIRON["DEPLOY_USER"]
          else if (c == "U") c = ENVIRON["DEPLOY_UID"]
          else if (c != "%") return ""
        }
        result = result c
      }
      if (substr(result, 1, 1) != "/") result = home "/" result
      return result
    }
    BEGIN { home = ENVIRON["DEPLOY_HOME"] }
    $1 == "authorizedkeysfile" {
      for (i = 2; i <= NF; i++) if (tolower($i) != "none") {
        path = expand($i)
        if (path == "") exit 1
        print path
      }
    }'
}

check_deploy_key_files() {
  # sshd -T joins key paths with spaces even if a path was quoted with spaces
  # inside. The parse trace retains the quotes so we can detect ambiguous paths.
  LC_ALL=C awk '
    match(tolower($0), /^debug3: .*:[0-9]+ setting authorizedkeysfile /) {
      args = substr($0, RLENGTH + 1)
      quote = ""
      for (i = 1; i <= length(args); i++) {
        c = substr(args, i, 1)
        if (c == "\\" && index("\\\"\047", substr(args, i + 1, 1))) { i++; continue }
        if (quote == "" && c == "#" && (i == 1 || substr(args, i - 1, 1) ~ /[ \t]/)) break
        if (c == quote) quote = ""
        else if (quote == "" && (c == "\"" || c == "\047")) quote = c
        else if (quote != "" && c ~ /[ \t]/) exit 1
      }
    }' "$work/candidate-trace" "$work/live-trace" ||
    refuse "could not verify AuthorizedKeysFile paths with quoted whitespace for $deploy_user"
  candidate_keys=$(deploy_key_paths "$candidate") && live_keys=$(deploy_key_paths "$live") ||
    refuse "could not resolve AuthorizedKeysFile for $deploy_user"
  found=no
  # The client may have authenticated using any of these files, so preserve every
  # populated live key file. Unused defaults (e.g. authorized_keys2) can be dropped.
  while IFS= read -r key_file; do
    has_deploy_key_file "$key_file" || continue
    found=yes
    printf '%s\n' "$candidate_keys" | grep -Fqx -- "$key_file" ||
      refuse "candidate AuthorizedKeysFile no longer includes $key_file for $deploy_user"
  done <<EOF
$live_keys
EOF
  [ "$found" = yes ] || refuse "could not find an existing AuthorizedKeysFile for $deploy_user"
}

has_deploy_key_file() (
  # Recent sshd versions accept globs. Preserve an active glob as a whole.
  IFS=
  for file in $1; do
    [ -f "$file" ] && [ -s "$file" ] && exit 0
  done
  exit 1
)
`
