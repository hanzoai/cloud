package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func execCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestK8sStatusAppsClustersKubeconfigAndKubectlForwarding(t *testing.T) {
	dir := sandbox(t)
	fakeHome := filepath.Join(dir, "home")
	kubeDir := filepath.Join(fakeHome, ".kube")
	if err := os.MkdirAll(kubeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", fakeHome)
	t.Setenv("KUBECONFIG", "")

	awsKC := filepath.Join(kubeDir, "hanzo-aws.yaml")
	kcYAML := `apiVersion: v1
kind: Config
current-context: hanzo-aws-prod
clusters:
  - name: hanzo-aws
    cluster:
      server: https://k8s-prod.hanzo.ai:6443
contexts:
  - name: hanzo-aws-prod
    context:
      cluster: hanzo-aws
      user: admin
`
	if err := os.WriteFile(awsKC, []byte(kcYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/compute/clusters":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"clusters": []map[string]any{
					{
						"doksClusterId": "cls-prod-1",
						"name":          "hanzo-aws",
						"region":        "us-east-1",
						"status":        "running",
						"kind":          "managed",
						"nodeCount":     6,
						"nvidiaGpu":     2,
					},
				},
			})
		case "/v1/deploy/applications":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{
					{
						"metadata": map[string]any{"name": "cloud", "namespace": "hanzo"},
						"spec": map[string]any{
							"project": "platform",
							"source": map[string]any{
								"repoURL":        "https://github.com/hanzo-inc/universe",
								"path":           "infra/k8s/cloud",
								"targetRevision": "main",
							},
						},
						"status": map[string]any{
							"sync":   map[string]any{"status": "Synced", "revision": "abcdef1234567890"},
							"health": map[string]any{"status": "Healthy"},
						},
					},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	t.Setenv("HANZO_CLOUD_URL", srv.URL)
	t.Setenv("HANZO_PLATFORM_URL", srv.URL)
	t.Setenv("HANZO_TOKEN", "test-iam-jwt")

	// 1. hanzo k8s status (-o json) resolves ~/.kube/hanzo-aws.yaml and uses ci.hanzo.ai + cd.hanzo.ai
	outStatus, err := execCLI(t, "-o", "json", "k8s", "status")
	if err != nil {
		t.Fatalf("k8s status: %v\n%s", err, outStatus)
	}
	if strings.Contains(outStatus, "cicd.hanzo.ai") {
		t.Fatalf("must never reference cicd.hanzo.ai: %s", outStatus)
	}
	var stView K8sStatusView
	if err := json.Unmarshal([]byte(outStatus), &stView); err != nil {
		t.Fatalf("decode k8s status: %v", err)
	}
	if stView.CIURL != "https://ci.hanzo.ai" || stView.CDURL != "https://cd.hanzo.ai" {
		t.Fatalf("expected ci.hanzo.ai and cd.hanzo.ai, got ci=%q cd=%q", stView.CIURL, stView.CDURL)
	}
	if stView.Kubeconfig.Path != awsKC || !stView.Kubeconfig.Exists || stView.Kubeconfig.Server != "https://k8s-prod.hanzo.ai:6443" {
		t.Fatalf("unexpected kubeconfig resolution: %+v", stView.Kubeconfig)
	}
	if stView.ClusterCount != 1 {
		t.Fatalf("expected 1 cluster, got %d", stView.ClusterCount)
	}

	// 2. hanzo k8s kubeconfig --path and --cluster
	outKCPath, err := execCLI(t, "k8s", "kubeconfig", "--path")
	if err != nil {
		t.Fatalf("k8s kubeconfig --path: %v", err)
	}
	if strings.TrimSpace(outKCPath) != awsKC {
		t.Fatalf("kubeconfig --path = %q, want %q", strings.TrimSpace(outKCPath), awsKC)
	}

	// 3. hanzo k8s apps & hanzo k8s clusters
	outApps, err := execCLI(t, "k8s", "apps")
	if err != nil {
		t.Fatalf("k8s apps: %v", err)
	}
	if !strings.Contains(outApps, "cloud") || !strings.Contains(outApps, "Synced") || !strings.Contains(outApps, "Healthy") {
		t.Fatalf("k8s apps table missing expected fields: %s", outApps)
	}
	outClusters, err := execCLI(t, "k8s", "clusters")
	if err != nil || !strings.Contains(outClusters, "hanzo-aws") {
		t.Fatalf("k8s clusters failed: err=%v out=%s", err, outClusters)
	}

	// 4. hanzo k8s kubectl / get / rollout forwarding
	var gotKC string
	var gotArgs []string
	origRun := runKubectl
	t.Cleanup(func() { runKubectl = origRun })
	runKubectl = func(_ context.Context, kc string, args []string, stdout, _ io.Writer) error {
		gotKC = kc
		gotArgs = append([]string(nil), args...)
		_, _ = io.WriteString(stdout, "forwarded-ok\n")
		return nil
	}

	outRollout, err := execCLI(t, "k8s", "rollout", "status", "deploy/cloud", "-n", "hanzo")
	if err != nil {
		t.Fatalf("k8s rollout: %v", err)
	}
	if gotKC != awsKC {
		t.Fatalf("forwarded kubeconfig = %q, want %q", gotKC, awsKC)
	}
	wantArgs := []string{"rollout", "status", "deploy/cloud", "-n", "hanzo"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("forwarded args = %v, want %v (out=%q)", gotArgs, wantArgs, outRollout)
	}
}

func TestK3sLifecycleKubeconfigAndKubectlForwarding(t *testing.T) {
	dir := sandbox(t)
	fakeHome := filepath.Join(dir, "home")
	kubeDir := filepath.Join(fakeHome, ".kube")
	if err := os.MkdirAll(kubeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", fakeHome)

	localKC := filepath.Join(kubeDir, "hanzo.yaml")
	if err := os.WriteFile(localKC, []byte("apiVersion: v1\nclusters:\n- name: local\n  cluster:\n    server: https://127.0.0.1:6443\ncurrent-context: default\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	state := "stopped"
	driver := "none"
	var lastUpReq K3sUpReq

	daemonSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeStatus := func(code int) {
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(K3sStatus{
				OK:               true,
				State:            state,
				Driver:           driver,
				Kubeconfig:       localKC,
				KubeconfigExists: true,
				K3sAPI:           "https://127.0.0.1:6443",
				TasksdRunning:    true,
				TasksdSocket:     "/tmp/tasks.zap.sock",
				Capabilities: K3sCapabilities{
					NativeK3s: true,
					Tasksd:    true,
					Kubectl:   true,
				},
			})
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/k3s/health":
			writeStatus(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/k3s/up":
			_ = json.NewDecoder(r.Body).Decode(&lastUpReq)
			state = "booting"
			driver = "native-k3s"
			writeStatus(http.StatusAccepted)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/k3s/down":
			state = "stopped"
			driver = "none"
			writeStatus(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
		}
	}))
	defer daemonSrv.Close()
	t.Setenv("HANZOD_URL", daemonSrv.URL)

	// 1. hanzo k3s status
	outSt, err := execCLI(t, "-o", "json", "k3s", "status")
	if err != nil {
		t.Fatalf("k3s status: %v", err)
	}
	var st K3sStatus
	if err := json.Unmarshal([]byte(outSt), &st); err != nil {
		t.Fatalf("decode k3s status: %v", err)
	}
	if !st.OK || st.State != "stopped" || !st.Capabilities.NativeK3s || !st.TasksdRunning {
		t.Fatalf("unexpected k3s status: %+v", st)
	}

	// 2. hanzo k3s up --mode native-k3s
	outUp, err := execCLI(t, "k3s", "up", "--mode", "native-k3s")
	if err != nil {
		t.Fatalf("k3s up: %v", err)
	}
	if lastUpReq.Mode != "native-k3s" || !strings.Contains(outUp, "booting") {
		t.Fatalf("k3s up mismatch: req=%+v out=%s", lastUpReq, outUp)
	}

	// 3. hanzo k3s kubeconfig --path
	outKC, err := execCLI(t, "k3s", "kubeconfig", "--path")
	if err != nil || strings.TrimSpace(outKC) != localKC {
		t.Fatalf("k3s kubeconfig --path = %q (err=%v), want %q", strings.TrimSpace(outKC), err, localKC)
	}

	// 4. hanzo k3s pods -A -> kubectl --kubeconfig <localKC> get pods -A
	var gotKC string
	var gotArgs []string
	origRun := runKubectl
	t.Cleanup(func() { runKubectl = origRun })
	runKubectl = func(_ context.Context, kc string, args []string, stdout, _ io.Writer) error {
		gotKC = kc
		gotArgs = append([]string(nil), args...)
		_, _ = io.WriteString(stdout, "local-pods-ok\n")
		return nil
	}
	if _, err := execCLI(t, "k3s", "pods", "-A"); err != nil {
		t.Fatalf("k3s pods: %v", err)
	}
	if gotKC != localKC || !reflect.DeepEqual(gotArgs, []string{"get", "pods", "-A"}) {
		t.Fatalf("k3s pods forward mismatch: kc=%q args=%v", gotKC, gotArgs)
	}

	// 5. hanzo k3s down
	outDown, err := execCLI(t, "k3s", "down")
	if err != nil || !strings.Contains(outDown, "stopped") {
		t.Fatalf("k3s down failed: err=%v out=%s", err, outDown)
	}
}
