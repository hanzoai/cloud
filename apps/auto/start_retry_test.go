package auto

// start_retry_test.go pins when an automation's Dev run is started again: three tries at
// most, after a pause, only for a request the Dev app could not take now, and always with
// the id of the automation's run so a try that did arrive starts nothing a second time.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/retry"
	"github.com/zap-proto/zip"
)

// scriptedStart is the Dev app's start answering in order (the last for ever). It keeps each
// request id's run the way the app's ledger does: a request that arrived has its session,
// whatever its answer was, and a retry of it answers that one.
type scriptedStart struct {
	mu       sync.Mutex
	answers  []error
	ids      []string
	sessions map[string]string
}

func (s *scriptedStart) tries() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.ids)
}

// scripted puts a scripted start in place of the Dev plane's, and the pauses between tries at
// nothing. It is called after devPlane, which it stands over.
func scripted(t *testing.T, answers ...error) *scriptedStart {
	t.Helper()
	s := &scriptedStart{answers: answers, sessions: map[string]string{}}
	was, wasPause := agentStart, retry.Pause
	retry.Pause = func(context.Context, time.Duration) error { return nil }
	agentStart = func(_ context.Context, in *client.CodingStartIn) (*client.CodingStarted, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		err := s.answers[min(len(s.ids), len(s.answers)-1)]
		s.ids = append(s.ids, in.RequestID)
		session, seen := s.sessions[in.RequestID]
		if !seen {
			session = fmt.Sprintf("sess_%032x", len(s.sessions)+1)
			s.sessions[in.RequestID] = session
		}
		if err != nil {
			return nil, err
		}
		return &client.CodingStarted{SessionID: session}, nil
	}
	t.Cleanup(func() { agentStart, retry.Pause = was, wasPause })
	return s
}

// begin makes an automation and one of its runs, and starts that run.
func begin(t *testing.T, app *zip.App) (AgentStarted, string, error) {
	t.Helper()
	a := decode[Automation](t, ask(t, app, http.MethodPost, "/v1/auto/automations", "acme", map[string]any{
		"name": "Ideas", "instructions": "Draft ideas.",
	}))
	run := decode[runStarted](t, req(t, app, http.MethodPost, "/v1/auto/automations/"+a.ID+"/run", "acme", nil)).Run
	got, err := StartAgentActivity(context.Background(), AgentStart{Owner: "acme", FlowID: a.ID, RunID: run.ID})
	return got, run.ID, err
}

var (
	busy    = zip.Errorf(http.StatusServiceUnavailable, "agent: try again")
	timeout = &net.OpError{Op: "dial", Err: syscall.ETIMEDOUT}
)

// A start the Dev app could not take is made again, with the id of the automation's run,
// and the run it ends in is the one session.
func TestAStartTheDevAppCouldNotTakeIsMadeAgain(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for name, failure := range map[string]error{
			"busy":        zip.Errorf(http.StatusTooManyRequests, "queue full"),
			"failing":     busy,
			"gateway":     zip.Errorf(http.StatusBadGateway, "bad gateway"),
			"timed out":   zip.Errorf(http.StatusGatewayTimeout, "timeout"),
			"not there":   fmt.Errorf("plane: %w", syscall.ECONNREFUSED),
			"dropped":     fmt.Errorf("plane: %w", syscall.ECONNRESET),
			"unreachable": fmt.Errorf("plane: %w", timeout),
		} {
			t.Run(name, func(t *testing.T) {
				app := newApp(t)
				captureStarter(t)
				devPlane(t, client.TurnEvents{})
				s := scripted(t, failure, failure, nil) // the third try answers
				got, id, err := begin(t, app)
				if err != nil || got.Session == "" {
					t.Fatalf("start = %+v, %v", got, err)
				}
				if s.tries() != retry.Tries {
					t.Fatalf("%d tries, want %d", s.tries(), retry.Tries)
				}
				for _, rid := range s.ids {
					if rid != "auto-"+id {
						t.Fatalf("a try carried the request id %q, want the automation run's: %v", rid, s.ids)
					}
				}
				if len(s.sessions) != 1 {
					t.Fatalf("%d Dev runs were made, want 1", len(s.sessions))
				}
			})
		}
	})
}

// A try that arrived and whose answer was lost does not start a second run: the retry carries
// the id and is answered the first's session.
func TestALostAnswerStartsNoSecondRun(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := newApp(t)
		captureStarter(t)
		devPlane(t, client.TurnEvents{})
		s := scripted(t, zip.Errorf(http.StatusGatewayTimeout, "no answer"), nil)
		got, _, err := begin(t, app)
		if err != nil || got.Session != fmt.Sprintf("sess_%032x", 1) {
			t.Fatalf("start = %+v, %v", got, err)
		}
		if s.tries() != 2 || len(s.sessions) != 1 {
			t.Fatalf("%d tries and %d Dev runs, want 2 and 1", s.tries(), len(s.sessions))
		}
	})
}

// A start that keeps failing is made retry.Tries times, and the run says it did not start.
func TestAStartIsGivenUp(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := newApp(t)
		captureStarter(t)
		devPlane(t, client.TurnEvents{})
		s := scripted(t, busy)
		if _, _, err := begin(t, app); err == nil || !errors.Is(err, busy) {
			t.Fatalf("err = %v, want the Dev app's failure", err)
		}
		if s.tries() != retry.Tries {
			t.Fatalf("%d tries, want %d", s.tries(), retry.Tries)
		}
	})
}

// A request the Dev app refused, or that failed for its own terms, is made once.
func TestARefusedStartIsMadeOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for name, refusal := range map[string]error{
			"bad request": zip.ErrBadRequest("coding: not a branch"),
			"payment":     zip.Errorf(http.StatusPaymentRequired, "insufficient balance"),
			"forbidden":   zip.ErrForbidden("coding: GitHub did not admit the run"),
			"missing":     zip.ErrNotFound("coding: no such run"),
			"conflict":    zip.ErrConflict("coding: the run is still working"),
			"unprocessed": zip.Errorf(http.StatusUnprocessableEntity, "the id was used for another request"),
			"not served":  zip.Errorf(http.StatusNotImplemented, "no"),
			"its terms":   errors.New("coding: a run on your own machine needs the repo it works in to open a pull request"),
		} {
			t.Run(name, func(t *testing.T) {
				app := newApp(t)
				captureStarter(t)
				devPlane(t, client.TurnEvents{})
				s := scripted(t, refusal, nil)
				if _, _, err := begin(t, app); err == nil || !errors.Is(err, refusal) {
					t.Fatalf("err = %v, want the refusal", err)
				}
				if s.tries() != 1 {
					t.Fatalf("%d tries, want 1", s.tries())
				}
			})
		}
	})
}

// A start whose context ends while it waits ends with it.
func TestAStartStopsWhenItsContextEnds(t *testing.T) {
	s := scripted(t, busy)
	ctx, cancel := context.WithCancel(context.Background())
	retry.Pause = func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}
	if _, err := startDev(ctx, &client.CodingStartIn{RequestID: "auto-r"}); !errors.Is(err, busy) {
		t.Fatalf("err = %v, want the failure the wait was for", err)
	}
	if s.tries() != 1 {
		t.Fatalf("%d tries, want 1", s.tries())
	}
}
