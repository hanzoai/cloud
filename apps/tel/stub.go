package tel

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// stubCarrier answers without a network.
//
// It is what the suite runs against, and what a deployment with no carrier
// credential falls back to — so the surface can be exercised end to end without
// spending a call. Its numbers are in the +1 555 range, which is reserved for
// fiction precisely so nobody dials one by accident.
type stubCarrier struct {
	mu   sync.Mutex
	seq  int
	held map[string]Number
}

func newStub() *stubCarrier { return &stubCarrier{held: map[string]Number{}} }

func (s *stubCarrier) id(prefix string) string {
	s.seq++
	return fmt.Sprintf("%s_%06d", prefix, s.seq)
}

func (s *stubCarrier) Search(_ context.Context, q NumberQuery) ([]Number, error) {
	if q.Country == "" {
		return nil, fmt.Errorf("country is required")
	}
	n := q.Limit
	if n <= 0 || n > 20 {
		n = 5
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Number, 0, n)
	for i := 0; i < n; i++ {
		s.seq++
		out = append(out, Number{
			ID:      fmt.Sprintf("stub_%06d", s.seq),
			E164:    fmt.Sprintf("+1555%07d", 1000000+s.seq),
			Country: strings.ToUpper(q.Country),
			Type:    orDefault(q.Type, "local"),
			Capable: []string{"voice", "sms"},
			Monthly: 100, Currency: "USD",
		})
	}
	return out, nil
}

func (s *stubCarrier) Buy(_ context.Context, e164 string) (Number, error) {
	if e164 == "" {
		return Number{}, fmt.Errorf("a number is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := Number{ID: s.id("num"), E164: e164, Country: "US", Type: "local",
		Capable: []string{"voice", "sms"}, Monthly: 100, Currency: "USD"}
	s.held[n.ID] = n
	return n, nil
}

func (s *stubCarrier) Release(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.held[id]; !ok {
		return fmt.Errorf("no such number")
	}
	delete(s.held, id)
	return nil
}

func (s *stubCarrier) Call(_ context.Context, r CallRequest) (Call, error) {
	if r.To == "" || r.From == "" {
		return Call{}, fmt.Errorf("from and to are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return Call{ID: s.id("call"), From: r.From, To: r.To, Status: "queued", Agent: r.Agent}, nil
}

func (s *stubCarrier) Hangup(context.Context, string) error { return nil }

func (s *stubCarrier) Send(_ context.Context, r SMSRequest) (SMS, error) {
	if r.To == "" || r.From == "" {
		return SMS{}, fmt.Errorf("from and to are required")
	}
	if r.Text == "" && len(r.Media) == 0 {
		return SMS{}, fmt.Errorf("a message needs text or media")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return SMS{ID: s.id("msg"), From: r.From, To: r.To, Text: r.Text, Status: "queued"}, nil
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}
