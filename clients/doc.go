// Package clients holds the typed inter-subsystem clients used by cloud.Deps.
//
// A dependency resolves to exactly one of two things, and BuildDeps decides
// which from cfg.Enabled(name) alone:
//
//   - the CO-RESIDENT implementation, when the subsystem is mounted in this
//     process. Direct Go method calls; no marshalling, no network. The
//     subsystem's own Mount installs it.
//
//   - Disabled<Subsystem>(): a typed client that fails closed with a clear
//     message. It lets mount code detect "the dep isn't here" without a nil
//     deref, and — the part that matters — it is HONEST about being absent.
//
// # There is no third factory, and there used to be
//
// This package also shipped <Subsystem>RPCAt(addr): a "ZAP RPC" client selected
// by CLOUD_<X>_ZAP_ADDR whose every method returned
//
//	cloud: ZAP RPC client for iam@iam.hanzo.svc:9653 not yet wired (zapc-gen pending)
//
// It was scaffolding for a code generator that never landed, and it made the
// fleet's transport story unfalsifiable. Setting the address logged
// "deps.IAM → ZAP RPC" at boot and then failed every call, so the one signal an
// operator had said the wire was up while nothing crossed it. A client that
// cannot carry a byte is worse than no client, because a missing one is
// diagnosed in seconds and a lying one is diagnosed in an incident.
//
// The transport for a peer that is NOT in this process is the peer plane:
// client.Call over the peer's own socket, addressed by NAME (see client/ask.go, and
// the generated per-app clients under plane/<app>). It needs no endpoint
// configuration, which is why removing the address knobs removed nothing real.
// A subsystem with a plane op is reached; one without is disabled and says so.
package clients
