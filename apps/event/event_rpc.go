// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package event

// event_rpc.go — the event endpoint, for a peer in ANOTHER PROCESS.
//
// This package owns POST /v1/event, event.fact and the /v1/event/insights reads over
// them. The pod forks one process per app, so an app that wanted its OWN facts
// answerable in the same query as the product's had two ways to get there and
// neither one was this:
//
//	IN-PROCESS   ingestEvents is the write core and it is unreachable from
//	             another pid. This is the shape apps/o11y/obs_rpc.go names in its
//	             own header — cloud.SetObsErrorIngest was written in one process
//	             and read as nil in the one that needed it — and it is the same
//	             mistake one layer over.
//	OVER HTTP    the public edge: a second gate, a second credential and a hop
//	             through the fleet's edge to reach a table one socket away.
//	             A peer with no browser and no bearer would have to be issued one
//	             to state a fact about its own work.
//
// So the peer ASKS, on the app's own socket, exactly as a debit asks commerce and
// a decision asks risk. It is the WRITE side of the surface ObsErrorPost already
// claims a slice of, and it reaches the SAME write core every HTTP endpoint reaches
// — one admission, one normalizer, one storage projection. A second path into
// event.fact would be a second answer to "what is an event", and the two would
// disagree the first time the scrubber changed.
//
// THE TENANT IS THE CALLER'S. It is minted from the plane principal and there is
// no field on the wire that could name one, so a peer can only ever write into
// its own organisation's partition. That is the HTTP endpoint's rule (analytics.go's
// tenancy paragraph) applied at the other endpoint, and it is the whole of the
// isolation: `org` is stamped into the fact by normalize, and every read binds it
// positionally.

import (
	"context"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud"
	planeops "github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// exposeCapture publishes the endpoint. build calls it.
func exposeCapture() {
	zip.Post[planeops.EventIn, planeops.EventCaptured](cloud.Plane(), "/event/capture", planeCapture,
		zip.WithOperationID(planeops.EventCapture),
		zip.WithSummary("Capture one occurrence onto the calling organisation's own event plane"))
}

// Captures ONE occurrence onto the calling organisation's own event plane — the
// same plane the HTTP endpoint fills and /v1/event/insights reads back, through the
// SAME write core.
//
// The organisation is the CALLER's, minted from the plane principal and never
// from this body, which carries no field that could name one. A peer that states
// no principal writes nothing: an unidentified caller has no partition, and
// defaulting one would be a shared store with a tenant anybody can reach.
//
// A name is REQUIRED and is refused rather than defaulted. Every other route into
// this core has a server-chosen default name for the signal it carries (a page
// view, an error, a span); a peer's occurrence is a tracked act, and a tracked
// act with no name is unroutable by the plane's own rule — so the refusal is the
// normalizer's, said at the boundary where the caller can still be told.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeCapture(ctx context.Context, in *planeops.EventIn) (*planeops.EventCaptured, error) {
	org := strings.TrimSpace(cloud.Who(ctx).Org)
	if org == "" {
		return nil, zip.ErrForbidden("capture: no org on the call, so the occurrence acts for no tenant")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, zip.ErrBadRequest("capture: 'name' is required — an unnamed act is unroutable")
	}
	attrs := stated(in.Attributes)
	revenue, _ := strconv.ParseFloat(said(attrs, "revenue"), 64)
	quantity, _ := strconv.ParseUint(said(attrs, "quantity"), 10, 32)
	res, err := ingestEvents(ctx, org, sourcePlane, []CaptureEvent{{
		Event:      name,
		Product:    strings.TrimSpace(in.Product),
		DistinctID: strings.TrimSpace(in.Subject),
		Timestamp:  strings.TrimSpace(in.At),
		// THE FOUR COMMERCE FACTS ARE COLUMNS, and this endpoint has to say so. Every
		// value on this plane is TEXT ([planeops.Signal] is its one name/value pair),
		// so a peer's sale arrived with revenue, currency, product and quantity
		// sitting in the property bag as strings while the fields the rest of the
		// program reads them from stayed zero — the /v1/event/insights commerce lens summed
		// nothing, and apps/destinations forwarded a purchase worth nothing to every
		// connected platform. They are read under the SAME names the HTTP wire spells
		// them with (CaptureEvent's json tags), so one sale has one vocabulary
		// whichever endpoint it came in through, and an unparseable value is simply the
		// zero it already was.
		Revenue:    revenue,
		Currency:   said(attrs, "currency"),
		ProductID:  said(attrs, "productId"),
		Quantity:   uint32(quantity),
		Properties: attrs,
	}})
	if err != nil {
		return nil, err
	}
	return &planeops.EventCaptured{Accepted: res.Accepted, Dropped: res.Dropped}, nil
}

// stated lifts the wire's name/value list into the property bag the write core
// reads. It is the plane's inverse of the map a map could not be: the list is the
// wire because zapenc refuses a map at encode, and this is the one place the two
// shapes meet.
//
// An unnamed pair is DROPPED rather than stored under "": an empty key and an
// absent one read identically out of the store, so keeping it buys nothing and
// costs a key in every row's dictionary — attributesOf makes the same call about
// empty VALUES for the same reason.
func stated(list []planeops.Signal) map[string]any {
	if len(list) == 0 {
		return nil
	}
	out := make(map[string]any, len(list))
	for _, s := range list {
		if name := strings.TrimSpace(s.Name); name != "" {
			out[name] = s.Value
		}
	}
	return out
}

// said returns what the peer stated under name, trimmed, "" if it stated nothing.
// Every value on this plane is text, so this is the only shape a stated fact has —
// which is why the columns above are parsed out of it rather than type-asserted.
func said(attrs map[string]any, name string) string {
	s, _ := attrs[name].(string)
	return strings.TrimSpace(s)
}
