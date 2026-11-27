package k8s

import (
	"reflect"
	"testing"
)

var avoid = map[string]any{"key": DiskLabel, "operator": "NotIn", "values": []any{DiskPressure}}

func terms(pod map[string]any) []any {
	return pod["affinity"].(map[string]any)["nodeAffinity"].(map[string]any)["requiredDuringSchedulingIgnoredDuringExecution"].(map[string]any)["nodeSelectorTerms"].([]any)
}

// A pod with no affinity gets one term holding the requirement.
func TestAvoidPressureOnABarePod(t *testing.T) {
	pod := map[string]any{"nodeSelector": map[string]any{"hanzo.ai/visor": "true"}}
	AvoidPressure(pod)
	want := []any{map[string]any{"matchExpressions": []any{avoid}}}
	if got := terms(pod); !reflect.DeepEqual(got, want) {
		t.Fatalf("terms = %#v", got)
	}
}

// Every existing term carries it, beside what the term already required, and a
// second call adds nothing.
func TestAvoidPressureJoinsEveryTerm(t *testing.T) {
	arch := map[string]any{"key": "kubernetes.io/arch", "operator": "In", "values": []any{"amd64"}}
	pod := map[string]any{"affinity": map[string]any{"nodeAffinity": map[string]any{
		"requiredDuringSchedulingIgnoredDuringExecution": map[string]any{
			"nodeSelectorTerms": []any{
				map[string]any{"matchExpressions": []any{arch}},
				map[string]any{"matchFields": []any{map[string]any{"key": "metadata.name", "operator": "In", "values": []any{"n"}}}},
			},
		},
	}}}
	AvoidPressure(pod)
	AvoidPressure(pod)
	got := terms(pod)
	if !reflect.DeepEqual(got[0].(map[string]any)["matchExpressions"], []any{arch, avoid}) {
		t.Fatalf("first term = %#v", got[0])
	}
	if !reflect.DeepEqual(got[1].(map[string]any)["matchExpressions"], []any{avoid}) {
		t.Fatalf("second term = %#v", got[1])
	}
}
