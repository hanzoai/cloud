package husdledger

import (
	"context"
	"errors"
	"os"
	"strconv"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/billing/husdindex"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/treasury"
	"github.com/hanzoai/cloud/clients/commerce/treasury/datastorestore"
	"github.com/hanzoai/cloud/clients/commerce/util/blockchain"
	"github.com/hanzoai/cloud/clients/commerce/util/husd"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
)

// Service is the chain-backed credit ledger, wired to production stores. It owns
// the treasury mint service (Steps 1-2), the on-chain indexer/projector (Step 3),
// and the settlement job (Step 5). MintCredit is the ONE way a credit is created
// once the chain path is enabled: a treasury-signed on-chain HUSD issuance whose
// value is projected back into the commerce ledger the balance endpoint reads.
//
// When the chain path is NOT configured (no token/key/seed), Enabled() is false
// and the billing handlers keep their existing DB-mint behavior — so a deploy
// without HUSD configured is unchanged, and the migration (Step 6) flips reads
// over deliberately.
type Service struct {
	cfg      husd.Config
	seed     []byte
	client   *husdindex.Client
	treasury *treasury.Treasury
	issuance *datastorestore.Store
	book     *addressBook
	indexer  *husdindex.Indexer
	enabled  bool

	// Settlement (Step 5): treasuryAddr is the sweep destination (the treasury
	// key's own address); settleTransfer is the org→treasury signer seam and
	// balanceReader is the on-chain balance read — both injectable for tests,
	// defaulting to the KMS-keyed on-chain transfer / the RPC client.
	treasuryAddr   string
	settleTransfer blockchain.TokenTransferFn
	balanceReader  husdindex.BalanceReader
	// Test seams (nil in production → the real KMS-keyed transfer / RPC client):
	// mintTransfer signs the treasury mint; indexReader feeds the projector.
	mintTransfer blockchain.TokenTransferFn
	indexReader  husdindex.Reader
}

// Option configures a Service (chain-seam injection for tests).
type Option func(*Service)

// WithSettleTransfer overrides the org→treasury transfer function (tests inject a
// fake so the settlement flow is proven without a chain).
func WithSettleTransfer(fn blockchain.TokenTransferFn) Option {
	return func(s *Service) { s.settleTransfer = fn }
}

// WithBalanceReader overrides the on-chain balance reader used by settlement +
// migration reconcile (tests inject a fake balanceOf so drift/reconcile is proven
// without a chain).
func WithBalanceReader(br husdindex.BalanceReader) Option {
	return func(s *Service) { s.balanceReader = br }
}

// WithMintTransfer overrides the treasury mint transfer (tests fake it).
func WithMintTransfer(fn blockchain.TokenTransferFn) Option {
	return func(s *Service) { s.mintTransfer = fn }
}

// WithIndexReader overrides the indexer's chain reader (tests feed the projector
// synthetic Transfers so MintCredit's projection runs without a chain).
func WithIndexReader(r husdindex.Reader) Option {
	return func(s *Service) { s.indexReader = r }
}

// projectMintPolls bounds how long MintCredit waits for a just-broadcast mint tx
// to be mined so it can project the credit before returning (fresh balance on the
// response). Beyond this the mint is already on chain and the background Sync
// backfills it; we never block indefinitely.
const (
	projectMintPolls    = 15
	projectMintInterval = time.Second
)

// ValidateConfig reports a FATAL chain-ledger misconfiguration that a caller must
// refuse to boot on: a derivation seed is supplied — the chain ledger is clearly
// intended — but the HUSD token address and/or treasury key are missing, so the
// ledger cannot run and would silently fall back to the DB credit path for a
// ledger the operator meant to enable. Fail closed.
//
// The asymmetry is deliberate: HUSD token+key WITHOUT a seed is NOT an error — it
// is the inert state. The shared HUSD config (util/husd) is ALSO used by the OSS
// contributor payout path, which needs a token+key but no ledger seed; there the
// ledger stays disabled (Enabled()==false) and the existing DB credit path is
// used, exactly as before the chain ledger existed. The seed is the ledger's SOLE
// intent signal, so only "seed set but not Configured" is a partial/incoherent
// config worth refusing.
func ValidateConfig(cfg husd.Config, seed []byte) error {
	if len(seed) > 0 && !cfg.Configured() {
		return errors.New("HUSD_ORG_DERIVATION_SEED is set (chain ledger intended) but HUSD_TOKEN_ADDRESS/HUSD_TREASURY_KEY are missing — refusing to start; set both, or unset the seed to keep the DB credit path")
	}
	return nil
}

// New builds a Service from the HUSD config and the org-derivation master seed.
// If the chain path is not fully configured it returns a DISABLED service (no
// panic, no partial state) so callers can safely wire it unconditionally and let
// Enabled() gate the on-chain path.
func New(cfg husd.Config, seed []byte, opts ...Option) *Service {
	s := &Service{cfg: cfg, seed: seed, settleTransfer: blockchain.TransferToken}
	for _, o := range opts {
		o(s)
	}
	if !cfg.Configured() || len(seed) == 0 {
		return s
	}
	// The sweep destination is the treasury key's OWN address (settlement returns
	// consumed HUSD to the mint source). Derivable offline from the key.
	if addr, err := treasury.AddressForKey(cfg.TreasuryKey); err == nil {
		s.treasuryAddr = addr
	}
	s.client = husdindex.NewClient(cfg.RPCURL, cfg.TokenAddress)
	if s.balanceReader == nil {
		s.balanceReader = s.client
	}
	// Issuances live in ONE system-namespace ledger (queryable by tx globally):
	// the same store is the mint's write target AND the indexer's IssuanceLookup.
	sysDB := datastore.New(nscontext.WithNamespace(context.Background(), systemNamespace))
	s.issuance = datastorestore.New(sysDB)
	var mintOpts []treasury.Option
	if s.mintTransfer != nil {
		mintOpts = append(mintOpts, treasury.WithTransfer(treasury.TransferFunc(s.mintTransfer)))
	}
	s.treasury = treasury.New(cfg, seed, s.issuance, mintOpts...)
	s.book = newAddressBook(seed)
	reader := husdindex.Reader(s.client)
	if s.indexReader != nil {
		reader = s.indexReader
	}
	s.indexer = husdindex.NewIndexer(
		reader, ledgerStore{}, s.issuance, &cursorStore{chainID: cfg.ChainID}, s.book,
		husdindex.Config{
			Decimals:      cfg.Decimals,
			Confirmations: indexConfirmations(),
			StartBlock:    s.headAtBoot(),
		},
	)
	s.enabled = true
	return s
}

// Enabled reports whether the on-chain credit path is live.
func (s *Service) Enabled() bool { return s != nil && s.enabled }

// Config returns the HUSD config (chain id, token, decimals) — read-only view for
// callers deciding Test partitioning / reconciliation.
func (s *Service) Config() husd.Config { return s.cfg }

// MintCredit is the ONE mint entrypoint: a treasury-signed on-chain HUSD issuance
// for req, projected back into the commerce ledger. It authorizes via the passed
// ctx (mintauth) exactly like the DB path, then (best-effort, bounded) projects
// the minted tx so the balance reflects it on return. Idempotent by req.IdemKey.
func (s *Service) MintCredit(ctx context.Context, req treasury.MintRequest) (*treasury.Receipt, error) {
	if !s.Enabled() {
		return nil, husd.ErrNotConfigured
	}
	rc, err := s.treasury.Mint(ctx, req)
	if err != nil {
		return nil, err
	}
	if !rc.Replayed && rc.TxHash != "" {
		s.projectMintedTx(rc.TxHash)
	}
	return rc, nil
}

// projectMintedTx projects a just-broadcast mint once it is mined, bounded by
// projectMintPolls. A miss only delays the ledger credit to the next Sync — the
// mint is already on chain, so no value is lost. Uses a background context (the
// projection is a ledger write, not the caller's request).
func (s *Service) projectMintedTx(txHash string) {
	ctx := context.Background()
	for i := 0; i < projectMintPolls; i++ {
		if n, err := s.indexer.ProjectTx(ctx, txHash); err != nil {
			log.Warn("husdledger: ProjectTx(%s) error (will retry via Sync): %v", txHash, err)
		} else if n > 0 {
			return
		}
		time.Sleep(projectMintInterval)
	}
	log.Warn("husdledger: mint %s not projected within budget; background Sync will backfill", txHash)
}

// SyncOnce runs one indexer pass (scan new final Transfers → project). It is the
// backfill + reconcile safety net behind the synchronous MintCredit projection,
// and the entrypoint an external CronJob drives. Idempotent. Returns the number
// of transfers projected this pass.
func (s *Service) SyncOnce(ctx context.Context) (int, error) {
	if !s.Enabled() {
		return 0, husd.ErrNotConfigured
	}
	return s.indexer.Sync(ctx)
}

// headAtBoot returns the current chain head so a FRESH deploy (empty cursor)
// indexes forward from now rather than cold-scanning from genesis. Historical
// mints are still projected synchronously at mint time; on RPC failure we fall
// back to 0 (cold scan, capped per call — correct, just slower) and self-heal.
func (s *Service) headAtBoot() uint64 {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if h, err := s.client.BlockNumber(ctx); err == nil {
		return h
	}
	return 0
}

func indexConfirmations() uint64 {
	if v := os.Getenv("HUSD_INDEX_CONFIRMATIONS"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			return n
		}
	}
	return 1 // Hanzo EVM has fast/instant finality; 1 confirmation suffices
}

// --- package-level default (set at boot; read by the billing handlers) ---

var defaultService *Service

// SetDefault installs the process-wide chain-backed ledger service. Called once
// at Bootstrap after the DB is wired.
func SetDefault(s *Service) { defaultService = s }

// Default returns the process-wide service (may be a disabled Service, never nil
// once SetDefault ran; callers must still guard with Enabled()).
func Default() *Service { return defaultService }
