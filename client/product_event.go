package client

import "encoding/json"

// AutoEventDeliver reaches the existing durable automation trigger engine.
const AutoEventDeliver = "auto_event_deliver"

// ProductEvent is a committed product outcome. The caller's verified org is
// carried by the plane; the event body cannot choose another tenant.
type ProductEvent struct {
	// Source is the producing capability: research, experiment, benchmark or agent.
	Source string `json:"source"`
	// Name is the event within that capability, such as improvement.reviewed.
	Name string `json:"name"`
	// Key is the stable identity of this exact evidence version, for deduplication.
	Key string `json:"key"`
	// Depth is the causation depth inherited from the originating automation.
	Depth int `json:"depth"`
	// Payload contains bounded structured metadata, never raw session transcripts.
	Payload json.RawMessage `json:"payload"`
}

// ProductEventDelivered reports how many matching enabled flows were started.
type ProductEventDelivered struct {
	// Matched is the number of new durable runs, excluding duplicate deliveries.
	Matched int `json:"matched"`
}
