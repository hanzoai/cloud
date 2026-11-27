package event

// clouds.go — the address space of the public clouds, as each provider publishes it.
//
// A person browses from a home, an office or a phone, not from a data centre, so a
// request from inside a cloud provider's own ranges is a program: a headless browser
// on a CI runner, a scraper, a monitor. Each provider publishes its ranges at a fixed
// address. They are fetched when the process starts and again every day, held in
// memory as one sorted set, and asked once per request. A provider that cannot be
// reached keeps the last ranges it gave; one that has never answered contributes
// none. Hetzner and OVH publish no list.

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	luxlog "github.com/luxfi/log"
)

// provider is one published list: its name, and how to load it.
type provider struct {
	name string
	load func(context.Context) ([]netip.Prefix, error)
}

var providers = []provider{
	{"aws", from("https://ip-ranges.amazonaws.com/ip-ranges.json", parseAWS)},
	{"google", from("https://www.gstatic.com/ipranges/cloud.json", parseGoogle)},
	{"azure", loadAzure},
	{"oracle", from("https://docs.oracle.com/en-us/iaas/tools/public_ip_ranges.json", parseOracle)},
	{"digitalocean", from("https://digitalocean.com/geo/google.csv", parseGeofeed)},
	{"linode", from("https://geoip.linode.com/", parseGeofeed)},
}

// clouds is the set every request asks. Empty until the first load lands.
var clouds atomic.Pointer[ranges]

var watching sync.Once

// WatchClouds loads the published ranges now and every day after, for the life of
// the process. Idempotent: the processes that classify a request (ingest here, the
// tag config in apps/projects) each call it at mount.
func WatchClouds(log luxlog.Logger) {
	watching.Do(func() {
		go func() {
			last := map[string][]netip.Prefix{}
			for {
				wait := 24 * time.Hour
				if !refresh(context.Background(), log, last) {
					wait = time.Hour
				}
				time.Sleep(wait)
			}
		}()
	})
}

// refresh loads every provider into last, keeping a provider's previous ranges when it
// fails, and publishes the union. It reports whether every provider answered.
func refresh(ctx context.Context, log luxlog.Logger, last map[string][]netip.Prefix) bool {
	all := true
	for _, p := range providers {
		ctx, cancel := context.WithTimeout(ctx, time.Minute)
		got, err := p.load(ctx)
		cancel()
		if err == nil && len(got) == 0 {
			err = errors.New("no ranges")
		}
		if err != nil {
			all = false
			log.Warn("cloud ranges not loaded", "provider", p.name, "err", err)
			continue
		}
		last[p.name] = got
		// Published as each provider lands, so the first is asked within seconds of boot.
		var union []netip.Prefix
		for _, got := range last {
			union = append(union, got...)
		}
		clouds.Store(merge(union))
	}
	return all
}

// from loads one document and parses it.
func from(url string, parse func([]byte) ([]netip.Prefix, error)) func(context.Context) ([]netip.Prefix, error) {
	return func(ctx context.Context) ([]netip.Prefix, error) {
		b, err := get(ctx, url)
		if err != nil {
			return nil, err
		}
		return parse(b)
	}
}

// maxDocument bounds one published list. Azure's, the largest, is about 4 MB.
const maxDocument = 64 << 20

var fetcher = &http.Client{Timeout: time.Minute}

func get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := fetcher.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, res.Status)
	}
	return io.ReadAll(io.LimitReader(res.Body, maxDocument))
}

// parseAWS reads ip-ranges.json.
func parseAWS(b []byte) ([]netip.Prefix, error) {
	var d struct {
		V4 []struct {
			P string `json:"ip_prefix"`
		} `json:"prefixes"`
		V6 []struct {
			P string `json:"ipv6_prefix"`
		} `json:"ipv6_prefixes"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	var s []string
	for _, p := range d.V4 {
		s = append(s, p.P)
	}
	for _, p := range d.V6 {
		s = append(s, p.P)
	}
	return prefixes(s), nil
}

// parseGoogle reads Google Cloud's cloud.json: the ranges its customers run in, not
// Google's own services.
func parseGoogle(b []byte) ([]netip.Prefix, error) {
	var d struct {
		Prefixes []struct {
			V4 string `json:"ipv4Prefix"`
			V6 string `json:"ipv6Prefix"`
		} `json:"prefixes"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	var s []string
	for _, p := range d.Prefixes {
		s = append(s, p.V4, p.V6)
	}
	return prefixes(s), nil
}

// parseOracle reads OCI's public_ip_ranges.json.
func parseOracle(b []byte) ([]netip.Prefix, error) {
	var d struct {
		Regions []struct {
			CIDRs []struct {
				CIDR string `json:"cidr"`
			} `json:"cidrs"`
		} `json:"regions"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	var s []string
	for _, r := range d.Regions {
		for _, c := range r.CIDRs {
			s = append(s, c.CIDR)
		}
	}
	return prefixes(s), nil
}

// parseGeofeed reads an RFC 8805 geofeed, which is how DigitalOcean and Linode publish
// theirs: one prefix per line, first field, with # comments.
func parseGeofeed(b []byte) ([]netip.Prefix, error) {
	r := csv.NewReader(bytes.NewReader(b))
	r.Comment = '#'
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	s := make([]string, 0, len(rows))
	for _, row := range rows {
		s = append(s, row[0])
	}
	return prefixes(s), nil
}

// azureLink is the weekly Service Tags file, named by date on Microsoft's download page.
var azureLink = regexp.MustCompile(`https://download\.microsoft\.com/download/[^"'\s]+/ServiceTags_Public_\d+\.json`)

// loadAzure finds this week's Service Tags file on its download page, then reads it.
func loadAzure(ctx context.Context) ([]netip.Prefix, error) {
	page, err := get(ctx, "https://www.microsoft.com/en-us/download/details.aspx?id=56519")
	if err != nil {
		return nil, err
	}
	link := azureLink.Find(page)
	if link == nil {
		return nil, errors.New("no Service Tags link on the download page")
	}
	b, err := get(ctx, string(link))
	if err != nil {
		return nil, err
	}
	return parseAzure(b)
}

// parseAzure reads the AzureCloud tag, which is the whole of Azure's public cloud.
func parseAzure(b []byte) ([]netip.Prefix, error) {
	var d struct {
		Values []struct {
			Name       string `json:"name"`
			Properties struct {
				Prefixes []string `json:"addressPrefixes"`
			} `json:"properties"`
		} `json:"values"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	for _, v := range d.Values {
		if v.Name == "AzureCloud" {
			return prefixes(v.Properties.Prefixes), nil
		}
	}
	return nil, errors.New("no AzureCloud tag")
}

// prefixes parses what parses and skips the rest: one malformed line in a provider's
// file does not cost the others.
func prefixes(s []string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(s))
	for _, v := range s {
		if p, err := netip.ParsePrefix(strings.TrimSpace(v)); err == nil {
			out = append(out, p.Masked())
		}
	}
	return out
}

// ranges is a set of addresses as sorted, disjoint [lo, hi] spans, one list per family.
type ranges struct {
	v4, v6 []span
}

type span struct{ lo, hi netip.Addr }

// merge folds prefixes into a ranges: sorted, with overlapping and adjacent spans joined.
func merge(ps []netip.Prefix) *ranges {
	r := &ranges{}
	for _, p := range ps {
		if p.Addr().Is4In6() {
			continue
		}
		p = p.Masked()
		s := span{p.Addr(), last(p)}
		if s.lo.Is4() {
			r.v4 = append(r.v4, s)
		} else {
			r.v6 = append(r.v6, s)
		}
	}
	r.v4, r.v6 = join(r.v4), join(r.v6)
	return r
}

func join(ss []span) []span {
	slices.SortFunc(ss, func(a, b span) int { return a.lo.Compare(b.lo) })
	out := ss[:0]
	for _, s := range ss {
		if n := len(out); n > 0 && (s.lo.Compare(out[n-1].hi) <= 0 || s.lo == out[n-1].hi.Next()) {
			if s.hi.Compare(out[n-1].hi) > 0 {
				out[n-1].hi = s.hi
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

// last is the highest address in p.
func last(p netip.Prefix) netip.Addr {
	b := p.Addr().AsSlice()
	for i := p.Bits(); i < len(b)*8; i++ {
		b[i/8] |= 0x80 >> (i % 8)
	}
	out, _ := netip.AddrFromSlice(b)
	return out
}

// has reports whether ip is in the set. A nil set holds nothing.
func (r *ranges) has(ip netip.Addr) bool {
	if r == nil || !ip.IsValid() {
		return false
	}
	ip = ip.Unmap()
	ss := r.v6
	if ip.Is4() {
		ss = r.v4
	}
	// The first span that starts after ip; the one before it is the only candidate.
	i, _ := slices.BinarySearchFunc(ss, ip, func(s span, ip netip.Addr) int {
		if s.lo.Compare(ip) <= 0 {
			return -1
		}
		return 1
	})
	return i > 0 && ss[i-1].hi.Compare(ip) >= 0
}
