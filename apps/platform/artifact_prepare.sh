set -eu
mkdir -p /w/src /w/dist /w/go
cd /w/src
: "${GIT_TOKEN:?empty forge credential: Secret hanzo-build/forge-token key token, from KMS orgs/hanzo/deploy/FORGE_TOKEN@prod}"
export GIT_CONFIG_COUNT=1 \
  GIT_CONFIG_KEY_0="http.https://git.hanzo.ai/.extraheader" \
  GIT_CONFIG_VALUE_0="Authorization: Basic $(printf 'x-access-token:%s' "$GIT_TOKEN" | base64 | tr -d '\n')"
git init -q
git remote add origin "$REPO_URL"
git -c protocol.version=2 fetch -q --depth 1 origin "$REF"
git checkout -q FETCH_HEAD
git --no-pager log --oneline -1
