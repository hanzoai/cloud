# LLM.md — apps/compute

Guidance for apps/compute. Moved from the root LLM.md; cross-cutting rules stay in the root.

## A degraded dependency is reported, never served as an empty success

Surfaces that fold two sources (Visor + the org's BYO registry) keep folding — a
page that 502s on an optional provider is worse than one that shows what it can —
but they now say what they could not reach. `clusterList.degraded` (`apps/visor/
degraded.go`) is additive and `omitempty`, so a healthy response is byte-identical
and an empty list means "you have none" **iff** `degraded` is absent.

This is a shipped defect, not a hypothetical: production Visor sits at
`v1.108.12`, four tags behind the commit that introduced `/v1/k8s/clusters`
(`v1.108.13`), so every call 404s and `GET /v1/k8s/clusters` answered
`{"clusters":[]}` to an operator running eight of them. **No Visor image exists past
`v1.108.12`** — `ghcr.io/hanzoai/visor:v1.108.13` is `not found` — so those routes
exist in git and in no running binary; `/v1/k8s/nodes` needs a Visor *release*, not
a cloud change. `terse()` keeps the upstream's HTML error page out of the response
and the logs.

The machines/GPU fold (`managedMachines`) has the same shape and is **not** covered
yet — it feeds three surfaces through a different type.

### `/v1/k8s/nodes` is Visor's first TYPED op, and the wire moved with it

Upstream it is now `zip.Get[controllers.Scope, controllers.Nodes]`
(`visor/routers/router.go`), so it is in the registry the OpenAPI document, the MCP
tool list, the CLI and every SDK are generated from — where the rest of Visor's `/v1`
surface still is not. A typed op answers its **Out directly**: `{"nodes":[…]}`, no
`{status,msg,data}` around it.

**The trap this closes.** Decoding an envelope into `visorNodes` does not fail: the
keys are simply unknown, `Nodes` stays nil, and an operator running eight clusters is
told *with a 200* that they have no workers. So the op always writes the key, and a
nil list means "this Visor does not serve this op" — `listK8sNodes` answers 502 and
`managedMachines` drops that one source and logs why. `TestK8sNodesRefusesTheOldEnvelope`
and `TestMachinesDropDOKSNodesOnSkew` pin both halves.

**Deploy order.** Visor first, then cloud — but the window is not dangerous either
way, because both directions of the skew now report instead of under-reporting, and
the route 404s in production today regardless (see the release note above).

## Machine computer relay: Cloud side is done, Visor side does not exist

`POST /v1/compute/machines/:id/computer` (the `computer` verb on the existing
machine-action dispatch) lets a bound cloud agent drive the machine's GUI through
the machine's local **hanzod v0.5.2**. Cloud validates and relays; it does not
invent a transport. There is **no** path from a Visor machine id to a local
hanzod in this repository — `computeClient` (client.go) reaches Visor only, and
the machine-agent commands here are bindings plus an agent RUN, neither of which
dials the machine. The node plane's WebSocket invoke addresses a *node id* and
is a different noun. So this needs a Visor/compute endpoint, specified exactly:

```
POST /v1/machines/{owner}/{name}/computer
Authorization: service Basic (COMPUTE_CLIENT_*) or the caller's forwarded bearer
X-Org-Id, X-User-Id, X-User-Email, X-Project-Id, X-App-Id: forwarded as on every
                                              machine route (never a body field)
X-Hanzo-Approve: true                        only when approval was granted
Body: {"action":"screenshot|click|type|…","args":{…},"approval":true}
```

Visor must:

1. Resolve the machine by `(owner, name)` from the validated principal's org;
   an id owned by another tenant is the same 404 as a missing machine. Refuse
   with 409 when the machine has no bound agent / no local hanzod.
2. Open the existing ZeroTier/fabric path to that machine's local hanzod
   (`127.0.0.1:3690`) and forward the body plus the approval header to
   `POST /v1/computer` (no action in the path) or `/v1/computer/{action}`.
3. Answer with hanzod's **status, Content-Type and body bytes verbatim**.
   Re-check the read/act split and reject an act without approval (defence in
   depth). Audit `(org, machine, action, decision, status)` but **never** log
   args, screen pixels, typed text or credentials. Map tunnel failure to 502
   and a machine timeout to 504.
4. Keep the read/act vocabulary in step with hanzod v0.5.2. Cloud's copy is
   `computerReadActions` in computer.go; everything not in that set — including
   an unknown action — requires approval evidence.

Cloud's behaviour while that endpoint is missing: all validation gates still
hold, and a Visor 404/405 is translated to a clear 503 ("the machine computer
relay is not deployed") instead of leaking an upstream HTML error page. Tests
use a fake Visor that serves the specified address, so the validation, approval
forwarding and byte/status passthrough are proven now; they become live when
Visor ships this endpoint. The exact follow-up for the thin `hanzo-inc/cloud`
repo is a module version bump to `github.com/hanzoai/cloud` after this lands —
it imports this module and does not copy these files.
