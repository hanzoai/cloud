//go:build controlplane

package controlplane

import (
	"sync"

	pulsar "github.com/luxfi/pulsar/pkg/pulsar"
)

// MsgKind tags a ceremony message.
type MsgKind uint8

const (
	// MsgProposal carries a candidate placement block. Voters recompute the
	// round digest from the block themselves — the proposer's word on the
	// digest is never trusted.
	MsgProposal MsgKind = iota + 1
	// MsgRound1 carries a voter's nonce commitment (SignRound1).
	MsgRound1
	// MsgRound2 carries a voter's revealed share partial.
	MsgRound2
)

// Message is a ceremony envelope. Only the fields for its Kind are meaningful.
type Message struct {
	Kind   MsgKind
	Height uint64
	Round  uint32
	From   pulsar.NodeID
	Block  *PlacementBlock
	R1     pulsar.SignRound1
	R2     pulsar.Partial
	// Commit is the sender's binding commitment to the z-share it will reveal
	// in Round2 (MsgRound1 only). The orchestration-layer anti-rush lock: the
	// share revealed in Round2 must open this commitment.
	Commit []byte
}

// Transport is the abstract message layer. The ceremony is defined entirely
// over Broadcast + Inbox; the in-memory bus below is the harness impl and real
// ZAP messaging is a later increment. Nothing in the driver depends on the
// concrete transport.
type Transport interface {
	// Broadcast delivers msg to every OTHER live node. A node processes its
	// own legs locally, so self-delivery is intentionally omitted.
	Broadcast(msg Message)
	// Inbox is the channel a node receives on.
	Inbox(node pulsar.NodeID) <-chan Message
}

// memBus is an in-memory broadcast Transport for the harness. It supports
// partitioning a node (SetAlive) to model crash faults, and — because it is a
// plain Transport — a byzantine driver can inject any crafted Message through
// Broadcast, which is how the adversarial tests drive equivocation and
// duplicate/rogue legs.
type memBus struct {
	mu      sync.Mutex
	inboxes map[pulsar.NodeID]chan Message
	alive   map[pulsar.NodeID]bool
}

// NewMemBus creates an in-memory bus for the given node set.
func NewMemBus(nodes []pulsar.NodeID) *memBus {
	b := &memBus{
		inboxes: map[pulsar.NodeID]chan Message{},
		alive:   map[pulsar.NodeID]bool{},
	}
	for _, n := range nodes {
		b.inboxes[n] = make(chan Message, 4096)
		b.alive[n] = true
	}
	return b
}

// SetAlive marks a node up or down. A down node neither receives nor (by driver
// convention) sends — modeling a crashed pod / network partition.
func (b *memBus) SetAlive(node pulsar.NodeID, up bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.alive[node] = up
}

func (b *memBus) isAlive(node pulsar.NodeID) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.alive[node]
}

// Broadcast fans msg out to every live node except the sender. Delivery is
// best-effort non-blocking on generously buffered inboxes; a full inbox drops
// the message, modeling a lossy link (the ceremony tolerates loss up to the
// fault bound).
func (b *memBus) Broadcast(msg Message) {
	b.mu.Lock()
	targets := make([]chan Message, 0, len(b.inboxes))
	for node, ch := range b.inboxes {
		if node == msg.From || !b.alive[node] {
			continue
		}
		targets = append(targets, ch)
	}
	b.mu.Unlock()
	for _, ch := range targets {
		select {
		case ch <- msg:
		default:
		}
	}
}

func (b *memBus) Inbox(node pulsar.NodeID) <-chan Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.inboxes[node]
}
