package secret

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrUnsealed is a read that may not mint finding no key: nothing was ever sealed
// under the name, so nothing signed with it can be genuine.
var ErrUnsealed = errors.New("secret: the key is not sealed yet")

// ErrMalformed is a key the store holds that is too short to be one. It is a fault
// to be read and mended, never a key to be replaced: every value already sealed
// under it would stop verifying.
var ErrMalformed = errors.New("secret: the key held is shorter than 32 bytes")

// ErrBusy is a mint that another process holds the lease for: nothing was minted here, and
// the caller may try again in a moment. It always comes wrapped in ErrUnsealed, since the
// key is not there yet.
var ErrBusy = errors.New("secret: another process is sealing the key")

// ErrNoLease is a mint asked of a Sealed that has no way to keep other processes out. It
// never mints: a key sealed without that exclusion can be overwritten by another pod's.
var ErrNoLease = errors.New("secret: no cross-process lease is configured; the key is not minted")

// Lease keeps every other process from minting while the one that took it does. It answers
// the function that gives the lease back and how long the holder has it, or an error when it
// cannot be taken: held by another, or the coordinator down. It must not grant a lease it
// cannot be sure is exclusive, and its release must do nothing once that time has gone
// (coord.Lease is the one the fleet uses). It is the caller's to supply, since this package
// imports nothing but the standard library.
type Lease func(ctx context.Context) (release func(), held time.Duration, err error)

// keyLen is the least a key is, and the size of one minted here.
const keyLen = 32

// faultWait is how long a read that failed, or found no key and may not mint, is
// answered again from memory, so a slow or dead KMS holds up one request in each
// window and not every request that follows.
const faultWait = 5 * time.Second

// Sealed is one deployment HMAC key held in KMS at a ref and cached in process once
// read. Each key authenticates only what one package seals under it, so one key per
// purpose per deployment is enough.
//
// The lock guards the cache and is never held across a KMS call: concurrent reads
// in a process share one call. The invariant: a pod only returns a key that KMS returned
// to it inside its own live lease.
type Sealed struct {
	ref   string
	lease Lease

	mu     sync.Mutex
	key    []byte
	fault  error
	at     time.Time
	flight map[bool]*call // the load in progress, by whether it may mint
}

// call is one load that readers arriving meanwhile wait for.
type call struct {
	done chan struct{}
	key  []byte
	err  error
}

// NewSealed is the key held at ref. lease is what a mint takes first so that two pods
// that find no key at once do not each store one; with none, the key is only ever read.
func NewSealed(ref string, lease Lease) *Sealed {
	return &Sealed{ref: ref, lease: lease, flight: map[bool]*call{}}
}

// Ref is where KMS holds the key.
func (k *Sealed) Ref() string { return k.ref }

// Get resolves the key from kms. With provision set, a key the store does not hold
// is generated (32 random bytes) and sealed on first use; without it a missing key
// is ErrUnsealed, so a path any stranger can reach never mints one. A key the store
// holds that is too short is ErrMalformed, whatever provision says. A mint takes the
// lease first and reads again inside it, so a key another pod sealed meanwhile is used and
// nothing is written over it; if the lease cannot be taken the answer is ErrUnsealed
// wrapping ErrBusy, and nothing is minted. With no lease configured a mint is ErrNoLease. A read that
// fails for any other reason is that error and mints nothing, so a KMS outage never
// replaces the key everything already issued was signed with. It fails closed when
// there is no KMS, and never answers an empty key.
func (k *Sealed) Get(ctx context.Context, kms Store, provision bool) ([]byte, error) {
	k.mu.Lock()
	if len(k.key) > 0 {
		defer k.mu.Unlock()
		return k.key, nil
	}
	// A remembered absence is no reason to refuse a caller that may mint.
	if k.fault != nil && time.Since(k.at) < faultWait && (!provision || !errors.Is(k.fault, ErrUnsealed)) {
		defer k.mu.Unlock()
		return nil, k.fault
	}
	k.mu.Unlock()
	if kms == nil {
		return nil, fmt.Errorf("%w (%s)", ErrUnconfigured, k.ref)
	}
	return k.shared(provision, func() ([]byte, error) { return k.load(context.WithoutCancel(ctx), kms, provision) })
}

// shared runs load once for every reader that arrives while it runs, and answers each
// the one result. It holds the lock only to find or leave the call, never across load.
func (k *Sealed) shared(provision bool, load func() ([]byte, error)) ([]byte, error) {
	k.mu.Lock()
	if c := k.flight[provision]; c != nil {
		k.mu.Unlock()
		<-c.done
		return c.key, c.err
	}
	c := &call{done: make(chan struct{})}
	k.flight[provision] = c
	k.mu.Unlock()

	c.key, c.err = load()

	k.mu.Lock()
	delete(k.flight, provision)
	k.mu.Unlock()
	close(c.done)
	return c.key, c.err
}

// load reads the key, and mints it when it may.
func (k *Sealed) load(ctx context.Context, kms Store, provision bool) ([]byte, error) {
	v, err := kms.GetSecret(ctx, k.ref)
	switch {
	case err == nil:
		return k.hold(v)
	case !errors.Is(err, ErrNone):
		return nil, k.fail(fmt.Errorf("secret: read %s: %w", k.ref, err))
	case !provision:
		return nil, k.fail(fmt.Errorf("%w: %s", ErrUnsealed, k.ref))
	}
	if k.lease == nil {
		return nil, fmt.Errorf("%w: %s", ErrNoLease, k.ref)
	}
	release, held, err := k.lease(ctx)
	if err != nil {
		// Not remembered: the pod that holds the lease is about to make the key.
		return nil, fmt.Errorf("%w: %w: %s: %v", ErrUnsealed, ErrBusy, k.ref, err)
	}
	// The whole mint, its three KMS calls together, gets half the lease; a mint that is
	// not done by then gives no key and does not give the lease back, since it may be the
	// next pod's by the time this one could. The lease lapses by itself.
	ctx, cancel := context.WithTimeout(ctx, held/2)
	defer cancel()
	v, done, err := within(ctx, func(ctx context.Context) ([]byte, error) { return k.mint(ctx, kms) })
	if !done || ctx.Err() != nil { // a key that arrived as the time ran out is as late as one that did not
		return nil, fmt.Errorf("%w: %s: the mint outran its lease and was dropped: %v", ErrUnsealed, k.ref, ctx.Err())
	}
	release()
	if err != nil {
		return nil, err
	}
	return k.hold(v)
}

// mint is the work a lease holder does: look again, then make the key, put it, and read it
// back. It answers the key KMS returned, never one it only generated.
func (k *Sealed) mint(ctx context.Context, kms Store) ([]byte, error) {
	// Inside the lease, look again: another pod may have sealed the key since the read above.
	v, err := kms.GetSecret(ctx, k.ref)
	switch {
	case err == nil:
		return v, nil
	case !errors.Is(err, ErrNone):
		return nil, k.fail(fmt.Errorf("secret: read %s: %w", k.ref, err))
	}
	fresh := make([]byte, keyLen)
	if _, err := rand.Read(fresh); err != nil {
		return nil, err
	}
	if err := kms.PutSecret(ctx, k.ref, fresh); err != nil {
		return nil, k.fail(fmt.Errorf("secret: seal %s: %w", k.ref, err))
	}
	v, err = kms.GetSecret(ctx, k.ref)
	if err != nil {
		return nil, k.fail(fmt.Errorf("secret: read back %s: %w", k.ref, err))
	}
	// Not what was written: another writer got in, so the lease did not exclude it. Use
	// nothing from this mint; the next read takes whatever KMS holds.
	if !bytes.Equal(v, fresh) {
		return nil, fmt.Errorf("%w: %s: what KMS holds is not what this pod wrote", ErrUnsealed, k.ref)
	}
	return v, nil
}

// within runs f and answers its result, or reports done false when ctx ends first. f may
// still be running then, since a KMS call that ignores its context cannot be stopped; its
// result is dropped.
func within(ctx context.Context, f func(context.Context) ([]byte, error)) (v []byte, done bool, err error) {
	type result struct {
		v   []byte
		err error
	}
	ch := make(chan result, 1)
	go func() { v, err := f(ctx); ch <- result{v, err} }()
	select {
	case r := <-ch:
		return r.v, true, r.err
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
}

// hold keeps a key the store answered, or refuses one that is too short.
func (k *Sealed) hold(v []byte) ([]byte, error) {
	if len(v) < keyLen {
		return nil, k.fail(fmt.Errorf("%w: %s holds %d bytes; it is not replaced", ErrMalformed, k.ref, len(v)))
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.key, k.fault = v, nil
	return v, nil
}

// fail remembers err for faultWait and answers it.
func (k *Sealed) fail(err error) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.fault, k.at = err, time.Now()
	return err
}
