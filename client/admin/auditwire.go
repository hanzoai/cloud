package admin

import (
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/audit"
)

// PlaneRecord is a trail record as it crosses the plane: to the trail on an append,
// and back from it on a read.
func PlaneRecord(r audit.Record) *client.AuditRecord {
	return &client.AuditRecord{
		Seq:        r.Seq,
		Time:       planeTime(r.Time),
		Org:        r.Actor.Org,
		Sub:        r.Actor.Sub,
		Email:      r.Actor.Email,
		Home:       r.Actor.Home,
		Action:     r.Action,
		Resource:   r.Resource.Type,
		ResourceID: r.Resource.ID,
		AuthMethod: r.Auth.Method,
		IsAdmin:    r.Auth.IsAdmin,
		Result:     r.Outcome.Result,
		Status:     r.Outcome.Status,
		Reason:     r.Outcome.Reason,
		SourceIP:   r.SourceIP,
		UserAgent:  r.UserAgent,
		RequestID:  r.RequestID,
		Method:     r.Method,
		Path:       r.Path,
		Before:     r.Before,
		After:      r.After,
		PrevHash:   r.PrevHash,
		Hash:       r.Hash,
		ActSub:     r.Actor.Act.Sub,
		ActOwner:   r.Actor.Act.Owner,
		ActName:    r.Actor.Act.Name,
		Agent:      r.Actor.Agent,
	}
}

// TrailRecord is a record off the plane, as the trail keeps it. Every string is
// copied out of the frame it arrived in: ZAP decodes text zero-copy, and the trail
// hands a sealed record to its mirror after the call has returned. An empty or
// unreadable time is the zero time, which Append stamps as now.
func TrailRecord(in *client.AuditRecord) audit.Record {
	at, _ := time.Parse(time.RFC3339Nano, in.Time)
	return audit.Record{
		Seq:  in.Seq,
		Time: at,
		Actor: audit.Actor{
			Org: strings.Clone(in.Org), Sub: strings.Clone(in.Sub),
			Email: strings.Clone(in.Email), Home: strings.Clone(in.Home),
			Act:   audit.Act{Sub: strings.Clone(in.ActSub), Owner: strings.Clone(in.ActOwner), Name: strings.Clone(in.ActName)},
			Agent: strings.Clone(in.Agent),
		},
		Action:    strings.Clone(in.Action),
		Resource:  audit.Resource{Type: strings.Clone(in.Resource), ID: strings.Clone(in.ResourceID)},
		Auth:      audit.AuthContext{Method: strings.Clone(in.AuthMethod), IsAdmin: in.IsAdmin},
		Outcome:   audit.Outcome{Result: strings.Clone(in.Result), Status: in.Status, Reason: strings.Clone(in.Reason)},
		SourceIP:  strings.Clone(in.SourceIP),
		UserAgent: strings.Clone(in.UserAgent),
		RequestID: strings.Clone(in.RequestID),
		Method:    strings.Clone(in.Method),
		Path:      strings.Clone(in.Path),
		Before:    in.Before,
		After:     in.After,
		PrevHash:  strings.Clone(in.PrevHash),
		Hash:      strings.Clone(in.Hash),
	}
}

// PlaneFilter is a read of the trail as it crosses the plane. It carries no org:
// the read's tenant is its caller.
func PlaneFilter(f audit.Filter) *client.AuditQueryIn {
	return &client.AuditQueryIn{
		Sub: f.Sub, Home: f.Home, Impersonated: f.Impersonated,
		Action: f.Action, Actions: f.Actions,
		Resource: f.Resource, ResourceID: f.ResourceID, Result: f.Result,
		Since: planeTime(f.Since), Until: planeTime(f.Until),
		Limit: f.Limit, Offset: f.Offset, Q: f.Q, Before: f.Before,
	}
}

// TrailFilter is a read off the plane for org, the tenant its caller acts for.
func TrailFilter(org string, in *client.AuditQueryIn) audit.Filter {
	since, _ := time.Parse(time.RFC3339Nano, in.Since)
	until, _ := time.Parse(time.RFC3339Nano, in.Until)
	actions := make([]string, 0, len(in.Actions))
	for _, a := range in.Actions {
		actions = append(actions, strings.Clone(a))
	}
	return audit.Filter{
		Org: org, Sub: strings.Clone(in.Sub), Home: strings.Clone(in.Home), Impersonated: in.Impersonated,
		Action: strings.Clone(in.Action), Actions: actions,
		Resource: strings.Clone(in.Resource), ResourceID: strings.Clone(in.ResourceID), Result: strings.Clone(in.Result),
		Since: since, Until: until,
		Limit: in.Limit, Offset: in.Offset, Q: strings.Clone(in.Q), Before: in.Before,
	}
}

// planeTime is a time on the plane: RFC3339Nano in UTC, or empty for the zero time.
func planeTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
