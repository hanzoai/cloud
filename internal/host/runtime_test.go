package host

// Tests for the agent-runtime meter. They drive the real functions with an
// in-memory watermark and a capturing emit, so the whole exactly-once path is
// exercised with no commerce, no store and no clock.
//
// Each test names the MUTATION that makes it fail, because a money test that
// passes against a broken meter is worse than no test: it is a green build over a
// revenue leak.

import (
	"sync"

	"github.com/hanzoai/cloud/sdk/meter"
)

func (d *debits) emit(_ string, u meter.Usage) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rows = append(d.rows, u)
}

func (d *debits) micros() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	var n int64
	for _, u := range d.rows {
		n += u.AmountMicros
	}
	return n
}

func (d *debits) len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.rows)
}

// debits captures what the meter would have sent to the ledger.
type debits struct {
	mu   sync.Mutex
	rows []meter.Usage
}
