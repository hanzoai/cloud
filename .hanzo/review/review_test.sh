#!/usr/bin/env bash
# Drives the real .hanzo/review/review.sh against a stub reviewer, to show what a
# status the reviewer CHOSE does versus one that means it never answered.
set -uo pipefail

REVIEW="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/review.sh"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"; kill "${STUB_PID:-0}" 2>/dev/null' EXIT

# one commit to review
repo="$WORK/repo"; mkdir -p "$repo"; cd "$repo" || exit 1
git init -q .; git config user.email t@t.t; git config user.name t
echo one > a.txt; git add a.txt; git commit -qm base
BASE=$(git rev-parse HEAD)
echo two >> a.txt; git commit -qam change
HEAD_SHA=$(git rev-parse HEAD)

stub() { # stub <status> [body]
  local status=$1 body=${2:-'{"choices":[{"message":{"content":"{\"verdict\":\"pass\"}"}}]}'}
  python3 - "$status" "$body" <<'PY' &
import sys, http.server
status, body = int(sys.argv[1]), sys.argv[2].encode()
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        self.rfile.read(int(self.headers.get('content-length', 0) or 0))
        self.send_response(status)
        self.send_header('content-type', 'application/json')
        self.send_header('content-length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *a): pass
http.server.HTTPServer(('127.0.0.1', 8791), H).serve_forever()
PY
  STUB_PID=$!
  sleep 1
}

run() { # run -> prints exit code
  REVIEW_API=http://127.0.0.1:8791 REVIEW_TOKEN=stub REVIEW_MODEL=stub REVIEW_FALLBACKS="" \
    bash "$REVIEW" "$BASE" "$HEAD_SHA" > "$WORK/out" 2>&1
  echo $?
}

fail=0
check() { # check <name> <want-exit> <want-grep>
  local name=$1 want=$2 pat=$3 got
  got=$(run)
  if [ "$got" = "$want" ] && grep -q "$pat" "$WORK/out"; then
    echo "  PASS  $name (exit $got)"
  else
    echo "  FAIL  $name: exit $got want $want; looking for '$pat'"
    sed -n '1,12p' "$WORK/out" | sed 's/^/        /'
    fail=1
  fi
  kill "$STUB_PID" 2>/dev/null; wait "$STUB_PID" 2>/dev/null
}

echo "reviewer ANSWERS (200, verdict pass) -> release continues"
stub 200; check "200 pass" 0 "no attack found"

echo "reviewer CHOOSES a refusal (500) -> release still blocked"
stub 500 '{"error":"boom"}'; check "500 refuses" 1 "cannot judge this change"

echo "reviewer ANSWERS but not with a verdict -> refuses, and says what arrived"
stub 200 '{"choices":[{"message":{"content":"I am unable to review this diff."}},{"finish_reason":"stop"}],"usage":{"completion_tokens":9}}'
check "200 non-verdict" 1 "It opened:"

# A long answer that OPENS like a verdict is the case the head alone cannot read:
# every remedy is consistent with those first 200 characters, and only the end
# says which one it is. The padding is longer than the window on purpose, so the
# reported tail can only have come from the far end of the text.
echo "a long non-verdict -> reports BOTH ends and the size, not just the opening"
pad=$(python3 -c "print('x'*400)")
stub 200 "{\"choices\":[{\"message\":{\"content\":\"{ \\\"verdict\\\": \\\"pass\\\", \\\"summary\\\": \\\"$pad\\\" and then it kept talking UNTERMINATED\"}},{\"finish_reason\":\"stop\"}],\"usage\":{\"completion_tokens\":2218}}"
check "long non-verdict names the end" 1 "and ended:"
grep -q 'UNTERMINATED' "$WORK/out" \
  && echo "  PASS  the tail shown is the far end of the answer" \
  || { echo "  FAIL  the tail did not reach the end of the text"; fail=1; }
grep -qE 'chars=[0-9]{3,}' "$WORK/out" \
  && echo "  PASS  the size is reported" \
  || { echo "  FAIL  no chars= in the refusal"; fail=1; }

echo "reviewer NEVER ANSWERS (503) -> recorded, release continues (~150s of retries)"
stub 503 '{"msg":"identity is unavailable"}'; check "503 unreviewed" 0 "UNREVIEWED"

exit $fail
