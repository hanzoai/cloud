// Package apps is the composition root for the OSS Hanzo Cloud core: the single,
// explicit list of which subsystems are linked into the binary AND the order they
// mount in.
//
// Wire() returns []cloud.MountSpec in mount order (slice position == order). There
// is no init()-registry and no order-int: adding, removing, or reordering a
// subsystem is a one-line edit to Wire(), read top-to-bottom. cmd/cloud calls
// Wire() and threads the slice into cloud.Serve — the set is defined ONCE, here.
//
// This is the local dev server: every subsystem here runs on one machine with a
// data directory as its only dependency.
//
// (This package must NOT live in package cloud: the subsystems import cloud for
// Deps + Typed, so a root-package bundle would form an import cycle. As a sibling
// subpackage it composes them without one.)
package apps

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud"

	// In-repo subsystem packages (clients/*). Each exports a Mount (and, where it
	// owns process-lifetime resources, a Shutdown); Wire references them directly.
	"github.com/hanzoai/cloud/clients/auditlog"
	"github.com/hanzoai/cloud/clients/base"
	"github.com/hanzoai/cloud/clients/code"
	"github.com/hanzoai/cloud/clients/dns"
	"github.com/hanzoai/cloud/clients/exec"
	"github.com/hanzoai/cloud/clients/flags"
	"github.com/hanzoai/cloud/clients/functions"
	"github.com/hanzoai/cloud/clients/gateway"
	"github.com/hanzoai/cloud/clients/ingress"
	"github.com/hanzoai/cloud/clients/kms"
	"github.com/hanzoai/cloud/clients/kv"
	"github.com/hanzoai/cloud/clients/plugin"
	"github.com/hanzoai/cloud/clients/security"
	"github.com/hanzoai/cloud/clients/session"
	"github.com/hanzoai/cloud/clients/storage"
	"github.com/hanzoai/cloud/clients/tasks"
)

// Wire returns every linked subsystem as a cloud.MountSpec, in mount order. The
// slice position IS the order: cloud.MountAll iterates it as-given, registering
// each subsystem's teardown as a zip shutdown hook so teardown runs in reverse
// (LIFO). Enablement is a separate axis: cloud.Serve mounts only the specs
// cfg.Enabled(name) admits, so a STAGED subsystem is linked but inert until named.
func Wire() []cloud.MountSpec {
	return []cloud.MountSpec{
		// Insights feature-flag evaluation seam (no routes; a hot value plane).
		{Name: "flags", Mount: flags.Mount, Shutdown: flags.Shutdown, OwnsHealth: true},
		// Embedded KMS secrets plane /v1/kms/*. OwnsHealth: serves its own
		// fail-closed /v1/kms/health. Fails closed until CLOUD_KMS_MASTER_KEY_REF.
		{Name: "kms", Mount: kms.Mount, OwnsHealth: true},
		// Embedded runtime edge (/v1/ingress/*). STAGED — edge listeners stay off
		// unless the operator names "ingress" in CLOUD_ENABLE.
		{Name: "ingress", Mount: ingress.Mount, Shutdown: ingress.Shutdown},
		// /v1/s3/buckets/* + /v1/s3/health. OwnsHealth (real fail-closed probe).
		{Name: "storage", Mount: storage.Mount, OwnsHealth: true},
		// The /v1/dns forward head: relays the console DNS dashboard to the DNS
		// control plane under the caller's own validated bearer.
		{Name: "dns", Mount: dns.Mount},
		{Name: "code", Mount: code.Mount, Shutdown: code.Shutdown},
		{Name: "session", Mount: session.Mount, Shutdown: ctxShutdown(session.Shutdown)},
		{Name: "security", Mount: security.Mount, Shutdown: ctxShutdown(security.Shutdown), OwnsHealth: true},
		{Name: "exec", Mount: exec.Mount},
		{Name: "gateway", Mount: gateway.Mount},
		{Name: "audit", Mount: auditlog.Mount},

		// The local apps. Each is a REAL implementation over the embedded store,
		// not a relay to a cluster: the dev edition is meant to be a working
		// product on a laptop with no network, and an app that only forwards
		// somewhere else would make it a client for a thing you do not have.
		//
		// Every route in these is a typed op, which is why they need no
		// per-app integration work: one declaration is simultaneously the REST
		// route, the OpenAPI schema, the MCP tool and the generated SDK method.
		{Name: "base", Mount: base.Mount, Shutdown: base.Shutdown},
		{Name: "kv", Mount: kv.Mount, Shutdown: kv.Shutdown},
		{Name: "tasks", Mount: tasks.Mount, Shutdown: tasks.Shutdown},
		// STAGED (see stagedSubsystems): functions RUNS CODE, so it is linked in
		// every build but mounts only when an operator names it.
		{Name: "functions", Mount: functions.Mount, Shutdown: functions.Shutdown},

		// Runtime wasm/proxy plugins — mounts dead last.
		{Name: "plugins", Mount: plugin.Mount},
	}
}

// ServeSingle is the ONE way to run a single app standalone: validate `name`
// against Wire(), then serve exactly it (cloud.Serve with a one-name enable list).
// Returns an error for an unknown name rather than booting a no-op.
func ServeSingle(name string) error {
	if name == "" {
		return fmt.Errorf("ServeSingle: empty app name")
	}
	for _, spec := range Wire() {
		if spec.Name == name {
			return cloud.Serve(Wire(), []string{name})
		}
	}
	return fmt.Errorf("ServeSingle: unknown app %q", name)
}

// ctxShutdown adapts a subsystem's zero-arg Shutdown() error to the
// cloud.ShutdownFunc(ctx) signature. Several subsystems expose the simpler form
// (their teardown ignores the deadline); this bridges the impedance mismatch in
// ONE place so the Wire entries stay declarative — no inline closures.
func ctxShutdown(f func() error) cloud.ShutdownFunc {
	return func(context.Context) error { return f() }
}
