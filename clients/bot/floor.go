package bot

// The three methods the control UI asks for before it will consider itself
// loaded, which no family owned because each sits at the edge of one.
//
// health is answered from what this process actually knows. plugins.list is
// answered empty, which is the honest answer rather than a placeholder: no
// plugin is served here yet, so the list has no members, and a reader can tell
// "no plugins" from "this server cannot say" only if the method answers.
//
// commands.list is deliberately NOT here. chat.metadata relays to whoever
// serves commands, and no family does yet, so the method stays unadvertised and
// the UI hides the palette rather than showing an empty one. That is the growth
// path the protocol is built for: advertise what works, hide what does not.

import (
	"time"
)

func init() {
	Register("health", Read, health)
	Register("plugins.list", Read, pluginList)
}

// health reports what the process can see of itself: the agents it serves and
// how many sessions the caller's org holds. It reads the caller's own store, so
// an org that cannot open one learns that here rather than at its first send.
func health(c *Call) (any, error) {
	if err := c.Bind(&struct{}{}); err != nil {
		return nil, err
	}
	st, err := c.Store()
	if err != nil {
		return nil, err
	}
	rows, err := listSessionRows(c, st)
	if err != nil {
		return nil, err
	}
	recent := make([]string, 0, 5)
	for _, r := range rows {
		if len(recent) == cap(recent) {
			break
		}
		recent = append(recent, r.Key)
	}
	return map[string]any{
		"ok":             true,
		"ts":             time.Now().UnixMilli(),
		"agents":         []any{map[string]any{"id": sessionAgent, "kind": "agent"}},
		"defaultAgentId": sessionAgent,
		"sessions": map[string]any{
			"count":  len(rows),
			"path":   "bot/session",
			"recent": recent,
		},
	}, nil
}

// pluginList answers the plugin surface. The cloud's own plugins are subsystems
// registered at boot, not things a bot installs, so the list is empty until a
// family owns the difference.
//
// diagnostics and mutationAllowed are as required as the list itself: a reader
// with no diagnostics and no answer about mutation cannot tell an empty
// installation from a broken one. Nothing here can be installed or removed, so
// mutation is refused rather than left unstated.
func pluginList(c *Call) (any, error) {
	if err := c.Bind(&struct{}{}); err != nil {
		return nil, err
	}
	return map[string]any{
		"plugins":         []any{},
		"diagnostics":     []any{},
		"mutationAllowed": false,
	}, nil
}
