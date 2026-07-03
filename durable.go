// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package cloud

import (
	"context"
	"fmt"

	aiobject "github.com/hanzoai/ai/object"
	tasksclient "github.com/hanzoai/tasks/pkg/sdk/client"
	tasksengine "github.com/hanzoai/tasks/pkg/tasks"
)

// durableZAPPort is the loopback ZAP port the embedded tasks engine binds. Loopback +
// single binary ⇒ the engine shares cloud's trust boundary: no external tasks Service,
// no cross-service auth, no per-org token minting. Non-9999 to never collide with the
// cluster tasks Service if one also runs.
const durableZAPPort = 19999

// embeddedTasks keeps the in-process engine alive for the process (a package ref the GC
// won't collect) and lets Serve stop it on shutdown.
var embeddedTasks *tasksengine.Embedded

// wireDurableIngest embeds the ONE hanzoai/tasks engine IN-PROCESS — the unified durable
// queue (there is no second async system; tasks/CONTRACT) — and injects a per-org
// loopback ZAP dialer into ai's ingest. A long ingest (github/crawl/s3) then runs as a
// durable workflow in the OWNER's namespace (CONTRACT §6: namespace maps 1:1 to org),
// tracked in the ONE Tasks product. In-process ZAP = mega fast, low latency/memory, no
// HTTP. Fail-soft by construction: any embed error leaves ai's dialer unset →
// EnqueueIngest returns ErrTasksNotConfigured → the handler runs ingest inline (always
// works). Called once, after MountAll (ai is mounted) and before Listen.
func wireDurableIngest(ctx context.Context, deps Deps) {
	emb, err := tasksengine.Embed(ctx, tasksengine.EmbedConfig{
		ZAPPort: durableZAPPort,
		NodeID:  "cloud-tasks",
		// RequireIdentity defaults false: the engine is loopback-only and shares cloud's
		// trust boundary. The org travels as the ZAP Namespace (per-org client) and data
		// isolation is enforced in the workflow input (IngestSource is owner-scoped).
	})
	if err != nil {
		deps.Logger.Warn("durable ingest: tasks embed failed; ingest runs inline", "err", err)
		return
	}
	embeddedTasks = emb
	addr := fmt.Sprintf("127.0.0.1:%d", emb.ZAPPort())
	aiobject.SetIngestDialer(func(org string) (tasksclient.Client, error) {
		return tasksclient.Dial(tasksclient.Options{HostPort: addr, Namespace: org})
	})
	deps.Logger.Info("durable ingest wired: in-process tasks engine", "addr", addr)
}
