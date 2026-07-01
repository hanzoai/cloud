package cloud

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
	"path/filepath"
	"time"

	"github.com/hanzoai/cloud/audit"
	luxlog "github.com/luxfi/log"
)

// buildAuditRecorder constructs the audit Recorder from cfg: the append-only
// SQLite chain at {DataDir}/audit.db plus a best-effort ClickHouse OLAP mirror
// when a datastore is configured. Returns (nil, nil) only when the trail is
// explicitly disabled — the caller then wires a no-op middleware.
func buildAuditRecorder(cfg *Config, logger luxlog.Logger) (*audit.Recorder, error) {
	if getenvBool("CLOUD_AUDIT_DISABLED") {
		if logger != nil {
			logger.Warn("audit trail DISABLED by CLOUD_AUDIT_DISABLED — no tamper-evident record will be kept")
		}
		return nil, nil
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
	if m, err := newAuditMirror(logger); err != nil {
		if logger != nil {
			logger.Warn("audit OLAP mirror unavailable — running local-only (chain integrity unaffected)", "err", err)
		}
	} else {
		mirror = m
	}

	dbPath := filepath.Join(cfg.DataDir, "audit.db")
	rec, err := audit.Open(dbPath, mirror)
	if err != nil {
		return nil, fmt.Errorf("open audit store: %w", err)
	}

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
				"count", cp.Count, "head", cp.Head, "ts", cp.Time.Format(time.RFC3339Nano))
		})
	} else {
		rec.StartCheckpoints(interval, nil)
	}

	if logger != nil {
		count, head := rec.Head()
		logger.Info("audit trail ready (tamper-evident, append-only)",
			"store", dbPath, "records", count, "head", head,
			"mirror", mirror != nil, "checkpoint_interval", interval.String())
	}
	return rec, nil
}

// auditCheckpointInterval resolves the head-digest checkpoint cadence.
// CLOUD_AUDIT_CHECKPOINT_INTERVAL is a Go duration (e.g. "5m", "1h"); default 5m;
// "0" disables periodic checkpoints (the on-close checkpoint still fires).
func auditCheckpointInterval() time.Duration {
	if v := getenv("CLOUD_AUDIT_CHECKPOINT_INTERVAL", ""); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return 5 * time.Minute
}
