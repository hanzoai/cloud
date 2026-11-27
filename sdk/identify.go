package cloud

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/zap-proto/zip"
)

// CallerTools is this program's per-caller tool half — and stating it, rather
// than leaving it nil, is what makes the agent MCP server UNCONDITIONAL.
//
// zip mounts the server only for an app that has something to project: a typed op,
// a composed plugin's catalogue, or a per-caller Source. With all three absent it
// returns before registering the route at all (zip@v1.25.1 mcp.go:99). That is
// the right default for a program nobody interrogates, and the wrong one for
// every program built here, because the fleet's MCP server ASKS EVERY COMPOSED
// SUBSYSTEM on each tools/list (client.Ask). A subsystem whose routes are all raw
// — a reverse proxy, or a surface owned by another module — projects no typed op,
// so nothing claimed POST /mcp in its process, so the ask fell through to the
// console's terminal handler and was answered with the signpost that is correct
// only on the host: 308 → /v1/mcp, an address a child does not serve
// (webui/mcp.go:44). Thirty of the fleet's subsystems — the whole of exec, tasks,
// agent, ask, websearch, crawl, index, kms, billing, platform and twenty more —
// were reported UNREACHABLE that way while every one of them was up, healthy and
// serving its REST surface.
//
// A server whose registry is empty answers {"tools":[]}, and that is a REAL answer:
// "asked, and serves nothing" is a different fact from "could not be asked", and
// keeping those two apart is the whole of package fleet. Its hanzo.ai/unavailable
// list means nothing while a healthy subsystem has no way to say the first one.
//
// nil in, empty out. A program that declares no Plugin.Offer still HAS a
// per-caller half; it simply holds no tools. It is consulted once per tools/list
// that names an org, answers nothing, and zip then returns the same pre-rendered
// bytes it always did — the memcpy that makes tools/list free is untouched (zip
// listTools: len(mine) == 0 ⇒ the build-time array, verbatim).
func CallerTools(declared zip.Source) zip.Source {
	if declared != nil {
		return declared
	}
	return noCallerTools{}
}

// noCallerTools is the per-caller half of a program that declares none: no tools
// exist because of who is asking, and a name nobody projected is nobody's.
//
// Its Call is reached only for a name the build-time catalogue did not claim, and
// it answers with the same sentence zip's own miss does — the fleet's MCP server
// never routes one here (it refuses an unlisted name itself, client/mcp.go), so
// this is the reply to a client that guessed.
type noCallerTools struct{}

func (noCallerTools) Tools(context.Context) []map[string]any { return nil }

func (noCallerTools) Call(_ context.Context, name string, _ json.RawMessage) (any, error) {
	return nil, fmt.Errorf("unknown tool: %s", name)
}

// Identify gives an app a trustworthy answer to who is calling, and makes that
// answer reachable from every route beneath it. App does this for every program,
// which is the only reason it can no longer be skipped.
//
// The two halves are one function because each is wrong without the other, and
// wrong in a way nothing reports. IdentityMiddleware deletes the authority
// headers a client sent and re-mints them from a verified IAM token, so it runs
// first: what it produces is the only principal in the process anyone may trust.
// The enrichment then parks that principal on the request context, which is the
// only path by which a typed op reaches it — a zip.Get[In, Out] handler receives a
// context and its decoded In and nothing else. Reversed, it parks whatever the
// caller claimed for itself. Installed alone, the boundary validates a caller and
// then every typed op reads an empty org and refuses that same caller, which
// reaches the wire as a 403 from a service behaving exactly as built.
//
// That last failure is the reason this is a function rather than two lines of
// advice. It is what the o11y binary did while assembling its own app, and its
// subsystem then compensated from inside its own Mount, on a group node that
// owned no routes — a program zip refuses to compose, which is the outage.
func Identify(app *zip.App, cfg *Config) { IdentifyWith(app, BoundaryOf(cfg)) }

// IdentifyWith is Identify over a boundary already built, so App can hand the same one
// to the edge.
func IdentifyWith(app *zip.App, v *Boundary) {
	// The identity trust boundary. HIP-0519 says identity is verified once, at the
	// edge, and that is the shape to reach. It rests on ONE assumption: the gateway
	// is the only ingress. That assumption does not hold here yet, and the estate's
	// own red-team probe says so — with this middleware removed, a request carrying
	// a forged X-Org-Id, X-User-Id and X-User-IsAdmin reads another org's secret
	// VALUE from the in-cluster KMS listener:
	//
	//	PROBE (b) forged org + forged X-User-Id + IsAdmin → 200 {"value":"…"}
	//
	// So this stays until service listeners are unreachable except through the
	// gateway. Removing it is a network-policy change first and a code change
	// second, and doing the code half alone is a cross-tenant secret read.
	// red_orgscope_isolation_test.go and TestAudit_AnonRequestNotAttributedToForgedOrg
	// fail the moment it is dropped; they are the gate on that work, not obstacles
	// to it.
	app.Use(SanitizeIdentity(v))

	// Besides the validated org, this carries the request a proxying subsystem
	// forwards identity from and the slot a creator writes 201 or 202 into. It must
	// precede every typed route, because fiber runs middleware in registration
	// order and one installed after its leaves never runs. A subsystem whose routes
	// are spread across several top-level nouns owns no single prefix to hang it
	// on, which is the other reason it belongs to whoever composes the app. See
	// typed.go.
	app.Use(Bridge())
}
