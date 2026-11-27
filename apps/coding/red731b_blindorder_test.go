package coding

// RED 731b L1: turn() reads the blind list (in.secrets()) BEFORE it reads the token it feeds (in.cred.current()). A
// renewal adopted between the two reads feeds a token that is not in that exec's Blind. The window is microseconds,
// so this hammers adopt to make it visible; the order is the defect.

import (
	"context"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
)

func TestRed731bATurnBlindsTheTokenItIsFed(t *testing.T) {
	r := newRenewRig(t)
	r.app.run("acme", "alice", "sess_1")
	b := r.key("acme", "alice", "sess_1")
	p := &pod{}
	servePod(t, p)
	in := sandbox{ctx: t.Context(), id: "sbx_1", ttl: 60, session: "sess_1", cred: b, extraSecrets: new([]string)}
	run := &client.RunIn{ID: "sbx_1", Session: "sess_1"}

	var stop atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // renewals, one every ~50us: far faster than a real one, slow enough that two never fall inside one turn's two reads
		defer wg.Done()
		for n := 0; !stop.Load(); n++ {
			time.Sleep(50 * time.Microsecond)
			b.adopt(jwtWith("ai:inference", "hammer-"+time.Now().Format("150405.000000000")+"-"+strings.Repeat("x", n%7)), time.Hour, time.Now())
		}
	}()
	bad := 0
	const turns = 3000
	for range turns {
		_, _ = in.turn(context.Background(), run, []string{"dev", "exec", "--skip-git-repo-check", "--", "-"}, "go", 60)
		p.mu.Lock()
		last := len(p.fed) - 1
		fed, blind := p.fed[last], p.blind[last]
		p.mu.Unlock()
		token, _, _ := strings.Cut(fed, "\n")
		if token != "" && !slices.Contains(blind, token) {
			bad++
		}
	}
	stop.Store(true)
	wg.Wait()
	if bad > 0 {
		t.Fatalf("%d of %d turns were fed a token that is not in their own Blind list", bad, turns)
	}
}
