package train

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/zap-proto/zip"
)

// Spec is what a job asks for (HIP-1333 §3). A client's create reads its
// Adaptation and Protect.
type Spec struct {
	// BaseModel is the base the capability is taught to: `kai`, or a model the engine
	// loads for a client.
	BaseModel string `json:"base_model"`
	// Revision pins the base. For kai: a finished run's name on the executor, or
	// `hub:<repo>@<rev>`.
	Revision string `json:"revision"`
	// Dataset is what the job trains on.
	Dataset *Dataset `json:"dataset,omitempty"`
	// Objective is the loss and the terms beside it.
	Objective Objective `json:"objective"`
	// Adaptation is how the update is parameterized.
	Adaptation Adaptation `json:"adaptation"`
	// Protect names what the base already does that must hold, and the budget.
	Protect *Protect `json:"protect,omitempty"`
	// Resources places and bounds the run.
	Resources Resources `json:"resources"`
	// Evaluation names the target suites the result reports.
	Evaluation *Evaluation `json:"evaluation,omitempty"`
	// Output is what the job produces.
	Output Output `json:"output"`
	// From is an artifact the job starts from, by sha256.
	From string `json:"from,omitempty"`
}

// Dataset is where the rows are: `stage:<name>`, `hf://<repo>[@<rev>]`,
// `s3://<bucket>/<key>` or `artifact:<sha256>` (JSON Lines rows the org uploaded
// through POST /v1/train/artifacts).
type Dataset struct {
	// URI names the data.
	URI string `json:"uri"`
	// Splits maps a role (train, validation) to the dataset's own split name.
	Splits map[string]string `json:"splits,omitempty"`
}

// Objective is the answer loss and the terms added to it.
type Objective struct {
	// Loss is `cross_entropy`.
	Loss string `json:"loss"`
	// Terms are added to each row's loss.
	Terms []Term `json:"terms,omitempty"`
}

// Term is one addend: `pairwise_margin`, `hard_margin` or `consistency`.
type Term struct {
	// Kind names the term.
	Kind string `json:"kind"`
	// Weight scales it; 0 is off.
	Weight float64 `json:"weight"`
	// Margin is a hinge's margin, in logits.
	Margin float64 `json:"margin,omitempty"`
}

// Adaptation is how the trained parameters are represented.
type Adaptation struct {
	// Mode is full, readout (the head alone, over the frozen base), lora, qlora,
	// basis or auto.
	Mode string `json:"mode"`
	// Rank is an adapter's rank, or how many of a basis's directions are used.
	Rank int `json:"rank,omitempty"`
	// Alpha is an adapter's scale.
	Alpha float64 `json:"alpha,omitempty"`
	// Targets are the modules an adapter or a basis attaches to.
	Targets []string `json:"targets,omitempty"`
	// Basis is a basis artifact, by sha256 or `basis://<base>/<name>`.
	Basis string `json:"basis,omitempty"`
	// Sources are lora artifacts (sha256) a basis is built from, in place of Basis.
	Sources []string `json:"sources,omitempty"`
	// ResidualRank is the rank of an orthonormal residual learned beside a basis's
	// coefficients; 0 trains the coefficients alone.
	ResidualRank int `json:"residual_rank,omitempty"`
	// Chose is what auto resolved to and why; absent unless auto was asked.
	Chose *Chose `json:"chose,omitempty"`
}

// Chose is auto's answer.
type Chose struct {
	// Mode is the mode the job runs.
	Mode string `json:"mode"`
	// Why says what decided it.
	Why string `json:"why"`
}

// Protect is what the trained parameters must not damage, whatever the adaptation.
type Protect struct {
	// Suites are capabilities the base already has, by suite.
	Suites []string `json:"suites,omitempty"`
	// Capabilities are capability artifacts, by sha256.
	Capabilities []string `json:"capabilities,omitempty"`
	// Projection projects every update off the protected directions — the suites'
	// gradient subspace at the base and each named capability's subspace — by its
	// strength; absent is no projection.
	Projection *Projection `json:"projection,omitempty"`
	// Distillation adds KL to the base's answers on a preservation set drawn from
	// the suites.
	Distillation bool `json:"distillation,omitempty"`
	// Budget bounds the regression per protected suite.
	Budget *Budget `json:"budget,omitempty"`
}

// Projection is g' = g − strength·P g, P the projector onto the protected directions.
type Projection struct {
	// Strength is λ in [0, 1]; absent is 1, the whole of P g removed.
	Strength *float64 `json:"strength,omitempty"`
}

// Budget bounds a protected suite's regression: accuracy down at most Accuracy,
// expected calibration error up at most ECE.
type Budget struct {
	// Accuracy is the largest accuracy drop allowed.
	Accuracy float64 `json:"accuracy"`
	// ECE is the largest calibration-error rise allowed.
	ECE float64 `json:"ece"`
}

// Resources places and bounds a run.
type Resources struct {
	// Machines are the org's linked machines by name; the first leads. When it
	// names none, Hanzo's executor runs the job and its compute is charged by the
	// device-second.
	Machines []string `json:"machines,omitempty"`
	// Devices are the accelerator kinds the job may run on — cuda, rocm, metal,
	// vulkan, cpu; none named is any its base runs.
	Devices []string `json:"devices,omitempty"`
	// Steps bounds optimizer steps; 0 runs the plan.
	Steps int `json:"steps,omitempty"`
	// MaxSeconds bounds the device-seconds the job's tasks use together; 0 is none.
	MaxSeconds int64 `json:"max_seconds,omitempty"`
	// Budget bounds what the job's compute costs, in US cents; 0 is none beyond
	// the payer's balance. Only a job Hanzo runs costs anything, so only it takes one.
	Budget int64 `json:"budget,omitempty"`
}

// Evaluation names the target suites.
type Evaluation struct {
	// Suites are the suites the job is meant to improve.
	Suites []string `json:"suites"`
}

// Output is what a job produces.
type Output struct {
	// Kind is checkpoint, lora, capability, basis or merged.
	Kind string `json:"kind"`
	// Name labels it.
	Name string `json:"name,omitempty"`
}

// Family is one base and how it trains: by which methods, where, and what a
// device-second costs there, and what else a create may ask of it. GET
// /v1/train/models answers these rows, and a create is validated against the
// same row.
type Family struct {
	// Base is the base model: `kai`, or `engine` for the models the engine loads.
	Base string `json:"base"`
	// Adaptations are the methods it trains by: a create's adaptation.mode.
	Adaptations []string `json:"adaptations"`
	// Runs are where it runs, each with what a device-second costs there.
	Runs []Run `json:"runs"`
	// Nouns are the resources that run it: jobs, clients.
	Nouns []string `json:"nouns"`
	// Executor is what runs it.
	Executor string `json:"executor"`
	// Devices are the accelerator kinds it runs on, wherever it runs.
	Devices []string `json:"devices"`
	// Protect are the protections it runs: projection, distillation, capabilities.
	Protect []string `json:"protect"`
	// Terms are the objective terms it runs.
	Terms []string `json:"terms"`
	// Outputs are the output kinds it produces.
	Outputs []string `json:"outputs"`
	// Datasets are the dataset URI schemes it reads.
	Datasets []string `json:"datasets"`
}

// Run is one place a base trains and its price there.
type Run struct {
	// Where is hanzo, Hanzo's devices, charged by the device-second to the payer
	// inside the job's budget; or machines, the org's linked machines, charged
	// nothing.
	Where string `json:"where"`
	// Devices are the accelerator kinds it runs on there; none is the executor's own.
	Devices []string `json:"devices"`
	// Price is what one device-second costs there, in nano-USD: 0 on the org's
	// machines.
	Price int64 `json:"price_nano_usd"`
	// meter names the price in the meter authority, "" for free; floor is its
	// price when the authority has no row, in micro-USD per span seconds.
	meter       string
	floor, span int64
}

var families = []Family{
	{
		Base:        "kai",
		Adaptations: []string{"full", "readout", "auto"},
		Runs: []Run{
			{Where: "hanzo", Devices: accelerators, meter: meterName("kai", "gpu"), floor: gpuHourFloor, span: 3600},
			{Where: "hanzo", Devices: []string{"cpu"}, meter: meterName("kai", "cpu"), floor: cpuHourFloor, span: 3600},
			{Where: "machines", Devices: devices},
		},
		Nouns:    []string{"jobs"},
		Executor: "train serve",
		Devices:  devices,
		Protect:  []string{"projection", "distillation"},
		Terms:    []string{"pairwise_margin", "hard_margin", "consistency"},
		Outputs:  []string{"capability", "checkpoint"},
		Datasets: []string{"stage", "artifact"},
	},
	{
		Base:        "engine",
		Adaptations: []string{"lora"},
		Runs: []Run{
			{Where: "hanzo", Devices: []string{}, meter: "client-second", floor: clientFloorMicros, span: 1},
		},
		Nouns:    []string{"clients"},
		Executor: "engine",
		Devices:  []string{},
		Protect:  []string{},
		Terms:    []string{},
		Outputs:  []string{"lora"},
		Datasets: []string{},
	},
}

var (
	modes = []string{"full", "readout", "lora", "qlora", "basis", "auto"}
	// accelerators are the device kinds priced as a GPU.
	accelerators = []string{"cuda", "rocm", "metal", "vulkan"}
	// devices are the accelerator kinds a claim names and a job may ask for.
	devices = append(slices.Clone(accelerators), "cpu")
	terms   = []string{"pairwise_margin", "hard_margin", "consistency"}
	kinds   = []string{"checkpoint", "lora", "capability", "basis", "merged"}

	name     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	sha      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	stage    = regexp.MustCompile(`^stage:([a-z0-9][a-z0-9._-]{0,63})$`)
	uploaded = regexp.MustCompile(`^artifact:([0-9a-f]{64})$`)
	hub      = regexp.MustCompile(`^hub:[A-Za-z0-9._-]+/[A-Za-z0-9._-]+@[A-Za-z0-9._-]+$`)
	basis    = regexp.MustCompile(`^basis://([A-Za-z0-9._-]+)/([A-Za-z0-9._-]+)$`)
	scheme   = regexp.MustCompile(`^(stage|hf|s3|artifact):`)
)

// defaultBudget is the regression budget a protect without one gets.
var defaultBudget = Budget{Accuracy: 0.02, ECE: 0.03}

// family is the row a base belongs to: kai, or the engine's for any other name.
func family(base string) Family {
	if base == "kai" {
		return families[0]
	}
	return families[1]
}

// refuse is a 400 problem carrying its code, and the supported set when there is one.
func refuse(code, msg string, supported []string) error {
	e := &zip.HTTPError{Status: http.StatusBadRequest, Code: code, Msg: msg}
	if supported != nil {
		e.Detail = map[string]any{"supported": supported}
	}
	return e
}

func invalid(format string, a ...any) error {
	return refuse("invalid_request", fmt.Sprintf(format, a...), nil)
}

// finite reports whether every x is a finite number.
func finite(xs ...float64) bool {
	for _, x := range xs {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return false
		}
	}
	return true
}

// names checks a list of names: at most max, each well formed, none repeated.
func names(field string, list []string, max int) error {
	if len(list) > max {
		return invalid("%s: at most %d", field, max)
	}
	for i, n := range list {
		if !name.MatchString(n) {
			return invalid("%s: %q is not a name", field, n)
		}
		if slices.Contains(list[:i], n) {
			return invalid("%s: %q twice", field, n)
		}
	}
	return nil
}

// artifacts is what validation reads of the org's artifacts: a lora or basis by
// its address, with the base it belongs to.
type artifacts interface {
	lookup(ctx context.Context, kind, key string) (meta map[string]any, ok bool, err error)
}

// shape checks what holds for every base: well-formed values, in range, named
// from the closed sets.
func (s *Spec) shape() error {
	s.BaseModel = strings.TrimSpace(s.BaseModel)
	if s.BaseModel == "" || len(s.BaseModel) > 256 {
		return invalid("base_model: required, at most 256 characters")
	}
	if len(s.Revision) > 256 {
		return invalid("revision: at most 256 characters")
	}
	if s.Dataset != nil {
		if !scheme.MatchString(s.Dataset.URI) || len(s.Dataset.URI) > 1024 {
			return invalid("dataset.uri: stage:, hf://, s3:// or artifact:, at most 1024 characters")
		}
		if len(s.Dataset.Splits) > 8 {
			return invalid("dataset.splits: at most 8")
		}
		for role, split := range s.Dataset.Splits {
			if !name.MatchString(role) || !name.MatchString(split) {
				return invalid("dataset.splits: %q → %q is not a name pair", role, split)
			}
		}
	}
	if s.Objective.Loss == "" {
		s.Objective.Loss = "cross_entropy"
	}
	if s.Objective.Loss != "cross_entropy" {
		return refuse("unsupported_objective", "objective.loss: "+s.Objective.Loss, []string{"cross_entropy"})
	}
	seen := map[string]bool{}
	for _, t := range s.Objective.Terms {
		if !slices.Contains(terms, t.Kind) {
			return invalid("objective.terms: %q is none of %s", t.Kind, strings.Join(terms, ", "))
		}
		if seen[t.Kind] {
			return invalid("objective.terms: %s twice", t.Kind)
		}
		seen[t.Kind] = true
		if !finite(t.Weight, t.Margin) || t.Weight < 0 {
			return invalid("objective.terms: %s needs a finite weight at or above 0 and a finite margin", t.Kind)
		}
		if t.Kind == "consistency" && t.Margin != 0 {
			return invalid("objective.terms: consistency takes no margin")
		}
	}
	a := &s.Adaptation
	if a.Mode == "" {
		a.Mode = "auto"
	}
	if !slices.Contains(modes, a.Mode) {
		return invalid("adaptation.mode: %q is none of %s", a.Mode, strings.Join(modes, ", "))
	}
	a.Chose = nil
	if a.Rank < 0 || a.Rank > 4096 || a.ResidualRank < 0 || a.ResidualRank > 4096 || !finite(a.Alpha) || a.Alpha < 0 {
		return invalid("adaptation: rank and residual_rank in [0, 4096], alpha finite and at or above 0")
	}
	if err := names("adaptation.targets", a.Targets, 64); err != nil {
		return err
	}
	for _, src := range a.Sources {
		if !sha.MatchString(src) {
			return invalid("adaptation.sources: %q is not a sha256", src)
		}
	}
	if len(a.Sources) > 64 {
		return invalid("adaptation.sources: at most 64")
	}
	if a.Basis != "" && !basis.MatchString(a.Basis) && !sha.MatchString(a.Basis) {
		return invalid("adaptation.basis: a sha256 or basis://<base>/<name>")
	}
	if a.Mode == "basis" {
		if (a.Basis == "") == (len(a.Sources) == 0) {
			return invalid("adaptation: basis takes a basis or sources, one of them")
		}
	} else if len(a.Sources) > 0 || a.Basis != "" || a.ResidualRank != 0 {
		return invalid("adaptation: basis, sources and residual_rank belong to mode basis")
	}
	if p := s.Protect; p != nil {
		if len(p.Suites) == 0 && len(p.Capabilities) == 0 {
			return invalid("protect: suites or capabilities")
		}
		if err := names("protect.suites", p.Suites, 64); err != nil {
			return err
		}
		for i, c := range p.Capabilities {
			if !sha.MatchString(c) {
				return invalid("protect.capabilities: %q is not a sha256", c)
			}
			if slices.Contains(p.Capabilities[:i], c) {
				return invalid("protect.capabilities: %s twice", c)
			}
		}
		if len(p.Capabilities) > 64 {
			return invalid("protect.capabilities: at most 64")
		}
		if q := p.Projection; q != nil {
			if q.Strength == nil {
				one := 1.0
				q.Strength = &one
			}
			if !finite(*q.Strength) || *q.Strength < 0 || *q.Strength > 1 {
				return invalid("protect.projection.strength: in [0, 1]")
			}
		}
		if p.Distillation && len(p.Suites) == 0 {
			return invalid("protect.distillation: needs suites to draw a preservation set from")
		}
		if p.Budget == nil {
			b := defaultBudget
			p.Budget = &b
		}
		if !finite(p.Budget.Accuracy, p.Budget.ECE) || p.Budget.Accuracy < 0 || p.Budget.ECE < 0 {
			return invalid("protect.budget: accuracy and ece finite and at or above 0")
		}
	}
	rs := s.Resources
	if err := names("resources.machines", rs.Machines, 8); err != nil {
		return err
	}
	for i, d := range rs.Devices {
		if !slices.Contains(devices, d) {
			return invalid("resources.devices: %q is none of %s", d, strings.Join(devices, ", "))
		}
		if slices.Contains(rs.Devices[:i], d) {
			return invalid("resources.devices: %s twice", d)
		}
	}
	if rs.Steps < 0 || rs.Steps > 10_000_000 || rs.MaxSeconds < 0 || rs.MaxSeconds > 100_000_000 || rs.Budget < 0 || rs.Budget > 1_000_000_000 {
		return invalid("resources: steps in [0, 10^7], max_seconds in [0, 10^8], budget in [0, 10^9] cents")
	}
	if rs.Budget > 0 && len(rs.Machines) > 0 {
		return invalid("resources.budget: a job on the org's own machines costs nothing to bound; budget a job Hanzo runs, one that names no machines")
	}
	if e := s.Evaluation; e != nil {
		if err := names("evaluation.suites", e.Suites, 64); err != nil {
			return err
		}
	}
	if s.Output.Kind != "" && !slices.Contains(kinds, s.Output.Kind) {
		return invalid("output.kind: %q is none of %s", s.Output.Kind, strings.Join(kinds, ", "))
	}
	if s.Output.Name != "" && !name.MatchString(s.Output.Name) {
		return invalid("output.name: %q is not a name", s.Output.Name)
	}
	if s.From != "" && !sha.MatchString(s.From) {
		return invalid("from: a sha256")
	}
	return nil
}

// agree checks that inputs naming other artifacts are the org's and belong
// together: an artifact dataset is one the org uploaded, every source adapter is
// a lora of this base, all on one revision and one module set, and a basis
// belongs to this base.
func (s *Spec) agree(ctx context.Context, arts artifacts) error {
	if s.Dataset != nil {
		if m := uploaded.FindStringSubmatch(s.Dataset.URI); m != nil {
			_, ok, err := arts.lookup(ctx, "dataset", m[1])
			if err != nil {
				return err
			}
			if !ok {
				return invalid("dataset.uri: %s is no dataset this org uploaded", m[1])
			}
		}
	}
	a := s.Adaptation
	var first map[string]any
	for _, src := range a.Sources {
		meta, ok, err := arts.lookup(ctx, "lora", src)
		if err != nil {
			return err
		}
		if !ok {
			return invalid("adaptation.sources: %s is no lora of this org", src)
		}
		if meta["base_model"] != s.BaseModel {
			return refuse("incompatible_adapters", fmt.Sprintf("source %s adapts %v, not %s", src, meta["base_model"], s.BaseModel), nil)
		}
		if first == nil {
			first = meta
			continue
		}
		if fmt.Sprint(meta["revision"]) != fmt.Sprint(first["revision"]) {
			return refuse("incompatible_adapters", fmt.Sprintf("sources adapt revisions %v and %v", first["revision"], meta["revision"]), nil)
		}
		if fmt.Sprint(meta["targets"]) != fmt.Sprint(first["targets"]) {
			return refuse("incompatible_adapters", fmt.Sprintf("sources adapt module sets %v and %v", first["targets"], meta["targets"]), nil)
		}
	}
	if a.Basis != "" {
		meta, ok, err := arts.lookup(ctx, "basis", a.Basis)
		if err != nil {
			return err
		}
		if !ok {
			return invalid("adaptation.basis: %s is no basis of this org", a.Basis)
		}
		if meta["base_model"] != s.BaseModel {
			return refuse("incompatible_basis", fmt.Sprintf("%s is a basis of %v, not %s", a.Basis, meta["base_model"], s.BaseModel), nil)
		}
	}
	if p := s.Protect; p != nil {
		for _, c := range p.Capabilities {
			meta, ok, err := arts.lookup(ctx, "capability", c)
			if err != nil {
				return err
			}
			if !ok {
				return invalid("protect.capabilities: %s is no capability of this org", c)
			}
			if meta["base_model"] != s.BaseModel {
				return refuse("incompatible_basis", fmt.Sprintf("capability %s was taught to %v, not %s", c, meta["base_model"], s.BaseModel), nil)
			}
		}
	}
	return nil
}

// supported checks each choice against what the base's family runs for noun, and
// resolves auto. The first unsupported choice is the answer.
func (s *Spec) supported(noun string) error {
	f := family(s.BaseModel)
	a := &s.Adaptation
	if a.Mode == "auto" {
		a.Chose = choose(f, s)
		a.Mode = a.Chose.Mode
	}
	if !slices.Contains(f.Adaptations, a.Mode) {
		return refuse("unsupported_adaptation", fmt.Sprintf("%s does not run %s", s.BaseModel, a.Mode), f.Adaptations)
	}
	if p := s.Protect; p != nil {
		if len(f.Protect) == 0 {
			return refuse("unsupported_protect", s.BaseModel+" protects nothing yet", f.Protect)
		}
		for _, m := range p.parts() {
			if !slices.Contains(f.Protect, m) {
				return refuse("unsupported_protect", fmt.Sprintf("%s does not run protect %s", s.BaseModel, m), f.Protect)
			}
		}
	}
	for _, t := range s.Objective.Terms {
		if !slices.Contains(f.Terms, t.Kind) {
			return refuse("unsupported_objective", fmt.Sprintf("%s does not run %s", s.BaseModel, t.Kind), f.Terms)
		}
	}
	if s.Output.Kind == "" {
		s.Output.Kind = f.Outputs[0]
	}
	if !slices.Contains(f.Outputs, s.Output.Kind) {
		return refuse("unsupported_output", fmt.Sprintf("%s does not produce %s", s.BaseModel, s.Output.Kind), f.Outputs)
	}
	if noun != "jobs" {
		return nil
	}
	if s.From != "" {
		return invalid("from: no %s executor starts from an artifact yet; start from revision", s.BaseModel)
	}
	if s.Dataset == nil {
		return invalid("dataset: required")
	}
	if !slices.Contains(f.Datasets, strings.SplitN(s.Dataset.URI, ":", 2)[0]) {
		return refuse("invalid_request", fmt.Sprintf("dataset.uri: %s reads %s", s.BaseModel, strings.Join(f.Datasets, ", ")), f.Datasets)
	}
	for _, d := range s.Resources.Devices {
		if !slices.Contains(f.Devices, d) {
			return refuse("unsupported_device", fmt.Sprintf("%s does not run on %s", s.BaseModel, d), f.Devices)
		}
	}
	if hanzo(*s) && s.Resources.Budget == 0 {
		return refuse("budget_required", "resources.budget: a job Hanzo runs is charged by the device-second, so it needs a budget in US cents; a job on the org's own machines needs none", nil)
	}
	if s.BaseModel == "kai" {
		if !stage.MatchString(s.Dataset.URI) && !uploaded.MatchString(s.Dataset.URI) {
			return invalid("dataset.uri: stage:<name> or artifact:<sha256>")
		}
		if !name.MatchString(s.Revision) && !hub.MatchString(s.Revision) {
			return invalid("revision: a finished run's name, or hub:<repo>@<rev>")
		}
	}
	return nil
}

// choose is auto: the mode a base's executor is measured on.
func choose(f Family, _ *Spec) *Chose {
	if f.Base == "kai" {
		return &Chose{Mode: "full", Why: "auto trains kai's full weights; readout, the head alone, is asked for by name"}
	}
	return &Chose{Mode: "lora", Why: "the engine trains lora adapters"}
}

// parts names the protections p asks for, as a Family lists them.
func (p *Protect) parts() []string {
	var out []string
	if p.Projection != nil {
		out = append(out, "projection")
	}
	if p.Distillation {
		out = append(out, "distillation")
	}
	if len(p.Capabilities) > 0 {
		out = append(out, "capabilities")
	}
	return out
}

// bases is the families that run noun.
func bases(noun string) []string {
	var out []string
	for _, f := range families {
		if slices.Contains(f.Nouns, noun) {
			out = append(out, f.Base)
		}
	}
	return out
}

// validate is the create's order (HIP-1333 §3): shape, base, agreement, support.
func (s *Spec) validate(ctx context.Context, noun string, arts artifacts) error {
	if err := s.shape(); err != nil {
		return err
	}
	if !slices.Contains(family(s.BaseModel).Nouns, noun) {
		return refuse("unsupported_base", fmt.Sprintf("%s: jobs run on kai today, clients on the engine's models", s.BaseModel), bases(noun))
	}
	if err := s.agree(ctx, arts); err != nil {
		return err
	}
	return s.supported(noun)
}
