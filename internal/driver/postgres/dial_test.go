package postgres

import (
	"testing"
	"time"
)

// The bound on reaching a server is dialTimeout unless CLOUD_SQL_DIAL_TIMEOUT names a
// positive duration, and a value that is not one never shortens it to nothing.
func TestDialBoundReadsAPositiveDurationAndOtherwiseKeepsTheDefault(t *testing.T) {
	for _, c := range []struct {
		env  string
		want time.Duration
	}{
		{"", dialTimeout},
		{"30s", 30 * time.Second},
		{" 45s ", 45 * time.Second},
		{"1m30s", 90 * time.Second},
		{"30", dialTimeout},
		{"soon", dialTimeout},
		{"0s", dialTimeout},
		{"-5s", dialTimeout},
	} {
		t.Setenv(DialTimeoutEnv, c.env)
		if got := dialBound(); got != c.want {
			t.Errorf("%s=%q: bound %v, want %v", DialTimeoutEnv, c.env, got, c.want)
		}
	}
}
