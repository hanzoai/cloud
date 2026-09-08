// Copyright © 2026 Hanzo AI. MIT License.

package cloud

// tariff.go — what a bill is made of, and what each part of it costs.
//
// FOUR COMPONENTS AND NO OTHERS. A bill is model inference, computer, web tools
// and media generation. Each is a line on the spend breakdown under its own name,
// so the four names a customer reads back are the four the meter attributes to —
// apps/agents charges against the constants below rather than against strings of
// its own, which is what stops a breakdown naming a component no price list
// mentions.
//
// TWO ARE QUOTED, TWO ARE MEASURED, and the split is a property of the work
// rather than a policy someone chose. A completion and a second of compute can
// both be bounded from above before they happen, so an agent is refused before it
// breaches its budget instead of after. A web call is priced from what the
// provider charged, and a render's cost is not knowable until it has rendered —
// so both are booked from what they used, and a render is admitted against the
// headroom under the agent's per-task cap, which is what lets a job run with no
// quote to check it against.
//
// EVERY RATE HERE IS THE ONE THE LEDGER BOOKS. This card is not a second copy of
// the price list: each priced row is read through [RateMicros] — the same
// resolver the metering path calls, answering from commerce's authority and
// falling back to the same compiled floor — so the number a customer is quoted
// and the number they are charged cannot drift apart. That is the whole reason
// the card is assembled in Go beside the floors rather than typed into the
// published document, where it went stale before (see apps/pricing/commerce.go).
//
// FOUR OF THE EIGHT ROWS CARRY NO METER. A paused computer, a computer's
// creation, the interfaces and a seat are zero by construction, not by a price
// nobody has set: "there is no creation fee" is the product. Resolving them
// through the authority would make each of them a row an operator could retune
// into a charge, which is the opposite of what they state.
//
// PER HOUR IN THE STORED NUMBER, per second in what is charged. A vCPU-second is
// 14 µ$ and a GiB-second is four and a half of them, which no integer holds; the
// hour is exact for both, and it is the unit [RuntimeCost] already prices a span
// in — rate × secs / 3600, half-up. So the hour is the fact, one number, and a
// reader wanting the second divides.

import (
	"context"
	"sync"
)

// The components a bill is made of. Every debit is attributed to exactly one, and
// the spend breakdown sums per component — so these are the names on an invoice,
// not an internal enum.
//
// They live here rather than in apps/agents, which is where they were, because
// two subsystems need to agree on them: the one that attributes spend and the one
// that publishes what the four are. A fact more than one app must state is a fact
// this package holds, which is the same argument [RuntimeHourMicros] makes.
const (
	ComponentModel    = "model"
	ComponentComputer = "computer"
	ComponentWeb      = "web"
	ComponentMedia    = "media"
)

// The tiers a completion is metered in. They are DISJOINT: a token is counted in
// exactly one of them, so the five never double-count a request between them.
const (
	TierIn         = "input"
	TierCacheRead  = "cache_read"
	TierCacheWrite = "cache_write"
	TierOut        = "output"
	TierReasoning  = "reasoning"
)

// The completion windows a request may ask for, dearest first. The window is the
// biggest lever on what a call costs — the same model at a different window is a
// different price — so it is chosen per request rather than per account, and the
// interactive tariff is paid only when a person is actually waiting.
//
// The ORDER is the published fact and it is what [Speeds] carries. The numbers
// are per model and come from commerce, addressed by the window: a window is a
// rung of a model's rate, the way a context rung already is, and inventing a
// factor here would be a second price for a model this package does not price.
const (
	SpeedImmediate = "immediate"
	SpeedPriority  = "priority"
	SpeedLoose     = "loose"
)

// ComputerProduct and its meters address what a running computer costs. An
// operator retunes either at admin.hanzo.ai, with an audit trail; the floors
// below are charged until they do.
const (
	ComputerProduct = "computer"
	VCPUMeter       = "vcpu-hour"
	MemoryMeter     = "memory-gib-hour"
)

// WebProduct and its meters address what one web call costs.
const (
	WebProduct  = "web"
	SearchMeter = "search"
	FetchMeter  = "fetch"
)

// The compiled floors, in micro-USD, in the unit each is charged by.
//
// They are constants for the reason [RuntimeHourMicros] is: money is not a float,
// and a price parsed on every debit rounds differently than one written down once.
// A vCPU and a GiB of memory for one hour is 50_400 + 16_200 = 66_600 µ$, which is
// what a session's computer costs for an hour of running and nothing at all for an
// hour of waiting.
const (
	VCPUHourMicros   int64 = 50_400 // $0.0504 per vCPU-hour — $0.000014 a second
	MemoryHourMicros int64 = 16_200 // $0.0162 per GiB-hour  — $0.0000045 a GiB-second
	SearchCallMicros int64 = 2_158  // $0.002158 one query, up to ten results
	FetchCallMicros  int64 = 1_079  // $0.001079 one page read as text
)

// Component is one of the four things a bill is made of.
type Component struct {
	// Name is the component's name on the spend breakdown.
	Name string `json:"name"`
	// Title is how it reads on a price list.
	Title string `json:"title"`
	// Basis is what it is metered by, in the fewest words that are true.
	Basis string `json:"basis"`
	// Quoted says whether a call is priced BEFORE it runs. A quoted component can
	// refuse a call that would breach a budget; a measured one is booked from what
	// it used and is bounded by the headroom under the per-task cap instead.
	Quoted bool `json:"quoted"`
	// Tiers are the disjoint tiers this component meters in, where it has them.
	Tiers []string `json:"tiers,omitempty"`
	// Note is what the component means, in one sentence.
	Note string `json:"note"`
}

// Speed is one completion speed: how soon an answer comes, and therefore what
// it costs.
type Speed struct {
	// Name is what a request asks for.
	Name string `json:"name"`
	// Rank orders the windows by price, 1 being the dearest.
	Rank int `json:"rank"`
	// Note is what asking for this window buys.
	Note string `json:"note"`
}

// Rate is one priced line of the card: what one unit costs, and what a unit is.
type Rate struct {
	// Name addresses the rate.
	Name string `json:"name"`
	// Title is how the line reads on a price list.
	Title string `json:"title"`
	// Component is which of the four this line bills under.
	Component string `json:"component"`
	// Micros is the price of ONE unit, in micro-USD. It is the number the ledger
	// books, resolved through the same authority the metering path reads.
	Micros int64 `json:"rate_micro_usd"`
	// Per names one unit, so a reader never has to guess what the number is per.
	Per string `json:"per"`
	// Note is what the line covers.
	Note string `json:"note"`
}

// Card is the whole rate card.
type Card struct {
	// Unit is the unit every amount on this card is stated in.
	Unit string `json:"unit"`
	// Components are the four things a bill is made of.
	Components []Component `json:"components"`
	// Speeds are the completion speeds, dearest first.
	Speeds []Speed `json:"windows"`
	// Rates are the priced lines, each in micro-USD per its own unit.
	Rates []Rate `json:"rates"`
}

// Components answers the four things a bill is made of. It takes no context
// because what a bill is made of is not a price and nobody publishes it.
func Components() []Component {
	return []Component{{
		Name:   ComponentModel,
		Title:  "Model inference",
		Basis:  "per token, five tiers",
		Quoted: true,
		Tiers:  []string{TierIn, TierCacheRead, TierCacheWrite, TierOut, TierReasoning},
		Note: "Input, cached reads, cache writes, output and reasoning are metered as five " +
			"disjoint tiers, at the tariff for the completion window the request asked for.",
	}, {
		Name:   ComponentComputer,
		Title:  "Computer",
		Basis:  "per second, while running",
		Quoted: true,
		Note: "vCPU and memory on what the machine was given. Nothing to create one, nothing " +
			"while it sleeps, and the boot disk is inside the allowance — so a computer " +
			"waiting between turns meters nothing.",
	}, {
		Name:   ComponentWeb,
		Title:  "Web tools",
		Basis:  "per call",
		Quoted: false,
		Note: "Priced from what the provider charged, so a web call is booked after it runs. " +
			"A fetch the domain policy refuses is stopped before the request and costs nothing.",
	}, {
		Name:   ComponentMedia,
		Title:  "Media generation",
		Basis:  "what the job cost",
		Quoted: false,
		Note: "Billed once per finished job at its real cost; a render cannot be quoted before " +
			"it runs, so it is admitted against the headroom under the agent's per-task cap " +
			"instead. A job that fails is not billed.",
	}}
}

// Speeds answers the three completion speeds, dearest first.
func Speeds() []Speed {
	return []Speed{
		{Name: SpeedImmediate, Rank: 1, Note: "Answers now, at the highest tariff."},
		{Name: SpeedPriority, Rank: 2, Note: "Answers soon, at a lower one."},
		{Name: SpeedLoose, Rank: 3, Note: "Answers eventually, at the lowest."},
	}
}

// Rates answers the eight priced lines, each resolved through the authority that
// owns it.
//
// THE FOUR ASKS ARE CONCURRENT because they are four independent questions and
// asking them in a row would multiply one unwell authority's timeout by four on a
// public price list. Each falls back to its own compiled floor, so a failed ask
// costs a stale price and never an error — the posture [RateNano] already takes.
func Rates(ctx context.Context) []Rate {
	var vcpu, memory, search, fetch int64
	var wg sync.WaitGroup
	ask := func(into *int64, product, meter string, floor int64) {
		defer wg.Done()
		*into = RateMicros(ctx, product, meter, floor)
	}
	wg.Add(4)
	go ask(&vcpu, ComputerProduct, VCPUMeter, VCPUHourMicros)
	go ask(&memory, ComputerProduct, MemoryMeter, MemoryHourMicros)
	go ask(&search, WebProduct, SearchMeter, SearchCallMicros)
	go ask(&fetch, WebProduct, FetchMeter, FetchCallMicros)
	wg.Wait()

	return []Rate{{
		Name: "computer.vcpu", Title: "Computer · vCPU", Component: ComponentComputer,
		Micros: vcpu, Per: "vcpu-hour",
		Note: "Charged by the second, while running.",
	}, {
		Name: "computer.memory", Title: "Computer · memory", Component: ComponentComputer,
		Micros: memory, Per: "gib-hour",
		Note: "Charged by the second, on the memory the machine was given.",
	}, {
		Name: "computer.paused", Title: "Computer · paused", Component: ComponentComputer,
		Micros: 0, Per: "hour",
		Note: "No vCPU, no memory, no storage.",
	}, {
		Name: "computer.created", Title: "Computer · created", Component: ComponentComputer,
		Micros: 0, Per: "computer",
		Note: "There is no creation fee.",
	}, {
		Name: "web_search", Title: "web_search", Component: ComponentWeb,
		Micros: search, Per: "call",
		Note: "One query, up to ten results.",
	}, {
		Name: "web_fetch", Title: "web_fetch", Component: ComponentWeb,
		Micros: fetch, Per: "call",
		Note: "One page read as text.",
	}, {
		Name: "interfaces", Title: "MCP, SDK and CLI", Component: ComponentModel,
		Micros: 0, Per: "call",
		Note: "The interfaces are not metered.",
	}, {
		Name: "seats", Title: "Seats", Component: ComponentModel,
		Micros: 0, Per: "seat",
		Note: "No per-seat fee, however many people use the organization.",
	}}
}

// Card answers the whole rate card, priced now.
func Current(ctx context.Context) Card {
	return Card{
		Unit:       "micro_usd",
		Components: Components(),
		Speeds:     Speeds(),
		Rates:      Rates(ctx),
	}
}
