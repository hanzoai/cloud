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

package featuregate

import "strings"

// seedFor returns the initial registry for a brand. LAUNCH POSTURE: every hosted
// product is seeded waitlistMode=ON (gated) — the platform opens in waitlist mode,
// and admin.hanzo.ai flips services OFF one at a time. Seed is idempotent
// (INSERT OR IGNORE), so this is only ever the FIRST-BOOT default; a live toggle is
// never overwritten. admin.<brand> is deliberately NOT seeded (it is admin-only via
// admin-guard, not a waitlist surface).
//
// White-labeled by brand so a Lux/Zoo/Pars deployment governs its OWN hosts. New
// hosted services onboard at runtime via POST /v1/admin/services (no redeploy).
func seedFor(brand string) []SeedService {
	d := domainFor(brand)
	// The apex product hosts each brand ships, plus their <label>.<domain> alias.
	return []SeedService{
		{Service: "studio", DisplayName: "Studio", Description: "AI app studio",
			WaitlistMode: true, Hosts: []string{"studio." + d}},
		{Service: "chat", DisplayName: "Chat", Description: "AI chat",
			WaitlistMode: true, Hosts: hostsFor(brand, "chat", "chat."+d)},
		{Service: "console", DisplayName: "Console", Description: "Cloud console",
			WaitlistMode: true, Hosts: []string{"console." + d}},
		{Service: "app", DisplayName: "App", Description: "App builder",
			WaitlistMode: true, Hosts: hostsFor(brand, "app", "app."+d)},
		{Service: "api", DisplayName: "API", Description: "Inference API gateway",
			WaitlistMode: true, Hosts: []string{"api." + d}},
		{Service: "team", DisplayName: "Team", Description: "Team workspace",
			WaitlistMode: true, Hosts: hostsFor(brand, "team", "team."+d)},
	}
}

// domainFor maps a brand to its primary domain. Defaults to hanzo.ai.
func domainFor(brand string) string {
	switch strings.ToLower(strings.TrimSpace(brand)) {
	case "lux":
		return "lux.network"
	case "zoo":
		return "zoo.ngo"
	case "pars":
		return "pars.network"
	default:
		return "hanzo.ai"
	}
}

// hostsFor returns the apex-brand host (<brand>.<tld> for the label, e.g.
// hanzo.chat / hanzo.app / hanzo.team) plus the <label>.<domain> alias, when the
// brand ships an apex-label domain; else just the alias. Hanzo/Zoo ship
// hanzo.chat / zoo.chat style apex hosts; the generic alias always applies.
func hostsFor(brand, label, alias string) []string {
	switch strings.ToLower(strings.TrimSpace(brand)) {
	case "", "hanzo":
		return []string{"hanzo." + label, alias}
	case "zoo":
		return []string{"zoo." + label, alias}
	default:
		return []string{alias}
	}
}
