package benchmark

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// presetsAs lists the presets who reads, keyed by name.
func presetsAs(t *testing.T, app *zip.App, who caller) map[string]Preset {
	t.Helper()
	code, raw := do(t, app, who, "GET", "/v1/benchmark/presets", nil)
	if code != 200 {
		t.Fatalf("GET presets as %q: %d %s", who.user, code, raw)
	}
	var out presetList
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data) == 0 || out.Data[0].Name != "ultra" || out.Data[0].Owner != "hanzo" {
		t.Errorf("as %q: first row %+v, want the reference blend", who.user, out.Data)
	}
	got := map[string]Preset{}
	for _, p := range out.Data[1:] {
		got[p.Name] = p
	}
	return got
}

// A posted preset is stored for its org, by name, and read back by that org's
// members alone — across a restart, and replaced, never duplicated, when the org
// posts the name again.
func TestAPostedPresetPersistsForItsOrg(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := mount(t)
		blend := map[string]any{"name": "research", "arms": []string{"enso-ultra", "zen5-pro"}, "panel": 2, "owner": "globex", "by": "forged", "at": "2000-01-01T00:00:00Z"}
		code, raw := do(t, app, acme, "POST", "/v1/benchmark/presets", blend)
		var stored presetStored
		_ = json.Unmarshal(raw, &stored)
		if code != 200 || stored.ServedAs != "enso-research" || stored.Preset.Owner != "acme" || stored.Preset.By != "acme/dave" ||
			stored.Preset.At.Year() == 2000 || strings.Join(stored.Preset.Rank, ",") != "enso-ultra,zen5-pro" {
			t.Fatalf("compose: %d %+v, want 200 stored as enso-research for acme by acme/dave, now, rank defaulted", code, stored)
		}

		if p, ok := presetsAs(t, app, acme)["research"]; !ok || p.Owner != "acme" || p.Panel != 2 || len(p.Arms) != 2 {
			t.Errorf("acme reads %+v (found %v), want its stored blend", p, ok)
		}
		for _, who := range []caller{nobody, globex, switched} {
			if got := presetsAs(t, app, who); len(got) != 0 {
				t.Errorf("as %q (org %s) read %v, want the reference blend only", who.user, who.org, got)
			}
		}

		// A restart is a new process over the same data.
		reopened := restarted(t, s.State.store)
		restart := zip.New(zip.Config{Logger: luxlog.New("test")})
		restart.Use(cloud.Bridge())
		routes(restart, &cloud.Service[state]{Base: s.Base, State: state{store: reopened, audit: s.State.audit}})
		if _, ok := presetsAs(t, restart, acme)["research"]; !ok {
			t.Error("the stored blend did not survive a restart")
		}
		s.State.store = reopened

		time.Sleep(time.Millisecond)
		blend["arms"], blend["panel"] = []string{"zen5-pro"}, 1
		if code, raw := do(t, app, acme, "POST", "/v1/benchmark/presets", blend); code != 200 {
			t.Fatalf("replace: %d %s", code, raw)
		}
		got := presetsAs(t, app, acme)
		if len(got) != 1 || strings.Join(got["research"].Arms, ",") != "zen5-pro" {
			t.Errorf("after a replace acme reads %+v, want one research blend of zen5-pro", got)
		}
		if n := len(versionsIn(t, s.State.store, "acme")); n != 2 {
			t.Errorf("the store holds %d versions, want both, appended", n)
		}
	})
}

// A name is the part after enso-, so a bare model id cannot be expressed and no
// blend shadows one of Hanzo's own tiers. Every other shape the doc refuses is a
// 422, and nothing refused is stored.
func TestAPresetNameIsTheSuffix(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := mount(t)
		arms := []string{"enso-ultra", "zen5-pro"}
		for _, tc := range []struct {
			why  string
			body map[string]any
		}{
			{"the prefix", map[string]any{"name": "enso-research", "arms": arms}},
			{"a capital", map[string]any{"name": "Research", "arms": arms}},
			{"an underscore", map[string]any{"name": "re_search", "arms": arms}},
			{"a leading hyphen", map[string]any{"name": "-research", "arms": arms}},
			{"65 characters", map[string]any{"name": strings.Repeat("r", 65), "arms": arms}},
			{"a Hanzo tier", map[string]any{"name": "flash", "arms": arms}},
			{"an arm twice", map[string]any{"name": "twice", "arms": []string{"zen5-pro", "zen5-pro"}}},
			{"an undeclared rank", map[string]any{"name": "rank", "arms": arms, "rank": []string{"gpt-5.5"}}},
			{"a panel wider than the rank", map[string]any{"name": "wide", "arms": arms, "panel": 3}},
			{"33 arms", map[string]any{"name": "many", "arms": func() []string {
				a := make([]string, maxArms+1)
				for i := range a {
					a[i] = fmt.Sprintf("arm-%d", i)
				}
				return a
			}()}},
			{"a long note", map[string]any{"name": "noted", "arms": arms, "note": strings.Repeat("n", maxNote+1)}},
		} {
			if code, raw := do(t, app, acme, "POST", "/v1/benchmark/presets", tc.body); code != 422 {
				t.Errorf("%s: %d %s, want 422", tc.why, code, raw)
			}
		}
		if code, _ := do(t, app, acme, "POST", "/v1/benchmark/presets", map[string]any{"name": "", "arms": arms}); code != 400 {
			t.Errorf("no name: %d, want 400", code)
		}
		if got := presetsAs(t, app, acme); len(got) != 0 {
			t.Errorf("refused blends were stored: %v", got)
		}
		if code, raw := do(t, app, acme, "POST", "/v1/benchmark/presets", map[string]any{"name": "a-1", "arms": arms}); code != 200 {
			t.Errorf("a-1: %d %s, want 200", code, raw)
		}
	})
}

// An org holds at most maxPresets names: a new one past that is refused, and
// replacing one it holds is not.
func TestAnOrgHoldsAtMostItsPresets(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := mount(t)
		for i := 0; i < maxPresets; i++ {
			p := Preset{Name: fmt.Sprintf("p%d", i), Owner: "acme", Arms: []string{"zen5-pro"}, Rank: []string{"zen5-pro"}, Panel: 1, At: time.Now().UTC()}
			if err := s.State.store.PutPreset(bg, p); err != nil {
				t.Fatal(err)
			}
		}
		if code, _ := do(t, app, acme, "POST", "/v1/benchmark/presets", map[string]any{"name": "one-more", "arms": []string{"zen5-pro"}}); code != 422 {
			t.Errorf("past the bound: %d, want 422", code)
		}
		if code, raw := do(t, app, acme, "POST", "/v1/benchmark/presets", map[string]any{"name": "p7", "arms": []string{"enso-ultra"}}); code != 200 {
			t.Errorf("replacing a held name: %d %s, want 200", code, raw)
		}
		if code, raw := do(t, app, globex, "POST", "/v1/benchmark/presets", map[string]any{"name": "one-more", "arms": []string{"zen5-pro"}}); code != 200 {
			t.Errorf("globex under its own bound: %d %s, want 200", code, raw)
		}
	})
}

// An org stores at most maxPresetWrites versions per UTC day; yesterday's do not
// count, and past the bound nothing is stored.
func TestAnOrgsDailyPresetWritesAreBounded(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := mount(t)
		now := time.Now().UTC()
		for i := 0; i < maxPresetWrites; i++ {
			at := now
			if i < 5 {
				at = now.Add(-24 * time.Hour)
			}
			if err := s.State.store.PutPreset(bg, Preset{Name: "churn", Owner: "acme", Arms: []string{"zen5-pro"}, Rank: []string{"zen5-pro"}, Panel: 1, At: at}); err != nil {
				t.Fatal(err)
			}
		}
		blend := map[string]any{"name": "churn", "arms": []string{"enso-ultra"}}
		for i := 0; i < 5; i++ {
			if code, raw := do(t, app, acme, "POST", "/v1/benchmark/presets", blend); code != 200 {
				t.Fatalf("write %d of the day's last 5: %d %s", i, code, raw)
			}
		}
		if code, _ := do(t, app, acme, "POST", "/v1/benchmark/presets", blend); code != 429 {
			t.Errorf("past the daily bound: %d, want 429", code)
		}
		if n := len(versionsIn(t, s.State.store, "acme")); n != maxPresetWrites+5 {
			t.Errorf("stored %d versions, want %d", n, maxPresetWrites+5)
		}
	})
}
