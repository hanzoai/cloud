package admin

import (
	"context"
	"sync"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/plane"
)

// capsPeer serves the spend-cap ops as app "commerce" on this process's plane,
// recording the org each call was answered FOR.
//
// It replaces an httptest stub of commerce's HTTP door. That door belonged to a
// standalone commerce there is no longer any of — /v1/billing is billing's
// address and the caps are reached BY NAME — and a test that keeps stubbing it
// proves the reader can parse a shape nothing serves.
//
// It uses the REAL op ids and the REAL wire, and it re-enforces production's own
// tenancy rule rather than relaxing it: the org rides the CALLER, the alert
// inputs cannot name one, and an org-less call is refused here exactly as
// commerce refuses it. A fixture that admitted one would let a test pass through
// a door production closes.
type capsPeer struct {
	mu  sync.Mutex
	org string
	op  string
}

func newCapsPeer(t *testing.T) *capsPeer {
	t.Helper()
	p := &capsPeer{}
	t.Setenv("ZIP_RUNTIME_DIR", "")
	t.Setenv("CLOUD_RUN_DIR", planetest.Dir(t))
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)

	record := func(ctx context.Context, op string) error {
		org := cloud.Who(ctx).Org
		if org == "" {
			return zip.ErrForbidden(op + ": no org on the call")
		}
		p.mu.Lock()
		p.org, p.op = org, op
		p.mu.Unlock()
		return nil
	}

	zip.Post[plane.SubjectIn, plane.Alerts](cloud.Plane(), "/billing/alerts",
		func(ctx context.Context, _ *plane.SubjectIn) (*plane.Alerts, error) {
			if err := record(ctx, plane.BillingAlerts); err != nil {
				return nil, err
			}
			return &plane.Alerts{Rows: []plane.Alert{{ID: "a1", Threshold: 10000, Enforce: true}}}, nil
		}, zip.WithOperationID(plane.BillingAlerts))

	zip.Post[plane.AlertSpec, plane.Alert](cloud.Plane(), "/billing/alert/raise",
		func(ctx context.Context, in *plane.AlertSpec) (*plane.Alert, error) {
			if err := record(ctx, plane.BillingAlertRaise); err != nil {
				return nil, err
			}
			return &plane.Alert{ID: "a1", Threshold: in.Threshold}, nil
		}, zip.WithOperationID(plane.BillingAlertRaise))

	zip.Post[plane.AlertPatch, plane.Alert](cloud.Plane(), "/billing/alert/amend",
		func(ctx context.Context, _ *plane.AlertPatch) (*plane.Alert, error) {
			if err := record(ctx, plane.BillingAlertAmend); err != nil {
				return nil, err
			}
			return &plane.Alert{ID: "a1"}, nil
		}, zip.WithOperationID(plane.BillingAlertAmend))

	zip.Post[plane.AlertRef, plane.Dropped](cloud.Plane(), "/billing/alert/drop",
		func(ctx context.Context, _ *plane.AlertRef) (*plane.Dropped, error) {
			if err := record(ctx, plane.BillingAlertDrop); err != nil {
				return nil, err
			}
			return &plane.Dropped{OK: true}, nil
		}, zip.WithOperationID(plane.BillingAlertDrop))

	stop, err := cloud.ServePlane("commerce", luxlog.NewNoOpLogger())
	if err != nil {
		t.Fatalf("serve plane: %v", err)
	}
	t.Cleanup(func() { _ = stop() })
	return p
}

// seen is the org the last cap call was answered for, and which op it was.
func (p *capsPeer) seen() (org, op string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.org, p.op
}
