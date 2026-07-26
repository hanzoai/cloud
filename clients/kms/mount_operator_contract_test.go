package kms

import "testing"

// The KMS operator is the fleet's only machine consumer of the secrets list, and
// it spells the parameters `environment`/`secretPath` and reads the `names` key.
// This plane's own clients use `env`/`path` and read `secrets`. These lock in the
// superset that lets ONE endpoint serve both, so the standalone KMS can be retired
// without a lockstep operator release.
func TestFirstQueryPrefersPrimaryThenAlias(t *testing.T) {
	for _, tc := range []struct {
		name   string
		q      map[string]string
		keys   []string
		expect string
	}{
		{"primary wins", map[string]string{"env": "prod", "environment": "dev"}, []string{"env", "environment"}, "prod"},
		{"alias used when primary absent", map[string]string{"environment": "prod"}, []string{"env", "environment"}, "prod"},
		{"alias path", map[string]string{"secretPath": "/ci"}, []string{"path", "secretPath"}, "/ci"},
		{"neither set", map[string]string{}, []string{"env", "environment"}, ""},
		{"empty primary falls through", map[string]string{"env": "", "environment": "prod"}, []string{"env", "environment"}, "prod"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstNonEmpty(func(k string) string { return tc.q[k] }, tc.keys...); got != tc.expect {
				t.Fatalf("firstQuery(%v, %v) = %q, want %q", tc.q, tc.keys, got, tc.expect)
			}
		})
	}
}
