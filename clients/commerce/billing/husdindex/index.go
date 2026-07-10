package husdindex

import (
	"context"
	"fmt"
	"math/big"
	"sort"

	"github.com/hanzoai/cloud/clients/commerce/treasury"
	"github.com/hanzoai/cloud/clients/commerce/util/husd"
)

// Reader is the subset of the RPC client the projector needs. *Client satisfies
// it; unit tests inject a fake so Sync is proven without a chain.
type Reader interface {
	BlockNumber(ctx context.Context) (uint64, error)
	TransfersTo(ctx context.Context, addrs []string, fromBlock, toBlock uint64) ([]Transfer, error)
}

// Ledger is where the indexer PROJECTS on-chain HUSD credits. In production this
// writes an idempotent tagged deposit into the commerce transaction ledger (so
// the existing GET /v1/billing/*/balance reads chain-sourced balance unchanged);
// in tests it is a fake. Credit MUST be idempotent on Credit.DedupKey — the
// indexer re-scans overlapping ranges after restarts and must never double-credit.
type Ledger interface {
	Credit(ctx context.Context, c Credit) error
}

// Credit is one projected on-chain HUSD credit for an org.
type Credit struct {
	OrgID string
	// Subject is the ledger DestinationId to credit (the mint's subject; for an
	// external payin with no issuance it is the org slug). This is where the
	// balance read looks, so the projection MUST write to it, not to OrgID.
	Subject     string
	AmountCents int64
	Tag         string // billing/bucket ledger tag (credit:husd | husd)
	Test        bool   // ledger Test partition (matches the mint / balance read)
	TxHash      string
	LogIndex    uint64
	DedupKey    string // txHash:logIndex — idempotency key for the projection
}

// IssuanceLookup resolves a mint tx to its off-chain issuance so the projected
// credit is tagged into the right bucket (a fungible Transfer carries no tag).
// Returns nil for a transfer not originated by a treasury mint (external payin).
type IssuanceLookup interface {
	ByTxHash(ctx context.Context, txHash string) (*treasury.Issuance, error)
}

// Cursor persists the last fully-scanned block so a restart resumes, not restarts.
type Cursor interface {
	Last(ctx context.Context) (uint64, error)
	Save(ctx context.Context, block uint64) error
}

// AddressBook maps derived org addresses (lowercased) → orgID, and lists the
// addresses to filter Transfer logs by. Production derives it from the org list
// + master seed (treasury.AddressForOrg); tests supply it directly.
type AddressBook interface {
	// Addresses returns the set of org HUSD addresses to watch.
	Addresses(ctx context.Context) ([]string, error)
	// OrgFor returns the orgID owning a (lowercased) address, ok=false if unknown.
	OrgFor(addr string) (string, bool)
}

// Indexer projects on-chain HUSD Transfers into the ledger.
type Indexer struct {
	reader        Reader
	ledger        Ledger
	lookup        IssuanceLookup
	cursor        Cursor
	book          AddressBook
	decimals      int
	confirmations uint64
	startBlock    uint64
	// maxRange caps blocks per getLogs call so a cold start doesn't ask for a
	// million-block window at once (RPCs reject huge ranges).
	maxRange uint64
}

// Config parameterizes an Indexer.
type Config struct {
	Decimals      int
	Confirmations uint64 // blocks below head to treat as final (reorg safety)
	StartBlock    uint64 // first block to scan when the cursor is empty
	MaxRange      uint64 // 0 → default 2000
}

// NewIndexer builds an Indexer.
func NewIndexer(reader Reader, ledger Ledger, lookup IssuanceLookup, cursor Cursor, book AddressBook, cfg Config) *Indexer {
	if cfg.Decimals == 0 {
		cfg.Decimals = husd.DefaultDecimals
	}
	if cfg.MaxRange == 0 {
		cfg.MaxRange = 2000
	}
	return &Indexer{
		reader:        reader,
		ledger:        ledger,
		lookup:        lookup,
		cursor:        cursor,
		book:          book,
		decimals:      cfg.Decimals,
		confirmations: cfg.Confirmations,
		startBlock:    cfg.StartBlock,
		maxRange:      cfg.MaxRange,
	}
}

// Sync scans new Transfer events up to (head − confirmations) and projects each
// into the ledger. It is idempotent: the ledger dedups on DedupKey, so a re-scan
// (overlap after a restart, or a re-run) never double-credits. Returns the
// number of transfers projected.
func (ix *Indexer) Sync(ctx context.Context) (int, error) {
	head, err := ix.reader.BlockNumber(ctx)
	if err != nil {
		return 0, err
	}
	if head < ix.confirmations {
		return 0, nil // chain too young
	}
	safeHead := head - ix.confirmations

	last, err := ix.cursor.Last(ctx)
	if err != nil {
		return 0, err
	}
	var from uint64
	if last == 0 {
		from = ix.startBlock
	} else {
		from = last + 1
	}
	if from > safeHead {
		return 0, nil // nothing new final yet
	}

	addrs, err := ix.book.Addresses(ctx)
	if err != nil {
		return 0, err
	}
	if len(addrs) == 0 {
		// No orgs to watch yet; still advance the cursor so we don't rescan.
		return 0, ix.cursor.Save(ctx, safeHead)
	}

	projected := 0
	for start := from; start <= safeHead; start += ix.maxRange {
		end := start + ix.maxRange - 1
		if end > safeHead {
			end = safeHead
		}
		transfers, err := ix.reader.TransfersTo(ctx, addrs, start, end)
		if err != nil {
			return projected, err
		}
		// Deterministic order: block then logIndex.
		sort.Slice(transfers, func(i, j int) bool {
			if transfers[i].Block != transfers[j].Block {
				return transfers[i].Block < transfers[j].Block
			}
			return transfers[i].LogIndex < transfers[j].LogIndex
		})
		for _, t := range transfers {
			if err := ix.project(ctx, t); err != nil {
				return projected, err
			}
			projected++
		}
		if err := ix.cursor.Save(ctx, end); err != nil {
			return projected, err
		}
	}
	return projected, nil
}

// TxReader reads the HUSD Transfer logs contained in a single already-mined tx.
// *Client satisfies it. It is a NARROW extension of Reader (kept separate so the
// Sync fakes need not implement it) used by ProjectTx for immediate, synchronous
// projection of a just-submitted mint.
type TxReader interface {
	TransfersInTx(ctx context.Context, txHash string, addrs []string) ([]Transfer, error)
}

// ProjectTx synchronously projects the HUSD Transfers in ONE mined tx (a
// just-submitted treasury mint) into the ledger, so the balance reflects the
// mint immediately rather than after the next Sync tick. It funnels through the
// SAME idempotent project() the background Sync uses (dedup on txHash:logIndex),
// so a later Sync over the same block never double-credits. Returns the number
// of transfers projected (0 if the tx touched none of our addresses — e.g. it is
// not yet mined; the background Sync will pick it up once final).
//
// It does NOT advance the Sync cursor: the background Sync remains the
// authoritative, gap-free scanner + reconciler; ProjectTx is a latency
// optimization layered on top, never a replacement.
func (ix *Indexer) ProjectTx(ctx context.Context, txHash string) (int, error) {
	tr, ok := ix.reader.(TxReader)
	if !ok {
		return 0, fmt.Errorf("husdindex: reader %T cannot read single-tx transfers", ix.reader)
	}
	addrs, err := ix.book.Addresses(ctx)
	if err != nil {
		return 0, err
	}
	if len(addrs) == 0 {
		return 0, nil
	}
	transfers, err := tr.TransfersInTx(ctx, txHash, addrs)
	if err != nil {
		return 0, err
	}
	sort.Slice(transfers, func(i, j int) bool { return transfers[i].LogIndex < transfers[j].LogIndex })
	n := 0
	for _, t := range transfers {
		if err := ix.project(ctx, t); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// project turns one Transfer into a tagged, idempotent ledger credit.
func (ix *Indexer) project(ctx context.Context, t Transfer) error {
	org, ok := ix.book.OrgFor(t.To)
	if !ok {
		return nil // not one of ours (filter should prevent this)
	}
	// Truncate to whole cents; never over-credit on sub-cent dust.
	cents, _, err := husd.WeiToCents(t.ValueWei, ix.decimals)
	if err != nil {
		return fmt.Errorf("husdindex: project %s: %w", t.DedupKey(), err)
	}
	if cents <= 0 {
		return nil // dust-only transfer, nothing to credit
	}

	// Resolve the off-chain issuance so the projected credit lands in the right
	// bucket AND the right sub-ledger (Subject). A fungible Transfer carries
	// neither, so both come from the mint's issuance; a transfer with no issuance
	// is an external payin (card/crypto → HUSD) → prepaid, credited to the org
	// slug (fail-closed: unknown money is real money, never a grant).
	tag := treasury.BucketPrepaid.LedgerTag()
	subject := org
	test := false
	if iss, err := ix.lookup.ByTxHash(ctx, t.TxHash); err != nil {
		return err
	} else if iss != nil && iss.OrgID == org {
		if iss.Bucket.Valid() {
			tag = iss.Bucket.LedgerTag()
		}
		if iss.Subject != "" {
			subject = iss.Subject
		}
		test = iss.Test
	}

	return ix.ledger.Credit(ctx, Credit{
		OrgID:       org,
		Subject:     subject,
		AmountCents: cents,
		Tag:         tag,
		Test:        test,
		TxHash:      t.TxHash,
		LogIndex:    t.LogIndex,
		DedupKey:    t.DedupKey(),
	})
}

// Reconcile fetches an org's on-chain balanceOf and returns it in cents together
// with any sub-cent remainder — the exact check the migration + monitoring
// assert against the indexed ledger balance. balanceReader is *Client (or a
// fake). Kept separate from Sync so reconciliation reads chain truth directly.
type BalanceReader interface {
	BalanceOf(ctx context.Context, addr string) (*big.Int, error)
}

// OnChainBalanceCents returns the org's on-chain HUSD balance in whole cents.
func OnChainBalanceCents(ctx context.Context, br BalanceReader, addr string, decimals int) (int64, error) {
	wei, err := br.BalanceOf(ctx, addr)
	if err != nil {
		return 0, err
	}
	cents, _, err := husd.WeiToCents(wei, decimals)
	return cents, err
}
