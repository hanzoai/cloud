package cloud

import "testing"

// TestAIM2MTokenURL pins the split-horizon resolution order for the agent-runner
// M2M token endpoint (the GAP that 502'd every POST /v1/agents/:ref/run: the
// public issuer host is Cloudflare-fronted and 403s an in-cluster loopback with
// edge error 1006). The runner must mint its token from an IN-CLUSTER URL. Order:
// explicit override → in-cluster IAM_URL → public IAMIssuer fallback.
func TestAIM2MTokenURL(t *testing.T) {
	const tokenPath = "/v1/iam/oauth/token"

	cases := []struct {
		name      string
		override  string // CLOUD_AI_IAM_TOKEN_URL
		iamURL    string // IAM_URL
		iamIssuer string // cfg.IAMIssuer
		want      string
	}{
		{
			name:     "explicit override wins over everything",
			override: "http://iam.internal:1234/custom/token",
			iamURL:   "http://iam.hanzo.svc",
			// even a public issuer present must not be chosen
			iamIssuer: "https://hanzo.id",
			want:      "http://iam.internal:1234/custom/token",
		},
		{
			name:      "in-cluster IAM_URL preferred over public issuer",
			iamURL:    "http://iam.hanzo.svc",
			iamIssuer: "https://hanzo.id",
			want:      "http://iam.hanzo.svc" + tokenPath,
		},
		{
			name:      "trailing slash on IAM_URL is trimmed",
			iamURL:    "http://iam.hanzo.svc/",
			iamIssuer: "https://hanzo.id",
			want:      "http://iam.hanzo.svc" + tokenPath,
		},
		{
			name:      "falls back to public issuer only when no in-cluster URL",
			iamIssuer: "https://hanzo.id",
			want:      "https://hanzo.id" + tokenPath,
		},
		{
			name: "empty when no identity is resolvable (keeps M2M branch off)",
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// t.Setenv restores prior values + prevents parallel interference.
			t.Setenv("CLOUD_AI_IAM_TOKEN_URL", tc.override)
			t.Setenv("IAM_URL", tc.iamURL)
			cfg := &Config{IAMIssuer: tc.iamIssuer}
			if got := aiM2MTokenURL(cfg); got != tc.want {
				t.Fatalf("aiM2MTokenURL() = %q, want %q", got, tc.want)
			}
		})
	}
}
