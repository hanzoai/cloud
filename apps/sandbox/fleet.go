// Copyright © 2026 Hanzo AI. MIT License.

package sandbox

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hanzoai/cloud/client"
	settings "github.com/hanzoai/cloud/client/settings"
)

// preference is the boundary THIS DEPLOYMENT would rather run, asked of the
// platform's own settings at the moment a sandbox is created.
//
// It was an environment variable, which made the fleet's isolation a value nobody
// could read and a rollout to change: 204 pods restart to answer a question an
// operator should be able to answer in a text field. It is now the same (org,
// product) row every product's config already uses, at admin.hanzo.ai, and the
// next lease reads it.
//
// Asked per lease, not cached. A cache would make the setting mean "eventually",
// and creating a sandbox already talks to the apiserver several times — one call
// over a unix socket is not the cost here. It also keeps the honest property that
// what an operator sees in the field is what the next sandbox gets.
//
// A PREFERENCE IS NOT A GRANT. What comes back still goes through runtimeFor's
// table like anything else: a name that cannot serve this sandbox's two facts
// falls to the boundary that can, and an unknown name fits nothing at all. So the
// worst an operator can do by mistyping here is get gvisor.
//
// Unreachable answers the same as unset — empty, the node's own default — because
// a sandbox that refused to start when the settings app was down would trade a
// configurable boundary for an outage, and the boundary it would be protecting is
// the one it already had before anyone configured it.
func (r *runtime) preference(ctx context.Context) string {
	out, err := settings.SettingsFleet(ctx, &client.Product{Product: product})
	if err != nil || out == nil {
		return ""
	}
	var doc struct {
		Runtime string `json:"runtime"`
	}
	if json.Unmarshal([]byte(out.Config), &doc) != nil {
		return ""
	}
	return strings.TrimSpace(doc.Runtime)
}

// product is this app's key in the settings store — the name an operator picks in
// the console, spelled once here so the field and the reader cannot drift.
const product = "sandbox"
