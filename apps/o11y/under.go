package o11y

import "github.com/zap-proto/zip"

// under is the [zip.OpTarget] this subsystem declares its typed ops on: the app
// itself, with the subsystem's own root prepended to each op's path.
//
// It replaces a.Group(o11yPrefix), and what changes is the two NAMES a typed op
// publishes, not the address it answers on.
//
// zip qualifies an op's id by the prefix of the occurrence it is declared under,
// because a definition included twice declares one id and produces two
// operations, and a document cannot hold two operations under one operationId.
// The rule is right, and o11yPrefix is not the kind of prefix it is about: it is
// not a composition point a host chose for us, it is this subsystem's own
// address, fixed by HIP-0106. Read through a Group it looked like one, so every
// published id came out "v1.o11y.get_logs" — renaming the OpenAPI operationId,
// the MCP tool, the CLI command and the generated SDK method for every op here,
// and an explicit WithOperationID is qualified the same way, so there was no
// opt-out.
//
// The same Group also cost the ops their ORIGIN. A schema is qualified by the
// app an op was declared in, and a Group is not that app, so the types went out
// bare — `logsResponse` where every other subsystem publishes `o11y.logsResponse`
// — and a bare name is one an unrelated subsystem can collide with in the woven
// document.
//
// An occurrence at the ROOT is unqualified and carries the app's own origin, so
// declaring here gets both: the id the declaration wrote, and the type name the
// weave expects. OpScope.Prefix then does what it documents and prepends to the
// op's path, so the ADDRESS is byte-identical to the Group's — same method, same
// full path. hanzoai/o11y's own table reaches the same conclusion for the same
// reason (relay.go); this is that shape on the cloud-native half.
//
// The middleware comes from the app's own scope rather than being zeroed: a
// Group inherits the app's wrap, and dropping it here would silently unwrap
// every typed op declared through this target.
type under struct {
	app  *zip.App
	root string
}

// OpScope satisfies [zip.OpTarget].
func (u under) OpScope() zip.OpScope {
	s := u.app.OpScope()
	s.Prefix = u.root
	return s
}
