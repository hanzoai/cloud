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

package finance

import (
	"github.com/hanzoai/cloud/internal/environ"
	"strconv"
)

// cashCeilingEnv names the daily upstream-cash ceiling, in integer CENTS.
const cashCeilingEnv = "CLOUD_DAILY_CASH_CEILING_CENTS"

// dailyCashCeilingCents reads the ceiling the cash circuit-breaker enforces.
//
// ZERO IS THE DEFAULT AND IT MEANS DISARMED. Unset, blank, unparseable or
// negative all yield 0, so the breaker stays off unless someone states a real
// number. That asymmetry is deliberate: this guard sits in front of all paid
// inference, so every ambiguous input must resolve toward "allow". A typo in a
// ConfigMap should cost a day of unguarded spend, never a fleet-wide outage.
func dailyCashCeilingCents() int64 {
	raw := environ.Or(cashCeilingEnv, "")
	if raw == "" {
		return 0
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}
