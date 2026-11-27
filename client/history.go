package client

import "strings"

// The budget a conversation is handed to a turn in, the same for every surface:
// a bridge's channel (apps/transport) and the app's conversation (apps/agents).
const (
	// HistoryBytes is the whole conversation a turn is handed — about 8k tokens.
	HistoryBytes = 32 << 10
	// TurnBytes caps one turn of it, so a dump or a paste cannot spend the rest.
	TurnBytes = 4 << 10
	// ToolsBytes caps the [Tools used] block an answer carries.
	ToolsBytes = 1536
	// ToolsMarker opens that block, after the answer's prose.
	ToolsMarker = "\n\n[Tools used]\n"
)

// Fit is the conversation a turn is handed: the newest turns whose texts fit in
// HistoryBytes, oldest first, each cut to TurnBytes (Cut).
func Fit(turns []Turn) []Turn {
	total, start := 0, 0
	for i := len(turns) - 1; i >= 0; i-- {
		n := min(len(turns[i].Text), TurnBytes)
		if total+n > HistoryBytes {
			start = i + 1
			break
		}
		total += n
	}
	out := make([]Turn, 0, len(turns)-start)
	for _, t := range turns[start:] {
		t.Text = Cut(t.Text)
		out = append(out, t)
	}
	return out
}

// Cut caps one turn at TurnBytes, keeping an answer's trailing [Tools used] block
// whole when its prose is what has to give.
func Cut(text string) string {
	if len(text) <= TurnBytes {
		return text
	}
	if idx := strings.Index(text, ToolsMarker); idx >= 0 {
		prose, tools := text[:idx], text[idx:]
		if len(tools) > ToolsBytes+len(ToolsMarker) {
			tools = tools[:ToolsBytes+len(ToolsMarker)]
		}
		if budget := TurnBytes - len(tools); budget > 64 && len(prose) > budget {
			return prose[:budget] + "\n[... message truncated ...]" + tools
		}
	}
	return text[:TurnBytes] + "\n[... message truncated ...]"
}
