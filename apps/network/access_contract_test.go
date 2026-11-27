package network

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/authztest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/spec"
)

// TestAuthorizationContract holds every route network serves, the two anonymous
// ones and the invite admin among them, to the shared authorization contract
// (internal/authztest): forged SuperAdmin headers and another org's name buy
// nothing, an anonymous caller is let in only where the route declared itself open,
// and /v1/admin is a SuperAdmin's.
//
// It runs as a deployment with the controller configured, which is production's
// posture: an unconfigured one answers its two list reads with an empty 200 to
// anyone, by design (network.go), and the contract would rightly call that a way
// in. The controller and IAM it is pointed at refuse every connection, so nothing
// leaves the test.
func TestAuthorizationContract(t *testing.T) {
	t.Setenv("IAM_CLIENT_ID", "contract-test")
	t.Setenv("IAM_CLIENT_SECRET", "contract-test")
	t.Setenv("ZT_CONTROLLER_URL", "http://127.0.0.1:1")
	t.Setenv("IAM_URL", "http://127.0.0.1:1")
	authztest.Run(t, authztest.Contract{
		Plugin: cloud.Plugin{Name: "network", Price: cloud.Free, Use: Use},
	})
}

// TestTheAnonymousOpsAreOpenInTheDocumentAndTheAdminOpsAreNot: the document asks no
// credential of verify or of the waitlist, says 202 for the waitlist, carries
// every body and answer as a named schema and tells a reader what verify does
// and does not say; the three admin ops ask the default credential and are
// published as the operator's own.
func TestTheAnonymousOpsAreOpenInTheDocumentAndTheAdminOpsAreNot(t *testing.T) {
	app := mountApp(t, &fakeZT{})
	doc, err := spec.Of(app, spec.Info{Title: "network", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	for _, c := range []struct {
		path, id, in, out, status string
		says                      []string
	}{
		{"/v1/network/invite/verify", "post_network_invite_verify", "InviteCheckIn", "InviteVerdict", "200",
			[]string{"never says why", "does not consume", "until redemption exists", "invite_required"}},
		{"/v1/network/waitlist", "post_network_waitlist", "WaitlistIn", "WaitlistEntry", "202",
			[]string{"pending", "same answer", "invite_required"}},
	} {
		op := doc.Paths[c.path]["post"]
		if op == nil {
			t.Fatalf("%s is not in the document", c.path)
		}
		if op.OperationID != c.id || op.Description == "" {
			t.Errorf("%s is %q with description %q", c.path, op.OperationID, op.Description)
		}
		if op.Security == nil || len(*op.Security) != 0 {
			t.Errorf("%s asks a credential: %v", c.path, op.Security)
		}
		for _, say := range c.says {
			if !strings.Contains(op.Description, say) {
				t.Errorf("%s does not tell its reader %q: %s", c.path, say, op.Description)
			}
		}
		raw, _ := json.Marshal(op)
		for _, want := range []string{"#/components/schemas/" + c.out, c.in, `"` + c.status + `"`} {
			if !strings.Contains(string(raw), want) {
				t.Errorf("%s: the document lacks %s: %s", c.path, want, raw)
			}
		}
	}
	for _, c := range []struct{ method, path, id, status string }{
		{"post", "/v1/admin/network/invites", "post_admin_network_invites", "201"},
		{"get", "/v1/admin/network/invites", "get_admin_network_invites", "200"},
		{"delete", "/v1/admin/network/invites/{id}", "delete_admin_network_invites_id", "204"},
	} {
		op := doc.Paths[c.path][c.method]
		if op == nil {
			t.Fatalf("%s %s is not in the document", c.method, c.path)
		}
		if op.Security != nil {
			t.Errorf("%s %s declares its own credential %v; it inherits the default", c.method, c.path, op.Security)
		}
		if op.Description == "" || !strings.Contains(op.Description, "SuperAdmin") {
			t.Errorf("%s %s does not say it is a SuperAdmin's: %q", c.method, c.path, op.Description)
		}
		if raw, _ := json.Marshal(op); !strings.Contains(string(raw), `"`+c.status+`"`) {
			t.Errorf("%s %s does not answer %s: %s", c.method, c.path, c.status, raw)
		}
	}
	if issue, _ := json.Marshal(doc.Paths["/v1/admin/network/invites"]["post"]); !strings.Contains(string(issue), "once") {
		t.Errorf("issuing does not tell its reader the codes are shown once: %s", issue)
	}
}
