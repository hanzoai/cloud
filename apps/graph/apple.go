package graph

// apple.go ingests Apple's Personal Knowledge Graph (graph.db's stable_graph
// and expired_stable_graph, plus entityTagging.db's 17-column kinship
// scorecard) into the organization's bitemporal assertion plane, and provides
// ReadAppleStore for reading ~/Library/IntelligencePlatform (macOS) or
// /private/var/mobile/Library/IntelligencePlatform (iOS) directly through
// github.com/hanzoai/sqlite.

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/sqlite"
	"github.com/zap-proto/zip"
)

// graphAppleTriple is one row of stable_graph or expired_stable_graph in
// Apple's graph.db.
type graphAppleTriple struct {
	// Subject is the source entity identifier in graph.db, such as
	// 9100000000007919. Required.
	Subject string `json:"subject" validate:"required"`
	// Predicate is the predicate code, such as PS1, PS33, PS500 or PS598.
	// Required.
	Predicate string `json:"predicate" validate:"required"`
	// RelationshipID groups the two rows of a reified edge in stable_graph:
	// one row with RelationshipPredicate PS1 naming the CS*/nm_* relationship
	// class, and a companion row naming the target entity or value. Absent on
	// single-row triples.
	RelationshipID string `json:"relationship_id,omitempty"`
	// RelationshipPredicate is the sub-predicate on a reified edge row, such as
	// PS1 for the relationship class or PS45 for the target entity. Absent on
	// single-row triples.
	RelationshipPredicate string `json:"relationship_predicate,omitempty"`
	// Object is the target entity identifier, class code or scalar literal.
	// Required.
	Object string `json:"object" validate:"required"`
	// Confidence is the daemon's certainty in [0,1]. Absent defaults to 1 for
	// copied facts and 0 for unscored rows.
	Confidence float64 `json:"confidence,omitempty"`
	// Sources names the OS stores that contributed the row, such as Contacts,
	// Calendar, Photos or Family Sharing. Absent defaults to apple/graph.db.
	Sources string `json:"sources,omitempty"`
	// SourceDuplicates is how many duplicate source records backed the row.
	SourceDuplicates int `json:"source_duplicates,omitempty"`
	// Timestamp is when the daemon recorded the row, RFC 3339. Absent defaults
	// to the batch's At instant.
	Timestamp string `json:"timestamp,omitempty"`
	// Until is when a withdrawn row in expired_stable_graph stopped holding,
	// RFC 3339. Absent on expired rows defaults to the batch's At instant.
	Until string `json:"until,omitempty"`
}

// graphAppleScorecard is one person's row from entityTagging.db.
type graphAppleScorecard struct {
	// Entity is the person's identifier in the graph. Required.
	Entity string `json:"entity" validate:"required"`
	// Scores maps each relationship column (mother, father, parent, sister,
	// brother, sibling, daughter, son, child, partner, coworker, friend,
	// family, familyAndFriends, alumni, unknown) to its probability in [0,1].
	Scores map[string]float64 `json:"scores" validate:"required"`
	// Features is the optional 144-element numeric input vector in
	// metadata.json order (0..19 identity, 20..59 communication, 60..143
	// photos).
	Features []float64 `json:"features,omitempty"`
}

// graphAppleIn is a snapshot of an Apple Personal Knowledge Graph to ingest.
type graphAppleIn struct {
	// Owner is the entity key for the device owner ("(Me)"). When set,
	// entities flagged with nm_isCurrentUser are linked to Owner via `same`,
	// and high-confidence kinship scorecard columns emit edges from Owner.
	Owner string `json:"owner,omitempty"`
	// At is the valid instant of the snapshot, RFC 3339. Required.
	At string `json:"at" validate:"required"`
	// Source names the device or store this snapshot came from. Absent defaults
	// to apple/intelligence-platform.
	Source string `json:"source,omitempty"`
	// Triples is the live rows from stable_graph. Reified two-row edges sharing
	// a relationship_id are paired into edges automatically.
	Triples []graphAppleTriple `json:"triples,omitempty"`
	// Expired is the withdrawn rows from expired_stable_graph. Each is filed
	// with an until instant so it stays in the bitemporal record and surfaces
	// in graphEntity's expired list without holding in live resolution.
	Expired []graphAppleTriple `json:"expired,omitempty"`
	// Scorecards is the per-person kinship probability rows from
	// entityTagging.db.
	Scorecards []graphAppleScorecard `json:"scorecards,omitempty"`
}

// apple ingests an Apple Personal Knowledge Graph snapshot — live stable_graph
// triples, withdrawn expired_stable_graph rows and entityTagging kinship
// scorecards — into the organization's assertion plane.
//
// Two-row reified edges sharing a relationship_id (one naming a CS*/nm_*
// relationship type under PS1, the other pointing at the target entity) are
// collapsed into edges under their canonical relation names while preserving
// the raw Apple predicate and relationship class in evidence. Withdrawn rows in
// expired are recorded with until stamped so they remain inspectable in history
// without holding as live facts.
//
// Time: at is the snapshot's valid instant, which an as_of read is bounded by;
// the server stamps knowable at the write, which an as_known read is bounded by.
//
// Example: {"owner": "me", "at": "2026-10-28T00:00:00Z", "triples": [{"subject": "9100000000007919", "predicate": "PS500", "relationship_id": "4412", "relationship_predicate": "PS1", "object": "CS165", "confidence": 0.96}, {"subject": "9100000000007919", "predicate": "PS500", "relationship_id": "4412", "relationship_predicate": "PS45", "object": "9100000000013724", "confidence": 0.96}]}
func (o ops) apple(ctx context.Context, in *graphAppleIn) (*graphAssertOut, error) {
	atTime, err := instant(in.At)
	if err != nil {
		return nil, err
	}
	if len(in.Triples) == 0 && len(in.Expired) == 0 && len(in.Scorecards) == 0 {
		return nil, zip.ErrBadRequest("at least one triple, expired row or scorecard is required")
	}

	baseSource := strings.TrimSpace(in.Source)
	if baseSource == "" {
		baseSource = "apple/intelligence-platform"
	}
	owner := key(in.Owner)

	var batch []graphFact
	batch = append(batch, translateAppleTriples(in.Triples, atTime, baseSource, owner, false)...)
	batch = append(batch, translateAppleTriples(in.Expired, atTime, baseSource, owner, true)...)
	batch = append(batch, translateAppleScorecards(in.Scorecards, in.At, baseSource, owner)...)

	if len(batch) == 0 {
		return nil, zip.ErrBadRequest("no valid assertions could be translated from the Apple graph payload")
	}
	return o.assert(ctx, &graphAssertIn{Assertions: batch})
}

func translateAppleTriples(rows []graphAppleTriple, batchAt time.Time, baseSource, owner string, isExpired bool) []graphFact {
	if len(rows) == 0 {
		return nil
	}
	type relGroupKey struct {
		subject   string
		predicate string
		relID     string
	}
	groups := make(map[relGroupKey][]graphAppleTriple)
	var groupOrder []relGroupKey
	var singles []graphAppleTriple

	for _, r := range rows {
		subj := key(r.Subject)
		pred := strings.TrimSpace(r.Predicate)
		obj := strings.TrimSpace(r.Object)
		if subj == "" || pred == "" || obj == "" {
			continue
		}
		relID := strings.TrimSpace(r.RelationshipID)
		if relID == "" || relID == "0" {
			singles = append(singles, r)
			continue
		}
		k := relGroupKey{
			subject:   subj,
			predicate: pred,
			relID:     relID,
		}
		if _, exists := groups[k]; !exists {
			groupOrder = append(groupOrder, k)
		}
		groups[k] = append(groups[k], r)
	}

	var out []graphFact

	// 1. Direct (1-row) triples.
	for _, r := range singles {
		out = append(out, emitSingleTriple(r, batchAt, baseSource, owner, isExpired)...)
	}

	// 2. Reified 2-row (or multi-row) edges sharing a relationshipId.
	for _, k := range groupOrder {
		members := groups[k]
		var relClass string
		var targets []graphAppleTriple
		var labels []string

		for _, m := range members {
			rp := strings.TrimSpace(m.RelationshipPredicate)
			switch {
			case rp == "PS1":
				relClass = strings.TrimSpace(m.Object)
			case rp == "nm_relationshipLabel" || rp == "nm_unlocalizedRelationshipLabel":
				labels = append(labels, strings.TrimSpace(m.Object))
				targets = append(targets, m)
			default:
				targets = append(targets, m)
			}
		}

		// If the group only had a PS1 row and no companion target row, emit it
		// as a single triple so nothing is silently dropped.
		if len(targets) == 0 {
			for _, m := range members {
				out = append(out, emitSingleTriple(m, batchAt, baseSource, owner, isExpired)...)
			}
			continue
		}

		// Identify whether there is an entity-pointing target row alongside a
		// label row (e.g. PS598 with nm_personIdentifier + nm_relationshipLabel).
		var entityTargets []graphAppleTriple
		for _, t := range targets {
			rp := strings.TrimSpace(t.RelationshipPredicate)
			if rp != "nm_relationshipLabel" && rp != "nm_unlocalizedRelationshipLabel" {
				entityTargets = append(entityTargets, t)
			}
		}
		if len(entityTargets) == 0 {
			entityTargets = targets
		}

		for _, t := range entityTargets {
			rel := canonicalRelationForPred(k.predicate)
			rp := strings.TrimSpace(t.RelationshipPredicate)
			val := strings.TrimSpace(t.Object)
			names := isEntityTarget(k.predicate, rp, relClass, val)
			if names {
				val = key(val)
			} else if rel == "type" {
				val = resolveClassLabel(val)
			}

			atStr, untilStr := tripleTimes(t, batchAt, isExpired)
			src := tripleSource(t.Sources, baseSource)
			ev := fmt.Sprintf("apple:%s#%s", k.predicate, k.relID)
			if relClass != "" || rp != "" {
				ev = fmt.Sprintf("apple:%s#%s(%s/%s)", k.predicate, k.relID, relClass, rp)
			}

			out = append(out, graphFact{
				Entity:     k.subject,
				Relation:   rel,
				Value:      val,
				Names:      names,
				At:         atStr,
				Until:      untilStr,
				Source:     src,
				Evidence:   ev,
				Confidence: clampConf(t.Confidence),
			})

			// If a relationship label (e.g. "sister", "coworker") accompanied
			// an entity target on the same relationshipId, emit the specific
			// labeled edge too.
			if names {
				for _, lbl := range labels {
					lblRel := key(slugifyLabel(lbl))
					if lblRel != "" && lblRel != rel {
						out = append(out, graphFact{
							Entity:     k.subject,
							Relation:   lblRel,
							Value:      val,
							Names:      true,
							At:         atStr,
							Until:      untilStr,
							Source:     src,
							Evidence:   ev + ":" + lblRel,
							Confidence: clampConf(t.Confidence),
						})
					}
				}
			}
		}
	}

	return out
}

func emitSingleTriple(r graphAppleTriple, batchAt time.Time, baseSource, owner string, isExpired bool) []graphFact {
	subj := key(r.Subject)
	pred := strings.TrimSpace(r.Predicate)
	rel := canonicalRelationForPred(pred)
	val := strings.TrimSpace(r.Object)
	names := isEntityTarget(pred, strings.TrimSpace(r.RelationshipPredicate), "", val)

	ev := "apple:" + pred
	if rel == "type" {
		if c, ok := classByID[val]; ok {
			ev = fmt.Sprintf("apple:%s=%s", pred, val)
			val = c.Label
		}
		names = false
	} else if names {
		val = key(val)
	}

	atStr, untilStr := tripleTimes(r, batchAt, isExpired)
	src := tripleSource(r.Sources, baseSource)
	conf := clampConf(r.Confidence)

	out := []graphFact{{
		Entity:     subj,
		Relation:   rel,
		Value:      val,
		Names:      names,
		At:         atStr,
		Until:      untilStr,
		Source:     src,
		Evidence:   ev,
		Confidence: conf,
	}}

	// When nm_isCurrentUser marks this entity as the device owner and the
	// caller supplied an owner key, bind them with `same`.
	if !isExpired && pred == "nm_isCurrentUser" && owner != "" && owner != subj && (val == "1" || strings.EqualFold(val, "true")) {
		out = append(out, graphFact{
			Entity:     subj,
			Relation:   "same",
			Value:      owner,
			Names:      true,
			At:         atStr,
			Source:     src,
			Evidence:   "apple:nm_isCurrentUser",
			Confidence: 1,
		})
	}
	return out
}

func translateAppleScorecards(cards []graphAppleScorecard, atStr, baseSource, owner string) []graphFact {
	var out []graphFact
	src := baseSource + "/entityTagging"

	kinshipEdges := map[string]bool{
		"mother": true, "father": true, "parent": true,
		"sister": true, "brother": true, "sibling": true,
		"daughter": true, "son": true, "child": true,
		"partner": true, "spouse": true, "coworker": true, "friend": true,
	}

	for _, card := range cards {
		ent := key(card.Entity)
		if ent == "" {
			continue
		}
		cols := make([]string, 0, len(card.Scores))
		for col := range card.Scores {
			cols = append(cols, col)
		}
		slices.Sort(cols)

		for _, col := range cols {
			score := clampConf(card.Scores[col])
			if score <= 0 {
				continue
			}
			cleanCol := strings.TrimSpace(col)
			if cleanCol == "" {
				continue
			}
			out = append(out, graphFact{
				Entity:     ent,
				Relation:   "kinship:" + cleanCol,
				Value:      strconv.FormatFloat(score, 'f', 4, 64),
				Names:      false,
				At:         atStr,
				Source:     src,
				Evidence:   "entityTagging:" + cleanCol,
				Confidence: score,
			})
			if owner != "" && owner != ent && kinshipEdges[cleanCol] && score >= 0.5 {
				out = append(out, graphFact{
					Entity:     owner,
					Relation:   key(cleanCol),
					Value:      ent,
					Names:      true,
					At:         atStr,
					Source:     src,
					Evidence:   "entityTagging:" + cleanCol,
					Confidence: score,
				})
			}
		}

		for i, v := range card.Features {
			if i >= len(appleFeatureNames) || v == 0 {
				continue
			}
			fname := appleFeatureNames[i]
			out = append(out, graphFact{
				Entity:     ent,
				Relation:   "feature:" + fname,
				Value:      strconv.FormatFloat(v, 'f', 4, 64),
				Names:      false,
				At:         atStr,
				Source:     src,
				Evidence:   fmt.Sprintf("entityTagging:feature[%d]", i),
				Confidence: 1,
			})
		}
	}
	return out
}

func canonicalRelationForPred(code string) string {
	if p, ok := predByID[code]; ok {
		return p.Relation
	}
	if over, ok := canonicalOverrides[code]; ok {
		return over
	}
	if strings.HasPrefix(code, "nm_") {
		return camelToSnake(strings.TrimPrefix(code, "nm_"))
	}
	return key(code)
}

func resolveClassLabel(code string) string {
	if c, ok := classByID[code]; ok {
		return c.Label
	}
	return code
}

func isEntityTarget(pred, relPred, relClass, val string) bool {
	if pred == "PS1" {
		return false
	}
	if edgePredicates[pred] || edgePredicates[relPred] {
		return true
	}
	if c, ok := classByID[relClass]; ok && c.Edge {
		// Reified edges pointing at an identifier, text, quantity or date are
		// scalar; the rest (employment, membership, location, participation,
		// living being, organization, person) link two entities.
		switch relClass {
		case "CS105", "CS101", "CS22", "CS3", "CS113", "nm_dateRelationshipType", "nm_localizedNameRelationshipType", "nm_confidenceRelationshipType":
			return false
		default:
			return true
		}
	}
	// Numeric 15..20-digit Apple entity IDs in graph.db (e.g. 9100000000007919
	// or 180145451991246617 on macOS 27).
	if len(val) >= 15 && len(val) <= 20 && allDigits(val) {
		return true
	}
	return false
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

func tripleTimes(r graphAppleTriple, batchAt time.Time, isExpired bool) (atStr, untilStr string) {
	at := batchAt
	if strings.TrimSpace(r.Timestamp) != "" {
		if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(r.Timestamp)); err == nil {
			at = parsed.UTC()
		}
	}
	if !isExpired {
		if strings.TrimSpace(r.Until) != "" {
			if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(r.Until)); err == nil && parsed.After(at) {
				return at.Format(time.RFC3339), parsed.UTC().Format(time.RFC3339)
			}
		}
		return at.Format(time.RFC3339), ""
	}

	// Expired row from expired_stable_graph: must carry an Until > At so
	// Resolve sees it as ended by batchAt while entity.expired preserves it.
	until := batchAt
	if strings.TrimSpace(r.Until) != "" {
		if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(r.Until)); err == nil {
			until = parsed.UTC()
		}
	}
	if !until.After(at) {
		at = until.Add(-time.Second)
	}
	return at.Format(time.RFC3339), until.Format(time.RFC3339)
}

func tripleSource(raw, fallback string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return fallback
	}
	return s
}

func clampConf(c float64) float64 {
	if c < 0 {
		return 0
	}
	if c > 1 {
		return 1
	}
	return c
}

// DefaultApplePaths returns the standard on-device IntelligencePlatform
// directory locations on macOS 13+ and iOS.
func DefaultApplePaths() []string {
	var paths []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		paths = append(paths, filepath.Join(home, "Library", "IntelligencePlatform"))
	}
	paths = append(paths, "/private/var/mobile/Library/IntelligencePlatform")
	return paths
}

// ReadAppleStore reads graph.db (stable_graph and expired_stable_graph) and
// entityTagging.db from a local IntelligencePlatform directory using
// github.com/hanzoai/sqlite and returns a graphAppleIn payload ready to file
// through POST /v1/graph/apple.
func ReadAppleStore(ctx context.Context, dir string) (*graphAppleIn, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}

	out := &graphAppleIn{
		At:     time.Now().UTC().Format(time.RFC3339),
		Source: "apple/intelligence-platform",
	}

	graphPath := filepath.Join(dir, "graph.db")
	if _, err := os.Stat(graphPath); err == nil {
		live, expired, err := readAppleGraphDB(ctx, graphPath)
		if err != nil {
			return nil, fmt.Errorf("read graph.db: %w", err)
		}
		out.Triples = live
		out.Expired = expired
	}

	for _, tagPath := range []string{
		filepath.Join(dir, "Artifacts", "entityTagging", "entityTagging.db"),
		filepath.Join(dir, "entityTagging.db"),
	} {
		if _, err := os.Stat(tagPath); err == nil {
			cards, err := readAppleEntityTaggingDB(ctx, tagPath)
			if err != nil {
				return nil, fmt.Errorf("read entityTagging.db: %w", err)
			}
			out.Scorecards = cards
			for _, c := range cards {
				if out.Owner == "" && c.Scores["myself"] >= 0.5 {
					out.Owner = c.Entity
				}
			}
			break
		}
	}

	return out, nil
}

func readAppleGraphDB(ctx context.Context, path string) (live, expired []graphAppleTriple, err error) {
	db, err := sqlite.OpenPragma(path, sqlite.DefaultPragmas)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = db.Close() }()

	if hasTable(ctx, db, "stable_graph") {
		live, err = queryGraphTable(ctx, db, "stable_graph")
		if err != nil {
			return nil, nil, err
		}
	}
	if hasTable(ctx, db, "expired_stable_graph") {
		expired, err = queryGraphTable(ctx, db, "expired_stable_graph")
		if err != nil {
			return nil, nil, err
		}
	}
	return live, expired, nil
}

func hasTable(ctx context.Context, db *sql.DB, name string) bool {
	var count int
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = $1`, name).Scan(&count)
	return err == nil && count > 0
}

func queryGraphTable(ctx context.Context, db *sql.DB, table string) ([]graphAppleTriple, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT * FROM %s`, table))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	var out []graphAppleTriple
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		var t graphAppleTriple
		for i, col := range cols {
			s := asString(vals[i])
			switch strings.ToLower(col) {
			case "subject", "subjectid", "subject_id":
				t.Subject = s
			case "predicate", "predicateid", "predicate_id":
				t.Predicate = s
			case "relationshipid", "relationship_id":
				t.RelationshipID = s
			case "relationshippredicate", "relationship_predicate":
				t.RelationshipPredicate = s
			case "object", "objectid", "object_id", "value":
				t.Object = s
			case "confidence":
				t.Confidence, _ = strconv.ParseFloat(s, 64)
			case "sources", "source":
				t.Sources = s
			case "sourceduplicates", "source_duplicates":
				t.SourceDuplicates, _ = strconv.Atoi(s)
			case "timestamp", "created_at", "at":
				t.Timestamp = normalizeAppleTimestamp(s)
			case "until", "expired_at", "expiredat":
				t.Until = normalizeAppleTimestamp(s)
			}
		}
		if t.Subject != "" && t.Predicate != "" && t.Object != "" {
			out = append(out, t)
		}
	}
	return out, rows.Err()
}

func readAppleEntityTaggingDB(ctx context.Context, path string) ([]graphAppleScorecard, error) {
	db, err := sqlite.OpenPragma(path, sqlite.DefaultPragmas)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	// Discover the scorecard table name in entityTagging.db.
	tableRows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	var tables []string
	for tableRows.Next() {
		var name string
		if err := tableRows.Scan(&name); err == nil {
			tables = append(tables, name)
		}
	}
	_ = tableRows.Close()

	knownCols := make(map[string]string, len(appleScoreColumns))
	for _, c := range appleScoreColumns {
		knownCols[strings.ToLower(c)] = c
	}

	var out []graphAppleScorecard
	for _, tbl := range tables {
		rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT * FROM %s`, tbl))
		if err != nil {
			continue
		}
		cols, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			continue
		}
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				break
			}
			card := graphAppleScorecard{Scores: make(map[string]float64)}
			for i, col := range cols {
				low := strings.ToLower(col)
				s := asString(vals[i])
				if low == "mdid" || low == "entity" || low == "entityid" || low == "entity_id" || low == "person" || low == "personid" || low == "person_id" || low == "subject" {
					card.Entity = s
					continue
				}
				if canonical, ok := knownCols[low]; ok {
					if v, err := strconv.ParseFloat(s, 64); err == nil && v > 0 {
						card.Scores[canonical] = v
					}
				}
			}
			if card.Entity != "" && len(card.Scores) > 0 {
				out = append(out, card)
			}
		}
		_ = rows.Close()
	}
	slices.SortFunc(out, func(a, b graphAppleScorecard) int {
		return cmp.Compare(a.Entity, b.Entity)
	})
	return out, nil
}

func asString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprint(x)
	}
}

func normalizeAppleTimestamp(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if _, err := time.Parse(time.RFC3339, s); err == nil {
		return s
	}
	if sec, err := strconv.ParseFloat(s, 64); err == nil && sec > 0 {
		// Apple CoreData timestamps are seconds since 2001-01-01T00:00:00Z
		// (978307200 Unix); values below 1e9 are CoreData epochs.
		if sec < 1e9 {
			sec += 978307200
		}
		return time.Unix(int64(sec), 0).UTC().Format(time.RFC3339)
	}
	return ""
}
