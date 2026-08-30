// gen-mcp-catalog projects fleet/catalog.json onto the surface an MCP client
// needs to offer the fleet without asking for it.
//
// The grouped projection is one tool per subsystem carrying its operation names
// in an enum, and a caller fetches one operation's own prose through `describe`
// when it picks one. So a client needs the names and the count, not the prose —
// which is the difference between 640K and something a package can carry.
//
// The client cannot ask the endpoint for this: a tool list is assembled
// synchronously at registration, before any request has been made.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/hanzoai/cloud/fleet"
)

// entry is one subsystem as a client offers it.
type entry struct {
	Ops []string `json:"ops"`
}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	in := filepath.Join(root, "fleet", "catalog.json")
	out := filepath.Join(root, "fleet", "mcp.json")

	raw, err := os.ReadFile(in)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read catalog:", err)
		os.Exit(1)
	}
	var catalog map[string][]struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		fmt.Fprintln(os.Stderr, "parse catalog:", err)
		os.Exit(1)
	}

	// A subsystem with no operation yields no tool, the same rule the endpoint's
	// own grouping applies — an empty enum is a tool a model cannot call.
	surface := map[string]entry{}
	ops := 0
	for name, list := range catalog {
		if len(list) == 0 {
			continue
		}
		// The endpoint withholds an operation whose name discloses a bearer secret,
		// or that mutates identity or authority. A client offering what the fleet
		// refuses would hold the policy on one transport and not the other, so the
		// SAME predicate decides here — never a second copy of the words.
		ids := make([]string, 0, len(list))
		for _, op := range list {
			if fleet.Withheld(op.ID) {
				continue
			}
			ids = append(ids, op.ID)
		}
		// Withholding can empty a subsystem, and an empty enum is a tool a model
		// cannot call — so it yields no tool at all, which is what the endpoint's
		// own grouping does with a name nothing survived under.
		if len(ids) == 0 {
			continue
		}
		sort.Strings(ids)
		surface[name] = entry{Ops: ids}
		ops += len(ids)
	}

	body, err := json.MarshalIndent(surface, "", " ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "encode:", err)
		os.Exit(1)
	}
	body = append(body, '\n')
	if err := os.WriteFile(out, body, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "write:", err)
		os.Exit(1)
	}
	fmt.Printf("fleet/mcp.json: %d subsystems, %d operations, %d bytes\n", len(surface), ops, len(body))
}
