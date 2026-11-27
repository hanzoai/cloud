package train

// captures.go — what ai hands train over the internal plane: each Kai decision's
// captured states, for an org that captures, and the corrections a person files
// against one of the org's decisions (/v1/decisions/:id/corrections is ai's
// address by the Decisions wire; this is the store behind it).
//
// A capture is Kai's `capture` object: each question's pooled state (F16), the
// base's own scores and probabilities, and the org's capability that answered,
// if one did, with what it answered. Its bytes are an object in the org's
// sealed bucket at <org>/captures/<decision>; its questions are rows the org's
// lessons, corrections and live counts read. A capture is kept
// TRAIN_CAPTURE_DAYS (30) unless a correction names its decision.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	peer "github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// decisionID is a decision's id as the service and ai mint them.
var decisionID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// captureKey is where a decision's captured states are kept.
func captureKey(org, decision string) string { return org + "/captures/" + decision }

const jsonType = "application/json"

// plane publishes the ops ai calls: whether an org captures, a decision's states,
// and a decision's corrections.
func plane(o ops) {
	p := cloud.Plane()
	p.Post("/train/capturing", o.capturingOp, zip.WithOperationID(peer.TrainCapturing),
		zip.WithSummary("Whether the caller's org captures its Kai decisions' states"))
	p.Post("/train/capture", o.captureOp, zip.WithOperationID(peer.TrainCapture),
		zip.WithSummary("File one Kai decision's captured states for the caller's org"))
	p.Post("/train/correct", o.correct, zip.WithOperationID(peer.TrainCorrect),
		zip.WithSummary("File a correction of one of the caller's org's decisions"))
	p.Post("/train/corrections", o.decisionCorrections, zip.WithOperationID(peer.TrainCorrections),
		zip.WithSummary("List one decision's corrections"))
	p.Post("/train/uncorrect", o.uncorrect, zip.WithOperationID(peer.TrainUncorrect),
		zip.WithSummary("Delete one correction of a decision"))
	p.Post("/train/capability/owned", o.capabilityOwnedOp, zip.WithOperationID(peer.TrainCapabilityOwned),
		zip.WithSummary("Whether the caller's org owns a publishable Kai capability"))
	p.Post("/train/capability/install", o.capabilityInstallOp, zip.WithOperationID(peer.TrainCapabilityInstall),
		zip.WithSummary("Install or uninstall a shared Kai capability for the caller's org"))
	p.Post("/train/capability/weights", o.capabilityWeightsOp, zip.WithOperationID(peer.TrainCapabilityWeights),
		zip.WithSummary("Read a published Kai capability's .tar weights for open-source distribution"))
}

// storeOf is org's store, for a plane caller that states the org and no person.
func (o ops) storeOf(org string) (*store, error) {
	ns, err := principal.OrgNamespace(org, "")
	if err != nil {
		return nil, zip.ErrBadRequest("not an org")
	}
	st, err := o.s.State.stores.For(ns)
	if err != nil {
		o.s.Log.Error("train store open failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "train store unavailable")
	}
	return st, nil
}

// planeOrg is the org a plane call acts for, or a refusal.
func planeOrg(ctx context.Context) (string, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return "", zip.ErrUnauthorized("train: no org on the call")
	}
	return org, nil
}

func (o ops) capturingOp(ctx context.Context, _ *cloud.Unit) (*peer.Capturing, error) {
	org, err := planeOrg(ctx)
	if err != nil {
		return nil, err
	}
	st, err := o.storeOf(org)
	if err != nil {
		return nil, err
	}
	on, err := o.capturing(ctx, st)
	if err != nil {
		return nil, o.fault("capture read", org, err)
	}
	return &peer.Capturing{On: on}, nil
}

// states is Kai's `capture` object.
type states struct {
	SHA256         string                `json:"sha256"`
	Representation string                `json:"representation"`
	Width          int                   `json:"width"`
	Questions      map[string]stateOfOne `json:"questions"`
}

type stateOfOne struct {
	Type       string    `json:"type"`
	Labels     []string  `json:"labels"`
	Pooled     string    `json:"pooled"`
	Tokens     int       `json:"tokens"`
	Logits     []float64 `json:"logits"`
	Probs      []float64 `json:"probs"`
	Capability *struct {
		Name   string `json:"name"`
		SHA256 string `json:"sha256"`
	} `json:"capability"`
	Answered []float64 `json:"answered"`
}

// argmax is the first index of the largest value, as Kai reads an answer.
func argmax(p []float64) int {
	best := 0
	for i, v := range p {
		if v > p[best] {
			best = i
		}
	}
	return best
}

// read checks a capture and answers its rows. Refused: an id, base or width the
// service cannot have written, and a question whose state, scores or answers
// its labels do not hold.
func (s *states) rows(decision, request string, at int64) ([]captureRow, error) {
	if !sha.MatchString(s.SHA256) || !sha.MatchString(s.Representation) || s.Width < 1 || s.Width > 1<<14 || len(s.Questions) > 100 {
		return nil, invalid("a capture names a base, a width or questions the decision service does not write")
	}
	out := make([]captureRow, 0, len(s.Questions))
	for key, q := range s.Questions {
		n := len(q.Labels)
		if !label(key) || !slices.Contains(questions, q.Type) || n < 2 || n > 256 || len(q.Logits) != n || len(q.Probs) != n ||
			(q.Answered != nil && len(q.Answered) != n) {
			return nil, invalid("capture question %q: its labels, scores and answers do not agree", key)
		}
		raw, err := base64.StdEncoding.DecodeString(q.Pooled)
		if err != nil || len(raw) != 2*s.Width {
			return nil, invalid("capture question %q: a state of another width", key)
		}
		r := captureRow{Decision: decision, Question: key, Kind: q.Type, Labels: q.Labels, Base: q.Labels[argmax(q.Probs)],
			Representation: s.Representation, Request: request, At: at}
		if c := q.Capability; c != nil && q.Answered != nil {
			r.Capability, r.Version = c.Name, c.SHA256
			r.Fired = !slices.Equal(q.Answered, q.Probs)
			r.Answered = q.Labels[argmax(q.Answered)]
			r.Changed = r.Answered != r.Base
		}
		out = append(out, r)
	}
	return out, nil
}

// captureOp keeps one decision's states for the caller's org, when the org
// captures: the object first, then its rows, so a row never names bytes that are
// not kept. It also watches the org's installed capabilities: one installed over
// another base than the capture's is dropped from the set (needs_validation),
// and one that fired is active.
func (o ops) captureOp(ctx context.Context, in *peer.CaptureIn) (*peer.Kept, error) {
	org, err := planeOrg(ctx)
	if err != nil {
		return nil, err
	}
	if !decisionID.MatchString(in.Decision) || len(in.Request) > 128 {
		return nil, invalid("a capture names a decision id and a request id")
	}
	st, err := o.storeOf(org)
	if err != nil {
		return nil, err
	}
	on, err := o.capturing(ctx, st)
	if err != nil {
		return nil, o.fault("capture read", org, err)
	}
	if !on {
		return &peer.Kept{}, nil
	}
	var s states
	if err := json.Unmarshal(in.States, &s); err != nil {
		return nil, invalid("a capture that is not the decision service's: %v", err)
	}
	rows, err := s.rows(in.Decision, in.Request, unix())
	if err != nil {
		return nil, err
	}
	seal, err := o.s.State.seal(ctx, st, org)
	if err != nil {
		return nil, o.fault("org key", org, err)
	}
	if err := o.s.State.objects.write(ctx, captureKey(org, in.Decision), in.States, jsonType, seal); err != nil {
		return nil, o.fault("capture store", org, err)
	}
	if err := st.capture(ctx, rows); err != nil {
		return nil, o.fault("capture write", org, err)
	}
	if err := o.watch(ctx, st, org, s.Representation, rows); err != nil {
		return nil, err
	}
	return &peer.Kept{Stored: true}, nil
}

// watch moves the org's installed capabilities by what a capture read by
// representation repr says: dropped from the set when they were taught over
// another, active once their installed version fired.
func (o ops) watch(ctx context.Context, st *store, org, repr string, rows []captureRow) error {
	cs, err := st.capabilities(ctx, `WHERE installed > 0`)
	if err != nil {
		return o.fault("capabilities read", org, err)
	}
	moved := false
	for _, c := range cs {
		v, err := st.version(ctx, c.Name, c.Installed)
		if err != nil {
			return o.fault("version read", org, err)
		}
		switch {
		case v.Representation != repr:
			c.Installed, c.since, c.State, c.Updated = 0, 0, validating, unix()
			o.s.Log.Warn("train: Kai reads differently under an installed capability; dropped from the set", "org", org, "capability", c.Name, "taught", v.Representation, "serving", repr)
			o.trail(ctx, org, "capability.needs_validation", c.Name, map[string]any{"version": v.Version, "taught": v.Representation, "serving": repr})
		case c.State == installed && slices.ContainsFunc(rows, func(r captureRow) bool { return r.Capability == c.Name && r.Version == v.SHA256 && r.Fired }):
			c.State, c.Updated = active, unix()
		default:
			continue
		}
		if err := st.keep(ctx, c); err != nil {
			return o.fault("capability write", org, err)
		}
		moved = true
	}
	if moved {
		return o.ship(org)
	}
	return nil
}

// resolve is the option label answer names among labels: the label itself, or a
// score's level by its index.
func resolve(kind string, labels []string, answer string) (string, bool) {
	if slices.Contains(labels, answer) {
		return answer, true
	}
	if kind == "score" {
		if i, err := strconv.Atoi(answer); err == nil && i >= 0 && i < len(labels) && strconv.Itoa(i) == answer {
			return labels[i], true
		}
	}
	return "", false
}

// note is text a person wrote: at most n characters, none a control character
// but a newline or a tab.
func note(s string, n int) bool {
	return len([]rune(s)) <= n && !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' })
}

func newID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

// correct files a correction of one question of one of the caller's org's
// decisions: the right answer, which must be one of the question's options and
// not what Kai answered. A decision Kai captured is checked against its capture;
// one made with capture off is kept as filed, missing its state, and cannot teach.
// The org's first correction turns capture on, so its next decisions can.
func (o ops) correct(ctx context.Context, in *peer.CorrectIn) (*peer.Correction, error) {
	st, org, err := o.tenant(ctx)
	if err != nil {
		return nil, err
	}
	switch {
	case !decisionID.MatchString(in.Decision):
		return nil, invalid("decision: %q is not a decision id", in.Decision)
	case !label(in.Question):
		return nil, invalid("question: a question key of 1 to 256 bytes")
	case !label(in.Answer):
		return nil, invalid("answer: an option label of 1 to 256 bytes")
	case !note(in.Note, 2000):
		return nil, invalid("note: at most 2000 characters of text")
	case in.Cohort != "" && !name.MatchString(in.Cohort):
		return nil, invalid("cohort: %q is not a name", in.Cohort)
	}
	had, err := st.corrections(ctx, `WHERE decision = $1 AND question = $2`, in.Decision, in.Question)
	if err != nil {
		return nil, o.fault("corrections read", org, err)
	}
	if len(had) > 0 {
		return nil, conflict("corrected", "this answer is already corrected ("+had[0].ID+"); delete that correction to file another")
	}
	c := correction{ID: newID("cor_"), Decision: in.Decision, Question: in.Question, Answer: in.Answer, Labels: []string{},
		Note: in.Note, Cohort: in.Cohort, By: caller(ctx), At: unix()}
	row, err := st.captured(ctx, in.Decision, in.Question)
	switch {
	case err == nil:
		a, ok := resolve(row.Kind, row.Labels, in.Answer)
		if !ok {
			return nil, invalid("answer: %q is none of %s's options: %s", in.Answer, in.Question, strings.Join(row.Labels, ", "))
		}
		// what the person was answered: the org's capability's label where one
		// answered, else Kai's own; naming Kai's own over a capability's is a
		// damage report
		said := row.Base
		if row.Capability != "" && row.Answered != "" {
			said = row.Answered
		}
		if a == said {
			return nil, conflict("not_wrong", "Kai answered "+a+": a correction names another answer")
		}
		c.Answer, c.Type, c.Labels, c.Base = a, row.Kind, row.Labels, said
	case errors.Is(err, errNotFound):
		other, err := st.captures(ctx, `WHERE decision = $1 LIMIT 1`, in.Decision)
		if err != nil {
			return nil, o.fault("captures read", org, err)
		}
		if len(other) > 0 {
			return nil, missing("question " + in.Question + " of decision " + in.Decision)
		}
	default:
		return nil, o.fault("captures read", org, err)
	}
	if err := st.correct(ctx, c); err != nil {
		return nil, o.fault("correction write", org, err)
	}
	if on, err := o.capturing(ctx, st); err != nil {
		return nil, o.fault("capture read", org, err)
	} else if !on {
		if err := st.set(ctx, "capture", "1"); err != nil {
			return nil, o.fault("capture write", org, err)
		}
	}
	if err := o.ship(org); err != nil {
		return nil, err
	}
	o.trail(ctx, org, "correction.create", in.Decision, map[string]any{"correction": c.ID, "question": c.Question, "answer": c.Answer, "base": c.Base})
	base, err := st.base(ctx)
	if err != nil {
		return nil, o.fault("captures read", org, err)
	}
	out, err := stated(ctx, st, c, base)
	if err != nil {
		return nil, o.fault("captures read", org, err)
	}
	return &out, nil
}

// decisionCorrections answers one of the org's decisions' corrections.
func (o ops) decisionCorrections(ctx context.Context, in *peer.DecisionIn) (*peer.Corrections, error) {
	st, org, err := o.tenant(ctx)
	if err != nil {
		return nil, err
	}
	if !decisionID.MatchString(in.Decision) {
		return nil, invalid("decision: %q is not a decision id", in.Decision)
	}
	cs, err := st.corrections(ctx, `WHERE decision = $1 ORDER BY at, id`, in.Decision)
	if err != nil {
		return nil, o.fault("corrections read", org, err)
	}
	base, err := st.base(ctx)
	if err != nil {
		return nil, o.fault("captures read", org, err)
	}
	out := &peer.Corrections{Data: []peer.Correction{}, Questions: []peer.Captured{}}
	for _, c := range cs {
		c, err := stated(ctx, st, c, base)
		if err != nil {
			return nil, o.fault("captures read", org, err)
		}
		out.Data = append(out.Data, c)
	}
	rows, err := st.captures(ctx, `WHERE decision = $1 ORDER BY question`, in.Decision)
	if err != nil {
		return nil, o.fault("captures read", org, err)
	}
	for _, r := range rows {
		said := r.Base
		if r.Capability != "" && r.Answered != "" {
			said = r.Answered
		}
		out.Questions = append(out.Questions, peer.Captured{Key: r.Question, Type: r.Kind, Labels: r.Labels, Answered: said})
	}
	return out, nil
}

// uncorrect deletes one correction. A capability already taught from it keeps
// its versions; the next lesson no longer reads it.
func (o ops) uncorrect(ctx context.Context, in *peer.UncorrectIn) (*cloud.Unit, error) {
	st, org, err := o.tenant(ctx)
	if err != nil {
		return nil, err
	}
	gone, err := st.uncorrect(ctx, in.Decision, in.ID)
	if err != nil {
		return nil, o.fault("correction delete", org, err)
	}
	if !gone {
		return nil, missing("correction")
	}
	if err := o.ship(org); err != nil {
		return nil, err
	}
	o.trail(ctx, org, "correction.delete", in.Decision, map[string]any{"correction": in.ID})
	return &cloud.Unit{}, nil
}

// capabilityOwnedOp answers whether the caller's org owns a publishable Kai
// capability of the given name, with its artifact hashes, shared representation,
// route and verified evaluation summary.
func (o ops) capabilityOwnedOp(ctx context.Context, in *peer.CapabilityOwnedIn) (*peer.CapabilityOwned, error) {
	org, err := planeOrg(ctx)
	if err != nil {
		return nil, err
	}
	if !capName.MatchString(in.Name) || reservedName(in.Name) {
		return &peer.CapabilityOwned{Mine: false}, nil
	}
	st, err := o.storeOf(org)
	if err != nil {
		return nil, err
	}
	c, err := st.capability(ctx, in.Name)
	if err == nil {
		vs, err := st.versions(ctx, c.Name)
		if err != nil {
			return nil, o.fault("versions read", org, err)
		}
		var chosen *Version
		if c.Installed > 0 {
			for i := range vs {
				if vs[i].Version == c.Installed && vs[i].Passed {
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
			return &peer.CapabilityOwned{Mine: false}, nil
		}
		var ev struct {
			Repairs     int     `json:"repairs"`
			Damage      int     `json:"damage"`
			FireRate    float64 `json:"fire_rate"`
			Specificity float64 `json:"specificity"`
			OOD         struct {
				Moved int `json:"moved"`
			} `json:"ood"`
			LatencyUs float64 `json:"latency_us"`
		}
		_ = json.Unmarshal(chosen.Evaluation, &ev)
		return &peer.CapabilityOwned{
			Mine:           true,
			Name:           c.Name,
			SHA256:         chosen.SHA256,
			BaseSHA256:     chosen.BaseSHA256,
			Representation: chosen.Representation,
			Form:           chosen.Form,
			Rank:           chosen.Rank,
			Bytes:          chosen.Bytes,
			QuestionKey:    c.Question.Key,
			QuestionType:   c.Question.Type,
			Labels:         c.Question.Labels,
			Repairs:        ev.Repairs,
			Damage:         ev.Damage,
			FireRate:       ev.FireRate,
			Specificity:    ev.Specificity,
			OODMoved:       ev.OOD.Moved,
			LatencyUs:      ev.LatencyUs,
		}, nil
	}
	if !errors.Is(err, errNotFound) {
		return nil, o.fault("capability read", org, err)
	}
	rs, err := st.published(ctx)
	if err != nil {
		return nil, o.fault("published read", org, err)
	}
	for _, r := range rs {
		var p published
		if json.Unmarshal([]byte(r.Published), &p) != nil || p.Name != in.Name || p.Kind != "capability" || p.Route == nil || len(p.Route.Keys) == 0 {
			continue
		}
		qk := p.Route.Keys[0]
		opt := p.Route.Options[qk]
		return &peer.CapabilityOwned{
			Mine:         true,
			Name:         p.Name,
			SHA256:       p.SHA256,
			BaseSHA256:   p.Base,
			Form:         p.Form,
			QuestionKey:  qk,
			QuestionType: opt.Type,
			Labels:       opt.Labels,
		}, nil
	}
	return &peer.CapabilityOwned{Mine: false}, nil
}

// findVersionBySHA locates a capability and version matching sum in st.
func findVersionBySHA(ctx context.Context, st *store, name, sum string) (capability, Version, error) {
	if c, err := st.capability(ctx, name); err == nil {
		vs, err := st.versions(ctx, c.Name)
		if err != nil {
			return capability{}, Version{}, err
		}
		for _, v := range vs {
			if v.SHA256 == sum {
				return c, v, nil
			}
		}
	}
	cs, err := st.capabilities(ctx, `ORDER BY name`)
	if err != nil {
		return capability{}, Version{}, err
	}
	for _, c := range cs {
		vs, err := st.versions(ctx, c.Name)
		if err != nil {
			return capability{}, Version{}, err
		}
		for _, v := range vs {
			if v.SHA256 == sum {
				return c, v, nil
			}
		}
	}
	return capability{}, Version{}, errNotFound
}

// capabilityInstallOp installs or uninstalls a shared Kai capability from the
// marketplace into the caller's org's published set.
func (o ops) capabilityInstallOp(ctx context.Context, in *peer.CapabilityInstallIn) (*peer.CapabilityInstalled, error) {
	org, err := planeOrg(ctx)
	if err != nil {
		return nil, err
	}
	if !capName.MatchString(in.Name) || reservedName(in.Name) {
		return nil, invalid("name: %q is not a capability name", in.Name)
	}
	srcOrg, sum, ok := peer.CapabilityOf(in.Ref)
	if !ok || !sha.MatchString(sum) {
		return nil, invalid("ref: %q is not a capability reference", in.Ref)
	}
	st, err := o.storeOf(org)
	if err != nil {
		return nil, err
	}
	if !in.On {
		c, err := st.capability(ctx, in.Name)
		if errors.Is(err, errNotFound) || (err == nil && c.Installed == 0) {
			return &peer.CapabilityInstalled{Name: in.Name, SHA256: sum, Installed: false}, nil
		}
		if err != nil {
			return nil, o.fault("capability read", org, err)
		}
		c.Installed, c.since, c.State, c.Updated = 0, 0, disabled, unix()
		if err := st.keep(ctx, c); err != nil {
			return nil, o.fault("capability write", org, err)
		}
		if err := o.ship(org); err != nil {
			return nil, err
		}
		return &peer.CapabilityInstalled{Name: in.Name, SHA256: sum, Installed: false}, nil
	}
	srcSt, err := o.storeOf(srcOrg)
	if err != nil {
		return nil, err
	}
	srcCap, srcVer, err := findVersionBySHA(ctx, srcSt, in.Name, sum)
	if errors.Is(err, errNotFound) {
		return nil, missing("capability " + in.Name)
	}
	if err != nil {
		return nil, o.fault("capability read", srcOrg, err)
	}
	if err := o.free(ctx, st, org, in.Name, srcCap.Question); err != nil {
		return nil, err
	}
	srcKey, err := o.keyOf(ctx, srcSt, srcOrg, sum)
	if err != nil {
		return nil, o.fault("artifact read", srcOrg, err)
	}
	tar, err := o.s.State.objects.read(ctx, srcKey)
	if err != nil {
		return nil, o.fault("artifact read", srcOrg, err)
	}
	if err := o.keepArtifact(ctx, st, org, sum, tar); err != nil {
		return nil, err
	}
	now := unix()
	c, err := st.capability(ctx, in.Name)
	if errors.Is(err, errNotFound) {
		c = capability{
			Capability: Capability{
				Name:        in.Name,
				Question:    srcCap.Question,
				State:       draft,
				Corrections: []string{},
				By:          caller(ctx),
				Created:     now,
				Updated:     now,
			},
		}
		if err := st.keep(ctx, c); err != nil {
			return nil, o.fault("capability write", org, err)
		}
	} else if err != nil {
		return nil, o.fault("capability read", org, err)
	}
	vs, err := st.versions(ctx, c.Name)
	if err != nil {
		return nil, o.fault("versions read", org, err)
	}
	var targetVer *Version
	for i := range vs {
		if vs[i].SHA256 == sum {
			targetVer = &vs[i]
			break
		}
	}
	if targetVer == nil {
		v := srcVer
		v.Version = 1
		if len(vs) > 0 {
			v.Version = vs[0].Version + 1
		}
		v.Corrections = []string{}
		v.By = srcOrg
		v.At = now
		c.Updated = now
		if err := st.addVersion(ctx, c.Name, v, c); err != nil {
			return nil, o.fault("version write", org, err)
		}
		targetVer = &v
	}
	if err := o.install(ctx, st, org, &c, *targetVer); err != nil {
		return nil, err
	}
	o.trail(ctx, org, "capability.install", c.Name, map[string]any{"source": srcOrg, "sha256": sum, "version": targetVer.Version})
	return &peer.CapabilityInstalled{Name: c.Name, SHA256: sum, Installed: true}, nil
}

// capabilityWeightsOp reads a published Kai capability's .tar weights for
// open-source download on the marketplace.
func (o ops) capabilityWeightsOp(ctx context.Context, in *peer.CapabilityWeightsIn) (*peer.CapabilityWeights, error) {
	srcOrg, sum, ok := peer.CapabilityOf(in.Ref)
	if !ok || !sha.MatchString(sum) {
		return nil, invalid("ref: %q is not a capability reference", in.Ref)
	}
	srcSt, err := o.storeOf(srcOrg)
	if err != nil {
		return nil, err
	}
	srcCap, srcVer, err := findVersionBySHA(ctx, srcSt, in.Name, sum)
	if errors.Is(err, errNotFound) {
		return nil, missing("capability " + in.Name)
	}
	if err != nil {
		return nil, o.fault("capability read", srcOrg, err)
	}
	srcKey, err := o.keyOf(ctx, srcSt, srcOrg, sum)
	if err != nil {
		return nil, o.fault("artifact read", srcOrg, err)
	}
	tar, err := o.s.State.objects.read(ctx, srcKey)
	if err != nil {
		return nil, o.fault("artifact read", srcOrg, err)
	}
	return &peer.CapabilityWeights{
		Name:           srcCap.Name,
		Version:        srcVer.Version,
		SHA256:         srcVer.SHA256,
		BaseSHA256:     srcVer.BaseSHA256,
		Representation: srcVer.Representation,
		Form:           srcVer.Form,
		Bytes:          srcVer.Bytes,
		Tar:            base64.StdEncoding.EncodeToString(tar),
	}, nil
}
