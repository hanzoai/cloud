package approval

import (
	"errors"
	"testing"
)

func TestNextStatus(t *testing.T) {
	cases := []struct {
		name    string
		current string
		action  string
		want    string
		wantErr error
	}{
		{"pending approve -> approved", StatusPending, ActionApprove, StatusApproved, nil},
		{"pending reject -> rejected", StatusPending, ActionReject, StatusRejected, nil},
		{"pending unknown action -> error", StatusPending, "escalate", StatusPending, ErrUnknownAction},
		{"approved approve -> already resolved", StatusApproved, ActionApprove, StatusApproved, ErrAlreadyResolved},
		{"approved reject -> already resolved", StatusApproved, ActionReject, StatusApproved, ErrAlreadyResolved},
		{"rejected approve -> already resolved", StatusRejected, ActionApprove, StatusRejected, ErrAlreadyResolved},
		{"rejected reject -> already resolved", StatusRejected, ActionReject, StatusRejected, ErrAlreadyResolved},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NextStatus(c.current, c.action)
			if got != c.want {
				t.Fatalf("NextStatus(%q, %q) status = %q, want %q", c.current, c.action, got, c.want)
			}
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("NextStatus(%q, %q) err = %v, want %v", c.current, c.action, err, c.wantErr)
			}
		})
	}
}
