package tel

import (
	"context"
	"fmt"
)

// Carrier is what the telecom surface needs from whatever actually moves the
// traffic. Numbers, calls and messages are the three verbs a carrier answers; the
// rest of this package is org isolation, records and policy on top of them.
//
// It is an interface because the carrier is a deployment decision, not a design
// one. A brand may terminate on a different network in a different jurisdiction,
// and a test may terminate on nothing at all — see stub.go, which is what the
// suite runs against. Nothing above this line names a network.
type Carrier interface {
	// Search returns numbers available to buy in a country, optionally filtered
	// to an area or pattern.
	Search(ctx context.Context, q NumberQuery) ([]Number, error)
	// Buy provisions a number. The returned Number carries the carrier's own id,
	// which is what later calls address it by.
	Buy(ctx context.Context, e164 string) (Number, error)
	// Release hands a number back.
	Release(ctx context.Context, id string) error

	// Call places one. The carrier answers with an id as soon as it has accepted
	// the request; the outcome arrives later on the event stream.
	Call(ctx context.Context, r CallRequest) (Call, error)
	// Hangup ends a call in progress.
	Hangup(ctx context.Context, id string) error

	// Send submits a message. Delivery is reported on the event stream, not here:
	// a carrier that returns "sent" synchronously is telling you it accepted the
	// request, and reporting that as delivery is how a message that never arrived
	// gets recorded as one that did.
	Send(ctx context.Context, r MessageRequest) (Message, error)
}

// NumberQuery narrows a search. Country is required — numbering is national, and
// a search without one is a question no carrier can answer.
type NumberQuery struct {
	Country string `json:"country"`
	Area    string `json:"area,omitempty"`
	Type    string `json:"type,omitempty"` // local | national | tollfree | mobile
	Limit   int    `json:"limit,omitempty"`
}

// Number is a phone number as this platform holds it.
type Number struct {
	ID       string `json:"id"`
	E164     string `json:"e164"`
	Country  string `json:"country"`
	Type     string `json:"type"`
	Org      string `json:"org,omitempty"`
	Capable  []string `json:"capable,omitempty"` // voice | sms | mms | fax
	Monthly  int64  `json:"monthly,omitempty"`   // minor units, as the carrier quoted it
	Currency string `json:"currency,omitempty"`
}

// CallRequest is one outbound call. Agent, when set, hands the call to a Hanzo
// assistant instead of connecting it to a person — see agent.go.
type CallRequest struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Agent   string `json:"agent,omitempty"`
	Webhook string `json:"webhook,omitempty"`
	Record  bool   `json:"record,omitempty"`
}

// Call is a call as this platform holds it.
type Call struct {
	ID     string `json:"id"`
	From   string `json:"from"`
	To     string `json:"to"`
	Status string `json:"status"` // queued | ringing | answered | completed | failed
	Org    string `json:"org,omitempty"`
	Agent  string `json:"agent,omitempty"`
}

// MessageRequest is one outbound message.
type MessageRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
	Text string `json:"text"`
	// Media are URLs. A message with media is an MMS to the carrier, and the
	// distinction is the carrier's to make rather than the caller's to declare.
	Media []string `json:"media,omitempty"`
}

// Message is a message as this platform holds it.
type Message struct {
	ID     string `json:"id"`
	From   string `json:"from"`
	To     string `json:"to"`
	Text   string `json:"text"`
	Status string `json:"status"` // queued | sent | delivered | failed
	Org    string `json:"org,omitempty"`
}

// ErrNoCarrier is returned when the surface is mounted without one configured.
// It is an explicit error rather than a nil dereference, because a deployment
// missing its carrier credential should say so on the first request instead of
// panicking on it.
var ErrNoCarrier = fmt.Errorf("tel: no carrier configured")
