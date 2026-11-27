package tools

import "strings"

// A tool's arguments are the JSON object the model emitted, decoded into the
// `args map[string]any` that Provider.Dispatch declares. The readers live beside
// that declaration so every provider reads the same JSON the same way.

// Str reads a field that names something — a status, a board, a slug — trimmed,
// because a name with a trailing space is the same name.
func Str(args map[string]any, k string) string {
	return strings.TrimSpace(Text(args, k))
}

// Text reads a field that is content — a prompt, a program, a message — verbatim.
// Leading whitespace is significant in a program, so content is never trimmed.
func Text(args map[string]any, k string) string {
	s, _ := args[k].(string)
	return s
}

// Int reads a number. encoding/json decodes every JSON number as a float64, so an
// int assertion alone answers 0 for every value a model sends.
func Int(args map[string]any, k string) int {
	switch v := args[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}
