// Copyright © 2026 Hanzo AI. MIT License.

package cron

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/fault"
	luxlog "github.com/luxfi/log"
)

// A FAILED RUN IS A FAULT, AND A SUCCESSFUL ONE OR ONE THIS PROCESS DID NOT HOLD IS
// NOT. Every failure is stated under one key per entry and root cause.
//
// MUTATION: drop the ReportFault line in record and the failure is not stated.
func TestAFailedRunIsStatedAsAFault(t *testing.T) {
	var got []fault.Fault
	cloud.SetHooks(cloud.Hooks{ReportFault: func(f fault.Fault) { got = append(got, f) }})
	t.Cleanup(func() { cloud.SetHooks(cloud.Hooks{}) })
	setWiring(&fakeKube{}, luxlog.NewNoOpLogger(), quick)

	ctx := context.Background()
	record(ctx, "job", "backup", "", nil)
	record(ctx, "job", "backup", "", coord.ErrNotHeld)
	record(ctx, "job", "backup", "", errors.New("BackoffLimitExceeded"))
	if len(got) != 1 || got[0].Op != "cron:backup" || got[0].Class != fault.Cron {
		t.Fatalf("faults = %+v, want one cron fault for backup", got)
	}
}
