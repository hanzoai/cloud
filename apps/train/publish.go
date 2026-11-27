package train

// publish.go — a job's output under a name, with the route the decision service
// answers it by; every org's published capabilities, listed; and one org's, as ai
// attaches them to each of the org's decisions.
//
// The route is the shape of a release's routing.json in the decision service
// (HIP-1332): the question keys a capability answers and, where a key names
// one, the exact question under it — its type and its option labels, in order.
// A question no route of the org names is answered by the base. Two capabilities
// of one org may not claim one question, so a publish that would is refused.
//
// A capability's form is how it applies to its base: head, a readout trained over
// the frozen base (adaptation readout), which shares the base's encoder; full, an
// update to the whole model (every other adaptation).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"unicode"

	contract "github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/namespace"
	"github.com/zap-proto/zip"
)

// Route is which questions a published capability answers.
type Route struct {
	// Keys are the question keys it answers.
	Keys []string `json:"keys"`
	// Options narrows a key to one question: a question under that key routes here
	// only when its type and option labels are exactly these. A key not named here
	// routes whatever its question.
	Options map[string]Options `json:"options,omitempty"`
}

// Options is the question an option-qualified route takes under a key.
type Options struct {
	// Type is choice, score or noul.
	Type string `json:"type"`
	// Labels are its option labels, in order: a choice's criteria, a score's levels
	// as text, a noul's two sides.
	Labels []string `json:"labels"`
}

// questions are the question types a route option names.
var questions = []string{"choice", "score", "noul"}

// label reports whether s is a question key or option label a route can carry:
// 1 to 256 bytes, no control characters.
func label(s string) bool {
	return s != "" && len(s) <= 256 && !strings.ContainsFunc(s, unicode.IsControl)
}

// check refuses a route the decision service would not read: no key, a key or
// label it cannot carry, one twice, options for a key it does not answer, a
// question with no labels or of no type.
func (r *Route) check() error {
	if len(r.Keys) == 0 || len(r.Keys) > 64 {
		return invalid("route.keys: 1 to 64 question keys")
	}
	for i, k := range r.Keys {
		if !label(k) {
			return invalid("route.keys: %q is not a question key", k)
		}
		if slices.Contains(r.Keys[:i], k) {
			return invalid("route.keys: %q twice", k)
		}
	}
	for k, q := range r.Options {
		if !slices.Contains(r.Keys, k) {
			return invalid("route.options: %q is a key the route does not answer", k)
		}
		if !slices.Contains(questions, q.Type) {
			return invalid("route.options.%s.type: %q is none of %s", k, q.Type, strings.Join(questions, ", "))
		}
		if len(q.Labels) == 0 || len(q.Labels) > 256 {
			return invalid("route.options.%s.labels: 1 to 256 option labels", k)
		}
		for i, l := range q.Labels {
			if !label(l) {
				return invalid("route.options.%s.labels: %q is not an option label", k, l)
			}
			if slices.Contains(q.Labels[:i], l) {
				return invalid("route.options.%s.labels: %q twice", k, l)
			}
		}
	}
	return nil
}

// clash is a key two routes both claim for one question — either names no
// question under it, or both name the same one — or "".
func clash(a, b Route) string {
	for _, k := range a.Keys {
		if !slices.Contains(b.Keys, k) {
			continue
		}
		x, xok := a.Options[k]
		y, yok := b.Options[k]
		if !xok || !yok || (x.Type == y.Type && slices.Equal(x.Labels, y.Labels)) {
			return k
		}
	}
	return ""
}

type publishIn struct {
	// ID names the job.
	ID string `json:"id"`
	// Name is the published name; the output's name when absent. Publishing a
	// name the org already published replaces that capability.
	Name string `json:"name"`
	// Force publishes a rejected job's output, and is recorded.
	Force bool `json:"force"`
	// Route is which questions the capability answers: required to publish a
	// capability, refused for any other output.
	Route *Route `json:"route"`
}

// published is the record a publish writes.
type published struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	SHA256 string `json:"sha256"`
	// Base is the base's weights' SHA-256 the capability was trained against.
	Base string `json:"base_sha256,omitempty"`
	// Form is a capability's: head or full.
	Form   string `json:"form,omitempty"`
	Route  *Route `json:"route,omitempty"`
	By     string `json:"by"`
	At     int64  `json:"at"`
	Forced bool   `json:"forced"`
}

// formOf is the form of the capability a job's adaptation trains.
func formOf(s Spec) string {
	if s.Adaptation.Mode == "readout" {
		return "head"
	}
	return "full"
}

// PublishTrainJob publishes a job's output under a name. Only a succeeded job's
// output is publishable; a rejected one broke its regression budget and needs
// force, which the record keeps. The output must be stored. A capability is
// published with its route — the question keys it answers and, per key, the
// exact question (`{"type": "choice"|"score"|"noul", "labels": [...]}`) — the
// base SHA-256 its result names, and its form: head for a readout, full for
// every other adaptation. A route that claims a question another of the org's
// published capabilities claims is 409 route_conflict. A name the org already
// published moves to this job. A head's tar is copied first to the serving store,
// the models bucket's capabilities/<org>/<sha256>, where the decision service
// fetches it; ai attaches the org's published capabilities to its decisions.
//
// Example: {"id": "job_0123456789abcdef", "name": "triage", "route": {"keys": ["topic"], "options": {"topic": {"type": "choice", "labels": ["billing", "bug", "other"]}}}}
func (o ops) publish(ctx context.Context, in *publishIn) (*Job, error) {
	st, org, r, err := o.load(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	switch {
	case r.Status == succeeded:
	case r.Status == rejected && in.Force:
	case r.Status == rejected:
		return nil, conflict("not_publishable", "the job broke its regression budget; publish with force to publish it anyway")
	default:
		return nil, conflict("not_publishable", "job is "+r.Status)
	}
	var spec Spec
	if err := json.Unmarshal([]byte(r.Spec), &spec); err != nil {
		return nil, o.fault("job read", org, err)
	}
	as, err := st.artifacts(ctx, r.ID)
	if err != nil {
		return nil, o.fault("artifact read", org, err)
	}
	i := slices.IndexFunc(as, func(a artifact) bool { return a.Kind == spec.Output.Kind && a.State == "stored" })
	if i < 0 {
		return nil, conflict("not_publishable", "no stored "+spec.Output.Kind+" artifact")
	}
	n := in.Name
	if n == "" {
		n = spec.Output.Name
	}
	if n == "" {
		n = r.ID
	}
	if !name.MatchString(n) {
		return nil, invalid("name: %q is not a name", n)
	}
	if _, err := st.capability(ctx, n); err == nil {
		return nil, conflict("name_taken", "a capability of this org is named "+n)
	} else if err != errNotFound {
		return nil, o.fault("capability read", org, err)
	}
	rec := published{Name: n, Kind: as[i].Kind, SHA256: as[i].SHA256, By: caller(ctx), At: unix(), Forced: r.Status == rejected}
	if rec.Kind == "capability" {
		if in.Route == nil {
			return nil, invalid("route: a capability is published with the questions it answers")
		}
		if err := in.Route.check(); err != nil {
			return nil, err
		}
		var res result
		if err := json.Unmarshal([]byte(r.Result), &res); err != nil || !sha.MatchString(res.Base.SHA256) {
			return nil, conflict("not_publishable", "the job's result names no base sha256 to install the capability over")
		}
		rec.Base, rec.Form, rec.Route = res.Base.SHA256, formOf(spec), in.Route
	} else if in.Route != nil {
		return nil, invalid("route: only a capability is routed; this job's output is a %s", rec.Kind)
	}
	if rec.Form == "head" {
		k, err := o.keyOf(ctx, st, org, rec.SHA256)
		if err != nil {
			return nil, o.fault("artifact read", org, err)
		}
		if err := o.s.State.objects.serve(ctx, k, org, rec.SHA256); err != nil {
			o.s.Log.Error("train publish: serving store", "org", org, "err", err)
			return nil, zip.Errorf(http.StatusServiceUnavailable, "object store unavailable")
		}
	}
	raw, _ := json.Marshal(rec)
	// The org's other records are read inside the write, so two publishes cannot
	// both pass the check and claim one question.
	beside := func(others []string) error {
		if rec.Route == nil {
			return nil
		}
		for _, x := range others {
			var p published
			if json.Unmarshal([]byte(x), &p) != nil || p.Route == nil || p.Name == n {
				continue
			}
			if k := clash(*rec.Route, *p.Route); k != "" {
				return conflict("route_conflict", fmt.Sprintf("capability %s already answers %q for that question", p.Name, k))
			}
		}
		return nil
	}
	if err := st.publish(ctx, r.ID, n, string(raw), beside); err != nil {
		var he *zip.HTTPError
		if errors.As(err, &he) {
			return nil, err
		}
		return nil, o.fault("publish", org, err)
	}
	if err := o.ship(org); err != nil {
		return nil, err
	}
	o.s.Log.Info("train job published", "org", org, "job", r.ID, "name", n, "forced", rec.Forced)
	r.Published = string(raw)
	return o.view(ctx, st, r)
}

// Install is one org's published capability, as the decision service
// installs it: a published job's output, or a capability's installed version.
type Install struct {
	// Org is the org it is published in, and answers for.
	Org string `json:"org"`
	// Name is its published name, one per org.
	Name string `json:"name"`
	// SHA256 is the capability artifact's: the tar its URL fetches.
	SHA256 string `json:"sha256"`
	// BaseSHA256 is the base's weights' SHA-256 it applies to.
	BaseSHA256 string `json:"base_sha256"`
	// Representation is what it reads of Kai, a capability's: when named, what it
	// binds to in place of BaseSHA256, so a release that keeps it keeps the
	// capability.
	Representation string `json:"representation,omitempty"`
	// Form is head, a head over the frozen base; calibration, the base's own
	// answer moved; pair, a boost and a veto over a noul; or full, an update to
	// the whole model.
	Form string `json:"form"`
	// Route is which questions it answers.
	Route Route `json:"route"`
	// Published is when, unix seconds.
	Published int64 `json:"published"`
	// URL fetches the artifact until Expires, unix seconds.
	URL     string `json:"url"`
	Expires int64  `json:"expires"`
}

type publishedIn struct {
	// Revision is the revision the caller holds. While it is current the answer
	// carries no capabilities and changed is false; omit it for the whole list
	// with fresh URLs.
	Revision string `json:"revision"`
}

type publishedOut struct {
	// Revision names this list: it moves when a capability is published,
	// replaced or moved to another name, and at nothing else.
	Revision string `json:"revision"`
	// Changed is false when the caller's revision is current.
	Changed bool `json:"changed"`
	// Data is every org's published capabilities, by org then name; empty when
	// nothing changed.
	Data []Install `json:"data"`
}

// capabilitiesOf is the published capabilities of the org whose namespace is ns,
// by name, read from its store: its published jobs' and, by its installed
// version, each of its capabilities (capabilities.go).
func capabilitiesOf(ctx context.Context, st *store, ns namespace.Namespace) ([]Install, error) {
	rs, err := st.published(ctx)
	if err != nil {
		return nil, err
	}
	var out []Install
	for _, r := range rs {
		var p published
		if json.Unmarshal([]byte(r.Published), &p) != nil || p.Kind != "capability" || p.Route == nil {
			continue
		}
		out = append(out, Install{Org: orgOf(r, ns), Name: p.Name, SHA256: p.SHA256, BaseSHA256: p.Base,
			Form: p.Form, Route: *p.Route, Published: p.At})
	}
	ins, err := st.installs(ctx)
	if err != nil {
		return nil, err
	}
	for _, in := range ins {
		in.Org = ns.ID()
		out = append(out, in)
	}
	slices.SortFunc(out, func(a, b Install) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// attached is a capability as ai attaches it to a decision: its name, artifact,
// base, form and route.
type attached struct {
	Name           string `json:"name"`
	SHA256         string `json:"sha256"`
	BaseSHA256     string `json:"base_sha256"`
	Representation string `json:"representation,omitempty"`
	Form           string `json:"form"`
	Route          Route  `json:"route"`
}

// exposeCapabilities publishes one org's capabilities on the internal plane, for
// ai to attach to the org's decisions. routes calls it.
func exposeCapabilities(o ops) {
	cloud.Plane().Post("/train/capabilities", o.capabilities,
		zip.WithOperationID(contract.TrainCapabilities),
		zip.WithSummary("An org's published capabilities, as its decisions carry them"))
}

// Answers the caller org's published capabilities as the decision service reads
// them on each of the org's decisions (X-Org-Capabilities): a JSON array by name,
// each its name, artifact sha256, base sha256, form and route; "[]" when the org
// has published none. The org is the caller's, from the plane, so one org never
// reads another's.
func (o ops) capabilities(ctx context.Context, _ *contract.CapabilitiesIn) (*contract.Capabilities, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("an org is required")
	}
	ns, err := principal.OrgNamespace(org, "")
	if err != nil {
		return nil, err
	}
	st, err := o.s.State.stores.For(ns)
	if err != nil {
		o.s.Log.Error("train store open failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusServiceUnavailable, "train store unavailable")
	}
	cs, err := capabilitiesOf(ctx, st, ns)
	if err != nil {
		return nil, o.fault("published read", org, err)
	}
	set := make([]attached, 0, len(cs))
	for _, c := range cs {
		set = append(set, attached{Name: c.Name, SHA256: c.SHA256, BaseSHA256: c.BaseSHA256, Representation: c.Representation,
			Form: c.Form, Route: c.Route})
	}
	raw, err := json.Marshal(set)
	if err != nil {
		return nil, o.fault("published read", org, err)
	}
	return &contract.Capabilities{Set: string(raw)}, nil
}

// listed is a capability read from its org's store, before its URL is signed.
type listed struct {
	Install
	st *store
}

// revisionOf names a list by what a poller installs from it — every field but
// the signed URL and its expiry, which differ on every read.
func revisionOf(cs []listed) string {
	h := sha256.New()
	for _, c := range cs {
		k := c.Install
		k.URL, k.Expires = "", 0
		b, _ := json.Marshal(k)
		h.Write(b)
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// ListPublishedTrainCapabilities answers every org's published capabilities —
// org, name, artifact sha256, base sha256, form (head or full), route and when —
// each with a download address that lives ten minutes: what the decision service
// polls to install them. `revision` names the list; a poll that sends the
// revision it holds is answered `changed: false` and no capabilities while it is
// current, so polling costs a read of the records and signs nothing. It reads
// every org, so it takes the platform credential (SuperAdmin) and answers anyone
// else 403. An org whose store cannot be read, or that this process holds and
// does not own, fails the answer (503) rather than being left out, because a list
// missing an org reads as that org's capabilities unpublished.
func (o ops) listPublished(ctx context.Context, in *publishedIn) (*publishedOut, error) {
	if !cloud.Super.Admits(cloud.AuthorityIn(ctx)) {
		return nil, cloud.Super.Refusal()
	}
	var all []listed
	var failed error
	_ = o.s.State.stores.Each(func(ns namespace.Namespace, st *store, err error) {
		if failed != nil {
			return
		}
		if err != nil {
			failed = err
			return
		}
		if !o.s.State.stores.Owned(ns) {
			failed = fmt.Errorf("train: %s is not this process's to read", ns)
			return
		}
		cs, err := capabilitiesOf(ctx, st, ns)
		if err != nil {
			failed = err
			return
		}
		for _, c := range cs {
			all = append(all, listed{st: st, Install: c})
		}
	})
	if failed != nil {
		o.s.Log.Error("train published: read", "err", failed)
		return nil, zip.Errorf(http.StatusServiceUnavailable, "train store unavailable")
	}
	slices.SortFunc(all, func(a, b listed) int {
		if c := strings.Compare(a.Org, b.Org); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	out := &publishedOut{Revision: revisionOf(all), Data: []Install{}}
	if in.Revision == out.Revision {
		return out, nil
	}
	out.Changed = true
	for _, c := range all {
		u, exp, err := o.download(ctx, c.st, c.Org, c.SHA256)
		if err != nil {
			o.s.Log.Error("train published: download link", "org", c.Org, "err", err)
			return nil, zip.Errorf(http.StatusServiceUnavailable, "object store unavailable")
		}
		c.URL, c.Expires = u, exp
		out.Data = append(out.Data, c.Install)
	}
	return out, nil
}
