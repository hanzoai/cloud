package agents

// attest_test.go — a fact filed in a chat turn cites the message it was said in,
// and only a public channel files one.
//
// Asserted on what the graph DECODES, through every spelling of the call, and
// then end to end: a turn whose model files a fact leaves a stored assertion
// carrying the Slack message as its source and evidence and the linked person as
// its author, and a turn in a closed channel leaves nothing.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/graph"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/zap-proto/zip"
)

var (
	// posted is a message in a thread of a public channel (the thread is
	// replyto_test.go's).
	posted       = client.Origin{Account: "t024be7ld", Message: "1726459260.002100", Public: true}
	saidSource   = "slack://t024be7ld/C024BE91L/1726459260.002100"
	saidEvidence = "https://slack.com/archives/C024BE91L/p1726459260002100?thread_ts=1726459200.001900&cid=C024BE91L"
	// fact is one assertion as a model writes it, naming a source of its own.
	fact = `{"entity":"project:atlas","relation":"owner","value":"dana","at":"2026-01-01T00:00:00Z",` +
		`"source":"the model's own idea","evidence":"https://example.com/made-up"}`
	// forged names its source and evidence again under keys a case-insensitive
	// decoder reads as the same fields: ſ (U+017F) folds to s.
	forged = strings.TrimSuffix(fact, "}") + `,"Source":"upper","ſource":"forged-longs","EVIDENCE":"https://example.com/forged"}`
	// ingest is a graphIngest a model writes, naming a source of its own.
	ingest = `{"op":"graphIngest","input":{"source":"https://example.com/made-up",` +
		`"at":"2026-01-01T00:00:00Z","text":"# project:ingested\nowner:: dana"}}`
)

// cited is what the graph op receives: each member's source and evidence, the
// envelope opened the way the MCP server opens it. A member that is not an
// object reads as empty.
func cited(args string) (source, evidence, entity []string) {
	var env struct {
		Op    string          `json:"op"`
		Input json.RawMessage `json:"input"`
	}
	body := args
	if json.Unmarshal([]byte(args), &env) == nil && env.Op != "" {
		body = string(env.Input)
	}
	var in struct {
		Assertions []json.RawMessage `json:"assertions"`
	}
	_ = json.Unmarshal([]byte(body), &in)
	for _, raw := range in.Assertions {
		var f struct{ Source, Evidence, Entity string }
		_ = json.Unmarshal(raw, &f)
		source, evidence, entity = append(source, f.Source), append(evidence, f.Evidence), append(entity, f.Entity)
	}
	return source, evidence, entity
}

// A fact filed in a turn cites that turn's message — in every spelling of the
// call, for every member of the batch, over whatever source the model named, and
// never for a call that files nothing.
func TestAFiledFactCitesTheTurnsMessage(t *testing.T) {
	for name, tc := range map[string]struct {
		to             client.Reply
		from           client.Origin
		tool, args     string
		source, proves []string
	}{
		"grouped — the shape every Slack turn takes": {
			to: thread, from: posted, tool: "graph", args: `{"op":"graphAssert","input":{"assertions":[` + fact + `]}}`,
			source: []string{saidSource}, proves: []string{saidEvidence},
		},
		"flat, every member of the batch": {
			to: thread, from: posted, tool: "graphAssert", args: `{"assertions":[` + fact + `,` + fact + `]}`,
			source: []string{saidSource, saidSource}, proves: []string{saidEvidence, saidEvidence},
		},
		"a member that is not an object is the graph's to refuse, and the rest are stamped": {
			to: thread, from: posted, tool: "graphAssert", args: `{"assertions":["junk",` + fact + `]}`,
			source: []string{"", saidSource}, proves: []string{"", saidEvidence},
		},
		"the envelope in another case": {
			to: thread, from: posted, tool: "graph", args: `{"Op":"graphAssert","Input":{"assertions":[` + fact + `]}}`,
			source: []string{saidSource}, proves: []string{saidEvidence},
		},
		"the batch in another case": {
			to: thread, from: posted, tool: "graph", args: `{"op":"graphAssert","input":{"Assertions":[` + fact + `]}}`,
			source: []string{saidSource}, proves: []string{saidEvidence},
		},
		"a member naming its source by a key the decoder folds": {
			to: thread, from: posted, tool: "graphAssert", args: `{"assertions":[` + forged + `]}`,
			source: []string{saidSource}, proves: []string{saidEvidence},
		},
		"said at the top level: the permalink names no thread": {
			to: channel, from: posted, tool: "graphAssert", args: `{"assertions":[` + fact + `]}`,
			source: []string{saidSource},
			proves: []string{"https://slack.com/archives/C024BE91L/p1726459260002100"},
		},
		"a slash command answers no message: the channel, and no permalink": {
			to: channel, from: client.Origin{Account: "t024be7ld", Public: true},
			tool: "graphAssert", args: `{"assertions":[` + fact + `]}`,
			source: []string{"slack://t024be7ld/C024BE91L"}, proves: []string{""},
		},
		"a turn with no conversation behind it keeps the caller's source": {
			to: client.Reply{}, tool: "graphAssert", args: `{"assertions":[` + fact + `]}`,
		},
		"another graph op files nothing": {
			to: thread, from: posted, tool: "graph", args: `{"op":"graphResolve","input":{"entity":"project:atlas","relation":"owner"}}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := attest(withReply(context.Background(), tc.to, tc.from), tc.tool, tc.args)
			if err != nil {
				t.Fatalf("a filing from a public channel was refused: %v", err)
			}
			if tc.source == nil {
				if got != tc.args {
					t.Fatalf("a call with nothing to cite was rewritten: %s", got)
				}
				return
			}
			source, evidence, entity := cited(got)
			if strings.Join(source, "|") != strings.Join(tc.source, "|") {
				t.Fatalf("the graph receives source %q, want %q (dispatched %s)", source, tc.source, got)
			}
			if strings.Join(evidence, "|") != strings.Join(tc.proves, "|") {
				t.Fatalf("the graph receives evidence %q, want %q (dispatched %s)", evidence, tc.proves, got)
			}
			// The model's own fact survives the stamp: a citation on an empty
			// assertion files nothing.
			if entity[len(entity)-1] != "project:atlas" {
				t.Fatalf("the model's assertion did not survive: %s", got)
			}
		})
	}
}

// graphIngest names the source of every assertion it records, so a turn's
// ingest is named for the turn's message, over the model's, in every case the
// decoder folds.
func TestAnIngestIsNamedForTheTurnsMessage(t *testing.T) {
	for name, args := range map[string]string{
		"grouped":         ingest,
		"a folded key":    strings.Replace(ingest, `"source"`, `"SOURCE"`, 1),
		"a folded letter": strings.Replace(ingest, `"source"`, `"ſource"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := attest(withReply(context.Background(), thread, posted), "graph", args)
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			var in struct {
				Input struct{ Source, Text string } `json:"input"`
			}
			if err := json.Unmarshal([]byte(got), &in); err != nil || in.Input.Source != saidSource || in.Input.Text == "" {
				t.Fatalf("the graph receives %+v, want source %s and the model's text (dispatched %s)", in.Input, saidSource, got)
			}
		})
	}
}

// The graph is read by the whole organization, so a channel it cannot read files
// nothing — by either op, in any spelling — and still reads.
func TestAClosedChannelFilesNothing(t *testing.T) {
	for name, from := range map[string]client.Origin{
		"a DM or a private channel":  {Account: "t024be7ld", Message: "1726459260.002100"},
		"a bridge that does not say": {},
	} {
		for call, args := range map[string]string{
			"graph, assert": `{"op":"graphAssert","input":{"assertions":[` + fact + `]}}`,
			"graph, folded": `{"Op":"graphAssert","Input":{"assertions":[` + fact + `]}}`,
			"graph, ingest": ingest,
		} {
			t.Run(name+"/"+call, func(t *testing.T) {
				got, err := attest(withReply(context.Background(), thread, from), "graph", args)
				if !errors.Is(err, errUnshared) || got != "" {
					t.Fatalf("a closed channel filed %s (err %v)", got, err)
				}
			})
		}
		t.Run(name+"/flat", func(t *testing.T) {
			if _, err := attest(withReply(context.Background(), thread, from), "graphAssert", `{"assertions":[`+fact+`]}`); !errors.Is(err, errUnshared) {
				t.Fatalf("a closed channel filed a flat assert (err %v)", err)
			}
		})
		t.Run(name+"/a read", func(t *testing.T) {
			args := `{"op":"graphResolve","input":{"entity":"project:atlas","relation":"owner"}}`
			if got, err := attest(withReply(context.Background(), thread, from), "graph", args); err != nil || got != args {
				t.Fatalf("a read from a closed channel was changed or refused: %s (err %v)", got, err)
			}
		})
	}
}

// The whole path: a Slack turn's model files a fact through the grouped `graph`
// tool, the real graph stores it, and reading it back says where it was said and
// who said it — in every spelling the server and the graph decode as a filing.
// The same call in a closed channel leaves the graph without it.
//
// It runs on SQLite alone: the graph is a store of its own (FTS5), not yet ported to
// hanzo-sql, and with the DSN set the seam refuses to open it.
func TestATurnFilesAFactWithItsProvenance(t *testing.T) {
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	edge, plane := bounded(t, "graph", client.Published("graph"), func(app *zip.App) {
		if err := graph.Use(app, cloud.Deps{}); err != nil {
			t.Fatalf("graph: %v", err)
		}
	})
	t.Cleanup(func() { _ = graph.Shutdown() })
	fleetEndpoint(t, map[string]string{"graph": edge}, map[string]string{"graph": plane})

	about := func(entity, f string) string { return strings.Replace(f, "project:atlas", entity, 1) }
	dm := client.Origin{Account: "t024be7ld", Message: "1726459260.002100"}
	for entity, tc := range map[string]struct {
		from           client.Origin
		call, evidence string
	}{
		"project:atlas": {
			from: posted, call: `{"op":"graphAssert","input":{"assertions":[` + fact + `]}}`,
			evidence: saidEvidence,
		},
		"project:folded": {
			from: posted, call: `{"Op":"graphAssert","Input":{"Assertions":[` + about("project:folded", forged) + `]}}`,
			evidence: saidEvidence,
		},
		"project:ingested": {from: posted, call: ingest, evidence: saidSource + "#0"},
		"project:secret": {
			from: dm, call: `{"op":"graphAssert","input":{"assertions":[` + about("project:secret", fact) + `]}}`,
		},
	} {
		t.Run(entity, func(t *testing.T) {
			ai := &scriptAI{replies: []infer.ChatResponse{
				{ToolCalls: []infer.ToolCall{{ID: "c1", Name: "graph", Arguments: tc.call}}, FinishReason: "tool_calls"},
				{Content: "Noted: Dana owns it."},
			}}
			a := mk("acme", "hanzo")
			a.Tools = []string{ToolsAll}
			r := executeRun(withReply(context.Background(), thread, tc.from), ai, "acme", "acme/u1", a, "Dana owns it now", nil, "", "run_test")
			if r.Status != "ok" {
				t.Fatalf("want ok, got %q err=%q", r.Status, r.Error)
			}
			if len(ai.seen) != 2 {
				t.Fatalf("want the filing and the answer, got %d completions", len(ai.seen))
			}
			msgs := ai.seen[1].Messages
			told := msgs[len(msgs)-1].Content
			w := winner(t, entity)
			if !tc.from.Public {
				if w != nil || !strings.Contains(told, errUnshared.Error()) {
					t.Fatalf("a closed channel filed %+v; the model was told %q", w, told)
				}
				return
			}
			if !strings.Contains(told, `"recorded":1`) {
				t.Fatalf("the graph did not record the fact: %s", told)
			}
			if w == nil || w.Value != "dana" || w.Source != saidSource || w.Evidence != tc.evidence || w.By != "acme/u1" {
				t.Fatalf("stored %+v, want value dana, source %s, evidence %s, by acme/u1", w, saidSource, tc.evidence)
			}
		})
	}
}

// stored is one assertion the graph holds, as graphResolve reads it back.
type stored struct{ Value, Source, Evidence, By string }

// winner is what the graph answers for entity's owner, read as the org's member;
// nil when it holds nothing.
func winner(t *testing.T, entity string) *stored {
	t.Helper()
	out, err := surfaceTools{}.call(context.Background(), "acme", "acme/u1", "graphResolve",
		`{"entity":"`+entity+`","relation":"owner"}`)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	var got struct {
		Winner *stored `json:"winner"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("resolve answered %s (%v)", out, err)
	}
	return got.Winner
}
