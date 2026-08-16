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
MODEL="${REVIEW_MODEL:-claude-sonnet-4-6}"
KEY="${REVIEW_API_KEY:-}"
# A diff no one can read is not a diff anyone reviewed. Bounded, and the bound
# REFUSES rather than truncating: a silent cut is how the hostile hunk is the
# one that fell off the end.
MAXBYTES="${REVIEW_MAX_BYTES:-400000}"

die() { echo "::error::review: $*" >&2; exit 1; }

[ -n "$KEY" ] || die "no REVIEW_API_KEY — the reviewer cannot run, so the change cannot pass"

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
  ':(exclude)openapi.yaml' ':(exclude)public.yaml'
  ':(exclude)openapi/floor.json' ':(exclude)openapi/closure.json'
  ':(exclude)fleet/catalog.json' ':(exclude)plugin/*/openapi.json'
)

diff_file=$(mktemp); trap 'rm -f "$diff_file" "${req:-}" "${resp:-}" 2>/dev/null' EXIT
git diff --no-color "$BASE".."$HEAD" -- . "${generated[@]}" > "$diff_file" 2>/dev/null || die "could not read the diff $BASE..$HEAD"

bytes=$(wc -c < "$diff_file")
files=$(git diff --name-only "$BASE".."$HEAD" -- . "${generated[@]}" | wc -l)
[ "$bytes" -gt 0 ] || { echo "review: empty diff — nothing to read"; exit 0; }
[ "$bytes" -le "$MAXBYTES" ] || die "diff is ${bytes}B over the ${MAXBYTES}B bound — split the change; a truncated review is not a review"

# Does this change touch the machinery that reviews and releases it?
selfmod=no
if git diff --name-only "$BASE".."$HEAD" | grep -qE '^\.hanzo/'; then selfmod=yes; fi

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

code=$(curl -sS -m 180 -o "$resp" -w '%{http_code}' "$API/v1/chat/completions" \
  -H "Authorization: Bearer $KEY" -H 'content-type: application/json' --data-binary @"$req" || echo 000)
[ "$code" = "200" ] || die "reviewer answered $code — cannot judge this change, so it does not pass ($(head -c 200 "$resp"))"

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
