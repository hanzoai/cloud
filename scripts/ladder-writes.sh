#!/usr/bin/env bash
# ladder-writes.sh — the personal ladder (Go, Pro, Dev, Max, Ultra, Ultimate and
# Unlimited) as data, written through the typed admin ops. A plan's name carries no multiple: the
# slug's suffix (-3x, -5x, -15x) says it. Re-runnable: every step reads what is there,
# writes only through ops that answer {"status":"ok"}, and reads back what it wrote.
#
#   TOKEN=<SuperAdmin bearer> scripts/ladder-writes.sh          # rows, usage, caps; all seven rungs on sale
#   TOKEN=<SuperAdmin bearer> scripts/ladder-writes.sh ultra    # Ultra restated with its automatic reload and the copy that reload makes true
#   TOKEN=<SuperAdmin bearer> scripts/ladder-writes.sh team     # the team seats' prices and the enterprise card's lines
#   TOKEN=<SuperAdmin bearer> scripts/ladder-writes.sh enterprise  # Enterprise sold by card: seats, its order, usage from the balance
#
# The ladder is hanzo-inc/cloud#565 (owner, 2026-11-09). Each rung includes a multiple
# of Go in every figure, its usage and both request windows: Go 50 a session, 150 a
# day and 500c of each class; Pro (pro-3x) 3x, Dev (dev-5x) 5x, Max (max-15x) 15x,
# Ultra 25x and Ultimate ($495) 50x. Unlimited ($995) has no caps. Every rung is listed
# and sold by card (owner, 2026-11-12; @hanzo/plans 1.10.1). Pro and Max were renamed to the
# multiple they include: pro-3x replaces pro-2x and max-15x replaces max-10x, whose
# holders are served the renamed rung (@hanzo/plans `replaces`, flags.Renamed).
#
# Order: every row is written as a draft (or restated at the status it holds), its
# usage saved and read back (all nine figures, named), its costliest model families
# capped and read back; then one rung at a time is put on sale, only after its
# readback passed; the rungs the ladder replaces are archived
# last, their annual prices cleared (personal plans are sold monthly). Ultra is sold
# as usage past its included amount paid from the prepaid balance; its automatic
# reload is switched on by the `ultra` step, run once reloads for personal buyers are
# deployed, and the first step leaves an Ultra that step has restated as it stands.
set -euo pipefail

API=${API:-https://api.hanzo.ai}
: "${TOKEN:?TOKEN must be a SuperAdmin bearer for $API}"
STEP=${1:-ladder}
case "$STEP" in ladder | ultra | team | enterprise) ;; *) echo "usage: $0 [ladder|ultra|team|enterprise]" >&2; exit 2 ;; esac
command -v jq >/dev/null || { echo "jq is required" >&2; exit 2; }

# call METHOD PATH [BODY] — prints the body; fails on any status but 2xx, and on any
# 2xx whose body says {"status":"error"}.
call() {
  local method=$1 path=$2 body=${3:-} out code
  out=$(curl -sS -X "$method" "$API$path" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    ${body:+--data "$body"} -w $'\n%{http_code}')
  code=${out##*$'\n'}
  out=${out%$'\n'*}
  if [[ $code != 2* ]]; then
    echo "$method $path: HTTP $code: $out" >&2
    return 1
  fi
  if [[ $(jq -r 'if type == "object" then (.status // "ok") else "ok" end' <<<"$out") != ok ]]; then
    echo "$method $path: $out" >&2
    return 1
  fi
  printf '%s' "$out"
}

# ---- the rows ---------------------------------------------------------------------

MAX='{"products":["crm","engine","team"],"apps":["hanzo"],"features":["inference","embeddings","vision","audio"],"seats":1,"earlyAccess":true}'
TAIL='"Early access to new models and features","Train custom models on your data","Hanzo Engine to run models on your own hardware","Dedicated support"'
LINE='For serious builders running agents, shipping code, and needing substantially higher limits.'

# row SLUG NAME PRICE DESCRIPTION FEATURES LIMITS LICENSING [RELOADSPERDAY RELOAD] — a
# personal, monthly plan statement with no annual price, at status STATUS (set below).
row() {
  jq -nc --arg s "$1" --arg n "$2" --argjson p "$3" --arg d "$4" --argjson f "$5" --argjson l "$6" --argjson lic "$7" \
    --argjson rpd "${8:-null}" --argjson rl "${9:-null}" \
    '{slug:$s,name:$n,description:$d,category:"personal",price:$p,annualTotal:0,interval:"month",popular:($s=="pro-3x"),
      features:$f,limits:$l,licensing:$lic} + (if $rpd == null then {} else {reloadsPerDay:$rpd} end)
      + (if $rl == null then {} else {reload:$rl} end)'
}
# usage_lines N — a rung's two usage lines: N times Go's usage, and N times Go's
# windows (the figures `usage` saves below).
usage_lines() { printf '"%sx more usage than Go","%sx higher limits every 5 hours and every day"' "$1" "$1"; }
ULTRA_LINES="\"Everything in Max, plus:\",$(usage_lines 25)"
# Ultimate: Ultra at 50x Go. Unlimited: Ultimate, with no cap on any class and no
# request window.
ULTIMATE_LINES="\"Everything in Ultra, plus:\",$(usage_lines 50)"
UNLIMITED_LINES='"Everything in Ultimate, plus:","No usage caps on any model","No limits every 5 hours or every day"'
ULTRA_PREPAID='Usage beyond what is included is paid from your prepaid balance at list price'
# Ultra's automatic reload, and the sentence that states it from the same figures: the
# card is charged AMOUNT whenever the balance falls below THRESHOLD, with no daily limit.
RELOAD_THRESHOLD=1000
RELOAD_AMOUNT=5000
ULTRA_RELOAD="Usage beyond what is included is paid from your balance at list price, and your card is charged \$$((RELOAD_AMOUNT / 100)) automatically whenever your balance falls below \$$((RELOAD_THRESHOLD / 100)), with no daily limit."
statement() {
  case $1 in
  go) row go Go 800 'Your everyday AI development toolkit, with access to every model and more included usage.' \
    '["Everything in Free, plus:","Build apps with Hanzo Dev","Connect any MCP server in Hanzo Dev","The Hanzo CLI for macOS, Windows and Linux","Every model, including Enso Ultra and Kai","Usage included on every model","Priority inference and priority support","Your own agents, working in the background","Automations, with daily runs included"]' \
    '{"teamGuests":3}' '{"products":["crm","team"]}' ;;
  pro-3x) row pro-3x Pro 1500 "$LINE" \
    "[\"Everything in Go, plus:\",$(usage_lines 3),$TAIL]" \
    '{"teamGuests":3}' "$MAX" ;;
  dev-5x) row dev-5x Dev 4500 "$LINE" \
    "[\"Everything in Pro, plus:\",$(usage_lines 5),$TAIL]" \
    '{"teamGuests":3}' "$MAX" ;;
  max-15x) row max-15x Max 9500 "$LINE" \
    "[\"Everything in Dev, plus:\",$(usage_lines 15),$TAIL]" \
    '{"teamGuests":3}' "$MAX" ;;
  ultra)
    local sentence=$ULTRA_PREPAID rpd=null reload=null
    if [[ $STEP == ultra ]]; then
      sentence=$ULTRA_RELOAD rpd=0 reload=$(jq -nc --argjson t "$RELOAD_THRESHOLD" --argjson a "$RELOAD_AMOUNT" '{thresholdCents: $t, amountCents: $a}')
    fi
    row ultra Ultra 19500 "For the biggest workloads. $sentence" \
      "[$ULTRA_LINES,\"$sentence\",$TAIL]" \
      '{"teamGuests":3}' "$MAX" "$rpd" "$reload" ;;
  ultimate) row ultimate Ultimate 49500 'Everything in Ultra, with 50x the usage.' \
    "[$ULTIMATE_LINES,\"$ULTRA_PREPAID\",$TAIL]" \
    '{"teamGuests":3}' "$MAX" ;;
  unlimited) row unlimited Unlimited 99500 'Everything in Ultra, with no usage caps.' \
    "[$UNLIMITED_LINES,$TAIL]" \
    '{"teamGuests":3}' "$MAX" ;;
  esac
}

# entries is the plan authority's raw rows, by slug.
entries() { call GET /v1/commerce/plans/entries | jq -c 'map({key: .slug, value: .}) | from_entries'; }

# put SLUG STATUS — restate SLUG at STATUS, adding it first when no row holds it, and
# read it back.
put() {
  local slug=$1 status=$2 body held
  body=$(statement "$slug" | jq -c --arg st "$status" '. + {status: $st}')
  held=$(entries | jq -c --arg s "$slug" '.[$s] // empty')
  if [[ -z $held ]]; then
    call POST /v1/admin/plans "$body" >/dev/null
  else
    call PUT "/v1/admin/plans/$slug" "$body" >/dev/null
  fi
  held=$(entries | jq -c --arg s "$slug" '.[$s] // empty')
  jq -e --argjson want "$body" '
    (.status // "active") == $want.status and .price == $want.price and (.annualTotal // 0) == 0 and .category == "personal"
    and .name == $want.name and .description == $want.description and .features == $want.features
    and ((.limits.reloadsPerDay // null) == ($want.reloadsPerDay // null)) and ((.limits.reload // null) == ($want.reload // null))' \
    <<<"$held" >/dev/null || { echo "$slug: read back $held, want $body" >&2; exit 1; }
  echo "$slug: $status"
}

# ---- usage ------------------------------------------------------------------------

# usage SLUG SESSION DAY CENTS — save the rung's Hanzo-only usage (ours CENTS with a
# weekly 168h rolling window at CENTS/4 for finite plans, premium 0 — third-party models
# are prepaid-only — and overage on) and read back all nine figures and that the policy
# names it. A window of 0 is unbounded, and CENTS -1 (flags.Unbounded) includes all of
# Hanzo's models.
usage() {
  local slug=$1 session=$2 day=$3 cents=$4 wh=0 wc=0 body
  if (( cents > 0 )); then
    wh=168
    wc=$(( cents / 4 ))
  fi
  body=$(jq -nc --argjson s "$session" --argjson d "$day" --argjson c "$cents" --argjson wh "$wh" --argjson wc "$wc" \
    '{session:$s,day:$d,overage:true,allowances:{premium:{periodCents:0,windowHours:0,windowCents:0},ours:{periodCents:$c,windowHours:$wh,windowCents:$wc}}}')
  call PUT "/v1/admin/usage/policy/plans/$slug" "$body" >/dev/null
  call GET /v1/admin/usage/policy | jq -e --arg p "$slug" --argjson s "$session" --argjson d "$day" --argjson c "$cents" --argjson wh "$wh" --argjson wc "$wc" '
    .data.plans[] | select(.plan == $p) | .named and .session == $s and .day == $d and .overage
    and .allowances.premium == {periodCents:0,windowHours:0,windowCents:0}
    and .allowances.ours == {periodCents:$c,windowHours:$wh,windowCents:$wc}' >/dev/null \
    || { echo "$slug: usage did not read back as saved" >&2; exit 1; }
  echo "$slug: usage $session/$day/ours=${cents}c (wk=${wc}c/${wh}h), premium=0 named"
}

# caps — monthly plans are Hanzo-only (premium=0), so third-party model rules carry no
# plan caps and grantsPay=false.
caps() {
  echo "caps: skipped (monthly plans are Hanzo-only; third-party models are prepaid-only)"
}

# archive SLUG — restate an old rung as it stands, archived, its annual price cleared.
archive() {
  local slug=$1 held body
  held=$(entries | jq -c --arg s "$slug" '.[$s] // empty')
  [[ -n $held ]] || { echo "$slug: no row to archive" >&2; exit 1; }
  body=$(jq -c '{slug,name,description,category,price,annualTotal:0,interval:"month",status:"archived",popular:(.popular // false),
    features:(.features // []),
    limits:((.limits // {}) | {teamGuests,maxMembers,minSeats,zones,recordsPerZone,queriesPerDay,spendLimits,spendLimit,balanceMonths,balanceCeiling} | with_entries(select(.value != null))),
    licensing:(.licensing // null)}' <<<"$held")
  call PUT "/v1/admin/plans/$slug" "$body" >/dev/null
  entries | jq -e --arg s "$slug" '.[$s] | .status == "archived" and (.annualTotal // 0) == 0 and (.priceAnnual // 0) == 0' >/dev/null \
    || { echo "$slug: did not read back archived with no annual price" >&2; exit 1; }
  echo "$slug: archived"
}

# status SLUG — the status a row holds now, draft when none.
status() { entries | jq -r --arg s "$1" '.[$s].status // "draft" | if . == "" then "active" else . end'; }

# lines TEXT... — the card lines, as a JSON array.
lines() { jq -nc '$ARGS.positional' --args "$@"; }

# restate SLUG FEATURES FIELDS — restate a held row with its card lines replaced and the
# fields of FIELDS (a JSON object) laid over it, then read both back. The row's status,
# capacities and licensing stay as they stand; seat pricing and contact sales are kept
# by the op.
restate() {
  local slug=$1 features=$2 fields=$3 held body
  held=$(entries | jq -c --arg s "$slug" '.[$s] // empty')
  [[ -n $held ]] || { echo "$slug: no row to restate" >&2; exit 1; }
  body=$(jq -c --argjson f "$features" --argjson o "$fields" '
    {slug,name,description,category,price,annualTotal:(.annualTotal // 0),interval:(.interval // "month"),
     status:(if (.status // "") == "" then "active" else .status end),popular:(.popular // false),features:$f,
     limits:((.limits // {}) | {teamGuests,maxMembers,minSeats,zones,recordsPerZone,queriesPerDay,spendLimits,spendLimit,balanceMonths,balanceCeiling} | with_entries(select(.value != null))),
     licensing:(.licensing // null)} + $o' <<<"$held")
  call PUT "/v1/admin/plans/$slug" "$body" >/dev/null
  entries | jq -e --arg s "$slug" --argjson want "$body" '.[$s] | .price == $want.price and (.annualTotal // 0) == $want.annualTotal
    and .description == $want.description and .features == $want.features and .category == $want.category
    and (.licensing // null) == $want.licensing' >/dev/null \
    || { echo "$slug: did not read back as restated" >&2; exit 1; }
  echo "$slug: restated"
}

# enterprise_lines — the Enterprise card's lines, stated once for both steps that
# write them: what an Enterprise organization has, and nothing it does not.
enterprise_lines() {
  lines 'Seat price + usage at API rates' \
    'US$20/seat/month, billed annually, 20 seat minimum. Usage cost scales with model and task.' \
    'All Team plan features, plus:' \
    'Audit logs' \
    'SCIM provisioning through Hanzo IAM'
}

# ENTERPRISE — the rest of what the Enterprise row is, stated by both steps that write
# it: its description, its category, and a Team seat's licensing (CRM and Team), with
# no product, app, engine feature or seat count beyond it.
ENTERPRISE='{"description":"For large businesses operating at scale","category":"enterprise","licensing":{"products":["crm","team"]}}'

# team — the team plan's two seat types and the enterprise card, as the pricing page
# draws them: a standard seat $25 a month or $20 billed yearly, a premium seat $125 a
# month or $100 billed yearly, and Enterprise by quote.
if [[ $STEP == team ]]; then
  restate team "$(lines '$25 per member a month, or $20 per member a month billed yearly' \
    'All Hanzo features, plus more usage than Pro' \
    "Add premium seats to raise the team's shared usage" \
    'One shared workspace: chat, meetings, calendar and contacts' \
    'Single sign-on, roles and an audit log' \
    'One bill for the whole team' \
    'Automations, with daily runs included')" '{}'
  restate team-premium "$(lines '$125 per member a month, or $100 per member a month billed yearly' \
    'Everything in Team, plus:' \
    '5x more usage than standard seats' \
    'Mix premium and standard seats on one team and one bill')" '{"price":12500,"annualTotal":120000}'
  restate enterprise "$(enterprise_lines)" "$ENTERPRISE"
  exit 0
fi

# enterprise — Enterprise sold by card, self-serve, as the order at hanzo.ai/enterprise
# draws it: a seat is $240 a year ($20 a month billed yearly), 20 to 500 seats (a
# larger organization is a contract), each seat a Team seat, and
# usage is paid from the organization's prepaid balance at API rates, so its usage
# policy includes none and allows overage. The order's starting balance is one month
# of the seats' price and at most $50,000 (250 times the floor order's month, and five
# months of the largest). The row states no spend limit, and clears one it holds. The
# row is read and the statement built before anything is written, and the readback
# compares what the step states, so a rerun over a written row passes. The step is
# run once the release that sells it is live.
ENTERPRISE_LIMITS='{"minSeats":20,"maxMembers":500,"balanceMonths":1,"balanceCeiling":50000}'
if [[ $STEP == enterprise ]]; then
  held=$(entries | jq -c '.enterprise // empty')
  [[ -n $held ]] || { echo "enterprise: no row to restate" >&2; exit 1; }
  body=$(jq -c --argjson f "$(enterprise_lines)" --argjson o "$ENTERPRISE" --argjson l "$ENTERPRISE_LIMITS" '
    {slug,name,price:24000,annualTotal:24000,interval:"year",status:"active",popular:(.popular // false),features:$f,
     perSeat:true,contactSales:false,
     limits:($l + (if (.limits.spendLimits // []) != [] or (.limits.spendLimit // null) != null then {spendLimits:[],spendLimit:0} else {} end))}
    + $o' <<<"$held")
  usage enterprise 0 0 0
  call PUT /v1/admin/plans/enterprise "$body" >/dev/null
  entries | jq -e --argjson want "$body" --argjson l "$ENTERPRISE_LIMITS" '.enterprise | .perSeat and (.contactSales | not)
    and .price == $want.price and .annualTotal == $want.annualTotal and .interval == $want.interval and .status == $want.status
    and .category == $want.category and .description == $want.description and .licensing == $want.licensing
    and .features == $want.features and (.limits.spendLimits // null) == null and (.limits.spendLimit // null) == null
    and (.limits | with_entries(select(.key as $k | $l | has($k)))) == $l' >/dev/null \
    || { echo "enterprise: did not read back as stated" >&2; exit 1; }
  echo "enterprise: sold by card, 20 to 500 seats at 24000c a year"
  exit 0
fi

# ladder_usage — every rung's usage, read back: Go's (50 a session, 150 a day, 500c of
# each class) times the rung's multiple, in every figure; Unlimited unbounded in all
# of them. A holder of a renamed slug is the renamed rung's: the limiter serves pro-2x
# as pro-3x and max-10x as max-15x (flags.Renamed), and until a cloud that does is
# deployed it reads the old slug's own figures, so those are the rung's too.
ladder_usage() {
  usage go 50 150 500
  usage pro-3x 150 450 1500
  usage dev-5x 250 750 2500
  usage max-15x 750 2250 7500
  usage ultra 1250 3750 12500
  usage ultimate 2500 7500 25000
  usage unlimited 0 0 -1
  usage pro-2x 150 450 1500
  usage max-10x 750 2250 7500
}

if [[ $STEP == ultra ]]; then
  usage ultra 1250 3750 12500
  put ultra active
  exit 0
fi

# 1. every row as a draft, or restated at the status it already holds; an Ultra the
#    ultra step has restated (it holds a reload) is left as it stands
ultra_reload=false
[[ -n $(entries | jq -r '.ultra.limits.reload // empty') ]] && ultra_reload=true
for slug in go pro-3x dev-5x max-15x ultimate unlimited; do put "$slug" "$(status "$slug")"; done
$ultra_reload || put ultra "$(status ultra)"
# 2. usage, read back
ladder_usage
# 3. the costliest families' share on each rung
caps
# 4. on sale, one rung at a time, each after its readback passed above
for slug in go pro-3x dev-5x max-15x; do put "$slug" active; done
$ultra_reload || put ultra active
for slug in ultimate unlimited; do put "$slug" active; done
# 5. the rungs the ladder replaces, last
for slug in pro-2x max-10x dev max-5x max-20x max-50x; do archive "$slug"; done
echo "ladder written; Ultra's automatic reload is switched on by: $0 ultra"
