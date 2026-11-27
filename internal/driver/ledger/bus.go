package ledger

// bus.go — a debit reaches the ledger on the bus, so the call that incurred it
// never waits for the books.
//
// Post is the request path's half. It seals the debit (its Ref, the ledger's
// idempotency key, minted here) and writes it to the USAGE stream, returning once
// the stream has it on disk: that PubAck is the durable record. Drain is the
// ledger's half, run by the process that owns the books: it reads the stream in
// batches and posts each org's debits in one transaction and one ship. A message
// seen twice — a publish retried after a lost ack, a batch redelivered after a
// crash between its ship and its ack — carries the same Ref and moves money once.
//
// The bus answers anyone who can reach its port, so a debit carries a MAC under a
// key derived from the KMS master every cloud process holds, and the ledger posts
// nothing it cannot verify.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/internal/driver/pubsub/stream"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk/bus"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/meter"
)

const (
	usageStream   = "USAGE"
	usageSubject  = "usage.debit"
	ledgerDurable = "ledger"
	sigHeader     = "Hanzo-Sig"
	postTimeout   = 2 * time.Second
	drainBatch    = 256
)

// usageStreamConfig is a hand-off queue with one reader, the ledger: an acked debit
// leaves it, and a full stream refuses new ones rather than drop an unposted one.
var usageStreamConfig = jetstream.StreamConfig{
	Replicas:   bus.Replicas(),
	Name:       usageStream,
	Subjects:   []string{usageSubject},
	Storage:    jetstream.FileStorage,
	Retention:  jetstream.WorkQueuePolicy,
	Discard:    jetstream.DiscardNew,
	MaxBytes:   bus.Usage.Ceiling,
	Duplicates: time.Hour,
}

// usageStreamFor is that configuration sized for the bus it is declared on: the
// ceiling, or the usage stream's share of a file store too small to hold it
// (bus.Usage), read from the account the bus reports.
func usageStreamFor(ctx context.Context, js jetstream.JetStream) jetstream.StreamConfig {
	cfg := usageStreamConfig
	if acct, err := js.AccountInfo(ctx); err == nil && acct != nil {
		cfg.MaxBytes = bus.Usage.Of(acct.Limits.MaxStore)
	}
	return cfg
}

// debit is one debit on the wire: the plane's RecordIn, which names the Ref, and
// the org whose books it is written to.
type debit struct {
	Org string `json:"org"`
	client.RecordIn
}

// ensured says this process has declared the usage stream; a failed publish clears
// it, so a stream lost with its server is declared again.
var ensured atomic.Bool

func usageBus(ctx context.Context) (jetstream.JetStream, error) {
	js, _, err := stream.Bus()
	if err != nil {
		return nil, err
	}
	if !ensured.Load() {
		if _, err := js.CreateOrUpdateStream(ctx, usageStreamFor(ctx, js)); err != nil {
			return nil, fmt.Errorf("stream %s: %w", usageStream, err)
		}
		ensured.Store(true)
	}
	return js, nil
}

// Post records one served call durably and returns: the usage stream holds it when
// Post returns nil, and the ledger's Drain posts it. When the stream cannot take it
// the call is posted to the ledger directly under the same Ref, so it is neither
// dropped nor, if the stream took it after all, posted twice. A $0 call is counted
// like any other; a negative one is refused.
func (c *books) Post(ctx context.Context, u meter.Usage) error {
	if u.Ref != "" && debitKey() != nil && publish(ctx, u) == nil {
		return nil
	}
	_, err := c.Record(ctx, u)
	return err
}

func publish(ctx context.Context, u meter.Usage) error {
	data, err := json.Marshal(debit{Org: u.Org, RecordIn: client.RecordIn{
		Subject: u.User,
		Amount:  client.Amount(moneyOf(u).Unwrap()),
		Usage:   wire(u),
	}})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, postTimeout)
	defer cancel()
	js, err := usageBus(ctx)
	if err != nil {
		return err
	}
	msg := &nats.Msg{Subject: usageSubject, Data: data, Header: nats.Header{}}
	msg.Header.Set(sigHeader, mac(data))
	if _, err = js.PublishMsg(ctx, msg, jetstream.WithMsgID(u.Ref)); err != nil {
		ensured.Store(false)
	}
	return err
}

// debitKey is the MAC key for debits on the bus: derived from the process's KMS
// master, nil when none is configured, and then no debit travels on the bus.
func debitKey() []byte {
	master, err := base64.StdEncoding.DecodeString(strings.TrimSpace(env.Or("CLOUD_KMS_MASTER_KEY_REF", "")))
	if err != nil || len(master) != 32 {
		return nil
	}
	h := hmac.New(sha256.New, master)
	h.Write([]byte("hanzo usage.debit"))
	return h.Sum(nil)
}

func mac(data []byte) string {
	h := hmac.New(sha256.New, debitKey())
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func signed(m jetstream.Msg) bool {
	if debitKey() == nil {
		return false
	}
	got, err := hex.DecodeString(m.Headers().Get(sigHeader))
	if err != nil {
		return false
	}
	want, _ := hex.DecodeString(mac(m.Data()))
	return hmac.Equal(got, want)
}

// Drain posts the debits on the usage stream to the ledger until ctx ends. The
// process that owns the books runs it; logf reports what it could not post.
func (c *books) Drain(ctx context.Context, logf func(msg string, kv ...any)) {
	for ctx.Err() == nil {
		if err := c.drain(ctx, logf); err != nil && ctx.Err() == nil {
			logf("metering: the usage drain stopped, starting again", "err", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}

func (c *books) drain(ctx context.Context, logf func(string, ...any)) error {
	js, err := usageBus(ctx)
	if err != nil {
		return err
	}
	cons, err := js.CreateOrUpdateConsumer(ctx, usageStream, jetstream.ConsumerConfig{
		Durable:       ledgerDurable,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       time.Minute,
		MaxDeliver:    -1,
		MaxAckPending: 4 * drainBatch,
	})
	if err != nil {
		return fmt.Errorf("consumer %s: %w", ledgerDurable, err)
	}
	for ctx.Err() == nil {
		batch, err := cons.Fetch(drainBatch, jetstream.FetchMaxWait(time.Second))
		if err != nil {
			return err
		}
		var msgs []jetstream.Msg
		for m := range batch.Messages() {
			msgs = append(msgs, m)
		}
		if err := batch.Error(); err != nil && !errors.Is(err, jetstream.ErrNoMessages) && !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		c.post(ctx, msgs, logf)
	}
	return ctx.Err()
}

// batcher is the ledger that can post one org's debits in one transaction and one ship.
type batcher interface {
	RecordUsages(ctx context.Context, ins []money.UsageInput) error
}

type posting struct {
	msg jetstream.Msg
	u   meter.Usage
}

// post writes msgs to the ledger, one batch per org's books, acking each once the
// ledger holds it. A batch that fails is posted one debit at a time, so one that can
// never land (its Ref names a different charge) is set aside without holding back
// the rest, and one that failed for now is delivered again.
func (c *books) post(ctx context.Context, msgs []jetstream.Msg, logf func(string, ...any)) {
	books := map[string][]posting{}
	var order []string
	for _, m := range msgs {
		if !signed(m) {
			logf("metering: a usage message no cloud process signed was set aside", "data", string(m.Data()))
			_ = m.Term()
			continue
		}
		u, err := decode(m.Data())
		if err != nil {
			logf("metering: a usage message that is not a debit was set aside", "err", err, "data", string(m.Data()))
			_ = m.Term()
			continue
		}
		if books[u.Org] == nil {
			order = append(order, u.Org)
		}
		books[u.Org] = append(books[u.Org], posting{msg: m, u: u})
	}
	b, _ := finance.Current().(batcher)
	for _, org := range order {
		ps := books[org]
		if b != nil {
			ins := make([]money.UsageInput, len(ps))
			for i, p := range ps {
				ins[i] = c.input(p.u)
			}
			if err := b.RecordUsages(ctx, ins); err == nil {
				for _, p := range ps {
					_ = p.msg.Ack()
				}
				continue
			}
		}
		for _, p := range ps {
			_, err := c.Record(ctx, p.u)
			switch {
			case err == nil:
				_ = p.msg.Ack()
			case errors.Is(err, finance.ErrRefReused):
				logf("metering: a debit whose ref names a different charge was set aside", "org", org, "ref", p.u.Ref, "err", err)
				_ = p.msg.Term()
			default:
				logf("metering: a debit could not be posted, delivering it again", "org", org, "ref", p.u.Ref, "err", err)
				_ = p.msg.NakWithDelay(5 * time.Second)
			}
		}
	}
}

// input is the ONE reading of a Usage as the ledger's input, for the co-resident
// Record and the drain alike.
func (c *books) input(u meter.Usage) money.UsageInput {
	return money.UsageInput{
		Org: u.Org, Subject: u.User, Amount: moneyOf(u), Currency: u.Currency,
		Model: u.Model, Provider: u.Provider, Project: u.Project, Service: u.Service,
		Ref: u.Ref, Cash: u.Cash,
		Cost: money.FromDecimal(u.Cost.Decimal()), Class: u.Class, Units: u.WorkUnits(), PaidBy: u.PaidBy,
		Actor: u.Actor, Key: u.Key,
	}
}

// wire is the ONE reading of a Usage as the plane's attribution, for the record op
// and the usage stream alike. The act's name and the correlation id cross as two
// different things: the receiver keys the debit on the ref, so a crossing that
// dropped it would turn a re-drive of ONE act into a SECOND debit. Who acted and what
// work was priced cross too, so the amount can be re-derived and not merely re-read.
func wire(u meter.Usage) client.Usage {
	w := client.Usage{
		Model: u.Model, Provider: u.Provider, Project: u.Project, Service: u.Service,
		Ref: u.Ref, RequestID: u.RequestID, ClientIP: u.ClientIP, Actor: u.Actor,
		PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens, TotalTokens: u.TotalTokens,
		Cash: u.Cash, Class: u.Class, Units: u.WorkUnits(), PaidBy: u.PaidBy, Key: u.Key,
	}
	if !u.Cost.IsZero() {
		cost := client.Amount(money.FromDecimal(u.Cost.Decimal()).Unwrap())
		w.Cost = &cost
	}
	return w
}

func decode(data []byte) (meter.Usage, error) {
	var d debit
	if err := json.Unmarshal(data, &d); err != nil {
		return meter.Usage{}, err
	}
	amt, err := d.Amount.Parse()
	if err != nil {
		return meter.Usage{}, err
	}
	if d.Org == "" || d.Subject == "" || d.Usage.Ref == "" {
		return meter.Usage{}, errors.New("a debit names an org, a subject and a ref")
	}
	cost, err := client.CostOf(d.Usage.Cost)
	if err != nil {
		return meter.Usage{}, err
	}
	return meter.Usage{
		User: d.Subject, Org: d.Org, Amount: money.FromDecimal(amt.Decimal()).Unwrap(), Currency: amt.Currency().Code,
		Model: d.Usage.Model, Provider: d.Usage.Provider, Project: d.Usage.Project, Service: d.Usage.Service,
		Ref: d.Usage.Ref, RequestID: d.Usage.RequestID, ClientIP: d.Usage.ClientIP, Actor: d.Usage.Actor,
		PromptTokens: d.Usage.PromptTokens, CompletionTokens: d.Usage.CompletionTokens, TotalTokens: d.Usage.TotalTokens,
		Cash: d.Usage.Cash, Cost: money.FromDecimal(cost.Decimal()).Unwrap(), Class: d.Usage.Class, Units: d.Usage.Units, PaidBy: d.Usage.PaidBy, Key: d.Usage.Key,
	}, nil
}
