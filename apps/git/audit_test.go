package git

// The directory audit says which directories on the volume the store no longer needs, and
// deletes none of them.

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
)

func TestTheAuditListsWhatTheStoreHoldsAndDeletesNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		s := service(t)
		ctx := context.Background()
		// A directory is made read-only when it is copied in; the test's cleanup must delete it.
		t.Cleanup(func() {
			for _, n := range []string{"adopted", "moved", "waiting"} {
				s.State.storage.thaw("acme", "", n)
			}
		})

		// A directory the store does not hold yet.
		_, _, _ = legacy(t, "acme", "waiting")
		// A directory brought in, equal.
		adopted, _, _ := legacy(t, "acme", "adopted")
		if objs, err := s.State.vault.home(ctx, s, adopted); err != nil || objs == nil {
			t.Fatalf("adopt: %v %v", objs, err)
		}
		// A directory brought in, then written to behind the store's back.
		moved, mdir, _ := legacy(t, "acme", "moved")
		if objs, err := s.State.vault.home(ctx, s, moved); err != nil || objs == nil {
			t.Fatalf("adopt: %v %v", objs, err)
		}
		s.State.storage.thaw("acme", "", "moved")
		_ = os.Chmod(mdir, 0o755)
		w := work(t)
		commitFile(t, w, "behind.txt", "written to the directory only\n", "behind")
		gitRun(t, w, "push", "-q", "-f", mdir, "main:refs/heads/main")
		// A linked repository's old cache directory.
		linkedIn(t, app, "cached", "# c\n")
		if err := s.State.storage.initBare("acme", "", "cached", "main"); err != nil {
			t.Fatal(err)
		}
		// A directory nothing names.
		if err := s.State.storage.initBare("acme", "", "ghost", "main"); err != nil {
			t.Fatal(err)
		}

		rep, err := s.State.vault.audit(ctx, s)
		if err != nil {
			t.Fatal(err)
		}
		kinds := map[string]dirAudit{}
		for _, a := range append(append([]dirAudit(nil), rep.Deletable...), rep.Unsafe...) {
			kinds[a.Name] = a
		}
		for name, want := range map[string]struct {
			kind string
			safe bool
		}{
			"waiting": {"unadopted", false},
			"adopted": {"adopted", true},
			"moved":   {"differs", false},
			"cached":  {"cache", true},
			"ghost":   {"orphan", false},
		} {
			got, ok := kinds[name]
			if !ok || got.Kind != want.kind || got.Safe != want.safe {
				t.Errorf("%s = %+v, want %s safe=%v", name, got, want.kind, want.safe)
			}
		}
		if rep.Safe != 2 || rep.Dirs != 5 {
			t.Fatalf("report = %d safe of %d", rep.Safe, rep.Dirs)
		}
		for name := range kinds {
			if !s.State.storage.exists("acme", "", name) {
				t.Fatalf("the audit deleted %s", name)
			}
		}
		if b, _ := json.Marshal(rep); len(b) == 0 {
			t.Fatal("the report does not encode")
		}
	})
}
