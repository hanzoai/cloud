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
