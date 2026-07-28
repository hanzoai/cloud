// Package digitalocean reads DigitalOcean's billing and infrastructure APIs. DO is
// our PRIMARY venue (a large promotional credit); this client turns the customer
// balance + billing history into money.Cents the finance aggregator folds into gross
// margin and runway, and exposes the account's physical inventory — droplets,
// block-storage volumes, DOKS clusters, load balancers — that the /v1/admin/infra
// board reads.
//
// This is the ONE DigitalOcean client the admin plane uses. A new DO read is a
// method here calling the shared get/send primitive, never a second client.
//
// Auth is a single personal-access token, DO_API_TOKEN, sourced from a KMSSecret on
// the cloud env — NEVER hard-coded. When the token is unset the client is not Ready
// and every read reports the honest not-configured state.
//
// SIGN CONVENTION (from DO's public OpenAPI spec): GET /v2/customers/my/balance
// returns decimal-dollar strings; account_balance carries the accounts-receivable
// sign — POSITIVE = we OWE DO, NEGATIVE = we hold CREDIT. Our promo credit shows as
// a negative account balance, so credit-remaining = -Account. Dollars are converted
// to cents once at this edge; everything downstream is money.Cents.
package digitalocean

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/admin/money"
)

// apiBase is DigitalOcean's public API host. Overridable in tests via NewWithBase.
const apiBase = "https://api.digitalocean.com"

// Client reads DigitalOcean billing with a personal-access token.
type Client struct {
	base  string
	token string // DO_API_TOKEN (secret; never logged)
	http  *http.Client
}

// New builds a DO client against the public API.
func New(token string) *Client { return NewWithBase(apiBase, token) }

// NewWithBase builds a DO client against base (a test may point it at a stub).
func NewWithBase(base, token string) *Client {
	return &Client{
		base:  strings.TrimRight(strings.TrimSpace(base), "/"),
		token: strings.TrimSpace(token),
		http:  &http.Client{Timeout: 15 * time.Second},
	}
}

// Ready reports whether a DO token is present.
func (c *Client) Ready() bool { return c != nil && c.token != "" }

// Balance is the decoded /v2/customers/my/balance, in cents. Account carries DO's
// accounts-receivable sign (positive = owed to DO, negative = credit we hold).
type Balance struct {
	Account     money.Cents
	MonthToDate money.Cents
	Usage       money.Cents
	At          string
}

// balanceWire is the raw DO JSON (all money fields are decimal-dollar strings).
type balanceWire struct {
	MonthToDateBalance string `json:"month_to_date_balance"`
	AccountBalance     string `json:"account_balance"`
	MonthToDateUsage   string `json:"month_to_date_usage"`
	GeneratedAt        string `json:"generated_at"`
}

// Balance fetches the customer balance, converting every dollar string to cents.
func (c *Client) Balance(ctx context.Context) (Balance, error) {
	var out Balance
	if !c.Ready() {
		return out, fmt.Errorf("DO_API_TOKEN not configured")
	}
	body, err := c.get(ctx, "/v2/customers/my/balance")
	if err != nil {
		return out, err
	}
	var w balanceWire
	if err := json.Unmarshal(body, &w); err != nil {
		return out, fmt.Errorf("do balance decode: %w", err)
	}
	return Balance{
		Account:     dollarsToCents(w.AccountBalance),
		MonthToDate: dollarsToCents(w.MonthToDateBalance),
		Usage:       dollarsToCents(w.MonthToDateUsage),
		At:          strings.TrimSpace(w.GeneratedAt),
	}, nil
}

// Entry is one billing-history row (used to build the credit burn-down series).
type Entry struct {
	Description string
	Amount      money.Cents
	Date        string
	Kind        string
	InvoiceID   string
}

// entryWire is the raw DO history row (amount is a decimal-dollar string).
type entryWire struct {
	Description string `json:"description"`
	Amount      string `json:"amount"`
	Date        string `json:"date"`
	Type        string `json:"type"`
	InvoiceID   string `json:"invoice_id"`
}

// History fetches recent billing history. Used only for the burn-down series; a
// failure is non-fatal to the caller (it renders the balance tiles with no series).
func (c *Client) History(ctx context.Context, perPage int) ([]Entry, error) {
	if !c.Ready() {
		return nil, fmt.Errorf("DO_API_TOKEN not configured")
	}
	if perPage <= 0 {
		perPage = 50
	}
	body, err := c.get(ctx, "/v2/customers/my/billing_history?per_page="+strconv.Itoa(perPage))
	if err != nil {
		return nil, err
	}
	var w struct {
		BillingHistory []entryWire `json:"billing_history"`
	}
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("do billing_history decode: %w", err)
	}
	out := make([]Entry, len(w.BillingHistory))
	for i, e := range w.BillingHistory {
		out[i] = Entry{
			Description: e.Description,
			Amount:      dollarsToCents(e.Amount),
			Date:        e.Date,
			Kind:        e.Type,
			InvoiceID:   e.InvoiceID,
		}
	}
	return out, nil
}

// Volume is one DO block-storage volume: capacity + attachment + region. DO's API
// gives capacity and which droplets a volume is attached to, but NOT fill % — the
// caller enriches fill only where a filesystem source (the datastore's own
// system.disks) reports it, and renders an honest "—" everywhere else.
type Volume struct {
	ID         string
	Name       string
	Region     string
	SizeGiB    int
	DropletIDs []int
	// Tags carries DO's resource tags. DOKS stamps `k8s:<cluster-uuid>` on the volumes
	// it provisions, but that tag is ADVISORY ONLY — it survives cluster deletion and
	// is wrong often enough that it must never decide whether a volume is garbage. The
	// only sound liveness test is a PV cross-reference (see clients/admin/infra).
	Tags      []string
	CreatedAt string
}

// volumeWire is the raw DO /v2/volumes row.
type volumeWire struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	SizeGigabytes int    `json:"size_gigabytes"`
	Region        struct {
		Slug string `json:"slug"`
	} `json:"region"`
	DropletIDs []int    `json:"droplet_ids"`
	Tags       []string `json:"tags"`
	CreatedAt  string   `json:"created_at"`
}

// Volumes lists ALL block-storage volumes across the account. Capacity and attachment
// are real; per-volume fill is NOT exposed by DO and stays absent (honest) until a
// filesystem source reports it.
func (c *Client) Volumes(ctx context.Context) ([]Volume, error) {
	rows, err := listAll[volumeWire](ctx, c, "/v2/volumes", "volumes")
	if err != nil {
		return nil, err
	}
	out := make([]Volume, len(rows))
	for i, v := range rows {
		out[i] = Volume{
			ID:         v.ID,
			Name:       v.Name,
			Region:     v.Region.Slug,
			SizeGiB:    v.SizeGigabytes,
			DropletIDs: v.DropletIDs,
			Tags:       v.Tags,
			CreatedAt:  v.CreatedAt,
		}
	}
	return out, nil
}

// Droplet is one DO droplet. LocalDiskGiB is the droplet's own disk, which is
// INCLUDED in MonthlyCents — it is NOT separately billed, and conflating it with
// block storage is how a fleet appears to hold terabytes it never pays for.
type Droplet struct {
	ID           int
	Name         string
	Region       string
	Status       string
	SizeSlug     string
	VCPUs        int
	MemoryMiB    int
	LocalDiskGiB int
	MonthlyCents money.Cents
	CreatedAt    string
	PrivateIP    string
	PublicIP     string
	Tags         []string
	VolumeIDs    []string
}

// dropletWire is the raw DO /v2/droplets row.
type dropletWire struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	SizeSlug string `json:"size_slug"`
	VCPUs    int    `json:"vcpus"`
	Memory   int    `json:"memory"`
	Disk     int    `json:"disk"`
	Size     struct {
		PriceMonthly float64 `json:"price_monthly"`
	} `json:"size"`
	Region struct {
		Slug string `json:"slug"`
	} `json:"region"`
	Networks struct {
		V4 []struct {
			Type      string `json:"type"`
			IPAddress string `json:"ip_address"`
		} `json:"v4"`
	} `json:"networks"`
	CreatedAt string   `json:"created_at"`
	Tags      []string `json:"tags"`
	VolumeIDs []string `json:"volume_ids"`
}

// Droplets lists ALL droplets across the account.
func (c *Client) Droplets(ctx context.Context) ([]Droplet, error) {
	rows, err := listAll[dropletWire](ctx, c, "/v2/droplets", "droplets")
	if err != nil {
		return nil, err
	}
	out := make([]Droplet, len(rows))
	for i, d := range rows {
		dr := Droplet{
			ID:           d.ID,
			Name:         d.Name,
			Region:       d.Region.Slug,
			Status:       d.Status,
			SizeSlug:     d.SizeSlug,
			VCPUs:        d.VCPUs,
			MemoryMiB:    d.Memory,
			LocalDiskGiB: d.Disk,
			MonthlyCents: centsOf(d.Size.PriceMonthly),
			CreatedAt:    d.CreatedAt,
			Tags:         d.Tags,
			VolumeIDs:    d.VolumeIDs,
		}
		for _, n := range d.Networks.V4 {
			switch n.Type {
			case "private":
				dr.PrivateIP = n.IPAddress
			case "public":
				dr.PublicIP = n.IPAddress
			}
		}
		out[i] = dr
	}
	return out, nil
}

// Cluster is one DOKS cluster.
type Cluster struct {
	ID        string
	Name      string
	Region    string
	Version   string
	Status    string
	Pools     []NodePool
	CreatedAt string
}

// NodePool is one DOKS node pool — the ONLY correct way to change a cluster's node
// count. DOKS owns the droplets in a pool: deleting or resizing one directly is undone
// by the pool controller, which recreates it.
type NodePool struct {
	ID    string
	Name  string
	Size  string
	Count int
}

// clusterWire is the raw DO /v2/kubernetes/clusters row.
type clusterWire struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Region  string `json:"region"`
	Version string `json:"version"`
	Status  struct {
		State string `json:"state"`
	} `json:"status"`
	NodePools []struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Size  string `json:"size"`
		Count int    `json:"count"`
	} `json:"node_pools"`
	CreatedAt string `json:"created_at"`
}

// Clusters lists ALL DOKS clusters. This is the authoritative denominator for the
// orphan analysis: a volume may only be called unreferenced once EVERY cluster here
// has been searched for a PV that claims it.
func (c *Client) Clusters(ctx context.Context) ([]Cluster, error) {
	rows, err := listAll[clusterWire](ctx, c, "/v2/kubernetes/clusters", "kubernetes_clusters")
	if err != nil {
		return nil, err
	}
	out := make([]Cluster, len(rows))
	for i, k := range rows {
		cl := Cluster{
			ID:        k.ID,
			Name:      k.Name,
			Region:    k.Region,
			Version:   k.Version,
			Status:    k.Status.State,
			CreatedAt: k.CreatedAt,
			Pools:     make([]NodePool, len(k.NodePools)),
		}
		for j, p := range k.NodePools {
			cl.Pools[j] = NodePool{ID: p.ID, Name: p.Name, Size: p.Size, Count: p.Count}
		}
		out[i] = cl
	}
	return out, nil
}

// Kubeconfig fetches a cluster's admin kubeconfig. DO returns a token-based config
// against the cluster's public https endpoint (never an exec plugin), which the
// caller must still funnel through fleet.SafeRESTConfig before dialing.
func (c *Client) Kubeconfig(ctx context.Context, clusterID string) ([]byte, error) {
	if !c.Ready() {
		return nil, fmt.Errorf("DO_API_TOKEN not configured")
	}
	if strings.TrimSpace(clusterID) == "" {
		return nil, fmt.Errorf("cluster id required")
	}
	return c.get(ctx, "/v2/kubernetes/clusters/"+url.PathEscape(clusterID)+"/kubeconfig")
}

// LoadBalancer is one DO load balancer. DO does not price LBs in the API, so cost is
// derived from the billed unit count (see lbUnitCents).
type LoadBalancer struct {
	ID           string
	Name         string
	Region       string
	Status       string
	IP           string
	SizeUnit     int
	MonthlyCents money.Cents
	DropletIDs   []int
}

// lbWire is the raw DO /v2/load_balancers row.
type lbWire struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	IP     string `json:"ip"`
	Region struct {
		Slug string `json:"slug"`
	} `json:"region"`
	SizeUnit   int   `json:"size_unit"`
	DropletIDs []int `json:"droplet_ids"`
}

// LoadBalancers lists ALL load balancers across the account.
func (c *Client) LoadBalancers(ctx context.Context) ([]LoadBalancer, error) {
	rows, err := listAll[lbWire](ctx, c, "/v2/load_balancers", "load_balancers")
	if err != nil {
		return nil, err
	}
	out := make([]LoadBalancer, len(rows))
	for i, l := range rows {
		units := l.SizeUnit
		if units <= 0 {
			units = 1
		}
		out[i] = LoadBalancer{
			ID:           l.ID,
			Name:         l.Name,
			Region:       l.Region.Slug,
			Status:       l.Status,
			IP:           l.IP,
			SizeUnit:     units,
			MonthlyCents: money.Cents(units) * lbUnitCents,
			DropletIDs:   l.DropletIDs,
		}
	}
	return out, nil
}

// Snapshot is a created block-storage snapshot.
type Snapshot struct {
	ID      string
	Name    string
	SizeGiB int
}

// SnapshotVolume takes a point-in-time snapshot of a volume. This is the "undo" that
// makes a delete recoverable, so the delete path takes one FIRST by default.
func (c *Client) SnapshotVolume(ctx context.Context, volumeID, name string) (Snapshot, error) {
	var out Snapshot
	if !c.Ready() {
		return out, fmt.Errorf("DO_API_TOKEN not configured")
	}
	if strings.TrimSpace(volumeID) == "" {
		return out, fmt.Errorf("volume id required")
	}
	body, err := c.send(ctx, http.MethodPost, "/v2/volumes/"+url.PathEscape(volumeID)+"/snapshots",
		map[string]string{"name": name})
	if err != nil {
		return out, err
	}
	var w struct {
		Snapshot struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			SizeGigabytes int    `json:"size_gigabytes"`
		} `json:"snapshot"`
	}
	if err := json.Unmarshal(body, &w); err != nil {
		return out, fmt.Errorf("do snapshot decode: %w", err)
	}
	return Snapshot{ID: w.Snapshot.ID, Name: w.Snapshot.Name, SizeGiB: w.Snapshot.SizeGigabytes}, nil
}

// DeleteVolume destroys a block-storage volume. Irreversible: callers MUST have
// proven the volume is referenced by no PV in any cluster first.
func (c *Client) DeleteVolume(ctx context.Context, volumeID string) error {
	if !c.Ready() {
		return fmt.Errorf("DO_API_TOKEN not configured")
	}
	if strings.TrimSpace(volumeID) == "" {
		return fmt.Errorf("volume id required")
	}
	_, err := c.send(ctx, http.MethodDelete, "/v2/volumes/"+url.PathEscape(volumeID), nil)
	return err
}

// DeleteDroplet destroys a droplet. Irreversible, and there is no snapshot-first undo
// for a droplet the way there is for a volume: callers MUST have proven the droplet is
// not a DOKS node first (see clients/admin/infra).
func (c *Client) DeleteDroplet(ctx context.Context, dropletID int) error {
	if !c.Ready() {
		return fmt.Errorf("DO_API_TOKEN not configured")
	}
	_, err := c.send(ctx, http.MethodDelete, "/v2/droplets/"+strconv.Itoa(dropletID), nil)
	return err
}

// Action is a queued DO droplet action. DO performs a resize ASYNCHRONOUSLY, so a
// successful call means "accepted", not "done" — the id is what an operator polls.
type Action struct {
	ID     int
	Status string
}

// ResizeDroplet changes a droplet's plan.
//
// disk=true makes the change PERMANENT AND IRREVERSIBLE: the disk grows and the droplet
// can never be resized down again. disk=false resizes CPU/RAM only and is reversible.
//
// DO requires the droplet to be powered off; if it is not, DO refuses and its message
// is surfaced verbatim rather than being retried or worked around.
func (c *Client) ResizeDroplet(ctx context.Context, dropletID int, size string, disk bool) (Action, error) {
	var out Action
	if !c.Ready() {
		return out, fmt.Errorf("DO_API_TOKEN not configured")
	}
	if strings.TrimSpace(size) == "" {
		return out, fmt.Errorf("size slug required")
	}
	body, err := c.send(ctx, http.MethodPost, "/v2/droplets/"+strconv.Itoa(dropletID)+"/actions",
		map[string]any{"type": "resize", "size": strings.TrimSpace(size), "disk": disk})
	if err != nil {
		return out, err
	}
	var w struct {
		Action struct {
			ID     int    `json:"id"`
			Status string `json:"status"`
		} `json:"action"`
	}
	if err := json.Unmarshal(body, &w); err != nil {
		return out, fmt.Errorf("do resize decode: %w", err)
	}
	return Action{ID: w.Action.ID, Status: w.Action.Status}, nil
}

// ResizeVolume grows a block-storage volume.
//
// GROW ONLY, and not by choice: DigitalOcean has no shrink. It resizes the DEVICE and does
// nothing to the filesystem on it, so a volume Kubernetes manages must be grown through its
// PersistentVolumeClaim instead — that path does both, and leaves nothing declaring a stale
// capacity. See infra.ExpandPVC. This call is for the volumes Kubernetes does not manage,
// where the DigitalOcean API is the only thing that holds the size.
func (c *Client) ResizeVolume(ctx context.Context, volumeID, region string, gib int) (Action, error) {
	var out Action
	if !c.Ready() {
		return out, fmt.Errorf("DO_API_TOKEN not configured")
	}
	if strings.TrimSpace(volumeID) == "" {
		return out, fmt.Errorf("volume id required")
	}
	if strings.TrimSpace(region) == "" {
		return out, fmt.Errorf("region required")
	}
	body, err := c.send(ctx, http.MethodPost, "/v2/volumes/"+url.PathEscape(volumeID)+"/actions",
		map[string]any{"type": "resize", "size_gigabytes": gib, "region": strings.TrimSpace(region)})
	if err != nil {
		return out, err
	}
	var w struct {
		Action struct {
			ID     int    `json:"id"`
			Status string `json:"status"`
		} `json:"action"`
	}
	if err := json.Unmarshal(body, &w); err != nil {
		return out, fmt.Errorf("do volume resize decode: %w", err)
	}
	return Action{ID: w.Action.ID, Status: w.Action.Status}, nil
}

// DeleteLoadBalancer destroys a load balancer. Irreversible, and it takes the public IP
// with it: callers MUST have proven no Kubernetes Service still targets it.
func (c *Client) DeleteLoadBalancer(ctx context.Context, lbID string) error {
	if !c.Ready() {
		return fmt.Errorf("DO_API_TOKEN not configured")
	}
	if strings.TrimSpace(lbID) == "" {
		return fmt.Errorf("load balancer id required")
	}
	_, err := c.send(ctx, http.MethodDelete, "/v2/load_balancers/"+url.PathEscape(lbID), nil)
	return err
}

// ScaleNodePool sets a node pool's node count. DO's update endpoint requires the pool's
// name alongside the count — omitting it clears the name, so it is always sent back.
func (c *Client) ScaleNodePool(ctx context.Context, clusterID, poolID, name string, count int) error {
	if !c.Ready() {
		return fmt.Errorf("DO_API_TOKEN not configured")
	}
	if strings.TrimSpace(clusterID) == "" || strings.TrimSpace(poolID) == "" {
		return fmt.Errorf("cluster id and node pool id required")
	}
	_, err := c.send(ctx, http.MethodPut,
		"/v2/kubernetes/clusters/"+url.PathEscape(clusterID)+"/node_pools/"+url.PathEscape(poolID),
		map[string]any{"name": name, "count": count})
	return err
}

// Pagination bounds for every DO collection read: 200 rows a page, a hard 25-page
// cap so a runaway can never loop, and the 8 MiB body ceiling a full droplet page
// needs (a volume page fits in far less).
const (
	perPage    = 200
	maxPages   = 25
	maxBody    = 8 << 20
	maxRespLen = maxBody
)

// lbUnitCents is DO's published price for one load-balancer node ($12/mo). DO does
// not return LB pricing in the API, so this is the one place the rate is written.
const lbUnitCents = money.Cents(1200)

// listAll follows DO's page-number pagination for a collection endpoint, decoding
// rows out of the response's named key. It is the ONE pagination loop in this client
// — every collection read goes through it, so "stop on the short page or the reported
// total" is stated once and cannot drift between endpoints.
func listAll[T any](ctx context.Context, c *Client, path, key string) ([]T, error) {
	if !c.Ready() {
		return nil, fmt.Errorf("DO_API_TOKEN not configured")
	}
	var out []T
	for page := 1; page <= maxPages; page++ {
		body, err := c.get(ctx, fmt.Sprintf("%s?per_page=%d&page=%d", path, perPage, page))
		if err != nil {
			return nil, err
		}
		var w struct {
			Meta struct {
				Total int `json:"total"`
			} `json:"meta"`
		}
		if err := json.Unmarshal(body, &w); err != nil {
			return nil, fmt.Errorf("do %s decode: %w", key, err)
		}
		var keyed map[string]json.RawMessage
		if err := json.Unmarshal(body, &keyed); err != nil {
			return nil, fmt.Errorf("do %s decode: %w", key, err)
		}
		var rows []T
		if raw, ok := keyed[key]; ok && len(raw) > 0 {
			if err := json.Unmarshal(raw, &rows); err != nil {
				return nil, fmt.Errorf("do %s decode: %w", key, err)
			}
		}
		out = append(out, rows...)
		// Stop on the last (short) page, or once we've collected the reported total.
		if len(rows) < perPage || (w.Meta.Total > 0 && len(out) >= w.Meta.Total) {
			break
		}
	}
	return out, nil
}

// get performs one token-authenticated DO GET and returns the raw body.
func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	return c.send(ctx, http.MethodGet, path, nil)
}

// send performs one token-authenticated DO request and returns the raw body. It is
// the single HTTP primitive of this client: every read and every mutation funnels
// through it, so auth, timeouts, the body ceiling and status handling exist once.
func (c *Client) send(ctx context.Context, method, path string, payload any) ([]byte, error) {
	var rdr io.Reader
	if payload != nil {
		enc, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(enc)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("digitalocean unreachable: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRespLen))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// DO returns {"id":"...","message":"..."} on error — surface the message so a
		// failed mutation says WHY, not just a bare status.
		var e struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &e) == nil && strings.TrimSpace(e.Message) != "" {
			return nil, fmt.Errorf("digitalocean status %d: %s", resp.StatusCode, e.Message)
		}
		return nil, fmt.Errorf("digitalocean status %d", resp.StatusCode)
	}
	return body, nil
}

// dollarsToCents parses a DO decimal-dollar string ("23.44", "-40000.00") into
// integer cents, rounding to the nearest cent. A blank/invalid string is 0 — DO
// always sends a value, so this only guards a malformed field, and zero there is
// the honest fallback (never a fabricated amount).
func dollarsToCents(s string) money.Cents {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return centsOf(f)
}

// centsOf rounds decimal dollars to integer cents.
func centsOf(f float64) money.Cents { return money.Cents(math.Round(f * 100)) }
