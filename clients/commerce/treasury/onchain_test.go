//go:build onchain

// Live end-to-end proof of the chain-backed credit ledger against the Hanzo
// TESTNET (EVM chainId 36962, HUSD test token). It exercises the REAL step-2
// mint service and the REAL step-3 indexer read client, and verifies the whole
// loop on chain:
//
//	treasury.Mint  →  real treasury-signed ERC-20 tx  →  receipt status 0x1
//	             →  husdindex.Client.BalanceOf(orgAddr) == minted amount
//	             →  husdindex Sync projects a bucket-tagged credit == minted cents
//	             →  a replay of the same idempotency key sends NO second tx.
//
// The signer is the standard EIP-155 legacy-tx sequence (keccak sighash →
// secp256k1 sign → RLP), built on luxfi/crypto only — CGO-free and free of the
// geth core/types dependency (whose transitive luxfi/pq pull trips the
// force-moved-tag go.sum mismatch on a laptop). It produces byte-identical
// signed transactions to the proven thirdparty/ethereum executor; correctness is
// verified on chain (receipt status + balance delta), not asserted.
//
// Gated behind the `onchain` build tag; needs a funded treasury + reachable RPC.
//
// Run (port-forward the testnet hanzod C-chain RPC first):
//
//	kubectl --context do-sfo3-hanzo-k8s -n hanzo-testnet port-forward pod/hanzod-0 19630:9630 &
//	HUSD_RPC_URL=http://localhost:19630/ext/bc/C/rpc \
//	HUSD_TREASURY_KEY=<hex from commerce-secrets> \
//	CGO_ENABLED=0 go test -tags onchain -run TestOnChain_ChainBackedLedger -v ./treasury
package treasury_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	luxcrypto "github.com/luxfi/crypto"

	"github.com/hanzoai/cloud/clients/commerce/billing/husdindex"
	"github.com/hanzoai/cloud/clients/commerce/treasury"
	"github.com/hanzoai/cloud/clients/commerce/util/blockchain"
	"github.com/hanzoai/cloud/clients/commerce/util/husd"
)

const (
	husdTestnetToken = "0xc57b7eCE2Ce2E74ef3Bc08Cfd5f5Fb41B6Ad4D66"
	husdTestnetChain = int64(36962)
	husdDecimals     = 18
)

// register the CGO-free signer into the util/blockchain seam.
func init() { blockchain.RegisterTokenTransfer(husdTransfer) }

var erc20TransferSelector = []byte{0xa9, 0x05, 0x9c, 0xbb} // transfer(address,uint256)

// husdTransfer signs + submits an EIP-155 ERC-20 transfer with the treasury key.
func husdTransfer(ctx context.Context, t blockchain.TokenTransfer) (string, error) {
	priv, err := luxcrypto.HexToECDSA(strings.TrimPrefix(t.TreasuryKey, "0x"))
	if err != nil {
		return "", fmt.Errorf("decode treasury key: %w", err)
	}
	from := luxcrypto.PubkeyToAddress(priv.PublicKey).Hex()

	nonce, err := rpcHexUint(ctx, t.RPCURL, "eth_getTransactionCount", from, "pending")
	if err != nil {
		return "", err
	}
	gp, err := rpcHexBig(ctx, t.RPCURL, "eth_gasPrice")
	if err != nil || gp.Sign() == 0 {
		gp = big.NewInt(25_000_000_000) // 25 gwei minBaseFee
	}
	gasLimit := t.GasLimit
	if gasLimit == 0 {
		gasLimit = 100_000
	}

	// ERC-20 transfer(to, amount) calldata.
	data := append([]byte{}, erc20TransferSelector...)
	data = append(data, leftPad32(mustHex(t.To))...)
	data = append(data, leftPad32(t.AmountWei.Bytes())...)

	tokenBytes := mustHex(t.TokenAddress)

	// EIP-155 sighash: rlp[nonce, gasPrice, gas, to, value(0), data, chainId, 0, 0].
	sigPayload := rlpList(
		rlpUint(nonce), rlpBig(gp), rlpUint(gasLimit),
		rlpStr(tokenBytes), rlpStr(nil), rlpStr(data),
		rlpUint(uint64(t.ChainID)), rlpStr(nil), rlpStr(nil),
	)
	sigHash := luxcrypto.Keccak256(sigPayload)
	sig, err := luxcrypto.Sign(sigHash, priv)
	if err != nil {
		return "", fmt.Errorf("sign: %w", err)
	}
	r := new(big.Int).SetBytes(sig[0:32])
	s := new(big.Int).SetBytes(sig[32:64])
	v := uint64(sig[64]) + 35 + 2*uint64(t.ChainID)

	signed := rlpList(
		rlpUint(nonce), rlpBig(gp), rlpUint(gasLimit),
		rlpStr(tokenBytes), rlpStr(nil), rlpStr(data),
		rlpUint(v), rlpBig(r), rlpBig(s),
	)
	return rpcSendRaw(ctx, t.RPCURL, "0x"+hex.EncodeToString(signed))
}

func TestOnChain_ChainBackedLedger(t *testing.T) {
	rpc := os.Getenv("HUSD_RPC_URL")
	key := os.Getenv("HUSD_TREASURY_KEY")
	if rpc == "" || key == "" {
		t.Skip("set HUSD_RPC_URL + HUSD_TREASURY_KEY to run the live testnet ledger proof")
	}

	token := husdTestnetToken
	if v := os.Getenv("HUSD_TOKEN_ADDRESS"); v != "" {
		token = v
	}
	cfg := husd.Config{
		ChainID: husdTestnetChain, RPCURL: rpc, TokenAddress: token,
		TreasuryKey: key, Decimals: husdDecimals,
	}
	seed := []byte("hanzo-husd-org-derivation-onchain-proof-seed-v1")

	// A FRESH per-run org so its address has zero prior HUSD → balanceOf delta is
	// exactly the mint.
	org := fmt.Sprintf("husd-proof-%d", time.Now().UnixNano())
	orgAddr, err := treasury.AddressForOrg(seed, org)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("org=%s derived HUSD address=%s", org, orgAddr)

	const amountCents = int64(1234) // $12.34
	client := husdindex.NewClient(rpc, token)

	before, err := client.BalanceOf(context.Background(), orgAddr)
	if err != nil {
		t.Fatalf("balanceOf before: %v", err)
	}
	t.Logf("balance before: %s", before)

	// --- STEP 2: mint via the real treasury service (ungated cron-style ctx). ---
	tr := treasury.New(cfg, seed, newMemStore()) // default transfer = blockchain.TransferToken → our signer
	idem := "proof:" + org
	rc, err := tr.Mint(context.Background(), treasury.MintRequest{
		OrgID: org, AmountCents: amountCents, Bucket: treasury.BucketCredit, Reason: "chain-ledger-proof", IdemKey: idem,
	})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if rc.Replayed || rc.TxHash == "" {
		t.Fatalf("unexpected first-mint receipt: %+v", rc)
	}
	t.Logf("REAL on-chain mint tx: %s (org %s ← $%.2f HUSD)", rc.TxHash, org, float64(amountCents)/100)

	status, block := waitReceipt(t, rpc, rc.TxHash)
	if status != "0x1" {
		t.Fatalf("mint tx mined non-success status=%s", status)
	}
	t.Logf("mint mined OK status=%s block=%s", status, block)

	// --- STEP 3a: the indexer read client reflects the balance exactly. ---
	after, err := client.BalanceOf(context.Background(), orgAddr)
	if err != nil {
		t.Fatal(err)
	}
	delta := new(big.Int).Sub(after, before)
	wantWei, _ := husd.CentsToWei(amountCents, husdDecimals)
	if delta.Cmp(wantWei) != 0 {
		t.Fatalf("balanceOf delta=%s, want %s", delta, wantWei)
	}
	onchainCents, err := husdindex.OnChainBalanceCents(context.Background(), client, orgAddr, husdDecimals)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("PROVEN reflect: on-chain balanceOf(%s) = %d cents", orgAddr, onchainCents)

	// --- STEP 3b: the indexer Sync projects a bucket-tagged credit == minted. ---
	blockNum, _ := hexToUint(block)
	ledger := &memLedger{seen: map[string]husdindex.Credit{}}
	lookup := memLookup{strings.ToLower(rc.TxHash): {OrgID: org, Bucket: treasury.BucketCredit, AmountCents: amountCents}}
	book := memBook{strings.ToLower(orgAddr): org}
	head, _ := client.BlockNumber(context.Background())
	ix := husdindex.NewIndexer(client, ledger, lookup, &memCursor{last: blockNum - 1}, book,
		husdindex.Config{Decimals: husdDecimals, Confirmations: 0, StartBlock: blockNum - 1, MaxRange: head - blockNum + 2})
	if _, err := ix.Sync(context.Background()); err != nil {
		t.Fatalf("indexer Sync: %v", err)
	}
	var credit husdindex.Credit
	var ok bool
	for _, c := range ledger.seen {
		if c.OrgID == org {
			credit, ok = c, true
		}
	}
	if !ok {
		t.Fatalf("indexer did not project a credit for %s; saw %d credits", org, len(ledger.seen))
	}
	if credit.AmountCents != amountCents || credit.Tag != "credit:husd" {
		t.Fatalf("projected credit wrong: %+v (want %dc, tag credit:husd)", credit, amountCents)
	}
	t.Logf("PROVEN project: indexer credited org=%s %dc tag=%s (== on-chain mint)", org, credit.AmountCents, credit.Tag)

	// --- STEP 2 idempotency LIVE: replay the same key sends NO second tx. ---
	rc2, err := tr.Mint(context.Background(), treasury.MintRequest{
		OrgID: org, AmountCents: amountCents, Bucket: treasury.BucketCredit, Reason: "chain-ledger-proof", IdemKey: idem,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !rc2.Replayed || rc2.TxHash != rc.TxHash {
		t.Fatalf("replay minted again? %+v", rc2)
	}
	again, _ := client.BalanceOf(context.Background(), orgAddr)
	if again.Cmp(after) != 0 {
		t.Fatalf("balance moved on replay: %s != %s (DOUBLE MINT)", again, after)
	}
	t.Logf("PROVEN idempotent: replay of key %q sent no second tx; balance unchanged", idem)
	t.Log("HEADLINE: minting is treasury-only + idempotent, and the indexer reflects on-chain balance — end-to-end on testnet 36962.")
}

// ---- minimal RLP (test-only; on-chain receipt verifies correctness) ----

func rlpStr(b []byte) []byte {
	if len(b) == 1 && b[0] < 0x80 {
		return b
	}
	return append(rlpLenPrefix(len(b), 0x80), b...)
}

func rlpList(items ...[]byte) []byte {
	var body []byte
	for _, it := range items {
		body = append(body, it...)
	}
	return append(rlpLenPrefix(len(body), 0xc0), body...)
}

func rlpLenPrefix(n int, offset byte) []byte {
	if n < 56 {
		return []byte{offset + byte(n)}
	}
	lb := new(big.Int).SetInt64(int64(n)).Bytes()
	return append([]byte{offset + 55 + byte(len(lb))}, lb...)
}

func rlpUint(n uint64) []byte {
	if n == 0 {
		return rlpStr(nil)
	}
	return rlpStr(new(big.Int).SetUint64(n).Bytes())
}

func rlpBig(n *big.Int) []byte {
	if n == nil || n.Sign() == 0 {
		return rlpStr(nil)
	}
	return rlpStr(n.Bytes())
}

func leftPad32(b []byte) []byte {
	if len(b) >= 32 {
		return b[len(b)-32:]
	}
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return out
}

func mustHex(addr string) []byte {
	b, _ := hex.DecodeString(strings.TrimPrefix(strings.TrimPrefix(addr, "0x"), "0X"))
	return b
}

// ---- in-memory adapters for the live proof ----

type memStore struct{ m map[string]*treasury.Issuance }

func newMemStore() *memStore { return &memStore{m: map[string]*treasury.Issuance{}} }
func (s *memStore) CreateIfAbsent(_ context.Context, iss *treasury.Issuance) (bool, *treasury.Issuance, error) {
	if ex, ok := s.m[iss.Id]; ok {
		cp := *ex
		return false, &cp, nil
	}
	cp := *iss
	s.m[iss.Id] = &cp
	return true, iss, nil
}
func (s *memStore) Update(_ context.Context, iss *treasury.Issuance) error {
	cp := *iss
	s.m[iss.Id] = &cp
	return nil
}
func (s *memStore) Get(_ context.Context, id string) (*treasury.Issuance, error) {
	if ex, ok := s.m[id]; ok {
		cp := *ex
		return &cp, nil
	}
	return nil, nil
}

type memLedger struct{ seen map[string]husdindex.Credit }

func (l *memLedger) Credit(_ context.Context, c husdindex.Credit) error {
	l.seen[c.DedupKey] = c
	return nil
}

type memLookup map[string]*treasury.Issuance

func (m memLookup) ByTxHash(_ context.Context, tx string) (*treasury.Issuance, error) {
	return m[strings.ToLower(tx)], nil
}

type memBook map[string]string

func (b memBook) Addresses(context.Context) ([]string, error) {
	out := make([]string, 0, len(b))
	for a := range b {
		out = append(out, a)
	}
	return out, nil
}
func (b memBook) OrgFor(a string) (string, bool) { o, ok := b[strings.ToLower(a)]; return o, ok }

type memCursor struct{ last uint64 }

func (c *memCursor) Last(context.Context) (uint64, error)   { return c.last, nil }
func (c *memCursor) Save(_ context.Context, b uint64) error { c.last = b; return nil }

// ---- minimal JSON-RPC (write side) ----

func rpcRaw(ctx context.Context, url, method string, params ...any) (string, error) {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		Result string `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("%s decode: %w (%s)", method, err, raw)
	}
	if out.Error != nil {
		return "", fmt.Errorf("%s: %s", method, out.Error.Message)
	}
	return out.Result, nil
}

func rpcHexUint(ctx context.Context, url, method string, params ...any) (uint64, error) {
	s, err := rpcRaw(ctx, url, method, params...)
	if err != nil {
		return 0, err
	}
	return hexToUint(s)
}

func rpcHexBig(ctx context.Context, url, method string, params ...any) (*big.Int, error) {
	s, err := rpcRaw(ctx, url, method, params...)
	if err != nil {
		return nil, err
	}
	n := new(big.Int)
	n.SetString(strings.TrimPrefix(s, "0x"), 16)
	return n, nil
}

func rpcSendRaw(ctx context.Context, url, rawHex string) (string, error) {
	return rpcRaw(ctx, url, "eth_sendRawTransaction", rawHex)
}

func hexToUint(h string) (uint64, error) {
	h = strings.TrimPrefix(h, "0x")
	if h == "" {
		return 0, nil
	}
	n := new(big.Int)
	if _, ok := n.SetString(h, 16); !ok {
		return 0, fmt.Errorf("bad hex %q", h)
	}
	return n.Uint64(), nil
}

func waitReceipt(t *testing.T, url, txHash string) (status, block string) {
	t.Helper()
	for i := 0; i < 90; i++ {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "eth_getTransactionReceipt", "params": []any{txHash}})
		resp, err := http.Post(url, "application/json", bytes.NewReader(body))
		if err == nil {
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			var out struct {
				Result *struct {
					Status      string `json:"status"`
					BlockNumber string `json:"blockNumber"`
				} `json:"result"`
			}
			if json.Unmarshal(raw, &out) == nil && out.Result != nil && out.Result.Status != "" {
				return out.Result.Status, out.Result.BlockNumber
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("receipt for %s not found", txHash)
	return "", ""
}

// TestOnChain_Step4_ProjectTx proves the Step-4 synchronous projection path on a
// REAL chain tx: a mint carrying a Subject + Test flag, then Indexer.ProjectTx —
// which reads the actual Transfer log via Client.TransfersInTx
// (eth_getTransactionReceipt) — projects ONE credit tagged into the right bucket,
// subject, and Test partition, idempotently.
func TestOnChain_Step4_ProjectTx(t *testing.T) {
	rpc := os.Getenv("HUSD_RPC_URL")
	key := os.Getenv("HUSD_TREASURY_KEY")
	if rpc == "" || key == "" {
		t.Skip("set HUSD_RPC_URL + HUSD_TREASURY_KEY to run the live testnet ledger proof")
	}
	token := husdTestnetToken
	if v := os.Getenv("HUSD_TOKEN_ADDRESS"); v != "" {
		token = v
	}
	cfg := husd.Config{ChainID: husdTestnetChain, RPCURL: rpc, TokenAddress: token, TreasuryKey: key, Decimals: husdDecimals}
	seed := []byte("hanzo-husd-org-derivation-onchain-proof-seed-v1")

	org := fmt.Sprintf("husd-step4-%d", time.Now().UnixNano())
	subject := org + "/alice"
	orgAddr, err := treasury.AddressForOrg(seed, org)
	if err != nil {
		t.Fatal(err)
	}
	const amountCents = int64(777) // $7.77

	client := husdindex.NewClient(rpc, token)
	tr := treasury.New(cfg, seed, newMemStore())
	rc, err := tr.Mint(context.Background(), treasury.MintRequest{
		OrgID: org, Subject: subject, AmountCents: amountCents, Bucket: treasury.BucketCredit,
		Reason: "step4-projecttx-proof", Test: true, IdemKey: "step4:" + org,
	})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if status, _ := waitReceipt(t, rpc, rc.TxHash); status != "0x1" {
		t.Fatalf("mint tx status=%s", status)
	}
	t.Logf("REAL on-chain mint tx: %s (subject %s ← $%.2f, test)", rc.TxHash, subject, float64(amountCents)/100)

	// The indexer's issuance lookup (production: the system-ns issuance store) —
	// here keyed to the mint's tx with its off-chain Subject + Test + bucket.
	ledger := &memLedger{seen: map[string]husdindex.Credit{}}
	lookup := memLookup{strings.ToLower(rc.TxHash): {
		OrgID: org, Subject: subject, Bucket: treasury.BucketCredit, AmountCents: amountCents, Test: true,
	}}
	book := memBook{strings.ToLower(orgAddr): org}
	ix := husdindex.NewIndexer(client, ledger, lookup, &memCursor{}, book,
		husdindex.Config{Decimals: husdDecimals, Confirmations: 0})

	// ProjectTx reads the real receipt and projects the credit.
	n, err := ix.ProjectTx(context.Background(), rc.TxHash)
	if err != nil {
		t.Fatalf("ProjectTx: %v", err)
	}
	if n != 1 {
		t.Fatalf("ProjectTx projected %d, want 1", n)
	}
	var got husdindex.Credit
	for _, c := range ledger.seen {
		got = c
	}
	if got.OrgID != org || got.Subject != subject || got.AmountCents != amountCents || got.Tag != "credit:husd" || !got.Test {
		t.Fatalf("projected credit wrong: %+v", got)
	}
	t.Logf("PROVEN ProjectTx: org=%s subject=%s %dc tag=%s test=%v dedup=%s (from real receipt)",
		got.OrgID, got.Subject, got.AmountCents, got.Tag, got.Test, got.DedupKey)

	// Idempotent: a second ProjectTx (or a later Sync) lands on the same dedup key.
	if _, err := ix.ProjectTx(context.Background(), rc.TxHash); err != nil {
		t.Fatal(err)
	}
	if len(ledger.seen) != 1 {
		t.Fatalf("re-project produced %d credits, want 1 (double credit)", len(ledger.seen))
	}
	t.Log("HEADLINE step 4: a mint projects exactly one bucket+subject+test-tagged credit from its real on-chain receipt, idempotently.")
}

// sendNative signs+submits an EIP-155 native-value tx (to fund an org address
// with gas so it can sign its own settlement sweep). 21000 gas, no data.
func sendNative(ctx context.Context, rpc, keyHex, to string, value *big.Int) (string, error) {
	priv, err := luxcrypto.HexToECDSA(strings.TrimPrefix(keyHex, "0x"))
	if err != nil {
		return "", err
	}
	from := luxcrypto.PubkeyToAddress(priv.PublicKey).Hex()
	nonce, err := rpcHexUint(ctx, rpc, "eth_getTransactionCount", from, "pending")
	if err != nil {
		return "", err
	}
	gp, err := rpcHexBig(ctx, rpc, "eth_gasPrice")
	if err != nil || gp.Sign() == 0 {
		gp = big.NewInt(25_000_000_000)
	}
	sigPayload := rlpList(rlpUint(nonce), rlpBig(gp), rlpUint(21000), rlpStr(mustHex(to)), rlpBig(value), rlpStr(nil),
		rlpUint(uint64(husdTestnetChain)), rlpStr(nil), rlpStr(nil))
	sig, err := luxcrypto.Sign(luxcrypto.Keccak256(sigPayload), priv)
	if err != nil {
		return "", err
	}
	r := new(big.Int).SetBytes(sig[0:32])
	s := new(big.Int).SetBytes(sig[32:64])
	v := uint64(sig[64]) + 35 + 2*uint64(husdTestnetChain)
	signed := rlpList(rlpUint(nonce), rlpBig(gp), rlpUint(21000), rlpStr(mustHex(to)), rlpBig(value), rlpStr(nil),
		rlpUint(v), rlpBig(r), rlpBig(s))
	return rpcSendRaw(ctx, rpc, "0x"+hex.EncodeToString(signed))
}

// TestOnChain_Step5_Settlement proves the settlement sweep on chain: after a mint
// gives an org an on-chain balance, the org's OWN derived key signs an
// org→treasury HUSD transfer (funded with gas from the treasury), and the org's
// balanceOf drops by exactly the swept amount while the treasury's rises by it —
// the on-chain half of driving balanceOf back down to the off-chain ledger.
func TestOnChain_Step5_Settlement(t *testing.T) {
	rpc := os.Getenv("HUSD_RPC_URL")
	key := os.Getenv("HUSD_TREASURY_KEY")
	if rpc == "" || key == "" {
		t.Skip("set HUSD_RPC_URL + HUSD_TREASURY_KEY to run the live testnet settlement proof")
	}
	token := husdTestnetToken
	if v := os.Getenv("HUSD_TOKEN_ADDRESS"); v != "" {
		token = v
	}
	cfg := husd.Config{ChainID: husdTestnetChain, RPCURL: rpc, TokenAddress: token, TreasuryKey: key, Decimals: husdDecimals}
	seed := []byte("hanzo-husd-org-derivation-onchain-proof-seed-v1")
	ctx := context.Background()

	org := fmt.Sprintf("husd-settle-%d", time.Now().UnixNano())
	acct, err := treasury.DeriveAccount(seed, org)
	if err != nil {
		t.Fatal(err)
	}
	treasuryAddr, err := treasury.AddressForKey(key)
	if err != nil {
		t.Fatal(err)
	}
	client := husdindex.NewClient(rpc, token)

	// 1) Mint $50 to the org (treasury → org).
	tr := treasury.New(cfg, seed, newMemStore())
	rc, err := tr.Mint(ctx, treasury.MintRequest{OrgID: org, AmountCents: 5000, Bucket: treasury.BucketPrepaid, Test: true, IdemKey: "settle-mint:" + org})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if status, _ := waitReceipt(t, rpc, rc.TxHash); status != "0x1" {
		t.Fatalf("mint status != 0x1")
	}
	orgBal0, _ := client.BalanceOf(ctx, acct.Address)
	treBal0, _ := client.BalanceOf(ctx, treasuryAddr)
	t.Logf("after mint: org=%s (%s) balance=%s treasury balance=%s", org, acct.Address, orgBal0, treBal0)

	// 2) Fund the org address with gas (native) from the treasury.
	gasHash, err := sendNative(ctx, rpc, key, acct.Address, big.NewInt(20_000_000_000_000_000)) // 0.02
	if err != nil {
		t.Fatalf("fund gas: %v", err)
	}
	if status, _ := waitReceipt(t, rpc, gasHash); status != "0x1" {
		t.Fatalf("gas funding status != 0x1")
	}

	// 3) Settlement sweep: $20 org → treasury, signed by the ORG's derived key.
	driftWei, _ := husd.CentsToWei(2000, husdDecimals)
	sweepHash, err := blockchain.TransferToken(ctx, blockchain.TokenTransfer{
		ChainID: cfg.ChainID, RPCURL: rpc, TokenAddress: token,
		TreasuryKey: acct.PrivateKeyHex(), // SIGNER = org key → from = orgAddr
		To:          treasuryAddr, AmountWei: driftWei, GasLimit: 100000,
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if status, _ := waitReceipt(t, rpc, sweepHash); status != "0x1" {
		t.Fatalf("sweep status != 0x1")
	}
	t.Logf("REAL org→treasury settlement sweep tx: %s ($20.00, org-key-signed)", sweepHash)

	// 4) org dropped exactly $20; treasury rose exactly $20.
	orgBal1, _ := client.BalanceOf(ctx, acct.Address)
	treBal1, _ := client.BalanceOf(ctx, treasuryAddr)
	orgDrop := new(big.Int).Sub(orgBal0, orgBal1)
	treGain := new(big.Int).Sub(treBal1, treBal0)
	if orgDrop.Cmp(driftWei) != 0 {
		t.Fatalf("org balance dropped %s, want %s", orgDrop, driftWei)
	}
	if treGain.Cmp(driftWei) != 0 {
		t.Fatalf("treasury balance rose %s, want %s", treGain, driftWei)
	}
	orgCents, _ := husdindex.OnChainBalanceCents(ctx, client, acct.Address, husdDecimals)
	t.Logf("PROVEN settlement: org swept $20 → treasury; org now holds %d cents (== $30 spendable)", orgCents)
	if orgCents != 3000 {
		t.Fatalf("org post-settle balance = %d cents, want 3000", orgCents)
	}
	t.Log("HEADLINE step 5: metered usage settles org→treasury (org-key-signed), reconciling on-chain balance to the ledger — proven on testnet 36962.")
}
