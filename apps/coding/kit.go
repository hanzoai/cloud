package coding

// kit.go is a run's half of its org's tool plane. The tool app owns the rows
// (apps/tools kit.go): the skills an admin of the org activated, and the MCP
// servers an admin registered with the tools of each an admin activated. A
// sandbox run asks it over the plane what to carry and hands it to its harness —
// the skills as files where the harness reads skills, the servers in its MCP
// configuration beside the run's own ask server (sandboxrunner.go).
//
// A SERVER'S ADDRESS AND CREDENTIAL NEVER REACH THE SANDBOX. The run's person can
// open a terminal on it, and the credential is the org's. The harness is handed
// the run's own MCP address for each server (relay.go, /v1/agent/mcp/<server>),
// authenticated by the run's ticket as the ask server is; the tool app adds the
// credential and dials the server itself.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/client"
	toolpeer "github.com/hanzoai/cloud/client/tool"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// Kit is what one sandbox run carries of its org's tools.
type Kit struct {
	// Skills are the documents of the org's active skills, by name.
	Skills []client.SkillDoc
	// Omitted are active skills the tool app left out of a run's bound.
	Omitted []string
	// Servers are the org's MCP servers, each under the key its harness
	// configuration names it by, at most client.KitServers of them.
	Servers []Server
	// Excess are servers left out, by id, over what a run carries.
	Excess []string
}

// Server is one MCP server as a harness is told about it.
type Server struct {
	// Name is the server's key in the harness's MCP configuration.
	Name string
	// ID is the server's id on the tool plane: the last segment of the run's MCP
	// address for it (relayURL), and the one name its ticket opens it by.
	ID string
	// Tools are the tools of it an admin of the org activated, by the server's
	// own names: the only ones the harness is given (argvFor).
	Tools []string
}

// skillRE is a skill's name as the tool plane admits one, which is also the
// directory its SKILL.md is written into: one lowercase path segment.
var skillRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// toolRE is a tool name a harness's allow-list can spell: what MCP clients admit
// of one, and never a comma, a space or a quote — claude's --allowedTools is a
// comma-separated list, where a name carrying a comma would allow a second thing.
var toolRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// serverRE is a server's id as the tool plane mints one (lowercase letters,
// digits and dashes), which is also one path segment of the run's MCP address.
var serverRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,127}$`)

// equipped resolves the org's kit for one sandbox run, on the run's own context,
// which acts for its org, as the run's person carries it: the tool app leaves out
// what that person muted, so a run carries what GET /v1/tool/kit shows them.
//
// A deployment that does not serve the tool plane runs every agent bare: there is
// nothing to carry. One that serves it and cannot answer fails the run — an agent
// started without the skills and servers its org set up would report failures
// that are not its own — as does an answer no harness could be handed.
func equipped(ctx context.Context, actor string) (Kit, error) {
	who := zip.CallerOf(ctx)
	who.User = actor
	c, cancel := bounded(zip.WithCaller(ctx, who))
	defer cancel()
	in, err := toolpeer.ToolsKit(c, &client.KitIn{})
	switch {
	case errors.Is(err, plane.ErrNoPeer):
		return Kit{}, nil
	case err != nil:
		return Kit{}, fmt.Errorf("the org's skills and MCP servers could not be read: %w", err)
	case in == nil:
		return Kit{}, errors.New("the org's skills and MCP servers could not be read: the tool app answered nothing")
	}
	k := Kit{Omitted: in.Omitted, Excess: in.Excess}
	for _, s := range in.Skills {
		if !skillRE.MatchString(s.Name) || strings.TrimSpace(s.Content) == "" {
			return Kit{}, fmt.Errorf("the tool app answered a skill that cannot be written: %q", s.Name)
		}
		k.Skills = append(k.Skills, s)
	}
	taken := map[string]bool{askServer: true, builtinServer: true}
	for _, r := range in.Servers {
		if !serverRE.MatchString(r.Name) {
			return Kit{}, fmt.Errorf("the tool app answered an MCP server no address can name: %q", r.Name)
		}
		for _, t := range r.Tools {
			if !toolRE.MatchString(t) {
				return Kit{}, fmt.Errorf("the tool app answered MCP server %q with a tool no harness can name: %q", r.Name, t)
			}
		}
		if len(r.Tools) == 0 {
			continue // nothing of it is activated, so there is nothing to call
		}
		// The bound is the run's too, whatever the tool app answered.
		if len(k.Servers) == client.KitServers {
			k.Excess = append(k.Excess, r.Name)
			continue
		}
		k.Servers = append(k.Servers, Server{Name: serverKey(r.Name, taken), ID: r.Name, Tools: slices.Clone(r.Tools)})
	}
	return k, nil
}

// The MCP servers a harness already carries under a name: the run's ask server
// (argvFor), and hanzo-mcp, which the dev harness's own configuration names.
const (
	askServer     = "run"
	builtinServer = "hanzo"
)

// serverKey is a server's name as a harness configuration key: what a TOML bare
// key and claude's server names both admit, lowercase, and not a name the harness
// already carries or an earlier server took — which gets a number instead.
func serverKey(name string, taken map[string]bool) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		}
	}
	key := strings.Trim(b.String(), "-")
	if len(key) > 48 {
		key = key[:48]
	}
	if key == "" {
		key = "mcp"
	}
	for i, k := 2, key; ; i++ {
		if !taken[k] {
			taken[k] = true
			return k
		}
		k = key + "-" + strconv.Itoa(i)
	}
}
