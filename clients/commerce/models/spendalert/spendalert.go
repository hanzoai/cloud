package spendalert

import (
	"strings"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/val"
	"github.com/hanzoai/orm"
)

func init() { orm.Register[SpendAlert]("spend-alert") }

// DefaultSoftPct is the soft-warn threshold applied when a row sets none: at 80%
// of Threshold the request still succeeds but carries an X-Spend-Warn signal.
const DefaultSoftPct = 80

// defaultProject is the reserved id of every org's default project. It MUST match
// the cloud wire contract (clients/principal.DefaultProject / iamauth.DefaultProject):
// an absent project header and the literal "default" denote the SAME scope, so the
// org-wide default row (Project "") and a request tagged project "default" resolve
// to one bucket — the backward-compatibility invariant.
const defaultProject = "default"

// SpendAlert is a tenant-configured budget on cumulative spend for a SCOPE
// (Project, Service). It is BOTH the soft alert it has always been AND, when
// Enforce is set, a HARD per-scope spend cap (issue #70).
//
//   - Threshold (cents) is the scope's cap / alert ceiling.
//   - Project / Service identify the scope. Both are INDEXED so period spend can
//     be summed per scope. "" is the wildcard: empty Project AND Service = the
//     org-wide default row; (P,"") = every service in project P; ("",S) = service
//     S in any project.
//   - Enforce=false (the DEFAULT — every legacy row keeps its behavior) => SOFT:
//     the cap only warns (X-Spend-Warn), never blocks.
//   - Enforce=true => HARD: once period spend for the scope would exceed
//     Threshold, the metering gate returns ErrSpendCapExceeded => 402.
//   - RateLimitRpm>0 => the cloud ScopeRateLimit middleware caps requests/min for
//     the scope => 429.
//
// The row carries NO running total: period spend is derived on demand from the
// append-only usage ledger, so a cap change takes effect on the next request with
// no reconciliation and can never drift from the debited spend.
type SpendAlert struct {
	mixin.Model[SpendAlert]

	UserId    string `json:"userId"`
	Title     string `json:"title"`
	Threshold int64  `json:"threshold"` // cents — the scope's spend cap / alert ceiling.
	Currency  string `json:"currency"`

	// Scope (issue #70), both INDEXED for per-scope spend sums.
	Project string `json:"project"`
	Service string `json:"service"`

	// Enforce turns the soft alert into a HARD cap (default false = soft/warn only).
	Enforce bool `json:"enforce"`

	// SoftPct is the soft-warn threshold percent (1..100). 0 => DefaultSoftPct.
	SoftPct int `json:"softPct"`

	// RateLimitRpm is the requests-per-minute ceiling for this scope. 0 = none.
	RateLimitRpm int `json:"rateLimitRpm"`

	// TriggeredAt stores the ISO timestamp when the alert last fired.
	// Empty string means not yet triggered.
	TriggeredAt string `json:"triggeredAt,omitempty"`
}

// EffectiveSoftPct clamps SoftPct into (0,100], defaulting an unset/invalid value
// to DefaultSoftPct so a row always yields a sane warn threshold.
func (s *SpendAlert) EffectiveSoftPct() int {
	if s.SoftPct <= 0 || s.SoftPct > 100 {
		return DefaultSoftPct
	}
	return s.SoftPct
}

// Covers reports whether this row's scope covers a request tagged (project,
// service): each of the row's axes is the wildcard "" or equals the request's.
func (s *SpendAlert) Covers(project, service string) bool {
	return (s.Project == "" || s.Project == project) &&
		(s.Service == "" || s.Service == service)
}

// NormalizeProject folds the default project onto the canonical "" scope so a
// request's project matches the org-wide default row exactly as the cloud
// principal resolver does. A non-default project is returned trimmed, verbatim.
func NormalizeProject(project string) string {
	p := strings.TrimSpace(project)
	if p == defaultProject {
		return ""
	}
	return p
}

func (s *SpendAlert) Validator() *val.Validator {
	return val.New()
}

func New(db *datastore.Datastore) *SpendAlert {
	s := new(SpendAlert)
	s.Init(db)
	s.Parent = db.NewKey("synckey", "", 1, nil)
	return s
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("spend-alert")
}
