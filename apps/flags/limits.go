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

package flags

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The usage policy: what a paid plan includes of each class of model, and how each
// model may be paid for. It is ONE record a SuperAdmin edits at admin.hanzo.ai
// (/v1/admin/usage/policy), held as these switches so a change is live within one
// flag TTL. A plan includes what is saved for it and nothing else: no figure of a
// plan is compiled in (carried.go is the one migration that saved the figures the
// binary once shipped).
//
// A plan includes, per seat and per billing period, usage of two classes measured
// at list price — premium (third-party frontier models) and ours (Hanzo's own) —
// with an optional shorter window on each, and two request windows (a session and a
// UTC day) over every request it covers. Past its included usage, prepaid cash pays
// when the plan allows overage.

// Unbounded is a class allowance (premium, ours) that includes all of the class's
// usage: no cap, which 0 (none included) cannot say and a large number only
// imitates. A plan saved with it is never refused or sent to credits for that
// class; its request windows, model policy and rate limits still hold.
const Unbounded = -1

// Renamed is each slug a rung was sold under before it was renamed, to the rung that
// answers for it now: @hanzo/plans' `replaces` lists, the statement commerce's tier
// reads too, so a subscription that recorded the old slug includes the rung's figures
// whichever slug the tier names. Pro and Max were renamed to the multiple of Go they
// include (hanzo-inc/cloud#565, owner 2026-11-09): pro-2x is Pro, pro-3x, and max-10x
// is Max, max-15x. max is the retired $99 rung, served as max-5x.
var Renamed = map[string]string{"max": "max-5x", "pro-2x": "pro-3x", "max-10x": "max-15x"}

// The classes a plan includes usage of.
var PlanClasses = []string{"premium", "ours"}

// CreditKey is the switch saying whether granted credit pays for a model of class
// that has no rule of its own in the model policy.
func CreditKey(class string) string { return "ai_credit_" + class }

// SeedCredit: granted credit pays for Hanzo's own models and never for a third
// party's. Credit is what the platform grants so a person can try what Hanzo serves;
// a premium model is paid by a paid plan or by cash, which only a paying customer
// adds.
var SeedCredit = map[string]bool{"premium": false, "ours": true}

// HandoverKey is the switch saying whether a signed-in app's chat on the Free plan,
// asking a model of class that nothing pays for, is handed to Enso's router at no
// charge (on) or refused 402 insufficient_balance naming the top-up (off). A program
// (an API key) is always refused: it named its model and gets it or the refusal.
func HandoverKey(class string) string { return "ai_handover_" + class }

// SeedHandover is the shipped handover per class: on, as the Free plan has always been
// served in the apps. It is the owner's to turn off for premium, so a third party's
// model in the apps is prepaid like it is on the API. Hanzo's own classes are not
// switched here: they follow the plan.
var SeedHandover = map[string]bool{"premium": true}

// FeeKey is the switch holding a fee, a percent, by who takes it. A vendor's
// (openrouter) is what a model resold from it bills over the price it states: its
// own charge on the credit it sells, passed through as part of what the call cost us.
// The platform's two (PlatformFee, BYOKFee) are the margin, and the only one: every
// call priced at cost bills what it cost us and the platform's fee on top (hanzoai/ai
// fee.go).
func FeeKey(name string) string { return "ai_fee_" + name }

const (
	// PlatformFee is the platform's fee on a call Hanzo's own accounts paid for.
	PlatformFee = "hanzo"
	// BYOKFee is the platform's fee on a call the org's own provider key paid for.
	BYOKFee = "hanzo_byok"
)

// SeedFees are every fee: OpenRouter charges 5.5% on the credit it sells; the
// platform's is 5.5%, OpenRouter's own rate, and 1% on a call the org's own key paid
// for.
var SeedFees = map[string]string{"openrouter": "5.5", PlatformFee: "5.5", BYOKFee: "1"}

// Platform reports whether a fee is the platform's own, which must be more than
// zero: a call sold at cost earns nothing.
func Platform(name string) bool { return name == PlatformFee || name == BYOKFee }

const (
	// PlanSessionHours is the length of one session window: a paid plan's request
	// window (apps/ai/limits) and the Free plan's calls (apps/allowance) alike.
	PlanSessionHours = "ai_plan_session_hours"
	// ModelPolicy is the per-model record: each model's class, whether granted credit
	// pays for it, its daily cap per plan where the plan cannot pay, its share of each
	// plan's class allowance and the Hanzo model it hands a conversation to past it.
	// JSON, keyed by model id or a `*` glob of ids.
	ModelPolicy = "ai_model_policy"
)

// Seeds for the switches above.
const SeedSessionHours = 5

// MaxSessionHours is the longest session window a value of PlanSessionHours can
// state: a week.
const MaxSessionHours = 7 * 24

// Session is the session window's length a value of PlanSessionHours states, held
// to 1 through MaxSessionHours: the seed where it states none or less than one, a
// week where it states more. A window opens at the first request after the last one
// ended and runs this long. The bound is what keeps a typed figure from
// overflowing a duration into a window of no length, which would count nothing.
func Session(hours int) time.Duration {
	if hours <= 0 {
		hours = SeedSessionHours
	}
	return time.Duration(min(hours, MaxSessionHours)) * time.Hour
}

// SeedFreeSession is the Free plan's zero-priced AI calls per session window, per
// person: 50 every 5 hours. It is the shipped default of the free
// allowance's ai_session_free (apps/allowance), and what the usage policy shows
// while that switch is unset.
const SeedFreeSession = 50

// TopPremium are the costliest tenth of premium models by list price, grouped so a
// family shares one share of a plan. What each may take of a plan's premium
// allowance is the cap a SuperAdmin saves for it in the model policy.
var TopPremium = []string{
	"openai/o1-pro*", "openai/o1", "openai/o3-pro*", "openai/gpt-*-pro*",
	"openai/gpt-6-astra*", "~openai/gpt-astra*", "openai/gpt-4", "openai/gpt-4-turbo*",
	"openai/gpt-5.5", "openai/gpt-chat-latest",
	"anthropic/claude-opus*", "~anthropic/claude-opus*",
	"anthropic/claude-fable*", "~anthropic/claude-fable*",
	"sakana/fugu-ultra*",
}

// VideoPremium are the video model families (served at /v1/videos/generations,
// each clip billed at what its vendor says it cost): one clip costs as much as
// hundreds of chat turns, so each family is capped per plan in the model policy,
// as the costliest chat families are, and is paid in real money only. A clip is also held at its worst case against what the plan has left
// before it starts (ai's paid lane), so the share is a ceiling and not the guard.
var VideoPremium = []string{
	"google/veo-*", "kwaivgi/kling-*", "bytedance/seedance-*", "alibaba/wan-*", "alibaba/happyhorse-*",
	"minimax/hailuo-*", "runway/*", "x-ai/grok-imagine-video*", "black-forest-labs/flux-*video*", "heygen/*",
}

// SeedModelPolicy is the model policy while none is saved: Kai is ours, takes
// granted credit, and a free plan may ask it 500 times a day; Zen answering
// decisions (zenlm/zen6, zenlm/zen6-flash, billed input and output at a PROVISIONAL
// launch price the owner has not confirmed) is ours, takes granted credit, and has
// no free daily cap: a free plan needs granted credit or prepaid cash; Jev is
// premium, is paid in real money only, and needs a paid plan or prepaid balance;
// each TopPremium and VideoPremium family is premium and paid in real money only.
// It states no plan's share of anything: a family's cap per plan is what a
// SuperAdmin saves in the policy.
var SeedModelPolicy = modelPolicy(nil)

// modelPolicy is the model policy's rules as JSON, each costly family capped per
// plan at caps (period cents by plan slug); nil caps none.
func modelPolicy(caps map[string]int) string {
	type cap struct {
		PeriodCents int `json:"periodCents"`
	}
	type rule struct {
		Class     string         `json:"class"`
		GrantsPay bool           `json:"grantsPay"`
		DailyCaps map[string]int `json:"dailyCaps,omitempty"`
		Caps      map[string]cap `json:"caps,omitempty"`
	}
	// Every Jev id — the decision model, its router, its aliases — is one rule, so
	// Jev is premium on whichever path asks; Kai, under both its names, is Hanzo's.
	jev := rule{Class: "premium", GrantsPay: false, DailyCaps: map[string]int{"free": 0}}
	kai := rule{Class: "ours", GrantsPay: true, DailyCaps: map[string]int{"free": 500}}
	// A Zen decision costs a completion, so a daily free allowance is the one thing
	// it must not inherit from Kai's: free 0 sends a free plan to credit or cash.
	zen := rule{Class: "ours", GrantsPay: true, DailyCaps: map[string]int{"free": 0}}
	out := map[string]rule{
		"kai":            kai,
		"hanzo/kai":      kai,
		"zenlm/zen6*":    zen,
		"typesafe/jev*":  jev,
		"~typesafe/jev*": jev,
	}
	var per map[string]cap
	for plan, cents := range caps {
		if per == nil {
			per = map[string]cap{}
		}
		per[plan] = cap{PeriodCents: cents}
	}
	for _, pattern := range append(slices.Clone(TopPremium), VideoPremium...) {
		out[pattern] = rule{Class: "premium", GrantsPay: false, Caps: per}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		panic(fmt.Sprintf("flags: model policy: %v", err))
	}
	return string(raw)
}

// Valued is the definition a valued platform switch is stored as: active for
// everyone, the value as its payload. A boolean switch turned off is stored
// inactive so TypeBool and TypeString evaluations both resolve it to false.
func Valued(value string, boolean bool) (json.RawMessage, error) {
	if boolean {
		if !strings.EqualFold(strings.TrimSpace(value), "true") {
			return json.Marshal(map[string]any{"active": false})
		}
		return json.Marshal(map[string]any{
			"active": true,
			"filters": map[string]any{
				"groups":   []map[string]any{{"properties": []any{}, "rollout_percentage": 100}},
				"payloads": map[string]any{"true": true},
			},
		})
	}
	var payload any = value
	if n, err := strconv.ParseInt(value, 10, 64); err == nil {
		payload = n
	}
	return json.Marshal(map[string]any{
		"active": true,
		"filters": map[string]any{
			"groups":   []map[string]any{{"properties": []any{}, "rollout_percentage": 100}},
			"payloads": map[string]any{"true": payload},
		},
	})
}

// PlanKey is the switch holding one rung's figure: field is session, day, premium,
// ours, premium_window, ours_window, premium_window_hours, ours_window_hours or
// overage.
func PlanKey(field, plan string) string {
	return "ai_plan_" + field + "_" + strings.ReplaceAll(strings.ToLower(plan), "-", "_")
}

// Keyed reports whether a plan slug keys switches of its own. A field's name is
// another's plus "_window" or "_hours" (premium, premium_window,
// premium_window_hours), so a slug beginning "window-" or "hours-" would key the
// switch of a different field of a different plan: PlanKey("premium", "window-x")
// is PlanKey("premium_window", "x").
func Keyed(plan string) bool {
	p := strings.ToLower(plan)
	return !strings.HasPrefix(p, "window-") && !strings.HasPrefix(p, "hours-")
}

// PlanFields are the per-plan switches, in the order the record lists them.
var PlanFields = []string{"session", "day", "premium", "premium_window", "premium_window_hours",
	"ours", "ours_window", "ours_window_hours", "overage"}

// The plan entitlements (hanzo-inc/cloud#803): what a request needs its payer's plan
// to include BY NAME before it is served, however much money the payer holds. Each is
// priced well above the rest of its kind, so neither a plan's included usage nor
// prepaid cash reaches it unless the plan names it, and a payer with no plan holds
// none. They are not PlanFields: a plan is named, and includes its figures, whether
// or not it includes an entitlement.
const (
	// EntitleFable is Claude Fable 5.2, by every name it answers to: the routes
	// apps/ai/capability gives the entitlement.
	EntitleFable = "fable"
	// EntitleFast is fast mode: a chat raced across providers ("fast": true, X-Fast),
	// or a vendor's own faster lane (speed: "fast"). One answer costs several.
	EntitleFast = "fast"
)

// Entitlements are every plan entitlement, in the order the policy lists them.
var Entitlements = []string{EntitleFable, EntitleFast}

// EntitlementName is each entitlement as a sentence names it.
var EntitlementName = map[string]string{EntitleFable: "Claude Fable 5.2", EntitleFast: "Fast mode"}

// EntitlementKey is the switch saying whether plan includes the entitlement name.
func EntitlementKey(name, plan string) string {
	return "ai_entitle_" + name + "_" + strings.ReplaceAll(strings.ToLower(strings.TrimSpace(plan)), "-", "_")
}

// SeedEntitled are the plans that include each entitlement while nothing is saved:
// the top of the personal ladder (Max 15x, Ultra, Ultimate, Unlimited) and the retired
// Max rungs above Max 15x (Max 20x, Max 50x). Every other plan includes neither until a
// SuperAdmin turns its switch on.
var SeedEntitled = map[string][]string{
	EntitleFable: {"max-15x", "ultra", "ultimate", "unlimited", "max-20x", "max-50x"},
	EntitleFast:  {"max-15x", "ultra", "ultimate", "unlimited", "max-20x", "max-50x"},
}

// Entitled reports whether plan includes the entitlement name as shipped.
func Entitled(name, plan string) bool {
	return slices.Contains(SeedEntitled[name], strings.ToLower(strings.TrimSpace(plan)))
}

const (
	// StorageOrgQuotaGiB is the default organization S3 and Library storage quota
	// across all monthly plans, in GiB (-1 = unlimited).
	StorageOrgQuotaGiB = "storage_org_quota_gib"
	// StorageMaxFileMiB is the largest single object an S3 or Library upload may
	// store, in MiB.
	StorageMaxFileMiB = "storage_max_file_mib"
	// SeedStorageQuotaGiB is the shipped default organization storage quota (100 GiB).
	SeedStorageQuotaGiB = 100
	// SeedStorageMaxFileMiB is the shipped default per-file upload limit (2048 MiB = 2 GiB).
	SeedStorageMaxFileMiB = 2048
	// MaxStorageQuotaGiB is the largest finite storage quota a plan or organization
	// override may state (1 PiB = 1048576 GiB).
	MaxStorageQuotaGiB = 1 << 20
)

// Rate is the switch holding the platform's default of the one per-minute limit:
// the model requests one person may make in a minute, on every surface that serves
// one (apps/ai/limits Pace). An organization's own setting may lower it and never
// raise it. It bounds the rate and nothing else: a plan whose usage is unbounded is
// held to it like every other.
const Rate = "ai_rpm"

// SeedRate is Rate as shipped, the owner's figure (2026-11-07): every person shares
// NVIDIA's lane of 80 requests a minute, two keys of 40, so no one person may pass
// 40. SuperAdmins set it at admin.hanzo.ai Usage policy.
const SeedRate = 40

// MaxRate is the most a per-minute figure may state: a million requests a minute is
// past any one person, and nothing larger is a limit.
const MaxRate = 1_000_000

// StoragePlanKey is the switch holding one plan's organization S3 storage quota in
// GiB (-1 = unlimited; 0 or unset = StorageOrgQuotaGiB).
func StoragePlanKey(plan string) string {
	return "storage_plan_quota_gib_" + strings.ReplaceAll(strings.ToLower(strings.TrimSpace(plan)), "-", "_")
}

// StorageOrgKey is the switch holding one organization's S3 storage quota override
// in GiB (-1 = unlimited; 0 or unset = defer to the organization's plan or StorageOrgQuotaGiB).
func StorageOrgKey(org string) string {
	return "storage_org_quota_gib_" + strings.ReplaceAll(strings.ToLower(strings.TrimSpace(org)), "-", "_")
}

func init() {
	Register(Def{Key: StorageOrgQuotaGiB, Category: "Gateway", Type: TypeInt, Default: strconv.Itoa(SeedStorageQuotaGiB),
		Label: "S3 & Library organization storage quota (GiB)",
		Desc:  "Default total S3 and Library storage an organization may hold across all of its buckets on monthly plans, in GiB (-1 = unlimited). Enterprise or custom plans and per-organization overrides (storage_plan_quota_gib_<plan>, storage_org_quota_gib_<org>) take precedence."})
	Register(Def{Key: StorageMaxFileMiB, Category: "Gateway", Type: TypeInt, Default: strconv.Itoa(SeedStorageMaxFileMiB),
		Label: "S3 & Library max single file upload (MiB)",
		Desc:  "Largest single file an S3 or Library upload may store, in MiB (2048 MiB = 2 GiB by default)."})
	Register(Def{Key: PlanSessionHours, Category: "Gateway", Type: TypeInt, Default: strconv.Itoa(SeedSessionHours),
		Label: "AI session length (hours)",
		Desc:  "A plan's session window, and the Free plan's window of free calls, starts at its first request after the last window ended and lasts this long: 1 to 168 hours; 0 or less is the shipped 5, more than 168 is 168."})
	Register(Def{Key: Rate, Category: "Gateway", Type: TypeInt, Default: strconv.Itoa(SeedRate),
		Label: "AI requests a minute, per person",
		Desc:  "The model requests one person may make in a minute, on every surface: the API, chat, coding runs, bots and MCP. An organization may set less for its own people and never more. 1 or more; a figure below 1 reads as the shipped 40. Edited at admin.hanzo.ai Usage policy."})
	Register(Def{Key: ModelPolicy, Category: "Gateway", Type: TypeString, Default: SeedModelPolicy,
		Label: "AI model policy",
		Desc:  "Per model or glob: its class (premium, ours, free), whether granted credit pays for it, its daily cap per plan when neither the plan nor credits pay (0 = needs a paid plan or prepaid), its share of each plan's class allowance, and the Hanzo model that answers past it. Edited at admin.hanzo.ai Usage policy."})
	for _, class := range PlanClasses {
		Register(Def{Key: CreditKey(class), Category: "Gateway", Type: TypeBool, Default: strconv.FormatBool(SeedCredit[class]),
			Label: "AI granted credit pays — " + class,
			Desc:  "Whether credit the platform granted pays for a " + class + " model that has no rule of its own. Off: a paid plan or cash pays, and nothing else. Edited at admin.hanzo.ai Usage policy."})
	}
	// One switch per entitlement per plan shipped with it, on the board so a
	// SuperAdmin can turn it off; any other plan is entitled by saving its key
	// (EntitlementKey) true.
	for _, name := range Entitlements {
		for _, plan := range SeedEntitled[name] {
			Register(Def{Key: EntitlementKey(name, plan), Category: "Gateway", Type: TypeBool, Default: strconv.FormatBool(Entitled(name, plan)),
				Label: "AI plan entitlement — " + name + ", " + plan,
				Desc:  "Whether the " + plan + " plan includes " + EntitlementName[name] + ". Off: a request that needs it is refused 402 entitlement_required before any provider is asked, whatever the payer holds; a payer with no plan holds no entitlement. Another plan includes it once its " + EntitlementKey(name, "<plan>") + " is saved true."})
		}
	}
	for class, seed := range SeedHandover {
		Register(Def{Key: HandoverKey(class), Category: "Gateway", Type: TypeBool, Default: strconv.FormatBool(seed),
			Label: "AI handover to Enso — " + class,
			Desc:  "Whether a signed-in app's Free-plan chat that asks a " + class + " model nothing pays for is answered by Enso at no charge (on) or refused 402 insufficient_balance naming the top-up (off). An API key is always refused. Edited at admin.hanzo.ai Usage policy."})
	}
	for name, pct := range SeedFees {
		def := Def{Key: FeeKey(name), Category: "Gateway", Type: TypeString, Default: pct,
			Label: "AI resale fee — " + name + " (%)",
			Desc:  "The percent a model resold from " + name + " bills over the price " + name + " states: the vendor's own charge on the credit it sells, passed through as part of the call's cost. Edited at admin.hanzo.ai Usage policy."}
		switch name {
		case PlatformFee:
			def.Label = "AI platform fee (%)"
			def.Desc = "The percent every call priced at cost bills over what it cost us, on Hanzo's own accounts: the margin, and the only one. More than zero. Edited at admin.hanzo.ai Usage policy."
		case BYOKFee:
			def.Label = "AI platform fee, own key (%)"
			def.Desc = "The percent a call on the org's own provider key bills over that provider's price: the platform's fee where the org pays the provider itself. More than zero. Edited at admin.hanzo.ai Usage policy."
		}
		Register(def)
	}
}
