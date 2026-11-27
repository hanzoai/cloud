package tools

import (
	"context"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// kit.go — what an agent run carries of its org's tool plane.
//
// A coding run's harness reads skills from a directory and MCP servers from its
// own configuration, so the run needs the org's whole kit before its harness
// starts: the documents of every skill the org activated, and every MCP server it
// registered with the tools of it the org activated. Activation is read in the
// org's default scope, where skill_docs.go reads it too.
//
// A RUN CARRIES ONLY WHAT AN ADMIN PUT IN PLACE (govern.go). Every colleague's
// run loads the kit, and the harness calls its tools without asking anyone, so a
// skill is carried when an admin activated it — and, for the org's own, wrote it
// — and a server when an admin registered it, with exactly the tools of it an
// admin activated. A server none of whose tools is activated gives a run nothing
// to call, and is not carried.
//
// A server is answered by its id and the tools a run may call of it, and never
// by its address or its credential: the run reaches it through this plane
// (relay.go), which adds the credential and dials the server itself.
//
// A PERSON NARROWS THEIR OWN. What a run carries is the admin's kit minus what
// its person muted (activation.go): a skill, a server's tool, or a whole server by
// its id. The layer can only take away, so it is the person's to write, and it is
// read for the run's person on the plane and for the caller at GET /v1/tool/kit —
// one read, so what a person is shown is what their runs carry.

// maxKitBytes bounds the skill documents one run carries, whatever the org
// activated. The run writes them into its sandbox on a command's standard input,
// and a kit that outgrew the plane's message would fail the run it was for.
const maxKitBytes = 512 << 10

// serveKit publishes the op. Mount calls it once the stores are open.
func serveKit() {
	cloud.Plane().Post("/tools/kit", planeKit,
		zip.WithOperationID(client.ToolsKit),
		zip.WithSummary("What an agent run carries of its org's tools: its active skills and its MCP servers"))
}

// planeKit answers what an agent run of the org on the call carries: the
// documents of the skills an admin activated, at most sixteen and 512 KiB with
// the rest named as omitted, and the servers an admin registered, each with the
// tools of it an admin activated, at most sixteen with the rest named as excess.
func planeKit(ctx context.Context, _ *client.KitIn) (*client.Kit, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("tools: the call acts for no org")
	}
	s := mounted
	if s == nil || s.State.skills == nil || s.State.activation == nil || s.State.servers == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "tools: the tool plane is not open")
	}
	return kit(ctx, s.State.activation, s.State.skills, s.State.servers, org, cloud.Who(ctx).User)
}

// kit is the op's body over its three stores: org's kit as person's runs carry it.
func kit(ctx context.Context, act *ActivationStore, store *SkillStore, servers *MCPServerStore, org, person string) (*client.Kit, error) {
	out := &client.Kit{Skills: []client.SkillDoc{}, Servers: []client.Remote{}}

	activated, err := act.Admitted(ctx, org, "")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "tools: %v", err)
	}
	muted, err := act.Muted(ctx, org, person)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "tools: %v", err)
	}
	out.Muted = muted
	activated = slices.DeleteFunc(activated, func(t string) bool { return slices.Contains(muted, t) })
	var names []string
	for _, t := range activated {
		if name, ok := strings.CutPrefix(t, "skill_"); ok && name != "" {
			names = append(names, name)
		}
	}
	if len(names) > maxSkillDocs {
		out.Omitted = append(out.Omitted, names[maxSkillDocs:]...)
		names = names[:maxSkillDocs]
	}
	docs, err := skillDocs(ctx, act, store, org, names)
	if err != nil {
		return nil, err
	}
	size := 0
	for _, d := range docs.Skills {
		if size+len(d.Content) > maxKitBytes {
			out.Omitted = append(out.Omitted, d.Name)
			continue
		}
		size += len(d.Content)
		out.Skills = append(out.Skills, d)
	}
	if len(out.Skills) < maxSkillDocs {
		var defaults []string
		for _, def := range defaultSkillNames {
			if !slices.Contains(muted, "skill_"+def) && !slices.Contains(names, def) {
				defaults = append(defaults, def)
			}
		}
		if len(defaults) > 0 {
			if brand, err := brandDocs(ctx, &client.SkillDocsIn{Names: defaults}); err == nil && brand != nil {
				for _, d := range brand.Skills {
					if len(out.Skills) >= maxSkillDocs || size+len(d.Content) > maxKitBytes {
						break
					}
					size += len(d.Content)
					out.Skills = append(out.Skills, d)
				}
			}
		}
	}

	registered, err := servers.List(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "tools: %v", err)
	}
	for _, srv := range registered {
		tools := served(activated, srv.ID)
		if !srv.Admitted || len(tools) == 0 || slices.Contains(muted, srv.ID) {
			continue
		}
		if len(out.Servers) == client.KitServers {
			out.Excess = append(out.Excess, srv.ID)
			continue
		}
		out.Servers = append(out.Servers, client.Remote{Name: srv.ID, Tools: tools})
	}
	return out, nil
}

// harnessTool is a tool name every harness can be told to allow: what MCP
// clients admit of one (a Claude tool name is at most 64 of these), and never a
// comma, a space or a quote, which a harness's allow-list is spelled with.
var harnessTool = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// served is the tools of server id among the activated names — each by the name
// the server itself gives it, since the tool plane's name for it is
// "<id>_<name>" — that a harness can be told to allow.
func served(activated []string, id string) []string {
	var out []string
	for _, t := range activated {
		if name, ok := strings.CutPrefix(t, id+"_"); ok && harnessTool.MatchString(name) {
			out = append(out, name)
		}
	}
	return out
}

// ── GET/PUT /v1/tool/kit — the caller's kit, and the caller's layer on it ──────

// GetKit answers the caller's kit: what their coding runs carry of the org's tool
// plane. Skills are the documents of those an admin of the org activated; servers
// are those an admin registered, each by its id with the tools of it an admin
// activated — never an address or a credential, which stay on the plane. Both
// leave out what the caller muted, and muted names it. It is the one read a run
// makes, so what it answers is what the caller's runs carry.
func (o toolOps) getKit(ctx context.Context, _ *cloud.Unit) (*client.Kit, error) {
	org, person, err := personOf(ctx)
	if err != nil {
		return nil, err
	}
	return kit(ctx, o.s.State.activation, o.s.State.skills, o.s.State.servers, org, person)
}

// muteReq changes the caller's own layer on the org's kit. Unmute is applied
// first, so a name in both ends up muted.
type muteReq struct {
	// Mute takes these out of the caller's runs: a skill (skill_<name>), a
	// server's tool (<server>_<tool>) or a whole server (<server>), each one the
	// org's kit carries.
	Mute []string `json:"mute"`
	// Unmute gives these back, as far as the org's kit carries them.
	Unmute []string `json:"unmute"`
}

// PutKit mutes and unmutes names of the org's kit for the caller's own runs, and
// answers the caller's kit after. The layer only narrows: a name the org's kit
// does not carry is refused 400, since muting it would say nothing, and unmuting
// never gives the caller more than an admin put in place. It is the caller's own,
// so any member may write it. More than 256 names is refused 413.
//
// Example: {"mute": ["skill_deploy", "linear"], "unmute": []}
func (o toolOps) putKit(ctx context.Context, in *muteReq) (*client.Kit, error) {
	org, person, err := personOf(ctx)
	if err != nil {
		return nil, err
	}
	if len(in.Mute)+len(in.Unmute) > maxBatch {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "too many names (max %d)", maxBatch)
	}
	act := o.s.State.activation
	carried, err := act.Admitted(ctx, org, "")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "tools: %v", err)
	}
	for _, name := range in.Mute {
		if !validToolName(name) || !carries(carried, name) {
			return nil, zip.ErrBadRequest("the org's kit carries no " + name)
		}
	}
	for _, name := range in.Unmute {
		if err := act.Unmute(ctx, org, person, name); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "%v", err)
		}
	}
	for _, name := range in.Mute {
		if err := act.Mute(ctx, org, person, name); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "%v", err)
		}
	}
	o.audit(ctx, "kit.mute", org, "kit", "ok", http.StatusOK)
	return kit(ctx, act, o.s.State.skills, o.s.State.servers, org, person)
}

// defaultSkillNames are core built-in skills enabled out of the box for every
// coding run and org kit unless muted by the person (`skill_code-review`,
// `skill_dev`, `skill_ui`).
var defaultSkillNames = []string{"code-review", "dev", "ui"}

// carries reports whether the admitted names hold name: a skill or a server's
// tool by its own name, or a server by its id, the first segment of its tools'.
func carries(admitted []string, name string) bool {
	if sk, ok := strings.CutPrefix(name, "skill_"); ok && slices.Contains(defaultSkillNames, sk) {
		return true
	}
	for _, t := range admitted {
		if t == name || !strings.HasPrefix(t, "skill_") && strings.HasPrefix(t, name+"_") {
			return true
		}
	}
	return false
}

// personOf is the org and the person a kit is read for: the caller's own. A kit
// is a person's, so a call with no person behind it is refused.
func personOf(ctx context.Context) (org, person string, err error) {
	scope, err := scopeOf(ctx)
	if err != nil {
		return "", "", err
	}
	if person = callerOf(ctx); person == "" {
		return "", "", zip.ErrForbidden("tools: a kit is a person's, and the call names none")
	}
	return scope.Org, person, nil
}
