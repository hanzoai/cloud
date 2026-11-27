package coding

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/client"
)

// board records every issue a run puts on the board.
type board struct{ moves []client.IssueIn }

func (b *board) put(_ context.Context, _ string, in client.IssueIn) error {
	b.moves = append(b.moves, in)
	return nil
}

func columns(b *board) []string {
	var out []string
	for _, m := range b.moves {
		out = append(out, m.State)
	}
	return out
}

// A run files its issue when it starts and moves it when it ends: one issue,
// keyed by the session and assigned to the agent, in progress and then done.
func TestARunMovesItsOwnIssue(t *testing.T) {
	b := &board{}
	d, _ := dispatcherFor(&fakeSessions{id: "sess_x"}, &fakePR{}, &fakeRunner{result: RunResult{OK: true}}, true)
	d.Issue = b.put

	if res := d.Run(context.Background(), baseReq()); !res.OK {
		t.Fatalf("run failed: %+v", res)
	}
	if got := columns(b); len(got) != 2 || got[0] != "in_progress" || got[1] != "done" {
		t.Fatalf("issue moved %v, want [in_progress done]", got)
	}
	for _, m := range b.moves {
		if m.ExtRef != "agent:sess_x" || m.Assignee == "" || m.Title == "" {
			t.Fatalf("every move must carry the whole issue, got %+v", m)
		}
	}
}

// A failed run goes back to todo: it needs someone, and it is not done.
func TestAFailedRunGoesBackToTodo(t *testing.T) {
	b := &board{}
	d, _ := dispatcherFor(&fakeSessions{id: "sess_x"}, &fakePR{}, &fakeRunner{err: context.DeadlineExceeded}, true)
	d.Issue = b.put
	d.Run(context.Background(), baseReq())
	if got := columns(b); len(got) != 2 || got[1] != "todo" {
		t.Fatalf("issue moved %v, want it to end in todo", got)
	}
}

// With no board a run is unchanged.
func TestARunWithoutABoardStillRuns(t *testing.T) {
	d, _ := dispatcherFor(&fakeSessions{id: "sess_x"}, &fakePR{}, &fakeRunner{result: RunResult{OK: true}}, true)
	if res := d.Run(context.Background(), baseReq()); !res.OK {
		t.Fatalf("run failed: %+v", res)
	}
}
