package agents

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/internal/aimodel"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/text"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
	"golang.org/x/crypto/sha3"
)

var (
	errGroupConflict = errors.New("agents: group already exists")
	errGroupNotFound = errors.New("agents: group not found")
)

// Coordination policy kinds supported by multi-layer Bot Groups.
const (
	PolicyLeader    = "leader"
	PolicyQuasar    = "quasar"
	PolicyMajority  = "majority"
	PolicyWeighted  = "weighted"
	PolicyUnanimous = "unanimous"
	PolicyKai       = "kai"
	PolicyCustom    = "custom"
)

// Lux Quasar preset profiles (Polaris fast 3-sample, Aurora balanced 5-sample,
// Pulsar high-assurance 7-sample).
const (
	QuasarPolaris = "polaris"
	QuasarAurora  = "aurora"
	QuasarPulsar  = "pulsar"
)

const (
	defaultPrimaryCapMicroUSD  int64 = 25_000_000 // $25 / week default ceiling
	defaultPrimaryTaskMicroUSD int64 = 5_000_000  // $5 / task default ceiling
	defaultPrimaryPeriod             = "week"
	maxGroupDepth                    = 4
)

// Group is an org-scoped coordination unit of bots at a specific hierarchy layer
// (Layer 1 under Primary Bot "hanzo", Layer 2+ for sub-groups and worker pools)
// governed by a leader, Lux Quasar consensus, or custom voting policy.
type Group struct {
	ID            string
	Org           string
	Name          string
	Description   string
	Layer         int
	ParentGroupID string
	Members       []string
	Policy        groupPolicy
	CreatedBy     string
	CreatedAt     int64
	UpdatedAt     int64
}

// groupWeight assigns a relative voting weight to one member bot under a
// weighted or consensus group policy.
type groupWeight struct {
	// Agent is the member bot's org-unique name or public agent_... id.
	Agent string `json:"agent"`
	// Weight is the non-negative voting multiplier assigned to this member (1.0 is
	// standard parity).
	Weight float64 `json:"weight"`
}

// groupPolicy configures how a Bot Group coordinates its members and reaches
// finality across proposals.
type groupPolicy struct {
	// Kind selects the coordination mechanism: leader, quasar, majority, weighted,
	// unanimous, kai, or custom.
	Kind string `json:"kind"`
	// Leader names the bot that decomposes tasks and synthesizes member outputs
	// when kind is leader (or adjudicates when kind is custom).
	Leader string `json:"leader,omitempty"`
	// QuasarProfile selects a Lux Quasar preset (polaris, aurora, or pulsar) when
	// kind is quasar. Explicit sampleK, alpha, and beta values override the preset.
	QuasarProfile string `json:"quasarProfile,omitempty"`
	// SampleK caps how many group members are sampled per consensus round (for
	// example 3 for polaris, 5 for aurora, 7 for pulsar). Zero samples all members.
	SampleK int `json:"sampleK,omitempty"`
	// Alpha is the supermajority quorum fraction in (0, 1] required for a round to
	// lock on a candidate proposal (defaults to 0.67, i.e. 2/3, for quasar).
	Alpha float64 `json:"alpha,omitempty"`
	// Beta is the number of consecutive quorum rounds required to finalize a Lux
	// Quasar certificate (defaults to 2).
	Beta int `json:"beta,omitempty"`
	// ConfidenceFloor is the minimum self-reported confidence in [0, 1] required by
	// the kai policy before accepting a consensus answer without escalation.
	ConfidenceFloor float64 `json:"confidenceFloor,omitempty"`
	// Weights optionally overrides per-member voting weights for weighted and
	// quasar policies; unlisted members default to weight 1.0.
	Weights []groupWeight `json:"weights,omitempty"`
	// MaxBudgetMicroUSD caps the combined spend of a single group run in integer
	// micro-USD when positive.
	MaxBudgetMicroUSD int64 `json:"maxBudgetMicroUSD,omitempty"`
	// CustomRule holds the natural-language adjudication rubric applied when kind
	// is custom.
	CustomRule string `json:"customRule,omitempty"`
	// EscalateToPrimary forwards deadlocked or low-confidence ballots to the org's
	// Primary Bot ("hanzo") for tie-breaking finality.
	EscalateToPrimary bool `json:"escalateToPrimary,omitempty"`
}

// groupView is the wire representation of one Bot Group.
type groupView struct {
	// ID is the group's stable server-minted handle ("grp_" + hex).
	ID string `json:"id"`
	// Name is the group's org-unique handle, addressable as group_<name> in tools
	// and transport routing.
	Name string `json:"name"`
	// Description summarizes what the group owns so Primary Bot "hanzo" and parent
	// layers know when to delegate to it.
	Description string `json:"description,omitempty"`
	// Layer is the hierarchy tier (1 = direct report to Primary Bot "hanzo", 2+ =
	// nested sub-groups and specialist pools).
	Layer int `json:"layer"`
	// ParentGroupID names the parent Bot Group ("grp_...") when this group sits at
	// Layer 2 or deeper; empty means it reports directly to Primary Bot "hanzo".
	ParentGroupID string `json:"parentGroupId,omitempty"`
	// Members lists the bot names belonging to this group in deterministic order.
	Members []string `json:"members"`
	// Policy defines the leader, Lux Quasar, or voting consensus rules for this
	// group.
	Policy groupPolicy `json:"policy"`
	// CreatedAt is when the group was created, RFC 3339 UTC.
	CreatedAt string `json:"createdAt"`
	// UpdatedAt is when the group definition was last updated, RFC 3339 UTC.
	UpdatedAt string `json:"updatedAt"`
}

// groupList wraps every Bot Group defined in the caller's org.
type groupList struct {
	// Groups is the org's Bot Groups ordered by layer ascending, then name.
	Groups []groupView `json:"groups"`
}

// createGroupIn defines a new Bot Group in the caller's org.
type createGroupIn struct {
	// Name is the group's org-unique handle matching ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$.
	Name string `json:"name"`
	// Description explains what work this group handles for parent coordinators.
	Description string `json:"description,omitempty"`
	// Layer places the group in the coordination hierarchy (defaults to 1, or
	// parent.Layer + 1 when parentGroupId is set).
	Layer int `json:"layer,omitempty"`
	// ParentGroupID optionally nests this group beneath an existing parent group
	// (by id or name).
	ParentGroupID string `json:"parentGroupId,omitempty"`
	// Members lists the org's bot names or ids to assign to this group.
	Members []string `json:"members,omitempty"`
	// Policy configures how the group coordinates (defaults to quasar aurora when
	// omitted).
	Policy *groupPolicy `json:"policy,omitempty"`
}

// groupRef addresses a single Bot Group by id or name from the URL path.
type groupRef struct {
	// Ref is the group's public grp_... id or its org-unique name.
	Ref string `json:"ref"`
}

// updateGroupIn applies a partial update to an existing Bot Group.
type updateGroupIn struct {
	// Ref is the group's public grp_... id or org-unique name from the path.
	Ref string `json:"ref"`
	// Description replaces the group's summary when present.
	Description *string `json:"description,omitempty"`
	// Layer moves the group to another coordination tier (1..8) when present.
	Layer *int `json:"layer,omitempty"`
	// ParentGroupID re-parents the group (or clears the parent with "") when
	// present.
	ParentGroupID *string `json:"parentGroupId,omitempty"`
	// Members replaces the group's member bot list when present.
	Members *[]string `json:"members,omitempty"`
	// Policy replaces the group's coordination policy when present.
	Policy *groupPolicy `json:"policy,omitempty"`
}

// runGroupIn executes a coordinated task across a Bot Group.
type runGroupIn struct {
	// Ref is the group's public grp_... id or org-unique name from the path.
	Ref string `json:"ref"`
	// Input is the task or prompt submitted to the Bot Group, up to 128 KiB.
	Input string `json:"input"`
}

// groupVoteView records one member bot's proposal and ballot weight during a
// group execution.
type groupVoteView struct {
	// Agent is the member bot's org-unique name.
	Agent string `json:"agent"`
	// RunID is the recorded run_... handle of this member's execution.
	RunID string `json:"runId,omitempty"`
	// Model is the Zen model that served this member's turn.
	Model string `json:"model,omitempty"`
	// Status is "ok" when the member answered or "error" when its turn failed.
	Status string `json:"status"`
	// Proposal is the candidate answer produced by this member bot.
	Proposal string `json:"proposal,omitempty"`
	// Choice is the zero-based candidate index this member attested to in the final
	// ballot round.
	Choice int `json:"choice,omitempty"`
	// Confidence is the member's self-reported certainty in [0, 1].
	Confidence float64 `json:"confidence,omitempty"`
	// Weight is the effective voting weight applied to this member's ballot.
	Weight float64 `json:"weight,omitempty"`
}

// quasarCertView is the Lux Quasar consensus finality certificate produced when
// a group runs under the quasar policy.
type quasarCertView struct {
	// Profile names the active Lux Quasar profile (polaris, aurora, or pulsar).
	Profile string `json:"profile"`
	// SampleK is the validator sample size used per round.
	SampleK int `json:"sampleK"`
	// Alpha is the quorum threshold fraction required per round.
	Alpha float64 `json:"alpha"`
	// Beta is the number of consecutive quorum rounds required for finality.
	Beta int `json:"beta"`
	// Rounds is the total number of ballot rounds executed.
	Rounds int `json:"rounds"`
	// Support is the winning proposal's weighted vote share in [0, 1] on the final
	// round.
	Support float64 `json:"support"`
	// Finalized is true when the winning proposal held >= alpha support for beta
	// consecutive rounds.
	Finalized bool `json:"finalized"`
	// Escalated is true when Primary Bot "hanzo" broke a tie after unfinalized
	// rounds.
	Escalated bool `json:"escalated,omitempty"`
	// ThresholdDigest is the 48-byte hex SHA3-384 cSHAKE256 commitment under domain
	// LUX-QUASAR-GROUPED-THRESHOLD-V1 over the round ballot tally.
	ThresholdDigest string `json:"thresholdDigest"`
	// CheckpointDigest is the 48-byte hex SHA3-384 cSHAKE256 commitment under
	// domain LUX-QUASAR-CHECKPOINT-V1 binding the threshold digest to the winning
	// output.
	CheckpointDigest string `json:"checkpointDigest"`
}

// runGroupOut is the result of running a task through a Bot Group.
type runGroupOut struct {
	// ID is the unique grp_run_... identifier for this coordinated execution.
	ID string `json:"id"`
	// GroupID is the grp_... handle of the Bot Group that executed the task.
	GroupID string `json:"groupId"`
	// Group is the org-unique name of the Bot Group.
	Group string `json:"group"`
	// Layer is the hierarchy tier at which the group executed.
	Layer int `json:"layer"`
	// Policy is the coordination policy kind used for this execution.
	Policy string `json:"policy"`
	// Status is "ok" when the group produced an answer or "error" when every member
	// failed.
	Status string `json:"status"`
	// Output is the synthesized, consensus-finalized, or leader-approved response.
	Output string `json:"output"`
	// Error describes what failed when status is "error".
	Error string `json:"error,omitempty"`
	// DurationMs is the wall-clock duration of the coordinated execution in
	// milliseconds.
	DurationMs int64 `json:"durationMs"`
	// Votes lists each participating member bot's run, proposal, and ballot choice.
	Votes []groupVoteView `json:"votes"`
	// Quasar carries the Lux Quasar finality certificate when policy is quasar.
	Quasar *quasarCertView `json:"quasar,omitempty"`
}

// bootstrapPrimaryIn configures idempotent Primary Bot provisioning for the org.
type bootstrapPrimaryIn struct {
	// Name optionally overrides the Primary Bot's handle when creating or promoting
	// one; defaults to "hanzo".
	Name string `json:"name,omitempty"`
	// Model optionally sets the Zen model when creating the Primary Bot; defaults
	// to the deployment's chat model.
	Model string `json:"model,omitempty"`
}

// bootstrapPrimaryOut returns the org's single Primary Bot after idempotent
// bootstrap.
type bootstrapPrimaryOut struct {
	// Agent is the org's active Primary Bot (at Layer 0, isPrimary = true).
	Agent agentView `json:"agent"`
	// Created is true when a new Primary Bot row was inserted by this call, and
	// false when an existing bot was returned or promoted.
	Created bool `json:"created"`
}

const groupCols = `id,org,name,description,layer,parent_group_id,members,policy,created_by,created_at,updated_at`

func (s *Store) migrateGroups(ctx context.Context, tx *sql.Tx) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS agent_groups (
  id              TEXT PRIMARY KEY,
  org             TEXT NOT NULL,
  name            TEXT NOT NULL,
  description     TEXT NOT NULL DEFAULT '',
  layer           BIGINT NOT NULL DEFAULT 1,
  parent_group_id TEXT NOT NULL DEFAULT '',
  members         TEXT NOT NULL DEFAULT '[]',
  policy          TEXT NOT NULL DEFAULT '{}',
  created_by      TEXT NOT NULL DEFAULT '',
  created_at      BIGINT NOT NULL,
  updated_at      BIGINT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_agent_groups_org_name ON agent_groups(org, name);
CREATE INDEX IF NOT EXISTS ix_agent_groups_org_layer ON agent_groups(org, layer, name);
`
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("migrate: agent_groups: %w", err)
	}
	return nil
}

func normalizePolicy(p *groupPolicy) (groupPolicy, error) {
	var out groupPolicy
	if p != nil {
		out = *p
	}
	out.Kind = strings.ToLower(strings.TrimSpace(out.Kind))
	if out.Kind == "" {
		out.Kind = PolicyQuasar
	}
	switch out.Kind {
	case PolicyLeader, PolicyQuasar, PolicyMajority, PolicyWeighted, PolicyUnanimous, PolicyKai, PolicyCustom:
	default:
		return groupPolicy{}, zip.ErrBadRequest("policy.kind must be leader, quasar, majority, weighted, unanimous, kai, or custom")
	}
	out.Leader = strings.TrimSpace(out.Leader)
	out.CustomRule = strings.TrimSpace(out.CustomRule)
	if len(out.CustomRule) > maxInstructions {
		return groupPolicy{}, zip.ErrBadRequest("policy.customRule too large")
	}
	if out.Kind == PolicyQuasar {
		out.QuasarProfile = strings.ToLower(strings.TrimSpace(out.QuasarProfile))
		if out.QuasarProfile == "" {
			out.QuasarProfile = QuasarAurora
		}
		defK, defAlpha, defBeta := 5, 0.67, 2
		switch out.QuasarProfile {
		case QuasarPolaris:
			defK, defAlpha, defBeta = 3, 0.67, 2
		case QuasarAurora:
			defK, defAlpha, defBeta = 5, 0.67, 2
		case QuasarPulsar:
			defK, defAlpha, defBeta = 7, 0.75, 3
		default:
			return groupPolicy{}, zip.ErrBadRequest("policy.quasarProfile must be polaris, aurora, or pulsar")
		}
		if out.SampleK <= 0 {
			out.SampleK = defK
		}
		if out.Alpha <= 0 || out.Alpha > 1 {
			out.Alpha = defAlpha
		}
		if out.Beta <= 0 {
			out.Beta = defBeta
		}
		if out.Beta > 6 {
			out.Beta = 6
		}
	} else {
		if out.Alpha < 0 || out.Alpha > 1 {
			return groupPolicy{}, zip.ErrBadRequest("policy.alpha must be between 0 and 1")
		}
		if out.Kind == PolicyMajority && out.Alpha == 0 {
			out.Alpha = 0.51
		}
		if out.Kind == PolicyWeighted && out.Alpha == 0 {
			out.Alpha = 0.5
		}
		if out.Kind == PolicyUnanimous {
			out.Alpha = 1.0
		}
		if out.Kind == PolicyKai && (out.ConfidenceFloor <= 0 || out.ConfidenceFloor > 1) {
			out.ConfidenceFloor = 0.75
		}
	}
	if len(out.Weights) > 0 {
		cleaned := make([]groupWeight, 0, len(out.Weights))
		seen := map[string]bool{}
		for _, w := range out.Weights {
			ag := strings.TrimSpace(w.Agent)
			if ag == "" || seen[ag] {
				continue
			}
			if w.Weight < 0 || math.IsNaN(w.Weight) || math.IsInf(w.Weight, 0) {
				return groupPolicy{}, zip.ErrBadRequest("policy.weights must be finite non-negative numbers")
			}
			seen[ag] = true
			cleaned = append(cleaned, groupWeight{Agent: ag, Weight: w.Weight})
		}
		out.Weights = cleaned
	}
	return out, nil
}

func scanGroup(sc interface{ Scan(...any) error }) (Group, error) {
	var g Group
	var membersRaw, policyRaw string
	err := sc.Scan(
		&g.ID, &g.Org, &g.Name, &g.Description, &g.Layer, &g.ParentGroupID,
		&membersRaw, &policyRaw, &g.CreatedBy, &g.CreatedAt, &g.UpdatedAt,
	)
	if err != nil {
		return Group{}, err
	}
	g.Members = decodeList(membersRaw)
	if policyRaw != "" {
		_ = json.Unmarshal([]byte(policyRaw), &g.Policy)
	}
	if g.Policy.Kind == "" {
		g.Policy.Kind = PolicyQuasar
	}
	return g, nil
}

func (s *Store) CreateGroup(ctx context.Context, g Group) error {
	if err := dialect.Text(g.ID, g.Org, g.Name, g.Description, g.ParentGroupID, g.CreatedBy); err != nil {
		return err
	}
	polBytes, err := json.Marshal(g.Policy)
	if err != nil {
		return fmt.Errorf("marshal policy: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO agent_groups (`+groupCols+`) VALUES (`+placeholders(groupCols)+`)`,
		g.ID, g.Org, g.Name, g.Description, g.Layer, g.ParentGroupID,
		encodeList(g.Members), string(polBytes), g.CreatedBy, g.CreatedAt, g.UpdatedAt)
	if err != nil {
		if dialect.Unique(err) {
			return errGroupConflict
		}
		return fmt.Errorf("insert group: %w", err)
	}
	return nil
}

func (s *Store) ResolveGroup(ctx context.Context, org, ref string) (Group, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+groupCols+` FROM agent_groups WHERE org=$1 AND (id=$2 OR name=$2)
		 ORDER BY (id=$2) DESC LIMIT 1`, org, strings.TrimSpace(ref))
	g, err := scanGroup(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Group{}, errGroupNotFound
	}
	if err != nil {
		return Group{}, fmt.Errorf("resolve group: %w", err)
	}
	return g, nil
}

func (s *Store) ListGroups(ctx context.Context, org string) ([]Group, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+groupCols+` FROM agent_groups WHERE org=$1 ORDER BY layer ASC, name ASC`, org)
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Group
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, fmt.Errorf("scan group: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) UpdateGroup(ctx context.Context, g Group) error {
	if err := dialect.Text(g.ID, g.Org, g.Name, g.Description, g.ParentGroupID); err != nil {
		return err
	}
	polBytes, err := json.Marshal(g.Policy)
	if err != nil {
		return fmt.Errorf("marshal policy: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE agent_groups SET description=$1, layer=$2, parent_group_id=$3, members=$4, policy=$5, updated_at=$6
		 WHERE org=$7 AND id=$8`,
		g.Description, g.Layer, g.ParentGroupID, encodeList(g.Members), string(polBytes), g.UpdatedAt, g.Org, g.ID)
	if err != nil {
		return fmt.Errorf("update group: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errGroupNotFound
	}
	return nil
}

func (s *Store) DeleteGroup(ctx context.Context, org, id string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin delete group: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`UPDATE agents SET group_id='' WHERE org=$1 AND group_id=$2`, org, id); err != nil {
		return false, fmt.Errorf("clear member group_id: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE agent_groups SET parent_group_id='' WHERE org=$1 AND parent_group_id=$2`, org, id); err != nil {
		return false, fmt.Errorf("clear child parent_group_id: %w", err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM agent_groups WHERE org=$1 AND id=$2`, org, id)
	if err != nil {
		return false, fmt.Errorf("delete group: %w", err)
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit delete group: %w", err)
	}
	return n > 0, nil
}

func toGroupView(g Group) groupView {
	return groupView{
		ID:            g.ID,
		Name:          g.Name,
		Description:   g.Description,
		Layer:         g.Layer,
		ParentGroupID: g.ParentGroupID,
		Members:       nonNil(g.Members),
		Policy:        g.Policy,
		CreatedAt:     text.Unix(g.CreatedAt),
		UpdatedAt:     text.Unix(g.UpdatedAt),
	}
}

// resolveMemberNames canonicalizes a caller's member list against the org's
// agents, returning canonical agent names without duplicates.
func resolveMemberNames(ctx context.Context, sto *Store, org string, raw []string) ([]string, error) {
	out := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, r := range raw {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		a, err := sto.Resolve(ctx, org, r)
		if errors.Is(err, errNotFound) {
			return nil, zip.ErrBadRequest("member agent not found in this org: " + r)
		}
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "resolve member: %v", err)
		}
		if !seen[a.Name] {
			seen[a.Name] = true
			out = append(out, a.Name)
		}
	}
	return out, nil
}

// syncGroupMembership stamps group_id and layer onto member agents so Agent
// reads and Group reads stay consistent.
func syncGroupMembership(ctx context.Context, sto *Store, org, groupID string, layer int, members []string) {
	now := time.Now().Unix()
	_, _ = sto.db.ExecContext(ctx,
		`UPDATE agents SET group_id='', updated_at=$1 WHERE org=$2 AND group_id=$3`, now, org, groupID)
	for _, name := range members {
		_, _ = sto.db.ExecContext(ctx,
			`UPDATE agents SET group_id=$1, layer=CASE WHEN is_primary<>0 THEN 0 ELSE $2 END, updated_at=$3
			 WHERE org=$4 AND name=$5`,
			groupID, layer, now, org, name)
	}
}

// ListGroupsForOrg lists every Bot Group in org for in-process callers such as
// agentToolProvider.
func ListGroupsForOrg(ctx context.Context, org string) ([]Group, error) {
	sto, org, err := mountedStore(org)
	if err != nil {
		return nil, err
	}
	return sto.ListGroups(ctx, org)
}

// RunGroupOnBehalf executes a Bot Group in-process on behalf of (org, userSub),
// returning a Run-compatible record so Slack channels and agent tools can route
// directly to group_<name>.
func RunGroupOnBehalf(ctx context.Context, org, userSub, ref, input string) (Run, error) {
	if mounted == nil {
		return Run{}, fmt.Errorf("%w: agents", cloud.ErrNoPeer)
	}
	sto, org, err := mountedStore(org)
	if err != nil {
		return Run{}, err
	}
	g, err := sto.ResolveGroup(ctx, org, ref)
	if errors.Is(err, errGroupNotFound) {
		return Run{}, errNotFound
	}
	if err != nil {
		return Run{}, err
	}
	actor := billingActor(org, userSub)
	payer := meter.PayerFor(org, "")
	out, err := runGroupForOrg(mounted, ctx, sto, org, g, input, payer, actor, mint.ID("grpobh"), "")
	if err != nil {
		return Run{}, err
	}
	return Run{
		ID:         out.ID,
		Org:        org,
		AgentName:  "group:" + g.Name,
		Status:     out.Status,
		Model:      out.Policy,
		Input:      input,
		Output:     out.Output,
		Error:      out.Error,
		DurationMs: out.DurationMs,
		CreatedAt:  time.Now().Unix(),
		Actor:      actor,
		ToolCalls:  len(out.Votes),
	}, nil
}

// BootstrapPrimaryAgent ensures the caller's org has its single Primary Bot
// (default name "hanzo", Layer 0, full cloud & computer-use tools) and returns
// it. Calling bootstrap repeatedly is idempotent: once an org has a Primary Bot,
// that bot is returned unchanged without creating a second primary.
//
// Example: {"name": "hanzo"}
func (o agentOps) bootstrap(ctx context.Context, in *bootstrapPrimaryIn) (*bootstrapPrimaryOut, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	existing, err := sto.Primary(ctx, org)
	if err == nil {
		n, _ := sto.CountRuns(ctx, org, existing.Name, runScopeOf(ctx, org))
		return &bootstrapPrimaryOut{Agent: toView(existing, n), Created: false}, nil
	}
	if !errors.Is(err, errNotFound) {
		return nil, zip.Errorf(http.StatusInternalServerError, "primary: %v", err)
	}

	name := builtinAgentName
	if in != nil && strings.TrimSpace(in.Name) != "" {
		name = strings.TrimSpace(in.Name)
	}
	if !nameRE.MatchString(name) {
		return nil, zip.ErrBadRequest("name must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}

	// If an agent with that handle already exists in the org, promote it to
	// Primary Bot (Layer 0) rather than failing with 409.
	if found, ferr := sto.Resolve(ctx, org, name); ferr == nil {
		now := time.Now().Unix()
		if err := sto.SetPrimary(ctx, org, found.Name, now); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "promote primary: %v", err)
		}
		found.IsPrimary = true
		found.Layer = 0
		found.UpdatedAt = now
		if len(found.Tools) == 0 {
			found.Tools = []string{ToolsAll}
			_ = sto.Update(ctx, found)
		}
		n, _ := sto.CountRuns(ctx, org, found.Name, runScopeOf(ctx, org))
		return &bootstrapPrimaryOut{Agent: toView(found, n), Created: false}, nil
	}

	model := aimodel.DefaultModel
	if in != nil && strings.TrimSpace(in.Model) != "" {
		m := strings.TrimSpace(in.Model)
		if err := validateModel(s, ctx, m); err != nil {
			return nil, err
		}
		model = m
	}
	model = aimodel.ZenModel(model)

	now := time.Now().Unix()
	a := Agent{
		ID:              mint.ID("agent"),
		Org:             org,
		Name:            name,
		Model:           model,
		Instructions:    builtinAgentInstructions,
		Description:     "Primary director bot coordinating all bots, layered groups, Slack channels, and desktop sandboxes.",
		Tools:           []string{ToolsAll},
		Status:          "ready",
		ExecutionMode:   ModeOneShot,
		CreatedAt:       now,
		UpdatedAt:       now,
		Payer:           payerOf(ctx, org).Subject(),
		CapMicroUSD:     defaultPrimaryCapMicroUSD,
		MaxTaskMicroUSD: defaultPrimaryTaskMicroUSD,
		Period:          defaultPrimaryPeriod,
		PeriodStartedAt: periodStart(time.Now(), defaultPrimaryPeriod),
		CreatedBy:       creatorOf(ctx),
		IsPrimary:       true,
		Layer:           0,
	}
	if err := sto.Create(ctx, a); err != nil {
		if errors.Is(err, errConflict) {
			if raced, rerr := sto.Resolve(ctx, org, name); rerr == nil {
				_ = sto.SetPrimary(ctx, org, raced.Name, now)
				raced.IsPrimary = true
				raced.Layer = 0
				return &bootstrapPrimaryOut{Agent: toView(raced, 0), Created: false}, nil
			}
		}
		return nil, written("bootstrap primary", err)
	}
	return &bootstrapPrimaryOut{Agent: toView(a, 0), Created: true}, nil
}

// ListAgentGroups returns every Bot Group defined in the caller's org, ordered
// from Layer 1 downward.
func (o agentOps) listGroups(ctx context.Context, _ *cloud.Unit) (*groupList, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	rows, err := sto.ListGroups(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list groups: %v", err)
	}
	out := make([]groupView, 0, len(rows))
	for _, g := range rows {
		out = append(out, toGroupView(g))
	}
	return &groupList{Groups: out}, nil
}

// CreateAgentGroup defines a multi-bot coordination group at a hierarchy layer
// under Primary Bot "hanzo" (or nested under a parent group) with a leader, Lux
// Quasar consensus, or voting policy.
//
// Example: {"name": "core-eng", "layer": 1, "policy": {"kind": "quasar", "quasarProfile": "aurora"}}
func (o agentOps) createGroup(ctx context.Context, in *createGroupIn) (*groupView, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, zip.ErrBadRequest("name is required")
	}
	if !nameRE.MatchString(name) {
		return nil, zip.ErrBadRequest("name must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	desc := strings.TrimSpace(in.Description)
	if len(desc) > maxInstructions {
		return nil, zip.ErrBadRequest("description too large")
	}

	parentID := ""
	layer := in.Layer
	if pRef := strings.TrimSpace(in.ParentGroupID); pRef != "" {
		parent, perr := sto.ResolveGroup(ctx, org, pRef)
		if errors.Is(perr, errGroupNotFound) {
			return nil, zip.ErrNotFound("parent group not found in this org")
		}
		if perr != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "parent group: %v", perr)
		}
		parentID = parent.ID
		if layer <= 0 {
			layer = parent.Layer + 1
		}
	}
	if layer <= 0 {
		layer = 1
	}
	if layer > 8 {
		return nil, zip.ErrBadRequest("layer must be between 1 and 8")
	}

	members, err := resolveMemberNames(ctx, sto, org, in.Members)
	if err != nil {
		return nil, err
	}
	pol, err := normalizePolicy(in.Policy)
	if err != nil {
		return nil, err
	}
	if pol.Leader != "" {
		lead, lerr := sto.Resolve(ctx, org, pol.Leader)
		if errors.Is(lerr, errNotFound) {
			return nil, zip.ErrBadRequest("policy.leader agent not found in this org: " + pol.Leader)
		}
		if lerr != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "resolve leader: %v", lerr)
		}
		pol.Leader = lead.Name
	}

	now := time.Now().Unix()
	g := Group{
		ID:            mint.ID("grp"),
		Org:           org,
		Name:          name,
		Description:   desc,
		Layer:         layer,
		ParentGroupID: parentID,
		Members:       members,
		Policy:        pol,
		CreatedBy:     creatorOf(ctx),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := sto.CreateGroup(ctx, g); err != nil {
		if errors.Is(err, errGroupConflict) {
			return nil, zip.ErrConflict("group already exists in this org")
		}
		return nil, written("create group", err)
	}
	syncGroupMembership(ctx, sto, org, g.ID, g.Layer, g.Members)
	v := toGroupView(g)
	return &v, nil
}

// GetAgentGroup returns one Bot Group by its public grp_... id or org-unique
// name.
func (o agentOps) getGroup(ctx context.Context, in *groupRef) (*groupView, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	g, err := sto.ResolveGroup(ctx, org, in.Ref)
	if errors.Is(err, errGroupNotFound) {
		return nil, zip.ErrNotFound("group not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get group: %v", err)
	}
	v := toGroupView(g)
	return &v, nil
}

// UpdateAgentGroup updates a Bot Group's members, hierarchy layer, parent group,
// or coordination policy in place.
func (o agentOps) updateGroup(ctx context.Context, in *updateGroupIn) (*groupView, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	g, err := sto.ResolveGroup(ctx, org, in.Ref)
	if errors.Is(err, errGroupNotFound) {
		return nil, zip.ErrNotFound("group not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get group: %v", err)
	}
	if in.Description != nil {
		desc := strings.TrimSpace(*in.Description)
		if len(desc) > maxInstructions {
			return nil, zip.ErrBadRequest("description too large")
		}
		g.Description = desc
	}
	if in.ParentGroupID != nil {
		pRef := strings.TrimSpace(*in.ParentGroupID)
		if pRef == "" {
			g.ParentGroupID = ""
		} else {
			parent, perr := sto.ResolveGroup(ctx, org, pRef)
			if errors.Is(perr, errGroupNotFound) {
				return nil, zip.ErrNotFound("parent group not found in this org")
			}
			if perr != nil {
				return nil, zip.Errorf(http.StatusInternalServerError, "parent group: %v", perr)
			}
			if parent.ID == g.ID {
				return nil, zip.ErrBadRequest("a group cannot be its own parent")
			}
			g.ParentGroupID = parent.ID
		}
	}
	if in.Layer != nil {
		if *in.Layer < 1 || *in.Layer > 8 {
			return nil, zip.ErrBadRequest("layer must be between 1 and 8")
		}
		g.Layer = *in.Layer
	}
	if in.Members != nil {
		members, merr := resolveMemberNames(ctx, sto, org, *in.Members)
		if merr != nil {
			return nil, merr
		}
		g.Members = members
	}
	if in.Policy != nil {
		pol, perr := normalizePolicy(in.Policy)
		if perr != nil {
			return nil, perr
		}
		if pol.Leader != "" {
			lead, lerr := sto.Resolve(ctx, org, pol.Leader)
			if errors.Is(lerr, errNotFound) {
				return nil, zip.ErrBadRequest("policy.leader agent not found in this org: " + pol.Leader)
			}
			if lerr != nil {
				return nil, zip.Errorf(http.StatusInternalServerError, "resolve leader: %v", lerr)
			}
			pol.Leader = lead.Name
		}
		g.Policy = pol
	}
	g.UpdatedAt = time.Now().Unix()
	if err := sto.UpdateGroup(ctx, g); err != nil {
		if errors.Is(err, errGroupNotFound) {
			return nil, zip.ErrNotFound("group not found")
		}
		return nil, written("update group", err)
	}
	syncGroupMembership(ctx, sto, org, g.ID, g.Layer, g.Members)
	v := toGroupView(g)
	return &v, nil
}

// DeleteAgentGroup deletes a Bot Group and unassigns its member bots without
// deleting the bots themselves. Answers 204.
func (o agentOps) deleteGroup(ctx context.Context, in *groupRef) (*cloud.Unit, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	g, err := sto.ResolveGroup(ctx, org, in.Ref)
	if errors.Is(err, errGroupNotFound) {
		return nil, zip.ErrNotFound("group not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "resolve group: %v", err)
	}
	ok, err := sto.DeleteGroup(ctx, org, g.ID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete group: %v", err)
	}
	if !ok {
		return nil, zip.ErrNotFound("group not found")
	}
	return nil, nil
}

// RunAgentGroup executes a prompt across a Bot Group using its configured
// coordination policy (leader delegation, Lux Quasar consensus with SHA3-384
// cSHAKE256 finality certificates, majority, weighted, unanimous, kai, or
// custom) and returns the finalized response along with every member's vote.
//
// Example: {"ref": "core-eng", "input": "Audit the auth service rollout plan"}
func (o agentOps) runGroup(ctx context.Context, in *runGroupIn) (*runGroupOut, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	if len(in.Input) > maxInput {
		return nil, zip.ErrBadRequest("input too large")
	}
	if strings.TrimSpace(in.Input) == "" {
		return nil, zip.ErrBadRequest("input is required")
	}
	if s.State.ai == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "inference is not configured on this deployment")
	}
	sub, keyed := principal.SubjectFrom(ctx)
	if !keyed {
		return nil, zip.ErrForbidden("a group run requires an authenticated caller with a stable subject")
	}
	g, err := sto.ResolveGroup(ctx, org, in.Ref)
	if errors.Is(err, errGroupNotFound) {
		return nil, zip.ErrNotFound("group not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get group: %v", err)
	}
	actor := billingActor(org, sub)
	payer := payerOf(ctx, org)
	out, err := runGroupForOrg(s, ctx, sto, org, g, in.Input, payer, actor, mint.ID("grprun"), "")
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ── Multi-layer Group Execution & Lux Quasar Consensus Engine ─────────────────

type groupDepthKey struct{}

func groupDepth(ctx context.Context) int {
	d, _ := ctx.Value(groupDepthKey{}).(int)
	return d
}

func withDeeperGroup(ctx context.Context) context.Context {
	return context.WithValue(ctx, groupDepthKey{}, groupDepth(ctx)+1)
}

func resolveGroupAgents(ctx context.Context, sto *Store, org string, g Group) ([]Agent, error) {
	seen := map[string]bool{}
	var out []Agent
	addByName := func(ref string) {
		ref = strings.TrimSpace(ref)
		if ref == "" || seen[ref] {
			return
		}
		if a, err := sto.Resolve(ctx, org, ref); err == nil && a.Status != retired && !seen[a.Name] {
			seen[a.Name] = true
			out = append(out, a)
		}
	}
	for _, m := range g.Members {
		addByName(m)
	}
	all, err := sto.List(ctx, org)
	if err == nil {
		for _, a := range all {
			if a.GroupID == g.ID && a.Status != retired && !seen[a.Name] {
				seen[a.Name] = true
				out = append(out, a)
			}
		}
	}
	if g.Policy.Leader != "" {
		addByName(g.Policy.Leader)
	}
	return out, nil
}

func weightMapOf(pol groupPolicy) map[string]float64 {
	m := make(map[string]float64, len(pol.Weights))
	for _, w := range pol.Weights {
		m[w.Agent] = w.Weight
	}
	return m
}

func memberWeight(weights map[string]float64, a Agent) float64 {
	if w, ok := weights[a.Name]; ok {
		return w
	}
	if w, ok := weights[a.ID]; ok {
		return w
	}
	return 1.0
}

var reConfidenceJSON = regexp.MustCompile(`(?i)"confidence"\s*:\s*([0-9]*\.?[0-9]+)`)
var reChoiceJSON = regexp.MustCompile(`(?i)"choice"\s*:\s*([0-9]+)`)

func parseBallotJSON(text string, numCandidates int) (int, float64, bool) {
	s := strings.TrimSpace(text)
	var parsed struct {
		Choice     int     `json:"choice"`
		Confidence float64 `json:"confidence"`
	}
	if start, end := strings.IndexByte(s, '{'), strings.LastIndexByte(s, '}'); start >= 0 && end > start {
		if json.Unmarshal([]byte(s[start:end+1]), &parsed) == nil {
			if parsed.Choice >= 0 && parsed.Choice < numCandidates {
				conf := parsed.Confidence
				if conf <= 0 || conf > 1 {
					conf = 0.8
				}
				return parsed.Choice, conf, true
			}
		}
	}
	if m := reChoiceJSON.FindStringSubmatch(s); len(m) == 2 {
		if c, err := strconv.Atoi(m[1]); err == nil && c >= 0 && c < numCandidates {
			conf := 0.8
			if cm := reConfidenceJSON.FindStringSubmatch(s); len(cm) == 2 {
				if v, verr := strconv.ParseFloat(cm[1], 64); verr == nil && v > 0 && v <= 1 {
					conf = v
				}
			}
			return c, conf, true
		}
	}
	return 0, 0.5, false
}

func parseSelfConfidence(text string) (string, float64) {
	conf := 0.82
	if m := reConfidenceJSON.FindStringSubmatch(text); len(m) == 2 {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil && v >= 0 && v <= 1 {
			conf = v
		}
	}
	return strings.TrimSpace(text), conf
}

func normalizeAnswerKey(s string) string {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(s)))
	return strings.Join(fields, " ")
}

// quasarDigest384 computes a 48-byte (384-bit) hex cSHAKE256 commitment using
// Lux Quasar's domain-separation customization string.
func quasarDigest384(domain string, payload []byte) string {
	h := sha3.NewCShake256(nil, []byte(domain))
	_, _ = h.Write(payload)
	sum := make([]byte, 48)
	_, _ = h.Read(sum)
	return hex.EncodeToString(sum)
}

func runGroupForOrg(
	s *cloud.Service[state],
	ctx context.Context,
	sto *Store,
	org string,
	g Group,
	input string,
	payer account.Account,
	actor, requestID, clientIP string,
) (runGroupOut, error) {
	if groupDepth(ctx) >= maxGroupDepth {
		return runGroupOut{}, zip.ErrBadRequest("maximum group nesting depth reached")
	}
	ctx = withDeeperGroup(ctx)
	start := time.Now()
	runID := mint.ID("grprun")

	members, err := resolveGroupAgents(ctx, sto, org, g)
	if err != nil {
		return runGroupOut{}, err
	}
	if len(members) == 0 {
		return runGroupOut{}, zip.ErrBadRequest("group has no active member bots")
	}

	pol, err := normalizePolicy(&g.Policy)
	if err != nil {
		return runGroupOut{}, err
	}

	// Leader policy: designated leader directs group members & child sub-groups.
	if pol.Kind == PolicyLeader {
		leader := members[0]
		if pol.Leader != "" {
			for _, m := range members {
				if m.Name == pol.Leader || m.ID == pol.Leader {
					leader = m
					break
				}
			}
		}
		var peers []string
		for _, m := range members {
			if m.Name != leader.Name {
				peers = append(peers, fmt.Sprintf("- %s (%s)", m.Name, nonEmpty(m.Description, "specialist bot")))
			}
		}
		allGroups, _ := sto.ListGroups(ctx, org)
		var childGroups []string
		for _, cg := range allGroups {
			if cg.ParentGroupID == g.ID {
				childGroups = append(childGroups, fmt.Sprintf("- group %s (Layer %d, policy=%s): %s", cg.Name, cg.Layer, cg.Policy.Kind, cg.Description))
			}
		}
		var brief strings.Builder
		fmt.Fprintf(&brief, "[Bot Group %q — Layer %d Leader Coordination]\nYou lead group %q.", g.Name, g.Layer, g.Name)
		if len(peers) > 0 {
			fmt.Fprintf(&brief, "\nGroup member bots you may delegate sub-tasks to via the `coordinate` tool:\n%s", strings.Join(peers, "\n"))
		}
		if len(childGroups) > 0 {
			fmt.Fprintf(&brief, "\nSubordinate child groups you may invoke via `coordinate` (action=\"run_group\"):\n%s", strings.Join(childGroups, "\n"))
		}
		fmt.Fprintf(&brief, "\n\nTask:\n%s", input)

		r, gateErr := runAgent(s, ctx, leader, brief.String(), nil, payer, actor, requestID, clientIP)
		if gateErr != nil {
			return runGroupOut{}, gateErr
		}
		votes := []groupVoteView{{
			Agent:      leader.Name,
			RunID:      r.ID,
			Model:      aimodel.ZenModel(r.Model),
			Status:     r.Status,
			Proposal:   r.Output,
			Choice:     0,
			Confidence: 1.0,
			Weight:     1.0,
		}}
		return runGroupOut{
			ID:         runID,
			GroupID:    g.ID,
			Group:      g.Name,
			Layer:      g.Layer,
			Policy:     pol.Kind,
			Status:     r.Status,
			Output:     r.Output,
			Error:      r.Error,
			DurationMs: time.Since(start).Milliseconds(),
			Votes:      votes,
		}, nil
	}

	// Sample participating bots (all members unless Quasar SampleK < len(members)).
	sampled := members
	if pol.Kind == PolicyQuasar && pol.SampleK > 0 && pol.SampleK < len(members) {
		sampled = append([]Agent(nil), members[:pol.SampleK]...)
	}
	weights := weightMapOf(pol)

	type memberProposal struct {
		agent Agent
		run   Run
		text  string
		conf  float64
		w     float64
	}
	props := make([]memberProposal, len(sampled))
	var wg sync.WaitGroup
	var firstGateErr error
	var gateMu sync.Mutex

	for i, m := range sampled {
		wg.Add(1)
		go func(idx int, bot Agent) {
			defer wg.Done()
			prompt := input
			if pol.Kind == PolicyKai {
				prompt = input + "\n\n(Include your confidence in [0,1] as JSON `{\"confidence\": 0.9}` at the very end if uncertain.)"
			}
			r, gerr := runAgent(s, deeper(ctx), bot, prompt, nil, payer, actor, requestID, clientIP)
			if gerr != nil {
				gateMu.Lock()
				if firstGateErr == nil {
					firstGateErr = gerr
				}
				gateMu.Unlock()
				return
			}
			txt, conf := parseSelfConfidence(r.Output)
			props[idx] = memberProposal{
				agent: bot,
				run:   r,
				text:  txt,
				conf:  conf,
				w:     memberWeight(weights, bot),
			}
		}(i, m)
	}
	wg.Wait()
	if firstGateErr != nil {
		return runGroupOut{}, firstGateErr
	}

	var validIdx []int
	var spentMicros int64
	for i, p := range props {
		spentMicros += p.run.MicroUSD
		if p.run.Status == "ok" && strings.TrimSpace(p.text) != "" {
			validIdx = append(validIdx, i)
		}
	}
	if len(validIdx) == 0 {
		errMsg := "all member bots failed to produce a proposal"
		votes := make([]groupVoteView, 0, len(props))
		for _, p := range props {
			if p.agent.Name == "" {
				continue
			}
			if p.run.Error != "" {
				errMsg = p.run.Error
			}
			votes = append(votes, groupVoteView{
				Agent:  p.agent.Name,
				RunID:  p.run.ID,
				Model:  aimodel.ZenModel(p.run.Model),
				Status: "error",
				Weight: p.w,
			})
		}
		return runGroupOut{
			ID:         runID,
			GroupID:    g.ID,
			Group:      g.Name,
			Layer:      g.Layer,
			Policy:     pol.Kind,
			Status:     "error",
			Error:      errMsg,
			DurationMs: time.Since(start).Milliseconds(),
			Votes:      votes,
		}, nil
	}

	// Custom rubric policy: adjudicate proposals with CustomRule.
	if pol.Kind == PolicyCustom {
		judge := props[validIdx[0]].agent
		if pol.Leader != "" {
			if j, jerr := sto.Resolve(ctx, org, pol.Leader); jerr == nil {
				judge = j
			}
		} else if prim, perr := sto.Primary(ctx, org); perr == nil {
			judge = prim
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "Adjudicate the following proposals from Bot Group %q (Layer %d).\n", g.Name, g.Layer)
		if pol.CustomRule != "" {
			fmt.Fprintf(&sb, "Policy rule: %s\n", pol.CustomRule)
		}
		fmt.Fprintf(&sb, "\nOriginal task:\n%s\n\nMember proposals:\n", input)
		for rank, idx := range validIdx {
			fmt.Fprintf(&sb, "[%d] %s:\n%s\n\n", rank, props[idx].agent.Name, props[idx].text)
		}
		fmt.Fprintf(&sb, "Provide the final authoritative response directly.")
		jr, gerr := runAgent(s, deeper(ctx), judge, sb.String(), nil, payer, actor, requestID, clientIP)
		if gerr != nil {
			return runGroupOut{}, gerr
		}
		votes := make([]groupVoteView, 0, len(props))
		for i, p := range props {
			votes = append(votes, groupVoteView{
				Agent:      p.agent.Name,
				RunID:      p.run.ID,
				Model:      aimodel.ZenModel(p.run.Model),
				Status:     p.run.Status,
				Proposal:   p.text,
				Choice:     i,
				Confidence: p.conf,
				Weight:     p.w,
			})
		}
		outText := jr.Output
		status := jr.Status
		if status != "ok" {
			outText = props[validIdx[0]].text
			status = "ok"
		}
		return runGroupOut{
			ID:         runID,
			GroupID:    g.ID,
			Group:      g.Name,
			Layer:      g.Layer,
			Policy:     pol.Kind,
			Status:     status,
			Output:     outText,
			DurationMs: time.Since(start).Milliseconds(),
			Votes:      votes,
		}, nil
	}

	// Deduplicate identical normalized answers into candidate buckets so bots
	// that already gave the same answer form natural consensus immediately.
	type candidate struct {
		text    string
		key     string
		authors []int
	}
	var candidates []candidate
	propChoice := make([]int, len(props))
	keyToCand := map[string]int{}
	for _, idx := range validIdx {
		k := normalizeAnswerKey(props[idx].text)
		cIdx, exists := keyToCand[k]
		if !exists {
			cIdx = len(candidates)
			keyToCand[k] = cIdx
			candidates = append(candidates, candidate{text: props[idx].text, key: k})
		}
		candidates[cIdx].authors = append(candidates[cIdx].authors, idx)
		propChoice[idx] = cIdx
	}

	totalWeight := 0.0
	for _, idx := range validIdx {
		totalWeight += props[idx].w
	}
	if totalWeight <= 0 {
		totalWeight = float64(len(validIdx))
	}

	tallySupport := func(choices []int) (int, float64, []float64) {
		scores := make([]float64, len(candidates))
		for _, idx := range validIdx {
			c := choices[idx]
			if c >= 0 && c < len(candidates) {
				w := props[idx].w
				if w <= 0 {
					w = 1.0
				}
				scores[c] += w
			}
		}
		bestC := 0
		bestS := -1.0
		for c, sc := range scores {
			if sc > bestS {
				bestS = sc
				bestC = c
			}
		}
		return bestC, bestS / totalWeight, scores
	}

	alpha := pol.Alpha
	if alpha <= 0 {
		alpha = 0.67
	}
	beta := pol.Beta
	if beta <= 0 {
		beta = 1
	}
	if pol.Kind != PolicyQuasar {
		beta = 1
	}

	winner, support, scores := tallySupport(propChoice)
	consecutive := 0
	rounds := 1
	if support >= alpha {
		consecutive = 1
	}

	maxRounds := beta + 2
	if len(candidates) == 1 && support >= alpha {
		// Single unanimous candidate locks every Quasar finality round without
		// burning redundant LLM calls.
		consecutive = beta
		rounds = beta
	} else {
		budgetExceeded := pol.MaxBudgetMicroUSD > 0 && spentMicros >= pol.MaxBudgetMicroUSD
		for consecutive < beta && rounds < maxRounds && !budgetExceeded {
			rounds++
			var candList strings.Builder
			for ci, c := range candidates {
				prevPct := int(math.Round((scores[ci] / totalWeight) * 100))
				fmt.Fprintf(&candList, "Candidate %d (prior support %d%%):\n%s\n\n", ci, prevPct, c.text)
			}
			ballotPrompt := fmt.Sprintf(
				"Lux Quasar / Group Consensus Ballot (Group %q, Round %d, quorum alpha=%.2f).\nTask:\n%s\n\nCandidates:\n%sReply ONLY with compact JSON selecting the strongest accurate candidate index: {\"choice\": 0, \"confidence\": 0.92}",
				g.Name, rounds, alpha, input, candList.String(),
			)
			nextChoices := append([]int(nil), propChoice...)
			var bwg sync.WaitGroup
			for _, idx := range validIdx {
				bwg.Add(1)
				go func(pIdx int) {
					defer bwg.Done()
					bot := props[pIdx].agent
					// Ballot check is pure model judgement with no tools so a vote round
					// stays fast and deterministic.
					voter := bot
					voter.Tools = nil
					voter.Instructions = "You are casting a consensus ballot. Reply strictly with JSON: {\"choice\": <int>, \"confidence\": <0..1>}."
					r, gerr := runAgent(s, deeper(ctx), voter, ballotPrompt, nil, payer, actor, requestID, clientIP)
					if gerr != nil || r.Status != "ok" {
						return
					}
					if ch, conf, ok := parseBallotJSON(r.Output, len(candidates)); ok {
						nextChoices[pIdx] = ch
						props[pIdx].conf = conf
					}
				}(idx)
			}
			bwg.Wait()
			propChoice = nextChoices
			newWinner, newSupport, newScores := tallySupport(propChoice)
			scores = newScores
			if newSupport >= alpha && (consecutive == 0 || newWinner == winner) {
				winner = newWinner
				support = newSupport
				consecutive++
			} else if newSupport >= alpha {
				winner = newWinner
				support = newSupport
				consecutive = 1
			} else {
				winner = newWinner
				support = newSupport
				consecutive = 0
			}
		}
	}

	finalized := consecutive >= beta && support >= alpha
	if pol.Kind == PolicyKai {
		// Kai also enforces ConfidenceFloor across supporters of the winning choice.
		avgConf, cnt := 0.0, 0
		for _, idx := range validIdx {
			if propChoice[idx] == winner {
				avgConf += props[idx].conf
				cnt++
			}
		}
		if cnt > 0 {
			avgConf /= float64(cnt)
		}
		if avgConf < pol.ConfidenceFloor {
			finalized = false
		}
	}

	finalOutput := candidates[winner].text
	escalated := false
	if !finalized && pol.EscalateToPrimary {
		if prim, perr := sto.Primary(ctx, org); perr == nil {
			var eb strings.Builder
			fmt.Fprintf(&eb, "[Escalation from Bot Group %q (Layer %d, policy=%s, top support=%.0f%% < required %.0f%%)]\nTask:\n%s\n\nCandidate Proposals:\n",
				g.Name, g.Layer, pol.Kind, support*100, alpha*100, input)
			for ci, c := range candidates {
				fmt.Fprintf(&eb, "Candidate %d (support %.0f%%):\n%s\n\n", ci, (scores[ci]/totalWeight)*100, c.text)
			}
			fmt.Fprintf(&eb, "Resolve the deadlock and provide the final authoritative answer.")
			pr, gerr := runAgent(s, deeper(ctx), prim, eb.String(), nil, payer, actor, requestID, clientIP)
			if gerr == nil && pr.Status == "ok" && strings.TrimSpace(pr.Output) != "" {
				finalOutput = pr.Output
				escalated = true
			}
		}
	}

	votes := make([]groupVoteView, 0, len(props))
	for i, p := range props {
		votes = append(votes, groupVoteView{
			Agent:      p.agent.Name,
			RunID:      p.run.ID,
			Model:      aimodel.ZenModel(p.run.Model),
			Status:     p.run.Status,
			Proposal:   p.text,
			Choice:     propChoice[i],
			Confidence: p.conf,
			Weight:     p.w,
		})
	}

	out := runGroupOut{
		ID:         runID,
		GroupID:    g.ID,
		Group:      g.Name,
		Layer:      g.Layer,
		Policy:     pol.Kind,
		Status:     "ok",
		Output:     finalOutput,
		DurationMs: time.Since(start).Milliseconds(),
		Votes:      votes,
	}

	if pol.Kind == PolicyQuasar {
		threshPayload, _ := json.Marshal(struct {
			Org     string    `json:"org"`
			GroupID string    `json:"groupId"`
			Profile string    `json:"profile"`
			Rounds  int       `json:"rounds"`
			Winner  int       `json:"winner"`
			Support float64   `json:"support"`
			Scores  []float64 `json:"scores"`
		}{
			Org: org, GroupID: g.ID, Profile: pol.QuasarProfile,
			Rounds: rounds, Winner: winner, Support: support, Scores: scores,
		})
		threshDigest := quasarDigest384("LUX-QUASAR-GROUPED-THRESHOLD-V1", threshPayload)
		outHash := sha3.Sum384([]byte(finalOutput))
		ckptPayload, _ := json.Marshal(struct {
			ThresholdDigest string `json:"thresholdDigest"`
			OutputSHA384    string `json:"outputSha384"`
			Finalized       bool   `json:"finalized"`
			Escalated       bool   `json:"escalated"`
		}{
			ThresholdDigest: threshDigest,
			OutputSHA384:    hex.EncodeToString(outHash[:]),
			Finalized:       finalized,
			Escalated:       escalated,
		})
		ckptDigest := quasarDigest384("LUX-QUASAR-CHECKPOINT-V1", ckptPayload)
		out.Quasar = &quasarCertView{
			Profile:          pol.QuasarProfile,
			SampleK:          len(sampled),
			Alpha:            alpha,
			Beta:             beta,
			Rounds:           rounds,
			Support:          math.Round(support*10000) / 10000,
			Finalized:        finalized,
			Escalated:        escalated,
			ThresholdDigest:  threshDigest,
			CheckpointDigest: ckptDigest,
		}
	}

	return out, nil
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) != "" {
		return s
	}
	return fallback
}

// ── In-process Multi-Layer Coordination Tool (`coordinate`) ──────────────────

// CoordinateTool lets Primary Bot "hanzo" and Group Leaders inspect the org's
// multi-layer bot hierarchy and delegate work to any subordinate bot or Bot
// Group without extra MCP staging hops.
var CoordinateTool = infer.ToolDef{
	Name: "coordinate",
	Description: "Coordinate across your organization's bots and multi-layer Bot Groups: inspect the hierarchy " +
		"(`list_topology`), delegate a task to a specific bot (`delegate_bot`), or execute a task across a Bot Group " +
		"governed by Leader or Lux Quasar / voting consensus (`run_group`).",
	Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["list_topology", "delegate_bot", "run_group"],
				"description": "What coordination action to run."
			},
			"target": {
				"type": "string",
				"description": "Bot name (for delegate_bot) or Bot Group name/id (for run_group)."
			},
			"input": {
				"type": "string",
				"description": "Task or prompt to hand to the target bot or group."
			}
		},
		"required": ["action"],
		"additionalProperties": false
	}`),
}

type coordKey struct{}

type coordinator struct {
	s       *cloud.Service[state]
	sto     *Store
	org     string
	self    Agent
	payer   account.Account
	actor   string
	reqID   string
	ip      string
	summary string
}

func coordFrom(ctx context.Context) *coordinator {
	c, _ := ctx.Value(coordKey{}).(*coordinator)
	return c
}

// withCoordination attaches multi-layer coordination context and topology
// instructions when an org uses Primary Bot or Bot Groups, leaving standalone
// unit-test agents completely untouched.
func withCoordination(
	ctx context.Context,
	s *cloud.Service[state],
	sto *Store,
	org string,
	a *Agent,
	payer account.Account,
	actor, reqID, clientIP string,
) context.Context {
	if s == nil || sto == nil || a == nil || agentDepth(ctx) >= maxAgentDepth {
		return ctx
	}
	groups, err := sto.ListGroups(ctx, org)
	if err != nil {
		return ctx
	}
	if !a.IsPrimary && a.GroupID == "" && len(groups) == 0 {
		return ctx
	}
	bots, _ := sto.List(ctx, org)
	summary := buildTopologySummary(*a, bots, groups)
	hasTargets := len(groups) > 0
	if !hasTargets {
		for _, b := range bots {
			if b.Status != retired && b.Name != a.Name {
				hasTargets = true
				break
			}
		}
	}
	c := &coordinator{
		s:       s,
		sto:     sto,
		org:     org,
		self:    *a,
		payer:   payer,
		actor:   actor,
		reqID:   reqID,
		ip:      clientIP,
		summary: summary,
	}
	if a.IsPrimary && hasTargets && summary != "" {
		a.Instructions = strings.TrimSpace(a.Instructions) + "\n\n" + summary
	}
	return context.WithValue(ctx, coordKey{}, c)
}

func buildTopologySummary(self Agent, bots []Agent, groups []Group) string {
	var sb strings.Builder
	sb.WriteString("## Organization Agents & Coordination\n")
	if self.IsPrimary {
		fmt.Fprintf(&sb, "Coordination tool: `coordinate` (coordinator handle: `%s`). Use it to inspect or delegate tasks to available agents and agent groups in this organization.\n", self.Name)
	}
	if len(groups) > 0 {
		sb.WriteString("Groups:\n")
		sort.SliceStable(groups, func(i, j int) bool {
			if groups[i].Layer != groups[j].Layer {
				return groups[i].Layer < groups[j].Layer
			}
			return groups[i].Name < groups[j].Name
		})
		for _, g := range groups {
			polLabel := g.Policy.Kind
			if g.Policy.Kind == PolicyQuasar && g.Policy.QuasarProfile != "" {
				polLabel = fmt.Sprintf("quasar:%s (alpha=%.2f, beta=%d)", g.Policy.QuasarProfile, g.Policy.Alpha, g.Policy.Beta)
			} else if g.Policy.Kind == PolicyLeader && g.Policy.Leader != "" {
				polLabel = "leader:" + g.Policy.Leader
			}
			fmt.Fprintf(&sb, "- Group `%s` (tier %d, policy=%s, members=[%s])", g.Name, g.Layer, polLabel, strings.Join(g.Members, ", "))
			if g.Description != "" {
				fmt.Fprintf(&sb, ": %s", g.Description)
			}
			sb.WriteByte('\n')
		}
	}
	var workers []string
	for _, b := range bots {
		if b.Status == retired || b.Name == self.Name {
			continue
		}
		workers = append(workers, fmt.Sprintf("`%s` (tier %d)", b.Name, b.Layer))
	}
	if len(workers) > 0 {
		fmt.Fprintf(&sb, "Available agents: %s\n", strings.Join(workers, ", "))
	}
	return strings.TrimSpace(sb.String())
}

func (c *coordinator) dispatch(ctx context.Context, rawArgs string) string {
	var args struct {
		Action string `json:"action"`
		Target string `json:"target"`
		Input  string `json:"input"`
	}
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return "error: invalid JSON arguments for coordinate: " + err.Error()
	}
	switch strings.TrimSpace(args.Action) {
	case "list_topology":
		bots, _ := c.sto.List(ctx, c.org)
		groups, _ := c.sto.ListGroups(ctx, c.org)
		return buildTopologySummary(c.self, bots, groups)
	case "delegate_bot":
		target := strings.TrimSpace(args.Target)
		if target == "" {
			return "error: target bot name is required for delegate_bot"
		}
		if strings.EqualFold(target, c.self.Name) || target == c.self.ID {
			return "error: cannot delegate to yourself"
		}
		bot, err := c.sto.Resolve(ctx, c.org, target)
		if err != nil {
			return "error: bot not found: " + target
		}
		r, gerr := runAgent(c.s, ctx, bot, args.Input, nil, c.payer, c.actor, c.reqID, c.ip)
		if gerr != nil {
			return "error: " + gerr.Error()
		}
		b, _ := json.Marshal(struct {
			Agent  string `json:"agent"`
			RunID  string `json:"runId"`
			Status string `json:"status"`
			Output string `json:"output"`
			Error  string `json:"error,omitempty"`
		}{Agent: bot.Name, RunID: r.ID, Status: r.Status, Output: r.Output, Error: r.Error})
		return string(b)
	case "run_group":
		target := strings.TrimSpace(args.Target)
		target = strings.TrimPrefix(target, "group:")
		target = strings.TrimPrefix(target, "group_")
		if target == "" {
			return "error: target group name is required for run_group"
		}
		grp, err := c.sto.ResolveGroup(ctx, c.org, target)
		if err != nil {
			return "error: group not found: " + target
		}
		res, gerr := runGroupForOrg(c.s, ctx, c.sto, c.org, grp, args.Input, c.payer, c.actor, c.reqID, c.ip)
		if gerr != nil {
			return "error: " + gerr.Error()
		}
		b, _ := json.Marshal(res)
		return string(b)
	default:
		return "error: unknown coordinate action (use list_topology, delegate_bot, or run_group)"
	}
}
