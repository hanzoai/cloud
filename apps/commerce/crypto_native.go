// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// crypto_native.go — native custody deposit capability discovery, intent minting,
// cross-org address collision protection, fee-inclusive plan quoting, and payer-scoped reads.
//
// Native crypto endpoints NEVER fall back to or conflate with Stripe processor status:
// capability discovery derives strictly from the running deposit watcher intersected with
// live non-Stripe custody, and fails closed (503) when unavailable.

import (
	"context"
	"fmt"
	"math/big"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	commercebilling "github.com/hanzoai/commerce/api/billing"
	"github.com/hanzoai/commerce/api/promo"
	"github.com/hanzoai/commerce/billing/depositledger"
	"github.com/hanzoai/commerce/billing/depositwatch"
	"github.com/hanzoai/commerce/billing/engine"
	"github.com/hanzoai/commerce/billing/gateway"
	commercedatastore "github.com/hanzoai/commerce/datastore"
	"github.com/hanzoai/commerce/models/cryptopaymentintent"
	"github.com/hanzoai/commerce/models/organization"
	commerceplan "github.com/hanzoai/commerce/models/plan"
	"github.com/hanzoai/commerce/payment/processor"
	commercetypes "github.com/hanzoai/commerce/types"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
)

// canonicalContracts maps well-known mainnet stablecoin contracts when an exchange-custodied
// asset carries an internal "kraken:<chain>:<token>" marker instead of an on-chain address.
var canonicalContracts = map[string]string{
	"base:usdc":      "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913",
	"ethereum:usdc":  "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48",
	"ethereum:usdt":  "0xdac17f958d2ee523a2206206994597c13d831ec7",
	"arbitrum:usdc":  "0xaf88d065e77c8cc2239327c5edb3a432268e5831",
	"arbitrum:usdt":  "0xfd086bc7cd5c481dcc9c85ebe478a1c0b69fcbb9",
	"optimism:usdc":  "0x0b2c639c533813f4aa9d7837caf62653d097ff85",
	"optimism:usdt":  "0x94b008aa00579c1307b0ef2c499ad98a8ce58e58",
	"polygon:usdc":   "0x3c499c542cef5e3811e1192ce70d8cc03d5c3359",
	"polygon:usdt":   "0xc2132d05d31c914a87c6611c10748aeb04b58e8f",
	"avalanche:usdc": "0xb97ef9ef8734c71904d8002f8b6bc66dd9c48a6e",
	"avalanche:usdt": "0x9702230a8ea53601f5cd2dc00fdbc13d4df4a8c7",
	"solana:usdc":    "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v",
	"solana:usdt":    "Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB",
}

func canonicalContract(chain, token, configured string) string {
	c := strings.TrimSpace(configured)
	normalized := strings.ToLower(c)
	if c != "" && !strings.HasPrefix(normalized, "kraken:") && !strings.HasPrefix(normalized, "pool:kraken:") {
		return c
	}
	key := strings.ToLower(strings.TrimSpace(chain)) + ":" + strings.ToLower(strings.TrimSpace(token))
	return canonicalContracts[key]
}

func tokenDecimals(a depositwatch.Asset) int {
	switch a.Family() {
	case depositwatch.FamilyXRPL:
		if strings.EqualFold(a.Token, "xrp") {
			return 6
		}
		return 15
	case depositwatch.FamilyBitcoin:
		return 8
	case depositwatch.FamilyTON:
		if strings.EqualFold(a.Token, "ton") {
			return 9
		}
		return 6
	default:
		return 6
	}
}

func effectiveConfirmations(a depositwatch.Asset) int {
	req := cryptopaymentintent.RequiredConfirmationsForChain(cryptopaymentintent.Chain(strings.ToLower(a.Chain)))
	if a.Confirmations > req {
		return a.Confirmations
	}
	return req
}

func minimumDepositCents(t depositwatch.Terms) int64 {
	const bps = 10_000
	keep := int64(bps - t.SlippageBps)
	if keep <= 0 {
		return t.FeeCents + 1
	}
	return ((t.FeeCents+1)*bps + keep - 1) / keep
}

func nativeDepositMinimum(ctx context.Context, asset depositwatch.Asset, cp processor.CryptoProcessor) (int64, bool) {
	minimum := minimumDepositCents(asset.Terms)
	if cp == nil || cp.Type() != processor.Kraken {
		return minimum, true
	}
	funding, ok := cp.(interface {
		MinimumUSDCents(context.Context, string, string) int64
	})
	if !ok {
		return 0, false
	}
	probe, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	exchangeMinimum := funding.MinimumUSDCents(probe, asset.Chain, asset.Token)
	return max(minimum, exchangeMinimum), exchangeMinimum > 0
}

// grossForNet returns the smallest USD cents deposit whose post-slippage, post-fee net
// credit is at least wantNetCents.
func grossForNet(wantNetCents int64, t depositwatch.Terms) int64 {
	if wantNetCents <= 0 {
		return minimumDepositCents(t)
	}
	const bps = 10_000
	keep := int64(bps - t.SlippageBps)
	if keep <= 0 {
		return wantNetCents + t.FeeCents
	}
	gross := ((wantNetCents+t.FeeCents)*bps + keep - 1) / keep
	if min := minimumDepositCents(t); gross < min {
		return min
	}
	return gross
}

func minUnitsString(minCents int64, decimals int, pegCents int64) string {
	if minCents <= 0 || decimals <= 0 {
		return "0"
	}
	if pegCents <= 0 {
		pegCents = 100
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	num := new(big.Int).Mul(big.NewInt(minCents), scale)
	den := big.NewInt(pegCents)
	// Ceiling division so minAmount base units always value >= minCents.
	num.Add(num, new(big.Int).Sub(den, big.NewInt(1)))
	num.Div(num, den)
	return num.String()
}

func liveWatchedAssets() []depositwatch.Asset {
	svc := depositledger.Default()
	if svc == nil || !svc.Running() {
		return nil
	}
	return svc.Assets()
}

func watchedAssetFor(chain, token string) (depositwatch.Asset, bool) {
	for _, a := range liveWatchedAssets() {
		if strings.EqualFold(a.Chain, chain) && strings.EqualFold(a.Token, token) {
			return a, true
		}
	}
	return depositwatch.Asset{}, false
}

// nativeCustodyProcessor resolves the active non-Stripe CryptoProcessor for asset ("" = any).
// Stripe is a Payment-Element rail and is NEVER consulted for native deposit custody.
func nativeCustodyProcessor(ctx context.Context, asset string) (processor.CryptoProcessor, error) {
	pol, err := commercebilling.ReadRouting(ctx, "")
	if err != nil {
		return nil, err
	}
	candidates := pol.CryptoProcessors(asset, func(t processor.ProcessorType) bool {
		return t != processor.Stripe
	})
	probe, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	var lastErr error
	for _, pt := range candidates {
		p, err := processor.Get(pt)
		if err != nil {
			lastErr = err
			continue
		}
		cp, ok := p.(processor.CryptoProcessor)
		if !ok {
			lastErr = fmt.Errorf("processor %s does not support native custody", pt)
			continue
		}
		if !cp.IsAvailable(probe) {
			lastErr = fmt.Errorf("crypto custody %s unavailable", pt)
			continue
		}
		return cp, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("crypto custody service unavailable")
}

func nativeSupportedChain(cp processor.CryptoProcessor, chain string) bool {
	if cp == nil {
		return false
	}
	for _, s := range cp.SupportedChains() {
		if strings.EqualFold(strings.TrimSpace(s), chain) {
			return true
		}
	}
	return false
}

// nativeCryptoOptions returns the live native deposit capability menu without ever falling
// back to or merging Stripe processor crypto configuration.
func nativeCryptoOptions(ctx context.Context) (*client.CryptoOptions, error) {
	watched := liveWatchedAssets()

	var custodyErr error
	if len(watched) == 0 {
		custodyErr = fmt.Errorf("crypto deposit watcher not running")
	}

	seen := map[string]bool{}
	assets := make([]client.CryptoPair, 0, len(watched))
	var anyCustodyErr error
	type custodyResult struct {
		cp  processor.CryptoProcessor
		err error
	}
	custodyByToken := map[string]custodyResult{}

	for _, a := range watched {
		chain := strings.ToLower(strings.TrimSpace(a.Chain))
		token := strings.ToLower(strings.TrimSpace(a.Token))
		key := chain + ":" + token
		if seen[key] {
			continue
		}
		var custody processor.CryptoProcessor
		if a.Pooled() {
			if strings.TrimSpace(a.PooledAddress) == "" {
				continue
			}
		} else {
			res, cached := custodyByToken[token]
			if !cached {
				res.cp, res.err = nativeCustodyProcessor(ctx, token)
				custodyByToken[token] = res
			}
			if res.err != nil {
				anyCustodyErr = res.err
				continue
			}
			if !nativeSupportedChain(res.cp, chain) {
				continue
			}
			custody = res.cp
		}
		minCents, ready := nativeDepositMinimum(ctx, a, custody)
		if !ready {
			anyCustodyErr = fmt.Errorf("deposit minimum unavailable for %s", key)
			continue
		}
		seen[key] = true
		dec := tokenDecimals(a)
		assets = append(assets, client.CryptoPair{
			Chain:              chain,
			Token:              token,
			Contract:           canonicalContract(chain, token, a.Contract),
			Decimals:           dec,
			MinimumCents:       minCents,
			MinAmount:          minUnitsString(minCents, dec, a.PegCents()),
			FeeCents:           a.Terms.FeeCents,
			Confirmations:      effectiveConfirmations(a),
			AddressTagRequired: a.Pooled(),
		})
	}

	sort.Slice(assets, func(i, j int) bool {
		if assets[i].Chain != assets[j].Chain {
			return assets[i].Chain < assets[j].Chain
		}
		return assets[i].Token < assets[j].Token
	})

	gw := gateway.Default().Networks()
	if gw == nil {
		gw = []string{}
	}
	if len(assets) == 0 && len(gw) == 0 {
		if custodyErr == nil {
			custodyErr = anyCustodyErr
		}
		RecordCryptoEvent("unavailable", "all", "all")
		return nil, zip.Errorf(503, "crypto deposits not configured")
	}

	cset, tset := map[string]bool{}, map[string]bool{}
	for _, p := range assets {
		cset[p.Chain] = true
		tset[p.Token] = true
	}
	chains := make([]string, 0, len(cset))
	for c := range cset {
		chains = append(chains, c)
	}
	sort.Strings(chains)
	tokens := make([]string, 0, len(tset))
	for t := range tset {
		tokens = append(tokens, t)
	}
	sort.Strings(tokens)

	return &client.CryptoOptions{
		Chains:  chains,
		Tokens:  tokens,
		Assets:  assets,
		Gateway: gw,
	}, nil
}

type planQuote struct {
	slug        string
	qty         int
	interval    commercetypes.Interval
	coupon      string
	chargeCents int64
}

func quotePlanChargeCents(ctx context.Context, org *organization.Organization, in *client.CryptoMintIn) (planQuote, error) {
	planID := strings.TrimSpace(in.PlanID)
	pl := commerceplan.New(commerceplan.AuthorityDB(ctx))
	found, err := pl.Query().Filter("Slug=", planID).Get()
	if err != nil || !found {
		if gerr := pl.GetById(planID); gerr != nil {
			return planQuote{}, zip.Errorf(400, "plan %q not found", planID)
		}
	}
	if pl.ContactSales {
		return planQuote{}, zip.Errorf(400, "plan %q is sold by quote; contact sales", pl.Slug)
	}
	if pl.Status != "" && !strings.EqualFold(string(pl.Status), "active") {
		return planQuote{}, zip.Errorf(400, "plan %q is not available for purchase", pl.Slug)
	}

	interval := commercetypes.Monthly
	if strings.EqualFold(strings.TrimSpace(in.Interval), string(commercetypes.Yearly)) || strings.EqualFold(strings.TrimSpace(in.Interval), "year") {
		interval = commercetypes.Yearly
	}

	var unitCents int64
	if interval == commercetypes.Yearly {
		if pl.AnnualTotal <= 0 {
			return planQuote{}, zip.Errorf(400, "plan %q does not offer annual billing", pl.Slug)
		}
		unitCents = int64(pl.AnnualTotal)
	} else {
		if in.Level < 0 || (in.Level > 0 && in.Level >= len(pl.Prices)) {
			return planQuote{}, zip.Errorf(400, "invalid price level %d for plan %q", in.Level, pl.Slug)
		}
		unitCents = int64(pl.Price)
		if in.Level > 0 && in.Level < len(pl.Prices) {
			unitCents = int64(pl.Prices[in.Level])
		}
	}
	if unitCents <= 0 {
		return planQuote{}, zip.Errorf(400, "plan %q requires no payment", pl.Slug)
	}

	qty := 1
	if pl.PerSeat {
		if in.Quantity > 0 {
			qty = in.Quantity
		}
		if pl.Limits != nil {
			if pl.Limits.MinSeats != nil && qty < *pl.Limits.MinSeats {
				qty = *pl.Limits.MinSeats
			}
			if pl.Limits.MaxMembers != nil && *pl.Limits.MaxMembers > 0 && qty > *pl.Limits.MaxMembers {
				return planQuote{}, zip.Errorf(400, "quantity %d exceeds plan max seats (%d)", qty, *pl.Limits.MaxMembers)
			}
		}
	}

	chargeCents := unitCents * int64(qty)

	// Apply active platform promo if applicable.
	if pr := promoNow(ctx); pr != nil && pr.Active && pr.PercentOff > 0 {
		if len(pr.Plans) == 0 || slices.ContainsFunc(pr.Plans, func(s string) bool { return strings.EqualFold(s, pl.Slug) }) {
			chargeCents = engine.Discounted(chargeCents, pr.PercentOff)
		}
	}

	couponCode := ""
	if raw := strings.TrimSpace(in.Coupon); raw != "" {
		couponCode = promo.CouponCode(raw)
		if couponCode == "" || interval != commercetypes.Monthly {
			return planQuote{}, zip.Errorf(400, "coupon is not valid for this plan")
		}
		offer, reason := promo.FindCoupon(ctx, couponCode, time.Now())
		if reason == "" && !offer.AppliesTo(pl.Slug) {
			reason = promo.CouponNotApplicable
		}
		if reason != "" {
			return planQuote{}, zip.Errorf(400, "coupon %s is not valid for plan %s (%s)", couponCode, pl.Slug, reason)
		}
		if promo.Redeemed(ctx, couponCode, org.Name) {
			return planQuote{}, zip.Errorf(400, "%v (%s)", promo.ErrCouponRedeemedByOrg, couponCode)
		}
		chargeCents = engine.Discounted(chargeCents, offer.Coupon().Percent)
		if chargeCents <= 0 {
			return planQuote{}, zip.Errorf(400, "coupon leaves nothing to charge")
		}
	}

	return planQuote{
		slug:        pl.Slug,
		qty:         qty,
		interval:    interval,
		coupon:      couponCode,
		chargeCents: chargeCents,
	}, nil
}

// nativeCryptoMint issues or reuses a native custody deposit intent bound to (org, payer)
// without ever routing to Stripe.
func nativeCryptoMint(ctx context.Context, org *organization.Organization, in *client.CryptoMintIn) (*client.CryptoDeposit, error) {
	if org == nil {
		return nil, zip.Errorf(500, "crypto deposit: no organization")
	}
	payer := strings.TrimSpace(in.Payer)
	if payer == "" {
		return nil, zip.Errorf(401, "authentication required")
	}
	if in.AmountCents < 0 {
		RecordCryptoEvent("validation_rejected", in.Chain, in.Token)
		return nil, zip.Errorf(400, "amountCents cannot be negative")
	}

	hasPlan := strings.TrimSpace(in.PlanID) != ""
	chain := strings.ToLower(strings.TrimSpace(in.Chain))
	if chain == "" {
		if hasPlan {
			chain = "base"
		} else {
			chain = "ethereum"
		}
	}
	token := strings.ToLower(strings.TrimSpace(in.Token))
	if token == "" {
		token = "usdc"
	}

	asset, ok := watchedAssetFor(chain, token)
	if !ok {
		RecordCryptoEvent("unavailable", chain, token)
		return nil, zip.Errorf(503, "%s on %s is not accepted yet — nothing is watching that address, so a deposit could not be credited. Use a card, bank transfer or wire.",
			strings.ToUpper(token), chain)
	}

	var existingAll []orgIntent
	var existingAllErr error
	if !asset.Pooled() {
		existingAll, existingAllErr = listAssetIntentsAllOrgs(chain, token)
		if !strings.HasPrefix(strings.ToLower(asset.Contract), "kraken:") && isAssetAmbiguousInList(asset, existingAll, existingAllErr) {
			RecordCryptoEvent("unavailable", chain, token)
			return nil, zip.Errorf(503, "Crypto deposits on %s are temporarily unavailable while custody addresses reconcile. Use another chain or card.", chain)
		}
	}

	var custody processor.CryptoProcessor
	if !asset.Pooled() {
		var err error
		custody, err = nativeCustodyProcessor(ctx, token)
		if err != nil {
			RecordCryptoEvent("unavailable", chain, token)
			return nil, zip.Errorf(503, "Crypto deposits not configured")
		}
		if !nativeSupportedChain(custody, chain) {
			RecordCryptoEvent("validation_rejected", chain, token)
			return nil, zip.Errorf(400, "unsupported chain %q", chain)
		}
	}
	minCents, ready := nativeDepositMinimum(ctx, asset, custody)
	if !ready {
		RecordCryptoEvent("unavailable", chain, token)
		return nil, zip.Errorf(503, "The custody provider's minimum deposit is temporarily unavailable for %s on %s", strings.ToUpper(token), chain)
	}
	amountCents := in.AmountCents
	var pq planQuote
	if hasPlan {
		var perr error
		pq, perr = quotePlanChargeCents(ctx, org, in)
		if perr != nil {
			RecordCryptoEvent("validation_rejected", chain, token)
			return nil, perr
		}
		// Include any asset sweep fee and slippage tolerance in the quoted deposit amount
		// so sending AmountCents always yields net credit >= pq.chargeCents to open the plan.
		amountCents = max(minCents, grossForNet(pq.chargeCents, asset.Terms))
	} else if amountCents > 0 && amountCents < minCents {
		RecordCryptoEvent("validation_rejected", chain, token)
		return nil, zip.Errorf(400, "amountCents (%d) is below the minimum deposit (%d cents) for %s on %s",
			amountCents, minCents, strings.ToUpper(token), chain)
	}

	db := commercedatastore.New(org.Namespaced(ctx))
	contract := canonicalContract(chain, token, asset.Contract)

	// Reuse the caller's own open, unexpired intent for the same (payer, chain, token, planId)
	// so page refreshes neither spray MPC keygens nor exhaust pooled destination tags.
	if existing := reusableIntentFor(ctx, db, asset, payer, chain, token, pq.slug, existingAll, existingAllErr); existing != nil {
		minimum, _ := existing.Metadata["minimumCents"].(string)
		minimumChanged := minimum != strconv.FormatInt(minCents, 10)
		amountChanged := amountCents > 0 && existing.Amount != amountCents
		if amountChanged {
			existing.Amount = amountCents
			if hasPlan {
				applyPlanMetadata(existing, pq, in.Level)
			}
		}
		if minimumChanged {
			if existing.Metadata == nil {
				existing.Metadata = commercetypes.Map{}
			}
			existing.Metadata["minimumCents"] = strconv.FormatInt(minCents, 10)
		}
		if minimumChanged || amountChanged {
			if err := existing.Update(); err != nil {
				return nil, zip.Errorf(503, "could not update crypto deposit terms")
			}
		}
		return intentToClientDeposit(existing, asset, contract), nil
	}

	var wallet processor.Wallet
	var tag string
	if asset.Pooled() {
		if strings.TrimSpace(asset.PooledAddress) == "" {
			RecordCryptoEvent("unavailable", chain, token)
			return nil, zip.Errorf(503, "Crypto deposits on %s are not configured — no custody account is set for %s.", chain, strings.ToUpper(token))
		}
		var err error
		tag, err = depositledger.NextTag(ctx, chain)
		if err != nil {
			RecordCryptoEvent("unavailable", chain, token)
			return nil, zip.Errorf(503, "Crypto deposits are temporarily unavailable — a deposit reference could not be issued. Try again shortly or choose another way to pay.")
		}
		wallet = processor.Wallet{Address: asset.PooledAddress}
	} else {
		mintCtx := processor.WithAddressFree(processor.WithToken(ctx, token), func(addr string) bool {
			return !addressClaimedInList(existingAll, existingAllErr, asset, addr, org.Name, payer, "")
		})
		var err error
		wallet, err = custody.GenerateAddress(mintCtx, payer, chain)
		if err != nil {
			RecordCryptoEvent("unavailable", chain, token)
			return nil, zip.Errorf(503, "Crypto deposits are temporarily unavailable — the custody service is not accepting new addresses. Try again shortly or choose another way to pay.")
		}
		if tag == "" && wallet.Tag != "" {
			tag = wallet.Tag
		}
	}

	if strings.TrimSpace(wallet.Address) == "" || (asset.Pooled() && tag == "") {
		RecordCryptoEvent("unavailable", chain, token)
		return nil, zip.Errorf(503, "Crypto deposits are temporarily unavailable — incomplete custody destination.")
	}

	intent := cryptopaymentintent.New(db)
	intent.Amount = amountCents
	intent.Currency = "usd"
	intent.Chain = cryptopaymentintent.Chain(chain)
	intent.Token = token
	intent.DepositAddress = wallet.Address
	intent.AddressTag = tag
	intent.WalletID = wallet.ID
	intent.CustomerRef = payer
	intent.Status = cryptopaymentintent.Pending
	intent.ExpiresAt = time.Now().Add(24 * time.Hour)
	intent.Metadata = commercetypes.Map{
		"org":          org.Name,
		"payer":        payer,
		"contract":     contract,
		"minimumCents": strconv.FormatInt(minCents, 10),
	}
	if hasPlan {
		applyPlanMetadata(intent, pq, in.Level)
	}
	intent.Defaults()

	if err := intent.Create(); err != nil {
		return nil, zip.Errorf(500, "failed to record deposit intent")
	}

	if !asset.Pooled() && addressClaimedElsewhere(ctx, asset, wallet.Address, org.Name, payer, intent.Id()) {
		// Withdraw colliding intent immediately and remove DepositAddress so depositledger's
		// cross-org watcher never sees two payers claiming one address.
		intent.Status = cryptopaymentintent.Expired
		intent.Metadata["withdrawnCollisionAddress"] = intent.DepositAddress
		intent.DepositAddress = ""
		_ = intent.Update()
		RecordCryptoEvent("unavailable", chain, token)
		return nil, zip.Errorf(503, "Crypto deposits are temporarily unavailable — no unique deposit address is free right now. Try again shortly or choose another way to pay.")
	}

	RecordCryptoEvent("minted", chain, token)
	return intentToClientDeposit(intent, asset, contract), nil
}

func applyPlanMetadata(intent *cryptopaymentintent.CryptoPaymentIntent, pq planQuote, level int) {
	if intent.Metadata == nil {
		intent.Metadata = commercetypes.Map{}
	}
	intent.Metadata["planId"] = pq.slug
	intent.Metadata["quantity"] = pq.qty
	intent.Metadata["level"] = level
	intent.Metadata["interval"] = string(pq.interval)
	intent.Metadata["coupon"] = pq.coupon
	intent.Metadata["chargeCents"] = pq.chargeCents
}

func reusableIntentFor(ctx context.Context, db *commercedatastore.Datastore, asset depositwatch.Asset, payer, chain, token, wantPlan string, existingAll []orgIntent, existingAllErr error) *cryptopaymentintent.CryptoPaymentIntent {
	rows := make([]*cryptopaymentintent.CryptoPaymentIntent, 0)
	keys, err := cryptopaymentintent.Query(db).
		Filter("CustomerRef=", payer).
		Filter("Chain=", chain).
		Filter("Token=", token).
		Filter("Status=", string(cryptopaymentintent.Pending)).
		GetAll(&rows)
	if err != nil {
		return nil
	}
	for idx, r := range rows {
		if idx >= len(keys) {
			break
		}
		loaded := cryptopaymentintent.New(db)
		if gerr := loaded.GetById(db.EncodeKey(keys[idx])); gerr != nil {
			continue
		}
		r = loaded
		if r.IsExpired() || strings.TrimSpace(r.DepositAddress) == "" {
			continue
		}
		if asset.Pooled() && strings.TrimSpace(r.AddressTag) == "" {
			continue
		}
		gotPlan := ""
		if r.Metadata != nil {
			gotPlan, _ = r.Metadata["planId"].(string)
		}
		if !strings.EqualFold(strings.TrimSpace(gotPlan), strings.TrimSpace(wantPlan)) {
			continue
		}
		orgName := ""
		if r.Metadata != nil {
			orgName, _ = r.Metadata["org"].(string)
		}
		if !asset.Pooled() && addressClaimedInList(existingAll, existingAllErr, asset, r.DepositAddress, orgName, payer, r.Id()) {
			continue
		}
		return r
	}
	return nil
}

// nativeCryptoDeposit reads one payer-owned native deposit intent back, advancing
// unfunded intents past ExpiresAt to status "expired" cleanly.
func nativeCryptoDeposit(ctx context.Context, org *organization.Organization, payer, id string) (*client.CryptoDeposit, error) {
	if org == nil || strings.TrimSpace(payer) == "" || strings.TrimSpace(id) == "" {
		return nil, zip.Errorf(404, "deposit not found")
	}
	// Never expose processor PaymentIntents (pi_...) on the native deposit endpoint.
	if strings.HasPrefix(strings.TrimSpace(id), "pi_") {
		return nil, zip.Errorf(404, "deposit not found")
	}
	db := commercedatastore.New(org.Namespaced(ctx))
	intent := cryptopaymentintent.New(db)
	if err := intent.GetById(strings.TrimSpace(id)); err != nil {
		return nil, zip.Errorf(404, "deposit not found")
	}
	if intent.CustomerRef != payer {
		return nil, zip.Errorf(404, "deposit not found")
	}
	// The background watcher updates this row. Request polling only reads its state.
	if intent.Status == cryptopaymentintent.Pending && strings.TrimSpace(intent.TxHash) == "" &&
		intent.SettlementAmount == 0 && intent.IsExpired() {
		intent.Status = cryptopaymentintent.Expired
		_ = intent.Update()
		RecordCryptoEvent("expired", string(intent.Chain), intent.Token)
	}

	asset, ok := watchedAssetFor(string(intent.Chain), intent.Token)
	contract := ""
	if intent.Metadata != nil {
		contract, _ = intent.Metadata["contract"].(string)
	}
	if contract == "" {
		contract = asset.Contract
	}
	contract = canonicalContract(string(intent.Chain), intent.Token, contract)
	if !ok {
		asset = depositwatch.Asset{Chain: string(intent.Chain), Token: intent.Token}
	}
	return intentToClientDeposit(intent, asset, contract), nil
}

func intentToClientDeposit(i *cryptopaymentintent.CryptoPaymentIntent, asset depositwatch.Asset, contract string) *client.CryptoDeposit {
	d := commercebilling.CryptoDeposit{
		ID:                    i.Id(),
		Status:                string(i.Status),
		Chain:                 string(i.Chain),
		Token:                 i.Token,
		DepositAddress:        i.DepositAddress,
		AddressTag:            i.AddressTag,
		ExpiresAt:             i.ExpiresAt.UTC().Format(time.RFC3339),
		AmountCents:           i.Amount,
		RequiredConfirmations: effectiveConfirmations(asset),
		Confirmations:         i.Confirmations,
	}
	if i.Status == cryptopaymentintent.Succeeded {
		d.CreditedCents = i.SettlementAmount
	}
	var sweptTx string
	var logIdx uint64
	if i.Metadata != nil {
		if p, _ := i.Metadata["planId"].(string); p != "" {
			d.PlanID = p
		}
		if s, _ := i.Metadata["subscriptionId"].(string); s != "" {
			d.SubscriptionID = s
		}
		sweptTx, _ = i.Metadata["sweptTxHash"].(string)
		switch v := i.Metadata["logIndex"].(type) {
		case uint64:
			logIdx = v
		case int64:
			if v >= 0 {
				logIdx = uint64(v)
			}
		case float64:
			if v >= 0 {
				logIdx = uint64(v)
			}
		case string:
			if parsed, err := strconv.ParseUint(v, 10, 64); err == nil {
				logIdx = parsed
			}
		}
	}
	if _, watched := watchedAssetFor(string(i.Chain), i.Token); watched {
		d.FeeCents = asset.Terms.FeeCents
		if !strings.HasPrefix(i.WalletID, "kraken:") {
			d.MinimumCents = minimumDepositCents(asset.Terms)
		}
	}
	savedMinimum, _ := i.Metadata["minimumCents"].(string)
	minimum, _ := strconv.ParseInt(savedMinimum, 10, 64)
	d.MinimumCents = max(d.MinimumCents, minimum)

	state := string(i.Status)
	switch {
	case sweptTx != "":
		state = "swept"
	case i.Status == cryptopaymentintent.Succeeded:
		state = "credited"
	case i.Status == cryptopaymentintent.Confirming:
		state = "confirming"
	case i.Status == cryptopaymentintent.Expired:
		state = "expired"
	default:
		state = "pending"
	}

	out := cryptoRow(d)
	out.Contract = contract
	out.TxHash = i.TxHash
	out.LogIndex = logIdx
	out.SettlementState = state
	out.SweptTxHash = sweptTx
	return out
}
