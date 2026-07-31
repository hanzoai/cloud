package cloud

import (
	"context"
	"net/http"
)

// ObsEventIngestFunc is the observability plane's claim on the canonical event
// door. POST /v1/event is ONE door for every event kind: the analytics app owns
// the route and offers each authenticated body here FIRST; the o11y plane
// claims the bodies that are LLM-observability ingestion batches
// ({"batch":[{"type":"trace-create"|…}]}) and declines everything else, so the
// product-event wire proceeds untouched. claimed=false means "not mine —
// continue"; claimed=true means the receipt (or error) is final. org is the
// SERVER-resolved tenant — the claim never re-derives identity.
type ObsEventIngestFunc func(ctx context.Context, org string, body []byte) (accepted, dropped int, claimed bool, err error)

var obsEventIngest ObsEventIngestFunc

// SetObsEventIngest installs the ONE claim. Called by the o11y subsystem's
// mount when its Datastore sink is available; nil (never installed) simply
// means every body walks the product wire.
func SetObsEventIngest(fn ObsEventIngestFunc) { obsEventIngest = fn }

// ObsEventIngest returns the installed claim, or nil.
func ObsEventIngest() ObsEventIngestFunc { return obsEventIngest }

// obsErrorIngest is the Sentry-wire consumer behind the same door: the o11y
// runtime handler that authenticates a DSN key and stores the envelope. The
// door's owner (analytics) forwards POST /v1/event/{project}/envelope|store
// here — the project segment is variable, so in the fleet router the door's
// owner must carry the route; the consumer is installed, like the batch claim,
// as a seam.
var obsErrorIngest http.Handler

// SetObsErrorIngest installs the Sentry-wire consumer. Called by the o11y
// subsystem once its runtime handler exists.
func SetObsErrorIngest(h http.Handler) { obsErrorIngest = h }

// ObsErrorIngest returns the installed consumer, or nil.
func ObsErrorIngest() http.Handler { return obsErrorIngest }
