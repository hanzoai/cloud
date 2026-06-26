// Package evalsvc mounts the Hanzo Cloud /v1/evals/* surface: a thin facade
// that composes two systems that ALREADY work — the console eval engine (the
// Langfuse v3 fork at console.hanzo.svc, whose public REST API owns Datasets,
// DatasetItems, Evaluators, DatasetRuns and Scores) and the in-process model
// gateway (the AI subsystem's OpenAI-compatible /v1/chat/completions) — into
// one OpenAI-style evals API.
//
// No eval logic is reimplemented here. Datasets, dataset-items, evaluators and
// score reads are proxied verbatim to the console's public API (HTTP Basic
// auth, a public/secret key pair that is itself project-scoped — so the key
// pair IS the IAM-org → console-project binding; there is no separate
// projectId to thread). POST /v1/evals/runs orchestrates a REAL run: for each
// dataset item it calls the in-process gateway for the model-under-test output,
// records a trace + dataset-run-item in the console, then (synchronously) calls
// the gateway again as LLM-as-judge and posts the score against that trace.
//
// Auth, two domains, kept orthogonal:
//   - console (datasets/scores/...) ← HTTP Basic, key pair from config/KMS.
//   - model gateway (chat)          ← the CALLER's own Authorization bearer,
//     forwarded to the loopback /v1/chat/completions. The run therefore
//     executes with the caller's identity and model entitlements; no privilege
//     escalation, and the gateway authenticates it exactly as a direct call.
//
// Order 145: binds /v1/evals/* BEFORE the AI subsystem's /v1/* catch-all (150),
// the same slot productsvc uses. The composition root auto-registers
// GET /v1/evals/health (serve.go) for every subsystem in the registry.
package evalsvc

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/zip"
	luxlog "github.com/luxfi/log"
)

// Run sizing: a synchronous run is bounded so one request cannot fan out into
// thousands of paired LLM calls. Larger sweeps belong on the async path (see
// runItem) where the console's own judge worker scores at its own pace.
const (
	defaultRunItems = 20
	maxRunItems     = 100
)

// config is resolved once at Mount from the canonical console env (the
// console-keys / console-langfuse-keys KMS-synced secret) plus the cloud
// listener for the in-process gateway loopback. Endpoint defaults to the real
// in-cluster console Service DNS; keys have no default and fail closed.
type config struct {
	consoleURL string // console base, no trailing slash
	publicKey  string // console (Langfuse) public key  — global fallback
	secretKey  string // console (Langfuse) secret key  — global fallback
	gatewayURL string // loopback base for the in-process gateway, e.g. http://127.0.0.1:8080
}

func loadConfig() config {
	return config{
		consoleURL: strings.TrimRight(firstNonEmpty(
			getenv("CONSOLE_HOST"),
			getenv("consoleEndpoint"),
			"http://console.hanzo.svc.cluster.local",
		), "/"),
		publicKey:  firstNonEmpty(getenv("CONSOLE_PUBLIC_KEY"), getenv("LANGFUSE_PUBLIC_KEY")),
		secretKey:  firstNonEmpty(getenv("CONSOLE_SECRET_KEY"), getenv("LANGFUSE_SECRET_KEY")),
		gatewayURL: loopbackBase(firstNonEmpty(getenv("CLOUD_LISTEN"), ":8080")),
	}
}

// service holds the resolved config + two HTTP clients (a short-timeout one for
// the console control-plane calls, a long-timeout one for the LLM gateway).
type service struct {
	cfg config
	log luxlog.Logger
	kms cloud.KMSClient // per-org key override; nil when KMS is not wired in-process
	cc  *http.Client    // console client
	gc  *http.Client    // gateway client (LLM latency)
}

// Mount registers the /v1/evals/* surface on app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("evalsvc.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("evalsvc.Mount: nil deps.Logger")
	}
	s := &service{
		cfg: loadConfig(),
		log: deps.Logger.New("subsystem", "evals"),
		kms: deps.KMS,
		cc:  &http.Client{Timeout: 30 * time.Second},
		gc:  &http.Client{Timeout: 120 * time.Second},
	}

	// Thin proxies — pass the body/query through verbatim and return the
	// console's status + body unchanged, so console validation errors surface
	// honestly instead of being masked.
	app.Post("/v1/evals/datasets", s.proxy(http.MethodPost, "/api/public/v2/datasets"))
	app.Post("/v1/evals/dataset-items", s.proxy(http.MethodPost, "/api/public/dataset-items"))
	app.Post("/v1/evals/evaluators", s.proxy(http.MethodPost, "/api/public/unstable/evaluators"))
	app.Get("/v1/evals/scores", s.proxy(http.MethodGet, "/api/public/v2/scores"))

	// Orchestration.
	app.Post("/v1/evals/runs", s.runHandler)

	s.log.Info("evals surface mounted",
		"console", s.cfg.consoleURL,
		"gateway", s.cfg.gatewayURL,
		"consoleKey", s.cfg.publicKey != "",
		"brand", deps.Brand,
	)
	return nil
}

// ── proxy ────────────────────────────────────────────────────────────────────

// proxy returns a handler that forwards method+path to the console under HTTP
// Basic auth, resolving the project key pair from the request's tenant.
func (s *service) proxy(method, consolePath string) func(c *zip.Ctx) error {
	return func(c *zip.Ctx) error {
		pk, sk, err := s.resolveKeys(c.Context(), tenant(c))
		if err != nil {
			return zip.Errorf(http.StatusServiceUnavailable, "%s", err.Error())
		}
		target := s.cfg.consoleURL + consolePath
		if q := c.Fiber().Request().URI().QueryString(); len(q) > 0 {
			target += "?" + string(q)
		}
		var body io.Reader
		if method == http.MethodPost {
			body = bytes.NewReader(c.Body())
		}
		req, err := http.NewRequestWithContext(c.Context(), method, target, body)
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "evals: build request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Basic "+basic(pk, sk))
		resp, err := s.cc.Do(req)
		if err != nil {
			return zip.Errorf(http.StatusBadGateway, "evals: console unreachable: %v", err)
		}
		defer resp.Body.Close()
		rb, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		c.SetHeader("Content-Type", "application/json")
		return c.Bytes(resp.StatusCode, rb)
	}
}

// ── run orchestration ────────────────────────────────────────────────────────

type judgeSpec struct {
	Model    string `json:"model"`
	Criteria string `json:"criteria"`
	Name     string `json:"name"`
}

type runRequest struct {
	Dataset string     `json:"dataset"`
	Model   string     `json:"model"`
	RunName string     `json:"runName"`
	Limit   int        `json:"limit"`
	Judge   *judgeSpec `json:"judge"`
}

type itemResult struct {
	ItemID  string  `json:"itemId"`
	TraceID string  `json:"traceId,omitempty"`
	Score   float64 `json:"score"`
	Output  string  `json:"output,omitempty"`
	Error   string  `json:"error,omitempty"`
}

type runSummary struct {
	Dataset    string       `json:"dataset"`
	Model      string       `json:"model"`
	JudgeModel string       `json:"judgeModel"`
	RunName    string       `json:"runName"`
	Items      int          `json:"items"`
	Scored     int          `json:"scored"`
	AvgScore   float64      `json:"avgScore"`
	Results    []itemResult `json:"results"`
}

func (s *service) runHandler(c *zip.Ctx) error {
	// The model gateway needs the caller's own credential — fail closed rather
	// than run models anonymously or with a service identity.
	authz := c.Header("Authorization")
	if authz == "" {
		return zip.ErrUnauthorized("evals/runs: missing Authorization bearer; the model gateway needs the caller's key/JWT")
	}
	var rr runRequest
	if err := json.Unmarshal(c.Body(), &rr); err != nil {
		return zip.Errorf(http.StatusBadRequest, "evals/runs: invalid JSON body: %v", err)
	}
	if rr.Dataset == "" || rr.Model == "" {
		return zip.Errorf(http.StatusBadRequest, "evals/runs: 'dataset' and 'model' are required")
	}
	pk, sk, err := s.resolveKeys(c.Context(), tenant(c))
	if err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "%s", err.Error())
	}
	basicAuth := basic(pk, sk)

	limit := rr.Limit
	if limit <= 0 || limit > maxRunItems {
		limit = defaultRunItems
	}
	runName := rr.RunName
	if runName == "" {
		runName = "run-" + time.Now().UTC().Format("20060102T150405Z")
	}
	judge := normalizeJudge(rr.Judge, rr.Model)

	items, err := s.fetchItems(c.Context(), basicAuth, rr.Dataset, limit)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "evals/runs: %v", err)
	}
	if len(items) == 0 {
		return zip.Errorf(http.StatusUnprocessableEntity, "evals/runs: dataset %q has no active items", rr.Dataset)
	}

	summary := runSummary{Dataset: rr.Dataset, Model: rr.Model, JudgeModel: judge.Model, RunName: runName, Items: len(items)}
	var sum float64
	for _, it := range items {
		res := s.runItem(c.Context(), authz, basicAuth, runName, rr.Model, judge, it)
		summary.Results = append(summary.Results, res)
		if res.Error == "" {
			sum += res.Score
			summary.Scored++
		}
	}
	if summary.Scored > 0 {
		summary.AvgScore = sum / float64(summary.Scored)
	}

	// Nothing scored is a real failure, not a fake 200.
	status := http.StatusOK
	if summary.Scored == 0 {
		status = http.StatusBadGateway
	}
	return c.JSON(status, summary)
}

// runItem is the single per-item seam, run synchronously:
//  1. model-under-test  → gateway /v1/chat/completions
//  2. trace             → console ingestion (carries the model output)
//  3. dataset-run-item  → links item→trace under runName (creates the run)
//  4. LLM-as-judge      → gateway /v1/chat/completions
//  5. score             → console /scores (numeric, on the trace)
//
// ASYNC EXTENSION POINT: steps 4–5 are the only inline judge work. To scale,
// skip them and instead register an evaluator once
// (POST /v1/evals/evaluators → /api/public/unstable/evaluators) that targets
// dataset runs; step 3 then enqueues the console's BullMQ LLM-as-judge worker
// (addDatasetRunItemsToEvalQueue), which scores this run item asynchronously
// using the project's stored LLM connection. Nothing else in this file changes.
func (s *service) runItem(ctx context.Context, authz, basicAuth, runName, model string, judge judgeSpec, it datasetItem) itemResult {
	res := itemResult{ItemID: it.ID}

	output, err := s.gatewayChat(ctx, authz, model, buildMessages(it.Input))
	if err != nil {
		res.Error = "model: " + err.Error()
		return res
	}
	res.Output = truncate(output, 2000)

	traceID := uuidv4()
	res.TraceID = traceID
	if err := s.postTrace(ctx, basicAuth, traceID, runName, model, it, output); err != nil {
		res.Error = "trace: " + err.Error()
		return res
	}
	if err := s.linkRunItem(ctx, basicAuth, runName, it.ID, traceID); err != nil {
		res.Error = "run-item: " + err.Error()
		return res
	}

	score, reasoning, err := s.judgeOutput(ctx, authz, judge, it, output)
	if err != nil {
		res.Error = "judge: " + err.Error()
		return res
	}
	res.Score = score
	if err := s.postScore(ctx, basicAuth, traceID, judge.Name, score, reasoning); err != nil {
		res.Error = "score: " + err.Error()
		return res
	}
	return res
}

// ── gateway (model-under-test + judge) ───────────────────────────────────────

func (s *service) gatewayChat(ctx context.Context, authz, model string, messages []map[string]any) (string, error) {
	payload := map[string]any{"model": model, "messages": messages, "temperature": 0, "stream": false}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	status, err := s.doJSON(ctx, s.gc, http.MethodPost, s.cfg.gatewayURL+"/v1/chat/completions",
		map[string]string{"Authorization": authz}, payload, &out)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		if out.Error != nil && out.Error.Message != "" {
			return "", fmt.Errorf("gateway %d: %s", status, out.Error.Message)
		}
		return "", fmt.Errorf("gateway status %d", status)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("gateway returned no choices")
	}
	return out.Choices[0].Message.Content, nil
}

func (s *service) judgeOutput(ctx context.Context, authz string, judge judgeSpec, it datasetItem, output string) (float64, string, error) {
	sys := "You are a strict evaluator. Score the ASSISTANT OUTPUT from 0.0 to 1.0 for how well it satisfies the criteria and matches the expected output. " +
		`Reply ONLY with compact JSON: {"score": <number 0..1>, "reasoning": "<one sentence>"}.`
	user := fmt.Sprintf("CRITERIA:\n%s\n\nINPUT:\n%s\n\nEXPECTED OUTPUT:\n%s\n\nASSISTANT OUTPUT:\n%s",
		judge.Criteria, asText(it.Input), asText(it.Expected), output)
	content, err := s.gatewayChat(ctx, authz, judge.Model, []map[string]any{
		{"role": "system", "content": sys},
		{"role": "user", "content": user},
	})
	if err != nil {
		return 0, "", err
	}
	return parseJudge(content)
}

// ── console writes (ingestion / run-items / scores) + reads (items) ──────────

func (s *service) fetchItems(ctx context.Context, basicAuth, dataset string, limit int) ([]datasetItem, error) {
	target := fmt.Sprintf("%s/api/public/dataset-items?datasetName=%s&limit=%d",
		s.cfg.consoleURL, url.QueryEscape(dataset), limit)
	var out struct {
		Data []datasetItem `json:"data"`
	}
	status, err := s.doJSON(ctx, s.cc, http.MethodGet, target, s.basicHeader(basicAuth), nil, &out)
	if err != nil {
		return nil, err
	}
	if status >= 300 {
		return nil, fmt.Errorf("dataset-items status %d", status)
	}
	active := make([]datasetItem, 0, len(out.Data))
	for _, it := range out.Data {
		if it.Status == "" || it.Status == "ACTIVE" {
			active = append(active, it)
		}
	}
	return active, nil
}

func (s *service) postTrace(ctx context.Context, basicAuth, traceID, runName, model string, it datasetItem, output string) error {
	batch := map[string]any{"batch": []map[string]any{{
		"id":        uuidv4(),
		"type":      "trace-create",
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"body": map[string]any{
			"id":     traceID,
			"name":   "eval:" + runName,
			"input":  it.Input,
			"output": output,
			"metadata": map[string]any{
				"dataset":       it.DatasetName,
				"datasetItemId": it.ID,
				"runName":       runName,
				"model":         model,
			},
			"tags": []string{"eval", "run:" + runName},
		},
	}}}
	// Ingestion returns 207 Multi-Status on success; treat <300 as accepted.
	status, err := s.doJSON(ctx, s.cc, http.MethodPost, s.cfg.consoleURL+"/api/public/ingestion", s.basicHeader(basicAuth), batch, nil)
	if err != nil {
		return err
	}
	if status >= 300 {
		return fmt.Errorf("ingestion status %d", status)
	}
	return nil
}

func (s *service) linkRunItem(ctx context.Context, basicAuth, runName, itemID, traceID string) error {
	body := map[string]any{"runName": runName, "datasetItemId": itemID, "traceId": traceID}
	status, err := s.doJSON(ctx, s.cc, http.MethodPost, s.cfg.consoleURL+"/api/public/dataset-run-items", s.basicHeader(basicAuth), body, nil)
	if err != nil {
		return err
	}
	if status >= 300 {
		return fmt.Errorf("dataset-run-items status %d", status)
	}
	return nil
}

func (s *service) postScore(ctx context.Context, basicAuth, traceID, name string, value float64, comment string) error {
	// Console score validation requires exactly one target; we attach to the
	// trace (which the dataset-run-item links into the run).
	body := map[string]any{"name": name, "value": value, "dataType": "NUMERIC", "traceId": traceID}
	if comment != "" {
		body["comment"] = truncate(comment, 500)
	}
	status, err := s.doJSON(ctx, s.cc, http.MethodPost, s.cfg.consoleURL+"/api/public/scores", s.basicHeader(basicAuth), body, nil)
	if err != nil {
		return err
	}
	if status >= 300 {
		return fmt.Errorf("scores status %d", status)
	}
	return nil
}

type datasetItem struct {
	ID          string `json:"id"`
	DatasetName string `json:"datasetName"`
	Status      string `json:"status"`
	Input       any    `json:"input"`
	Expected    any    `json:"expectedOutput"`
}

// ── key resolution ───────────────────────────────────────────────────────────

// resolveKeys returns the console key pair for the tenant. Per-org KMS keys
// (console-pk-{org}/console-sk-{org}) win when KMS is wired in-process (the
// extension point that lights up once deps.KMS is populated); otherwise the
// global key pair from env (the console-keys secret) is used. Missing keys fail
// closed with a precise, config-naming error — never a fake success.
func (s *service) resolveKeys(ctx context.Context, org string) (publicKey, secretKey string, err error) {
	if s.kms != nil && org != "" {
		if pkb, e := s.kms.GetSecret(ctx, "console-pk-"+org); e == nil && len(pkb) > 0 {
			if skb, e2 := s.kms.GetSecret(ctx, "console-sk-"+org); e2 == nil && len(skb) > 0 {
				return string(pkb), string(skb), nil
			}
		}
	}
	if s.cfg.publicKey != "" && s.cfg.secretKey != "" {
		return s.cfg.publicKey, s.cfg.secretKey, nil
	}
	return "", "", fmt.Errorf(
		"evals: no console API key for org %q: set CONSOLE_PUBLIC_KEY/CONSOLE_SECRET_KEY (KMS-synced secret 'console-keys') or per-org KMS 'console-pk-%s'/'console-sk-%s'",
		org, org, org)
}

func (s *service) basicHeader(basicAuth string) map[string]string {
	return map[string]string{"Authorization": "Basic " + basicAuth}
}

// ── one HTTP+JSON round-trip ─────────────────────────────────────────────────

func (s *service) doJSON(ctx context.Context, client *http.Client, method, target string, headers map[string]string, payload, into any) (int, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return 0, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	rb, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if into != nil && len(rb) > 0 {
		if err := json.Unmarshal(rb, into); err != nil {
			return resp.StatusCode, fmt.Errorf("decode %s: %v (%s)", target, err, truncate(string(rb), 200))
		}
	}
	return resp.StatusCode, nil
}

// ── pure helpers ─────────────────────────────────────────────────────────────

// tenant resolves the org slug used to scope console keys, accepting the
// IAM/Hanzo project headers and falling back to the gateway-minted X-Org-Id.
func tenant(c *zip.Ctx) string {
	if v := c.Header("X-IAM-Project-Id"); v != "" {
		return v
	}
	if v := c.Header("X-Hanzo-Org"); v != "" {
		return v
	}
	return c.Org() // X-Org-Id
}

// loopbackBase turns a listen address (":8080", "0.0.0.0:8080") into a loopback
// base URL for the in-process gateway.
func loopbackBase(listen string) string {
	_, port, err := net.SplitHostPort(listen)
	if err != nil || port == "" {
		port = "8080"
	}
	return "http://127.0.0.1:" + port
}

// buildMessages turns a dataset item input (string, {messages:[...]}, or any
// JSON) into OpenAI chat messages.
func buildMessages(input any) []map[string]any {
	switch v := input.(type) {
	case string:
		return []map[string]any{{"role": "user", "content": v}}
	case map[string]any:
		if raw, ok := v["messages"].([]any); ok && len(raw) > 0 {
			out := make([]map[string]any, 0, len(raw))
			for _, m := range raw {
				if mm, ok := m.(map[string]any); ok {
					out = append(out, mm)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	return []map[string]any{{"role": "user", "content": asText(input)}}
}

// parseJudge extracts {score, reasoning} from a judge model reply, tolerating
// surrounding prose; falls back to a bare float. No score is invented on
// failure — the caller records an item error instead.
func parseJudge(content string) (float64, string, error) {
	if i := strings.IndexByte(content, '{'); i >= 0 {
		if j := strings.LastIndexByte(content, '}'); j > i {
			var v struct {
				Score     float64 `json:"score"`
				Reasoning string  `json:"reasoning"`
			}
			if err := json.Unmarshal([]byte(content[i:j+1]), &v); err == nil {
				return clamp01(v.Score), v.Reasoning, nil
			}
		}
	}
	if f, err := strconv.ParseFloat(strings.TrimSpace(content), 64); err == nil {
		return clamp01(f), "", nil
	}
	return 0, "", fmt.Errorf("could not parse judge score from %q", truncate(content, 120))
}

func normalizeJudge(j *judgeSpec, model string) judgeSpec {
	out := judgeSpec{
		Model:    model,
		Name:     "llm-judge",
		Criteria: "The output should be correct, relevant, and match the expected output.",
	}
	if j != nil {
		if j.Model != "" {
			out.Model = j.Model
		}
		if j.Name != "" {
			out.Name = j.Name
		}
		if j.Criteria != "" {
			out.Criteria = j.Criteria
		}
	}
	return out
}

func asText(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func basic(pk, sk string) string {
	return base64.StdEncoding.EncodeToString([]byte(pk + ":" + sk))
}

func uuidv4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func clamp01(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func getenv(key string) string { return strings.TrimSpace(os.Getenv(key)) }

func init() {
	cloud.Register("evals", 145, func(app any, deps cloud.Deps) error {
		a, ok := app.(*zip.App)
		if !ok {
			return fmt.Errorf("evalsvc.Mount: app is %T, want *zip.App", app)
		}
		return Mount(a, deps)
	})
}
