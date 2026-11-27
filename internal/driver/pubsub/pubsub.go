package pubsub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/hanzoai/cloud/sdk/coord"
)

// A process that links this package carries the bus on the cluster
// CLOUD_PUBSUB_URL names.
func init() {
	coord.Register(coord.Driver{Bus: func(url, name string) (coord.Bus, error) {
		b, err := newNATSBus(url, name)
		if err != nil {
			return nil, err
		}
		return b, nil
	}})
}

// natsBus carries the bus on the deployment's shared hanzoai/pubsub cluster (NATS core
// subjects), where the KV carried it before. The sealing, freshness and replay window
// are bus.go's and do not change; this file only moves frames.
//
// Subjects. A channel is "coord:<deployment>:bus:<subject>", which holds characters a
// NATS subject cannot ('#', '/', ':' are fine but '.', '*', '>' and spaces are
// wildcards or separators), so the channel is named by a digest: one token under
// "coord.", which is also the one pattern ("coord.>") a client's permission grants.
//
// What NATS does not say. A PUBLISH on the KV answers how many connections received
// it; a NATS publish answers nothing. So a question learns who is listening from the
// listeners themselves (bus.go: ack). This backend reports exact() false.
//
// Loss. A message sent while the connection was down is buffered by the client up to
// its reconnect buffer and otherwise lost, and one a slow subscriber could not hold is
// dropped by the client. Both are told to the subscribers as a gap: on every
// reconnect, and on a slow-consumer drop.
type natsBus struct {
	nc *nats.Conn

	mu   sync.Mutex
	subs map[*nats.Subscription]*natsSub
}

type natsSub struct {
	deliver func([]byte)
	gap     func()
}

const (
	// flushTimeout bounds how long a subscription waits for the server to hold its interest.
	flushTimeout = 300 * time.Millisecond
	// natsPending bounds what a subscription may hold unread before the client drops
	// its newest messages and the subscriber is told it missed some.
	natsPendingMsgs  = 65536
	natsPendingBytes = 64 << 20
	// natsReconnectBuf is what a client holds for a server it cannot reach.
	natsReconnectBuf = 16 << 20
)

// newNATSBus connects to url, which may carry the client's user and password
// (nats://user:password@host:4222). It returns at once whether or not the server
// answers: a bus that is down is retried until it is not, and a pod must start
// without it.
func newNATSBus(url, name string) (*natsBus, error) {
	b := &natsBus{subs: map[*nats.Subscription]*natsSub{}}
	nc, err := nats.Connect(url,
		nats.Name(name),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(500*time.Millisecond),
		nats.ReconnectBufSize(natsReconnectBuf),
		nats.RetryOnFailedConnect(true),
		nats.ReconnectHandler(func(*nats.Conn) { b.lose(nil) }),
		nats.ErrorHandler(func(_ *nats.Conn, sub *nats.Subscription, err error) {
			if sub != nil && errors.Is(err, nats.ErrSlowConsumer) {
				b.lose(sub)
			}
		}),
	)
	if err != nil {
		return nil, err
	}
	b.nc = nc
	return b, nil
}

// lose tells one subscription (or, for nil, every subscription) it may have missed
// messages.
func (b *natsBus) lose(only *nats.Subscription) {
	b.mu.Lock()
	var gaps []func()
	for sub, s := range b.subs {
		if (only == nil || only == sub) && s.gap != nil {
			gaps = append(gaps, s.gap)
		}
	}
	b.mu.Unlock()
	for _, g := range gaps {
		go g()
	}
}

func subjectOf(channel string) string {
	sum := sha256.Sum256([]byte(channel))
	return "coord." + hex.EncodeToString(sum[:16])
}

func (b *natsBus) Exact() bool { return false }

// publish sends msg. NATS says how many received it: nothing.
func (b *natsBus) Publish(_ context.Context, channel string, msg []byte) (int, error) {
	if err := b.nc.Publish(subjectOf(channel), msg); err != nil {
		return 0, err
	}
	return 0, nil
}

// subscribe is active when it returns, which is when the server has the interest: a
// message published after it is received. A bus that does not answer in time leaves
// the subscription in place for when it comes back, and gap is called then.
func (b *natsBus) Subscribe(channel string, deliver func([]byte), gap func()) (func(), error) {
	s := &natsSub{deliver: deliver, gap: gap}
	sub, err := b.nc.Subscribe(subjectOf(channel), func(m *nats.Msg) { s.deliver(m.Data) })
	if err != nil {
		return nil, err
	}
	_ = sub.SetPendingLimits(natsPendingMsgs, natsPendingBytes)
	b.mu.Lock()
	b.subs[sub] = s
	b.mu.Unlock()
	if err := b.nc.FlushTimeout(flushTimeout); err != nil {
		// Whatever was published before the server held the interest is lost.
		if gap != nil {
			go gap()
		}
	}
	return func() {
		b.mu.Lock()
		delete(b.subs, sub)
		b.mu.Unlock()
		_ = sub.Unsubscribe()
	}, nil
}

func (b *natsBus) Close() {
	_ = b.nc.FlushTimeout(time.Second)
	b.nc.Close()
}
