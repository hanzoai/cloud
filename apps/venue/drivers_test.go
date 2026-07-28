package venue

import (
	"encoding/base64"
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
	t.Setenv(fleetAllowPrivateHostsEnv, "1")
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
	t.Setenv(fleetAllowPrivateHostsEnv, "1")
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
	t.Setenv(fleetAllowPrivateHostsEnv, "1")
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

// ── Azure (AKS) — service principal + keyless workload identity federation ───

// azureStub emulates the AAD token endpoint + ARM. The token handler asserts the
// expected auth shape (client_secret for SP, client_assertion for WIF), so a
// successful discover proves venue sent the right credential. clusters is
// name->apiserver endpoint; the kubeconfig is base64 like ARM returns it.
func azureStub(t *testing.T, tenant, wantSecret, wantAssertion string, clusters map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/"+tenant+"/oauth2/v2.0/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if wantSecret != "" && r.Form.Get("client_secret") != wantSecret {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		}
		if wantAssertion != "" && r.Form.Get("client_assertion") != wantAssertion {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"aad-token","token_type":"Bearer","expires_in":3600}`))
	})
	// managedClusters list + per-cluster listClusterUserCredential.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/listClusterUserCredential") {
			name := strings.TrimSuffix(r.URL.Path, "/listClusterUserCredential")
			name = name[strings.LastIndex(name, "/")+1:]
			ep, ok := clusters[name]
			if !ok {
				http.Error(w, `{"error":{}}`, http.StatusNotFound)
				return
			}
			kube := base64.StdEncoding.EncodeToString([]byte(doKubeconfig(ep)))
			_, _ = w.Write([]byte(`{"kubeconfigs":[{"name":"clusterUser","value":"` + kube + `"}]}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/managedClusters") {
			var items []string
			for name, ep := range clusters {
				id := "/subscriptions/sub-1/resourceGroups/rg1/providers/Microsoft.ContainerService/managedClusters/" + name
				host := strings.TrimPrefix(ep, "https://")
				items = append(items, fmt.Sprintf(`{"id":%q,"name":%q,"location":"eastus","properties":{"fqdn":%q}}`, id, name, host))
			}
			_, _ = w.Write([]byte(`{"value":[` + strings.Join(items, ",") + `]}`))
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// Service-principal Azure: tenant+client+secret → AAD token → list AKS → fold.
func TestAzure_ServicePrincipalDiscoversAndFolds(t *testing.T) {
	t.Setenv(fleetAllowPrivateHostsEnv, "1")
	api := fakeAPIServer(t)
	az := azureStub(t, "tenant-1", "sp-secret", "", map[string]string{"prod-aks": api.URL})
	t.Setenv("AZURE_AD_ENDPOINT", az.URL)
	t.Setenv("AZURE_ARM_ENDPOINT", az.URL)

	f := newRecFolder()
	kc := newKMS(t)
	app := newVenue(t, f, kc)

	res := call(t, app, http.MethodPost, "/v1/cloud/azure/accounts", "acme", "", true, map[string]any{
		"label": "prod", "tenantId": "tenant-1", "clientId": "client-1",
		"clientSecret": "sp-secret", "subscriptionIds": []string{"sub-1"},
	})
	if res.Code != http.StatusCreated {
		t.Fatalf("azure link want 201, got %d (%s)", res.Code, res.Body)
	}
	if strings.Contains(string(res.Body), "sp-secret") {
		t.Fatalf("response leaked the client secret: %s", res.Body)
	}
	var out struct {
		Account  accountView     `json:"account"`
		Clusters []clusterResult `json:"clusters"`
	}
	_ = json.Unmarshal(res.Body, &out)
	if out.Account.ExternalID != "tenant-1" {
		t.Fatalf("azure tenant wrong: %+v", out.Account)
	}
	if len(out.Clusters) != 1 || !out.Clusters[0].Folded || out.Clusters[0].Nodes != 2 {
		t.Fatalf("azure fold wrong: %+v", out.Clusters)
	}
	if f.count() != 1 {
		t.Fatalf("azure should fold 1 cluster, got %d", f.count())
	}
}

// Keyless Azure: no client secret → WIF client assertion (Hanzo's federated
// token) → fold. Proves the keyless path forwards the assertion, not a secret.
func TestAzure_KeylessWorkloadIdentityFederation(t *testing.T) {
	t.Setenv(fleetAllowPrivateHostsEnv, "1")
	t.Setenv("AZURE_FEDERATED_TOKEN", "hanzo-federated-oidc-token")
	api := fakeAPIServer(t)
	az := azureStub(t, "tenant-1", "", "hanzo-federated-oidc-token", map[string]string{"prod-aks": api.URL})
	t.Setenv("AZURE_AD_ENDPOINT", az.URL)
	t.Setenv("AZURE_ARM_ENDPOINT", az.URL)

	f := newRecFolder()
	kc := newKMS(t)
	app := newVenue(t, f, kc)

	res := call(t, app, http.MethodPost, "/v1/cloud/azure/accounts", "acme", "", true, map[string]any{
		"label": "prod", "tenantId": "tenant-1", "clientId": "client-1",
		"subscriptionIds": []string{"sub-1"}, // NO clientSecret → keyless WIF
	})
	if res.Code != http.StatusCreated {
		t.Fatalf("azure WIF link want 201, got %d (%s)", res.Code, res.Body)
	}
	if f.count() != 1 {
		t.Fatalf("azure WIF should fold 1 cluster, got %d", f.count())
	}
	// The sealed credential is keyless (no secret).
	raw, err := kc.Get("/orgs/acme/cloud/azure/prod", credName, venueEnv)
	if err != nil {
		t.Fatalf("azure credential not sealed: %v", err)
	}
	var cr cred
	_ = json.Unmarshal(raw, &cr)
	if cr.ClientSecret != "" {
		t.Fatalf("keyless azure credential must carry no secret: %+v", cr)
	}
}

// HIGH-1: a hostile external_account (WIF) credentialJson — whose credential_source
// would make the pod fetch a token from the cloud metadata IP (SSRF) — is REJECTED
// in verify(), before google.CredentialsFromJSON ever runs, so nothing is fetched,
// read, or sealed.
func TestGCP_HostileExternalAccountRejected(t *testing.T) {
	// NO GCP_STATIC_TOKEN: the credentialJson validation path runs.
	f := newRecFolder()
	kc := newKMS(t)
	app := newVenue(t, f, kc)

	hostile := `{"type":"external_account","audience":"//iam.googleapis.com/x","subject_token_type":"urn:ietf:params:oauth:token-type:jwt","token_url":"https://sts.googleapis.com/v1/token","credential_source":{"url":"http://169.254.169.254/computeMetadata/v1/instance/service-accounts/default/token","headers":{"Metadata-Flavor":"Google"}}}`
	res := call(t, app, http.MethodPost, "/v1/cloud/gcp/accounts", "acme", "", true, map[string]any{
		"label": "prod", "credentialJson": hostile, "projectIds": []string{"p"},
	})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("hostile external_account want 400, got %d (%s)", res.Code, res.Body)
	}
	if _, err := kc.Get("/orgs/acme/cloud/gcp/prod", credName, venueEnv); err == nil {
		t.Fatal("a rejected hostile credential must not be sealed")
	}
	if f.count() != 0 {
		t.Fatalf("nothing should fold on a rejected credential, got %d", f.count())
	}
}

func TestValidateGoogleCredential(t *testing.T) {
	reject := []string{
		`{"type":"external_account","credential_source":{"url":"http://169.254.169.254/"}}`, // SSRF
		`{"type":"external_account","credential_source":{"file":"/var/run/secrets/token"}}`, // LFI
		`{"type":"impersonated_service_account"}`,                                           // recursion
		`{"type":"service_account","token_uri":"http://attacker.example.com/token"}`,        // non-google token_uri
		`{"type":"service_account","token_uri":"https://attacker.example.com/token"}`,       // wrong host
		`not json`,
		`{}`, // no type
	}
	for _, c := range reject {
		if err := validateGoogleCredential([]byte(c)); err == nil {
			t.Fatalf("must reject: %s", c)
		}
	}
	accept := []string{
		`{"type":"service_account","token_uri":"https://oauth2.googleapis.com/token","project_id":"p"}`,
		`{"type":"service_account","project_id":"p"}`, // no token_uri (google default)
	}
	for _, c := range accept {
		if err := validateGoogleCredential([]byte(c)); err != nil {
			t.Fatalf("must accept %s: %v", c, err)
		}
	}
}

func TestValidRegion(t *testing.T) {
	for _, ok := range []string{"us-west-2", "us-east-1", "eu-central-1", "ap-southeast-1"} {
		if !validRegion(ok) {
			t.Fatalf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"evil.com/", "us-west-2/", "../", "us_west_2", "US-WEST-2", "us-west", "sts.evil.com", "", ".", "us-west-2.evil.com"} {
		if validRegion(bad) {
			t.Fatalf("%q should be INVALID (region SSRF)", bad)
		}
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
		{"azure", map[string]any{"label": "x", "tenantId": "t"}},                // no subscriptionIds
	}
	for _, tc := range cases {
		r := call(t, app, http.MethodPost, "/v1/cloud/"+tc.provider+"/accounts", "acme", "", true, tc.body)
		if r.Code != http.StatusBadRequest {
			t.Fatalf("%s missing inputs want 400, got %d (%s)", tc.provider, r.Code, r.Body)
		}
	}
}
