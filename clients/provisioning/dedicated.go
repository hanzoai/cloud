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
	// secretEnv builds the admin Secret data. For an envFrom engine (secretMount
	// == "") each key is an env var the image reads (the CR references it via one
	// envFrom secretRef), so the instance boots with THIS admin credential. For a
	// mounted engine (secretMount != "") the map is instead a set of FILES the
	// secret projects — its keys are filenames, its values file contents (e.g.
	// kv's "kv.conf" -> "requirepass <pw>"). Either way the credential lives ONLY
	// in the runtime Secret — never a plaintext in the CR/commit.
	secretEnv func(user, pw, db string) map[string]string
	// dsn builds the customer connection string.
	dsn func(user, pw, host string, port int, db string) string
	// adminUser overrides the DSN/role username (else dedicatedAdminUser="admin").
	// Valkey ("kv") authenticates its built-in "default" user via requirepass —
	// there is no "admin" ACL user — so kv sets this to "default"; a made-up
	// username would fail AUTH. Empty ⇒ "admin" (datastore/sql/docdb).
	adminUser string
	// env is non-secret plain env stamped on the CR's spec.env. sql needs
	// PGDATA=<mount>/pgdata: a fresh block PVC root holds a lost+found that
	// Postgres initdb refuses, so PGDATA must be a SUBDIRECTORY of the mount (the
	// same layout the shared sql-0 uses). Empty for engines that need none.
	env map[string]string
	// args are container args stamped on the CR's spec.args (the image ENTRYPOINT
	// is preserved). kv loads its per-instance requirepass from the mounted config
	// file by passing it as the first arg; the envFrom engines set none.
	args []string
	// secretMount, when set, mounts the admin Secret as FILES at this path instead
	// of exporting it via envFrom. Valkey ("kv") reads its password from a
	// requirepass config file (the kv-server binary does NOT read a password from
	// env), so its admin Secret is a config file mounted here + loaded via args —
	// still RBAC-scoped, never inlined in the CR. Empty ⇒ envFrom (the default).
	secretMount string
	// pod memory floor/ceiling — a database needs a real memory reservation.
	memReq string
	memLim string
	// dataMount, when non-empty, is the container path the instance's "data" PVC
	// mounts at. The operator only mounts the PVC when the CR names a volumeMount
	// (build_statefulset does not auto-mount), so an engine that must persist to
	// disk MUST set this — else the pod writes to ephemeral container storage.
	dataMount string
	// fsGroup, when non-zero, is the pod-level securityContext.fsGroup the
	// operator stamps on the StatefulSet (spec.fsGroup). A freshly-provisioned
	// block PVC is mounted root:root, so a NON-root image (FerretDB runs as UID
	// 1000, distroless — no entrypoint can chown) cannot write its data dir until
	// the kubelet group-owns the volume to fsGroup. Only engines whose image runs
	// non-root and self-manages nothing need this; a root/self-chowning image
	// (ClickHouse) leaves it 0 and gets no securityContext (byte-identical STS).
	fsGroup int64
}

// dedicatedEngines is the closed set of kinds provisioned as a dedicated
// instance. It is exactly the FOUR on-demand data add-ons — Hanzo KV / SQL /
// DocDB / Datastore — every product runs on Base/SQLite by default and an org
// ENABLES one of these per instance on demand (disabling reverts to Base). All
// four route through the ONE identical dedicated path (createDedicated), varying
// only by the data in this table: the operator materializes each as a Datastore
// CR of the matching spec.type (valkey / postgresql / docdb / datastore). Adding
// an add-on kind = one more entry here; nothing else in the control plane
// special-cases a kind. sql + kv moved here from the shared-logical registry so
// each org OWNS its instance — a per-tenant credential is naturally scoped and a
// cross-tenant grant is impossible (one tenant per instance).
var dedicatedEngines = map[string]engine{
	// PostgreSQL ("sql"): ghcr.io/hanzoai/sql provisions POSTGRES_USER (as a
	// SUPERUSER, with POSTGRES_PASSWORD) on POSTGRES_DB at entrypoint — the exact
	// env the shared sql-0 StatefulSet uses. PGDATA must be a SUBDIRECTORY of the
	// data mount: a fresh block PVC root carries a lost+found that Postgres initdb
	// refuses, so the shared sql-0 sets PGDATA=/var/lib/postgresql/data/pgdata and
	// we mirror it. Root/self-chowning image (no fsGroup needed).
	"sql": {
		prefix: "sql", dsType: "postgresql",
		image: "ghcr.io/hanzoai/sql", tag: env("CLOUD_DEDICATED_SQL_TAG", "18"),
		ports:      []enginePort{{"sql", 5432}},
		clientPort: 5432,
		dataMount:  "/var/lib/postgresql/data",
		env:        map[string]string{"PGDATA": "/var/lib/postgresql/data/pgdata"},
		memReq:     "128Mi", memLim: "512Mi",
		secretEnv: func(user, pw, db string) map[string]string {
			return map[string]string{
				"POSTGRES_USER":     user,
				"POSTGRES_PASSWORD": pw,
				"POSTGRES_DB":       db,
			}
		},
		dsn: func(user, pw, host string, port int, db string) string {
			return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", user, pw, host, port, db)
		},
	},
	// Valkey ("kv"): ghcr.io/hanzoai/kv runs the kv-server binary directly, which
	// authenticates its built-in "default" user via `requirepass`. The binary does
	// NOT read a password from env, so — unlike the env-configured engines — kv's
	// admin Secret is a valkey config FILE ("kv.conf" -> "requirepass <pw>")
	// MOUNTED at secretMount and loaded as the first arg; the password stays
	// RBAC-scoped in the Secret, never inlined in the CR spec. DSN user is
	// "default" (there is no "admin" ACL user). Root/self-chowning image.
	"kv": {
		prefix: "kv", dsType: "valkey",
		image: "ghcr.io/hanzoai/kv", tag: env("CLOUD_DEDICATED_KV_TAG", "9"),
		ports:       []enginePort{{"kv", 6379}},
		clientPort:  6379,
		dataMount:   "/data",
		adminUser:   "default",
		secretMount: "/etc/kvconf",
		args:        []string{"/etc/kvconf/kv.conf", "--bind", "0.0.0.0", "--dir", "/data", "--protected-mode", "no", "--maxmemory-policy", "allkeys-lru"},
		memReq:      "64Mi", memLim: "512Mi",
		secretEnv: func(_, pw, _ string) map[string]string {
			// A valkey config snippet, not env: requirepass sets the default
			// user's password (genToken output is [A-Za-z0-9_-] — no quoting
			// needed, no injection). The instance boots reading THIS file.
			return map[string]string{"kv.conf": "requirepass " + pw + "\n"}
		},
		dsn: func(user, pw, host string, port int, _ string) string {
			return fmt.Sprintf("redis://%s:%s@%s:%d", user, pw, host, port)
		},
	},
	// ClickHouse ("datastore"): ghcr.io/hanzoai/datastore provisions
	// DATASTORE_USER (with DATASTORE_PASSWORD) on DATASTORE_DB at entrypoint and
	// drops the built-in default user, so the provisioned user is the instance
	// admin — the exact env the shared datastore StatefulSet uses. Tag is pinned
	// to an IMMUTABLE build (env-overridable, symmetric with sql/kv/docdb): a
	// floating ":26" resolves to whichever of the two datastore lineages (bridge
	// vs fork, distinct data dirs) last pushed under it — a per-org instance must
	// boot a deterministic image, so pin the digest-stable tag the shared
	// datastore CR already runs.
	"datastore": {
		prefix: "ds", dsType: "datastore",
		image: "ghcr.io/hanzoai/datastore", tag: env("CLOUD_DEDICATED_DATASTORE_TAG", "26.2.3.2"),
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
	// FerretDB on Hanzo SQL ("docdb"): the managed "document database" is a
	// dedicated per-org FerretDB instance speaking the MongoDB wire protocol on
	// :27017, backed by Hanzo SQL (SQLite, pure-Go modernc — Mongo databases map
	// to SQLite files under /state, collections to tables, documents to JSON1
	// rows). Existing MongoDB drivers/clients (mongodb://) work unchanged. ZERO
	// raw mongod (no WiredTiger), ZERO Postgres. Per-instance SCRAM auth via
	// FerretDB's SQLite-backend authentication (FERRETDB_SETUP_*). Runs on the
	// generic Datastore controller — image/ports/volumeMounts drive the
	// StatefulSet verbatim, no operator branch needed.
	"docdb": {
		prefix: "ddb", dsType: "docdb",
		image: "ghcr.io/hanzoai/docdb-sqlite", tag: env("CLOUD_DEDICATED_DOCDB_TAG", "1.24.0"),
		ports:      []enginePort{{"mongo", 27017}},
		clientPort: 27017,
		dataMount:  "/state",
		fsGroup:    1000, // FerretDB image runs as UID:GID 1000 (distroless); PVC must be group-writable
		memReq:     "128Mi", memLim: "512Mi",
		secretEnv: func(user, pw, db string) map[string]string {
			// FerretDB v1.24 SQLite backend + per-instance SCRAM auth. Data at
			// rest is SQLite files under /state — NO mongod, NO Postgres. The
			// SQLite backend requires new-auth to bootstrap the setup user, and
			// --setup-database is rejected without it (FerretDB main.go). State
			// dir is pinned to the PVC so process state persists across restarts.
			return map[string]string{
				"FERRETDB_HANDLER":              "sqlite",
				"FERRETDB_SQLITE_URL":           "file:/state/",
				"FERRETDB_STATE_DIR":            "/state",
				"FERRETDB_LISTEN_ADDR":          ":27017",
				"FERRETDB_TEST_ENABLE_NEW_AUTH": "true",
				"FERRETDB_SETUP_USERNAME":       user,
				"FERRETDB_SETUP_PASSWORD":       pw,
				"FERRETDB_SETUP_DATABASE":       db,
			}
		},
		dsn: func(user, pw, host string, port int, db string) string {
			return fmt.Sprintf("mongodb://%s:%s@%s:%d/%s?authSource=%s", user, pw, host, port, db, db)
		},
	},
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
// name validated, billing gated, (org,kind,name) dedup checked). When instance
// is non-empty the assembled DSN is also projected as <KIND>_URL into that app
// instance's addons Secret, switching it off Base onto this backend.
func createDedicated(s *cloud.Service[state], c *zip.Ctx, ctx context.Context, kind, org, name string, e engine, fee int64, instance string) error {
	if s.State.orch == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "dedicated provisioning unavailable: no cluster client")
	}
	if err := s.State.orch.Ready(); err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "dedicated provisioning unavailable: %v", err)
	}

	ns := tenantNamespace(org)
	inst := instanceName(kind, org, name)
	db := sanitizeIdent(name)

	// Global (cross-org) uniqueness guard on the instance identity — fail closed
	// BEFORE touching the cluster (mirrors the shared path's PhysicalExists).
	if exists, err := s.State.store.PhysicalExists(ctx, inst); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
	} else if exists {
		return zip.ErrConflict("resource already exists")
	}

	// Ensure the tenant namespace + wait for the operator to grant cloud-api
	// create access in it. Honest retryable 503 while the grant is still landing.
	if err := s.State.orch.EnsureTenant(ctx, ns, org); err != nil {
		if errors.Is(err, errTenantProvisioning) {
			return zip.Errorf(http.StatusServiceUnavailable, "tenant still provisioning, retry")
		}
		s.Log.Error("ensure tenant failed", "kind", kind, "org", org, "ns", ns, "err", err)
		return zip.Errorf(http.StatusBadGateway, "ensure tenant: %v", err)
	}

	pw, err := genToken(24)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	id, err := genID()
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	user := e.adminUser
	if user == "" {
		user = dedicatedAdminUser
	}
	secretName := inst + "-admin"

	// Seal the instance admin credential in KMS (durable + audited). The password
	// is ALSO returned once in this response and projected as the namespace Secret
	// below — but NEVER stored in plaintext at rest here.
	secretRef := fmt.Sprintf("org/%s/%s/%s", org, kind, name)
	storedRef := ""
	if s.State.sec.Enabled() {
		if err := s.State.sec.Put(secretRef, []byte(pw)); err != nil {
			s.Log.Error("kms put failed", "kind", kind, "err", err)
			return zip.Errorf(http.StatusInternalServerError, "store secret failed")
		}
		storedRef = secretRef
	} else {
		s.Log.Warn("KMS degraded: instance admin password returned once, not persisted", "kind", kind, "org", org, "name", name)
	}

	// Project the admin Secret (the env the image boots from) then apply the
	// Datastore CR (the operator materializes the StatefulSet + Service + PVC).
	secObj := adminSecretObj(ns, org, inst, kind, secretName, e.secretEnv(user, pw, db))
	if err := s.State.orch.ApplySecret(ctx, ns, secretName, secObj); err != nil {
		if storedRef != "" {
			_ = s.State.sec.Delete(storedRef)
		}
		s.Log.Error("project admin secret failed", "kind", kind, "org", org, "err", err)
		return zip.Errorf(http.StatusBadGateway, "project admin secret: %v", err)
	}
	size := dedicatedSize(kind)
	crObj := datastoreCR(ns, org, inst, id, kind, e, size, os.Getenv("CLOUD_DEDICATED_STORAGE_CLASS"), secretName, env("CLOUD_DEDICATED_PULL_SECRET", "ghcr-pull"))
	if err := s.State.orch.ApplyDatastore(ctx, ns, inst, crObj); err != nil {
		_ = s.State.orch.DeleteSecret(ctx, ns, secretName)
		if storedRef != "" {
			_ = s.State.sec.Delete(storedRef)
		}
		s.Log.Error("launch instance failed", "kind", kind, "org", org, "inst", inst, "err", err)
		return zip.Errorf(http.StatusBadGateway, "launch instance: %v", err)
	}

	host := fmt.Sprintf("%s.%s.svc", inst, ns)
	dsn := e.dsn(user, pw, host, e.clientPort, db)
	r := Resource{
		ID: id, Org: org, Kind: kind, Name: name,
		PhysicalName: inst, SecretRef: storedRef,
		Host: host, Port: e.clientPort, Username: user, DBName: db,
		Status: statusProvisioning, CreatedAt: time.Now().Unix(), Size: size, Instance: instance,
	}
	if err := s.State.store.Insert(ctx, r); err != nil {
		// Undo the instance + secret we just created.
		_ = s.State.orch.DeleteDatastore(ctx, ns, inst)
		_ = s.State.orch.DeleteSecret(ctx, ns, secretName)
		if storedRef != "" {
			_ = s.State.sec.Delete(storedRef)
		}
		if errors.Is(err, errConflict) {
			return zip.ErrConflict("resource already exists")
		}
		return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}

	// Instance binding (no-op when not instance-bound): project the DSN as
	// <KIND>_URL into the app instance's addons Secret so it switches off Base onto
	// this backend. This is done AFTER Insert (only the row winner reaches it, so
	// concurrent duplicates can't race the injection) and is part of the atomic
	// provision — a failure rolls back the row + instance + secret so the org is
	// never billed for a half-wired resource and the instance stays on Base.
	if err := injectAddonURL(s, ctx, org, instance, kind, dsn); err != nil {
		// Best-effort revert to Base FIRST. injectAddonURL is not atomic: the
		// strategic-merge PATCH can LAND server-side yet still return err (a
		// dropped response, a timeout after the write commits). Delete any
		// <KIND>_URL we may have written BEFORE we tear the backend down, so a
		// committed-but-errored inject can't leave the instance pointing at a
		// deleted backend — a dangling DSN is worse than Base. Idempotent: a
		// no-write inject makes this a no-op (missing key/Secret is success).
		_ = removeAddonURL(s, ctx, org, instance, kind)
		_, _ = s.State.store.Delete(ctx, org, kind, name)
		_ = s.State.orch.DeleteDatastore(ctx, ns, inst)
		_ = s.State.orch.DeleteSecret(ctx, ns, secretName)
		if storedRef != "" {
			_ = s.State.sec.Delete(storedRef)
		}
		s.Log.Error("inject addon url failed; rolled back provision", "kind", kind, "org", org, "instance", instance, "err", err)
		return zip.Errorf(http.StatusBadGateway, "wire instance: %v", err)
	}

	// The instance is launching + persisted + wired — attribute the provision to
	// the caller's OWN org, carrying the size dimension so the invoice line names
	// the instance. The recurring footprint meter charges ongoing GB-time.
	meterProvision(s, org, kind, size, fee, c.RequestID(), cloud.ClientIP(c))

	return c.JSON(http.StatusCreated, createResp{
		ID: id, Kind: kind, Name: name, Status: statusProvisioning,
		Host: host, Port: e.clientPort, Username: user, Database: db,
		ConnectionString: dsn, Password: pw,
	})
}

// reconcileDedicated advances a "provisioning" row to "ready" when the operator
// reports the instance StatefulSet Running. Honest: any read error leaves the
// row provisioning (never a fabricated ready). Called on get and by the meter
// sweep so an unpolled instance still becomes ready (and billable).
func reconcileDedicated(s *cloud.Service[state], ctx context.Context, r Resource) Resource {
	if r.Status != statusProvisioning || s.State.orch == nil {
		return r
	}
	phase, err := s.State.orch.DatastorePhase(ctx, tenantNamespace(r.Org), r.PhysicalName)
	if err != nil {
		return r
	}
	if phase == phaseRunning {
		if _, uErr := s.State.store.UpdateStatus(ctx, r.Org, r.Kind, r.Name, statusReady); uErr == nil {
			r.Status = statusReady
		}
	}
	return r
}

// dropDedicated tears down the instance: delete the Datastore CR (the operator
// garbage-collects the StatefulSet + Service + PVC via owner refs) and its admin
// Secret. The KMS secret + metadata row are removed by the shared drop tail.
func dropDedicated(s *cloud.Service[state], ctx context.Context, r Resource) error {
	if s.State.orch == nil {
		return nil
	}
	ns := tenantNamespace(r.Org)
	if err := s.State.orch.DeleteDatastore(ctx, ns, r.PhysicalName); err != nil {
		return err
	}
	// Reap the retained data volume (data-<instance>-0 for the single-replica
	// StatefulSet) so a dropped instance leaves no storage footprint, then the
	// admin Secret. Both best-effort after the CR delete — a missing object is
	// success (already gone).
	_ = s.State.orch.DeletePVC(ctx, ns, "data-"+r.PhysicalName+"-0")
	return s.State.orch.DeleteSecret(ctx, ns, r.PhysicalName+"-admin")
}

// ----- billing --------------------------------------------------------------

// meterProvision attributes a dedicated instance's provision to the caller's OWN
// org with a size dimension, via the ONE commerce meter (never a parallel path).
// No-op when billing is unconfigured or the kind is free (fee 0).
func meterProvision(s *cloud.Service[state], org, kind, size string, fee int64, requestID, clientIP string) {
	s.Bill.MeterUsage(org, kind, metering.Usage{
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
func meterDedicatedFootprint(s *cloud.Service[state], ctx context.Context) {
	rows, err := s.State.store.ListAllByStatus(ctx, statusReady)
	if err != nil {
		s.Log.Error("footprint meter: list failed", "err", err)
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
		s.Bill.MeterUsage(r.Org, r.Kind, metering.Usage{
			AmountCents: cents,
			Model:       r.Kind + ":" + r.Size + ":gbday",
			RequestID:   r.ID,
		})
	}
}

// startFootprintMeter runs the recurring footprint charge on a ticker until
// Shutdown. It only starts when billing actually enforces, so an unconfigured
// deployment spins no goroutine.
func startFootprintMeter(s *cloud.Service[state]) {
	if s.Bill == nil || !s.Bill.Enabled() {
		return
	}
	stop := make(chan struct{})
	s.State.stopMeter = func() { close(stop) }
	go func() {
		t := time.NewTicker(dedicatedMeterInterval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				meterDedicatedFootprint(s, ctx)
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
		"resources": map[string]any{
			"requests": map[string]any{"cpu": "100m", "memory": e.memReq},
			"limits":   map[string]any{"cpu": "1000m", "memory": e.memLim},
		},
		"partOf": "managed-database",
	}
	// Admin-credential wiring. Env-configured engines (datastore/sql/docdb) read
	// it via one envFrom secretRef. A mounted engine (kv) instead reads a config
	// FILE the same Secret projects — mounted read-only at secretMount and loaded
	// via args — because the kv-server binary takes no password from env. Either
	// way the Secret is referenced by NAME only; no plaintext lands in the CR.
	var volumeMounts []any
	// Mount the "data" PVC (the operator names its volumeClaimTemplate "data")
	// only when the engine declares a data path — the operator's build_statefulset
	// does NOT auto-mount, so without this the instance writes to ephemeral
	// container storage and loses data on restart.
	if e.dataMount != "" {
		volumeMounts = append(volumeMounts, map[string]any{"name": "data", "mountPath": e.dataMount})
	}
	if e.secretMount != "" {
		spec["volumes"] = []any{map[string]any{
			"name":   "addon-secret",
			"secret": map[string]any{"secretName": secretName},
		}}
		volumeMounts = append(volumeMounts, map[string]any{
			"name": "addon-secret", "mountPath": e.secretMount, "readOnly": true,
		})
	} else {
		spec["envFrom"] = []any{map[string]any{"secretRef": map[string]any{"name": secretName}}}
	}
	if len(volumeMounts) > 0 {
		spec["volumeMounts"] = volumeMounts
	}
	// Non-secret plain env (e.g. sql's PGDATA subdirectory).
	if len(e.env) > 0 {
		envList := make([]any, 0, len(e.env))
		for k, v := range e.env {
			envList = append(envList, map[string]any{"name": k, "value": v})
		}
		spec["env"] = envList
	}
	// Container args (e.g. kv loading its per-instance requirepass config first).
	if len(e.args) > 0 {
		args := make([]any, 0, len(e.args))
		for _, a := range e.args {
			args = append(args, a)
		}
		spec["args"] = args
	}
	// A non-root image (fsGroup set) needs the operator to group-own its PVC via
	// the pod securityContext.fsGroup, else it cannot write the mounted volume.
	if e.fsGroup != 0 {
		spec["fsGroup"] = e.fsGroup
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
	// PatchAddonSecret merges key=value into the instance's addons Secret
	// (create-if-absent), preserving the other <KIND>_URL keys already there. This
	// is the injection sink for instance binding — the app reads <KIND>_URL from
	// it to switch off Base onto a provisioned backend.
	PatchAddonSecret(ctx context.Context, ns, name, org, key, value string) error
	// RemoveAddonSecretKey deletes one <KIND>_URL from the addons Secret so the
	// instance reverts to Base. A missing Secret is a no-op success (idempotent).
	RemoveAddonSecretKey(ctx context.Context, ns, name, key string) error
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
