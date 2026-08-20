// Command gen-fleet-catalog writes what each subsystem serves, so the fleet's
// agent door can answer tools/list without a process per subsystem.
//
// THE SOURCE IS EACH APP'S OWN SPEC, plugin/<app>/openapi.json, which that app's
// own binary emits from its own live router. That is the whole reason this can
// exist: an earlier catalogue was hand-kept per app and drifted — one subsystem
// declared 12 tools while it served 365 — because nothing regenerated it from
// the thing it described. These subsets are regenerated from source by
// `make -f mk/fleet.mk check` and held against openapi.yaml by the weave, so a
// catalogue derived from them is red in CI the moment it disagrees.
//
// It carries operation ids and prose, and no schemas. The door publishes one
// tool per subsystem whose `op` enum holds names, and a model fetches the schema
// for the one it picked — that fetch reaches the owning subsystem, which is one
// process rather than a hundred.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// op is one operation as its own subsystem published it.
type op struct {
	ID  string `json:"id"`
	Doc string `json:"doc"`
}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	specs, err := filepath.Glob(filepath.Join(root, "plugin", "*", "openapi.json"))
	if err != nil || len(specs) == 0 {
		fmt.Fprintf(os.Stderr, "gen-fleet-catalog: no specs at %s/plugin/*/openapi.json (%v)\n", root, err)
		os.Exit(1)
	}

	out := map[string][]op{}
	for _, path := range specs {
		app := filepath.Base(filepath.Dir(path))
		raw, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "gen-fleet-catalog: read %s: %v\n", path, err)
			os.Exit(1)
		}
		var doc struct {
			Paths map[string]map[string]struct {
				OperationID string `json:"operationId"`
				Summary     string `json:"summary"`
				Description string `json:"description"`
				// x-tool, written by openapi.Fold for the ops the app's own typed
				// registry holds — the same registry zip builds its MCP tools from.
				Tool bool `json:"x-tool"`
			} `json:"paths"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			fmt.Fprintf(os.Stderr, "gen-fleet-catalog: parse %s: %v\n", path, err)
			os.Exit(1)
		}
		seen := map[string]bool{}
		var ops []op
		for _, methods := range doc.Paths {
			for method, o := range methods {
				// The document's own junk keys are not operations.
				if method == "parameters" || o.OperationID == "" || seen[o.OperationID] {
					continue
				}
				// DESCRIBED IS NOT DISPATCHABLE, and this catalog is read by the
				// door as the list of names a child ANSWERS TO. A document carries
				// every route; only a typed op becomes a tool in the child, so an
				// unmarked operation published here is a name the door offers and
				// the child rejects — measured live as `unknown tool` on ten apps'
				// worth of operations. Op's doc comment already promised this
				// ("the id its own registry answers to"); the generator is what
				// disagreed with it. See openapi.Operation.Tool.
				if !o.Tool {
					continue
				}
				seen[o.OperationID] = true
				// The description, falling back to the summary — the same
				// preference a child's own descriptor carries.
				ops = append(ops, op{ID: o.OperationID, Doc: pick(o.Description, o.Summary)})
			}
		}
		// AN APP WITH NO OPERATIONS STILL GETS AN ENTRY, and the difference matters
		// now that the door reads this and asks nothing. Skipping made "publishes no
		// tools" and "was never generated" the same absence, and the door's fallback
		// for absence used to be to ASK the subsystem — so a missing entry cost a
		// cold start and was invisible. With no asking left, the same absence would
		// silently publish less than the fleet routes.
		//
		// Present-and-empty says the app was read and had nothing; absent now means
		// missing, which fleet's coverage gate can refuse.
		sort.Slice(ops, func(i, j int) bool { return ops[i].ID < ops[j].ID })
		if ops == nil {
			ops = []op{}
		}
		out[app] = ops
	}

	body, err := json.MarshalIndent(out, "", " ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "gen-fleet-catalog: encode: %v\n", err)
		os.Exit(1)
	}
	body = append(body, '\n')
	dst := filepath.Join(root, "fleet", "catalog.json")
	if err := os.WriteFile(dst, body, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "gen-fleet-catalog: write %s: %v\n", dst, err)
		os.Exit(1)
	}
	n := 0
	for _, ops := range out {
		n += len(ops)
	}
	fmt.Printf("%s: %d subsystems, %d operations\n", dst, len(out), n)
}

func pick(first, second string) string {
	if first != "" {
		return first
	}
	return second
}
