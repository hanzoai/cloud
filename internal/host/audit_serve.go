package host

// Audit trail construction — the wiring Serve calls to stand up the Recorder.
//
// The audit store is a COMPLIANCE CONTROL, so its persistence is treated like the
// pricing catalog overlay's: a non-persistent (in-memory) audit trail would
// silently lose the record of every prior action on each restart — a fail-OPEN
// degradation of an integrity control. So an empty DataDir is a hard boot error
// in a normal run (prod always sets CLOUD_DATA_DIR; provisioning + pricing already
// require it, so the unified binary always has one). The trail can be turned OFF
// deliberately (CLOUD_AUDIT_DISABLED=true) for a minimal single-service dev run —
// an explicit opt-out, never a silent one.

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	cloud "github.com/hanzoai/cloud/sdk"

	adminpeer "github.com/hanzoai/cloud/client/admin"
	"github.com/hanzoai/cloud/internal/driver/datastore"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/env"
	luxlog "github.com/luxfi/log"
)

// holdsTrail reports whether this process holds the deployment's audit trail: the
// process that mounts admin, whose GET /v1/admin/audit reads it.
func holdsTrail(plugins []cloud.Plugin) bool {
	return slices.ContainsFunc(plugins, func(p cloud.Plugin) bool { return p.Name == adminpeer.App })
}

// Recorder constructs the audit Recorder from cfg.
//
// ONE STORE. The process that holds the trail (holds — the one mounting admin)
// opens the append-only SQLite chain, plus a best-effort datastore OLAP mirror
// when a datastore is configured. Every other process opens nothing: its Recorder
// is audit.Remote over the plane, so each record it appends lands in the chain
// /v1/admin/audit reads, and an append admin does not take is an error its call
// site fails closed on. A process holding no chain needs no data dir for one.
//
// Returns (nil, nil) only when the trail is explicitly disabled — the caller then
// wires a no-op middleware. proc names the process whose chain this is (serve.go
// procName): "cloud" for a fused host, the app's own name for a plugin child.
//
// The chain lands where cek and namespace put it — {DataDir}/orgs/_platform/ —
// NOT at {DataDir} itself. Chains at the data root are the abandoned pre-cek
// location, open by nobody; a reading that lists {DataDir} sees frozen files and
// concludes the trail died. Ask namespace where a chain is; never spell it here.
func Recorder(cfg *cloud.Config, logger luxlog.Logger, proc string, holds bool) (*audit.Recorder, error) {
	if envTrue("CLOUD_AUDIT_DISABLED") {
		if logger != nil {
			logger.Warn("audit trail DISABLED by CLOUD_AUDIT_DISABLED — no tamper-evident record will be kept")
		}
		return nil, nil
	}
	if !holds {
		if logger != nil {
			logger.Info("audit trail is held by "+adminpeer.App+"; every record is appended there", "proc", proc)
		}
		rec := audit.Remote(adminpeer.App, auditPeer{})
		rec.Acting(cloud.ActOf)
		return rec, nil
	}
	if cfg.DataDir == "" {
		return nil, fmt.Errorf("empty DataDir — the audit trail is a compliance control and requires a persistent data dir (set CLOUD_DATA_DIR); refusing to boot with a non-persistent trail that would lose all prior records on restart (or set CLOUD_AUDIT_DISABLED=true to opt out explicitly)")
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("data dir: %w", err)
	}

	// OLAP mirror is optional and best-effort. A mirror that cannot be reached at
	// boot must NOT stop the binary — the local chain is the authority — so a
	// mirror construction error is logged and the trail runs local-only.
	var mirror audit.Mirror
	if m, err := datastore.New(logger); err != nil {
		if logger != nil {
			logger.Warn("audit OLAP mirror unavailable — running local-only (chain integrity unaffected)", "err", err)
		}
	} else {
		mirror = m
	}

	// ONE CHAIN, ONE WRITER — audit.Name states the rule. The holder opens the chain
	// under its own name, so admin keeps writing the chain it always wrote and every
	// record already in it stays where /v1/admin/audit reads. The chains other
	// processes wrote before they appended here are history on disk, and
	// Recorder.Trail still verifies each one.
	rec, err := audit.Open(cfg.DataDir, audit.Name(proc), mirror)
	if err != nil {
		return nil, fmt.Errorf("open audit store: %w", err)
	}
	rec.Acting(cloud.ActOf)

	// ONE CHAIN FOR ALL REPLICAS on hanzo-sql (CLOUD_SQL_DSN set): every replica's
	// holder appends to the same chain, each commit reading the head under the chain's
	// write lock, so replicas share one head and there is nothing to fork. The shard id
	// below names a SQLite partition and means nothing for the one chain.
	//
	// PER-SHARD audit under horizontal scale on SQLite. The chains live on THIS pod's
	// own RWO PVC, so under shard routing each pod's chain covers ONLY the tenants
	// routed to it (its shard) — and org-scoped audit queries route to the owning
	// shard where those records live. Soundness: the chain is a per-FILE hash chain
	// whose head is read from the file at every commit; because no two pods share the
	// file, there is no cross-pod head to fork (the very failure that pinned cloud to
	// replicas:1 was two pods on ONE audit file). Integrity is preserved WITHIN each
	// partition; a deployment-wide view is the union of the N per-shard chains. The
	// shard id is stamped on the AU-9 checkpoint stream below so the external
	// tail-truncation monitor tracks N heads (one per shard) rather than expecting a
	// single global head.
	shard := strings.TrimSpace(cfg.ShardSelf) // "" when single-pod — a harmless empty tag

	// AU-9 tail-truncation anchor: emit a periodic head-digest checkpoint to the
	// append-only observability log (and, when a mirror supports it, an
	// independent digest store). An external o11y monitor compares consecutive
	// checkpoints and alerts on a count regression — the only way to detect that
	// the most-recent records were deleted (an internal chain walk cannot). The
	// interval is CLOUD_AUDIT_CHECKPOINT_INTERVAL (default 5m; 0 disables).
	interval := auditCheckpointInterval()
	if logger != nil {
		rec.StartCheckpoints(interval, func(cp audit.Checkpoint) {
			logger.Info("audit_head_checkpoint",
				"shard", shard, "count", cp.Count, "head", cp.Head, "ts", cp.Time.Format(time.RFC3339Nano))
		})
	} else {
		rec.StartCheckpoints(interval, nil)
	}

	// The chains move to hanzo-sql in the background, the live one while it is
	// appended to (audit/move.go); with no CLOUD_SQL_DSN, or no CLOUD_SQL_MOVE, this
	// does nothing.
	rec.StartMove(logger)

	if logger != nil {
		count, head := rec.Head()
		logger.Info("audit trail ready (tamper-evident, append-only)",
			"store", audit.Name(proc), "shard", shard, "records", count, "head", head,
			"mirror", mirror != nil, "checkpoint_interval", interval.String())
	}
	return rec, nil
}

// auditCheckpointInterval resolves the head-digest checkpoint cadence.
// CLOUD_AUDIT_CHECKPOINT_INTERVAL is a Go duration (e.g. "5m", "1h"); default 5m;
// "0" disables periodic checkpoints (the on-close checkpoint still fires).
func auditCheckpointInterval() time.Duration {
	if v := env.Or("CLOUD_AUDIT_CHECKPOINT_INTERVAL", ""); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return 5 * time.Minute
}

// envTrue reports whether an env var is set to a truthy value (true/1/yes,
// case-insensitive).
func envTrue(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "true", "1", "yes":
		return true
	}
	return false
}
