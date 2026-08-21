package mq

// The surface ledger, measured against the SPEC OF INTENT. The authored MQ
// document (hanzoai/openapi d86248f^:mq/openapi.yaml) states 41 operations;
// this surface serves the 15 the broker genuinely answers for a tenant —
// streams, direct message access, pull consumers, health, info — every one a
// TYPED op, and REFUSES the other 26 with the reason pinned below. A refusal
// nobody can re-check is how a servable operation stays unserved forever, so
// both directions are tests: an op leaving `served` fails here, and an op
// added while still named in `refused` fails here.

import (
	"sort"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
)

// served is the closed list of operations this surface answers — the authored
// spec's streams/consumer/health core, addressed exactly as the document
// spells them.
var served = map[string]bool{
	"GET /v1/mq/stream":                                true,
	"POST /v1/mq/stream":                               true,
	"GET /v1/mq/stream/{name}":                         true,
	"PUT /v1/mq/stream/{name}":                         true,
	"DELETE /v1/mq/stream/{name}":                      true,
	"POST /v1/mq/stream/{name}/purge":                  true,
	"GET /v1/mq/stream/{name}/message":                 true,
	"DELETE /v1/mq/stream/{name}/message/{seq}":        true,
	"GET /v1/mq/stream/{stream}/consumer":              true,
	"POST /v1/mq/stream/{stream}/consumer":             true,
	"GET /v1/mq/stream/{stream}/consumer/{name}":       true,
	"DELETE /v1/mq/stream/{stream}/consumer/{name}":    true,
	"POST /v1/mq/stream/{stream}/consumer/{name}/next": true,
	"GET /v1/mq/health":                                true,
	"GET /v1/mq/info":                                  true,
}

// The four reasons, each a fact about THIS deployment that a route would
// contradict — not a deferral. When a fact stops being true, serve the op and
// delete its row.
const (
	// reasonPubsub: one broker, two orthogonal products. The subject side —
	// publishing, subscribing, request/reply, subject introspection — is the
	// pubsub product's surface (/v1/pubsub, apps/pubsub). Serving it at
	// /v1/mq too would be the same broker op behind two doors, and cloud
	// serves every capability through exactly one.
	reasonPubsub = "the subject side of the broker is the pubsub product's surface (/v1/pubsub); " +
		"a second door on /v1/mq would duplicate the op across products"
	// reasonKV: cloud already provisions keyed stores at /v1/kv and
	// /v1/datastore (apps/provisioning, apps/datastore). NATS KV is an
	// implementation, not a second product: a /v1/mq/kv door would be a
	// duplicate capability door with its own diverging shape.
	reasonKV = "keyed storage is already a cloud product (/v1/kv, /v1/datastore); " +
		"a broker-flavoured second door would duplicate the capability"
	// reasonObjects: object storage is the storage product (/v1/s3/buckets,
	// apps/s3). Same door rule as KV.
	reasonObjects = "object storage is already a cloud product (/v1/s3); " +
		"a broker-flavoured second door would duplicate the capability"
	// reasonAccounts: the authored account/connection listings are broker
	// MONITOR data (accountz/connz). The embedded plane exposes no monitor
	// endpoint to a client connection and the wire protocol carries none, so
	// no real backend answers them — and org accounts are IAM's noun besides.
	reasonAccounts = "broker monitor data (accountz/connz) that the embedded plane does not expose " +
		"to a client connection; no real backend answers today"
)

// refused pins every authored operation this surface does NOT serve, keyed as
// the authored document spells it, so each refusal names something checkable.
var refused = map[string]string{
	"POST /v1/mq/publish":                  reasonPubsub,
	"GET /v1/mq/subscribe/{subject}":       reasonPubsub,
	"POST /v1/mq/request":                  reasonPubsub,
	"GET /v1/mq/subjects":                  reasonPubsub,
	"GET /v1/mq/subjects/{subject}/info":   reasonPubsub,
	"GET /v1/mq/kv":                        reasonKV,
	"POST /v1/mq/kv":                       reasonKV,
	"GET /v1/mq/kv/{bucket}":               reasonKV,
	"DELETE /v1/mq/kv/{bucket}":            reasonKV,
	"GET /v1/mq/kv/{bucket}/keys":          reasonKV,
	"GET /v1/mq/kv/{bucket}/{key}":         reasonKV,
	"PUT /v1/mq/kv/{bucket}/{key}":         reasonKV,
	"DELETE /v1/mq/kv/{bucket}/{key}":      reasonKV,
	"GET /v1/mq/kv/{bucket}/{key}/history": reasonKV,
	"GET /v1/mq/kv/{bucket}/watch":         reasonKV,
	"GET /v1/mq/objects":                   reasonObjects,
	"POST /v1/mq/objects":                  reasonObjects,
	"GET /v1/mq/objects/{store}":           reasonObjects,
	"DELETE /v1/mq/objects/{store}":        reasonObjects,
	"GET /v1/mq/objects/{store}/list":      reasonObjects,
	"GET /v1/mq/objects/{store}/{name}":    reasonObjects,
	"PUT /v1/mq/objects/{store}/{name}":    reasonObjects,
	"DELETE /v1/mq/objects/{store}/{name}": reasonObjects,
	"GET /v1/mq/accounts":                  reasonAccounts,
	"GET /v1/mq/accounts/{id}":             reasonAccounts,
	"GET /v1/mq/accounts/{id}/connections": reasonAccounts,
}

// wireApp mounts the surface with nothing listening on the bus: routes and
// projections are a function of Mount alone, never of a live broker.
func wireApp(t *testing.T) *zip.App {
	t.Helper()
	t.Setenv("CLOUD_PUBSUB_URL", "nats://127.0.0.1:1")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{}); err != nil {
		t.Fatalf("mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(nil) })
	return app
}

// surface projects the mounted routes the same way the fleet document is
// projected, keyed "METHOD /path" as the document spells them.
func surface(t *testing.T, app *zip.App) (ops map[string]bool, typed map[string]bool) {
	t.Helper()
	doc, err := openapi.Spec(app, openapi.Info{Title: "mq", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	ops = map[string]bool{}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/mq") {
			continue
		}
		for method := range item {
			ops[strings.ToUpper(method)+" "+path] = true
		}
	}
	typed = map[string]bool{}
	for key := range reg.Ops {
		if i := strings.Index(key, " "); i > 0 && strings.HasPrefix(key[i+1:], "/v1/mq") {
			typed[key] = true
		}
	}
	return ops, typed
}

// TestServedIsExactlyTheLedger: the mounted surface and the `served` ledger
// are the same set — an op appearing or disappearing is a decision made HERE.
func TestServedIsExactlyTheLedger(t *testing.T) {
	ops, _ := surface(t, wireApp(t))
	var extra, missing []string
	for key := range ops {
		if !served[key] {
			extra = append(extra, key)
		}
	}
	for key := range served {
		if !ops[key] {
			missing = append(missing, key)
		}
	}
	sort.Strings(extra)
	sort.Strings(missing)
	if len(extra) > 0 {
		t.Errorf("serving ops the ledger does not name: %s", strings.Join(extra, ", "))
	}
	if len(missing) > 0 {
		t.Errorf("ledger names ops that are not served: %s", strings.Join(missing, ", "))
	}
}

// TestEveryOpIsTyped: this surface has NO untyped routes — every op projects
// prose, an MCP tool, a CLI command and an SDK method, or it does not ship.
func TestEveryOpIsTyped(t *testing.T) {
	ops, typed := surface(t, wireApp(t))
	if len(ops) == 0 {
		t.Fatal("mq serves nothing at all — the router moved and this gate is now blind")
	}
	var untyped []string
	for key := range ops {
		if !typed[key] {
			untyped = append(untyped, key)
		}
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("untyped: %s — an untyped route projects to NOTHING; type it or drop it",
			strings.Join(untyped, ", "))
	}
}

// TestRefusedStaysRefused: no refused authored op is served, and no refusal
// shadows a served one — the ledger halves stay disjoint and exhaustive over
// the authored document's 41 operations.
func TestRefusedStaysRefused(t *testing.T) {
	ops, _ := surface(t, wireApp(t))
	for key, why := range refused {
		if ops[key] {
			t.Errorf("%s is served but still pinned refused (%q) — delete its refusal row", key, why)
		}
		if served[key] {
			t.Errorf("%s is in BOTH ledgers — a refusal that shadows a served op keeps nobody honest", key)
		}
	}
	if want := 41; len(served)+len(refused) != want {
		t.Errorf("ledger covers %d ops, the authored spec states %d — account for every authored op",
			len(served)+len(refused), want)
	}
}
