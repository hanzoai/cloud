package connectorruntime

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
)

// connectorsFS holds every vendored connector program: real ActivePieces piece
// bundles (produced by the bundle step, *.connector.js) and hand-written
// generic connectors (*.js). Each file's base name (sans extension, sans the
// ".connector" tag) is the connector id.
//
//go:embed connectors/*.js
var connectorsFS embed.FS

// pkg is the process-wide runtime + registry KB and the HTTP surface share.
// One shim, one net/http doer, N compiled connectors. Built at init so the
// in-process API is ready the moment the package is imported (KB does not wait
// for Mount).
var pkg = mustBuildRegistry()

type registry struct {
	rt   *Runtime
	mu   sync.RWMutex
	byID map[string]*Connector
}

func mustBuildRegistry() *registry {
	rt, err := NewRuntime(nil)
	if err != nil {
		panic("connectorruntime: build runtime: " + err.Error())
	}
	reg := &registry{rt: rt, byID: map[string]*Connector{}}
	entries, err := connectorsFS.ReadDir("connectors")
	if err != nil {
		panic("connectorruntime: read connectors: " + err.Error())
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		src, err := fs.ReadFile(connectorsFS, "connectors/"+e.Name())
		if err != nil {
			panic("connectorruntime: read " + e.Name() + ": " + err.Error())
		}
		id := connectorID(e.Name())
		c, err := rt.Compile(id, src)
		if err != nil {
			panic("connectorruntime: compile " + id + ": " + err.Error())
		}
		reg.byID[id] = c
	}
	return reg
}

// connectorID derives the connector id from a file name:
// "brave-search.connector.js" -> "brave-search", "notion.js" -> "notion".
func connectorID(name string) string {
	name = strings.TrimSuffix(name, ".js")
	name = strings.TrimSuffix(name, ".connector")
	return name
}

// Run executes one connector action natively in-process. org is the caller's
// already-validated tenant — recorded for attribution; the runtime resolves no
// credential itself, it runs with the auth the caller passes (the KB path
// hands it this org's KMS-sealed OAuth token). Returns the action result or an
// error (unknown connector/action, or the action's own failure).
func Run(ctx context.Context, org, connector, action string, auth any, props map[string]any) (any, error) {
	pkg.mu.RLock()
	c, ok := pkg.byID[connector]
	pkg.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("connectorruntime: unknown connector %q", connector)
	}
	return pkg.rt.Run(ctx, c, RunInput{Action: action, Auth: auth, Props: props})
}

// Has reports whether a connector is registered (used to fail closed before a
// doomed call).
func Has(connector string) bool {
	pkg.mu.RLock()
	defer pkg.mu.RUnlock()
	_, ok := pkg.byID[connector]
	return ok
}

// Connectors lists the registered connector ids (stable order) for the
// catalogue / health surface.
func Connectors() []string {
	pkg.mu.RLock()
	defer pkg.mu.RUnlock()
	out := make([]string, 0, len(pkg.byID))
	for id := range pkg.byID {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
