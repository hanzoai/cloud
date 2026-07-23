package venue

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/cek"
	"github.com/hanzoai/cloud/clients/fleet"
	"github.com/hanzoai/cloud/clients/kms"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

// ── harness ─────────────────────────────────────────────────────────────────

// The per-org KMS store rides the cek data plane, which fail-closes without a
// master key on encryption-capable builds; supply one process-wide (mirrors
// clients/integrations, clients/git).
func TestMain(m *testing.M) {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic(err)
	}
	cek.SetMasterKey(k)
	os.Exit(m.Run())
}

func newKMS(t *testing.T) *kms.Client {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	kc, err := kms.New(kms.Config{
		DataDir:      t.TempDir(),
		MasterKeyB64: base64.StdEncoding.EncodeToString(key),
	}, luxlog.New("test"))
	if err != nil {
		t.Fatalf("kms.New: %v", err)
	}
	if !kc.Ready() {
		t.Fatal("test kms must be Ready")
	}
	t.Cleanup(func() { _ = kc.Close() })
	return kc
}

// recFolder is a faithful fake fold sink: Register REPLICATES fleet.Register's
// reachability contract — it builds a client from the kubeconfig and lists nodes
// against the real (fake) apiserver, counting GPUs — so a fold proves the
// discovered kubeconfig is valid and reaches a live apiserver. It records the
// resulting clusters keyed (org|project|name), org-scoped exactly like fleet.
type recFolder struct {
	mu       sync.Mutex
	clusters map[string]fleet.Cluster
}

func newRecFolder() *recFolder { return &recFolder{clusters: map[string]fleet.Cluster{}} }

// scope replicates fleet.scopeRef: the default project ("" or "default") shares
// the org-only shard, so the fake keys clusters exactly as the real registry.
func scope(org, project string) string {
	if project == "" || project == "default" {
		return org
	}
	return org + "/" + project
}

func key(org, project, name string) string { return scope(org, project) + "|" + name }

var nodeGVR = schema.GroupVersionResource{Version: "v1", Resource: "nodes"}

func (f *recFolder) Register(ctx context.Context, org, project, name, kubeconfig, provider string, isDefault bool) (fleet.Cluster, error) {
	restCfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
	if err != nil {
		return fleet.Cluster{}, fmt.Errorf("kubeconfig unusable: %w", err)
	}
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return fleet.Cluster{}, err
	}
	ul, err := dyn.Resource(nodeGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fleet.Cluster{}, fmt.Errorf("cluster unreachable with this kubeconfig: %w", err)
	}
	rec := fleet.Cluster{Name: name, Org: org, Kind: "byo", Provider: provider, Endpoint: restCfg.Host, Nodes: len(ul.Items), Default: isDefault}
	for _, n := range ul.Items {
		alloc, ok, _ := unstructured.NestedMap(n.Object, "status", "allocatable")
		if !ok {
			continue
		}
		rec.NvidiaGPU += qint(alloc["nvidia.com/gpu"])
		rec.AmdGPU += qint(alloc["amd.com/gpu"])
	}
	f.mu.Lock()
	f.clusters[key(org, project, name)] = rec
	f.mu.Unlock()
	return rec, nil
}

func (f *recFolder) Deregister(org, project, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := key(org, project, name)
	if _, ok := f.clusters[k]; !ok {
		return false, nil
	}
	delete(f.clusters, k)
	return true, nil
}

func (f *recFolder) List(org, project string) ([]fleet.Cluster, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fleet.Cluster
	prefix := scope(org, project) + "|"
	for k, c := range f.clusters {
		if strings.HasPrefix(k, prefix) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *recFolder) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.clusters)
}

func qint(v any) int {
	if v == nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(fmt.Sprint(v)))
	if err != nil {
		return 0
	}
	return n
}

func newVenue(t *testing.T, f folder, kc *kms.Client) *zip.App {
	t.Helper()
	deps := cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), Domain: "api.hanzo.ai", Brand: "hanzo", KMS: kc}
	s := &cloud.Service[state]{
		Base: cloud.NewBase(deps, "venue"),
		State: state{kms: kc, fleet: f, drivers: map[string]driver{
			providerDO:    doDriver{},
			providerAWS:   awsDriver{},
			providerGCP:   gcpDriver{},
			providerAzure: azureDriver{},
		}},
	}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	routes(app, s)
	return app
}

type result struct {
	Code int
	Body []byte
}

// call issues a request as org. admin sets the org-admin header; user!="" makes
// the principal validated. project sets X-Project-Id.
func call(t *testing.T, app *zip.App, method, path, org, project string, admin bool, body any) result {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	rq := httptest.NewRequest(method, path, r)
	if body != nil {
		rq.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u-"+org)
	}
	if admin {
		rq.Header.Set("X-User-IsOrgAdmin", "true")
	}
	if project != "" {
		rq.Header.Set("X-Project-Id", project)
	}
	resp, err := app.Fiber().Test(rq)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return result{Code: resp.StatusCode, Body: b}
}

// fakeAPIServer is a TLS k8s apiserver answering GET /api/v1/nodes with a node
// list carrying a GPU node (proves fleet-style node + GPU inventory on fold).
func fakeAPIServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/nodes" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"NodeList","items":[
				{"metadata":{"name":"n1"},"status":{"allocatable":{"nvidia.com/gpu":"2"}}},
				{"metadata":{"name":"n2"},"status":{"allocatable":{}}}
			]}`))
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func caB64(srv *httptest.Server) string {
	return base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: srv.Certificate().Raw,
	}))
}

// doKubeconfig is a kubeconfig pointing at the fake apiserver (insecure-skip so
// the self-signed TLS is accepted — DO hands back a ready kubeconfig).
func doKubeconfig(server string) string {
	return `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: ` + server + `
    insecure-skip-tls-verify: true
  name: c
contexts:
- context:
    cluster: c
    user: u
  name: c
current-context: c
users:
- name: u
  user:
    token: do-cluster-token`
}

// doStub emulates the DO API. clusters is name->endpoint; token gates auth.
func doStub(t *testing.T, wantToken string, clusters map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/account", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+wantToken {
			http.Error(w, `{"id":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"account":{"uuid":"acct-uuid-123","email":"team@org.co"}}`))
	})
	// Exact path → list; subtree /{id}/kubeconfig → the cluster's kubeconfig.
	mux.HandleFunc("/v2/kubernetes/clusters", func(w http.ResponseWriter, r *http.Request) {
		var items []string
		for id, ep := range clusters {
			items = append(items, fmt.Sprintf(`{"id":%q,"name":%q,"region":"sfo3","endpoint":%q}`, id, id, ep))
		}
		_, _ = w.Write([]byte(`{"kubernetes_clusters":[` + strings.Join(items, ",") + `],"meta":{"total":1}}`))
	})
	mux.HandleFunc("/v2/kubernetes/clusters/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/kubeconfig") {
			http.Error(w, "no route", http.StatusNotFound)
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v2/kubernetes/clusters/"), "/kubeconfig")
		ep, ok := clusters[id]
		if !ok {
			http.Error(w, "no cluster", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(doKubeconfig(ep)))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// linkDO points the DO driver at stub and links account (label) as org.
func linkDO(t *testing.T, app *zip.App, org, project, label, token string) result {
	t.Helper()
	return call(t, app, http.MethodPost, "/v1/cloud/digitalocean/accounts", org, project, true,
		map[string]any{"label": label, "token": token})
}

// ── tests ───────────────────────────────────────────────────────────────────

// The anchor: a DO token → discover the account's DOKS clusters → fold each into
// the fleet. Proves the fold reaches a live apiserver (node + GPU inventory), the
// token is sealed in KMS and never in the response, and the account lists the
// folded cluster.
func TestDigitalOcean_LinkDiscoversAndFolds(t *testing.T) {
	t.Setenv(allowPrivateEndpointsEnv, "1")
	api := fakeAPIServer(t)
	do := doStub(t, "dop_v1_secret", map[string]string{"c1": api.URL})
	t.Setenv("DIGITALOCEAN_API_URL", do.URL)

	f := newRecFolder()
	kc := newKMS(t)
	app := newVenue(t, f, kc)

	res := linkDO(t, app, "acme", "", "prod", "dop_v1_secret")
	if res.Code != http.StatusCreated {
		t.Fatalf("link want 201, got %d (%s)", res.Code, res.Body)
	}
	// The token must NEVER appear in the response.
	if strings.Contains(string(res.Body), "dop_v1_secret") {
		t.Fatalf("response leaked the token: %s", res.Body)
	}
	var out struct {
		Account  accountView     `json:"account"`
		Clusters []clusterResult `json:"clusters"`
	}
	if err := json.Unmarshal(res.Body, &out); err != nil {
		t.Fatalf("body: %v (%s)", err, res.Body)
	}
	if out.Account.ExternalID != "acct-uuid-123" || out.Account.Account != "team@org.co" {
		t.Fatalf("account identity wrong: %+v", out.Account)
	}
	if len(out.Clusters) != 1 || !out.Clusters[0].Folded {
		t.Fatalf("want 1 folded cluster, got %+v", out.Clusters)
	}
	if out.Clusters[0].Nodes != 2 || out.Clusters[0].NvidiaGPU != 2 {
		t.Fatalf("fold did not inventory the apiserver: %+v", out.Clusters[0])
	}
	// The cluster is in THE fleet, org-scoped.
	if got, _ := f.List("acme", ""); len(got) != 1 {
		t.Fatalf("fleet should hold 1 cluster for acme, got %d", len(got))
	}
	// The token is sealed in KMS and round-trips.
	raw, err := kc.Get("/orgs/acme/cloud/digitalocean/prod", credName, venueEnv)
	if err != nil {
		t.Fatalf("credential not sealed: %v", err)
	}
	var cr cred
	if err := json.Unmarshal(raw, &cr); err != nil || cr.Token != "dop_v1_secret" {
		t.Fatalf("sealed credential wrong: %v %q", err, cr.Token)
	}
	// The account is listed for the org with the fold name.
	list := call(t, app, http.MethodGet, "/v1/cloud/accounts", "acme", "", false, nil)
	if !strings.Contains(string(list.Body), out.Clusters[0].Cluster) {
		t.Fatalf("account list missing the fold name: %s", list.Body)
	}
}

// An org may link MANY labeled accounts per provider; each seals independently
// and folds its own clusters.
func TestMultiCredentialPerOrg(t *testing.T) {
	t.Setenv(allowPrivateEndpointsEnv, "1")
	api := fakeAPIServer(t)
	// Two DO "teams", each their own token + cluster, on one stub keyed by token.
	teamA := doStub(t, "tokA", map[string]string{"ca": api.URL})
	t.Setenv("DIGITALOCEAN_API_URL", teamA.URL)
	f := newRecFolder()
	kc := newKMS(t)
	app := newVenue(t, f, kc)

	if r := linkDO(t, app, "acme", "", "teamA", "tokA"); r.Code != http.StatusCreated {
		t.Fatalf("link teamA: %d (%s)", r.Code, r.Body)
	}
	// Second stub for teamB (different token + cluster id).
	teamB := doStub(t, "tokB", map[string]string{"cb": api.URL})
	t.Setenv("DIGITALOCEAN_API_URL", teamB.URL)
	if r := linkDO(t, app, "acme", "", "teamB", "tokB"); r.Code != http.StatusCreated {
		t.Fatalf("link teamB: %d (%s)", r.Code, r.Body)
	}

	list := call(t, app, http.MethodGet, "/v1/cloud/accounts", "acme", "", false, nil)
	var lv struct {
		Accounts []accountView `json:"accounts"`
	}
	_ = json.Unmarshal(list.Body, &lv)
	if len(lv.Accounts) != 2 {
		t.Fatalf("want 2 accounts, got %d (%s)", len(lv.Accounts), list.Body)
	}
	// Both credentials sealed under distinct labels.
	for _, label := range []string{"teamA", "teamB"} {
		if _, err := kc.Get("/orgs/acme/cloud/digitalocean/"+label, credName, venueEnv); err != nil {
			t.Fatalf("credential for %s not sealed: %v", label, err)
		}
	}
	if f.count() != 2 {
		t.Fatalf("want 2 folded clusters (one per account), got %d", f.count())
	}
}

// One org can neither see, sync, nor unlink another org's accounts; its folds are
// isolated in its own fleet shard.
func TestTenantIsolation(t *testing.T) {
	t.Setenv(allowPrivateEndpointsEnv, "1")
	api := fakeAPIServer(t)
	do := doStub(t, "tok", map[string]string{"c1": api.URL})
	t.Setenv("DIGITALOCEAN_API_URL", do.URL)
	f := newRecFolder()
	kc := newKMS(t)
	app := newVenue(t, f, kc)

	if r := linkDO(t, app, "org1", "", "prod", "tok"); r.Code != http.StatusCreated {
		t.Fatalf("org1 link: %d (%s)", r.Code, r.Body)
	}
	// org2 sees no accounts.
	l2 := call(t, app, http.MethodGet, "/v1/cloud/accounts", "org2", "", false, nil)
	var lv struct {
		Accounts []accountView `json:"accounts"`
	}
	_ = json.Unmarshal(l2.Body, &lv)
	if len(lv.Accounts) != 0 {
		t.Fatalf("org2 must see 0 accounts, got %d", len(lv.Accounts))
	}
	// org2 cannot sync org1's account (its own shard has no such account).
	sync := call(t, app, http.MethodPost, "/v1/cloud/digitalocean/accounts/prod/sync", "org2", "", true, nil)
	if sync.Code != http.StatusNotFound {
		t.Fatalf("org2 sync of org1 account want 404, got %d (%s)", sync.Code, sync.Body)
	}
	// org2 unlink is idempotent-OK but touches nothing of org1.
	call(t, app, http.MethodDelete, "/v1/cloud/digitalocean/accounts/prod", "org2", "", true, nil)
	if got, _ := f.List("org1", ""); len(got) != 1 {
		t.Fatalf("org1 cluster must survive org2 unlink, have %d", len(got))
	}
	if got, _ := f.List("org2", ""); len(got) != 0 {
		t.Fatalf("org2 must have 0 clusters, have %d", len(got))
	}
}

// Without KMS ready, link fails closed (503) and stores nothing — never plaintext.
func TestFailClosedNoKMS(t *testing.T) {
	t.Setenv(allowPrivateEndpointsEnv, "1")
	api := fakeAPIServer(t)
	do := doStub(t, "tok", map[string]string{"c1": api.URL})
	t.Setenv("DIGITALOCEAN_API_URL", do.URL)
	f := newRecFolder()
	app := newVenue(t, f, nil) // nil KMS

	r := linkDO(t, app, "acme", "", "prod", "tok")
	if r.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 without KMS, got %d (%s)", r.Code, r.Body)
	}
	if f.count() != 0 {
		t.Fatalf("nothing should fold without KMS, got %d", f.count())
	}
}

// A bad credential is refused and NOTHING is stored (verify-before-seal).
func TestBadCredentialStoresNothing(t *testing.T) {
	t.Setenv(allowPrivateEndpointsEnv, "1")
	api := fakeAPIServer(t)
	do := doStub(t, "correct-token", map[string]string{"c1": api.URL})
	t.Setenv("DIGITALOCEAN_API_URL", do.URL)
	f := newRecFolder()
	kc := newKMS(t)
	app := newVenue(t, f, kc)

	r := linkDO(t, app, "acme", "", "prod", "WRONG-token")
	if r.Code != http.StatusBadRequest {
		t.Fatalf("bad credential want 400, got %d (%s)", r.Code, r.Body)
	}
	if _, err := kc.Get("/orgs/acme/cloud/digitalocean/prod", credName, venueEnv); err == nil {
		t.Fatal("a rejected credential must not be sealed")
	}
	if f.count() != 0 {
		t.Fatalf("nothing should fold on a bad credential, got %d", f.count())
	}
	// The error body must not echo the token.
	if strings.Contains(string(r.Body), "WRONG-token") {
		t.Fatalf("error leaked the token: %s", r.Body)
	}
}

// unlink detaches this account's folded clusters, deletes the sealed credential,
// and forgets the account. Idempotent.
func TestUnlinkDetaches(t *testing.T) {
	t.Setenv(allowPrivateEndpointsEnv, "1")
	api := fakeAPIServer(t)
	do := doStub(t, "tok", map[string]string{"c1": api.URL})
	t.Setenv("DIGITALOCEAN_API_URL", do.URL)
	f := newRecFolder()
	kc := newKMS(t)
	app := newVenue(t, f, kc)

	if r := linkDO(t, app, "acme", "", "prod", "tok"); r.Code != http.StatusCreated {
		t.Fatalf("link: %d (%s)", r.Code, r.Body)
	}
	if f.count() != 1 {
		t.Fatalf("precondition: 1 folded, got %d", f.count())
	}
	un := call(t, app, http.MethodDelete, "/v1/cloud/digitalocean/accounts/prod", "acme", "", true, nil)
	if un.Code != http.StatusOK {
		t.Fatalf("unlink want 200, got %d (%s)", un.Code, un.Body)
	}
	if f.count() != 0 {
		t.Fatalf("unlink must detach the folded cluster, still have %d", f.count())
	}
	if _, err := kc.Get("/orgs/acme/cloud/digitalocean/prod", credName, venueEnv); err == nil {
		t.Fatal("unlink must delete the sealed credential")
	}
	// Idempotent: a second unlink still returns OK.
	if un2 := call(t, app, http.MethodDelete, "/v1/cloud/digitalocean/accounts/prod", "acme", "", true, nil); un2.Code != http.StatusOK {
		t.Fatalf("second unlink want 200, got %d", un2.Code)
	}
}

// sync reconciles the fold set: new clusters fold, vanished ones detach.
func TestSyncReconciles(t *testing.T) {
	t.Setenv(allowPrivateEndpointsEnv, "1")
	api := fakeAPIServer(t)
	clusters := map[string]string{"c1": api.URL}
	do := doStub(t, "tok", clusters)
	t.Setenv("DIGITALOCEAN_API_URL", do.URL)
	f := newRecFolder()
	kc := newKMS(t)
	app := newVenue(t, f, kc)

	if r := linkDO(t, app, "acme", "", "prod", "tok"); r.Code != http.StatusCreated {
		t.Fatalf("link: %d (%s)", r.Code, r.Body)
	}
	if f.count() != 1 {
		t.Fatalf("precondition 1, got %d", f.count())
	}
	// A second cluster appears upstream; sync folds it.
	clusters["c2"] = api.URL
	if s := call(t, app, http.MethodPost, "/v1/cloud/digitalocean/accounts/prod/sync", "acme", "", true, nil); s.Code != http.StatusOK {
		t.Fatalf("sync add: %d (%s)", s.Code, s.Body)
	}
	if f.count() != 2 {
		t.Fatalf("sync should fold the new cluster, got %d", f.count())
	}
	// The first cluster is deleted upstream; sync detaches it.
	delete(clusters, "c1")
	if s := call(t, app, http.MethodPost, "/v1/cloud/digitalocean/accounts/prod/sync", "acme", "", true, nil); s.Code != http.StatusOK {
		t.Fatalf("sync remove: %d (%s)", s.Code, s.Body)
	}
	if f.count() != 1 {
		t.Fatalf("sync should detach the vanished cluster, got %d", f.count())
	}
}

// Mutations require org admin; reads do not.
func TestAdminGate(t *testing.T) {
	t.Setenv(allowPrivateEndpointsEnv, "1")
	api := fakeAPIServer(t)
	do := doStub(t, "tok", map[string]string{"c1": api.URL})
	t.Setenv("DIGITALOCEAN_API_URL", do.URL)
	app := newVenue(t, newRecFolder(), newKMS(t))

	// non-admin link → 403
	r := call(t, app, http.MethodPost, "/v1/cloud/digitalocean/accounts", "acme", "", false,
		map[string]any{"label": "prod", "token": "tok"})
	if r.Code != http.StatusForbidden {
		t.Fatalf("non-admin link want 403, got %d (%s)", r.Code, r.Body)
	}
	// non-admin read → OK
	if l := call(t, app, http.MethodGet, "/v1/cloud/accounts", "acme", "", false, nil); l.Code != http.StatusOK {
		t.Fatalf("non-admin list want 200, got %d", l.Code)
	}
}

// The provider cards list the three connectable providers with their keyless flag.
func TestProviderCards(t *testing.T) {
	app := newVenue(t, newRecFolder(), newKMS(t))
	r := call(t, app, http.MethodGet, "/v1/cloud", "acme", "", false, nil)
	if r.Code != http.StatusOK {
		t.Fatalf("cards want 200, got %d", r.Code)
	}
	for _, want := range []string{providerDO, providerAWS, providerGCP, providerAzure} {
		if !strings.Contains(string(r.Body), want) {
			t.Fatalf("cards missing %s: %s", want, r.Body)
		}
	}
}

// The SSRF guard blocks a DISCOVERED endpoint on a non-routable host when the
// test bypass is OFF: the cluster is not folded and the error is generic.
func TestSSRFGuardBlocksPrivateEndpoint(t *testing.T) {
	// NOTE: allowPrivateEndpointsEnv deliberately UNSET here.
	api := fakeAPIServer(t)
	do := doStub(t, "tok", map[string]string{"c1": api.URL}) // api.URL is loopback https
	t.Setenv("DIGITALOCEAN_API_URL", do.URL)
	f := newRecFolder()
	app := newVenue(t, f, newKMS(t))

	r := linkDO(t, app, "acme", "", "prod", "tok")
	if r.Code != http.StatusCreated {
		t.Fatalf("link should still succeed (account linked), got %d (%s)", r.Code, r.Body)
	}
	var out struct {
		Clusters []clusterResult `json:"clusters"`
	}
	_ = json.Unmarshal(r.Body, &out)
	if len(out.Clusters) != 1 || out.Clusters[0].Folded {
		t.Fatalf("loopback endpoint must NOT fold, got %+v", out.Clusters)
	}
	if !strings.Contains(out.Clusters[0].Error, "non-routable") {
		t.Fatalf("want SSRF-guard error, got %q", out.Clusters[0].Error)
	}
	if f.count() != 0 {
		t.Fatalf("nothing should fold past the SSRF guard, got %d", f.count())
	}
}
