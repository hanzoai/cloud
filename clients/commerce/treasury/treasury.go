package treasury

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/mintauth"
	"github.com/hanzoai/cloud/clients/commerce/util/blockchain"
	"github.com/hanzoai/cloud/clients/commerce/util/husd"
)

// Step 2: the mint service. treasury.Mint is the ONE way a credit comes into
// existence in the chain-backed ledger — a treasury-signed on-chain HUSD
// issuance to the org's derived address. Commerce holds NO mint key: the value
// is created by the KMS treasury key inside util/blockchain.TransferToken, not by
// any DB write. Mint additionally enforces the mintauth policy (a gated HTTP
// caller must carry proven authority), so a compromised or low-privilege request
// can only ever REQUEST a mint; two independent locks (mint authority + custody
// of the KMS key) must both hold for money to be created.

// Bucket names which billing bucket a mint funds. On-chain HUSD is fungible and
// a Transfer event carries no tag, so the credit-vs-prepaid distinction is
// recorded HERE, off-chain, at issuance time; the indexer (step 3) reads it back
// to tag the projected ledger deposit (billing/bucket classifies from that tag).
type Bucket string

const (
	// BucketCredit is a non-cash grant (welcome/starter/promo). Spendable on
	// non-GPU usage only (billing/bucket.Credit).
	BucketCredit Bucket = "credit"
	// BucketPrepaid is real money paid in (card top-up, crypto payin, migration
	// of a real balance). The only bucket GPUs may draw from (billing/bucket.Prepaid).
	BucketPrepaid Bucket = "prepaid"
)

// Valid reports whether b is a known bucket.
func (b Bucket) Valid() bool { return b == BucketCredit || b == BucketPrepaid }

// LedgerTag maps a bucket to the transaction Tags string the indexer writes so
// billing/bucket.DepositKind classifies the projected deposit into the same
// bucket. Credit → "credit:husd" (a grant prefix bucket.DepositKind treats as
// Credit); Prepaid → "husd" (real money, treated as Prepaid).
func (b Bucket) LedgerTag() string {
	if b == BucketCredit {
		return "credit:husd"
	}
	return "husd"
}

// IssuanceStatus tracks a mint's lifecycle. An issuance is the off-chain,
// idempotent record of a mint request keyed by its idempotency key.
type IssuanceStatus string

const (
	// StatusPending: the record was created and we are (or a crashed prior
	// attempt was) submitting the on-chain tx. A pending record with no TxHash is
	// treated as in-flight — never resubmitted blindly.
	StatusPending IssuanceStatus = "pending"
	// StatusMinted: the on-chain tx was submitted; TxHash is set. Replays return
	// the stored receipt with no new tx.
	StatusMinted IssuanceStatus = "minted"
	// StatusFailed: submission failed before broadcast (validation/signing); the
	// key may be retried.
	StatusFailed IssuanceStatus = "failed"
)

// Issuance is the durable, idempotent record of ONE mint request. Its storage id
// is deterministic in the idempotency key so concurrent duplicate requests
// collapse onto one row (the money-safe primitive; see billing/bucket + gift
// card design). It is also the audit trail: every credit that exists traces to
// exactly one Issuance and its on-chain TxHash.
type Issuance struct {
	Id      string `json:"id"`
	IdemKey string `json:"idemKey"`
	OrgID   string `json:"orgId"`
	// Subject is the off-chain ledger DestinationId this mint credits (an IAM
	// "owner/name" per-user key, or the org slug for org-pooled billing). On-chain
	// HUSD is per-ORG (one derived address per OrgID), so many subjects share one
	// address; Subject records which sub-ledger the projected credit lands in.
	// Defaults to OrgID (org-pooled) when the caller leaves it empty.
	Subject     string         `json:"subject"`
	OrgAddress  string         `json:"orgAddress"`
	AmountCents int64          `json:"amountCents"`
	Bucket      Bucket         `json:"bucket"`
	Reason      string         `json:"reason"`
	// Test marks a mint on the test chain (matches the ledger's Test partition so
	// the balance read finds it). A test-mode org mints test HUSD; a live org mints
	// live HUSD — the projected credit's Test flag MUST equal the balance read's.
	Test        bool           `json:"test"`
	ChainID     int64          `json:"chainId"`
	TokenAddr   string         `json:"tokenAddr"`
	TxHash      string         `json:"txHash,omitempty"`
	Status      IssuanceStatus `json:"status"`
	CreatedAt   time.Time      `json:"createdAt"`
	MintedAt    time.Time      `json:"mintedAt,omitempty"`
}

// IssuanceStore persists issuances. It is an interface (not a concrete datastore)
// so the mint service's logic is unit-tested with an in-memory fake (CGO-free,
// no sqlite) and wired to the real per-org SQLite in production. The store MUST
// make CreateIfAbsent atomic on Issuance.Id: concurrent same-id calls yield
// exactly one created==true (the deterministic-id ON CONFLICT upsert).
type IssuanceStore interface {
	// CreateIfAbsent records iss iff its Id is not already present. Returns
	// created==true with iss when this call created it, or created==false with the
	// pre-existing record otherwise. Exactly one concurrent caller sees created==true.
	CreateIfAbsent(ctx context.Context, iss *Issuance) (created bool, existing *Issuance, err error)
	// Update persists status/txHash/mintedAt on an existing issuance.
	Update(ctx context.Context, iss *Issuance) error
	// Get loads an issuance by id (nil,nil if absent).
	Get(ctx context.Context, id string) (*Issuance, error)
}

// TransferFunc signs+submits a treasury ERC-20 transfer, returning the tx hash.
// Defaults to util/blockchain.TransferToken (KMS-keyed, geth signing in the
// linked sub-module); injectable so unit tests need no chain.
type TransferFunc func(ctx context.Context, t blockchain.TokenTransfer) (string, error)

// MintRequest is a request to bring AmountCents of HUSD into existence for an
// org. IdemKey makes it exactly-once: the same key mints at most one on-chain tx.
type MintRequest struct {
	OrgID string
	// Subject is the ledger DestinationId to credit (defaults to OrgID when empty —
	// org-pooled billing). See Issuance.Subject.
	Subject     string
	AmountCents int64
	Bucket      Bucket
	Reason      string
	IdemKey     string
	// Test routes the mint to the test partition of the ledger (must match the
	// balance read's Test flag). Set from org.TestMode() by the caller.
	Test bool
}

// Receipt is the result of a mint: the on-chain tx and where the value landed.
type Receipt struct {
	IdemKey     string
	OrgID       string
	Subject     string
	OrgAddress  string
	AmountCents int64
	Bucket      Bucket
	Reason      string
	Test        bool
	ChainID     int64
	TxHash      string
	Replayed    bool // true if the key had already minted; no new tx was sent
}

// Errors.
var (
	ErrNotConfigured = husd.ErrNotConfigured
	// ErrInFlight: an issuance for this key exists and is mid-submit (pending, no
	// tx). The caller must retry after the in-flight attempt resolves — resending
	// would risk a double mint. Not an error the ledger invented value.
	ErrInFlight = errors.New("treasury: mint already in flight for this idempotency key")
	// ErrNotAuthorized re-exports the mintauth refusal for callers.
	ErrNotAuthorized = mintauth.ErrNotAuthorized
)

// Treasury is the mint service. Construct with New; call Mint.
type Treasury struct {
	cfg        husd.Config
	masterSeed []byte
	store      IssuanceStore
	transfer   TransferFunc
	now        func() time.Time
}

// Option configures a Treasury.
type Option func(*Treasury)

// WithTransfer overrides the on-chain transfer function (tests inject a fake).
func WithTransfer(fn TransferFunc) Option { return func(t *Treasury) { t.transfer = fn } }

// WithClock overrides the clock (tests inject a fixed time).
func WithClock(now func() time.Time) Option { return func(t *Treasury) { t.now = now } }

// New builds a Treasury. cfg is the HUSD config (KMS-injected; must be
// Configured for real mints). masterSeed is the KMS-sourced org-address
// derivation seed. store persists issuances. By default the on-chain transfer is
// util/blockchain.TransferToken (requires the geth signer sub-module linked).
func New(cfg husd.Config, masterSeed []byte, store IssuanceStore, opts ...Option) *Treasury {
	t := &Treasury{
		cfg:        cfg,
		masterSeed: masterSeed,
		store:      store,
		transfer:   blockchain.TransferToken,
		now:        time.Now,
	}
	for _, o := range opts {
		o(t)
	}
	return t
}

// IssuanceID is the deterministic storage id for an idempotency key. Distinct
// keys → distinct ids; the same key → the same id, so a replay lands on the same
// row and concurrent duplicates collapse.
func IssuanceID(idemKey string) string {
	sum := sha256.Sum256([]byte("husd-issuance-v1:" + idemKey))
	return hex.EncodeToString(sum[:])
}

// Mint brings AmountCents of HUSD into existence for req.OrgID by a
// treasury-signed on-chain transfer to the org's derived address. It is:
//   - authorized: refused unless mintauth.Require(ctx) passes (a gated HTTP
//     caller must carry proven mint authority; crons/migrations are ungated).
//   - idempotent: the same IdemKey mints at most one on-chain tx; a replay
//     returns the stored receipt (Replayed=true) with no new tx.
//   - fail-closed: no token/key configured ⇒ ErrNotConfigured (no DB fallback,
//     no silent mint). The value is created ONLY by the KMS-keyed transfer.
func (t *Treasury) Mint(ctx context.Context, req MintRequest) (*Receipt, error) {
	// Lock 1: mint authority (same policy as the ledger write sink).
	if err := mintauth.Require(ctx); err != nil {
		return nil, err
	}

	// Validate the request.
	if req.OrgID == "" {
		return nil, ErrEmptyOrg
	}
	if req.AmountCents <= 0 {
		return nil, fmt.Errorf("treasury: mint amount must be positive, got %d", req.AmountCents)
	}
	if !req.Bucket.Valid() {
		return nil, fmt.Errorf("treasury: invalid bucket %q (want credit|prepaid)", req.Bucket)
	}
	if strings.TrimSpace(req.IdemKey) == "" {
		return nil, errors.New("treasury: idempotency key required")
	}
	// Lock 2 (custody): the value is created by the KMS treasury key. No config ⇒
	// fail closed, never a DB fallback.
	if !t.cfg.Configured() {
		return nil, ErrNotConfigured
	}

	orgAddr, err := AddressForOrg(t.masterSeed, req.OrgID)
	if err != nil {
		return nil, err
	}
	amountWei, err := husd.CentsToWei(req.AmountCents, t.cfg.Decimals)
	if err != nil {
		return nil, err
	}

	subject := strings.TrimSpace(req.Subject)
	if subject == "" {
		subject = req.OrgID // org-pooled billing: the subject IS the org
	}

	id := IssuanceID(req.IdemKey)
	iss := &Issuance{
		Id:          id,
		IdemKey:     req.IdemKey,
		OrgID:       req.OrgID,
		Subject:     subject,
		OrgAddress:  orgAddr,
		AmountCents: req.AmountCents,
		Bucket:      req.Bucket,
		Reason:      req.Reason,
		Test:        req.Test,
		ChainID:     t.cfg.ChainID,
		TokenAddr:   t.cfg.TokenAddress,
		Status:      StatusPending,
		CreatedAt:   t.now().UTC(),
	}

	created, existing, err := t.store.CreateIfAbsent(ctx, iss)
	if err != nil {
		return nil, fmt.Errorf("treasury: record issuance: %w", err)
	}
	if !created {
		// Idempotent replay: an issuance for this key already exists.
		switch existing.Status {
		case StatusMinted:
			return receiptFrom(existing, true), nil
		case StatusPending:
			// Mid-submit by another attempt — do not resend (double-mint risk).
			return nil, ErrInFlight
		case StatusFailed:
			// A prior attempt failed pre-broadcast; reclaim the row and retry.
			iss = existing
			iss.Status = StatusPending
			iss.Subject = subject
			iss.OrgAddress = orgAddr
			iss.AmountCents = req.AmountCents
			iss.Bucket = req.Bucket
			iss.Reason = req.Reason
			iss.Test = req.Test
			iss.ChainID = t.cfg.ChainID
			iss.TokenAddr = t.cfg.TokenAddress
			if err := t.store.Update(ctx, iss); err != nil {
				return nil, fmt.Errorf("treasury: reclaim failed issuance: %w", err)
			}
		default:
			return nil, fmt.Errorf("treasury: issuance %s in unknown status %q", id, existing.Status)
		}
	}

	// We own the pending record. Sign + submit the treasury→org transfer.
	txHash, txErr := t.transfer(ctx, blockchain.TokenTransfer{
		ChainID:      t.cfg.ChainID,
		RPCURL:       t.cfg.RPCURL,
		TokenAddress: t.cfg.TokenAddress,
		TreasuryKey:  t.cfg.TreasuryKey,
		To:           orgAddr,
		AmountWei:    amountWei,
		GasLimit:     t.cfg.GasLimit,
	})
	if txErr != nil {
		// Mark failed (retryable). NOTE: the seam does not distinguish a
		// pre-broadcast failure from a broadcast-then-errored one; a retry after a
		// broadcast-then-errored submit is the one window the step-6 reconcile job
		// backstops (chain balance == Σ issuances per org). See LLM.md.
		iss.Status = StatusFailed
		_ = t.store.Update(ctx, iss)
		return nil, fmt.Errorf("treasury: mint transfer: %w", txErr)
	}

	// Store lowercased: the indexer keys its ByTxHash lookup on the lowercased
	// on-chain Transfer log hash, so the recorded hash MUST match that form.
	iss.TxHash = strings.ToLower(txHash)
	iss.Status = StatusMinted
	iss.MintedAt = t.now().UTC()
	if err := t.store.Update(ctx, iss); err != nil {
		// The tx DID land; failing to record it is a reconcile concern, not a lost
		// mint. Surface it so the caller/operator reconciles rather than retries.
		return nil, fmt.Errorf("treasury: mint submitted (tx=%s) but recording failed: %w", txHash, err)
	}

	return receiptFrom(iss, false), nil
}

func receiptFrom(iss *Issuance, replayed bool) *Receipt {
	return &Receipt{
		IdemKey:     iss.IdemKey,
		OrgID:       iss.OrgID,
		Subject:     iss.Subject,
		OrgAddress:  iss.OrgAddress,
		AmountCents: iss.AmountCents,
		Bucket:      iss.Bucket,
		Reason:      iss.Reason,
		Test:        iss.Test,
		ChainID:     iss.ChainID,
		TxHash:      iss.TxHash,
		Replayed:    replayed,
	}
}

// AmountWeiForCents exposes the cents→wei conversion for callers verifying a
// mint amount against the chain (e.g. the indexer reconciliation).
func AmountWeiForCents(cents int64, decimals int) (*big.Int, error) {
	return husd.CentsToWei(cents, decimals)
}
