package train

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	contract "github.com/hanzoai/cloud/client"
	peer "github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"
)

// The representation Kai's states are read by, and the one a later Kai serves; and
// the weights both were read with, which a representation outlives.
var (
	kaiBase  = strings.Repeat("a", 64)
	kaiMoved = strings.Repeat("b", 64)
	weights  = strings.Repeat("w", 64)
)

// teacherFake stands in for the decision service's POST /v1/teach: it keeps each
// lesson and answers a tar of its own count, its evaluation promoted as promote
// says.
type teacherFake struct {
	mu      sync.Mutex
	lessons []lessonBody
	promote string
}

func (f *teacherFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/decisions" {
		var body struct {
			Questions map[string]struct {
				Type     string   `json:"type"`
				Criteria []string `json:"criteria"`
				Scale    int      `json:"scale"`
			} `json:"questions"`
		}
		raw, _ := io.ReadAll(r.Body)
		if json.Unmarshal(raw, &body) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		pooled := base64.StdEncoding.EncodeToString([]byte{0, 60, 0, 188})
		ans := map[string]any{}
		qs := map[string]any{}
		for k, q := range body.Questions {
			switch q.Type {
			case "choice":
				labels := q.Criteria
				logits := make([]float64, len(labels))
				probs := make([]float64, len(labels))
				if len(labels) > 0 {
					logits[0] = 2.0
					probs[0] = 1.0
				}
				ans[k] = map[string]any{"type": "choice", "choice": labels[0], "probabilities": map[string]float64{labels[0]: 1.0}}
				qs[k] = map[string]any{"type": "choice", "labels": labels, "pooled": pooled, "logits": logits, "probs": probs}
			default:
				ans[k] = map[string]any{"type": "noul", "noul": 0.2}
				qs[k] = map[string]any{"type": "noul", "labels": []string{"false", "true"}, "pooled": pooled, "logits": []float64{1, -1}, "probs": []float64{0.8, 0.2}}
			}
		}
		capRaw, _ := json.Marshal(map[string]any{
			"sha256":         strings.Repeat("e", 64),
			"representation": kaiBase,
			"width":          2,
			"questions":      qs,
		})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "dec_cold",
			"answers": ans,
			"capture": json.RawMessage(capRaw),
		})
		return
	}
	var l lessonBody
	raw, _ := io.ReadAll(r.Body)
	if r.URL.Path != "/v1/teach" || json.Unmarshal(raw, &l) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.lessons = append(f.lessons, l)
	n, promote := len(f.lessons), f.promote
	f.mu.Unlock()
	if l.Representation != kaiBase {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"base_moved","message":"moved"}}`))
		return
	}
	tar := []byte(fmt.Sprintf("tar-%d", n))
	sum := sha256.Sum256(tar)
	ev := map[string]any{
		"base":      map[string]any{"sha256": weights, "representation": l.Representation},
		"candidate": map[string]any{"form": "head", "rank": 2, "sha256": hex.EncodeToString(sum[:]), "bytes": len(tar)},
		"route":     l.Question, "repairs": 2, "damage": 0, "fire_rate": 0.5, "promotion": promote,
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"tar": base64.StdEncoding.EncodeToString(tar), "evaluation": ev})
}

func (f *teacherFake) last() lessonBody {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lessons[len(f.lessons)-1]
}

// teaching is a rig whose decision service is fake.
func teaching(t *testing.T) (*rig, *teacherFake, ops) {
	t.Helper()
	pin(t)
	r := mount(t, "")
	f := &teacherFake{promote: "publishable"}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	r.st.teacher = &teacher{url: srv.URL, http: srv.Client()}
	r.st.keepCaptures = 30 * 24 * 3600
	return r, f, ops{s: r.svc}
}

// asked is a plane call for org: ai filing a capture, or relaying a person.
func asked(org string) context.Context {
	return zip.WithCaller(context.Background(), zip.Caller{Org: org, User: "u-" + org})
}

var intent = []string{"billing", "bug", "other"}

// captured is a decision's capture: an intent choice Kai answered billing and an
// urgent noul it answered false, over base; fired names the org's capability
// that answered the intent with bug.
func captured(base, fired string) json.RawMessage {
	pooled := base64.StdEncoding.EncodeToString([]byte{0, 60, 0, 188})
	q := map[string]any{
		"intent": map[string]any{"type": "choice", "labels": intent, "pooled": pooled, "logits": []float64{2, 1, 0}, "probs": []float64{0.7, 0.2, 0.1}},
		"urgent": map[string]any{"type": "noul", "labels": []string{"false", "true"}, "pooled": pooled, "logits": []float64{0, -1}, "probs": []float64{0.73, 0.27}},
	}
	if fired != "" {
		q["intent"].(map[string]any)["capability"] = map[string]any{"name": "triage", "sha256": fired}
		q["intent"].(map[string]any)["answered"] = []float64{0.1, 0.8, 0.1}
	}
	b, _ := json.Marshal(map[string]any{"sha256": strings.Repeat("e", 64), "representation": base, "width": 2, "questions": q})
	return b
}

func (o ops) mustCapture(t *testing.T, org, decision string, states json.RawMessage) {
	t.Helper()
	out, err := o.captureOp(asked(org), &peer.CaptureIn{Decision: decision, States: states})
	if err != nil || !out.Stored {
		t.Fatalf("capture %s: %v %+v", decision, err, out)
	}
}

func (o ops) mustCorrect(t *testing.T, org, decision, question, answer string) peer.Correction {
	t.Helper()
	c, err := o.correct(asked(org), &peer.CorrectIn{Decision: decision, Question: question, Answer: answer})
	if err != nil {
		t.Fatalf("correct %s: %v", decision, err)
	}
	return *c
}

// status is the HTTP status a plane refusal carries.
func status(err error) int {
	var he *zip.HTTPError
	if e, ok := err.(*zip.HTTPError); ok {
		he = e
	}
	if he == nil {
		return 0
	}
	return he.Status
}

// installsOf is what the published list carries for org, by name: each sha256.
func (r *rig) installsOf(org string) map[string]string {
	r.t.Helper()
	m := r.must(200, "GET", "/v1/train/published", "hanzo", nil)
	out := map[string]string{}
	for _, d := range m["data"].([]any) {
		e := d.(map[string]any)
		if e["org"] == org {
			out[e["name"].(string)] = e["sha256"].(string)
		}
	}
	return out
}

// One correction to rollback: corrections of captured decisions teach a capability
// of their question on its cached states, its version is publishable only when its
// evaluation passed, publishing installs it in the published set the decision
// service reads, a retrained version is a new one, rollback puts exactly the
// version before it back, and disable takes it out.
func TestACapabilityIsTaughtInstalledAndRolledBack(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r, f, o := teaching(t)
		r.must(200, "PUT", "/v1/capabilities/capture", "acme", map[string]any{"on": true})
		for i := range 6 {
			o.mustCapture(t, "acme", fmt.Sprintf("dec_%d", i), captured(kaiBase, ""))
		}
		var ids []string
		for i := range 3 {
			c := o.mustCorrect(t, "acme", fmt.Sprintf("dec_%d", i), "intent", "bug")
			if c.State != "captured" || c.Base != "billing" || c.Type != "choice" {
				t.Fatalf("correction %+v, want captured over Kai's billing", c)
			}
			ids = append(ids, c.ID)
		}
		// what Kai answered is no correction, nor is an option the question lacks, nor a second one
		if _, err := o.correct(asked("acme"), &peer.CorrectIn{Decision: "dec_4", Question: "intent", Answer: "billing"}); status(err) != 409 {
			t.Fatalf("Kai's own answer filed as a correction: %v", err)
		}
		if _, err := o.correct(asked("acme"), &peer.CorrectIn{Decision: "dec_4", Question: "intent", Answer: "spam"}); status(err) != 400 {
			t.Fatalf("an answer the question does not offer filed: %v", err)
		}
		if _, err := o.correct(asked("acme"), &peer.CorrectIn{Decision: "dec_0", Question: "intent", Answer: "other"}); status(err) != 409 {
			t.Fatalf("a second correction of one answer filed: %v", err)
		}
		// a decision made with capture off is kept, and cannot teach
		if c := o.mustCorrect(t, "acme", "dec_off", "intent", "bug"); c.State != "missing" {
			t.Fatalf("an uncaptured decision's correction is %s, want missing", c.State)
		}
		// a decision's corrections come with what it captured: each question's options and
		// the label the caller was answered, what a correction from a trace names
		dc, err := o.decisionCorrections(asked("acme"), &peer.DecisionIn{Decision: "dec_0"})
		if err != nil || len(dc.Data) != 1 || len(dc.Questions) != 2 || dc.Questions[0].Key != "intent" ||
			dc.Questions[0].Answered != "billing" || len(dc.Questions[0].Labels) != 3 {
			t.Fatalf("dec_0's corrections %+v: %v", dc, err)
		}
		m := r.must(200, "GET", "/v1/capabilities/corrections", "acme", nil)
		if n := len(m["data"].([]any)); n != 4 {
			t.Fatalf("corrections = %d, want 4", n)
		}
		if qs := m["questions"].([]any); len(qs) != 1 || qs[0].(map[string]any)["corrections"].(float64) != 3 {
			t.Fatalf("questions = %v, want intent's 3", qs)
		}
		m = r.must(201, "POST", "/v1/capabilities", "acme", map[string]any{"name": "triage", "corrections": ids})
		if m["state"] != draft || str(m, "question", "key") != "intent" {
			t.Fatalf("created %v", m)
		}
		if code, _ := r.do("POST", "/v1/capabilities", "acme", map[string]any{"name": "other", "corrections": ids[:1]}); code != 409 {
			t.Fatalf("a second capability of one question: %d, want 409", code)
		}
		if len(r.installsOf("acme")) != 0 {
			t.Fatal("a draft is in the published set")
		}

		m = r.must(200, "POST", "/v1/capabilities/triage/train", "acme", map[string]any{})
		if m["state"] != publishable || m["version"].(float64) != 1 {
			t.Fatalf("trained %v, want version 1 publishable", m)
		}
		l := f.last()
		targets, mine, others := 0, 0, 0
		for _, e := range l.Rows {
			switch {
			case e.Key == "intent" && e.Answer != nil && *e.Answer == "bug":
				targets++
			case e.Key == "intent" && e.Answer == nil:
				mine++
			case e.Key == "urgent":
				others++
			}
		}
		if l.Representation != kaiBase || l.Question.Key != "intent" || targets != 3 || mine != 3 || others != 6 {
			t.Fatalf("lesson over %s: %d targets, %d of Kai's own, %d others; want 3, 3, 6", l.Representation, targets, mine, others)
		}
		v1 := m["versions"].([]any)[0].(map[string]any)
		if _, ok := v1["evaluation"].(map[string]any)["cost_micro_usd"]; !ok || v1["representation"] != kaiBase || v1["base_sha256"] != weights {
			t.Fatalf("version %v, want its evaluation's cost and its base", v1)
		}

		r.must(200, "POST", "/v1/capabilities/triage/publish", "acme", map[string]any{})
		if got := r.installsOf("acme"); got["triage"] != v1["sha256"] {
			t.Fatalf("published set %v, want triage at version 1", got)
		}
		// copied where Kai fetches it, by content address, and in the set ai attaches
		if r.objs.served["capabilities/acme/"+v1["sha256"].(string)] != v1["sha256"] {
			t.Fatalf("serving store %v, want acme's version 1", r.objs.served)
		}
		set, err := o.capabilities(asked("acme"), &contract.CapabilitiesIn{})
		if err != nil || !strings.Contains(set.Set, `"name":"triage"`) || !strings.Contains(set.Set, v1["sha256"].(string)) ||
			!strings.Contains(set.Set, `"representation":"`+kaiBase+`"`) {
			t.Fatalf("acme's attached set %v: %v", set, err)
		}
		if other, _ := o.capabilities(asked("globex"), &contract.CapabilitiesIn{}); other.Set != "[]" {
			t.Fatalf("globex's set %s carries acme's", other.Set)
		}
		m = r.must(200, "POST", "/v1/capabilities/triage/train", "acme", map[string]any{})
		if m["state"] != installed || m["installed"].(float64) != 1 || m["version"].(float64) != 2 {
			t.Fatalf("retrained %v, want version 2 beside installed version 1", m)
		}
		r.must(200, "POST", "/v1/capabilities/triage/publish", "acme", map[string]any{"version": 2})
		v2 := r.installsOf("acme")["triage"]
		m = r.must(200, "POST", "/v1/capabilities/triage/rollback", "acme", nil)
		if m["installed"].(float64) != 1 || r.installsOf("acme")["triage"] != v1["sha256"] || v2 == v1["sha256"] {
			t.Fatalf("rolled back to %v, want version 1's artifact exactly", m["installed"])
		}
		if code, _ := r.do("POST", "/v1/capabilities/triage/rollback", "acme", nil); code != 409 {
			t.Fatalf("a rollback past the first installed version: %d, want 409", code)
		}

		// live: a fire that changed the label and no correction contradicts is a repair;
		// one whose label a correction puts back to Kai's is damage
		o.mustCapture(t, "acme", "dec_live", captured(kaiBase, v1["sha256"].(string)))
		o.mustCapture(t, "acme", "dec_hurt", captured(kaiBase, v1["sha256"].(string)))
		// the person was answered bug, the capability's; billing, Kai's own, is right
		if c := o.mustCorrect(t, "acme", "dec_hurt", "intent", "billing"); c.Base != "bug" {
			t.Fatalf("a damage report says Kai answered %s, want the capability's bug", c.Base)
		}
		m = r.must(200, "GET", "/v1/capabilities/triage", "acme", nil)
		live := m["live"].(map[string]any)
		if m["state"] != active || live["fires"].(float64) != 2 || live["repairs"].(float64) != 1 || live["damage"].(float64) != 1 || live["version"].(float64) != 1 {
			t.Fatalf("live %v in %s, want 2 fires, a repair and a damage, active", live, m["state"])
		}

		if code, _ := r.do("DELETE", "/v1/capabilities/triage", "acme", nil); code != 409 {
			t.Fatalf("an installed capability deleted: %d", code)
		}
		m = r.must(200, "POST", "/v1/capabilities/triage/disable", "acme", nil)
		if m["state"] != disabled || len(r.installsOf("acme")) != 0 {
			t.Fatalf("disabled %v, published %v", m["state"], r.installsOf("acme"))
		}
		r.must(200, "DELETE", "/v1/capabilities/triage", "acme", nil)
	})
}

// A version whose evaluation did not pass is never publishable, and a base that
// moved under an installed version drops it from the set until it is taught again.
func TestOnlyAPassingVersionOverTheServedBaseIsInstalled(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r, f, o := teaching(t)
		r.must(200, "PUT", "/v1/capabilities/capture", "acme", map[string]any{"on": true})
		for i := range 4 {
			o.mustCapture(t, "acme", fmt.Sprintf("dec_%d", i), captured(kaiBase, ""))
		}
		c := o.mustCorrect(t, "acme", "dec_0", "intent", "bug")
		r.must(201, "POST", "/v1/capabilities", "acme", map[string]any{"name": "triage", "corrections": []string{c.ID}})
		f.promote = "rejected"
		m := r.must(200, "POST", "/v1/capabilities/triage/train", "acme", map[string]any{})
		if m["state"] != evaluated {
			t.Fatalf("a failed gate left %s, want evaluated", m["state"])
		}
		if code, _ := r.do("POST", "/v1/capabilities/triage/publish", "acme", map[string]any{}); code != 409 {
			t.Fatalf("a version that failed its gate published: %d", code)
		}
		f.promote = "publishable"
		r.must(200, "POST", "/v1/capabilities/triage/train", "acme", map[string]any{})
		r.must(200, "POST", "/v1/capabilities/triage/publish", "acme", map[string]any{})
		if len(r.installsOf("acme")) != 1 {
			t.Fatal("a passing version is not in the set")
		}
		// Kai serves another base: the capability leaves the set and says why
		o.mustCapture(t, "acme", "dec_new", captured(kaiMoved, ""))
		m = r.must(200, "GET", "/v1/capabilities/triage", "acme", nil)
		if m["state"] != validating || m["installed"].(float64) != 0 || len(r.installsOf("acme")) != 0 {
			t.Fatalf("after the base moved: %s, installed %v, set %v", m["state"], m["installed"], r.installsOf("acme"))
		}
		if code, body := r.do("POST", "/v1/capabilities/triage/publish", "acme", map[string]any{}); code != 409 {
			t.Fatalf("a version over another base published: %d %v", code, body)
		}
		// its correction's state was captured over the old base: it cannot teach the new one
		if code, _ := r.do("POST", "/v1/capabilities/triage/train", "acme", map[string]any{}); code != 409 {
			t.Fatalf("a lesson over states of another base: %d, want 409", code)
		}
	})
}

// An org's captures, corrections and capabilities are its own: another org sees
// none of them, its correction of the same decision id lands in its own store,
// and a capture for an org that does not capture is not kept.
func TestCapabilitiesAreTheOrgsOwn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r, _, o := teaching(t)
		r.must(200, "PUT", "/v1/capabilities/capture", "acme", map[string]any{"on": true})
		o.mustCapture(t, "acme", "dec_0", captured(kaiBase, ""))
		if out, err := o.captureOp(asked("globex"), &peer.CaptureIn{Decision: "dec_g", States: captured(kaiBase, "")}); err != nil || out.Stored {
			t.Fatalf("an org that does not capture kept a capture: %v %+v", err, out)
		}
		c := o.mustCorrect(t, "acme", "dec_0", "intent", "bug")
		if g := o.mustCorrect(t, "globex", "dec_0", "intent", "bug"); g.State != "missing" {
			t.Fatalf("globex's correction of acme's decision reads acme's capture: %+v", g)
		}
		m := r.must(200, "GET", "/v1/capabilities/corrections", "globex", nil)
		for _, d := range m["data"].([]any) {
			if d.(map[string]any)["id"] == c.ID {
				t.Fatal("globex lists acme's correction")
			}
		}
		r.must(201, "POST", "/v1/capabilities", "acme", map[string]any{"name": "triage", "corrections": []string{c.ID}})
		if code, _ := r.do("GET", "/v1/capabilities/triage", "globex", nil); code != 404 {
			t.Fatalf("globex reads acme's capability: %d", code)
		}
		if code, _ := r.do("POST", "/v1/capabilities", "globex", map[string]any{"name": "triage", "corrections": []string{c.ID}}); code != 404 {
			t.Fatalf("globex taught from acme's correction: %d", code)
		}
	})
}

// Cold-start authoring from (state, answer) examples encodes via Kai, synthesizes
// baseline preservation witnesses, trains in one call, serves weights, and installs
// across organizations over theshared representation hash.
func TestColdStartCapabilityFromExamplesAndCrossOrgInstall(t *testing.T) {
	r, _, o := teaching(t)
	created := r.must(201, "POST", "/v1/capabilities", "acme", map[string]any{
		"name":     "cold-triage",
		"question": map[string]any{"key": "intent", "type": "choice", "labels": intent},
		"examples": []map[string]any{
			{"state": "login button throws 500 error", "answer": "bug", "note": "crash"},
			{"state": "dark mode crashes on settings", "answer": "bug"},
		},
		"train": true,
	})
	if created["state"] != publishable || created["version"].(float64) != 1 {
		t.Fatalf("cold-start created %v, want publishable version 1", created)
	}
	w := r.must(200, "GET", "/v1/capabilities/cold-triage/weights", "acme", nil)
	sha := w["sha256"].(string)
	if w["representation"] != kaiBase || w["tar"] == "" || sha == "" {
		t.Fatalf("weights %v, want representation %s and non-empty tar", w, kaiBase)
	}
	r.must(200, "POST", "/v1/capabilities/cold-triage/publish", "acme", map[string]any{})

	owned, err := o.capabilityOwnedOp(asked("acme"), &peer.CapabilityOwnedIn{Name: "cold-triage"})
	if err != nil || !owned.Mine || owned.SHA256 != sha || owned.Representation != kaiBase {
		t.Fatalf("capabilityOwnedOp = %+v, err %v", owned, err)
	}

	ref := peer.CapabilityResource("acme", sha)
	inst, err := o.capabilityInstallOp(asked("globex"), &peer.CapabilityInstallIn{
		Ref:  ref,
		Name: "cold-triage",
		On:   true,
	})
	if err != nil || !inst.Installed || inst.SHA256 != sha {
		t.Fatalf("capabilityInstallOp = %+v, err %v", inst, err)
	}
	if got := r.installsOf("globex"); got["cold-triage"] != sha {
		t.Fatalf("globex published set %v, want cold-triage at %s", got, sha)
	}
	if r.objs.served["capabilities/globex/"+sha] != sha {
		t.Fatalf("globex serving store %v, want %s", r.objs.served, sha)
	}
	cw, err := o.capabilityWeightsOp(asked("globex"), &peer.CapabilityWeightsIn{Ref: ref, Name: "cold-triage"})
	if err != nil || cw.SHA256 != sha || cw.Tar == "" {
		t.Fatalf("capabilityWeightsOp = %+v, err %v", cw, err)
	}
}
