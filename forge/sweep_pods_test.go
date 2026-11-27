package forge

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/driver/kv/kvtest"
	"github.com/hanzoai/cloud/internal/runs"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/coord/coordtest"
)

// A ROLLING DEPLOY. Pod B boots and sweeps the org's run keys while pod A is still up
// and executing a run that has worked for hours. The key is A's run's, and B leaves
// it. When A is gone nothing holds the run, and the next sweep takes the key.
func TestSweepOrg_LeavesTheKeyOfARunOnAnotherPodUntilThePodIsGone(t *testing.T) {
	for name, mk := range map[string]func(t *testing.T) func(string) *coord.Coord{
		"in process": func(t *testing.T) func(string) *coord.Coord { return coordtest.Memory(t) },
		"kv": func(t *testing.T) func(string) *coord.Coord {
			pod, _ := kvtest.Mini(t)
			return pod
		},
	} {
		t.Run(name, func(t *testing.T) {
			pod := mk(t)
			a, b := pod("cloud-0"), pod("cloud-1")
			s := newRepoStub(t)
			s.keys = []map[string]any{
				{"id": 1, "title": grantTitle + "sess_far", "created_at": time.Now().Add(-3 * time.Hour).Format(time.RFC3339)},
			}
			stopA := a.Serve(runs.Held, func(context.Context, string, []byte) ([]byte, bool) {
				return runs.Answer([]string{"sess_far"})
			})
			defer stopA()
			booting := s.client(t).Live(func(ctx context.Context) (map[string]bool, error) { return runs.Others(ctx, b) })

			if n, err := booting.SweepOrg(context.Background(), "acme"); err != nil || n != 0 {
				t.Fatalf("pod b, booting, swept %d (%v): the run is pod a's and is working", n, err)
			}
			s.mu.Lock()
			if len(s.deleted) != 0 {
				s.mu.Unlock()
				t.Fatalf("pod b withdrew %v, the key of a run pod a executes", s.deleted)
			}
			s.mu.Unlock()

			stopA() // pod a is gone
			// A fresh client: the repository list a client has read is cached for it.
			n, err := s.client(t).Live(func(ctx context.Context) (map[string]bool, error) { return runs.Others(ctx, b) }).SweepOrg(context.Background(), "acme")
			if err != nil || n != 1 {
				t.Fatalf("with pod a gone, swept %d (%v), want the one key", n, err)
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if len(s.deleted) != 1 || !strings.HasSuffix(s.deleted[0], "/keys/1") {
				t.Fatalf("withdrew %v, want the key of the run nobody executes", s.deleted)
			}
		})
	}
}
