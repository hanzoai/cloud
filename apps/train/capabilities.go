package train

// capabilities.go — /v1/capabilities: teach Kai from mistakes without retraining
// Kai.
//
// A capability is an org's small, routed correction over Kai's frozen base. Its
// org marks a Kai answer wrong (a correction, filed through ai at
// /v1/decisions/:id/corrections, captures.go); Kai captured that decision's
// state, so a capability of that question is taught on cached states, on the
// CPU, in seconds, by the decision service itself (POST /v1/teach, lesson.go):
// the smallest form that passes evaluation is kept. Every lesson makes a new,
// immutable version with its evaluation; a version whose evaluation passed is
// publishable, and publishing installs it in the org's published set, the set
// ai attaches to the org's decisions (publish.go's list). Rollback moves the
// installed pointer back; disable takes it out of the set.
//
// One state machine, shared by the platform and the runtime:
//
//	draft → trained → evaluated → publishable → installed → active → disabled
//
// A lesson trains and evaluates in one call, so a capability passes through
// trained and rests in evaluated (its gate failed) or publishable. Plus
// needs_validation, when Kai's base moved under an installed version: it is
// dropped from the set and never served over a base it was not taught on.
// Monitoring is a property of an installed capability (Live), not a state, and
// rollback is an action, not a state.
//
// Capabilities share train's store, bucket, org key and published set: a store
// has one owner, and a capability's artifact is installed the way a job's is.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strconv"

	peer "github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/zap-proto/zip"
)

// Capability states.
const (
	draft       = "draft"
	evaluated   = "evaluated"
	publishable = "publishable"
	installed   = "installed"
	active      = "active"
	disabled    = "disabled"
	validating  = "needs_validation"
)

// capName is a capability's name: lowercase words joined by hyphens. corrections
// and capture are the addresses beside the capabilities, never one's name.
var capName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

func reservedName(n string) bool { return n == "corrections" || n == "capture" }

// Capability is one of the org's capabilities.
type Capability struct {
	// Name is its name, one per org; it is the name its versions install under.
	Name string `json:"name"`
	// Question is what it answers: the route it is installed behind.
	Question Question `json:"question"`
	// State is where it is in its life: draft, trained, evaluated, publishable,
	// installed, active, disabled or needs_validation.
	State string `json:"state"`
	// Version is its latest version, 0 in draft.
	Version int `json:"version"`
	// Installed is the version in the org's published set, 0 when none.
	Installed int `json:"installed"`
	// Corrections are the corrections it is taught from.
	Corrections []string `json:"corrections"`
	// Versions are its versions, newest first (one capability read).
	Versions []Version `json:"versions,omitempty"`
	// Live is what its installed version did since it went in.
	Live    *Live  `json:"live,omitempty"`
	By      string `json:"by"`
	Created int64  `json:"created"`
	Updated int64  `json:"updated"`
}

// capability is a capability as the store holds it.
type capability struct {
	Capability
	// since is when the installed version went in.
	since int64
	// history is every version installed, oldest first; the last is the one in
	// the set while one is.
	history []int
}

// Version is one lesson's outcome: an immutable artifact and its evaluation.
type Version struct {
	Version int `json:"version"`
	// SHA256 is its artifact's: the tar the decision service installs.
	SHA256 string `json:"sha256"`
	// BaseSHA256 is Kai's weights it was taught over.
	BaseSHA256 string `json:"base_sha256"`
	// Representation is what it reads of Kai (the weights, tokenizer, layout and
	// pooling, hashed): the only Kai it serves on, whatever the release.
	Representation string `json:"representation"`
	// Form is calibration, head or pair; Rank its place, 1 the smallest.
	Form string `json:"form"`
	Rank int    `json:"rank"`
	// Bytes is its artifact's size.
	Bytes int64 `json:"bytes"`
	// Questions are the question keys it answers.
	Questions []string `json:"questions"`
	// Passed says its evaluation passed the gate: it is publishable.
	Passed bool `json:"passed"`
	// Evaluation is its evaluation record, in the shared /evals words.
	Evaluation json.RawMessage `json:"evaluation"`
	// Corrections are the corrections it was taught from.
	Corrections []string `json:"corrections"`
	By          string   `json:"by"`
	At          int64    `json:"at"`
}

// Live is what an installed version did on live decisions since it went in.
type Live struct {
	Version int `json:"version"`
	// Fires counts decisions it answered otherwise than the base would have.
	Fires int `json:"fires"`
	// Repairs are fires that changed the label and no correction contradicts;
	// Damage, fires whose label a correction put back to the base's; Neutral,
	// fires that kept the base's label.
	Repairs    int     `json:"repairs"`
	Damage     int     `json:"damage"`
	Neutral    int     `json:"neutral"`
	RepairRate float64 `json:"repair_rate"`
	DamageRate float64 `json:"damage_rate"`
	// LastFired is when it last fired, unix seconds; 0 never.
	LastFired int64 `json:"last_fired"`
}

// correction is a correction as the store holds it.
type correction = peer.Correction

// route is the published route of a capability of q: its key, naming q exactly.
func (q Question) route() *Route {
	return &Route{Keys: []string{q.Key}, Options: map[string]Options{q.Key: {Type: q.Type, Labels: q.Labels}}}
}

func capRoutes(app cloud.Router, o ops) {
	g := app.Group("/v1/capabilities")
	g.Get("", o.listCapabilities, zip.WithOperationID("capabilitiesList"))
	g.Post("", o.createCapability, zip.WithStatus(http.StatusCreated), zip.WithOperationID("capabilitiesCreate"))
	g.Get("/corrections", o.listCorrections, zip.WithOperationID("capabilitiesCorrections"))
	g.Get("/capture", o.getCapture, zip.WithOperationID("capabilitiesCapture"))
	g.Put("/capture", o.putCapture, zip.WithOperationID("capabilitiesSetCapture"))
	g.Get("/:name", o.getCapability, zip.WithOperationID("capabilitiesGet"))
	g.Get("/:name/weights", o.capabilityWeights, zip.WithOperationID("capabilitiesWeights"))
	g.Delete("/:name", o.deleteCapability, zip.WithOperationID("capabilitiesDelete"))
	g.Post("/:name/train", o.trainCapability, zip.WithOperationID("capabilitiesTrain"))
	g.Post("/:name/publish", o.publishCapability, zip.WithOperationID("capabilitiesPublish"))
	g.Post("/:name/disable", o.disableCapability, zip.WithOperationID("capabilitiesDisable"))
	g.Post("/:name/rollback", o.rollbackCapability, zip.WithOperationID("capabilitiesRollback"))
}

type capabilitiesOut struct {
	Data []Capability `json:"data"`
}

// show is a capability as it answers: its latest version, and what its
// installed version did live.
func (o ops) show(ctx context.Context, st *store, c capability, whole bool) (Capability, error) {
	out := c.Capability
	vs, err := st.versions(ctx, c.Name)
	if err != nil {
		return Capability{}, err
	}
	if len(vs) > 0 {
		out.Version = vs[0].Version
	}
	if whole {
		out.Versions = vs
	}
	if c.Installed > 0 {
		i := slices.IndexFunc(vs, func(v Version) bool { return v.Version == c.Installed })
		if i >= 0 {
			l, err := st.fires(ctx, c.Name, vs[i].SHA256, c.since)
			if err != nil {
				return Capability{}, err
			}
			l.Version = c.Installed
			out.Live = &l
		}
	}
	if out.Corrections == nil {
		out.Corrections = []string{}
	}
	return out, nil
}

// ListCapabilities answers the org's capabilities by name, each with its state,
// its latest and installed versions, and what the installed one did live.
func (o ops) listCapabilities(ctx context.Context, _ *cloud.Unit) (*capabilitiesOut, error) {
	st, org, err := o.tenant(ctx)
	if err != nil {
		return nil, err
	}
	cs, err := st.capabilities(ctx, `ORDER BY name`)
	if err != nil {
		return nil, o.fault("capabilities read", org, err)
	}
	out := &capabilitiesOut{Data: []Capability{}}
	for _, c := range cs {
		v, err := o.show(ctx, st, c, false)
		if err != nil {
			return nil, o.fault("capabilities read", org, err)
		}
		out.Data = append(out.Data, v)
	}
	return out, nil
}

// ExampleIn is one labeled (state, answer) example for cold-start teaching:
// State is encoded once by the decision service with X-Capture: 1, and Answer
// is the right option label (or score level index).
type ExampleIn struct {
	// State is the decision state (a JSON string or object) Kai reads.
	State json.RawMessage `json:"state"`
	// Answer is the right option label for the question.
	Answer string `json:"answer"`
	// Note says why, for the person reading it later.
	Note string `json:"note,omitempty"`
}

type createIn struct {
	// Name is its name: lowercase words joined by hyphens, at most 64.
	Name string `json:"name"`
	// Corrections are the corrections it is taught from: captured corrections of
	// one question, which becomes its route.
	Corrections []string `json:"corrections"`
	// Question is the route it answers when created cold-start from Examples
	// without prior captured corrections.
	Question *Question `json:"question,omitempty"`
	// Examples are cold-start (state, answer) pairs encoded once over Kai's
	// shared representation and filed as captures and corrections of Question.
	Examples []ExampleIn `json:"examples,omitempty"`
	// Train runs a lesson immediately after creating the capability.
	Train bool `json:"train,omitempty"`
}

// CreateCapability makes a capability in draft from captured corrections of one
// question — or cold-start from labeled (state, answer) examples of Question,
// encoded once over Kai's shared representation — which becomes the route it
// will be installed behind. When Train is true, it immediately runs a CPU lesson
// and returns the evaluated or publishable capability. Refused: a name taken by
// another capability or by a published job's output (409 name_taken),
// corrections of two questions or not captured (400), and a question another of
// the org's capabilities answers (409 route_conflict).
//
// Example: {"name": "refund-intent", "corrections": ["cor_0123456789abcdef"]}
func (o ops) createCapability(ctx context.Context, in *createIn) (*Capability, error) {
	st, org, err := o.tenant(ctx)
	if err != nil {
		return nil, err
	}
	if !capName.MatchString(in.Name) || reservedName(in.Name) {
		return nil, invalid("name: %q is not a capability name: lowercase letters, digits and hyphens, at most 64", in.Name)
	}
	if _, err := st.capability(ctx, in.Name); err == nil {
		return nil, conflict("name_taken", "a capability of this org is already named "+in.Name)
	} else if err != errNotFound {
		return nil, o.fault("capability read", org, err)
	}
	corrections := slices.Clone(in.Corrections)
	if len(in.Examples) > 0 {
		if in.Question == nil {
			return nil, invalid("question: required when creating a capability from examples")
		}
		if err := in.Question.route().check(); err != nil {
			return nil, err
		}
		if err := o.free(ctx, st, org, in.Name, *in.Question); err != nil {
			return nil, err
		}
		exIDs, err := o.ingestExamples(ctx, st, org, *in.Question, in.Examples)
		if err != nil {
			return nil, err
		}
		for _, id := range exIDs {
			if !slices.Contains(corrections, id) {
				corrections = append(corrections, id)
			}
		}
	}
	q, ids, err := o.taught(ctx, st, org, corrections, in.Question)
	if err != nil {
		return nil, err
	}
	if err := o.free(ctx, st, org, in.Name, q); err != nil {
		return nil, err
	}
	now := unix()
	c := capability{Capability: Capability{Name: in.Name, Question: q, State: draft, Corrections: ids, By: caller(ctx), Created: now, Updated: now}}
	if err := st.keep(ctx, c); err != nil {
		return nil, o.fault("capability write", org, err)
	}
	if err := o.ship(org); err != nil {
		return nil, err
	}
	o.trail(ctx, org, "capability.create", in.Name, map[string]any{"question": q, "corrections": ids})
	if in.Train {
		return o.trainCapability(ctx, &trainIn{Name: in.Name})
	}
	v, err := o.show(ctx, st, c, true)
	if err != nil {
		return nil, o.fault("capability read", org, err)
	}
	return &v, nil
}

// taught is the question corrections ids ask, and the ids, each a captured
// correction of the org's; want, when given, is the question they must ask.
func (o ops) taught(ctx context.Context, st *store, org string, ids []string, want *Question) (Question, []string, error) {
	if len(ids) == 0 || len(ids) > 4096 {
		return Question{}, nil, invalid("corrections: 1 to 4096 correction ids")
	}
	var q *Question
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return Question{}, nil, invalid("corrections: %q twice", id)
		}
		seen[id] = true
		cs, err := st.corrections(ctx, `WHERE id = $1`, id)
		if err != nil {
			return Question{}, nil, o.fault("corrections read", org, err)
		}
		if len(cs) == 0 {
			return Question{}, nil, missing("correction " + id)
		}
		c := cs[0]
		if c.Type == "" {
			return Question{}, nil, invalid("correction %s: its decision was made with capture off, so it cannot teach", id)
		}
		got := Question{Key: c.Question, Type: c.Type, Labels: c.Labels}
		if q == nil {
			q = &got
		} else if !q.same(got) {
			return Question{}, nil, invalid("corrections ask two questions: a capability answers one")
		}
	}
	if want != nil && !want.same(*q) {
		return Question{}, nil, invalid("corrections ask %s, not the capability's question", q.Key)
	}
	return *q, ids, nil
}

// free refuses a name a published job holds and a question another capability or
// a published job answers.
func (o ops) free(ctx context.Context, st *store, org, name string, q Question) error {
	others, err := st.published(ctx)
	if err != nil {
		return o.fault("published read", org, err)
	}
	for _, r := range others {
		var p published
		if json.Unmarshal([]byte(r.Published), &p) != nil {
			continue
		}
		if p.Name == name {
			return conflict("name_taken", "a published job's output is already named "+name)
		}
		if p.Route != nil {
			if k := clash(*q.route(), *p.Route); k != "" {
				return conflict("route_conflict", "capability "+p.Name+" already answers "+strconv.Quote(k)+" for that question")
			}
		}
	}
	cs, err := st.capabilities(ctx, `WHERE name != $1`, name)
	if err != nil {
		return o.fault("capabilities read", org, err)
	}
	for _, c := range cs {
		if c.Question.same(q) {
			return conflict("route_conflict", "capability "+c.Name+" already answers that question")
		}
	}
	return nil
}

type nameIn struct {
	Name string `json:"name"`
}

// capOf is the caller's org's store and its capability of the name, or 404.
func (o ops) capOf(ctx context.Context, name string) (*store, string, capability, error) {
	st, org, err := o.tenant(ctx)
	if err != nil {
		return nil, "", capability{}, err
	}
	c, err := st.capability(ctx, name)
	if err == errNotFound {
		return nil, "", capability{}, missing("capability")
	}
	if err != nil {
		return nil, "", capability{}, o.fault("capability read", org, err)
	}
	return st, org, c, nil
}

// GetCapability answers one capability: its versions with their evaluations,
// newest first, and what its installed version did live.
func (o ops) getCapability(ctx context.Context, in *nameIn) (*Capability, error) {
	st, org, c, err := o.capOf(ctx, in.Name)
	if err != nil {
		return nil, err
	}
	v, err := o.show(ctx, st, c, true)
	if err != nil {
		return nil, o.fault("capability read", org, err)
	}
	return &v, nil
}

// CapabilityWeights answers a capability's .tar weights (base64) and shared
// representation metadata for its installed or newest passing version, so the
// org can export or open-source its trained adapter.
func (o ops) capabilityWeights(ctx context.Context, in *nameIn) (*peer.CapabilityWeights, error) {
	st, org, c, err := o.capOf(ctx, in.Name)
	if err != nil {
		return nil, err
	}
	vs, err := st.versions(ctx, c.Name)
	if err != nil {
		return nil, o.fault("versions read", org, err)
	}
	if len(vs) == 0 {
		return nil, conflict("no_version", c.Name+" has no trained version")
	}
	var chosen *Version
	if c.Installed > 0 {
		for i := range vs {
			if vs[i].Version == c.Installed {
				chosen = &vs[i]
				break
			}
		}
	}
	if chosen == nil {
		for i := range vs {
			if vs[i].Passed {
				chosen = &vs[i]
				break
			}
		}
	}
	if chosen == nil {
		chosen = &vs[0]
	}
	k, err := o.keyOf(ctx, st, org, chosen.SHA256)
	if err != nil {
		return nil, o.fault("artifact read", org, err)
	}
	tar, err := o.s.State.objects.read(ctx, k)
	if err != nil {
		return nil, o.fault("artifact read", org, err)
	}
	return &peer.CapabilityWeights{
		Name:           c.Name,
		Version:        chosen.Version,
		SHA256:         chosen.SHA256,
		BaseSHA256:     chosen.BaseSHA256,
		Representation: chosen.Representation,
		Form:           chosen.Form,
		Bytes:          chosen.Bytes,
		Tar:            base64.StdEncoding.EncodeToString(tar),
	}, nil
}

type unmadeOut struct {
	Name    string `json:"name"`
	Deleted bool   `json:"deleted"`
}

// DeleteCapability deletes a capability and its versions; their artifacts go
// with the org's retention. Refused 409 installed while a version is in the set:
// disable it first.
func (o ops) deleteCapability(ctx context.Context, in *nameIn) (*unmadeOut, error) {
	st, org, c, err := o.capOf(ctx, in.Name)
	if err != nil {
		return nil, err
	}
	if c.Installed > 0 {
		return nil, conflict("installed", "version "+strconv.Itoa(c.Installed)+" is installed; disable it first")
	}
	if err := st.unmake(ctx, c.Name); err != nil {
		return nil, o.fault("capability delete", org, err)
	}
	if err := o.ship(org); err != nil {
		return nil, err
	}
	o.trail(ctx, org, "capability.delete", c.Name, nil)
	return &unmadeOut{Name: c.Name, Deleted: true}, nil
}

type trainIn struct {
	Name string `json:"name"`
	// Corrections, when given, replace the corrections it is taught from: captured
	// corrections of its question.
	Corrections []string `json:"corrections"`
	// Examples, when given, encode additional (state, answer) pairs over Kai's
	// shared representation and append any new corrections to the capability.
	Examples []ExampleIn `json:"examples,omitempty"`
}

// TrainCapability teaches a capability a new version from its corrections, on
// the CPU over the states its org's decisions captured: no row is encoded again.
// When Examples are provided, they are encoded once over Kai's shared
// representation and any new mistakes are added to its corrections before the
// lesson runs. The smallest correction that passes evaluation is kept
// (calibration, then a head, then a boost and veto pair); its evaluation, held
// out against base Kai, says what it repaired and damaged, how often it fires,
// that every other answer is base Kai's bit for bit, and what it costs. A
// version that passed is publishable; one that did not is evaluated, with the
// gate's reasons. An installed capability keeps serving its installed version
// until a new one is published.
//
// Example: {"corrections": ["cor_0123456789abcdef", "cor_fedcba9876543210"]}
func (o ops) trainCapability(ctx context.Context, in *trainIn) (*Capability, error) {
	st, org, c, err := o.capOf(ctx, in.Name)
	if err != nil {
		return nil, err
	}
	if len(in.Corrections) > 0 {
		_, ids, err := o.taught(ctx, st, org, in.Corrections, &c.Question)
		if err != nil {
			return nil, err
		}
		c.Corrections = ids
	}
	if len(in.Examples) > 0 {
		exIDs, err := o.ingestExamples(ctx, st, org, c.Question, in.Examples)
		if err != nil {
			return nil, err
		}
		for _, id := range exIDs {
			if !slices.Contains(c.Corrections, id) {
				c.Corrections = append(c.Corrections, id)
			}
		}
	}
	v, err := o.lesson(ctx, st, org, c)
	if err != nil {
		return nil, err
	}
	vs, err := st.versions(ctx, c.Name)
	if err != nil {
		return nil, o.fault("versions read", org, err)
	}
	v.Version = 1
	if len(vs) > 0 {
		v.Version = vs[0].Version + 1
	}
	switch {
	case c.Installed > 0 && (c.State == installed || c.State == active):
		// the installed version serves until another is published
	case v.Passed:
		c.State = publishable
	default:
		c.State = evaluated
	}
	c.Updated = unix()
	if err := st.addVersion(ctx, c.Name, *v, c); err != nil {
		return nil, o.fault("version write", org, err)
	}
	if err := o.ship(org); err != nil {
		return nil, err
	}
	o.trail(ctx, org, "capability.train", c.Name, map[string]any{"version": v.Version, "sha256": v.SHA256, "form": v.Form, "passed": v.Passed})
	out, err := o.show(ctx, st, c, true)
	if err != nil {
		return nil, o.fault("capability read", org, err)
	}
	return &out, nil
}

type publishCapIn struct {
	Name string `json:"name"`
	// Version is the version to install; the newest that passed when absent.
	Version int `json:"version"`
}

// PublishCapability installs a version in the org's published set, where ai
// attaches it to the org's decisions: the version's evaluation must have passed,
// and it must have been taught over the base Kai serves now. Refused 409:
// not_publishable (no passing version, or that one did not pass),
// needs_validation (Kai's base moved since it was taught: train again),
// route_conflict (a published job answers its question).
func (o ops) publishCapability(ctx context.Context, in *publishCapIn) (*Capability, error) {
	st, org, c, err := o.capOf(ctx, in.Name)
	if err != nil {
		return nil, err
	}
	var v Version
	if in.Version > 0 {
		if v, err = st.version(ctx, c.Name, in.Version); err == errNotFound {
			return nil, missing("version")
		} else if err != nil {
			return nil, o.fault("version read", org, err)
		}
	} else {
		vs, err := st.versions(ctx, c.Name)
		if err != nil {
			return nil, o.fault("versions read", org, err)
		}
		i := slices.IndexFunc(vs, func(v Version) bool { return v.Passed })
		if i < 0 {
			return nil, conflict("not_publishable", "no version of "+c.Name+" passed evaluation")
		}
		v = vs[i]
	}
	if err := o.install(ctx, st, org, &c, v); err != nil {
		return nil, err
	}
	o.trail(ctx, org, "capability.publish", c.Name, map[string]any{"version": v.Version, "sha256": v.SHA256})
	out, err := o.show(ctx, st, c, true)
	if err != nil {
		return nil, o.fault("capability read", org, err)
	}
	return &out, nil
}

// install puts v in the org's published set as c's installed version.
func (o ops) install(ctx context.Context, st *store, org string, c *capability, v Version) error {
	if !v.Passed {
		return conflict("not_publishable", "version "+strconv.Itoa(v.Version)+" did not pass evaluation")
	}
	base, err := st.base(ctx)
	if err != nil {
		return o.fault("base read", org, err)
	}
	if base != "" && base != v.Representation {
		return conflict(validating, "Kai reads differently since version "+strconv.Itoa(v.Version)+" was taught; train it again")
	}
	if err := o.free(ctx, st, org, c.Name, c.Question); err != nil {
		return err
	}
	if err := o.serveVersion(ctx, st, org, v.SHA256); err != nil {
		return err
	}
	if n := len(c.history); n == 0 || c.history[n-1] != v.Version {
		c.history = append(c.history, v.Version)
	}
	c.Installed, c.since, c.State, c.Updated = v.Version, unix(), installed, unix()
	if err := st.keep(ctx, *c); err != nil {
		return o.fault("capability write", org, err)
	}
	return o.ship(org)
}

// DisableCapability takes a capability's installed version out of the org's
// published set: its questions are answered by base Kai, bit for bit, from the
// next decision. Its versions stay, and any one that passed can be published
// again.
func (o ops) disableCapability(ctx context.Context, in *nameIn) (*Capability, error) {
	st, org, c, err := o.capOf(ctx, in.Name)
	if err != nil {
		return nil, err
	}
	if c.Installed == 0 {
		return nil, conflict("not_installed", c.Name+" has no version installed")
	}
	was := c.Installed
	c.Installed, c.since, c.State, c.Updated = 0, 0, disabled, unix()
	if err := st.keep(ctx, c); err != nil {
		return nil, o.fault("capability write", org, err)
	}
	if err := o.ship(org); err != nil {
		return nil, err
	}
	o.trail(ctx, org, "capability.disable", c.Name, map[string]any{"version": was})
	out, err := o.show(ctx, st, c, true)
	if err != nil {
		return nil, o.fault("capability read", org, err)
	}
	return &out, nil
}

// RollbackCapability puts the version installed before the installed one back
// in the org's published set: exactly that version's artifact, nothing retrained.
// Refused 409 no_previous when none was installed before it, and
// needs_validation when that one was taught over another base than Kai serves.
func (o ops) rollbackCapability(ctx context.Context, in *nameIn) (*Capability, error) {
	st, org, c, err := o.capOf(ctx, in.Name)
	if err != nil {
		return nil, err
	}
	n := len(c.history)
	if c.Installed == 0 || n < 2 {
		return nil, conflict("no_previous", c.Name+" has no earlier installed version to roll back to")
	}
	prev := c.history[n-2]
	v, err := st.version(ctx, c.Name, prev)
	if err != nil {
		return nil, o.fault("version read", org, err)
	}
	was := c.Installed
	c.history = c.history[:n-1]
	if err := o.install(ctx, st, org, &c, v); err != nil {
		return nil, err
	}
	o.trail(ctx, org, "capability.rollback", c.Name, map[string]any{"from": was, "to": prev, "sha256": v.SHA256})
	out, err := o.show(ctx, st, c, true)
	if err != nil {
		return nil, o.fault("capability read", org, err)
	}
	return &out, nil
}

// Capture is whether the org's Kai decisions keep each question's state, and
// for how long.
type Capture struct {
	On bool `json:"on"`
	// RetentionDays is how long a decision's states are kept unless a correction
	// names it.
	RetentionDays int `json:"retention_days"`
}

type captureIn struct {
	On bool `json:"on"`
}

func (o ops) capturing(ctx context.Context, st *store) (bool, error) {
	v, err := st.setting(ctx, "capture")
	return v == "1", err
}

func (o ops) captureOf(on bool) *Capture {
	return &Capture{On: on, RetentionDays: int(o.s.State.keepCaptures / 86400)}
}

// GetCapture answers whether the org's Kai decisions keep their states for its
// capabilities to learn from. Off until the org turns it on or files its first
// correction.
func (o ops) getCapture(ctx context.Context, _ *cloud.Unit) (*Capture, error) {
	st, org, err := o.tenant(ctx)
	if err != nil {
		return nil, err
	}
	on, err := o.capturing(ctx, st)
	if err != nil {
		return nil, o.fault("capture read", org, err)
	}
	return o.captureOf(on), nil
}

// PutCapture turns capture on or off for the org's Kai decisions from the next
// one. Off keeps what was captured until its retention ends.
func (o ops) putCapture(ctx context.Context, in *captureIn) (*Capture, error) {
	st, org, err := o.tenant(ctx)
	if err != nil {
		return nil, err
	}
	v := "0"
	if in.On {
		v = "1"
	}
	if err := st.set(ctx, "capture", v); err != nil {
		return nil, o.fault("capture write", org, err)
	}
	if err := o.ship(org); err != nil {
		return nil, err
	}
	o.trail(ctx, org, "capability.capture", "capture", map[string]any{"on": in.On})
	return o.captureOf(in.On), nil
}

type correctionsIn struct {
	// Question keeps one question's corrections.
	Question string `json:"question"`
}

// CorrectionGroup is one question's corrections, counted.
type CorrectionGroup struct {
	Key         string   `json:"key"`
	Type        string   `json:"type"`
	Labels      []string `json:"labels"`
	Corrections int      `json:"corrections"`
}

type correctionsOut struct {
	Data      []correction      `json:"data"`
	Questions []CorrectionGroup `json:"questions"`
}

// ListCorrections answers every correction of the org's decisions, newest first,
// each captured (it can teach) or missing (its decision was made with capture
// off), and the questions they ask, each counted: what a capability is made from.
func (o ops) listCorrections(ctx context.Context, in *correctionsIn) (*correctionsOut, error) {
	st, org, err := o.tenant(ctx)
	if err != nil {
		return nil, err
	}
	q, args := `ORDER BY at DESC, id`, []any{}
	if in.Question != "" {
		q, args = `WHERE question = $1 ORDER BY at DESC, id`, []any{in.Question}
	}
	cs, err := st.corrections(ctx, q, args...)
	if err != nil {
		return nil, o.fault("corrections read", org, err)
	}
	base, err := st.base(ctx)
	if err != nil {
		return nil, o.fault("captures read", org, err)
	}
	out := &correctionsOut{Data: []correction{}, Questions: []CorrectionGroup{}}
	for _, c := range cs {
		c, err := stated(ctx, st, c, base)
		if err != nil {
			return nil, o.fault("corrections read", org, err)
		}
		out.Data = append(out.Data, c)
		if c.Type == "" {
			continue
		}
		g := Question{Key: c.Question, Type: c.Type, Labels: c.Labels}
		i := slices.IndexFunc(out.Questions, func(x CorrectionGroup) bool {
			return Question{Key: x.Key, Type: x.Type, Labels: x.Labels}.same(g)
		})
		if i < 0 {
			out.Questions = append(out.Questions, CorrectionGroup{Key: g.Key, Type: g.Type, Labels: g.Labels})
			i = len(out.Questions) - 1
		}
		out.Questions[i].Corrections++
	}
	return out, nil
}

// stated is c with its state: captured when its decision's state is held over
// base, the base Kai serves now, else missing.
func stated(ctx context.Context, st *store, c correction, base string) (correction, error) {
	if c.Labels == nil {
		c.Labels = []string{}
	}
	c.State = "missing"
	row, err := st.captured(ctx, c.Decision, c.Question)
	if err == errNotFound {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if row.Representation == base {
		c.State = "captured"
	}
	return c, nil
}

// trail appends a capability action to the deployment's trail, as the person
// who took it. A deployment with no trail records nothing.
func (o ops) trail(ctx context.Context, org, action, name string, facts map[string]any) {
	if o.s.State.audit == nil {
		return
	}
	who := cloud.Who(ctx)
	if facts == nil {
		facts = map[string]any{}
	}
	facts["org"], facts["name"] = org, name
	after, _ := json.Marshal(facts)
	if _, err := o.s.State.audit.Append(ctx, audit.Record{
		Actor:     audit.Actor{Org: org, Sub: caller(ctx), Email: who.Email},
		Action:    "train." + action,
		Resource:  audit.Resource{Type: "train.capability", ID: org + "/" + name},
		Auth:      audit.AuthContext{Method: "session"},
		Outcome:   audit.Outcome{Result: "success", Status: http.StatusOK},
		SourceIP:  who.IP,
		RequestID: who.RequestID,
		After:     audit.Redact(after),
	}); err != nil {
		o.s.Log.Error("train: a capability action could not be audited", "org", org, "action", action, "err", err)
	}
}
