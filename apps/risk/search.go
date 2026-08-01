package risk

// search.go bounds the one expensive thing this app will do on a caller's word.
//
// WHAT IT WAS. POST /v1/ml/search started a bare goroutine per call with a
// ten-minute budget, replaying 243 topologies over up to five thousand recorded
// decisions. Nothing bounded how many ran at once, nothing could stop one, and a
// rollout — Recreate at one replica — left the row saying `running` forever, which
// is the same bytes as a search about to answer and the opposite fact. One
// authenticated caller with a loop could hold every core on a shared pod.
//
// WHAT IT IS. A queue with a fixed number of workers, ONE run per tenant, a
// bounded backlog, a deadline, a cancel op, and a durable status that a restart
// resolves rather than abandons:
//
//	one per tenant   a second start answers 409 with the run already going, so a
//	                 loop costs one slot and not one per iteration.
//	workers          searchWorkers goroutines for the whole process. The grid is
//	                 CPU, and CPU is the pod's, not the caller's.
//	backlog          searchQueue deep. Full answers 429 — the honest word for
//	                 "come back", and one a client can act on.
//	deadline         searchBudget, enforced by the context every candidate polls.
//	cancel           DELETE /v1/ml/search/{id} stops it and says so on the row.
//	shutdown         teardown cancels every run and marks its row, so nothing
//	                 survives a rollout claiming to be in progress.
//	priced           gated before and metered after, on the caller's own ledger.

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"time"

	"github.com/zap-proto/zip"
)

const (
	// searchWorkers is how many searches run at once across the WHOLE process.
	// Two: the grid is seconds to minutes of one core, and a decision path
	// sharing the pod must keep its cores.
	searchWorkers = 2
	// searchQueue is how deep the backlog goes before a start is refused. Short
	// on purpose — a long queue turns a capacity refusal into a latency mystery.
	searchQueue = 8
	// searchBudget bounds one run. The grid is closed (243 candidates over at
	// most 5,000 events), so this is a ceiling for a pathological host and not
	// the thing that makes the work finite.
	searchBudget = 2 * time.Minute
)

// The durable statuses a search row carries. `running` is the only one a reader
// must be able to trust, which is why nothing may leave one behind.
const (
	searchRunning   = "running"
	searchDone      = "done"
	searchRefused   = "refused"
	searchCancelled = "cancelled"
)

// runner is the process's search queue. One for the app, built by build() and
// stopped by teardown, so nothing here is a package global that a second mount
// would share with the first.
type runner struct {
	jobs chan job
	log  logger

	mu      sync.Mutex
	running map[Tenant]*run
	stopped bool
	wg      sync.WaitGroup
}

// job is one queued search: everything the worker needs, resolved at start time
// so the worker touches no request.
type job struct {
	t   Tenant
	id  string
	db  *sql.DB
	obs []observation
}

// run is a search in flight, and the handle that stops it.
type run struct {
	id     string
	cancel context.CancelFunc
}

func newRunner(log logger) *runner {
	r := &runner{jobs: make(chan job, searchQueue), log: log, running: map[Tenant]*run{}}
	for i := 0; i < searchWorkers; i++ {
		r.wg.Add(1)
		go r.work()
	}
	return r
}

// errBusy and errBacklog are the two refusals, and they are DIFFERENT statuses
// because they are different facts: one says this tenant already has a search
// going, the other says the node does. A client retries the second and reads the
// first.
var (
	errBusy    = zip.Errorf(409, "this tenant already has a search running; read it or cancel it before starting another")
	errBacklog = zip.Errorf(429, "this node's search queue is full; retry shortly")
)

// start queues one search. It refuses rather than queues when the tenant already
// has one going, so a caller in a loop occupies one slot forever instead of
// filling the backlog and then the pod.
func (r *runner) start(j job) error {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return zip.Errorf(503, "this node is shutting down and is not starting searches")
	}
	if _, going := r.running[j.t]; going {
		r.mu.Unlock()
		return errBusy
	}
	// Claimed BEFORE the send, so two concurrent starts cannot both queue.
	r.running[j.t] = &run{id: j.id}
	r.mu.Unlock()

	select {
	case r.jobs <- j:
		return nil
	default:
		r.mu.Lock()
		delete(r.running, j.t)
		r.mu.Unlock()
		return errBacklog
	}
}

// cancel stops a tenant's run if the named one is the one going. Answers whether
// it did: cancelling a search that already finished is not an error, and telling
// the caller which happened is the whole content of the reply.
//
// A run still in the QUEUE has no cancel function yet — no worker has reached
// it. Dropping the claim is how that one is cancelled: the worker that picks the
// job up finds no claim and writes the cancelled row instead of doing the work.
// Handling only the started case would leave a queued search uncancellable
// precisely while the queue is full, which is when it matters.
func (r *runner) cancel(t Tenant, id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	going, ok := r.running[t]
	if !ok || going.id != id {
		return false
	}
	if going.cancel != nil {
		going.cancel()
		return true
	}
	delete(r.running, t)
	return true
}

// work is one worker. It runs until the queue closes.
func (r *runner) work() {
	defer r.wg.Done()
	for j := range r.jobs {
		r.execute(j)
	}
}

func (r *runner) execute(j job) {
	ctx, cancel := context.WithTimeout(context.Background(), searchBudget)
	defer cancel()

	r.mu.Lock()
	if going, ok := r.running[j.t]; ok && going.id == j.id {
		going.cancel = cancel
	} else {
		// Cancelled or superseded before a worker picked it up.
		r.mu.Unlock()
		record := searchReport{Refusal: "cancelled before it started"}
		r.write(j, searchCancelled, record)
		return
	}
	r.mu.Unlock()

	rep, err := searchRun(ctx, j.t, j.obs)
	status := searchDone
	if err != nil {
		status = searchRefused
		if ctx.Err() != nil {
			status = searchCancelled
		}
		rep.Refusal = err.Error()
	}
	r.write(j, status, rep)

	r.mu.Lock()
	if going, ok := r.running[j.t]; ok && going.id == j.id {
		delete(r.running, j.t)
	}
	r.mu.Unlock()
}

func (r *runner) write(j job, status string, rep searchReport) {
	b, _ := json.Marshal(rep)
	if err := putSearch(j.db, j.id, status, b); err != nil {
		r.log.Error("risk: a search report could not be kept", "search", j.id, "err", err)
	}
}

// stop cancels every run and waits for the workers. A search that was in flight
// leaves a `cancelled` row rather than a `running` one, because a rollout must
// not be the reason a reader is told a search is still going.
func (r *runner) stop() {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return
	}
	r.stopped = true
	for _, going := range r.running {
		if going.cancel != nil {
			going.cancel()
		}
	}
	close(r.jobs)
	r.mu.Unlock()
	r.wg.Wait()
}

// abandonSearches resolves rows left `running` by a process that is gone.
//
// It runs when a tenant's file is opened, which is the first moment anyone could
// read one. Marked `cancelled` and not `refused`: nothing about the search was
// wrong, the process serving it stopped — and cloud deploys Recreate at one
// replica, so that is not an exceptional case but every single rollout.
func abandonSearches(db *sql.DB) error {
	body, err := json.Marshal(searchReport{
		Refusal: "the process running this search stopped before it finished; start it again",
	})
	if err != nil {
		return err
	}
	_, err = db.Exec(`UPDATE search SET status = ?, body = ? WHERE status = ?`,
		searchCancelled, string(body), searchRunning)
	return err
}
