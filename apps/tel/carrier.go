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
	Send(ctx context.Context, r SMSRequest) (SMS, error)
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
	// ID is the carrier's handle for the number, and the id every route here
	// addresses it by. It is not the number itself — see E164.
	ID string `json:"id"`
	// E164 is the number in E.164: a leading + and digits only, no spaces or dashes.
	// That is what a carrier accepts and what a search result must be bought by.
	E164 string `json:"e164"`
	// Country is the ISO 3166-1 alpha-2 code the number is issued under. Numbering is
	// national, so this is what makes a search answerable at all.
	Country string `json:"country"`
	// Type is what kind of number it is: "local", "national", "tollfree" or "mobile".
	// It decides both price and what a carrier will let it originate.
	Type string `json:"type"`
	// Org is the tenant holding the number. A search result carries none — nobody
	// holds it yet — which is how an available number is told from a held one.
	Org string `json:"org,omitempty"`
	// Capable is what the number can carry: any of "voice", "sms", "mms", "fax". A
	// number missing "sms" cannot send one no matter what this platform does.
	Capable []string `json:"capable,omitempty"`
	// Monthly is the recurring rental in the MINOR unit of Currency (cents for USD),
	// exactly as the carrier quoted it. It is a price, not a charge: nothing is billed
	// by this field.
	Monthly int64 `json:"monthly,omitempty"`
	// Currency is the ISO 4217 code Monthly is denominated in. Without it the number
	// beside it means nothing, so the two are always read together.
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
	// ID is the carrier's handle for the call — what a hangup or a lookup names.
	ID string `json:"id"`
	// From is the calling number in E.164. It must be one this org holds: a carrier
	// refuses an origination from a number nobody proved they own.
	From string `json:"from"`
	// To is the called number in E.164.
	To string `json:"to"`
	// Status is where the call is: "queued", "ringing", "answered", "completed" or
	// "failed". Only the last two are terminal.
	Status string `json:"status"`
	// Org is the tenant the call was placed for or received by.
	Org string `json:"org,omitempty"`
	// Agent names the Hanzo assistant handling the call. Set means the call was
	// answered by that assistant rather than connected to a person.
	Agent string `json:"agent,omitempty"`
}

// SMSRequest is one outbound message.
type SMSRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
	Text string `json:"text"`
	// Media are URLs. A message with media is an MMS to the carrier, and the
	// distinction is the carrier's to make rather than the caller's to declare.
	Media []string `json:"media,omitempty"`
}

// SMS is a message as this platform holds it -- text, or media, or both;
// the carriers call the media case MMS and route it over the same number.
type SMS struct {
	// ID is the carrier's handle for the message.
	ID string `json:"id"`
	// From is the sending number in E.164, and must be one this org holds.
	From string `json:"from"`
	// To is the receiving number in E.164.
	To string `json:"to"`
	// Text is the message body. Empty is legal when the message carried only media.
	Text string `json:"text"`
	// Status is where the message is: "queued", "sent", "delivered" or "failed".
	// "sent" means the carrier took it; "delivered" means the handset got it, and
	// not every carrier or destination reports that.
	Status string `json:"status"`
	// Org is the tenant the message was sent for or received by.
	Org string `json:"org,omitempty"`
}

// ErrNoCarrier is returned when the surface is mounted without one configured.
// It is an explicit error rather than a nil dereference, because a deployment
// missing its carrier credential should say so on the first request instead of
// panicking on it.
var ErrNoCarrier = fmt.Errorf("tel: no carrier configured")
