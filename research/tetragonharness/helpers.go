// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"context"
	"time"
)

// useContext returns the root context for escape runs (no global deadline;
// per-probe deadlines come from RunOptions).
func useContext() context.Context { return context.Background() }

// contextWithTimeout wraps context.WithTimeout with a float64 duration.
func contextWithTimeout(secs float64) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), time.Duration(secs*float64(time.Second)))
}
