package tools

// govern.go — who may change what the org's agent runs carry.
//
// A coding run is handed its org's kit (kit.go): the skills activated in the org,
// and the MCP servers it registered with the tools of each it activated. Every
// colleague's run loads that kit, and the harness is told to call those tools
// without asking anyone (claude's --allowedTools, dev's bypassed approvals). So a
// change to the kit is a change to every colleague's run, and it is an admin's to
// make: registering, revising or deregistering an MCP server, writing or removing
// the org's own skill, and switching a skill or a server's tool on or off. A
// member is refused all of them. The rest of the plane — a member's connector
// actions, functions and agents — is the member's to switch, as it always was.
//
// Each such change is on the trail BEFORE it is made, and a change the trail
// cannot record is refused rather than made unrecorded — the rule an admin's act
// on another member's sandbox follows (apps/sandbox crossed).
//
// Which changes were made by an admin is also KEPT, on the row itself (admitted,
// on a server, a skill and an activation), and a run carries only admitted rows.
// That is what makes the gate hold for rows it never saw: a server a member
// registered, a skill a member wrote and a tool a member switched on before the
// gate existed, a skill a push wrote, and a name a member switched on before an
// admin registered the server that now answers to it.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/zap-proto/zip"
)

// administers reports that the caller is an admin of the org the call acts in,
// or a SuperAdmin: cloud.Admin, the one rule. A typed op reads the caller off the
// request behind it (cloud.AuthorityIn). A plane op has none, and reads the
// caller its sibling forwarded (cloud.Who) — the same three facts, which a call
// stating only an org leaves empty, so a background call administers nothing.
func administers(ctx context.Context) bool {
	if a := cloud.AuthorityIn(ctx); a.Validated {
		return cloud.Admin.Admits(a)
	}
	who := cloud.Who(ctx)
	return cloud.Admin.Admits(cloud.Authority{
		Validated: strings.TrimSpace(who.User) != "", Super: who.Admin, OrgAdmin: who.OrgAdmin,
	})
}

// errAdmin is the refusal a member gets on every change to what the org's agent
// runs carry.
func errAdmin(what string) error {
	return zip.ErrForbidden("tools: " + what + " changes what every agent run in the org carries, so it takes an admin of the org")
}

// governed reports whether switching name changes what the org's agent runs
// carry: a skill, or a tool of an MCP server the org registered (the server's id
// is the name's first segment).
func governed(ctx context.Context, servers *MCPServerStore, org, name string) (bool, error) {
	if strings.HasPrefix(name, "skill_") {
		return true, nil
	}
	id, _, ok := strings.Cut(name, "_")
	if !ok || servers == nil {
		return false, nil
	}
	switch _, err := servers.Get(ctx, org, id); {
	case err == nil:
		return true, nil
	case errors.Is(err, ErrUnknownTool):
		return false, nil
	default:
		return false, zip.Errorf(http.StatusInternalServerError, "tools: read servers: %v", err)
	}
}

// mayToggle refuses a member who would switch a governed name, before anything
// is switched.
func mayToggle(ctx context.Context, servers *MCPServerStore, org string, names ...string) error {
	if administers(ctx) {
		return nil
	}
	for _, name := range names {
		on, err := governed(ctx, servers, org, name)
		if err != nil {
			return err
		}
		if on {
			return errAdmin("switching " + name)
		}
	}
	return nil
}

// record writes one change to what the org's agent runs carry to the trail,
// BEFORE it is made. The caller is the one the call carries (cloud.Who): a
// verified token off a request, or the caller a sibling forwarded on the plane.
// A deployment with no trail logs it, as every such deployment does.
func record(s *cloud.Service[state], ctx context.Context, action, org, resource string, detail any) error {
	who := cloud.Who(ctx)
	s.Log.Info("tools: a change to what the org's agent runs carry",
		"org", org, "action", action, "resource", resource, "by", who.User)
	if s.State.audit == nil {
		return nil
	}
	after, _ := json.Marshal(detail)
	_, err := s.State.audit.Append(ctx, audit.Record{
		Actor:     audit.Actor{Org: org, Sub: who.User, Email: who.Email},
		Action:    action,
		Resource:  audit.Resource{Type: "tools", ID: resource},
		Auth:      audit.AuthContext{Method: "attested", IsAdmin: who.Admin},
		Outcome:   audit.Outcome{Result: "success", Status: http.StatusOK, Reason: "changes what the org's agent runs carry"},
		SourceIP:  who.IP,
		RequestID: who.RequestID,
		After:     audit.Redact(after),
	})
	if err != nil {
		s.Log.Error("tools: a change to what the org's agent runs carry could not be audited",
			"org", org, "action", action, "resource", resource, "err", err)
		return zip.Errorf(http.StatusServiceUnavailable, "tools: this change could not be audited")
	}
	return nil
}
