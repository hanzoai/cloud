package team

import (
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// supportRefused is every account RPC verb a support session (an IAM
// impersonation, cloud impersonation.go) may not call, and why. The RPC rides one
// address for every verb, so cloud's address table cannot say it: an invite
// writes an IAM membership with this service's own credential, where IAM never
// sees the session's bearer, and a workspace token outlives the session by twelve
// hours and carries no mark.
var supportRefused = map[string]string{
	"sendInvite":      "a support session cannot change memberships: the person does that signed in as themselves",
	"selectWorkspace": "a support session cannot open a workspace: its token would outlive the session",
}

// refusedToSupport is the refusal a support session gets for verb, and false for
// every other caller and every other verb.
func refusedToSupport(c *zip.Ctx, verb string) (Status, bool) {
	why, listed := supportRefused[verb]
	if _, imp := principal.Impersonated(c); !listed || !imp {
		return Status{}, false
	}
	return Status{Severity: "ERROR", Code: "account:status:Forbidden", Params: map[string]any{"message": why}}, true
}
