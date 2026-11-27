// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// crypto_reconcile.go — cross-org custody identity protection, expired shared-address
// collision healing, durable (deposit_id, tx_hash, log_index) & (chain, tx_hash, log_index)
// idempotent exactly-once settlement, and sweep tracking.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/commerce/billing/depositledger"
	"github.com/hanzoai/commerce/billing/depositwatch"
	"github.com/hanzoai/commerce/billing/engine"
	commercedatastore "github.com/hanzoai/commerce/datastore"
	"github.com/hanzoai/commerce/mintauth"
	"github.com/hanzoai/commerce/models/cryptopaymentintent"
	"github.com/hanzoai/commerce/models/organization"
	"github.com/hanzoai/commerce/models/paymentorg"
	commerceplan "github.com/hanzoai/commerce/models/plan"
	"github.com/hanzoai/commerce/models/subscription"
	"github.com/hanzoai/commerce/models/transaction"
	"github.com/hanzoai/commerce/models/types/currency"
	commerceorg "github.com/hanzoai/commerce/pkg/org"
	commercetypes "github.com/hanzoai/commerce/types"
	commercensctx "github.com/hanzoai/commerce/util/nscontext"
	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/internal/funds"
)

var (
	// ErrDepositNotOwned indicates the deposit intent does not exist or belongs to another org/payer.
	ErrDepositNotOwned = errors.New("crypto deposit: not found for organization and payer")
	// ErrDepositAssetMismatch indicates the transfer arrived on the wrong chain, token, or contract.
	ErrDepositAssetMismatch = errors.New("crypto deposit: chain, token, or contract mismatch")
	// ErrDepositBelowMinimum indicates the transfer does not clear the asset's minimum + fee.
	ErrDepositBelowMinimum = errors.New("crypto deposit: transfer amount is below minimum")
	// ErrDuplicateDepositTx indicates (deposit_id, tx_hash, log_index) or (chain, tx_hash, log_index) was already credited.
	ErrDuplicateDepositTx = errors.New("crypto deposit: duplicate or replayed transaction event")
)

// DepositReceiptIn describes an observed on-chain transfer against a payer's deposit intent.
type DepositReceiptIn struct {
	Org           string
	Payer         string
	DepositID     string
	Chain         string
	Token         string
	Contract      string
	TxHash        string
	LogIndex      uint64
	Block         uint64
	Confirmations int
	Units         *big.Int // raw token base units
	Decimals      int      // token decimals on chain (e.g. 6 for USDC)
}

// DepositReceiptOut reports the settlement outcome for one observed transfer.
type DepositReceiptOut struct {
	DepositID       string
	Status          string
	SettlementState string
	CreditedCents   int64
	WalletCents     int64
	SubscriptionID  string
	TransactionID   string
	Duplicate       bool
}

type orgIntent struct {
	org    string
	db     *commercedatastore.Datastore
	intent *cryptopaymentintent.CryptoPaymentIntent
}

func listAssetIntentsAllOrgs(chain, token string) ([]orgIntent, error) {
	orgs := make([]*organization.Organization, 0)
	if _, err := organization.Query(commercedatastore.New(context.Background())).GetAll(&orgs); err != nil {
		return nil, err
	}
	var out []orgIntent
	for _, o := range orgs {
		name := strings.TrimSpace(o.Name)
		if name == "" {
			continue
		}
		db := commercedatastore.New(commercensctx.WithNamespace(context.Background(), name))
		rows := make([]*cryptopaymentintent.CryptoPaymentIntent, 0)
		keys, err := cryptopaymentintent.Query(db).
			Filter("Chain=", strings.ToLower(strings.TrimSpace(chain))).
			Filter("Token=", strings.ToLower(strings.TrimSpace(token))).
			GetAll(&rows)
		if err != nil {
			return nil, err
		}
		for idx := range rows {
			if idx >= len(keys) {
				break
			}
			loaded := cryptopaymentintent.New(db)
			if gerr := loaded.GetById(db.EncodeKey(keys[idx])); gerr != nil {
				continue
			}
			out = append(out, orgIntent{org: name, db: db, intent: loaded})
		}
	}
	return out, nil
}

// addressClaimedElsewhere reports whether any deposit intent across ANY organization
// (other than exceptID or the same (orgName, payer)) already holds `address` on `asset`.
// Because depositledger.Watched scans all orgs and all statuses, sharing a non-pooled
// address across two distinct subjects wedges the asset's RPC scan pass.
func addressClaimedElsewhere(_ context.Context, asset depositwatch.Asset, address, orgName, payer, exceptID string) bool {
	if asset.Pooled() || strings.TrimSpace(address) == "" {
		return false
	}
	target := asset.Identity(address, "")
	all, err := listAssetIntentsAllOrgs(asset.Chain, asset.Token)
	if err != nil {
		return true // fail closed if datastore cannot be enumerated
	}
	for _, item := range all {
		r := item.intent
		if r.Id() == exceptID || strings.TrimSpace(r.DepositAddress) == "" {
			continue
		}
		if asset.Identity(r.DepositAddress, r.AddressTag) != target {
			continue
		}
		if !strings.EqualFold(item.org, orgName) || r.CustomerRef != payer {
			return true
		}
		// Even for the same payer, do not reuse an address bound to a different open intent.
		if (r.Status == cryptopaymentintent.Pending || r.Status == cryptopaymentintent.Confirming) && !r.IsExpired() {
			return true
		}
	}
	return false
}

// isAssetAmbiguous reports whether two distinct subjects still claim the same non-pooled
// deposit identity across organizations for `asset`.
func isAssetAmbiguous(_ context.Context, asset depositwatch.Asset) bool {
	if asset.Pooled() {
		return false
	}
	all, err := listAssetIntentsAllOrgs(asset.Chain, asset.Token)
	if err != nil {
		return true
	}
	ownerByID := map[string]string{}
	for _, item := range all {
		r := item.intent
		if strings.TrimSpace(r.DepositAddress) == "" || strings.TrimSpace(r.CustomerRef) == "" {
			continue
		}
		id := asset.Identity(r.DepositAddress, r.AddressTag)
		owner := strings.ToLower(item.org) + "\x00" + r.CustomerRef
		if prev, exists := ownerByID[id]; exists && prev != owner {
			return true
		}
		ownerByID[id] = owner
	}
	return false
}

// healAllWatchedCollisions retires unfunded, expired duplicate Kraken shared-address
// intents across all currently watched assets so depositledger's RPC scanner never
// stays wedged behind stale exchange-address rotations.
func healAllWatchedCollisions(ctx context.Context) {
	svc := depositledger.Default()
	if svc == nil {
		return
	}
	for _, a := range svc.Assets() {
		healExpiredSharedAddresses(ctx, a.Chain, a.Token)
	}
}

// healExpiredSharedAddresses detaches DepositAddress (archiving it in Metadata) on
// unfunded, zero-tx exchange-shared intents when multiple subjects hold the same
// address, keeping the primary/funded holder intact so the address remains watched.
func healExpiredSharedAddresses(_ context.Context, chain, token string) int {
	asset := depositwatch.Asset{Chain: strings.ToLower(strings.TrimSpace(chain)), Token: strings.ToLower(strings.TrimSpace(token))}
	if asset.Pooled() {
		return 0
	}
	all, err := listAssetIntentsAllOrgs(asset.Chain, asset.Token)
	if err != nil || len(all) < 2 {
		return 0
	}

	byAddr := map[string][]orgIntent{}
	for _, item := range all {
		addr := strings.TrimSpace(item.intent.DepositAddress)
		if addr == "" {
			continue
		}
		id := asset.Identity(addr, item.intent.AddressTag)
		byAddr[id] = append(byAddr[id], item)
	}

	retired := 0
	now := time.Now()
	for _, group := range byAddr {
		subjects := map[string]bool{}
		for _, item := range group {
			subjects[strings.ToLower(item.org)+"\x00"+item.intent.CustomerRef] = true
		}
		if len(subjects) <= 1 {
			continue
		}
		// Pick the keeper: prefer any funded/confirming intent, else any unexpired pending
		// intent, else the earliest created intent.
		keeperIdx := 0
		for i := 1; i < len(group); i++ {
			cur, best := group[i].intent, group[keeperIdx].intent
			curActive := strings.TrimSpace(cur.TxHash) != "" || cur.SettlementAmount > 0 || cur.Status == cryptopaymentintent.Succeeded || cur.Status == cryptopaymentintent.Confirming
			bestActive := strings.TrimSpace(best.TxHash) != "" || best.SettlementAmount > 0 || best.Status == cryptopaymentintent.Succeeded || best.Status == cryptopaymentintent.Confirming
			if curActive && !bestActive {
				keeperIdx = i
				continue
			}
			if curActive == bestActive {
				curLive := cur.Status == cryptopaymentintent.Pending && now.Before(cur.ExpiresAt)
				bestLive := best.Status == cryptopaymentintent.Pending && now.Before(best.ExpiresAt)
				if curLive && !bestLive {
					keeperIdx = i
					continue
				}
				if curLive == bestLive && cur.CreatedAt.Before(best.CreatedAt) {
					keeperIdx = i
				}
			}
		}
		keeperOwner := strings.ToLower(group[keeperIdx].org) + "\x00" + group[keeperIdx].intent.CustomerRef
		for i, item := range group {
			if i == keeperIdx {
				continue
			}
			owner := strings.ToLower(item.org) + "\x00" + item.intent.CustomerRef
			if owner == keeperOwner {
				continue
			}
			in := item.intent
			// NEVER detach an intent that has any sighted/settled tx or non-Kraken MPC key unless expired & unfunded.
			if strings.TrimSpace(in.TxHash) != "" || in.SettlementAmount > 0 ||
				in.Status == cryptopaymentintent.Succeeded || in.Status == cryptopaymentintent.Confirming {
				continue
			}
			isKraken := strings.HasPrefix(strings.ToLower(in.WalletID), "kraken:")
			isExpired := in.Status == cryptopaymentintent.Expired || now.After(in.ExpiresAt)
			if !isKraken && !isExpired {
				continue
			}
			if in.Metadata == nil {
				in.Metadata = commercetypes.Map{}
			}
			in.Metadata["retiredSharedAddress"] = in.DepositAddress
			in.DepositAddress = ""
			in.Status = cryptopaymentintent.Expired
			if err := in.Update(); err == nil {
				retired++
				luxlog.Default().Info("retired duplicate unfunded shared deposit intent",
					"org", item.org, "intent", in.Id(), "chain", chain, "token", token)
			}
		}
	}
	return retired
}

func eventCreditKey(db *commercedatastore.Datastore, dedup string) commercedatastore.Key {
	sum := sha256.Sum256([]byte("crypto-deposit\x00" + dedup))
	root := db.NewKey("synckey", "", 1, nil)
	return db.NewKey("transaction", "cdep_"+hex.EncodeToString(sum[:16]), 0, root)
}

func depositReceiptKey(db *commercedatastore.Datastore, depositID, txHash string, logIndex uint64) commercedatastore.Key {
	raw := fmt.Sprintf("deposit-receipt\x00%s\x00%s\x00%d", strings.TrimSpace(depositID), strings.ToLower(strings.TrimSpace(txHash)), logIndex)
	sum := sha256.Sum256([]byte(raw))
	root := db.NewKey("synckey", "", 1, nil)
	return db.NewKey("transaction", "drcpt_"+hex.EncodeToString(sum[:16]), 0, root)
}

func valueTransferCents(units *big.Int, decimals int, pegCents int64, terms depositwatch.Terms) int64 {
	if units == nil || units.Sign() <= 0 || decimals <= 0 {
		return 0
	}
	if pegCents <= 0 {
		pegCents = 100
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	gross := new(big.Int).Mul(units, big.NewInt(pegCents))
	gross.Div(gross, scale)
	if !gross.IsInt64() {
		return 0
	}
	g := gross.Int64()
	if g <= 0 {
		return 0
	}
	afterSlip := (g * int64(10_000-terms.SlippageBps)) / 10_000
	net := afterSlip - terms.FeeCents
	if net <= 0 {
		return 0
	}
	return net
}

var reconcileMu sync.Mutex

// RecordConfirmedDeposit validates (org, payer, deposit_id, chain, token, contract, minimum,
// confirmations) and posts an idempotent, exactly-once Cash credit (and plan activation if
// plan-bound). Replaying an already-credited (chain, tx_hash, log_index) or
// (deposit_id, tx_hash, log_index) returns ErrDuplicateDepositTx with zero additional credit.
func RecordConfirmedDeposit(ctx context.Context, in DepositReceiptIn) (*DepositReceiptOut, error) {
	reconcileMu.Lock()
	defer reconcileMu.Unlock()

	orgName := strings.TrimSpace(in.Org)
	payer := strings.TrimSpace(in.Payer)
	depID := strings.TrimSpace(in.DepositID)
	if orgName == "" || payer == "" || depID == "" {
		return nil, ErrDepositNotOwned
	}
	org, err := commerceorg.Resolve(ctx, orgName)
	if err != nil || org == nil {
		return nil, ErrDepositNotOwned
	}

	db := commercedatastore.New(org.Namespaced(ctx))
	intent := cryptopaymentintent.New(db)
	if err := intent.GetById(depID); err != nil || intent.CustomerRef != payer {
		return nil, ErrDepositNotOwned
	}

	chain := strings.ToLower(strings.TrimSpace(in.Chain))
	token := strings.ToLower(strings.TrimSpace(in.Token))
	if !strings.EqualFold(string(intent.Chain), chain) || !strings.EqualFold(intent.Token, token) {
		RecordCryptoEvent("validation_rejected", chain, token)
		return nil, fmt.Errorf("%w: intent expects %s/%s, got %s/%s", ErrDepositAssetMismatch, intent.Chain, intent.Token, chain, token)
	}

	asset, watched := watchedAssetFor(chain, token)
	if !watched {
		asset = depositwatch.Asset{Chain: chain, Token: token}
	}
	expectedContract := ""
	if intent.Metadata != nil {
		expectedContract, _ = intent.Metadata["contract"].(string)
	}
	if expectedContract == "" {
		expectedContract = canonicalContract(chain, token, asset.Contract)
	}
	gotContract := strings.TrimSpace(in.Contract)
	if expectedContract != "" && gotContract != "" {
		same := expectedContract == gotContract
		if asset.Family() == depositwatch.FamilyEVM {
			same = strings.EqualFold(expectedContract, gotContract)
		}
		if !same {
			RecordCryptoEvent("validation_rejected", chain, token)
			return nil, fmt.Errorf("%w: contract %q does not match %q", ErrDepositAssetMismatch, gotContract, expectedContract)
		}
	}

	txHash := asset.Fold(strings.TrimSpace(in.TxHash))
	if txHash == "" {
		RecordCryptoEvent("validation_rejected", chain, token)
		return nil, errors.New("crypto deposit: missing transaction hash")
	}

	decimals := in.Decimals
	if decimals <= 0 {
		decimals = tokenDecimals(asset)
	}
	netCents := valueTransferCents(in.Units, decimals, asset.PegCents(), asset.Terms)
	if netCents <= 0 {
		RecordCryptoEvent("validation_rejected", chain, token)
		return nil, ErrDepositBelowMinimum
	}

	reqConf := effectiveConfirmations(asset)
	if in.Confirmations < reqConf {
		if intent.Status == cryptopaymentintent.Pending {
			_ = intent.MarkConfirming(txHash, int64(in.Block))
		}
		if intent.Status == cryptopaymentintent.Confirming && strings.EqualFold(intent.TxHash, txHash) {
			intent.Confirmations = in.Confirmations
			intent.BlockNumber = int64(in.Block)
			if intent.Metadata == nil {
				intent.Metadata = commercetypes.Map{}
			}
			intent.Metadata["logIndex"] = in.LogIndex
			_ = intent.Update()
		}
		RecordCryptoEvent("confirming", chain, token)
		return &DepositReceiptOut{
			DepositID:       intent.Id(),
			Status:          string(cryptopaymentintent.Confirming),
			SettlementState: "confirming",
		}, nil
	}

	dedup := fmt.Sprintf("%s:%s:%d", chain, txHash, in.LogIndex)
	evKey := eventCreditKey(db, dedup)
	rcptKey := depositReceiptKey(db, intent.Id(), txHash, in.LogIndex)

	existing := transaction.New(db)
	if err := existing.Get(evKey); err == nil {
		RecordCryptoEvent("replay_rejected", chain, token)
		subID := ""
		if existing.Metadata != nil {
			subID, _ = existing.Metadata["subscriptionId"].(string)
		}
		return &DepositReceiptOut{
			DepositID:       intent.Id(),
			Status:          string(intent.Status),
			SettlementState: "credited",
			CreditedCents:   int64(existing.Amount),
			SubscriptionID:  subID,
			TransactionID:   existing.Id(),
			Duplicate:       true,
		}, ErrDuplicateDepositTx
	} else if !errors.Is(err, commercedatastore.ErrNoSuchEntity) {
		return nil, fmt.Errorf("crypto deposit: check event receipt: %w", err)
	}

	dupRcpt := transaction.New(db)
	if err := dupRcpt.Get(rcptKey); err == nil {
		RecordCryptoEvent("replay_rejected", chain, token)
		return &DepositReceiptOut{
			DepositID:       intent.Id(),
			Status:          string(intent.Status),
			SettlementState: "credited",
			CreditedCents:   int64(dupRcpt.Amount),
			TransactionID:   dupRcpt.Id(),
			Duplicate:       true,
		}, ErrDuplicateDepositTx
	} else if !errors.Is(err, commercedatastore.ErrNoSuchEntity) {
		return nil, fmt.Errorf("crypto deposit: check deposit receipt: %w", err)
	}

	var subID string
	var consumed int64
	if intent.Metadata != nil {
		if pid, _ := intent.Metadata["planId"].(string); strings.TrimSpace(pid) != "" {
			var perr error
			subID, consumed, perr = activateIntentPlan(ctx, org, db, payer, intent, netCents, txHash)
			if perr != nil {
				return nil, perr
			}
		}
	}

	walletCents := netCents - consumed
	if walletCents > 0 {
		reason := fmt.Sprintf("crypto deposit %s %s on %s (%s)", in.Units.String(), strings.ToUpper(token), chain, txHash)
		if err := settleCryptoDepositCash(ctx, strings.ToLower(orgName), payer, reason, "crypto-deposit:"+dedup, walletCents, org.TestMode()); err != nil {
			return nil, fmt.Errorf("crypto deposit: credit ledger: %w", err)
		}
	}

	trans := transaction.New(db)
	if err := trans.SetKey(evKey); err != nil {
		return nil, err
	}
	trans.Type = transaction.Deposit
	trans.DestinationId = payer
	trans.DestinationKind = transaction.IAMUserKind
	trans.Currency = currency.Type("usd")
	trans.Amount = currency.Cents(netCents)
	trans.Tags = "crypto-deposit"
	trans.Test = org.TestMode()
	trans.Notes = fmt.Sprintf("Crypto deposit: %s %s on %s (%s)", in.Units.String(), strings.ToUpper(token), chain, txHash)
	trans.Metadata = commercetypes.Map{
		"source":        "crypto-deposit",
		"org":           orgName,
		"payer":         payer,
		"chain":         chain,
		"token":         token,
		"contract":      expectedContract,
		"txHash":        txHash,
		"eventIndex":    in.LogIndex,
		"logIndex":      in.LogIndex,
		"blockNumber":   in.Block,
		"confirmations": in.Confirmations,
		"units":         in.Units.String(),
		"rate":          asset.PegRate(),
		"intentId":      intent.Id(),
	}
	if subID != "" {
		trans.Metadata["subscriptionId"] = subID
		trans.Metadata["planConsumedCents"] = consumed
	}
	trans.SetContext(mintauth.WithAuthorized(trans.Context()))
	if err := trans.Create(); err != nil {
		return nil, fmt.Errorf("crypto deposit: write receipt: %w", err)
	}

	// Advance intent state to Succeeded ("credited").
	if intent.Status != cryptopaymentintent.Succeeded && intent.Status != cryptopaymentintent.Refunded {
		if intent.Status != cryptopaymentintent.Confirming {
			_ = intent.MarkConfirming(txHash, int64(in.Block))
		}
		intent.RequiredConfirmations = reqConf
		intent.Confirmations = in.Confirmations
		intent.BlockNumber = int64(in.Block)
		intent.CryptoAmount = in.Units.String()
		intent.SettlementCurrency = "usd"
		if intent.Metadata == nil {
			intent.Metadata = commercetypes.Map{}
		}
		intent.Metadata["logIndex"] = in.LogIndex
		if subID != "" {
			intent.Metadata["subscriptionId"] = subID
		}
		_ = intent.MarkSucceeded(netCents, asset.PegRate())
		_ = intent.Update()
	}

	if subID != "" {
		told(org.Name, funds.Plan)
		mirrorOrgSubscriptions(org)
		mirrorOrgInvoices(org)
		mirrorOrgBook(org.Name)
	}
	RecordCryptoEvent("credited", chain, token)

	return &DepositReceiptOut{
		DepositID:       intent.Id(),
		Status:          string(intent.Status),
		SettlementState: "credited",
		CreditedCents:   netCents,
		WalletCents:     walletCents,
		SubscriptionID:  subID,
		TransactionID:   trans.Id(),
	}, nil
}

// MarkDepositSwept records the treasury sweep transaction hash on a credited deposit intent,
// advancing SettlementState from "credited" to "swept".
func MarkDepositSwept(ctx context.Context, orgName, payer, depositID, sweepTxHash string) error {
	reconcileMu.Lock()
	defer reconcileMu.Unlock()

	org, err := commerceorg.Resolve(ctx, strings.TrimSpace(orgName))
	if err != nil || org == nil {
		return ErrDepositNotOwned
	}
	db := commercedatastore.New(org.Namespaced(ctx))
	intent := cryptopaymentintent.New(db)
	if err := intent.GetById(strings.TrimSpace(depositID)); err != nil || intent.CustomerRef != strings.TrimSpace(payer) {
		return ErrDepositNotOwned
	}
	if intent.Status != cryptopaymentintent.Succeeded {
		return errors.New("crypto deposit: only credited deposits can be marked swept")
	}
	if intent.Metadata == nil {
		intent.Metadata = commercetypes.Map{}
	}
	intent.Metadata["sweptTxHash"] = strings.TrimSpace(sweepTxHash)
	intent.Metadata["sweptAt"] = time.Now().UTC().Format(time.RFC3339)
	if err := intent.Update(); err != nil {
		return err
	}
	RecordCryptoEvent("swept", string(intent.Chain), intent.Token)
	return nil
}

func activateIntentPlan(ctx context.Context, org *organization.Organization, db *commercedatastore.Datastore, subject string, intent *cryptopaymentintent.CryptoPaymentIntent, depositedCents int64, txHash string) (string, int64, error) {
	planID, _ := intent.Metadata["planId"].(string)
	planID = strings.TrimSpace(planID)
	if planID == "" {
		return "", 0, nil
	}
	if existingSub, _ := intent.Metadata["subscriptionId"].(string); existingSub != "" {
		var charge int64
		switch v := intent.Metadata["chargeCents"].(type) {
		case int64:
			charge = v
		case float64:
			charge = int64(v)
		}
		return existingSub, charge, nil
	}

	chargeCents := intent.Amount
	switch v := intent.Metadata["chargeCents"].(type) {
	case int64:
		if v > 0 {
			chargeCents = v
		}
	case float64:
		if int64(v) > 0 {
			chargeCents = int64(v)
		}
	}
	if chargeCents <= 0 || depositedCents < chargeCents {
		// Underpaid deposit: credit the entire received amount to Cash balance so zero funds are stranded.
		return "", 0, nil
	}

	pl := commerceplan.New(commerceplan.AuthorityDB(ctx))
	found, err := pl.Query().Filter("Slug=", planID).Get()
	if err != nil || !found {
		if gerr := pl.GetById(planID); gerr != nil {
			return "", 0, fmt.Errorf("resolve plan %q: %w", planID, gerr)
		}
	}

	qty := 1
	switch v := intent.Metadata["quantity"].(type) {
	case int:
		if v > 0 {
			qty = v
		}
	case float64:
		if int(v) > 0 {
			qty = int(v)
		}
	}
	interval := commercetypes.Monthly
	if iv, _ := intent.Metadata["interval"].(string); iv == string(commercetypes.Yearly) || iv == "year" {
		interval = commercetypes.Yearly
	}

	now := time.Now().UTC()
	periodEnd := now.AddDate(0, 1, 0)
	if interval == commercetypes.Yearly {
		periodEnd = now.AddDate(1, 0, 0)
	}

	sub := subscription.New(db)
	sub.UserId = subject
	sub.PlanId = pl.Slug
	sub.Status = subscription.Active
	sub.Quantity = qty
	sub.ProviderType = "credit"
	sub.DefaultPaymentMethod = "credits"
	sub.PeriodStart = now
	sub.PeriodEnd = periodEnd
	sub.Test = org.TestMode()
	sub.Plan = *pl
	sub.Plan.Interval = interval
	sub.Plan.Price = currency.Cents(chargeCents / int64(max(qty, 1)))
	sub.Metadata = commercetypes.Map{
		"source":   "subscribe/crypto",
		"intentId": intent.Id(),
		"txHash":   txHash,
	}
	if err := sub.Create(); err != nil {
		return "", 0, err
	}
	engine.Grant(sub, engine.Coupon{}, nil, sub.PeriodStart)
	if inv, ierr := engine.CreatePaidFirstInvoice(db, sub, "crypto", txHash); ierr == nil && inv != nil {
		_ = paymentorg.Put(txHash, org.Name, subject, inv.Id(), 0, org.TestMode())
	}
	return sub.Id(), chargeCents, nil
}
