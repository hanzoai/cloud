package principals

// statements.go — the TaxPrincipalCredential: a statement, signed for each of an
// org's agents, that the principal the agent answers to holds a certified, valid
// tax form.
//
// THE FORM IS THE AGENT-PRINCIPAL STATEMENT. It is the JWS attest.go signs — the
// same key, typ and issuer, verified by principal.Verify against GET
// /v1/principal/keys — carrying one more claim, `tax`, which names the class of
// form (w9, w8ben, w8bene) and when its certification was signed. A verifier that
// trusts an agent's principal statement reads the credential with the same code.
//
// IT FOLLOWS THE FORM, AND NOTHING ELSE SAYS IT. tax owns the form and agents own
// the roster; reconcile asks both, as the org, and makes the statements match
// them: one live statement per agent while the form is certified and valid, and
// none once it is not. Tax asks for a reconcile when a certification completes or
// a form is rewritten, and the sweep asks again on its own clock for every org
// whose form it last saw certified and every org holding a statement, so a W-8
// that expires, an agent created since — the org's first, too — and a nudge that
// never arrived all converge.
//
// A STATEMENT IS WITHDRAWN, NEVER EDITED. Revoking one records when and why beside
// it; the JWS it was does not change. Its status is readable by anyone who holds
// it, at GET /v1/principal/statements/{id} — the JWS already names its agent and
// org, so the status discloses nothing the holder does not have.
//
// ON CHAIN it is a claim the platform's ONCHAINID ClaimIssuer signs under the
// TAX_PRINCIPAL topic. That issuer is Issuer below; this deployment binds none
// until the contract lands (statements_chain_test.go names what is missing), and
// an unbound issuer is no claim, not a failed one.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	agentpeer "github.com/hanzoai/cloud/client/agent"
	taxpeer "github.com/hanzoai/cloud/client/tax"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/iam"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk/dialect"
)

// A statement's status is readable by whoever holds it.
func init() {
	spec.Open("/v1/principal/statements/:id", "GET")
}

const (
	// taxTTL is how long a statement resting on a W-9 stands before it is signed
	// again. A W-9 does not expire; a statement does, so an offline verifier holds
	// nothing older than this, and the sweep renews it while the form stands.
	taxTTL = 365 * 24 * time.Hour
	// renewBefore is how long before a W-9 statement expires the sweep renews it.
	renewBefore = 30 * 24 * time.Hour
	// sweepEvery is how often the sweep runs.
	sweepEvery = time.Hour
)

// Issuer puts a TaxPrincipalCredential on chain as a claim, and withdraws it.
//
// It is the one call the chain binding makes: the ONCHAINID ClaimIssuer signs a
// claim under the TAX_PRINCIPAL topic about the principal's commitment, and a
// revocation adds the claim's signature to the issuer's revocation set. The
// statement's own id is the reference that ties the two.
type Issuer interface {
	// Issue signs the claim for one statement and answers its on-chain reference.
	Issue(ctx context.Context, s client.Statement) (string, error)
	// Revoke withdraws the claim Issue answered ref for.
	Revoke(ctx context.Context, ref string) error
}

// taxForm is what a statement rests on: the class of form and when its
// certification was signed. A new signature is a new form.
type taxForm struct {
	form   string
	signed int64
}

// reconcile makes the org's statements match its form and roster, as the org: ctx
// acts for org, so tax and agents answer that org's own facts. Every change is on
// the trail with the statement it changed.
func (o ops) reconcile(ctx context.Context, org string) (*client.Reconciled, error) {
	form, err := taxpeer.TaxStanding(ctx, &client.StandingIn{})
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "principal: tax: %v", err)
	}
	roster, err := agentpeer.AgentsRoster(ctx, &client.RosterIn{})
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "principal: agents: %v", err)
	}
	now := time.Now()
	live, err := o.s.State.store.live(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%v", err)
	}
	want := taxForm{form: form.Form, signed: form.Signed}
	valid := form.Form != "" && form.Certified && form.Valid
	if err := o.s.State.store.saw(ctx, org, valid, want, form.Expires, now.Unix()); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%v", err)
	}
	agents := map[string]iam.AgentBrief{}
	if valid {
		for _, a := range roster.Agents {
			agents[a.ID] = a
		}
	}
	out := &client.Reconciled{}
	covered := map[string]bool{}
	for _, st := range live {
		why := ""
		switch {
		case !valid:
			why = "the form is no longer certified and valid"
		case st.Form != want.form || st.signed != want.signed:
			why = "the form was signed again"
		case agents[st.Agent].ID == "":
			why = "the agent is gone"
		case st.Expires > 0 && now.Add(renewBefore).Unix() >= st.Expires && form.Expires == 0:
			why = "renewed"
		}
		if why == "" {
			covered[st.Agent] = true
			if err := o.chain(ctx, st); err != nil {
				return out, err
			}
			continue
		}
		if err := o.revoke(ctx, st, why, now.Unix()); err != nil {
			return out, err
		}
		out.Revoked++
	}
	for _, id := range sortedKeys(agents) {
		if covered[id] {
			continue
		}
		if err := o.issue(ctx, org, agents[id], want, form.Expires, now); err != nil {
			return out, err
		}
		out.Issued++
	}
	return out, nil
}

// issue signs one statement for an agent and records it. It stands until the
// form's own expiry, or taxTTL for a form that has none.
func (o ops) issue(ctx context.Context, org string, a iam.AgentBrief, f taxForm, expires int64, now time.Time) error {
	if expires == 0 {
		expires = now.Add(taxTTL).Unix()
	}
	c := iam.Claims{Iss: issuer(), Sub: a.ID, Name: a.Name, Org: org, Parents: []string{},
		Iat: now.Unix(), Exp: expires, Jti: jti(), Tax: &iam.TaxClaim{Form: f.form, Signed: f.signed}}
	if a.Parent != "" {
		c.Parents = []string{a.Parent}
	}
	jws, _, err := o.s.State.keys.sign(ctx, c)
	if err != nil {
		return err
	}
	st := statementRow{Statement: client.Statement{ID: c.Jti, Agent: a.ID, Org: org, Kind: client.StatementTax, Form: f.form,
		Issued: c.Iat, Expires: c.Exp, Status: statusLive}, signed: f.signed, body: jws}
	if err := o.commit(ctx, func(tx *sql.Tx) ([]audit.Record, error) {
		if err := putStatement(ctx, tx, st); err != nil {
			return nil, err
		}
		rec, err := o.record(ctx, org, "principal.statement.issued", audit.Resource{Type: "principal.statement", ID: st.ID},
			map[string]any{"agent": a.ID, "kind": st.Kind, "form": st.Form, "expires": st.Expires}, false)
		return []audit.Record{rec}, err
	}); err != nil {
		return err
	}
	return o.chain(ctx, st)
}

// chain puts a live statement on chain when an issuer is bound and it is not
// there yet. It runs after the statement is recorded, so a claim never exists for
// a statement the store does not hold; one that failed to issue is issued by the
// next reconcile.
func (o ops) chain(ctx context.Context, st statementRow) error {
	is := o.s.State.claims
	if is == nil || st.claim != "" {
		return nil
	}
	ref, err := is.Issue(ctx, st.Statement)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "principal: chain claim: %v", err)
	}
	return o.commit(ctx, func(tx *sql.Tx) ([]audit.Record, error) {
		if _, err := tx.ExecContext(ctx, `UPDATE statements SET claim = $1 WHERE id = $2`, ref, st.ID); err != nil {
			return nil, fmt.Errorf("principal: record chain claim: %w", err)
		}
		rec, err := o.record(ctx, st.Org, "principal.statement.claimed", audit.Resource{Type: "principal.statement", ID: st.ID},
			map[string]any{"agent": st.Agent, "claim": ref}, false)
		return []audit.Record{rec}, err
	})
}

// revoke withdraws one statement, and its claim on chain when it has one.
func (o ops) revoke(ctx context.Context, st statementRow, why string, at int64) error {
	if is := o.s.State.claims; is != nil && st.claim != "" {
		if err := is.Revoke(ctx, st.claim); err != nil {
			return zip.Errorf(http.StatusBadGateway, "principal: chain claim: %v", err)
		}
	}
	return o.commit(ctx, func(tx *sql.Tx) ([]audit.Record, error) {
		if err := revokeStatement(ctx, tx, st.ID, why, at); err != nil {
			return nil, err
		}
		rec, err := o.record(ctx, st.Org, "principal.statement.revoked", audit.Resource{Type: "principal.statement", ID: st.ID},
			map[string]any{"agent": st.Agent, "kind": st.Kind, "reason": why}, false)
		return []audit.Record{rec}, err
	})
}

// Brings the statements signed for the caller's agents in line with the tax form
// the caller certified: one TaxPrincipalCredential for each agent while the form
// is certified and valid, none once it is not, and a new one for every agent when
// the form is signed again. Tax asks for it when a certification completes or a
// form is rewritten; the facts are read from tax and agents, never from the call.
func (o ops) planeStatements(ctx context.Context, _ *client.StatementsIn) (*client.Reconciled, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("principal statements: no org on the call")
	}
	if o.s.State.audit == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%v", errUnaudited)
	}
	return o.reconcile(ctx, org)
}

// statementRef addresses one statement.
type statementRef struct {
	// ID is the statement's jti, as its JWS carries it.
	ID string `json:"id"`
}

// Reads whether one statement the platform signed about an agent still stands:
// live, expired, or revoked and when. Open to anyone — a verifier holding a
// TaxPrincipalCredential checks it here after checking its signature offline
// against GET /v1/principal/keys. The answer names nothing the statement itself
// does not.
func (o ops) statement(ctx context.Context, in *statementRef) (*client.Statement, error) {
	st, err := o.s.State.store.statement(ctx, strings.TrimSpace(in.ID))
	switch {
	case errors.Is(err, errNotFound):
		return nil, zip.ErrNotFound("statement not found")
	case err != nil:
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%v", err)
	}
	out := st.Statement
	out.Status = st.status(time.Now().Unix())
	return &out, nil
}

// maxPublic bounds one public read: a page of a shop names at most this many
// sellers.
const maxPublic = 200

// Answers, for each org named, whether its tax form is certified and valid — what
// its agents' TaxPrincipalCredentials rest on, and what a shop shows beside a
// seller, and all it shows: no form, number, country or agent. It is read off the
// form and not off the statements, so a seller with no agents yet is documented
// as surely as one with many. It needs no org on the call, because the answer is
// the one a holder of any of the org's statements can already read at GET
// /v1/principal/statements/{id}.
func (o ops) planePublic(ctx context.Context, in *client.PublicIn) (*client.Publics, error) {
	if len(in.Orgs) > maxPublic {
		return nil, zip.ErrBadRequest(fmt.Sprintf("principal public: at most %d orgs", maxPublic))
	}
	live, err := o.s.State.store.documented(ctx, in.Orgs, time.Now().Unix())
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%v", err)
	}
	out := &client.Publics{Principals: make([]client.Public, 0, len(in.Orgs))}
	for _, org := range in.Orgs {
		out.Principals = append(out.Principals, client.Public{Org: org, Documented: live[org]})
	}
	return out, nil
}

// statementsLoop names the sweep's lease: one process in the deployment runs it.
const statementsLoop = "principals:statements"

// sweep revokes every statement past its expiry and reconciles every org that
// holds one or whose form it last saw certified, on its own clock, until ctx ends.
func (o ops) sweep(ctx context.Context) {
	lead := o.s.Coord.Lead(statementsLoop)
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		_ = lead.Do(ctx, o.sweepOnce)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// sweepOnce is one pass. A failure for one org is logged and the next is tried.
//
// Each statement it revokes and each org it reconciles is fenced first: a pass that
// outlives this process's hold on the sweep stops before its next act.
func (o ops) sweepOnce(ctx context.Context) {
	if o.s.State.audit == nil {
		return
	}
	lead := o.s.Coord.Lead(statementsLoop)
	now := time.Now().Unix()
	due, err := o.s.State.store.expired(ctx, now)
	if err != nil {
		o.s.Log.Error("principal: statements sweep", "err", err)
		return
	}
	lapsed := map[string]bool{}
	for _, st := range due {
		if lead.Hold(ctx) != nil {
			return
		}
		if err := o.revoke(ctx, st, "expired", now); err != nil {
			o.s.Log.Error("principal: revoke an expired statement", "id", st.ID, "err", err)
		}
		lapsed[st.Org] = true
	}
	holders, err := o.s.State.store.holders(ctx)
	if err != nil {
		o.s.Log.Error("principal: statements sweep", "err", err)
		return
	}
	// An org whose last statement just expired holds none, and is reconciled all the
	// same: while its form stands, its agents are signed for again. So is one whose
	// form was certified before it had an agent: the agent it made since is signed
	// for here, because nothing about the form changed to ask for it.
	for _, org := range holders {
		lapsed[org] = true
	}
	for _, org := range sortedKeys(lapsed) {
		if lead.Hold(ctx) != nil {
			return
		}
		cctx, cancel := context.WithTimeout(cloud.For(ctx, org), askBudget)
		if _, err := o.reconcile(cctx, org); err != nil {
			o.s.Log.Warn("principal: reconcile statements", "org", org, "err", err)
		}
		cancel()
	}
}

// ---- store ------------------------------------------------------------------

const (
	statusLive    = "live"
	statusExpired = "expired"
	statusRevoked = "revoked"
)

// statementRow is one statement as the store holds it.
type statementRow struct {
	client.Statement
	signed int64  // the certification signature it rests on
	body   string // the JWS
	claim  string // the on-chain claim reference, when it has one
}

// status is what the statement is at now.
func (s statementRow) status(now int64) string {
	switch {
	case s.Revoked > 0:
		return statusRevoked
	case s.Expires > 0 && now >= s.Expires:
		return statusExpired
	}
	return statusLive
}

const statementCols = `id, org, agent, kind, form, signed, issued, expires, revoked, body, claim`

func scanStatement(sc interface{ Scan(...any) error }) (statementRow, error) {
	var s statementRow
	err := sc.Scan(&s.ID, &s.Org, &s.Agent, &s.Kind, &s.Form, &s.signed, &s.Issued, &s.Expires, &s.Revoked, &s.body, &s.claim)
	return s, err
}

func putStatement(ctx context.Context, tx *sql.Tx, s statementRow) error {
	if err := dialect.Text(s.ID, s.Org, s.Agent, s.Kind, s.Form, s.body, s.claim); err != nil {
		return fmt.Errorf("principal: put statement: %w", err)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO statements (`+statementCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,0,$9,$10)`,
		s.ID, s.Org, s.Agent, s.Kind, s.Form, s.signed, s.Issued, s.Expires, s.body, s.claim)
	if err != nil {
		return fmt.Errorf("principal: put statement: %w", err)
	}
	return nil
}

func revokeStatement(ctx context.Context, tx *sql.Tx, id, why string, at int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE statements SET revoked = $1, reason = $2 WHERE id = $3 AND revoked = 0`, at, why, id)
	if err != nil {
		return fmt.Errorf("principal: revoke statement: %w", err)
	}
	return nil
}

func (s *store) statements(ctx context.Context, q string, args ...any) ([]statementRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+statementCols+` FROM statements WHERE `+q, args...)
	if err != nil {
		return nil, fmt.Errorf("principal: statements: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []statementRow
	for rows.Next() {
		st, err := scanStatement(rows)
		if err != nil {
			return nil, fmt.Errorf("principal: statements: %w", err)
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// live is the org's statements that have not been revoked, oldest first.
func (s *store) live(ctx context.Context, org string) ([]statementRow, error) {
	return s.statements(ctx, `org = $1 AND revoked = 0 ORDER BY issued`, org)
}

// ofOrgStatements is the org's statements, newest first, at most 200.
func (s *store) ofOrgStatements(ctx context.Context, org string) ([]statementRow, error) {
	return s.statements(ctx, `org = $1 ORDER BY issued DESC LIMIT 200`, org)
}

// expired is every unrevoked statement past its expiry.
func (s *store) expired(ctx context.Context, now int64) ([]statementRow, error) {
	return s.statements(ctx, `revoked = 0 AND expires > 0 AND expires <= $1 ORDER BY expires LIMIT 500`, now)
}

// statement is one statement by id.
func (s *store) statement(ctx context.Context, id string) (statementRow, error) {
	st, err := scanStatement(s.db.QueryRowContext(ctx, `SELECT `+statementCols+` FROM statements WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return statementRow{}, errNotFound
	}
	return st, err
}

// documented is which of orgs had a certified, valid form when last reconciled
// that has not expired by now.
func (s *store) documented(ctx context.Context, orgs []string, now int64) (map[string]bool, error) {
	out := map[string]bool{}
	if len(orgs) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(orgs)+1)
	args = append(args, now)
	for _, org := range orgs {
		args = append(args, org)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT org FROM forms WHERE (expires = 0 OR expires > $1)
		AND org IN (`+dialect.Marks(2, len(orgs))+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("principal: documented: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var org string
		if err := rows.Scan(&org); err != nil {
			return nil, fmt.Errorf("principal: documented: %w", err)
		}
		out[org] = true
	}
	return out, rows.Err()
}

// saw records what reconcile last read of org's form: kept while it is certified
// and valid, forgotten once it is not.
func (s *store) saw(ctx context.Context, org string, valid bool, f taxForm, expires, now int64) error {
	q, args := `DELETE FROM forms WHERE org = $1`, []any{org}
	if valid {
		q, args = `INSERT INTO forms (org, form, signed, expires, at) VALUES ($1,$2,$3,$4,$5) ON CONFLICT(org) DO UPDATE SET
			form = excluded.form, signed = excluded.signed, expires = excluded.expires, at = excluded.at`,
			[]any{org, f.form, f.signed, expires, now}
	}
	if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("principal: record form: %w", err)
	}
	return nil
}

// holders is every org with a live statement, and every org whose form was
// certified and valid when last reconciled.
func (s *store) holders(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT org FROM statements WHERE revoked = 0 UNION SELECT org FROM forms ORDER BY org`)
	if err != nil {
		return nil, fmt.Errorf("principal: holders: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var org string
		if err := rows.Scan(&org); err != nil {
			return nil, err
		}
		out = append(out, org)
	}
	return out, rows.Err()
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
