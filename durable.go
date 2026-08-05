// SPDX-License-Identifier: Apache-2.0

package cloud

// Durable execution seam (OSS core).
//
// The private Hanzo Cloud build embeds the hanzoai/tasks engine in-process (the
// unified durable queue) and injects it here. The OSS core ships ONLY the seam:
// a DurableEngine interface (cloud/types) plus a no-op default, and a single
// registration hook the private build (or an operator) calls to install a real
// engine. Everything degrades fail-soft — a subsystem that enqueues durable work
// runs it inline when only the no-op is registered, exactly as the private path
// falls back to inline ingest when the engine fails to embed.

import (
	"context"

	"github.com/hanzoai/cloud/types"
)

// DurableEngine is re-exported at the cloud root so subsystem code spells
// cloud.DurableEngine (the same aliasing the other client interfaces use).
type DurableEngine = types.DurableEngine

// NoopDurable is the OSS default engine: it accepts every call and does nothing
// durable. Submit returns an empty id, Signal/Query are no-ops. A subsystem must
// treat an empty Submit id as "run inline" — the fail-soft contract.
type NoopDurable struct{}

func (NoopDurable) Submit(ctx context.Context, namespace, kind string, payload []byte) (string, error) {
	return "", nil
}
func (NoopDurable) Signal(ctx context.Context, namespace, id, name string, payload []byte) error {
	return nil
}
func (NoopDurable) Query(ctx context.Context, namespace, id string) ([]byte, error) {
	return nil, nil
}

// durableEngine holds the process-wide engine. It is the no-op until an operator
// or the private build installs a real one via RegisterDurableEngine. A package
// ref the GC won't collect; read via Durable().
var durableEngine DurableEngine = NoopDurable{}

// RegisterDurableEngine installs the process durable engine. The private build
// calls this once at boot with the embedded hanzoai/tasks engine; the OSS core
// leaves the no-op in place. Exactly one registration; a nil argument is ignored
// so the no-op is never replaced by a nil that would nil-panic every caller.
func RegisterDurableEngine(e DurableEngine) {
	if e != nil {
		durableEngine = e
	}
}

// Durable returns the process durable engine — the registered real engine, or
// the no-op default. Never nil.
func Durable() DurableEngine { return durableEngine }

// firstNonEmptyStr returns the first non-empty string, else the last.
func firstNonEmptyStr(vals ...string) string {
	last := ""
	for _, v := range vals {
		if v != "" {
			return v
		}
		last = v
	}
	return last
}

// wireDurableIngest is the OSS no-op boot hook Serve calls after MountAll. In the
// private build this embeds the tasks engine and wires ai's durable ingest; in the
// OSS core there is no ai subsystem and no embedded engine, so it only logs that
// the durable plane is the no-op default. Kept as the ONE call site so the private
// build overrides behavior by registering an engine, not by editing Serve.
func wireDurableIngest(ctx context.Context, deps Deps) {
	if _, isNoop := durableEngine.(NoopDurable); isNoop {
		deps.Logger.Info("durable engine: no-op default (OSS core) — durable work runs inline")
	}
}
