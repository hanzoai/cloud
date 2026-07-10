package config

import (
	_ "embed"
	"encoding/json"
	"sync"

	"github.com/hanzoai/cloud/clients/commerce/ossattr"
)

//go:embed oss-payout.json
var ossPayoutJSON []byte

var (
	ossPayoutOnce sync.Once
	ossPayout     *OSSPayoutProgram
)

// OSSPayoutProgram is the parsed oss-payout.json config. It carries the policy
// knobs for the SBOM-driven OSS-developer payout system; the actual cap is
// enforced in ossattr (MaxPoolFraction), so a value here above 0.25 is clamped.
type OSSPayoutProgram struct {
	Id               string  `json:"id"`
	Name             string  `json:"name"`
	Version          int     `json:"version"`
	Active           bool    `json:"active"`
	PoolFraction     float64 `json:"poolFraction"`
	DirectWeight     float64 `json:"directWeight"`
	TransitiveWeight float64 `json:"transitiveWeight"`
	Notes            string  `json:"notes,omitempty"`
}

// Policy converts the config into the pure ossattr.Policy used by the
// attribution core. The pool fraction is clamped to MaxPoolFraction by
// ossattr itself; weights fall back to the ossattr defaults when unset.
func (p *OSSPayoutProgram) Policy() ossattr.Policy {
	pol := ossattr.DefaultPolicy()
	pol.PoolFraction = p.PoolFraction
	if p.DirectWeight > 0 {
		pol.DirectWeight = p.DirectWeight
	}
	if p.TransitiveWeight > 0 {
		pol.TransitiveWeight = p.TransitiveWeight
	}
	return pol
}

// GetOSSPayoutProgram returns the parsed OSS payout program config, loaded once
// from the embedded JSON. On parse failure it returns the safe default (full
// 25% pool, default weights, active).
func GetOSSPayoutProgram() *OSSPayoutProgram {
	ossPayoutOnce.Do(func() {
		cfg := &OSSPayoutProgram{}
		if err := json.Unmarshal(ossPayoutJSON, cfg); err != nil {
			ossPayout = &OSSPayoutProgram{
				Id:               "hanzo-oss-payout",
				Active:           true,
				PoolFraction:     ossattr.MaxPoolFraction,
				DirectWeight:     1.0,
				TransitiveWeight: 0.25,
			}
			return
		}
		ossPayout = cfg
	})
	return ossPayout
}
