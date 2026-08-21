#!/usr/bin/env bash
#
# review — read the change under release and refuse it if it carries an attack.
#
# THE THREAT THIS ANSWERS. Every other gate in this pipeline asks whether the
# code is CORRECT. None asks whether it is HOSTILE. A commit that exfiltrates a
# provider key, opens an egress, weakens an auth check or plants a backdoor
# compiles, passes its tests, regenerates its documents and ships — the drift
# gate has no opinion about intent. This is the one job that reads the diff and
# asks what it is FOR.
#
# IT FAILS CLOSED, AND THAT IS THE WHOLE POINT. A gate that waves the change
# through when the reviewer is unreachable is a gate an attacker turns off by
# making the reviewer unreachable. So an error, a timeout, an unparseable
# verdict and a diff too large to read all REFUSE, and there is no way past it.
set -euo pipefail

BASE="${1:?usage: review.sh <base-sha> <head-sha>}"
HEAD="${2:?usage: review.sh <base-sha> <head-sha>}"

API="${REVIEW_API:-https://api.hanzo.ai}"
# The default is a model this gateway can actually reach on its own paid
# providers. claude-sonnet-4-6 routes to do-ai, whose token was revoked — the
# gate then answered 401 on every push and nothing shipped for a day. Whatever
# stands here must be PAID and private: never the `free` pool, which is
# data-shared, and this reviewer is handed the diff of a private repository.
MODEL="${REVIEW_MODEL:-fireworks/gpt-oss-120b}"

# An IAM access token, minted for this run by whoever invokes the reviewer.
# There is no API key here and there is not meant to be one: IAM issues tokens,
# the gateway accepts them, and a bearer that outlives the run is a bearer that
# has to be rotated in a second place and is therefore forgotten there.
TOKEN="${REVIEW_TOKEN:-}"
# A diff no one can read is not a diff anyone reviewed. Bounded, and the bound
# REFUSES rather than truncating: a silent cut is how the hostile hunk is the
# one that fell off the end.
MAXBYTES="${REVIEW_MAX_BYTES:-400000}"

die() { echo "::error::review: $*" >&2; exit 1; }

[ -n "$TOKEN" ] || die "no REVIEW_TOKEN — the reviewer has no identity, so the change cannot pass"

# `req` and `resp` are created further down, so the trap defaults them: under
# `set -u` a trap that expands an unset name fails, and a failing trap replaces
# the exit status the script chose.
# GENERATED ARTIFACTS ARE NOT READ, and the drift gate is why that is safe.
#
# These six paths are written by `make -f mk/fleet.mk check`, which regenerates
# them FROM SOURCE and refuses any porcelain change (mk/fleet.mk — the same list,
# and it is the list because that gate is what defines it). So they are a
# projection of code that IS reviewed, and a hostile hunk cannot hide in one: to
# survive it would have to be reproduced by the generator, which means it is in
# the generator, which is source. `image` needs `gate`, so the regeneration has
# always already run by the time an image exists.
#
# Reading them anyway is not neutral, it is what stopped releases. Measured
# against v1.801.536: the whole diff was 1,449,734B against a 400,000B bound —
# and 1,331,591B of that, 92%, was these six. The bound then REFUSED (correctly,
# it will not truncate), and because the base is the last release TAG the backlog
# only grew with each push: no release, bigger diff, refused again. A reviewer
# that cannot read a change because it is full of machine output is not reviewing
# the change, and here it was also the thing preventing the change from shipping.
#
# The bound stays 400,000B. What changed is that the budget is spent on prose a
# person wrote: the same range measures 118,143B once these are dropped.
generated=(
  ':(exclude)openapi.yaml' ':(exclude)private.yaml'
  ':(exclude)openapi/floor.json' ':(exclude)openapi/closure.json'
  ':(exclude)fleet/catalog.json' ':(exclude)plugin/*/openapi.json'
)

# Does this change touch the machinery that reviews and releases it?
selfmod=no
if git diff --name-only "$BASE".."$HEAD" | grep -qE '^\.hanzo/'; then selfmod=yes; fi

# READING A CHANGE TOO LARGE FOR ONE REQUEST, RATHER THAN REFUSING IT.
#
# The bound is about ONE request: past it the reviewer would be reading a
# truncated diff, and the hunk that fell off the end is the one that mattered. So
# the bound must never be raised and must never truncate. But REFUSING at the
# bound, with the base being the last release reachable from HEAD, is a deadlock —
# and it closed. Measured on this range: 1,701,350B against the 400,000B bound,
# 4.3x over, 487 files, 207 commits, every one of them refused. Production sat on
# v1.801.536, which is also the base the diff was measured from, so nothing could
# ship and each push made the diff bigger. The exclusion of generated artifacts
# above bought headroom (118,143B at the time it landed) and 207 commits of
# ordinary Go and prose refilled it. Excluding more content only moves where it
# locks; the shape is the problem.
#
# So a change larger than one request is read in AS MANY REQUESTS AS IT TAKES.
# Slices are cut on commit boundaries, each one measured before it is sent, and
# together they cover BASE..HEAD with no gap — so no request is truncated and no
# byte goes unread. That satisfies both laws this script is built on rather than
# trading one for the other: it is strictly MORE review than the refusal it
# replaces, which reviewed nothing at all.
#
# The refusal survives where it is still the honest answer: a SINGLE commit whose
# own diff exceeds the bound cannot be split by this rule, and is refused naming
# itself. And the slice count falls back to 1 as soon as a release lands, because
# the base advances with it.
#
# A slice that rejects exits here. First refusal wins and the rest are not read,
# which is the fail-closed direction.

judge() {
local diff_file="$1" LABEL="$2" files="$3" bytes
trap 'rm -f "${req:-}" "${resp:-}" 2>/dev/null' EXIT
bytes=$(wc -c < "$diff_file")
echo "review: reading $LABEL — ${bytes}B, ${files} file(s)"
# RETURN, never exit: `exit` inside judge would end the script and report
# success with every later request unread.
[ "$bytes" -gt 0 ] || { echo "review: $LABEL is empty — nothing to read"; return 0; }
[ "$bytes" -le "$MAXBYTES" ] || die "diff is ${bytes}B over the ${MAXBYTES}B bound — split the change; a truncated review is not a review"

req=$(mktemp); resp=$(mktemp)
python3 - "$diff_file" "$MODEL" "$selfmod" "$files" > "$req" <<'PY'
import json, sys
diff = open(sys.argv[1], encoding="utf-8", errors="replace").read()
model, selfmod, files = sys.argv[2], sys.argv[3], sys.argv[4]
system = (
 "You review a diff that is about to be RELEASED to production, for hostility rather than for style.\n"
 "Report only what the diff itself shows. Do not speculate about code you cannot see.\n\n"
 "REFUSE (verdict \"reject\") when the diff does any of:\n"
 "- moves a credential, key, token or secret anywhere it can be read (a log, an error, a header, a file, a request body)\n"
 "- adds or widens egress: a new outbound host, an upload, a webhook, a DNS or shell call carrying data\n"
 "- weakens authentication or authorization: a check removed, a gate made conditional, a default turned permissive, a scope widened\n"
 "- disables, narrows or bypasses a test, a CI gate or this reviewer\n"
 "- introduces obfuscated or encoded payloads, eval of remote content, or a dependency swapped to an unrecognised source\n"
 "- adds a hidden trigger: a branch keyed on a magic value, a date, a username, an env var that changes behaviour silently\n\n"
 "ALLOW (verdict \"pass\") ordinary work, including refactors, renames, deletions and dependency BUMPS to known upstreams.\n"
 "A change being large, or touching security-adjacent code, is not by itself grounds to refuse.\n\n"
 "Answer with STRICT JSON and nothing else:\n"
 '{"verdict":"pass"|"reject","confidence":0.0-1.0,"findings":[{"severity":"high"|"medium","file":"path","line":int|null,'
 '"what":"what the diff does","why":"why that is hostile"}],"summary":"one sentence"}\n'
 "An empty findings list with verdict reject is invalid; name what you refused."
)
user = (f"This change touches .hanzo/ (the release and review machinery itself), so judge whether it "
        f"weakens the gate that is judging it.\n\n" if selfmod == "yes" else "")
user += f"{files} files changed.\n\nDIFF:\n{diff}"
# ASK FOR JSON, so there is nothing to extract. The prompt already said "STRICT
# JSON and nothing else" and a prompt is a request; response_format is a
# constraint the gateway enforces. Verified against api.hanzo.ai on this model:
# the content comes back bare and parses directly.
json.dump({"model": model, "max_tokens": 1500, "temperature": 0,
           "response_format": {"type": "json_object"},
           "messages": [{"role":"system","content":system},{"role":"user","content":user}]}, sys.stdout)
PY

# ASKED PROPERLY BEFORE IT IS REFUSED. This gate fails closed, so every dropped
# packet is a refused release — and a 502 from a gateway being rolled, or a
# connection cut mid-body, is not a verdict but the ABSENCE of one. Refusing on
# it reports "this change is hostile" about a change nobody read. So a transient
# answer is asked again, with a pause between tries, and refused only once the
# reviewer has genuinely been given the chance to answer. The fail-closed
# property is untouched: exhausting the tries falls through to the same `die`.
#
# A 4xx is NOT retried. That is the gateway ANSWERING — a refused bearer, an
# unknown model, a body it will not take — and asking twice more returns the
# same answer twice more, turning an instant legible refusal into a slow one.
#
# `-w` prints 000 itself when the transfer never completed, so a `|| echo 000`
# fallback APPENDS to that rather than standing in for it, and the refusal read
# `answered 000000` — a status nothing can look up. Take curl's own word, and
# default only the case where it printed nothing at all.
attempt=1
while :; do
  code=$(curl -sS -m 180 -o "$resp" -w '%{http_code}' "$API/v1/chat/completions" \
    -H "Authorization: Bearer $TOKEN" -H 'content-type: application/json' --data-binary @"$req" || true)
  code=${code:-000}
  case "$code" in
    000|429|502|503|504) : ;;
    *) break ;;
  esac
  if [ "$attempt" -ge 3 ]; then break; fi
  echo "review: $LABEL — reviewer answered $code, which is no answer; asking again ($attempt of 3)"
  sleep $(( attempt * 5 ))
  attempt=$(( attempt + 1 ))
done
# A REFUSED BEARER NAMES ITSELF. The gateway answers `jwt: audience not allowed`
# without saying which audience it saw or which it wanted, and the two live in
# different repositories — the aud is whatever IAM application minted this token
# (HIP-0111: client_id == app == aud), the allowlist is GATEWAY_ALLOWED_AUDIENCES
# on the deployment. So a reader holding only the 401 has to go and mint a token
# by hand to learn the one value that decides it. We are holding the token, so we
# can just read it: `iss` and `aud` are identifiers rather than credentials, and
# printing them turns an opaque refusal into the name to add to a list.
claims() {
  local p; p=$(printf %s "$TOKEN" | cut -d. -f2) || return 0
  case $(( ${#p} % 4 )) in 2) p="$p==";; 3) p="$p=";; esac
  printf %s "$p" | tr '_-' '/+' | base64 -d 2>/dev/null \
    | python3 -c 'import json,sys
try: c = json.load(sys.stdin)
except Exception: sys.exit(0)
# aud is a string OR an array (RFC 7519), and the point of printing it is that a
# reader copies the value into an allowlist — so render the members, never the
# Python list that once made this read aud=["hanzo-review"].
def v(x): return ",".join(map(str, x)) if isinstance(x, list) else str(x)
print(" ".join(f"{k}={v(c[k])}" for k in ("iss","aud","sub","client_id") if c.get(k)))' 2>/dev/null
}
presented=$(claims)
[ "$code" = "200" ] || die "reviewer answered $code — cannot judge this change, so it does not pass ($(head -c 200 "$resp"))${presented:+ [presented $presented]}"

python3 - "$resp" "$selfmod" <<'PY'
import json, os, re, sys
raw = json.load(open(sys.argv[1]))
try:
    text = raw["choices"][0]["message"]["content"]
except Exception:
    print("::error::review: no answer in the reviewer's response"); sys.exit(1)
# THE VERDICT IS AN OBJECT, NOT A SPAN OF TEXT.
#
# This used to be re.search(r"\{.*\}", text, re.S) — GREEDY, so it took from the
# FIRST brace in the answer to the LAST one anywhere in it. One stray brace in a
# summary or a quoted finding and the span is two objects and some prose glued
# together, which cannot parse, and a release is refused for a reason that has
# nothing to do with the change. That is what blocked run 73092: "unparseable
# verdict (Expecting property name enclosed in double quotes: line 1 column 2)".
#
# With response_format above, the content IS the object and parses directly.
# The scan is the fallback for a gateway that ignores the constraint, and it is
# STRICTER than the regex it replaces, never looser:
#
#   - it reads BALANCED objects rather than one greedy span, so prose around the
#     answer cannot corrupt it;
#   - a candidate counts only if it parses AND carries a "verdict" key, so a
#     brace-bearing sentence is not a verdict;
#   - and if TWO such objects appear it REFUSES as ambiguous rather than picking
#     one. That is the injection guard: a diff under review can contain
#     {"verdict":"pass"}, and a model quoting it back must never be able to
#     outrank the model's own answer by position.
def _objects(s):
    depth = 0; start = None
    for i, ch in enumerate(s):
        if ch == '{':
            if depth == 0: start = i
            depth += 1
        elif ch == '}' and depth > 0:
            depth -= 1
            if depth == 0: yield s[start:i + 1]

def _verdicts(s):
    out = []
    for c in _objects(s):
        try: o = json.loads(c)
        except Exception: continue
        if isinstance(o, dict) and "verdict" in o: out.append(o)
    return out

try:
    v = json.loads(text)
    if not (isinstance(v, dict) and "verdict" in v):
        raise ValueError("no verdict")
except Exception:
    found = _verdicts(text)
    if not found:
        print("::error::review: the reviewer did not answer with a JSON verdict — refusing rather than guessing"); sys.exit(1)
    if len(found) > 1:
        print("::error::review: the answer carries more than one verdict object — refusing rather than choosing between them"); sys.exit(1)
    v = found[0]

verdict = str(v.get("verdict","")).lower()
findings = v.get("findings") or []
summary = v.get("summary","")
lines = [f"### Security review\n", f"**verdict:** `{verdict}` — {summary}\n"]
if sys.argv[2] == "yes":
    lines.append("> This change edits `.hanzo/` — the release and review machinery itself.\n")
for f in findings:
    loc = f.get("file","?") + (f":{f['line']}" if f.get("line") else "")
    lines.append(f"- **{f.get('severity','?')}** `{loc}` — {f.get('what','')} · _{f.get('why','')}_")
summary_path = os.environ.get("GITHUB_STEP_SUMMARY")
if summary_path:
    open(summary_path,"a").write("\n".join(lines) + "\n")
print("\n".join(lines))

if verdict == "reject":
    if not findings:
        print("::error::review: rejected with no finding named — treating as a refusal"); sys.exit(1)
    print("::error::review REFUSED this change — see the findings above"); sys.exit(1)
if verdict != "pass":
    print(f"::error::review: unknown verdict {verdict!r} — refusing rather than guessing"); sys.exit(1)
print("review: no attack found in this diff")
PY
rm -f "$req" "$resp"
}

# ── the change, as units no larger than one request ───────────────────────────
#
# A UNIT is what one commit actually CONTRIBUTES, which is not the same as the
# range diff up to it, and the difference is what made a naive walk wrong:
#
#   A MERGE contributes its conflict RESOLUTION and nothing else. Its content
#   arrives through the commits it brought, which rev-list already walks, so
#   diffing a merge against its first parent re-reads all of them. Measured on
#   efd4c1127: first-parent 880,148B against a combined diff of 246B. This is the
#   same first-parent trap the base rule above already documents, one level down.
#
#   A commit too large to read is split BY FILE, because a file is the smallest
#   thing a finding can name. Measured on 27b36872d: 2,426,774B across 1,623
#   files, largest single file 194,999B — so the split terminates well inside the
#   bound. A single FILE over the bound is irreducible and refused naming itself,
#   which is where the original refusal is still the honest answer.
#
# Units are then packed into requests while they fit. Coverage is every commit's
# own change exactly once; no request is truncated; and the whole range is read
# whatever its size. Sending per-commit diffs rather than one squashed range diff
# also keeps commit boundaries and messages in front of the reviewer, which is
# what makes a hunk legible as intent rather than as text.

units=$(mktemp -d); trap 'rm -rf "$units" 2>/dev/null' EXIT
n=0

emit() {  # emit <path-to-diff> <label> <file-count>
  [ -s "$1" ] || return 0
  local b; b=$(wc -c < "$1")
  [ "$b" -le "$MAXBYTES" ] || die "$2 diffs ${b}B over the ${MAXBYTES}B bound and cannot be split further — a truncated review is not a review"
  n=$((n + 1)); printf '%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$b" >> "$units/index"
}

for c in $(git rev-list --reverse "$BASE".."$HEAD"); do
  short=$(git rev-parse --short "$c")
  d="$units/$short.diff"
  # `--parents -1` prints "<sha> <parent>..."; more than two words is a merge.
  # NOT `--count --parents`, which prints the count and drops the parents, so
  # every merge reads as a normal commit and takes the first-parent path above.
  if [ "$(git rev-list --parents -1 "$c" | wc -w)" -gt 2 ]; then
    # a merge: the resolution only
    git show --cc --no-color --format= "$c" -- . "${generated[@]}" > "$d" 2>/dev/null || die "could not read merge $short"
    emit "$d" "merge $short" "$(git show --cc --name-only --format= "$c" -- . "${generated[@]}" 2>/dev/null | wc -l)"
    continue
  fi
  git diff --no-color "$c^1" "$c" -- . "${generated[@]}" > "$d" 2>/dev/null || die "could not read commit $short"
  if [ "$(wc -c < "$d")" -le "$MAXBYTES" ]; then
    emit "$d" "commit $short" "$(git diff --name-only "$c^1" "$c" -- . "${generated[@]}" | wc -l)"
    continue
  fi
  # too large to read whole: one unit per file
  rm -f "$d"
  i=0
  git diff --name-only "$c^1" "$c" -- . "${generated[@]}" | while IFS= read -r f; do
    i=$((i + 1)); fd="$units/$short.$i.diff"
    git diff --no-color "$c^1" "$c" -- "$f" > "$fd" 2>/dev/null || die "could not read $f in $short"
    printf '%s\t%s\t%s\t%s\n' "$fd" "commit $short · $f" 1 "$(wc -c < "$fd")" >> "$units/index"
  done
done

[ -s "$units/index" ] || { echo "review: nothing to read in $BASE..$HEAD"; exit 0; }

# A file unit written by the subshell above skipped emit's bound check, so it is
# applied here over every unit — one place, so no path can miss it.
while IFS=$(printf '\t') read -r _ label _ b; do
  [ "$b" -le "$MAXBYTES" ] || die "$label diffs ${b}B over the ${MAXBYTES}B bound and cannot be split further — a truncated review is not a review"
done < "$units/index"

# pack units into requests, in order, while they fit
slice=$(mktemp); slices=0; acc=0; nfiles=0; first=""; last=""
flush() {
  [ "$acc" -gt 0 ] || return 0
  slices=$((slices + 1))
  judge "$slice" "slice $slices ($first .. $last)" "$nfiles"
  : > "$slice"; acc=0; nfiles=0; first=""
}
while IFS=$(printf '\t') read -r path label files b; do
  if [ "$acc" -gt 0 ] && [ $((acc + b)) -gt "$MAXBYTES" ]; then flush; fi
  cat "$path" >> "$slice"
  acc=$((acc + b)); nfiles=$((nfiles + files)); last="$label"
  [ -n "$first" ] || first="$label"
done < "$units/index"
flush
rm -f "$slice"

echo "review: read $BASE..$HEAD as $(wc -l < "$units/index") unit(s) in $slices request(s); no attack found"
