package marketplace

import (
	"math/big"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/luxfi/crypto"
)

// word is v as one ABI word.
func word(v int64) []byte {
	b := make([]byte, 32)
	big.NewInt(v).FillBytes(b)
	return b
}

func topicOf(v int64) [32]byte {
	var t [32]byte
	copy(t[:], word(v))
	return t
}

// The topics are the Keccak-256 of the contracts' own signatures: a log the
// escrow emits is read by exactly the event it is.
func TestTopicsAreTheContractsSignatures(t *testing.T) {
	for sig, topic := range map[string][32]byte{TransitionEvent: TransitionTopic, RatedEvent: RatedTopic} {
		var want [32]byte
		copy(want[:], crypto.Keccak256([]byte(sig)))
		if topic != want {
			t.Fatalf("%s topic %x, want %x", sig, topic, want)
		}
	}
}

// A Transition decodes word for word as IEconomic declares it, and only the step
// that pays the seller settles a job.
func TestTransitionDecodes(t *testing.T) {
	token := make([]byte, 32)
	copy(token[12:], []byte("0123456789abcdefghij"))
	var principal [32]byte
	principal[31] = 7
	data := append([]byte{}, word(4)...) // Released
	data = append(data, word(1)...)      // Service
	data = append(data, principal[:]...)
	data = append(data, word(0)...)
	data = append(data, token...)
	data = append(data, word(250_000_000)...)
	data = append(data, word(250_000_000)...)
	data = append(data, word(0)...)
	data = append(data, word(9)...)
	l := Log{Topics: [][32]byte{TransitionTopic, topicOf(42), topicOf(0), topicOf(17)}, Data: data}
	tr, err := DecodeTransition(l)
	if err != nil {
		t.Fatal(err)
	}
	if tr.ID.Int64() != 42 || tr.SellerAgent.Int64() != 17 || tr.BuyerAgent.Sign() != 0 || tr.State != StatusReleased ||
		tr.Category != client.CategoryService || tr.BuyerPrincipal != principal || tr.Amount.Int64() != 250_000_000 ||
		tr.Paid.Int64() != 250_000_000 || tr.Refunded.Sign() != 0 || tr.Token != "0x303132333435363738396162636465666768696a" {
		t.Fatalf("decoded %+v", tr)
	}
	if !tr.Settles() {
		t.Fatal("a release that paid the seller does not settle the job")
	}
	// A refund pays nobody.
	refund := append([]byte{}, data...)
	copy(refund[0:32], word(7))
	copy(refund[6*32:7*32], word(0))
	copy(refund[7*32:8*32], word(250_000_000))
	if tr, err := DecodeTransition(Log{Topics: l.Topics, Data: refund}); err != nil || tr.State != StatusRefunded || tr.Settles() {
		t.Fatalf("a refund = %+v %v, want refunded and settling nothing", tr, err)
	}
	// What is not a Transition, or not one the enums know, is refused.
	for name, bad := range map[string]Log{
		"another event":  {Topics: [][32]byte{RatedTopic, topicOf(1), topicOf(2), topicOf(3)}, Data: data},
		"short data":     {Topics: l.Topics, Data: data[:64]},
		"state None":     {Topics: l.Topics, Data: append(word(0), data[32:]...)},
		"state past end": {Topics: l.Topics, Data: append(word(10), data[32:]...)},
	} {
		if _, err := DecodeTransition(bad); err == nil {
			t.Fatalf("%s decoded", name)
		}
	}
}

// A Rated decodes as Feedback declares it.
func TestRatedDecodes(t *testing.T) {
	var payer [32]byte
	copy(payer[12:], []byte("buyer-wallet-address"))
	r, err := DecodeRated(Log{Topics: [][32]byte{RatedTopic, topicOf(42), topicOf(17), payer}, Data: word(3)})
	if err != nil {
		t.Fatal(err)
	}
	if r.Job.Int64() != 42 || r.Agent.Int64() != 17 || r.Index != 3 || r.Client != "0x62757965722d77616c6c65742d61646472657373" {
		t.Fatalf("decoded %+v", r)
	}
}
