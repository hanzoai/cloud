package train

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
)

// engine is the engine deployment whose training wire clients run on. The wire
// keeps the engine's own paths; only the address callers use is /v1/train.
type engine struct {
	base string
	key  string
	http *http.Client
}

func newEngine() *engine {
	return &engine{
		base: strings.TrimRight(env.Or("ENGINE_UPSTREAM", "http://engine.hanzo.svc.cluster.local:36900"), "/"),
		key:  env.Or("ENGINE_API_KEY", ""),
		http: &http.Client{
			Timeout:       10 * time.Minute,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

const wire = "/v1/train/clients"

// Relay is the engine's answer, passed through as it came.
type Relay struct{ raw json.RawMessage }

// MarshalJSON emits the engine's payload unchanged.
func (r Relay) MarshalJSON() ([]byte, error) {
	if len(r.raw) == 0 {
		return []byte(`{}`), nil
	}
	return r.raw, nil
}

func (e *engine) send(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, e.base+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if e.key != "" {
		req.Header.Set("Authorization", "Bearer "+e.key)
	}
	resp, err := e.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	return resp.StatusCode, data, err
}

// relay runs one engine call and maps its failure to the caller's: the engine
// refusing the platform credential is 503, its 5xx is 502, anything else is its
// own status and reason.
func (e *engine) relay(ctx context.Context, method, path string, body any) (*Relay, error) {
	st, data, err := e.send(ctx, method, path, body)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "engine unavailable")
	}
	switch {
	case st >= 200 && st < 300:
		return &Relay{raw: data}, nil
	case st == http.StatusUnauthorized || st == http.StatusForbidden:
		return nil, zip.Errorf(http.StatusServiceUnavailable, "engine refused the platform credential")
	case st >= 500:
		return nil, zip.Errorf(http.StatusBadGateway, "engine error: %s", reason(data))
	default:
		return nil, zip.Errorf(st, "%s", reason(data))
	}
}

func reason(body []byte) string {
	var d struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &d) == nil && (d.Error != "" || d.Message != "") {
		return d.Error + d.Message
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 512 {
		s = s[:512]
	}
	if s == "" {
		return "engine error"
	}
	return s
}

// plane answers a collection call; an engine that 404s it serves no training
// plane, which is the platform's fault and not the caller's.
func (e *engine) plane(ctx context.Context, method string, body any) (*Relay, error) {
	out, err := e.relay(ctx, method, wire, body)
	var he *zip.HTTPError
	if errors.As(err, &he) && he.Status == http.StatusNotFound {
		return nil, &zip.HTTPError{Status: http.StatusServiceUnavailable, Code: "training_unavailable", Msg: "the engine serves no training plane"}
	}
	return out, err
}

// owned resolves a client id in the caller's org, or the one not-found every
// other org's id also gets.
func (o ops) owned(ctx context.Context, id string) (*store, string, error) {
	st, org, err := o.tenant(ctx)
	if err != nil {
		return nil, "", err
	}
	ok, err := st.owns(ctx, id)
	if err != nil {
		return nil, "", o.fault("client read", org, err)
	}
	if !ok {
		return nil, "", missing("training client")
	}
	return st, org, nil
}

// charge meters a client operation by the engine seconds it took.
func (o ops) charge(ctx context.Context, org, id, op string, took time.Duration) {
	rate := cloud.RateMicros(ctx, "train", "client-second", clientFloorMicros)
	micros := int64(math.Ceil(took.Seconds() * float64(rate)))
	if micros <= 0 {
		return
	}
	o.s.Bill.Record(account.PayerOf("", org), "client", meter.Usage{
		Model:        "client-second",
		AmountMicros: micros,
		RequestID:    fmt.Sprintf("train-%s-%s-%d", id, op, time.Now().UnixNano()),
	})
}

// clientFloorMicros is $0.18 an engine-hour.
const clientFloorMicros = 50

type createClientIn struct {
	// BaseModel is a model the engine loads: a Hugging Face repo id or a local path.
	BaseModel string `json:"base_model"`
	// LoraConfig is the adapter's shape, as the engine's wire takes it.
	LoraConfig json.RawMessage `json:"lora_config,omitempty"`
	// Adaptation is lora, the one mode clients run; its shape is lora_config.
	Adaptation *Adaptation `json:"adaptation,omitempty"`
	// Protect is refused: clients protect nothing yet.
	Protect *Protect `json:"protect,omitempty"`
}

// CreateTrainClient loads base_model on the engine with a LoRA adapter and answers
// the client, loading; poll it until ready. The wire is the engine's own
// create → forward_backward → optim_step → sample → save_weights loop, so a loop
// written against the engine runs here with only the path changed. The
// capability fields are validated as a job's are: adaptation is lora, protect is
// refused, and kai is not a client base. A client holds a model in the memory every
// org's inference shares, so only a SuperAdmin creates one until the engine isolates
// orgs. An org holds at most TRAIN_CLIENTS live clients and the engine at most
// TRAIN_ENGINE_CLIENTS across every org; past that the answer is 503 engine_full.
//
// Example: {"base_model": "HuggingFaceTB/SmolLM2-135M", "lora_config": {"rank": 16, "alpha": 32}}
func (o ops) createClient(ctx context.Context, in *createClientIn) (*Relay, error) {
	if !cloud.Super.Admits(cloud.AuthorityIn(ctx)) {
		return nil, cloud.Super.Refusal()
	}
	st, org, err := o.tenant(ctx)
	if err != nil {
		return nil, err
	}
	spec := Spec{BaseModel: in.BaseModel, Protect: in.Protect}
	if in.Adaptation != nil {
		a := *in.Adaptation
		if a.Rank != 0 || a.Alpha != 0 || len(a.Targets) > 0 {
			return nil, invalid("adaptation: a client's adapter shape is lora_config")
		}
		spec.Adaptation = a
	}
	if spec.Adaptation.Mode == "" {
		spec.Adaptation.Mode = "lora"
	}
	if err := spec.validate(ctx, "clients", st); err != nil {
		return nil, err
	}
	live, err := st.clients(ctx)
	if err != nil {
		return nil, o.fault("client read", org, err)
	}
	if len(live) >= o.s.State.cap {
		return nil, conflict("client_limit", fmt.Sprintf("the org holds %d live clients; delete one first", len(live)))
	}
	all, err := o.s.State.engine.plane(ctx, http.MethodGet, nil)
	if err != nil {
		return nil, err
	}
	var held struct {
		Clients []json.RawMessage `json:"clients"`
	}
	if json.Unmarshal(all.raw, &held) != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "engine answered no client list")
	}
	if len(held.Clients) >= o.s.State.room {
		return nil, &zip.HTTPError{Status: http.StatusServiceUnavailable, Code: "engine_full", Msg: "the engine holds all the training clients it takes; retry later"}
	}
	body := map[string]any{"base_model": spec.BaseModel}
	if len(in.LoraConfig) > 0 {
		body["lora_config"] = in.LoraConfig
	}
	out, err := o.s.State.engine.plane(ctx, http.MethodPost, body)
	if err != nil {
		return nil, err
	}
	var info struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(out.raw, &info) != nil || info.ID == "" {
		return nil, zip.Errorf(http.StatusBadGateway, "engine answered no client id")
	}
	raw, _ := json.Marshal(spec)
	if err := st.addClient(ctx, client{ID: info.ID, Base: spec.BaseModel, Spec: string(raw), By: caller(ctx), Created: unix()}); err != nil {
		_, _, _ = o.s.State.engine.send(context.WithoutCancel(ctx), http.MethodDelete, wire+"/"+info.ID, nil)
		return nil, o.fault("client create", org, err)
	}
	if err := o.ship(org); err != nil {
		return nil, err
	}
	o.s.Log.Info("train client created", "org", org, "client", info.ID, "base", spec.BaseModel)
	return out, nil
}

type clientList struct {
	// Clients are the org's live clients, as the engine reports them.
	Clients []json.RawMessage `json:"clients"`
}

// ListTrainClients answers the org's live clients as the engine reports them. A
// client the engine no longer holds is dropped.
func (o ops) listClients(ctx context.Context, _ *cloud.Unit) (*clientList, error) {
	st, org, err := o.tenant(ctx)
	if err != nil {
		return nil, err
	}
	mine, err := st.clients(ctx)
	if err != nil {
		return nil, o.fault("client read", org, err)
	}
	out := &clientList{Clients: []json.RawMessage{}}
	if len(mine) == 0 {
		return out, nil
	}
	all, err := o.s.State.engine.plane(ctx, http.MethodGet, nil)
	if err != nil {
		return nil, err
	}
	var list struct {
		Clients []json.RawMessage `json:"clients"`
	}
	if err := json.Unmarshal(all.raw, &list); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "engine answered no client list")
	}
	held := map[string]bool{}
	for _, c := range list.Clients {
		var info struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(c, &info) == nil && slices.Contains(mine, info.ID) {
			held[info.ID] = true
			out.Clients = append(out.Clients, c)
		}
	}
	var gone []string
	for _, id := range mine {
		if !held[id] {
			gone = append(gone, id)
		}
	}
	if len(gone) > 0 {
		if err := st.drop(ctx, gone...); err != nil {
			return nil, o.fault("client drop", org, err)
		}
		if err := o.ship(org); err != nil {
			return nil, err
		}
	}
	return out, nil
}

type clientIn struct {
	// ID names the client.
	ID string `json:"id"`
}

// member forwards a call on an owned client; a client the engine no longer holds
// is dropped from the org and answers not found.
func (o ops) member(ctx context.Context, id, method, path string, body any) (*Relay, error) {
	st, org, err := o.owned(ctx, id)
	if err != nil {
		return nil, err
	}
	began := time.Now()
	out, err := o.s.State.engine.relay(ctx, method, wire+"/"+id+path, body)
	if path != "" && err == nil {
		o.charge(ctx, org, id, strings.TrimPrefix(path, "/"), time.Since(began))
	}
	var he *zip.HTTPError
	if errors.As(err, &he) && he.Status == http.StatusNotFound {
		if derr := st.drop(ctx, id); derr != nil {
			return nil, o.fault("client drop", org, derr)
		}
		if serr := o.ship(org); serr != nil {
			return nil, serr
		}
		return nil, missing("training client")
	}
	return out, err
}

// GetTrainClient answers one of the org's clients with its loss history.
func (o ops) getClient(ctx context.Context, in *clientIn) (*Relay, error) {
	return o.member(ctx, in.ID, http.MethodGet, "", nil)
}

// DeleteTrainClient drops one of the org's clients and frees its memory on the engine.
func (o ops) deleteClient(ctx context.Context, in *clientIn) (*Relay, error) {
	st, org, err := o.owned(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	out, err := o.s.State.engine.relay(ctx, http.MethodDelete, wire+"/"+in.ID, nil)
	var he *zip.HTTPError
	if err != nil && !(errors.As(err, &he) && he.Status == http.StatusNotFound) {
		return nil, err
	}
	if err := st.drop(ctx, in.ID); err != nil {
		return nil, o.fault("client drop", org, err)
	}
	if err := o.ship(org); err != nil {
		return nil, err
	}
	if out == nil {
		out = &Relay{raw: json.RawMessage(fmt.Sprintf(`{"id":%q,"deleted":true}`, in.ID))}
	}
	return out, nil
}

// op forwards one training operation of an owned client and meters its time.
func (o ops) op(ctx context.Context, id, verb string, body any) (*Relay, error) {
	return o.member(ctx, id, http.MethodPost, "/"+verb, body)
}

type forwardIn struct {
	// ID names the client.
	ID string `json:"id"`
	// Data are the examples: {prompt, completion} text, or tokenized datums.
	Data []json.RawMessage `json:"data"`
}

// ForwardBackwardTrainClient accumulates the gradients of a batch on the client.
//
// Example: {"id": "tc-…", "data": [{"prompt": "2+2=", "completion": "4"}]}
func (o ops) forwardBackward(ctx context.Context, in *forwardIn) (*Relay, error) {
	return o.op(ctx, in.ID, "forward_backward", map[string]any{"data": in.Data})
}

type optimIn struct {
	// ID names the client.
	ID string `json:"id"`
	// AdamParams are the optimizer's parameters for this step.
	AdamParams json.RawMessage `json:"adam_params,omitempty"`
}

// OptimStepTrainClient applies the optimizer to the accumulated gradients.
func (o ops) optimStep(ctx context.Context, in *optimIn) (*Relay, error) {
	body := map[string]any{}
	if len(in.AdamParams) > 0 {
		body["adam_params"] = in.AdamParams
	}
	return o.op(ctx, in.ID, "optim_step", body)
}

type sampleIn struct {
	// ID names the client.
	ID string `json:"id"`
	// Prompt is a text prompt; exactly one of prompt and tokens.
	Prompt *string `json:"prompt,omitempty"`
	// Tokens is a tokenized prompt.
	Tokens []uint32 `json:"tokens,omitempty"`
	// SamplingParams are the decoding parameters.
	SamplingParams json.RawMessage `json:"sampling_params,omitempty"`
	// NumSamples is how many sequences to decode.
	NumSamples *int `json:"num_samples,omitempty"`
}

// SampleTrainClient decodes from the client's current weights.
func (o ops) sample(ctx context.Context, in *sampleIn) (*Relay, error) {
	body := map[string]any{}
	if in.Prompt != nil {
		body["prompt"] = *in.Prompt
	}
	if in.Tokens != nil {
		body["tokens"] = in.Tokens
	}
	if len(in.SamplingParams) > 0 {
		body["sampling_params"] = in.SamplingParams
	}
	if in.NumSamples != nil {
		body["num_samples"] = *in.NumSamples
	}
	return o.op(ctx, in.ID, "sample", body)
}

type saveIn struct {
	// ID names the client.
	ID string `json:"id"`
	// Name is the adapter's name.
	Name string `json:"name"`
	// Output is lora, the one kind a client writes.
	Output *Output `json:"output,omitempty"`
}

// SaveWeightsTrainClient writes the client's adapter on the engine.
func (o ops) saveWeights(ctx context.Context, in *saveIn) (*Relay, error) {
	if in.Output != nil && in.Output.Kind != "lora" {
		return nil, refuse("unsupported_output", "a client writes a lora adapter", []string{"lora"})
	}
	return o.op(ctx, in.ID, "save_weights", map[string]any{"name": in.Name})
}
