package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/internal/runs"
	"github.com/hanzoai/cloud/sdk/coord"
)

// runs.go — the runs executing on THIS pod, and the other pods' view of them.
//
// A run's context is cancelled to stop it, and the cancel lives in the memory of
// the pod running it. A stop, a pause, or the reaper's question "is anything
// running this?" lands on whichever pod took the request or won the sweep, and
// that pod is not the one running it two times in three. So the registry answers
// for its own pod, and for the others over the bus (sdk/coord): a stop is
// asked of the pods until the one that runs the session cancels it, and "who
// holds what" is asked of all of them.

// registry holds the cancel of every run executing in this pod, by session. It is
// how a stop reaches a run: cancelling its context stops the model call and every
// tool call under it. The run removes itself when it ends.
type registry struct {
	sync.Mutex
	m map[string]context.CancelCauseFunc

	bus   *coord.Coord // the deployment's coord when it spans pods (attach), else nil
	leave func()
}

func newRegistry() *registry { return &registry{m: map[string]context.CancelCauseFunc{}} }

// The questions the registry answers for the other pods. What a pod is running is
// runs.Held, which every process that executes runs answers.
const runsStop = "agents.interrupt"

// orphanClaim is how long the right to carry on a lost session is held: longer than
// the sweep that finds it takes to start the run, shorter than a run can be lost again.
const orphanClaim = 10 * time.Minute

// stopCall is an interrupt as another pod is asked it: a pause, or a stop and why.
type stopCall struct {
	ID     string `json:"id"`
	Pause  bool   `json:"pause,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// callOf says cause as a question to another pod: a run is paused with ErrPaused
// and stopped with ErrStopped, wrapped around the reason.
func callOf(id string, cause error) stopCall {
	if errors.Is(cause, ErrPaused) {
		return stopCall{ID: id, Pause: true}
	}
	return stopCall{ID: id, Reason: strings.TrimPrefix(cause.Error(), ErrStopped.Error()+": ")}
}

// cause is the error the run's context carries.
func (c stopCall) cause() error {
	if c.Pause {
		return ErrPaused
	}
	return fmt.Errorf("%w: %s", ErrStopped, c.Reason)
}

// attach makes the registry answer for the other pods, or for none: a coord that
// answers for this process alone leaves it as it was. Called again, it stops
// answering as before.
func (r *registry) attach(c *coord.Coord) {
	r.Lock()
	leave := r.leave
	r.bus, r.leave = nil, nil
	r.Unlock()
	if leave != nil {
		leave()
	}
	if c == nil || !c.Shared() {
		return
	}
	stopHeld := c.Serve(runs.Held, func(context.Context, string, []byte) ([]byte, bool) {
		return runs.Answer(r.ids())
	})
	stopStop := c.Serve(runsStop, func(_ context.Context, _ string, body []byte) ([]byte, bool) {
		var call stopCall
		if json.Unmarshal(body, &call) != nil {
			return nil, false
		}
		return nil, r.interruptHere(call.ID, call.cause())
	})
	r.Lock()
	r.bus = c
	r.leave = func() { stopHeld(); stopStop() }
	r.Unlock()
}

func (r *registry) coord() *coord.Coord {
	r.Lock()
	defer r.Unlock()
	return r.bus
}

func (r *registry) hold(id string, cancel context.CancelCauseFunc) bool {
	r.Lock()
	defer r.Unlock()
	if _, ok := r.m[id]; ok {
		return false
	}
	r.m[id] = cancel
	return true
}

func (r *registry) release(id string) {
	r.Lock()
	delete(r.m, id)
	r.Unlock()
}

// ids are the sessions this pod is running.
func (r *registry) ids() []string {
	r.Lock()
	defer r.Unlock()
	out := make([]string, 0, len(r.m))
	for id := range r.m {
		out = append(out, id)
	}
	return out
}

// Held is every session with a run executing anywhere in the deployment: this
// process's, and every other pod's. See [registry.held].
func Held(ctx context.Context) (map[string]bool, error) { return running.held(ctx) }

// interruptHere cancels the run executing session id with cause, if one is in this
// pod.
func (r *registry) interruptHere(id string, cause error) bool {
	r.Lock()
	cancel, ok := r.m[id]
	r.Unlock()
	if ok {
		cancel(cause)
	}
	return ok
}

// interrupt cancels the run executing session id with cause, wherever in the
// deployment it executes, and says whether one did. A pod that could not be asked
// may have been running it: that is not the answer "no", and is logged as a
// failure to reach it.
func (r *registry) interrupt(ctx context.Context, id string, cause error) bool {
	if r.interruptHere(id, cause) {
		return true
	}
	c := r.coord()
	if c == nil {
		return false
	}
	b, err := json.Marshal(callOf(id, cause))
	if err != nil {
		return false
	}
	_, ok, err := c.Call(ctx, runsStop, b)
	if err != nil && mounted != nil {
		mounted.Log.Warn("agents: could not ask every pod whether it runs a session", "session", id, "err", err)
	}
	return ok
}

// held is every session with a run executing anywhere in the deployment, and an
// error when the other pods could not all be asked: a run may be executing that
// this pod cannot see, and the one thing it must not do then is say none is.
func (r *registry) held(ctx context.Context) (map[string]bool, error) {
	out, err := runs.Others(ctx, r.coord())
	for _, id := range r.ids() {
		out[id] = true
	}
	return out, err
}

// claim says whether this pod is the one to carry on x, a session no pod is running:
// resume its run, or end it. Every pod that finds x lost asks, and the deployment
// answers yes once. x is named by when it was last heard from, so the claim is for
// this loss of it and not for the next: a run that is resumed and lost again is a
// session heard from since.
func (r *registry) claim(ctx context.Context, x Session) (bool, error) {
	c := r.coord()
	if c == nil {
		return true, nil
	}
	return c.Once(ctx, "orphan", x.ID+"/"+strconv.FormatInt(x.UpdatedAt, 10), orphanClaim)
}
