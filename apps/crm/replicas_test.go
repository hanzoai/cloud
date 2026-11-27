package crm

// The CRM's writers run on every replica. These tests stand a second replica beside the
// first: another Keyed beside the same database, which is what another pod is; on SQLite
// there is one process and the Keyed is the whole lock.

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/lock"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/locus"
)

// Events about one form delivered together to two replicas all land: each replica reads
// the form's data and writes it back whole, so without the form's lock across replicas one
// event is written over by the other's. Each change waits for the other to have read the
// form, which is the moment two replicas would each write over the other.
func TestEventsAboutOneFormOnTwoReplicasAllLand(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := open(t)
		d := betaLead()
		if err := FileLead(ctx, org, "application", d, filedAt); err != nil {
			t.Fatalf("file: %v", err)
		}
		ref := d["public_ref"].(string)

		var mu sync.Mutex
		var reads int
		met := make(chan struct{})
		meet := func() {
			mu.Lock()
			reads++
			first := reads <= 2
			if reads == 2 {
				close(met)
			}
			mu.Unlock()
			if first {
				select {
				case <-met:
				case <-time.After(300 * time.Millisecond): // alone: the lock held the other out
				}
			}
		}
		replicas := []*lock.Keyed{{}, {}}
		if locus.DSN() == "" {
			replicas[1] = replicas[0] // a file belongs to one process: one Keyed
		}
		const events = 6
		var wg sync.WaitGroup
		for i := range events {
			wg.Add(1)
			go func() {
				defer wg.Done()
				name := fmt.Sprintf("event-%d", i)
				_, err := amend(context.Background(), replicas[i%2], org, "webhook", ref, func(data map[string]any) bool {
					meet()
					seen, _ := data["events"].([]any)
					data["events"] = append(seen, name)
					return true
				})
				if err != nil {
					t.Errorf("amend %s: %v", name, err)
				}
			}()
		}
		wg.Wait()
		got, err := Held(ctx, org, ref)
		if err != nil {
			t.Fatal(err)
		}
		if seen, _ := got["events"].([]any); len(seen) != events {
			t.Fatalf("%d of %d events about one form were kept: %v", len(seen), events, seen)
		}
	})
}
