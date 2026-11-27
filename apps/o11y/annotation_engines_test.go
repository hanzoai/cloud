package o11y

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/dialect"
)

// A queue renamed to a name its project already holds is a conflict, the write that
// failed leaves the store usable (on PostgreSQL a failed statement aborts its
// transaction, and a rollback has to be all it costs), and the same name in another
// project is not one.
func TestARenameToATakenNameLeavesTheStoreUsable(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, err := openAnnStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		ctx := context.Background()
		for i, q := range []annQueue{
			{ID: "q1", Org: "acme", Project: "p", Name: "first"},
			{ID: "q2", Org: "acme", Project: "p", Name: "second"},
			{ID: "q3", Org: "acme", Project: "other", Name: "first"},
		} {
			q.CreatedAt, q.UpdatedAt = int64(i), int64(i)
			if _, err := st.CreateQueue(ctx, q); err != nil {
				t.Fatalf("create %s: %v", q.ID, err)
			}
		}
		if _, err := st.UpdateQueue(ctx, annQueue{ID: "q2", Org: "acme", Name: "first", UpdatedAt: 9}); !errors.Is(err, errQueueConflict) {
			t.Fatalf("a rename to a taken name = %v, want errQueueConflict", err)
		}
		if _, err := st.CreateQueue(ctx, annQueue{ID: "q4", Org: "acme", Project: "p", Name: "first"}); !errors.Is(err, errQueueConflict) {
			t.Fatalf("a create with a taken name = %v, want errQueueConflict", err)
		}
		got, err := st.GetQueue(ctx, "acme", "q2")
		if err != nil || got.Name != "second" {
			t.Fatalf("the renamed queue = %+v, %v; want it unchanged", got, err)
		}
		if _, err := st.UpdateQueue(ctx, annQueue{ID: "q3", Org: "acme", Name: "renamed", UpdatedAt: 9}); err != nil {
			t.Fatalf("a rename to a free name: %v", err)
		}
	})
}

// A page is a window of the list with the total beside it, and text an engine cannot
// keep is refused before either is asked.
func TestAQueueListPagesAndRefusesText(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, err := openAnnStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		ctx := context.Background()
		for i := range 5 {
			if _, err := st.CreateQueue(ctx, annQueue{ID: fmt.Sprintf("q%d", i), Org: "acme", Project: "p",
				Name: fmt.Sprintf("n%d", i), CreatedAt: int64(i), UpdatedAt: int64(i)}); err != nil {
				t.Fatal(err)
			}
		}
		page, total, err := st.ListQueues(ctx, "acme", "p", 2, 2)
		if err != nil || total != 5 || len(page) != 2 || page[0].Name != "n2" || page[1].Name != "n1" {
			t.Fatalf("page 2 of 5 = %+v, total %d, %v; want n2, n1 of 5", page, total, err)
		}
		if _, err := st.CreateQueue(ctx, annQueue{ID: "bad", Org: "acme", Name: "a\x00b"}); !errors.Is(err, dialect.ErrText) {
			t.Fatalf("a name holding a NUL = %v, want ErrText", err)
		}
		if err := st.AddItems(ctx, []annItem{{ID: "i1", Org: "acme", QueueID: "q0", ObjectID: "t\xff", Status: "PENDING"}}); !errors.Is(err, dialect.ErrText) {
			t.Fatalf("an object id that is not UTF-8 = %v, want ErrText", err)
		}
	})
}
