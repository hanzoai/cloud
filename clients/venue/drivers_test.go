package venue

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ── AWS (EKS) — keyless cross-account role assumption ───────────────────────

// awsStub emulates STS (query protocol) + EKS (REST). AssumeRole REQUIRES the
// exact external id, so a successful discover PROVES venue passed it (confused-
// deputy protection). clusters is name->apiserver endpoint; caB64 is the fold CA.
func awsStub(t *testing.T, wantExternalID, caB64 string, clusters map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	// STS query protocol: POST / with Action + (for AssumeRole) ExternalId.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/clusters") {
			awsEKS(w, r, caB64, clusters)
			return
		}
		_ = r.ParseForm()
		switch r.Form.Get("Action") {
		case "AssumeRole":
			if r.Form.Get("ExternalId") != wantExternalID {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`<ErrorResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><Error><Code>AccessDenied</Code><Message>external id mismatch</Message></Error></ErrorResponse>`))
				return
			}
			w.Header().Set("Content-Type", "text/xml")
			_, _ = w.Write([]byte(`<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult><Credentials><AccessKeyId>ASIAEXAMPLE</AccessKeyId><SecretAccessKey>secret</SecretAccessKey><SessionToken>session</SessionToken><Expiration>2035-01-01T00:00:00Z</Expiration></Credentials><AssumedRoleUser><Arn>arn:aws:sts::123456789012:assumed-role/hanzo/hanzo-venue-discovery</Arn><AssumedRoleId>AROAEXAMPLE:hanzo</AssumedRoleId></AssumedRoleUser></AssumeRoleResult><ResponseMetadata><RequestId>r</RequestId></ResponseMetadata></AssumeRoleResponse>`))
		case "GetCallerIdentity":
			w.Header().Set("Content-Type", "text/xml")
			_, _ = w.Write([]byte(`<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><GetCallerIdentityResult><Arn>arn:aws:sts::123456789012:assumed-role/hanzo/sess</Arn><UserId>AROAEXAMPLE:sess</UserId><Account>123456789012</Account></GetCallerIdentityResult><ResponseMetadata><RequestId>r</RequestId></ResponseMetadata></GetCallerIdentityResponse>`))
		default:
			http.Error(w, "unknown action", http.StatusBadRequest)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func awsEKS(w http.ResponseWriter, r *http.Request, caB64 string, clusters map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	// DescribeCluster: GET /clusters/{name}
	if name := strings.TrimPrefix(r.URL.Path, "/clusters/"); name != "" && name != "clusters" && !strings.Contains(name, "/") {
		ep, ok := clusters[name]
		if !ok {
			http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(fmt.Sprintf(`{"cluster":{"name":%q,"arn":"arn:aws:eks:us-west-2:123456789012:cluster/%s","status":"ACTIVE","endpoint":%q,"certificateAuthority":{"data":%q}}}`, name, name, ep, caB64)))
		return
	}
	// ListClusters: GET /clusters
	var names []string
	for n := range clusters {
		names = append(names, fmt.Sprintf("%q", n))
	}
	_, _ = w.Write([]byte(`{"clusters":[` + strings.Join(names, ",") + `]}`))
}

// The keyless AWS path: assume a cross-account role pinned by an external id,
// discover EKS clusters, fold each. The stub only assumes the role when the exact
// external id is presented, so this passing proves venue forwards it.
func TestAWS_KeylessRoleAssumptionDiscoversAndFolds(t *testing.T) {
	t.Setenv(allowPrivateEndpointsEnv, "1")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAPLATFORM")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "platform-secret")
	t.Setenv("AWS_REGION", "us-west-2")
	api := fakeAPIServer(t)
	aws := awsStub(t, "org-shared-external-id", caB64(api), map[string]string{"prod-eks": api.URL})
	t.Setenv("AWS_ENDPOINT_URL", aws.URL)

	f := newRecFolder()
	kc := newKMS(t)
	app := newVenue(t, f, kc)

	res := call(t, app, http.MethodPost, "/v1/cloud/aws/accounts", "acme", "", true, map[string]any{
		"label": "prod", "roleArn": "arn:aws:iam::123456789012:role/HanzoDiscovery",
		"externalId": "org-shared-external-id", "regions": []string{"us-west-2"},
	})
	if res.Code != http.StatusCreated {
		t.Fatalf("aws link want 201, got %d (%s)", res.Code, res.Body)
	}
	if strings.Contains(string(res.Body), "org-shared-external-id") {
		t.Fatalf("response leaked the external id: %s", res.Body)
	}
	var out struct {
		Account  accountView     `json:"account"`
		Clusters []clusterResult `json:"clusters"`
	}
	_ = json.Unmarshal(res.Body, &out)
	if out.Account.ExternalID != "123456789012" {
		t.Fatalf("aws account id wrong: %+v", out.Account)
	}
	if len(out.Clusters) != 1 || !out.Clusters[0].Folded || out.Clusters[0].Nodes != 2 {
		t.Fatalf("aws fold wrong: %+v", out.Clusters)
	}
	if f.count() != 1 {
		t.Fatalf("aws should fold 1 cluster, got %d", f.count())
	}
	// The keyless credential (role + external id) is sealed, no access keys.
	raw, err := kc.Get("/orgs/acme/cloud/aws/prod", credName, venueEnv)
	if err != nil {
		t.Fatalf("aws credential not sealed: %v", err)
	}
	var cr cred
	_ = json.Unmarshal(raw, &cr)
	if cr.ExternalID != "org-shared-external-id" || cr.Token != "" {
		t.Fatalf("sealed aws credential wrong (should be keyless): %+v", cr)
	}
}

// Confused-deputy: the WRONG external id fails the assumption — verify refuses and
// nothing is stored.
func TestAWS_WrongExternalIDRefused(t *testing.T) {
	t.Setenv(allowPrivateEndpointsEnv, "1")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAPLATFORM")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "platform-secret")
	t.Setenv("AWS_REGION", "us-west-2")
	api := fakeAPIServer(t)
	aws := awsStub(t, "the-right-id", caB64(api), map[string]string{"prod-eks": api.URL})
	t.Setenv("AWS_ENDPOINT_URL", aws.URL)

	f := newRecFolder()
	kc := newKMS(t)
	app := newVenue(t, f, kc)

	res := call(t, app, http.MethodPost, "/v1/cloud/aws/accounts", "acme", "", true, map[string]any{
		"label": "prod", "roleArn": "arn:aws:iam::123456789012:role/HanzoDiscovery",
		"externalId": "WRONG-id", "regions": []string{"us-west-2"},
	})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("wrong external id want 400, got %d (%s)", res.Code, res.Body)
	}
	if _, err := kc.Get("/orgs/acme/cloud/aws/prod", credName, venueEnv); err == nil {
		t.Fatal("a refused assumption must seal nothing")
	}
	if f.count() != 0 {
		t.Fatalf("nothing should fold on refused assumption, got %d", f.count())
	}
}

// ── GCP (GKE) — keyless Workload Identity Federation ─────────────────────────

func gcpStub(t *testing.T, caB64 string, clusters map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/clusters") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var items []string
		for name, ep := range clusters {
			items = append(items, fmt.Sprintf(`{"name":%q,"location":"us-central1","endpoint":%q,"selfLink":"https://container.googleapis.com/v1/%s","masterAuth":{"clusterCaCertificate":%q}}`, name, ep, name, caB64))
		}
		_, _ = w.Write([]byte(`{"clusters":[` + strings.Join(items, ",") + `]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// The keyless GCP path: a token source (WIF in prod; a static token in test)
// drives the container REST API; each GKE cluster folds with a kubeconfig bearing
// the google access token.
func TestGCP_KeylessDiscoversAndFolds(t *testing.T) {
	t.Setenv(allowPrivateEndpointsEnv, "1")
	t.Setenv("GCP_STATIC_TOKEN", "ya29.test-access-token")
	api := fakeAPIServer(t)
	// GKE reports the endpoint host without a scheme; gcp.go prepends https://.
	host := strings.TrimPrefix(api.URL, "https://")
	gcp := gcpStub(t, caB64(api), map[string]string{"prod-gke": host})
	t.Setenv("GCP_CONTAINER_ENDPOINT", gcp.URL)

	f := newRecFolder()
	kc := newKMS(t)
	app := newVenue(t, f, kc)

	res := call(t, app, http.MethodPost, "/v1/cloud/gcp/accounts", "acme", "", true, map[string]any{
		"label": "prod", "projectIds": []string{"my-project"},
	})
	if res.Code != http.StatusCreated {
		t.Fatalf("gcp link want 201, got %d (%s)", res.Code, res.Body)
	}
	var out struct {
		Account  accountView     `json:"account"`
		Clusters []clusterResult `json:"clusters"`
	}
	_ = json.Unmarshal(res.Body, &out)
	if out.Account.ExternalID != "my-project" {
		t.Fatalf("gcp account project wrong: %+v", out.Account)
	}
	if len(out.Clusters) != 1 || !out.Clusters[0].Folded || out.Clusters[0].Nodes != 2 {
		t.Fatalf("gcp fold wrong: %+v", out.Clusters)
	}
	if f.count() != 1 {
		t.Fatalf("gcp should fold 1 cluster, got %d", f.count())
	}
}

// Missing required inputs are refused before any provider call (bounds check).
func TestProviderInputValidation(t *testing.T) {
	app := newVenue(t, newRecFolder(), newKMS(t))
	cases := []struct {
		provider string
		body     map[string]any
	}{
		{"aws", map[string]any{"label": "x", "regions": []string{"us-west-2"}}}, // no roleArn/externalId
		{"gcp", map[string]any{"label": "x"}},                                   // no projectIds
		{"digitalocean", map[string]any{"label": "x"}},                          // no token
	}
	for _, tc := range cases {
		r := call(t, app, http.MethodPost, "/v1/cloud/"+tc.provider+"/accounts", "acme", "", true, tc.body)
		if r.Code != http.StatusBadRequest {
			t.Fatalf("%s missing inputs want 400, got %d (%s)", tc.provider, r.Code, r.Body)
		}
	}
}
