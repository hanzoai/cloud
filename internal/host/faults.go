// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package host

// faults.go — this program reports its own failures to Sentinel.
//
// ErrorHandler (sdk/errmap.go) is the one place a propagated error is rendered, so
// the one place a 5xx is known for certain; a run that ends in error and a failed
// job state theirs through cloud.ReportFault. Each is a fault.Fault, stated on the
// event plane through the SAME ingest a browser's error takes (EventCapture with an Error, apps/event/event_rpc.go), so it is scrubbed, grouped
// into an issue and filed by Sentinel's event.error consumer like any other error.
//
// BOUNDED AND DETACHED. A report never blocks the response it describes: it runs
// on its own goroutine, at most faultsInFlight at once, and at most faultsPerWindow
// per faultWindow — past either it is dropped, because a flood of one failure is one
// issue and the count is not worth an outage of the ingest that files it.

import (
	"context"
	"sync"
	"time"

	"github.com/hanzoai/cloud/client"
	eventpeer "github.com/hanzoai/cloud/client/event"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/fault"
)

const (
	faultsInFlight  = 4
	faultsPerWindow = 30
	faultWindow     = time.Minute
	faultTimeout    = 5 * time.Second
	// faultProduct is the surface this program's own failures are filed under.
	faultProduct = "cloud"
)

// faultSend is the ONE plane call, a variable so a test can observe what leaves.
var faultSend = eventpeer.EventCapture

var faults = struct {
	sem    chan struct{}
	mu     sync.Mutex
	start  time.Time
	count  int
	org    string
	source string
}{sem: make(chan struct{}, faultsInFlight)}

// ReportFaultsAs names the org a fault is filed under and the program that raised
// it. App calls it once; before it is called no fault is reported.
func ReportFaultsAs(org, program string) {
	faults.mu.Lock()
	faults.org, faults.source = org, program
	faults.mu.Unlock()
}

// admitFault reports whether one more report fits the window.
func admitFault(now time.Time) bool {
	faults.mu.Lock()
	defer faults.mu.Unlock()
	if faults.org == "" {
		return false
	}
	if now.Sub(faults.start) >= faultWindow {
		faults.start, faults.count = now, 0
	}
	if faults.count >= faultsPerWindow {
		return false
	}
	faults.count++
	return true
}

// reportFault states one fault on the event plane, detached and bounded.
func reportFault(f fault.Fault) {
	if !admitFault(time.Now()) {
		return
	}
	select {
	case faults.sem <- struct{}{}:
	default:
		return
	}
	faults.mu.Lock()
	org, source := faults.org, faults.source
	faults.mu.Unlock()
	in := faultEvent(source, f)
	go func() {
		defer func() { <-faults.sem; _ = recover() }()
		ctx, cancel := context.WithTimeout(context.Background(), faultTimeout)
		defer cancel()
		_, _ = faultSend(cloud.For(ctx, org), in)
	}()
}

// faultEvent is the plane input for one fault. Pure, so it is asserted in a test.
// The attributes are what the aggregator reads without parsing the message.
func faultEvent(source string, f fault.Fault) *client.EventIn {
	attrs := []client.Signal{
		{Name: "service", Value: source},
		{Name: "fault.class", Value: string(f.Class)},
		{Name: "fault.code", Value: f.Code},
		{Name: "fault.op", Value: f.Op},
		{Name: "release", Value: env.Revision()},
	}
	if f.Request != "" {
		attrs = append(attrs, client.Signal{Name: "fault.request", Value: f.Request})
	}
	return &client.EventIn{
		Name:       f.Class.Event(),
		Product:    faultProduct,
		Attributes: attrs,
		Error:      &client.EventError{Type: f.Code, Message: f.Reason, Path: f.Route(), Fingerprint: f.Fingerprint()},
	}
}
