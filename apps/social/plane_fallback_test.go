package social

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/sdk"

	"github.com/zap-proto/zip"
)

// The post UI stands in only where the provider holds no app for the network. A
// refusal is the caller's own, and a second door must not reopen it.
func TestARefusedConnectIsNotOfferedThePostUI(t *testing.T) {
	for name, c := range map[string]struct {
		err  error
		want bool
	}{
		"nothing to authorize and no error": {nil, true},
		"not configured on this deployment": {zip.Errorf(http.StatusServiceUnavailable, "x integration is not configured on this deployment"), true},
		"a member refused":                  {zip.ErrForbidden("connecting the x connector requires org admin"), false},
		"an unknown provider":               {zip.ErrNotFound("unknown provider"), false},
		"no provider app deployed here":     {fmt.Errorf("%w: provider (no socket, no router)", cloud.ErrNoPeer), true},
		"a provider that failed to answer":  {errors.New("wake provider: the router answered nothing"), false},
	} {
		if got := unconfigured(c.err); got != c.want {
			t.Errorf("%s: the post UI is offered = %v, want %v", name, got, c.want)
		}
	}
}
