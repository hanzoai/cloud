package ml

// health_test.go holds ONE property: /v1/ml/health reports the serving plane's
// CAPACITY, not only that its CRD is served.
//
// The property exists because kserve admits an InferenceService with no runtime to
// back it. Deleting the last ClusterServingRuntime is a legitimate operator act —
// the cluster carried ten of them, nine for backends nothing had ever deployed —
// and a probe that reads only "is the CRD served" answers 200 through every one of
// those deletions, including the one that takes the last runtime with it. This is
// the graded, operator-readable state that makes the purge safe to repeat.
//
// Every case asserts BOTH directions at the same address, because a one-sided
// assertion is satisfied by a probe that is simply always degraded (or a fixture
// whose list is always empty), and neither of those is the property.

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

// listKinds is what the fake dynamic client needs to answer a LIST at all: a
// GVR -> list-kind map. Every coordinate this probe reads is registered, because
// the fake client PANICS on an unregistered one rather than returning an error —
// so an unreadable list has to be injected as the error it really is (a reactor,
// below), not faked by leaving a kind out.
func listKinds() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "namespaces"}: "NamespaceList",
		isvcGVR:                                 "InferenceServiceList",
		runtimeGVR:                              "ClusterServingRuntimeList",
	}
}

// runtimeObj is one ClusterServingRuntime as the fake client stores it. Cluster
// scoped: no namespace, which is also the scope the probe must read it at.
func runtimeObj(name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(runtimeGVR.Group + "/" + runtimeGVR.Version)
	u.SetKind("ClusterServingRuntime")
	u.SetName(name)
	return u
}

// probe reads /v1/ml/health against a cluster holding
// exactly the given objects, and returns the status code with the decoded report.
func probe(t *testing.T, path string, objs ...runtime.Object) (int, map[string]any) {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds(), objs...)
	code, body := req(t, wireApp(t, dyn), http.MethodGet, path, "", "")
	var rep map[string]any
	if err := json.Unmarshal(body, &rep); err != nil {
		t.Fatalf("%s body %s: %v", path, body, err)
	}
	return code, rep
}

// TestServingHealthReportsRuntimeCapacity is the discriminator: the SAME cluster,
// the same served CRD, differing only in whether a runtime exists, must answer
// differently — and the count has to reach the report, since that number is what
// an operator reads to see the purge went one runtime too far.
func TestServingHealthReportsRuntimeCapacity(t *testing.T) {
	code, rep := probe(t, "/v1/ml/health")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("GET /v1/ml/health with zero serving runtimes = %d %v, want 503: an "+
			"InferenceService admitted here never schedules", code, rep)
	}
	if rep["status"] != "degraded" {
		t.Fatalf("zero-runtime report %v does not name the state as degraded", rep)
	}
	if got, ok := rep[runtimeGVR.Resource].(float64); !ok || got != 0 {
		t.Fatalf("zero-runtime report %v does not carry a readable count of 0", rep)
	}

	code, rep = probe(t, "/v1/ml/health", runtimeObj("kserve-mlserver"))
	if code != http.StatusOK {
		t.Fatalf("GET /v1/ml/health with one serving runtime = %d %v, want 200", code, rep)
	}
	if rep["status"] != "ok" {
		t.Fatalf("one-runtime report %v is not ok", rep)
	}
	if got, ok := rep[runtimeGVR.Resource].(float64); !ok || got != 1 {
		t.Fatalf("one-runtime report %v does not carry a count of 1", rep)
	}
}

// TestServingHealthSeparatesAnUnreadableRuntimeListFromAnEmptyOne is the second
// half of the named-state rule. Both states are 503, so a code alone cannot tell
// them apart — but they call for different acts (grant the read vs. install a
// runtime), so the REPORT has to. The injected refusal is the exact shape a missing
// RBAC grant takes: this service's ClusterRole has to name clusterservingruntimes,
// and a cluster where it does not must not read as a cluster with none.
func TestServingHealthSeparatesAnUnreadableRuntimeListFromAnEmptyOne(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds())
	dyn.PrependReactor("list", runtimeGVR.Resource, func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Group: runtimeGVR.Group, Resource: runtimeGVR.Resource}, "",
			errors.New(`clusterservingruntimes is forbidden: User "system:serviceaccount:hanzo:cloud" cannot list resource`))
	})
	code, body := req(t, wireApp(t, dyn), http.MethodGet, "/v1/ml/health", "", "")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("GET /v1/ml/health with an unreadable runtime list = %d %s, want 503", code, body)
	}
	var rep map[string]any
	if err := json.Unmarshal(body, &rep); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	if _, isCount := rep[runtimeGVR.Resource].(float64); isCount {
		t.Fatalf("an unreadable runtime list reported as a COUNT %v — a missing grant "+
			"must not read as an empty cluster", rep)
	}
	if s, ok := rep[runtimeGVR.Resource].(string); !ok || s == "" {
		t.Fatalf("report %v does not carry the real read error", rep)
	}
}
