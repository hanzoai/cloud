package cek

import (
	"os"
	"testing"
)

// The dev opt-out has exactly one job: relax the NO-KEY case on a capable build,
// and only when asked explicitly. These cases pin the boundary, because the
// failure mode of getting it wrong is a production binary that silently writes
// plaintext — the thing the whole package exists to prevent.
func TestDevUnencrypted_OnlyExplicitTruthyValuesCount(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"yes", true},
		{"on", true},
		{" 1 ", true}, // trimmed — a trailing newline from a shell export still counts

		// Everything else is OFF. "0" and "" matter most: an empty export
		// inherited from a parent shell, or a deliberate disable, must never be
		// read as consent to drop encryption.
		{"", false},
		{"0", false},
		{"false", false},
		{"no", false},
		{"off", false},
		{"maybe", false},
		{"2", false},
	}
	for _, tc := range cases {
		t.Run("val="+tc.val, func(t *testing.T) {
			t.Setenv(DevUnencryptedEnv, tc.val)
			if got := devUnencrypted(); got != tc.want {
				t.Fatalf("devUnencrypted() with %q = %v, want %v", tc.val, got, tc.want)
			}
		})
	}
}

// Absent entirely is not opted in. Separate from the empty-string case above:
// unset and set-to-empty reach different branches of os.Getenv.
func TestDevUnencrypted_UnsetIsOff(t *testing.T) {
	os.Unsetenv(DevUnencryptedEnv)
	if devUnencrypted() {
		t.Fatal("devUnencrypted() = true with the variable unset; the default must be OFF")
	}
}

// The two variables must stay distinct. Overloading one to mean both "here is
// the key" and "run unencrypted" is how a deployment goes plaintext because
// someone put the wrong value in a secret.
func TestDevUnencrypted_IsNotTheMasterKeyVariable(t *testing.T) {
	if DevUnencryptedEnv == MasterKeyEnv {
		t.Fatalf("the dev opt-out and the master key must be different variables; both are %q", MasterKeyEnv)
	}
}
