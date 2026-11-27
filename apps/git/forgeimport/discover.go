package forgeimport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Item is a repository the forge lists.
type Item struct {
	Owner, Repo string
	SizeKB      int64
}

// Discover lists every repository the credential can see, by walking the forge's search
// (GET /v1/repos/search). The size is the forge's own figure in KiB, used only to order
// the work.
func Discover(ctx context.Context, hc *http.Client, base, token string) ([]Item, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	base = strings.TrimSuffix(base, "/")
	var out []Item
	for page := 1; ; page++ {
		q := url.Values{"limit": {"50"}, "page": {strconv.Itoa(page)}, "sort": {"id"}, "order": {"asc"}}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/repos/search?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		if token != "" {
			req.Header.Set("Authorization", "token "+token)
		}
		req.Header.Set("Accept", "application/json")
		resp, err := hc.Do(req)
		if err != nil {
			return nil, fmt.Errorf("forgeimport: list repositories: %w", err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		_ = resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("forgeimport: list repositories: status %d", resp.StatusCode)
		}
		type repo struct {
			Name  string `json:"name"`
			Size  int64  `json:"size"`
			Owner struct {
				Login string `json:"login"`
			} `json:"owner"`
		}
		var list []repo
		var wrapped struct {
			Data []repo `json:"data"`
		}
		if err := json.Unmarshal(body, &wrapped); err == nil && wrapped.Data != nil {
			list = wrapped.Data
		} else if err := json.Unmarshal(body, &list); err != nil {
			return nil, fmt.Errorf("forgeimport: list repositories: %w", err)
		}
		if len(list) == 0 {
			return out, nil
		}
		for _, r := range list {
			out = append(out, Item{Owner: r.Owner.Login, Repo: r.Name, SizeKB: r.Size})
		}
	}
}

// Register records the items as pending in the registry (existing rows keep their
// state), mapped to their org.
func (im *Importer) Register(ctx context.Context, items []Item) error {
	for _, it := range items {
		err := im.c.Registry.Discover(ctx, Row{
			Owner: it.Owner, Repo: it.Repo, Org: im.c.Org(it.Owner), StoreID: StoreID(it.Owner, it.Repo), SizeKB: it.SizeKB,
		})
		if err != nil {
			return err
		}
	}
	return nil
}
