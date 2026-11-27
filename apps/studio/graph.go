package studio

// graph.go — what a graph may say, and how its references resolve.
//
// A graph is data a person (or a model planning for them) wrote, so it is checked
// at the door: ids, ops, the references between steps, the parameters each op
// needs, and that the whole is a DAG — the last by flow/core, which is the
// scheduler the run executes under (durable.go), so the graph that validates here
// is the one that runs.
//
// References are {{path}} in strings and nothing else. A string that is exactly
// one reference takes the value itself; a reference inside longer text is
// rendered as text. There is no expression language: a path names a value, it
// computes nothing.

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	flow "github.com/hanzoai/flow/core"
	"github.com/zap-proto/zip"
)

// The executors a step runs on.
const (
	gateway = "gateway"       // api.hanzo.ai's model API, bought for the org
	gpu     = "studio.render" // a GPU the org linked, through the engine's gpu-jobs lane
	cpu     = "render"        // the platform's render workers, through render-jobs
	native  = "flow.run"      // native flow-core on hanzo-engine; no op routes here yet
)

// op is what Studio knows about one step op: where it runs, the parameters it
// cannot do without, and the meter it is priced on.
type op struct {
	executor string
	needs    []string
	meter    string // the commerce meter (product studio); "" is billed elsewhere
	floor    int64  // cents per unit when the meter authority has no row
}

// known is every op a step may name. One table, read by validation, pricing and
// dispatch alike, so an op cannot be valid and unpriced, or priced and unrunnable.
var known = map[string]op{
	// chat is bought through deps.AI, whose meter debits it as every model call
	// is debited (metered_ai.go); Studio only reserves for it.
	"chat":           {executor: gateway, needs: []string{"prompt"}},
	"image.generate": {executor: gateway, needs: []string{"prompt"}, meter: "image", floor: 8},
	"image.edit":     {executor: gateway, needs: []string{"prompt", "images"}, meter: "image", floor: 8},
	"frame.generate": {executor: gateway, needs: []string{"prompt"}, meter: "image", floor: 8},
	"frame.edit":     {executor: gateway, needs: []string{}, meter: "image", floor: 2},
	"voice.speak":    {executor: gateway, needs: []string{"text"}, meter: "voice", floor: 2},
	"music.generate": {executor: gateway, needs: []string{"prompt"}, meter: "music", floor: 10},
	"video.generate": {executor: gateway, needs: []string{"prompt"}, meter: "video", floor: 40},
	"model.generate": {executor: gateway, needs: []string{"prompt"}, meter: "model", floor: 20},
	// A GPU the org linked is the org's own machine: it renders for nothing.
	"studio.render":      {executor: gpu, needs: []string{"graph"}},
	"render.html":        {executor: cpu, needs: []string{"html", "width", "height"}, meter: "render", floor: 1},
	"render.document":    {executor: cpu, needs: []string{"document"}, meter: "render", floor: 1},
	"render.slides":      {executor: cpu, needs: []string{"document"}, meter: "render", floor: 2},
	"render.composition": {executor: cpu, needs: []string{"html"}, meter: "edit", floor: 5},
	"video.edit":         {executor: cpu, needs: []string{"timeline"}, meter: "edit", floor: 5},
}

// Graph bounds.
const (
	maxSteps = 64
	maxItems = 64
	maxGraph = 256 << 10 // a graph as stored, bytes
	maxInput = 256 << 10 // a run's input as stored, bytes
)

var (
	stepID = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	refRE  = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_.\-]*)\s*\}\}`)
	// whole is a string that is one reference and nothing else.
	whole = regexp.MustCompile(`^\{\{\s*([A-Za-z_][A-Za-z0-9_.\-]*)\s*\}\}$`)
)

// unprocessable is a graph or input that does not validate: 422, naming what.
func unprocessable(format string, args ...any) error {
	return zip.Errorf(http.StatusUnprocessableEntity, format, args...)
}

// check validates g and answers its steps' dependencies in order.
func check(g Graph) (map[string][]string, error) {
	if n := len(g.Steps); n == 0 || n > maxSteps {
		return nil, unprocessable("a graph has 1 to %d steps", maxSteps)
	}
	if b, _ := json.Marshal(g); len(b) > maxGraph {
		return nil, unprocessable("a graph is at most %d bytes", maxGraph)
	}
	ids := map[string]bool{}
	for _, s := range g.Steps {
		if !stepID.MatchString(s.ID) {
			return nil, unprocessable("step id %q: a lowercase letter, then up to 31 lowercase letters, digits or _", s.ID)
		}
		if ids[s.ID] {
			return nil, unprocessable("step id %q is used twice", s.ID)
		}
		ids[s.ID] = true
	}
	deps := map[string][]string{}
	for _, s := range g.Steps {
		o, ok := known[s.Op]
		if !ok {
			return nil, unprocessable("step %s: no op %q", s.ID, s.Op)
		}
		for _, need := range o.needs {
			if v, ok := s.With[need]; !ok || v == nil || v == "" {
				return nil, unprocessable("step %s: %s needs with.%s", s.ID, s.Op, need)
			}
		}
		if s.Each != "" && !whole.MatchString(s.Each) {
			return nil, unprocessable("step %s: each is one {{path}} naming an array", s.ID)
		}
		var refs []string
		walk(s.With, func(p string) { refs = append(refs, p) })
		walk(s.Each, func(p string) { refs = append(refs, p) })
		for _, p := range refs {
			head, rest, _ := strings.Cut(p, ".")
			switch head {
			case "input", "brand":
			case "item", "index":
				if s.Each == "" {
					return nil, unprocessable("step %s: %s is only inside a step with each", s.ID, head)
				}
			case "steps":
				dep, _, _ := strings.Cut(rest, ".")
				if !ids[dep] {
					return nil, unprocessable("step %s: no step %q to read", s.ID, dep)
				}
				if dep == s.ID {
					return nil, unprocessable("step %s reads itself", s.ID)
				}
				if !slices.Contains(deps[s.ID], dep) {
					deps[s.ID] = append(deps[s.ID], dep)
				}
			default:
				return nil, unprocessable("step %s: %q is not input, brand, steps, item or index", s.ID, p)
			}
		}
		slices.Sort(deps[s.ID])
	}
	// The scheduler's own validation: it is the one that runs the graph.
	if _, err := build(g, deps, nil); err != nil {
		return nil, err
	}
	return deps, nil
}

// walk calls fn with every reference path in v.
func walk(v any, fn func(path string)) {
	switch t := v.(type) {
	case string:
		for _, m := range refRE.FindAllStringSubmatch(t, -1) {
			fn(m[1])
		}
	case map[string]any:
		for _, x := range t {
			walk(x, fn)
		}
	case []any:
		for _, x := range t {
			walk(x, fn)
		}
	}
}

// ── flow/core ────────────────────────────────────────────────────────────

// result is what one step produced, as it travels a flow/core edge.
type result struct {
	Text   string   `json:"text,omitempty"`
	JSON   any      `json:"json,omitempty"`
	Assets []string `json:"assets,omitempty"`
}

// PortType is flow/core's tag; every Studio edge carries a step result.
func (result) PortType() string { return flow.AnyType }

// node is one step as flow/core schedules it: an input port per step it reads, one
// output port.
type node struct {
	spec Spec
	deps []string
	run  func(Spec, flow.Inputs) (flow.Outputs, error)
}

func (n node) ID() string { return n.spec.ID }

func (n node) Ins() []flow.Port {
	ps := make([]flow.Port, len(n.deps))
	for i, d := range n.deps {
		ps[i] = flow.Port{Name: d, Type: flow.AnyType}
	}
	return ps
}

func (n node) Outs() []flow.Port { return []flow.Port{{Name: "out", Type: flow.AnyType}} }

func (n node) Run(_ context.Context, in flow.Inputs) (flow.Outputs, error) { return n.run(n.spec, in) }

// build is g as a flow/core graph whose steps run with run, validated by flow/core
// (every port wired, no cycle).
func build(g Graph, deps map[string][]string, run func(Spec, flow.Inputs) (flow.Outputs, error)) (*flow.Graph, error) {
	fg := flow.NewGraph()
	for _, s := range g.Steps {
		if err := fg.Add(node{spec: s, deps: deps[s.ID], run: run}); err != nil {
			return nil, unprocessable("%v", err)
		}
	}
	for _, s := range g.Steps {
		for _, d := range deps[s.ID] {
			if err := fg.Wire(flow.PortRef{Step: d, Port: "out"}, flow.PortRef{Step: s.ID, Port: d}); err != nil {
				return nil, unprocessable("%v", err)
			}
		}
	}
	if err := fg.Validate(); err != nil {
		return nil, unprocessable("%v", err)
	}
	return fg, nil
}

// ── resolution ───────────────────────────────────────────────────────────

// scope is what a step's references read.
type scope struct {
	input map[string]any
	brand map[string]any
	steps map[string]result
	item  any
	index int
	fan   bool
}

// resolve returns v with every reference replaced.
func (sc scope) resolve(v any) (any, error) {
	switch t := v.(type) {
	case string:
		if m := whole.FindStringSubmatch(t); m != nil {
			return sc.lookup(m[1])
		}
		var bad error
		out := refRE.ReplaceAllStringFunc(t, func(s string) string {
			m := refRE.FindStringSubmatch(s)
			x, err := sc.lookup(m[1])
			if err != nil {
				bad = err
				return ""
			}
			return text(x)
		})
		return out, bad
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			r, err := sc.resolve(x)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			r, err := sc.resolve(x)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	}
	return v, nil
}

// lookup reads one path.
func (sc scope) lookup(path string) (any, error) {
	parts := strings.Split(path, ".")
	var cur any
	rest := parts[1:]
	switch parts[0] {
	case "input":
		cur = sc.input
	case "brand":
		cur = sc.brand
	case "item":
		cur = sc.item
	case "index":
		return sc.index, nil
	case "steps":
		if len(parts) < 3 {
			return nil, unprocessable("%s: name a step and what of it (text, json, asset, assets)", path)
		}
		r, ok := sc.steps[parts[1]]
		if !ok {
			return nil, unprocessable("%s: that step has not run", path)
		}
		switch parts[2] {
		case "text":
			cur = r.Text
		case "json":
			cur = r.JSON
		case "asset":
			if len(r.Assets) == 0 {
				return nil, unprocessable("%s: that step made no asset", path)
			}
			cur = r.Assets[0]
		case "assets":
			as := make([]any, len(r.Assets))
			for i, a := range r.Assets {
				as[i] = a
			}
			cur = as
		default:
			return nil, unprocessable("%s: a step has text, json, asset and assets", path)
		}
		rest = parts[3:]
	default:
		return nil, unprocessable("%s: no such root", path)
	}
	for _, p := range rest {
		switch t := cur.(type) {
		case map[string]any:
			cur = t[p]
		case []any:
			i, err := strconv.Atoi(p)
			if err != nil || i < 0 || i >= len(t) {
				return nil, unprocessable("%s: no item %s", path, p)
			}
			cur = t[i]
		default:
			return nil, unprocessable("%s: %s is not inside a value", path, p)
		}
	}
	// A design token's leaf is {$value, $type}; a reference to one means its value.
	if m, ok := cur.(map[string]any); ok {
		if v, ok := m["$value"]; ok {
			cur = v
		}
	}
	if cur == nil {
		return nil, unprocessable("%s is empty", path)
	}
	return cur, nil
}

// text renders a resolved value inside longer text.
func text(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 1e15 {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case bool:
		return strconv.FormatBool(t)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// items is the array a step's each names.
func (sc scope) items(each string) ([]any, error) {
	if each == "" {
		return nil, nil
	}
	v, err := sc.resolve(each)
	if err != nil {
		return nil, err
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, unprocessable("each %s is not an array", each)
	}
	if len(arr) == 0 || len(arr) > maxItems {
		return nil, unprocessable("each %s has %d items; a step fans out to 1..%d", each, len(arr), maxItems)
	}
	return arr, nil
}

// num reads a numeric parameter, def when absent.
func num(with map[string]any, key string, def float64) float64 {
	switch t := with[key].(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil {
			return f
		}
	}
	return def
}

// str reads a string parameter, def when absent.
func str(with map[string]any, key, def string) string {
	if s, ok := with[key].(string); ok && strings.TrimSpace(s) != "" {
		return s
	}
	return def
}

// sizeOf parses "WxH".
func sizeOf(s string) (int, int, bool) {
	w, h, ok := strings.Cut(strings.ToLower(strings.TrimSpace(s)), "x")
	if !ok {
		return 0, 0, false
	}
	x, err1 := strconv.Atoi(w)
	y, err2 := strconv.Atoi(h)
	if err1 != nil || err2 != nil || x <= 0 || y <= 0 {
		return 0, 0, false
	}
	return x, y, true
}

// kindOf is the kind of creation a graph makes, read from its last step's op.
func kindOf(g Graph) string {
	if len(g.Steps) == 0 {
		return "other"
	}
	last := g.Steps[len(g.Steps)-1]
	switch last.Op {
	case "image.generate", "image.edit", "frame.generate":
		return "image"
	case "voice.speak":
		return "voice"
	case "music.generate":
		return "music"
	case "video.generate", "video.edit", "render.composition":
		return "video"
	case "model.generate":
		return "model"
	case "render.document":
		return "document"
	case "render.slides":
		return "slides"
	case "render.html":
		return "poster"
	}
	return "other"
}
