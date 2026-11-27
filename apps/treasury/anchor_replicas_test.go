package treasury

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/treasury/ledger"
	"github.com/hanzoai/cloud/apps/treasury/ledger/sqlstore"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
	"github.com/luxfi/crypto"
	"github.com/luxfi/geth/common"
	"github.com/luxfi/geth/core/types"
	luxlog "github.com/luxfi/log"
)

// chain is the part of an EVM node an anchor talks to, and as strict about the one
// thing that matters here: a transaction is accepted only at the sender's next nonce,
// and what it was accepted at is kept. Reading the nonce is slow, as a node's is under
// load, so two senders that read together read the same one.
type chain struct {
	mu     sync.Mutex
	nonces []uint64
	hashes map[string]bool
	refuse int
}

func (c *chain) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     json.RawMessage   `json:"id"`
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	reply := func(result any, rpcErr string) {
		out := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if rpcErr != "" {
			out["error"] = map[string]any{"code": -32000, "message": rpcErr}
		} else {
			out["result"] = result
		}
		_ = json.NewEncoder(w).Encode(out)
	}
	c.mu.Lock()
	next := uint64(len(c.nonces))
	c.mu.Unlock()
	switch req.Method {
	case "eth_chainId":
		reply(fmt.Sprintf("0x%x", defaultHanzoChainID), "")
	case "eth_getTransactionCount":
		time.Sleep(60 * time.Millisecond)
		reply(fmt.Sprintf("0x%x", next), "")
	case "eth_gasPrice":
		reply("0x3b9aca00", "")
	case "eth_sendRawTransaction":
		var raw string
		_ = json.Unmarshal(req.Params[0], &raw)
		var tx types.Transaction
		if err := tx.UnmarshalBinary(common.FromHex(raw)); err != nil {
			reply(nil, "bad transaction: "+err.Error())
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if tx.Nonce() != uint64(len(c.nonces)) {
			c.refuse++
			reply(nil, fmt.Sprintf("nonce too low: next %d, tx %d", len(c.nonces), tx.Nonce()))
			return
		}
		c.nonces = append(c.nonces, tx.Nonce())
		c.hashes[tx.Hash().Hex()] = true
		reply(tx.Hash().Hex(), "")
	case "eth_getTransactionReceipt":
		var h string
		_ = json.Unmarshal(req.Params[0], &h)
		c.mu.Lock()
		known := c.hashes[h]
		c.mu.Unlock()
		if !known {
			reply(nil, "")
			return
		}
		reply(map[string]any{
			"type": "0x0", "status": "0x1", "cumulativeGasUsed": "0x5208", "gasUsed": "0x5208",
			"logsBloom": "0x" + strings.Repeat("00", 256), "logs": []any{},
			"transactionHash": h, "blockHash": "0x" + strings.Repeat("11", 32), "blockNumber": "0x5", "transactionIndex": "0x0",
		}, "")
	default:
		reply(nil, "unsupported "+req.Method)
	}
}

// Two replicas asked to anchor the same book at once both land: they send in turn, the
// second reading the nonce after the first's transaction is in, so the chain holds
// nonces 0 and 1 and refused nothing. Sent together they read the same nonce and the
// chain keeps one; the other fails after its root was computed and its signer asked. The
// book remembers the last anchor for every replica.
//
// MUTATION: skip the lock in submit and the second replica's transaction is refused
// for a nonce already used.
func TestTwoReplicasAnchorInTurn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		node := &chain{hashes: map[string]bool{}}
		rpc := httptest.NewServer(node)
		defer rpc.Close()

		priv, err := crypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		BindAnchorSigner(common.Address(crypto.PubkeyToAddress(priv.PublicKey)), func(_ context.Context, hash []byte) ([]byte, error) {
			return crypto.Sign(hash, priv)
		})
		t.Cleanup(func() { BindAnchorSigner(common.Address{}, nil) })

		var books []*sqlstore.Store
		for range 2 {
			if len(books) > 0 && locus.DSN() == "" {
				books = append(books, books[0])
				continue
			}
			b, err := sqlstore.Opener(testMaster, "treasury", "")(namespace.System(), open(namespace.System(), "treasury"))
			if err != nil {
				t.Fatalf("open the book: %v", err)
			}
			t.Cleanup(func() { _ = b.Close() })
			books = append(books, b)
		}
		pods := make([]*anchorer, len(books))
		for i, b := range books {
			pods[i] = &anchorer{rpcURL: rpc.URL, chainID: defaultHanzoChainID, log: luxlog.New("test"), records: b}
		}

		var wg sync.WaitGroup
		errs := make([]error, len(pods))
		for i, a := range pods {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = a.submit(context.Background(), ledger.New(books[i]))
			}()
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Errorf("replica %d did not anchor: %v", i, err)
			}
		}
		node.mu.Lock()
		sent, refused := append([]uint64(nil), node.nonces...), node.refuse
		node.mu.Unlock()
		if len(sent) != 2 || sent[0] != 0 || sent[1] != 1 || refused != 0 {
			t.Fatalf("the chain holds nonces %v and refused %d; want 0 and 1 and nothing refused", sent, refused)
		}
		for i, b := range books {
			if last, ok, err := b.Anchor(context.Background()); err != nil || !ok || last.Block != 5 || last.TxHash == "" {
				t.Fatalf("replica %d remembers %+v, %v, %v", i, last, ok, err)
			}
		}
	})
}
