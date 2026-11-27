// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cloud

import (
	"context"
	"sync"
	"time"

	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/ai/address"
)

// Window is a fixed-window request count per caller address, for a keyspace the
// caller influences.
//
// An address is charged to each of its address.Buckets, narrowest first: an IPv4
// address as itself, an IPv6 address as its /64 and then its /48 at the bucket's
// scale, so a tunnel holding a /48 is sixteen callers and never 65,536.
//
// A named window is the deployment's (coord): every pod charges the same window
// under its name, so a caller spread across pods is held to one limit, not one per
// pod. With no shared KV it counts in this process alone, apart from every other
// window, which is the deployment's count when it is one pod. Expired windows are
// swept, so the keyspace a caller mints for free is never held past its window.
//
// The zero Window has no name and counts in this process alone, whatever the
// deployment shares: the edge's flood budget is one pod's, a guard on what that pod
// will accept, and costs no round trip on every public request. It counts nothing
// until Spend names a limit; NewWindow fixes one for Take.
type Window struct {
	name   string
	limit  int
	period time.Duration

	once sync.Once
	own  *coord.Coord
}

// NewWindow allows limit requests per caller in each period, counted under name.
func NewWindow(name string, limit int, period time.Duration) *Window {
	return &Window{name: name, limit: limit, period: period}
}

// Take charges one request from the caller at addr to each of its address.Buckets
// and reports whether it is within the limit.
func (w *Window) Take(addr string) bool {
	left, _ := w.Spend(address.Buckets(addr), w.limit, w.period)
	return left >= 0
}

// Spend is Take at a limit and period read per request, over the buckets the caller
// is counted on: it charges one request to each, narrowest first, each held to limit
// times its scale in windows of period. It answers what the tightest bucket has left
// — negative once over — and when the caller may next be admitted: the latest reset
// among the buckets with no room left, else the tightest bucket's.
//
// A request over the limit is still charged to the bucket that refused it, so a caller
// cannot ride a refused request for free. A WIDER BUCKET IS CHARGED ONLY FOR WHAT THE
// NARROWER ADMITTED: one /64 hammering past its own limit spends nothing of the /48
// its neighbours share. A full /48 behind a refusing /64 still sets the reset, or the
// caller is told to return while the /48 will refuse them.
func (w *Window) Spend(buckets []address.Bucket, limit int, period time.Duration) (left int, reset time.Time) {
	keys := make([]string, len(buckets))
	scales := make([]int, len(buckets))
	for i, b := range buckets {
		keys[i], scales[i] = b.Key, b.Scale
	}
	return w.coord().Spend(context.Background(), w.name, keys, scales, limit, period)
}

// Ceiling charges one request to every window at once, or to none, in one question
// to the count: coord.Ceiling, under w's name. It answers each window's count, its
// time to reset as the count measures it, and whether the request was admitted. A
// rate ceiling that binds several windows to one request — a key's own and its
// org's — asks once, not once per window.
func (w *Window) Ceiling(ws []coord.Window) ([]int, []time.Duration, bool) {
	return w.coord().Ceiling(context.Background(), w.name, ws)
}

// coord is where w counts: the deployment's count when w is named and the deployment
// shares one, else w's own in this process.
func (w *Window) coord() *coord.Coord {
	c := coord.Default()
	if w.name == "" || !c.Shared() {
		w.once.Do(func() { w.own = coord.Alone() })
		c = w.own
	}
	return c
}
