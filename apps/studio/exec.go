package studio

// exec.go — what a part does on each executor.
//
// THE GATEWAY. A model step is bought from api.hanzo.ai's model API for the run's
// org and person. Chat goes through deps.AI, whose meter debits it as every model
// call is debited. Media — images, speech, music, video, 3D — goes over the plane
// to the ai app (client.AIMedia), which serves it as a call this deployment's meter
// covers, so Studio prices it and debits it once (money.go).
//
// THE LANES. A render or a GPU step becomes a job whose inputs are signed GET URLs
// for exactly the assets it names and whose output is a signed PUT URL for the one
// asset it will make, minted under the org the run belongs to (lane.go).
//
// THE NATIVE EXECUTOR (flow.run) is the seam a template moves through when its
// steps run as native flow-core on hanzo-engine in GPU-resident mode. No op routes
// to it today: the ops table names its executor per op, so moving one op is a
// one-line change there plus a case below.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/outbound"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/aimodel"
	"github.com/hanzoai/cloud/sdk/infer"
)

// The default models, one per kind of work. A step's with.model overrides.
var models = map[string]string{
	"chat":           aimodel.DefaultModel,
	"image.generate": "iro",
	"image.edit":     "iro",
	"frame.generate": "iro",
	"frame.edit":     "iro",
	"voice.speak":    "koe",
	"music.generate": "uta",
	"video.generate": "koma",
	"model.generate": "mori",
}

// allowed are the models a media step may name, per op: the ones its meter's
// price was set for. A step naming another is refused before anything is bought,
// so a graph cannot buy an expensive model at a cheap one's price.
var allowed = map[string][]string{
	"image.generate": {
		"iro", "iro-1", "zen-image", "zen3-image", "enso-image", "koma",
		"flux-3", "flux3", "qwen-image", "qwen-image-edit", "qwen-image-edit-2511", "z-image-turbo", "Qwen/Qwen-Image", "Qwen/Qwen-Image-Edit-2511",
		"black-forest-labs/flux.2-klein-4b", "black-forest-labs/flux-3-image-klein",
		"black-forest-labs/flux.1-dev", "black-forest-labs/flux.1-schnell", "black-forest-labs/flux.1-kontext-dev",
		"google/gemini-3.1-flash-image", "google/gemini-3-pro-image",
		"openai/gpt-5-image", "openai/gpt-5-image-mini", "openai/gpt-5.4-image-2",
		"gemini-3.1-flash-image", "gemini-3-pro-image",
		"gpt-5-image", "gpt-5-image-mini", "gpt-5.4-image-2",
	},
	"image.edit": {
		"iro", "iro-1", "zen-image", "zen3-image", "enso-image", "koma",
		"flux-3", "flux3", "qwen-image", "qwen-image-edit", "qwen-image-edit-2511", "z-image-turbo", "Qwen/Qwen-Image", "Qwen/Qwen-Image-Edit-2511",
		"black-forest-labs/flux.1-kontext-dev", "black-forest-labs/flux.2-klein-4b", "black-forest-labs/flux-3-image-klein",
		"google/gemini-3.1-flash-image", "google/gemini-3-pro-image",
		"openai/gpt-5-image", "openai/gpt-5.4-image-2",
		"gemini-3.1-flash-image", "gemini-3-pro-image",
		"gpt-5-image", "gpt-5.4-image-2",
	},
	"frame.generate": {
		"iro", "iro-1", "zen-image", "zen3-image", "enso-image", "koma",
		"flux-3", "flux3", "qwen-image", "qwen-image-edit", "qwen-image-edit-2511", "z-image-turbo", "Qwen/Qwen-Image", "Qwen/Qwen-Image-Edit-2511",
		"black-forest-labs/flux.2-klein-4b", "black-forest-labs/flux-3-image-klein",
		"black-forest-labs/flux.1-dev", "black-forest-labs/flux.1-schnell", "black-forest-labs/flux.1-kontext-dev",
		"google/gemini-3.1-flash-image", "google/gemini-3-pro-image",
		"openai/gpt-5-image", "openai/gpt-5-image-mini", "openai/gpt-5.4-image-2",
		"gemini-3.1-flash-image", "gemini-3-pro-image",
		"gpt-5-image", "gpt-5-image-mini", "gpt-5.4-image-2",
	},
	"frame.edit": {
		"iro", "iro-1", "zen-image", "zen3-image", "enso-image", "koma",
		"flux-3", "flux3", "qwen-image", "qwen-image-edit", "qwen-image-edit-2511", "z-image-turbo", "Qwen/Qwen-Image", "Qwen/Qwen-Image-Edit-2511",
		"black-forest-labs/flux.2-klein-4b", "black-forest-labs/flux-3-image-klein",
		"black-forest-labs/flux.1-dev", "black-forest-labs/flux.1-schnell", "black-forest-labs/flux.1-kontext-dev",
		"google/gemini-3.1-flash-image", "google/gemini-3-pro-image",
		"openai/gpt-5-image", "openai/gpt-5-image-mini", "openai/gpt-5.4-image-2",
		"gemini-3.1-flash-image", "gemini-3-pro-image",
		"gpt-5-image", "gpt-5-image-mini", "gpt-5.4-image-2",
	},
	"voice.speak":    {"koe", "koe-1", "zen-voice", "zen3-tts", "zen-voice-mini", "oto", "oto-1", "openai/gpt-audio", "openai/gpt-audio-mini", "gpt-audio", "gpt-audio-mini"},
	"music.generate": {"uta", "uta-1", "zen-music", "zen-musician", "oto", "oto-1", "zen-foley"},
	"video.generate": {"koma", "koma-1", "zen-video", "zen-director", "nvidia/cosmos-predict2-7b"},
	"model.generate": {"mori", "mori-1", "zen-3d", "microsoft/trellis"},
}

// assetID is a Library item id as it appears in a step's parameters.
var assetID = regexp.MustCompile(`^lib_[A-Za-z0-9_-]{6,64}$`)

// assetURL is a Library item referenced inside HTML.
var assetURL = regexp.MustCompile(`asset://(lib_[A-Za-z0-9_-]{6,64})`)

// gateway runs one model step.
func (s *state) gateway(ctx context.Context, r runRow, p Part) (result, int64, error) {
	with := p.With
	model := str(with, "model", models[p.Op])
	if list, ok := allowed[p.Op]; ok && !slices.Contains(list, model) {
		return result{}, 0, fatal("%s takes the models %s; %q is not one", p.Op, strings.Join(list, ", "), model)
	}
	switch p.Op {
	case "chat":
		return s.chat(ctx, r, p, model)
	case "image.generate", "image.edit", "frame.generate", "frame.edit":
		n := int(min(max(num(with, "n", 1), 1), 4))
		rawPrompt := str(with, "prompt", "")
		prompt := noDiffusionText(rawPrompt)
		path := "/v1/images/generations"
		if p.Op == "frame.generate" {
			path = "/v1/frame/generations"
			prompt = rawPrompt
		} else if p.Op == "frame.edit" {
			path = "/v1/frame/edits"
			prompt = rawPrompt
		}
		body := map[string]any{"model": model, "prompt": prompt, "n": n,
			"size": str(with, "size", "1024x1024"), "response_format": "b64_json"}
		for _, k := range []string{
			"headline", "accent", "eyebrow", "badge", "subhead", "subline",
			"pills", "layout", "theme", "terminal", "terminal_box", "copy_box",
			"layers", "layer_patches", "instruction", "edit_background", "background_b64", "image",
		} {
			if v, ok := with[k]; ok {
				body[k] = v
			}
		}
		if p.Op == "image.edit" {
			path = "/v1/images/edits"
			images, err := s.dataURLs(ctx, r, with["images"])
			if err != nil {
				return result{}, 0, err
			}
			body["images"] = images
		}
		st, out, err := s.media(ctx, r, http.MethodPost, path, body)
		if err != nil {
			return result{}, 0, err
		}
		if st/100 != 2 {
			return result{}, 0, refusal(st, out)
		}
		var resp struct {
			Data []struct {
				B64 string `json:"b64_json"`
				URL string `json:"url"`
			} `json:"data"`
		}
		if err := json.Unmarshal(out, &resp); err != nil || len(resp.Data) == 0 {
			return result{}, 0, fatal("the model answered no image")
		}
		var res result
		for i, d := range resp.Data {
			var data []byte
			switch {
			case d.B64 != "":
				if data, err = base64.StdEncoding.DecodeString(d.B64); err != nil {
					return result{}, 0, fatal("the model's image does not decode")
				}
			case d.URL != "":
				if data, err = fetch(ctx, d.URL); err != nil {
					return result{}, 0, err
				}
			}
			a, err := s.keep(ctx, r, p, i, data)
			if err != nil {
				return result{}, 0, err
			}
			res.Assets = append(res.Assets, a.ID)
		}
		return res, s.rate(ctx, p.Op) * int64(len(res.Assets)), nil
	case "voice.speak":
		text := str(with, "text", "")
		format := str(with, "format", "mp3")
		body := map[string]any{"model": model, "input": text, "voice": str(with, "voice", "alloy"), "response_format": format}
		st, out, err := s.media(ctx, r, http.MethodPost, "/v1/audio/speech", body)
		if err != nil {
			return result{}, 0, err
		}
		if st/100 != 2 {
			return result{}, 0, refusal(st, out)
		}
		data, err := audioOf(ctx, out)
		if err != nil {
			return result{}, 0, err
		}
		a, err := s.keep(ctx, r, p, 0, data)
		if err != nil {
			return result{}, 0, err
		}
		units := int64(max((len([]rune(text))+999)/1000, 1))
		return result{Assets: []string{a.ID}}, s.rate(ctx, p.Op) * units, nil
	case "music.generate":
		body := map[string]any{"model": model, "prompt": str(with, "prompt", "")}
		if sec := num(with, "seconds", 0); sec > 0 {
			body["seconds"] = sec
		}
		st, out, err := s.media(ctx, r, http.MethodPost, "/v1/audio/music", body)
		if err != nil {
			return result{}, 0, err
		}
		if st/100 != 2 {
			return result{}, 0, refusal(st, out)
		}
		data, err := audioOf(ctx, out)
		if err != nil {
			return result{}, 0, err
		}
		a, err := s.keep(ctx, r, p, 0, data)
		if err != nil {
			return result{}, 0, err
		}
		return result{Assets: []string{a.ID}}, s.rate(ctx, p.Op), nil
	case "video.generate", "model.generate":
		root := "/v1/videos"
		body := map[string]any{"model": model, "prompt": str(with, "prompt", "")}
		if p.Op == "video.generate" {
			if sec := num(with, "seconds", 0); sec > 0 {
				body["seconds"] = int(sec)
			}
			if sz := str(with, "size", ""); sz != "" {
				body["size"] = sz
			}
		} else {
			root = "/v1/3d"
		}
		if img, ok := with["image"].(string); ok && img != "" {
			urls, err := s.dataURLs(ctx, r, []any{img})
			if err != nil {
				return result{}, 0, err
			}
			body["image"] = urls[0]
		}
		data, err := s.async(ctx, r, p, root, body)
		if err != nil {
			return result{}, 0, err
		}
		a, err := s.keep(ctx, r, p, 0, data)
		if err != nil {
			return result{}, 0, err
		}
		return result{Assets: []string{a.ID}}, s.rate(ctx, p.Op), nil
	}
	return result{}, 0, fatal("op %s is not a gateway op", p.Op)
}

// chat runs one completion as the run's org, metered by deps.AI.
func (s *state) chat(ctx context.Context, r runRow, p Part, model string) (result, int64, error) {
	if s.ai == nil {
		return result{}, 0, fatal("this deployment has no model client")
	}
	with := p.With
	prompt := str(with, "prompt", "")
	asJSON, _ := with["json"].(bool)
	var msgs []infer.ChatMessage
	system := str(with, "system", "")
	if asJSON {
		system = strings.TrimSpace(system + "\n\nAnswer with one JSON value and nothing else: no prose, no code fence.")
	}
	if system != "" {
		msgs = append(msgs, infer.ChatMessage{Role: "system", Content: system})
	}
	msgs = append(msgs, infer.ChatMessage{Role: "user", Content: prompt})
	pay := payerOf(r)
	req := &infer.ChatRequest{
		Model: model, Prompt: prompt, Messages: msgs, MaxTokens: int(min(max(num(with, "max_tokens", 4096), 64), 32768)),
		Org: p.Org, Payer: pay.wallet(), Project: pay.Project, RunID: p.Run,
	}
	resp, err := s.ai.ChatCompletion(s.as(ctx, r), req)
	if err != nil {
		if st := status(err); st == http.StatusPaymentRequired {
			return result{}, 0, fatal("the wallet cannot pay for the model call")
		}
		return result{}, 0, err
	}
	out := result{Text: resp.Content}
	if asJSON {
		v, err := parseJSON(resp.Content)
		if err != nil {
			return result{}, 0, fmt.Errorf("the model did not answer JSON: %w", err)
		}
		out.JSON = v
	}
	return out, billedCents(resp), nil
}

// parseJSON reads the first JSON value in a model's answer, past any fence.
func parseJSON(s string) (any, error) {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "{["); i > 0 {
		s = s[i:]
	}
	dec := json.NewDecoder(strings.NewReader(s))
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// as is ctx stated as the run's person in the run's org: who a model call is for.
func (s *state) as(ctx context.Context, r runRow) context.Context {
	org, _ := tenantOf(ctx)
	pay := payerOf(r)
	return zipCaller(ctx, org, r.Creator, pay.Project)
}

// media makes one media call through the ai app, for the run's org and person.
func (s *state) media(ctx context.Context, r runRow, method, path string, body any) (int, []byte, error) {
	var req []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		req = b
	}
	out, err := s.buy(s.as(ctx, r), &client.MediaIn{Method: method, Path: path, Request: req})
	if err != nil {
		return 0, nil, err
	}
	return out.Status, out.Body, nil
}

// async creates a video or a 3D model, follows the job, and reads what it made.
func (s *state) async(ctx context.Context, r runRow, p Part, root string, body any) ([]byte, error) {
	st, out, err := s.media(ctx, r, http.MethodPost, root+"/generations", body)
	if err != nil {
		return nil, err
	}
	if st/100 != 2 {
		return nil, refusal(st, out)
	}
	var job struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &job); err != nil || job.ID == "" || !safeID.MatchString(job.ID) {
		return nil, fatal("the model answered no job")
	}
	deadline := time.Now().Add(18 * time.Minute)
	for {
		switch strings.ToLower(job.Status) {
		case "completed", "succeeded", "done":
			st, data, err := s.media(ctx, r, http.MethodGet, root+"/"+job.ID+"/content", nil)
			if err != nil {
				return nil, err
			}
			if st/100 != 2 || len(data) == 0 {
				return nil, refusal(st, data)
			}
			return data, nil
		case "failed", "error", "cancelled", "canceled":
			return nil, fatal("the model's job %s", strings.ToLower(job.Status))
		}
		if time.Now().After(deadline) {
			return nil, fatal("the model's job did not finish in time")
		}
		s.progress(ctx, p, 0.2, "generating")
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
		}
		st, out, err := s.media(ctx, r, http.MethodGet, root+"/"+job.ID, nil)
		if err != nil {
			continue
		}
		if st/100 != 2 {
			return nil, refusal(st, out)
		}
		_ = json.Unmarshal(out, &job)
	}
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9_\-]{1,128}$`)

// audioOf is the audio in a speech or music answer: the bytes themselves, or a
// JSON answer carrying them as base64 or a URL.
func audioOf(ctx context.Context, out []byte) ([]byte, error) {
	if m := detect(out); strings.HasPrefix(m, "audio/") || strings.HasPrefix(m, "video/") {
		return out, nil
	}
	var j struct {
		Audio string `json:"audio"`
		B64   string `json:"b64_json"`
		URL   string `json:"url"`
		Data  []struct {
			B64 string `json:"b64_json"`
			URL string `json:"url"`
		} `json:"data"`
	}
	if json.Unmarshal(out, &j) != nil {
		return nil, fatal("the model answered no audio")
	}
	if len(j.Data) > 0 {
		j.B64, j.URL = j.Data[0].B64, j.Data[0].URL
	}
	switch {
	case j.Audio != "":
		return base64.StdEncoding.DecodeString(j.Audio)
	case j.B64 != "":
		return base64.StdEncoding.DecodeString(j.B64)
	case j.URL != "":
		return fetch(ctx, j.URL)
	}
	return nil, fatal("the model answered no audio")
}

// fetch reads a file a model's answer points at: https only, bounded.
func fetch(ctx context.Context, raw string) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return nil, fatal("the model's file is not at an https address")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := files.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("the model's file answered %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxFetch))
}

// files reads an address a model's answer named, which is not ours to trust: it
// is hanzoai/outbound's client, so the address is judged at the dial on the IP
// connected and every redirect is dialled the same way.
var files = outbound.Client(outbound.Options{Timeout: 2 * time.Minute, Redirects: 5})

// refusal is a model API answer that was not a success, as a part's error. A
// refusal that names money or a bad request will not change on a retry.
func refusal(st int, body []byte) error {
	msg := strings.TrimSpace(string(body))
	var e struct {
		Error any    `json:"error"`
		Msg   string `json:"msg"`
	}
	if json.Unmarshal(body, &e) == nil {
		switch t := e.Error.(type) {
		case string:
			msg = t
		case map[string]any:
			if m, ok := t["message"].(string); ok {
				msg = m
			}
		}
		if e.Msg != "" {
			msg = e.Msg
		}
	}
	msg = firstLine(msg)
	if st == http.StatusTooManyRequests || st >= 500 {
		return fmt.Errorf("the model API answered %d: %s", st, msg)
	}
	return fatal("the model API answered %d: %s", st, msg)
}

// dataURLs reads image items the run's person may read as data: URLs for a model.
func (s *state) dataURLs(ctx context.Context, r runRow, v any) ([]string, error) {
	var ids []string
	switch t := v.(type) {
	case string:
		ids = []string{t}
	case []any:
		for _, x := range t {
			if id, ok := x.(string); ok {
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 || len(ids) > 8 {
		return nil, fatal("images is 1 to 8 image items")
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		it, err := s.readable(s.as(ctx, r), id)
		if err != nil {
			return nil, fatal("item %s is not one this run may read", id)
		}
		if it.Kind != "image" {
			return nil, fatal("item %s is not an image", id)
		}
		data, err := s.read(ctx, it)
		if err != nil {
			return nil, err
		}
		out[i] = "data:" + it.Mime + ";base64," + base64.StdEncoding.EncodeToString(data)
	}
	return out, nil
}

// ── lane jobs ────────────────────────────────────────────────────────────

// output is the file a render job makes: its media type, by op and format.
func output(p Part) (string, error) {
	f := strings.ToLower(str(p.With, "format", ""))
	switch p.Op {
	case "render.html":
		switch or(f, "png") {
		case "png":
			return "image/png", nil
		case "jpeg", "jpg":
			return "image/jpeg", nil
		case "webp":
			return "image/webp", nil
		case "pdf":
			return "application/pdf", nil
		}
	case "render.document":
		switch or(f, "pdf") {
		case "pdf":
			return "application/pdf", nil
		case "docx":
			return "application/vnd.openxmlformats-officedocument.wordprocessingml.document", nil
		case "md":
			return "text/markdown", nil
		}
	case "render.slides":
		switch or(f, "pptx") {
		case "pptx":
			return "application/vnd.openxmlformats-officedocument.presentationml.presentation", nil
		case "pdf":
			return "application/pdf", nil
		}
	case "render.composition", "video.edit":
		if p.Op == "video.edit" {
			if tl, ok := p.With["timeline"].(map[string]any); ok {
				f = strings.ToLower(str(tl, "format", f))
			}
		}
		switch or(f, "mp4") {
		case "mp4":
			return "video/mp4", nil
		case "webm":
			return "video/webm", nil
		}
	case "studio.render":
		return "", nil
	}
	return "", fatal("%s does not make %q", p.Op, f)
}

// jobInput builds a lane job for one part: its parameters, and a signed GET URL
// for every Library item it names that the run's person may read. It names no
// output: the worker asks for its upload once the file exists (door, output).
func (s *state) jobInput(ctx context.Context, r runRow, p Part) (map[string]any, error) {
	org, _ := tenantOf(ctx)
	with, err := s.materialize(ctx, r, p)
	if err != nil {
		return nil, err
	}
	as := s.as(ctx, r)
	inputs := map[string]any{}
	var bad error
	collect := func(id string) {
		if _, done := inputs[id]; done || bad != nil {
			return
		}
		it, err := s.readable(as, id)
		if err != nil {
			bad = fatal("item %s is not one this run may read", id)
			return
		}
		inputs[id] = map[string]any{"url": it.URL, "mime": it.Mime, "name": it.Name, "bytes": it.Bytes}
	}
	var visit func(v any)
	visit = func(v any) {
		switch t := v.(type) {
		case string:
			if assetID.MatchString(t) {
				collect(t)
			}
			for _, m := range assetURL.FindAllStringSubmatch(t, -1) {
				collect(m[1])
			}
		case map[string]any:
			for _, x := range t {
				visit(x)
			}
		case []any:
			for _, x := range t {
				visit(x)
			}
		}
	}
	visit(with)
	if bad != nil {
		return nil, bad
	}
	in := map[string]any{"op": p.Op, "with": with, "inputs": inputs, "org": org, "run": p.Run, "step": p.Step, "item": p.Item,
		"limits": map[string]any{"seconds": int(cpuClose.Seconds()), "bytes": maxFetch}}
	if known[p.Op].executor == gpu {
		in["prompt"] = with["graph"]
	} else if _, err := output(p); err != nil {
		return nil, err
	}
	return in, nil
}

// materialize is a part's parameters with what a worker needs inlined: a document
// named by id becomes its IR and title, and a brand becomes its tokens.
func (s *state) materialize(ctx context.Context, r runRow, p Part) (map[string]any, error) {
	with := map[string]any{}
	for k, v := range p.With {
		with[k] = v
	}
	if p.Op == "render.document" || p.Op == "render.slides" {
		switch d := with["document"].(type) {
		case string:
			doc, err := s.document(ctx, r.Creator, d)
			if err != nil {
				return nil, fatal("document %s is not one this run may read", d)
			}
			b, _ := json.Marshal(doc.IR)
			var ir map[string]any
			_ = json.Unmarshal(b, &ir)
			with["ir"], with["title"] = ir, or(str(with, "title", ""), doc.Title)
		case map[string]any:
			with["ir"] = d
		default:
			return nil, fatal("%s: document is a document id or an IR", p.Op)
		}
		delete(with, "document")
	}
	if _, ok := with["tokens"]; !ok {
		var tokens map[string]any
		_ = json.Unmarshal([]byte(r.Tokens), &tokens)
		if len(tokens) > 0 {
			with["tokens"] = tokens
		}
	}
	if b, _ := json.Marshal(with); len(b) > 1<<20 {
		return nil, fatal("a render job's parameters are at most 1 MiB")
	}
	return with, nil
}

// cost is what one lane part costs, from what it made.
func (s *state) cost(ctx context.Context, p Part, out result) (int64, error) {
	if known[p.Op].meter == "" {
		return 0, nil
	}
	return s.rate(ctx, p.Op), nil
}

// zipCaller states a caller on ctx for a call this process makes for a run.
func zipCaller(ctx context.Context, org, user, project string) context.Context {
	return zip.WithCaller(ctx, zip.Caller{Org: org, User: user, Project: project})
}

// billedCents is what the meter charged the run's payer for a completion — the
// model API's price for the answer (ChatResponse.Billed) — in whole cents, rounded
// up. A completion nobody priced was charged nothing.
func billedCents(resp *infer.ChatResponse) int64 {
	if resp.Billed == nil || *resp.Billed <= 0 {
		return 0
	}
	return (*resp.Billed + 9_999_999) / 10_000_000
}

var (
	reStudioQuotedCopy = regexp.MustCompile(`(?i)\s*\b(for|saying|reading|titled|with\s+text|with\s+copy|labeled)\s+["'“”‘’][^"'“”‘’]+["'“”‘’]`)
	reStudioQuotes     = regexp.MustCompile(`["'“”‘’][^"'“”‘’]{2,}["'“”‘’]`)
	reStudioDiff       = regexp.MustCompile(`(?i)\bshowing\s+a\s+(small\s+)?(diff|code\s+snippet|terminal\s+output)\b`)
)

func noDiffusionText(p string) string {
	s := strings.TrimSpace(p)
	if s == "" {
		return s
	}
	s = reStudioQuotedCopy.ReplaceAllString(s, "")
	s = reStudioQuotes.ReplaceAllString(s, "")
	s = reStudioDiff.ReplaceAllString(s, "with a blank dark obsidian glass surface")
	s = strings.TrimRight(strings.TrimSpace(s), ",.;:")
	if !strings.Contains(strings.ToLower(s), "no text") {
		s += ". Strictly no text, no letters, no words, no numbers, no typography, no code, no glyphs, no watermarks; leave all surfaces blank of text."
	}
	return s
}
