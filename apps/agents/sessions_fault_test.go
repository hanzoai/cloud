package agents

import (
	"context"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/fault"
	"github.com/hanzoai/cloud/sdk/mint"
)

// A RUN THAT ENDS IN ERROR IS STATED ONCE, WITH ITS REASON. The first write that
// moves a session to error reports the fault, keyed by its agent and the shape of
// its reason; a later write of the same error, or of a late reason, reports nothing.
//
// MUTATION: drop the `ended` guard and the second write reports again.
func TestASessionEndingInErrorIsStatedOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		var got []fault.Fault
		cloud.SetHooks(cloud.Hooks{ReportFault: func(f fault.Fault) { got = append(got, f) }})
		t.Cleanup(func() { cloud.SetHooks(cloud.Hooks{}) })

		s := testSessionStore(t)
		ctx := context.Background()
		id := "sess_" + mint.ID("f")
		x := mkSession("acme", id, "", id)
		x.Agent = "dev"
		if err := s.CreateSession(ctx, x); err != nil {
			t.Fatal(err)
		}
		x.Status, x.Error, x.EndedAt, x.UpdatedAt = StatusError, "dev exited with status 101", time.Now().Unix(), time.Now().Unix()
		for range 2 {
			if err := s.UpdateSession(ctx, x); err != nil {
				t.Fatal(err)
			}
		}
		if len(got) != 1 {
			t.Fatalf("a session ended in error was stated %d times, want 1", len(got))
		}
		want := fault.Ended("dev", "dev exited with status 101")
		want.Request = id
		if got[0] != want {
			t.Fatalf("fault = %+v, want %+v", got[0], want)
		}
	})
}

func TestInterpreterScriptExitDoesNotReportPlatformFault(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		var got []fault.Fault
		cloud.SetHooks(cloud.Hooks{ReportFault: func(f fault.Fault) { got = append(got, f) }})
		t.Cleanup(func() { cloud.SetHooks(cloud.Hooks{}) })

		s := testSessionStore(t)
		ctx := context.Background()

		// User script exits in python/node harnesses are caller-code failures, not cloud faults.
		for _, reason := range []string{
			"the python harness exited 1: SyntaxError: unterminated triple-quoted string literal",
			"the python harness exited 1: subprocess.CalledProcessError: Command '['/usr/bin/python3', '-m', 'pip', 'install']' returned non-zero exit status 1.",
			"the node harness exited 1: ReferenceError: foo is not defined",
		} {
			id := "sess_" + mint.ID("py")
			x := mkSession("acme", id, "", id)
			x.Agent = "hanzo"
			if err := s.CreateSession(ctx, x); err != nil {
				t.Fatal(err)
			}
			x.Status, x.Error, x.EndedAt, x.UpdatedAt = StatusError, reason, time.Now().Unix(), time.Now().Unix()
			if err := s.UpdateSession(ctx, x); err != nil {
				t.Fatal(err)
			}
		}
		if len(got) != 0 {
			t.Fatalf("interpreter user-script exits reported %d platform faults, want 0: %+v", len(got), got)
		}

		// A killed interpreter (e.g. OOM SIGKILL) is still a platform/sandbox fault and must report.
		id := "sess_" + mint.ID("oom")
		x := mkSession("acme", id, "", id)
		x.Agent = "hanzo"
		if err := s.CreateSession(ctx, x); err != nil {
			t.Fatal(err)
		}
		x.Status, x.Error, x.EndedAt, x.UpdatedAt = StatusError, "the python harness was killed by SIGKILL (exit 137) before it finished its turn", time.Now().Unix(), time.Now().Unix()
		if err := s.UpdateSession(ctx, x); err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Fatalf("killed python harness reported %d faults, want 1", len(got))
		}
	})
}
