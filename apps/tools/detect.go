package tools

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/zap-proto/zip"
)

const (
	defaultDetectThreshold = 0.55
	defaultMaxDetectSkills = 6
	maxDetectSkillsLimit   = 16
	defaultMaxDetectTools  = 8
	maxDetectToolsLimit    = 32
)

// detectModel names what ranks the candidates: rank.go's ranker, measured on
// testdata/detect (TestDetectRanking). A decision model takes its place only by
// beating it there.
const detectModel = "rank-1"

// detectReq asks which skills and tools should be enabled for a prompt.
type detectReq struct {
	// Prompt is the user message, task description, or conversation turn to
	// evaluate against the organization's available skills and tools.
	Prompt string `json:"prompt"`
	// AutoActivate switches on the recommended skills and tools that the caller
	// is permitted to activate for the current org and project scope.
	AutoActivate bool `json:"autoActivate,omitempty"`
	// Threshold is the minimum calibrated probability (0 to 1, default 0.55) a
	// skill or tool must score to be recommended.
	Threshold float64 `json:"threshold,omitempty"`
	// MaxSkills bounds how many skills are returned (default 6, max 16).
	MaxSkills int `json:"maxSkills,omitempty"`
	// MaxTools bounds how many callable tools are returned (default 8, max 32).
	MaxTools int `json:"maxTools,omitempty"`
}

// detectRecommendation is one skill or tool recommended for the prompt.
type detectRecommendation struct {
	// Name is the tool or skill identifier as reported by GET /v1/tool (for
	// example "skill_dev", "skill_ui", or "github_create_issue").
	Name string `json:"name"`
	// Source names the provider that owns the entry: skill, mcp, connector,
	// function, agent or zap-service.
	Source Source `json:"source"`
	// Description summarizes what the skill or tool does.
	Description string `json:"description"`
	// Probability is how likely the candidate fits the prompt, in [0, 1];
	// 0.8 or more means it clearly does.
	Probability float64 `json:"probability"`
	// Activated reports whether the candidate is switched on for the caller's
	// org and project after processing autoActivate.
	Activated bool `json:"activated"`
}

// detectResult is the tool and skill recommendation for a prompt.
type detectResult struct {
	// Model names what ranked the candidates ("rank-1").
	Model string `json:"model"`
	// DecisionID is the identifier of the decision (POST /v1/decisions) the
	// candidates were ranked by, when a decision model ranked them; absent
	// from rank-1.
	DecisionID string `json:"decisionId,omitempty"`
	// Skills lists the recommended skills (skill_<name>), ordered from highest
	// to lowest probability.
	Skills []detectRecommendation `json:"skills"`
	// Tools lists the recommended callable tools, ordered from highest to
	// lowest probability.
	Tools []detectRecommendation `json:"tools"`
	// Activated lists the tool and skill names that were newly switched on by
	// this call when autoActivate was set.
	Activated []string `json:"activated"`
}

// builtinSkillSummaries describes the built-in default skills, in the words a
// person asks for them with, when an org lists no description of its own.
var builtinSkillSummaries = map[string]string{
	"code-review": "Review code before it merges: pull requests, diffs, patches and changes, for correctness, bugs, regressions, race conditions, security (auth, injection, secrets, vulnerabilities), simplification, readability and test coverage.",
	"dev":         "Any coding task, done with the smallest change that solves it: write, fix, debug, refactor or simplify code; implement features, endpoints, functions and scripts; write tests; database migrations; failing CI builds; git merges, rebases and conflicts.",
	"ui":          "Design, build or fix user interfaces: web pages, landing pages, screens and components, layout, styling and CSS (React, Next.js, Tailwind), responsive layouts on mobile phones, tablets and desktop, accessibility, and visual parity with a reference design.",
}

// activateDefaultSkillsForPR automatically enables the Ponytail code-review
// ("skill_code-review") and senior dev ("skill_dev") skills when a pull
// request is opened, reopened, or synchronized for an organization/project.
func activateDefaultSkillsForPR(ctx context.Context, org, project string) []string {
	if mounted == nil || mounted.State.activation == nil || strings.TrimSpace(org) == "" {
		return nil
	}
	prSkills := []string{"skill_code-review", "skill_dev"}
	activated := make([]string, 0, len(prSkills))
	for _, name := range prSkills {
		// Activate at org scope ("") and optional project scope so both chat and
		// repo-scoped runs inherit Ponytail code-review immediately.
		if err := mounted.State.activation.Activate(ctx, org, "", name, SourceSkill, "github:pull_request", true); err == nil {
			activated = append(activated, name)
		}
		if p := strings.TrimSpace(project); p != "" {
			_ = mounted.State.activation.Activate(ctx, org, p, name, SourceSkill, "github:pull_request", true)
		}
	}
	return activated
}

// Recommends which skills and tools in the caller's org and project fit a
// prompt, and optionally activates them automatically.
//
// Every candidate is ranked by what its name and description say against the
// prompt, the rarer a shared word the more it counts, through one vocabulary
// for how people ask ("phones" and "responsive", "excel" and "xlsx"). Its
// probability needs enough of that evidence, and a candidate well behind the
// best of its kind (a skill, or an operation or tool) is held under 0.8, so a
// prompt no skill fits recommends none. When autoActivate is true, recommended
// entries that are not yet enabled — and that the caller has authority to
// toggle — are activated in the caller's (org, project) scope and recorded on
// the audit trail.
//
// Example: {"prompt": "Review this React pull request and fix the responsive layout bug", "autoActivate": true}
func (o toolOps) detectTools(ctx context.Context, in *detectReq) (*detectResult, error) {
	scope, err := scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	prompt := strings.TrimSpace(in.Prompt)
	if prompt == "" {
		return nil, zip.ErrBadRequest("prompt is required")
	}

	threshold := in.Threshold
	if threshold <= 0 || threshold > 1 {
		threshold = defaultDetectThreshold
	}
	maxSkills := in.MaxSkills
	switch {
	case maxSkills <= 0:
		maxSkills = defaultMaxDetectSkills
	case maxSkills > maxDetectSkillsLimit:
		maxSkills = maxDetectSkillsLimit
	}
	maxTools := in.MaxTools
	switch {
	case maxTools <= 0:
		maxTools = defaultMaxDetectTools
	case maxTools > maxDetectToolsLimit:
		maxTools = maxDetectToolsLimit
	}

	candidates := gatherDetectCandidates(ctx, o.s.State.activation, scope)
	probs := relevance(prompt, candidates)

	skills := make([]detectRecommendation, 0)
	tools := make([]detectRecommendation, 0)
	for _, c := range candidates {
		p := probs[c.Name]
		if p < threshold {
			continue
		}
		rec := detectRecommendation{
			Name:        c.Name,
			Source:      c.Source,
			Description: c.Description,
			Probability: p,
			Activated:   c.Activated,
		}
		if c.Source == SourceSkill || strings.HasPrefix(c.Name, "skill_") {
			skills = append(skills, rec)
		} else {
			tools = append(tools, rec)
		}
	}

	sortRecommendations(skills)
	sortRecommendations(tools)
	if len(skills) > maxSkills {
		skills = skills[:maxSkills]
	}
	if len(tools) > maxTools {
		tools = tools[:maxTools]
	}

	activated := make([]string, 0)
	if in.AutoActivate && o.s != nil && o.s.State.activation != nil {
		var toActivate []detectRecommendation
		for i := range skills {
			if !skills[i].Activated && mayToggle(ctx, o.s.State.servers, scope.Org, skills[i].Name) == nil {
				toActivate = append(toActivate, skills[i])
			}
		}
		for i := range tools {
			if !tools[i].Activated && mayToggle(ctx, o.s.State.servers, scope.Org, tools[i].Name) == nil {
				toActivate = append(toActivate, tools[i])
			}
		}
		if len(toActivate) > 0 {
			names := make([]string, 0, len(toActivate))
			for _, rec := range toActivate {
				names = append(names, rec.Name)
			}
			if err := record(o.s, ctx, "activation.detect", scope.Org, "activation", map[string]any{
				"project":  projectKey(scope.Project),
				"activate": names,
				"model":    detectModel,
			}); err == nil {
				actor, admitted := callerOf(ctx), administers(ctx)
				enabledSet := make(map[string]bool, len(toActivate))
				for _, rec := range toActivate {
					if err := o.s.State.activation.Activate(ctx, scope.Org, scope.Project, rec.Name, rec.Source, actor, admitted); err == nil {
						activated = append(activated, rec.Name)
						enabledSet[rec.Name] = true
					}
				}
				for i := range skills {
					if enabledSet[skills[i].Name] {
						skills[i].Activated = true
					}
				}
				for i := range tools {
					if enabledSet[tools[i].Name] {
						tools[i].Activated = true
					}
				}
			}
		}
	}

	return &detectResult{
		Model:     detectModel,
		Skills:    skills,
		Tools:     tools,
		Activated: activated,
	}, nil
}

func sortRecommendations(list []detectRecommendation) {
	slices.SortFunc(list, func(a, b detectRecommendation) int {
		if c := cmp.Compare(b.Probability, a.Probability); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})
}

func gatherDetectCandidates(ctx context.Context, act *ActivationStore, scope Scope) []Tool {
	listed := Default().List(ctx, scope)
	seen := make(map[string]bool, len(listed)+len(defaultSkillNames))
	out := make([]Tool, 0, len(listed)+len(defaultSkillNames))
	for _, t := range listed {
		seen[t.Name] = true
		if t.Description == "" {
			if sk, ok := strings.CutPrefix(t.Name, "skill_"); ok {
				t.Description = builtinSkillSummaries[sk]
			}
		}
		out = append(out, t)
	}
	for _, def := range defaultSkillNames {
		name := "skill_" + def
		if seen[name] {
			continue
		}
		isAct := act != nil && act.IsActivated(ctx, scope.Org, scope.Project, name)
		out = append(out, Tool{
			Name:        name,
			Source:      SourceSkill,
			Description: builtinSkillSummaries[def],
			Activated:   isAct,
		})
	}
	return out
}
