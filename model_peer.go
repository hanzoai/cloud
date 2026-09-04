// Copyright © 2026 Hanzo AI. MIT License.

package cloud

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/client/settings"
	luxlog "github.com/luxfi/log"
)

// Which model a Hanzo surface runs on, asked at request time rather than compiled.
//
// AI routing moves on a different clock than this binary. A tier gets repriced, a
// backend is retired, a family is not served on a given deployment — none of which
// should need a rebuild, a release train and a rollout to answer. So the roles
// below resolve through the platform's own product configuration: the reserved
// admin org's `ai` document, edited at admin.hanzo.ai, read on the next request.
//
// This is the SETTINGS engine every tenant's product config already goes through
// (apps/settings), not a second store: the platform is just another (org, product)
// key. It is also the shape rate_peer.go uses for a price, and for the same
// reason — one authority, an audit trail, and a compiled FLOOR that keeps serving
// when the authority cannot be reached.
//
// WHY THIS IS NOT THE KNOB THAT WAS DELETED. model.go records a deployment knob
// (CLOUD_AI_DEFAULT_MODEL) being removed because it was a SECOND place holding one
// decision, and it drifted twice — once shipping an upstream name to customers,
// once masking a wrong constant because production set the variable to a different
// value. The defect was two sources, not runtime resolution. Here there is one
// source, the row; the constant is a floor that is only reached when the row
// cannot be read, and it can never be set to something that disagrees with a live
// deployment because no deployment sets it.

// modelCallTimeout bounds the ask. Short for the same reason a price's is: the
// floor is a real model that was serving a moment ago, and answering with it beats
// making a caller wait on a lookup.
var modelCallTimeout = 2 * time.Second

// The roles a surface asks for. These are the KEYS in the `ai` product document,
// so an operator sees the same words the code does.
const (
	RoleDefault  = "defaultModel"
	RoleChat     = "chatModel"
	RoleFallback = "fallbackModel"
)

// Model resolves the model for a role, falling back to floor.
//
// floor is the caller's compiled constant — DefaultModel, ChatModel or
// FallbackModel. Every failure returns it: an unreachable authority, an
// unconfigured platform, a document that does not parse, a key that is absent or
// blank. A surface therefore always names a model, and the worst case is the one
// this binary shipped with.
//
// The answer is normalised through ZenModel, so a row naming an upstream family
// cannot publish the mapping this package exists to keep private — an operator
// typing one at admin.hanzo.ai gets our name for it, not theirs.
func Model(ctx context.Context, role, floor string) string {
	ctx, cancel := context.WithTimeout(ctx, modelCallTimeout)
	defer cancel()

	out, err := settings.SettingsFleet(ctx, &client.Product{Product: "ai"})
	switch {
	case err != nil:
		// Warn, not error: this is a stale ROUTE rather than a failed request, and
		// it is per turn, so an outage must not write a line per completion at
		// error level.
		luxlog.New("cloud").New("subsystem", "model").Warn(
			"model routing unreadable, using the compiled floor",
			"role", role, "floor", floor, "err", err)
		return floor
	case out == nil || strings.TrimSpace(out.Config) == "":
		// Nothing configured. The ordinary state of a deployment that has not
		// overridden its routing, so it is not worth a line.
		return floor
	}

	var doc map[string]any
	if err := json.Unmarshal([]byte(out.Config), &doc); err != nil {
		luxlog.New("cloud").New("subsystem", "model").Warn(
			"model routing document does not parse, using the compiled floor",
			"role", role, "floor", floor, "err", err)
		return floor
	}
	name, _ := doc[role].(string)
	if name = strings.TrimSpace(name); name == "" {
		return floor
	}
	return ZenModel(name)
}

// Route is the ordered list of models an unnamed request runs on: the configured
// default first, then the free tier of the same family.
//
// The free hop is what makes an unnamed request always answerable. A paid tier
// can be unavailable on a deployment that does not carry it, or unaffordable for
// an org that has spent its balance, and neither is a reason to answer a customer
// with an error when a model we serve to everyone is sitting right there.
//
// It is the SAME family on purpose. Falling from enso to zen-free would answer as
// a different product; falling to enso-free answers as a cheaper enso, which is
// what the family means. A deployment whose default IS a free tier gets a one-hop
// route rather than the same name twice.
func Route(ctx context.Context) []string {
	primary := Model(ctx, RoleDefault, DefaultModel)
	free := FreeModel(primary)
	if free == "" || free == primary {
		return []string{primary}
	}
	return []string{primary, free}
}

// FreeModel is the free tier of a model's own family, or "" when it has none.
//
// The families are ours, so this is a fact about our own catalog rather than a
// guess: enso and zen each publish a free tier, and `free` is the generic one for
// anything else we serve. An upstream name has no family here and gets "" — its
// price is upstream cost and there is no free tier of somebody else's model.
func FreeModel(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	switch {
	case name == "":
		return ""
	case strings.HasSuffix(name, "-free"), name == "free":
		return "" // already the floor of its family
	case strings.HasPrefix(name, "enso"):
		return "enso-free"
	case strings.HasPrefix(name, "zen"):
		return "zen-free"
	case UpstreamModel(name):
		return "" // not ours; no family, no free tier
	default:
		return "free"
	}
}
