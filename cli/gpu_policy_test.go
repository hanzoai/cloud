package cli

import (
	"encoding/json"
	"testing"
)

func TestSharePolicyReject(t *testing.T) {
	studioInput := json.RawMessage(`{"prompt":{},"org":"karma","project":"swimwear"}`)
	engineInput := json.RawMessage(`{"model":"Qwen/Qwen3-4B","org":"hanzo"}`)

	cases := []struct {
		name    string
		policy  *SharePolicy
		jobType string
		input   json.RawMessage
		allow   bool // true == reject() returns ""
	}{
		{"nil policy is permissive", nil, "studio.render", studioInput, true},
		{"empty policy is permissive", &SharePolicy{}, "studio.render", studioInput, true},
		{
			"job type allowed",
			&SharePolicy{AllowedJobTypes: []string{"studio.render"}},
			"studio.render", studioInput, true,
		},
		{
			"job type not allowed",
			&SharePolicy{AllowedJobTypes: []string{"engine.serve"}},
			"studio.render", studioInput, false,
		},
		{
			"org allowed",
			&SharePolicy{AllowedOrgs: []string{"karma", "hanzo"}},
			"studio.render", studioInput, true,
		},
		{
			"org not allowed",
			&SharePolicy{AllowedOrgs: []string{"hanzo"}},
			"studio.render", studioInput, false,
		},
		{
			"project not allowed",
			&SharePolicy{AllowedProjects: []string{"lifestyle"}},
			"studio.render", studioInput, false,
		},
		{
			"model allowed",
			&SharePolicy{AllowedModels: []string{"Qwen/Qwen3-4B"}},
			"engine.serve", engineInput, true,
		},
		{
			"model not allowed",
			&SharePolicy{AllowedModels: []string{"meta/llama-3"}},
			"engine.serve", engineInput, false,
		},
		{
			"absent field skips its gate (input has no project)",
			&SharePolicy{AllowedProjects: []string{"lifestyle"}},
			"engine.serve", engineInput, true,
		},
		{
			"combined: all gates pass",
			&SharePolicy{
				AllowedJobTypes: []string{"studio.render"},
				AllowedOrgs:     []string{"karma"},
				AllowedProjects: []string{"swimwear"},
			},
			"studio.render", studioInput, true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason := tc.policy.reject(tc.jobType, tc.input)
			if got := reason == ""; got != tc.allow {
				t.Fatalf("reject()=%q; want allow=%v", reason, tc.allow)
			}
		})
	}
}

func TestLoadSharePolicyInline(t *testing.T) {
	t.Setenv("HANZO_GPU_POLICY", `{"allowedJobTypes":["studio.render"],"allowedOrgs":["karma"],"maxConcurrent":2}`)
	p, err := loadSharePolicy()
	if err != nil {
		t.Fatalf("loadSharePolicy: %v", err)
	}
	if p == nil {
		t.Fatal("expected a policy, got nil")
	}
	if len(p.AllowedJobTypes) != 1 || p.AllowedJobTypes[0] != "studio.render" {
		t.Fatalf("AllowedJobTypes=%v", p.AllowedJobTypes)
	}
	if p.MaxConcurrent != 2 {
		t.Fatalf("MaxConcurrent=%d", p.MaxConcurrent)
	}
	if p.reject("engine.serve", json.RawMessage(`{}`)) == "" {
		t.Fatal("expected engine.serve to be rejected by studio.render-only policy")
	}
}

func TestLoadSharePolicyUnset(t *testing.T) {
	t.Setenv("HANZO_GPU_POLICY", "")
	t.Setenv("HANZO_GPU_POLICY_FILE", "")
	p, err := loadSharePolicy()
	if err != nil || p != nil {
		t.Fatalf("unset policy: got (%v, %v), want (nil, nil)", p, err)
	}
}
