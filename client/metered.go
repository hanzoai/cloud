package client

import "strings"

// metered.go — the platform's own flat fees, named as x402 resources.
//
// A metered route charges a flat fee per act through the prepaid balance gate
// (cloud.Meter): an invoke, a run, a create. x402 sells the same act for the same
// fee to a caller that pays per request instead, so the fee needs a name the rail
// can price and the route can settle. Two processes read that name — the product's,
// which settles it, and x402's, which prices it — and a name each end spelled for
// itself is how a route asks for one resource while the table is keyed on another
// and the act quietly reads as free. So the table is here, in the leaf both import,
// beside ToolResource and JobResource.
//
// The PRICE is not in the table. It is each fee's own operator knob, read the way
// the balance gate reads it (cloud.ResourceFeeCents), so the two cannot quote
// different numbers for one act. The table names the knob; it never copies it.
//
// The prefix keeps the key space apart from request paths, which the Enforce
// middleware prices: no path begins "meter:", so a metered fee can never be bought
// by hitting a URL that happens to spell it.

// meteredResourcePrefix namespaces a metered fee as an x402 resource.
const meteredResourcePrefix = "meter:"

// The fee knobs, one spelling each. The product reads its own to gate and debit;
// x402 reads the same one to price the fee.
const (
	// FunctionFeeEnv prices one function invoke (kind "invoke").
	FunctionFeeEnv = "CLOUD_FUNCTION_FEE_CENTS"
	// AgentFeeEnv prices one agent run (kind "agent").
	AgentFeeEnv = "CLOUD_AGENT_FEE_CENTS"
	// ProvisionFeeEnv prices one provisioned resource, per kind
	// (CLOUD_PROVISION_FEE_CENTS_SQL, …, else CLOUD_PROVISION_FEE_CENTS).
	ProvisionFeeEnv = "CLOUD_PROVISION_FEE_CENTS"
	// AIFeeEnv prices one model inference call over x402, per kind
	// (CLOUD_AI_FEE_CENTS_CHAT, CLOUD_AI_FEE_CENTS_RESPONSES, else CLOUD_AI_FEE_CENTS).
	AIFeeEnv = "CLOUD_AI_FEE_CENTS"
)

// MeteredFee is one flat fee a metered route charges: what it is called as an x402
// resource, the route it is charged on, and the knob that prices it.
type MeteredFee struct {
	// Product names the fee: function.invoke, agent.run, provision.<kind>.
	Product string
	// Method and Path are the route the fee is charged on, in the published
	// document's spelling.
	Method string
	Path   string
	// Summary says what one payment of the fee buys.
	Summary string
	// EnvPrefix and Kind are the fee's operator knob, resolved by
	// cloud.ResourceFeeCents(EnvPrefix, Kind).
	EnvPrefix string
	Kind      string
}

// Resource is the fee's name as an x402 resource.
func (f MeteredFee) Resource() string { return meteredResourcePrefix + f.Product }

// FunctionInvoke is the flat fee for one function invocation.
var FunctionInvoke = MeteredFee{
	Product: "function.invoke", Method: "POST", Path: "/v1/function/{name}/invoke",
	Summary:   "One invocation of one of your org's functions",
	EnvPrefix: FunctionFeeEnv, Kind: "invoke",
}

// AgentRun is the flat fee for one agent run.
var AgentRun = MeteredFee{
	Product: "agent.run", Method: "POST", Path: "/v1/agent/{ref}/run",
	Summary:   "One run of one of your org's agents",
	EnvPrefix: AgentFeeEnv, Kind: "agent",
}

// ChatCompletion is the per-request fee for one chat completion paid over x402.
var ChatCompletion = MeteredFee{
	Product: "ai.chat", Method: "POST", Path: "/v1/chat/completions",
	Summary:   "One chat completion request",
	EnvPrefix: AIFeeEnv, Kind: "chat",
}

// ResponsesCall is the per-request fee for one Responses API call paid over x402.
var ResponsesCall = MeteredFee{
	Product: "ai.responses", Method: "POST", Path: "/v1/responses",
	Summary:   "One Responses API request",
	EnvPrefix: AIFeeEnv, Kind: "responses",
}

// ProvisionKinds are the kinds POST /v1/provisioning/<kind> creates, each its own
// fee because each has its own knob.
var ProvisionKinds = []string{"sql", "kv", "docdb", "datastore", "vector", "search", "s3"}

// Provision is the flat fee for creating one resource of kind. ok is false for a
// kind provisioning does not create.
func Provision(kind string) (MeteredFee, bool) {
	for _, k := range ProvisionKinds {
		if k == kind {
			return MeteredFee{
				Product: "provision." + kind, Method: "POST", Path: "/v1/provisioning/" + kind,
				Summary:   "Creating one " + kind + " resource for your org",
				EnvPrefix: ProvisionFeeEnv, Kind: kind,
			}, true
		}
	}
	return MeteredFee{}, false
}

// MeteredFees is every flat fee x402 can sell, in a stable order.
func MeteredFees() []MeteredFee {
	out := []MeteredFee{FunctionInvoke, AgentRun, ChatCompletion, ResponsesCall}
	for _, k := range ProvisionKinds {
		f, _ := Provision(k)
		out = append(out, f)
	}
	return out
}

// Metered reports whether a resource is in the metered fees' key space at all —
// a fee this table names, or one it does not and that is therefore sold by nobody.
func Metered(resource string) bool { return strings.HasPrefix(resource, meteredResourcePrefix) }

// MeteredOf reads a resource back as the fee it names. ok=false means it is not a
// metered fee at all — a tool, a job, a request path.
func MeteredOf(resource string) (MeteredFee, bool) {
	product, ok := strings.CutPrefix(resource, meteredResourcePrefix)
	if !ok {
		return MeteredFee{}, false
	}
	for _, f := range MeteredFees() {
		if f.Product == product {
			return f, true
		}
	}
	return MeteredFee{}, false
}
