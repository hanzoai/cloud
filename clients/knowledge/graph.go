package knowledge

import (
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/framework"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// graph.go serves GET /v1/kb/graph: the org's knowledge as a node/edge graph shaped
// for a force-directed renderer. Nodes are kb-page / kb-memory / kb-source
// documents (plus the connector and dangling-link endpoints edges reach); edges are
// the parent tree (kb-page.parent), the wikilinks (kb-link edges resolved to a page
// by value), and connector provenance (a kb-source to its kb-connector). Everything
// is org-scoped through principal.Org and optionally narrowed to a project.
//
// Wikilink targets are resolved HERE, by matching a kb-link's target_title against
// the current pages' titles/slugs — so a rename or trash of a target never needs an
// edge rewrite, and a target that matches no page renders as a distinct "unresolved"
// node (an honest dangling link).

// Graph bounds keep one response renderable: a per-doctype node cap and an edge cap.
const (
	graphNodeLimit = 2000
	graphEdgeLimit = 10000
)

type graphNode struct {
	ID      string `json:"id"`             // "<doctype>:<name>" — globally unique, click-to-open key
	Type    string `json:"type"`           // kb-page | kb-memory | kb-source | kb-connector | unresolved
	Title   string `json:"title"`          // display label
	Name    string `json:"name,omitempty"` // the document name (empty for synthetic nodes)
	Project string `json:"project,omitempty"`
}

type graphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"` // parent | link | provenance
}

// graph fetches the org's knowledge documents and shapes them into the graph.
func graph(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("valid principal required")
	}
	project := strings.TrimSpace(c.Query("project"))
	scope := map[string]string{}
	if project != "" {
		scope["project"] = project
	}
	ctx := c.Context()

	pages, err := framework.Search(ctx, org, DTPage, scope, graphNodeLimit)
	if err != nil {
		return graphUnavailable(s, c, org, err)
	}
	memories, err := framework.Search(ctx, org, DTMemory, scope, graphNodeLimit)
	if err != nil {
		return graphUnavailable(s, c, org, err)
	}
	sources, err := framework.Search(ctx, org, DTSource, scope, graphNodeLimit)
	if err != nil {
		return graphUnavailable(s, c, org, err)
	}
	// Connectors carry no project field, so they are fetched org-wide; only those a
	// provenance edge reaches are emitted as nodes.
	connectors, err := framework.Search(ctx, org, DTConnector, nil, 100)
	if err != nil {
		return graphUnavailable(s, c, org, err)
	}
	// kb-link edges: fetched org-wide, then filtered to sources that are in the
	// (project-scoped) page set during the build.
	links, err := framework.Search(ctx, org, DTLink, nil, graphEdgeLimit)
	if err != nil {
		return graphUnavailable(s, c, org, err)
	}

	nodes, edges := buildGraph(pages, memories, sources, connectors, links)
	return c.JSON(http.StatusOK, map[string]any{"nodes": nodes, "edges": edges})
}

// graphUnavailable degrades a store outage to an honest empty graph (never a 5xx),
// mirroring the search handler's fail-honest contract for the RAG path.
func graphUnavailable(s *cloud.Service[state], c *zip.Ctx, org string, err error) error {
	s.Log.Warn("kb graph failed", "org", org, "err", err)
	return c.JSON(http.StatusOK, map[string]any{"nodes": []graphNode{}, "edges": []graphEdge{}, "degraded": true})
}

// buildGraph assembles the node/edge graph from the org's documents. It is pure
// (no I/O) so the resolution + dangling-link + provenance logic is unit-testable.
func buildGraph(pages, memories, sources, connectors, links []framework.Document) ([]graphNode, []graphEdge) {
	nodes := make([]graphNode, 0, len(pages)+len(memories)+len(sources))
	nodeSet := map[string]bool{}
	addNode := func(n graphNode) {
		if nodeSet[n.ID] {
			return
		}
		nodeSet[n.ID] = true
		nodes = append(nodes, n)
	}

	// pageByName gives the parent-tree lookup; titleIndex resolves a wikilink target
	// (by title first, then slug) to a page node id.
	pageByName := map[string]bool{}
	titleIndex := map[string]string{} // lower(title|slug) → page node id
	for _, p := range pages {
		id := nodeID(DTPage, p.Name)
		pageByName[p.Name] = true
		title := docTitle(p)
		addNode(graphNode{ID: id, Type: DTPage, Title: title, Name: p.Name, Project: str(p.Data["project"])})
		indexPut(titleIndex, strings.ToLower(p.Name), id)
		if t := strings.ToLower(title); t != "" {
			indexPut(titleIndex, t, id)
		}
	}
	for _, m := range memories {
		addNode(graphNode{ID: nodeID(DTMemory, m.Name), Type: DTMemory, Title: docTitle(m), Name: m.Name, Project: str(m.Data["project"])})
	}
	sourceProvider := map[string]string{} // source node id → provider
	for _, sc := range sources {
		id := nodeID(DTSource, sc.Name)
		addNode(graphNode{ID: id, Type: DTSource, Title: docTitle(sc), Name: sc.Name, Project: str(sc.Data["project"])})
		if pr := str(sc.Data["provider"]); pr != "" {
			sourceProvider[id] = pr
		}
	}
	connectorByProvider := map[string]framework.Document{}
	for _, cn := range connectors {
		connectorByProvider[str(cn.Data["provider"])] = cn
	}

	edges := make([]graphEdge, 0, len(pages)+len(links))
	edgeSet := map[string]bool{}
	addEdge := func(e graphEdge) {
		k := e.From + "\x00" + e.To + "\x00" + e.Kind
		if e.From == e.To || edgeSet[k] {
			return
		}
		edgeSet[k] = true
		edges = append(edges, e)
	}

	// Parent tree.
	for _, p := range pages {
		parent := str(p.Data["parent"])
		if parent != "" && pageByName[parent] {
			addEdge(graphEdge{From: nodeID(DTPage, p.Name), To: nodeID(DTPage, parent), Kind: "parent"})
		}
	}

	// Wikilinks: resolve target_title to a page; a miss becomes an "unresolved" node.
	for _, l := range links {
		src := str(l.Data["source"])
		if !pageByName[src] {
			continue // source not in the current (project-scoped) page set
		}
		title := str(l.Data["target_title"])
		if strings.TrimSpace(title) == "" {
			continue
		}
		from := nodeID(DTPage, src)
		if to, ok := titleIndex[strings.ToLower(title)]; ok {
			addEdge(graphEdge{From: from, To: to, Kind: "link"})
			continue
		}
		to := "unresolved:" + strings.ToLower(title)
		addNode(graphNode{ID: to, Type: "unresolved", Title: title})
		addEdge(graphEdge{From: from, To: to, Kind: "link"})
	}

	// Connector provenance: a source → the kb-connector for its provider.
	for id, provider := range sourceProvider {
		cn, ok := connectorByProvider[provider]
		if !ok {
			continue
		}
		cid := nodeID(DTConnector, cn.Name)
		addNode(graphNode{ID: cid, Type: DTConnector, Title: provider, Name: cn.Name})
		addEdge(graphEdge{From: id, To: cid, Kind: "provenance"})
	}

	return nodes, edges
}

// nodeID is the globally-unique node identifier: "<doctype>:<name>". A page and a
// memory that share a name never collide, and the console splits on the first ":"
// to open the underlying document.
func nodeID(doctype, name string) string { return doctype + ":" + name }

// indexPut records the first mapping for a key (title collisions resolve to the
// earliest page, deterministically).
func indexPut(m map[string]string, key, id string) {
	if key == "" {
		return
	}
	if _, ok := m[key]; !ok {
		m[key] = id
	}
}

// docTitle returns a document's display title, falling back to its name when the
// title field is empty.
func docTitle(d framework.Document) string {
	if t := str(d.Data["title"]); t != "" {
		return t
	}
	return d.Name
}
