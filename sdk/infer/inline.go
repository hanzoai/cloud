package infer

import "strings"

var mistralUnparsedMarker = string([]byte{91, 84, 79, 79, 76, 95, 67, 65, 76, 76, 83, 93})

var toolMarkers = []string{
	"<｜DSML｜tool_calls>",   // U+FF5C — deepseek's agentic markup
	"<｜tool▁call▁begin｜>",  // U+FF5C, U+2581 — deepseek's fenced form
	"<｜tool▁calls▁begin｜>", //   …and its plural opener
	"<tool_call>",          // qwen / hermes
	"</tool_call>",         // qwen / hermes closing
	mistralUnparsedMarker,  // mistral
	"<|python_tag|>",       // llama
	"<|tool_call>",         // gemma
	"<ernie:tool_call", "</ernie:tool_call",
	"<minimax:tool_call", "</minimax:tool_call",
	"<function_calls>", "</function_calls>",
	"<parameter ", "</parameter>",
	"<invoke ", "</invoke>",
}

func unparsed(content string) string {
	for _, marker := range toolMarkers {
		if strings.Contains(content, marker) {
			return marker
		}
	}
	return ""
}

func streamPrefix(content string) string {
	end := len(content)
	for _, marker := range toolMarkers {
		if i := strings.Index(content, marker); i >= 0 {
			end = min(end, i)
			continue
		}
		for n := min(len(content), len(marker)-1); n > 0; n-- {
			if strings.HasSuffix(content, marker[:n]) {
				end = min(end, len(content)-n)
				break
			}
		}
	}
	// The provider can drop the opening wrapper and leave only a trailing tag,
	// either on its own line or inline after prose (e.g. `Let me build it: {"name":...}`).
	envelopes := []string{`{"name":`, `{"op":`, `{"action":`}
	for _, env := range envelopes {
		if i := strings.Index(content, env); i >= 0 {
			end = min(end, i)
			continue
		}
		for n := min(len(content), len(env)-1); n > 0; n-- {
			if strings.HasSuffix(content, env[:n]) {
				end = min(end, len(content)-n)
				break
			}
		}
	}
	return content[:end]
}

// stripUnparsedTail removes leaked inline tool-call XML/JSON suffixes when
// structured tool_calls were already parsed alongside stray markup in content.
func stripUnparsedTail(content string) string {
	if unparsed(content) == "" {
		return content
	}
	return strings.TrimRight(streamPrefix(content), " \t\r\n")
}
