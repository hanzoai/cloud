package train

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/hanzoai/cloud/internal/lock"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
)

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

type state struct {
	stores  *cloud.OrgStore[*store]
	objects objects
	// seal is the id of an org's key its objects are encrypted under, minted in
	// KMS on first use (keys.go), under a lock of the org's store that every replica
	// takes.
	seal   func(ctx context.Context, st *store, org string) (string, error)
	engine *engine
	bell   *bell
	// audit is the platform's trail: a platform claim and a deletion by retention
	// are written to it before they are made. Nil on a deployment with none.
	audit *audit.Recorder
	// afford weighs micro-USD against a payer's balance and spend caps: the
	// meter's Authorize, in cents rounded up. debit is where the app's money
	// goes: the meter's Record. Both are values so a money test watches the
	// ledger's two questions rather than a network client.
	afford func(ctx context.Context, payer account.Account, project string, validated bool, micros int64) error
	debit  func(payer account.Account, kind string, u meter.Usage)
	// payers serializes each payer's money gate, across replicas (money.go).
	payers *lock.Keyed
	ttl    int64
	// retain is how many seconds an unpublished object is kept; 0 keeps it.
	retain int64
	cap    int
	room   int
	// teacher is the decision service a capability's lesson is taught by.
	teacher *teacher
	// keepCaptures is how many seconds a decision's captured states are kept
	// unless a correction names it.
	keepCaptures int64
	// router is the Enso family's routing in the ai process, which a policy is
	// promoted through (policies.go).
	router router
}

// Use mounts /v1/train and starts its hourly sweep.
func Use(app cloud.Router, deps cloud.Deps) error {
	return cloud.Use(app, deps, "train", func(b cloud.Base) (state, error) { return build(b, deps.Audit, deps.MasterKey) }, func(app cloud.Router, s *cloud.Service[state]) {
		routes(app, s)
		plane(ops{s: s})
		life, stop := context.WithCancel(context.Background())
		closeStores := shutdown
		shutdown = func() error { stop(); return closeStores() }
		go run(s, life)
	})
}

func build(b cloud.Base, rec *audit.Recorder, master []byte) (state, error) {
	return state{
		stores:  cloud.NewOrgStoreAt(b, subsystem, openStore(master)),
		objects: newBucket(),
		seal:    newKeys(b.KMS).seal,
		engine:  newEngine(),
		bell:    newBell(),
		audit:   rec,
		afford: func(ctx context.Context, payer account.Account, project string, validated bool, micros int64) error {
			return b.Bill.Authorize(ctx, payer, project, validated, "compute", centsUp(micros))
		},
		debit:  b.Bill.Record,
		payers: &lock.Keyed{},
		ttl:    int64(env.Int("TRAIN_LEASE_TTL", 600)),
		retain: int64(env.Int("TRAIN_RETENTION_DAYS", 30)) * 24 * 3600,
		cap:    env.Int("TRAIN_CLIENTS", 2),
		room:   env.Int("TRAIN_ENGINE_CLIENTS", 2),
		// the decision service ai forwards Kai's decisions to
		teacher:      &teacher{url: env.Or("KAI_URL", "http://kai.hanzo.svc:8080"), http: &http.Client{}},
		keepCaptures: int64(env.Int("TRAIN_CAPTURE_DAYS", 30)) * 24 * 3600,
		router:       aiRouter{},
	}, nil
}

var shutdown = func() error { return nil }

// Shutdown stops the sweep and closes every open per-org store.
func Shutdown() error { return shutdown() }

type ops struct{ s *cloud.Service[state] }

func routes(app cloud.Router, s *cloud.Service[state]) {
	shutdown = s.State.stores.CloseAll
	o := ops{s: s}
	exposeCapabilities(o)
	g := app.Group("/v1/train")
	// A create a wallet cannot fund refuses on the money wire's own 402/503.
	g.Use(cloud.DenyEnvelope())
	g.Get("/models", o.models)
	g.Get("/published", o.listPublished)
	g.Post("/artifacts", o.upload)
	g.Get("/artifacts", o.listObjects)
	g.Get("/artifacts/:sha256", o.getObject)
	g.Delete("/artifacts/:sha256", o.deleteArtifact)
	g.Post("/jobs/claim", o.claim)
	g.Post("/jobs", o.createJob)
	g.Get("/jobs", o.listJobs)
	g.Get("/jobs/:id", o.getJob)
	g.Post("/jobs/:id/cancel", o.cancel)
	g.Post("/jobs/:id/publish", o.publish)
	g.Get("/jobs/:id/events", o.listEvents)
	g.Post("/jobs/:id/events", o.report)
	g.Get("/jobs/:id/metrics", o.metrics)
	g.Get("/jobs/:id/artifacts", o.listArtifacts)
	g.Post("/jobs/:id/artifacts", o.register)
	g.Post("/clients", o.createClient)
	g.Get("/clients", o.listClients)
	g.Get("/clients/:id", o.getClient)
	g.Delete("/clients/:id", o.deleteClient)
	g.Post("/clients/:id/forward_backward", o.forwardBackward)
	g.Post("/clients/:id/optim_step", o.optimStep)
	g.Post("/clients/:id/sample", o.sample)
	g.Post("/clients/:id/save_weights", o.saveWeights)
	capRoutes(app, o)
	policyRoutes(app, o)
}

// tenant is the caller's org store, from the validated principal, with every
// lapsed lease in it ended first.
func (o ops) tenant(ctx context.Context) (*store, string, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, "", err
	}
	ns, err := principal.OrgNamespace(org, "")
	if err != nil {
		return nil, "", err
	}
	st, err := o.s.State.stores.For(ns)
	if err != nil {
		o.s.Log.Error("train store open failed", "org", org, "err", err)
		return nil, "", zip.Errorf(http.StatusInternalServerError, "train store unavailable")
	}
	if err := st.expire(ctx, unix(), o.s.State.ttl); err != nil {
		return nil, "", o.fault("expire", org, err)
	}
	return st, org, nil
}

// ship makes a write durable before it is acknowledged; a replica that cannot is
// answered 503 so the caller retries where the org's writer is.
func (o ops) ship(org string) error {
	ns, err := principal.OrgNamespace(org, "")
	if err != nil {
		return err
	}
	acked, err := o.s.State.stores.Sync(ns)
	if err != nil || !acked {
		o.s.Log.Warn("train durable ship failed", "org", org, "err", err)
		return zip.Errorf(http.StatusServiceUnavailable, "train store failover in progress; retry")
	}
	return nil
}

func (o ops) fault(what, org string, err error) error {
	o.s.Log.Error("train "+what+" failed", "org", org, "err", err)
	return zip.Errorf(http.StatusInternalServerError, "train %s failed", what)
}

// missing is the one answer for an id this org does not have, whoever does.
func missing(what string) error { return zip.ErrNotFound(what + " not found") }

func conflict(code, msg string) error {
	return &zip.HTTPError{Status: http.StatusConflict, Code: code, Msg: msg}
}

// caller is the validated principal's subject.
func caller(ctx context.Context) string {
	id, _ := principal.SubjectFrom(ctx)
	return id
}

func project(ctx context.Context) string { return principal.ProjectFrom(ctx) }

// bell wakes long polls waiting on an org when its jobs change.
type bell struct {
	mu sync.Mutex
	ch map[string]chan struct{}
}

func newBell() *bell { return &bell{ch: map[string]chan struct{}{}} }

func (b *bell) wait(org string) <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.ch[org]
	if !ok {
		c = make(chan struct{})
		b.ch[org] = c
	}
	return c
}

func (b *bell) ring(org string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if c, ok := b.ch[org]; ok {
		close(c)
		delete(b.ch, org)
	}
}

// poll calls try until it reports done, the org's bell rings between tries, or
// wait elapses; a second between tries bounds what a bell on another replica
// costs.
func (o ops) poll(ctx context.Context, org string, wait time.Duration, try func() (bool, error)) error {
	deadline := time.Now().Add(wait)
	for {
		ring := o.s.State.bell.wait(org)
		ok, err := try()
		if err != nil || ok {
			return err
		}
		left := time.Until(deadline)
		if left <= 0 {
			return nil
		}
		t := time.NewTimer(min(left, time.Second))
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-ring:
		case <-t.C:
		}
		t.Stop()
	}
}

// catalog is GET /v1/train/models's answer.
type catalog struct {
	// Data is every base the caller can train, each with its methods, where it
	// runs and what a device-second costs there.
	Data []Family `json:"data"`
}

// ListTrainableModels answers each base the caller can train today, in one shape:
// the base, the methods it trains by (adaptations), where it runs (runs: Hanzo's
// devices or the org's machines) with what one device-second costs there in
// nano-USD, then the resources that run it (nouns), its executor and devices, and
// the protections, objective terms, outputs and dataset schemes it takes. Bases
// are ordered by name, a base's runs by place, then price. A create is validated
// against the same rows, and a noun the caller cannot start is not listed: the
// engine's clients are a SuperAdmin's.
func (o ops) models(ctx context.Context, _ *cloud.Unit) (*catalog, error) {
	super := cloud.Super.Admits(cloud.AuthorityIn(ctx))
	out := &catalog{Data: []Family{}}
	for _, f := range families {
		f.Nouns = slices.DeleteFunc(slices.Clone(f.Nouns), func(n string) bool { return n == "clients" && !super })
		if len(f.Nouns) == 0 {
			continue
		}
		runs := make([]Run, len(f.Runs))
		for i, r := range f.Runs {
			runs[i] = r.priced(ctx)
		}
		slices.SortStableFunc(runs, func(a, b Run) int {
			return cmp.Or(cmp.Compare(a.Where, b.Where), cmp.Compare(a.Price, b.Price))
		})
		f.Runs = runs
		out.Data = append(out.Data, f)
	}
	slices.SortFunc(out.Data, func(a, b Family) int { return cmp.Compare(a.Base, b.Base) })
	return out, nil
}

// priced is r with its price: the meter authority's row, else the floor, per
// device-second, rounded half up.
func (r Run) priced(ctx context.Context) Run {
	if r.meter == "" {
		return r
	}
	r.Price = (cloud.RateNano(ctx, "train", r.meter, r.floor*1_000) + r.span/2) / r.span
	return r
}
