package runs

import (
	"context"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/driver/kv/kvtest"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/coord/coordtest"
)

func each(t *testing.T, f func(t *testing.T, pod func(string) *coord.Coord)) {
	t.Helper()
	t.Run("in process", func(t *testing.T) { f(t, coordtest.Memory(t)) })
	t.Run("kv", func(t *testing.T) {
		pod, _ := kvtest.Mini(t)
		f(t, pod)
	})
}

// The runs of every other pod are one set to whoever asks, and a pod that runs none
// is not in it.
func TestOthersAreEveryOtherPodsRuns(t *testing.T) {
	each(t, func(t *testing.T, pod func(string) *coord.Coord) {
		a, b, c, d := pod("cloud-0"), pod("cloud-1"), pod("cloud-2"), pod("cloud-3")
		defer a.Serve(Held, func(context.Context, string, []byte) ([]byte, bool) { return Answer([]string{"sess_a"}) })()
		defer b.Serve(Held, func(context.Context, string, []byte) ([]byte, bool) { return Answer([]string{"sess_b1", "sess_b2"}) })()
		defer c.Serve(Held, func(context.Context, string, []byte) ([]byte, bool) { return Answer(nil) })()
		got, err := Others(context.Background(), d)
		if err != nil || len(got) != 3 || !got["sess_a"] || !got["sess_b1"] || !got["sess_b2"] {
			t.Fatalf("pod d sees %v (%v), want a's and b's runs", got, err)
		}
		// A pod's own runs are not "others".
		own, err := Others(context.Background(), a)
		if err != nil || own["sess_a"] || len(own) != 2 {
			t.Fatalf("pod a sees %v (%v), want only b's", own, err)
		}
	})
}

// A pod that does not answer is an error, not an absence: its runs are unknown.
func TestAPodThatDoesNotAnswerIsNotNoRuns(t *testing.T) {
	each(t, func(t *testing.T, pod func(string) *coord.Coord) {
		a, mute, asker := pod("cloud-0"), pod("cloud-1"), pod("cloud-2")
		release := make(chan struct{})
		defer close(release)
		defer a.Serve(Held, func(context.Context, string, []byte) ([]byte, bool) { return Answer([]string{"sess_a"}) })()
		defer mute.Serve(Held, func(context.Context, string, []byte) ([]byte, bool) { <-release; return nil, false })()
		was := Wait
		Wait = 200 * time.Millisecond
		defer func() { Wait = was }()
		got, err := Others(context.Background(), asker)
		if err == nil {
			t.Fatalf("a pod that never answered was taken for running nothing: %v", got)
		}
		if !got["sess_a"] {
			t.Fatalf("what the answering pod said was lost with the error: %v", got)
		}
	})
}

// A process that executes no runs and has no shared coord cannot ask; one that
// executes them all (a nil coord) has no others.
func TestAProcessThatCannotAskSaysSo(t *testing.T) {
	if _, err := Others(context.Background(), coord.Alone()); err != ErrAlone {
		t.Fatalf("a process with no one to ask: %v, want ErrAlone", err)
	}
	if got, err := Others(context.Background(), nil); err != nil || len(got) != 0 {
		t.Fatalf("the process that executes every run: %v %v", got, err)
	}
}
