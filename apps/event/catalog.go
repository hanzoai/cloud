// Copyright (C) 2020-2026, Hanzo AI Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package event

import (
	_ "embed"
	"encoding/json"
	"maps"
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
	Names     []string                 `json:"names"`
	Reserved  []string                 `json:"reserved"`
	Platforms map[string]PlatformNames `json:"platforms"`
}

// PlatformNames is what each ad and analytics platform calls one event: the
// platform table of @hanzo/events. Standard is the normalized name the adapters
// that name no events (Google Ads, Pinterest, Reddit) key on. A value is one
// name or several; the first is the event and the rest are what the platform
// also wants for the same moment.
type PlatformNames struct {
	Standard string          `json:"standard"`
	GA4      json.RawMessage `json:"ga4"`
	Meta     json.RawMessage `json:"meta"`
	LinkedIn json.RawMessage `json:"linkedin"`
	X        json.RawMessage `json:"x"`
	TikTok   json.RawMessage `json:"tiktok"`
	Reddit   json.RawMessage `json:"reddit"`
	OpenAI   json.RawMessage `json:"openai"`
	Money    bool            `json:"money"`
}

func namesIn(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}
	}
	var many []string
	_ = json.Unmarshal(raw, &many)
	return many
}

// Platform returns what an event is called on a platform (ga4, meta, linkedin,
// x, tiktok), in the order it is sent. Nil when the platform is not sent it.
func Platform(name, platform string) []string {
	catalogOnce.Do(loadCatalog)
	p, ok := platforms[name]
	if !ok {
		return nil
	}
	switch platform {
	case "ga4":
		return namesIn(p.GA4)
	case "meta":
		return namesIn(p.Meta)
	case "linkedin":
		return namesIn(p.LinkedIn)
	case "x":
		return namesIn(p.X)
	case "tiktok":
		return namesIn(p.TikTok)
	case "reddit":
		return namesIn(p.Reddit)
	case "openai":
		return namesIn(p.OpenAI)
	}
	return nil
}

// Listed reports whether the platform table has a row for the event. An event it
// lists is named on each platform by the table alone.
func Listed(name string) bool {
	catalogOnce.Do(loadCatalog)
	_, ok := platforms[name]
	return ok
}

// StandardOf returns an event's normalized name ("" when it has none).
func StandardOf(name string) string {
	catalogOnce.Do(loadCatalog)
	return platforms[name].Standard
}

// CarriesMoney reports whether the event states an amount a platform bids on: a
// purchase, a change of subscription, a refund. Only a peer app or the org's
// secret key may state one.
func CarriesMoney(name string) bool {
	catalogOnce.Do(loadCatalog)
	return platforms[name].Money
}

var (
	catalogOnce sync.Once
	knownNames  map[string]struct{}
	platforms   map[string]PlatformNames
)

func loadCatalog() {
	var c eventCatalog
	// A malformed catalog leaves the set EMPTY, which flags every event rather
	// than refusing any. That is the safe direction: the flag is advisory, so a
	// broken catalog costs a noisy column, while a panic here would cost every
	// event in flight on a plane whose whole job is not losing them.
	_ = json.Unmarshal(catalogJSON, &c)
	platforms = c.Platforms
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
	maps.Copy(out, p)
	out[PropUnknownEvent] = true
	return out
}
