package marketplace

// chain.go — the Lux agent contracts, as the marketplace reads them.
//
// luxfi/standard's contracts/agents is the chain half of this market: Jobs, the
// escrow a buyer funds and only the parties (or two of buyer, seller and arbiter)
// can move; Settlement, the direct x402 rail on chain; and Feedback, the ERC-8004
// reputation registry that only a settled job's parties may write. Both payment
// contracts emit one event, IEconomic.Transition, for every step of every payment,
// and Feedback emits Rated for every rating a job earned. Everything the market
// needs is in those logs.
//
// The event layouts below are the contracts' own on luxfi/standard main,
// signature for signature, and decode is tested against them (chain_test.go).
// What is NOT here yet is a deployment: DEPLOYMENTS.md lists no Jobs or Feedback,
// so no escrow address, no network and no log source exists to bind. Until one
// does, Chain is nil and a job on the chain rail is refused 503 naming this.
// Binding it is: implement Chain over the
// deployment's RPC, set state.chain in Use, and let the sweep read Transitions into
// jobs and Rated into reputation.

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/hanzoai/cloud/client"
	"github.com/luxfi/crypto"
)

// unbound names what the chain rail waits on, in every refusal that cites it.
const unbound = "luxfi/standard contracts/agents (Jobs, Feedback) has no deployment to read"

// The events, exactly as the contracts declare them.
const (
	// TransitionEvent is IEconomic.Transition, which Jobs and Settlement emit.
	TransitionEvent = "Transition(uint256,uint256,uint256,uint8,uint8,bytes32,bytes32,address,uint256,uint256,uint256,bytes32)"
	// RatedEvent is Feedback.Rated.
	RatedEvent = "Rated(uint256,uint256,address,uint64)"
)

// Topics are the events' log topics: the Keccak-256 of each signature.
var (
	TransitionTopic = topic(TransitionEvent)
	RatedTopic      = topic(RatedEvent)
)

func topic(sig string) [32]byte {
	var t [32]byte
	copy(t[:], crypto.Keccak256([]byte(sig)))
	return t
}

// Chain is what the marketplace needs from a deployment of the agent contracts:
// where a buyer funds a job, and the logs the contracts emitted.
type Chain interface {
	// Escrow is the Jobs contract a buyer funds: its CAIP-2 network and address.
	Escrow() (network, address string)
	// Reputation is the Feedback registry's address, on the same network.
	Reputation() string
	// Logs answers the logs contract emitted under topic from block on, in chain
	// order, and the block to read from next.
	Logs(ctx context.Context, contract string, topic [32]byte, from uint64) ([]Log, uint64, error)
}

// Log is one EVM log.
type Log struct {
	// Topics are the indexed words, the event's topic first.
	Topics [][32]byte
	// Data is the ABI-encoded non-indexed fields.
	Data []byte
	// Block, Tx and Index place it on chain.
	Block uint64
	Tx    string
	Index uint
}

// The escrow's states and categories, in IEconomic's enum order.
var (
	states     = []string{"", StatusOpen, StatusAccepted, StatusDelivered, StatusReleased, StatusDisputed, "resolved", StatusRefunded, StatusCancelled, "paid"}
	categories = []string{"", client.CategoryService, client.CategoryGoods, client.CategoryTransfer, client.CategoryRefund, client.CategoryRoyalty}
)

// Transition is one IEconomic.Transition.
type Transition struct {
	// ID is the job (Jobs) or payment (Settlement) id.
	ID *big.Int
	// BuyerAgent and SellerAgent are the ERC-8004 agent ids; zero for a buyer that
	// is no registered agent.
	BuyerAgent, SellerAgent *big.Int
	// State is the state entered, in this package's words ("resolved" and "paid"
	// are the chain's own).
	State string
	// Category is what the payment is for, in the economic event's words.
	Category string
	// BuyerPrincipal and SellerPrincipal are the principals' commitments.
	BuyerPrincipal, SellerPrincipal [32]byte
	// Token is the ERC-20 paid in.
	Token string
	// Amount is the whole payment; Paid what this step credited the seller and
	// Refunded what it credited back to the buyer, in the token's units.
	Amount, Paid, Refunded *big.Int
	// Delivery is the seller's delivery hash, or a direct payment's reference.
	Delivery [32]byte
}

// DecodeTransition reads a Transition out of a log: three indexed words (id,
// buyerAgent, sellerAgent) after the topic, and nine words of data.
func DecodeTransition(l Log) (Transition, error) {
	if len(l.Topics) != 4 || l.Topics[0] != TransitionTopic {
		return Transition{}, errors.New("marketplace: not a Transition log")
	}
	if len(l.Data) != 9*32 {
		return Transition{}, fmt.Errorf("marketplace: Transition data is %d bytes, want %d", len(l.Data), 9*32)
	}
	word := func(i int) []byte { return l.Data[i*32 : (i+1)*32] }
	enum := func(i int, names []string) (string, error) {
		n := new(big.Int).SetBytes(word(i))
		if !n.IsUint64() || n.Uint64() == 0 || n.Uint64() >= uint64(len(names)) {
			return "", fmt.Errorf("marketplace: Transition word %d is %s, not an enum value", i, n)
		}
		return names[n.Uint64()], nil
	}
	t := Transition{
		ID:          new(big.Int).SetBytes(l.Topics[1][:]),
		BuyerAgent:  new(big.Int).SetBytes(l.Topics[2][:]),
		SellerAgent: new(big.Int).SetBytes(l.Topics[3][:]),
		Amount:      new(big.Int).SetBytes(word(5)),
		Paid:        new(big.Int).SetBytes(word(6)),
		Refunded:    new(big.Int).SetBytes(word(7)),
	}
	var err error
	if t.State, err = enum(0, states); err != nil {
		return Transition{}, err
	}
	if t.Category, err = enum(1, categories); err != nil {
		return Transition{}, err
	}
	copy(t.BuyerPrincipal[:], word(2))
	copy(t.SellerPrincipal[:], word(3))
	if !zero(word(4)[:12]) {
		return Transition{}, errors.New("marketplace: Transition token is not an address")
	}
	t.Token = "0x" + strings.ToLower(fmt.Sprintf("%x", word(4)[12:]))
	copy(t.Delivery[:], word(8))
	return t, nil
}

// Rating is one Feedback.Rated: which job earned rating index Index from Client
// to Agent.
type Rating struct {
	Job, Agent *big.Int
	Client     string
	Index      uint64
}

// DecodeRated reads a Rated out of a log: three indexed words (job, agentId,
// clientAddress) after the topic, and one word of data.
func DecodeRated(l Log) (Rating, error) {
	if len(l.Topics) != 4 || l.Topics[0] != RatedTopic || len(l.Data) != 32 {
		return Rating{}, errors.New("marketplace: not a Rated log")
	}
	if !zero(l.Topics[3][:12]) {
		return Rating{}, errors.New("marketplace: Rated client is not an address")
	}
	idx := new(big.Int).SetBytes(l.Data)
	if !idx.IsUint64() {
		return Rating{}, errors.New("marketplace: Rated index overflows uint64")
	}
	return Rating{Job: new(big.Int).SetBytes(l.Topics[1][:]), Agent: new(big.Int).SetBytes(l.Topics[2][:]),
		Client: "0x" + fmt.Sprintf("%x", l.Topics[3][12:]), Index: idx.Uint64()}, nil
}

// Settles reports whether a Transition is the one step of a job that pays the
// seller — the step that states a job's one economic event: released, or resolved
// with a share to the seller. A step that moves nothing, or only returns the
// buyer's money, pays nobody.
func (t Transition) Settles() bool {
	return (t.State == StatusReleased || t.State == "resolved") && t.Paid.Sign() > 0
}

func zero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
