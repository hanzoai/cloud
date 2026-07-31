package cloud

import (
	"context"
	"fmt"
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

// SlackSenderFunc posts text to an org's Slack channel through the ONE product
// egress — the installed Hanzo app's bot token, custodied in KMS per org. It is
// registered by the integrations subsystem, and lives HERE (the shared cloud
// package) rather than being called across subsystems directly: each subsystem
// is a separate plugin with isolated package globals, so o11y reaching into
// integrations' own `mounted` var sees a nil copy — the exact "integrations:
// not mounted" failure the alert forward hit. A closure registered from
// integrations carries integrations' own linkage, so calling it from anywhere
// runs against the instance that actually holds the token store. Same pattern
// as SetObsEventIngest.
type SlackSenderFunc func(ctx context.Context, org, channel, threadTS, text string) error

var slackSender SlackSenderFunc

// SetSlackSender installs the ONE Slack egress. Called by integrations.Mount.
func SetSlackSender(fn SlackSenderFunc) { slackSender = fn }

// SlackSend posts through the installed egress, or errors if none is installed
// (integrations not mounted / disabled). Never panics on a nil seam.
func SlackSend(ctx context.Context, org, channel, threadTS, text string) error {
	fn := slackSender
	if fn == nil {
		return fmt.Errorf("slack egress not installed (integrations subsystem unmounted)")
	}
	return fn(ctx, org, channel, threadTS, text)
}
