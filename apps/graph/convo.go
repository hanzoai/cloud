package graph

// convo.go extracts entity state, kinship edges and conversational claims from
// multi-turn dialogue and files them into the organization's assertion plane.
//
// Each turn binds first-person pronouns (I, me, my) to its speaker, second-
// person pronouns (you, your) to its partner, and proper-noun subjects to their
// entity keys. Questions are ignored. Because every turn carries its own valid
// instant, a later turn stating a new value for a single-valued property (such
// as lives_in, role, status or email) supersedes the earlier one in Resolve
// while preserving the earlier claim in the entity's expired history; explicit
// departures (such as "I left Acme" or "I no longer work at Acme") file a
// retraction that ends the prior state at that turn's instant.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/zap-proto/zip"
)

// graphTurn is one utterance in a conversation.
type graphTurn struct {
	// ID is the turn's identifier within the conversation, appended to Source
	// as `#<id>` in Evidence. Absent defaults to its 1-based position.
	ID string `json:"id,omitempty"`
	// Speaker is the entity key of the participant speaking this turn. First-
	// person references (I, me, my) resolve to Speaker. Required.
	Speaker string `json:"speaker" validate:"required"`
	// Partner is the entity key of the participant addressed in this turn.
	// Second-person references (you, your) resolve to Partner.
	Partner string `json:"partner,omitempty"`
	// Text is what the speaker said. Required.
	Text string `json:"text" validate:"required"`
	// At is when this turn was spoken, RFC 3339. Absent defaults to the
	// conversation's At instant.
	At string `json:"at,omitempty"`
}

// graphConvoIn is a sequence of conversation turns to extract and store.
type graphConvoIn struct {
	// Source names the conversation these turns came from, such as
	// chat/thread-42. Required.
	Source string `json:"source" validate:"required"`
	// At is the default valid instant for any turn that omits its own At,
	// RFC 3339. Required.
	At string `json:"at" validate:"required"`
	// Turns is the ordered dialogue turns to read. Required.
	Turns []graphTurn `json:"turns" validate:"required"`
	// DryRun when true returns the extracted assertions without writing them to
	// the store. Absent or false files them through the assertion admission
	// path.
	DryRun bool `json:"dry_run,omitempty"`
}

// graphConvoOut reports the assertions extracted from the conversation and what
// became of them in the store.
type graphConvoOut struct {
	// Assertions is the assertions extracted from the turns, in turn order.
	Assertions []graphFact `json:"assertions"`
	// Recorded is how many extracted assertions became new rows in the store.
	// Zero when DryRun is true.
	Recorded int `json:"recorded"`
	// Duplicate is how many extracted assertions the store already held.
	Duplicate int `json:"duplicate"`
	// Refused is how many extracted assertions were refused by admission or
	// schema checks.
	Refused int `json:"refused"`
	// Reasons names why each refused assertion was refused, in order.
	Reasons []string `json:"reasons,omitempty"`
}

// convo extracts entity state and relationship assertions from conversation
// turns and updates the graph.
//
// First-person statements bind to the turn's speaker, second-person statements
// to its partner, and named subjects to their entity keys. Questions are
// skipped. Single-valued state updates (such as moving cities or changing
// roles) supersede prior values at the turn's timestamp, and explicit
// departures (such as leaving an employer) file a retraction ending the prior
// claim. Set dry_run to inspect what the turns state without writing.
//
// Time: each turn's at (or the batch's at when omitted) is the valid instant of
// the assertions extracted from it, which an as_of read is bounded by; each
// becomes knowable at the write, which an as_known read is bounded by.
//
// Example: {"source": "chat/rev-1", "at": "2026-10-28T10:00:00Z", "turns": [{"id": "1", "speaker": "mara", "partner": "tomas", "text": "I work at Halden Labs and I live in Berlin."}]}
func (o ops) convo(ctx context.Context, in *graphConvoIn) (*graphConvoOut, error) {
	source := strings.TrimSpace(in.Source)
	if source == "" {
		return nil, zip.ErrBadRequest("source is required")
	}
	if _, err := instant(in.At); err != nil {
		return nil, err
	}
	if len(in.Turns) == 0 {
		return nil, zip.ErrBadRequest("turns is required")
	}

	var extracted []graphFact
	for i, turn := range in.Turns {
		speaker := key(turn.Speaker)
		if speaker == "" {
			return nil, zip.ErrBadRequest(fmt.Sprintf("turn %d: speaker is required", i+1))
		}
		turnAt := strings.TrimSpace(turn.At)
		if turnAt == "" {
			turnAt = in.At
		} else if _, err := instant(turnAt); err != nil {
			return nil, err
		}
		tid := strings.TrimSpace(turn.ID)
		if tid == "" {
			tid = strconv.Itoa(i + 1)
		}
		ev := fmt.Sprintf("%s#%s", source, tid)
		partner := key(turn.Partner)

		extracted = append(extracted, extractTurnFacts(speaker, partner, turn.Text, turnAt, source, ev)...)
	}

	if len(extracted) == 0 {
		return nil, zip.ErrBadRequest("the conversation turns state no extractable entity properties or relations")
	}

	out := &graphConvoOut{Assertions: extracted}
	if in.DryRun {
		return out, nil
	}

	res, err := o.assert(ctx, &graphAssertIn{Assertions: extracted})
	if err != nil {
		return nil, err
	}
	out.Recorded = res.Recorded
	out.Duplicate = res.Duplicate
	out.Refused = res.Refused
	out.Reasons = res.Reasons
	return out, nil
}

var kinshipNouns = map[string]string{
	"mother":    "mother",
	"mom":       "mother",
	"father":    "father",
	"dad":       "father",
	"parent":    "parent",
	"sister":    "sister",
	"brother":   "brother",
	"sibling":   "sibling",
	"daughter":  "daughter",
	"son":       "son",
	"child":     "child",
	"partner":   "partner",
	"spouse":    "spouse",
	"wife":      "spouse",
	"husband":   "spouse",
	"coworker":  "coworker",
	"colleague": "coworker",
	"friend":    "friend",
	"manager":   "manager",
	"boss":      "manager",
}

func extractTurnFacts(speaker, partner, text, at, source, evidence string) []graphFact {
	var out []graphFact

	// 1. Check for explicit `relation:: value` lines first.
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if before, after, ok := strings.Cut(line, "::"); ok {
			rel := key(strings.TrimSpace(before))
			val := strings.TrimSpace(after)
			if rel != "" && val != "" && !strings.ContainsAny(rel, " \t") {
				names := false
				if strings.HasPrefix(val, "[[") && strings.HasSuffix(val, "]]") {
					inner := strings.TrimSpace(val[2 : len(val)-2])
					if inner != "" {
						val = key(inner)
						names = true
					}
				}
				out = append(out, graphFact{
					Entity:     speaker,
					Relation:   rel,
					Value:      val,
					Names:      names,
					At:         at,
					Source:     source,
					Evidence:   evidence,
					Confidence: 1,
				})
			}
		}
	}

	// 2. Sentence and clause segmentation for natural dialogue.
	for _, sent := range splitTurnSentences(text) {
		sent = strings.TrimSpace(sent)
		if sent == "" || strings.HasSuffix(sent, "?") || strings.Contains(sent, "::") {
			continue
		}
		body := strings.TrimRight(sent, ".!")
		for _, clause := range splitTurnClauses(body) {
			if facts := matchStructuredClause(speaker, partner, clause, at, source, evidence); len(facts) > 0 {
				out = append(out, facts...)
				continue
			}
			if f, ok := matchGeneralClause(speaker, partner, clause, at, source, evidence); ok {
				out = append(out, f)
			}
		}
	}
	return out
}

func matchStructuredClause(speaker, partner, clause, at, source, evidence string) []graphFact {
	s := trimConversationalLead(strings.TrimSpace(clause))
	if s == "" {
		return nil
	}
	words := strings.Fields(s)
	if len(words) < 3 {
		return nil
	}
	low := strings.ToLower(s)

	// Pattern A: Possessive property or kinship:
	// "my <noun> is <value>", "your <noun> is <value>"
	if (strings.EqualFold(words[0], "my") || strings.EqualFold(words[0], "your")) && len(words) >= 4 {
		holder := speaker
		if strings.EqualFold(words[0], "your") {
			if partner == "" {
				return nil
			}
			holder = partner
		}
		// Find "is" or "was" at index 2 or 3.
		isIdx := -1
		for i := 2; i <= 3 && i < len(words)-1; i++ {
			if strings.EqualFold(words[i], "is") || strings.EqualFold(words[i], "was") {
				isIdx = i
				break
			}
		}
		if isIdx > 1 {
			propRaw := strings.ToLower(strings.Join(words[1:isIdx], "_"))
			valRaw := cleanObjectValue(strings.Join(words[isIdx+1:], " "))
			if valRaw != "" {
				if kin, ok := kinshipNouns[propRaw]; ok {
					return []graphFact{{
						Entity:     holder,
						Relation:   kin,
						Value:      key(valRaw),
						Names:      true,
						At:         at,
						Source:     source,
						Evidence:   evidence,
						Confidence: 0.95,
					}}
				}
				names := propRaw == "employer" || propRaw == "company" || propRaw == "organization"
				if propRaw == "company" || propRaw == "organization" {
					propRaw = "employer"
				}
				val := valRaw
				if names {
					val = key(valRaw)
				}
				return []graphFact{{
					Entity:     holder,
					Relation:   key(propRaw),
					Value:      val,
					Names:      names,
					At:         at,
					Source:     source,
					Evidence:   evidence,
					Confidence: 0.95,
				}}
			}
		}
	}

	// Resolve subject at start of clause.
	subj, restWords, ok := bindClauseSubject(speaker, partner, words)
	if !ok || len(restWords) < 2 {
		return nil
	}
	rest := strings.Join(restWords, " ")
	restLow := strings.ToLower(rest)

	// Pattern B: "<Person> is my/your <kin>"
	if strings.HasPrefix(restLow, "is my ") || strings.HasPrefix(restLow, "is your ") {
		holder := speaker
		if strings.HasPrefix(restLow, "is your ") {
			if partner == "" {
				return nil
			}
			holder = partner
		}
		kinWord := strings.ToLower(cleanObjectValue(strings.Join(restWords[2:], "_")))
		if kin, ok := kinshipNouns[kinWord]; ok {
			return []graphFact{{
				Entity:     holder,
				Relation:   kin,
				Value:      key(subj),
				Names:      true,
				At:         at,
				Source:     source,
				Evidence:   evidence,
				Confidence: 0.95,
			}}
		}
	}

	// Pattern C: Employment transitions & retractions.
	for _, p := range []string{
		"no longer work at ", "no longer works at ",
		"no longer work for ", "no longer works for ",
		"left ", "quit ",
	} {
		if strings.HasPrefix(restLow, p) {
			target := cleanObjectValue(rest[len(p):])
			if target != "" {
				// Emit a retraction on `employer` so the prior employer moves
				// to expired, plus a historical `left` edge to the company.
				return []graphFact{
					{
						Entity:   subj,
						Relation: "employer",
						Value:    "",
						Names:    false,
						At:       at,
						Source:   source,
						Evidence: evidence,
					},
					{
						Entity:     subj,
						Relation:   "left_employer",
						Value:      key(target),
						Names:      true,
						At:         at,
						Source:     source,
						Evidence:   evidence,
						Confidence: 0.95,
					},
				}
			}
		}
	}

	for _, p := range []string{
		"work at ", "works at ", "work for ", "works for ",
		"am working at ", "is working at ", "joined ",
	} {
		if strings.HasPrefix(restLow, p) {
			org := cleanObjectValue(rest[len(p):])
			if org != "" {
				// Employer is treated as a single-valued property or edge;
				// filing as scalar property `employer` AND edge `works_at`
				// lets both single-value supersession and graph walks work.
				return []graphFact{
					{
						Entity:     subj,
						Relation:   "employer",
						Value:      org,
						Names:      false,
						At:         at,
						Source:     source,
						Evidence:   evidence,
						Confidence: 0.95,
					},
					{
						Entity:     subj,
						Relation:   "works_at",
						Value:      key(org),
						Names:      true,
						At:         at,
						Source:     source,
						Evidence:   evidence,
						Confidence: 0.95,
					},
				}
			}
		}
	}

	// Pattern D: Location / residence state ("live in", "lives in", "moved to", "relocated to").
	for _, p := range []string{
		"live in ", "lives in ", "am living in ", "is living in ",
		"moved to ", "relocated to ",
	} {
		if strings.HasPrefix(restLow, p) {
			place := cleanObjectValue(rest[len(p):])
			if place != "" {
				return []graphFact{{
					Entity:     subj,
					Relation:   "lives_in",
					Value:      place,
					Names:      false,
					At:         at,
					Source:     source,
					Evidence:   evidence,
					Confidence: 0.95,
				}}
			}
		}
	}

	// Pattern E: Role / occupation ("am a <role>", "is a <role>", "became a <role>").
	for _, p := range []string{
		"am a ", "am an ", "is a ", "is an ", "became a ", "became an ", "work as a ", "work as an ", "works as a ", "works as an ",
	} {
		if strings.HasPrefix(restLow, p) {
			role := cleanObjectValue(rest[len(p):])
			if role != "" {
				return []graphFact{{
					Entity:     subj,
					Relation:   "role",
					Value:      role,
					Names:      false,
					At:         at,
					Source:     source,
					Evidence:   evidence,
					Confidence: 0.95,
				}}
			}
		}
	}

	_ = low
	return nil
}

func matchGeneralClause(speaker, partner, clause, at, source, evidence string) (graphFact, bool) {
	s := trimConversationalLead(strings.TrimSpace(clause))
	words := strings.Fields(s)
	if len(words) < 2 {
		return graphFact{}, false
	}
	subj, rest, ok := bindClauseSubject(speaker, partner, words)
	if !ok || len(rest) == 0 {
		return graphFact{}, false
	}
	verb, obj, ok := splitVerbObject(rest)
	if !ok || verb == "" || obj == "" {
		return graphFact{}, false
	}
	return graphFact{
		Entity:     subj,
		Relation:   key(verb),
		Value:      obj,
		Names:      false,
		At:         at,
		Source:     source,
		Evidence:   evidence,
		Confidence: 0.9,
	}, true
}

func bindClauseSubject(speaker, partner string, words []string) (string, []string, bool) {
	w0 := words[0]
	low0 := strings.ToLower(w0)
	switch low0 {
	case "i":
		return speaker, words[1:], true
	case "i'm", "im":
		return speaker, append([]string{"am"}, words[1:]...), true
	case "i've", "ive":
		return speaker, append([]string{"have"}, words[1:]...), true
	case "i'd", "id":
		return speaker, append([]string{"would"}, words[1:]...), true
	case "i'll", "ill":
		return speaker, append([]string{"will"}, words[1:]...), true
	case "you":
		if partner == "" {
			return "", nil, false
		}
		return partner, words[1:], true
	case "you're", "youre":
		if partner == "" {
			return "", nil, false
		}
		return partner, append([]string{"are"}, words[1:]...), true
	}

	// Proper-noun subject (1 to 3 capitalised words not in discourse stop-list).
	if isCapitalisedNameWord(w0) {
		end := 1
		for end < len(words) && end < 3 && isCapitalisedNameWord(words[end]) {
			end++
		}
		name := key(strings.Join(words[:end], " "))
		if name != "" {
			return name, words[end:], true
		}
	}
	return "", nil, false
}

var stopProperWords = map[string]bool{
	"the": true, "a": true, "an": true, "this": true, "that": true, "these": true, "those": true,
	"it": true, "he": true, "she": true, "they": true, "we": true, "there": true, "here": true,
	"today": true, "yesterday": true, "tomorrow": true, "now": true, "then": true, "yes": true, "no": true,
	"well": true, "so": true, "but": true, "and": true, "or": true, "if": true, "when": true, "while": true,
	"since": true, "because": true, "although": true, "though": true, "actually": true, "honestly": true,
}

func isCapitalisedNameWord(w string) bool {
	w = strings.Trim(w, ",.;:!?\"'()")
	if w == "" || stopProperWords[strings.ToLower(w)] {
		return false
	}
	r := []rune(w)
	return unicode.IsUpper(r[0])
}

func splitVerbObject(words []string) (string, string, bool) {
	for len(words) > 0 && isAdverbHedge(words[0]) {
		words = words[1:]
	}
	if len(words) == 0 {
		return "", "", false
	}
	neg := false
	if n, negated := stripNegationPrefix(words); negated {
		neg = true
		words = n
	} else if n := stripAuxiliaryPrefix(words); len(n) < len(words) {
		words = n
	}
	for len(words) > 0 && isAdverbHedge(words[0]) {
		words = words[1:]
	}
	if len(words) < 2 {
		return "", "", false
	}
	verb := strings.ToLower(strings.Trim(words[0], ",.;:!?\"'()"))
	obj := cleanObjectValue(strings.Join(words[1:], " "))
	if verb == "" || obj == "" {
		return "", "", false
	}
	if neg {
		verb = "not_" + verb
	}
	return verb, obj, true
}

func stripNegationPrefix(words []string) ([]string, bool) {
	if len(words) >= 2 {
		w0, w1 := strings.ToLower(words[0]), strings.ToLower(words[1])
		if (w0 == "do" || w0 == "does" || w0 == "did" || w0 == "am" || w0 == "is" || w0 == "are" || w0 == "was" || w0 == "were" || w0 == "have" || w0 == "has" || w0 == "had" || w0 == "will" || w0 == "would" || w0 == "can" || w0 == "could") && w1 == "not" {
			return words[2:], true
		}
		if w0 == "no" && w1 == "longer" {
			return words[2:], true
		}
	}
	if len(words) >= 1 {
		switch strings.ToLower(words[0]) {
		case "don't", "dont", "doesn't", "doesnt", "didn't", "didnt", "won't", "wont", "can't", "cant", "never":
			return words[1:], true
		}
	}
	return words, false
}

func stripAuxiliaryPrefix(words []string) []string {
	if len(words) >= 2 {
		w0, w1 := strings.ToLower(words[0]), strings.ToLower(words[1])
		if (w0 == "have" || w0 == "has" || w0 == "had") && w1 == "been" {
			return words[2:]
		}
	}
	if len(words) >= 1 {
		switch strings.ToLower(words[0]) {
		case "have", "has", "had", "will", "would", "can", "could":
			return words[1:]
		}
	}
	return words
}

func isAdverbHedge(w string) bool {
	switch strings.ToLower(strings.Trim(w, ",")) {
	case "really", "actually", "just", "also", "still", "always", "recently", "currently":
		return true
	}
	return false
}

func trimConversationalLead(s string) string {
	for _, lead := range []string{
		"Well, ", "well, ", "Oh, ", "oh, ", "Yeah, ", "yeah, ", "Yes, ", "yes, ",
		"No, ", "no, ", "Actually, ", "actually, ", "Honestly, ", "honestly, ",
		"By the way, ", "by the way, ", "Anyway, ", "anyway, ", "So, ", "so, ",
	} {
		if after, ok := strings.CutPrefix(s, lead); ok {
			s = strings.TrimSpace(after)
		}
	}
	return s
}

func cleanObjectValue(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, ",.;:!?\"'")
	return strings.TrimSpace(s)
}

func splitTurnSentences(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '.', '!', '?', '\n':
			if i+1 == len(s) || s[i] == '\n' || s[i+1] == ' ' || s[i+1] == '\t' {
				out = append(out, s[start:i+1])
				start = i + 1
			}
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func splitTurnClauses(s string) []string {
	for _, sep := range []string{", and ", " and I ", "; ", ", but "} {
		if strings.Contains(s, sep) {
			var out []string
			parts := strings.Split(s, sep)
			for i, p := range parts {
				if i > 0 && sep == " and I " {
					p = "I " + p
				}
				out = append(out, splitTurnClauses(p)...)
			}
			return out
		}
	}
	return []string{s}
}
