package coding

// github_retry_test.go pins what a GitHub call does when GitHub rate limits it or fails in
// flight (#817): it waits and tries again, as long as GitHub says or by a doubling wait,
// a bounded number of times, and an answer that is the answer is returned at once.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/retry"
)

// reply is one answer of a scripted GitHub: a status, headers, and a connection drop in
// place of both.
type reply struct {
	status int
	header map[string]string
	drop   bool
}

// scripted is a GitHub that answers its replies in order, and the last one for ever.
type scripted struct {
	mu      sync.Mutex
	replies []reply
	bodies  []string
}

func (s *scripted) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	n := min(len(s.bodies), len(s.replies)-1)
	s.bodies = append(s.bodies, string(b))
	rep := s.replies[n]
	s.mu.Unlock()
	if rep.drop {
		c, _, _ := w.(http.Hijacker).Hijack()
		c.Close()
		return
	}
	for k, v := range rep.header {
		w.Header().Set(k, v)
	}
	w.WriteHeader(rep.status)
	if rep.status == http.StatusOK {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}
}

func (s *scripted) seen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bodies)
}

// github stands a scripted GitHub in for the real one and records every wait a call makes
// instead of making it. The waits are what the call chose; none of them is slept.
func github(t *testing.T, replies ...reply) (*scripted, *[]time.Duration) {
	t.Helper()
	s := &scripted{replies: replies}
	srv := httptest.NewServer(s)
	was, wasPause := githubAPI, retry.Pause
	waits := &[]time.Duration{}
	githubAPI = srv.URL
	retry.Pause = func(_ context.Context, d time.Duration) error { *waits = append(*waits, d); return nil }
	t.Cleanup(func() { srv.Close(); githubAPI, retry.Pause = was, wasPause })
	return s, waits
}

func ask(t *testing.T, method string, in any) (int, bool, error) {
	t.Helper()
	var out struct {
		OK bool `json:"ok"`
	}
	st, err := (&hub{}).doCall(context.Background(), method, "/repos/o/r", "tok", in, &out)
	return st, out.OK, err
}

// A 429 that names its wait is waited for exactly that long, and the call then succeeds.
func TestAGitHubCallWaitsForRetryAfter(t *testing.T) {
	s, waits := github(t,
		reply{status: 429, header: map[string]string{"Retry-After": "7"}},
		reply{status: 200})
	st, ok, err := ask(t, http.MethodGet, nil)
	if err != nil || st != 200 || !ok {
		t.Fatalf("status %d ok %v err %v, want the second answer", st, ok, err)
	}
	if s.seen() != 2 || len(*waits) != 1 || (*waits)[0] != 7*time.Second {
		t.Fatalf("%d requests, waits %v, want 2 requests and one wait of 7s", s.seen(), *waits)
	}
}

// Retry-After may be an HTTP date.
func TestAGitHubCallWaitsUntilTheRetryAfterDate(t *testing.T) {
	_, waits := github(t,
		reply{status: 429, header: map[string]string{"Retry-After": time.Now().Add(9 * time.Second).UTC().Format(http.TimeFormat)}},
		reply{status: 200})
	if st, _, err := ask(t, http.MethodGet, nil); err != nil || st != 200 {
		t.Fatalf("status %d err %v, want 200", st, err)
	}
	if len(*waits) != 1 || (*waits)[0] < 7*time.Second || (*waits)[0] > 9*time.Second {
		t.Fatalf("waits %v, want one of about 9s", *waits)
	}
}

// A 403 is a rate limit only when it says when to come back; a 403 without that is a refusal.
func TestAForbiddenCallIsRetriedOnlyWithRetryAfter(t *testing.T) {
	s, waits := github(t, reply{status: 403, header: map[string]string{"Retry-After": "3"}}, reply{status: 200})
	if st, _, err := ask(t, http.MethodGet, nil); err != nil || st != 200 || s.seen() != 2 || (*waits)[0] != 3*time.Second {
		t.Fatalf("status %d err %v requests %d waits %v, want the 403 with Retry-After retried once after 3s", st, err, s.seen(), *waits)
	}
	s, waits = github(t, reply{status: 403}, reply{status: 200})
	if st, _, _ := ask(t, http.MethodGet, nil); st != 403 || s.seen() != 1 || len(*waits) != 0 {
		t.Fatalf("status %d requests %d waits %v, want a bare 403 answered at once", st, s.seen(), *waits)
	}
}

// A 429 with no usable Retry-After backs off by a doubling wait, its lower half fixed and
// its upper half random.
func TestAGitHubCallBacksOffWithoutRetryAfter(t *testing.T) {
	for _, header := range []map[string]string{nil, {"Retry-After": "soon"}, {"Retry-After": "0"}} {
		_, waits := github(t,
			reply{status: 429, header: header}, reply{status: 429, header: header}, reply{status: 200})
		if st, _, err := ask(t, http.MethodGet, nil); err != nil || st != 200 {
			t.Fatalf("Retry-After %v: status %d err %v, want 200 on the third try", header, st, err)
		}
		if len(*waits) != 2 {
			t.Fatalf("Retry-After %v: waits %v, want two", header, *waits)
		}
		for i, w := range *waits {
			ceil := min(retry.First<<i, retry.Longest)
			if w < ceil/2 || w > ceil {
				t.Errorf("Retry-After %v: wait %d is %v, want %v..%v", header, i, w, ceil/2, ceil)
			}
		}
	}
}

// Every passing server fault is tried again, and the call that follows succeeds.
func TestAGitHubCallIsRetriedOnAServerFault(t *testing.T) {
	for _, st := range []int{500, 502, 503, 504} {
		s, waits := github(t, reply{status: st}, reply{status: 200})
		got, ok, err := ask(t, http.MethodGet, nil)
		if err != nil || got != 200 || !ok || s.seen() != 2 || len(*waits) != 1 {
			t.Fatalf("%d: status %d ok %v err %v requests %d waits %v, want 200 after one retry", st, got, ok, err, s.seen(), *waits)
		}
	}
}

// A dropped connection is a fault that passes.
func TestAGitHubCallIsRetriedOnADroppedConnection(t *testing.T) {
	s, _ := github(t, reply{drop: true}, reply{status: 200})
	if st, ok, err := ask(t, http.MethodGet, nil); err != nil || st != 200 || !ok || s.seen() != 2 {
		t.Fatalf("status %d ok %v err %v requests %d, want 200 after one retry", st, ok, err, s.seen())
	}
}

// Any other answer is the answer: a refusal is never made again.
func TestAGitHubCallIsNotRetriedOnARefusal(t *testing.T) {
	for _, st := range []int{400, 401, 404, 409, 422} {
		s, waits := github(t, reply{status: st}, reply{status: 200})
		if got, _, err := ask(t, http.MethodGet, nil); err != nil || got != st || s.seen() != 1 || len(*waits) != 0 {
			t.Fatalf("%d: status %d err %v requests %d waits %v, want it answered once", st, got, err, s.seen(), *waits)
		}
	}
}

// A call that never stops failing is made retry.Tries times, and its last answer is returned.
func TestAGitHubCallGivesUp(t *testing.T) {
	for _, st := range []int{429, 503} {
		s, waits := github(t, reply{status: st})
		if got, _, err := ask(t, http.MethodGet, nil); err != nil || got != st {
			t.Fatalf("%d: status %d err %v, want the last answer", st, got, err)
		}
		if s.seen() != retry.Tries || len(*waits) != retry.Tries-1 {
			t.Fatalf("%d: %d requests and %d waits, want %d and %d", st, s.seen(), len(*waits), retry.Tries, retry.Tries-1)
		}
	}
	s, _ := github(t, reply{drop: true})
	if _, _, err := ask(t, http.MethodGet, nil); err == nil || s.seen() != retry.Tries {
		t.Fatalf("err %v requests %d, want the connection error after %d tries", err, s.seen(), retry.Tries)
	}
}

// A Retry-After longer than a run's step may be held for is not waited for.
func TestAGitHubCallDoesNotWaitPastTheHold(t *testing.T) {
	s, waits := github(t, reply{status: 429, header: map[string]string{"Retry-After": strconv.Itoa(int(retry.Hold/time.Second) + 1)}}, reply{status: 200})
	if st, _, err := ask(t, http.MethodGet, nil); err != nil || st != 429 || s.seen() != 1 || len(*waits) != 0 {
		t.Fatalf("status %d err %v requests %d waits %v, want the 429 answered at once", st, err, s.seen(), *waits)
	}
}

// What is sent again is what was sent: a write's body arrives whole on every try.
func TestAGitHubCallSendsItsBodyOnEveryTry(t *testing.T) {
	s, _ := github(t, reply{status: 503}, reply{status: 429}, reply{status: 200})
	if st, _, err := ask(t, http.MethodPost, map[string]string{"title": "t"}); err != nil || st != 200 {
		t.Fatalf("status %d err %v, want 200", st, err)
	}
	if len(s.bodies) != 3 {
		t.Fatalf("%d requests, want 3", len(s.bodies))
	}
	for i, b := range s.bodies {
		if b != `{"title":"t"}` {
			t.Errorf("try %d sent %q, want the whole body", i+1, b)
		}
	}
}

// A call whose context ends while it waits is answered what the try before answered, and not made again.
func TestAGitHubCallStopsWhenItsContextEnds(t *testing.T) {
	s, _ := github(t, reply{status: 429, header: map[string]string{"Retry-After": "30"}}, reply{status: 200})
	retry.Pause = func(ctx context.Context, _ time.Duration) error {
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	st, err := (&hub{}).doCall(ctx, http.MethodGet, "/repos/o/r", "tok", nil, nil)
	if err != nil || st != 429 || s.seen() != 1 {
		t.Fatalf("status %d err %v requests %d, want the 429 of the first try and no second", st, err, s.seen())
	}
}

func TestWaitOf(t *testing.T) {
	for _, c := range []struct {
		in   string
		want time.Duration
	}{{"", 0}, {"0", 0}, {"5", 5 * time.Second}, {" 10 ", 10 * time.Second}, {"-4", 0}, {"soon", 0},
		{time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat), 0}} {
		if got := waitOf(c.in); got != c.want {
			t.Errorf("waitOf(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
