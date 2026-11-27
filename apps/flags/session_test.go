package flags

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"
)

// ONE SESSION LENGTH. A plan's session and the Free plan's window read the same
// switch through this one rule: the hours it states, held to 1 through a week, and
// five where it states none — unset, zero or less never means a window of no length,
// and no typed figure overflows into one.
func TestTheSessionIsFiveHoursUnlessSet(t *testing.T) {
	for set, want := range map[int]time.Duration{
		0: 5 * time.Hour, -2: 5 * time.Hour, 1: time.Hour, 3: 3 * time.Hour, 24: 24 * time.Hour,
		168: 168 * time.Hour, 169: 168 * time.Hour, 2_562_048: 168 * time.Hour, 3_000_000: 168 * time.Hour,
	} {
		if got := Session(set); got != want {
			t.Errorf("Session(%d) = %v, want %v", set, got, want)
		}
	}
	if SeedFreeSession != 50 {
		t.Errorf("the Free plan ships %d calls a session, want 50", SeedFreeSession)
	}
}

// A VALUED SWITCH SET INACTIVE IS UNSET. Writing {"active": false} is how a figure is
// taken back: every reader falls to its own default, so the Free plan's day reads
// as no day on this image and as the old image's own figure on a rollback. A figure
// STORED as 0 is not that: it is set, and an image that reads 0 as no limit obeys it.
func TestAnInactiveSwitchIsUnset(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		newTestClient(t)
		ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "hanzo"})
		ask := func() *client.FlagValue {
			t.Helper()
			v, err := value(ctx, &client.FlagValueIn{Key: "ai_allowance_free", Kind: "int", Default: "3"})
			if err != nil {
				t.Fatal(err)
			}
			return v
		}
		stored := `{"active": true, "filters": {"groups": [{"properties": [], "rollout_percentage": 100}], "payloads": {"true": 0}}}`
		if err := SetPlatformSwitch("ai_allowance_free", json.RawMessage(stored), "z@hanzo.ai"); err != nil {
			t.Fatal(err)
		}
		if v := ask(); !v.Set || v.N != 0 {
			t.Fatalf("a stored 0 reads %+v, want set at 0", v)
		}
		if err := SetPlatformSwitch("ai_allowance_free", json.RawMessage(`{"active": false}`), "z@hanzo.ai"); err != nil {
			t.Fatal(err)
		}
		if v := ask(); v.Set {
			t.Fatalf("a switch set inactive reads %+v, want unset so each reader takes its default", v)
		}
	})
}
