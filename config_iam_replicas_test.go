package cloud

import "testing"

// TestValidateIAMSingleReplica locks the single-replica contract embedded IAM
// requires (Beego's process-local "memory" session store): an iam-enabled cloud
// above 1 replica must refuse to boot, while replicas=1, an unset count, and
// iam-disabled-at-any-scale all pass.
func TestValidateIAMSingleReplica(t *testing.T) {
	base := func() *Config {
		return &Config{Brand: "hanzo", Domain: "api.hanzo.ai", DataDir: "/var/lib/cloud"}
	}
	cases := []struct {
		name     string
		enable   []string
		replicas int
		wantErr  bool
	}{
		{"iam enabled, 2 replicas -> refuse", []string{"iam", "kms"}, 2, true},
		{"iam enabled, 3 replicas -> refuse", []string{"iam"}, 3, true},
		{"iam enabled, 1 replica -> ok", []string{"iam"}, 1, false},
		{"iam enabled, replicas unset -> ok", []string{"iam"}, 0, false},
		{"iam disabled, 5 replicas -> ok", []string{"kms", "o11y"}, 5, false},
		// IAM is a STAGED subsystem (stagedSubsystems["iam"]): the empty-Enable
		// "mount everything" default deliberately does NOT mount it (booting the
		// IAM embed under mount-all corrupts the shared Beego global and crashes
		// the `ai` subsystem — see config.go). So an empty list is iam-DISABLED,
		// and >1 replica is allowed. The guard only fires when iam is EXPLICITLY
		// enabled (the cases above), which is the sole way IAM ever runs.
		{"empty list is iam-staged/disabled, 4 replicas -> ok", nil, 4, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base()
			c.Enable = tc.enable
			c.Replicas = tc.replicas
			err := c.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("Validate() = nil, want error (iam + replicas=%d)", tc.replicas)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}
