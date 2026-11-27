package sandbox

// A long run is carried for as long as it works and its wallet can pay: the exec
// deadline is the caller's, a command in flight keeps the sandbox from reading as
// abandoned, and a lease is carried into another hour only while that hour is paid.

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
	"k8s.io/client-go/tools/remotecommand"
)

// deadlines is an exec channel that records how long the call was given.
type deadlines struct{ given chan time.Duration }

func (d *deadlines) stream(ctx context.Context, _, _ string, _ []string, _ io.Reader, _, _ io.Writer) error {
	at, ok := ctx.Deadline()
	if !ok {
		d.given <- -1
		return nil
	}
	d.given <- time.Until(at)
	return nil
}

func (d *deadlines) tty(context.Context, string, string, []string, io.Reader, io.Writer, remotecommand.TerminalSizeQueue) error {
	return errors.New("not a terminal test")
}

// gave runs one command through Run with the timeout asked and answers the
// deadline it got, as the caller of the public exec sees it.
func gave(t *testing.T, timeoutSec int) time.Duration {
	t.Helper()
	d := &deadlines{given: make(chan time.Duration, 1)}
	s := service(t, d)
	ready(t, s)
	seed(t, s, "acme", "m_1")
	if _, err := Run(s, context.Background(), "acme", "m_1", Cmd{Argv: []string{"sleep", "1"}, TimeoutSec: timeoutSec}); err != nil {
		t.Fatalf("run: %v", err)
	}
	return <-d.given
}

// The deadline a caller names is the deadline the command gets, longer or shorter
// than the one a call that names none is given. It could only be shorter: an hour
// asked for was cut to execTimeout, a quarter of an hour, and the run died at it.
//
// MUTATION: restore `&& time.Duration(timeoutSec)*time.Second < d` and the hour is 30s.
func TestACommandIsGivenTheTimeoutItAsksFor(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		if got := gave(t, 3600); got < 59*time.Minute || got > time.Hour {
			t.Fatalf("a command that asked for an hour was given %s", got)
		}
		if got := gave(t, 0); got < 29*time.Second || got > 30*time.Second {
			t.Fatalf("a command that asked for nothing was given %s, want the exec default (30s here)", got)
		}
		if got := gave(t, 5); got > 5*time.Second {
			t.Fatalf("a command that asked for 5s was given %s", got)
		}
	})
}

// What a caller may ask for is the sandbox's whole life and no more: a week asked
// of the endpoint is a day at the command.
//
// MUTATION: drop the capTTL in Run and the week is a week.
func TestACommandCannotAskForMoreThanASandboxLives(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		week := 7 * 24 * 3600
		if got, most := gave(t, week), newClocks().absolute; got > most {
			t.Fatalf("a week asked for was given %s, more than a sandbox lives (%s)", got, most)
		}
	})
}

// A command that is in flight stamps its sandbox until it ends, so a build or a
// model that prints nothing for longer than the idle allowance is not reaped as an
// abandoned sandbox. It is measured through Run, which is what starts the stamp.
//
// MUTATION: delete `go pulse(...)` in Run and LastUsedAt never moves.
func TestACommandInFlightKeepsItsSandboxFromReadingAsIdle(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := &held{running: make(chan struct{}), release: make(chan struct{})}
		s := service(t, h)
		ready(t, s)
		s.State.pulse = 50 * time.Millisecond
		seed(t, s, "acme", "m_1")

		done := make(chan error, 1)
		go func() {
			_, err := Run(s, context.Background(), "acme", "m_1", Cmd{Argv: []string{"sleep", "3600"}})
			done <- err
		}()
		<-h.running

		st, err := storeFor(s, "acme")
		if err != nil {
			t.Fatalf("store: %v", err)
		}
		// Stamps are whole seconds. Run stamps once on entry; only the pulse can stamp
		// the sandbox again while the command is still running.
		read := func() int64 {
			m, err := st.Get(context.Background(), "acme", "m_1")
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			return m.LastUsedAt
		}
		first := read()
		for deadline := time.Now().Add(4 * time.Second); read() <= first; time.Sleep(100 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("a command in flight never re-stamped its sandbox")
			}
		}
		close(h.release)
		if err := <-done; err != nil {
			t.Fatalf("run: %v", err)
		}
	})
}

// sweeping puts paid, working leases a minute from their end into a fresh store,
// sweeps once with the payer's balance read by read, and answers each lease's
// expiry before and after.
func sweeping(t *testing.T, ids []string, read func(context.Context, string, string) (int64, error)) (before, after map[string]int64) {
	t.Helper()
	paid := balanceCents
	t.Cleanup(func() { balanceCents = paid })
	balanceCents = read

	stores := cloud.NewOrgStore[*Store](cloud.Base{DataDir: t.TempDir()}, "sandbox", openStore)
	t.Cleanup(func() { _ = stores.CloseAll() })
	s := &Service{Log: luxlog.NewNoOpLogger(), State: state{stores: stores, clk: newClocks()}}
	st, err := storeFor(s, "acme")
	if err != nil {
		t.Fatalf("storeFor: %v", err)
	}
	now := time.Now().Unix()
	before, after = map[string]int64{}, map[string]int64{}
	for _, id := range ids {
		m := lease(t, st, id, "acme", "acme", now)
		m.ExpiresAt = now + 60
		if err := st.Put(context.Background(), m); err != nil {
			t.Fatalf("put: %v", err)
		}
		before[id] = m.ExpiresAt
	}

	sweep(context.Background(), s)

	for _, id := range ids {
		back, err := st.Get(context.Background(), "acme", id)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		after[id] = back.ExpiresAt
	}
	return before, after
}

func balance(cents int64) func(context.Context, string, string) (int64, error) {
	return func(context.Context, string, string) (int64, error) { return cents, nil }
}

// A working lease whose wallet covers the next hour is carried into it.
func TestAWorkingLeaseIsCarriedWhileItsWalletCoversTheNextHour(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		before, after := sweeping(t, []string{"m_1"}, balance(1<<40))
		if after["m_1"]-before["m_1"] < extendBy/2 {
			t.Fatalf("a funded, working lease was not carried: expires %d, was %d", after["m_1"], before["m_1"])
		}
	})
}

// The hour that cannot be paid is not granted: the lease keeps its own expiry and
// ends there, rather than being carried into a negative balance.
//
// MUTATION: drop `&& read.covers(ctx, m)` from the sweep and the broke lease is carried.
func TestAWorkingLeaseIsNotCarriedIntoAnHourItsWalletCannotPay(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		before, after := sweeping(t, []string{"m_1"}, balance(0))
		if after["m_1"] != before["m_1"] {
			t.Fatalf("a lease with an empty wallet was carried: expires %d, was %d", after["m_1"], before["m_1"])
		}
	})
}

// A finance plane that does not answer costs the sweep seconds, once for the
// payer, and not a quarter of an hour for every lease: the sweep ends every
// tenant's leases, and one wedged read must not stop it.
//
// MUTATION: read the balance on the sweep's own context and this takes the
// ceiling of the read; ask per lease and the reads are three, not one.
func TestABalanceThatNeverAnswersCostsTheSweepSecondsAndOneRead(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		var reads int
		hang := func(ctx context.Context, _, _ string) (int64, error) {
			reads++
			<-ctx.Done()
			return 0, ctx.Err()
		}
		start := time.Now()
		before, after := sweeping(t, []string{"m_1", "m_2", "m_3"}, hang)
		if took := time.Since(start); took > balanceWait+2*time.Second {
			t.Fatalf("one unanswered balance held the sweep for %s", took)
		}
		if reads != 1 {
			t.Fatalf("the payer's balance was read %d times in one sweep, want once", reads)
		}
		for id := range before {
			if after[id] != before[id] {
				t.Fatalf("%s was carried on a balance nobody could read", id)
			}
		}
	})
}
