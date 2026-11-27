package studio

// wire.go — the shapes /v1/studio takes and gives. The contract is these types:
// zipdoc lifts their comments into the published document, so what is said here
// is what a client reads.

// Graph is a run's work, as steps. A step runs once every step it reads from has
// succeeded, so order comes from the references and never from the list.
//
// Strings interpolate {{path}}; a string that is exactly one {{path}} takes the
// referenced value itself (an array, an asset id, a number) rather than its text.
// Paths: input.<name>, brand.<token path>, steps.<id>.text, steps.<id>.json.<field…>,
// steps.<id>.asset (its first output), steps.<id>.assets (all of them, in order),
// and inside a step with each: item, item.<field> and index.
type Graph struct {
	// Steps are the graph's steps, 1 to 64 of them.
	Steps []Spec `json:"steps"`
}

// Spec is one step of a graph.
type Spec struct {
	// ID names the step within its graph: a lowercase letter, then up to 31
	// lowercase letters, digits or underscores.
	ID string `json:"id"`
	// Op is what the step does: chat, image.generate, image.edit, voice.speak,
	// music.generate, video.generate, model.generate, studio.render, render.html,
	// render.document, render.slides, render.composition or video.edit.
	Op string `json:"op"`
	// With are the op's parameters. Values may reference other steps.
	With map[string]any `json:"with,omitempty"`
	// Each fans the step out: a {{path}} naming an array, run once per item (at
	// most 64), its outputs the items' outputs in order.
	Each string `json:"each,omitempty"`
	// Title is what a person sees for the step.
	Title string `json:"title,omitempty"`
}

// Problem is an RFC 9457 problem document, as a run or a step records why it
// failed.
type Problem struct {
	Type   string `json:"type"`
	Title  string `json:"title,omitempty"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
	Code   string `json:"code,omitempty"`
}

// Template is a reusable recipe: a graph and the inputs it asks for. The public
// catalog's templates have no org; an org's own are its creator's until shared.
type Template struct {
	// ID is a catalog slug (image, poster) or an org template's id.
	ID          string         `json:"id"`
	Kind        string         `json:"kind"`
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	Inputs      map[string]any `json:"inputs"`
	Graph       Graph          `json:"graph"`
	// Preview is an asset id or https URL of a still that shows what it makes.
	Preview string `json:"preview,omitempty"`
	Org     string `json:"org"`
	Creator string `json:"creator"`
	Shared  bool   `json:"shared"`
	Created string `json:"created,omitempty"`
	Updated string `json:"updated,omitempty"`
}

// Flow is a saved graph.
type Flow struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Kind        string         `json:"kind,omitempty"`
	Inputs      map[string]any `json:"inputs,omitempty"`
	Graph       Graph          `json:"graph"`
	Org         string         `json:"org"`
	Creator     string         `json:"creator"`
	Shared      bool           `json:"shared"`
	Created     string         `json:"created"`
	Updated     string         `json:"updated"`
}

// Document is a document or a deck as its IR: blocks for a doc, slides for a deck.
// A run renders it (template document or slides, input.document its id).
type Document struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Title   string `json:"title"`
	Brand   string `json:"brand,omitempty"`
	IR      IR     `json:"ir"`
	Org     string `json:"org"`
	Creator string `json:"creator"`
	Shared  bool   `json:"shared"`
	Created string `json:"created"`
	Updated string `json:"updated"`
}

// IR is a document's content. A doc is blocks: {type: heading, level, text},
// {type: paragraph, text}, {type: list, ordered, items}, {type: quote, text},
// {type: image, asset, caption}, {type: table, rows, header}, {type: code, text},
// {type: break}. A deck is slides: {layout: title|content|image|split|quote, title,
// subtitle, bullets, body, image, notes}. **bold** and *italic* are honoured.
type IR struct {
	Blocks []map[string]any `json:"blocks,omitempty"`
	Slides []map[string]any `json:"slides,omitempty"`
}

// Brand is a palette and a type system as design tokens (W3C DTCG): the font and
// type groups are the type system, color the palette, space and radius the
// dimensions. A graph reads them as {{brand.color.primary}}.
type Brand struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	Tokens  map[string]any `json:"tokens"`
	Logos   []string       `json:"logos,omitempty"`
	Org     string         `json:"org"`
	Creator string         `json:"creator"`
	Shared  bool           `json:"shared"`
	Created string         `json:"created"`
	Updated string         `json:"updated"`
}

// Run is one creation: its graph, its input, every step's state, what it made and
// what it cost.
type Run struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Template string `json:"template,omitempty"`
	Flow     string `json:"flow,omitempty"`
	Brand    string `json:"brand,omitempty"`
	Graph    Graph  `json:"graph"`
	// Input is what the run was given, as its inputs schema reads it.
	Input map[string]any `json:"input"`
	Steps []Step         `json:"steps"`
	// Outputs are the Library items the run made, the final steps' first.
	Outputs []Item `json:"outputs"`
	// Text is the last chat step's text, when the run's product is words.
	Text    string `json:"text,omitempty"`
	Version int    `json:"version"`
	Parent  string `json:"parent,omitempty"`
	// Root is the first version's id; equal to id on version 1.
	Root string `json:"root"`
	// EstimateCents is what was reserved when the run started.
	EstimateCents int64 `json:"estimate_cents"`
	// ChargedCents is what the finished steps cost; final once the run ends.
	ChargedCents int64    `json:"charged_cents"`
	Error        *Problem `json:"error,omitempty"`
	Org          string   `json:"org"`
	Creator      string   `json:"creator"`
	Shared       bool     `json:"shared"`
	Created      string   `json:"created"`
	Started      string   `json:"started,omitempty"`
	Finished     string   `json:"finished,omitempty"`
}

// Step is one step of a run as it stands.
type Step struct {
	ID    string `json:"id"`
	Op    string `json:"op"`
	Title string `json:"title,omitempty"`
	// Executor is where the step runs: gateway, studio.render, render or flow.run.
	Executor string `json:"executor"`
	// Status is pending, queued, running, succeeded, failed, canceled or skipped.
	Status string `json:"status"`
	// Attempt is how many times the step has started.
	Attempt  int     `json:"attempt"`
	Progress float64 `json:"progress"`
	// Message is what the step is doing now, for a person.
	Message string `json:"message,omitempty"`
	// Items is how many items a step with each fanned out to.
	Items int `json:"items,omitempty"`
	// Outputs are the Library item ids the step made.
	Outputs   []string `json:"outputs"`
	Text      string   `json:"text,omitempty"`
	CostCents int64    `json:"cost_cents"`
	Error     *Problem `json:"error,omitempty"`
	Started   string   `json:"started,omitempty"`
	Finished  string   `json:"finished,omitempty"`
}

// Gpu is one GPU that can take a studio.render step.
type Gpu struct {
	ID string `json:"id"`
	// Name is the device, as its worker reports it.
	Name    string `json:"name"`
	Machine string `json:"machine,omitempty"`
	VramMB  int64  `json:"vram_mb,omitempty"`
	// Owner is org (linked by your org with hanzo link) or platform.
	Owner string `json:"owner"`
	// Status is idle, busy or offline.
	Status string `json:"status"`
	// Seen is its worker's last heartbeat.
	Seen string `json:"seen,omitempty"`
}

// Message is one turn of a chat thread.
type Message struct {
	ID     string   `json:"id"`
	Role   string   `json:"role"`
	Text   string   `json:"text"`
	Assets []string `json:"assets,omitempty"`
	// Runs are the runs this message started.
	Runs    []string `json:"runs,omitempty"`
	Created string   `json:"created"`
}

// Thread is one chat conversation and its messages.
type Thread struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Messages []Message `json:"messages"`
	Org      string    `json:"org"`
	Creator  string    `json:"creator"`
	Created  string    `json:"created"`
	Updated  string    `json:"updated"`
}

// ThreadCard is one thread in the list.
type ThreadCard struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Updated string `json:"updated"`
}

// ── pages ─────────────────────────────────────────────────────────────────

// TemplatePage is one page of templates; pass next back as cursor.
type TemplatePage struct {
	Data []Template `json:"data"`
	Next string     `json:"next,omitempty"`
}

// FlowPage is one page of flows.
type FlowPage struct {
	Data []Flow `json:"data"`
	Next string `json:"next,omitempty"`
}

// RunPage is one page of runs, newest first (versions: oldest first).
type RunPage struct {
	Data []Run  `json:"data"`
	Next string `json:"next,omitempty"`
}

// DocumentPage is one page of documents.
type DocumentPage struct {
	Data []Document `json:"data"`
	Next string     `json:"next,omitempty"`
}

// BrandPage is one page of brands.
type BrandPage struct {
	Data []Brand `json:"data"`
	Next string  `json:"next,omitempty"`
}

// GpuList is every GPU a studio.render step could run on now.
type GpuList struct {
	Data []Gpu `json:"data"`
}

// ThreadPage is one page of threads, most recent first.
type ThreadPage struct {
	Data []ThreadCard `json:"data"`
	Next string       `json:"next,omitempty"`
}

// Estimate is what a run would reserve, step by step.
type Estimate struct {
	Cents int64       `json:"cents"`
	Steps []StepPrice `json:"steps"`
}

// StepPrice is one step's share of an estimate.
type StepPrice struct {
	ID    string `json:"id"`
	Op    string `json:"op"`
	Cents int64  `json:"cents"`
}

// ── inputs ────────────────────────────────────────────────────────────────

// ref names one resource by the id in its path.
type ref struct {
	// ID is the resource's id, from the path.
	ID string `json:"id"`
}

// pageIn pages a list.
type pageIn struct {
	// Limit is how many rows one page holds, 1 to 100 (default 50).
	Limit int `json:"limit"`
	// Cursor is the previous page's next.
	Cursor string `json:"cursor"`
}

// templatesIn pages the templates, optionally of one kind.
type templatesIn struct {
	// Kind keeps only templates that make this: image, poster, document, slides,
	// type, model, ad, video, film, voice, music or other.
	Kind   string `json:"kind"`
	Limit  int    `json:"limit"`
	Cursor string `json:"cursor"`
}

// runsIn pages the runs.
type runsIn struct {
	Kind string `json:"kind"`
	// Status keeps only runs at this status.
	Status string `json:"status"`
	Limit  int    `json:"limit"`
	Cursor string `json:"cursor"`
}

// documentsIn pages the documents.
type documentsIn struct {
	// Kind is doc or deck.
	Kind   string `json:"kind"`
	Limit  int    `json:"limit"`
	Cursor string `json:"cursor"`
}

// TemplateWrite saves a template to the caller's org.
type TemplateWrite struct {
	Kind        string         `json:"kind"`
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	Inputs      map[string]any `json:"inputs,omitempty"`
	Graph       Graph          `json:"graph"`
	Preview     string         `json:"preview,omitempty"`
	Shared      bool           `json:"shared,omitempty"`
}

// TemplatePatch changes one template; absent fields stay as they are.
type TemplatePatch struct {
	ID          string         `json:"id"`
	Title       *string        `json:"title,omitempty"`
	Description *string        `json:"description,omitempty"`
	Inputs      map[string]any `json:"inputs,omitempty"`
	Graph       *Graph         `json:"graph,omitempty"`
	Preview     *string        `json:"preview,omitempty"`
	Shared      *bool          `json:"shared,omitempty"`
}

// FlowWrite saves a graph as a flow.
type FlowWrite struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Kind        string         `json:"kind,omitempty"`
	Inputs      map[string]any `json:"inputs,omitempty"`
	Graph       Graph          `json:"graph"`
	Shared      bool           `json:"shared,omitempty"`
}

// FlowPatch changes one flow; absent fields stay as they are.
type FlowPatch struct {
	ID          string         `json:"id"`
	Name        *string        `json:"name,omitempty"`
	Description *string        `json:"description,omitempty"`
	Kind        *string        `json:"kind,omitempty"`
	Inputs      map[string]any `json:"inputs,omitempty"`
	Graph       *Graph         `json:"graph,omitempty"`
	Shared      *bool          `json:"shared,omitempty"`
}

// DocumentWrite writes a document or a deck.
type DocumentWrite struct {
	// Kind is doc or deck.
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Brand  string `json:"brand,omitempty"`
	IR     IR     `json:"ir"`
	Shared bool   `json:"shared,omitempty"`
}

// DocumentPatch changes one document; absent fields stay as they are.
type DocumentPatch struct {
	ID     string  `json:"id"`
	Title  *string `json:"title,omitempty"`
	Brand  *string `json:"brand,omitempty"`
	IR     *IR     `json:"ir,omitempty"`
	Shared *bool   `json:"shared,omitempty"`
}

// BrandWrite saves a brand.
type BrandWrite struct {
	Name   string         `json:"name"`
	Tokens map[string]any `json:"tokens"`
	Logos  []string       `json:"logos,omitempty"`
	Shared bool           `json:"shared,omitempty"`
}

// BrandPatch changes one brand; absent fields stay as they are.
type BrandPatch struct {
	ID     string         `json:"id"`
	Name   *string        `json:"name,omitempty"`
	Tokens map[string]any `json:"tokens,omitempty"`
	Logos  []string       `json:"logos,omitempty"`
	Shared *bool          `json:"shared,omitempty"`
}

// RunCreate starts a run: exactly one of template, flow or graph (or parent
// alone, to run that run's graph again as its next version).
type RunCreate struct {
	// Template is a template id.
	Template string `json:"template,omitempty"`
	// Flow is a flow id.
	Flow  string `json:"flow,omitempty"`
	Graph *Graph `json:"graph,omitempty"`
	// Input are the values the inputs schema asks for.
	Input map[string]any `json:"input,omitempty"`
	// Brand is a brand id; its tokens are brand.* in the graph.
	Brand string `json:"brand,omitempty"`
	Title string `json:"title,omitempty"`
	// Parent makes this run the next version of that run.
	Parent string `json:"parent,omitempty"`
	Shared bool   `json:"shared,omitempty"`
	// Key is the Idempotency-Key header: the same key from the same person within a
	// day answers the run it started rather than starting a second.
	Key string `json:"-" header:"Idempotency-Key"`
}

// RunPatch renames or shares one run.
type RunPatch struct {
	ID     string  `json:"id"`
	Title  *string `json:"title,omitempty"`
	Shared *bool   `json:"shared,omitempty"`
}

// ChatTurn is one message in a thread.
type ChatTurn struct {
	// Thread continues this thread; absent starts one.
	Thread  string `json:"thread,omitempty"`
	Message string `json:"message"`
	// Assets are Library item ids the message refers to.
	Assets []string `json:"assets,omitempty"`
	Brand  string   `json:"brand,omitempty"`
	// Model is the planning model; default the platform's.
	Model string `json:"model,omitempty"`
}
