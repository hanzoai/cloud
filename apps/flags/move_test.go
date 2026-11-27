package flags

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/principal"
)

// A project's flag definitions leave their file for hanzo-sql whole: every row of every
// table the same, the activity log's ids kept, and the next entry after it numbered
// after them and not from 1.
func TestFlagsMoveFromTheirFile(t *testing.T) {
	dir := t.TempDir()
	project := principal.MustOrgNamespace("acme", "web")
	t.Setenv(locus.Env, "")
	stores := cloud.NewOrgStore[*Store](cloud.Base{DataDir: dir}, "flags", openStore)
	s, err := stores.For(project)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		def := json.RawMessage(fmt.Sprintf(`{"active": true, "name": "flag %d ünï", "filters": {"groups": [{"rollout_percentage": %d}]}}`, i, i*10))
		if err := s.Upsert(fmt.Sprintf("flag-%d", i), def, "me@acme.test"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Upsert("flag-0", json.RawMessage(`{"active": false}`), "you@acme.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Delete("flag-5", "me@acme.test"); err != nil {
		t.Fatal(err)
	}
	want := sqltest.Dump(t, s.db)
	wantActivity, err := s.Activity(100)
	if err != nil {
		t.Fatal(err)
	}
	wantDefs, wantN, err := s.DefsJSON()
	if err != nil {
		t.Fatal(err)
	}
	wantRows, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if err := stores.CloseAll(); err != nil {
		t.Fatal(err)
	}

	sqltest.Server(t)
	t.Setenv(locus.MoveEnv, "lazy")
	stores = cloud.NewOrgStore[*Store](cloud.Base{DataDir: dir}, "flags", openStore)
	t.Cleanup(func() { _ = stores.CloseAll() })
	m, err := stores.For(project)
	if err != nil {
		t.Fatal(err)
	}
	if !dialect.Of(m.db).Postgres() {
		t.Fatal("the flags store did not move")
	}
	sqltest.Same(t, want, sqltest.Dump(t, m.db))

	gotActivity, err := m.Activity(100)
	if err != nil || !reflect.DeepEqual(gotActivity, wantActivity) {
		t.Fatalf("the activity log changed in the move: %v\n got %+v\nwant %+v", err, gotActivity, wantActivity)
	}
	gotDefs, gotN, err := m.DefsJSON()
	if err != nil || gotN != wantN || string(gotDefs) != string(wantDefs) {
		t.Fatalf("the definitions changed in the move: %v\n got %s\nwant %s", err, gotDefs, wantDefs)
	}
	if gotRows, err := m.List(); err != nil || !reflect.DeepEqual(gotRows, wantRows) {
		t.Fatalf("the definition rows changed in the move: %v", err)
	}

	// The log's next id follows the ones it had.
	if err := m.Upsert("after", json.RawMessage(`{"active": true}`), "me@acme.test"); err != nil {
		t.Fatal(err)
	}
	last, err := m.Activity(1)
	if err != nil || len(last) != 1 || last[0].ID != wantActivity[0].ID+1 {
		t.Fatalf("the first activity after the move is %+v, %v; want id %d", last, err, wantActivity[0].ID+1)
	}
}
