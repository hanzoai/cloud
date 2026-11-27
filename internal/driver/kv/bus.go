package kv

import (
	"context"
	"sync"
	"time"

	kvc "github.com/hanzokv/go/v9"

	"github.com/hanzoai/cloud/sdk/coord"
)

// kvBus is a pod's one subscribing connection to the KV, shared by every channel the
// pod listens on. Publishing goes through the client's pool and the breaker (ask);
// listening cannot, since a subscription is a connection that stays open.
type kvBus struct {
	mu   sync.Mutex
	ps   *kvc.PubSub
	subs map[string]*kvSub
}

type kvSub struct {
	deliver func([]byte)
	gap     func()
	ready   chan struct{} // closed by the first confirmation
	seen    bool          // a confirmation has come
	late    bool          // the first one did not come in time: what was published meanwhile is lost
}

// publish answers how many connections received msg, this pod's included.
func (b *kv) Publish(ctx context.Context, channel string, msg []byte) (int, error) {
	var n int64
	err := b.ask(ctx, func(ctx context.Context) error {
		var err error
		n, err = b.c.Publish(ctx, channel, msg).Result()
		return err
	})
	return int(n), err
}

// subscribe listens on channel. It answers when the server has confirmed the
// subscription, so a message published after it returns is received; a KV that does
// not confirm in time leaves the subscription in place for when it comes back, and
// gap is called then, because whatever was published before is lost.
//
// A KV that drops the connection and answers again resubscribes the channel by
// itself (the client keeps the list), and the second confirmation is how this pod
// learns it was deaf in between: gap is called then as well.
func (b *kv) Subscribe(channel string, deliver func([]byte), gap func()) (func(), error) {
	s := &kvSub{deliver: deliver, gap: gap, ready: make(chan struct{})}
	b.bus.mu.Lock()
	if b.bus.ps == nil {
		b.bus.ps = b.c.Subscribe(context.Background())
		b.bus.subs = map[string]*kvSub{}
		go b.pump(b.bus.ps.ChannelWithSubscriptions(kvc.WithChannelSize(1024)))
	}
	ps := b.bus.ps
	b.bus.subs[channel] = s
	b.bus.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	err := ps.Subscribe(ctx, channel)
	cancel()
	if err == nil {
		t := time.NewTimer(callTimeout)
		select {
		case <-s.ready:
		case <-t.C:
			err = coord.ErrUnavailable
		}
		t.Stop()
	}
	if err != nil {
		b.bus.mu.Lock()
		if !s.seen {
			s.late = true
		}
		b.bus.mu.Unlock()
	}
	return func() {
		b.bus.mu.Lock()
		if b.bus.subs[channel] == s {
			delete(b.bus.subs, channel)
		}
		b.bus.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		_ = ps.Unsubscribe(ctx, channel)
		cancel()
	}, nil
}

// pump routes what the connection receives to the channel's subscription.
func (b *kv) pump(in <-chan interface{}) {
	for m := range in {
		switch m := m.(type) {
		case *kvc.Subscription:
			if m.Kind != "subscribe" {
				continue
			}
			b.bus.mu.Lock()
			s := b.bus.subs[m.Channel]
			if s == nil {
				b.bus.mu.Unlock()
				continue
			}
			missed := s.seen || s.late
			if !s.seen {
				close(s.ready)
			}
			s.seen, s.late = true, false
			b.bus.mu.Unlock()
			if missed && s.gap != nil {
				s.gap()
			}
		case *kvc.Message:
			b.bus.mu.Lock()
			s := b.bus.subs[m.Channel]
			b.bus.mu.Unlock()
			if s != nil {
				s.deliver([]byte(m.Payload))
			}
		}
	}
}

// closeBus ends the pod's subscribing connection.
func (b *kv) Close() {
	b.bus.mu.Lock()
	ps := b.bus.ps
	b.bus.ps, b.bus.subs = nil, nil
	b.bus.mu.Unlock()
	if ps != nil {
		_ = ps.Close()
	}
}

func (b *kv) Exact() bool { return true }
