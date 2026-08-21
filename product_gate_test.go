package cloud_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ONE NAME, ONE PRODUCT, ONE PLACE.
//
// cloud/apps/<name> is plugin-mode wiring for github.com/hanzoai/<name>: the
// product owns its functionality and ships its own standalone daemon, and the
// app MOUNTS it into the cloud process — the way apps/iam mounts hanzoai/iam.
// When that boundary is only a convention, functionality accretes on the cloud
// side until the app IS the product and the OSS repo is a shell. The root's
// IAM history is the worked example, and apps/team/token — a whole bearer
// authority one directory over from the guards that deleted its twin — is what
// the drift looks like when nobody counts.
//
// So the boundary counts itself. Every app either mounts its product or holds
// a pin below naming which kind of debt it is, and the lists only ratchet
// DOWN: wiring an app means deleting its pin, and a new app either arrives
// mounted or arrives with a pin somebody wrote on purpose.
//
// The buckets record what existed in the working set when each pin was
// written. A same-named repo is not proof of the same product, so an entry is
// re-verified at its migration, never trusted from here.

// mismatched apps mount a real product that carries ANOTHER name. The rule
// wants the names equal, so each entry is a rename decision owed at migration
// — one of the two names gives way — not a settled state.
var mismatched = map[string]string{
	"deploy": "cd", // gitops delivery — the product repo is hanzoai/cd (Hanzo CD)
}

// unwired apps have an upstream repo to mount and do not mount it yet: the
// functionality is (at least partly) duplicated on the cloud side. The worst
// bucket, and the front of the migration worklist.
var unwired = []string{
	"admin", "billing", "bots", "datastore", "dns",
	"engine", "eval", "event", "functions", "gateway", "git", "idv", "ingress", "kms",
	"marketing", "ml", "mpc", "networks", "platform", "research", "skills",
	"social", "team", "usage", "visor", "world",
}

// unextracted apps have no upstream repo at all: the functionality lives only
// here. At migration each either becomes hanzoai/<name> with the app reduced
// to wiring, or proves it is cloud's own machinery that belongs below apps/ —
// either way the pin comes off.
var unextracted = []string{
	"admission", "ads", "affiliates", "agents", "allowance", "answer", "ask",
	"auditlog", "authors", "auto", "benchmark", "blueprint", "books",
	"campaigns", "catalog", "catalogsync", "channels", "cloudflare", "cms",
	"code", "coding", "company", "compliance", "connectorruntime", "content",
	"controlplane", "crawl", "crm", "cron", "datasets", "graph",
	"destinations", "domain", "entitlements", "erp", "esign", "exec",
	"experiments", "explorer", "finance", "fleet", "flow", "goja", "guide",
	"help", "index", "integrations", "k8s", "knowledge",
	// kv is the key-value door. The STORE is hanzoai/pubsub's — one embedded
	// JetStream node, reached through apps/pubsub — so nothing here duplicates a
	// product; what lives here is the tenant-scoped door onto it, and no
	// hanzoai/kv exists to mount. Of the two branches this bucket names, the
	// second is the likelier: a door that qualifies bucket names by org and
	// translates the plane's refusals is cloud's own machinery, not a forkable
	// product with a daemon of its own.
	"kv",
	"label",
	"leaderboard", "legal", "links", "lsp", "marketplace", "meet", "membership",
	"metering", "mq",
	// nodes is the machine control plane, split out of bots (HIP-0139 §7.2). It
	// carries the pin bots carried for it: no hanzoai/nodes exists, and what lives
	// here — the presence registry over Hanzo KV, the socket, and the policy that
	// decides what a command may be — is the whole of the functionality. The
	// decision it owes is this bucket's first branch, because an agent runtime that
	// connects your machines and runs commands on them is a product somebody would
	// fork, not cloud's own machinery.
	"nodes",
	"payout", "plan", "plugin", "prefs", "principal",
	"projects", "prompts", "provisioning", "reference",
	"referrals", "registry", "risk", "rollingcap", "s3", "s3admin",
	"samples", "sandbox", "sbom", "search", "security",
	// seo is the search-visibility surface: a typed proxy onto a measurement
	// vendor, metered at that vendor's own published prices. It imports only
	// hanzoai/cloud and no hanzoai/seo exists to mount, so the functionality lives
	// here and nowhere else — which is what this bucket means. The decision this pin
	// owes leans toward the first branch: a keyword-and-backlink API is a product
	// somebody would fork, not cloud's own machinery.
	"seo",
	"settings", "share",
	"sites",
	"sync",
	// taxonomy is the product catalogue's own shape — the categories, tags and
	// display order the console used to hold as a TypeScript array. It imports only
	// hanzoai/cloud, and no hanzoai/taxonomy exists to mount, so the functionality
	// lives here and nowhere else. The decision this pin owes is likelier the second
	// branch than the first: what a console renders its own navigation from reads
	// like cloud's own machinery rather than a forkable product with a daemon.
	"taxonomy",
	// tel is numbers, calls and messages: it holds a carrier relationship and the
	// org-scoped records of what was bought, dialled and sent. It imports only
	// hanzoai/cloud, and no hanzoai/tel exists to mount — so the functionality
	// lives here and nowhere else, which is what this bucket means.
	"tel",
	"templates", "tenant", "tools", "todo", "translate",
	"treasury", "validators", "wallets", "webhooks", "websearch",
	// web3 is the chain-access surface. It REPLACES the api/ half of
	// hanzoai/bootnode rather than extracting from it — that half was Python
	// serving four routes, and this is the richer router bootnode's own api-go/
	// already sketched, finished here. So bootnode is not an upstream to mount:
	// what it held is retired, and no hanzoai/web3 exists. The decision this pin
	// owes is the one this bucket names — become hanzoai/web3, or show the chain
	// surface is cloud's own machinery and belongs below apps/.
	"web3",
	"x402",
}

func TestEveryAppMountsItsProduct(t *testing.T) {
	files := appFiles(t)

	pinned := map[string]string{}
	pin := func(name, list string) {
		if prev, dup := pinned[name]; dup {
			t.Errorf("apps/%s is pinned twice (%s and %s) — one app, one pin", name, prev, list)
			return
		}
		pinned[name] = list
	}
	for _, n := range unwired {
		pin(n, "unwired")
	}
	for _, n := range unextracted {
		pin(n, "unextracted")
	}
	for n := range mismatched {
		pin(n, "mismatched")
	}

	tally := map[string]int{}
	for name, ff := range files {
		imports := map[string]bool{}
		for _, f := range ff {
			for _, imp := range fileImports(t, f) {
				imports[imp] = true
			}
		}
		mountsSelf := mountsProduct(imports, name)
		list, isPinned := pinned[name]

		switch {
		case mountsSelf && isPinned:
			t.Errorf("apps/%s mounts hanzoai/%s — remove it from %s so the pins keep describing "+
				"the code that exists; this list only ratchets down", name, name, list)
		case mountsSelf:
			tally["mounted"]++
		case !isPinned:
			t.Errorf("apps/%s mounts no product and holds no pin.\n"+
				"cloud/apps/<name> is plugin-mode wiring for github.com/hanzoai/<name>: build the "+
				"functionality in the product repo — which keeps its own standalone daemon, so the OSS "+
				"product stays forkable — and mount it here. If the product repo does not exist yet, "+
				"create it; pinning the app instead (unwired / unextracted / mismatched, whichever is "+
				"true) is a decision made on purpose, and the migration owes its removal.", name)
		case list == "mismatched":
			product := mismatched[name]
			if !mountsProduct(imports, product) {
				t.Errorf("apps/%s is pinned as mounting hanzoai/%s and does not import it — "+
					"fix the pin or the app", name, product)
			} else {
				tally[list]++
			}
		default:
			tally[list]++
		}
	}

	for name := range pinned {
		if _, exists := files[name]; !exists {
			t.Errorf("pin for apps/%s, which does not exist — delete the entry", name)
		}
	}

	var parts []string
	for _, k := range []string{"mounted", "mismatched", "unwired", "unextracted"} {
		parts = append(parts, k+" "+strconv.Itoa(tally[k]))
	}
	t.Logf("apps %d: %s", len(files), strings.Join(parts, ", "))
}

// mountsProduct reports whether the import set reaches github.com/hanzoai/<product>.
func mountsProduct(imports map[string]bool, product string) bool {
	root := "github.com/hanzoai/" + product
	if imports[root] {
		return true
	}
	for imp := range imports {
		if strings.HasPrefix(imp, root+"/") {
			return true
		}
	}
	return false
}

// appFiles maps each app (immediate directory of apps/) to its non-test Go
// files, recursively, skipping testdata and hidden/underscore directories.
func appFiles(t *testing.T) map[string][]string {
	t.Helper()
	entries, err := os.ReadDir("apps")
	if err != nil {
		t.Fatalf("read apps/: %v", err)
	}
	files := map[string][]string{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		files[name] = nil
		err := filepath.WalkDir(filepath.Join("apps", name), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "_") {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
				files[name] = append(files[name], filepath.ToSlash(path))
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk apps/%s: %v", name, err)
		}
		sort.Strings(files[name])
	}
	return files
}

// fileImports parses just the import block of one file.
func fileImports(t *testing.T, path string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Errorf("parse %s: %v", path, err)
		return nil
	}
	var out []string
	for _, spec := range f.Imports {
		imp, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Errorf("%s: import %s: %v", path, spec.Path.Value, err)
			continue
		}
		out = append(out, imp)
	}
	return out
}
