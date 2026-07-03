package provisioning

// Dedicated-instance strategy — TRUE multitenancy by isolation-by-instance.
//
// datastore (ClickHouse) and docdb (FerretDB) are NOT provisioned as a logical
// resource inside a shared backend. A shared ClickHouse/FerretDB cannot scope a
// per-tenant role (that is why both were honest-gated: unavailableKinds). Here
// each create() launches the org's OWN dedicated instance — a StatefulSet +
// Service + PVC materialized by the Hanzo operator from a `Datastore` CR the
// control plane writes into the org's namespace (tenant-<org>). Because the org
// OWNS the whole instance, its admin credential is naturally tenant-scoped: a
// cross-tenant grant is impossible, there being exactly one tenant on the
// instance. That is what un-gates the two kinds.
//
// Two invariants bind every instance:
//   - ORG OWNERSHIP / ISOLATION — the instance lands in tenant-<org>, a
//     namespace DERIVED from the VALIDATED org (never a request field), and is
//     labeled hanzo.ai/org=<org> + hanzo.ai/resource=<id>. The namespace is the
//     cross-tenant boundary; a caller cannot name another org's namespace.
//   - BILLING ATTRIBUTION — a running instance consumes real compute/storage, so
//     it is metered to the org's ledger through the ONE commerce meter
//     (cloud.ResourceMeter): a provision debit carrying the size dimension at
//     create, and a recurring footprint charge for as long as it runs. Drop
//     stops the meter (the row is removed).
//
// The five shared kinds (sql/vector/kv/s3/search) keep the shared-logical
// strategy in provisioner.go — this file is purely additive.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/commerce/metering"
	"github.com/zap-proto/zip"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Resource statuses + the operator's readiness phase. A dedicated resource is
// "provisioning" until the operator reports the instance StatefulSet Running,
// then "ready". Shared-logical resources are "ready" immediately.
const (
	statusProvisioning = "provisioning"
	statusReady        = "ready"
	phaseRunning       = "Running" // crd::Phase::Running (serde default variant name)
)

// dedicatedAdminUser is the sole (therefore admin) user each instance boots
// with. It is safe to reuse the name across instances: the credential is unique
// per instance and the instance is isolated, so "admin" leaks nothing.
const dedicatedAdminUser = "admin"

// Billing + sizing policy knobs (clearly-named, env-overridable — never a
// fabricated price). defaultStoragePriceCents mirrors hanzoai/pricing
// infrastructure.blockStorage.pricePerGBMonthly ($0.08/GB-month).
const (
	dedicatedSizeEnvPrefix   = "CLOUD_DEDICATED_SIZE" // per-kind CLOUD_DEDICATED_SIZE_DATASTORE
	defaultDedicatedSize     = "10Gi"                 // one instance's default storage footprint
	storagePriceEnv          = "CLOUD_STORAGE_PRICE_CENTS_PER_GB_MONTH"
	defaultStoragePriceCents = 8              // $0.08/GB-month
	dedicatedMeterInterval   = 24 * time.Hour // one GB-day charge per tick
)

// Tenant-RBAC readiness wait bounds — a brand-new tenant namespace's RoleBinding
// (which grants cloud-api create on datastores + secrets) is projected
// asynchronously by the operator when the namespace appears; a first provision
// waits for it rather than 403ing.
const (
	tenantRBACReadyTimeout = 45 * time.Second
	tenantRBACPollInitial  = 250 * time.Millisecond
	tenantRBACPollMax      = 2 * time.Second
)

// errTenantProvisioning — the operator has not yet granted cloud-api access in a
// freshly-created tenant namespace. Retryable (mapped to HTTP 503), never a
// fabricated success.
var errTenantProvisioning = errors.New("provisioning: tenant RBAC still provisioning")

// enginePort is one container port exposed on the instance Service.
type enginePort struct {
	name string
	num  int
}

// engine describes how one dedicated kind materializes: the container image the
// operator runs, the ports it exposes, the client port + DSN scheme a customer
// connects with, and the Secret env the image reads to provision its sole
// (admin) user. Adding a dedicated kind = one more entry here. Nothing else in
// the control plane special-cases a kind.
type engine struct {
	prefix     string // instance-name prefix (keeps a datastore and a docdb of one (org,name) distinct)
	dsType     string // Datastore CR spec.type (selects the operator's engine branch)
	image      string // container image repository
	tag        string // container image tag (pinned; never :latest)
	ports      []enginePort
	clientPort int // the port a customer connects on (the DSN port)
	// secretEnv builds the admin Secret data; each key is an env var the image
	// reads (referenced by the CR via one envFrom secretRef), so the instance
	// boots with THIS admin credential — never a plaintext in the CR/commit.
	secretEnv func(user, pw, db string) map[string]string
	// dsn builds the customer connection string.
	dsn func(user, pw, host string, port int, db string) string
	// pod memory floor/ceiling — a database needs a real memory reservation.
	memReq string
	memLim string
	// dataMount, when non-empty, is the container path the instance's "data" PVC
	// mounts at. The operator only mounts the PVC when the CR names a volumeMount
	// (build_statefulset does not auto-mount), so an engine that must persist to
	// disk MUST set this — else the pod writes to ephemeral container storage.
	dataMount string
	// iamAuth marks an IAM-native engine (Hanzo Base): callers authenticate via
	// Hanzo IAM, so there is NO per-resource admin password. The admin Secret
	// carries IAM/KMS config instead, and no password is generated, sealed, or
	// returned. secretEnv/dsn ignore the user/pw args for such engines.
	iamAuth bool
}

// dedicatedEngines is the closed set of kinds provisioned as a dedicated
// instance. Its membership is exactly the former unavailableKinds set, now
// un-gated because isolation is by instance.
var dedicatedEngines = map[string]engine{
	// ClickHouse ("datastore"): ghcr.io/hanzoai/datastore:26 provisions
	// DATASTORE_USER (with DATASTORE_PASSWORD) on DATASTORE_DB at entrypoint and
	// drops the built-in default user, so the provisioned user is the instance
	// admin — the exact env the shared datastore StatefulSet uses.
	"datastore": {
		prefix: "ds", dsType: "datastore",
		image: "ghcr.io/hanzoai/datastore", tag: "26",
		ports:      []enginePort{{"http", 8123}, {"native", 9000}},
		clientPort: 8123,
		memReq:     "256Mi", memLim: "1Gi",
		secretEnv: func(user, pw, db string) map[string]string {
			return map[string]string{
				"DATASTORE_DB":       db,
				"DATASTORE_USER":     user,
				"DATASTORE_PASSWORD": pw,
			}
		},
		dsn: func(user, pw, host string, port int, db string) string {
			return fmt.Sprintf("clickhouse://%s:%s@%s:%d/%s?protocol=http", user, pw, host, port, db)
		},
	},
	// Hanzo Base ("docdb"): the managed "document database" is a dedicated
	// per-org Hanzo Base instance — JSON document collections on per-tenant
	// SQLite with native realtime (SSE at /v1/realtime), on :8090 with data at
	// /data. IAM-native: the base image's platform plugin validates the org's
	// Hanzo IAM tokens (IAM_URL/KMS_URL/IAM_CLIENT_* via the admin Secret), so
	// there is NO per-resource password. ZERO MongoDB / Mongo-wire / FerretDB —
	// the per-org docdb instance IS a Base instance now. The engine runs on the
	// generic Datastore controller (spec.type is free-form; image/ports/
	// volumeMounts drive the StatefulSet verbatim — no operator branch needed).
	"docdb": {
		prefix: "ddb", dsType: "base",
		image: "ghcr.io/hanzoai/base", tag: env("CLOUD_DEDICATED_DOCDB_TAG", "v1.5.3"),
		ports:      []enginePort{{"http", 8090}},
		clientPort: 8090,
		dataMount:  "/data",
		iamAuth:    true,
		memReq:     "128Mi", memLim: "512Mi",
		secretEnv: func(_, _, _ string) map[string]string {
			return baseInstanceEnv()
		},
		dsn: func(_, _, host string, port int, _ string) string {
			return fmt.Sprintf("http://%s:%d/v1", host, port)
		},
	},
}

// baseInstanceEnv is the boot env for a dedicated Hanzo Base instance: the IAM +
// KMS coordinates the base image's platform plugin reads to validate the org's
// Hanzo IAM tokens (and derive the per-tenant encryption key). Sourced from the
// cloud binary's OWN configured IAM identity; only keys that are set are
// projected. There is NO per-resource password — Base is IAM-native.
func baseInstanceEnv() map[string]string {
	m := map[string]string{}
	for _, k := range []string{"IAM_URL", "KMS_URL", "IAM_CLIENT_ID", "IAM_CLIENT_SECRET"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			m[k] = v
		}
	}
	return m
}

// tenantNamespace is the org's physical namespace — the cross-tenant isolation
// boundary. org MUST already be the sanitized slug (tenant() returns
// sanitizeOrg(c.Org())), so this only prepends the prefix; re-sanitizing an
// already-suffixed slug would double-suffix it. Matches platform's
// tenant-<org> convention exactly, so a DB instance and the org's apps share
// one namespace.
func tenantNamespace(org string) string {
	if org == "" {
		org = "unknown"
	}
	return "tenant-" + org
}

// instanceName is the dedicated instance's deterministic, DNS-1123 name:
// <prefix>-<orgHash10>-<name>. The fixed-width org hash keeps the org→name
// boundary unambiguous (two orgs never fold onto one instance); the kind prefix
// keeps a datastore and a docdb of the same (org,name) distinct. Bounded well
// inside 63 chars even after the StatefulSet pod (-0) and PVC (data-…-0)
// suffixes (name ≤ 40 by nameRE).
func instanceName(kind, org, name string) string {
	return dedicatedEngines[kind].prefix + "-" + orgHash(org)[:10] + "-" + name
}

// dedicatedSize is the declared storage footprint for a new instance: a per-kind
// override (CLOUD_DEDICATED_SIZE_DATASTORE), else the global CLOUD_DEDICATED_SIZE,
// else 10Gi.
func dedicatedSize(kind string) string {
	if v := os.Getenv(dedicatedSizeEnvPrefix + "_" + strings.ToUpper(kind)); v != "" {
		return v
	}
	return env(dedicatedSizeEnvPrefix, defaultDedicatedSize)
}

// createDedicated launches the org's dedicated instance and records it as
// "provisioning". Called by create() after the shared preamble (org resolved,
// name validated, billing gated, (org,kind,name) dedup checked).
func (s *svc) createDedicated(c *zip.Ctx, ctx context.Context, kind, org, name string, e engine, fee int64) error {
	if s.orch == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "dedicated provisioning unavailable: no cluster client")
	}
	if err := s.orch.Ready(); err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "dedicated provisioning unavailable: %v", err)
	}

	ns := tenantNamespace(org)
	inst := instanceName(kind, org, name)
	db := sanitizeIdent(name)

	// Global (cross-org) uniqueness guard on the instance identity — fail closed
	// BEFORE touching the cluster (mirrors the shared path's PhysicalExists).
	if exists, err := s.store.PhysicalExists(ctx, inst); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
	} else if exists {
		return zip.ErrConflict("resource already exists")
	}

	// Ensure the tenant namespace + wait for the operator to grant cloud-api
	// create access in it. Honest retryable 503 while the grant is still landing.
	if err := s.orch.EnsureTenant(ctx, ns, org); err != nil {
		if errors.Is(err, errTenantProvisioning) {
			return zip.Errorf(http.StatusServiceUnavailable, "tenant still provisioning, retry")
		}
		s.log.Error("ensure tenant failed", "kind", kind, "org", org, "ns", ns, "err", err)
		return zip.Errorf(http.StatusBadGateway, "ensure tenant: %v", err)
	}

	// Secretful engines get a per-instance admin password; IAM-native engines
	// (Base) do not — callers authenticate via Hanzo IAM, so nothing is sealed
	// or returned.
	pw := ""
	if !e.iamAuth {
		p, gerr := genToken(24)
		if gerr != nil {
			return zip.Errorf(http.StatusInternalServerError, "rng: %v", gerr)
		}
		pw = p
	}
	id, err := genID()
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	user := dedicatedAdminUser
	secretName := inst + "-admin"

	// Seal the instance admin credential in KMS (durable + audited). The password
	// is ALSO returned once in this response and projected as the namespace Secret
	// below — but NEVER stored in plaintext at rest here.
	secretRef := fmt.Sprintf("org/%s/%s/%s", org, kind, name)
	storedRef := ""
	if !e.iamAuth {
		if s.sec.Enabled() {
			if err := s.sec.Put(secretRef, []byte(pw)); err != nil {
				s.log.Error("kms put failed", "kind", kind, "err", err)
				return zip.Errorf(http.StatusInternalServerError, "store secret failed")
			}
			storedRef = secretRef
		} else {
			s.log.Warn("KMS degraded: instance admin password returned once, not persisted", "kind", kind, "org", org, "name", name)
		}
	}

	// IAM-native engines return no credential; the customer authenticates via
	// Hanzo IAM against the instance's own API.
	respUser, respPw := user, pw
	if e.iamAuth {
		respUser, respPw = "", ""
	}

	// Project the admin Secret (the env the image boots from) then apply the
	// Datastore CR (the operator materializes the StatefulSet + Service + PVC).
	secObj := adminSecretObj(ns, org, inst, kind, secretName, e.secretEnv(user, pw, db))
	if err := s.orch.ApplySecret(ctx, ns, secretName, secObj); err != nil {
		if storedRef != "" {
			_ = s.sec.Delete(storedRef)
		}
		s.log.Error("project admin secret failed", "kind", kind, "org", org, "err", err)
		return zip.Errorf(http.StatusBadGateway, "project admin secret: %v", err)
	}
	size := dedicatedSize(kind)
	crObj := datastoreCR(ns, org, inst, id, kind, e, size, os.Getenv("CLOUD_DEDICATED_STORAGE_CLASS"), secretName, env("CLOUD_DEDICATED_PULL_SECRET", "ghcr-pull"))
	if err := s.orch.ApplyDatastore(ctx, ns, inst, crObj); err != nil {
		_ = s.orch.DeleteSecret(ctx, ns, secretName)
		if storedRef != "" {
			_ = s.sec.Delete(storedRef)
		}
		s.log.Error("launch instance failed", "kind", kind, "org", org, "inst", inst, "err", err)
		return zip.Errorf(http.StatusBadGateway, "launch instance: %v", err)
	}

	host := fmt.Sprintf("%s.%s.svc", inst, ns)
	r := Resource{
		ID: id, Org: org, Kind: kind, Name: name,
		PhysicalName: inst, SecretRef: storedRef,
		Host: host, Port: e.clientPort, Username: respUser, DBName: db,
		Status: statusProvisioning, CreatedAt: time.Now().Unix(), Size: size,
	}
	if err := s.store.Insert(ctx, r); err != nil {
		// Undo the instance + secret we just created.
		_ = s.orch.DeleteDatastore(ctx, ns, inst)
		_ = s.orch.DeleteSecret(ctx, ns, secretName)
		if storedRef != "" {
			_ = s.sec.Delete(storedRef)
		}
		if errors.Is(err, errConflict) {
			return zip.ErrConflict("resource already exists")
		}
		return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}

	// The instance is launching + persisted — attribute the provision to the
	// caller's OWN org, carrying the size dimension so the invoice line names the
	// instance. The recurring footprint meter charges ongoing GB-time.
	s.meterProvision(org, kind, size, fee, c.RequestID(), cloud.ClientIP(c))

	return c.JSON(http.StatusCreated, createResp{
		ID: id, Kind: kind, Name: name, Status: statusProvisioning,
		Host: host, Port: e.clientPort, Username: respUser, Database: db,
		ConnectionString: e.dsn(user, pw, host, e.clientPort, db), Password: respPw,
	})
}

// reconcileDedicated advances a "provisioning" row to "ready" when the operator
// reports the instance StatefulSet Running. Honest: any read error leaves the
// row provisioning (never a fabricated ready). Called on get and by the meter
// sweep so an unpolled instance still becomes ready (and billable).
func (s *svc) reconcileDedicated(ctx context.Context, r Resource) Resource {
	if r.Status != statusProvisioning || s.orch == nil {
		return r
	}
	phase, err := s.orch.DatastorePhase(ctx, tenantNamespace(r.Org), r.PhysicalName)
	if err != nil {
		return r
	}
	if phase == phaseRunning {
		if _, uErr := s.store.UpdateStatus(ctx, r.Org, r.Kind, r.Name, statusReady); uErr == nil {
			r.Status = statusReady
		}
	}
	return r
}

// dropDedicated tears down the instance: delete the Datastore CR (the operator
// garbage-collects the StatefulSet + Service + PVC via owner refs) and its admin
// Secret. The KMS secret + metadata row are removed by the shared drop tail.
func (s *svc) dropDedicated(ctx context.Context, r Resource) error {
	if s.orch == nil {
		return nil
	}
	ns := tenantNamespace(r.Org)
	if err := s.orch.DeleteDatastore(ctx, ns, r.PhysicalName); err != nil {
		return err
	}
	// Reap the retained data volume (data-<instance>-0 for the single-replica
	// StatefulSet) so a dropped instance leaves no storage footprint, then the
	// admin Secret. Both best-effort after the CR delete — a missing object is
	// success (already gone).
	_ = s.orch.DeletePVC(ctx, ns, "data-"+r.PhysicalName+"-0")
	return s.orch.DeleteSecret(ctx, ns, r.PhysicalName+"-admin")
}

// ----- billing --------------------------------------------------------------

// meterProvision attributes a dedicated instance's provision to the caller's OWN
// org with a size dimension, via the ONE commerce meter (never a parallel path).
// No-op when billing is unconfigured or the kind is free (fee 0).
func (s *svc) meterProvision(org, kind, size string, fee int64, requestID, clientIP string) {
	s.bill.MeterUsage(org, kind, metering.Usage{
		AmountCents: fee,
		Model:       kind + ":" + size, // e.g. datastore:10Gi — names the instance on the invoice
		RequestID:   requestID,
		ClientIP:    clientIP,
	})
}

// meterDedicatedFootprint charges every RUNNING dedicated instance its ongoing
// storage footprint to the instance's OWN org — the recurring hook the shared
// provisioners reserved ("once a live-size source exists"), now unblocked
// because a dedicated instance has a declared size. One call == one charge
// period (dedicatedMeterInterval); the caller's ticker fires it. A dropped
// instance (row gone) is never charged — that is how delete stops the meter.
func (s *svc) meterDedicatedFootprint(ctx context.Context) {
	rows, err := s.store.ListAllByStatus(ctx, statusReady)
	if err != nil {
		s.log.Error("footprint meter: list failed", "err", err)
		return
	}
	for _, r := range rows {
		if _, dedicated := dedicatedEngines[r.Kind]; !dedicated {
			continue
		}
		cents := gbDayCents(sizeToGB(r.Size))
		if cents <= 0 {
			continue
		}
		s.bill.MeterUsage(r.Org, r.Kind, metering.Usage{
			AmountCents: cents,
			Model:       r.Kind + ":" + r.Size + ":gbday",
			RequestID:   r.ID,
		})
	}
}

// startFootprintMeter runs the recurring footprint charge on a ticker until
// Shutdown. It only starts when billing actually enforces, so an unconfigured
// deployment spins no goroutine.
func (s *svc) startFootprintMeter() {
	if s.bill == nil || !s.bill.Enabled() {
		return
	}
	stop := make(chan struct{})
	s.stopMeter = func() { close(stop) }
	go func() {
		t := time.NewTicker(dedicatedMeterInterval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				s.meterDedicatedFootprint(ctx)
				cancel()
			}
		}
	}()
}

// gbDayCents prices one GB-day of storage: ceil(gb * $/GB-month / 30), floored
// at 1 cent for any non-empty instance so a running instance is always billed.
func gbDayCents(gb int) int64 {
	if gb <= 0 {
		return 0
	}
	monthly := int64(gb) * int64(atoiEnv(storagePriceEnv, defaultStoragePriceCents))
	d := (monthly + 29) / 30
	if d < 1 {
		d = 1
	}
	return d
}

// sizeToGB parses a K8s quantity ("10Gi", "512Mi", "1Ti", "5G", raw bytes) to
// whole GB, rounded UP so a footprint is never undercharged. Unknown/empty -> 0.
func sizeToGB(size string) int {
	s := strings.TrimSpace(size)
	if s == "" {
		return 0
	}
	units := []struct {
		suffix string
		gb     float64
	}{
		{"Ti", 1024}, {"Gi", 1}, {"Mi", 1.0 / 1024}, {"Ki", 1.0 / (1024 * 1024)},
		{"T", 1000}, {"G", 1}, {"M", 1.0 / 1000}, {"K", 1.0 / 1e6},
	}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			f, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(s, u.suffix)), 64)
			if err != nil {
				return 0
			}
			return int(math.Ceil(f * u.gb))
		}
	}
	f, err := strconv.ParseFloat(s, 64) // raw bytes
	if err != nil {
		return 0
	}
	if gb := f / 1e9; gb < 1 && f > 0 {
		return 1
	} else {
		return int(math.Ceil(gb))
	}
}

// ----- K8s object builders --------------------------------------------------

// datastoreCR renders the operator hanzo.ai/v1 Datastore CR for a dedicated
// instance. It lives in tenant-<org>, is labeled with the org + resource id (the
// ownership/cost-attribution boundary), and pins image/ports/storage/resources
// explicitly. The admin credential is referenced by name only (envFrom
// secretRef) — never inlined. The `Datastore` Kind is used (not `DocDB`) because
// only its controller writes status.phase, the readiness signal.
func datastoreCR(ns, org, inst, resourceID, kind string, e engine, size, storageClass, secretName, pullSecret string) *unstructured.Unstructured {
	ports := make([]any, 0, len(e.ports))
	for _, p := range e.ports {
		ports = append(ports, map[string]any{"name": p.name, "containerPort": int64(p.num), "protocol": "TCP"})
	}
	storage := map[string]any{"size": size}
	if storageClass != "" {
		storage["storageClassName"] = storageClass
	}
	spec := map[string]any{
		"type":     e.dsType,
		"image":    map[string]any{"repository": e.image, "tag": e.tag, "pullPolicy": "IfNotPresent"},
		"replicas": int64(1),
		"storage":  storage,
		"ports":    ports,
		"envFrom":  []any{map[string]any{"secretRef": map[string]any{"name": secretName}}},
		"resources": map[string]any{
			"requests": map[string]any{"cpu": "100m", "memory": e.memReq},
			"limits":   map[string]any{"cpu": "1000m", "memory": e.memLim},
		},
		"partOf": "managed-database",
	}
	// Mount the "data" PVC (the operator names its volumeClaimTemplate "data")
	// only when the engine declares a data path — the operator's build_statefulset
	// does NOT auto-mount, so without this the instance writes to ephemeral
	// container storage and loses data on restart.
	if e.dataMount != "" {
		spec["volumeMounts"] = []any{map[string]any{"name": "data", "mountPath": e.dataMount}}
	}
	if pullSecret != "" {
		spec["imagePullSecrets"] = []any{map[string]any{"name": pullSecret}}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "hanzo.ai/v1",
		"kind":       "Datastore",
		"metadata": map[string]any{
			"name":      inst,
			"namespace": ns,
			"labels": map[string]any{
				"hanzo.ai/org":               org,
				"hanzo.ai/managed-by":        "provisioning",
				"hanzo.ai/kind":              kind,
				"hanzo.ai/resource":          resourceID,
				"app.kubernetes.io/instance": inst,
			},
		},
		"spec": spec,
	}}
}

// adminSecretObj renders the per-instance admin Secret. Its keys ARE the env
// vars the image reads (the CR references it via one envFrom secretRef). Created
// at runtime via the API (not a committed manifest), so no plaintext ever lands
// in git.
func adminSecretObj(ns, org, inst, kind, secretName string, data map[string]string) *unstructured.Unstructured {
	sd := make(map[string]any, len(data))
	for k, v := range data {
		sd[k] = v
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      secretName,
			"namespace": ns,
			"labels": map[string]any{
				"hanzo.ai/org":        org,
				"hanzo.ai/managed-by": "provisioning",
				"hanzo.ai/kind":       kind,
			},
		},
		"type":       "Opaque",
		"stringData": sd,
	}}
}

// ----- orchestrator ---------------------------------------------------------

// orchestrator is the cluster side of the dedicated strategy: ensure the tenant
// namespace + RBAC, project the admin Secret, apply/observe/delete the Datastore
// CR. The dynamic-client impl talks to the operator CR path; tests inject a fake.
type orchestrator interface {
	Ready() error
	EnsureTenant(ctx context.Context, ns, org string) error
	ApplySecret(ctx context.Context, ns, name string, obj *unstructured.Unstructured) error
	ApplyDatastore(ctx context.Context, ns, name string, obj *unstructured.Unstructured) error
	DatastorePhase(ctx context.Context, ns, name string) (string, error)
	DeleteDatastore(ctx context.Context, ns, name string) error
	DeleteSecret(ctx context.Context, ns, name string) error
	// DeletePVC removes a StatefulSet-retained data volume. Owner-ref GC tears
	// down the StatefulSet + Service when the CR is deleted, but a StatefulSet
	// deliberately RETAINS its volumeClaimTemplate PVCs — so a dropped instance
	// would leak storage the platform keeps paying for. drop reaps it explicitly.
	DeletePVC(ctx context.Context, ns, name string) error
}

var (
	datastoresGVR = schema.GroupVersionResource{Group: "hanzo.ai", Version: "v1", Resource: "datastores"}
	secretsGVR    = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	pvcsGVR       = schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}
	nsGVR         = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	ssarGVR       = schema.GroupVersionResource{Group: "authorization.k8s.io", Version: "v1", Resource: "selfsubjectaccessreviews"}
)

// k8sOrchestrator is the production orchestrator over the in-cluster (or
// KUBECONFIG) dynamic client — the SAME mechanism clients/platform uses to write
// operator Service CRs, mirrored for the DB CR family. A nil dyn fails every op
// closed with initErr.
type k8sOrchestrator struct {
	dyn         dynamic.Interface
	initErr     string
	rbacTimeout time.Duration // 0 => production default; tests shrink it
}

func newOrchestrator() *k8sOrchestrator {
	o := &k8sOrchestrator{}
	cfg, err := rest.InClusterConfig()
	if err != nil {
		cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{})
		cfg, err = cc.ClientConfig()
		if err != nil {
			o.initErr = fmt.Sprintf("no in-cluster config and no kubeconfig: %v", err)
			return o
		}
	}
	cfg.UserAgent = "hanzo-cloud-provisioning"
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		o.initErr = fmt.Sprintf("dynamic client: %v", err)
		return o
	}
	o.dyn = dyn
	return o
}

func (o *k8sOrchestrator) Ready() error {
	if o == nil || o.dyn == nil {
		if o != nil && o.initErr != "" {
			return fmt.Errorf("%s", o.initErr)
		}
		return fmt.Errorf("kubernetes client not configured")
	}
	return nil
}

// EnsureTenant creates tenant-<org> if absent (which triggers the operator to
// project cloud-api's per-tenant RoleBinding) and blocks until cloud-api may
// create datastores in it, or the bounded window elapses (errTenantProvisioning).
func (o *k8sOrchestrator) EnsureTenant(ctx context.Context, ns, org string) error {
	if err := o.Ready(); err != nil {
		return err
	}
	_, err := o.dyn.Resource(nsGVR).Get(ctx, ns, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		obj := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "Namespace",
			"metadata": map[string]any{"name": ns, "labels": map[string]any{
				"hanzo.ai/org":        org,
				"hanzo.ai/managed-by": "platform", // the label the operator's tenant-RBAC controller watches
			}},
		}}
		if _, cErr := o.dyn.Resource(nsGVR).Create(ctx, obj, metav1.CreateOptions{}); cErr != nil && !apierrors.IsAlreadyExists(cErr) {
			return cErr
		}
	} else if err != nil {
		return err
	}
	return o.waitCanCreateDatastores(ctx, ns)
}

func (o *k8sOrchestrator) canCreateDatastores(ctx context.Context, ns string) (bool, error) {
	ssar := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "authorization.k8s.io/v1", "kind": "SelfSubjectAccessReview",
		"spec": map[string]any{"resourceAttributes": map[string]any{
			"namespace": ns, "verb": "create", "group": "hanzo.ai", "resource": "datastores",
		}},
	}}
	out, err := o.dyn.Resource(ssarGVR).Create(ctx, ssar, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	allowed, _, _ := unstructured.NestedBool(out.Object, "status", "allowed")
	return allowed, nil
}

func (o *k8sOrchestrator) waitCanCreateDatastores(ctx context.Context, ns string) error {
	timeout := o.rbacTimeout
	if timeout <= 0 {
		timeout = tenantRBACReadyTimeout
	}
	deadline := time.Now().Add(timeout)
	backoff := tenantRBACPollInitial
	for {
		allowed, probeErr := o.canCreateDatastores(ctx, ns)
		if allowed {
			return nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			if probeErr != nil {
				return fmt.Errorf("%w: %s not ready (last probe: %v)", errTenantProvisioning, ns, probeErr)
			}
			return fmt.Errorf("%w: %s not ready", errTenantProvisioning, ns)
		}
		sleep := backoff
		if sleep > tenantRBACPollMax {
			sleep = tenantRBACPollMax
		}
		if sleep > remaining {
			sleep = remaining
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleep):
		}
		backoff *= 2
	}
}

func (o *k8sOrchestrator) ApplySecret(ctx context.Context, ns, name string, obj *unstructured.Unstructured) error {
	return o.apply(ctx, secretsGVR, ns, name, obj)
}

func (o *k8sOrchestrator) ApplyDatastore(ctx context.Context, ns, name string, obj *unstructured.Unstructured) error {
	return o.apply(ctx, datastoresGVR, ns, name, obj)
}

// apply creates obj, or — if a residual of a failed prior attempt already holds
// the name (the row dedup guarantees no LIVE resource maps here) — replaces it,
// so a retried provision is deterministic. Owner-ref GC tears down a replaced
// Datastore's StatefulSet+PVC.
func (o *k8sOrchestrator) apply(ctx context.Context, gvr schema.GroupVersionResource, ns, name string, obj *unstructured.Unstructured) error {
	_, err := o.dyn.Resource(gvr).Namespace(ns).Create(ctx, obj, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		if dErr := o.dyn.Resource(gvr).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{}); dErr != nil && !apierrors.IsNotFound(dErr) {
			return dErr
		}
		_, err = o.dyn.Resource(gvr).Namespace(ns).Create(ctx, obj, metav1.CreateOptions{})
	}
	return err
}

func (o *k8sOrchestrator) DatastorePhase(ctx context.Context, ns, name string) (string, error) {
	obj, err := o.dyn.Resource(datastoresGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", errNotFound
	}
	if err != nil {
		return "", err
	}
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	return phase, nil
}

func (o *k8sOrchestrator) DeleteDatastore(ctx context.Context, ns, name string) error {
	return o.del(ctx, datastoresGVR, ns, name)
}

func (o *k8sOrchestrator) DeleteSecret(ctx context.Context, ns, name string) error {
	return o.del(ctx, secretsGVR, ns, name)
}

func (o *k8sOrchestrator) DeletePVC(ctx context.Context, ns, name string) error {
	return o.del(ctx, pvcsGVR, ns, name)
}

func (o *k8sOrchestrator) del(ctx context.Context, gvr schema.GroupVersionResource, ns, name string) error {
	err := o.dyn.Resource(gvr).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
