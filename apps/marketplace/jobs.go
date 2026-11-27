package marketplace

// jobs.go — two-sided work: one org hires another, through a listing or by a
// direct offer, and pays for it once, when the work is released.
//
//	quoted ──pay──▶ funding ──held──▶ open ──accept──▶ accepted ──deliver──▶ delivered ──release──▶ released
//	                                    │                  │                    │   (buyer, or the review
//	                             cancel │ decline   refund │ dispute    dispute │    window passing)
//	                                    ▼                  ▼                    ▼
//	                         cancelled / declined      refunded ◀──refund── disputed ──release──▶ released
//	                                                       ▲  (seller; the buyer   │   (buyer; or the arbiter)
//	                                                       │   if the deadline     │
//	                                                       └──passes; the arbiter; └──the arbiter's time lapsing
//
// A quote, and a funding the rail never answered, lapse after an hour; whatever
// the funding's payment set aside is given back first.
//
// THE MONEY. A job is paid over x402 from the buyer's balance into the seller's
// payout wallet, ONCE, at release. When the job opens the buyer signs an x402
// authorization for exactly its terms; the rail verifies it, checks it stays
// acceptable until a dispute could last be ruled on, holds its nonce for this job
// alone, and sets its amount aside in the buyer's wallet (x402_authorize) — so the
// payment the seller works for can be spent on nothing else, and is there when the
// work is released. A wallet that cannot cover it opens no job. Release settles it
// from what was set aside (x402_settle). Every other ending gives the
// authorization up (x402_void), and the amount returns to the buyer's wallet. The
// money stays in the buyer's own books until it is paid: Hanzo never holds a job's
// funds, and the rail states the one economic event a settled job makes, with the
// category the parties agreed.
//
// ONE ATTEMPT, ONE JOB. A buyer names each hire with an attempt of its own, sent
// with every request the hire makes. The same attempt answers the same job: its
// quote until it is paid, the job once it opened. So a buyer whose answer was
// lost sends the same request again, and whatever happened the first time — a
// quote made, a payment held, the job opened — is what it is answered, never a
// second job holding a second amount.
//
// AN ENDING IS DECIDED BEFORE THE MONEY MOVES. A job that ends unpaid is given
// its ending first (Job.Ending) and takes no other step from then on — an accept
// or a delivery racing a cancel loses to it — and reaches it once the rail has
// returned the amount; a rail that does not answer leaves it ending, and the clock
// finishes it. A payment presented for a quote is kept on the quote before the
// rail is asked to hold it, so a hold whose answer was lost is given back when
// another payment is presented for the quote, or the quote lapses.
//
// CLEARANCE FIRST, AND AT EVERY STEP THE MONEY TAKES. No job is quoted, funded or
// released without principal's decision, asked then, that the buyer may pay the
// seller this amount; the latest is kept on the job. The quote's is the payment's
// first clearance, and funding and release ask again of it, which spends none of
// the buyer's own hourly bound on clearances. A blocked payment is refused, and
// one that waits on the seller's tax form is refused until the seller furnishes
// it — a seller blocked after the job opened is not paid by it. A delivered job
// whose release no longer clears goes to the arbiter when its review window
// closes; the clock pays it the moment it clears again, and refunds it only if it
// still does not clear when the arbiter's time runs out.
//
// ON CHAIN a job is the Lux escrow's (chain.go): the buyer funds the contract,
// and the contract, not this store, is where the money sits.

import (
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	principalpeer "github.com/hanzoai/cloud/client/principal"
	walletpeer "github.com/hanzoai/cloud/client/wallet"
	x402peer "github.com/hanzoai/cloud/client/x402"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/principal"
	protocol "github.com/hanzoai/commerce/payment/x402"
	"github.com/zap-proto/zip"
)

// The states of a job.
const (
	// StatusQuoted is a job whose terms are cleared and whose payment is not yet
	// signed. Nobody but its buyer sees it, and it lapses after quoteTTL.
	StatusQuoted = "quoted"
	// StatusFunding is a quote whose payment was presented: the rail is asked to
	// set its amount aside, and the job opens when it has. A payment refused, or
	// whose answer was lost, stays on it until the same one is sent again, another
	// is presented in its place, or the quote lapses and gives it back. Nobody but
	// its buyer sees it.
	StatusFunding = "funding"
	// StatusOpen is funded — its amount set aside in the buyer's wallet — and
	// waiting for the seller.
	StatusOpen = "open"
	// StatusAccepted is being worked on.
	StatusAccepted = "accepted"
	// StatusDelivered is waiting for the buyer to release, until the review window
	// closes and it releases itself.
	StatusDelivered = "delivered"
	// StatusReleased paid the seller.
	StatusReleased = "released"
	// StatusDisputed waits for a party to give way or the arbiter to rule.
	StatusDisputed = "disputed"
	// StatusDeclined was turned down by the seller before any work.
	StatusDeclined = "declined"
	// StatusCancelled was taken back by the buyer, or reached its deadline, before
	// the seller accepted it.
	StatusCancelled = "cancelled"
	// StatusRefunded ended after acceptance without paying the seller.
	StatusRefunded = "refunded"
)

// unopened is every state of a job that has not opened: its buyer's alone.
var unopened = []string{StatusQuoted, StatusFunding}

// The rails a job settles on.
const (
	RailX402  = client.RailX402  // the buyer's balance, settled at release
	RailChain = client.RailChain // the Lux escrow contract
)

const (
	// defaultReview is how long a buyer has after delivery to release or dispute
	// before the job releases itself.
	defaultReview = 3 * 24 * time.Hour
	// maxReview is the Lux escrow's MAX_REVIEW.
	maxReview = 30 * 24 * time.Hour
	// disputeTimeout is how long a dispute may wait on the arbiter before it
	// lapses to a refund — within the escrow's MIN_TIMEOUT..MAX_TIMEOUT.
	disputeTimeout = 14 * 24 * time.Hour
	// clockRoom is how long a job's payment must stay valid after the arbiter's
	// time runs out: the clock that makes the last payment runs a day behind at
	// worst, its longest backoff.
	clockRoom = 24 * time.Hour
	// quoteTTL is how long a quote waits for its payment.
	quoteTTL = time.Hour
	// minWork is the least time a job may leave the seller before its deadline.
	minWork = time.Hour
	// maxJobs bounds one list.
	maxJobs = 500
	// payTimeout bounds one crossing to the rail.
	payTimeout = 60 * time.Second
)

// actHire is the metered act of opening a job. Billing's catalog prices it at
// nothing: the meter is wired around it, and the price is the catalog's to set
// when it has one.
const actHire = "hire"

// Job is one piece of two-sided work, as its parties read it.
type Job struct {
	// ID is the job, "job_"-prefixed.
	ID string `json:"id"`
	// Listing is the listing it was hired through; empty for a direct offer.
	Listing string `json:"listing"`
	// Title names the work: the listing's title, or the offer's own.
	Title string `json:"title"`
	// Attempt is the buyer's own key for the hire that made the job, when it sent
	// one.
	Attempt string `json:"attempt,omitempty"`
	// BuyerOrg is the org that pays.
	BuyerOrg string `json:"buyerOrg"`
	// SellerOrg is the org that does the work and is paid.
	SellerOrg string `json:"sellerOrg"`
	// Amount is what the job pays, exact USD.
	Amount money.Amount `json:"amount"`
	// Currency labels Amount: USD.
	Currency string `json:"currency"`
	// Category is what the payment is for — service, goods, transfer or royalty —
	// as the buyer declared it and the seller accepted it. It is what the
	// economic event says.
	Category string `json:"category"`
	// Status is where the job stands: quoted or funding, which only its buyer
	// sees, then open, accepted, delivered, released, disputed, declined,
	// cancelled or refunded.
	Status string `json:"status"`
	// Ending is the unpaid ending the job was given — declined, cancelled or
	// refunded — while the rail returns the amount it set aside. It takes no other
	// step meanwhile, and Status becomes Ending once the money is back.
	Ending string `json:"ending,omitempty"`
	// Brief is what the buyer asked for.
	Brief string `json:"brief"`
	// Performed is where the work is performed, ISO 3166-1 alpha-2, when said.
	Performed string `json:"performed,omitempty"`
	// Deadline is when delivery must land by, unix seconds.
	Deadline int64 `json:"deadline,omitempty"`
	// Review is how many seconds after delivery the buyer has to release or
	// dispute before the job releases itself.
	Review int64 `json:"review"`
	// Clearance is principal's latest decision that the buyer may pay the seller
	// this amount — asked when the job was quoted, again when it was funded, and
	// again when it was released: GET /v1/principal/clearance/{id}.
	Clearance string `json:"clearance"`
	// Delivery is what the seller delivered, once it has.
	Delivery *Delivery `json:"delivery,omitempty"`
	// Dispute is why the job was stopped, once it was.
	Dispute *Dispute `json:"dispute,omitempty"`
	// Escrow is how the job is paid.
	Escrow Escrow `json:"escrow"`
	// History is every step the job took, oldest first.
	History []Step `json:"history"`
	// CreatedAt is when the job was quoted, unix seconds.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is when it last moved, unix seconds.
	UpdatedAt int64 `json:"updatedAt"`
}

// Delivery is what a seller delivered.
type Delivery struct {
	// Note says what was delivered.
	Note string `json:"note"`
	// URL is where it is, https. It is a KMS value (the row holds its path), so only the
	// reads that serve a job to a party fill it.
	URL string `json:"url,omitempty"`
	// Hash commits to what was delivered: the seller's own 32-byte hash, or the
	// SHA-256 of the note and URL. It is the escrow's delivery hash on chain.
	Hash string `json:"hash"`
	// At is when, unix seconds.
	At int64 `json:"at"`

	// urlRef is the path the row holds in place of URL.
	urlRef string
}

// Dispute is why a job was stopped.
type Dispute struct {
	// Reason is the disputing party's words.
	Reason string `json:"reason"`
	// By is the org that disputed.
	By string `json:"by"`
	// At is when, unix seconds.
	At int64 `json:"at"`
	// Contested is the clock's own dispute: the payment stopped clearing when the
	// review window closed. A party's dispute never carries it, whatever the party
	// is named.
	Contested bool `json:"contested,omitempty"`
}

// Escrow is how a job is paid, and whether it was.
type Escrow struct {
	// Rail is x402 — the buyer's balance, settled at release — or chain, the Lux
	// escrow contract.
	Rail string `json:"rail"`
	// Network is the CAIP-2 network the payment is signed for.
	Network string `json:"network"`
	// Contract is the contract the money moves through: the token the x402
	// authorization transfers, or the escrow on chain.
	Contract string `json:"contract"`
	// PayTo is the seller's payout address.
	PayTo string `json:"payTo,omitempty"`
	// TxHash is the transaction that paid the seller, once one did: the chain's
	// hash, or the x402 settlement id on the ledger.
	TxHash string `json:"txHash,omitempty"`
}

// Step is one step of a job.
type Step struct {
	// Status is the state the job entered.
	Status string `json:"status"`
	// At is when, unix seconds.
	At int64 `json:"at"`
	// By is the org whose act it was, or "marketplace" for a clock that ran out.
	By string `json:"by"`
}

// bySweep names a step no party took: a review window or a deadline passing.
const bySweep = "marketplace"

// ── hire ──────────────────────────────────────────────────────────────────────

// hireIn opens a job.
type hireIn struct {
	// Listing is the public listing the job is hired through. Empty for a direct
	// offer, which names Seller instead.
	Listing string `json:"listing"`
	// Seller is the org offered the job directly; only without a listing.
	Seller string `json:"seller"`
	// Title names a direct offer; a listing's job takes the listing's title.
	Title string `json:"title"`
	// Brief is what the buyer asks for, and what delivered looks like. Required,
	// at most 4096 characters.
	Brief string `json:"brief"`
	// Amount is what the job pays, U.S. dollars to the cent: "250.00".
	Amount string `json:"amount"`
	// Category is what the payment is for: service (the default), goods,
	// transfer or royalty.
	Category string `json:"category"`
	// Deadline is when delivery must land by, unix seconds. Without one, it is
	// the latest the payment leaves room for: its expiry, less the review window,
	// fourteen days for a ruling on a dispute, and a day for the clock.
	Deadline int64 `json:"deadline"`
	// Review is how many seconds the buyer has after delivery to release or
	// dispute: three days by default, thirty at most.
	Review int64 `json:"review"`
	// Performed is where the work is performed, ISO 3166-1 alpha-2. It decides
	// whether a foreign seller's pay is U.S.-source.
	Performed string `json:"performed"`
	// Wallet is the buyer's wallet the payment is signed from. When named, the
	// authorization must be signed by its address.
	Wallet string `json:"wallet"`
	// Rail is x402 (the default) or chain.
	Rail string `json:"rail"`
	// Payment is the buyer's signed x402 authorization for the job — the value
	// PAYMENT-SIGNATURE carries, for a caller that sends no headers of its own,
	// over MCP or the command line. The header wins when both are sent.
	Payment string `json:"payment"`
	// Attempt is the buyer's own key for this hire: chosen once, when the hire
	// starts — a random id, 1 to 128 letters, digits, dots, colons, dashes or
	// underscores — and sent with every request it makes, the quote and the
	// payment alike, with the rest of the request unchanged. The same attempt
	// answers the same job: the quote's terms to sign until it is paid, the job
	// once it opened, whatever it has come to since. Another request under an
	// attempt already used is refused (422): a new hire is a new attempt.
	Attempt string `json:"attempt"`
}

// Hires another org for a piece of work, through a public listing or by a direct
// offer, and answers 201 with the job — open, waiting for the seller.
//
// It is paid over x402, in two steps on this one call. Sent without payment, the
// terms are cleared first — principal decides whether the buyer may pay the seller
// this amount, and a blocked payment, one that waits on the seller's tax form, and
// one that clears only with tax withheld, which this rail does not withhold, are
// refused and nothing is opened — and it answers 402 with the terms to sign, for
// the job resource job:<id>: on PAYMENT-REQUIRED, in the body, and in the message.
// Sent again with the signed authorization — on PAYMENT-SIGNATURE, or as
// payment — echoing that resource, the terms are cleared once more, the job is
// funding, and the rail checks the payment against exactly those terms, checks
// it stays acceptable until a dispute could last be ruled on, holds it for this
// job, and sets the amount aside in the buyer's wallet: a wallet that cannot
// cover it answers 402 insufficient_funds and opens nothing. Nothing is paid
// until the buyer releases the job, or its review window passes after delivery;
// a job that ends unpaid returns the amount to the wallet. Any member of the
// buying org; a platform SuperAdmin inspecting another org cannot spend it.
//
// A hire sent with an attempt — the buyer's own key for it, in the body — is one
// hire however often it is sent: the same attempt with the same request answers
// the same job, so a request whose answer was lost is sent again as it was, and
// opens one job and sets its amount aside once. It answers the quote's terms
// again while the job is quoted or funding (a quote over its hour is given up
// and quoted anew), and the job, 201, once it opened. Another request under an
// attempt already used is refused, 422. The buyer's own jobs list its quotes
// and those being funded, with their attempts, so an attempt in flight is found
// there too. Without an attempt, the same terms asked again within the hour
// answer the same quote, and the same payment sent again answers the job it
// opened.
//
// Example: {"listing": "lst_4f1c", "brief": "Summarize our Q3 filings", "amount": "250.00", "attempt": "8f14e45f-ea8d-4c4e-9c1b-0f3b2b1a7d6e"}
func (o marketOps) hire(ctx context.Context, in *hireIn) (*Job, error) {
	buyer, c, err := o.buyer(ctx)
	if err != nil {
		return nil, err
	}
	if err := cloud.CSRF(ctx); err != nil {
		return nil, err
	}
	payment := strings.TrimSpace(cmp.Or(c.Header(client.HeaderPaymentSignature), in.Payment))
	attempt := strings.TrimSpace(in.Attempt)
	if attempt != "" {
		if !attempts.MatchString(attempt) {
			return nil, zip.ErrBadRequest("an attempt is 1 to 128 letters, digits, dots, colons, dashes or underscores")
		}
		// One request of an attempt at a time in this process: a quote and its payment,
		// or one sent twice, never interleave. Another replica's is kept apart by the
		// store: an attempt is one job (the unique index on buyer and attempt refuses a
		// second quote), and a funding takes its job's lock across replicas (fund).
		release := o.s.State.money.Hold("attempt:" + buyer + ":" + attempt)
		defer release()
		j, found, err := o.s.State.store.attempted(ctx, buyer, attempt)
		switch {
		case err != nil:
			return nil, zip.Errorf(http.StatusInternalServerError, "%v", err)
		case !found:
		case j.asked != asked(in):
			return nil, zip.Errorf(http.StatusUnprocessableEntity,
				"attempt %s made job %s with another request: a new hire is a new attempt", attempt, j.ID)
		case !slices.Contains(unopened, j.Status):
			return &j.Job, nil // the job this attempt opened, as it stands
		case payment != "":
		case time.Now().Unix() <= j.CreatedAt+int64(quoteTTL.Seconds()):
			return nil, o.ask(c, buyer, j)
		default:
			// A quote past its hour is given up — whatever its payment set aside
			// returned first — and the terms quoted anew.
			if err := o.lapse(ctx, j.ID); err != nil {
				return nil, zip.Errorf(http.StatusServiceUnavailable, "the payment rail did not answer for job %s, which this attempt quoted: %v", j.ID, err)
			}
		}
	}
	if payment != "" {
		return o.fund(ctx, c, buyer, in, payment)
	}
	return nil, o.quote(ctx, c, buyer, in)
}

// attempts is what an attempt may be spelled with.
var attempts = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// asked is what a hire asks for, as a digest: the whole request but its payment
// and its attempt. An attempt sent again must ask the same.
func asked(in *hireIn) string {
	req := *in
	req.Payment, req.Attempt = "", ""
	b, _ := json.Marshal(req)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// quote clears a job's terms, records them, and answers the 402 that names what
// to sign.
func (o marketOps) quote(ctx context.Context, c *zip.Ctx, buyer string, in *hireIn) error {
	j, err := o.terms(ctx, buyer, in)
	if err != nil {
		return err
	}
	if j.Escrow.Rail == RailChain {
		return zip.Errorf(http.StatusServiceUnavailable, "the Lux job escrow is not bound in this deployment: %s", unbound)
	}
	st := o.s.State.store
	j.Attempt, j.asked = strings.TrimSpace(in.Attempt), asked(in)
	if j.Attempt == "" {
		// The same terms, asked again without an attempt: the quote they already
		// have. Funding it clears it again.
		q, found, err := st.quoted(ctx, j, time.Now().Add(-quoteTTL).Unix())
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "%v", err)
		}
		if found {
			return o.ask(c, buyer, q)
		}
	}
	clr, err := o.clear(ctx, j, "")
	if err != nil {
		return err
	}
	j.Clearance = clr
	now := time.Now().Unix()
	j.ID, j.Status, j.CreatedAt, j.UpdatedAt = "job_"+randHex(8), StatusQuoted, now, now
	if err := st.putJob(ctx, j, buyer, func() error {
		return o.trail(ctx, buyer, buyer, "marketplace.job."+StatusQuoted, audit.Resource{Type: "marketplace.job", ID: j.ID},
			map[string]any{"seller": j.SellerOrg, "amount": j.Amount.String(), "category": j.Category, "clearance": j.Clearance,
				"attempt": j.Attempt})
	}); err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "%v", err)
	}
	return o.ask(c, buyer, j)
}

// ask answers the 402 that names what to sign for a quoted job.
func (o marketOps) ask(c *zip.Ctx, buyer string, j jobRow) error {
	resource := client.JobResource(j.ID)
	auth, err := x402peer.X402Authorize(as(c, buyer), &client.AuthorizationIn{Resource: resource})
	if err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "the payment rail did not answer: %v", err)
	}
	if auth.Challenge == "" {
		return zip.Errorf(http.StatusServiceUnavailable, "the payment rail stated no terms: %s", auth.Reason)
	}
	return challenge(c, auth.Challenge, fmt.Sprintf("sign the payment for %s (clearance %s) and send this request again with it on %s or as payment",
		resource, j.Clearance, client.HeaderPaymentSignature))
}

// errUncleared is principal's refusal of a payment: blocked, waiting on the
// seller's tax form, or clearing only net of a withholding this rail cannot make.
// It wraps the refusal the caller answers with, and tells it apart from a
// clearance that could not be asked at all.
var errUncleared = errors.New("marketplace: the payment does not clear")

// clear asks principal whether the buyer may pay the seller a job's amount, and
// answers the clearance only when the payment can be made as this rail makes it:
// in full, to the seller, with nothing withheld. again is the job's clearance
// being asked again — the quote's, or the latest since — and empty for the
// quote's own. ctx acts for the buyer.
func (o marketOps) clear(ctx context.Context, j jobRow, again string) (string, error) {
	cl, err := principalpeer.PrincipalClear(ctx, &client.ClearIn{Payee: j.SellerOrg, Amount: dollars(j.Amount),
		Category: j.Category, Rail: RailX402, Performed: j.Performed, Again: again})
	switch {
	case errors.Is(err, cloud.ErrNoPeer):
		return "", zip.Errorf(http.StatusServiceUnavailable, "clearance is unreachable, and no job opens without one")
	case err != nil:
		return "", zip.Errorf(http.StatusBadGateway, "clearance: %v", err)
	}
	switch cl.Verdict {
	case client.ClearCleared:
	case client.ClearNeedsForm:
		return "", fmt.Errorf("%w: %w", errUncleared, zip.Errorf(http.StatusConflict,
			"clearance %s: the seller must furnish its tax form to you first (%s)", cl.ID, strings.Join(cl.Required, ", ")))
	default:
		return "", fmt.Errorf("%w: %w", errUncleared, zip.Errorf(http.StatusForbidden,
			"clearance %s: this payment cannot clear now (%s)", cl.ID, strings.Join(cl.Required, ", ")))
	}
	// A payment cleared net of withholding — backup withholding on a payee with no
	// TIN (IRC §3406), or chapter 3 on a foreign one — is one the payer must split:
	// the net to the seller, the rest kept back and deposited by the payer. x402
	// pays one amount to one payee, and apps/tax reports what the rails withheld as
	// nothing, so it cannot make that payment: it is refused, never paid gross.
	withheld, werr := money.ParseUSD(cmp.Or(strings.TrimSpace(cl.Withheld), "0"))
	net, nerr := money.ParseUSD(cmp.Or(strings.TrimSpace(cl.Net), j.Amount.String()))
	if werr != nil || nerr != nil || withheld.Sign() != 0 || net.Cmp(j.Amount) != 0 {
		return "", fmt.Errorf("%w: %w", errUncleared, zip.Errorf(http.StatusConflict,
			"clearance %s: this payment clears as %s to the seller with %s withheld, and x402 pays the whole amount with nothing withheld: "+
				"the seller's tax form must lift the withholding first", cl.ID, cmp.Or(cl.Net, "?"), cmp.Or(cl.Withheld, "0")))
	}
	return cl.ID, nil
}

// challenge refuses 402 with the terms to sign where every caller finds them: on
// PAYMENT-REQUIRED for an x402 client, in the body as the rail's own refusal
// states them, and at the end of the message for a caller that reads only that —
// over MCP, a tool result carries the message and no header.
func challenge(c *zip.Ctx, terms, msg string) error {
	c.SetHeader(client.HeaderPaymentRequired, terms)
	detail := map[string]any{"paymentRequired": terms}
	var req protocol.PaymentRequired
	if protocol.DecodeHeader(terms, &req) == nil {
		detail["x402Version"], detail["accepts"], detail["resource"] = req.X402Version, req.Accepts, req.Resource
	}
	return zip.ErrPaymentRequired(msg + "; the terms (" + client.HeaderPaymentRequired + "): " + terms).With(detail)
}

// fund opens a quoted job on its buyer's signed authorization.
func (o marketOps) fund(ctx context.Context, c *zip.Ctx, buyer string, in *hireIn, payment string) (*Job, error) {
	pay, err := protocol.ParsePayment(payment)
	if err != nil {
		return nil, zip.ErrBadRequest("the payment does not parse: " + err.Error())
	}
	id, ok := "", false
	if pay.Resource != nil {
		id, ok = client.JobOf(pay.Resource.URL)
	}
	if !ok {
		return nil, zip.ErrBadRequest("the payment names no job: echo the challenge's resource")
	}
	st := o.s.State.store
	// One funding of a job at a time, on every replica: two payments raced onto one
	// quote set one amount aside, never two, and a funding that finds the job opened
	// by the other must answer that job and not give its payment up.
	release, err := o.hold(ctx, id)
	if err != nil {
		return nil, err
	}
	defer release()
	j, found, err := st.job(ctx, id)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	if !found || j.BuyerOrg != buyer {
		return nil, zip.ErrNotFound("job not found")
	}
	if j.Attempt != strings.TrimSpace(in.Attempt) {
		return nil, zip.Errorf(http.StatusConflict, "the payment is for job %s, quoted under another attempt", id)
	}
	if !slices.Contains(unopened, j.Status) {
		if j.payment == payment {
			return &j.Job, nil // the same payment again: the job it opened
		}
		return nil, zip.Errorf(http.StatusConflict, "job %s is already paid for", id)
	}
	if err := j.matches(in); err != nil {
		return nil, err
	}
	// The authorization must still settle at the last moment the job can pay its
	// seller: a dispute raised as the review window after the deadline closes, and
	// ruled on as the arbiter's time runs out — or paid by the clock, a day behind
	// at worst.
	a := pay.Payload.Authorization
	signed, perr := a.Parse()
	if perr != nil {
		return nil, zip.ErrBadRequest("the payment's authorization does not parse: " + perr.Error())
	}
	validBefore := int64(signed.ValidBefore)
	tail := j.review + int64((disputeTimeout + clockRoom).Seconds())
	deadline := j.Deadline
	if deadline == 0 {
		deadline = validBefore - tail
	}
	now := time.Now().Unix()
	if deadline < now+int64(minWork.Seconds()) || deadline+tail > validBefore {
		return nil, zip.Errorf(http.StatusPaymentRequired,
			"the authorization must stay valid through the deadline, the %d-second review window after it, the %d-second ruling on a dispute and a day for the clock",
			j.review, int64(disputeTimeout.Seconds()))
	}
	if w := strings.TrimSpace(in.Wallet); w != "" {
		p, err := walletpeer.WalletsPayee(as(c, buyer), &client.PayeeIn{WalletID: w})
		if err != nil {
			return nil, zip.Errorf(http.StatusBadGateway, "wallets: %v", err)
		}
		if !p.Found || !strings.EqualFold(p.Address, a.From) {
			return nil, zip.ErrForbidden("the payment is not signed by wallet " + w)
		}
	}
	// The payment is checked before anything is asked or kept for it: a payment the
	// rail would refuse costs no clearance.
	auth, err := x402peer.X402Authorize(as(c, buyer), &client.AuthorizationIn{Resource: client.JobResource(id), Payment: payment,
		Until: deadline + tail})
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the payment rail did not answer: %v", err)
	}
	if !auth.OK {
		return nil, refused(c, auth)
	}
	// The payment is agreed now, so it is cleared now: the quote's clearance may be
	// an hour old, and a seller blocked since is not hired.
	clr, err := o.clear(ctx, j, j.Clearance)
	if err != nil {
		return nil, err
	}
	ch, err := o.s.Bill.Reserve(ctx, cloud.PayerOf(ctx), actHire, 0)
	if err != nil {
		return nil, cloud.Denied(err)
	}
	defer ch.Release()
	// A payment presented for this quote before, whose answer never came back, may
	// have set the amount aside: it is given up before another is held.
	if j.payment != "" && j.payment != payment {
		if _, err := o.void(j, j.payment); err != nil {
			return nil, zip.Errorf(http.StatusServiceUnavailable, "the payment rail did not answer for the payment presented before this one: %v", err)
		}
	}
	// The payment is kept on the quote before the rail is asked to hold it, so a
	// hold whose answer is lost is found, and given back if the job never opens.
	if err := o.present(ctx, j, buyer, payment); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%v", err)
	}
	j.Status, j.payment = StatusFunding, payment
	auth, err = x402peer.X402Authorize(as(c, buyer), &client.AuthorizationIn{Resource: client.JobResource(id), Payment: payment,
		Until: deadline + tail, Hold: true})
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the payment rail did not answer: %v; "+
			"send the same payment again, and whatever it set aside is given back if the job does not open", err)
	}
	if !auth.OK {
		return nil, refused(c, auth)
	}
	moved, err := o.step(ctx, j, []string{StatusFunding}, StatusOpen, buyer, map[string]any{
		"payment": payment, "payer": auth.From, "valid_before": auth.ValidBefore, "deadline": deadline,
		"network": auth.Network, "asset": auth.Asset, "pay_to": auth.PayTo, "clearance": clr,
	}, map[string]any{"seller": j.SellerOrg, "amount": j.Amount.String(), "category": j.Category, "clearance": clr})
	if err == nil && !moved {
		err = zip.Errorf(http.StatusConflict, "job %s is no longer being funded", id)
	}
	if err != nil {
		// What was set aside for a job that did not open goes back to the buyer; the
		// quote keeps the payment, so its lapse gives it back if this did not.
		if _, verr := o.void(j, payment); verr != nil {
			o.s.Log.Warn("marketplace: a payment held for a job that did not open is returned when its quote lapses", "job", id, "err", verr)
		}
		return nil, err
	}
	ch.Debit(meter.Usage{Model: actHire})
	return o.read(ctx, id)
}

// present keeps the payment presented for a quote on it, before the rail is asked
// to hold it, and the quote is funding from then on: a hold whose answer never came
// back is found by it, and given back if the job never opens. It is on the buyer's
// trail alone, as the quote is. Its caller holds the job's lock.
func (o marketOps) present(ctx context.Context, j jobRow, buyer, payment string) error {
	if o.s.State.audit == nil {
		return errUnaudited
	}
	return o.s.State.store.tx(ctx, func(tx *sql.Tx) error {
		st := o.s.State.store
		moved, err := st.move(ctx, tx, j.ID, []string{StatusQuoted}, "", StatusFunding, buyer, map[string]any{"payment": payment})
		if err != nil {
			return err
		}
		if !moved {
			// Funding already: this payment takes the place of the one before it,
			// which its caller gave up first.
			held, err := st.esc.Put(ctx, j.BuyerOrg, j.ID, "payment", payment)
			if err != nil {
				return fmt.Errorf("marketplace: present payment: %w", err)
			}
			res, err := tx.ExecContext(ctx, `UPDATE jobs SET payment = $1, updated_at = $2 WHERE id = $3 AND status = $4`,
				held, time.Now().Unix(), j.ID, StatusFunding)
			if err != nil {
				return fmt.Errorf("marketplace: present payment: %w", err)
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return fmt.Errorf("marketplace: job %s is no longer quoted", j.ID)
			}
		}
		return o.trail(ctx, buyer, buyer, "marketplace.job."+StatusFunding, audit.Resource{Type: "marketplace.job", ID: j.ID},
			map[string]any{"seller": j.SellerOrg, "amount": j.Amount.String(), "attempt": j.Attempt})
	})
}

// refused answers the rail's refusal of a payment, with the terms to sign again
// when it states them.
func refused(c *zip.Ctx, auth *client.Authorization) error {
	if auth.Challenge != "" {
		c.SetHeader(client.HeaderPaymentRequired, auth.Challenge)
	}
	return zip.Errorf(cmpStatus(auth.Status), "the payment was refused: %s (%s)", auth.Reason, auth.Code)
}

// terms resolves what a hire asks for into a job's terms.
func (o marketOps) terms(ctx context.Context, buyer string, in *hireIn) (jobRow, error) {
	var j jobRow
	st := o.s.State.store
	listing := strings.TrimSpace(in.Listing)
	switch {
	case listing != "" && strings.TrimSpace(in.Seller) != "":
		return j, zip.ErrBadRequest("a job names a listing or a seller, not both")
	case listing != "":
		l, found, err := st.Public(ctx, listing)
		if err != nil {
			return j, zip.Errorf(http.StatusInternalServerError, "%v", err)
		}
		if !found {
			return j, zip.ErrNotFound("listing not found")
		}
		j.Listing, j.Title, j.SellerOrg, j.recipient = l.ID, l.Title, l.PublisherOrg, l.Recipient
	default:
		j.SellerOrg = strings.TrimSpace(in.Seller)
		j.Title = strings.TrimSpace(in.Title)
		if !principal.NamesOrg(j.SellerOrg) {
			return j, zip.ErrBadRequest("a direct offer names the seller org")
		}
		if j.Title == "" || len(j.Title) > maxTitle {
			return j, zip.ErrBadRequest("a direct offer has a title (<=200 chars)")
		}
	}
	if j.SellerOrg == buyer {
		return j, zip.ErrBadRequest("an org cannot hire itself")
	}
	if j.recipient == "" {
		p, bound, err := st.payout(ctx, j.SellerOrg)
		if err != nil {
			return j, zip.Errorf(http.StatusInternalServerError, "%v", err)
		}
		if !bound {
			return j, zip.Errorf(http.StatusConflict, "the seller has no payout wallet to be paid into")
		}
		j.recipient = p.Wallet
	}
	j.BuyerOrg = buyer
	j.Brief = strings.TrimSpace(in.Brief)
	if j.Brief == "" || len(j.Brief) > maxText {
		return j, zip.ErrBadRequest("a brief is required (<=4096 chars)")
	}
	amount, err := money.ParseUSD(strings.TrimSpace(in.Amount))
	if err != nil || amount.Sign() <= 0 || amount.Cmp(money.FromCents(amount.Cents())) != 0 {
		return j, zip.ErrBadRequest("amount is a positive U.S. dollar amount to the cent, e.g. \"250.00\"")
	}
	j.Amount, j.Currency = amount, "USD"
	j.Category = strings.TrimSpace(in.Category)
	if j.Category == "" {
		j.Category = client.CategoryService
	}
	if !slices.Contains([]string{client.CategoryService, client.CategoryGoods, client.CategoryTransfer, client.CategoryRoyalty}, j.Category) {
		return j, zip.ErrBadRequest("category is service, goods, transfer or royalty")
	}
	j.review = in.Review
	if j.review == 0 {
		j.review = int64(defaultReview.Seconds())
	}
	if j.review < 0 || j.review > int64(maxReview.Seconds()) {
		return j, zip.ErrBadRequest("review is at most 30 days, in seconds")
	}
	j.Review = j.review
	j.Deadline = in.Deadline
	if j.Deadline != 0 && j.Deadline < time.Now().Add(minWork).Unix() {
		return j, zip.ErrBadRequest("the deadline leaves the seller less than an hour")
	}
	j.Performed = strings.ToUpper(strings.TrimSpace(in.Performed))
	if j.Performed != "" && !alpha2.MatchString(j.Performed) {
		return j, zip.ErrBadRequest("performed is an ISO 3166-1 alpha-2 code")
	}
	j.Escrow.Rail = strings.TrimSpace(in.Rail)
	if j.Escrow.Rail == "" {
		j.Escrow.Rail = RailX402
	}
	if j.Escrow.Rail != RailX402 && j.Escrow.Rail != RailChain {
		return j, zip.ErrBadRequest("rail is x402 or chain")
	}
	return j, nil
}

var alpha2 = regexp.MustCompile(`^[A-Z]{2}$`)

// matches refuses a payment sent with terms other than the ones its quote cleared.
func (j jobRow) matches(in *hireIn) error {
	if l := strings.TrimSpace(in.Listing); l != "" && l != j.Listing {
		return zip.Errorf(http.StatusConflict, "the payment is for job %s, hired through another listing", j.ID)
	}
	if s := strings.TrimSpace(in.Seller); s != "" && s != j.SellerOrg {
		return zip.Errorf(http.StatusConflict, "the payment is for job %s, offered to another seller", j.ID)
	}
	if a := strings.TrimSpace(in.Amount); a != "" {
		if amount, err := money.ParseUSD(a); err != nil || amount.Cmp(j.Amount) != 0 {
			return zip.Errorf(http.StatusConflict, "the payment is for job %s, at another amount", j.ID)
		}
	}
	return nil
}

// ── the parties' acts ─────────────────────────────────────────────────────────

// jobRef addresses one job.
type jobRef struct {
	// ID is the job, from the path.
	ID string `json:"id"`
}

// Accepts a job the caller's org was hired for: the seller takes the work on and
// the clock toward its deadline is the seller's. Only an open job, and only before
// its deadline.
//
// Example: {"id": "job_9a1f"}
func (o marketOps) accept(ctx context.Context, in *jobRef) (*Job, error) {
	j, org, err := o.party(ctx, in.ID, sellerSide)
	if err != nil {
		return nil, err
	}
	if j.Deadline > 0 && time.Now().Unix() > j.Deadline {
		return nil, zip.Errorf(http.StatusConflict, "job %s is past its deadline", j.ID)
	}
	return o.act(ctx, j, []string{StatusOpen}, StatusAccepted, org, nil, nil)
}

// declineIn turns a job down.
type declineIn struct {
	// ID is the job, from the path.
	ID string `json:"id"`
	// Reason is the seller's words to the buyer, at most 4096 characters.
	Reason string `json:"reason"`
}

// Declines a job the caller's org was hired for, before any work. Nothing was
// paid: the buyer's authorization is given up, never settled, and the amount it
// set aside returns to the buyer's wallet.
//
// Example: {"id": "job_9a1f", "reason": "Booked through March"}
func (o marketOps) decline(ctx context.Context, in *declineIn) (*Job, error) {
	j, org, err := o.party(ctx, in.ID, sellerSide)
	if err != nil {
		return nil, err
	}
	return o.unpaid(ctx, j, StatusDeclined, org, map[string]any{"reason": clip(in.Reason)}, StatusOpen)
}

// Takes back a job the caller's org opened, before the seller accepts it. Nothing
// was paid: the amount set aside returns to the wallet.
//
// Example: {"id": "job_9a1f"}
func (o marketOps) cancel(ctx context.Context, in *jobRef) (*Job, error) {
	j, org, err := o.party(ctx, in.ID, buyerSide)
	if err != nil {
		return nil, err
	}
	return o.unpaid(ctx, j, StatusCancelled, org, nil, StatusOpen)
}

// deliverIn records a delivery.
type deliverIn struct {
	// ID is the job, from the path.
	ID string `json:"id"`
	// Note says what was delivered. Required, at most 4096 characters.
	Note string `json:"note"`
	// URL is where it is, https.
	URL string `json:"url"`
	// Hash is the seller's own commitment to what was delivered, 32 bytes as 0x
	// hex; the SHA-256 of the note and URL when empty.
	Hash string `json:"hash"`
}

var hash32 = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)

// Records delivery of a job the caller's org accepted, before its deadline, and
// starts the review window: the buyer releases or disputes within it, or it
// releases itself when it closes.
//
// Example: {"id": "job_9a1f", "note": "Summary attached", "url": "https://files.example.com/q3.pdf"}
func (o marketOps) deliver(ctx context.Context, in *deliverIn) (*Job, error) {
	j, org, err := o.party(ctx, in.ID, sellerSide)
	if err != nil {
		return nil, err
	}
	note, url := strings.TrimSpace(in.Note), strings.TrimSpace(in.URL)
	if note == "" || len(note) > maxText {
		return nil, zip.ErrBadRequest("a delivery note is required (<=4096 chars)")
	}
	if url != "" && (!strings.HasPrefix(url, "https://") || len(url) > maxURL) {
		return nil, zip.ErrBadRequest("a delivery url is https")
	}
	h := strings.TrimSpace(in.Hash)
	switch {
	case h == "":
		sum := sha256.Sum256([]byte(note + "\n" + url))
		h = "0x" + hex.EncodeToString(sum[:])
	case !hash32.MatchString(h):
		return nil, zip.ErrBadRequest("a delivery hash is 32 bytes as 0x hex")
	}
	h = strings.ToLower(h)
	now := time.Now().Unix()
	if j.Deadline > 0 && now > j.Deadline {
		return nil, zip.Errorf(http.StatusConflict, "job %s is past its deadline", j.ID)
	}
	delivered, err := o.act(ctx, j, []string{StatusAccepted}, StatusDelivered, org,
		map[string]any{"delivery_note": note, "delivery_url": url, "delivery_hash": h, "delivered_at": now},
		map[string]any{"hash": h})
	if err == nil {
		o.stated(ctx, eventDelivered, *delivered, 0)
	}
	return delivered, err
}

// Releases a job the caller's org is paying for, paying the seller the whole
// amount: the buyer's authorization is settled on the rail, once, and the rail
// states the payment. The buyer may release any time after acceptance, including
// to end a dispute.
//
// Example: {"id": "job_9a1f"}
func (o marketOps) release(ctx context.Context, in *jobRef) (*Job, error) {
	j, org, err := o.party(ctx, in.ID, buyerSide)
	if err != nil {
		return nil, err
	}
	return o.pay(ctx, j, org, unsettled...)
}

// unsettled is every state a job's payment may be settled from, or given up in.
var unsettled = []string{StatusAccepted, StatusDelivered, StatusDisputed}

// disputeIn stops a job for a ruling.
type disputeIn struct {
	// ID is the job, from the path.
	ID string `json:"id"`
	// Reason is the disputing party's words. Required, at most 4096 characters.
	Reason string `json:"reason"`
}

// Stops a job for a ruling, as either party: before delivery (and before the
// deadline), or within the review window after it. Nothing moves while it is
// disputed. It ends when the buyer releases it, the seller refunds it, the
// platform's arbiter rules, or the arbiter's time lapses and it is refunded.
//
// Example: {"id": "job_9a1f", "reason": "The summary covers Q2, not Q3"}
func (o marketOps) dispute(ctx context.Context, in *disputeIn) (*Job, error) {
	j, org, err := o.party(ctx, in.ID, eitherSide)
	if err != nil {
		return nil, err
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" || len(reason) > maxText {
		return nil, zip.ErrBadRequest("a reason is required (<=4096 chars)")
	}
	now := time.Now().Unix()
	switch j.Status {
	case StatusAccepted:
		if j.Deadline > 0 && now > j.Deadline {
			return nil, zip.Errorf(http.StatusConflict, "job %s is past its deadline", j.ID)
		}
	case StatusDelivered:
		if now > j.Delivery.At+j.review {
			return nil, zip.Errorf(http.StatusConflict, "job %s is past its review window", j.ID)
		}
	default:
		return nil, zip.Errorf(http.StatusConflict, "job %s is %s", j.ID, j.Status)
	}
	return o.act(ctx, j, []string{j.Status}, StatusDisputed, org,
		map[string]any{"dispute_reason": reason, "disputed_by": org, "disputed_at": now}, map[string]any{"reason": reason})
}

// Refunds a job the caller's org accepted, as the seller, at any point before it
// is paid: nothing was moved, so the buyer's authorization is given up, never
// settled, and the amount it set aside returns to the buyer's wallet.
//
// Example: {"id": "job_9a1f"}
func (o marketOps) refund(ctx context.Context, in *jobRef) (*Job, error) {
	j, org, err := o.party(ctx, in.ID, sellerSide)
	if err != nil {
		return nil, err
	}
	return o.unpaid(ctx, j, StatusRefunded, org, nil, unsettled...)
}

// resolveIn is the arbiter's ruling.
type resolveIn struct {
	// ID is the job, from the path.
	ID string `json:"id"`
	// Outcome is release — the seller is paid the whole amount — or refund.
	Outcome string `json:"outcome"`
}

// Rules on a disputed job, as the platform's arbiter: release pays the seller the
// whole amount from the buyer's held authorization, if the payment still clears;
// refund ends it with nothing paid, and the amount set aside returns to the
// buyer's wallet. A platform SuperAdmin only.
//
// Example: {"id": "job_9a1f", "outcome": "refund"}
func (o marketOps) resolve(ctx context.Context, in *resolveIn) (*Job, error) {
	if _, err := principal.Acting(ctx); err != nil {
		return nil, err
	}
	if !cloud.Super.Admits(cloud.AuthorityIn(ctx)) {
		return nil, zip.ErrForbidden("only the platform's arbiter rules on a dispute")
	}
	if o.s.State.audit == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%v", errUnaudited)
	}
	j, found, err := o.s.State.store.job(ctx, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	if !found || slices.Contains(unopened, j.Status) {
		return nil, zip.ErrNotFound("job not found")
	}
	if j.Ending != "" {
		return nil, ended(j)
	}
	if j.Status != StatusDisputed {
		return nil, zip.Errorf(http.StatusConflict, "job %s is %s", j.ID, j.Status)
	}
	by := "arbiter:" + cloud.Who(ctx).User
	switch in.Outcome {
	case "release":
		return o.pay(ctx, j, by, StatusDisputed)
	case "refund":
		return o.unpaid(ctx, j, StatusRefunded, by, map[string]any{"ruling": "refund"}, StatusDisputed)
	}
	return nil, zip.ErrBadRequest("outcome is release or refund")
}

// ── reading ───────────────────────────────────────────────────────────────────

// jobsQuery narrows the caller's jobs.
type jobsQuery struct {
	// Role is buyer — the jobs the caller's org hired for — or seller, the jobs it
	// was hired for. Buyer when empty.
	Role string `json:"role"`
	// Status keeps one state.
	Status string `json:"status"`
}

// jobPage is a page of jobs, newest first. It is never null.
type jobPage struct {
	// Jobs is at most 500 jobs.
	Jobs []Job `json:"jobs"`
}

// Lists the jobs the caller's org is a party to, newest first: as buyer, the work
// it hired — with the quotes it has not paid and the jobs being funded, each with
// the attempt that made it, so a hire whose answer was lost is found here — and as
// seller, the work it was hired for, which never includes another org's quote.
// Any member of the org.
//
// Example: {"role": "seller", "status": "open"}
func (o marketOps) listJobs(ctx context.Context, in *jobsQuery) (*jobPage, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	seller := false
	switch in.Role {
	case "", "buyer":
	case "seller":
		seller = true
	default:
		return nil, zip.ErrBadRequest("role is buyer or seller")
	}
	rows, err := o.s.State.store.jobsOf(ctx, seller, org, strings.TrimSpace(in.Status), maxJobs)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	out := &jobPage{Jobs: make([]Job, 0, len(rows))}
	for _, j := range rows {
		out.Jobs = append(out.Jobs, j.Job)
	}
	return out, nil
}

// Reads one job the caller's org is a party to. Another org's job, and one that
// does not exist, are the same 404.
//
// Example: {"id": "job_9a1f"}
func (o marketOps) getJob(ctx context.Context, in *jobRef) (*Job, error) {
	j, _, err := o.party(ctx, in.ID, reading)
	if err != nil {
		return nil, err
	}
	return &j.Job, nil
}

// ── the rules every act shares ────────────────────────────────────────────────

type side int

const (
	buyerSide side = iota
	sellerSide
	eitherSide
	// reading is any party reading, which is the one thing a SuperAdmin inspecting
	// another org may do as it.
	reading
)

// buyer is the org a hire is paid from: the acting org, and only when it is also
// the org whose books the request spends — a SuperAdmin inspecting another org
// spends its own, and a job is never paid from books other than its buyer's.
func (o marketOps) buyer(ctx context.Context) (string, *zip.Ctx, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return "", nil, err
	}
	c, ok := cloud.Request(ctx)
	if !ok {
		return "", nil, zip.ErrForbidden(cloud.Unattested)
	}
	if meter.Payer(c).Org() != org {
		return "", nil, zip.ErrForbidden("a job is paid from the acting org's own books")
	}
	if o.s.State.audit == nil {
		return "", nil, zip.Errorf(http.StatusServiceUnavailable, "%v", errUnaudited)
	}
	return org, c, nil
}

// party reads a job as one of its parties, on the side the act needs. A job the
// caller is no party to is a 404, the same as one that does not exist; a party
// on the wrong side is a 403.
//
// A party ACTS from its own books. A SuperAdmin inspecting another org reads that
// org's jobs, and takes none of its steps — least of all a release, which spends
// the buyer's wallet — and the history never names an org for a step its members
// did not take. The platform's one act on a job is the arbiter's ruling.
func (o marketOps) party(ctx context.Context, id string, want side) (jobRow, string, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return jobRow{}, "", err
	}
	if want != reading {
		if c, ok := cloud.Request(ctx); !ok || meter.Payer(c).Org() != org {
			return jobRow{}, "", zip.ErrForbidden("a party acts on a job from its own org: a SuperAdmin inspecting it reads and rules, and takes no party's step")
		}
	}
	j, found, err := o.s.State.store.job(ctx, strings.TrimSpace(id))
	if err != nil {
		return jobRow{}, "", zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	if !found || (org != j.BuyerOrg && org != j.SellerOrg) || (slices.Contains(unopened, j.Status) && org != j.BuyerOrg) {
		return jobRow{}, "", zip.ErrNotFound("job not found")
	}
	switch {
	case want == buyerSide && org != j.BuyerOrg:
		return jobRow{}, "", zip.ErrForbidden("only the buyer may do this")
	case want == sellerSide && org != j.SellerOrg:
		return jobRow{}, "", zip.ErrForbidden("only the seller may do this")
	case want != reading && j.Ending != "":
		return jobRow{}, "", ended(j)
	}
	return j, org, nil
}

// ended refuses a step on a job that was given an unpaid ending.
func ended(j jobRow) error {
	return zip.Errorf(http.StatusConflict, "job %s is %s, as soon as the payment rail returns what it set aside", j.ID, j.Ending)
}

// act moves a job and answers it as it now stands. A job that has already moved
// on is a 409.
func (o marketOps) act(ctx context.Context, j jobRow, from []string, next, by string, set, detail map[string]any) (*Job, error) {
	moved, err := o.step(ctx, j, from, next, by, set, detail)
	if err != nil {
		return nil, err
	}
	if !moved {
		cur, _, _ := o.s.State.store.job(ctx, j.ID)
		if cur.Ending != "" {
			return nil, ended(cur)
		}
		return nil, zip.Errorf(http.StatusConflict, "job %s is %s", j.ID, cur.Status)
	}
	return o.read(ctx, j.ID)
}

// step moves a job and puts the move on the trail, in one transaction: a step
// the trail did not take is not taken.
func (o marketOps) step(ctx context.Context, j jobRow, from []string, next, by string, set, detail map[string]any) (bool, error) {
	if o.s.State.audit == nil {
		return false, zip.Errorf(http.StatusServiceUnavailable, "%v", errUnaudited)
	}
	var moved bool
	err := o.s.State.store.tx(ctx, func(tx *sql.Tx) error {
		var err error
		if moved, err = o.s.State.store.move(ctx, tx, j.ID, from, j.ending, next, by, set); err != nil || !moved {
			return err
		}
		after := map[string]any{"from": j.Status, "to": next, "buyer": j.BuyerOrg, "seller": j.SellerOrg}
		for k, v := range detail {
			after[k] = v
		}
		for _, org := range []string{j.BuyerOrg, j.SellerOrg} {
			if err := o.trail(ctx, org, by, "marketplace.job."+next, audit.Resource{Type: "marketplace.job", ID: j.ID}, after); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return false, zip.Errorf(http.StatusServiceUnavailable, "%v", err)
	}
	return moved, nil
}

// pay settles a job's held authorization on the rail, as its buyer, and marks it
// released — from one of the states in from, which the act that asked expects.
// A payment principal no longer clears, or the rail refuses, leaves the job where
// it was: the clearance is asked again here, under the job's lock, because the
// one the job opened on may be weeks old, and a seller blocked since is not paid.
//
// It is safe to run twice: the rail settles an authorization once and answers the
// same receipt again, so a job whose release was interrupted after the money
// moved is released by the next attempt without moving it twice.
func (o marketOps) pay(ctx context.Context, j jobRow, by string, from ...string) (*Job, error) {
	release, err := o.hold(ctx, j.ID)
	if err != nil {
		return nil, err
	}
	defer release()
	j, err = o.now(ctx, j.ID, from)
	if err != nil {
		return nil, err
	}
	call, cancel := context.WithTimeout(cloud.For(context.Background(), j.BuyerOrg), payTimeout)
	clr, err := o.clear(call, j, j.Clearance)
	cancel()
	if err != nil {
		return nil, err
	}
	return o.settle(ctx, j, by, from, clr)
}

// unpaid ends a job without paying the seller — declined, cancelled or refunded —
// from one of the states in from. The ending is decided first: the job is given
// it, and from then on takes no other step, so an accept or a delivery that lands
// while the rail returns the money is refused instead of landing on a job whose
// money went back. It reaches the ending once the rail has given the buyer's held
// payment up (close); a rail that does not answer leaves it ending, for the clock
// to finish.
//
// It and pay hold the job's lock from the question to the answer, so an ending
// that gives the payment up and a release that takes it can never both be decided
// on the same job: a refund raced against a release ends one way, with the money
// where the job says it is.
func (o marketOps) unpaid(ctx context.Context, j jobRow, next, by string, detail map[string]any, from ...string) (*Job, error) {
	release, err := o.hold(ctx, j.ID)
	if err != nil {
		return nil, err
	}
	defer release()
	j, err = o.now(ctx, j.ID, from)
	if err != nil {
		return nil, err
	}
	if j.Escrow.Rail != RailX402 || j.payment == "" {
		return o.act(ctx, j, from, next, by, nil, detail)
	}
	// The decision is on the trail with the job's mark, in one transaction, and the
	// ending it reaches is on it again when the money is back.
	var stored string
	var decided bool
	err = o.s.State.store.tx(ctx, func(tx *sql.Tx) error {
		var err error
		if stored, decided, err = decide(ctx, tx, j.ID, from, ending{To: next, By: by, Detail: detail}); err != nil || !decided {
			return err
		}
		after := map[string]any{"from": j.Status, "to": next, "buyer": j.BuyerOrg, "seller": j.SellerOrg}
		maps.Copy(after, detail)
		for _, org := range []string{j.BuyerOrg, j.SellerOrg} {
			if err := o.trail(ctx, org, by, "marketplace.job.ending", audit.Resource{Type: "marketplace.job", ID: j.ID}, after); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%v", err)
	}
	if !decided {
		return nil, zip.Errorf(http.StatusConflict, "job %s moved on", j.ID)
	}
	j.ending, j.Ending = stored, next
	return o.close(ctx, j)
}

// close gives the payment of a job that was given an unpaid ending up on the rail,
// and takes the job to that ending. A release that moved the money and was
// interrupted before the job was marked leaves a job that reads unpaid and is not:
// the rail says spent instead, that one is finished as the release it was, and
// the ending is refused. Its caller holds the job's lock.
func (o marketOps) close(ctx context.Context, j jobRow) (*Job, error) {
	e, err := j.end()
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	spent, err := o.void(j, j.payment)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "job %s is %s once the payment rail returns what it set aside, and the rail did not answer: %v",
			j.ID, e.To, err)
	}
	if spent {
		if _, err := o.settle(ctx, j, e.By, []string{j.Status}, ""); err != nil {
			return nil, err
		}
		return nil, zip.Errorf(http.StatusConflict, "job %s was already paid: it is released", j.ID)
	}
	return o.act(ctx, j, []string{j.Status}, e.To, e.By, nil, e.Detail)
}

// finish takes a job to the unpaid ending it was given, when the first attempt
// was cut short: the rail did not answer, or the process stopped.
func (o marketOps) finish(ctx context.Context, id string) error {
	release, err := o.hold(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	j, found, err := o.s.State.store.job(ctx, id)
	if err != nil || !found || j.ending == "" {
		return err
	}
	_, err = o.close(ctx, j)
	return err
}

// void gives a job's held payment up on the rail, as its buyer: the amount set
// aside returns to the buyer's wallet. spent is the rail saying the payment was
// already presented for settlement, and nothing was returned.
func (o marketOps) void(j jobRow, payment string) (spent bool, err error) {
	call, cancel := context.WithTimeout(cloud.For(context.Background(), j.BuyerOrg), payTimeout)
	defer cancel()
	out, err := x402peer.X402Void(call, &client.VoidIn{Resource: client.JobResource(j.ID), Payment: payment})
	if err != nil {
		return false, err
	}
	return out.Spent, nil
}

// now reads a job again under its lock, and refuses it unless it is in one of the
// states in from and was given no ending: whatever the caller read before the lock
// may have moved on.
func (o marketOps) now(ctx context.Context, id string, from []string) (jobRow, error) {
	j, found, err := o.s.State.store.job(ctx, id)
	if err != nil {
		return jobRow{}, zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	if !found {
		return jobRow{}, zip.ErrNotFound("job not found")
	}
	if j.Ending != "" {
		return jobRow{}, ended(j)
	}
	if !slices.Contains(from, j.Status) {
		return jobRow{}, zip.Errorf(http.StatusConflict, "job %s is %s", j.ID, j.Status)
	}
	return j, nil
}

// settle pays a job's held authorization on the rail and marks it released, with
// the clearance it was paid on when one was asked. Its caller holds the job's
// lock.
func (o marketOps) settle(ctx context.Context, j jobRow, by string, from []string, clr string) (*Job, error) {
	if j.Escrow.Rail != RailX402 {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the Lux job escrow is not bound in this deployment: %s", unbound)
	}
	call, cancel := context.WithTimeout(cloud.For(context.Background(), j.BuyerOrg), payTimeout)
	defer cancel()
	out, err := x402peer.X402Settle(call, &client.SettleIn{Resource: client.JobResource(j.ID), Payment: j.payment})
	switch {
	case err != nil:
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the payment rail did not answer: %v", err)
	case out.Free:
		return nil, zip.Errorf(http.StatusConflict, "job %s is no longer payable", j.ID)
	case !out.OK:
		return nil, zip.Errorf(cmpStatus(out.Status), "the payment did not settle: %s (%s)", out.Reason, out.Code)
	}
	var receipt protocol.SettlementResponse
	if err := protocol.DecodeHeader(out.Response, &receipt); err != nil || receipt.Transaction == "" {
		return nil, zip.Errorf(http.StatusBadGateway, "the payment rail answered no receipt")
	}
	set := map[string]any{"settlement": receipt.Transaction}
	detail := map[string]any{"settlement": receipt.Transaction, "amount": j.Amount.String()}
	if clr != "" {
		set["clearance"], detail["clearance"] = clr, clr
	}
	return o.act(ctx, j, from, StatusReleased, by, set, detail)
}

// read answers a job as it stands.
func (o marketOps) read(ctx context.Context, id string) (*Job, error) {
	j, found, err := o.s.State.store.job(ctx, id)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	if !found {
		return nil, zip.ErrNotFound("job not found")
	}
	return &j.Job, nil
}

// as is the context a peer call acts for org in, from the request that asked.
func as(c *zip.Ctx, org string) context.Context { return cloud.As(c, org) }

// dollars renders an amount to the cent, the way a clearance reads one.
func dollars(a money.Amount) string {
	cents := a.Cents()
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

// cmpStatus is a refusal's status, never a success.
func cmpStatus(status int) int {
	if status < 400 {
		return http.StatusBadGateway
	}
	return status
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxText {
		return s[:maxText]
	}
	return s
}

// hold takes job id's lock across every replica and answers its release: a release
// and a refund, a funding and a lapse of one job never interleave, wherever each runs.
// A caller that cannot have it before its context ends is told the job is busy.
func (o marketOps) hold(ctx context.Context, id string) (func(), error) {
	release, err := o.s.State.money.Across(ctx, o.s.State.store.db, "job:"+id)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "job %s is busy; send it again", id)
	}
	return release, nil
}
