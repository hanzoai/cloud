// SPDX-License-Identifier: Apache-2.0 OR MIT

package sovereignty

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zap-proto/zip"
)

// --- Shared HTTP JSON Helpers ---

func startZipTestServer(app *zip.App, err error) *httptest.Server {
	if err != nil {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}))
	}
	return httptest.NewServer(zipHTTPHandler(app))
}

func doJSONRequest(client *http.Client, method, url string, in any, out any) (int, error) {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return 0, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errPayload struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal(respBytes, &errPayload) == nil {
			if errPayload.Error != "" {
				return resp.StatusCode, fmt.Errorf("HTTP %d: %s", resp.StatusCode, errPayload.Error)
			}
			if errPayload.Message != "" {
				return resp.StatusCode, fmt.Errorf("HTTP %d: %s", resp.StatusCode, errPayload.Message)
			}
		}
		return resp.StatusCode, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBytes)))
	}
	if out != nil && len(respBytes) > 0 {
		if err := json.Unmarshal(respBytes, out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

// --- Typed Control-Plane API Request/Response Models ---

// NodeHealthOut is the typed health response for sovereignty infrastructure nodes.
type NodeHealthOut struct {
	ID      string           `json:"id"`
	Plane   ControlPlaneKind `json:"plane"`
	Status  string           `json:"status"`
	Version string           `json:"version,omitempty"`
}

// ListPeersIn carries optional query filters for GET /v1/peers.
type ListPeersIn struct {
	Model string `json:"model,omitempty"`
}

// ListPeersOut is the typed response for GET /v1/peers.
type ListPeersOut struct {
	Providers []ProviderAdvertisement `json:"providers"`
}

// ListModelsOut is the typed response for GET /v1/models.
type ListModelsOut struct {
	Models []ModelManifest `json:"models"`
}

// IDParamIn binds a path `:id` parameter.
type IDParamIn struct {
	ID string `json:"id"`
}

// ProviderHealthOut is the typed health response for ProviderNodeServer.
type ProviderHealthOut struct {
	ProviderID string `json:"providerId"`
	Address    string `json:"address"`
	Status     string `json:"status"`
}

// FacilitatorHealthOut is the typed health response for FacilitatorServer.
type FacilitatorHealthOut struct {
	ID      string           `json:"id"`
	Plane   ControlPlaneKind `json:"plane"`
	Address string           `json:"address"`
	Status  string           `json:"status"`
}

// MintRequest is the typed request for POST /v1/payments/mint.
type MintRequest struct {
	Address string `json:"address"`
	Amount  uint64 `json:"amount"`
}

// AddressParamIn binds a path `:address` parameter.
type AddressParamIn struct {
	Address string `json:"address"`
}

// BalanceOut is the typed response for mint and balance queries.
type BalanceOut struct {
	Address string `json:"address"`
	Balance uint64 `json:"balance"`
}

// LedgerOut is the typed response for GET /v1/payments/ledger.
type LedgerOut struct {
	Entries           []SettlementReceipt `json:"entries"`
	ConservationDrift int64               `json:"conservationDrift"`
}

// StoreSnapshotOut is the typed response for POST /v1/vault/snapshots.
type StoreSnapshotOut struct {
	SnapshotID string `json:"snapshotId"`
	Stored     bool   `json:"stored"`
}

// VaultExportOut is the typed response for GET /v1/vault/export.
type VaultExportOut struct {
	Envelopes []EncryptedDataEnvelope `json:"envelopes"`
}

// HanzoGatewayHealthOut is the typed response for the Hanzo convenience gateway health endpoint.
type HanzoGatewayHealthOut struct {
	Service     string           `json:"service"`
	Plane       ControlPlaneKind `json:"plane"`
	Registry    string           `json:"registry"`
	Mirror      string           `json:"mirror"`
	Scheduler   string           `json:"scheduler"`
	Facilitator string           `json:"facilitator"`
	Vault       string           `json:"vault"`
}

// --- Core Stateful Services Exposed Over Real HTTP Servers ---

// BootstrapServer is a decentralized peer discovery, model manifest, and
// community governance node serving real HTTP over loopback via typed zip operations.
type BootstrapServer struct {
	ID         string
	Plane      ControlPlaneKind
	Domain     EIP712Domain
	Server     *httptest.Server
	offline    atomic.Bool
	mu         sync.RWMutex
	providers  map[string]ProviderAdvertisement
	models     map[string]ModelManifest
	governance map[string]*GovernanceDecision
	quorum     int
}

// NewBootstrapServer starts an independent loopback HTTP bootstrap/registry server.
func NewBootstrapServer(id string, plane ControlPlaneKind, domain EIP712Domain, governanceQuorum int) *BootstrapServer {
	if governanceQuorum <= 0 {
		governanceQuorum = 2
	}
	b := &BootstrapServer{
		ID:         id,
		Plane:      plane,
		Domain:     domain,
		providers:  make(map[string]ProviderAdvertisement),
		models:     make(map[string]ModelManifest),
		governance: make(map[string]*GovernanceDecision),
		quorum:     governanceQuorum,
	}
	app, err := NewBootstrapApp(b, nil)
	b.Server = startZipTestServer(app, err)
	return b
}

// URL returns the real loopback HTTP base URL.
func (b *BootstrapServer) URL() string { return b.Server.URL }

// Close shuts down the HTTP listener completely.
func (b *BootstrapServer) Close() {
	b.offline.Store(true)
	b.Server.Close()
}

func (b *BootstrapServer) opHealth(_ context.Context, _ *emptyIn) (*NodeHealthOut, error) {
	if b.offline.Load() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%s", ErrHanzoPlaneOffline.Error())
	}
	return &NodeHealthOut{
		ID:      b.ID,
		Plane:   b.Plane,
		Status:  "ok",
		Version: ProtocolVersion,
	}, nil
}

func (b *BootstrapServer) opRegisterPeer(_ context.Context, ad *ProviderAdvertisement) (*ProviderAdvertisement, error) {
	if ad == nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", ErrInvalidRequest.Error())
	}
	if err := VerifyProviderAdvertisement(b.Domain, *ad); err != nil {
		return nil, zip.Errorf(http.StatusUnauthorized, "%s", err.Error())
	}
	b.mu.Lock()
	b.providers[ad.ProviderID] = *ad
	b.mu.Unlock()
	cp := *ad
	return &cp, nil
}

func (b *BootstrapServer) opListPeers(_ context.Context, in *ListPeersIn) (*ListPeersOut, error) {
	modelFilter := ""
	if in != nil {
		modelFilter = strings.TrimSpace(in.Model)
	}
	b.mu.RLock()
	out := make([]ProviderAdvertisement, 0, len(b.providers))
	for _, ad := range b.providers {
		if modelFilter == "" || containsString(ad.ModelIDs, modelFilter) {
			out = append(out, ad)
		}
	}
	b.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ProviderID < out[j].ProviderID })
	return &ListPeersOut{Providers: out}, nil
}

func (b *BootstrapServer) opRegisterModel(_ context.Context, m *ModelManifest) (*ModelManifest, error) {
	if m == nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", ErrInvalidRequest.Error())
	}
	if err := VerifyModelManifest(b.Domain, *m); err != nil {
		return nil, zip.Errorf(http.StatusUnauthorized, "%s", err.Error())
	}
	b.mu.Lock()
	b.models[m.ModelID] = *m
	b.mu.Unlock()
	cp := *m
	return &cp, nil
}

func (b *BootstrapServer) opListModels(_ context.Context, _ *emptyIn) (*ListModelsOut, error) {
	b.mu.RLock()
	list := make([]ModelManifest, 0, len(b.models))
	for _, m := range b.models {
		list = append(list, m)
	}
	b.mu.RUnlock()
	sort.Slice(list, func(i, j int) bool { return list[i].ModelID < list[j].ModelID })
	return &ListModelsOut{Models: list}, nil
}

func (b *BootstrapServer) opGetModel(_ context.Context, in *IDParamIn) (*ModelManifest, error) {
	id := ""
	if in != nil {
		id = strings.TrimSpace(in.ID)
	}
	b.mu.RLock()
	m, ok := b.models[id]
	b.mu.RUnlock()
	if !ok {
		return nil, zip.Errorf(http.StatusNotFound, "%s", ErrNotFound.Error())
	}
	return &m, nil
}

func (b *BootstrapServer) opGovernanceVote(_ context.Context, vote *GovernanceVote) (*GovernanceDecision, error) {
	if vote == nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", ErrInvalidRequest.Error())
	}
	if err := VerifyGovernanceVote(b.Domain, *vote); err != nil {
		return nil, zip.Errorf(http.StatusUnauthorized, "%s", err.Error())
	}
	b.mu.Lock()
	dec, ok := b.governance[vote.ProposalID]
	if !ok {
		dec = &GovernanceDecision{
			ProposalID:     vote.ProposalID,
			Action:         vote.Action,
			Payload:        vote.Payload,
			QuorumRequired: b.quorum,
		}
		b.governance[vote.ProposalID] = dec
	} else if dec.Action != vote.Action || dec.Payload != vote.Payload {
		b.mu.Unlock()
		return nil, zip.Errorf(http.StatusConflict, "%s: vote action/payload conflicts with stored proposal %s", ErrInvalidRequest, vote.ProposalID)
	}
	duplicate := false
	for _, existing := range dec.Votes {
		if SameAddress(existing.VoterAddress, vote.VoterAddress) {
			duplicate = true
			break
		}
	}
	if !duplicate {
		dec.Votes = append(dec.Votes, *vote)
		if vote.Approve {
			dec.Approvals++
		}
		dec.Ratified = dec.Approvals >= dec.QuorumRequired
	}
	snapshot := *dec
	snapshot.Votes = append([]GovernanceVote(nil), dec.Votes...)
	b.mu.Unlock()
	return &snapshot, nil
}

func (b *BootstrapServer) opGetGovernance(_ context.Context, in *IDParamIn) (*GovernanceDecision, error) {
	id := ""
	if in != nil {
		id = strings.TrimSpace(in.ID)
	}
	b.mu.RLock()
	dec, ok := b.governance[id]
	if !ok {
		b.mu.RUnlock()
		return nil, zip.Errorf(http.StatusNotFound, "%s", ErrNotFound.Error())
	}
	snapshot := *dec
	snapshot.Votes = append([]GovernanceVote(nil), dec.Votes...)
	b.mu.RUnlock()
	return &snapshot, nil
}

// SnapshotState exports all signed providers, manifests, and governance decisions.
func (b *BootstrapServer) SnapshotState() ([]ProviderAdvertisement, []ModelManifest, []GovernanceDecision) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	providers := make([]ProviderAdvertisement, 0, len(b.providers))
	for _, p := range b.providers {
		providers = append(providers, p)
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].ProviderID < providers[j].ProviderID })

	models := make([]ModelManifest, 0, len(b.models))
	for _, m := range b.models {
		models = append(models, m)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ModelID < models[j].ModelID })

	govs := make([]GovernanceDecision, 0, len(b.governance))
	for _, g := range b.governance {
		cp := *g
		cp.Votes = append([]GovernanceVote(nil), g.Votes...)
		govs = append(govs, cp)
	}
	sort.Slice(govs, func(i, j int) bool { return govs[i].ProposalID < govs[j].ProposalID })
	return providers, models, govs
}

// HydrateFromFork imports verified providers, models, and governance records
// during an independent operator fork or migration.
func (b *BootstrapServer) HydrateFromFork(providers []ProviderAdvertisement, models []ModelManifest, govs []GovernanceDecision) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, p := range providers {
		b.providers[p.ProviderID] = p
	}
	for _, m := range models {
		b.models[m.ModelID] = m
	}
	for _, g := range govs {
		cp := g
		cp.Votes = append([]GovernanceVote(nil), g.Votes...)
		b.governance[g.ProposalID] = &cp
	}
}

// ArtifactMirrorServer stores and serves content-addressed model weight blobs
// keyed by their lowercase hex SHA-256 digest.
type ArtifactMirrorServer struct {
	ID            string
	Plane         ControlPlaneKind
	Server        *httptest.Server
	mu            sync.RWMutex
	blobs         map[string][]byte
	tamperDigests map[string]bool
}

// NewArtifactMirrorServer starts a real loopback content-addressed model mirror.
func NewArtifactMirrorServer(id string, plane ControlPlaneKind) *ArtifactMirrorServer {
	a := &ArtifactMirrorServer{
		ID:            id,
		Plane:         plane,
		blobs:         make(map[string][]byte),
		tamperDigests: make(map[string]bool),
	}
	app, err := NewMirrorApp(a, nil)
	a.Server = startZipTestServer(app, err)
	return a
}

// URL returns the mirror's base HTTP URL.
func (a *ArtifactMirrorServer) URL() string { return a.Server.URL }

// Close shuts down the HTTP mirror listener.
func (a *ArtifactMirrorServer) Close() { a.Server.Close() }

// InjectTamperedBlob flips a byte on subsequent GET responses for digest to
// verify fail-closed SHA-256 integrity checks on clients and providers.
func (a *ArtifactMirrorServer) InjectTamperedBlob(digest string, enable bool) {
	a.mu.Lock()
	a.tamperDigests[strings.ToLower(digest)] = enable
	a.mu.Unlock()
}

// SnapshotBlobs returns a deep copy of all content-addressed model weight blobs
// stored on this mirror so fork bundles remain self-contained after origin shutdown.
func (a *ArtifactMirrorServer) SnapshotBlobs() map[string][]byte {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make(map[string][]byte, len(a.blobs))
	for k, v := range a.blobs {
		out[k] = append([]byte(nil), v...)
	}
	return out
}

// HydrateBlobs verifies SHA-256 digests and installs model artifacts onto this mirror.
func (a *ArtifactMirrorServer) HydrateBlobs(blobs map[string][]byte) error {
	verified := make(map[string][]byte, len(blobs))
	for digest, data := range blobs {
		norm := strings.ToLower(strings.TrimSpace(digest))
		actual := SHA256Hex(data)
		if norm == "" || !strings.EqualFold(actual, norm) {
			return fmt.Errorf("%w: hydrate blob %s got %s", ErrModelDigestMismatch, digest, actual)
		}
		verified[norm] = append([]byte(nil), data...)
	}
	a.mu.Lock()
	for k, v := range verified {
		a.blobs[k] = v
	}
	a.mu.Unlock()
	return nil
}

func (a *ArtifactMirrorServer) opHealth(_ context.Context, _ *emptyIn) (*NodeHealthOut, error) {
	return &NodeHealthOut{ID: a.ID, Plane: a.Plane, Status: "ok"}, nil
}

func (a *ArtifactMirrorServer) zipPutBlob(c *zip.Ctx) error {
	digest := strings.ToLower(strings.TrimSpace(c.Param("digest")))
	data := c.Body()
	actual := SHA256Hex(data)
	if !strings.EqualFold(actual, digest) {
		return zip.Errorf(http.StatusBadRequest, "%s: got %s want %s", ErrModelDigestMismatch, actual, digest)
	}
	a.mu.Lock()
	a.blobs[digest] = append([]byte(nil), data...)
	a.mu.Unlock()
	return c.JSON(http.StatusOK, map[string]any{"digestSha256": digest, "sizeBytes": len(data)})
}

func (a *ArtifactMirrorServer) zipGetBlob(c *zip.Ctx) error {
	digest := strings.ToLower(strings.TrimSpace(c.Param("digest")))
	a.mu.RLock()
	blob, ok := a.blobs[digest]
	tamper := a.tamperDigests[digest]
	a.mu.RUnlock()
	if !ok {
		return zip.Errorf(http.StatusNotFound, "%s", ErrNotFound.Error())
	}
	out := append([]byte(nil), blob...)
	if tamper && len(out) > 0 {
		out[0] ^= 0xff
	}
	c.SetHeader("Content-Type", "application/octet-stream")
	return c.Bytes(http.StatusOK, out)
}

// ProviderNodeServer is an independent compute worker node running its own HTTP
// inference endpoint with its own ephemeral secp256k1 identity.
type ProviderNodeServer struct {
	ProviderID   string
	KeyPair      *KeyPair
	Domain       EIP712Domain
	HTTPClient   *http.Client
	Server       *httptest.Server
	mu           sync.RWMutex
	modelCache   map[string][]byte
	tamperOutput bool
}

// NewProviderNodeServer starts a real loopback HTTP compute provider node.
func NewProviderNodeServer(providerID string, kp *KeyPair, domain EIP712Domain, client *http.Client) *ProviderNodeServer {
	p := &ProviderNodeServer{
		ProviderID: providerID,
		KeyPair:    kp,
		Domain:     domain,
		HTTPClient: client,
		modelCache: make(map[string][]byte),
	}
	app, err := NewProviderApp(p, nil)
	p.Server = startZipTestServer(app, err)
	return p
}

// URL returns the provider's real HTTP URL.
func (p *ProviderNodeServer) URL() string { return p.Server.URL }

// Close shuts down the provider HTTP listener.
func (p *ProviderNodeServer) Close() { p.Server.Close() }

// SetTamperOutput toggles malicious output corruption for adversarial testing.
func (p *ProviderNodeServer) SetTamperOutput(v bool) {
	p.mu.Lock()
	p.tamperOutput = v
	p.mu.Unlock()
}

// FetchAndVerifyModel downloads content-addressed model weights from mirrorURL
// and verifies SHA-256 before caching them locally.
func (p *ProviderNodeServer) FetchAndVerifyModel(mirrorURL, expectedDigest string) error {
	blob, err := DownloadAndVerifyModelBlob(p.HTTPClient, mirrorURL, expectedDigest)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.modelCache[strings.ToLower(expectedDigest)] = blob
	p.mu.Unlock()
	return nil
}

// BuildSignedAdvertisement constructs a provider-signed ProviderAdvertisement.
func (p *ProviderNodeServer) BuildSignedAdvertisement(modelIDs []string, pricePer1K uint64, capacityRPM uint32, now time.Time) (ProviderAdvertisement, error) {
	ad := ProviderAdvertisement{
		ProviderID:       p.ProviderID,
		OperatorAddress:  p.KeyPair.Address(),
		EndpointURL:      p.URL(),
		ModelIDs:         append([]string(nil), modelIDs...),
		PricePer1KAtomic: pricePer1K,
		CapacityRPM:      capacityRPM,
		IssuedAt:         now.Unix(),
		ExpiresAt:        now.Add(24 * time.Hour).Unix(),
	}
	if err := SignProviderAdvertisement(p.Domain, p.KeyPair, &ad); err != nil {
		return ProviderAdvertisement{}, err
	}
	return ad, nil
}

func (p *ProviderNodeServer) opHealth(_ context.Context, _ *emptyIn) (*ProviderHealthOut, error) {
	return &ProviderHealthOut{
		ProviderID: p.ProviderID,
		Address:    p.KeyPair.Address(),
		Status:     "ok",
	}, nil
}

func (p *ProviderNodeServer) opInfer(_ context.Context, req *InferenceRequest) (*InferenceExecutionReceipt, error) {
	if req == nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", ErrInvalidRequest.Error())
	}
	p.mu.RLock()
	blob, ok := p.modelCache[strings.ToLower(req.ModelDigestSHA256)]
	tamper := p.tamperOutput
	p.mu.RUnlock()
	if !ok {
		return nil, zip.Errorf(http.StatusNotFound, "%s: model %s not loaded on provider %s", ErrNotFound, req.ModelDigestSHA256, p.ProviderID)
	}
	outText, outDigest, tokens := ExecuteDeterministicInference(blob, req.Prompt, req.Seed, req.MaxTokens)
	if tamper {
		outText = outText + " [tampered]"
		outDigest = SHA256Hex([]byte(outText))
	}
	receipt := InferenceExecutionReceipt{
		JobID:              req.JobID,
		ProviderID:         p.ProviderID,
		ProviderAddress:    p.KeyPair.Address(),
		ModelID:            req.ModelID,
		ModelDigestSHA256:  strings.ToLower(req.ModelDigestSHA256),
		InputDigestSHA256:  ComputeRequestInputDigest(*req),
		OutputText:         outText,
		OutputDigestSHA256: outDigest,
		TokensGenerated:    tokens,
		ExecutedAt:         time.Now().UTC().Unix(),
		VerificationClass:  VerificationClassDeterministicHash,
	}
	if err := SignExecutionReceipt(p.Domain, p.KeyPair, &receipt); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "%s", err.Error())
	}
	return &receipt, nil
}

// SchedulerServer is a stateless HTTP job scheduler that discovers eligible
// providers from its configured bootstrap URLs and dispatches inference over HTTP.
type SchedulerServer struct {
	ID            string
	Plane         ControlPlaneKind
	Domain        EIP712Domain
	BootstrapURLs []string
	HTTPClient    *http.Client
	Server        *httptest.Server
	rrCounter     atomic.Uint64
}

// NewSchedulerServer starts a real loopback HTTP scheduler node.
func NewSchedulerServer(id string, plane ControlPlaneKind, domain EIP712Domain, bootstrapURLs []string, client *http.Client) *SchedulerServer {
	s := &SchedulerServer{
		ID:            id,
		Plane:         plane,
		Domain:        domain,
		BootstrapURLs: append([]string(nil), bootstrapURLs...),
		HTTPClient:    client,
	}
	app, err := NewSchedulerApp(s, nil)
	s.Server = startZipTestServer(app, err)
	return s
}

// URL returns the scheduler's HTTP base URL.
func (s *SchedulerServer) URL() string { return s.Server.URL }

// Close shuts down the scheduler HTTP listener.
func (s *SchedulerServer) Close() { s.Server.Close() }

type scheduleDispatchRequest struct {
	Request           InferenceRequest `json:"request"`
	PreferredProvider string           `json:"preferredProvider,omitempty"`
}

func (s *SchedulerServer) opHealth(_ context.Context, _ *emptyIn) (*NodeHealthOut, error) {
	return &NodeHealthOut{ID: s.ID, Plane: s.Plane, Status: "ok"}, nil
}

func (s *SchedulerServer) opSchedule(_ context.Context, in *scheduleDispatchRequest) (*InferenceExecutionReceipt, error) {
	if in == nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", ErrInvalidRequest.Error())
	}
	providers, err := DiscoverPeersFromBootstraps(s.HTTPClient, s.Domain, s.BootstrapURLs, in.Request.ModelID)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%s", err.Error())
	}
	if len(providers) == 0 {
		return nil, zip.Errorf(http.StatusNotFound, "%s: no providers for model %s", ErrNotFound, in.Request.ModelID)
	}
	var chosen ProviderAdvertisement
	if in.PreferredProvider != "" {
		found := false
		for _, p := range providers {
			if p.ProviderID == in.PreferredProvider {
				chosen = p
				found = true
				break
			}
		}
		if !found {
			return nil, zip.Errorf(http.StatusNotFound, "%s: preferred provider %s not found", ErrNotFound, in.PreferredProvider)
		}
	} else {
		idx := s.rrCounter.Add(1) - 1
		chosen = providers[int(idx)%len(providers)]
	}
	var receipt InferenceExecutionReceipt
	if _, err := doJSONRequest(s.HTTPClient, http.MethodPost, chosen.EndpointURL+"/v1/infer", in.Request, &receipt); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "%s", err.Error())
	}
	return &receipt, nil
}

// FacilitatorServer is an independent EIP-712 payment settlement facilitator
// with replay protection, atomic balance conservation, and a hash-chained ledger.
type FacilitatorServer struct {
	ID            string
	Plane         ControlPlaneKind
	KeyPair       *KeyPair
	Domain        EIP712Domain
	Server        *httptest.Server
	mu            sync.RWMutex
	balances      map[string]uint64
	mintedTotal   uint64
	seenPayments  map[string]struct{}
	ledger        []SettlementReceipt
	prevChainHash string
}

// NewFacilitatorServer starts a real loopback HTTP payment facilitator node.
func NewFacilitatorServer(id string, plane ControlPlaneKind, kp *KeyPair, domain EIP712Domain) *FacilitatorServer {
	f := &FacilitatorServer{
		ID:            id,
		Plane:         plane,
		KeyPair:       kp,
		Domain:        domain,
		balances:      make(map[string]uint64),
		seenPayments:  make(map[string]struct{}),
		prevChainHash: "genesis",
	}
	app, err := NewFacilitatorApp(f, nil)
	f.Server = startZipTestServer(app, err)
	return f
}

// URL returns the facilitator HTTP base URL.
func (f *FacilitatorServer) URL() string { return f.Server.URL }

// Close shuts down the facilitator HTTP listener.
func (f *FacilitatorServer) Close() { f.Server.Close() }

func (f *FacilitatorServer) opHealth(_ context.Context, _ *emptyIn) (*FacilitatorHealthOut, error) {
	return &FacilitatorHealthOut{
		ID:      f.ID,
		Plane:   f.Plane,
		Address: f.KeyPair.Address(),
		Status:  "ok",
	}, nil
}

func (f *FacilitatorServer) opMint(_ context.Context, in *MintRequest) (*BalanceOut, error) {
	if in == nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", ErrInvalidRequest.Error())
	}
	norm, err := NormalizeAddress(in.Address)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	f.mu.Lock()
	f.balances[norm] += in.Amount
	f.mintedTotal += in.Amount
	bal := f.balances[norm]
	f.mu.Unlock()
	return &BalanceOut{Address: norm, Balance: bal}, nil
}

func (f *FacilitatorServer) opSettle(_ context.Context, auth *PaymentAuthorization) (*SettlementReceipt, error) {
	if auth == nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", ErrInvalidRequest.Error())
	}
	if err := VerifyPaymentAuthorization(f.Domain, *auth); err != nil {
		return nil, zip.Errorf(http.StatusUnauthorized, "%s", err.Error())
	}
	if auth.ValidBefore > 0 && time.Now().UTC().Unix() > auth.ValidBefore {
		return nil, zip.Errorf(http.StatusBadRequest, "%s: payment authorization expired", ErrInvalidRequest)
	}
	payer, err := NormalizeAddress(auth.PayerAddress)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	payee, err := NormalizeAddress(auth.PayeeAddress)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	f.mu.Lock()
	if _, exists := f.seenPayments[auth.PaymentID]; exists {
		f.mu.Unlock()
		return nil, zip.Errorf(http.StatusConflict, "%s", ErrReplayDetected.Error())
	}
	if f.balances[payer] < auth.AmountAtomic {
		f.mu.Unlock()
		return nil, zip.Errorf(http.StatusPaymentRequired, "%s", ErrInsufficientBalance.Error())
	}
	f.balances[payer] -= auth.AmountAtomic
	f.balances[payee] += auth.AmountAtomic
	f.seenPayments[auth.PaymentID] = struct{}{}

	settledAt := time.Now().UTC().Unix()
	prevHash := f.prevChainHash
	ledgerHash := SHA256Hex(
		[]byte(prevHash),
		[]byte(auth.PaymentID),
		[]byte(auth.JobID),
		[]byte(payer),
		[]byte(payee),
		[]byte(fmt.Sprintf("%d:%d", auth.AmountAtomic, settledAt)),
	)
	receipt := SettlementReceipt{
		PaymentID:          auth.PaymentID,
		JobID:              auth.JobID,
		FacilitatorID:      f.ID,
		FacilitatorAddress: f.KeyPair.Address(),
		PayerAddress:       payer,
		PayeeAddress:       payee,
		AmountAtomic:       auth.AmountAtomic,
		SettledAt:          settledAt,
		PrevLedgerHash:     prevHash,
		LedgerHash:         ledgerHash,
	}
	if err := SignSettlementReceipt(f.Domain, f.KeyPair, &receipt); err != nil {
		// Roll back balance mutation on signing failure.
		f.balances[payee] -= auth.AmountAtomic
		f.balances[payer] += auth.AmountAtomic
		delete(f.seenPayments, auth.PaymentID)
		f.mu.Unlock()
		return nil, zip.Errorf(http.StatusInternalServerError, "%s", err.Error())
	}
	f.prevChainHash = ledgerHash
	f.ledger = append(f.ledger, receipt)
	f.mu.Unlock()
	return &receipt, nil
}

func (f *FacilitatorServer) opBalance(_ context.Context, in *AddressParamIn) (*BalanceOut, error) {
	addr := ""
	if in != nil {
		addr = strings.TrimSpace(in.Address)
	}
	norm, err := NormalizeAddress(addr)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	f.mu.RLock()
	bal := f.balances[norm]
	f.mu.RUnlock()
	return &BalanceOut{Address: norm, Balance: bal}, nil
}

func (f *FacilitatorServer) opLedger(_ context.Context, _ *emptyIn) (*LedgerOut, error) {
	f.mu.RLock()
	out := append([]SettlementReceipt(nil), f.ledger...)
	drift := f.conservationDriftLocked()
	f.mu.RUnlock()
	return &LedgerOut{
		Entries:           out,
		ConservationDrift: drift,
	}, nil
}

func (f *FacilitatorServer) conservationDriftLocked() int64 {
	var sum uint64
	for _, b := range f.balances {
		sum += b
	}
	return int64(sum) - int64(f.mintedTotal)
}

// ConservationDrift returns the exact atomic drift between balances and minted supply.
func (f *FacilitatorServer) ConservationDrift() int64 {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.conservationDriftLocked()
}

// SnapshotState exports the settlement ledger and account balances for fork bundles.
func (f *FacilitatorServer) SnapshotState() ([]SettlementReceipt, map[string]uint64) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	ledger := append([]SettlementReceipt(nil), f.ledger...)
	bals := make(map[string]uint64, len(f.balances))
	for k, v := range f.balances {
		bals[k] = v
	}
	return ledger, bals
}

// HydrateFromFork restores balances and settlement ledger on an independent forked facilitator.
func (f *FacilitatorServer) HydrateFromFork(ledger []SettlementReceipt, bals map[string]uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.balances = make(map[string]uint64, len(bals))
	f.mintedTotal = 0
	for k, v := range bals {
		f.balances[k] = v
		f.mintedTotal += v
	}
	f.ledger = append([]SettlementReceipt(nil), ledger...)
	f.seenPayments = make(map[string]struct{}, len(ledger))
	f.prevChainHash = "genesis"
	for _, s := range ledger {
		f.seenPayments[s.PaymentID] = struct{}{}
		f.prevChainHash = s.LedgerHash
	}
}

// SovereignVaultServer stores client-side AES-256-GCM encrypted user workspace
// envelopes without ever possessing plaintext or user decryption keys.
type SovereignVaultServer struct {
	ID        string
	Plane     ControlPlaneKind
	Domain    EIP712Domain
	Server    *httptest.Server
	mu        sync.RWMutex
	envelopes map[string]EncryptedDataEnvelope
}

// NewSovereignVaultServer starts a real loopback HTTP encrypted data vault.
func NewSovereignVaultServer(id string, plane ControlPlaneKind, domain EIP712Domain) *SovereignVaultServer {
	v := &SovereignVaultServer{
		ID:        id,
		Plane:     plane,
		Domain:    domain,
		envelopes: make(map[string]EncryptedDataEnvelope),
	}
	app, err := NewVaultApp(v, nil)
	v.Server = startZipTestServer(app, err)
	return v
}

// URL returns the vault's base HTTP URL.
func (v *SovereignVaultServer) URL() string { return v.Server.URL }

// Close shuts down the vault HTTP listener.
func (v *SovereignVaultServer) Close() { v.Server.Close() }

func (v *SovereignVaultServer) opHealth(_ context.Context, _ *emptyIn) (*NodeHealthOut, error) {
	return &NodeHealthOut{ID: v.ID, Plane: v.Plane, Status: "ok"}, nil
}

func (v *SovereignVaultServer) opPutSnapshot(_ context.Context, env *EncryptedDataEnvelope) (*StoreSnapshotOut, error) {
	if env == nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", ErrInvalidRequest.Error())
	}
	if err := VerifyEncryptedEnvelope(v.Domain, *env); err != nil {
		if err == ErrInvalidSignature {
			return nil, zip.Errorf(http.StatusUnauthorized, "%s", err.Error())
		}
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	v.mu.Lock()
	if existing, exists := v.envelopes[env.SnapshotID]; exists && !SameAddress(existing.OwnerAddress, env.OwnerAddress) {
		v.mu.Unlock()
		return nil, zip.Errorf(http.StatusForbidden, "%s: snapshot %s is owned by another principal", ErrUnauthorizedDataAccess, env.SnapshotID)
	}
	v.envelopes[env.SnapshotID] = *env
	v.mu.Unlock()
	return &StoreSnapshotOut{SnapshotID: env.SnapshotID, Stored: true}, nil
}

func (v *SovereignVaultServer) opGetSnapshot(_ context.Context, in *IDParamIn) (*EncryptedDataEnvelope, error) {
	id := ""
	if in != nil {
		id = strings.TrimSpace(in.ID)
	}
	v.mu.RLock()
	env, ok := v.envelopes[id]
	v.mu.RUnlock()
	if !ok {
		return nil, zip.Errorf(http.StatusNotFound, "%s", ErrNotFound.Error())
	}
	return &env, nil
}

func (v *SovereignVaultServer) opExportAll(_ context.Context, _ *emptyIn) (*VaultExportOut, error) {
	return &VaultExportOut{Envelopes: v.SnapshotState()}, nil
}

// SnapshotState exports all stored encrypted envelopes in deterministic order.
func (v *SovereignVaultServer) SnapshotState() []EncryptedDataEnvelope {
	v.mu.RLock()
	defer v.mu.RUnlock()
	out := make([]EncryptedDataEnvelope, 0, len(v.envelopes))
	for _, e := range v.envelopes {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SnapshotID < out[j].SnapshotID })
	return out
}

// HydrateFromFork imports encrypted envelopes onto an independent operator vault.
func (v *SovereignVaultServer) HydrateFromFork(envs []EncryptedDataEnvelope) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, e := range envs {
		if existing, exists := v.envelopes[e.SnapshotID]; exists && !SameAddress(existing.OwnerAddress, e.OwnerAddress) {
			continue
		}
		v.envelopes[e.SnapshotID] = e
	}
}

// --- Control Plane A (Hanzo-Operated Convenience Plane) ---

// HanzoControlPlane groups all Hanzo-operated convenience servers so they can
// be cleanly killed together by KillHanzoControlPlane().
type HanzoControlPlane struct {
	Gateway     *httptest.Server
	Registry    *BootstrapServer
	Mirror      *ArtifactMirrorServer
	Scheduler   *SchedulerServer
	Facilitator *FacilitatorServer
	Vault       *SovereignVaultServer
	killed      atomic.Bool
}

func (hp *HanzoControlPlane) opGatewayHealth(_ context.Context, _ *emptyIn) (*HanzoGatewayHealthOut, error) {
	if hp.killed.Load() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%s", ErrHanzoPlaneOffline.Error())
	}
	return &HanzoGatewayHealthOut{
		Service:     "hanzo-gateway",
		Plane:       PlaneHanzo,
		Registry:    hp.Registry.URL(),
		Mirror:      hp.Mirror.URL(),
		Scheduler:   hp.Scheduler.URL(),
		Facilitator: hp.Facilitator.URL(),
		Vault:       hp.Vault.URL(),
	}, nil
}

// NewHanzoControlPlane starts all Hanzo-operated convenience endpoints and
// registers every one of them with guard so they are counted and blocked on kill.
func NewHanzoControlPlane(domain EIP712Domain, guard *HostBlocker) (*HanzoControlPlane, error) {
	facKP, err := GenerateKeyPair()
	if err != nil {
		return nil, err
	}
	client := guard.NewHTTPClient(5 * time.Second)
	reg := NewBootstrapServer("hanzo-registry", PlaneHanzo, domain, 2)
	mir := NewArtifactMirrorServer("hanzo-mirror", PlaneHanzo)
	sched := NewSchedulerServer("hanzo-scheduler", PlaneHanzo, domain, []string{reg.URL()}, client)
	fac := NewFacilitatorServer("hanzo-facilitator", PlaneHanzo, facKP, domain)
	vault := NewSovereignVaultServer("hanzo-vault", PlaneHanzo, domain)

	hp := &HanzoControlPlane{
		Registry:    reg,
		Mirror:      mir,
		Scheduler:   sched,
		Facilitator: fac,
		Vault:       vault,
	}
	gwApp, gwErr := NewHanzoGatewayApp(hp, nil)
	hp.Gateway = startZipTestServer(gwApp, gwErr)

	guard.RegisterHanzoEndpoint(hp.Gateway.URL)
	guard.RegisterHanzoEndpoint(reg.URL())
	guard.RegisterHanzoEndpoint(mir.URL())
	guard.RegisterHanzoEndpoint(sched.URL())
	guard.RegisterHanzoEndpoint(fac.URL())
	guard.RegisterHanzoEndpoint(vault.URL())
	return hp, nil
}

// Kill shuts down every Hanzo-operated HTTP listener immediately.
func (hp *HanzoControlPlane) Kill() {
	if hp == nil {
		return
	}
	if hp.killed.Swap(true) {
		return
	}
	hp.Gateway.Close()
	hp.Scheduler.Close()
	hp.Facilitator.Close()
	hp.Registry.Close()
	hp.Mirror.Close()
	hp.Vault.Close()
}

// --- Control Plane B (Independent Community Operator Plane) ---

// CommunityControlPlane is an independently administered decentralized control
// plane with zero references to Hanzo infrastructure.
type CommunityControlPlane struct {
	OperatorID     string
	OperatorKP     *KeyPair
	Domain         EIP712Domain
	BootstrapNodes []*BootstrapServer
	Mirror         *ArtifactMirrorServer
	Scheduler      *SchedulerServer
	Facilitator    *FacilitatorServer
	Vault          *SovereignVaultServer
}

// NewCommunityControlPlane starts a multi-peer community control plane running
// over independent loopback HTTP listeners.
func NewCommunityControlPlane(operatorID string, domain EIP712Domain, client *http.Client, bootstrapCount int) (*CommunityControlPlane, error) {
	if bootstrapCount < 1 {
		bootstrapCount = 2
	}
	opKP, err := GenerateKeyPair()
	if err != nil {
		return nil, err
	}
	facKP, err := GenerateKeyPair()
	if err != nil {
		return nil, err
	}
	boots := make([]*BootstrapServer, bootstrapCount)
	bootURLs := make([]string, bootstrapCount)
	for i := 0; i < bootstrapCount; i++ {
		boots[i] = NewBootstrapServer(fmt.Sprintf("%s-bootstrap-%d", operatorID, i+1), PlaneCommunity, domain, 2)
		bootURLs[i] = boots[i].URL()
	}
	mir := NewArtifactMirrorServer(operatorID+"-mirror", PlaneCommunity)
	sched := NewSchedulerServer(operatorID+"-scheduler", PlaneCommunity, domain, bootURLs, client)
	fac := NewFacilitatorServer(operatorID+"-facilitator", PlaneCommunity, facKP, domain)
	vault := NewSovereignVaultServer(operatorID+"-vault", PlaneCommunity, domain)

	return &CommunityControlPlane{
		OperatorID:     operatorID,
		OperatorKP:     opKP,
		Domain:         domain,
		BootstrapNodes: boots,
		Mirror:         mir,
		Scheduler:      sched,
		Facilitator:    fac,
		Vault:          vault,
	}, nil
}

// BootstrapURLs returns all community bootstrap HTTP endpoints.
func (cp *CommunityControlPlane) BootstrapURLs() []string {
	urls := make([]string, len(cp.BootstrapNodes))
	for i, b := range cp.BootstrapNodes {
		urls[i] = b.URL()
	}
	return urls
}

// Close shuts down every server in the community plane.
func (cp *CommunityControlPlane) Close() {
	if cp == nil {
		return
	}
	cp.Scheduler.Close()
	cp.Facilitator.Close()
	cp.Mirror.Close()
	cp.Vault.Close()
	for _, b := range cp.BootstrapNodes {
		b.Close()
	}
}

// ExportForkBundle builds and signs a complete portable NetworkForkBundle from
// this community operator's live state, including verified content-addressed
// model artifacts so forks remain self-contained after origin shutdown.
func (cp *CommunityControlPlane) ExportForkBundle() (NetworkForkBundle, error) {
	providers, models, govs := cp.BootstrapNodes[0].SnapshotState()
	ledger, bals := cp.Facilitator.SnapshotState()
	vault := cp.Vault.SnapshotState()
	blobs := cp.Mirror.SnapshotBlobs()

	bundle := NetworkForkBundle{
		ProtocolVersion:  ProtocolVersion,
		ExportedAt:       time.Now().UTC().Unix(),
		OperatorID:       cp.OperatorID,
		OperatorAddress:  cp.OperatorKP.Address(),
		Providers:        providers,
		Models:           models,
		ModelBlobs:       blobs,
		SettlementLedger: ledger,
		Balances:         bals,
		Governance:       govs,
		EncryptedVault:   vault,
	}
	if err := SignForkBundle(cp.Domain, cp.OperatorKP, &bundle); err != nil {
		return NetworkForkBundle{}, err
	}
	return bundle, nil
}

// HydrateFromFork verifies bundle offline and hydrates every component of this
// fresh community operator plane—including its local content-addressed model
// artifact mirror—from the portable snapshot.
func (cp *CommunityControlPlane) HydrateFromFork(bundle NetworkForkBundle) error {
	if err := VerifyForkBundleOffline(cp.Domain, bundle); err != nil {
		return err
	}
	if err := cp.Mirror.HydrateBlobs(bundle.ModelBlobs); err != nil {
		return err
	}
	for _, b := range cp.BootstrapNodes {
		b.HydrateFromFork(bundle.Providers, bundle.Models, bundle.Governance)
	}
	cp.Facilitator.HydrateFromFork(bundle.SettlementLedger, bundle.Balances)
	cp.Vault.HydrateFromFork(bundle.EncryptedVault)
	return nil
}

// --- Decentralized Client Protocol Helpers ---

// DiscoverPeersFromBootstraps queries independent bootstrap peers in order,
// verifies every returned ProviderAdvertisement EIP-712 signature locally, and
// fails closed with ErrBootstrapQuorumLost if zero bootstrap peers are reachable.
func DiscoverPeersFromBootstraps(client *http.Client, domain EIP712Domain, bootstrapURLs []string, modelID string) ([]ProviderAdvertisement, error) {
	if len(bootstrapURLs) == 0 {
		return nil, ErrBootstrapQuorumLost
	}
	reachable := 0
	dedup := make(map[string]ProviderAdvertisement)
	for _, base := range bootstrapURLs {
		target := base + "/v1/peers"
		if modelID != "" {
			target += "?model=" + modelID
		}
		var out struct {
			Providers []ProviderAdvertisement `json:"providers"`
		}
		if _, err := doJSONRequest(client, http.MethodGet, target, nil, &out); err != nil {
			continue
		}
		reachable++
		for _, ad := range out.Providers {
			if err := VerifyProviderAdvertisement(domain, ad); err != nil {
				continue
			}
			dedup[ad.ProviderID] = ad
		}
	}
	if reachable == 0 {
		return nil, ErrBootstrapQuorumLost
	}
	res := make([]ProviderAdvertisement, 0, len(dedup))
	for _, ad := range dedup {
		res = append(res, ad)
	}
	sort.Slice(res, func(i, j int) bool { return res[i].ProviderID < res[j].ProviderID })
	return res, nil
}

// DiscoverModelManifest queries independent bootstrap peers for modelID and
// verifies the publisher's EIP-712 signature locally.
func DiscoverModelManifest(client *http.Client, domain EIP712Domain, bootstrapURLs []string, modelID string) (ModelManifest, error) {
	if len(bootstrapURLs) == 0 {
		return ModelManifest{}, ErrBootstrapQuorumLost
	}
	reachable := 0
	for _, base := range bootstrapURLs {
		var m ModelManifest
		status, err := doJSONRequest(client, http.MethodGet, base+"/v1/models/"+modelID, nil, &m)
		if err != nil {
			if status == http.StatusNotFound {
				reachable++
			}
			continue
		}
		reachable++
		if err := VerifyModelManifest(domain, m); err != nil {
			return ModelManifest{}, err
		}
		return m, nil
	}
	if reachable == 0 {
		return ModelManifest{}, ErrBootstrapQuorumLost
	}
	return ModelManifest{}, ErrNotFound
}

// DownloadAndVerifyModelBlob fetches content-addressed model weight bytes from
// mirrorURL and verifies SHA-256(bytes) == expectedDigest before returning.
func DownloadAndVerifyModelBlob(client *http.Client, mirrorURL, expectedDigest string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, mirrorURL+"/v1/blobs/"+strings.ToLower(expectedDigest), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sovereignty: mirror GET %s returned HTTP %d", expectedDigest, resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	actual := SHA256Hex(data)
	if !strings.EqualFold(actual, expectedDigest) {
		return nil, fmt.Errorf("%w: mirror served %s expected %s", ErrModelDigestMismatch, actual, expectedDigest)
	}
	return data, nil
}

// UploadModelBlob uploads content-addressed model bytes to mirrorURL.
func UploadModelBlob(client *http.Client, mirrorURL, digest string, data []byte) error {
	req, err := http.NewRequest(http.MethodPut, mirrorURL+"/v1/blobs/"+strings.ToLower(digest), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("sovereignty: mirror PUT failed HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func containsString(list []string, target string) bool {
	for _, s := range list {
		if s == target {
			return true
		}
	}
	return false
}
