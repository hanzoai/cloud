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
		{"iam enabled, 2 replicas -> refuse", []string{"iam", "kmssvc"}, 2, true},
		{"iam enabled, 3 replicas -> refuse", []string{"iam"}, 3, true},
		{"iam enabled, 1 replica -> ok", []string{"iam"}, 1, false},
		{"iam enabled, replicas unset -> ok", []string{"iam"}, 0, false},
		{"iam disabled, 5 replicas -> ok", []string{"kmssvc", "o11y"}, 5, false},
		{"all enabled (empty list) is iam-enabled, 4 replicas -> refuse", nil, 4, true},
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
