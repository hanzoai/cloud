package claw

import (
	"strings"
	"testing"
)

// approveMethods is every method this family serves, and what each costs. A
// client reads the first list out of the handshake to decide which controls to
// offer; the cost decides who may use them.
var approveMethods = map[string]Scope{
	"exec.approval.list":     Approvals,
	"plugin.approval.list":   Approvals,
	"openclaw.approval.list": Approvals,
}

// approveGone is every method a reviewer's pane would reach that this gateway
// does not serve. Advertising one puts a control on the page that fails when it
// is pressed, so each must stay off the handshake as well as off the registry.
var approveGone = []string{
	"approval.resolve",
	"exec.approval.resolve",
	"plugin.approval.resolve",
	"exec.approval.grants.revoke",
}

// What each method costs is the protocol's own price: operator.approvals, the
// capability that exists so a reviewer need not also be an administrator.
func TestApprovalListsCostApprovals(t *testing.T) {
	surface.mu.RLock()
	defer surface.mu.RUnlock()
	for method, need := range approveMethods {
		m, ok := surface.methods[method]
		if !ok {
			t.Errorf("%s is not registered", method)
			continue
		}
		if m.need != need {
			t.Errorf("%s costs %s, want %s", method, m.need, need)
		}
	}
	for _, method := range approveGone {
		if _, ok := surface.methods[method]; ok {
			t.Errorf("%s is registered, and nothing here can answer it", method)
		}
	}
}

// The handshake lists what this surface answers, and a client offers only the
// controls it finds there. A name on that list that can only refuse is worse
// than a name that is absent: the first renders a button that errors, the
// second renders nothing.
func TestApprovalSurfaceIsAdvertisedExactly(t *testing.T) {
	app := mount(t)

	_, frame := ask(t, app, who{org: "acme", admin: true}, "1:a", "connect", `{"minProtocol":4,"maxProtocol":4}`)
	features, _ := payload(t, frame)["features"].(map[string]any)
	names, ok := features["methods"].([]any)
	if !ok {
		t.Fatalf("the handshake advertises no method list: %v", features)
	}
	listed := map[string]bool{}
	for _, m := range names {
		listed[m.(string)] = true
	}
	for method := range approveMethods {
		if !listed[method] {
			t.Errorf("%s is served but not advertised, so a client will not call it", method)
		}
	}
	for _, method := range approveGone {
		if listed[method] {
			t.Errorf("%s is advertised and cannot be answered", method)
		}
	}
	// Nothing here raises an approval, so nothing here announces the events an
	// approval would raise.
	for _, e := range features["events"].([]any) {
		if strings.HasPrefix(e.(string), "openclaw.approval.") {
			t.Errorf("%v is announced, and nothing in this cloud emits it", e)
		}
	}
}

// The client asks the three lists at once and merges them into one queue. Each
// answers a bare array: a fulfilled answer that is not an array contributes
// nothing, so a wrapper object would empty the pane rather than fill it.
func TestApprovalQueueIsThreeBareArrays(t *testing.T) {
	app := mount(t)
	for method := range approveMethods {
		_, frame := ask(t, app, who{org: "acme"}, "1:a", method, `{}`)
		if frame["ok"] != true {
			t.Errorf("%s could not be read: %v", method, frame)
			continue
		}
		got, ok := frame["payload"]
		if !ok {
			t.Errorf("%s carries no payload; the client requires an array", method)
			continue
		}
		list, ok := got.([]any)
		if !ok {
			t.Errorf("%s answered %#v, want an array", method, got)
			continue
		}
		if len(list) != 0 {
			t.Errorf("nothing raises an approval here, yet %s holds %d: %v", method, len(list), list)
		}
	}
}

// The protocol declares no parameter schema for any of the three and the
// TypeScript that serves them reads none, so a caller that sends a field is a
// caller the protocol accepts. Closing them here would refuse it.
func TestApprovalListsIgnoreParameters(t *testing.T) {
	app := mount(t)
	for method := range approveMethods {
		for _, params := range []string{``, `{}`, `{"cursor":"x","limit":5,"nonsense":true}`} {
			_, frame := ask(t, app, who{org: "acme"}, "1:a", method, params)
			if frame["ok"] != true {
				t.Errorf("%s refused %s: %v", method, params, frame)
			}
		}
	}
}

// Reviewing is its own capability, so a caller who cannot review is refused
// even though every member of an org happens to hold it. The registration is
// what has to be right, and this is the one request that can see it.
func TestApprovalListsCanBeRefused(t *testing.T) {
	mount(t)
	for method := range approveMethods {
		one := &Call{Method: method, svc: mounted.Load(), me: caller{org: "acme", user: "u@acme", grant: Grant{Read, Write}}}
		if _, err := dispatch(one); err == nil {
			t.Errorf("%s answered a caller holding no operator.approvals", method)
		} else if f, ok := err.(*Fault); !ok || f.Code != codeForbidden {
			t.Errorf("%s refused with %v, want FORBIDDEN", method, err)
		}
	}
}
