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

package limits

// rate.go — the one per-minute limit: how many model requests one person may make
// in a minute, whichever surface serves them — the model API, Zen, a sibling's call
// over the plane (coding runs, bots, agents), the MCP door — counted in ONE window
// per person, the deployment's (cloud.Window "rpm"), so every surface and every pod
// spends the same count.
//
// The limit is the platform's default (flags.Rate), which SuperAdmins set at
// admin.hanzo.ai, lowered where the organization the request acts in, or the
// person's home organization, set less for its own people at platform.hanzo.ai: the
// least of the three. An organization never sets more: the default is the share of a
// lane every person shares. It bounds the rate and nothing else: a plan whose usage
// is unbounded is held to it like any other.
//
// A request past the limit is refused and counts nothing; one inside it is counted
// as it is admitted, so calls in flight cannot all take the last unit.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/flags"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/principal"
)

// CodeRate is what a request past the per-minute limit is refused with: 429, and
// when the window turns.
const CodeRate = "rate_limit_exceeded"

// ErrRate refuses an organization's setting above the platform's default.
var ErrRate = errors.New("an organization may set less than the platform's requests a minute, never more")

// Pace is where one person stands against the per-minute limit after a request was
// asked: the limit in force, what is left of this minute's window, when it turns,
// and whether the request was admitted. A zero Limit names nobody and counted
// nothing.
type Pace struct {
	Limit     int
	Remaining int
	Reset     time.Duration
	Admitted  bool
}

// Pace counts one model request made by person acting in org against the
// per-minute limit, or refuses it, counting nothing, when person's window is full.
// person is whose window it is: "<home org>/<name>", or the key a caller with no
// such name is counted by (a user id, an address). The limit is the least of the
// platform's, the acting org's and the home org's, so a member of several orgs
// cannot act in the one that set less to get past the one that did. A request with
// no key at all, which only a call between processes can be, is not this limit's to
// count: it is admitted and says nothing.
func (l *Limiter) Pace(ctx context.Context, person, org string) Pace {
	if person == "" {
		return Pace{Admitted: true}
	}
	limit, _, _ := l.Rates(ctx, org)
	if home, _, ok := strings.Cut(person, "/"); ok && home != org {
		limit = min(limit, first(l.Rates(ctx, home)))
	}
	used, wait, ok := l.minute.Ceiling([]coord.Window{{Key: url.PathEscape(person), Limit: limit, TTL: time.Minute}})
	return Pace{Limit: limit, Remaining: max(0, limit-used[0]), Reset: wait[0], Admitted: ok}
}

// first is the first of Rates' three answers.
func first(rpm, _, _ int) int { return rpm }

// Rates is the per-minute limit as it governs a person acting in org: the requests
// a minute in force (rpm), the platform's default, and org's own setting, 0 when it
// has none. rpm is the default, lowered where org set less. The default is the
// switch as value knows it — its last answer through a flag plane that cannot
// answer, across a restart too, the seed before any — and a figure below 1 is the
// seed: never no limit at all.
func (l *Limiter) Rates(ctx context.Context, org string) (rpm, def, own int) {
	def = l.figure(ctx, principal.Reserved, flags.Rate, flags.SeedRate)
	if def < 1 {
		def = flags.SeedRate
	}
	def = min(def, flags.MaxRate)
	l.mu.Lock()
	own = l.rates[org]
	l.mu.Unlock()
	if own > 0 && own < def {
		return own, def, own
	}
	return def, def, own
}

// SetRate sets org's own requests a minute per person, written by actor; 0 clears
// it, so org's people are held to the platform's default. A figure above the
// default is refused (ErrRate), and one the store does not take is an error.
func (l *Limiter) SetRate(ctx context.Context, org string, rpm int, actor string) error {
	if _, def, _ := l.Rates(ctx, org); rpm < 0 || rpm > def {
		return ErrRate
	}
	if err := l.store.setRate(ctx, org, rpm, actor, l.now()); err != nil {
		return err
	}
	l.mu.Lock()
	if rpm == 0 {
		delete(l.rates, org)
	} else {
		l.rates[org] = rpm
	}
	l.mu.Unlock()
	return nil
}

// rateSchema is each organization's own setting, created with the rest of the
// store.
const rateSchema = `
CREATE TABLE IF NOT EXISTS rate (
  org   TEXT    NOT NULL PRIMARY KEY,
  rpm   BIGINT  NOT NULL,
  at    BIGINT  NOT NULL,
  actor TEXT    NOT NULL
);`

// setRate keeps org's own setting; 0 removes it.
func (s *limitStore) setRate(ctx context.Context, org string, rpm int, actor string, now time.Time) error {
	if err := dialect.Text(org, actor); err != nil {
		return fmt.Errorf("limits rate: %w", err)
	}
	var err error
	if rpm == 0 {
		_, err = s.db.ExecContext(ctx, `DELETE FROM rate WHERE org = $1`, org)
	} else {
		_, err = s.db.ExecContext(ctx, `INSERT INTO rate (org, rpm, at, actor) VALUES ($1, $2, $3, $4)
ON CONFLICT (org) DO UPDATE SET rpm = excluded.rpm, at = excluded.at, actor = excluded.actor`, org, rpm, now.Unix(), actor)
	}
	if err != nil {
		return fmt.Errorf("limits rate: %w", err)
	}
	return nil
}

// rates is every organization's own setting.
func (s *limitStore) rates(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT org, rpm FROM rate`)
	if err != nil {
		return nil, fmt.Errorf("limits rate: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int{}
	for rows.Next() {
		var org string
		var rpm int
		if err := rows.Scan(&org, &rpm); err != nil {
			return nil, fmt.Errorf("limits rate: %w", err)
		}
		out[org] = rpm
	}
	return out, rows.Err()
}
