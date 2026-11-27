package iam

// tokensource.go — the token this deployment's own IAM identity is read through.
//
// It used to be oauth2's reuse source, which holds ONE lock across the HTTP call that refreshes
// the token, and that call can last the whole 30s a call to IAM is allowed. Every caller needing
// the token while it was refreshed queued behind that lock, and one after another each made the
// call again when the first failed: a token endpoint that hung held every coding run's first
// delegation and every renewal for as many times thirty seconds as there were runs, and none of
// them could give up sooner, because Token() takes no context.
//
// Two things replace it. The token is kept fresh BEFORE it is needed: a timer, not a poll, asks for
// the next one when half of this one's life has passed, so a refresh is rarely on any caller's path.
// And a caller that does need one waits on the refresh that is under way, one for all of them rather
// than one after another, for as long as its OWN context allows (TokenWithin) and no longer.

import (
	"context"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// expiryDelta is how short of its expiry a token is no longer handed out: oauth2's own, the
// seconds a token needs to reach the service that checks it.
const expiryDelta = 10 * time.Second

// ahead is a token source that refreshes before expiry and shares one refresh among its waiters.
type ahead struct {
	fetch func(context.Context) (*oauth2.Token, error)
	now   func() time.Time
	after func(time.Duration, func())

	mu     sync.Mutex // guards the fields below; never held across fetch
	tok    *oauth2.Token
	flight *refresh
}

// refresh is one call to IAM for a token, and what its waiters are told.
type refresh struct {
	done chan struct{}
	tok  *oauth2.Token
	err  error
}

func newAhead(fetch func(context.Context) (*oauth2.Token, error)) *ahead {
	return &ahead{fetch: fetch, now: time.Now, after: func(d time.Duration, f func()) { time.AfterFunc(d, f) }}
}

// Token is the cached token while it is good, else the result of the refresh under way, which it
// starts if none is.
func (a *ahead) Token() (*oauth2.Token, error) {
	a.mu.Lock()
	if a.usable() {
		t := a.tok
		a.mu.Unlock()
		return t, nil
	}
	f := a.flight
	if f == nil {
		f = a.begin()
	}
	a.mu.Unlock()
	<-f.done
	return f.tok, f.err
}

// usable reports a cached token with more than expiryDelta of its life left. a.mu is held.
func (a *ahead) usable() bool {
	if a.tok == nil || a.tok.AccessToken == "" {
		return false
	}
	return a.tok.Expiry.IsZero() || a.now().Before(a.tok.Expiry.Add(-expiryDelta))
}

// begin starts the one refresh. a.mu is held.
func (a *ahead) begin() *refresh {
	f := &refresh{done: make(chan struct{})}
	a.flight = f
	go a.run(f)
	return f
}

func (a *ahead) run(f *refresh) {
	tok, err := a.fetch(context.WithValue(context.Background(), oauth2.HTTPClient, Client))
	a.mu.Lock()
	a.flight = nil
	switch {
	case err == nil && tok != nil:
		a.tok = tok
		a.arm(2)
	case a.usable():
		a.arm(2) // the token still in hand is asked after again before it runs out
	}
	f.tok, f.err = tok, err
	a.mu.Unlock()
	close(f.done)
}

// arm sets the one timer for the next refresh at 1/divisor of the life the token has left, and sets
// none for a token with no expiry or with almost none left, which callers refresh as they need it.
// a.mu is held.
func (a *ahead) arm(divisor int) {
	if a.tok == nil || a.tok.Expiry.IsZero() {
		return
	}
	left := a.tok.Expiry.Sub(a.now())
	if left <= expiryDelta {
		return
	}
	a.after(left/time.Duration(divisor), a.ahead)
}

// ahead is the timer's firing: a refresh in the background, unless one is under way.
func (a *ahead) ahead() {
	a.mu.Lock()
	if a.flight == nil {
		a.begin()
	}
	a.mu.Unlock()
}

// TokenWithin is src's token for a caller that will wait for it as long as ctx allows. Token() takes
// no context, so the call is made in a goroutine the caller can leave: the refresh it waited on goes
// on for whoever else needs it.
func TokenWithin(ctx context.Context, src oauth2.TokenSource) (*oauth2.Token, error) {
	type got struct {
		tok *oauth2.Token
		err error
	}
	c := make(chan got, 1)
	go func() {
		tok, err := src.Token()
		c <- got{tok, err}
	}()
	select {
	case g := <-c:
		return g.tok, g.err
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}
