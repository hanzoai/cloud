package sandbox

// activation.go — how long a session took to get its sandbox back, and which of
// three ways it went (sdk/activation), recorded apart from the model's first token.
//
// Every door to a running sandbox decides one of them:
//
//	warm       Lease or resume named a sandbox whose pod is alive: nothing started
//	relocated  its pod was gone under a running lease and a new one took its id
//	           (Lease, or fault when an exec found it gone)
//	cold       a pod was started for a session that had none: a parked sandbox
//	           resumed (unpark), or a new lease
//
// Only the call that did the work records it. A resume or a restart that loses its
// compare-and-set to another call (Store.Shift, Store.Swap) started nothing, and
// is that call's activation, not a second one — the same rule that keeps the
// racing calls from starting a second pod.

import (
	"context"
	"errors"
	"net/http"

	"github.com/hanzoai/cloud/sdk/activation"
	"github.com/zap-proto/zip"
	"go.opentelemetry.io/otel/attribute"
)

// surface is this plane's name on an activation (activation.AttrSurface).
const surface = "sandbox"

// activated records the activation clock timed, for the sandbox m in org.
//
// A refusal is not an activation: a lease declined for its wallet, its quota, a
// conflict or a missing id never started or found anything, so it is left out
// rather than counted as a fast one. An activation that was attempted and failed
// (the pod did not start) is recorded as a failure.
func activated(ctx context.Context, clock *activation.Clock, org string, m Sandbox, err error) {
	if declined(err) {
		return
	}
	var attrs []attribute.KeyValue
	for k, v := range map[string]string{
		"hanzo.sandbox.id":      m.ID,
		"hanzo.sandbox.class":   m.Class,
		"hanzo.sandbox.runtime": m.Runtime,
	} {
		if v != "" {
			attrs = append(attrs, attribute.String(k, v))
		}
	}
	clock.End(ctx, surface, org, err, attrs...)
}

// declined reports whether err is an answer to the caller (a 4xx) rather than a
// failure to make the session ready.
func declined(err error) bool {
	var he *zip.HTTPError
	return errors.As(err, &he) && he.Status < http.StatusInternalServerError
}
