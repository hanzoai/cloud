package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
)

// model is the model plane as a test sees it: deps.AI, the same interface the
// metered client in production satisfies. It cites the facts that mention
// `about` and the first finding it is handed, and records every request so a
// test can hold the org to paying for them.
type model struct {
	about string
	// mapReply and reduceReply, when set, replace what it would have said.
	mapReply, reduceReply string
	// mapErr and reduceErr fail the call instead.
	mapErr, reduceErr error

	mu   sync.Mutex
	seen []*infer.ChatRequest
}

var label = regexp.MustCompile(`(?m)^\[(\d+)\] (.*)$`)

func (m *model) ChatCompletion(_ context.Context, req *infer.ChatRequest) (*infer.ChatResponse, error) {
	m.mu.Lock()
	m.seen = append(m.seen, req)
	m.mu.Unlock()

	if strings.HasPrefix(req.Prompt, "These are facts") {
		if m.mapErr != nil {
			return nil, m.mapErr
		}
		if m.mapReply != "" {
			return &infer.ChatResponse{Content: m.mapReply}, nil
		}
		var cite []string
		for _, l := range label.FindAllStringSubmatch(req.Prompt, -1) {
			if strings.Contains(l[2], m.about) {
				cite = append(cite, l[1])
			}
		}
		if len(cite) == 0 {
			return &infer.ChatResponse{Content: `{"findings":[]}`}, nil
		}
		return &infer.ChatResponse{Content: fmt.Sprintf(
			"```json\n{\"findings\":[{\"text\":\"%s is involved\",\"score\":80,\"facts\":[%s]}]}\n```",
			m.about, strings.Join(cite, ","))}, nil
	}
	if m.reduceErr != nil {
		return nil, m.reduceErr
	}
	if m.reduceReply != "" {
		return &infer.ChatResponse{Content: m.reduceReply}, nil
	}
	return &infer.ChatResponse{Content: `{"answer":"` + m.about + ` owns it.","findings":[1]}`}, nil
}

func (m *model) Embed(context.Context, *infer.EmbedRequest) ([][]float32, error) { return nil, nil }
func (m *model) Rerank(context.Context, *infer.RerankRequest) ([]float64, error) { return nil, nil }

func (m *model) calls() []*infer.ChatRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.seen)
}

// org files two services, each with an owner, as two communities.
func org(t *testing.T, app *zip.App) {
	t.Helper()
	assertFact(t, app, "", "acme/svc/billing", "depends", "acme/svc/ledger", true)
	assertFact(t, app, "", "acme/svc/billing", "owner", "acme/team/money", true)
	assertFact(t, app, "", "acme/svc/billing", "tier", "1", false)
	assertFact(t, app, "", "acme/svc/search", "depends", "acme/svc/index", true)
	assertFact(t, app, "", "acme/svc/search", "owner", "acme/team/find", true)
}

// question posts a question and returns the status and body.
func question(t *testing.T, app *zip.App, q string) (int, []byte) {
	t.Helper()
	return call(t, app, http.MethodPost, "/v1/graph/answer", "", map[string]any{"question": q})
}

// TestAnAnswerCitesRowsOfThisGraph is the contract: the answer rests on
// assertions a read of this graph returns, the community it names is the one
// they live in, and the org whose graph it is pays for every call.
func TestAnAnswerCitesRowsOfThisGraph(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		m := &model{about: "acme/team/money"}
		app := mount(t, cloud.Deps{AI: m})
		org(t, app)

		code, b := question(t, app, "Who owns billing?")
		if code != http.StatusOK {
			t.Fatalf("answer: status %d: %s", code, b)
		}
		var out graphAnswerOut
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("answer answered something that is not its shape: %v: %s", err, b)
		}
		if !out.Grounded || out.Answer != "acme/team/money owns it." {
			t.Fatalf("grounded=%v answer=%q", out.Grounded, out.Answer)
		}

		owner := answered(t, app, "", "/v1/graph?entity=acme/svc/billing&relation=owner")
		if len(owner) != 1 {
			t.Fatalf("read back %d owner assertions, want 1", len(owner))
		}
		if !slices.Equal(out.Assertions, []string{owner[0].ID}) {
			t.Errorf("cited %v, want exactly the owner assertion %s", out.Assertions, owner[0].ID)
		}
		if len(out.Findings) != 1 || !slices.Equal(out.Findings[0].Assertions, out.Assertions) {
			t.Errorf("findings = %+v", out.Findings)
		}

		// The community it names is the one billing is in, by the same numbering
		// the communities endpoint answers with.
		code, b = call(t, app, http.MethodPost, "/v1/graph/communities", "", map[string]any{"as_of": out.AsOf})
		if code != http.StatusOK {
			t.Fatalf("communities: status %d: %s", code, b)
		}
		var cs graphCommunitiesOut
		if err := json.Unmarshal(b, &cs); err != nil {
			t.Fatalf("communities: %v", err)
		}
		if len(out.Communities) != 1 || !slices.Contains(cs.Communities[out.Communities[0]].Members, "acme/svc/billing") {
			t.Errorf("cited community %v, which does not hold billing: %+v", out.Communities, cs.Communities)
		}

		// Every call — both maps, if the batches split, and the reduce — was made
		// for this org and billed to it, under a stated ceiling.
		calls := m.calls()
		if out.Calls != len(calls) || len(calls) < 2 {
			t.Fatalf("the answer reports %d calls; the model saw %d", out.Calls, len(calls))
		}
		for _, r := range calls {
			if r.Org != "acme" || r.Payer != account.Org("acme") || r.MaxTokens != replyMax {
				t.Errorf("a call went out as org=%q payer=%q max=%d", r.Org, r.Payer.Subject(), r.MaxTokens)
			}
		}
		if out.Asked != 2 || out.Skipped != 0 || out.Clipped {
			t.Errorf("asked=%d skipped=%d clipped=%v, want both communities read whole", out.Asked, out.Skipped, out.Clipped)
		}
	})
}

// TestNothingRelevantIsNotAnAnswer: when no community holds a fact bearing on
// the question, the plane says so and buys no answer to reduce from nothing.
func TestNothingRelevantIsNotAnAnswer(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		m := &model{about: "acme/team/nobody"}
		app := mount(t, cloud.Deps{AI: m})
		org(t, app)

		code, b := question(t, app, "Who owns payroll?")
		if code != http.StatusOK {
			t.Fatalf("answer: status %d: %s", code, b)
		}
		var out graphAnswerOut
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("answer: %v", err)
		}
		if out.Grounded || out.Answer != "" || len(out.Assertions) != 0 {
			t.Errorf("an answer from nothing: %+v", out)
		}
		for _, r := range m.calls() {
			if !strings.HasPrefix(r.Prompt, "These are facts") {
				t.Error("an answer was asked for with no finding to answer from")
			}
		}
	})
}

// TestFindingsThatDoNotAnswerAreNotAnAnswer: the model found something, and
// what it found does not settle the question. That is said, not dressed up.
func TestFindingsThatDoNotAnswerAreNotAnAnswer(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		m := &model{about: "acme/team/money", reduceReply: `{"answer":"","findings":[]}`}
		app := mount(t, cloud.Deps{AI: m})
		org(t, app)

		code, b := question(t, app, "Who owns billing?")
		if code != http.StatusOK {
			t.Fatalf("answer: status %d: %s", code, b)
		}
		var out graphAnswerOut
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("answer: %v", err)
		}
		if out.Grounded || out.Answer != "" || len(out.Assertions) != 0 || out.Calls != 2 {
			t.Errorf("findings that answer nothing came back as %+v", out)
		}
	})
}

// TestAFailedCallIsNoAnswer: a model call that fails, or answers in something
// other than what it was asked for, or cites what it was never handed, fails
// the request. None of them may come back as an answer.
func TestAFailedCallIsNoAnswer(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for name, m := range map[string]*model{
			"a map call errs":         {about: "acme/team/money", mapErr: errors.New("upstream 500")},
			"a map reply is not JSON": {about: "acme/team/money", mapReply: "billing is owned by money"},
			"the reduce call errs":    {about: "acme/team/money", reduceErr: errors.New("upstream 500")},
			"the answer cites nothing it was handed": {about: "acme/team/money",
				reduceReply: `{"answer":"money owns it","findings":[7]}`},
		} {
			t.Run(name, func(t *testing.T) {
				app := mount(t, cloud.Deps{AI: m})
				org(t, app)
				code, b := question(t, app, "Who owns billing?")
				if code != http.StatusBadGateway {
					t.Fatalf("status %d, want 502: %s", code, b)
				}
				if strings.Contains(string(b), "owns it") || strings.Contains(string(b), "upstream") {
					t.Errorf("a failed answer carried an answer or the plane's internals: %s", b)
				}
			})
		}
	})
}

// TestAPayerOutOfFundsIsRefusedAsMoney: the meter in front of the model refuses
// before tokens are spent, and the caller reads the money wire's 402 — the same
// bytes every Hanzo surface refuses with — rather than a model failure.
func TestAPayerOutOfFundsIsRefusedAsMoney(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		m := &model{about: "acme/team/money", mapErr: fmt.Errorf("gate: %w", meter.ErrInsufficientBalance)}
		app := mount(t, cloud.Deps{AI: m})
		org(t, app)

		code, b := question(t, app, "Who owns billing?")
		if code != http.StatusPaymentRequired {
			t.Fatalf("status %d, want 402: %s", code, b)
		}
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(b, &body); err != nil || body.Error.Code != "insufficient_balance" {
			t.Errorf("the refusal is not the money wire's: %s", b)
		}
	})
}

// TestNoModelPlaneRefusesTheAnswerOnly: a deployment with no model plane
// refuses an answer by name and still partitions.
func TestNoModelPlaneRefusesTheAnswerOnly(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountGraph(t)
		org(t, app)
		if code, b := question(t, app, "Who owns billing?"); code != http.StatusServiceUnavailable {
			t.Errorf("answer with no model plane: status %d, want 503: %s", code, b)
		}
		if code, b := call(t, app, http.MethodPost, "/v1/graph/communities", "", map[string]any{}); code != http.StatusOK {
			t.Errorf("communities with no model plane: status %d: %s", code, b)
		}
	})
}

// TestAQuestionIsText: punctuation in a question is text, never full-text
// syntax, and an empty or oversized question is refused by name.
func TestAQuestionIsText(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		m := &model{about: "acme/team/money"}
		app := mount(t, cloud.Deps{AI: m})
		org(t, app)
		for _, q := range []string{`"billing" OR -owner* ^:`, "?", `who's NEAR(billing)`} {
			if code, b := question(t, app, q); code != http.StatusOK {
				t.Errorf("question %q: status %d: %s", q, code, b)
			}
		}
		if code, _ := question(t, app, "  "); code != http.StatusBadRequest {
			t.Errorf("an empty question: status %d, want 400", code)
		}
		if code, _ := question(t, app, strings.Repeat("x", questionMax+1)); code != http.StatusBadRequest {
			t.Errorf("an oversized question: status %d, want 400", code)
		}
	})
}

// TestTheQuestionAimsTheFanOut: the community the question's words find is read
// first, whatever its size, and within a community the entity they found is
// read before its hubs.
func TestTheQuestionAimsTheFanOut(t *testing.T) {
	p := detect(append(clique("big1", "big2", "big3", "big4"), clique("small1", "small2")...))
	ranked, named := order(p, []string{"small2"})
	if !slices.Equal(ranked, []int{1, 0}) {
		t.Errorf("order = %v, want the matched community first", ranked)
	}
	if i, _ := slices.BinarySearch(p.keys, "small2"); named[int32(i)] != 0 || len(named) != 1 {
		t.Errorf("named = %v, want small2 alone, first", named)
	}
	if ranked, _ := order(p, nil); !slices.Equal(ranked, []int{0, 1}) {
		t.Errorf("order with no match = %v, want largest first", ranked)
	}
}

// TestWhatTheQuestionNamesSurvivesTheBudget: a community too large for one call
// is cut, and the cut keeps the entity the question named even when it is the
// least connected member.
func TestWhatTheQuestionNamesSurvivesTheBudget(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		ctx := context.Background()
		var facts []Fact
		// A hub with far more facts than one call carries, and a leaf hanging off it.
		for i := range 400 {
			facts = append(facts, mk(t, "acme/hub", "depends", fmt.Sprintf("acme/dep/%03d-with-a-long-enough-key", i), true, "deploy", t0))
		}
		facts = append(facts, mk(t, "acme/leaf", "depends", "acme/hub", true, "deploy", t0))
		facts = append(facts, mk(t, "acme/leaf", "owner", "acme/team/leaf", false, "deploy", t0))
		if _, err := s.record(ctx, facts); err != nil {
			t.Fatalf("record: %v", err)
		}
		asOf := t0.Add(time.Hour)
		p, err := partitionAt(ctx, s, "", point(asOf))
		if err != nil {
			t.Fatalf("partition: %v", err)
		}
		hits, err := s.rank(ctx, "leaf owner", point(asOf))
		if err != nil {
			t.Fatalf("rank: %v", err)
		}
		ranked, named := order(p, hits)
		batches, _, clipped, err := gather(ctx, s, p, ranked, named, point(asOf))
		if err != nil {
			t.Fatalf("gather: %v", err)
		}
		if !clipped {
			t.Fatal("a community over the budget was not reported clipped; this test no longer cuts")
		}
		if !slices.ContainsFunc(batches[0].facts, func(h given) bool { return h.Entity == "acme/leaf" && h.Relation == "owner" }) {
			t.Error("the cut dropped the entity the question named")
		}
	})
}
