package organization

import (
	"os"
	"testing"
)

// SQUARE_ENVIRONMENT is the single authority for the test/live (sandbox vs
// production) split; org.Live is the fallback only when the var is UNSET. Any
// SET-but-unrecognized value fails CLOSED to test/sandbox.
func TestOrganization_TestMode(t *testing.T) {
	cases := []struct {
		name   string
		env    string
		envSet bool
		live   bool
		want   bool // want test mode (sandbox)?
	}{
		{"production env overrides live org", "production", true, true, false},
		{"production env overrides test org", "production", true, false, false},
		{"prod alias", "prod", true, false, false},
		{"live alias", "live", true, false, false},
		{"mixed-case PRODUCTION", "PRODUCTION", true, false, false},
		{"sandbox env overrides live org", "sandbox", true, true, true},
		{"sandbox env, test org", "sandbox", true, false, true},
		{"test alias", "test", true, true, true},
		{"padded sandbox", "  sandbox  ", true, true, true},
		{"unset -> live org is production", "", false, true, false},
		{"unset -> test org is sandbox", "", false, false, true},
		// L1: any SET-but-unrecognized value fails CLOSED to sandbox, even for a
		// live org — a typo on a sandbox-intended deploy must not charge production.
		{"garbage set, live org -> fail-closed sandbox", "produciton", true, true, true},
		{"garbage set, test org -> sandbox", "bogus", true, false, true},
		{"empty placeholder, live org -> fail-closed sandbox", "", true, true, true},
		{"whitespace placeholder, live org -> fail-closed sandbox", "   ", true, true, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.envSet {
				t.Setenv("SQUARE_ENVIRONMENT", tc.env)
			} else if v, ok := os.LookupEnv("SQUARE_ENVIRONMENT"); ok {
				os.Unsetenv("SQUARE_ENVIRONMENT")
				t.Cleanup(func() { _ = os.Setenv("SQUARE_ENVIRONMENT", v) })
			}

			org := Organization{}
			org.Live = tc.live

			if got := org.TestMode(); got != tc.want {
				t.Fatalf("TestMode(live=%v, env=%q set=%v) = %v, want %v", tc.live, tc.env, tc.envSet, got, tc.want)
			}

			wantEnv := "production"
			if tc.want {
				wantEnv = "sandbox"
			}
			if got := org.SquareEnvironment(); got != wantEnv {
				t.Fatalf("SquareEnvironment = %q, want %q", got, wantEnv)
			}
		})
	}
}
