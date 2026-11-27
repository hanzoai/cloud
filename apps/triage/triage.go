// Package triage closes the loop from an error to its fix. A fault counted past its
// threshold becomes one issue in the repository that owns it; an issue labelled for
// it becomes a run; the run's pull request merges itself when it is green and no
// path in it needs a person; a fix that does not hold comes back as the same issue.
// Every act is the actor's, on the audit trail, and stops at the kill switch. What
// each of those is, is set in admin and never here.
package triage

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/hanzoai/cloud/apps/event"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/bus"
	"github.com/hanzoai/commerce/infra"
	nats "github.com/hanzoai/pubsub-go"
	"github.com/hanzoai/pubsub-go/jetstream"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

type state struct {
	store *Store
	audit *audit.Recorder
	log   luxlog.Logger
	now   func() time.Time
	// asked is the (fault, release) pairs already checked for a regression.
	asked sync.Map
}

var (
	mounted *cloud.Service[state]
	cancel  context.CancelFunc
	done    sync.WaitGroup
)

// Use mounts the admin surface, the plane op GitHub's deliveries arrive on, and the
// consumer of the error stream.
func Use(app cloud.Router, deps cloud.Deps) error {
	store, err := openStore(cloud.DataDir())
	if err != nil {
		return err
	}
	base := cloud.NewBase(deps, "triage")
	s := &cloud.Service[state]{Base: base, State: state{store: store, audit: deps.Audit, log: base.Log, now: time.Now}}
	mounted = s
	o := ops{s}
	g := app.Group("/v1/admin/triage")
	g.Get("/config", o.config)
	g.Put("/config", o.setConfig)
	g.Get("/faults", o.faults)
	g.Get("/work", o.work)
	cloud.Plane().Post("/triage/github", planeGitHub,
		zip.WithOperationID(client.TriageGitHub),
		zip.WithSummary("Tell triage one verified GitHub delivery"))

	var ctx context.Context
	ctx, cancel = context.WithCancel(context.Background())
	done.Add(1)
	go func() {
		defer done.Done()
		consume(ctx, s)
	}()
	return nil
}

// Shutdown stops the consumer and closes the ledger.
func Shutdown(context.Context) error {
	if cancel != nil {
		cancel()
		done.Wait()
	}
	if mounted != nil {
		return mounted.State.store.Close()
	}
	return nil
}

func planeGitHub(ctx context.Context, in *client.GitHubDelivery) (*client.TriageTold, error) {
	org := cloud.Who(ctx).Org
	if org == "" || mounted == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "triage: not serving")
	}
	acted, err := mounted.State.told(ctx, org, in)
	return &client.TriageTold{Acted: acted}, err
}

const durable = "triage"

// consume reads the error stream until ctx ends, reconnecting when the bus or the
// durable goes away. A fact that cannot be handled goes back to the stream.
func consume(ctx context.Context, s *cloud.Service[state]) {
	for ctx.Err() == nil {
		cl, err := infra.NewPubSubClient(ctx, &infra.PubSubConfig{URL: bus.URL(), Name: durable, EnableJetStream: true, MaxReconnects: -1})
		if err == nil {
			err = listen(ctx, &s.State, cl)
			_ = cl.Close()
		}
		if err != nil && ctx.Err() == nil {
			s.Log.Warn("triage: the error stream ended, reconnecting", "err", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}
}

func listen(ctx context.Context, st *state, cl *infra.PubSubClient) error {
	if err := event.EnsureEventStream(ctx, cl); err != nil {
		return err
	}
	if _, err := cl.CreateConsumer(ctx, event.EventStream, &infra.ConsumerConfig{
		Name: durable, Durable: durable, Description: "triage: count faults and file their issues",
		FilterSubject: event.ErrorSubject, DeliverPolicy: infra.DeliverNew, AckPolicy: infra.AckExplicit,
		AckWait: time.Minute, MaxDeliver: 5,
	}); err != nil {
		return err
	}
	cons, err := cl.JetStream().Consumer(ctx, event.EventStream, durable)
	if err != nil {
		return err
	}
	cc, err := cons.Consume(func(m jetstream.Msg) {
		if f, ok := event.DecodeFault(m.Data()); ok && st.observe(ctx, f) != nil {
			_ = m.NakWithDelay(30 * time.Second)
			return
		}
		_ = m.Ack()
	}, jetstream.ConsumeErrHandler(func(cc jetstream.ConsumeContext, err error) {
		if errors.Is(err, nats.ErrNoResponders) || errors.Is(err, jetstream.ErrConsumerDeleted) || errors.Is(err, jetstream.ErrConsumerNotFound) {
			cc.Stop()
		}
	}))
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		cc.Stop()
		<-cc.Closed()
		return nil
	case <-cc.Closed():
		return errors.New("the subscription ended")
	}
}
