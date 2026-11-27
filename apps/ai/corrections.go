// Copyright © 2026 Hanzo AI. MIT License.

package ai

// corrections.go — a decision's corrections and its captured states.
//
// /v1/decisions is ai's address by the Decisions wire, so a correction of a
// decision is filed here, at /v1/decisions/:decision/corrections; the org's
// corrections, captures and capabilities are train's store (apps/train
// captures.go), so every op here asks train over the plane as its caller, who
// never names an org. The states Kai captured of an org's decision reach train
// the same way, off the request path, and only for an org that captures: the
// caller is never answered them (hanzoai/ai controllers/decisions.go).

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/hanzoai/cloud/client"
	trainpeer "github.com/hanzoai/cloud/client/train"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/hanzoai/cloud/sdk/principal"
	aiobject "github.com/hanzoai/ai/object"
	"github.com/zap-proto/zip"
)

// mountCorrections registers a decision's corrections beside the decision.
func mountCorrections(app *zip.App) {
	g := app.Group("/v1/decisions/:decision/corrections")
	g.Post("", correct, zip.WithOperationID("decisionsCorrect"), zip.WithStatus(201))
	g.Get("", corrections, zip.WithOperationID("decisionsCorrections"))
	g.Delete("/:id", uncorrect, zip.WithOperationID("decisionsUncorrect"))
}

// asCaller is the caller of this request, in the org it acts for, delegated to
// train.
func asCaller(ctx context.Context) (context.Context, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	return plane.For(ctx, org), nil
}

// CorrectDecision files a correction of one question of one of the org's Kai
// decisions: the right answer, which must be one of that question's options and
// not what Kai answered, and why. A decision Kai captured can teach a capability
// of its question (/v1/capabilities); one made with capture off is kept with
// state missing and cannot. The org's first correction turns capture on, so its
// next decisions can teach. Refused: 409 not_wrong when the answer is Kai's own,
// 409 corrected when the question already has a correction, 404 when the
// decision captured no such question.
//
// Example: {"question": "intent", "answer": "refund", "note": "the customer asked for their money back"}
func correct(ctx context.Context, in *client.CorrectIn) (*client.Correction, error) {
	cx, err := asCaller(ctx)
	if err != nil {
		return nil, err
	}
	return trainpeer.TrainCorrect(cx, in)
}

// ListDecisionCorrections answers one of the org's decisions' corrections, each
// captured (it can teach) or missing (the decision was made with capture off).
func corrections(ctx context.Context, in *client.DecisionIn) (*client.Corrections, error) {
	cx, err := asCaller(ctx)
	if err != nil {
		return nil, err
	}
	return trainpeer.TrainCorrections(cx, in)
}

// DeleteDecisionCorrection deletes one correction of one of the org's decisions.
// A capability already taught from it keeps its versions.
func uncorrect(ctx context.Context, in *client.UncorrectIn) (*cloud.Unit, error) {
	cx, err := asCaller(ctx)
	if err != nil {
		return nil, err
	}
	return trainpeer.TrainUncorrect(cx, in)
}

// capturing answers, per org, whether its decisions capture their states: train's
// setting, held for a few seconds so a burst of decisions asks once.
type capturing struct {
	mu   sync.Mutex
	held map[string]heldCapture
}

type heldCapture struct {
	on bool
	at time.Time
}

// captureTTL is how long an org's setting is held: a switch turned on reaches
// its decisions within it.
const captureTTL = 2 * time.Second

func (c *capturing) on(ctx context.Context, org string) bool {
	c.mu.Lock()
	h, ok := c.held[org]
	c.mu.Unlock()
	if ok && time.Since(h.at) < captureTTL {
		return h.on
	}
	out, err := trainpeer.TrainCapturing(plane.For(context.WithoutCancel(ctx), org))
	on := err == nil && out.On
	if err != nil {
		slog.Warn("ai: capture setting unread; this decision captures nothing", "org", org, "err", err)
	}
	c.mu.Lock()
	if c.held == nil {
		c.held = map[string]heldCapture{}
	}
	c.held[org] = heldCapture{on: on, at: time.Now()}
	c.mu.Unlock()
	return on
}

// captureDepth bounds the captures waiting for train; past it a capture is
// dropped and said so, never waited on by a decision.
const captureDepth = 256

// filer hands each capture to train off the request path.
type filer struct {
	once sync.Once
	q    chan aiobject.Capture
}

func (f *filer) file(_ context.Context, c aiobject.Capture) {
	f.once.Do(func() {
		f.q = make(chan aiobject.Capture, captureDepth)
		for range 4 {
			go func() {
				for c := range f.q {
					in := client.CaptureIn{Decision: c.Decision, Request: c.Request, Model: c.Model, States: c.States}
					ctx, cancel := context.WithTimeout(plane.For(context.Background(), c.Org), 30*time.Second)
					if _, err := trainpeer.TrainCapture(ctx, &in); err != nil {
						slog.Warn("ai: a decision's states were not kept", "org", c.Org, "decision", c.Decision, "err", err)
					}
					cancel()
				}
			}()
		}
	})
	select {
	case f.q <- c:
	default:
		slog.Warn("ai: captures are backed up; a decision's states were dropped", "org", c.Org, "decision", c.Decision)
	}
}

// useCapture installs ai's capture hooks: which orgs capture, and where their
// decisions' states go.
func useCapture() {
	var c capturing
	var f filer
	aiobject.SetCapture(c.on, f.file)
}
