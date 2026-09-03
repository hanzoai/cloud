// Package standing prices and tracks what it costs to KEEP a company, as
// distinct from what it cost to form one.
//
// Forming is an act that ends. Standing is an obligation that does not: a state
// wants its annual report, a franchise tax falls due, and somebody has to be the
// agent of record for as long as the entity exists. Those are different questions
// with different answers and different money, and folding them into the formation
// price is how a customer agrees to a number that is not what they will pay
// again next year.
//
// It is also the number that actually decides where to incorporate. Delaware is
// cheaper to FORM than Wyoming for a corporation and dearer to KEEP, and a
// founder shown only the formation fee is being shown the half that reverses.
package standing

import (
	"context"
	"fmt"
	"github.com/hanzoai/cloud/internal/environ"
	"strconv"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// Jurisdiction is the state the entity stands in. Same spelling company uses —
// one vocabulary, so a caller never has to translate between the two surfaces.
type Jurisdiction string

// Structure is the entity kind, spelled as company spells it.
type Structure string

const (
	JurisdictionDE Jurisdiction = "DE"
	JurisdictionWY Jurisdiction = "WY"

	StructureCCorp  Structure = "c-corp"
	StructureLLC    Structure = "llc"
	StructureDAOLLC Structure = "dao-llc"
)

// Obligation is one recurring thing an entity owes to stay in good standing.
type Obligation struct {
	// Code names the obligation so a caller can branch without reading prose.
	Code string `json:"code"`
	// Label is what the payer sees.
	Label string `json:"label"`
	// AmountCents is what it costs each period.
	AmountCents int64 `json:"amountCents"`
	// Every is how often it falls due — "yearly" for every obligation known here.
	Every string `json:"every"`
	// PassThrough marks money we collect and remit to the state rather than keep.
	PassThrough bool `json:"passThrough,omitempty"`
	// Source names the authority that publishes a pass-through amount, so the
	// figure can be checked without first working out who would know.
	Source string `json:"source,omitempty"`
	// AsOf is when that amount was last checked against its source, RFC 3339 date.
	AsOf string `json:"asOf,omitempty"`
	// Stale reports that AsOf is older than the review window. It tells; it does
	// not block.
	Stale bool `json:"stale,omitempty"`
	// Minimum marks a floor rather than a fixed price — a franchise tax that
	// scales with shares or assets is quoted at its minimum, and an entity past
	// the threshold owes more. Saying so is the difference between a quote and a
	// number someone later disputes.
	Minimum bool `json:"minimum,omitempty"`
}

// Upkeep is what keeping one entity costs for one period, itemised.
//
// It is NOT called Cost: admin already publishes a Cost, and two shapes under
// one name means every generated SDK binds whichever it read last. Upkeep is
// the plainer word anyway — the cost of keeping a thing.
type Upkeep struct {
	// Structure is the entity this prices.
	Structure Structure `json:"structure"`
	// Jurisdiction is the state whose obligations these are.
	Jurisdiction Jurisdiction `json:"jurisdiction"`
	// Obligations are the recurring charges, in the order a reader should see them.
	Obligations []Obligation `json:"obligations"`
	// YearlyCents is what the entity owes every year, all obligations summed.
	YearlyCents int64 `json:"yearlyCents"`
	// AtLeast reports that some obligation is a minimum, so YearlyCents is a floor
	// rather than a final figure.
	AtLeast bool `json:"atLeast,omitempty"`
	// Currency is the ISO code every amount here is denominated in.
	Currency string `json:"currency"`
}

// stateObligation is a jurisdiction's own recurring charge, with its receipt.
//
// Same rule as company's filing fees: the figure ships so the surface works, and
// it carries WHERE it came from and WHEN it was checked so a stale one is
// findable rather than silently wrong.
type stateObligation struct {
	code, label, source, asOf string
	cents                     int64
	minimum                   bool
	structure                 Structure // empty applies to every structure
}

var stateObligations = map[Jurisdiction][]stateObligation{
	JurisdictionDE: {
		{structure: StructureLLC, code: "franchise_tax", label: "Delaware LLC franchise tax", cents: 30000,
			source: "Delaware Division of Corporations", asOf: "2026-08-21"},
		{structure: StructureDAOLLC, code: "franchise_tax", label: "Delaware LLC franchise tax", cents: 30000,
			source: "Delaware Division of Corporations", asOf: "2026-08-21"},
		{structure: StructureCCorp, code: "franchise_tax", label: "Delaware corporate franchise tax", cents: 17500,
			source: "Delaware Division of Corporations", asOf: "2026-08-21", minimum: true},
		{structure: StructureCCorp, code: "annual_report", label: "Delaware annual report", cents: 5000,
			source: "Delaware Division of Corporations", asOf: "2026-08-21"},
	},
	JurisdictionWY: {
		{code: "annual_report", label: "Wyoming annual report", cents: 6000,
			source: "Wyoming Secretary of State", asOf: "2026-08-21", minimum: true},
	},
}

// reviewWindow is how long a published figure is trusted before it wants
// re-checking. States revise annually at most.
const reviewWindow = 365 * 24 * time.Hour

func stale(asOf string, now time.Time) bool {
	if asOf == "" {
		return false
	}
	t, err := time.Parse("2006-01-02", asOf)
	if err != nil {
		return true // an unparseable date is not a date anyone checked
	}
	return now.Sub(t) > reviewWindow
}

// agentFeeCents is what we charge to be the agent of record, per year. Ours to
// set, so it has a default; ops may move it.
func agentFeeCents() int64 {
	if v := environ.Or("CLOUD_STANDING_AGENT_FEE_CENTS", ""); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			return n
		}
	}
	return defaultAgentFeeCents
}

const defaultAgentFeeCents int64 = 19900 // $199/yr

// CostOf itemises what keeping this entity costs each year.
//
// It REFUSES a jurisdiction whose obligations are unknown rather than answering
// zero — an entity quoted at nothing per year reads as free to keep, which is the
// most expensive wrong answer on this surface.
func UpkeepOf(s Structure, j Jurisdiction, agentOfRecord bool) (*Upkeep, error) {
	rows, ok := stateObligations[j]
	if !ok {
		return nil, fmt.Errorf("standing: no recurring obligations known for %s — refusing to report that an entity there costs nothing to keep", j)
	}
	now := time.Now()
	c := &Upkeep{Structure: s, Jurisdiction: j, Currency: "usd"}

	for _, r := range rows {
		if r.structure != "" && r.structure != s {
			continue
		}
		c.Obligations = append(c.Obligations, Obligation{
			Code: r.code, Label: r.label, AmountCents: r.cents, Every: "yearly",
			PassThrough: true, Source: r.source, AsOf: r.asOf,
			Stale: stale(r.asOf, now), Minimum: r.minimum,
		})
	}
	if agentOfRecord {
		c.Obligations = append(c.Obligations, Obligation{
			Code: "agent_of_record", Label: "Agent of record", AmountCents: agentFeeCents(), Every: "yearly",
		})
	}
	for _, o := range c.Obligations {
		c.YearlyCents += o.AmountCents
		if o.Minimum {
			c.AtLeast = true
		}
	}
	return c, nil
}

// upkeepIn is what a caller states to be told the yearly upkeep.
type upkeepIn struct {
	// Structure is the entity kind: c-corp, llc or dao-llc.
	Structure Structure `json:"structure"`
	// Jurisdiction is the state the entity stands in.
	Jurisdiction Jurisdiction `json:"jurisdiction"`
	// AgentOfRecord includes our agent fee, which most entities owe someone.
	AgentOfRecord bool `json:"agentOfRecord,omitempty"`
}

type ops struct{}

// upkeep reports what keeping this entity costs every year, itemised.
//
// This is the figure that decides where to incorporate, and the one a formation
// price cannot show: Delaware is cheaper to form than Wyoming for a corporation
// and dearer to keep, so a founder shown only the formation fee is shown the half
// that reverses. Each state line carries the authority that publishes it and the
// date it was checked, and a franchise tax that scales is marked a minimum rather
// than quoted as final.
func (ops) upkeep(_ context.Context, in *upkeepIn) (*Upkeep, error) {
	if in == nil {
		return nil, fmt.Errorf("standing: an upkeep states a structure and a jurisdiction")
	}
	return UpkeepOf(in.Structure, in.Jurisdiction, in.AgentOfRecord)
}

// Mount registers the standing surface on app per HIP-0106.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("standing.Use:  nil app")
	}
	log := luxlog.Default().New("subsystem", "standing")
	o := ops{}
	// Internal audience by default — the fleet publishes nothing to customers it
	// was not explicitly told to. Promoting this to the customer contract is a
	// separate, deliberate act.
	zip.Post(cloud.ZipApp(app), "/v1/standing/upkeep", o.upkeep)
	log.Info("standing surface mounted", "prefix", "/v1/standing", "brand", cloud.Brand())
	return nil
}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc
