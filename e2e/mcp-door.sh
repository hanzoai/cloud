#!/usr/bin/env bash
# THE ONE MCP DOOR, end to end, against a real host and real plugin processes.
#
# It proves the two properties the design rests on, by MEASURING them rather than
# by reading code:
#
#   1. tools/list costs ZERO process wakes. The host answers from the fleet
#      catalogue (fleet/catalog.json, generated from every plugin/<app>/openapi.json
#      by plugin/gen-fleet-catalog and embedded), so the child process count before
#      and after must be identical.
#   2. tools/call wakes exactly ONE child — the plugin that owns the named tool —
#      over ZAP on its private unix socket, and returns that plugin's own answer.
#
# It also fails when a listed tool has an EMPTY description. A model pays context
# for every tool it is shown and cannot choose one that says nothing; that exact
# bug shipped here once (zipdoc blind to group prefixes), so it is asserted.
#
#   usage:  make cloud && make -f mk/fleet.mk binaries  # host + plugins into ./bin
#           e2e/mcp-door.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="$ROOT/bin"
PORT="${PORT:-18080}"
# EMPTY mounts the WHOLE fleet, which is the strongest form of the measurement:
# 112 plugins mounted, tools/list answered, child count unchanged. A named subset
# runs faster; every child pulls its data-plane key from the broker, so any subset
# must include it (cmd/cloud refuses one that omits it).
ENABLE="${ENABLE-}"
export CLOUD_KMS_MASTER_KEY_REF="${CLOUD_KMS_MASTER_KEY_REF:-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=}"
export CLOUD_DATA_DIR="${CLOUD_DATA_DIR:-$(mktemp -d)}"

[ -x "$BIN/cloud" ] || { echo "no $BIN/cloud — run: make cloud"; exit 1; }

"$BIN/cloud" --listen "127.0.0.1:$PORT" --zap "127.0.0.1:0" ${ENABLE:+--enable "$ENABLE"} \
  >"$CLOUD_DATA_DIR/host.log" 2>&1 &
HOST=$!
trap 'kill -TERM $HOST 2>/dev/null || true; wait $HOST 2>/dev/null || true' EXIT

for _ in $(seq 1 300); do
  curl -sf "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1 && break
  sleep 0.2
done
curl -sf "http://127.0.0.1:$PORT/healthz" >/dev/null || { echo "host never came up:"; cat "$CLOUD_DATA_DIR/host.log"; exit 1; }

# children counts only the plugins THIS host spawned.
children() { pgrep -P "$HOST" 2>/dev/null | wc -l | tr -d ' '; }

# The caller's own credential rides through: the host forwards the inbound request
# verbatim, and the OWNING plugin's cloud.Serve chain re-derives identity from it.
rpc() {
  curl -s -X POST "http://127.0.0.1:$PORT/v1/mcp" -H 'Content-Type: application/json' \
    ${AUTH:+-H "Authorization: $AUTH"} -d "$1"
}

echo "== the door is the host's =="
rpc '{"jsonrpc":"2.0","id":0,"method":"initialize"}' | tee "$CLOUD_DATA_DIR/init.json"; echo

BEFORE=$(children)
echo "== tools/list =="
rpc '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' > "$CLOUD_DATA_DIR/list.json"
AFTER=$(children)
COUNT=$(python3 -c 'import json,sys;print(len(json.load(open(sys.argv[1]))["result"]["tools"]))' "$CLOUD_DATA_DIR/list.json")
echo "tools listed: $COUNT   children before=$BEFORE after=$AFTER"

[ "$BEFORE" = "$AFTER" ] || { echo "FAIL: tools/list woke $((AFTER-BEFORE)) child process(es); it must wake ZERO"; exit 1; }
[ "$COUNT" -gt 0 ] || { echo "FAIL: the door lists no tools"; exit 1; }

python3 - "$CLOUD_DATA_DIR/list.json" <<'PY'
import json, sys
tools = json.load(open(sys.argv[1]))["result"]["tools"]
bare = [t["name"] for t in tools if not (t.get("description") or "").strip()]
if bare:
    print("FAIL: tools listed with an EMPTY description:", ", ".join(bare[:10])); sys.exit(1)
# ABSENT, not empty: `{}` is a real schema — it is what an op whose body is an
# arbitrary document (a PostHog flag definition, say) honestly publishes.
noschema = [t["name"] for t in tools if t.get("inputSchema") is None]
if noschema:
    print("FAIL: tools listed with NO inputSchema at all:", ", ".join(noschema[:10])); sys.exit(1)
print("every listed tool carries prose and a schema")

# ONE TOOL PER SUBSYSTEM, the operation in an argument (fleet/grouped.go). The
# flat surface was 1,189 tools in 977 KB and clients keep 128, so the count is a
# correctness property here and not a nicety.
#
# What makes a tool a subsystem is that it CARRIES an `op` enum, not what it is
# called. The names used to be `hanzo_<app>` and this read the prefix; a prefix is
# a convention and the enum is the thing itself, so it asks about the thing.
def enum_of(t):
    return t["inputSchema"].get("properties", {}).get("op", {}).get("enum", [])
stray = [t["name"] for t in tools if t["name"] != "describe" and not enum_of(t)]
if stray:
    print("FAIL: the door published a flat operation:", ", ".join(stray[:10])); sys.exit(1)
if not any(t["name"] == "describe" for t in tools):
    print("FAIL: the door published no `describe`; the enums carry names only and cannot be read without it"); sys.exit(1)
ops = [op for t in tools for op in enum_of(t)]
print("%d tools carrying %d operations (a client keeps 128)" % (len(tools), len(ops)))
if len(tools) >= 128:
    print("FAIL: %d tools is back over the cap" % len(tools)); sys.exit(1)
if not ops:
    print("FAIL: the door published no operations at all"); sys.exit(1)
PY

echo
echo "== one operation, with the doc comment its handler carries =="
# A READ by default: the point is to show the owner answering, not to mutate.
# `describe` is how a schema is fetched now — the enums carry names only.
TOOL="${TOOL:-$(python3 -c '
import json,sys
tools=json.load(open(sys.argv[1]))["result"]["tools"]
ops=[op for t in tools for op in t["inputSchema"].get("properties",{}).get("op",{}).get("enum",[])]
reads=[o for o in ops if o.startswith("get_")]
print((reads or ops)[0])' "$CLOUD_DATA_DIR/list.json")}"
GROUP="$(python3 -c '
import json,sys
for t in json.load(open(sys.argv[1]))["result"]["tools"]:
    if sys.argv[2] in t["inputSchema"].get("properties",{}).get("op",{}).get("enum",[]):
        print(t["name"]); break
else:
    sys.exit("FAIL: %s is in no subsystem enum" % sys.argv[2])' "$CLOUD_DATA_DIR/list.json" "$TOOL")"
echo "$TOOL is served through $GROUP"
rpc "{\"jsonrpc\":\"2.0\",\"id\":9,\"method\":\"tools/call\",\"params\":{\"name\":\"describe\",\"arguments\":{\"op\":\"$TOOL\"}}}" \
  > "$CLOUD_DATA_DIR/describe.json"
python3 - "$CLOUD_DATA_DIR/describe.json" "$TOOL" <<'PY'
import json, sys
res = json.load(open(sys.argv[1])).get("result") or {}
text = "".join(c.get("text", "") for c in res.get("content", []))
if not text:
    print("FAIL: describe returned nothing for %s: %s" % (sys.argv[2], json.dumps(res)[:400])); sys.exit(1)
d = json.loads(text)
if d.get("name") != sys.argv[2] or d.get("inputSchema") is None:
    print("FAIL: describe answered %s" % text[:400]); sys.exit(1)
print(json.dumps(d, indent=2)[:1600])
PY

echo
echo "== tools/call, through the subsystem tool =="
BEFORE=$(children)
ARGS="${ARGS:-{\}}"
rpc "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"$GROUP\",\"arguments\":{\"op\":\"$TOOL\",\"input\":$ARGS}}}" | tee "$CLOUD_DATA_DIR/call.json"; echo
AFTER=$(children)
echo "children before=$BEFORE after=$AFTER  (a cold owner wakes exactly one)"
[ $((AFTER-BEFORE)) -le 1 ] || { echo "FAIL: tools/call woke $((AFTER-BEFORE)) children; it must wake at most ONE"; exit 1; }

echo
echo "OK — one door, zero wakes on list, one wake on call."
