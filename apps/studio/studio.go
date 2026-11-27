package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/orm/tenant"
	tasks "github.com/hanzoai/tasks/pkg/tasks"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	aipeer "github.com/hanzoai/cloud/client/ai"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/iam"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/spec"
)

// prefix is the capability's address.
const prefix = "/v1/studio"

// cloudUnit is the empty answer of an op that answers nothing (204).
type cloudUnit = cloud.Unit

// state is Studio's: its store, the planes it writes to, and the engine it runs on.
type state struct {
	db      *tenant.DB
	closeDB func() error
	bill    *cloud.Meter
	ai      infer.Client
	lib     shelf
	jobs    tracker
	fleet   fleet
	log     luxlog.Logger
	work    *durable
	cpu     lane
	gpu     lane
	bells   *bells
	// buy is the media call to the ai app (client.AIMedia).
	buy func(context.Context, *client.MediaIn) (*client.Chat, error)
	// poll is how often a lane part reads the job it follows.
	poll time.Duration
	// afford weighs cents against the payer's wallet: the meter's Authorize, a value so
	// a money test watches the ledger's question rather than a network client.
	afford func(ctx context.Context, p cloud.Payer, cents int64) error
}

var mounted *cloud.Service[*state]

// Use mounts /v1/studio and starts the run worker on this process's engine as soon
// as the engine is up, which resumes every run that was open when the process last
// stopped.
func Use(app cloud.Router, deps cloud.Deps) error {
	return useWith(app, deps, wiring{lib: planeLibrary{}, jobs: planeJobs{}, fleet: planeFleet, gpu: gpuLane{}, buy: aipeer.AIMedia, engine: cloud.EmbeddedTasks})
}

// wiring is what a deployment and a test differ in: the planes and the engine.
type wiring struct {
	lib    shelf
	jobs   tracker
	fleet  fleet
	gpu    lane
	buy    func(context.Context, *client.MediaIn) (*client.Chat, error)
	engine func() *tasks.Embedded
}

func useWith(app cloud.Router, deps cloud.Deps, w wiring) error {
	if app == nil {
		return errors.New("studio: nil app")
	}
	if cloud.DataDir() == "" {
		return fmt.Errorf("studio.Use: empty cloud.DataDir()")
	}
	b := cloud.NewBase(deps, "studio")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, closeDB, err := openDB(ctx, cloud.DataDir())
	if err != nil {
		return fmt.Errorf("studio.Use: %w", err)
	}
	st := &state{
		db: db, closeDB: closeDB, bill: b.Bill, ai: deps.AI, lib: w.lib, jobs: w.jobs, fleet: w.fleet,
		log: b.Log, cpu: cpuLane{engine: w.engine}, gpu: w.gpu, buy: w.buy, bells: newBells(), poll: 1500 * time.Millisecond,
	}
	st.afford = func(ctx context.Context, p cloud.Payer, cents int64) error {
		return b.Bill.Authorize(ctx, p.Wallet, p.Project, p.Validated, "run", cents)
	}
	st.work = newDurable(st, w.engine)
	s := &cloud.Service[*state]{Base: b, State: st}
	mounted = s
	routes(app, s)
	go st.work.boot()
	b.Log.Info("studio mounted", "prefix", prefix, "catalog", len(catalog()))
	return nil
}

// Shutdown stops the run worker — every run resumes from the engine's history on
// the next start — and closes the store.
func Shutdown(_ context.Context) error {
	if mounted == nil {
		return nil
	}
	mounted.State.work.stop()
	return mounted.State.closeDB()
}

// ops binds the service to the typed ops, so each op is a method value — the only
// bound form zipdoc lifts prose from.
type ops struct{ s *cloud.Service[*state] }

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

func routes(app cloud.Router, s *cloud.Service[*state]) {
	o := ops{s: s}
	g := app.Group(prefix)

	g.Get("/templates", o.templates)
	g.Post("/templates", o.templateCreate, zip.WithStatus(http.StatusCreated))
	g.Get("/templates/:id", o.templateGet)
	g.Patch("/templates/:id", o.templatePatch)
	g.Delete("/templates/:id", o.templateDelete)

	g.Get("/flows", o.flows)
	g.Post("/flows", o.flowCreate, zip.WithStatus(http.StatusCreated))
	g.Get("/flows/:id", o.flowGet)
	g.Patch("/flows/:id", o.flowPatch)
	g.Delete("/flows/:id", o.flowDelete)

	g.Get("/documents", o.documents)
	g.Post("/documents", o.documentCreate, zip.WithStatus(http.StatusCreated))
	g.Get("/documents/:id", o.documentGet)
	g.Patch("/documents/:id", o.documentPatch)
	g.Delete("/documents/:id", o.documentDelete)

	g.Get("/brands", o.brands)
	g.Post("/brands", o.brandCreate, zip.WithStatus(http.StatusCreated))
	g.Get("/brands/:id", o.brandGet)
	g.Patch("/brands/:id", o.brandPatch)
	g.Delete("/brands/:id", o.brandDelete)

	g.Post("/estimate", o.price)
	g.Get("/runs", o.runs)
	g.Post("/runs", o.create, zip.WithStatus(http.StatusCreated))
	g.Get("/runs/:id", o.run)
	g.Patch("/runs/:id", o.runPatch)
	g.Post("/runs/:id/cancel", o.cancel)
	g.Post("/runs/:id/retry", o.retry)
	g.Get("/runs/:id/versions", o.versions)
	g.Raw(http.MethodGet, "/runs/:id/events", o.events)

	g.Get("/gpus", o.gpus)

	g.Raw(http.MethodPost, "/chat", o.chat)
	g.Get("/threads", o.threads)
	g.Get("/threads/:id", o.threadGet)
	g.Delete("/threads/:id", o.threadDelete)

	g.Raw(http.MethodPost, "/render/*", s.State.door)
}

func init() {
	spec.Describe(prefix+"/render/*", http.MethodPost,
		"The render lane's door, for Studio's render workers alone",
		"The CPU render workers claim render jobs here, heartbeat them, ask for the upload of the one file a job "+
			"makes, and complete or fail them: the durable engine's own activity verbs, for the render lane only. "+
			"Admits the render workload's identity and answers 403 to every other caller, a person included.")
}

// who is the caller as Studio keys them: the context bound to their validated org
// for every store statement, and their person. A credential that names no person
// is refused: a row with no creator would be nobody's.
func (o ops) who(ctx context.Context) (context.Context, string, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, "", err
	}
	ctx = scoped(ctx, org)
	me, err := person(ctx)
	if err != nil {
		return nil, "", err
	}
	return ctx, me, nil
}

// person is the caller's subject: the IAM row the identity boundary vouched for.
func person(ctx context.Context) (string, error) {
	self, err := iam.SelfFrom(ctx)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(self.ID) == "" {
		return "", zip.ErrForbidden("this credential names no person: sign in, or use an API key of yours")
	}
	return self.ID, nil
}

// orgOf is the org ctx is bound to.
func orgOf(ctx context.Context) string {
	org, _ := tenantOf(ctx)
	return org
}

// track puts a run's state on the jobs board, as its person. The board is a view:
// a failure to reach it is logged and never fails the run.
func (s *state) track(ctx context.Context, run string) {
	if s.jobs == nil {
		return
	}
	r, ok, err := s.runRow(ctx, run)
	if err != nil || !ok {
		return
	}
	steps, err := s.db.Select[stepRow]("steps").Where(tenant.Eq("run", run)).All(ctx)
	if err != nil {
		return
	}
	done := 0
	var outputs []string
	read := map[string]bool{}
	for _, d := range depsOf(mustGraph(r)) {
		for _, x := range d {
			read[x] = true
		}
	}
	for _, st := range steps {
		if st.Status == succeeded {
			done++
			if !read[st.ID] {
				v := st.view()
				outputs = append(outputs, v.Outputs...)
			}
		}
	}
	state := map[string]string{queued: "scheduled", running: "running", succeeded: "succeeded", failed: "failed", canceled: "cancelled"}[r.Status]
	kind := "render"
	switch r.Kind {
	case "video", "film", "ad":
		kind = "video"
	}
	in := jobIn{
		Kind: kind, Title: r.Title, Ref: r.ID, Origin: origin(r.ID), State: state,
		Summary: fmt.Sprintf("%d of %d steps", done, len(steps)), Outputs: nonNil(outputs), Cost: r.Charged,
	}
	if len(steps) > 0 {
		in.Progress = float64(done) / float64(len(steps))
	}
	if r.Fault != "" {
		var p Problem
		if json.Unmarshal([]byte(r.Fault), &p) == nil {
			in.Error = firstLine(p.Detail)
		}
	}
	if err := s.jobs.track(s.as(ctx, r), in); err != nil {
		s.log.Debug("studio: the jobs board did not take a run's state", "run", run, "err", err)
	}
}
