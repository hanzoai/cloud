// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package host

import (
	"errors"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/fault"
)

func attr(in *client.EventIn, name string) string {
	for _, a := range in.Attributes {
		if a.Name == name {
			return a.Value
		}
	}
	return ""
}

// A fault is stated under its kind's name with the key the response carried, the
// code and the place it was made of, and the build that answered.
func TestFaultEvent(t *testing.T) {
	f := fault.Request("POST", "/v1/event", 500, "", errors.New("boom"))
	f.Request = "req-1"
	in := faultEvent("event", f)
	if in.Name != "server_error" || in.Product != "cloud" || in.Error == nil {
		t.Fatalf("event = %+v", in)
	}
	if in.Error.Message != "boom" || in.Error.Path != "/v1/event" || in.Error.Type != "internal" || in.Error.Fingerprint != f.Fingerprint() {
		t.Fatalf("error = %+v", in.Error)
	}
	for name, want := range map[string]string{
		"service": "event", "fault.class": "server", "fault.code": "internal", "fault.op": "POST /v1/event", "fault.request": "req-1",
	} {
		if got := attr(in, name); got != want {
			t.Errorf("attribute %s = %q, want %q", name, got, want)
		}
	}
	if run := faultEvent("cloud", fault.Ended("dev", "exited 101")); run.Name != "run_error" || run.Error.Path != "" {
		t.Fatalf("a run is stated as a run with no route: %+v", run)
	}
}

// The window admits a bounded number of reports, and none before an org is named.
func TestFaultWindow(t *testing.T) {
	ReportFaultsAs("", "")
	if admitFault(time.Now()) {
		t.Fatal("no org named: nothing may be reported")
	}
	ReportFaultsAs("hanzo", "event")
	t.Cleanup(func() { ReportFaultsAs("", "") })
	now := time.Now()
	faults.mu.Lock()
	faults.start, faults.count = now, 0
	faults.mu.Unlock()
	for i := 0; i < faultsPerWindow; i++ {
		if !admitFault(now) {
			t.Fatalf("report %d refused inside the window", i)
		}
	}
	if admitFault(now) {
		t.Fatal("the window admitted one past its bound")
	}
	if !admitFault(now.Add(faultWindow)) {
		t.Fatal("a new window must admit again")
	}
}
