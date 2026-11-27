forge="${FORGE_URL:-https://git.hanzo.ai}"
api="${FORGE_API:-https://git.hanzo.ai/v1}"
auth="X-Forge-Anonymous: 1"
[ -n "${GIT_TOKEN:-}" ] && auth="Authorization: token $GIT_TOKEN"
n=$GIT_CONFIG_COUNT
for r in $(sed -n 's#^github\.com/hanzoai/\([A-Za-z0-9._-]*\)[/ ].*#\1#p' go.sum | sort -u); do
  to="https://github.com/hanzoai/$r"
  if tags=$(git ls-remote --tags "$forge/hanzoai/$r" 2>/dev/null); then
    held=1
    for need in $(awk -v p="github.com/hanzoai/$r" '
      index($1, p) == 1 && (length($1) == length(p) || substr($1, length(p) + 1, 1) == "/") {
        dir = substr($1, length(p) + 2); sub(/(^|\/)v[0-9]+$/, "", dir)
        v = $2; sub(/\/go\.mod$/, "", v); sub(/\+incompatible$/, "", v)
        k = split(v, part, "-"); last = part[k]; ts = part[k - 1]
        if (k >= 3 && length(last) == 12 && last ~ /^[0-9a-f]+$/ && length(ts) >= 14 && substr(ts, length(ts) - 13) ~ /^[0-9]+$/)
          print "commit:" last
        else
          print "tag:" (dir == "" ? "" : dir "/") v
      }' go.sum | sort -u); do
      case "$need" in
        tag:*) printf '%s\n' "$tags" | cut -f2 | grep -qxF "refs/tags/${need#tag:}" || { held=0; break; } ;;
        commit:*) curl -fsS -o /dev/null -m 20 -H "$auth" "$api/repos/hanzoai/$r/git/commits/${need#commit:}" 2>/dev/null || { held=0; break; } ;;
      esac
    done
    [ "$held" = 1 ] && to="$forge/hanzoai/$r"
  fi
  export "GIT_CONFIG_KEY_$n=url.$to.insteadOf" "GIT_CONFIG_VALUE_$n=https://github.com/hanzoai/$r"
  echo "module route: github.com/hanzoai/$r -> $to"
  n=$((n+1))
done
export GIT_CONFIG_COUNT=$n