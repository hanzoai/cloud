// Package hareplica gives the unified cloud binary zero-downtime SQLite HA on
// the Hanzo-native stack (hanzoai/sqlite + hanzoai/replicate + SeaweedFS S3),
// NO LiteFS / Litestream / Postgres. It is the in-process complement to the
// operator's HA TOPOLOGY (StatefulSet + per-pod PVC + headless + primary-only
// Service): the operator expresses the shape, this package owns the mechanism.
//
// Model (data-safe, empirically validated — see hack/sqlite-ha-proto + the
// follow-visibility probe): a long-lived *sql.DB reader (cloud's subsystems all
// hold SetMaxOpenConns(1) WAL handles) does NOT observe replicate.Restore(Follow)
// updates, so continuous follow cannot refresh cloud's read handles. Therefore
// role is elected ONCE at boot and FIXED for the pod's lifetime — no
// hot-promotion, which is the very thing that would let a standby's stale
// handles write on top of stale data and diverge from S3. Role changes happen
// only through a fresh pod boot.
//
//	Boot (before MountAll opens handles): RESTORE every {DataDir}/<db> from S3
//	  (S3 is the source of truth for the elected-primary lineage). Never ship
//	  local-before-election — a stale standby shipping would regress S3.
//	Elect (s3.Leaser conditional-write CAS): winner = PRIMARY (self-labels
//	  hanzo.ai/role=primary, renews the lease, ships every DB's WAL to S3 on an
//	  interval); loser = STANDBY (serves reads from its boot snapshot, forwards
//	  mutating /v1 requests to the primary-only Service).
//	Readiness gated on caught-up: a pod is Ready only after RestoreAll + Start,
//	  so a booting/rejoining pod never serves until it holds a caught-up copy.
//	Drain (preStop, primary): final Sync every DB, release the lease, drop the
//	  role label — an orderly handoff with zero data lag.
//
// Guarantees on a planned StatefulSet rolling deploy: ZERO read downtime (>=1
// pod is always Ready in the read Service serving GET) and write-survives-
// handoff (the next primary is a freshly-booted pod that restored the drained
// primary's final ship). Honest tradeoffs: standby reads are boot-snapshot-
// stale; one brief write-blip window per roll (the primary's own replacement);
// an unplanned primary crash loses only the last un-shipped LTX (bounded lag =
// ShipInterval) and stalls writes until that pod restarts in place.
package hareplica

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	replicate "github.com/hanzoai/replicate"
	reps3 "github.com/hanzoai/replicate/s3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	luxlog "github.com/luxfi/log"
)

// RoleLabelKey / RoleLabelPrimary must match the operator's HA primary-only
// Service selector (manifests::LABEL_ROLE / ROLE_PRIMARY).
const (
	RoleLabelKey     = "hanzo.ai/role"
	RoleLabelPrimary = "primary"
	forwardedHeader  = "X-HA-Forwarded"
)

// globalDBs is the canonical set of global subsystem SQLite files under
// DataDir. Enumerated (not globbed) so a fresh standby restores S3-only files
// it has never seen locally. Files whose subsystem is disabled simply don't
// exist post-MountAll and are skipped by the shipper.
var globalDBs = []string{
	"tracker.db", "agents.db", "projects.db", "provisioning.db", "crm.db",
	"prompts.db", "functions.db", "automations.db", "evals.db", "git.db",
	"security.db", "framework.db", "platform.db", "integrations.db",
	"catalog.db", "audit.db",
}

// Config is the in-process HA configuration, read from env by ConfigFromEnv.
type Config struct {
	Enabled bool

	DataDir string // cloud data root (e.g. /var/lib/cloud)

	// SeaweedFS S3 target. One bucket; each DB ships to <S3Prefix>/<db>, the
	// lease lives at <S3Prefix>/lock.json.
	S3Endpoint     string
	S3Bucket       string
	S3Prefix       string
	S3Region       string
	S3AccessKey    string
	S3SecretKey    string
	ForcePathStyle bool

	// PrimaryURL is the primary-only Service base URL (scheme+host+port) a
	// standby forwards mutating /v1 requests to (e.g.
	// http://cloud-ha-primary.hanzo.svc:8000).
	PrimaryURL string

	// Pod identity (Downward API) for self-labeling the lease holder.
	PodName      string
	PodNamespace string

	LeaseTTL      time.Duration
	RenewInterval time.Duration
	ShipInterval  time.Duration
}

// ConfigFromEnv builds HA config from env. dataDir comes from the resolved
// cloud Deps so the two never drift.
func ConfigFromEnv(dataDir string, getenv func(string, string) string) Config {
	boolOf := func(k string) bool {
		v := strings.ToLower(getenv(k, ""))
		return v == "1" || v == "true" || v == "yes"
	}
	durOf := func(k string, d time.Duration) time.Duration {
		if v := getenv(k, ""); v != "" {
			if p, err := time.ParseDuration(v); err == nil {
				return p
			}
		}
		return d
	}
	forcePath := true
	if v := strings.ToLower(getenv("HA_S3_FORCE_PATH_STYLE", "")); v == "0" || v == "false" || v == "no" {
		forcePath = false
	}
	return Config{
		Enabled:        boolOf("HA_ENABLED"),
		DataDir:        dataDir,
		S3Endpoint:     getenv("HA_S3_ENDPOINT", "http://s3.hanzo.svc:9000"),
		S3Bucket:       getenv("HA_S3_BUCKET", "cloud-ha"),
		S3Prefix:       getenv("HA_S3_PREFIX", "cloud"),
		S3Region:       getenv("HA_S3_REGION", "us-east-1"),
		S3AccessKey:    getenv("HA_S3_ACCESS_KEY", ""),
		S3SecretKey:    getenv("HA_S3_SECRET_KEY", ""),
		ForcePathStyle: forcePath,
		PrimaryURL:     strings.TrimRight(getenv("HA_PRIMARY_URL", ""), "/"),
		PodName:        getenv("POD_NAME", ""),
		PodNamespace:   getenv("POD_NAMESPACE", ""),
		LeaseTTL:       durOf("HA_LEASE_TTL", 30*time.Second),
		RenewInterval:  durOf("HA_RENEW_INTERVAL", 10*time.Second),
		ShipInterval:   durOf("HA_SHIP_INTERVAL", time.Second),
	}
}

type managedDB struct {
	name string
	rdb  *replicate.DB
}

// Manager owns lease election, WAL shipping, boot restore, and write routing.
type Manager struct {
	cfg    Config
	log    luxlog.Logger
	leaser *reps3.Leaser
	http   *http.Client

	mu    sync.Mutex
	lease *replicate.Lease
	dbs   []managedDB

	primary atomic.Bool
	ready   atomic.Bool

	k8s     kubernetes.Interface
	cancel  context.CancelFunc
	shipped sync.WaitGroup
}

// New constructs a Manager. It does not touch S3 or elect until RestoreAll /
// Start are called from the boot sequence.
func New(cfg Config, log luxlog.Logger) *Manager {
	return &Manager{
		cfg:  cfg,
		log:  log,
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

// Enabled reports whether HA is active for this deployment.
func (m *Manager) Enabled() bool { return m != nil && m.cfg.Enabled }

// IsPrimary reports whether this pod currently holds the single-primary lease.
func (m *Manager) IsPrimary() bool { return m != nil && m.primary.Load() }

// Ready reports whether this pod has finished boot-restore + election and may
// serve traffic. Gates the /readyz probe so a not-caught-up pod stays out of
// the Service endpoints.
func (m *Manager) Ready() bool { return m != nil && m.ready.Load() }

// replicaClient builds a per-DB S3 replica client (ships/restores <db> to
// <bucket>/<prefix>/<db>).
func (m *Manager) replicaClient(name string) *reps3.ReplicaClient {
	rc := reps3.NewReplicaClient()
	rc.Endpoint = m.cfg.S3Endpoint
	rc.Bucket = m.cfg.S3Bucket
	rc.Path = m.cfg.S3Prefix + "/" + name
	rc.Region = m.cfg.S3Region
	rc.AccessKeyID = m.cfg.S3AccessKey
	rc.SecretAccessKey = m.cfg.S3SecretKey
	rc.ForcePathStyle = m.cfg.ForcePathStyle
	return rc
}

// newLeaser builds the single-primary lease over S3, sharing the S3 config with
// the replica clients. The aws client is hand-built because reps3 exposes no
// getter for its own; *awss3.Client satisfies reps3.S3API (Get/Put/DeleteObject).
func (m *Manager) newLeaser(ctx context.Context) (*reps3.Leaser, error) {
	acfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(m.cfg.S3Region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(m.cfg.S3AccessKey, m.cfg.S3SecretKey, ""),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}
	s3c := awss3.NewFromConfig(acfg, func(o *awss3.Options) {
		o.UsePathStyle = m.cfg.ForcePathStyle
		if m.cfg.S3Endpoint != "" {
			o.BaseEndpoint = aws.String(m.cfg.S3Endpoint)
		}
	})
	l := reps3.NewLeaser()
	l.Bucket = m.cfg.S3Bucket
	l.Path = m.cfg.S3Prefix
	l.TTL = m.cfg.LeaseTTL
	l.MaxTTL = m.cfg.LeaseTTL
	if m.cfg.PodName != "" {
		l.Owner = m.cfg.PodName
	}
	l.SetClient(s3c)
	return l, nil
}

// RestoreAll pulls the latest committed state of every global DB from S3 into
// DataDir BEFORE the subsystems open their handles, so this pod boots caught
// up. A DB with no S3 snapshot yet is left untouched (the subsystem creates it
// fresh and the primary's first ship seeds it). Uses temp-file + atomic rename
// so a local file is only overwritten once the authoritative S3 copy is in hand.
func (m *Manager) RestoreAll(ctx context.Context) error {
	if !m.Enabled() {
		return nil
	}
	if err := os.MkdirAll(m.cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("data dir: %w", err)
	}
	restored := 0
	for _, name := range globalDBs {
		if err := m.restoreOne(ctx, name); err != nil {
			// Both sentinels mean "no restorable backup exists yet" (empty
			// prefix / no complete snapshot chain) — keep the local file and let
			// the subsystem create it fresh; the primary's first ship seeds S3.
			if errors.Is(err, replicate.ErrNoSnapshots) || errors.Is(err, replicate.ErrTxNotAvailable) {
				continue
			}
			return fmt.Errorf("restore %s: %w", name, err)
		}
		restored++
		m.log.Info("boot-restored db from S3", "db", name)
	}
	m.log.Info("HA boot restore complete", "restored", restored, "candidates", len(globalDBs))
	return nil
}

func (m *Manager) restoreOne(ctx context.Context, name string) error {
	dst := filepath.Join(m.cfg.DataDir, name)
	tmp := dst + ".ha-restore"
	_ = os.Remove(tmp)
	_ = os.Remove(tmp + "-txid")

	r := replicate.NewReplicaWithClient(replicate.NewDB(tmp), m.replicaClient(name))
	opt := replicate.NewRestoreOptions()
	opt.OutputPath = tmp
	opt.IntegrityCheck = replicate.IntegrityCheckFull
	if err := r.Restore(ctx, opt); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// Atomic swap: readers never observe a half-written DB.
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("swap restored db: %w", err)
	}
	// Move the WAL/SHM aside — the restored file is a checkpointed snapshot; a
	// stale sidecar WAL from a previous life must not shadow it.
	_ = os.Remove(dst + "-wal")
	_ = os.Remove(dst + "-shm")
	return nil
}

// Start runs AFTER MountAll (all subsystem DBs exist). It elects via the lease:
// on win this pod becomes PRIMARY (self-labels, renews, ships); on loss it
// stays STANDBY for its lifetime. Either way the pod becomes Ready. A hard S3
// error fails Start so the pod is NOT marked Ready (fail-closed).
func (m *Manager) Start(ctx context.Context) error {
	if !m.Enabled() {
		m.ready.Store(true) // non-HA: readiness is unconditional (byte-identical)
		return nil
	}
	loopCtx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	leaser, err := m.newLeaser(ctx)
	if err != nil {
		return err
	}
	m.leaser = leaser

	if k8s, err := inClusterK8s(); err != nil {
		m.log.Warn("no in-cluster k8s client; primary self-labeling disabled", "err", err)
	} else {
		m.k8s = k8s
	}

	lease, err := leaser.AcquireLease(ctx)
	switch {
	case err == nil:
		m.mu.Lock()
		m.lease = lease
		m.mu.Unlock()
		m.primary.Store(true)
		m.log.Info("acquired primary lease", "generation", lease.Generation, "owner", lease.Owner)
		if err := m.setRoleLabel(ctx, true); err != nil {
			m.log.Warn("primary self-label failed (primary Service may not route)", "err", err)
		}
		if err := m.openShippers(); err != nil {
			return fmt.Errorf("open shippers: %w", err)
		}
		m.shipped.Add(2)
		go m.renewLoop(loopCtx)
		go m.shipLoop(loopCtx)
	case isLeaseHeld(err):
		m.primary.Store(false)
		m.log.Info("standby — primary lease held elsewhere", "detail", err.Error())
	default:
		return fmt.Errorf("acquire lease: %w", err) // hard S3 error → not Ready
	}

	m.ready.Store(true)
	return nil
}

// openShippers opens a replicate.DB tailer per existing global DB. It runs
// alongside the subsystem's own writer connection (WAL allows the dual open —
// proven by hack/sqlite-ha-proto). MonitorInterval=0: shipping is driven
// deterministically by shipLoop, mirroring the proven prototype.
func (m *Manager) openShippers() error {
	for _, name := range globalDBs {
		path := filepath.Join(m.cfg.DataDir, name)
		if _, err := os.Stat(path); err != nil {
			continue // subsystem disabled / DB not created — nothing to ship
		}
		rdb := replicate.NewDB(path)
		rdb.MonitorInterval = 0
		rdb.Replica = replicate.NewReplicaWithClient(rdb, m.replicaClient(name))
		rdb.Replica.SyncInterval = m.cfg.ShipInterval
		if err := rdb.Open(); err != nil {
			return fmt.Errorf("open replicate db %s: %w", name, err)
		}
		m.dbs = append(m.dbs, managedDB{name: name, rdb: rdb})
	}
	m.log.Info("opened WAL shippers", "count", len(m.dbs))
	return nil
}

// shipLoop continuously ships each DB's WAL to S3 (DB→shadow via db.Sync, then
// shadow→backend LTX via Replica.Sync) so the bounded crash-lag stays ≤ one
// ShipInterval.
func (m *Manager) shipLoop(ctx context.Context) {
	defer m.shipped.Done()
	t := time.NewTicker(m.cfg.ShipInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.shipOnce(ctx)
		}
	}
}

func (m *Manager) shipOnce(ctx context.Context) {
	for _, md := range m.dbs {
		if err := md.rdb.Sync(ctx); err != nil {
			if ctx.Err() == nil {
				m.log.Warn("db sync failed", "db", md.name, "err", err)
			}
			continue
		}
		if err := md.rdb.Replica.Sync(ctx); err != nil && ctx.Err() == nil {
			m.log.Warn("replica sync failed", "db", md.name, "err", err)
		}
	}
}

// renewLoop keeps the lease alive. On a definitive loss to ANOTHER owner it
// fails secure — the process exits so it can never be a second writer; k8s
// restarts it clean. A transient renew error is retried; an expired-but-
// unclaimed lease (our model: standbys never steal) is re-acquired.
func (m *Manager) renewLoop(ctx context.Context) {
	defer m.shipped.Done()
	t := time.NewTicker(m.cfg.RenewInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.mu.Lock()
			cur := m.lease
			m.mu.Unlock()
			if cur == nil {
				continue
			}
			next, err := m.leaser.RenewLease(ctx, cur)
			if err == nil {
				m.mu.Lock()
				m.lease = next
				m.mu.Unlock()
				continue
			}
			if !errors.Is(err, replicate.ErrLeaseNotHeld) {
				m.log.Warn("lease renew transient error; will retry", "err", err)
				continue
			}
			// Lost the lease. Try to re-acquire (in our model no standby steals
			// it, so an expiry is reclaimable).
			reacq, aerr := m.leaser.AcquireLease(ctx)
			if aerr == nil {
				m.mu.Lock()
				m.lease = reacq
				m.mu.Unlock()
				m.log.Warn("lease expired then re-acquired", "generation", reacq.Generation)
				continue
			}
			if isLeaseHeld(aerr) {
				// Another owner exists — impossible in the boot-election model,
				// but if it happens we MUST NOT be a second writer. Fail secure.
				m.log.Error("lease taken by another owner; demoting via exit to avoid split-brain", "err", aerr)
				m.primary.Store(false)
				_ = m.setRoleLabel(context.Background(), false)
				os.Exit(1)
			}
			m.log.Warn("lease re-acquire transient error; will retry", "err", aerr)
		}
	}
}

// Drain is the orderly preStop handoff for the primary: final Sync every DB,
// release the lease, drop the role label. Safe (near no-op) on a standby.
func (m *Manager) Drain(ctx context.Context) error {
	if !m.Enabled() {
		return nil
	}
	// Stop the background loops first so they don't race the final ship.
	if m.cancel != nil {
		m.cancel()
	}
	if !m.primary.Load() {
		m.log.Info("drain on standby — nothing to hand off")
		return nil
	}
	m.log.Info("draining primary: final ship + lease release")
	// Final ship: db.Sync + Replica.Sync, then Close (which also flushes).
	var firstErr error
	for _, md := range m.dbs {
		if err := md.rdb.Sync(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
		if err := md.rdb.Replica.Sync(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
		if err := md.rdb.Close(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	_ = m.setRoleLabel(ctx, false)
	m.primary.Store(false)
	m.mu.Lock()
	lease := m.lease
	m.lease = nil
	m.mu.Unlock()
	if lease != nil {
		if err := m.leaser.ReleaseLease(ctx, lease); err != nil &&
			!errors.Is(err, reps3.ErrLeaseAlreadyReleased) && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		m.log.Warn("drain completed with errors", "err", firstErr)
	} else {
		m.log.Info("drain complete — lease released, handoff ready")
	}
	return firstErr
}

// setRoleLabel patches (or clears) hanzo.ai/role=primary on this pod so the
// operator's primary-only Service selects exactly the lease holder. No-op when
// not running in-cluster (local/dev).
func (m *Manager) setRoleLabel(ctx context.Context, primary bool) error {
	if m.k8s == nil || m.cfg.PodName == "" || m.cfg.PodNamespace == "" {
		return nil
	}
	var value any = RoleLabelPrimary
	if !primary {
		value = nil // JSON null removes the label key
	}
	patch := fmt.Sprintf(`{"metadata":{"labels":{%q:%s}}}`,
		RoleLabelKey, jsonScalar(value))
	_, err := m.k8s.CoreV1().Pods(m.cfg.PodNamespace).Patch(
		ctx, m.cfg.PodName, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}

func jsonScalar(v any) string {
	if v == nil {
		return "null"
	}
	return fmt.Sprintf("%q", v)
}

func inClusterK8s() (kubernetes.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(cfg)
}

func isLeaseHeld(err error) bool {
	var held *replicate.LeaseExistsError
	return errors.As(err, &held)
}

// ---- write routing (standby → primary) --------------------------------------

// ForwardedHeader marks a request already forwarded once, so a mis-labeled
// standby never re-forwards into a loop (it fails closed instead).
const ForwardedHeader = forwardedHeader

// IsReadMethod reports whether the HTTP method is a non-mutating read that a
// standby may serve locally.
func IsReadMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

// hopByHop headers are connection-scoped and must not be proxied.
var hopByHop = map[string]struct{}{
	"Connection": {}, "Keep-Alive": {}, "Proxy-Authenticate": {},
	"Proxy-Authorization": {}, "Te": {}, "Trailer": {},
	"Transfer-Encoding": {}, "Upgrade": {},
}

// ForwardResult is the primary's response to a forwarded write, copied back to
// the original caller verbatim.
type ForwardResult struct {
	Status int
	Header http.Header
	Body   []byte
}

// ForwardWrite proxies a mutating request to the primary-only Service and
// returns its response. Framework-agnostic (the zip glue lives in the cloud
// package). Runs BEFORE identity/audit/billing so the PRIMARY performs those
// exactly once — no double-audit / double-bill. copyReqHeaders yields the
// original request headers (hop-by-hop are dropped here).
func (m *Manager) ForwardWrite(
	ctx context.Context,
	method, originalURL string,
	body []byte,
	copyReqHeaders func(add func(k, v string)),
) *ForwardResult {
	if m.cfg.PrimaryURL == "" {
		return &ForwardResult{Status: http.StatusServiceUnavailable,
			Body: []byte(`{"error":"no primary route configured"}`)}
	}
	req, err := http.NewRequestWithContext(ctx, method, m.cfg.PrimaryURL+originalURL, bytes.NewReader(body))
	if err != nil {
		return &ForwardResult{Status: http.StatusBadGateway, Body: []byte(`{"error":"forward build failed"}`)}
	}
	copyReqHeaders(func(k, v string) {
		if _, hop := hopByHop[http.CanonicalHeaderKey(k)]; hop {
			return
		}
		req.Header.Add(k, v)
	})
	req.Header.Set(forwardedHeader, "1")

	resp, err := m.http.Do(req)
	if err != nil {
		m.log.Warn("write forward to primary failed", "err", err)
		return &ForwardResult{Status: http.StatusServiceUnavailable, Body: []byte(`{"error":"primary unreachable"}`)}
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	hdr := make(http.Header, len(resp.Header))
	for k, vv := range resp.Header {
		if _, hop := hopByHop[http.CanonicalHeaderKey(k)]; hop {
			continue
		}
		for _, v := range vv {
			hdr.Add(k, v)
		}
	}
	return &ForwardResult{Status: resp.StatusCode, Header: hdr, Body: out}
}

// LoopbackOnly reports whether an ops-port request originates from loopback —
// the /internal/drain control endpoint is preStop-only (least privilege).
func LoopbackOnly(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
