// Command gen-fleet-catalog writes what each subsystem serves, so the fleet's
// agent MCP server can answer tools/list without a process per subsystem.
//
// THE SOURCE IS EACH APP'S OWN SPEC, plugin/<app>/openapi.json, which that app's
// own binary emits from its own live router. That is the whole reason this can
// exist: an earlier catalogue was hand-kept per app and drifted — one subsystem
// declared 12 tools while it served 365 — because nothing regenerated it from
// the thing it described. These subsets are regenerated from source by
// `make -f mk/fleet.mk check` and held against the composed document by the compose, so a
// catalogue derived from them is red in CI the moment it disagrees.
//
// It carries operation ids and prose, and no schemas. The MCP server publishes one
// tool per subsystem whose `op` enum holds names, and a model fetches the schema
// for the one it picked — that fetch reaches the owning subsystem, which is one
// process rather than a hundred.
//
// THE AUDIENCE COMES FROM openapi.yaml AND NOT FROM THE SUBSET, and the two are
// not the same answer. A subset's x-public is what the app's own binary could
// derive about itself, and one term of that rule is a fleet fact the app cannot
// see: its STAGE (HIP-0139 §8, stamped by the compose). Read off the subsets, a
// beta capability's 355 operations stayed in the MCP server — offered to every model
// while the same operations were absent from every generated SDK, which is
// exactly the split the paragraph below says does not exist. openapi.yaml IS the
// public contract — the compose writes the customer projection there and everything
// the fleet serves to private.yaml — so reading it is not a second copy of the
// rule; it is the only copy, asked where it has been fully applied.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"sigs.k8s.io/yaml"
)

// op is one operation as its own subsystem published it. Read says the
// operation's method is GET — the one fact the MCP server needs to mark a tool
// read-only, and the only one it cannot derive from an id a child declared.
type op struct {
	ID   string `json:"id"`
	Doc  string `json:"doc"`
	Read bool   `json:"read,omitempty"`
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

	published, err := contract(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gen-fleet-catalog: %v\n", err)
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
				// MCP server as the list of names a child ANSWERS TO. A document
				// carries every route; only a typed op becomes a tool in the child, so
				// an unmarked operation published here is a name the MCP server offers and
				// the child rejects — measured live as `unknown tool` on ten apps'
				// worth of operations. Op's doc comment already promised this
				// ("the id its own registry answers to"); the generator is what
				// disagreed with it. See openapi.Operation.Tool.
				if !o.Tool {
					continue
				}
				// THE MCP SERVER IS THE PUBLIC CONTRACT. An operation the public document
				// leaves out — the operator's /v1/admin family, a relay, a
				// legacy spelling, a capability that is not yet ga — is not offered
				// to a model either; the tool surface and the SDK surface are two
				// projections of one audience, so this asks the document that IS
				// that audience rather than the subset's own guess at it.
				if !published[o.OperationID] {
					continue
				}
				seen[o.OperationID] = true
				// The description, falling back to the summary — the same
				// preference a child's own descriptor carries.
				ops = append(ops, op{ID: o.OperationID, Doc: pick(o.Description, o.Summary), Read: method == "get"})
			}
		}
		// AN APP WITH NO OPERATIONS STILL GETS AN ENTRY, and the difference matters
		// now that the MCP server reads this and asks nothing. Skipping made "publishes
		// no tools" and "was never generated" the same absence, and the MCP server's fallback
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

// contract is every operationId in the published contract, read off openapi.yaml.
//
// An id is a fleet-wide key — openapi.uniqueOperationIDs refuses a document where
// two addresses share one — so membership is all this needs and the address does
// not have to be matched a second time. `make -f mk/fleet.mk documents` writes
// openapi.yaml immediately before running this, from the same subsets, so the two
// always describe one commit.
//
// A missing or empty openapi.yaml is a REFUSAL. Treating it as "nothing is public"
// would silently write a catalog with no tools in it, and the MCP server would answer
// tools/list with an empty fleet — 200 OK, and wrong in the way nobody files.
func contract(root string) (map[string]bool, error) {
	path := filepath.Join(root, "openapi.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w — run `make -f mk/fleet.mk openapi` first", path, err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	ids := map[string]bool{}
	for _, methods := range doc.Paths {
		for method, o := range methods {
			if method == "parameters" || o.OperationID == "" {
				continue
			}
			ids[o.OperationID] = true
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("%s names no operation — the MCP server would publish an empty fleet", path)
	}
	return ids, nil
}

func pick(first, second string) string {
	if first != "" {
		return first
	}
	return second
}
