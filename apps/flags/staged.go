package flags

// staged.go — the flag of a capability that is not ga is a SuperAdmin's to set.
//
// A capability that is not ga (manifest.App.Stage, HIP-0139 §8) answers an org
// only when that org holds the flag whose key IS the capability's name
// (cloud.Stage, hold_rpc.go), and the flag is read from the org's own default
// store — the store PUT /v1/flag/defs/:key writes. So without this rule any
// signed-in member lets their own org into an alpha by writing one definition.
// Letting a customer in is the platform's decision, so a write or delete of a
// staged key takes a SuperAdmin (owner == "admin", cloud.Super) — never an admin
// of the org, whose authority is the org's own — and is on the platform's audit
// trail before it lands. A change the trail cannot record is refused.
//
// A staged key is a manifest row's name, or a SURFACE: a screen the console shows
// an org that holds the flag of its name, with no address of its own for
// cloud.Stage to refuse. Every other key is the org's owners' and admins' to set
// (routes.go authoring).

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/zap-proto/zip"
)

// surfaces are the staged capabilities that are screens, by key, with their
// stage. research is a manifest row (manifest/apps.go), so it is not here.
var surfaces = map[string]string{
	// machines is the console's Machines screen: the caller's linked machines
	// and their network, over addresses that are ga on their own.
	"machines": manifest.Alpha,
}

// Stage is the stage a flag key gates — the manifest row of that name, else the
// surface — or "" for a key that gates no staged capability.
func Stage(key string) string {
	if s := manifest.StageOf(key); s != "" {
		return s
	}
	return surfaces[key]
}

// staging refuses a write of a staged key by anyone but a SuperAdmin, and puts
// the SuperAdmin's write on the audit trail before it is made. A key that gates
// nothing passes untouched.
func (o ops) staging(ctx context.Context, cl caller, action, key string, body json.RawMessage) error {
	stage := Stage(key)
	if stage == "" {
		return nil
	}
	if !cloud.Super.Admits(cloud.AuthorityIn(ctx)) {
		return zip.ErrForbidden("flag " + key + " lets an org into a capability that is not ga, so it is a SuperAdmin's to set")
	}
	who := cloud.Who(ctx)
	o.s.Log.Info("flags: a staged capability's flag changes",
		"org", cl.org, "project", cl.project, "key", key, "stage", stage, "action", action, "by", who.User)
	if o.s.State.audit == nil {
		return nil
	}
	after, _ := json.Marshal(map[string]any{"project": cl.project, "stage": stage, "definition": body})
	_, err := o.s.State.audit.Append(ctx, audit.Record{
		Actor:     audit.Actor{Org: cl.org, Sub: who.User, Email: who.Email},
		Action:    action,
		Resource:  audit.Resource{Type: "flag", ID: key},
		Auth:      audit.AuthContext{Method: "attested", IsAdmin: true},
		Outcome:   audit.Outcome{Result: "success", Status: http.StatusOK, Reason: "lets an org into a " + stage + " capability"},
		SourceIP:  who.IP,
		RequestID: who.RequestID,
		After:     audit.Redact(after),
	})
	if err != nil {
		o.s.Log.Error("flags: a staged capability's flag could not be audited", "org", cl.org, "key", key, "err", err)
		return zip.Errorf(http.StatusServiceUnavailable, "flags: this change could not be audited")
	}
	return nil
}
