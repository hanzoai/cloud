package triage

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

type ops struct{ s *cloud.Service[state] }

// admin is the gate of every op here: the settings and the ledger are the platform's,
// so a SuperAdmin's alone.
func admin(ctx context.Context) error {
	if !principal.SuperFrom(ctx) {
		return zip.ErrForbidden("triage is the platform's: a SuperAdmin reads and sets it")
	}
	return nil
}

// config is the settings as saved.
func (o ops) config(ctx context.Context, _ *cloud.Unit) (*Config, error) {
	if err := admin(ctx); err != nil {
		return nil, err
	}
	c, err := o.s.State.store.Config(ctx)
	return &c, err
}

// setConfig saves the settings. The change is on the audit trail before it is made,
// and turning the kill switch on ends every live run.
func (o ops) setConfig(ctx context.Context, in *Config) (*Config, error) {
	if err := admin(ctx); err != nil {
		return nil, err
	}
	if err := in.valid(); err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	st := &o.s.State
	was, err := st.store.Config(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "triage: %v", err)
	}
	before, _ := json.Marshal(was)
	after, _ := json.Marshal(in)
	who := cloud.Who(ctx)
	if st.audit != nil {
		if _, err := st.audit.Append(ctx, audit.Record{
			Actor: audit.Actor{Org: who.Org, Sub: who.User}, Action: "triage.config",
			Resource: audit.Resource{Type: "triage", ID: "config"}, Auth: audit.AuthContext{Method: "jwt", IsAdmin: true},
			Outcome: audit.Outcome{Result: "success", Status: http.StatusOK}, Before: audit.Redact(before), After: audit.Redact(after),
		}); err != nil {
			return nil, zip.Errorf(http.StatusServiceUnavailable, "triage: the audit trail did not take the change, so it was not made")
		}
	}
	if err := st.store.SetConfig(ctx, *in); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "triage: %v", err)
	}
	if was.Enabled && !in.Enabled {
		if err := st.stopAll(ctx, was); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "triage: stopping the live runs: %v", err)
		}
	}
	return in, nil
}

// valid is what a setting must be to be kept: a switch that is on names an actor and
// a label, a repository is owner/name, and no figure is negative.
func (c Config) valid() error {
	bad := func(msg string) error { return zip.ErrBadRequest(msg) }
	if c.Enabled && (strings.TrimSpace(c.Actor) == "" || strings.TrimSpace(c.Label) == "") {
		return bad("an enabled triage names the actor it acts as and the label that hands an issue to a run")
	}
	for class, l := range c.Threshold {
		if !slices.Contains([]string{"server", "dependency", "run", "cron", "client"}, class) || l.Count < 0 || l.Minutes < 0 {
			return bad("threshold " + class + ": a known class, and no negative figure")
		}
	}
	if c.Runs.Seconds < 0 || c.Runs.PerDay < 0 || c.Runs.Attempts < 0 {
		return bad("runs: no negative figure")
	}
	for _, r := range c.Repos {
		if owner, name, ok := strings.Cut(r.Repo, "/"); !ok || owner == "" || name == "" || strings.Contains(name, "/") || r.Org == "" || r.Concurrent < 0 {
			return bad("repos: each names its org and owner/name, with no negative concurrency")
		}
	}
	return nil
}

// faults is the ledger: every fingerprint counted, and the state of its issue.
func (o ops) faults(ctx context.Context, _ *cloud.Unit) (*faultList, error) {
	if err := admin(ctx); err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.Faults(ctx)
	return &faultList{Faults: rows}, err
}

type faultList struct {
	Faults []Fault `json:"faults"`
}

// work is every issue handed to a run, and where the run stands.
func (o ops) work(ctx context.Context, _ *cloud.Unit) (*workList, error) {
	if err := admin(ctx); err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.All(ctx)
	return &workList{Work: rows}, err
}

type workList struct {
	Work []Work `json:"work"`
}
