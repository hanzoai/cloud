package company

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// WHAT A FORMATION COSTS, ITEMISED.
//
// The fee used to be one number — $999, quoted in an error string — and that is
// only true for the half of the bill that is OURS. Forming a company also incurs
// the state's filing fee, and a customer who wants an EIN expedited or an agent
// of record on file incurs those too. A single figure cannot say which of those
// a payer is agreeing to, and a quote that cannot be itemised is a quote nobody
// can check.
//
// STATE FEES ARE NOT WRITTEN DOWN HERE, DELIBERATELY. Delaware's and Wyoming's
// filing fees are published numbers set by those states and revised by them —
// a figure compiled in here would be right until the day it silently was not,
// and the way that surfaces is a customer charged the wrong amount for a filing
// we already submitted. They are configured per deployment, and a jurisdiction
// with no configured fee REFUSES to quote rather than guessing. That is the same
// direction every other gate in this repo fails in.
//
// Ours are different in kind: the service fee and the agent fee are prices we
// set, so they have defaults and live in code.

// Charge is one line of a quote.
type Charge struct {
	// Code names the line so a caller can branch on it without reading prose.
	Code string `json:"code"`
	// Label is what the payer sees on the invoice.
	Label string `json:"label"`
	// AmountCents is what this line costs.
	AmountCents int64 `json:"amountCents"`
	// PassThrough marks money we collect and remit rather than keep — the state's
	// fee is not our revenue, and a quote that hides that is a quote that reads
	// as a bigger margin than it is.
	PassThrough bool `json:"passThrough,omitempty"`
	// Recurring marks a line that repeats. An agent of record is billed every
	// year for as long as the entity stands, and a payer agreeing to a one-time
	// total is not agreeing to that.
	Recurring string `json:"recurring,omitempty"`
}

// Tariff is the whole bill for one formation, itemised.
//
// It is NOT called Quote: marketing already publishes a Quote, a promotional
// one, and two shapes under one name means every generated SDK binds whichever
// it read last. A tariff is precisely this — a published schedule of charges.
type Tariff struct {
	// Structure is the entity this prices: c-corp, llc or dao-llc.
	Structure Structure `json:"structure"`
	// Jurisdiction is the state of formation the filing fee belongs to.
	Jurisdiction Jurisdiction `json:"jurisdiction"`
	// Lines are the charges, in the order a reader should see them.
	Lines []Charge `json:"lines"`
	// DueNowCents is what is charged to begin: every non-recurring line.
	DueNowCents int64 `json:"dueNowCents"`
	// RecurringCents is what repeats, and Recurring says how often.
	RecurringCents int64 `json:"recurringCents,omitempty"`
	// Recurring is how often RecurringCents repeats — "yearly" for an agent of
	// record. Empty when nothing on this quote recurs.
	Recurring string `json:"recurring,omitempty"`
	// Currency is the ISO code every amount on this quote is denominated in.
	Currency string `json:"currency"`
}

// Options are the choices a payer makes that change the bill.
type Options struct {
	// ExpeditedEIN prioritises the EIN. It costs money and it is only meaningful
	// for a founder who has no SSN — see the EIN flow.
	ExpeditedEIN bool `json:"expeditedEin,omitempty"`
	// AgentOfRecord puts us on file as the entity's agent, which is a yearly
	// obligation rather than a one-time act.
	AgentOfRecord bool `json:"agentOfRecord,omitempty"`
}

// envCents reads an ops-configured amount. Absent and malformed are the same
// answer — not configured — because a fee that parses as zero because someone
// typed "$149" would file a company for free and look deliberate.
func envCents(key string) (int64, bool) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// stateFeeCents is the jurisdiction's own filing fee, in cents, or not-configured.
// The key carries the jurisdiction so one deployment can serve both without the
// two ever being confused for each other.
func stateFeeCents(j Jurisdiction) (int64, bool) {
	return envCents("CLOUD_COMPANY_STATE_FEE_CENTS_" + strings.ToUpper(string(j)))
}

// agentFeeCents is what we charge to be the agent of record, per year.
func agentFeeCents() int64 {
	if n, ok := envCents("CLOUD_COMPANY_AGENT_FEE_CENTS"); ok {
		return n
	}
	return defaultAgentFeeCents
}

// expeditedEINFeeCents is what we charge to prioritise the EIN.
func expeditedEINFeeCents() int64 {
	if n, ok := envCents("CLOUD_COMPANY_EXPEDITED_EIN_FEE_CENTS"); ok {
		return n
	}
	return defaultExpeditedEINFeeCents
}

const (
	// Ours to set, so they have a default.
	defaultAgentFeeCents        int64 = 19900 // $199/yr, agent of record
	defaultExpeditedEINFeeCents int64 = 9900  // $99, prioritised EIN
)

// QuoteFor itemises what forming this entity costs.
//
// It REFUSES when the state's fee is unknown. Quoting only our half would read
// as the whole bill, and the customer would meet the rest of it after we had
// already filed — which is the moment it is least fixable.
func TariffFor(s Structure, j Jurisdiction, o Options) (*Tariff, error) {
	state, ok := stateFeeCents(j)
	if !ok {
		return nil, fmt.Errorf("company: no filing fee configured for %s — set CLOUD_COMPANY_STATE_FEE_CENTS_%s to that state's published fee; refusing to quote a formation whose cost is unknown", j, strings.ToUpper(string(j)))
	}

	q := &Tariff{Structure: s, Jurisdiction: j, Currency: "usd"}
	q.Lines = append(q.Lines,
		Charge{Code: "formation", Label: "Formation service", AmountCents: feeCents()},
		Charge{Code: "state_filing", Label: jurisdictionName(j) + " filing fee", AmountCents: state, PassThrough: true},
	)
	if o.ExpeditedEIN {
		q.Lines = append(q.Lines, Charge{Code: "expedited_ein", Label: "Expedited EIN", AmountCents: expeditedEINFeeCents()})
	}
	if o.AgentOfRecord {
		q.Lines = append(q.Lines, Charge{Code: "agent_of_record", Label: "Agent of record", AmountCents: agentFeeCents(), Recurring: "yearly"})
	}

	for _, l := range q.Lines {
		if l.Recurring != "" {
			q.RecurringCents += l.AmountCents
			q.Recurring = l.Recurring
			continue
		}
		q.DueNowCents += l.AmountCents
	}
	return q, nil
}

// tariffIn is what a caller states to be priced. It is deliberately the same
// vocabulary the formation itself takes, so a quote and the thing it prices
// cannot drift into two spellings of one choice.
type tariffIn struct {
	// Structure is the entity being formed: c-corp, llc or dao-llc.
	Structure Structure `json:"structure"`
	// Jurisdiction is the state of formation.
	Jurisdiction Jurisdiction `json:"jurisdiction"`
	// ExpeditedEIN prioritises the EIN.
	ExpeditedEIN bool `json:"expeditedEin,omitempty"`
	// AgentOfRecord puts us on file as the entity's agent, yearly.
	AgentOfRecord bool `json:"agentOfRecord,omitempty"`
}

// tariff itemises what a formation costs before anyone commits to it.
//
// It answers what is due now and what recurs, as separate figures, and marks the
// state's filing fee as money we collect and remit rather than keep. A caller can
// therefore show a payer the whole bill — which is the point of quoting at all,
// and was impossible while the fee was one number in an error string.
//
// A jurisdiction whose filing fee this deployment has not been told REFUSES,
// naming the setting that fixes it. Quoting our half as though it were the total
// is the one answer that would be worse than no answer.
func (o ops) tariff(_ context.Context, in *tariffIn) (*Tariff, error) {
	if in == nil {
		return nil, fmt.Errorf("company: a quote states a structure and a jurisdiction")
	}
	return TariffFor(in.Structure, in.Jurisdiction, Options{
		ExpeditedEIN:  in.ExpeditedEIN,
		AgentOfRecord: in.AgentOfRecord,
	})
}

// dollars renders cents for a human sentence. It exists so a message can name
// the fee it is refusing over WITHOUT a literal price written into prose, which
// is how "$999" ended up in an error, a comment and a test that all had to be
// edited together the day the price moved.
func dollars(cents int64) string {
	whole := cents / 100
	frac := cents % 100
	if frac == 0 {
		return strconv.FormatInt(whole, 10)
	}
	return fmt.Sprintf("%d.%02d", whole, frac)
}
