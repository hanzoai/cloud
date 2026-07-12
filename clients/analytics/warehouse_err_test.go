// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0

package analytics

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"

	"github.com/zap-proto/zip"
)

// TestWarehouseErrStatus locks the honest-status contract: a datastore
// connectivity failure (the observed `:9000` i/o timeout) degrades to 503
// "unavailable" — never a raw 502 — while a query the warehouse actively
// rejected stays a 502 bad-gateway.
func TestWarehouseErrStatus(t *testing.T) {
	timeoutErr := &net.OpError{Op: "dial", Net: "tcp", Err: &timeoutError{}}
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"datastore i/o timeout", fmt.Errorf("read tcp 10.0.0.1:9000: i/o timeout"), http.StatusServiceUnavailable},
		{"connection refused", fmt.Errorf("dial tcp 10.0.0.1:9000: connect: connection refused"), http.StatusServiceUnavailable},
		{"context deadline", context.DeadlineExceeded, http.StatusServiceUnavailable},
		{"typed net timeout", timeoutErr, http.StatusServiceUnavailable},
		{"connection reset", fmt.Errorf("read: connection reset by peer"), http.StatusServiceUnavailable},
		{"bad SQL (reachable)", fmt.Errorf("code: 47, DB::Exception: Unknown identifier: foo"), http.StatusBadGateway},
		{"unknown table (reachable)", fmt.Errorf("code: 60, DB::Exception: Table hanzo.x doesn't exist"), http.StatusBadGateway},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			he, ok := warehouseErr("llm", tc.err).(*zip.HTTPError)
			if !ok {
				t.Fatalf("warehouseErr did not return *zip.HTTPError")
			}
			if he.Status != tc.want {
				t.Fatalf("warehouseErr(%q) status = %d, want %d", tc.err, he.Status, tc.want)
			}
		})
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
