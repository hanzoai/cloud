package reference

// source.go turns a publisher's bytes into entries. One parser per publisher and
// no default: luxfi/aml pkg/screen fell back to one parser for four different
// list formats on the grounds that they looked similar, and three of the four
// then failed silently every night for months. A format this file does not know
// is an error, never an empty list.
//
// Every parser here is TOTAL over its input in one direction only: it refuses
// bytes it cannot read, and it skips individual rows it cannot read while
// counting them, so a publisher who changes one column does not turn the whole
// set into silence.

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// maxEntries bounds one source's contribution, so a publisher who starts
// serving something enormous costs a refusal rather than the process.
const maxEntries = 200_000

// errEmpty is what every fetched parser returns for a source that yielded
// nothing. It is an error rather than an empty slice because no publisher's list
// of disposable domains, hosting ranges or crawler patterns is empty — zero
// entries means the fetch or the parse is wrong, and the previous version must
// stand.
func errEmpty(name string) error {
	return fmt.Errorf("reference: %s parsed to no entries, which no published list is", name)
}

// parseLines reads a newline-delimited list, ignoring blanks and # comments. The
// value is the same for every member, because a bare list states membership and
// nothing else.
func parseLines(source string, value map[string]string) func([]byte) ([]Entry, error) {
	return func(body []byte) ([]Entry, error) {
		out := make([]Entry, 0, 1024)
		for _, line := range strings.Split(string(body), "\n") {
			line = strings.ToLower(strings.TrimSpace(line))
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if len(out) >= maxEntries {
				return nil, fmt.Errorf("reference: %s carries more than %d entries", source, maxEntries)
			}
			out = append(out, Entry{Key: line, Value: value})
		}
		if len(out) == 0 {
			return nil, errEmpty(source)
		}
		return out, nil
	}
}

// prefix normalises one CIDR (or a bare address, which is its own /32 or /128)
// into the canonical masked form entries are keyed by. A bare address is
// accepted because two of the publishers here list addresses rather than blocks.
func prefix(s string) (netip.Prefix, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return netip.Prefix{}, false
	}
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Masked(), true
	}
	if a, err := netip.ParseAddr(s); err == nil {
		return netip.PrefixFrom(a, a.BitLen()), true
	}
	return netip.Prefix{}, false
}

// block builds one network entry. class is what the address IS (hosting, tor,
// reserved) and operator is who runs it — two separate facts, because "this is a
// datacentre" and "this is Amazon" are different inputs to a rule.
func block(p netip.Prefix, class, operator, region string) Entry {
	v := map[string]string{"class": class}
	if operator != "" {
		v["operator"] = operator
	}
	if region != "" {
		v["region"] = region
	}
	return Entry{Key: p.String(), Value: v}
}

// parseCIDRs reads a newline-delimited CIDR list.
func parseCIDRs(source, class, operator string) func([]byte) ([]Entry, error) {
	return func(body []byte) ([]Entry, error) {
		out := make([]Entry, 0, 256)
		for _, line := range strings.Split(string(body), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			p, ok := prefix(line)
			if !ok {
				continue
			}
			out = append(out, block(p, class, operator, ""))
		}
		if len(out) == 0 {
			return nil, errEmpty(source)
		}
		return out, nil
	}
}

// parseTor reads the bulk exit list: one exit address per line. An exit address
// is not hosting — it is an address whose traffic arrived through a network
// designed to detach it from its origin, which is a different fact and gets its
// own class.
func parseTor(body []byte) ([]Entry, error) {
	out := make([]Entry, 0, 2048)
	for _, line := range strings.Split(string(body), "\n") {
		p, ok := prefix(line)
		if !ok {
			continue
		}
		out = append(out, block(p, "tor", "tor", ""))
	}
	if len(out) == 0 {
		return nil, errEmpty("tor")
	}
	return out, nil
}

func parseAWS(body []byte) ([]Entry, error) {
	var doc struct {
		Prefixes []struct {
			IP      string `json:"ip_prefix"`
			Region  string `json:"region"`
			Service string `json:"service"`
		} `json:"prefixes"`
		V6 []struct {
			IP      string `json:"ipv6_prefix"`
			Region  string `json:"region"`
			Service string `json:"service"`
		} `json:"ipv6_prefixes"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("reference: aws ranges: %w", err)
	}
	out := make([]Entry, 0, len(doc.Prefixes)+len(doc.V6))
	// The AMAZON service row is the union of every other row, so keeping only it
	// gives one entry per block instead of four saying the same thing.
	for _, p := range doc.Prefixes {
		if p.Service != "AMAZON" {
			continue
		}
		if q, ok := prefix(p.IP); ok {
			out = append(out, block(q, "hosting", "aws", p.Region))
		}
	}
	for _, p := range doc.V6 {
		if p.Service != "AMAZON" {
			continue
		}
		if q, ok := prefix(p.IP); ok {
			out = append(out, block(q, "hosting", "aws", p.Region))
		}
	}
	if len(out) == 0 {
		return nil, errEmpty("aws")
	}
	return out, nil
}

func parseGCP(body []byte) ([]Entry, error) {
	var doc struct {
		Prefixes []struct {
			V4    string `json:"ipv4Prefix"`
			V6    string `json:"ipv6Prefix"`
			Scope string `json:"scope"`
		} `json:"prefixes"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("reference: gcp ranges: %w", err)
	}
	out := make([]Entry, 0, len(doc.Prefixes))
	for _, p := range doc.Prefixes {
		for _, raw := range []string{p.V4, p.V6} {
			if q, ok := prefix(raw); ok {
				out = append(out, block(q, "hosting", "gcp", p.Scope))
			}
		}
	}
	if len(out) == 0 {
		return nil, errEmpty("gcp")
	}
	return out, nil
}

func parseOracle(body []byte) ([]Entry, error) {
	var doc struct {
		Regions []struct {
			Region string `json:"region"`
			CIDRs  []struct {
				CIDR string `json:"cidr"`
			} `json:"cidrs"`
		} `json:"regions"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("reference: oracle ranges: %w", err)
	}
	var out []Entry
	for _, r := range doc.Regions {
		for _, c := range r.CIDRs {
			if q, ok := prefix(c.CIDR); ok {
				out = append(out, block(q, "hosting", "oracle", r.Region))
			}
		}
	}
	if len(out) == 0 {
		return nil, errEmpty("oracle")
	}
	return out, nil
}

func parseFastly(body []byte) ([]Entry, error) {
	var doc struct {
		V4 []string `json:"addresses"`
		V6 []string `json:"ipv6_addresses"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("reference: fastly ranges: %w", err)
	}
	out := make([]Entry, 0, len(doc.V4)+len(doc.V6))
	for _, raw := range append(append([]string{}, doc.V4...), doc.V6...) {
		if q, ok := prefix(raw); ok {
			out = append(out, block(q, "hosting", "fastly", ""))
		}
	}
	if len(out) == 0 {
		return nil, errEmpty("fastly")
	}
	return out, nil
}

// geofeed reads the RFC 8805 self-published geofeed both Linode and
// DigitalOcean serve: prefix, country, region, city, postal code. Only the first
// three columns are kept — a city is not a risk signal and a postal code is
// closer to personal data than to one.
func geofeed(source, operator string) func([]byte) ([]Entry, error) {
	return func(body []byte) ([]Entry, error) {
		var out []Entry
		for _, line := range strings.Split(string(body), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			cols := strings.Split(line, ",")
			p, ok := prefix(cols[0])
			if !ok {
				continue
			}
			region := ""
			if len(cols) > 1 {
				region = strings.TrimSpace(cols[1])
			}
			out = append(out, block(p, "hosting", operator, region))
		}
		if len(out) == 0 {
			return nil, errEmpty(source)
		}
		return out, nil
	}
}

var (
	parseLinode       = geofeed("linode", "linode")
	parseDigitalOcean = geofeed("digitalocean", "digitalocean")
)

// parseSpecial reads an IANA special-purpose address registry. These are the
// blocks no public host may legitimately be reached at, so an inbound
// connection claiming one is a claim about the world that is not true.
//
// A footnote marker rides on some blocks ("192.0.0.0/29[2]") and one row can
// carry several blocks in one field, so the cell is split and each part is
// stripped before parsing.
func parseSpecial(body []byte) ([]Entry, error) {
	r := csv.NewReader(strings.NewReader(string(body)))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("reference: iana special registry: %w", err)
	}
	var out []Entry
	for i, row := range rows {
		if i == 0 || len(row) < 2 {
			continue
		}
		name := strings.Trim(strings.TrimSpace(row[1]), `"`)
		for _, part := range strings.Split(row[0], ",") {
			if cut := strings.IndexByte(part, '['); cut >= 0 {
				part = part[:cut]
			}
			p, ok := prefix(part)
			if !ok {
				continue
			}
			e := block(p, "reserved", "iana", "")
			e.Value["name"] = name
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		return nil, errEmpty("iana special registry")
	}
	return out, nil
}

// parseASN reads the IANA autonomous system number registry. Rows address a
// RANGE of numbers ("1877-1901"), so entries are keyed by the range and matched
// numerically — see MatchRange.
//
// What this set answers is narrow and worth being precise about: whether a
// number has been delegated at all, and to which regional registry. It does NOT
// answer whether the operator behind it is trustworthy; that is the reputation
// seam.
func parseASN(body []byte) ([]Entry, error) {
	r := csv.NewReader(strings.NewReader(string(body)))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("reference: iana as-numbers: %w", err)
	}
	var out []Entry
	for i, row := range rows {
		if i == 0 || len(row) < 2 {
			continue
		}
		lo, hi, ok := span(row[0])
		if !ok {
			continue
		}
		desc := strings.TrimSpace(row[1])
		out = append(out, Entry{
			Key: fmt.Sprintf("%d-%d", lo, hi),
			Value: map[string]string{
				"registry": registry(desc),
				"status":   status(desc),
			},
		})
	}
	if len(out) == 0 {
		return nil, errEmpty("iana as-numbers")
	}
	return out, nil
}

// span reads "1877-1901" or "0" into a closed numeric interval.
func span(s string) (lo, hi uint64, ok bool) {
	s = strings.TrimSpace(s)
	a, b, dash := strings.Cut(s, "-")
	lo, err := strconv.ParseUint(strings.TrimSpace(a), 10, 32)
	if err != nil {
		return 0, 0, false
	}
	if !dash {
		return lo, lo, true
	}
	hi, err = strconv.ParseUint(strings.TrimSpace(b), 10, 32)
	if err != nil {
		return 0, 0, false
	}
	if hi < lo {
		return 0, 0, false
	}
	return lo, hi, true
}

// registry names the regional registry a block was delegated to, from the
// registry's own description ("Assigned by ARIN").
func registry(desc string) string {
	for _, rir := range []string{"ARIN", "RIPE NCC", "APNIC", "LACNIC", "AFRINIC"} {
		if strings.Contains(strings.ToUpper(desc), rir) {
			return strings.ToLower(strings.ReplaceAll(rir, " NCC", ""))
		}
	}
	return ""
}

// status is whether the block is delegated, reserved or held back. An
// autonomous system number that IANA has not delegated cannot legitimately
// appear in routing, so a claim naming one is false on its face.
func status(desc string) string {
	d := strings.ToLower(desc)
	switch {
	case strings.Contains(d, "assigned by"):
		return "delegated"
	case strings.Contains(d, "reserved"):
		return "reserved"
	case strings.Contains(d, "unallocated"), strings.Contains(d, "available"):
		return "unallocated"
	default:
		return "other"
	}
}

// parseCrawlers reads the crawler-user-agents catalogue. Its `pattern` field is
// a regular expression by design, so it is carried through as one and compiled
// when the snapshot is built — rewriting it as a literal would drop the
// alternations and anchors the publisher put there on purpose.
func parseCrawlers(body []byte) ([]Entry, error) {
	var doc []struct {
		Pattern string `json:"pattern"`
		URL     string `json:"url"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("reference: crawler patterns: %w", err)
	}
	out := make([]Entry, 0, len(doc))
	for _, c := range doc {
		p := strings.TrimSpace(c.Pattern)
		if p == "" {
			continue
		}
		e := Entry{Key: p, Value: map[string]string{"class": "crawler"}}
		if c.URL != "" {
			e.Value["about"] = c.URL
		}
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil, errEmpty("crawler patterns")
	}
	return out, nil
}
