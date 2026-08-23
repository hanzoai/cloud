package zzmeasure

import (
	"reflect"
	"testing"

	"github.com/hanzoai/cloud/plane"
)

// The rule LLM.md states: "no input type on the plane may carry an Org or Owner
// field". plane_test.go:132 checks a HAND-WRITTEN list of 23 types. These are the
// plane input types that are NOT on that list.
func TestOrgNamingInputsNotOnTheList(t *testing.T) {
	inputs := []any{
		plane.RunOnBehalfIn{}, plane.ChannelsIngestIn{}, plane.ChatIdentityIn{},
		plane.ChatSendIn{}, plane.RouteRunIn{}, plane.SessionOpenIn{},
		plane.SessionCloseIn{}, plane.SessionEventIn{}, plane.SiteIn{},
		plane.TargetGateIn{}, plane.TargetRefIn{}, plane.Send{}, plane.Recipient{},
	}
	for _, in := range inputs {
		typ := reflect.TypeOf(in)
		for i := 0; i < typ.NumField(); i++ {
			if n := typ.Field(i).Name; n == "Org" || n == "Owner" {
				t.Logf("VIOLATION %s.%s  json=%q", typ.Name(), n, typ.Field(i).Tag.Get("json"))
			}
		}
	}
}
