// Copyright © 2026 Hanzo AI. MIT License.

package git

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A link's host is asked about before it is dialled (admit), and the dial is
// judged again where it happens: a name that resolved public for the question can
// resolve to this host for the connection. The client every request to a host
// goes through therefore reaches no address that is not public.
func TestUpstreamDialsOnlyPublicAddresses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("reached"))
	}))
	defer srv.Close()

	resp, err := upstream(5 * time.Second).Get(srv.URL + "/info/refs?service=git-upload-pack")
	if err == nil {
		resp.Body.Close()
		t.Fatalf("the upstream client dialled a loopback address (%s): %d", srv.URL, resp.StatusCode)
	}
	if !strings.Contains(err.Error(), "not a public address") {
		t.Fatalf("refused for the wrong reason: %v", err)
	}
}
