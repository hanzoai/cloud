package patrol

import (
	"net/http"
	"sync"
	"testing"
)

// act_test.go holds one act to one application. advance reads the state,
// decides, then writes, and a transition that ran twice leaves two rows saying
// one thing happened.

// TestOneActRunsOnce proves a transition is applied once. advance reads the
// state, decides, then writes — three steps with nothing holding the document
// between them, so two callers racing the same act both read `received`, both
// pass the table, and both land.
func TestOneActRunsOnce(t *testing.T) {
	app := mount(t)
	name := opened(t, app)

	const racers = 8
	var wg sync.WaitGroup
	codes := make([]int, racers)
	start := make(chan struct{})
	for i := range racers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			code, _ := call(t, app, http.MethodPost, "/v1/patrol/incident/"+name+"/step", org, controller,
				map[string]any{"act": Verify})
			codes[i] = code
		}(i)
	}
	close(start)
	wg.Wait()

	won := 0
	for _, c := range codes {
		if c == http.StatusOK {
			won++
		}
	}
	if won != 1 {
		t.Errorf("%d of %d racing VERIFYs were applied, want exactly 1: %v", won, racers, codes)
	}

	code, raw := call(t, app, http.MethodGet, "/v1/patrol/incident/"+name, org, controller, nil)
	if code != http.StatusOK {
		t.Fatalf("read the incident want 200, got %d (%s)", code, raw)
	}
	var got patrolIncident
	data(t, raw, &got)
	verifies := 0
	for _, s := range got.Steps {
		if s.Act == Verify {
			verifies++
		}
	}
	if verifies != 1 {
		t.Errorf("the trail carries %d VERIFY steps for one transition, want 1: %+v", verifies, got.Steps)
	}
}
