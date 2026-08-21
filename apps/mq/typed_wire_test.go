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

// proseless is the CLOSED list of published properties that carry NO description
// because the SEAM they arrived through cannot carry one — not because nobody
// wrote it.
//
// EMBEDDED STRUCT. makeIn (consumers.go) is the create body: the path's stream,
// plus an embedded Durable, which is the consumer config the read half also
// answers. zipdoc files a field's prose under the type that DECLARES it, so the
// eleven lifted keys are Durable.ack_policy, Durable.ack_wait and so on — while
// the schema generator INLINES the promotion and publishes makeIn.ack_policy,
// which no key matches. Every one of the eleven IS described, on Durable, and
// reaches the document as the Durable component and as Consumer.config's $ref to
// it. The gap is one shape short of a create body.
//
// Not worked around. Unrolling the embedding into eleven copies on makeIn
// replaces one true statement with two that can drift — and the copies would be
// the ones a caller reads while the original is the one the code uses.
//
// Exact in BOTH directions: a bare property anywhere else goes red, and an entry
// here that starts publishing prose goes red too, which is the day zipdoc learns
// to follow an embedding and this ledger must shrink rather than outlive the gap.
var proseless = map[string]bool{
	"makeIn.ack_policy":      true,
	"makeIn.ack_wait":        true,
	"makeIn.deliver_policy":  true,
	"makeIn.description":     true,
	"makeIn.durable_name":    true,
	"makeIn.filter_subject":  true,
	"makeIn.max_ack_pending": true,
	"makeIn.max_deliver":     true,
	"makeIn.opt_start_seq":   true,
	"makeIn.opt_start_time":  true,
	"makeIn.replay_policy":   true,
}

// TestEveryPublishedFieldIsDescribed closes the half of the surface the gates
// above cannot see. Typing a route documents its ADDRESS and its SHAPE; the
// shape's FIELDS come from a different place — a doc comment on each one, which
// zipdoc lifts one at a time.
//
// It matters here because a consumer's knobs are a broker's vocabulary and not
// English. ack_policy, deliver_policy and replay_policy each draw from a FIXED
// set of words (explicit|all|none, all|last|new|by_start_sequence|by_start_time|
// last_per_subject, instant|original) that nothing in the shape itself lists;
// ack_wait is a DURATION STRING with a unit ("30s"), not a count; max_deliver
// counts attempts and takes -1 for unlimited, while max_ack_pending counts
// messages in flight — two integers a name alone would not tell apart. And
// filter_subject is org-RELATIVE: the value a caller writes is not the subject
// the broker sees.
//
// Presence is all a gate can check, and it is checked against the ledger above.
// A description restating the field's name is worse than none, and only a reader
// catches that.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(wireApp(t), openapi.Info{Title: "mq", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("mq publishes no schemas at all — the gate would pass vacuously")
	}
	published, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}

	var bare, stale []string
	seen := map[string]bool{}
	for _, path := range published {
		seen[path] = true
		if !proseless[path] {
			bare = append(bare, path)
		}
	}
	for path := range proseless {
		if !seen[path] {
			stale = append(stale, path)
		}
	}

	if len(bare) > 0 {
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted onto "+
			"the first of them alone — then run: make -C apps/mq describe",
			len(bare), strings.Join(bare, ", "))
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("proseless names propert(ies) that are gone or now described: %s\n"+
			"An exemption that outlives its cause is how a generator gap becomes permanent — "+
			"delete the entr(ies).", strings.Join(stale, ", "))
	}
}
