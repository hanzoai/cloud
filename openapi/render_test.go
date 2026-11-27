package openapi_test

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud/sdk/spec"
)

// A string the YAML encoder leaves bare must still read back as a string. An
// address is the case the fleet publishes: 0x and 40 hex digits overflow int64,
// so the encoder's own resolver does not take it for a number and emits it
// plain — and a YAML 1.1 reader, the SDK generators among them, then reads a
// 160-bit integer where the document said a string.
func TestRenderKeepsAWideLiteralAString(t *testing.T) {
	const addr = "0x1f9840a85d5aF5bf1D1762F925BDADdC4201F984"
	d := &spec.Document{OpenAPI: "3.1.0", Paths: map[string]spec.PathItem{
		"/v1/send": {"post": {OperationID: "send", RequestBody: map[string]any{
			"content": map[string]any{"application/json": map[string]any{
				"example": map[string]any{
					"to":  addr,
					"via": []any{addr, "0b" + strings.Repeat("1", 70), map[string]any{"hop": addr}},
				},
			}},
		}}},
	}}
	y, err := render(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`to: "` + addr + `"`,
		`- "` + addr + `"`,
		`- "0b` + strings.Repeat("1", 70) + `"`,
		`- hop: "` + addr + `"`,
	} {
		if !strings.Contains(string(y), want) {
			t.Errorf("rendered YAML does not quote %s:\n%s", want, y)
		}
	}
}
