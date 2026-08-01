package risk

// surface_test.go proves the FIVE surfaces one typed declaration is supposed to
// produce actually carry these ops — read off the COMMITTED artifacts, not off
// the router the other tests mount.
//
// The distinction matters and is the exact failure that lost plugin/ingress
// eight published paths: a router can serve a route while the generated subset,
// the woven fleet document and therefore every SDK know nothing about it. Two
// derived things agreeing with each other is not evidence. So this reads
// plugin/risk/openapi.json (the SDK's source), plugin/risk/mcp.json (the tool
// list), the fleet openapi.yaml (what the SDK repos pull), and derives the CLI
// from the spec with the same function the CLI itself uses.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// theOps are the operationIds this app promises. One list, checked against four
// artifacts, so a rename shows up as four failures rather than as a silently
// missing SDK method.
var theOps = []string{
	"riskDecide", "riskDecisions", "riskDecisionDetail", "riskLabel",
	"riskSubjectState", "riskActivity", "riskSimulate",
	"riskRules", "riskCreateRule", "riskUpdateRule", "riskDeleteRule",
	"riskLists", "riskCreateList", "riskAddListEntries", "riskRemoveListEntry",
	"riskSuppressions", "riskSuppress", "riskUnsuppress",
	"riskControls", "riskSetControl", "riskReleaseControl",
	"riskDictionary", "riskMode", "riskSetMode",
	"mlScore", "mlTrain", "mlState", "mlSetAppetite", "mlFeatures",
	"mlSearch", "mlSearchResult", "mlSnapshot", "mlRestore",
	"mlFit", "mlFits", "mlFitDetail", "mlCancelFit", "mlSetFitRole", "mlFitTally",
	"mlSchedule", "mlSetSchedule", "mlDrift",
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Dir(filepath.Dir(wd)) // apps/risk -> repo root
}

func readSubset(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "plugin", "risk", "openapi.json"))
	if err != nil {
		t.Fatalf("plugin/risk/openapi.json: %v\nRun: make -C apps/risk describe", err)
	}
	return b
}

// TestSubsetCarriesEveryOp reads the app's OWN published document — the one the
// fleet weave consumes and every generated SDK ultimately comes from.
func TestSubsetCarriesEveryOp(t *testing.T) {
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
			Summary     string `json:"summary"`
			Description string `json:"description"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(readSubset(t), &doc); err != nil {
		t.Fatalf("parse subset: %v", err)
	}
	got := map[string]string{}
	for _, methods := range doc.Paths {
		for method, op := range methods {
			if op.OperationID == "" {
				continue
			}
			got[op.OperationID] = method
			if strings.TrimSpace(op.Summary) == "" {
				t.Errorf("%s publishes no summary — the CLI derives its one-line help from it", op.OperationID)
			}
			if strings.TrimSpace(op.Description) == "" {
				t.Errorf("%s publishes no description — an MCP client reads it to choose the tool", op.OperationID)
			}
		}
	}
	for _, id := range theOps {
		if _, ok := got[id]; !ok {
			t.Errorf("%s is missing from plugin/risk/openapi.json — no SDK, no CLI command, no MCP tool", id)
		}
	}
}

// TestFleetDocumentCarriesEveryPath reads the WOVEN document. The subset can be
// right while the weave is stale, and the weave is what the SDK repos pull.
func TestFleetDocumentCarriesEveryPath(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "openapi.yaml"))
	if err != nil {
		t.Fatalf("openapi.yaml: %v", err)
	}
	body := string(b)
	for _, p := range []string{
		"/v1/risk/decide", "/v1/risk/decisions", "/v1/risk/decisions/{id}",
		"/v1/risk/decisions/{id}/label", "/v1/risk/subjects/{kind}/{id}",
		"/v1/risk/activity", "/v1/risk/simulate", "/v1/risk/rules",
		"/v1/risk/rules/{id}", "/v1/risk/lists", "/v1/risk/lists/{name}/entries",
		"/v1/risk/lists/{name}/entries/{value}", "/v1/risk/suppressions",
		"/v1/risk/suppressions/{id}", "/v1/risk/controls", "/v1/risk/controls/{id}",
		"/v1/risk/dictionary", "/v1/risk/mode", "/v1/risk/health",
		"/v1/ml/score", "/v1/ml/train", "/v1/ml/state", "/v1/ml/state/appetite",
		"/v1/ml/features", "/v1/ml/search", "/v1/ml/search/{id}",
		"/v1/ml/snapshot", "/v1/ml/restore",
		"/v1/ml/fits", "/v1/ml/fits/{id}", "/v1/ml/fits/{id}/cancel",
		"/v1/ml/fits/{id}/role", "/v1/ml/fits/{id}/tally",
		"/v1/ml/schedule", "/v1/ml/drift",
	} {
		if !strings.Contains(body, "\n  "+p+":") {
			t.Errorf("%s is absent from the woven openapi.yaml — the SDK repos pull this file, so no "+
				"generated client can reach it. Run: make describe", p)
		}
	}
	// The ml app's own paths must still be there: risk claims LEAVES under
	// /v1/ml, it does not take the stem.
	for _, p := range []string{"/v1/ml/models", "/v1/ml/health", "/v1/train/jobs"} {
		if !strings.Contains(body, "\n  "+p+":") {
			t.Errorf("%s disappeared from the fleet document — the risk row swallowed an ml route", p)
		}
	}
}

// TestCLIDerivesEveryCommand runs the SAME derivation the CLI runs. A published
// operation with no derivable command is an operation no `hanzo` invocation can
// reach.
func TestCLIDerivesEveryCommand(t *testing.T) {
	cmds, err := zip.CommandsFromSpec(readSubset(t))
	if err != nil {
		t.Fatalf("CommandsFromSpec: %v", err)
	}
	byID := map[string]zip.Command{}
	for _, c := range cmds {
		byID[c.OperationID] = c
	}
	for _, id := range theOps {
		c, ok := byID[id]
		if !ok {
			t.Errorf("%s derives no CLI command", id)
			continue
		}
		if c.Name == "" || c.Service == "" {
			t.Errorf("%s derives a nameless command (%q %q)", id, c.Service, c.Name)
		}
		if strings.TrimSpace(c.Summary) == "" {
			t.Errorf("%s derives a command with no help text", id)
		}
	}
	// Path parameters must reach the command as ARGS, or the command cannot
	// address the record it names.
	if c := byID["riskDecisionDetail"]; len(c.Args) == 0 {
		t.Error("riskDecisionDetail derives no positional argument for {id}")
	}
	if c := byID["riskSubjectState"]; len(c.Args) < 2 {
		t.Errorf("riskSubjectState derives %d args, want 2 for {kind} and {id}", len(c.Args))
	}
}

// TestMCPToolsCarryEveryTypedOp reads the published tool list. Every typed op
// becomes a tool whose inputSchema is the In type's schema and whose call runs
// the same handler — so a missing tool is a capability an agent cannot use.
func TestMCPToolsCarryEveryTypedOp(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "plugin", "risk", "mcp.json"))
	if err != nil {
		t.Fatalf("plugin/risk/mcp.json: %v\nRun: make -C apps/risk describe", err)
	}
	var tools []struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		InputSchema map[string]any `json:"inputSchema"`
	}
	if err := json.Unmarshal(b, &tools); err != nil {
		t.Fatalf("parse mcp.json: %v", err)
	}
	got := map[string]bool{}
	for _, tool := range tools {
		got[tool.Name] = true
		if strings.TrimSpace(tool.Description) == "" {
			t.Errorf("MCP tool %s has no description — a model cannot choose it", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Errorf("MCP tool %s has no input schema", tool.Name)
		}
	}
	for _, id := range theOps {
		if !got[id] {
			t.Errorf("%s is not an MCP tool", id)
		}
	}
	if len(tools) != len(theOps) {
		var extra []string
		for _, tool := range tools {
			if !containsString(theOps, tool.Name) {
				extra = append(extra, tool.Name)
			}
		}
		sort.Strings(extra)
		t.Errorf("%d MCP tools published, %d promised; unlisted: %s",
			len(tools), len(theOps), strings.Join(extra, ", "))
	}
}
