// Copyright (C) 2020-2026, Hanzo AI Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package event

import (
	_ "embed"
	"encoding/json"
	"sync"
)

// catalog.json is the published event vocabulary, from @hanzo/events. It is
// GENERATED there and copied here — refresh it with:
//
//	cp ../../ui/pkgs/events/dist/catalog.json apps/event/catalog.json
//
// A copy is the honest cost of the endpoint being Go and the catalog being an npm
// package. What a copy must not become is a second DEFINITION: nothing here
// adds a name, and TestCatalogIsTheShapeThePackagePublishes fails if the file
// stops being the thing that package writes. The alternative — fetching it at
// boot — would make ingest depend on the network to answer a question about a
// string, and answer it differently on two pods mid-rollout.
//
//go:embed catalog.json
var catalogJSON []byte

type eventCatalog struct {
	Names    []string `json:"names"`
	Reserved []string `json:"reserved"`
}

var (
	catalogOnce sync.Once
	knownNames  map[string]struct{}
)

func loadCatalog() {
	var c eventCatalog
	// A malformed catalog leaves the set EMPTY, which flags every event rather
	// than refusing any. That is the safe direction: the flag is advisory, so a
	// broken catalog costs a noisy column, while a panic here would cost every
	// event in flight on a plane whose whole job is not losing them.
	_ = json.Unmarshal(catalogJSON, &c)
	knownNames = make(map[string]struct{}, len(c.Names)+len(c.Reserved))
	for _, n := range c.Names {
		knownNames[n] = struct{}{}
	}
	for _, n := range c.Reserved {
		knownNames[n] = struct{}{}
	}
}

// knownEvent reports whether a name is in the published vocabulary.
//
// It answers a question about the CATALOG, never about admission. An unknown
// event is recorded and flagged, never refused: a surface that ships a new
// event before the catalog does would otherwise lose the data outright, and the
// data is the part nobody can recover. A flag can be acted on next week.
func knownEvent(name string) bool {
	catalogOnce.Do(loadCatalog)
	_, ok := knownNames[name]
	return ok
}

// PropUnknownEvent marks an event whose name the vocabulary does not carry.
//
// It is a PROPERTY rather than a column because the property bag already
// travels and needs no DDL, and because this is a fact about the name rather
// than about the event — a taxonomy question, answered where a taxonomy reader
// is already looking. `$` marks it as ours, matching $source and $pageview.
const PropUnknownEvent = "$unknown_event"

// flagUnknown tags an event whose name is not in the vocabulary, so a typo is
// findable later instead of becoming a permanent silent column.
//
// Only a plain `event` is checked. A pageview, identify, group or error carries
// a reserved name the client did not choose, so flagging one would report our
// own vocabulary as unknown.
func flagUnknown(p map[string]any, typ, name string) map[string]any {
	if canonicalType(typ) != "event" || name == "" || knownEvent(name) {
		return p
	}
	out := make(map[string]any, len(p)+1)
	for k, v := range p {
		out[k] = v
	}
	out[PropUnknownEvent] = true
	return out
}
