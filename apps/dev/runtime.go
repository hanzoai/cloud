package dev

// runtime.go — the wasm runtime: the Dev loop (hanzo-inc/dev) hosted in cloud, for
// sessions that need no cell until a turn asks for one. A session here is a row in
// the action ledger, and the Dev environment protocol reaches it through an Adapter
// at the host boundary (HIP-1330 amendment).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"

	dev "github.com/hanzoai/dev/go"
	tasks "github.com/hanzoai/tasks/pkg/tasks"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/apps/dev/loop"
	"github.com/hanzoai/cloud/client"
	sandboxpeer "github.com/hanzoai/cloud/client/sandbox"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/principal"
)

// wasmPrefix marks a wasm session's id, so the browser's stream knows what answers it.
const wasmPrefix = "w_"

// devPrelude gives every Hanzo Dev coding session concise, progressive capability
// guidance without bloating the context window.
var devPrelude = "You are Hanzo Dev, a coding agent in the Hanzo ecosystem. " +
	"You can build, customize, and mod any part of Hanzo — apps, UI mods, extensions, skills, and MCP servers — " +
	"following Hanzo's open-source, local-first, and cloud-native philosophy. " +
	"Discover skills progressively on demand without loading the whole catalog: read https://hanzoskills.com/llms.txt " +
	"(or /catalog.json) -> fetch only the needed category (/categories/<slug>.md or .json) -> load /skills/<id>/SKILL.md " +
	"(or use /skill or POST /v1/tool/detect). Read HIP specifications on demand via https://hips.hanzo.ai/llms.txt."

// Runtime is the loop wired to cloud's stores, model gateway, sandbox and tasks engine.
type Runtime struct {
	loop *loop.Loop
	ai   infer.Client
	work *durable
	logf luxlog.Logger

	mu      sync.Mutex
	watches map[string]map[chan string]struct{}
}

// running is the runtime this process serves, set by Serve. Nil means no wasm runtime.
var running atomic.Pointer[Runtime]

// NewRuntime opens the core once, the ledger per org, and the worker once the engine is up.
func NewRuntime(b cloud.Base, ai infer.Client, engine func() *tasks.Embedded) (*Runtime, error) {
	core, err := dev.Open(context.Background())
	if err != nil {
		return nil, err
	}
	stores := cloud.NewOrgStore(b, "dev", loop.OpenStore)
	model := env.Or("DEV_MODEL", "enso")
	rt := &Runtime{ai: ai, logf: b.Log, watches: map[string]map[chan string]struct{}{}, loop: &loop.Loop{
		Core: core, Model: &model, Prelude: &devPrelude,
		Ship: func(org string) error {
			ns, err := principal.OrgNamespace(org, "")
			if err != nil {
				return err
			}
			acked, err := stores.Sync(ns)
			if err != nil {
				return err
			}
			if !acked {
				return errors.New("this replica is not the org's elected writer, so the ledger was not shipped")
			}
			return nil
		},
		Store: func(org string) (*loop.Store, error) {
			ns, err := principal.OrgNamespace(org, "")
			if err != nil {
				return nil, err
			}
			return stores.For(ns)
		},
	}}
	rt.work = newDurable(rt, engine)
	go rt.work.boot()
	return rt, nil
}

func (rt *Runtime) log(msg string, err error) {
	if rt.logf != nil {
		rt.logf.Error(msg, "error", err)
	}
}

// Serve makes rt the wasm runtime of this process.
func (rt *Runtime) Serve() { running.Store(rt) }

// Close stops the worker.
func (rt *Runtime) Close() { rt.work.stop() }

// hostFor is the world a session's work is done in: its person's model and cell, paid
// for by who (actingAs). What is shown reaches every watcher of the session.
func (rt *Runtime) hostFor(org string, who zip.Caller, session string) *host {
	return &host{ai: rt.ai, org: org, owner: who.User, who: who, model: env.Or("DEV_MODEL", "enso"),
		emit: func(t string) { rt.publish(session, t) }}
}

// Watch is what is shown on a session from now on, until cancel. A watcher that was
// away reads what it missed with Lines.
func (rt *Runtime) Watch(session string) (<-chan string, func()) {
	ch := make(chan string, 64)
	rt.mu.Lock()
	if rt.watches[session] == nil {
		rt.watches[session] = map[chan string]struct{}{}
	}
	rt.watches[session][ch] = struct{}{}
	rt.mu.Unlock()
	return ch, func() {
		rt.mu.Lock()
		delete(rt.watches[session], ch)
		rt.mu.Unlock()
	}
}

func (rt *Runtime) publish(session, text string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for ch := range rt.watches[session] {
		select {
		case ch <- text:
		default: // a watcher that does not keep up reads the rest from the record
		}
	}
}

// Lines is what was shown on a session after line n.
func (rt *Runtime) Lines(org, subject, session string, n int64) ([]loop.Line, error) {
	return rt.loop.Lines(org, subject, session, n)
}

// Submit takes a person's words as the session's next turn and sets its work going in
// the background. It answers as soon as the turn is recorded; what happens is shown
// to the session's watchers. A deployment whose worker is not up yet has still
// recorded the turn, and says so.
func (rt *Runtime) Submit(ctx context.Context, org, subject, session, prompt string) error {
	if err := rt.loop.Submit(ctx, org, subject, session, prompt); err != nil {
		return err
	}
	_, err := rt.work.start(ctx, driving(ctx, org, subject, session))
	return err
}

// Turn is Submit that waits for the turn to end, showing emit what is shown as it
// happens. It is what a caller with nothing to watch with uses.
func (rt *Runtime) Turn(ctx context.Context, org, subject, session, prompt string, emit func(string)) error {
	lines, cancel := rt.Watch(session)
	defer cancel()
	if err := rt.loop.Submit(ctx, org, subject, session, prompt); err != nil {
		return err
	}
	run, err := rt.work.start(ctx, driving(ctx, org, subject, session))
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- run.Get(ctx, nil) }()
	for {
		select {
		case t := <-lines:
			if emit != nil {
				emit(t)
			}
		case err := <-done:
			for {
				select {
				case t := <-lines:
					if emit != nil {
						emit(t)
					}
				default:
					return err
				}
			}
		}
	}
}

// Suspend puts a session away and gives its cell back; its disk stays.
func (rt *Runtime) Suspend(ctx context.Context, org, subject, session string) error {
	lease, err := rt.loop.Suspend(ctx, org, subject, session)
	if err != nil {
		return err
	}
	if lease == "" {
		return nil
	}
	if _, err := sandboxpeer.SandboxEnd(acting(org, subject), &client.EndIn{ID: lease}); err != nil && !gone(err) {
		return err
	}
	return rt.loop.Unleased(org, session, lease)
}

// Resume brings a suspended session back, and sets any work it left going again.
func (rt *Runtime) Resume(ctx context.Context, org, subject, session string) error {
	if err := rt.loop.Resume(org, subject, session); err != nil {
		return err
	}
	if p, err := rt.loop.Pending(org, subject, session); err != nil || !p {
		return err
	}
	_, err := rt.work.start(ctx, driving(ctx, org, subject, session))
	return err
}

// Interrupt stops the commands running in the session's cell and keeps the cell and
// the session. What the command had printed so far is the result the core is told.
func (rt *Runtime) Interrupt(org, subject, session string) (int, error) {
	lease, err := rt.loop.Lease(org, subject, session)
	if err != nil || lease == "" {
		return 0, err
	}
	out, err := sandboxpeer.SandboxStop(acting(org, subject), &client.StopIn{ID: lease})
	if err != nil {
		return 0, err
	}
	return out.Stopped, nil
}

// openWasm starts a session on the wasm runtime.
func openWasm(ctx context.Context) (*Session, error) {
	rt := running.Load()
	if rt == nil {
		return nil, zip.Errorf(http.StatusNotImplemented,
			"dev: the wasm runtime is not linked into this deployment (HIP-1330), so no session can run on it")
	}
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	sub, ok := principal.SubjectFrom(ctx)
	if !ok || sub == "" {
		return nil, zip.ErrForbidden("dev: a session belongs to a person, and this credential names none")
	}
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	id := wasmPrefix + hex.EncodeToString(b[:])
	if err := rt.loop.New(org, sub, id); err != nil {
		return nil, err
	}
	return &Session{ID: id, Label: "Hanzo cloud (chat)", Zap: zapURL(id)}, nil
}

// endWasm stops a wasm session and gives back the cell it leased, if any.
func endWasm(ctx context.Context, id string) error {
	rt := running.Load()
	if rt == nil {
		return zip.ErrNotFound("dev: no such session")
	}
	org, err := principal.Acting(ctx)
	if err != nil {
		return err
	}
	sub, _ := principal.SubjectFrom(ctx)
	lease, err := rt.loop.Stop(org, sub, id)
	if errors.Is(err, loop.ErrNoSession) {
		return zip.ErrNotFound("dev: no such session")
	}
	if err != nil {
		return err
	}
	if lease == "" {
		return nil
	}
	if _, err := sandboxpeer.SandboxEnd(acting(org, sub), &client.EndIn{ID: lease}); err != nil && !gone(err) {
		return err
	}
	return rt.loop.Ended(org, id, lease)
}
