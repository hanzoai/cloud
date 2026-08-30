package market

// chain.go — the roster, and where each chain's two upstreams are.
//
// ONE registry serves every chain, and the chain is a path element rather than a
// hostname or a deployment. `GET {explore}/v1/explorer/admin/chains` enumerates
// them with the contracts deployed on each, so what exists is asked rather than
// compiled in — a chain added there appears here without a release.

import (
	"context"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/internal/environ"
)

// explore is the indexer estate for a deployment environment.
//
// It is keyed on the env the process already knows (cloud.Deps.Env), so there is no
// second place to say which network this is. Devnet is ABSENT rather than empty:
// no indexer has been deployed for it, every path on api-explore.lux-dev.network
// answers 404, and the honest report of that is Unconfigured — which the missing
// key produces without a branch.
var explore = map[string]string{
	"mainnet": "https://api-explore.lux.network",
	"testnet": "https://api-explore.lux-test.network",
}

// base is this deployment's registry root, or empty where none is deployed.
// LUX_EXPLORE repoints it — at a fork, a staging estate, or a test server — because
// an indexer holds the history of the chain it ingested and of no other.
func base(env string) string {
	return strings.TrimRight(environ.Or("LUX_EXPLORE", explore[env]), "/")
}

// Market is one chain considered as a place to trade: what the registry says is
// deployed on it, where its two upstreams are, and what its market maker amounts to.
//
// A CHAIN WITH NO MARKET MAKER IS STILL A ROW. "Nothing is deployed here" is an
// answer, and being able to give it — rather than omitting the chain, or reporting
// it as unread — is most of why this type exists.
//
// It is named for the capability's own noun rather than for the chain, because the
// fleet's schema names are one flat namespace and `Chain` in it already means the
// three-field value web3 publishes. The caller-facing words stay "chain": the roster
// field is `chains` and the parameter is `chain`.
type Market struct {
	// Slug is the chain's word in every indexer path — `cchain`, `zoo`. It is the
	// value a caller passes back as `chain`, and it is NOT the chain id: `96369`,
	// `C` and `c-chain` all answer 404 in that position.
	Slug string `json:"slug"`
	Name string `json:"name"`
	// ID is the EVM chain id, which is what a wallet must agree with.
	ID   int    `json:"id"`
	Coin string `json:"coin"`

	// RPC is the chain's PUBLIC JSON-RPC, empty where the registry names only a
	// route this process happens to have. The registry's own `rpc` field is the
	// INDEXER's route to the node and is sometimes inside its cluster — plain HTTP
	// on a `.svc.cluster.local` name — which is reachable from the indexer, from
	// nothing else, and from no browser. Publishing that as the chain's endpoint
	// hands every caller an address that cannot answer them. See [endpoint].
	RPC string `json:"rpc,omitempty"`

	// Amm reports whether an automated market maker is deployed on this chain,
	// which is the registry's factory addresses being present and not the indexer
	// having rows. The two disagree in exactly the interesting case: a chain with a
	// factory and nothing traded yet is a live venue with no history, and a chain
	// with neither has no venue at all.
	Amm bool `json:"amm"`

	// Factory is the AMM's factory contracts, by generation, omitted where none is
	// deployed. Addresses come from the registry because that is what the indexer
	// itself ingested from; anything else is a second copy free to drift.
	Factory map[string]string `json:"factory,omitempty"`

	// Graph is where this chain's indexer answers, empty where it has none.
	Graph string `json:"graph,omitempty"`

	// Reach is how far the read of this chain's FIGURES got — its own, so one
	// indexer being down describes one row and leaves the others to answer.
	Reach Reach `json:"reach"`

	// Figures is the chain's whole market maker, and Day its most recent active one.
	// Both are absent unless Reach says Read, so a caller cannot mistake a zero this
	// process never received for one the indexer computed. Day is also absent on a
	// chain that has never traded — which reach reports as Read, so the two absences
	// are told apart by the state beside them and never by the gap itself.
	Figures *Figures `json:"figures,omitempty"`
	Day     *Day     `json:"day,omitempty"`
}

// row is the registry's own shape. Only the fields this reads are declared; the
// registry carries treasury, genesis supply, router and quoter addresses that no
// operation here answers, and declaring them would publish a surface nothing serves.
type row struct {
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	ID        int    `json:"chain_id"`
	Type      string `json:"type"`
	Coin      string `json:"coin"`
	RPC       string `json:"rpc"`
	PublicRPC string `json:"public_rpc"`
	FactoryV2 string `json:"factory_v2"`
	FactoryV3 string `json:"factory_v3"`
	Enabled   bool   `json:"enabled"`
	Graph     struct {
		Enabled   bool `json:"enabled"`
		Subgraphs []struct {
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		} `json:"subgraphs"`
	} `json:"graph"`
}

// mount is the subgraph these operations read.
//
// It is `amm` and only `amm`. The same estate serves a `dex` subgraph whose root
// fields include `orders`, and this API does not read it: a list that aggregates
// open offers is an excluded activity, and the way to not build one is to not fetch
// it. Nothing here is a switch away from serving a book.
const mount = "amm"

// endpoint is the chain's public JSON-RPC, or empty.
//
// The registry gives two, and which one is public is not a fact about the field
// name: `public_rpc` is set for the primary chain and absent for the rest, whose
// `rpc` IS public. So the rule is about the ADDRESS — a cluster-internal name is
// never handed to a caller, whoever named it.
func endpoint(r row) string {
	if r.PublicRPC != "" {
		return r.PublicRPC
	}
	if internal(r.RPC) {
		return ""
	}
	return r.RPC
}

// internal reports whether a URL names somewhere only its own cluster can reach.
func internal(url string) bool {
	return strings.Contains(url, ".svc.cluster.local") ||
		strings.Contains(url, ".svc") ||
		strings.Contains(url, ".local:") ||
		strings.HasSuffix(url, ".local")
}

// graphAt is where this chain's AMM indexer answers, or empty where it has none.
// A chain whose registry row switches the graph off, or carries no `amm` subgraph,
// has no indexer to ask and says Unconfigured rather than failing a request to a
// URL nobody serves.
func graphAt(root string, r row) string {
	if root == "" || !r.Graph.Enabled {
		return ""
	}
	for _, g := range r.Graph.Subgraphs {
		if g.Name == mount && g.Enabled {
			return root + "/v1/graph/" + r.Slug + "/" + mount + "/graphql"
		}
	}
	return ""
}

// market turns a registry row into the roster entry, with no figures read yet.
func market(root string, r row) Market {
	c := Market{
		Slug: r.Slug, Name: r.Name, ID: r.ID, Coin: r.Coin,
		RPC: endpoint(r), Graph: graphAt(root, r),
	}
	if r.FactoryV2 != "" || r.FactoryV3 != "" {
		c.Amm = true
		c.Factory = map[string]string{}
		if r.FactoryV2 != "" {
			c.Factory["v2"] = r.FactoryV2
		}
		if r.FactoryV3 != "" {
			c.Factory["v3"] = r.FactoryV3
		}
	}
	return c
}

// roster reads the registry. The error is the transport's, and the caller turns it
// into a Reach — this function does not decide how a failure reads.
func (s *state) roster(ctx context.Context) ([]row, error) {
	var out struct {
		Chains []row `json:"chains"`
	}
	if err := s.getJSON(ctx, s.base+"/v1/explorer/admin/chains", &out); err != nil {
		return nil, err
	}
	live := make([]row, 0, len(out.Chains))
	for _, r := range out.Chains {
		// A disabled row is a chain the operator has taken out of service, and an
		// answer about it would describe a venue nobody is serving. Type is checked
		// because every read below speaks EVM and a row that is not one would be
		// asked questions it has no answers to.
		if r.Enabled && r.Type == "evm" {
			live = append(live, r)
		}
	}
	return live, nil
}

// getJSON issues one read and decodes it. The registry is plain REST; the same
// unreachable-versus-refused reasoning as [state.ask] does not arise, because a
// REST server that declines says so in its status line.
func (s *state) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return decode(resp, out)
}
