package marketplace

// seller.go — where an org stands as a seller, in one read, and the one fact it
// proves here: which wallet it is paid into.
//
// Every other fact has an owner, and each is asked, as the org: principal for its
// identity verification, legal entity, own screening and the credentials signed
// for its agents; tax for its form and the 1099s it received; the event plane for
// what it earned. A seller sees about itself only what those owners let an org
// see about itself — never a sanctions match, never a list entry.
//
// THE PAYOUT WALLET IS PROVED, not named. The seller asks for a challenge naming
// one of its wallets, signs it with that wallet — POST /v1/wallet/{id}/sign with
// the digest for a wallet the platform holds, personal_sign for one it holds
// itself — and the signature, recovered to the wallet's address, binds it. A job
// offered directly, with no listing to name a wallet, pays into this one.

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	eventpeer "github.com/hanzoai/cloud/client/event"
	principalpeer "github.com/hanzoai/cloud/client/principal"
	taxpeer "github.com/hanzoai/cloud/client/tax"
	walletpeer "github.com/hanzoai/cloud/client/wallet"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/luxfi/crypto"
	"github.com/zap-proto/zip"
)

const (
	// challengeTTL is how long a payout challenge may be signed.
	challengeTTL = 15 * time.Minute
	// earningPages bounds how much of a year one onboarding read sums.
	earningPages = 20
)

// sellerQuery names the year earnings and 1099s are read for.
type sellerQuery struct {
	// Year is the calendar year (UTC); the current one when zero.
	Year int `json:"year"`
}

// Onboarding is where the caller's org stands as a seller.
type Onboarding struct {
	// Org is the seller org.
	Org string `json:"org"`
	// Ready is true when nothing is missing.
	Ready bool `json:"ready"`
	// Missing names what the org still owes before it is paid without a hitch:
	// identity, sanctions, tax_form, tax_invalid or payout.
	Missing []string `json:"missing"`
	// Identity is the founders' identity verification (KYC): verified, pending,
	// failed or none.
	Identity string `json:"identity"`
	// Entity is the legal entity's formation stage (KYB) — "company" once formed
	// — or empty when the org began none.
	Entity string `json:"entity,omitempty"`
	// Tax is the org's own tax form, when it has one.
	Tax *TaxStatus `json:"tax,omitempty"`
	// Sanctions is the org's own screening: clear, review, blocked, unscreened or
	// unavailable, and why in words.
	Sanctions string `json:"sanctions"`
	// SanctionsReason says why Sanctions is what it is.
	SanctionsReason string `json:"sanctionsReason"`
	// Payout is the wallet the org proved it is paid into.
	Payout Payout `json:"payout"`
	// Credentials are the TaxPrincipalCredentials signed for the org's agents,
	// newest first.
	Credentials []client.Statement `json:"credentials"`
	// Earnings is what the org was paid in Year, from the economic events the rails
	// stated.
	Earnings Earnings `json:"earnings"`
	// Received are the 1099s payers furnished the org for Year.
	Received []client.Received `json:"received"`
	// Sources names each owning app asked, and whether it answered: ok, absent
	// (not run by this deployment) or failed.
	Sources []Source `json:"sources"`
}

// TaxStatus is an org's form, as the org itself may read it.
type TaxStatus struct {
	// Form is w9, w8ben or w8bene.
	Form string `json:"form"`
	// Certified is whether its signature covers the form as it stands.
	Certified bool `json:"certified"`
	// Valid is whether it is complete and unexpired.
	Valid bool `json:"valid"`
	// Expires is when a W-8 lapses, unix seconds; a W-9 does not.
	Expires int64 `json:"expires,omitempty"`
}

// Payout is a seller's proved payout wallet.
type Payout struct {
	// Wallet is the wallet id, in the seller's org.
	Wallet string `json:"wallet,omitempty"`
	// Address is its address, recovered from the signature that proved it.
	Address string `json:"address,omitempty"`
	// Bound is true once a signature proved it.
	Bound bool `json:"bound"`
	// BoundAt is when, unix seconds.
	BoundAt int64 `json:"boundAt,omitempty"`
}

// Earnings is what an org was paid in one year.
type Earnings struct {
	// Year is the calendar year (UTC).
	Year int `json:"year"`
	// Currency is USD.
	Currency string `json:"currency"`
	// Gross is the sum of every payment to the org, exact USD.
	Gross money.Amount `json:"gross"`
	// Payments is how many.
	Payments int `json:"payments"`
	// ByRail sums them by the rail that moved them.
	ByRail map[string]money.Amount `json:"byRail"`
	// Partial is true when the year held more than one read sums.
	Partial bool `json:"partial,omitempty"`
}

// Source is one owning app asked for an onboarding read.
type Source struct {
	// App is the app.
	App string `json:"app"`
	// Status is ok, absent or failed.
	Status string `json:"status"`
}

// Answers where the caller's org stands as a seller, in one read: its founders'
// identity verification (KYC) and legal entity (KYB), its tax form and whether it
// is certified and valid, its own sanctions screening — only what an org may see
// about itself — the payout wallet it proved, the TaxPrincipalCredentials signed
// for its agents, what it earned in the year from the economic events the rails
// stated, and the 1099s payers furnished it. Ready says nothing is missing. Each
// owning app is asked as the org; one this deployment does not run is named absent
// rather than read as empty. An org admin reads it.
//
// Example: {"year": 2026}
func (o marketOps) seller(ctx context.Context, in *sellerQuery) (*Onboarding, error) {
	org, err := publisher(ctx)
	if err != nil {
		return nil, err
	}
	year := in.Year
	if year == 0 {
		year = time.Now().UTC().Year()
	}
	if year < 2000 || year > 9999 {
		return nil, zip.ErrBadRequest("year is a calendar year")
	}
	out := &Onboarding{Org: org, Missing: []string{}, Credentials: []client.Statement{}, Received: []client.Received{},
		Earnings: Earnings{Year: year, Currency: "USD", Gross: money.Zero(), ByRail: map[string]money.Amount{}}}
	asked := func(app string, err error) bool {
		status := "ok"
		switch {
		case errors.Is(err, cloud.ErrNoPeer):
			status = "absent"
		case err != nil:
			status = "failed"
			o.s.Log.Warn("marketplace: onboarding read", "app", app, "org", org, "err", err)
		}
		out.Sources = append(out.Sources, Source{App: app, Status: status})
		return err == nil
	}
	out.Identity, out.Sanctions = "none", "unavailable"
	if st, err := principalpeer.PrincipalStanding(ctx, &client.PrincipalIn{}); asked(principalpeer.App, err) {
		out.Identity, out.Entity, out.Sanctions, out.SanctionsReason = st.Identity, st.Entity, st.Sanctions, st.Reason
		out.Credentials = st.Statements
	}
	if f, err := taxpeer.TaxStanding(ctx, &client.StandingIn{}); asked(taxpeer.App, err) && f.Form != "" {
		out.Tax = &TaxStatus{Form: f.Form, Certified: f.Certified, Valid: f.Valid, Expires: f.Expires}
	}
	if inbox, err := taxpeer.TaxInbox(ctx, &client.InboxIn{Year: year}); err == nil {
		out.Received = inbox.Received
	} else if !errors.Is(err, cloud.ErrNoPeer) {
		o.s.Log.Warn("marketplace: onboarding read", "app", "tax inbox", "org", org, "err", err)
	}
	asked(eventpeer.App, o.earnings(ctx, org, &out.Earnings))
	p, _, err := o.s.State.store.payout(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	out.Payout = p
	if out.Identity != "verified" {
		out.Missing = append(out.Missing, "identity")
	}
	if out.Sanctions != "clear" {
		out.Missing = append(out.Missing, "sanctions")
	}
	switch {
	case out.Tax == nil:
		out.Missing = append(out.Missing, "tax_form")
	case !out.Tax.Certified || !out.Tax.Valid:
		out.Missing = append(out.Missing, "tax_invalid")
	}
	if !p.Bound {
		out.Missing = append(out.Missing, "payout")
	}
	out.Ready = len(out.Missing) == 0
	noStore(ctx)
	return out, nil
}

// earnings sums what org was paid in e.Year, from the event plane, as org.
func (o marketOps) earnings(ctx context.Context, org string, e *Earnings) error {
	from := time.Date(e.Year, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	to := time.Date(e.Year+1, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	gross := money.Zero()
	cursor := ""
	for range earningPages {
		page, err := eventpeer.EventEconomics(ctx, &client.EconomicsIn{From: from, To: to, Cursor: cursor, Limit: 1000})
		if err != nil {
			return err
		}
		gone := map[string]bool{}
		for _, id := range page.Superseded {
			gone[id] = true
		}
		for _, ev := range page.Rows {
			if ev.Seller != org || gone[ev.ID] {
				continue
			}
			amt, err := money.ParseUSD(ev.Gross)
			if err != nil {
				return fmt.Errorf("event %s: gross %q: %w", ev.ID, ev.Gross, err)
			}
			gross = gross.Add(amt)
			rail, ok := e.ByRail[ev.Rail]
			if !ok {
				rail = money.Zero()
			}
			e.ByRail[ev.Rail] = rail.Add(amt)
			e.Payments++
		}
		if cursor = page.Next; cursor == "" {
			e.Gross = gross
			return nil
		}
	}
	e.Gross, e.Partial = gross, true
	return nil
}

// payoutIn names the wallet a seller means to be paid into.
type payoutIn struct {
	// Wallet is one of the caller org's wallets. Required.
	Wallet string `json:"wallet"`
}

// PayoutChallenge is what a seller signs to prove its payout wallet.
type PayoutChallenge struct {
	// Wallet is the wallet to prove.
	Wallet string `json:"wallet"`
	// Address is its address, which the signature must recover to.
	Address string `json:"address"`
	// Message is the text to sign with the wallet (EIP-191 personal_sign).
	Message string `json:"message"`
	// Digest is the EIP-191 hash of Message, 0x hex: what POST
	// /v1/wallet/{id}/sign signs, for a wallet the platform holds.
	Digest string `json:"digest"`
	// Expires is when the challenge may no longer be signed, unix seconds.
	Expires int64 `json:"expires"`
}

// Starts binding one of the caller org's wallets as the wallet it is paid into:
// answers a challenge naming the org, the wallet and its address, to be signed
// with that wallet within fifteen minutes and sent to POST
// /v1/marketplace/seller/payout/verify. A newer challenge replaces an older one.
// An org admin binds.
//
// Example: {"wallet": "wal_9f2"}
func (o marketOps) challengePayout(ctx context.Context, in *payoutIn) (*PayoutChallenge, error) {
	org, err := publisher(ctx)
	if err != nil {
		return nil, err
	}
	wallet := strings.TrimSpace(in.Wallet)
	if wallet == "" {
		return nil, zip.ErrBadRequest("wallet is required")
	}
	p, err := walletpeer.WalletsPayee(ctx, &client.PayeeIn{WalletID: wallet})
	if err != nil {
		return nil, peerErr("wallets", err)
	}
	if !p.Found || p.Address == "" {
		return nil, zip.ErrNotFound("wallet not found")
	}
	expires := time.Now().Add(challengeTTL).Unix()
	msg := fmt.Sprintf("Hanzo marketplace payout wallet\norg: %s\nwallet: %s\naddress: %s\nnonce: %s\nexpires: %d",
		org, wallet, p.Address, randHex(16), expires)
	if err := o.s.State.store.challenge(ctx, org, wallet, p.Address, msg, expires); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	d := personal(msg)
	return &PayoutChallenge{Wallet: wallet, Address: p.Address, Message: msg, Digest: "0x" + hex.EncodeToString(d[:]), Expires: expires}, nil
}

// verifyIn carries the signed challenge.
type verifyIn struct {
	// Signature is the wallet's 65-byte signature over the challenge's message
	// (EIP-191), 0x hex. Required.
	Signature string `json:"signature"`
}

// Binds the caller org's payout wallet: the signature over its outstanding
// challenge is recovered, and must recover to the wallet's address, before the
// challenge expires. A challenge binds once. It answers the payout wallet as it
// now stands; a job offered directly to the org is paid into it. An org admin
// binds.
//
// Example: {"signature": "0x5f1c…1b"}
func (o marketOps) bindPayout(ctx context.Context, in *verifyIn) (*Payout, error) {
	org, err := publisher(ctx)
	if err != nil {
		return nil, err
	}
	if o.s.State.audit == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%v", errUnaudited)
	}
	st := o.s.State.store
	wallet, address, msg, expires, found, err := st.pending(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	now := time.Now().Unix()
	if !found || now > expires {
		return nil, zip.Errorf(http.StatusConflict, "no payout challenge is outstanding: ask POST /v1/marketplace/seller/payout for one")
	}
	signer, err := recoverAddress(personal(msg), in.Signature)
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	if !strings.EqualFold(signer, address) {
		return nil, zip.ErrForbidden("the signature is not " + address + "'s")
	}
	held, err := st.seal("payouts", "address", address, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%v", err)
	}
	err = st.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO payouts (org, wallet, address, bound_at) VALUES ($1,$2,$3,$4)
			ON CONFLICT(org) DO UPDATE SET wallet=excluded.wallet, address=excluded.address, bound_at=excluded.bound_at`,
			org, wallet, held, now); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM challenges WHERE org = $1 AND message = $2`, org, msg)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errSpent
		}
		return o.trail(ctx, org, org, "marketplace.payout.bound", audit.Resource{Type: "marketplace.payout", ID: org},
			map[string]any{"wallet": wallet, "address": address})
	})
	switch {
	case errors.Is(err, errSpent):
		return nil, zip.Errorf(http.StatusConflict, "the challenge was already used")
	case err != nil:
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%v", err)
	}
	return &Payout{Wallet: wallet, Address: address, Bound: true, BoundAt: now}, nil
}

var errSpent = errors.New("marketplace: the challenge was already used")

// personal is the EIP-191 hash of a message: what personal_sign signs.
func personal(msg string) [32]byte {
	var out [32]byte
	copy(out[:], crypto.Keccak256([]byte(fmt.Sprintf("\x19Ethereum Signed Message:\n%d%s", len(msg), msg))))
	return out
}

// recoverAddress recovers the address that signed digest.
func recoverAddress(digest [32]byte, sigHex string) (string, error) {
	sig, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(sigHex), "0x"))
	if err != nil || len(sig) != 65 {
		return "", errors.New("the signature is 65 bytes as 0x hex")
	}
	if sig[64] >= 27 {
		sig[64] -= 27
	}
	if sig[64] > 1 {
		return "", errors.New("the signature's recovery id is not 0 or 1")
	}
	pub, err := crypto.SigToPub(digest[:], sig)
	if err != nil {
		return "", errors.New("the signature does not recover")
	}
	return crypto.PubkeyToAddress(*pub).Hex(), nil
}
