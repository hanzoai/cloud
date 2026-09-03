// gen-mcp-catalog projects surface/catalog.json onto the surface an MCP client
// needs to offer the surface without asking for it, and writes that projection to
// every runtime that carries one.
//
// The grouped projection is one tool per subsystem carrying its operation names
// in an enum, and a caller fetches one operation's own prose through `describe`
// when it picks one. So a client needs the names and the count, not the prose —
// which is the difference between 640K and something a package can carry.
//
// The client cannot ask the endpoint for this: a tool list is assembled
// synchronously at registration, before any request has been made, and a client
// that needs the network to say what it can do has nothing to say when the
// network is what failed.
//
// It writes the sibling copies because the alternative is one generator per
// language, each with its own idea of the rule — which is what it replaced. This
// is NOT cloud pushing a release: the repos still build, test and publish on
// their own cadence. It writes a file into a checkout already open beside this
// one, and skips a sibling that is not there rather than inventing it.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/hanzoai/cloud/surface"
)

// entry is one subsystem as a client offers it.
type entry struct {
	Ops []string `json:"ops"`
}

// carriers are the runtimes that embed the catalog, relative to the directory
// holding this checkout. The Rust crate reads the first of these directly, so it
// is not listed twice.
var carriers = []string{
	filepath.Join("mcp", "src", "tools", "catalog.json"),
	filepath.Join("python-sdk", "pkg", "hanzo-tools-api", "hanzo_tools", "api", "catalog.json"),
}

func main() {
	root, shrink := ".", false
	for _, a := range os.Args[1:] {
		if a == "--shrink" {
			shrink = true
			continue
		}
		root = a
	}

	projected, ops, err := project(filepath.Join(root, "surface", "catalog.json"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	body, err := json.MarshalIndent(projected, "", " ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "encode:", err)
		os.Exit(1)
	}
	body = append(body, '\n')

	mine := filepath.Join(root, "surface", "mcp.json")

	// A projection smaller than the one it replaces is refused, because a surface
	// answering partially and a surface that lost capabilities look identical from
	// here — and the quiet direction of that mistake is a client that stops
	// offering operations the API still serves.
	if was := count(mine); was > ops && !shrink {
		fmt.Fprintf(os.Stderr,
			"refusing to shrink the catalog: %d operations -> %d.\n"+
				"Re-run when the surface is whole, or pass --shrink if operations were withdrawn.\n",
			was, ops)
		os.Exit(1)
	}

	targets := append([]string{mine}, siblings(root)...)
	for _, t := range targets {
		if err := os.WriteFile(t, body, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(1)
		}
	}
	fmt.Printf("%d subsystems, %d operations, %d bytes\n", len(projected), ops, len(body))
	for _, t := range targets {
		fmt.Println("  " + t)
	}
}

// project reads the catalog and answers what a client may offer.
func project(path string) (map[string]entry, int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("read catalog: %w", err)
	}
	var catalog map[string][]struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return nil, 0, fmt.Errorf("parse catalog: %w", err)
	}

	projected := map[string]entry{}
	ops := 0
	for name, list := range catalog {
		// The endpoint withholds an operation whose name discloses a bearer secret,
		// or that mutates identity or authority. A client offering what the surface
		// refuses would hold the policy on one transport and not the other, so the
		// SAME predicate decides here — never a second copy of the words.
		ids := make([]string, 0, len(list))
		for _, op := range list {
			if surface.Withheld(op.ID) {
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
		projected[name] = entry{Ops: ids}
		ops += len(ids)
	}
	return projected, ops, nil
}

// siblings answers the carrier paths whose directory exists beside this checkout.
func siblings(root string) []string {
	beside, err := filepath.Abs(filepath.Join(root, ".."))
	if err != nil {
		return nil
	}
	var found []string
	for _, rel := range carriers {
		p := filepath.Join(beside, rel)
		if _, err := os.Stat(filepath.Dir(p)); err == nil {
			found = append(found, p)
		}
	}
	return found
}

// count answers how many operations a written projection carries, and 0 when
// there is none to compare against.
func count(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var prior map[string]entry
	if json.Unmarshal(raw, &prior) != nil {
		return 0
	}
	n := 0
	for _, e := range prior {
		n += len(e.Ops)
	}
	return n
}
