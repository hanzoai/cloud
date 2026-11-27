package cli

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const (
	defaultHanzodURL = "http://127.0.0.1:3690"
	defaultCIURL     = "https://ci.hanzo.ai"
	defaultCDURL     = "https://cd.hanzo.ai"
)

// runKubectl executes kubectl with the resolved kubeconfig and arguments.
// Package-level var so tests can verify exact invocation deterministically.
var runKubectl = defaultRunKubectl

func defaultRunKubectl(ctx context.Context, kubeconfig string, args []string, stdout, stderr io.Writer) error {
	bin, err := exec.LookPath("kubectl")
	if err != nil {
		return fmt.Errorf("kubectl not found on PATH: %w", err)
	}
	finalArgs := prependKubeconfigArg(kubeconfig, args)
	cmd := exec.CommandContext(ctx, bin, finalArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if kubeconfig != "" {
		env := make([]string, 0, len(os.Environ())+1)
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "KUBECONFIG=") {
				env = append(env, kv)
			}
		}
		env = append(env, "KUBECONFIG="+kubeconfig)
		cmd.Env = env
	}
	return cmd.Run()
}

func prependKubeconfigArg(kubeconfig string, args []string) []string {
	if kubeconfig == "" {
		return args
	}
	for _, a := range args {
		if a == "--kubeconfig" || strings.HasPrefix(a, "--kubeconfig=") {
			return args
		}
	}
	out := make([]string, 0, len(args)+2)
	out = append(out, "--kubeconfig", kubeconfig)
	out = append(out, args...)
	return out
}

// extractFlagFromRawArgs extracts `--kubeconfig <v>` or `--kubeconfig=<v>` when DisableFlagParsing is set.
func extractKubeconfigFlag(args []string) (string, []string) {
	var val string
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--kubeconfig" && i+1 < len(args) {
			val = args[i+1]
			i++
			continue
		}
		if v, ok := strings.CutPrefix(a, "--kubeconfig="); ok {
			val = v
			continue
		}
		rest = append(rest, a)
	}
	return val, rest
}

// KubeconfigInfo summarizes a resolved kubeconfig without exposing secret credentials/keys.
type KubeconfigInfo struct {
	Path           string `json:"path"`
	Exists         bool   `json:"exists"`
	Cluster        string `json:"cluster,omitempty"`
	CurrentContext string `json:"current_context,omitempty"`
	Server         string `json:"server,omitempty"`
	Source         string `json:"source,omitempty"`
}

type rawKubeYAML struct {
	CurrentContext string `yaml:"current-context"`
	Clusters       []struct {
		Name    string `yaml:"name"`
		Cluster struct {
			Server string `yaml:"server"`
		} `yaml:"cluster"`
	} `yaml:"clusters"`
	Contexts []struct {
		Name    string `yaml:"name"`
		Context struct {
			Cluster string `yaml:"cluster"`
		} `yaml:"context"`
	} `yaml:"contexts"`
}

func inspectKubeconfigFile(path, source, clusterHint string) KubeconfigInfo {
	info := KubeconfigInfo{
		Path:    path,
		Cluster: clusterHint,
		Source:  source,
	}
	if path == "" {
		return info
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return info
	}
	info.Exists = true
	var parsed rawKubeYAML
	if yaml.Unmarshal(b, &parsed) != nil {
		return info
	}
	info.CurrentContext = parsed.CurrentContext
	wantCluster := clusterHint
	if wantCluster == "" && parsed.CurrentContext != "" {
		for _, ctxItem := range parsed.Contexts {
			if ctxItem.Name == parsed.CurrentContext {
				wantCluster = ctxItem.Context.Cluster
				break
			}
		}
	}
	for _, c := range parsed.Clusters {
		if wantCluster != "" && c.Name == wantCluster {
			if info.Cluster == "" {
				info.Cluster = c.Name
			}
			info.Server = c.Cluster.Server
			return info
		}
	}
	if len(parsed.Clusters) > 0 {
		if info.Cluster == "" {
			info.Cluster = parsed.Clusters[0].Name
		}
		info.Server = parsed.Clusters[0].Cluster.Server
	}
	return info
}

func resolveCloudKubeconfig(explicitFlag, clusterID string) KubeconfigInfo {
	if explicitFlag != "" {
		return inspectKubeconfigFile(explicitFlag, "flag", clusterID)
	}
	if envPath := strings.TrimSpace(os.Getenv("HANZO_K8S_KUBECONFIG")); envPath != "" {
		return inspectKubeconfigFile(envPath, "HANZO_K8S_KUBECONFIG", clusterID)
	}
	home, _ := os.UserHomeDir()
	if clusterID != "" && home != "" {
		for _, cand := range []string{
			filepath.Join(home, ".kube", "hanzo-"+clusterID+".yaml"),
			filepath.Join(home, ".kube", clusterID+".yaml"),
		} {
			if _, err := os.Stat(cand); err == nil {
				return inspectKubeconfigFile(cand, "cluster", clusterID)
			}
		}
	}
	if kcEnv := strings.TrimSpace(os.Getenv("KUBECONFIG")); kcEnv != "" {
		parts := filepath.SplitList(kcEnv)
		for _, p := range parts {
			if p != "" {
				if _, err := os.Stat(p); err == nil {
					return inspectKubeconfigFile(p, "KUBECONFIG", clusterID)
				}
			}
		}
		if len(parts) > 0 && parts[0] != "" {
			return inspectKubeconfigFile(parts[0], "KUBECONFIG", clusterID)
		}
	}
	if home != "" {
		candidates := []struct {
			path, source string
		}{
			{filepath.Join(home, ".kube", "hanzo-aws.yaml"), "~/.kube/hanzo-aws.yaml"},
			{filepath.Join(home, ".kube", "hanzo-cloud.yaml"), "~/.kube/hanzo-cloud.yaml"},
			{filepath.Join(home, ".kube", "config"), "~/.kube/config"},
		}
		for _, c := range candidates {
			if _, err := os.Stat(c.path); err == nil {
				return inspectKubeconfigFile(c.path, c.source, clusterID)
			}
		}
		return inspectKubeconfigFile(filepath.Join(home, ".kube", "hanzo-aws.yaml"), "default", clusterID)
	}
	return KubeconfigInfo{Cluster: clusterID, Source: "none"}
}

// ---------------------------------------------------------------------------
// DeployApplications — GET /v1/deploy/applications
// ---------------------------------------------------------------------------

// DeployApp represents one CD / tenant Application projected by GET /v1/deploy/applications.
type DeployApp struct {
	Name           string `json:"name"`
	Namespace      string `json:"namespace,omitempty"`
	Project        string `json:"project,omitempty"`
	RepoURL        string `json:"repoURL,omitempty"`
	Path           string `json:"path,omitempty"`
	TargetRevision string `json:"targetRevision,omitempty"`
	SyncStatus     string `json:"syncStatus,omitempty"`
	Revision       string `json:"revision,omitempty"`
	HealthStatus   string `json:"healthStatus,omitempty"`
	ReconciledAt   string `json:"reconciledAt,omitempty"`
}

type argoAppWire struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		Project string `json:"project"`
		Source  struct {
			RepoURL        string `json:"repoURL"`
			Path           string `json:"path"`
			TargetRevision string `json:"targetRevision"`
		} `json:"source"`
	} `json:"spec"`
	Status struct {
		Sync struct {
			Status   string `json:"status"`
			Revision string `json:"revision"`
		} `json:"sync"`
		Health struct {
			Status string `json:"status"`
		} `json:"health"`
		ReconciledAt string `json:"reconciledAt"`
	} `json:"status"`
	// Flat fallback fields if returned in summary form.
	Name   string `json:"name"`
	Health string `json:"health"`
	Sync   string `json:"sync"`
}

func (p *Platform) DeployApplications(ctx context.Context) ([]DeployApp, error) {
	var raw struct {
		Items        []argoAppWire `json:"items"`
		Applications []argoAppWire `json:"applications"`
	}
	if err := p.do(ctx, http.MethodGet, "/v1/deploy/applications", p.token, nil, &raw); err != nil {
		return nil, err
	}
	items := raw.Items
	if len(items) == 0 && len(raw.Applications) > 0 {
		items = raw.Applications
	}
	out := make([]DeployApp, 0, len(items))
	for _, it := range items {
		name := cmp.Or(it.Metadata.Name, it.Name)
		syncSt := cmp.Or(it.Status.Sync.Status, it.Sync)
		healthSt := cmp.Or(it.Status.Health.Status, it.Health)
		out = append(out, DeployApp{
			Name:           name,
			Namespace:      it.Metadata.Namespace,
			Project:        it.Spec.Project,
			RepoURL:        it.Spec.Source.RepoURL,
			Path:           it.Spec.Source.Path,
			TargetRevision: it.Spec.Source.TargetRevision,
			SyncStatus:     syncSt,
			Revision:       it.Status.Sync.Revision,
			HealthStatus:   healthSt,
			ReconciledAt:   it.Status.ReconciledAt,
		})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// hanzo k8s — Unified Cloud Kubernetes management
// ---------------------------------------------------------------------------

type K8sStatusView struct {
	OK                 bool           `json:"ok"`
	CloudURL           string         `json:"cloud_url"`
	CIURL              string         `json:"ci_url"`
	CDURL              string         `json:"cd_url"`
	Kubeconfig         KubeconfigInfo `json:"kubeconfig"`
	APIServerReachable bool           `json:"apiserver_reachable"`
	KubernetesVersion  string         `json:"kubernetes_version,omitempty"`
	Clusters           []Cluster      `json:"clusters,omitempty"`
	ClusterCount       int            `json:"cluster_count"`
}

func probeK8sVersion(ctx context.Context, kubeconfig string) (bool, string) {
	if kubeconfig == "" {
		return false, ""
	}
	if _, err := os.Stat(kubeconfig); err != nil {
		return false, ""
	}
	bin, err := exec.LookPath("kubectl")
	if err != nil {
		return false, ""
	}
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(pctx, bin, "--kubeconfig", kubeconfig, "--request-timeout=4s", "version", "-o", "json").Output()
	if err != nil {
		return false, ""
	}
	var ver struct {
		ServerVersion struct {
			GitVersion string `json:"gitVersion"`
		} `json:"serverVersion"`
	}
	if json.Unmarshal(out, &ver) == nil && ver.ServerVersion.GitVersion != "" {
		return true, ver.ServerVersion.GitVersion
	}
	return true, ""
}

func newK8sCmd(envOf func() *Env, gf *globalFlags) *cobra.Command {
	var kubeconfigFlag string

	cmd := &cobra.Command{
		Use:   "k8s",
		Short: "Manage Cloud Kubernetes clusters, GitOps apps (cd.hanzo.ai), kubeconfig, and kubectl",
		Long: "Unified Hanzo Cloud Kubernetes management: inspect cluster status, list managed/BYO\n" +
			"clusters, view GitOps applications deployed via cd.hanzo.ai (built by ci.hanzo.ai),\n" +
			"resolve cloud kubeconfigs (including ~/.kube/hanzo-aws.yaml), and forward kubectl commands.",
	}
	cmd.PersistentFlags().StringVar(&kubeconfigFlag, "kubeconfig", "", "explicit cloud kubeconfig path (default: $KUBECONFIG or ~/.kube/hanzo-aws.yaml)")

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show Cloud Kubernetes kubeconfig, API reachability, and org cluster summary",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e := envOf()
			kc := resolveCloudKubeconfig(kubeconfigFlag, "")
			reachable, kver := probeK8sVersion(cmd.Context(), kc.Path)
			var clusters []Cluster
			if tok := e.platformToken(gf.platformToken); tok != "" {
				if cs, err := newPlatform(e.CloudURL, tok).Clusters(cmd.Context()); err == nil {
					clusters = cs
				} else if e.PlatformURL != e.CloudURL {
					if cs2, err2 := e.platform(gf).Clusters(cmd.Context()); err2 == nil {
						clusters = cs2
					}
				}
			}
			view := K8sStatusView{
				OK:                 kc.Exists || len(clusters) > 0,
				CloudURL:           e.CloudURL,
				CIURL:              defaultCIURL,
				CDURL:              defaultCDURL,
				Kubeconfig:         kc,
				APIServerReachable: reachable,
				KubernetesVersion:  kver,
				Clusters:           clusters,
				ClusterCount:       len(clusters),
			}
			return e.emit(view, func(w io.Writer) {
				tw := newTab(w)
				fmt.Fprintf(tw, "cloud_url:\t%s\n", view.CloudURL)
				fmt.Fprintf(tw, "ci_url:\t%s\n", view.CIURL)
				fmt.Fprintf(tw, "cd_url:\t%s\n", view.CDURL)
				fmt.Fprintf(tw, "kubeconfig:\t%s (exists=%s)\n", dashIfEmpty(kc.Path), yesno(kc.Exists))
				fmt.Fprintf(tw, "context:\t%s\n", dashIfEmpty(kc.CurrentContext))
				fmt.Fprintf(tw, "server:\t%s\n", dashIfEmpty(kc.Server))
				fmt.Fprintf(tw, "reachable:\t%s\n", yesno(view.APIServerReachable))
				if view.KubernetesVersion != "" {
					fmt.Fprintf(tw, "k8s_version:\t%s\n", view.KubernetesVersion)
				}
				fmt.Fprintf(tw, "clusters:\t%d\n", view.ClusterCount)
				tw.Flush()
			})
		},
	}

	// Reuse newClustersCmd from commands.go / cluster.go and make bare `hanzo k8s clusters` list clusters.
	clustersCmd := newClustersCmd(envOf, gf)
	clustersCmd.Aliases = nil
	clustersCmd.RunE = func(cmd *cobra.Command, args []string) error {
		listSub, _, err := cmd.Find([]string{"list"})
		if err != nil || listSub == cmd {
			return cmd.Help()
		}
		listSub.SetContext(cmd.Context())
		return listSub.RunE(listSub, args)
	}

	var syncFilter, healthFilter string
	appsCmd := &cobra.Command{
		Use:   "apps",
		Short: "List Cloud GitOps applications (/v1/deploy/applications reconciled by cd.hanzo.ai)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e := envOf()
			tok := e.platformToken(gf.platformToken)
			apps, err := newPlatform(e.CloudURL, tok).DeployApplications(cmd.Context())
			if err != nil && e.PlatformURL != e.CloudURL {
				if alt, err2 := e.platform(gf).DeployApplications(cmd.Context()); err2 == nil {
					apps, err = alt, nil
				}
			}
			if err != nil {
				return err
			}
			filtered := make([]DeployApp, 0, len(apps))
			for _, a := range apps {
				if syncFilter != "" && !strings.EqualFold(a.SyncStatus, syncFilter) {
					continue
				}
				if healthFilter != "" && !strings.EqualFold(a.HealthStatus, healthFilter) {
					continue
				}
				filtered = append(filtered, a)
			}
			return e.emit(filtered, func(w io.Writer) {
				tw := newTab(w)
				fmt.Fprintln(tw, "NAME\tNAMESPACE\tPROJECT\tSYNC\tHEALTH\tREVISION")
				for _, a := range filtered {
					rev := a.Revision
					if len(rev) > 12 {
						rev = rev[:12]
					}
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
						a.Name, dashIfEmpty(a.Namespace), dashIfEmpty(a.Project),
						dashIfEmpty(a.SyncStatus), dashIfEmpty(a.HealthStatus), dashIfEmpty(rev))
				}
				tw.Flush()
				if len(filtered) == 0 {
					fmt.Fprintln(w, "(no applications)")
				}
			})
		},
	}
	appsCmd.Flags().StringVar(&syncFilter, "sync", "", "filter by sync status (e.g. Synced, OutOfSync)")
	appsCmd.Flags().StringVar(&healthFilter, "health", "", "filter by health status (e.g. Healthy, Degraded)")

	var clusterID string
	var pathOnly bool
	kubeconfigCmd := &cobra.Command{
		Use:   "kubeconfig",
		Short: "Resolve Cloud kubeconfig path and cluster endpoint (including ~/.kube/hanzo-aws.yaml)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e := envOf()
			kc := resolveCloudKubeconfig(kubeconfigFlag, clusterID)
			if clusterID != "" && !kc.Exists {
				if tok := e.platformToken(gf.platformToken); tok != "" {
					if cs, err := newPlatform(e.CloudURL, tok).Clusters(cmd.Context()); err == nil {
						for _, c := range cs {
							if c.ID() == clusterID || c.Name == clusterID {
								kc.Cluster = c.Name
								kc.Source = "cloud-api"
								break
							}
						}
					}
				}
			}
			if pathOnly {
				fmt.Fprintln(cmd.OutOrStdout(), kc.Path)
				return nil
			}
			return e.emit(kc, func(w io.Writer) {
				tw := newTab(w)
				fmt.Fprintf(tw, "path:\t%s\n", dashIfEmpty(kc.Path))
				fmt.Fprintf(tw, "exists:\t%s\n", yesno(kc.Exists))
				fmt.Fprintf(tw, "cluster:\t%s\n", dashIfEmpty(kc.Cluster))
				fmt.Fprintf(tw, "context:\t%s\n", dashIfEmpty(kc.CurrentContext))
				fmt.Fprintf(tw, "server:\t%s\n", dashIfEmpty(kc.Server))
				fmt.Fprintf(tw, "source:\t%s\n", dashIfEmpty(kc.Source))
				tw.Flush()
			})
		},
	}
	kubeconfigCmd.Flags().StringVar(&clusterID, "cluster", "", "cluster ID or name to resolve")
	kubeconfigCmd.Flags().BoolVar(&pathOnly, "path", false, "print only the resolved kubeconfig file path")

	newCloudKubectlPass := func(use, short string, prefixArgs ...string) *cobra.Command {
		return &cobra.Command{
			Use:                use,
			Short:              short,
			DisableFlagParsing: true,
			RunE: func(cmd *cobra.Command, args []string) error {
				if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
					return cmd.Help()
				}
				rawKC, cleaned := extractKubeconfigFlag(args)
				kc := resolveCloudKubeconfig(cmp.Or(rawKC, kubeconfigFlag), "")
				fullArgs := make([]string, 0, len(prefixArgs)+len(cleaned))
				fullArgs = append(fullArgs, prefixArgs...)
				fullArgs = append(fullArgs, cleaned...)
				return runKubectl(cmd.Context(), kc.Path, fullArgs, cmd.OutOrStdout(), cmd.ErrOrStderr())
			},
		}
	}

	cmd.AddCommand(
		statusCmd,
		clustersCmd,
		appsCmd,
		kubeconfigCmd,
		newCloudKubectlPass("kubectl [args...]", "Execute kubectl against the Cloud Kubernetes cluster"),
		newCloudKubectlPass("get [args...]", "Run `kubectl get` against the Cloud Kubernetes cluster", "get"),
		newCloudKubectlPass("describe [args...]", "Run `kubectl describe` against the Cloud Kubernetes cluster", "describe"),
		newCloudKubectlPass("logs [args...]", "Run `kubectl logs` against the Cloud Kubernetes cluster", "logs"),
		newCloudKubectlPass("rollout [args...]", "Run `kubectl rollout` against the Cloud Kubernetes cluster", "rollout"),
		newCloudKubectlPass("apply [args...]", "Run `kubectl apply` against the Cloud Kubernetes cluster", "apply"),
		newCloudKubectlPass("pods [args...]", "Run `kubectl get pods` against the Cloud Kubernetes cluster", "get", "pods"),
	)
	return cmd
}

// ---------------------------------------------------------------------------
// hanzo k3s — Unified local/edge k3s management orchestrated by hanzod
// Matches /home/z/work/hanzo/node/daemon/src/cluster.rs (/v1/k3s & /v1/cluster)
// ---------------------------------------------------------------------------

// K3sCapabilities mirrors hanzod's cluster.rs `capabilities` object.
type K3sCapabilities struct {
	VMK3s     bool `json:"vm_k3s"`
	NativeK3s bool `json:"native_k3s"`
	CloudHost bool `json:"cloud_host"`
	Tasksd    bool `json:"tasksd"`
	Kubectl   bool `json:"kubectl"`
}

// K3sStatus mirrors hanzod's cluster.rs `ClusterCore::status()` JSON payload 1:1.
type K3sStatus struct {
	OK                 bool            `json:"ok"`
	State              string          `json:"state"`
	Driver             string          `json:"driver"`
	Kubeconfig         string          `json:"kubeconfig"`
	KubeconfigExists   bool            `json:"kubeconfig_exists"`
	K3sAPI             string          `json:"k3s_api"`
	CloudUpstream      *string         `json:"cloud_upstream"`
	TasksdRunning      bool            `json:"tasksd_running"`
	TasksdSocket       string          `json:"tasksd_socket"`
	AttestationPresent bool            `json:"attestation_present"`
	Capabilities       K3sCapabilities `json:"capabilities"`
}

// K3sUpReq mirrors hanzod's cluster.rs `ClusterUpReq` 1:1.
type K3sUpReq struct {
	Mode         string `json:"mode,omitempty"`
	CPUs         uint32 `json:"cpus,omitempty"`
	MemoryMB     uint64 `json:"memory_mb,omitempty"`
	CloudImage   string `json:"cloud_image,omitempty"`
	JoinServer   string `json:"join_server,omitempty"`
	JoinToken    string `json:"join_token,omitempty"`
	UpstreamPort uint16 `json:"upstream_port,omitempty"`
}

type hanzodClient struct {
	baseURL string
	http    *http.Client
}

func newHanzodClient(explicitURL string) *hanzodClient {
	u := strings.TrimRight(cmp.Or(strings.TrimSpace(explicitURL), strings.TrimSpace(os.Getenv("HANZOD_URL")), defaultHanzodURL), "/")
	return &hanzodClient{
		baseURL: u,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *hanzodClient) callJSON(ctx context.Context, method string, paths []string, body any, out any) error {
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	var lastErr error
	for _, path := range paths {
		var rdr io.Reader
		if payload != nil {
			rdr = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "hanzo-cli/"+Version)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("hanzod %s%s: %w", c.baseURL, path, err)
			break
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			lastErr = &apiError{status: resp.StatusCode, message: serverMessage(raw), path: path}
			continue
		}
		if resp.StatusCode/100 != 2 {
			return fmt.Errorf("hanzod %s: HTTP %d: %s", path, resp.StatusCode, serverMessage(raw))
		}
		if out != nil && len(raw) > 0 {
			if err := json.Unmarshal(raw, out); err != nil {
				return fmt.Errorf("hanzod %s: decode response: %w", path, err)
			}
		}
		return nil
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("hanzod %s: no endpoint responded", c.baseURL)
}

func (c *hanzodClient) Status(ctx context.Context) (*K3sStatus, error) {
	st := &K3sStatus{}
	err := c.callJSON(ctx, http.MethodGet, []string{"/v1/k3s/health", "/v1/k3s/status", "/v1/cluster/health", "/v1/k3s"}, nil, st)
	return st, err
}

func (c *hanzodClient) Up(ctx context.Context, req K3sUpReq) (*K3sStatus, error) {
	st := &K3sStatus{}
	var body any
	if req != (K3sUpReq{}) {
		body = req
	}
	err := c.callJSON(ctx, http.MethodPost, []string{"/v1/k3s/up", "/v1/cluster/up"}, body, st)
	return st, err
}

func (c *hanzodClient) Down(ctx context.Context) (*K3sStatus, error) {
	st := &K3sStatus{}
	err := c.callJSON(ctx, http.MethodPost, []string{"/v1/k3s/down", "/v1/cluster/down"}, nil, st)
	return st, err
}

func resolveK3sKubeconfig(ctx context.Context, explicitFlag string, daemon *hanzodClient) KubeconfigInfo {
	if explicitFlag != "" {
		return inspectKubeconfigFile(explicitFlag, "flag", "local-k3s")
	}
	if envPath := strings.TrimSpace(os.Getenv("HANZO_K3S_KUBECONFIG")); envPath != "" {
		return inspectKubeconfigFile(envPath, "HANZO_K3S_KUBECONFIG", "local-k3s")
	}
	// Check hanzod /v1/k3s/kubeconfig first (if served), then /v1/k3s/health status payload.
	var kcResp struct {
		Path       string `json:"path"`
		Kubeconfig string `json:"kubeconfig"`
		Server     string `json:"server"`
	}
	if err := daemon.callJSON(ctx, http.MethodGet, []string{"/v1/k3s/kubeconfig"}, nil, &kcResp); err == nil {
		p := cmp.Or(kcResp.Path, kcResp.Kubeconfig)
		if p != "" {
			info := inspectKubeconfigFile(p, "hanzod:/v1/k3s/kubeconfig", "local-k3s")
			if info.Server == "" && kcResp.Server != "" {
				info.Server = kcResp.Server
			}
			return info
		}
	}
	if st, err := daemon.Status(ctx); err == nil && st.Kubeconfig != "" {
		if st.KubeconfigExists {
			info := inspectKubeconfigFile(st.Kubeconfig, "hanzod", "local-k3s")
			if info.Server == "" {
				info.Server = st.K3sAPI
			}
			return info
		}
		// If hanzod reported a default path that isn't created yet, check ~/.kube/hanzo-k3s.yaml too.
		home, _ := os.UserHomeDir()
		if home != "" {
			alt := filepath.Join(home, ".kube", "hanzo-k3s.yaml")
			if _, err := os.Stat(alt); err == nil {
				return inspectKubeconfigFile(alt, "~/.kube/hanzo-k3s.yaml", "local-k3s")
			}
		}
		info := inspectKubeconfigFile(st.Kubeconfig, "hanzod", "local-k3s")
		if info.Server == "" {
			info.Server = st.K3sAPI
		}
		return info
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		for _, cand := range []struct{ path, src string }{
			{filepath.Join(home, ".kube", "hanzo-k3s.yaml"), "~/.kube/hanzo-k3s.yaml"},
			{filepath.Join(home, ".kube", "hanzo.yaml"), "~/.kube/hanzo.yaml"},
			{"/etc/rancher/k3s/k3s.yaml", "/etc/rancher/k3s/k3s.yaml"},
		} {
			if _, err := os.Stat(cand.path); err == nil {
				return inspectKubeconfigFile(cand.path, cand.src, "local-k3s")
			}
		}
		return inspectKubeconfigFile(filepath.Join(home, ".kube", "hanzo-k3s.yaml"), "default", "local-k3s")
	}
	return KubeconfigInfo{Cluster: "local-k3s", Source: "none"}
}

func printK3sStatus(w io.Writer, st *K3sStatus) {
	tw := newTab(w)
	fmt.Fprintf(tw, "state:\t%s\n", dashIfEmpty(st.State))
	fmt.Fprintf(tw, "driver:\t%s\n", dashIfEmpty(st.Driver))
	fmt.Fprintf(tw, "k3s_api:\t%s\n", dashIfEmpty(st.K3sAPI))
	fmt.Fprintf(tw, "kubeconfig:\t%s (exists=%s)\n", dashIfEmpty(st.Kubeconfig), yesno(st.KubeconfigExists))
	fmt.Fprintf(tw, "tasksd_running:\t%s\n", yesno(st.TasksdRunning))
	if st.CloudUpstream != nil && *st.CloudUpstream != "" {
		fmt.Fprintf(tw, "cloud_upstream:\t%s\n", *st.CloudUpstream)
	}
	var caps []string
	if st.Capabilities.NativeK3s {
		caps = append(caps, "native_k3s")
	}
	if st.Capabilities.VMK3s {
		caps = append(caps, "vm_k3s")
	}
	if st.Capabilities.CloudHost {
		caps = append(caps, "cloud_host")
	}
	if st.Capabilities.Tasksd {
		caps = append(caps, "tasksd")
	}
	if st.Capabilities.Kubectl {
		caps = append(caps, "kubectl")
	}
	fmt.Fprintf(tw, "capabilities:\t%s\n", dashIfEmpty(strings.Join(caps, ", ")))
	tw.Flush()
}

func newK3sCmd(envOf func() *Env) *cobra.Command {
	var hanzodURL, kubeconfigFlag string

	cmd := &cobra.Command{
		Use:   "k3s",
		Short: "Manage local/edge k3s orchestrated by hanzod (/v1/k3s & /v1/cluster)",
		Long: "Unified local/edge Kubernetes (k3s) management orchestrated by hanzod\n" +
			"(HANZOD_URL, default http://127.0.0.1:3690): status, up, down, kubeconfig, and kubectl.",
	}
	cmd.PersistentFlags().StringVar(&hanzodURL, "hanzod-url", "", "hanzod base URL (default $HANZOD_URL or "+defaultHanzodURL+")")
	cmd.PersistentFlags().StringVar(&kubeconfigFlag, "kubeconfig", "", "explicit local k3s kubeconfig path")

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Query local k3s & tasksd status from hanzod",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e := envOf()
			st, err := newHanzodClient(hanzodURL).Status(cmd.Context())
			if err != nil {
				return err
			}
			return e.emit(st, func(w io.Writer) { printK3sStatus(w, st) })
		},
	}

	var upReq K3sUpReq
	upCmd := &cobra.Command{
		Use:   "up",
		Short: "Start local/edge k3s via hanzod (POST /v1/k3s/up)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e := envOf()
			if upReq.JoinToken == "" {
				upReq.JoinToken = strings.TrimSpace(os.Getenv("K3S_TOKEN"))
			}
			st, err := newHanzodClient(hanzodURL).Up(cmd.Context(), upReq)
			if err != nil {
				return err
			}
			return e.emit(st, func(w io.Writer) { printK3sStatus(w, st) })
		},
	}
	upCmd.Flags().StringVar(&upReq.Mode, "mode", "", "cluster driver: auto|vm-k3s|native-k3s|cloud-host|tasksd")
	upCmd.Flags().Uint32Var(&upReq.CPUs, "cpus", 0, "VM CPU count (vm-k3s)")
	upCmd.Flags().Uint64Var(&upReq.MemoryMB, "memory-mb", 0, "VM memory in MiB (vm-k3s)")
	upCmd.Flags().StringVar(&upReq.CloudImage, "image", "", "VM/cloud image override")
	upCmd.Flags().StringVar(&upReq.JoinServer, "join-server", "", "existing k3s server URL to join as agent")
	upCmd.Flags().StringVar(&upReq.JoinToken, "join-token", "", "k3s join token (prefer K3S_TOKEN env)")
	upCmd.Flags().Uint16Var(&upReq.UpstreamPort, "upstream-port", 0, "upstream hanzo-cloud port")

	downCmd := &cobra.Command{
		Use:   "down",
		Short: "Stop local/edge k3s managed by hanzod (POST /v1/k3s/down)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e := envOf()
			st, err := newHanzodClient(hanzodURL).Down(cmd.Context())
			if err != nil {
				return err
			}
			return e.emit(st, func(w io.Writer) { printK3sStatus(w, st) })
		},
	}

	var pathOnly bool
	kubeconfigCmd := &cobra.Command{
		Use:   "kubeconfig",
		Short: "Resolve local hanzod-managed k3s kubeconfig (~/.kube/hanzo.yaml or ~/.kube/hanzo-k3s.yaml)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e := envOf()
			kc := resolveK3sKubeconfig(cmd.Context(), kubeconfigFlag, newHanzodClient(hanzodURL))
			if pathOnly {
				fmt.Fprintln(cmd.OutOrStdout(), kc.Path)
				return nil
			}
			return e.emit(kc, func(w io.Writer) {
				tw := newTab(w)
				fmt.Fprintf(tw, "path:\t%s\n", dashIfEmpty(kc.Path))
				fmt.Fprintf(tw, "exists:\t%s\n", yesno(kc.Exists))
				fmt.Fprintf(tw, "context:\t%s\n", dashIfEmpty(kc.CurrentContext))
				fmt.Fprintf(tw, "server:\t%s\n", dashIfEmpty(kc.Server))
				fmt.Fprintf(tw, "source:\t%s\n", dashIfEmpty(kc.Source))
				tw.Flush()
			})
		},
	}
	kubeconfigCmd.Flags().BoolVar(&pathOnly, "path", false, "print only the resolved kubeconfig file path")

	newK3sKubectlPass := func(use, short string, prefixArgs ...string) *cobra.Command {
		return &cobra.Command{
			Use:                use,
			Short:              short,
			DisableFlagParsing: true,
			RunE: func(cmd *cobra.Command, args []string) error {
				if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
					return cmd.Help()
				}
				rawKC, cleaned := extractKubeconfigFlag(args)
				kc := resolveK3sKubeconfig(cmd.Context(), cmp.Or(rawKC, kubeconfigFlag), newHanzodClient(hanzodURL))
				fullArgs := make([]string, 0, len(prefixArgs)+len(cleaned))
				fullArgs = append(fullArgs, prefixArgs...)
				fullArgs = append(fullArgs, cleaned...)
				return runKubectl(cmd.Context(), kc.Path, fullArgs, cmd.OutOrStdout(), cmd.ErrOrStderr())
			},
		}
	}

	cmd.AddCommand(
		statusCmd,
		upCmd,
		downCmd,
		kubeconfigCmd,
		newK3sKubectlPass("kubectl [args...]", "Execute kubectl targeting local hanzod-managed k3s"),
		newK3sKubectlPass("get [args...]", "Run `kubectl get` targeting local k3s", "get"),
		newK3sKubectlPass("pods [args...]", "Run `kubectl get pods` targeting local k3s", "get", "pods"),
		newK3sKubectlPass("logs [args...]", "Run `kubectl logs` targeting local k3s", "logs"),
		newK3sKubectlPass("apply [args...]", "Run `kubectl apply` targeting local k3s", "apply"),
		newK3sKubectlPass("describe [args...]", "Run `kubectl describe` targeting local k3s", "describe"),
		newK3sKubectlPass("rollout [args...]", "Run `kubectl rollout` targeting local k3s", "rollout"),
	)
	return cmd
}
