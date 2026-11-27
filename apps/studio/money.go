package studio

// money.go — what a run costs, and how it is paid.
//
// A RUN IS PAID FOR UP FRONT. Creating one prices every step (estimate) and
// reserves that much against the org's wallet: the wallet must cover this run's
// estimate plus whatever every other open run of the org still holds, or the
// create answers 402 and nothing runs. The reservation is the run itself — its
// estimate less what it has been charged, for as long as it is queued or running —
// so it survives a restart with the run, and closing the run releases it.
//
// A STEP IS DEBITED WHEN IT FINISHES, ONCE. Each part's cost is debited under a ref
// derived from the run, the step, the item and the attempt (studio_<run>_<step>_<item>),
// which the ledger is exactly-once on, so a part retried after a lost reply moves
// the money once. The run's charged is the sum of its parts; when it closes, that
// is what it cost. Commerce has no credit verb, so nothing is taken that a step did
// not spend: the estimate is held, never debited.
//
// WHO PAYS is resolved while the request is real (cloud.PayerOf) and stored on the
// run, because a step runs long after the request that started it.
//
// Prices are rows in commerce's meter authority (product studio, one meter per
// kind of work), with the floors in ops as what is charged when no row is
// published. A chat step is debited by the model meter itself (deps.AI), so Studio
// reserves for it and never debits it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hanzoai/account"
	"github.com/hanzoai/orm/tenant"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
)

// payerRec is who pays for a run, as stored on it.
type payerRec struct {
	Wallet    string `json:"wallet"`
	Project   string `json:"project,omitempty"`
	Validated bool   `json:"validated,omitempty"`
	Actor     string `json:"actor,omitempty"`
}

func recordPayer(p cloud.Payer) payerRec {
	return payerRec{Wallet: p.Wallet.String(), Project: p.Project, Validated: p.Validated, Actor: p.Actor}
}

// payerOf is the payer a run stored.
func payerOf(r runRow) payerRec {
	var p payerRec
	_ = json.Unmarshal([]byte(r.Payer), &p)
	return p
}

func (p payerRec) wallet() account.Account { return account.Parse(p.Wallet) }

// rate is one unit of an op's work, in cents, from the meter authority.
func (s *state) rate(ctx context.Context, name string) int64 {
	o := known[name]
	if o.meter == "" {
		return 0
	}
	return cloud.RateCents(ctx, "studio", o.meter, o.floor)
}

// estimate prices a graph step by step. A reference the estimate cannot read yet
// — a count a model will decide — is priced at the op's default; a fan-out over an
// input array is priced per item, and one over a step's output at maxEstimateItems.
func (s *state) estimate(ctx context.Context, g Graph, input map[string]any) Estimate {
	out := Estimate{Steps: make([]StepPrice, 0, len(g.Steps))}
	for _, st := range g.Steps {
		items := 1
		if st.Each != "" {
			items = maxEstimateItems
			if m := whole.FindStringSubmatch(st.Each); m != nil {
				if v, err := (scope{input: input}).lookup(m[1]); err == nil {
					if arr, ok := v.([]any); ok {
						items = max(len(arr), 1)
					}
				}
			}
		}
		per := s.unitCents(ctx, st, input)
		c := per * int64(items)
		out.Steps = append(out.Steps, StepPrice{ID: st.ID, Op: st.Op, Cents: c})
		out.Cents += c
	}
	return out
}

// maxEstimateItems is what an estimate assumes a fan-out over a model's output
// makes: a storyboard's shots, a deck's slides.
const maxEstimateItems = 8

// unitCents is one part of a step, priced before it runs.
func (s *state) unitCents(ctx context.Context, st Spec, input map[string]any) int64 {
	switch st.Op {
	case "chat":
		limit := num(st.With, "max_tokens", 4096)
		prompt, _ := st.With["prompt"].(string)
		system, _ := st.With["system"].(string)
		model := str(st.With, "model", models[st.Op])
		return cloud.MicrosToGateCents(cloud.HoldMicros(ctx, model, cloud.EstTokens(system, prompt), int(limit)))
	case "image.generate", "image.edit", "frame.generate", "frame.edit":
		return s.rate(ctx, st.Op) * int64(min(max(num(st.With, "n", 1), 1), 4))
	case "voice.speak":
		text, _ := st.With["text"].(string)
		if m := whole.FindStringSubmatch(text); m != nil {
			if v, err := (scope{input: input}).lookup(m[1]); err == nil {
				text = fmt.Sprint(v)
			} else {
				text = strings.Repeat(" ", 2000)
			}
		}
		return s.rate(ctx, st.Op) * int64(max((len([]rune(text))+999)/1000, 1))
	}
	return s.rate(ctx, st.Op)
}

// held is what the org's open runs still hold: each one's estimate less what it
// has been charged. db is the transaction the reservation is weighed in.
func held(ctx context.Context, db *tenant.DB) (int64, error) {
	open, err := db.Select[runRow]("runs", "estimate", "charged").Where(tenant.In("status", queued, running)).All(ctx)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, r := range open {
		total += max(r.Estimate-r.Charged, 0)
	}
	return total, nil
}

// reserve weighs cents, with everything the org already holds, against the
// wallet, and runs commit in the same transaction — commit is what makes it held
// (it opens the run, or re-opens it on a retry). A wallet that cannot cover it is
// refused as the money plane refuses: 402.
//
// ONE RESERVATION AT A TIME PER ORG, ACROSS EVERY REPLICA. The sum of what is held
// and the run that adds to it are one serializable transaction (tenant.DB.Tx): two
// creates that each read the other's reservation as absent cannot both commit, wherever
// they run, and the one the server aborts is run again on what the other held. The
// weighing is a read of the wallet and may run more than once; commit touches only tx.
func (s *state) reserve(ctx context.Context, p cloud.Payer, cents int64, commit func(tx *tenant.DB) error) error {
	return s.db.Tx(ctx, func(tx *tenant.DB) error {
		if cents > 0 {
			have, err := held(ctx, tx)
			if err != nil {
				return err
			}
			if err := s.afford(ctx, p, have+cents); err != nil {
				return cloud.Denied(err)
			}
		}
		return commit(tx)
	})
}

// debit charges one part's cost, once.
func (s *state) debit(ctx context.Context, r runRow, p Part, cents int64) error {
	if cents <= 0 || known[p.Op].meter == "" {
		return nil
	}
	pay := payerOf(r)
	w := pay.wallet()
	if w.Zero() {
		return nil
	}
	err := s.bill.Debit(ctx, w, meter.Usage{
		AmountCents: cents,
		Model:       "studio/" + p.Op,
		Project:     pay.Project,
		Actor:       pay.Actor,
		RequestID:   p.Run,
		Ref:         fmt.Sprintf("studio_%s_%s_%d", p.Run, p.Step, p.Item),
		Units:       1,
	})
	if errors.Is(err, cloud.ErrNoMeter) {
		// A deployment that meters nothing charges nothing.
		return nil
	}
	return err
}
