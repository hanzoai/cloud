package integrations

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
)

// TestPlaneSlackSend_OrgFromCaller pins the tenancy of the plane op: the org is
// read from the CALLER's context, never the argument, so a caller cannot post as
// another tenant. With no org on the call it refuses before touching the token
// store; SlackSendIn carries no org field to forge.
func TestPlaneSlackSend_OrgFromCaller(t *testing.T) {
	if _, err := planeSlackSend(context.Background(), &plane.SlackSendIn{Channel: "#ops", Text: "x"}); err == nil {
		t.Fatal("no org on the call must refuse, not send")
	}
	// The In type must not carry an org — the only safe source is the context.
	var in plane.SlackSendIn
	_ = in.Channel
	_ = in.Thread
	_ = in.Text
	// (compile-time: any `in.Org` here would fail to build, which is the point)

	// With an org stamped on the context, it proceeds past the gate into the
	// egress (which then fails only for lack of a mounted token store in a unit
	// context — a DIFFERENT error than the forbidden gate).
	ctx := cloud.For(context.Background(), "acme")
	if _, err := planeSlackSend(ctx, &plane.SlackSendIn{Channel: "#ops", Text: "x"}); err != nil {
		if err.Error() == "slack send: no org on the call" {
			t.Fatalf("org on the context must pass the gate; got the no-org refusal: %v", err)
		}
	}
}
