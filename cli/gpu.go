package cli

// gpu.go — `hanzo gpu connect|status|disconnect`: bring THIS machine's GPU to the
// Hanzo cloud fleet. connect authenticates against IAM (reusing the `hanzo login`
// credential store), registers the machine + its GPU inventory as a heartbeating
// presence record in the org's `fleet` tasks namespace, and runs an OUTBOUND-only
// worker loop that CLAIMS jobs from the org's `gpu-jobs` namespace — the NAT-safe
// primitive (the worker dials out; nothing dials in). The registered GPU then shows
// up on console.hanzo.ai's Machines + GPUs pages (provider="byo") via cloud's
// /v1/fleet union.
//
// One identity, one way: the same IAM token `hanzo login` mints (org in its `owner`
// claim) authorizes every cloud call; the server derives the tenant from the token,
// so the CLI never sends an org header. Job types are pluggable (jobHandlers); v1
// ships `echo` (trivial, no GPU — smoke/E2E) and `studio.render` (POST to the local
// ComfyUI at 127.0.0.1:8188 and poll history).
//
// --serve-engine adds the `engine.serve` capability: the worker probes a local
// hanzo-engine (the OpenAI + Anthropic model server on :1234), advertises its model
// endpoint in the presence record, and prints (or with --register-provider, POSTs)
// the /v1/add-provider call that routes api.hanzo.ai model traffic to this GPU. One
// fleet, two job types: engine.serve (model serving) alongside studio.render.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

const (
	fleetNS       = "fleet"
	defaultJobsNS = "gpu-jobs"
	// heartbeatEvery keeps the presence record fresh; well under cloud's 90s
	// byoLiveWindow so one missed beat does not flap the machine offline.
	heartbeatEvery = 30 * time.Second
	claimPoll      = 2 * time.Second
	claimLeaseSecs = 120
	// localComfyUI is the studio render backend the studio.render handler drives.
	localComfyUI = "http://127.0.0.1:8188"
	// defaultEngineURL is where --serve-engine probes hanzo-engine on THIS node.
	// hanzo-engine binds its OpenAI + Anthropic HTTP API on 0.0.0.0:1234 by default.
	defaultEngineURL = "http://localhost:1234"
)

// Fleet capability names advertised in the presence record. studioCap is always
// present (the worker claims gpu-jobs); engineCap is added by --serve-engine so the
// gateway can route model calls to a hanzo-engine running on this node.
const (
	studioCap = "studio.render"
	engineCap = "engine.serve"
)

// ---------------------------------------------------------------------------
// Command wiring.
// ---------------------------------------------------------------------------

func newGPUCmd(envOf func() *Env, _ *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gpu",
		Short: "Bring this machine's GPU to the Hanzo cloud fleet",
		Long: "Connect this machine's GPU to the Hanzo cloud so it appears in the console\n" +
			"(Machines + GPUs, provider=byo) and claims jobs from your org's gpu-jobs queue.\n" +
			"Authentication reuses the `hanzo login` token; the org is taken from its claims.",
	}

	var jobsNS string
	var daemon bool
	var serveEngine bool
	var engineURL string
	var engineEndpoint string
	var registerProvider bool
	connect := &cobra.Command{
		Use:   "connect",
		Short: "Register this GPU and run the outbound worker loop",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts := connectOpts{
				jobsNS:           jobsNS,
				serveEngine:      serveEngine,
				engineURL:        engineURL,
				engineEndpoint:   engineEndpoint,
				registerProvider: registerProvider,
			}
			if daemon {
				return installDaemon(cmd, opts)
			}
			return runConnect(cmd, envOf(), opts)
		},
	}
	connect.Flags().StringVar(&jobsNS, "jobs-namespace", defaultJobsNS, "tasks namespace to claim jobs from")
	connect.Flags().BoolVar(&daemon, "daemon", false, "install a systemd --user unit (Restart=always) instead of running in the foreground")
	connect.Flags().BoolVar(&serveEngine, "serve-engine", false, "also advertise a hanzo-engine model server (OpenAI + Anthropic) running on this node")
	connect.Flags().StringVar(&engineURL, "engine-url", defaultEngineURL, "local URL where hanzo-engine is probed (GET /v1/models)")
	connect.Flags().StringVar(&engineEndpoint, "engine-endpoint", "", "public URL to advertise for gateway routing (defaults to --engine-url; a BYO node needs a reachable URL/tunnel)")
	connect.Flags().BoolVar(&registerProvider, "register-provider", false, "auto-register the engine endpoint as an org model provider (POST /v1/add-provider)")

	status := &cobra.Command{
		Use:   "status",
		Short: "Show the org's connected GPUs (this machine highlighted)",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return runGPUStatus(cmd, envOf()) },
	}

	disconnect := &cobra.Command{
		Use:   "disconnect",
		Short: "Remove this machine from the fleet",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return runDisconnect(cmd, envOf()) },
	}

	cmd.AddCommand(connect, status, disconnect)
	return cmd
}

// ---------------------------------------------------------------------------
// worker — the connect loop's state.
// ---------------------------------------------------------------------------

type worker struct {
	env      *Env
	http     *http.Client
	baseURL  string
	identity string // sanitized hostname == fleet activityId == runId
	hostname string
	jobsNS   string
	gpus     []gpuInfo
	handlers map[string]jobHandler

	// engine.serve — advertise a hanzo-engine model server running on this node.
	serveEngine  bool
	engineURL    string               // local URL probed for /v1/models
	engineAdvURL string               // endpoint advertised for gateway routing
	engine       *engineAdvertisement // latest probe result (nil until probed)
}

// engineAdvertisement describes a hanzo-engine model server on this node.
// hanzo-engine serves the OpenAI AND Anthropic HTTP APIs from ONE axum port
// (0.0.0.0:1234), so the gateway can route model calls here as an OpenAI-compatible
// (Type=Local) provider. This rides in the fleet presence record's Input.
type engineAdvertisement struct {
	URL    string   `json:"url"`              // base the gateway calls (…:1234)
	APIs   []string `json:"apis,omitempty"`   // wire formats served: ["openai","anthropic"]
	Models []string `json:"models,omitempty"` // model ids from GET /v1/models
	Status string   `json:"status"`           // "ready" | "unreachable"
}

type gpuInfo struct {
	Name        string `json:"name"`
	MemoryTotal string `json:"memoryTotal,omitempty"`
}

// registration is the fleet presence activity's Input — the shape cloud's
// clients/visor/fleet.go fleetRegistration decodes. Capabilities + Engine are
// additive (omitempty): an older cloud that does not read them still renders the
// GPU; a newer one advertises the engine endpoint on GET /v1/fleet/workers.
type registration struct {
	Hostname     string               `json:"hostname"`
	Os           string               `json:"os"`
	Version      string               `json:"version"`
	JobQueue     string               `json:"jobQueue"`
	GPUs         []gpuInfo            `json:"gpus"`
	Capabilities []string             `json:"capabilities,omitempty"`
	Engine       *engineAdvertisement `json:"engine,omitempty"`
}

type jobHandler func(ctx context.Context, input json.RawMessage) (any, error)

func newWorker(env *Env, jobsNS string) (*worker, error) {
	host, _ := os.Hostname()
	if host == "" {
		host = "worker"
	}
	id := sanitizeID(host)
	w := &worker{
		env:      env,
		http:     &http.Client{Timeout: 60 * time.Second},
		baseURL:  env.CloudURL,
		identity: id,
		hostname: host,
		jobsNS:   firstNonEmpty(jobsNS, defaultJobsNS),
		gpus:     detectGPUs(),
	}
	w.handlers = map[string]jobHandler{
		"echo":          echoHandler,
		"studio.render": studioRenderHandler,
	}
	return w, nil
}

var idRE = regexp.MustCompile(`[^a-z0-9-]+`)

// sanitizeID lower-cases and reduces s to the [a-z0-9-] alphabet valid as a
// tasks path segment / activity id, so a hostname is a stable machine key.
func sanitizeID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = idRE.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "worker"
	}
	return s
}

// detectGPUs queries nvidia-smi for the machine's accelerators, degrading
// gracefully to an empty list (CPU-only) when nvidia-smi is absent or errors.
func detectGPUs() []gpuInfo {
	out, err := exec.Command("nvidia-smi", "--query-gpu=name,memory.total", "--format=csv,noheader").Output()
	if err != nil {
		return nil
	}
	var gpus []gpuInfo
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		name, mem, _ := strings.Cut(line, ",")
		gpus = append(gpus, gpuInfo{Name: strings.TrimSpace(name), MemoryTotal: strings.TrimSpace(mem)})
	}
	return gpus
}

// ---------------------------------------------------------------------------
// connect.
// ---------------------------------------------------------------------------

// connectOpts is the resolved `hanzo gpu connect` configuration.
type connectOpts struct {
	jobsNS           string
	serveEngine      bool
	engineURL        string // local URL to probe hanzo-engine
	engineEndpoint   string // public URL to advertise (defaults to engineURL)
	registerProvider bool   // auto POST /v1/add-provider for the engine
}

func runConnect(cmd *cobra.Command, env *Env, opts connectOpts) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if _, err := env.ensureToken(ctx); err != nil {
		return err
	}
	w, err := newWorker(env, opts.jobsNS)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()

	// engine.serve: probe the local hanzo-engine once so the first registration
	// carries its live model list + reachability.
	if opts.serveEngine {
		w.serveEngine = true
		w.engineURL = firstNonEmpty(opts.engineURL, defaultEngineURL)
		w.engineAdvURL = firstNonEmpty(opts.engineEndpoint, w.engineURL)
		w.refreshEngine(ctx)
	}

	if err := w.register(ctx); err != nil {
		return fmt.Errorf("register: %w", err)
	}
	fmt.Fprintf(out, "connected %q to %s (org %s) — %s\n", w.hostname, w.baseURL, orgOf(env), describeGPUs(w.gpus))
	fmt.Fprintf(out, "claiming %s jobs; heartbeating every %s. Ctrl-C to stop (the machine goes offline after ~90s; `hanzo gpu disconnect` removes it).\n", w.jobsNS, heartbeatEvery)

	if w.serveEngine {
		w.printEngineHint(out)
		if opts.registerProvider {
			if err := w.registerProvider(ctx, w.engine); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "register-provider: %v\n", err)
			} else {
				fmt.Fprintf(out, "  → registered org model provider %q on %s\n", "gpu-"+w.identity, w.baseURL)
			}
		}
	}

	hb := time.NewTicker(heartbeatEvery)
	defer hb.Stop()
	poll := time.NewTicker(claimPoll)
	defer poll.Stop()

	// Heartbeat once immediately so the machine reports online without waiting a
	// full interval.
	_ = w.heartbeat(ctx)

	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(out, "\nstopping (machine will go offline; run `hanzo gpu disconnect` to remove it)")
			return nil
		case <-hb.C:
			// Re-probe the engine; if its reachability or model set changed, rewrite
			// the presence record so the fleet advertisement stays honest (e.g. the
			// engine came up after connect, or loaded a new model).
			if w.serveEngine && w.refreshEngine(ctx) {
				if err := w.register(ctx); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "re-advertise engine: %v\n", err)
				} else {
					fmt.Fprintf(out, "engine %s — %s\n", w.engine.URL, describeEngine(w.engine))
				}
			}
			if err := w.heartbeat(ctx); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "heartbeat: %v\n", err)
			}
		case <-poll.C:
			if err := w.claimAndRun(ctx, out); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "claim: %v\n", err)
			}
		}
	}
}

// register ensures the fleet + jobs namespaces exist, then writes this machine's
// presence record (activityId==runId==identity, no requestId so a reconnect
// overwrites any prior/terminal record with a fresh online row).
func (w *worker) register(ctx context.Context) error {
	for _, ns := range []string{fleetNS, w.jobsNS} {
		if _, err := w.call(ctx, http.MethodPost, "/v1/tasks/namespaces", map[string]any{
			"namespaceInfo": map[string]any{"name": ns},
		}, nil); err != nil {
			return fmt.Errorf("ensure namespace %q: %w", ns, err)
		}
	}
	_, err := w.call(ctx, http.MethodPost, "/v1/tasks/namespaces/"+fleetNS+"/activities", map[string]any{
		"activityId":       w.identity,
		"runId":            w.identity,
		"activityType":     map[string]any{"name": "fleet.worker"},
		"taskQueue":        fleetNS,
		"heartbeatTimeout": "120s",
		"input":            w.buildRegistration(),
	}, nil)
	return err
}

// buildRegistration is the pure presence-record payload: this machine's inventory,
// its advertised capabilities, and (when serving) its hanzo-engine endpoint.
func (w *worker) buildRegistration() registration {
	return registration{
		Hostname:     w.hostname,
		Os:           runtime.GOOS,
		Version:      Version,
		JobQueue:     w.jobsNS,
		GPUs:         w.gpus,
		Capabilities: w.capabilities(),
		Engine:       w.engine,
	}
}

// capabilities lists what this worker does for the org. studio.render is always
// present (it claims gpu-jobs); engine.serve is added when --serve-engine advertises
// a local hanzo-engine model server.
func (w *worker) capabilities() []string {
	caps := []string{studioCap}
	if w.serveEngine {
		caps = append(caps, engineCap)
	}
	return caps
}

func (w *worker) heartbeat(ctx context.Context) error {
	path := fmt.Sprintf("/v1/tasks/namespaces/%s/activities/%s/%s/heartbeat", fleetNS, w.identity, w.identity)
	_, err := w.call(ctx, http.MethodPost, path, map[string]any{
		"details": map[string]any{"gpus": len(w.gpus), "ts": time.Now().UTC().Format(time.RFC3339)},
	}, nil)
	return err
}

// claimAndRun claims the next job (if any), runs its handler, and reports the
// terminal result. A 204 (empty queue) is a no-op.
func (w *worker) claimAndRun(ctx context.Context, out io.Writer) error {
	var act claimedActivity
	code, err := w.call(ctx, http.MethodPost, "/v1/tasks/namespaces/"+w.jobsNS+"/activities/claim", map[string]any{
		"taskQueue":    w.jobsNS,
		"identity":     w.identity,
		"leaseSeconds": claimLeaseSecs,
	}, &act)
	if err != nil {
		return err
	}
	if code == http.StatusNoContent {
		return nil // nothing to do
	}
	wf, run := act.Execution.WorkflowId, act.Execution.RunId
	fmt.Fprintf(out, "claimed job %s (type %s)\n", wf, act.Type.Name)

	h, ok := w.handlers[act.Type.Name]
	if !ok {
		cause := fmt.Sprintf("no handler for job type %q", act.Type.Name)
		_, _ = w.call(ctx, http.MethodPost, w.actPath(wf, run, "fail"), map[string]any{"cause": cause, "identity": w.identity}, nil)
		fmt.Fprintf(out, "  → failed: %s\n", cause)
		return nil
	}
	result, herr := h(ctx, act.Input)
	if herr != nil {
		_, _ = w.call(ctx, http.MethodPost, w.actPath(wf, run, "fail"), map[string]any{"cause": herr.Error(), "identity": w.identity}, nil)
		fmt.Fprintf(out, "  → failed: %v\n", herr)
		return nil
	}
	if _, err := w.call(ctx, http.MethodPost, w.actPath(wf, run, "complete"), map[string]any{"result": result, "identity": w.identity}, nil); err != nil {
		return fmt.Errorf("complete %s: %w", wf, err)
	}
	fmt.Fprintf(out, "  → completed\n")
	return nil
}

func (w *worker) actPath(wf, run, verb string) string {
	return fmt.Sprintf("/v1/tasks/namespaces/%s/activities/%s/%s/%s", w.jobsNS, wf, run, verb)
}

type claimedActivity struct {
	Execution struct {
		WorkflowId string `json:"workflowId"`
		RunId      string `json:"runId"`
	} `json:"execution"`
	Type struct {
		Name string `json:"name"`
	} `json:"type"`
	Input     json.RawMessage `json:"input"`
	TaskQueue string          `json:"taskQueue"`
	Status    string          `json:"status"`
}

// ---------------------------------------------------------------------------
// status / disconnect.
// ---------------------------------------------------------------------------

type fleetWorker struct {
	ID            string               `json:"id"`
	Hostname      string               `json:"hostname"`
	Provider      string               `json:"provider"`
	Location      string               `json:"location"`
	Status        string               `json:"status"`
	GPUs          []gpuInfo            `json:"gpus"`
	LastHeartbeat string               `json:"lastHeartbeat"`
	Capabilities  []string             `json:"capabilities,omitempty"`
	Engine        *engineAdvertisement `json:"engine,omitempty"`
}

func runGPUStatus(cmd *cobra.Command, env *Env) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	if _, err := env.ensureToken(ctx); err != nil {
		return err
	}
	w, _ := newWorker(env, "")
	var resp struct {
		Workers []fleetWorker `json:"workers"`
	}
	if _, err := w.call(ctx, http.MethodGet, "/v1/fleet/workers", nil, &resp); err != nil {
		return err
	}
	return env.emit(resp.Workers, func(out io.Writer) {
		if len(resp.Workers) == 0 {
			fmt.Fprintln(out, "no GPUs connected. Run `hanzo gpu connect` on a machine with a GPU.")
			return
		}
		fmt.Fprintf(out, "%-20s %-8s %-10s %-28s %s\n", "MACHINE", "STATUS", "PROVIDER", "GPU", "LAST HEARTBEAT")
		for _, fw := range resp.Workers {
			marker := ""
			if fw.ID == w.identity {
				marker = " *"
			}
			fmt.Fprintf(out, "%-20s %-8s %-10s %-28s %s%s\n",
				fw.Hostname, fw.Status, fw.Provider, describeGPUs(fw.GPUs), fw.LastHeartbeat, marker)
			if fw.Engine != nil {
				fmt.Fprintf(out, "%-20s   ↳ engine %s — %s\n", "", fw.Engine.URL, describeEngine(fw.Engine))
			}
		}
	})
}

func runDisconnect(cmd *cobra.Command, env *Env) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	if _, err := env.ensureToken(ctx); err != nil {
		return err
	}
	w, _ := newWorker(env, "")
	path := fmt.Sprintf("/v1/tasks/namespaces/%s/activities/%s/%s/complete", fleetNS, w.identity, w.identity)
	if _, err := w.call(ctx, http.MethodPost, path, map[string]any{
		"result":   map[string]any{"disconnected": true},
		"identity": w.identity,
	}, nil); err != nil {
		return fmt.Errorf("disconnect: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "disconnected %q from the fleet\n", w.hostname)
	return nil
}

// ---------------------------------------------------------------------------
// HTTP + token.
// ---------------------------------------------------------------------------

// call issues one authed JSON request to the cloud API, decoding a 2xx body into
// out (when non-nil) and returning the status code. A 204 returns (204,nil) with
// out untouched — the empty-queue signal the claim loop reads.
func (w *worker) call(ctx context.Context, method, path string, body, out any) (int, error) {
	tok, err := w.env.ensureToken(ctx)
	if err != nil {
		return 0, err
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, w.baseURL+path, rdr)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "hanzo-cli/"+Version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := w.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		return resp.StatusCode, fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, serverMessage(raw))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s %s: decode response: %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}

// ensureToken returns a valid IAM bearer token, refreshing it when it is missing
// or within 90s of expiry and a refresh token is available. Refreshed credentials
// are persisted so a long-running connect loop survives token rotation. Returns a
// clear error when no usable credential exists.
func (e *Env) ensureToken(ctx context.Context) (string, error) {
	if t := os.Getenv("HANZO_TOKEN"); t != "" {
		return t, nil
	}
	if e.creds == nil || e.creds.AccessToken == "" {
		return "", fmt.Errorf("not logged in: run `hanzo login`")
	}
	needsRefresh := e.creds.Expiry > 0 && time.Now().Add(90*time.Second).Unix() >= e.creds.Expiry
	if needsRefresh && e.creds.RefreshToken != "" {
		iam := newIAMClient(e.IAMIssuer, e.ClientID)
		tr, err := iam.refreshGrant(ctx, e.creds.RefreshToken)
		if err == nil {
			nc := credsFromToken(tr)
			// Preserve any machine tokens + a refresh token IAM did not re-issue.
			nc.PlatformToken = e.creds.PlatformToken
			nc.BuildToken = e.creds.BuildToken
			if nc.RefreshToken == "" {
				nc.RefreshToken = e.creds.RefreshToken
			}
			*e.creds = *nc
			_ = e.creds.Save()
		}
		// On refresh failure fall through: the current token may still be valid
		// (clock skew) and the server is the authority.
	}
	return e.creds.AccessToken, nil
}

func orgOf(e *Env) string {
	if e.creds != nil && e.creds.Owner != "" {
		return e.creds.Owner
	}
	return "?"
}

func describeGPUs(gpus []gpuInfo) string {
	if len(gpus) == 0 {
		return "CPU-only"
	}
	parts := make([]string, len(gpus))
	for i, g := range gpus {
		if g.MemoryTotal != "" {
			parts[i] = g.Name + " (" + g.MemoryTotal + ")"
		} else {
			parts[i] = g.Name
		}
	}
	return strings.Join(parts, ", ")
}

// ---------------------------------------------------------------------------
// Job handlers (pluggable).
// ---------------------------------------------------------------------------

// echoHandler returns its input verbatim — a trivial, GPU-free job for smoke
// tests and E2E, and the reference shape for a job handler.
func echoHandler(_ context.Context, input json.RawMessage) (any, error) {
	var v any
	if len(input) > 0 {
		_ = json.Unmarshal(input, &v)
	}
	return map[string]any{"echo": v}, nil
}

// studioRenderHandler POSTs the job payload to the local ComfyUI /prompt endpoint
// and polls /history/{id} until the prompt completes, returning the output file
// paths. The payload is the ComfyUI prompt graph (input.prompt) — the same body
// studio.hanzo.ai submits. GPU work happens entirely on the local studio server;
// this handler only drives it.
func studioRenderHandler(ctx context.Context, input json.RawMessage) (any, error) {
	var req struct {
		Prompt json.RawMessage `json:"prompt"`
	}
	if err := json.Unmarshal(input, &req); err != nil || len(req.Prompt) == 0 {
		return nil, fmt.Errorf("studio.render: input needs a `prompt` graph")
	}
	cl := &http.Client{Timeout: 60 * time.Second}
	body, _ := json.Marshal(map[string]any{"prompt": req.Prompt})
	post, err := http.NewRequestWithContext(ctx, http.MethodPost, localComfyUI+"/prompt", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	post.Header.Set("Content-Type", "application/json")
	resp, err := cl.Do(post)
	if err != nil {
		return nil, fmt.Errorf("studio.render: POST /prompt: %w (is the local studio server on %s?)", err, localComfyUI)
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("studio.render: /prompt HTTP %d: %s", resp.StatusCode, serverMessage(raw))
	}
	var pr struct {
		PromptID string `json:"prompt_id"`
	}
	if err := json.Unmarshal(raw, &pr); err != nil || pr.PromptID == "" {
		return nil, fmt.Errorf("studio.render: no prompt_id in /prompt response")
	}
	// Poll history until the prompt shows up (completed).
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
		hreq, _ := http.NewRequestWithContext(ctx, http.MethodGet, localComfyUI+"/history/"+pr.PromptID, nil)
		hresp, err := cl.Do(hreq)
		if err != nil {
			continue
		}
		hraw, _ := io.ReadAll(io.LimitReader(hresp.Body, 8<<20))
		hresp.Body.Close()
		var hist map[string]json.RawMessage
		if err := json.Unmarshal(hraw, &hist); err != nil {
			continue
		}
		if entry, ok := hist[pr.PromptID]; ok {
			return map[string]any{"promptId": pr.PromptID, "outputs": collectOutputs(entry)}, nil
		}
	}
	return nil, fmt.Errorf("studio.render: prompt %s did not complete within the deadline", pr.PromptID)
}

// collectOutputs pulls the output image/file names out of a ComfyUI history entry.
func collectOutputs(entry json.RawMessage) []string {
	var e struct {
		Outputs map[string]struct {
			Images []struct {
				Filename  string `json:"filename"`
				Subfolder string `json:"subfolder"`
			} `json:"images"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(entry, &e); err != nil {
		return nil
	}
	var files []string
	for _, node := range e.Outputs {
		for _, img := range node.Images {
			files = append(files, filepath.Join(img.Subfolder, img.Filename))
		}
	}
	return files
}

// ---------------------------------------------------------------------------
// engine.serve — advertise a local hanzo-engine model server.
// ---------------------------------------------------------------------------

// probeEngine GETs {base}/v1/models and returns the served model ids. hanzo-engine
// answers an OpenAI-shaped {"object":"list","data":[{"id":...}]} once it is serving;
// a transport error or non-2xx means it is not up yet.
func probeEngine(ctx context.Context, base string) ([]string, error) {
	url := strings.TrimRight(base, "/") + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	cl := &http.Client{Timeout: 5 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	var ml struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &ml); err != nil {
		return nil, fmt.Errorf("decode model list: %w", err)
	}
	ids := make([]string, 0, len(ml.Data))
	for _, m := range ml.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// refreshEngine re-probes the local engine and updates w.engine, returning true when
// the advertisement materially changed (reachability or model set) so the caller can
// rewrite the presence record.
func (w *worker) refreshEngine(ctx context.Context) bool {
	prev := w.engine
	adv := &engineAdvertisement{URL: w.engineAdvURL, APIs: []string{"openai", "anthropic"}}
	if models, err := probeEngine(ctx, w.engineURL); err != nil {
		adv.Status = "unreachable"
	} else {
		adv.Status = "ready"
		adv.Models = models
	}
	w.engine = adv
	return !sameEngine(prev, adv)
}

func sameEngine(a, b *engineAdvertisement) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.URL != b.URL || a.Status != b.Status || len(a.Models) != len(b.Models) {
		return false
	}
	for i := range a.Models {
		if a.Models[i] != b.Models[i] {
			return false
		}
	}
	return true
}

func describeEngine(adv *engineAdvertisement) string {
	if adv == nil {
		return "no engine"
	}
	if adv.Status != "ready" {
		return adv.Status
	}
	n := len(adv.Models)
	if n == 1 {
		return "ready · 1 model"
	}
	return fmt.Sprintf("ready · %d models", n)
}

// providerBody is the POST /v1/add-provider payload registering this node's engine
// as an org model provider. hanzo-engine is OpenAI-compatible, so Type=Local: the
// gateway speaks the OpenAI wire format to it and auto-appends /v1 to providerUrl.
func (w *worker) providerBody() map[string]any {
	model := "default"
	if w.engine != nil && len(w.engine.Models) > 0 {
		model = w.engine.Models[0]
	}
	url := ""
	if w.engine != nil {
		url = w.engine.URL
	}
	return map[string]any{
		"name":               "gpu-" + w.identity,
		"category":           "Model",
		"type":               "Local",
		"providerUrl":        url,
		"subType":            model,
		"compatibleProvider": model,
	}
}

// printEngineHint prints the ready-to-run registration so an operator can route
// api.hanzo.ai model calls to this GPU (or pass --register-provider to do it here).
func (w *worker) printEngineHint(out io.Writer) {
	adv := w.engine
	if adv == nil {
		return
	}
	fmt.Fprintf(out, "serving hanzo-engine (OpenAI + Anthropic) at %s — %s\n", adv.URL, describeEngine(adv))
	body, _ := json.Marshal(w.providerBody())
	fmt.Fprintln(out, "  route api.hanzo.ai model calls to this GPU by registering it as an org provider:")
	fmt.Fprintf(out, "    curl -sS %s/v1/add-provider -H \"Authorization: Bearer $HANZO_TOKEN\" \\\n", w.baseURL)
	fmt.Fprintf(out, "      -H 'Content-Type: application/json' -d '%s'\n", body)
	fmt.Fprintln(out, "  (or pass --register-provider. The endpoint must be reachable from api.hanzo.ai —")
	fmt.Fprintln(out, "   a cloud GPU is in-cluster; a BYO node needs a public URL/tunnel. add-provider needs a platform-admin token today.)")
}

// registerProvider POSTs /v1/add-provider so the gateway routes model calls to this
// node's engine. Requires the engine to be reachable and (today) a platform-admin
// token; both failures are reported clearly rather than swallowed.
func (w *worker) registerProvider(ctx context.Context, adv *engineAdvertisement) error {
	if adv == nil || adv.Status != "ready" {
		return fmt.Errorf("engine not ready at %s — start hanzo-engine, then retry", w.engineURL)
	}
	code, err := w.call(ctx, http.MethodPost, "/v1/add-provider", w.providerBody(), nil)
	if err != nil {
		if code == http.StatusForbidden {
			return fmt.Errorf("add-provider is gated to a platform-admin token today; register from the console or with an admin token: %w", err)
		}
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// Daemon install (systemd --user).
// ---------------------------------------------------------------------------

func installDaemon(cmd *cobra.Command, opts connectOpts) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return err
	}
	args := "gpu connect"
	if opts.jobsNS != "" && opts.jobsNS != defaultJobsNS {
		args += " --jobs-namespace " + opts.jobsNS
	}
	if opts.serveEngine {
		args += " --serve-engine"
		if opts.engineURL != "" && opts.engineURL != defaultEngineURL {
			args += " --engine-url " + opts.engineURL
		}
		if opts.engineEndpoint != "" {
			args += " --engine-endpoint " + opts.engineEndpoint
		}
		if opts.registerProvider {
			args += " --register-provider"
		}
	}
	unit := fmt.Sprintf(`[Unit]
Description=Hanzo GPU worker (bring-your-own compute)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s %s
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
`, exe, args)
	unitPath := filepath.Join(unitDir, "hanzo-gpu.service")
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "wrote %s\n", unitPath)
	if _, err := exec.LookPath("systemctl"); err != nil {
		fmt.Fprintln(out, "systemctl not found; enable the unit manually once available.")
		return nil
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	if err := exec.Command("systemctl", "--user", "enable", "--now", "hanzo-gpu.service").Run(); err != nil {
		fmt.Fprintf(out, "unit written; enable it with: systemctl --user enable --now hanzo-gpu.service\n")
		return nil
	}
	fmt.Fprintln(out, "enabled and started hanzo-gpu.service (Restart=always). Check: systemctl --user status hanzo-gpu")
	fmt.Fprintln(out, "note: the daemon reuses ~/.hanzo/credentials.json — keep `hanzo login` current, or set HANZO_TOKEN in the unit.")
	return nil
}
