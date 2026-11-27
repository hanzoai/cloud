package graph

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/sqlite"
)

func TestOntologyCatalogCountsAndFilters(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountGraph(t)

		code, b := call(t, app, http.MethodGet, "/v1/graph/ontology", "", nil)
		if code != http.StatusOK {
			t.Fatalf("GET /v1/graph/ontology: %d %s", code, b)
		}
		var full graphOntologyOut
		if err := json.Unmarshal(b, &full); err != nil {
			t.Fatalf("unmarshal ontology: %v", err)
		}
		if len(full.Classes) != 181 {
			t.Errorf("classes = %d, want 181", len(full.Classes))
		}
		edgeCount := 0
		for _, c := range full.Classes {
			if c.Edge {
				edgeCount++
			}
		}
		if edgeCount != 46 {
			t.Errorf("edge relationship-type classes = %d, want 46", edgeCount)
		}
		if len(full.Predicates) != 278 {
			t.Errorf("predicates = %d, want 278", len(full.Predicates))
		}
		if len(full.Features) != 144 {
			t.Errorf("features = %d, want 144", len(full.Features))
		}
		if len(full.ScoreColumns) != 17 {
			t.Errorf("score_columns = %d, want 17", len(full.ScoreColumns))
		}

		// Filtered query: group=edge & q=employment
		code, b = call(t, app, http.MethodGet, "/v1/graph/ontology?group=edge&q=employment", "", nil)
		if code != http.StatusOK {
			t.Fatalf("GET filtered ontology: %d %s", code, b)
		}
		var filtered graphOntologyOut
		if err := json.Unmarshal(b, &filtered); err != nil {
			t.Fatalf("unmarshal filtered ontology: %v", err)
		}
		if len(filtered.Classes) != 1 || filtered.Classes[0].ID != "CS165" {
			t.Errorf("filtered classes = %+v, want [CS165]", filtered.Classes)
		}
	})
}

func TestAppleIngestReifiedEdgesExpiredAndScorecard(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountGraph(t)

		payload := graphAppleIn{
			Owner:  "me",
			At:     "2026-09-01T12:00:00Z",
			Source: "apple/macbook",
			Triples: []graphAppleTriple{
				// Owner type, name and current-user flag
				{Subject: "9100000000007919", Predicate: "PS1", Object: "SB104", Confidence: 1.0, Sources: "Contacts"},
				{Subject: "9100000000007919", Predicate: "PS33", Object: "Zach", Confidence: 1.0, Sources: "Contacts"},
				{Subject: "9100000000007919", Predicate: "nm_isCurrentUser", Object: "1", Confidence: 1.0, Sources: "Contacts"},
				// Organization entity
				{Subject: "9100000000013724", Predicate: "PS1", Object: "SB10", Confidence: 1.0, Sources: "Contacts"},
				{Subject: "9100000000013724", Predicate: "PS33", Object: "Halden Labs", Confidence: 1.0, Sources: "Contacts"},
				// Reified 2-row employment edge sharing relationshipId 4412
				{
					Subject: "9100000000007919", Predicate: "PS500",
					RelationshipID: "4412", RelationshipPredicate: "PS1",
					Object: "CS165", Confidence: 0.96, Sources: "Contacts",
				},
				{
					Subject: "9100000000007919", Predicate: "PS500",
					RelationshipID: "4412", RelationshipPredicate: "PS45",
					Object: "9100000000013724", Confidence: 0.96, Sources: "Contacts",
				},
				// Reified kinship relationship with label ("sister")
				{
					Subject: "9100000000007919", Predicate: "PS598",
					RelationshipID: "5001", RelationshipPredicate: "PS1",
					Object: "nm_livingBeingRelationship", Confidence: 0.96, Sources: "Family Sharing",
				},
				{
					Subject: "9100000000007919", Predicate: "PS598",
					RelationshipID: "5001", RelationshipPredicate: "nm_personIdentifier",
					Object: "9100000000008001", Confidence: 0.96, Sources: "Family Sharing",
				},
				{
					Subject: "9100000000007919", Predicate: "PS598",
					RelationshipID: "5001", RelationshipPredicate: "nm_relationshipLabel",
					Object: "sister", Confidence: 0.96, Sources: "Family Sharing",
				},
			},
			Expired: []graphAppleTriple{
				// A withdrawn employer edge from expired_stable_graph
				{
					Subject: "9100000000007919", Predicate: "PS500",
					RelationshipID: "4100", RelationshipPredicate: "PS1",
					Object: "CS165", Confidence: 0.82, Sources: "Calendar",
					Timestamp: "2026-08-01T00:00:00Z", Until: "2026-08-25T00:00:00Z",
				},
				{
					Subject: "9100000000007919", Predicate: "PS500",
					RelationshipID: "4100", RelationshipPredicate: "PS45",
					Object: "9100000000099999", Confidence: 0.82, Sources: "Calendar",
					Timestamp: "2026-08-01T00:00:00Z", Until: "2026-08-25T00:00:00Z",
				},
			},
			Scorecards: []graphAppleScorecard{
				{
					Entity: "9100000000008002",
					Scores: map[string]float64{
						"mother":           0.96,
						"parent":           0.96,
						"family":           0.96,
						"familyAndFriends": 0.96,
					},
					Features: []float64{1, 0, 0, 1, 0, 1},
				},
			},
		}

		code, b := call(t, app, http.MethodPost, "/v1/graph/apple", "", payload)
		if code != http.StatusOK {
			t.Fatalf("POST /v1/graph/apple: %d %s", code, b)
		}
		var ingested graphAssertOut
		if err := json.Unmarshal(b, &ingested); err != nil {
			t.Fatalf("unmarshal assert out: %v", err)
		}
		if ingested.Recorded == 0 || ingested.Refused != 0 {
			t.Fatalf("ingested = %+v, want recorded > 0 and refused == 0", ingested)
		}

		// Inspect the owner entity ("9100000000007919") via POST /v1/graph/entity.
		code, b = call(t, app, http.MethodPost, "/v1/graph/entity", "", graphEntityIn{
			Entity: "9100000000007919",
			AsOf:   "2026-09-01T12:00:00Z",
		})
		if code != http.StatusOK {
			t.Fatalf("POST /v1/graph/entity: %d %s", code, b)
		}
		var state graphEntityOut
		if err := json.Unmarshal(b, &state); err != nil {
			t.Fatalf("unmarshal entity state: %v", err)
		}
		if !state.Known || state.Type != "person" {
			t.Errorf("state.Known=%v state.Type=%q, want true and person", state.Known, state.Type)
		}

		// Outgoing edges must hold live employer 9100000000013724, sister 9100000000008001, and same -> me.
		outEdges := make(map[string]string)
		for _, e := range state.Outgoing {
			outEdges[e.Relation] = e.Value
		}
		if outEdges["employer"] != "9100000000013724" {
			t.Errorf("live employer edge = %q, want 9100000000013724 (out=%+v)", outEdges["employer"], state.Outgoing)
		}
		if outEdges["sister"] != "9100000000008001" {
			t.Errorf("live sister edge = %q, want 9100000000008001", outEdges["sister"])
		}
		if outEdges["same"] != "me" {
			t.Errorf("same edge = %q, want me", outEdges["same"])
		}

		// Expired must hold the withdrawn employer 9100000000099999 from expired_stable_graph.
		if len(state.Expired) != 1 || state.Expired[0].Relation != "employer" || state.Expired[0].Value != "9100000000099999" {
			t.Errorf("state.Expired = %+v, want withdrawn employer 9100000000099999", state.Expired)
		}

		// Inspect the mother entity ("9100000000008002") for its kinship scorecard and incoming edge from "me".
		code, b = call(t, app, http.MethodPost, "/v1/graph/entity", "", graphEntityIn{
			Entity: "9100000000008002",
			AsOf:   "2026-09-01T12:00:00Z",
		})
		if code != http.StatusOK {
			t.Fatalf("POST /v1/graph/entity mother: %d %s", code, b)
		}
		var motherState graphEntityOut
		if err := json.Unmarshal(b, &motherState); err != nil {
			t.Fatalf("unmarshal mother state: %v", err)
		}
		if len(motherState.Scorecard) != 4 || motherState.Scorecard[0].Score != 0.96 {
			t.Errorf("mother scorecard = %+v, want 4 columns at 0.96", motherState.Scorecard)
		}
		if len(motherState.Incoming) == 0 {
			t.Errorf("mother incoming edges empty, want mother/parent edges from me")
		}
	})
}

func TestReadAppleStoreFromSQLiteFiles(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		dir := t.TempDir()

		// 1. Build a synthetic graph.db using github.com/hanzoai/sqlite.
		graphDB, err := sqlite.OpenPragma(filepath.Join(dir, "graph.db"), sqlite.DefaultPragmas)
		if err != nil {
			t.Fatalf("open graph.db: %v", err)
		}
		_, err = graphDB.ExecContext(ctx, `
		CREATE TABLE stable_graph (
			subject TEXT,
			predicate TEXT,
			relationshipId TEXT,
			relationshipPredicate TEXT,
			object TEXT,
			confidence REAL,
			sources TEXT,
			sourceDuplicates INTEGER,
			timestamp TEXT
		);
		CREATE TABLE expired_stable_graph (
			subject TEXT,
			predicate TEXT,
			relationshipId TEXT,
			relationshipPredicate TEXT,
			object TEXT,
			confidence REAL,
			sources TEXT,
			sourceDuplicates INTEGER,
			timestamp TEXT
		);
		INSERT INTO stable_graph VALUES ('9100000000007919', 'PS1', '0', '', 'SB104', 1.0, 'Contacts', 1, '815641460.5');
		INSERT INTO stable_graph VALUES ('9100000000007919', 'PS33', '0', '', 'Zach', 1.0, 'Contacts', 1, '815641460.5');
		INSERT INTO stable_graph VALUES ('9100000000007919', 'PS500', '4412', 'PS1', 'CS165', 0.96, 'Contacts', 1, '815641460.5');
		INSERT INTO stable_graph VALUES ('9100000000007919', 'PS500', '4412', 'PS45', '9100000000013724', 0.96, 'Contacts', 1, '815641460.5');
		INSERT INTO expired_stable_graph VALUES ('9100000000007919', 'PS33', '0', '', 'Old Alias', 0.5, 'Suggestions', 1, '814000000.0');
	`)
		if err != nil {
			t.Fatalf("seed graph.db: %v", err)
		}
		_ = graphDB.Close()

		// 2. Build a synthetic entityTagging.db using github.com/hanzoai/sqlite.
		if err := os.MkdirAll(filepath.Join(dir, "Artifacts", "entityTagging"), 0o755); err != nil {
			t.Fatalf("mkdir entityTagging: %v", err)
		}
		tagDB, err := sqlite.OpenPragma(filepath.Join(dir, "Artifacts", "entityTagging", "entityTagging.db"), sqlite.DefaultPragmas)
		if err != nil {
			t.Fatalf("open entityTagging.db: %v", err)
		}
		_, err = tagDB.ExecContext(ctx, `
		CREATE TABLE entity_tagging (
			mdid TEXT PRIMARY KEY,
			family REAL,
			familyAndFriends REAL,
			mother REAL,
			partner REAL,
			coworker REAL,
			myself REAL,
			alumni REAL,
			unknown REAL
		);
		INSERT INTO entity_tagging VALUES ('9100000000008005', 0.96, 0.96, 0.0, 0.96, 0.5, 0.0, 0.0, 0.0);
	`)
		if err != nil {
			t.Fatalf("seed entityTagging.db: %v", err)
		}
		_ = tagDB.Close()

		// 3. Read via ReadAppleStore and ingest into /v1/graph/apple.
		in, err := ReadAppleStore(ctx, dir)
		if err != nil {
			t.Fatalf("ReadAppleStore: %v", err)
		}
		in.Owner = "me"
		in.At = "2026-12-01T00:00:00Z"
		if len(in.Triples) != 4 || len(in.Expired) != 1 || len(in.Scorecards) != 1 {
			t.Fatalf("ReadAppleStore counts: triples=%d expired=%d scorecards=%d", len(in.Triples), len(in.Expired), len(in.Scorecards))
		}

		app := mountGraph(t)
		code, b := call(t, app, http.MethodPost, "/v1/graph/apple", "", in)
		if code != http.StatusOK {
			t.Fatalf("POST /v1/graph/apple from SQLite store: %d %s", code, b)
		}
	})
}

func TestConvoUpdatesEntityStateAcrossTurns(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountGraph(t)

		convo := graphConvoIn{
			Source: "chat/session-1",
			At:     "2026-09-01T10:00:00Z",
			Turns: []graphTurn{
				{
					ID:      "1",
					Speaker: "mara",
					Partner: "tomas",
					At:      "2026-09-01T10:00:00Z",
					Text:    "I work at Bell & Marrow, and I live in Berlin. My sister is Ines Quill. Where do you work?",
				},
				{
					ID:      "2",
					Speaker: "tomas",
					Partner: "mara",
					At:      "2026-09-01T11:00:00Z",
					Text:    "Your role is staff engineer, and I live in Zurich.",
				},
				{
					ID:      "3",
					Speaker: "mara",
					Partner: "tomas",
					At:      "2026-09-01T12:00:00Z",
					Text:    "Actually, I left Bell & Marrow. I joined Halden Labs, and I moved to Tokyo.",
				},
			},
		}

		code, b := call(t, app, http.MethodPost, "/v1/graph/convo", "", convo)
		if code != http.StatusOK {
			t.Fatalf("POST /v1/graph/convo: %d %s", code, b)
		}
		var out graphConvoOut
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("unmarshal convo out: %v", err)
		}
		if out.Recorded == 0 || out.Refused != 0 {
			t.Fatalf("convo out = %+v, want recorded > 0 and refused == 0", out)
		}

		// 1. Check Mara's state after Turn 3 (12:30Z):
		//    - employer is "Halden Labs" (superseding "Bell & Marrow")
		//    - lives_in is "Tokyo" (superseding "Berlin")
		//    - role is "staff engineer" (asserted by Tomas in Turn 2 via "Your role is...")
		//    - sister edge points to "ines quill"
		//    - Expired holds "Berlin" and "Bell & Marrow"
		code, b = call(t, app, http.MethodPost, "/v1/graph/entity", "", graphEntityIn{
			Entity: "mara",
			AsOf:   "2026-09-01T12:30:00Z",
		})
		if code != http.StatusOK {
			t.Fatalf("POST /v1/graph/entity mara @ 12:30Z: %d %s", code, b)
		}
		var nowState graphEntityOut
		if err := json.Unmarshal(b, &nowState); err != nil {
			t.Fatalf("unmarshal mara @ 12:30Z: %v", err)
		}
		props := make(map[string]string)
		for _, p := range nowState.Properties {
			props[p.Relation] = p.Value
		}
		if props["employer"] != "Halden Labs" {
			t.Errorf("mara employer @ 12:30Z = %q, want Halden Labs", props["employer"])
		}
		if props["lives_in"] != "Tokyo" {
			t.Errorf("mara lives_in @ 12:30Z = %q, want Tokyo", props["lives_in"])
		}
		if props["role"] != "staff engineer" {
			t.Errorf("mara role @ 12:30Z = %q, want staff engineer", props["role"])
		}

		expiredVals := make(map[string]string)
		for _, ex := range nowState.Expired {
			expiredVals[ex.Relation] = ex.Value
		}
		if expiredVals["lives_in"] != "Berlin" {
			t.Errorf("mara expired lives_in = %q, want Berlin (expired=%+v)", expiredVals["lives_in"], nowState.Expired)
		}
		if expiredVals["employer"] != "Bell & Marrow" {
			t.Errorf("mara expired employer = %q, want Bell & Marrow (expired=%+v)", expiredVals["employer"], nowState.Expired)
		}

		// 2. Time-travel check: Mara's state at 10:30Z (between Turn 1 and Turn 2).
		code, b = call(t, app, http.MethodPost, "/v1/graph/entity", "", graphEntityIn{
			Entity: "mara",
			AsOf:   "2026-09-01T10:30:00Z",
		})
		if code != http.StatusOK {
			t.Fatalf("POST /v1/graph/entity mara @ 10:30Z: %d %s", code, b)
		}
		var earlierState graphEntityOut
		if err := json.Unmarshal(b, &earlierState); err != nil {
			t.Fatalf("unmarshal mara @ 10:30Z: %v", err)
		}
		earlyProps := make(map[string]string)
		for _, p := range earlierState.Properties {
			earlyProps[p.Relation] = p.Value
		}
		if earlyProps["employer"] != "Bell & Marrow" || earlyProps["lives_in"] != "Berlin" {
			t.Errorf("mara @ 10:30Z = %+v, want employer=Bell & Marrow and lives_in=Berlin", earlyProps)
		}
	})
}
