package claw

import (
	"encoding/json"
	"fmt"
	"sort"
	"testing"

	"github.com/zap-proto/zip"
)

// The configuration is one document and a write replaces the whole of it, so
// the hash a caller states — "this is the document I read" — has to be checked
// against the document being replaced. Checked against a copy read a moment
// earlier, two writers both pass and the second drops everything the first
// wrote, having been told it was writing over what it had seen.

// config reads the stored configuration and the hash a write must present.
func config(t *testing.T, app *zip.App, w who) (map[string]any, string) {
	t.Helper()
	_, frame := ask(t, app, w, "cfg", "config.get", `{}`)
	if frame["ok"] != true {
		t.Fatalf("config.get refused: %v", frame)
	}
	view := payload(t, frame)
	cfg, _ := view["config"].(map[string]any)
	hash, _ := view["hash"].(string)
	if hash == "" {
		t.Fatalf("the configuration states no hash to write against: %v", view)
	}
	return cfg, hash
}

func TestConcurrentConfigPatchesAllLand(t *testing.T) {
	app := mount(t)
	me := boss("acme")

	for round := range 6 {
		_, hash := config(t, app, me)

		keys := []string{"a", "b", "c", "d", "e", "f"}
		params := make([]string, len(keys))
		for i, key := range keys {
			raw, err := json.Marshal(map[string]any{key: fmt.Sprintf("r%d", round)})
			if err != nil {
				t.Fatalf("build patch: %v", err)
			}
			body, err := json.Marshal(map[string]any{"raw": string(raw), "baseHash": hash})
			if err != nil {
				t.Fatalf("build params: %v", err)
			}
			params[i] = string(body)
		}

		took := []string{}
		for i, frame := range atOnce(t, app, me, "config.patch", params) {
			if frame["ok"] == true {
				took = append(took, keys[i])
			}
		}
		if len(took) == 0 {
			t.Fatal("every patch stating the hash it read was refused")
		}

		cfg, _ := config(t, app, me)
		lost := []string{}
		for _, key := range took {
			if cfg[key] != fmt.Sprintf("r%d", round) {
				lost = append(lost, key)
			}
		}
		sort.Strings(lost)
		if len(lost) > 0 {
			t.Fatalf("%d of %d accepted config patches are not in the configuration: %v — "+
				"each stated the hash of a document it was then not written against",
				len(lost), len(took), lost)
		}
	}
}
