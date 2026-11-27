package auto

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hanzoai/cloud/apps/provider"
	"github.com/hanzoai/cloud/client"
	projectpeer "github.com/hanzoai/cloud/client/project"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// automation.go — the Automation: a task that runs itself on a schedule, or
// whenever someone asks.
//
// It is not a second store. An automation IS a flow: a trigger (core.schedule,
// or core.manual when it runs only on demand) and one step, core.agent, which
// hands the instructions to a Dev run (agent.go). This file reads that one shape
// out of a flow and writes it back, so the builder, the run history, the durable
// engine and the connector catalogue all see an automation as the flow it is,
// and /v1/auto/automations is the same record said in the words a person uses.
//
// A DRAFT is a flow that does nothing yet: a manual trigger or none, no step,
// no pinned version — named, and not yet told what to do. It is listed and read
// here as an automation with no instructions, runs for nobody, and becomes an
// automation in place, same id, when someone saves its instructions.
//
// THE PERSON IT RUNS AS is whoever last said what it does. A Dev run is held by a
// person — their settings, their name on its work, their ask_user prompts — so an
// automation records the subject that created it, and an edit to it records the
// editor instead. Otherwise one member could rewrite another's instructions and
// have them carried out in the other's name.

const (
	maxName = 200
	// maxPrompt is the Dev run's own prompt bound (apps/coding maxPromptLen), and
	// maxInstructions leaves room under it for the terms and the name the prompt
	// carries ahead of them (agentTask).
	maxPrompt       = 32 << 10
	maxInstructions = maxPrompt - 2<<10
	// maxAutomations is how many automations one org may keep.
	maxAutomations = 100
	// sourceDev is the product an automation is made in when it says none: the
	// Dev app's Scheduled tasks, where automations were first made.
	sourceDev = "dev"
)

// projectRE and modelRE are the shapes a project slug and a model id take: what
// the Dev runtime itself admits (apps/coding RepoRE, and the ids GET /v1/models
// lists).
var (
	projectRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	modelRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
)

// Automation is a task that runs itself.
type Automation struct {
	// Event runs the automation on a committed product outcome instead of a schedule.
	Event *OutcomeTrigger `json:"event,omitempty"`
	// Target is an optional registered execution target, such as the person's local machine.
	Target *string `json:"target,omitempty"`
	// ID is the automation's id, which is also its flow's.
	ID string `json:"id"`
	// Name is what it is called.
	Name string `json:"name"`
	// Instructions are what the agent is asked to do each run.
	Instructions string `json:"instructions"`
	// Project is the Dev project the run works in, by its slug; null for none.
	Project *string `json:"project"`
	// Model is the model the agent thinks with; null for the default.
	Model *string `json:"model"`
	// Schedule is when it runs.
	Schedule Schedule `json:"schedule"`
	// Permissions is auto (works and uses connectors without stopping) or ask
	// (changes nothing and ends with the actions it proposes; agent.go agentTask).
	Permissions string `json:"permissions"`
	// Notify sends a one-line summary to the automation's person when a run ends.
	Notify bool `json:"notify"`
	// PostTo are the connected accounts each run's answer is posted to — x,
	// linkedin, facebook — after review when Permissions is ask (post.go). Never null.
	PostTo []string `json:"postTo"`
	// Enabled is whether its schedule is armed.
	Enabled bool `json:"enabled"`
	// Draft is a flow with no step yet: it has no instructions, never runs, and
	// becomes an automation when its instructions are saved.
	Draft bool `json:"draft"`
	// Source is the product it was made in: chat, dev, studio or workflow.
	Source string `json:"source"`
	// Next is when it runs next, RFC 3339 UTC; null when manual or disabled.
	Next *string `json:"next"`
	// Last is its most recent run that was not skipped; null before the first.
	Last *LastRun `json:"last"`
	// Created and Updated are RFC 3339 UTC.
	Created string `json:"created"`
	Updated string `json:"updated"`
	// Dir is the folder on that machine the run works in, inside one its owner
	// granted. Null when the machine may choose its own folder.
	Dir *string `json:"dir,omitempty"` // last: keep the ZAP ordinals of older fields
}

// LastRun is an automation's most recent run, in brief.
type LastRun struct {
	ID string `json:"id"`
	// Status is succeeded, failed, running or queued.
	Status  string `json:"status"`
	At      string `json:"at"`
	Summary string `json:"summary"`
}

// AutomationRun is one run of an automation.
type AutomationRun struct {
	ID string `json:"id"`
	// Status is succeeded, failed, running, queued, review for a post waiting
	// for its person to post or discard it, skipped for a start that found the
	// previous run still going, or refused for a run whose person is no longer a
	// member of the org.
	Status string `json:"status"`
	// At is when it started and Finished when it ended (null while it runs), RFC
	// 3339 UTC.
	At       string  `json:"at"`
	Finished *string `json:"finished"`
	// Summary is one line on how it went.
	Summary string `json:"summary"`
	// Transcript opens the run's Dev run; null when it started none.
	Transcript *string `json:"transcript"`
	// Draft is the post the agent wrote, for an automation that posts; empty
	// otherwise.
	Draft string `json:"draft"`
	// Posts are where it went: one per account, its link or the platform's
	// reason it did not go. Never null.
	Posts []PostResult `json:"posts"`
	// Outputs are the Library items the run made: its answer, kept as a
	// document. Never null.
	Outputs []string `json:"outputs"`
}

// Starter is a ready-made automation to begin from.
type Starter struct {
	Key          string   `json:"key"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Instructions string   `json:"instructions"`
	Schedule     Schedule `json:"schedule"`
	Icon         string   `json:"icon"`
}

type automationPage struct {
	// Data is the org's automations.
	Data []Automation `json:"data"`
}

type automationRunPage struct {
	// Data is the automation's runs, newest first.
	Data []AutomationRun `json:"data"`
}

type starterPage struct {
	// Data is the starters.
	Data []Starter `json:"data"`
}

type runHandle struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type runStarted struct {
	// Run is the run this started, or the skipped start when one was going.
	Run runHandle `json:"run"`
}

type automationQuery struct {
	// Q keeps the automations whose name or instructions contain it, ignoring case.
	Q string `json:"q"`
	// Sort is name, next or updated (the default, newest first).
	Sort string `json:"sort"`
	// Source keeps the automations made in one product: chat, dev, studio or workflow.
	Source string `json:"source"`
}

type automationRef struct {
	// ID is the automation, from the path.
	ID string `json:"id"`
}

type automationRunsQuery struct {
	// ID is the automation, from the path.
	ID string `json:"id"`
	// Limit bounds the page (default 200, maximum 1000).
	Limit int `json:"limit"`
}

type automationIn struct {
	// Event arms a product-outcome trigger; it is mutually exclusive with a timed schedule.
	Event *OutcomeTrigger `json:"event"`
	// Target selects a registered execution target for the coding run.
	Target *string `json:"target"`
	// Name is what it is called. Required.
	Name string `json:"name"`
	// Instructions are what the agent is asked to do each run. Required.
	Instructions string `json:"instructions"`
	// Project is a Dev project's slug to work in. Optional.
	Project *string `json:"project"`
	// Model is the model id to think with. Optional; absent is the default.
	Model *string `json:"model"`
	// Schedule is when it runs. Absent runs it only on demand.
	Schedule *Schedule `json:"schedule"`
	// Permissions is auto (works without stopping) or ask (proposes, changes
	// nothing). Absent is ask.
	Permissions string `json:"permissions"`
	// Notify sends a one-line summary when a run ends.
	Notify bool `json:"notify"`
	// PostTo are the connected accounts each run's answer is posted to: x,
	// linkedin, facebook. Absent posts nothing.
	PostTo []string `json:"postTo"`
	// Enabled arms its schedule. Absent is true.
	Enabled *bool `json:"enabled"`
	// Source is the product it is made in: chat, dev, studio or workflow. Absent
	// is dev.
	Source string `json:"source"`
	// Dir is the folder on that machine the run works in: an absolute path inside
	// one of the folders its owner granted. Absent lets the machine choose. It
	// needs Target; the coding run checks the grant again and refuses 403
	// `folder_not_granted` for a folder outside every grant.
	Dir *string `json:"dir"` // last: keep the ZAP ordinals of older fields
}

type automationPatch struct {
	// Event replaces the trigger with a product outcome; null clears it.
	Event *OutcomeTrigger `json:"event"`
	// Target replaces the registered execution target; null clears it.
	Target *string `json:"target"`
	// ID is the automation, from the path.
	ID           string    `json:"id"`
	Name         *string   `json:"name"`
	Instructions *string   `json:"instructions"`
	Project      *string   `json:"project"`
	Model        *string   `json:"model"`
	Schedule     *Schedule `json:"schedule"`
	Permissions  *string   `json:"permissions"`
	Notify       *bool     `json:"notify"`
	PostTo       *[]string `json:"postTo"`
	// Enabled arms or disarms its schedule.
	Enabled *bool `json:"enabled"`
	// Dir replaces the granted folder on the target; null clears it.
	Dir *string `json:"dir"` // last: keep the ZAP ordinals of older fields
}

// definition is what an automation is, apart from its record: everything its
// flow's version holds.
type definition struct {
	Event                                           *OutcomeTrigger
	Target                                          string
	Dir                                             string // granted folder on Target; empty lets the machine choose
	Name, Instructions, Project, Model, Permissions string
	Schedule                                        Schedule
	Notify                                          bool
	// PostTo are the accounts its answer is posted to (post.go).
	PostTo []string
	// Source is the product it was made in; what its runs make is filed in the
	// Library under it.
	Source string
	// draft is a flow with no step yet (draftOf).
	draft bool
}

// check validates a definition at the boundary: 400 for a field that cannot be
// what it says, 422 for a schedule that cannot run.
func (d *definition) check() error {
	d.Name, d.Instructions = strings.TrimSpace(d.Name), strings.TrimSpace(d.Instructions)
	d.Project, d.Model = strings.TrimSpace(d.Project), strings.TrimSpace(d.Model)
	d.Permissions = strings.ToLower(strings.TrimSpace(d.Permissions))
	if d.Source = cmp.Or(strings.ToLower(strings.TrimSpace(d.Source)), sourceDev); !oneOf(jobProducts, d.Source) {
		return invalid("source must be chat, dev, studio or workflow")
	}
	switch {
	case d.Name == "":
		return invalid("name is required")
	case utf8.RuneCountInString(d.Name) > maxName:
		return invalid("name is longer than 200 characters")
	case strings.ContainsFunc(d.Name, unicode.IsControl):
		return invalid("name is one line of text")
	case d.Instructions == "":
		return invalid("instructions are required")
	case len(d.Instructions) > maxInstructions:
		return invalid("instructions are longer than 30 KiB")
	case d.Project != "" && !projectRE.MatchString(d.Project):
		return invalid("project is not a project slug")
	case d.Model != "" && !modelRE.MatchString(d.Model):
		return invalid("model is not a model id")
	}
	if d.Permissions == "" {
		d.Permissions = PermissionAsk
	}
	if d.Permissions != PermissionAuto && d.Permissions != PermissionAsk {
		return invalid("permissions must be auto or ask")
	}
	to, err := postTargets(d.PostTo)
	if err != nil {
		return invalid(err.Error())
	}
	d.PostTo = to
	s, err := normalize(d.Schedule)
	if err != nil {
		return &zip.HTTPError{Status: http.StatusUnprocessableEntity, Code: "invalid_schedule", Msg: err.Error()}
	}
	d.Schedule = s
	if d.Event != nil {
		if !outcomeName.MatchString(d.Event.Source) || !outcomeName.MatchString(d.Event.Name) {
			return invalid("event needs bounded source and name")
		}
		if compile(d.Schedule) != "" || d.Schedule.At != "" {
			return invalid("an automation uses an event or a schedule, not both")
		}
	}
	if len(d.Target) > 256 || strings.ContainsFunc(d.Target, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return invalid("target must be a registered target id")
	}
	if d.Dir != "" && d.Target == "" {
		return invalid("dir needs a target")
	}
	return nil
}

// OutcomeTrigger selects one exact event from the native product-outcome plane.
type OutcomeTrigger struct {
	// Source is the producing capability, for example research.
	Source string `json:"source"`
	// Name is the event, for example improvement.reviewed.
	Name string `json:"name"`
}

var outcomeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func invalid(msg string) error {
	return &zip.HTTPError{Status: http.StatusBadRequest, Code: "invalid_request", Msg: msg}
}

// trigger is the definition's step tree: the schedule, then the agent.
func (d definition) trigger() *FlowTrigger {
	sched := map[string]any{"kind": d.Schedule.Kind, "tz": d.Schedule.TZ}
	for k, v := range map[string]string{"at": d.Schedule.At, "day": d.Schedule.Day, "cron": d.Schedule.Cron} {
		if v != "" {
			sched[k] = v
		}
	}
	t := &FlowTrigger{
		Name: "trigger", Type: TriggerTypeProvider, DisplayName: "Manual", Valid: true,
		Strategy: provider.StrategyManual,
		Settings: StepSettings{ProviderName: coreProvider, TriggerName: "manual", Input: map[string]any{"schedule": sched}},
	}
	if expr := compile(d.Schedule); expr != "" {
		t.DisplayName, t.Strategy, t.Settings.TriggerName = "Schedule", provider.StrategyPolling, "schedule"
		t.Settings.Input["cron"] = expr
	}
	if d.Event != nil {
		t.DisplayName, t.Strategy = "Product outcome", provider.StrategyWebhook
		t.Settings.ProviderName, t.Settings.TriggerName = d.Event.Source, d.Event.Name
	}
	step := map[string]any{"name": d.Name, "instructions": d.Instructions, "permissions": d.Permissions, "notify": d.Notify}
	if d.Target != "" {
		step["target"] = d.Target
	}
	if d.Dir != "" {
		step["dir"] = d.Dir
	}
	if d.Project != "" {
		step["project"] = d.Project
	}
	if d.Model != "" {
		step["model"] = d.Model
	}
	if len(d.PostTo) > 0 {
		step["postTo"] = d.PostTo
	}
	if d.Source != "" && d.Source != sourceDev {
		step["source"] = d.Source
	}
	t.NextAction = &FlowAction{
		Name: agentAction, Type: ActionTypeProvider, DisplayName: "Agent", Valid: true,
		Settings: StepSettings{ProviderName: coreProvider, ActionName: agentAction, Input: step},
	}
	// Its answer is posted: at once when it acts on its own, after its person
	// says so when it asks first.
	if len(d.PostTo) > 0 {
		t.NextAction.NextAction = &FlowAction{
			Name: postAction, Type: ActionTypeProvider, DisplayName: "Post", Valid: true,
			Settings: StepSettings{ProviderName: coreProvider, ActionName: postAction,
				Input: map[string]any{"to": d.PostTo, "review": d.Permissions == PermissionAsk}},
		}
	}
	return t
}

// definitionOf reads an automation out of a flow version, and reports false for a
// version that is not one: anything but a schedule or manual trigger followed by
// exactly one agent step, whose definition passes check and whose step tree is
// byte for byte the one these routes write for it (trigger).
func definitionOf(v *FlowVersion) (definition, bool) {
	if v == nil || v.Trigger == nil {
		return definition{}, false
	}
	t := v.Trigger
	if t.Strategy != provider.StrategyWebhook && (t.Settings.ProviderName != coreProvider || (t.Settings.TriggerName != "schedule" && t.Settings.TriggerName != "manual")) {
		return definition{}, false
	}
	a := t.NextAction
	if a == nil || a.Settings.ProviderName != coreProvider || a.Settings.ActionName != agentAction {
		return definition{}, false
	}
	raw, ok := t.Settings.Input["schedule"].(map[string]any)
	if !ok {
		return definition{}, false
	}
	var s Schedule
	if b, err := json.Marshal(raw); err != nil || json.Unmarshal(b, &s) != nil {
		return definition{}, false
	}
	in := a.Settings.Input
	d := definition{
		Target: strInput(in, "target"),
		Dir:    strInput(in, "dir"),
		Name:   v.DisplayName, Instructions: strInput(in, "instructions"),
		Project: strInput(in, "project"), Model: strInput(in, "model"),
		Permissions: cmp.Or(strInput(in, "permissions"), PermissionAsk),
		Schedule:    s, Notify: boolInput(in, "notify"), PostTo: strsInput(in, "postTo"),
		Source: strInput(in, "source"),
	}
	if t.Strategy == provider.StrategyWebhook {
		d.Event = &OutcomeTrigger{Source: t.Settings.ProviderName, Name: t.Settings.TriggerName}
	}
	if d.check() != nil {
		return definition{}, false
	}
	have, herr := json.Marshal(t)
	want, werr := json.Marshal(d.trigger())
	if herr != nil || werr != nil || !bytes.Equal(have, want) {
		return definition{}, false
	}
	return d, true
}

// draftOf reads a draft out of flow f and its latest version v: no pinned
// version, and a manual core trigger with no step, or no trigger at all. It is
// the automation it will be when its instructions are saved: run on demand,
// proposing what it would do. A webhook or a schedule with no step is the
// builder's, armed and its own, and not a draft.
func draftOf(f Flow, v *FlowVersion) (definition, bool) {
	if v == nil || f.PublishedVersionID != "" || !bare(v.Trigger) {
		return definition{}, false
	}
	name := strings.TrimSpace(v.DisplayName)
	if name == "" || utf8.RuneCountInString(name) > maxName || strings.ContainsFunc(name, unicode.IsControl) {
		name = "Untitled"
	}
	return definition{Name: name, Permissions: PermissionAsk, Schedule: Schedule{Kind: KindManual, TZ: "UTC"}, Source: sourceDev, draft: true}, true
}

// bare reports whether a step tree does nothing yet: no trigger, or a manual
// core trigger with no step.
func bare(t *FlowTrigger) bool {
	return t == nil || (t.NextAction == nil && t.Settings.ProviderName == coreProvider && t.Settings.TriggerName == "manual" &&
		(t.Strategy == "" || t.Strategy == provider.StrategyManual))
}

// isAutomation reports whether a version is an automation, which runs one at a
// time (runInput).
func isAutomation(v *FlowVersion) bool {
	_, ok := definitionOf(v)
	return ok
}

// present is an automation as the page reads it.
func present(f Flow, d definition, last *FlowRun, now time.Time) Automation {
	a := Automation{
		Event: d.Event, Target: orNull(d.Target), Dir: orNull(d.Dir),
		ID: f.ID, Name: d.Name, Instructions: d.Instructions,
		Project: orNull(d.Project), Model: orNull(d.Model),
		Schedule: d.Schedule, Permissions: d.Permissions, Notify: d.Notify, PostTo: nonNil(d.PostTo),
		Enabled: f.Status == FlowEnabled && !d.draft, Draft: d.draft, Source: cmp.Or(d.Source, sourceDev),
		Created: stamp(f.Created), Updated: stamp(f.Updated),
	}
	if a.Enabled {
		if t := upcoming(d.Schedule, now); !t.IsZero() {
			at := t.Format(time.RFC3339)
			a.Next = &at
		}
	}
	if last != nil {
		a.Last = &LastRun{ID: last.ID, Status: wireStatus(last.Status), At: stamp(last.StartTime), Summary: last.Summary}
	}
	return a
}

// presentRun is one run as the page reads it.
func presentRun(r FlowRun) AutomationRun {
	out := AutomationRun{ID: r.ID, Status: wireStatus(r.Status), At: stamp(r.StartTime), Summary: r.Summary, Draft: r.Draft, Posts: []PostResult{}, Outputs: []string{}}
	_ = json.Unmarshal([]byte(r.Posts), &out.Posts)
	_ = json.Unmarshal([]byte(cmp.Or(r.Outputs, "[]")), &out.Outputs)
	if terminal(r.Status) && r.FinishTime > 0 {
		at := stamp(r.FinishTime)
		out.Finished = &at
	}
	if link := transcript(r.Session); link != "" {
		out.Transcript = &link
	}
	return out
}

// wireStatus is a run status in the page's words.
func wireStatus(s FlowRunStatus) string {
	switch s {
	case RunSucceeded:
		return "succeeded"
	case RunQueued:
		return "queued"
	case RunSkipped:
		return "skipped"
	case RunRefused:
		return "refused"
	case RunPaused:
		return "review"
	case RunRunning:
		return "running"
	}
	return "failed"
}

func stamp(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func orNull(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Person is who an automation runs as: the IAM subject its Dev run is held by,
// and the address its notice goes to.
type Person struct{ Sub, Email string }

// person is the caller as the person an automation runs as. A credential that
// names no subject cannot hold a Dev run (plugin/agent coding.go), so it cannot
// define one either.
func person(ctx context.Context) (Person, error) {
	sub, keyed := principal.SubjectFrom(ctx)
	if sub = strings.TrimSpace(sub); !keyed || sub == "" {
		return Person{}, zip.ErrForbidden("automations: this credential names no person for the automation to run as")
	}
	var email string
	if c, ok := cloud.Request(ctx); ok {
		email = strings.TrimSpace(c.UserEmail())
	}
	return Person{Sub: sub, Email: email}, nil
}

// manages refuses a caller who is not the person a flow runs as: only they read
// it, run it, change it, delete it or read its runs. The runs are theirs — what
// their connectors read — and an org admin governs the org, not its members'
// work, so being one admits nothing here. A flow that names no person is
// nobody's, and refused to everyone. The refusal is a 404, the answer an id that
// never existed gets, so a member learns nothing of another's flows.
func manages(ctx context.Context, who Person) error {
	sub, _ := principal.SubjectFrom(ctx)
	if sub = strings.TrimSpace(sub); sub != "" && sub == who.Sub {
		return nil
	}
	return zip.ErrNotFound("automation not found")
}

// mayPost refuses a definition that posts, from a caller who is not an admin of
// the org: the accounts it posts to are the organization's, and whoever defines
// it is the person it posts as.
func mayPost(ctx context.Context, d definition) error {
	if len(d.PostTo) == 0 {
		return nil
	}
	if c, ok := cloud.Request(ctx); ok && principal.IsOrgAdmin(c) {
		return nil
	}
	return zip.ErrForbidden("automations: only an admin of the organization defines an automation that posts to its accounts")
}

// mayManage is manages as a yes or no, for what a reader is shown.
func mayManage(ctx context.Context, who Person) bool { return manages(ctx, who) == nil }

// plain refuses to change a flow that is an automation through the generic flow
// routes. What an automation does and the person it runs as are written together,
// here and nowhere else (DefineVersion), and its armed schedule with them.
func (o ops) plain(ctx context.Context, org, id string) error {
	v, err := o.s.State.store.LatestVersion(ctx, org, id)
	if err == nil && isAutomation(&v) {
		return errAutomation(id)
	}
	return nil
}

func errAutomation(id string) error {
	return &zip.HTTPError{Status: http.StatusConflict, Code: "automation",
		Msg: "flow " + id + " is an automation; change it at /v1/auto/automations/" + id}
}

// projectOwned asks the project app whether slug is one of org's projects. It is
// a value so a test can answer for the app; production never reassigns it.
var projectOwned = func(ctx context.Context, org, slug string) (bool, error) {
	out, err := projectpeer.ProjectsOwnership(cloud.For(ctx, org), &client.OwnerIn{IDOrSlug: slug})
	if err != nil {
		return false, err
	}
	return out != nil && out.Mine, nil
}

// ours refuses a project that is not the org's. A deployment with no project app
// has nothing to ask, and a slug is then taken on its shape alone.
func ours(ctx context.Context, org, slug string) error {
	if slug == "" {
		return nil
	}
	mine, err := projectOwned(ctx, org, slug)
	switch {
	case errors.Is(err, plane.ErrNoPeer):
		return nil
	case err != nil:
		return zip.Errorf(http.StatusServiceUnavailable, "automations: the project could not be checked: %v", err)
	case !mine:
		return &zip.HTTPError{Status: http.StatusUnprocessableEntity, Code: "invalid_project", Msg: "project " + slug + " is not one of this organization's projects"}
	}
	return nil
}

// load reads one automation of org: its flow, its version and its definition. A
// flow that is neither an automation nor a draft is not found here, whatever it
// is elsewhere.
func (o ops) load(ctx context.Context, org, id string) (Flow, FlowVersion, definition, error) {
	f, err := o.s.State.store.GetFlow(ctx, org, id)
	if err != nil {
		return Flow{}, FlowVersion{}, definition{}, mapStoreErr(err, "automation not found")
	}
	v, err := o.s.State.store.LatestVersion(ctx, org, f.ID)
	if err != nil {
		return Flow{}, FlowVersion{}, definition{}, mapStoreErr(err, "automation not found")
	}
	d, ok := definitionOf(&v)
	if !ok {
		d, ok = draftOf(f, &v)
	}
	if !ok {
		return Flow{}, FlowVersion{}, definition{}, zip.ErrNotFound("automation not found")
	}
	return f, v, d, nil
}

// last is a flow's most recent run that was not skipped, or nil.
func (o ops) last(ctx context.Context, org, flowID string) *FlowRun {
	r, err := o.s.State.store.LastRun(ctx, org, flowID)
	if err != nil {
		return nil
	}
	// Its line is what the person's connectors read: only those who may manage
	// the automation see it.
	if who, err := o.s.State.store.FlowOwner(ctx, org, flowID); err != nil || !mayManage(ctx, who) {
		r.Summary = ""
	}
	return &r
}

// owned loads one automation of org for the person it runs as, and answers any
// other caller as though it did not exist.
func (o ops) owned(ctx context.Context, org, id string) (Flow, FlowVersion, definition, error) {
	f, v, d, err := o.load(ctx, org, id)
	if err != nil {
		return f, v, d, err
	}
	who, err := o.s.State.store.FlowOwner(ctx, org, f.ID)
	if err != nil {
		return f, v, d, mapStoreErr(err, "automation not found")
	}
	return f, v, d, manages(ctx, who)
}

// ListAutomations returns your automations — another member's are never listed.
// `q` keeps those whose name or instructions contain it, ignoring case; `source`
// keeps those made in one product; `sort` is `updated` (the default, newest
// first), `name`, or `next` (soonest first, unscheduled last).
func (o ops) listAutomations(ctx context.Context, in *automationQuery) (*automationPage, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	sub, _ := principal.SubjectFrom(ctx)
	order := strings.ToLower(strings.TrimSpace(in.Sort))
	if order != "" && order != "updated" && order != "name" && order != "next" {
		return nil, invalid("sort must be name, next or updated")
	}
	source := strings.ToLower(strings.TrimSpace(in.Source))
	if source != "" && !oneOf(jobProducts, source) {
		return nil, invalid("source must be chat, dev, studio or workflow")
	}
	flows, err := o.s.State.store.ListFlows(ctx, org, strings.TrimSpace(sub), maxLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	q := strings.ToLower(strings.TrimSpace(in.Q))
	now := time.Now()
	out := make([]Automation, 0, len(flows))
	for _, f := range flows {
		v, err := o.s.State.store.LatestVersion(ctx, org, f.ID)
		if err != nil {
			continue
		}
		d, ok := definitionOf(&v)
		if !ok {
			d, ok = draftOf(f, &v)
		}
		if !ok {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(d.Name), q) && !strings.Contains(strings.ToLower(d.Instructions), q) {
			continue
		}
		if source != "" && cmp.Or(d.Source, sourceDev) != source {
			continue
		}
		out = append(out, present(f, d, o.last(ctx, org, f.ID), now))
	}
	switch order {
	case "name":
		slices.SortStableFunc(out, func(a, b Automation) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	case "next":
		slices.SortStableFunc(out, func(a, b Automation) int {
			switch {
			case a.Next == nil && b.Next == nil:
				return 0
			case a.Next == nil:
				return 1
			case b.Next == nil:
				return -1
			}
			return strings.Compare(*a.Next, *b.Next)
		})
	}
	return &automationPage{Data: out}, nil
}

// CreateAutomation creates an automation and arms its schedule. It runs as the
// caller: their Dev run, in this org, each time it fires. `name` and
// `instructions` are required; no `schedule` runs it only on demand, no
// `permissions` is ask: it proposes what it would do and changes nothing, and
// `enabled: false` creates it off, its schedule unarmed.
//
// Example: {"name": "Daily briefing", "instructions": "What needs my attention today across calendar, email and messages.", "schedule": {"kind": "weekdays", "at": "08:00", "tz": "America/Los_Angeles"}, "permissions": "ask", "notify": true}
func (o ops) createAutomation(ctx context.Context, in *automationIn) (*Automation, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	who, err := person(ctx)
	if err != nil {
		return nil, err
	}
	d := definition{Name: in.Name, Instructions: in.Instructions, Permissions: in.Permissions, Notify: in.Notify, PostTo: in.PostTo, Source: in.Source}
	d.Event = in.Event
	if in.Target != nil {
		d.Target = *in.Target
	}
	if in.Dir != nil {
		d.Dir = *in.Dir
	}
	if in.Project != nil {
		d.Project = *in.Project
	}
	if in.Model != nil {
		d.Model = *in.Model
	}
	if in.Schedule != nil {
		d.Schedule = *in.Schedule
	}
	if err := d.check(); err != nil {
		return nil, err
	}
	if err := d.routable(); err != nil {
		return nil, err
	}
	if err := ahead(d.Schedule, time.Now()); err != nil {
		return nil, err
	}
	if err := ours(ctx, org, d.Project); err != nil {
		return nil, err
	}
	if err := mayPost(ctx, d); err != nil {
		return nil, err
	}
	enabled := in.Enabled == nil || *in.Enabled
	now := time.Now().UnixMilli()
	f := Flow{ID: mint.ID("flow"), Org: org, Status: FlowDisabled, Created: now, Updated: now}
	if enabled {
		f.Status = FlowEnabled
	}
	if _, err := o.s.State.store.CreateAutomationFlow(ctx, f, maxAutomations); errors.Is(err, errAutomationLimit) {
		return nil, &zip.HTTPError{Status: http.StatusUnprocessableEntity, Code: "automation_limit",
			Msg: fmt.Sprintf("this organization keeps %d automations, the most it may; delete one first", maxAutomations)}
	} else if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "create automation: %v", err)
	}
	// Whatever fails from here takes the flow with it, so a refused create leaves
	// nothing behind.
	undo := func() { _, _ = o.s.State.store.DeleteFlow(context.WithoutCancel(ctx), org, f.ID) }
	if err := o.s.State.store.SetFlowOwner(ctx, org, f.ID, who); err != nil {
		undo()
		return nil, zip.Errorf(http.StatusInternalServerError, "create automation: %v", err)
	}
	v, err := o.s.State.store.CreateVersion(ctx, FlowVersion{
		ID: mint.ID("ver"), Org: org, FlowID: f.ID, DisplayName: d.Name, Trigger: d.trigger(), Valid: true,
		State: VersionDraft, SchemaVersion: LatestFlowSchemaVersion, Created: now, Updated: now,
	})
	if err != nil {
		undo()
		return nil, mapStoreErr(err, "automation not found")
	}
	if err := armTrigger(o.s, ctx, org, f, v, nil, enabled); err != nil {
		undo()
		return nil, err
	}
	auditHTTP(o.s, ctx, org, "automations.automation.create", f.ID, "ok", http.StatusCreated)
	// Once, with no time, is now: it runs as it is made, and then it is done.
	var last *FlowRun
	if d.Event == nil && d.Schedule.Kind == KindOnce && d.Schedule.At == "" {
		run, _, err := startRun(o.s, ctx, org, f, v, mint.ID("run"), 0, nil)
		if err != nil {
			return nil, engineErr(err)
		}
		last = &run
	}
	a := present(f, d, last, time.Now())
	return &a, nil
}

// GetAutomation returns one automation. A flow of the org that is not an
// automation, and anything of another org, answers not-found.
//
// Example: {"id": "flow_1"}
func (o ops) getAutomation(ctx context.Context, in *automationRef) (*Automation, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	f, _, d, err := o.owned(ctx, org, in.ID)
	if err != nil {
		return nil, err
	}
	a := present(f, d, o.last(ctx, org, f.ID), time.Now())
	return &a, nil
}

// UpdateAutomation changes an automation, for the person it runs as: any field
// it was created with, and
// `enabled`, which arms or disarms its schedule. A field the request omits is
// left alone; `project` or `model` sent as null (or "") clears it. Any change
// but `enabled` makes the caller the person it runs as from then on. A draft is
// finished by saving its `instructions`: it becomes an automation in place, on
// unless `enabled` says otherwise.
//
// Example: {"id": "flow_1", "schedule": {"kind": "weekly", "day": "fri", "at": "16:00", "tz": "Europe/London"}}
func (o ops) updateAutomation(ctx context.Context, in *automationPatch) (*Automation, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	f, v, d, err := o.owned(ctx, org, in.ID)
	if err != nil {
		return nil, err
	}
	sent := nulls(ctx)
	defines := false
	set := func(dst *string, src *string, key string) {
		if src != nil {
			*dst, defines = *src, true
		} else if sent[key] {
			*dst, defines = "", true // sent as null
		}
	}
	set(&d.Name, in.Name, "name")
	set(&d.Instructions, in.Instructions, "instructions")
	set(&d.Project, in.Project, "project")
	set(&d.Model, in.Model, "model")
	set(&d.Target, in.Target, "target")
	set(&d.Dir, in.Dir, "dir")
	set(&d.Permissions, in.Permissions, "permissions")
	if in.Schedule != nil {
		d.Schedule, defines = *in.Schedule, true
		d.Event = nil
	}
	if in.Event != nil {
		if in.Schedule != nil {
			return nil, invalid("send event or schedule, not both")
		}
		d.Event, d.Schedule, defines = in.Event, Schedule{}, true
	} else if sent["event"] {
		d.Event, defines = nil, true
	}
	if in.Notify != nil {
		d.Notify, defines = *in.Notify, true
	}
	if in.PostTo != nil {
		d.PostTo, defines = *in.PostTo, true
	} else if sent["postTo"] {
		d.PostTo, defines = nil, true
	}
	if err := d.check(); err != nil {
		return nil, err
	}
	if in.Schedule != nil {
		if err := ahead(d.Schedule, time.Now()); err != nil {
			return nil, err
		}
	}
	if defines {
		if err := d.routable(); err != nil {
			return nil, err
		}
		if err := mayPost(ctx, d); err != nil {
			return nil, err
		}
	}
	if in.Project != nil || sent["project"] {
		if err := ours(ctx, org, d.Project); err != nil {
			return nil, err
		}
	}
	enabled := f.Status == FlowEnabled || d.draft
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	var who Person
	if defines {
		if who, err = person(ctx); err != nil {
			return nil, err
		}
	}

	// Disarm what the stored version armed before it is replaced: its schedule is
	// named by the flow, and only the old version knows it had one. A draft armed
	// nothing.
	if !d.draft {
		if err := armTrigger(o.s, ctx, org, f, v, nil, false); err != nil {
			return nil, err
		}
	}
	now := time.Now().UnixMilli()
	// What it does and who it runs as change in one write, so two edits at once
	// never leave one person's words running as the other. A change of enabled
	// alone writes neither: it would write back what this call read, over an edit
	// made since.
	if defines {
		v.DisplayName, v.Trigger, v.Valid, v.Updated = d.Name, d.trigger(), true, now
		if d.draft {
			v, err = o.s.State.store.FinishDraft(ctx, v, who, maxAutomations)
		} else {
			v, err = o.s.State.store.DefineVersion(ctx, v, who)
		}
		switch {
		case errors.Is(err, errAutomationLimit):
			return nil, &zip.HTTPError{Status: http.StatusUnprocessableEntity, Code: "automation_limit",
				Msg: fmt.Sprintf("this organization keeps %d automations, the most it may; delete one first", maxAutomations)}
		case errors.Is(err, errChanged):
			return nil, &zip.HTTPError{Status: http.StatusConflict, Code: "changed",
				Msg: "this draft changed since it was read; open it again"}
		case err != nil:
			return nil, mapStoreErr(err, "automation not found")
		}
		if d.draft {
			unschedule(ctx, o.s, org, f.ID)
			d.draft = false
		}
	}
	// An automation runs what it says now: no earlier version stays pinned.
	f.PublishedVersionID, f.Updated = "", now
	f.Status = FlowDisabled
	if enabled {
		f.Status = FlowEnabled
	}
	if f, err = o.s.State.store.UpdateFlow(ctx, f); err != nil {
		return nil, mapStoreErr(err, "automation not found")
	}
	if enabled {
		if err := armTrigger(o.s, ctx, org, f, v, nil, true); err != nil {
			// Saved, and not armed: say so in its record rather than leave it
			// reading enabled with nothing behind it.
			f.Status = FlowDisabled
			_, _ = o.s.State.store.UpdateFlow(context.WithoutCancel(ctx), f)
			return nil, err
		}
	}
	auditHTTP(o.s, ctx, org, "automations.automation.update", f.ID, "ok", http.StatusOK)
	a := present(f, d, o.last(ctx, org, f.ID), time.Now())
	return &a, nil
}

// drop deletes flow f of org — the ONE delete, for an automation and a plain flow
// alike, whichever route asked. It disarms what the flow armed first, so no
// schedule outlives it; for an automation it also refuses a caller who may not
// manage it, and stops a Dev run it has going.
func (o ops) drop(ctx context.Context, org string, f Flow) error {
	v, verr := o.s.State.store.LatestVersion(ctx, org, f.ID)
	action := "automations.flow.delete"
	if verr == nil && isAutomation(&v) {
		who, err := o.s.State.store.FlowOwner(ctx, org, f.ID)
		if err != nil {
			return mapStoreErr(err, "automation not found")
		}
		if err := manages(ctx, who); err != nil {
			return err
		}
		if runs, err := o.s.State.store.ListRuns(ctx, org, f.ID, defaultLimit); err == nil {
			for _, r := range runs {
				if !terminal(r.Status) {
					halt(ctx, o.s, r)
				}
			}
		}
		action = "automations.automation.delete"
	}
	if err := armTrigger(o.s, ctx, org, f, v, verr, false); err != nil {
		return err
	}
	unschedule(ctx, o.s, org, f.ID)
	deleted, err := o.s.State.store.DeleteFlow(ctx, org, f.ID)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return zip.ErrNotFound("flow not found")
	}
	auditHTTP(o.s, ctx, org, action, f.ID, "ok", http.StatusNoContent)
	return nil
}

// unschedule deletes flow id's engine schedule whatever its latest version says.
// A schedule is named by the flow, and one an earlier version armed outlives
// that version otherwise: a deleted flow, or a finished draft, would go on
// firing it. A flow with no schedule has nothing to delete.
func unschedule(ctx context.Context, s *cloud.Service[state], org, id string) {
	if err := disableSchedule(ctx, org, "flow-"+id); err != nil {
		s.Log.Debug("auto: no schedule to delete", "org", org, "flow", id, "err", err)
	}
}

// nulls is the set of top-level keys the request body sent as null, which is
// what tells a field cleared from one left alone. Off the HTTP path there is no
// body, and a null is then the same as absent.
func nulls(ctx context.Context) map[string]bool {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(c.Body(), &body) != nil {
		return nil
	}
	out := make(map[string]bool, len(body))
	for k, v := range body {
		if string(v) == "null" {
			out[k] = true
		}
	}
	return out
}

// DeleteAutomation deletes an automation, its schedule and its run history, and
// stops a Dev run it has going. It answers no content.
//
// Example: {"id": "flow_1"}
func (o ops) deleteAutomation(ctx context.Context, in *automationRef) (*cloud.Unit, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	f, _, _, err := o.owned(ctx, org, in.ID)
	if err != nil {
		return nil, err
	}
	return nil, o.drop(ctx, org, f)
}

// RunAutomation starts one run now, whether or not its schedule is armed. Only
// the person it runs as may start one; who did is kept. An
// automation runs one at a time: while a run is going, this records a skipped
// start and answers it. Every run first asks IAM whether the person it runs as
// is still a member of the org; one who is not makes the run refused and turns
// the automation off.
//
// Example: {"id": "flow_1"}
func (o ops) runAutomation(ctx context.Context, in *automationRef) (*runStarted, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	f, v, d, err := o.owned(ctx, org, in.ID)
	if err != nil {
		return nil, err
	}
	if d.draft {
		return nil, &zip.HTTPError{Status: http.StatusUnprocessableEntity, Code: "draft",
			Msg: "this automation has no instructions yet; save them to run it"}
	}
	run, _, err := startRun(o.s, ctx, org, f, v, mint.ID("run"), 0, nil)
	if err != nil {
		return nil, engineErr(err)
	}
	// Who pressed Run is kept on the run and in the trail.
	actor, _ := principal.SubjectFrom(ctx)
	if err := o.s.State.store.SetRunActor(ctx, org, run.ID, strings.TrimSpace(actor)); err != nil {
		o.s.Log.Warn("auto: who started a run was not recorded", "org", org, "run", run.ID, "err", err)
	}
	auditHTTP(o.s, ctx, org, "automations.automation.run", f.ID+"/"+run.ID, wireStatus(run.Status), http.StatusCreated)
	return &runStarted{Run: runHandle{ID: run.ID, Status: wireStatus(run.Status)}}, nil
}

// ListAutomationRuns returns one automation's runs, newest first, to the person
// it runs as. Each links the
// Dev run it started as `transcript`. The optional `limit` bounds the page.
//
// Example: {"id": "flow_1"}
func (o ops) listAutomationRuns(ctx context.Context, in *automationRunsQuery) (*automationRunPage, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	f, _, _, err := o.owned(ctx, org, in.ID)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ListRuns(ctx, org, f.ID, boundLimit(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list runs: %v", err)
	}
	out := make([]AutomationRun, 0, len(rows))
	for _, r := range rows {
		out = append(out, presentRun(r))
	}
	return &automationRunPage{Data: out}, nil
}

type reviewIn struct {
	// ID is the automation, and Run its run in review, from the path.
	ID  string `json:"id"`
	Run string `json:"run"`
	// Post posts it when true and discards it when false.
	Post bool `json:"post"`
}

// ReviewRun answers a run that holds a post for review, for the person the
// automation runs as: `post` true posts it to the
// automation's accounts and answers the run while it posts; false discards it.
// A run not in review answers 409; one held longer than a day is not posted.
//
// Example: {"id": "flow_1", "run": "run_1", "post": true}
func (o ops) reviewRun(ctx context.Context, in *reviewIn) (*AutomationRun, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	f, _, d, err := o.owned(ctx, org, in.ID)
	if err != nil {
		return nil, err
	}
	store := o.s.State.store
	run, err := store.GetRun(ctx, org, strings.TrimSpace(in.Run))
	if err != nil || run.FlowID != f.ID {
		return nil, zip.ErrNotFound("run not found")
	}
	// Claimed before anything is said or sent, so two answers at once post once.
	if ok, err := store.ClaimReview(ctx, org, run.ID); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "review: %v", err)
	} else if !ok {
		return nil, &zip.HTTPError{Status: http.StatusConflict, Code: "not_in_review", Msg: "this run is not waiting for review"}
	}
	now := time.Now()
	// What happens after this answer happens off the request: Detach keeps the
	// caller this request stated and lets the request go, so a call made later —
	// IAM about the run's person, the provider app's post — says who it is for.
	detached := zip.Detach(ctx)
	end := func(status FlowRunStatus, summary string) {
		bg := detached
		_ = store.SetRunSummary(bg, org, run.ID, summary)
		_ = store.UpdateRunStatus(bg, org, run.ID, status, time.Now().UnixMilli(), time.Now().UnixMilli())
	}
	verdict := "discard"
	switch {
	case now.Sub(time.UnixMilli(run.FinishTime)) > reviewWindow:
		verdict = "late"
		end(RunFailed, "Not posted: nobody reviewed it within a day.")
	case !in.Post:
		end(RunSucceeded, "Discarded at review; nothing was posted.")
	case len(d.PostTo) == 0:
		verdict = "nowhere"
		end(RunFailed, "Not posted: this automation no longer posts anywhere.")
	default:
		verdict = "post"
		post := PostIn{Owner: org, RunID: run.ID, To: d.PostTo, Text: run.Draft}
		go func() {
			bg, cancel := context.WithTimeout(detached, deliverWithin)
			defer cancel()
			results, err := deliver(bg, o.s, post)
			if err != nil {
				end(RunFailed, "Not posted: "+err.Error())
				return
			}
			_ = store.UpdateRunStatus(bg, org, post.RunID, posted(results), time.Now().UnixMilli(), time.Now().UnixMilli())
		}()
	}
	auditHTTP(o.s, ctx, org, "automations.automation.review", f.ID+"/"+run.ID, verdict, http.StatusOK)
	if run, err = store.GetRun(ctx, org, run.ID); err != nil {
		return nil, mapStoreErr(err, "run not found")
	}
	out := presentRun(run)
	return &out, nil
}

// ListTemplates returns the starter automations: a name, what it does, the
// instructions it runs and the schedule it suggests, in the caller's own zone.
func (o ops) listTemplates(ctx context.Context, _ *cloud.Unit) (*starterPage, error) {
	if err := validated(ctx); err != nil {
		return nil, err
	}
	return &starterPage{Data: slices.Clone(starters)}, nil
}

// starters are the six ready-made automations. Their schedules name no zone: the
// page fills in the person's own.
var starters = []Starter{
	{
		Key: "briefing", Name: "Daily briefing", Icon: "sunrise",
		Description:  "What needs your attention today across calendar, email and messages.",
		Instructions: "Give me a short briefing on what needs my attention today. Look at today's calendar, unread and flagged email, and messages that mention me or wait on my reply. Lead with anything urgent, then list the rest in order of importance, one line each.",
		Schedule:     Schedule{Kind: KindWeekdays, At: "08:00"},
	},
	{
		Key: "inbox", Name: "Inbox triage", Icon: "inbox",
		Description:  "Categorize your inbox and draft replies to anything urgent.",
		Instructions: "Go through my inbox since the last triage. Sort each message into urgent, needs a reply, FYI or noise. Draft a reply to every urgent message and leave the drafts for me to review; do not send anything. Finish with a count per category and the urgent ones by subject.",
		Schedule:     Schedule{Kind: KindWeekdays, At: "08:00"},
	},
	{
		Key: "meetings", Name: "Meeting prep", Icon: "calendar",
		Description:  "A short brief before each meeting on your calendar: attendees, context and agenda.",
		Instructions: "For each meeting on my calendar today, write a short brief: who is attending and their role, what we last discussed with them, open items, and a suggested agenda. Keep each brief to a few lines.",
		Schedule:     Schedule{Kind: KindWeekdays, At: "08:00"},
	},
	{
		Key: "review", Name: "Weekly review", Icon: "list-checks",
		Description:  "A Friday summary of what happened this week.",
		Instructions: "Summarize my week: what shipped or got done, meetings and decisions, what slipped, and what is waiting on me next week. Group it by project and keep it to one screen.",
		Schedule:     Schedule{Kind: KindWeekly, Day: "fri", At: "16:00"},
	},
	{
		Key: "ideas", Name: "Content ideas", Icon: "lightbulb",
		Description:  "Draft a few post ideas each week from the latest news in your industry.",
		Instructions: "Read this week's most important news in my industry and draft five post ideas from it. For each: a headline, two sentences on the angle, and the source it builds on.",
		Schedule:     Schedule{Kind: KindWeekly, Day: "mon", At: "09:00"},
	},
	{
		Key: "monitor", Name: "Monitor a topic", Icon: "radar",
		Description:  "Watch for news or mentions of a topic, competitor or keyword.",
		Instructions: "Search for new news and mentions of the topic I name here since the last run: <topic>. List each new item with its source, date and one line on why it matters. Say plainly when there is nothing new.",
		Schedule:     Schedule{Kind: KindDaily, At: "09:00"},
	},
}
