#!/usr/bin/env bash
# pay_smoke.sh [<base url> <version> | --self-test]
#
# Permanent Commercial Lifecycle & Payment Settlement Release Gate (hanzo-inc/cloud#1053).
#
# Modes:
#   1. `pay_smoke.sh --self-test`:
#      Runs the hermetic 8-stage commercial lifecycle test suite with -race across
#      apps/commerce, apps/iam, and apps/billing (wallet auth -> org provisioning ->
#      deposit creation -> tx submission -> on-chain confirmation -> ledger credit ->
#      API inference spend -> receipt reconciliation + replay rejection).
#   2. `pay_smoke.sh <base url> <version>`:
#      Waits for <base url> to serve <version>, then certifies on the live testnet:
#        - Stripe test-mode guard & POST /v1/billing/route (200)
#        - Processor & Stripe crypto unconfirmed intent cancel + repeat poll + 0 phantom credit
#        - Web3 challenge/verify + browser OIDC PKCE (S256) + replay rejection + lowercase same-org check
#        - Native crypto watcher health (/v1/commerce/deposits) & non-Stripe options/deposit gate
#          (plus live testnet ERC-20 transfer -> confirm -> credit when PAY_SMOKE_TESTNET_RPC is set)
#        - Stripe test-card settlement -> exact +500c single deposit -> live AI spend -> receipt reconciliation
#
# Security: NEVER logs or prints bearer tokens, auth codes, client secrets, or private keys.
set -euo pipefail

if [ "${1:-}" = "--self-test" ]; then
  export GOPRIVATE="${GOPRIVATE:-github.com/hanzoai/*,github.com/hanzo-inc/*,github.com/zap-proto/*,zap-proto.io/*}"
  export GONOPROXY="${GONOPROXY:-none}"
  export GOPROXY="${GOPROXY:-https://proxy.golang.org|direct}"
  export GOWORK=off CGO_ENABLED=1
  export CONSOLE_CSRF_KEY="${CONSOLE_CSRF_KEY:-ZGV2LWNzcmYta2V5LWZvci10ZXN0cy1vbmx5LTAwMDA=}"
  tags="sqlite_fts5 sqlite_math_functions"
  go test -race -short -count=1 -timeout 10m -tags "$tags" \
    -run 'TestCryptoOptionsRichMetadataAndMinimumEnforcement|TestADepositIsMintedOnlyForAnAcceptedAsset|TestAShutRailIsUnavailableNotRefused' \
    github.com/hanzoai/cloud/apps/billing
  go test -race -short -count=1 -timeout 10m -tags "$tags" \
    -run 'TestWeb3WalletLoginSameOrgPKCEAndDomainBinding' \
    github.com/hanzoai/cloud/apps/iam
  go test -race -short -count=1 -timeout 15m -tags "$tags" \
    -run 'TestFullCommercialLifecycleReleaseGate|TestConfirmedCryptoDepositIsSpendableCash|TestNativeCryptoNeverConflatesStripeAndFailsClosed|TestDepositSettlementIdempotencyReplayAndValidation|TestFeeInclusivePlanQuoteAndExpiredIntentAndCollisionHealing|TestStalledWatcherDetectionAndProcessorCancellationClassification' \
    github.com/hanzoai/cloud/apps/commerce
  echo "::notice::pay_smoke --self-test: all 8 commercial lifecycle stages + race gates passed"
  exit 0
fi

base=${1:?base url} want=${2:?version}
: "${IAM_CLIENT_ID:?}" "${IAM_CLIENT_SECRET:?}"
issuer="${IAM_ISSUER:-https://hanzo.id}"
amount=500

smoke_dir=$(mktemp -d)
unconfirmed_id=""
cleanup() {
  if [ -n "$unconfirmed_id" ]; then
    local c
    c=$(api POST "/v1/billing/topup/intent/$unconfirmed_id/cancel" -d '{}' -o /dev/null -w '%{http_code}' 2>/dev/null || true)
    [ "$c" = 200 ] || echo "::warning::could not cancel the unconfirmed top-up $unconfirmed_id ($c); commerce abandons it in 48h"
  fi
  rm -rf "$smoke_dir"
}
trap cleanup EXIT

got=""
for _ in $(seq 1 540); do
  got=$(curl -fsSI --connect-timeout 5 --max-time 15 "$base/v1/health" 2>/dev/null \
    | tr -d '\r' | sed -n 's/^[Xx]-[Aa]pi-[Vv]ersion: *//p' | head -1 || true)
  [ "$got" = "$want" ] && break
  if [ -n "$got" ] && [ "$(printf '%s\n%s\n' "${got#v}" "${want#v}" | sort -V | head -1)" = "${want#v}" ]; then
    echo "::notice::$base is serving $got, past $want; the newer release owns the smoke gate"
    exit 0
  fi
  sleep 10
done
[ "$got" = "$want" ] || { echo "::error::$base never reported $want (last saw '${got:-none}')"; exit 1; }

token=$(curl -sS --max-time 20 "$issuer/v1/iam/oauth/token" \
  -u "${IAM_CLIENT_ID}:${IAM_CLIENT_SECRET}" -d grant_type=client_credentials 2>/dev/null | jq -r '.access_token // empty')
[ -n "$token" ] || { echo "::error::IAM minted no token for the run's identity"; exit 1; }
echo "::add-mask::$token"
api() { local m=$1 p=$2; shift 2; printf 'Authorization: Bearer %s\n' "$token" | curl -sS -m 60 -H @- -X "$m" -H 'Content-Type: application/json' "$@" "$base$p"; }
wapi() { local wt=$1 m=$2 p=$3; shift 3; printf 'Authorization: Bearer %s\n' "$wt" | curl -sS -m 60 -H @- -X "$m" -H 'Content-Type: application/json' "$@" "$base$p"; }

settings=$(api GET /v1/billing/settings)
pk=$(jq -r '.stripe.publishableKey // empty' <<<"$settings")
case "$pk" in pk_test_*) ;; *) echo "::error::$base is not in Stripe test mode; refusing to move money"; exit 1 ;; esac

code=$(api POST /v1/billing/route -d '{"country":"US"}' -o /dev/null -w '%{http_code}')
[ "$code" = 200 ] || { echo "::error::$base answered $code to POST /v1/billing/route on $want"; exit 1; }

bal() { api GET /v1/billing/balance | jq -r '.balance'; }
deposits() { api GET '/v1/billing/transactions?limit=300' | jq '[.transactions[]? | select(.type=="deposit")] | length'; }
b0=$(bal); d0=$(deposits)

# ── 1. Processor Cancellation & Zero-Phantom-Credit Gate ─────────────────────
# Use DE/eur (matching step 4 below) so checkout routes to Stripe test mode
# rather than returning 409 Conflict ("this checkout is on square") on US/usd.
c_intent=$(api POST /v1/billing/topup/intent -H "X-Idempotency-Key: smoke-cancel-$want-$(date +%s)" -d "{\"amountCents\":$amount,\"country\":\"DE\",\"currency\":\"eur\"}")
cid=$(jq -r '.intentId // empty' <<<"$c_intent")
[ -n "$cid" ] || { echo "::error::failed to open processor topup intent for cancel check: $(jq -c 'del(.clientSecret)? // .' <<<"$c_intent" 2>/dev/null | head -c 300)"; exit 1; }
c_pre=$(api GET "/v1/billing/topup/intent/$cid" | jq -r '.credited // false')
[ "$c_pre" = false ] || { echo "::error::unconfirmed intent $cid reported credited=true"; exit 1; }
for pass in 1 2; do
  c_res=$(api POST "/v1/billing/topup/intent/$cid/cancel" -d '{}')
  [ "$(jq -r '.state // empty' <<<"$c_res")" = "canceled" ] \
    || { echo "::error::cancel pass $pass on $cid did not answer state=canceled: $c_res"; exit 1; }
done
for pass in 1 2; do
  c_poll=$(api GET "/v1/billing/topup/intent/$cid")
  [ "$(jq -r '.state // empty' <<<"$c_poll")" = "canceled" ] && [ "$(jq -r '.credited // false' <<<"$c_poll")" = false ] \
    || { echo "::error::post-cancel poll $pass on $cid did not stay canceled/uncredited: $c_poll"; exit 1; }
done

# Verify Stripe crypto PaymentIntent repeat-cancel + repeat-poll classification (commerce#88) when enabled.
cc_code=$(api POST /v1/billing/topup/crypto -d "{\"amountCents\":$amount}" -o "$smoke_dir/crypto_topup.json" -w '%{http_code}' || echo 000)
if [ "$cc_code" = "200" ]; then
  cc_id=$(jq -r '.intentId // empty' "$smoke_dir/crypto_topup.json")
  [ -n "$cc_id" ] || { echo "::error::/v1/billing/topup/crypto answered 200 without intentId"; exit 1; }
  for pass in 1 2; do
    cc_res=$(api POST "/v1/billing/topup/intent/$cc_id/cancel" -d '{}')
    [ "$(jq -r '.state // empty' <<<"$cc_res")" = "canceled" ] \
      || { echo "::error::crypto cancel pass $pass on $cc_id did not answer state=canceled: $cc_res"; exit 1; }
  done
  for pass in 1 2; do
    cc_poll=$(api GET "/v1/billing/topup/intent/$cc_id")
    [ "$(jq -r '.state // empty' <<<"$cc_poll")" = "canceled" ] && [ "$(jq -r '.credited // false' <<<"$cc_poll")" = false ] \
      || { echo "::error::crypto post-cancel poll $pass on $cc_id turned non-canceled: $cc_poll"; exit 1; }
  done
elif [ "$cc_code" != "400" ] && [ "$cc_code" != "409" ] && [ "$cc_code" != "503" ]; then
  echo "::error::unexpected HTTP $cc_code from POST /v1/billing/topup/crypto"
  exit 1
fi
[ "$(bal)" -eq "$b0" ] || { echo "::error::canceled processor intent altered balance"; exit 1; }

# ── 2. Web3 Wallet Auth, Replay Rejection & Same-Org Provisioning Gate ───────
web3_cid="${WEB3_SMOKE_CLIENT_ID:-hanzo-app}"
methods=$(curl -sS --max-time 20 "$issuer/v1/iam/auth/methods?clientId=${web3_cid}&responseType=code" || echo '{}')
if [ "$(jq -r '.data.web3 // empty' <<<"$methods" 2>/dev/null)" = "false" ]; then
  echo "::error::$issuer/v1/iam/auth/methods reported web3=false for $web3_cid"
  exit 1
fi

sign_siwe() {
  SMOKE_MSG="$1" python3 - <<'PY'
import hashlib, hmac, json, os
RC=[1,0x8082,0x800000000000808A,0x8000000080008000,0x808B,0x80000001,0x8000000080008081,0x8000000000008009,0x8A,0x88,0x80008009,0x8000000A,0x8000808B,0x800000000000008B,0x8000000000008089,0x8000000000008003,0x8000000000008002,0x8000000000000080,0x800A,0x800000008000000A,0x8000000080008081,0x8000000000008080,0x80000001,0x8000000080008008]
ROT=[[0,36,3,41,18],[1,44,10,45,2],[62,6,43,15,61],[28,55,25,21,56],[27,20,39,8,14]]
M=(1<<64)-1; rol=lambda x,n:((x<<n)|(x>>(64-n)))&M
def kec(d):
    b=bytearray(d)+b"\x01"
    while len(b)%136: b+=b"\x00"
    b[-1]|=0x80; s=[[0]*5 for _ in range(5)]
    for o in range(0,len(b),136):
        for i in range(17): s[i%5][i//5]^=int.from_bytes(b[o+8*i:o+8*i+8],"little")
        for r in range(24):
            c=[s[x][0]^s[x][1]^s[x][2]^s[x][3]^s[x][4] for x in range(5)]
            dd=[c[(x-1)%5]^rol(c[(x+1)%5],1) for x in range(5)]
            for x in range(5):
                for y in range(5): s[x][y]^=dd[x]
            bb=[[0]*5 for _ in range(5)]
            for x in range(5):
                for y in range(5): bb[y][(2*x+3*y)%5]=rol(s[x][y],ROT[x][y])
            for x in range(5):
                for y in range(5): s[x][y]=bb[x][y]^((~bb[(x+1)%5][y]&M)&bb[(x+2)%5][y])
            s[0][0]^=RC[r]
    return b"".join(s[x][y].to_bytes(8,"little") for y in range(5) for x in range(5))[:32]
P=0xFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEFFFFFC2F
N=0xFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141
G=(0x79BE667EF9DCBBAC55A06295CE870B07029BFCDB2DCE28D959F2815B16F81798,0x483ADA7726A3C4655DA4FBFC0E1108A8FD17B448A68554199C47D08FFB10D4B8)
def padd(a,b):
    if not a: return b
    if not b: return a
    x1,y1=a; x2,y2=b
    if x1==x2 and (y1+y2)%P==0: return None
    l=((3*x1*x1)*pow(2*y1,P-2,P) if x1==x2 else (y2-y1)*pow(x2-x1,P-2,P))%P
    x3=(l*l-x1-x2)%P; return (x3,(l*(x1-x3)-y1)%P)
def pmul(k,pt=G):
    r=None
    while k:
        if k&1: r=padd(r,pt)
        pt=padd(pt,pt); k>>=1
    return r
priv=hmac.new(os.environ["IAM_CLIENT_SECRET"].encode(),b"hanzo-pay-smoke-wallet-v1",hashlib.sha256).digest()
d=int.from_bytes(priv,"big"); px,py=pmul(d)
raw=kec(px.to_bytes(32,"big")+py.to_bytes(32,"big"))[12:].hex(); kh=kec(raw.encode()).hex()
addr="0x"+"".join(c.upper() if int(kh[i],16)>=8 else c for i,c in enumerate(raw))
msg=os.environ.get("SMOKE_MSG","")
if not msg:
    print(json.dumps({"checksum":addr,"lower":addr.lower()}))
else:
    mb=msg.encode(); z=int.from_bytes(kec(f"\x19Ethereum Signed Message:\n{len(mb)}".encode()+mb),"big")
    v,k=b"\x01"*32,b"\x00"*32; zb=z.to_bytes(32,"big")
    k=hmac.new(k,v+b"\x00"+priv+zb,hashlib.sha256).digest(); v=hmac.new(k,v,hashlib.sha256).digest()
    k=hmac.new(k,v+b"\x01"+priv+zb,hashlib.sha256).digest(); v=hmac.new(k,v,hashlib.sha256).digest()
    while True:
        v=hmac.new(k,v,hashlib.sha256).digest(); cand=int.from_bytes(v,"big")
        if 1<=cand<N:
            rx,ry=pmul(cand); r=rx%N; s=pow(cand,N-2,N)*(z+r*d)%N
            if r and s:
                rec=(ry&1)^(2 if rx>=N else 0)
                if s>N//2: s=N-s; rec^=1
                print("0x"+(r.to_bytes(32,"big")+s.to_bytes(32,"big")+bytes([rec+27])).hex()); break
        k=hmac.new(k,v+b"\x00",hashlib.sha256).digest(); v=hmac.new(k,v,hashlib.sha256).digest()
PY
}

addrs=$(sign_siwe "")
w_checksum=$(jq -r '.checksum' <<<"$addrs")
w_lower=$(jq -r '.lower' <<<"$addrs")

wallet_login() {
  local addr=$1
  local n_raw
  n_raw=$(curl -sS --max-time 20 "$issuer/v1/iam/web3/nonce?chain=evm&address=${addr}&clientId=${web3_cid}")
  [ "$(jq -r '.status // empty' <<<"$n_raw")" = "ok" ] || { echo "::error::web3 nonce failed: $n_raw" >&2; return 1; }
  local dom uri stmt nce iat exp ver msg sig v_body v_res
  dom=$(jq -r '.data.domain' <<<"$n_raw")
  uri=$(jq -r '.data.uri' <<<"$n_raw")
  stmt=$(jq -r '.data.statement // empty' <<<"$n_raw")
  nce=$(jq -r '.data.nonce' <<<"$n_raw")
  iat=$(jq -r '.data.issuedAt' <<<"$n_raw")
  exp=$(jq -r '.data.expirationTime // empty' <<<"$n_raw")
  ver=$(jq -r '.data.version // "1"' <<<"$n_raw")
  msg=$(python3 - "$dom" "$addr" "$stmt" "$uri" "$ver" "$nce" "$iat" "$exp" <<'PY'
import sys
dom,addr,stmt,uri,ver,nce,iat,exp = sys.argv[1:9]
lines=[f"{dom} wants you to sign in with your Ethereum account:", addr, ""]
if stmt: lines += [stmt, ""]
lines += [f"URI: {uri}", f"Version: {ver}", f"Nonce: {nce}", f"Issued At: {iat}"]
if exp: lines.append(f"Expiration Time: {exp}")
sys.stdout.write("\n".join(lines))
PY
)
  sig=$(sign_siwe "$msg")
  v_body=$(jq -nc --arg addr "$addr" --arg msg "$msg" --arg sig "$sig" --arg cid "$web3_cid" \
    '{chain:"evm",scheme:"secp256k1-eip191",address:$addr,message:$msg,signature:$sig,clientId:$cid,type:"token"}')
  v_res=$(curl -sS --max-time 20 -X POST -H 'Content-Type: application/json' -d "$v_body" "$issuer/v1/iam/web3/verify")
  [ "$(jq -r '.status // empty' <<<"$v_res")" = "ok" ] || { echo "::error::web3 verify failed for $addr: $(jq -c 'del(.data)?' <<<"$v_res" 2>/dev/null || echo failed)" >&2; return 1; }
  # Replay of the exact same signed challenge MUST fail.
  local replay
  replay=$(curl -sS --max-time 20 -X POST -H 'Content-Type: application/json' -d "$v_body" "$issuer/v1/iam/web3/verify")
  [ "$(jq -r '.status // empty' <<<"$replay")" != "ok" ] || { echo "::error::replayed web3 challenge was accepted!" >&2; return 1; }
  jq -r '.data // empty' <<<"$v_res"
}

wt1=$(wallet_login "$w_checksum")
[ -n "$wt1" ] && echo "::add-mask::$wt1"
wt2=$(wallet_login "$w_lower")
[ -n "$wt2" ] && echo "::add-mask::$wt2"

decode_owner() {
  JWT_VAL="$1" python3 - <<'PY'
import base64, json, os
parts = os.environ["JWT_VAL"].split(".")
if len(parts) == 3:
    pad = parts[1] + "=" * (-len(parts[1]) % 4)
    c = json.loads(base64.urlsafe_b64decode(pad))
    ba = c.get("billing_account") or ""
    sub_org = (c.get("sub") or "").split("/")[0] if "/" in (c.get("sub") or "") else ""
    print(ba[4:] if ba.startswith("org:") else (sub_org or c.get("owner", "")))
else:
    print(os.environ["JWT_VAL"])
PY
}
w_org1=$(decode_owner "$wt1")
w_org2=$(decode_owner "$wt2")
[ -n "$w_org1" ] && [ "$w_org1" = "$w_org2" ] \
  || { echo "::error::checksum vs lowercase wallet sign-in resolved different identities ($w_org1 vs $w_org2)"; exit 1; }

# Browser-grade OIDC Authorization Code + PKCE (S256) wallet exchange:
# auth code & PKCE verifier stay in memory and enter curl via stdin config (never argv, files, or logs).
wallet_login_pkce() {
  local addr=$1
  local pkce_pair pkce_verifier pkce_challenge n_raw dom uri stmt nce iat exp ver msg sig v_body v_res auth_code tok_res jwt_tok
  pkce_pair=$(python3 - <<'PY'
import base64, hashlib, os
v = base64.urlsafe_b64encode(os.urandom(32)).decode().rstrip("=")
c = base64.urlsafe_b64encode(hashlib.sha256(v.encode()).digest()).decode().rstrip("=")
print(f"{v} {c}")
PY
)
  pkce_verifier=${pkce_pair%% *}
  pkce_challenge=${pkce_pair#* }
  n_raw=$(curl -sS --max-time 20 "$issuer/v1/iam/web3/nonce?chain=evm&address=${addr}&clientId=${web3_cid}")
  [ "$(jq -r '.status // empty' <<<"$n_raw")" = "ok" ] || { echo "::error::web3 PKCE nonce failed for $addr" >&2; return 1; }
  dom=$(jq -r '.data.domain' <<<"$n_raw")
  uri=$(jq -r '.data.uri' <<<"$n_raw")
  stmt=$(jq -r '.data.statement // empty' <<<"$n_raw")
  nce=$(jq -r '.data.nonce' <<<"$n_raw")
  iat=$(jq -r '.data.issuedAt' <<<"$n_raw")
  exp=$(jq -r '.data.expirationTime // empty' <<<"$n_raw")
  ver=$(jq -r '.data.version // "1"' <<<"$n_raw")
  msg=$(python3 - "$dom" "$addr" "$stmt" "$uri" "$ver" "$nce" "$iat" "$exp" <<'PY'
import sys
dom,addr,stmt,uri,ver,nce,iat,exp = sys.argv[1:9]
lines=[f"{dom} wants you to sign in with your Ethereum account:", addr, ""]
if stmt: lines += [stmt, ""]
lines += [f"URI: {uri}", f"Version: {ver}", f"Nonce: {nce}", f"Issued At: {iat}"]
if exp: lines.append(f"Expiration Time: {exp}")
sys.stdout.write("\n".join(lines))
PY
)
  sig=$(sign_siwe "$msg")
  v_body=$(jq -nc --arg addr "$addr" --arg msg "$msg" --arg sig "$sig" --arg cid "$web3_cid" --arg cc "$pkce_challenge" \
    '{chain:"evm",scheme:"secp256k1-eip191",address:$addr,message:$msg,signature:$sig,clientId:$cid,type:"code",codeChallenge:$cc,codeChallengeMethod:"S256"}')
  v_res=$(curl -sS --max-time 20 -X POST -H 'Content-Type: application/json' -d "$v_body" "$issuer/v1/iam/web3/verify")
  [ "$(jq -r '.status // empty' <<<"$v_res")" = "ok" ] || { echo "::error::web3 PKCE verify failed for $addr" >&2; return 1; }
  auth_code=$(jq -r '.data // empty' <<<"$v_res")
  [ -n "$auth_code" ] || { echo "::error::web3 PKCE verify returned empty code for $addr" >&2; return 1; }
  local replay
  replay=$(curl -sS --max-time 20 -X POST -H 'Content-Type: application/json' -d "$v_body" "$issuer/v1/iam/web3/verify")
  [ "$(jq -r '.status // empty' <<<"$replay")" != "ok" ] || { echo "::error::replayed web3 PKCE challenge was accepted!" >&2; return 1; }
  tok_res=$(PKCE_CID="$web3_cid" PKCE_CODE="$auth_code" PKCE_VERIFIER="$pkce_verifier" python3 - <<'PY' \
    | curl -sS --max-time 20 --config - -X POST "$issuer/v1/iam/oauth/token"
import os, sys, urllib.parse
form = urllib.parse.urlencode({
    "grant_type": "authorization_code",
    "client_id": os.environ["PKCE_CID"],
    "code": os.environ["PKCE_CODE"],
    "code_verifier": os.environ["PKCE_VERIFIER"],
})
escaped = form.replace("\\", "\\\\").replace('"', '\\"')
sys.stdout.write(f'header = "Content-Type: application/x-www-form-urlencoded"\ndata = "{escaped}"\n')
PY
)
  jwt_tok=$(jq -r '.access_token // empty' <<<"$tok_res" 2>/dev/null || true)
  [ "$(awk -F. '{print NF}' <<<"$jwt_tok")" = "3" ] \
    || { echo "::error::PKCE oauth/token exchange did not return a 3-segment JWT for $addr" >&2; return 1; }
  printf '%s' "$jwt_tok"
}

wp1=$(wallet_login_pkce "$w_checksum")
echo "::add-mask::$wp1"
wp2=$(wallet_login_pkce "$w_lower")
echo "::add-mask::$wp2"
wp_org1=$(decode_owner "$wp1")
wp_org2=$(decode_owner "$wp2")
[ "$wp_org1" = "$w_org1" ] && [ "$wp_org2" = "$w_org1" ] \
  || { echo "::error::PKCE wallet JWT org mismatch ($wp_org1 / $wp_org2 vs $w_org1)"; exit 1; }
w_bal_acct=$(wapi "$wp1" GET /v1/billing/balance | jq -r '.account // empty')
[ "$w_bal_acct" = "$w_org1" ] \
  || { echo "::error::wallet PKCE JWT billing balance account '$w_bal_acct' did not match canonical wallet org '$w_org1'"; exit 1; }

# ── 3. Native Crypto Watcher & Non-Conflation Gate ───────────────────────────
dep_health_code=$(api GET /v1/commerce/deposits -o "$smoke_dir/deposits.json" -w '%{http_code}' || echo 000)
if [ "$dep_health_code" = "200" ]; then
  jq -e '.assets != null' "$smoke_dir/deposits.json" >/dev/null \
    || { echo "::error::/v1/commerce/deposits returned malformed payload"; exit 1; }
elif [ "$dep_health_code" != "403" ]; then
  echo "::error::unexpected HTTP $dep_health_code from GET /v1/commerce/deposits (want 200 or role-gated 403)"
  exit 1
fi

opt_code=$(api GET /v1/billing/crypto/options -o "$smoke_dir/opts.json" -w '%{http_code}' || echo 000)
if [ "$opt_code" = "200" ]; then
  asset_count=$(jq -r '.assets | length' "$smoke_dir/opts.json")
  if [ "$asset_count" -gt 0 ]; then
    jq -e 'all(.assets[]; (.chain|length)>0 and (.token|length)>0 and (.decimals//0)>0 and (.confirmations//0)>0 and (.minimumCents//0)>0)' "$smoke_dir/opts.json" >/dev/null \
      || { echo "::error::/v1/billing/crypto/options returned incomplete asset metadata (Stripe conflation check failed)"; exit 1; }
    c_chain=$(jq -r '.assets[0].chain' "$smoke_dir/opts.json")
    c_token=$(jq -r '.assets[0].token' "$smoke_dir/opts.json")
    c_min=$(jq -r 'if (.assets[0].minimumCents//0) > 500 then .assets[0].minimumCents else 500 end' "$smoke_dir/opts.json")
    m1_code=$(api POST /v1/billing/crypto/deposit -d "{\"chain\":\"$c_chain\",\"token\":\"$c_token\",\"amountCents\":$c_min}" -o "$smoke_dir/m1.json" -w '%{http_code}' || echo 000)
    m2_code=$(api POST /v1/billing/crypto/deposit -d "{\"chain\":\"$c_chain\",\"token\":\"$c_token\",\"amountCents\":$c_min}" -o "$smoke_dir/m2.json" -w '%{http_code}' || echo 000)
    [ "$m1_code" = "200" ] && [ "$m2_code" = "200" ] \
      && [ "$(jq -r '.id // empty' "$smoke_dir/m1.json")" = "$(jq -r '.id // empty' "$smoke_dir/m2.json")" ] \
      && [ -n "$(jq -r '.depositAddress // empty' "$smoke_dir/m1.json")" ] \
      || { echo "::error::native crypto deposit mint did not reuse open intent cleanly ($m1_code/$m2_code)"; exit 1; }
  fi
elif [ "$opt_code" != "503" ]; then
  echo "::error::unexpected HTTP $opt_code from GET /v1/billing/crypto/options (want 200 or fail-closed 503)"
  exit 1
fi

# Live authorized testnet ERC-20 transfer -> watcher confirmation -> cash credit gate.
# Subprocess runs under `env -i PATH="$PATH"` so no IAM/runner credentials enter its environment,
# and RPC is pinned to Hanzo's own testnet endpoint or loopback.
if [ -n "${PAY_SMOKE_TESTNET_RPC:-}" ]; then
  case "$PAY_SMOKE_TESTNET_RPC" in
    https://rpc.hanzo-test.network/v1/chain/hanzo/rpc|http://127.0.0.1:*) ;;
    *) echo "::error::PAY_SMOKE_TESTNET_RPC is not an allowed Hanzo testnet RPC endpoint"; exit 1 ;;
  esac
  [ -f "${PAY_SMOKE_TESTNET_KEY_FILE:-}" ] || { echo "::error::PAY_SMOKE_TESTNET_KEY_FILE is required when PAY_SMOKE_TESTNET_RPC is set"; exit 1; }
  [ "$opt_code" = "200" ] || { echo "::error::PAY_SMOKE_TESTNET_RPC set but /v1/billing/crypto/options returned $opt_code"; exit 1; }
  evm_asset=$(jq -c '[.assets[] | select((.contract//"") | test("^0x[0-9a-fA-F]{40}$"))][0] // empty' "$smoke_dir/opts.json")
  [ -n "$evm_asset" ] || { echo "::error::no watched EVM ERC-20 contract in /v1/billing/crypto/options"; exit 1; }
  t_chain=$(jq -r '.chain' <<<"$evm_asset")
  t_token=$(jq -r '.token' <<<"$evm_asset")
  t_contract=$(jq -r '.contract' <<<"$evm_asset")
  t_dec=$(jq -r '.decimals' <<<"$evm_asset")
  t_cents=$(jq -r 'if (.minimumCents//0) > 500 then .minimumCents else 500 end' <<<"$evm_asset")
  wb0=$(wapi "$wp1" GET /v1/billing/balance | jq -r '.cash // 0')
  wd0=$(wapi "$wp1" GET '/v1/billing/transactions?limit=300' | jq '[.transactions[]? | select(.type=="deposit")] | length')
  w_dep_code=$(wapi "$wp1" POST /v1/billing/crypto/deposit -d "{\"chain\":\"$t_chain\",\"token\":\"$t_token\",\"amountCents\":$t_cents}" -o "$smoke_dir/wdep.json" -w '%{http_code}' || echo 000)
  [ "$w_dep_code" = "200" ] || { echo "::error::wallet crypto deposit mint failed with HTTP $w_dep_code"; exit 1; }
  dep_id=$(jq -r '.id // empty' "$smoke_dir/wdep.json")
  dep_addr=$(jq -r '.depositAddress // empty' "$smoke_dir/wdep.json")
  [ -n "$dep_id" ] && [ -n "$dep_addr" ] || { echo "::error::wallet crypto deposit mint returned empty id/address"; exit 1; }
  tx_hash=$(env -i PATH="$PATH" python3 - "$PAY_SMOKE_TESTNET_RPC" "$PAY_SMOKE_TESTNET_KEY_FILE" "$t_contract" "$dep_addr" "$t_cents" "$t_dec" <<'PY'
import hashlib, hmac, json, sys, time, urllib.request
rpc, kf, token_addr, to_addr, cents_s, dec_s = sys.argv[1:7]
RC=[1,0x8082,0x800000000000808A,0x8000000080008000,0x808B,0x80000001,0x8000000080008081,0x8000000000008009,0x8A,0x88,0x80008009,0x8000000A,0x8000808B,0x800000000000008B,0x8000000000008089,0x8000000000008003,0x8000000000008002,0x8000000000000080,0x800A,0x800000008000000A,0x8000000080008081,0x8000000000008080,0x80000001,0x8000000080008008]
ROT=[[0,36,3,41,18],[1,44,10,45,2],[62,6,43,15,61],[28,55,25,21,56],[27,20,39,8,14]]
M=(1<<64)-1; rol=lambda x,n:((x<<n)|(x>>(64-n)))&M
def kec(d):
    b=bytearray(d)+b"\x01"
    while len(b)%136: b+=b"\x00"
    b[-1]|=0x80; s=[[0]*5 for _ in range(5)]
    for o in range(0,len(b),136):
        for i in range(17): s[i%5][i//5]^=int.from_bytes(b[o+8*i:o+8*i+8],"little")
        for r in range(24):
            c=[s[x][0]^s[x][1]^s[x][2]^s[x][3]^s[x][4] for x in range(5)]
            dd=[c[(x-1)%5]^rol(c[(x+1)%5],1) for x in range(5)]
            for x in range(5):
                for y in range(5): s[x][y]^=dd[x]
            bb=[[0]*5 for _ in range(5)]
            for x in range(5):
                for y in range(5): bb[y][(2*x+3*y)%5]=rol(s[x][y],ROT[x][y])
            for x in range(5):
                for y in range(5): s[x][y]=bb[x][y]^((~bb[(x+1)%5][y]&M)&bb[(x+2)%5][y])
            s[0][0]^=RC[r]
    return b"".join(s[x][y].to_bytes(8,"little") for y in range(5) for x in range(5))[:32]
P=0xFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEFFFFFC2F
N=0xFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141
G=(0x79BE667EF9DCBBAC55A06295CE870B07029BFCDB2DCE28D959F2815B16F81798,0x483ADA7726A3C4655DA4FBFC0E1108A8FD17B448A68554199C47D08FFB10D4B8)
def padd(a,b):
    if not a: return b
    if not b: return a
    x1,y1=a; x2,y2=b
    if x1==x2 and (y1+y2)%P==0: return None
    l=((3*x1*x1)*pow(2*y1,P-2,P) if x1==x2 else (y2-y1)*pow(x2-x1,P-2,P))%P
    x3=(l*l-x1-x2)%P; return (x3,(l*(x1-x3)-y1)%P)
def pmul(k,pt=G):
    r=None
    while k:
        if k&1: r=padd(r,pt)
        pt=padd(pt,pt); k>>=1
    return r
def rlp_i(n): return b"" if n==0 else n.to_bytes((n.bit_length()+7)//8,"big")
def rlp(x):
    if isinstance(x,int): x=rlp_i(x)
    if isinstance(x,(bytes,bytearray)):
        if len(x)==1 and x[0]<0x80: return bytes(x)
        if len(x)<=55: return bytes([0x80+len(x)])+bytes(x)
        l=rlp_i(len(x)); return bytes([0xb7+len(l)])+l+bytes(x)
    p=b"".join(rlp(i) for i in x)
    if len(p)<=55: return bytes([0xc0+len(p)])+p
    l=rlp_i(len(p)); return bytes([0xf7+len(l)])+l+p
def jrpc(m,p):
    req=urllib.request.Request(rpc,data=json.dumps({"jsonrpc":"2.0","id":1,"method":m,"params":p}).encode(),headers={"Content-Type":"application/json","User-Agent":"hanzo-pay-smoke/1.0"})
    out=json.loads(urllib.request.urlopen(req,timeout=25).read().decode())
    if "error" in out: raise RuntimeError(out["error"].get("message","rpc error"))
    return out["result"]
rk=open(kf,"r",encoding="utf-8").read().strip()
priv=bytes.fromhex(rk.removeprefix("0x").removeprefix("0X")); d=int.from_bytes(priv,"big"); px,py=pmul(d)
sender="0x"+kec(px.to_bytes(32,"big")+py.to_bytes(32,"big"))[12:].hex()
units=(int(cents_s)*(10**int(dec_s)))//100
to_clean=to_addr.strip().removeprefix("0x").removeprefix("0X")
tok_clean=token_addr.strip().removeprefix("0x").removeprefix("0X")
calldata=bytes.fromhex("a9059cbb")+bytes.fromhex(to_clean).rjust(32,b"\x00")+units.to_bytes(32,"big")
cid=int(jrpc("eth_chainId",[]),16)
nonce=int(jrpc("eth_getTransactionCount",[sender,"latest"]),16)
gp=max(int(jrpc("eth_gasPrice",[]),16),1_000_000_000)*2
to_b=bytes.fromhex(tok_clean)
zh=kec(rlp([nonce,gp,120000,to_b,0,calldata,cid,0,0])); z=int.from_bytes(zh,"big")
v,k=b"\x01"*32,b"\x00"*32
k=hmac.new(k,v+b"\x00"+priv+zh,hashlib.sha256).digest(); v=hmac.new(k,v,hashlib.sha256).digest()
k=hmac.new(k,v+b"\x01"+priv+zh,hashlib.sha256).digest(); v=hmac.new(k,v,hashlib.sha256).digest()
while True:
    v=hmac.new(k,v,hashlib.sha256).digest(); cand=int.from_bytes(v,"big")
    if 1<=cand<N:
        rx,ry=pmul(cand); r=rx%N; s=pow(cand,N-2,N)*(z+r*d)%N
        if r and s:
            rec=(ry&1)^(2 if rx>=N else 0)
            if s>N//2: s=N-s; rec^=1
            raw="0x"+rlp([nonce,gp,120000,to_b,0,calldata,cid*2+35+rec,r,s]).hex(); break
    k=hmac.new(k,v+b"\x00",hashlib.sha256).digest(); v=hmac.new(k,v,hashlib.sha256).digest()
txh=jrpc("eth_sendRawTransaction",[raw])
for _ in range(30):
    rcpt=jrpc("eth_getTransactionReceipt",[txh])
    if rcpt:
        if rcpt.get("status")=="0x1":
            print(txh); sys.exit(0)
        raise RuntimeError(f"ERC-20 transfer {txh} reverted with status {rcpt.get('status')}")
    time.sleep(2)
raise RuntimeError(f"ERC-20 transfer {txh} did not confirm within timeout")
PY
)
  credited_cents=0
  for _ in $(seq 1 60); do
    dep_state=$(wapi "$wp1" GET "/v1/billing/crypto/deposit/$dep_id")
    if [ "$(jq -r '.status // empty' <<<"$dep_state")" = "succeeded" ]; then
      credited_cents=$(jq -r '.creditedCents // 0' <<<"$dep_state")
      break
    fi
    sleep 3
  done
  [ "$credited_cents" -gt 0 ] || { echo "::error::ERC-20 transfer $tx_hash was not credited on deposit $dep_id"; exit 1; }
  wb1=$(wapi "$wp1" GET /v1/billing/balance | jq -r '.cash // 0')
  wd1=$(wapi "$wp1" GET '/v1/billing/transactions?limit=300' | jq '[.transactions[]? | select(.type=="deposit")] | length')
  [ $((wb1 - wb0)) -eq "$credited_cents" ] && [ $((wd1 - wd0)) -eq 1 ] \
    || { echo "::error::wallet crypto ledger moved by $((wb1 - wb0)) in $((wd1 - wd0)) deposits, want $credited_cents in 1"; exit 1; }
  echo "::notice::testnet ERC-20 transfer $tx_hash settled deposit $dep_id (+${credited_cents}c cash)"
fi

# ── 4. Live Test-Mode Top-Up -> Settlement -> Inference -> Reconciliation ────
intent=$(api POST /v1/billing/topup/intent -H "X-Idempotency-Key: smoke-$want-$(date +%s)" -d "{\"amountCents\":$amount,\"country\":\"DE\",\"currency\":\"eur\"}")
id=$(jq -r '.intentId // empty' <<<"$intent"); secret=$(jq -r '.clientSecret // empty' <<<"$intent")
[ -n "$id" ] && [ -n "$secret" ] || {
  echo "::error::the checkout opened no top-up on $want: $(jq -c 'del(.clientSecret)? // .' <<<"$intent" 2>/dev/null | head -c 600 || head -c 600 <<<"$intent")"
  exit 1
}
echo "::add-mask::$secret"
unconfirmed_id="$id"
[ "$(jq -r '.usdCents' <<<"$intent")" = "$amount" ] || { echo "::error::the top-up quote credits $(jq -r '.usdCents' <<<"$intent") cents, not $amount"; exit 1; }

st=$(curl -sS -m 60 -u "$pk:" "https://api.stripe.com/v1/payment_intents/$id/confirm" \
  -d "client_secret=$secret" -d payment_method=pm_card_visa -d return_url=https://pay.hanzo.ai/done | jq -r '.status // .error.message')
case "$st" in succeeded|processing) unconfirmed_id="" ;; *) echo "::error::Stripe's test card was not accepted: $st"; exit 1 ;; esac

credited=""
for _ in $(seq 1 45); do
  credited=$(api GET "/v1/billing/topup/intent/$id" | jq -r '.credited // false')
  [ "$credited" = true ] && break
  sleep 4
done
[ "$credited" = true ] || { echo "::error::the payment succeeded at Stripe and was not credited within 3 minutes"; exit 1; }
sleep 10 # duplicate webhook delivery window
b1=$(bal); d1=$(deposits)
[ $((b1 - b0)) -eq "$amount" ] && [ $((d1 - d0)) -eq 1 ] \
  || { echo "::error::ledger moved by $((b1 - b0)) in $((d1 - d0)) deposits, want $amount in 1"; exit 1; }

# Stage 7 & 8: Unconditional live AI decision call + new usage receipt reconciliation on every release,
# plus optional chat/completions model check when PAY_SMOKE_INFERENCE_MODEL is configured.
prev_usage_id=$(wapi "$wp1" GET /v1/billing/usage | jq -r '.usage[0].transactionId // empty')
dec_code=$(wapi "$wp1" POST /v1/decisions -o "$smoke_dir/dec.json" -w '%{http_code}' \
  -d '{"model":"kai","state":"pay_smoke release gate","questions":{"next":{"type":"choice","options":["proceed","halt"]}}}' || echo 000)
dec_id=$(jq -r '.id // empty' "$smoke_dir/dec.json" 2>/dev/null || true)
[ "$dec_code" = "200" ] && [ -n "$dec_id" ] \
  || { echo "::error::live decision call on kai failed with HTTP $dec_code"; exit 1; }
new_usage_id=$(wapi "$wp1" GET /v1/billing/usage | jq -r '.usage[0].transactionId // empty')
[ -n "$new_usage_id" ] && [ "$new_usage_id" != "$prev_usage_id" ] \
  || { echo "::error::/v1/billing/usage did not record a fresh usage receipt for wallet PKCE org ($prev_usage_id -> $new_usage_id)"; exit 1; }

if [ -n "${PAY_SMOKE_INFERENCE_MODEL:-}" ]; then
  inf_code=$(api POST /v1/chat/completions -o "$smoke_dir/inf.json" -w '%{http_code}' \
    -d "{\"model\":\"${PAY_SMOKE_INFERENCE_MODEL}\",\"max_tokens\":8,\"messages\":[{\"role\":\"user\",\"content\":\"ping\"}]}" || echo 000)
  [ "$inf_code" = "200" ] || { echo "::error::funded inference call on ${PAY_SMOKE_INFERENCE_MODEL} failed with HTTP $inf_code"; exit 1; }
  b2=$(bal)
  [ "$b2" -le "$b1" ] || { echo "::error::post-inference balance $b2 exceeded pre-inference balance $b1"; exit 1; }
fi

echo "::notice::$base on $want: wallet-auth PKCE same-org ($w_org1), cancel+crypto-cancel check, watcher-gate, decision-usage ($new_usage_id), and test-mode settlement (+${amount}c in 1 receipt) certified"
