package triage

import (
	"context"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// THE SETTINGS ARE A SUPERADMIN'S, AND A SWITCH THAT IS ON NAMES WHO ACTS.
func TestOnlyASuperAdminSetsTriage(t *testing.T) {
	each(t, func(t *testing.T) {
		l := newLoop(t, Config{})
		o := ops{&cloud.Service[state]{State: state{store: l.store, log: l.log, now: l.state.now}}}
		person := zip.WithCaller(context.Background(), zip.Caller{Org: "hanzo", User: "u1", OrgAdmin: true})
		if _, err := o.setConfig(person, &Config{}); err == nil {
			t.Fatal("an org admin set the platform's loop")
		}
		if _, err := o.config(person, nil); err == nil {
			t.Fatal("an org admin read it")
		}
		super := zip.WithCaller(context.Background(), zip.Caller{Org: "admin", User: "z", Admin: true, Owner: "admin"})
		for name, bad := range map[string]Config{
			"no actor":          {Enabled: true, Label: "auto"},
			"no label":          {Enabled: true, Actor: "a"},
			"unknown class":     {Threshold: map[string]Limit{"weather": {Count: 1}}},
			"a negative figure": {Runs: Runs{PerDay: -1}},
			"a repository":      {Repos: []Repo{{Org: "hanzo", Repo: "cloud"}}},
		} {
			if _, err := o.setConfig(super, &bad); err == nil || strings.TrimSpace(err.Error()) == "" {
				t.Errorf("%s was saved", name)
			}
		}
		good := config(true, true)
		if _, err := o.setConfig(super, &good); err != nil {
			t.Fatal(err)
		}
		got, _ := o.config(super, nil)
		if !got.Enabled || got.Repos[0].Repo != "hanzo-inc/cloud" {
			t.Fatalf("saved %+v", got)
		}
	})
}

// TURNING THE KILL SWITCH ON IS ONE WRITE AND STOPS WHAT IS RUNNING.
func TestTheSwitchEndsRunsThroughTheEditor(t *testing.T) {
	each(t, func(t *testing.T) {
		l := newLoop(t, config(true, false))
		o := ops{&cloud.Service[state]{State: state{store: l.store, log: l.log, now: l.state.now}}}
		l.see(t, "efefefefefefefef", 3)
		off := config(true, false)
		off.Enabled = false
		super := zip.WithCaller(context.Background(), zip.Caller{Org: "admin", User: "z", Admin: true, Owner: "admin"})
		if _, err := o.setConfig(super, &off); err != nil {
			t.Fatal(err)
		}
		if len(l.w.stops) != 1 {
			t.Fatalf("the live run was not stopped: %+v", l.w.stops)
		}
	})
}
