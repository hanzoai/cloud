package worlds

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/tenant"
	"github.com/zap-proto/zip"
)

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

const alpha = "sekai-1-alpha"

var safeID = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,128}$`)

type service struct {
	base, token                   string
	inferenceBase, inferenceToken string
	orgs                          map[string]bool
	http                          *http.Client
}
type empty struct{}
type object = map[string]any

// Model is an available public API alias, never an internal artifact reference.
type Model struct {
	ID     string `json:"id"`
	Stage  string `json:"release_stage"`
	Domain string `json:"domain"`
}
type Models struct {
	Data []Model `json:"data"`
}
type Create struct {
	Model  string `json:"model"`
	Domain string `json:"domain"`
}
type World struct {
	ID string `url:"id" json:"-"`
}
type Event struct {
	ID        string    `url:"id" json:"-"`
	EventID   string    `json:"event_id"`
	EntityID  string    `json:"entity_id"`
	Timestamp float64   `json:"timestamp"`
	Modality  string    `json:"modality"`
	Features  []float64 `json:"features"`
	Neighbors []string  `json:"neighbors,omitempty"`
}
type Candidate struct {
	ID      string      `json:"id"`
	Actions [][]float64 `json:"actions"`
	Cost    float64     `json:"cost"`
}
type Plan struct {
	ID         string      `url:"id" json:"-"`
	EntityID   string      `json:"entity_id"`
	Target     []float64   `json:"target"`
	Candidates []Candidate `json:"candidates"`
}
type Rollout struct {
	ID       string      `url:"id" json:"-"`
	EntityID string      `json:"entity_id"`
	Actions  [][]float64 `json:"actions"`
}
type Belief struct {
	ID     string `url:"id" json:"-"`
	Entity string `url:"entity" json:"-"`
}

// Inference requests finite-state belief filtering and expected free-energy
// policy inference for a registered world. The host owns the generative model,
// preferences, habits and hard admissibility constraints; callers cannot
// supply or override them.
type Inference struct {
	ID          string    `url:"id" json:"-"`
	Belief      []float64 `json:"belief"`
	Observation []int     `json:"observation,omitempty"`
	Policies    [][]int   `json:"policies"`
}

// UnmarshalJSON rejects unknown fields (such as caller attempts to inject
// admissible masks, habits, preferences, model parameters, tenant identity,
// or execution_authorized) so malformed override payloads fail closed with 400.
func (i *Inference) UnmarshalJSON(data []byte) error {
	type wire struct {
		Belief      []float64 `json:"belief"`
		Observation []int     `json:"observation,omitempty"`
		Policies    [][]int   `json:"policies"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var w wire
	if err := dec.Decode(&w); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return errors.New("unexpected trailing JSON data")
	}
	i.Belief = w.Belief
	i.Observation = w.Observation
	i.Policies = w.Policies
	return nil
}

type inferencePayload struct {
	Belief      []float64 `json:"belief"`
	Observation []int     `json:"observation,omitempty"`
	Policies    [][]int   `json:"policies"`
}

func validateOrigin(raw, envName string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if base == "" {
		return "", nil
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return "", fmt.Errorf("%s must be an HTTP(S) origin", envName)
	}
	return base, nil
}

func Use(app cloud.Router, _ cloud.Deps) error {
	base, err := validateOrigin(os.Getenv("SEKAI_UPSTREAM_URL"), "SEKAI_UPSTREAM_URL")
	if err != nil {
		return err
	}
	inferBase, err := validateOrigin(os.Getenv("WORLDS_INFERENCE_UPSTREAM_URL"), "WORLDS_INFERENCE_UPSTREAM_URL")
	if err != nil {
		return err
	}
	inferToken := os.Getenv("WORLDS_INFERENCE_UPSTREAM_KEY")
	if inferToken == "" && inferBase != "" {
		inferToken = os.Getenv("ENGINE_API_KEY")
	}
	s := &service{
		base:           base,
		token:          os.Getenv("SEKAI_UPSTREAM_KEY"),
		inferenceBase:  inferBase,
		inferenceToken: inferToken,
		orgs:           map[string]bool{},
		http:           &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
	for _, org := range strings.Split(os.Getenv("SEKAI_ALPHA_ORGS"), ",") {
		if org = strings.TrimSpace(org); org != "" {
			s.orgs[org] = true
		}
	}
	g := app.Group("/v1/worlds")
	g.Get("/models", s.models)
	g.Post("", s.create)
	g.Get("/:id", s.world)
	g.Post("/:id/events", s.observe)
	g.Post("/:id/plans", s.plan)
	g.Post("/:id/rollouts", s.rollout)
	g.Post("/:id/inference", s.infer)
	g.Get("/:id/beliefs/:entity", s.belief)
	return nil
}

// scopeTenant derives the opaque, deployment-brand + validated-org + project
// tenant key without reading caller-supplied tenant headers.
func (s *service) scopeTenant(ctx context.Context) (string, error) {
	key, err := tenant.Of(ctx, cloud.Brand())
	if err != nil {
		return "", err
	}
	if !s.orgs[key.Org()] {
		return "", zip.ErrForbidden("Sekai-1 Alpha is not enabled for this organization")
	}
	digest := sha256.Sum256([]byte(key.String() + "\x00" + principal.ProjectFrom(ctx)))
	return "tenant_" + hex.EncodeToString(digest[:]), nil
}

// scope never reads caller-supplied tenant headers. The runtime key includes the
// deployment brand, validated organization AND validated project without exposing
// their internal names to a different storage namespace.
func (s *service) scope(ctx context.Context) (string, error) {
	org, err := s.scopeTenant(ctx)
	if err != nil {
		return "", err
	}
	if s.base == "" || s.token == "" {
		return "", zip.Errorf(503, "Sekai-1 Alpha runtime is not configured")
	}
	return org, nil
}

func (s *service) call(ctx context.Context, method, path string, input any) (*object, error) {
	org, err := s.scope(ctx)
	if err != nil {
		return nil, err
	}
	return s.callUpstream(ctx, s.base, s.token, org, method, path, input)
}

func (s *service) callUpstream(ctx context.Context, base, token, org, method, reqPath string, input any) (*object, error) {
	var body []byte
	var err error
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil {
			return nil, zip.ErrBadRequest("invalid JSON input")
		}
	}
	if len(body) > 64<<10 {
		return nil, zip.Errorf(413, "world request exceeds 64 KiB")
	}
	req, err := http.NewRequestWithContext(ctx, method, base+reqPath, bytes.NewReader(body))
	if err != nil {
		return nil, zip.ErrInternal("could not construct world request")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("Content-Type", "application/json")
	response, err := s.http.Do(req)
	if err != nil {
		return nil, zip.Errorf(503, "Sekai-1 Alpha runtime is unavailable")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, zip.Errorf(502, "invalid world runtime response")
	}
	if response.StatusCode >= 400 {
		status := response.StatusCode
		if status >= 500 {
			status = 503
		}
		// Do not relay internal paths, checkpoint identifiers or stack traces.
		return nil, zip.Errorf(status, "Sekai-1 Alpha request failed (HTTP %d)", response.StatusCode)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, zip.Errorf(502, "unexpected world runtime redirect")
	}
	var output object
	if json.Unmarshal(raw, &output) != nil || output == nil {
		return nil, zip.Errorf(502, "invalid world runtime JSON")
	}
	delete(output, "capture") // representation features and artifact hashes stay private
	return &output, nil
}
func path(id string) (string, error) {
	if !safeID.MatchString(id) || strings.Contains(id, "..") {
		return "", zip.ErrBadRequest("invalid world id")
	}
	return "/v1/worlds/" + url.PathEscape(id), nil
}

func validateInference(in *Inference) error {
	if in == nil {
		return zip.ErrBadRequest("inference request body is required")
	}
	if len(in.Belief) == 0 || len(in.Belief) > 256 {
		return zip.ErrBadRequest("belief must contain between 1 and 256 finite probabilities")
	}
	sum := 0.0
	for _, b := range in.Belief {
		if math.IsNaN(b) || math.IsInf(b, 0) || b < 0 {
			return zip.ErrBadRequest("belief entries must be finite and non-negative")
		}
		sum += b
	}
	if math.Abs(sum-1.0) > 1e-9 {
		return zip.ErrBadRequest("belief must sum to 1.0")
	}
	if in.Observation != nil {
		if len(in.Observation) != 2 || in.Observation[0] < 0 || in.Observation[0] >= 64 || in.Observation[1] < 0 || in.Observation[1] >= 256 {
			return zip.ErrBadRequest("observation must be a 2-element [action, outcome] tuple within model bounds")
		}
	}
	if len(in.Policies) == 0 || len(in.Policies) > 256 {
		return zip.ErrBadRequest("policies must contain between 1 and 256 candidate sequences")
	}
	for _, policy := range in.Policies {
		if len(policy) == 0 || len(policy) > 16 {
			return zip.ErrBadRequest("each policy horizon must be between 1 and 16 actions")
		}
		for _, a := range policy {
			if a < 0 || a >= 64 {
				return zip.ErrBadRequest("policy action index out of bounds")
			}
		}
	}
	return nil
}

// models lists alpha aliases only after the private runtime confirms readiness.
func (s *service) models(ctx context.Context, _ *empty) (*Models, error) {
	out, err := s.call(ctx, "GET", "/v1/worlds/models", nil)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(out)
	var models Models
	if json.Unmarshal(raw, &models) != nil {
		return nil, zip.Errorf(502, "invalid model catalog")
	}
	available := []Model{}
	for _, m := range models.Data {
		if m.ID == alpha && m.Stage == "alpha" && m.Domain == "simulation" {
			available = append(available, m)
		}
	}
	return &Models{Data: available}, nil
}

// create starts a simulation with the proprietary alpha model; other domains fail closed.
func (s *service) create(ctx context.Context, in *Create) (*object, error) {
	if in.Model != alpha || in.Domain != "simulation" {
		return nil, zip.ErrBadRequest("only sekai-1-alpha simulations are enabled")
	}
	return s.call(ctx, "POST", "/v1/worlds", in)
}

// world reads a world in the authenticated tenant and project.
func (s *service) world(ctx context.Context, in *World) (*object, error) {
	p, e := path(in.ID)
	if e != nil {
		return nil, e
	}
	return s.call(ctx, "GET", p, nil)
}

// observe updates a local belief from an API feature vector.
func (s *service) observe(ctx context.Context, in *Event) (*object, error) {
	p, e := path(in.ID)
	if e != nil {
		return nil, e
	}
	return s.call(ctx, "POST", p+"/events", in)
}

// plan compares proposed actions without authorizing external execution.
func (s *service) plan(ctx context.Context, in *Plan) (*object, error) {
	p, e := path(in.ID)
	if e != nil {
		return nil, e
	}
	return s.call(ctx, "POST", p+"/plans", in)
}

// rollout predicts latent outcomes for a proposed action sequence.
func (s *service) rollout(ctx context.Context, in *Rollout) (*object, error) {
	p, e := path(in.ID)
	if e != nil {
		return nil, e
	}
	return s.call(ctx, "POST", p+"/rollouts", in)
}

// infer runs finite-state Bayesian belief filtering and expected free-energy
// policy inference under host-owned model and admissibility constraints.
// Results strictly separate inference, advisory recommendation and execution
// authorization, which is invariantly false.
func (s *service) infer(ctx context.Context, in *Inference) (*object, error) {
	p, e := path(in.ID)
	if e != nil {
		return nil, e
	}
	if err := validateInference(in); err != nil {
		return nil, err
	}
	org, err := s.scopeTenant(ctx)
	if err != nil {
		return nil, err
	}
	base, token := s.inferenceBase, s.inferenceToken
	if base == "" {
		base, token = s.base, s.token
	}
	if base == "" || (s.inferenceBase == "" && token == "") {
		return nil, zip.Errorf(503, "world inference runtime is not configured")
	}
	payload := &inferencePayload{
		Belief:      in.Belief,
		Observation: in.Observation,
		Policies:    in.Policies,
	}
	out, err := s.callUpstream(ctx, base, token, org, "POST", p+"/inference", payload)
	if err != nil {
		return nil, err
	}
	// Fail closed if upstream ever claims execution authorization.
	if rawExec, exists := (*out)["execution_authorized"]; exists {
		authorized, ok := rawExec.(bool)
		if !ok || authorized {
			return nil, zip.Errorf(502, "world inference runtime violated non-execution invariant")
		}
	}
	(*out)["execution_authorized"] = false
	if rec, ok := (*out)["recommendation"].(map[string]any); ok {
		if adv, exists := rec["advisory_only"]; exists {
			isAdvisory, ok := adv.(bool)
			if !ok || !isAdvisory {
				return nil, zip.Errorf(502, "world inference recommendation violated advisory invariant")
			}
		}
		rec["advisory_only"] = true
	} else if sel, ok := (*out)["selected"]; ok {
		// Normalize legacy flat response into explicit inference / recommendation separation.
		(*out)["recommendation"] = map[string]any{
			"policy_index":  sel,
			"advisory_only": true,
		}
	}
	return out, nil
}

// belief reads an entity's local belief without sharing model artifacts.
func (s *service) belief(ctx context.Context, in *Belief) (*object, error) {
	p, e := path(in.ID)
	if e != nil {
		return nil, e
	}
	if !safeID.MatchString(in.Entity) {
		return nil, zip.ErrBadRequest("invalid entity id")
	}
	return s.call(ctx, "GET", p+"/beliefs/"+url.PathEscape(in.Entity), nil)
}
