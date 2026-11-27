package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/sdk/text"
	"gopkg.in/yaml.v3"
)

// staged reports whether path belongs to a capability the fleet has not taken to
// ga, in which case a 404 from an anonymous prober is the CONTRACT rather than a
// miss.
//
// cloud.Stage answers a staged capability with zip's ordinary not-found, chosen
// deliberately over 403 so the refusal is not an existence oracle — and asserted
// byte-for-byte against an unrouted path. So no response can tell the two apart;
// that is the point of it. The only thing that can is the manifest, which is why
// this asks there rather than trying to read the difference off the wire.
//
// Without this, `research` and `admission` answering exactly as designed read as
// five unrouted addresses and failed the release.
func staged(path string) bool {
	owner := manifest.OwnerOf(path)
	return owner != "" && manifest.StageOf(owner) != ""
}

const timeout = 20 * time.Second

// sentinel is substituted for every {param}. Deliberately unmistakable: whatever
// comes back, a reader can tell the id was the probe's and not a real one.
const sentinel = "zzz-reach-probe"

// scratch is the one domain a probe may write to besides this machine: the
// scratch instance, infra/scratch in hanzo-inc/universe.
const scratch = "hanzo.lol"

// production reports whether base is an estate a sentinel must not reach —
// anything but the scratch instance or this machine. An unparseable base is
// production: not knowing where a write lands is reason enough not to send it.
func production(base string) bool {
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" {
		return true
	}
	h := strings.ToLower(u.Hostname())
	return !(h == scratch || strings.HasSuffix(h, "."+scratch) ||
		h == "localhost" || h == "::1" || strings.HasPrefix(h, "127."))
}

// routerMiss is zip/fiber's router-miss body, exactly. This one string is the
// whole reason a parameterised address can be probed at all.
const routerMiss = "404 page not found"

var paramRE = regexp.MustCompile(`\{[^}]+\}`)

type op struct{ method, published, probe string }

// probedOps returns every address worth asking about. Wildcard keys are
// dropped, not filled: a made-up wildcard cannot distinguish "unrouted" from
// "routed, and the value was nonsense".
func probedOps(doc map[string]any) []op {
	paths, _ := doc["paths"].(map[string]any)
	seen := map[op]bool{}
	for path, item := range paths {
		if !strings.HasPrefix(path, "/") || strings.Contains(path, "{wildcard") {
			continue
		}
		methods, _ := item.(map[string]any)
		for method := range methods {
			switch strings.ToLower(method) {
			case "get", "head":
				seen[op{strings.ToUpper(method), path, paramRE.ReplaceAllString(path, sentinel)}] = true
			}
		}
	}
	out := make([]op, 0, len(seen))
	for o := range seen {
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].method != out[j].method {
			return out[i].method < out[j].method
		}
		return out[i].published < out[j].published
	})
	return out
}

// probe returns (status, first 200 bytes of body, server header). A transport
// error — DNS, TLS, timeout — is status 0: not a routing answer.
func probe(client *http.Client, base, method, url string) (int, string, string) {
	req, err := http.NewRequest(method, strings.TrimRight(base, "/")+url, nil)
	if err != nil {
		return 0, err.Error(), ""
	}
	req.Header.Set("accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, truncate(err.Error()), ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
	return resp.StatusCode, string(body), resp.Header.Get("server")
}

func truncate(s string) string { return text.Cut(s, 200) }

// isDark reports whether this answer PROVED the address is unrouted.
//
// The asymmetry is deliberate: a literal address has no made-up value to blame,
// so any 404 condemns it — the rule that caught an edge worker whose 404 carried
// a JSON body.
func isDark(published string, code int, body string) bool {
	if code != 404 {
		return false
	}
	if !strings.Contains(published, "{") {
		return true
	}
	return strings.TrimSpace(body) == routerMiss
}

// checkInstrument proves the oracle before trusting it. It runs on every real
// run and costs nothing.
//
// The whole gate rests on one string, so the string gets a test — and it lives
// HERE rather than in a test file nobody runs, because a measuring instrument
// that is not checked at the moment of measuring is an instrument nobody checks.
// The case that matters most is row 2: an edge worker's 404 carried a JSON body,
// and a body-only rule would have called it healthy.
func checkInstrument() error {
	doc := map[string]any{"paths": map[string]any{
		"/v1/lit":           map[string]any{"get": map[string]any{}},
		"/v1/x/{wildcard1}": map[string]any{"get": map[string]any{}},
	}}
	got := probedOps(doc)
	if len(got) != 1 || got[0] != (op{"GET", "/v1/lit", "/v1/lit"}) {
		return fmt.Errorf("wildcards must drop, got %v", got)
	}

	const handler404 = `{"status":404,"error":"no such repository"}`
	for _, c := range []struct {
		published string
		code      int
		body      string
		want      bool
		why       string
	}{
		{"/v1/lit", 404, routerMiss, true, "literal, router miss"},
		{"/v1/lit", 404, handler404, true, "literal, ANY 404 is dark"},
		{"/v1/lit", 401, "", false, "401 proves routing reached a handler"},
		{"/v1/t/{id}", 404, routerMiss + "\n", true, "parameterised, router miss"},
		{"/v1/t/{id}", 404, handler404, false, "parameterised, the id was made up"},
	} {
		if got := isDark(c.published, c.code, c.body); got != c.want {
			return fmt.Errorf("isDark(%s) = %v, want %v", c.why, got, c.want)
		}
	}

	for base, want := range map[string]bool{
		"https://api.hanzo.ai":           true,
		"https://api.lux.network":        true,
		"https://hanzo.lol.evil.example": true,
		"https://evilhanzo.lol":          true,
		"not a url":                      true,
		"https://api.hanzo.lol":          false,
		"http://127.0.0.1:18080":         false,
		"http://localhost:8080":          false,
	} {
		if got := production(base); got != want {
			return fmt.Errorf("production(%q) = %v, want %v", base, got, want)
		}
	}
	return nil
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: reach <spec.yaml> <base-url> <ratchet-file>")
		os.Exit(2)
	}
	if err := checkInstrument(); err != nil {
		fmt.Fprintf(os.Stderr, "::error::reach's own oracle is wrong: %v\n", err)
		os.Exit(2)
	}
	specPath, base, ratchetPath := os.Args[1], os.Args[2], os.Args[3]

	raw, err := os.ReadFile(specPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "::error::%v\n", err)
		os.Exit(2)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		fmt.Fprintf(os.Stderr, "::error::%s is not valid YAML: %v\n", specPath, err)
		os.Exit(2)
	}

	ratchet := map[string]bool{}
	if rb, err := os.ReadFile(ratchetPath); err == nil {
		for line := range strings.SplitSeq(string(rb), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				ratchet[line] = true
			}
		}
	}

	ops := probedOps(doc)
	left := 0
	if production(base) {
		lit := ops[:0]
		for _, o := range ops {
			if strings.Contains(o.published, "{") {
				left++
				continue
			}
			lit = append(lit, o)
		}
		ops = lit
	}
	asked := map[string]bool{}
	for _, o := range ops {
		asked[o.method+" "+o.published] = true
	}
	client := &http.Client{
		Timeout: timeout,
		// A redirect is a routing answer about a DIFFERENT address; follow it as
		// a browser would so the status reflects what a caller finally gets.
	}

	var mu sync.Mutex
	dark := map[string]string{}
	sem := make(chan struct{}, 16)
	var wg sync.WaitGroup
	for _, o := range ops {
		wg.Add(1)
		go func(o op) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if staged(o.published) {
				return // its 404 is the stage gate, not a miss
			}
			code, body, server := probe(client, base, o.method, o.probe)
			if isDark(o.published, code, body) {
				if server == "" {
					server = "?"
				}
				mu.Lock()
				dark[o.method+" "+o.published] = server
				mu.Unlock()
			}
		}(o)
	}
	wg.Wait()

	var newDark, healed []string
	for k := range dark {
		if !ratchet[k] {
			newDark = append(newDark, k)
		}
	}
	for k := range ratchet {
		if _, still := dark[k]; !still && asked[k] {
			healed = append(healed, k)
		}
	}
	sort.Strings(newDark)
	sort.Strings(healed)

	literal := 0
	for _, o := range ops {
		if !strings.Contains(o.published, "{") {
			literal++
		}
	}
	fmt.Printf("reach: %d addresses probed against %s (%d literal, %d parameterised)\n",
		len(ops), base, literal, len(ops)-literal)
	if left > 0 {
		fmt.Printf("reach: %s is production, so its %d parameterised addresses are left to a run against scratch\n", base, left)
	}
	fmt.Printf("reach: %d dark, %d on the ratchet\n", len(dark), len(ratchet))

	for _, line := range healed {
		fmt.Printf("  HEALED  %s — delete this line from %s\n", line, ratchetPath)
	}
	for _, line := range newDark {
		fmt.Printf("  DARK    %s  (answered by %s)\n", line, dark[line])
	}

	if len(newDark) > 0 {
		fmt.Printf("\n::error::%d address(es) this document publishes are not routed by %s. "+
			"The document is the contract every SDK, the MCP tool list and the CLI are "+
			"generated from, so an address nothing answers is a method every client ships "+
			"and no caller can use. Fix the owner — a stale subset regenerates, a missing "+
			"prefix goes in manifest/apps.go, an edge interceptor gives the path back to "+
			"the origin that can describe it.\n", len(newDark), base)
		os.Exit(1)
	}
	if len(healed) > 0 {
		fmt.Printf("\n::error::%d ratchet line(s) now answer. The ratchet may only shrink, "+
			"and it shrinks by being edited: delete them from %s and commit. A ratchet "+
			"nobody prunes becomes an allowlist.\n", len(healed), ratchetPath)
		os.Exit(1)
	}
	fmt.Println("reach: every published address is routed, and nothing is dark that was not")
}
