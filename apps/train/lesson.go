package train

// lesson.go — a capability's lesson: the org's captured rows and its corrections,
// taught by the decision service (kai's POST /v1/teach) on the CPU, without
// encoding a row again, and the version it makes.
//
// The lesson reads the captures over the base Kai serves now (the org's newest
// capture's): the question's own rows, each the right answer a selected
// correction names or, uncorrected, Kai's own answer taken as right; and every
// other question's, which the capability must never move. A row a correction
// names that the capability was not taught from is left out: it is known wrong
// and is not a target. The service answers the smallest form that passed its
// gate, or the best that did not, as a tar and its evaluation; the tar is kept
// as an object of the org's, sealed under its key, and the version records it.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

const (
	// lessonRows is the most rows a lesson reads: the service's own bound.
	lessonRows = 16_384
	// keptRows and otherRows bound the newest rows of the question Kai answered
	// right, and of other questions, a lesson reads beside every target: what keeps
	// a lesson to seconds on the decision service's CPU.
	keptRows  = 2048
	otherRows = 4096
	// lessonFetches is how many captures are read at once.
	lessonFetches = 16
	// lessonTimeout bounds one lesson at the service.
	lessonTimeout = 3 * time.Minute
	// tarBytes is the most a capability's tar may be: an org's table share.
	tarBytes = 32 << 20
)

// teacher is the decision service a lesson is taught by.
type teacher struct {
	url  string
	http *http.Client
}

// example is one row of a lesson, as the service reads it.
type example struct {
	ID     string    `json:"id"`
	Key    string    `json:"key"`
	Type   string    `json:"type"`
	Labels []string  `json:"labels"`
	Pooled string    `json:"pooled"`
	Tokens int       `json:"tokens,omitempty"`
	Logits []float64 `json:"logits"`
	Answer *string   `json:"answer,omitempty"`
}

type lessonBody struct {
	Representation string    `json:"representation"`
	Question       Question  `json:"question"`
	Rows           []example `json:"rows"`
}

// taughtBody is the service's answer.
type taughtBody struct {
	Tar        string          `json:"tar"`
	Evaluation json.RawMessage `json:"evaluation"`
}

// lesson teaches c a version from its corrections over the org's captures, and
// answers it unnumbered: its artifact kept, its evaluation with what the lesson
// cost.
func (o ops) lesson(ctx context.Context, st *store, org string, c capability) (*Version, error) {
	base, err := st.base(ctx)
	if err != nil {
		return nil, o.fault("base read", org, err)
	}
	if base == "" {
		return nil, conflict("no_captures", "no decision of this org was captured: turn capture on and make Kai decisions first")
	}
	rows, err := o.rowsFor(ctx, st, org, base, c)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	t, err := o.s.State.teacher.teach(ctx, lessonBody{Representation: base, Question: c.Question, Rows: rows})
	if err != nil {
		return nil, err
	}
	took := time.Since(started)
	tar, err := base64.StdEncoding.DecodeString(t.Tar)
	if err != nil || len(tar) == 0 || len(tar) > tarBytes {
		return nil, zip.Errorf(http.StatusBadGateway, "the decision service answered a lesson with no artifact")
	}
	var ev struct {
		Base struct {
			SHA256 string `json:"sha256"`
		} `json:"base"`
		Candidate struct {
			Form string `json:"form"`
			Rank int    `json:"rank"`
		} `json:"candidate"`
		Promotion string `json:"promotion"`
	}
	if err := json.Unmarshal(t.Evaluation, &ev); err != nil || ev.Candidate.Form == "" {
		return nil, zip.Errorf(http.StatusBadGateway, "the decision service answered a lesson with no evaluation")
	}
	// what the lesson cost: its seconds on the decision service's CPU, at the rate
	// a CPU-second of training is priced
	rate := cloud.RateMicros(ctx, "train", "kai-cpu-hour", cloud.RuntimeHourMicros)
	micro := (rate*took.Milliseconds() + 1_800_000) / 3_600_000
	evaluation, err := withCost(t.Evaluation, micro)
	if err != nil {
		return nil, o.fault("evaluation", org, err)
	}
	sum := sha256.Sum256(tar)
	hexsum := hex.EncodeToString(sum[:])
	if err := o.keepArtifact(ctx, st, org, hexsum, tar); err != nil {
		return nil, err
	}
	return &Version{SHA256: hexsum, BaseSHA256: ev.Base.SHA256, Representation: base, Form: ev.Candidate.Form, Rank: ev.Candidate.Rank, Bytes: int64(len(tar)),
		Questions: []string{c.Question.Key}, Passed: ev.Promotion == "publishable", Evaluation: evaluation,
		Corrections: c.Corrections, By: caller(ctx), At: unix()}, nil
}

// withCost is an evaluation with what its lesson cost, micro-USD.
func withCost(ev json.RawMessage, micro int64) (json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(ev, &m); err != nil {
		return nil, err
	}
	m["cost_micro_usd"], _ = json.Marshal(micro)
	return json.Marshal(m)
}

// rowsFor are the lesson's rows over base: c's question's first, newest first,
// then every other question's.
func (o ops) rowsFor(ctx context.Context, st *store, org, base string, c capability) ([]example, error) {
	caps, err := st.captures(ctx, `WHERE representation = $1 ORDER BY at DESC, decision LIMIT $2`, base, 4*lessonRows)
	if err != nil {
		return nil, o.fault("captures read", org, err)
	}
	corr, err := st.corrections(ctx, ``)
	if err != nil {
		return nil, o.fault("corrections read", org, err)
	}
	type at struct{ decision, question string }
	named := make(map[at]correction, len(corr))
	for _, k := range corr {
		named[at{k.Decision, k.Question}] = k
	}
	taught := map[string]bool{}
	for _, id := range c.Corrections {
		taught[id] = true
	}
	var mine, others []captureRow
	answers := map[at]*string{}
	targets, kept := 0, 0
	for _, r := range caps {
		k, corrected := named[at{r.Decision, r.Question}]
		q := Question{Key: r.Question, Type: r.Kind, Labels: r.Labels}
		if q.same(c.Question) {
			if corrected && !taught[k.ID] {
				continue
			}
			// a row a capability answered otherwise than Kai, and no one corrected, is
			// neither Kai's answer nor a confirmed one: it teaches nothing
			if !corrected && r.Capability != "" && r.Changed {
				continue
			}
			if corrected {
				a := k.Answer
				answers[at{r.Decision, r.Question}] = &a
				targets++
			} else if kept++; kept > keptRows {
				continue
			}
			mine = append(mine, r)
			continue
		}
		if len(others) >= otherRows {
			continue
		}
		if corrected && k.Type != "" {
			a := k.Answer
			answers[at{r.Decision, r.Question}] = &a
		}
		others = append(others, r)
	}
	if targets == 0 {
		return nil, conflict("no_corrections", "none of "+c.Name+"'s corrections has a state captured over the base Kai serves now")
	}
	chosen := append(mine, others...)
	if len(chosen) > lessonRows {
		chosen = chosen[:lessonRows]
	}
	// each decision's states, read once
	decisions := map[string]bool{}
	for _, r := range chosen {
		decisions[r.Decision] = true
	}
	held, err := o.statesOf(ctx, org, decisions)
	if err != nil {
		return nil, err
	}
	out := make([]example, 0, len(chosen))
	for _, r := range chosen {
		s, ok := held[r.Decision]
		if !ok || s.Representation != base {
			continue
		}
		q, ok := s.Questions[r.Question]
		if !ok {
			continue
		}
		out = append(out, example{ID: r.Decision, Key: r.Question, Type: q.Type, Labels: q.Labels, Pooled: q.Pooled,
			Tokens: q.Tokens, Logits: q.Logits, Answer: answers[at{r.Decision, r.Question}]})
	}
	return out, nil
}

// statesOf reads each decision's captured states, lessonFetches at a time. A
// capture whose bytes are gone is left out; any other failure fails the lesson.
func (o ops) statesOf(ctx context.Context, org string, decisions map[string]bool) (map[string]*states, error) {
	ids := make(chan string)
	var mu sync.Mutex
	out := make(map[string]*states, len(decisions))
	var failed error
	var wg sync.WaitGroup
	for range lessonFetches {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for d := range ids {
				b, err := o.s.State.objects.read(ctx, captureKey(org, d))
				var s states
				if err == nil {
					err = json.Unmarshal(b, &s)
				}
				mu.Lock()
				switch {
				case err == nil:
					out[d] = &s
				case errors.Is(err, errMissing):
				default:
					if failed == nil {
						failed = err
					}
				}
				mu.Unlock()
			}
		}()
	}
	for d := range decisions {
		ids <- d
	}
	close(ids)
	wg.Wait()
	if failed != nil {
		return nil, o.fault("capture read", org, failed)
	}
	return out, nil
}

// keepArtifact keeps a capability's tar as an object of the org's, sealed under
// its key; bytes it already holds are not written again.
func (o ops) keepArtifact(ctx context.Context, st *store, org, sum string, tar []byte) error {
	if held, err := st.holds(ctx, sum); err != nil {
		return o.fault("object read", org, err)
	} else if held {
		return nil
	}
	seal, err := o.s.State.seal(ctx, st, org)
	if err != nil {
		return o.fault("org key", org, err)
	}
	k := key(org, sum)
	if err := o.s.State.objects.write(ctx, k, tar, tarType, seal); err != nil {
		return o.fault("artifact store", org, err)
	}
	payer := ""
	if p := cloud.PayerOf(ctx); p.Billable() {
		payer = p.Wallet.String()
	}
	if err := st.keepObject(ctx, sum, k, int64(len(tar)), payer, seal, unix()); err != nil {
		return o.fault("object write", org, err)
	}
	return nil
}

// teach asks the service for a lesson.
func (t *teacher) teach(ctx context.Context, l lessonBody) (*taughtBody, error) {
	if t == nil || t.url == "" {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "this deployment names no decision service to teach a capability")
	}
	body, err := json.Marshal(l)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, lessonTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(t.url, "/")+"/v1/teach", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.http.Do(req)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "the decision service could not be reached to teach a capability")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2*tarBytes))
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "the decision service's lesson broke off")
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		switch {
		case resp.StatusCode == http.StatusConflict && e.Error.Code == "base_moved":
			return nil, conflict(validating, "Kai's base moved while the lesson was read; make Kai decisions again and train")
		case resp.StatusCode == http.StatusUnprocessableEntity:
			return nil, invalid("%s", e.Error.Message)
		}
		return nil, zip.Errorf(http.StatusBadGateway, "the decision service refused the lesson (%d)", resp.StatusCode)
	}
	var out taughtBody
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "the decision service's lesson is not JSON: %v", err)
	}
	return &out, nil
}

// encode runs one cold-start example state through the decision service with
// X-Capture: 1, returning the decision id and captured states JSON.
func (t *teacher) encode(ctx context.Context, org string, q Question, state json.RawMessage) (string, json.RawMessage, error) {
	if t == nil || t.url == "" {
		return "", nil, zip.Errorf(http.StatusServiceUnavailable, "this deployment names no decision service to encode examples")
	}
	qSpec := map[string]any{
		"type":         q.Type,
		"instructions": q.Key,
	}
	switch q.Type {
	case "choice":
		qSpec["criteria"] = q.Labels
	case "score":
		qSpec["levels"] = q.Labels
	default:
		qSpec["labels"] = q.Labels
	}
	guardKey := "_ood_guard"
	if q.Key == guardKey {
		guardKey = "_ood_guard_2"
	}
	guardSpec := map[string]any{
		"type":         "noul",
		"instructions": guardKey,
		"labels":       []string{"false", "true"},
	}
	reqBody, err := json.Marshal(map[string]any{
		"model": "kai",
		"state": state,
		"questions": map[string]any{
			q.Key:    qSpec,
			guardKey: guardSpec,
		},
	})
	if err != nil {
		return "", nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, lessonTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(t.url, "/")+"/v1/decisions", bytes.NewReader(reqBody))
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Capture", "1")
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	resp, err := t.http.Do(req)
	if err != nil {
		return "", nil, zip.Errorf(http.StatusBadGateway, "the decision service could not be reached to encode examples")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", nil, zip.Errorf(http.StatusBadGateway, "the decision service's response broke off")
	}
	if resp.StatusCode != http.StatusOK {
		return "", nil, zip.Errorf(http.StatusBadGateway, "the decision service refused example encoding (%d)", resp.StatusCode)
	}
	var out struct {
		ID      string          `json:"id"`
		Capture json.RawMessage `json:"capture"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Capture) == 0 {
		return "", nil, zip.Errorf(http.StatusBadGateway, "the decision service returned no capture for example")
	}
	return out.ID, out.Capture, nil
}

// ingestExamples encodes cold-start (state, answer) examples once through the
// decision service, stores their captures, files corrections for examples that
// disagree with base Kai, and ensures baseline preserve witnesses exist when the
// caller supplied only mistakes.
func (o ops) ingestExamples(ctx context.Context, st *store, org string, q Question, examples []ExampleIn) ([]string, error) {
	if len(examples) == 0 || len(examples) > 512 {
		return nil, invalid("examples: 1 to 512 examples")
	}
	resolved := make([]string, len(examples))
	for i, ex := range examples {
		trimmed := bytes.TrimSpace(ex.State)
		if len(trimmed) == 0 || !json.Valid(trimmed) {
			return nil, invalid("examples[%d].state: valid JSON state required", i)
		}
		a, ok := resolve(q.Type, q.Labels, ex.Answer)
		if !ok {
			return nil, invalid("examples[%d].answer: %q is none of %s's options: %s", i, ex.Answer, q.Key, strings.Join(q.Labels, ", "))
		}
		if !note(ex.Note, 2000) {
			return nil, invalid("examples[%d].note: at most 2000 characters of text", i)
		}
		resolved[i] = a
	}
	if on, err := o.capturing(ctx, st); err != nil {
		return nil, o.fault("capture read", org, err)
	} else if !on {
		if err := st.set(ctx, "capture", "1"); err != nil {
			return nil, o.fault("capture write", org, err)
		}
	}
	seal, err := o.s.State.seal(ctx, st, org)
	if err != nil {
		return nil, o.fault("org key", org, err)
	}
	now := unix()
	by := caller(ctx)
	seenDec := map[string]bool{}
	var ids []string
	var lastStates states
	var baseLabel string
	targets, preserve := 0, 0
	for i, ex := range examples {
		decID, capRaw, err := o.s.State.teacher.encode(ctx, org, q, ex.State)
		if err != nil {
			return nil, err
		}
		if !decisionID.MatchString(decID) || seenDec[decID] {
			decID = newID("dec_")
		}
		seenDec[decID] = true
		var s states
		if err := json.Unmarshal(capRaw, &s); err != nil {
			return nil, invalid("a capture that is not the decision service's: %v", err)
		}
		rows, err := s.rows(decID, "", now)
		if err != nil {
			return nil, err
		}
		if err := o.s.State.objects.write(ctx, captureKey(org, decID), capRaw, jsonType, seal); err != nil {
			return nil, o.fault("capture store", org, err)
		}
		if err := st.capture(ctx, rows); err != nil {
			return nil, o.fault("capture write", org, err)
		}
		lastStates = s
		var qRow *captureRow
		for j := range rows {
			if rows[j].Question == q.Key {
				qRow = &rows[j]
				break
			}
		}
		if qRow == nil {
			return nil, zip.Errorf(http.StatusBadGateway, "the decision service did not capture question %q", q.Key)
		}
		baseLabel = qRow.Base
		if resolved[i] == qRow.Base {
			preserve++
			continue
		}
		c := correction{
			ID:       newID("cor_"),
			Decision: decID,
			Question: q.Key,
			Type:     q.Type,
			Labels:   q.Labels,
			Answer:   resolved[i],
			Base:     qRow.Base,
			Note:     ex.Note,
			By:       by,
			At:       now,
		}
		if err := st.correct(ctx, c); err != nil {
			return nil, o.fault("correction write", org, err)
		}
		ids = append(ids, c.ID)
		targets++
	}
	if len(ids) == 0 {
		return nil, invalid("examples: base Kai already answered every example as labeled; include at least one example Kai gets wrong")
	}
	// If the caller supplied only failure examples (preserve < targets), synthesize
	// neutral zero-pooled baseline witness captures so the lesson's preserve >= targets
	// gate has baseline reference rows without distorting the learned head weights.
	if preserve < targets && lastStates.Width > 0 {
		zeroPooled := base64.StdEncoding.EncodeToString(make([]byte, 2*lastStates.Width))
		n := len(q.Labels)
		baseIdx := 0
		for idx, l := range q.Labels {
			if l == baseLabel {
				baseIdx = idx
				break
			}
		}
		logits := make([]float64, n)
		probs := make([]float64, n)
		for idx := range q.Labels {
			if idx == baseIdx {
				logits[idx] = 2.5
				probs[idx] = 0.92
			} else {
				logits[idx] = 0.0
				probs[idx] = 0.08 / float64(max(1, n-1))
			}
		}
		guardKey := "_ood_guard"
		if q.Key == guardKey {
			guardKey = "_ood_guard_2"
		}
		synStates := states{
			SHA256:         lastStates.SHA256,
			Representation: lastStates.Representation,
			Width:          lastStates.Width,
			Questions: map[string]stateOfOne{
				q.Key: {
					Type:   q.Type,
					Labels: q.Labels,
					Pooled: zeroPooled,
					Tokens: 8,
					Logits: logits,
					Probs:  probs,
				},
				guardKey: {
					Type:   "noul",
					Labels: []string{"false", "true"},
					Pooled: zeroPooled,
					Tokens: 8,
					Logits: []float64{2.5, 0.0},
					Probs:  []float64{0.92, 0.08},
				},
			},
		}
		synRaw, _ := json.Marshal(synStates)
		for k := 0; k < targets-preserve; k++ {
			synDec := newID("dec_")
			rows, err := synStates.rows(synDec, "", now)
			if err != nil {
				return nil, err
			}
			if err := o.s.State.objects.write(ctx, captureKey(org, synDec), synRaw, jsonType, seal); err != nil {
				return nil, o.fault("capture store", org, err)
			}
			if err := st.capture(ctx, rows); err != nil {
				return nil, o.fault("capture write", org, err)
			}
		}
	}
	return ids, nil
}

// keepObject records bytes kept for the org: what its storage meter counts and
// retention reads.
func (s *store) keepObject(ctx context.Context, sum, key string, size int64, payer, seal string, now int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO objects (sha256, key, size, payer, seal, stored) VALUES ($1,$2,$3,$4,$5,$6)
ON CONFLICT(sha256) DO UPDATE SET key = excluded.key, seal = excluded.seal`, sum, key, size, payer, seal, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO storage (payer, metered_at) VALUES ($1,$2) ON CONFLICT (payer) DO NOTHING`, payer, now); err != nil {
		return err
	}
	return tx.Commit()
}

// serveVersion copies a capability's artifact to the serving store, where the
// decision service fetches it (objects.serve).
func (o ops) serveVersion(ctx context.Context, st *store, org, sum string) error {
	k, err := o.keyOf(ctx, st, org, sum)
	if err != nil {
		return o.fault("artifact read", org, err)
	}
	if err := o.s.State.objects.serve(ctx, k, org, sum); err != nil {
		o.s.Log.Error("train: a capability could not be copied to the serving store", "org", org, "sha256", sum, "err", err)
		return zip.Errorf(http.StatusServiceUnavailable, "the capability could not be copied where Kai reads it; try again")
	}
	return nil
}
