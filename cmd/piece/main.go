package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/hanzoai/cloud/piece"
	"github.com/hanzoai/cloud/script"
)

func main() {
	src := flag.String("src", "", "checkout of the pieces source (the tree holding packages/pieces)")
	rev := flag.String("rev", "", "provenance recorded in the catalog, as repo@commit")
	out := flag.String("out", ".", "cloud repository root")
	logos := flag.Bool("logos", false, "fetch each piece's logo")
	cache := flag.String("cache", filepath.Join(os.TempDir(), "piece-logos"), "where fetched logos are cached")
	flag.Parse()
	if *src == "" || *rev == "" {
		fmt.Fprintln(os.Stderr, "usage: piece -src <checkout> -rev <repo@commit> [-logos]")
		os.Exit(2)
	}
	if err := run(*src, *rev, *out, *logos, *cache); err != nil {
		fmt.Fprintln(os.Stderr, "piece:", err)
		os.Exit(1)
	}
}

type found struct {
	id, dir string
	core    bool
}

type result struct {
	piece   piece.Piece
	program []byte
	logoURL string
	err     error
}

func run(src, rev, out string, fetch bool, cache string) error {
	dirs, err := discover(src)
	if err != nil {
		return err
	}
	results := make([]result, len(dirs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())
	for i, d := range dirs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, d found) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = ingest(d)
		}(i, d)
	}
	wg.Wait()

	dir := filepath.Join(out, "piece", "catalog")
	programs := filepath.Join(dir, "program")
	logoDir := filepath.Join(dir, "logo")
	for _, d := range []string{programs, logoDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	written := map[string]bool{}
	var pieces []piece.Piece
	var failed []string
	for _, r := range results {
		if r.err != nil {
			failed = append(failed, r.err.Error())
			continue
		}
		p := r.piece
		if r.program != nil {
			// A hand-written program of the same id is the one the runtime
			// already serves; a generated one never replaces it.
			if _, err := os.Stat(filepath.Join(programs, p.ID+".js")); err == nil {
				p.Runnable = true
			} else {
				name := p.ID + ".provider.js"
				if err := os.WriteFile(filepath.Join(programs, name), r.program, 0o644); err != nil {
					return err
				}
				written[name] = true
				p.Runnable = true
			}
		}
		p.Logo = logo(logoDir, cache, p.ID, r.logoURL, fetch)
		pieces = append(pieces, p)
	}
	// A program this run did not produce belongs to a piece that no longer
	// bundles closed, or no longer exists: it must not keep running.
	entries, err := os.ReadDir(programs)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".provider.js") && !written[e.Name()] {
			if err := os.Remove(filepath.Join(programs, e.Name())); err != nil {
				return err
			}
		}
	}
	// Likewise a logo no piece names.
	named := map[string]bool{}
	for _, p := range pieces {
		named[p.Logo] = true
	}
	logos, err := os.ReadDir(logoDir)
	if err != nil {
		return err
	}
	for _, e := range logos {
		if !named[e.Name()] {
			if err := os.Remove(filepath.Join(logoDir, e.Name())); err != nil {
				return err
			}
		}
	}
	sort.Slice(pieces, func(i, j int) bool { return pieces[i].ID < pieces[j].ID })
	if err := writeCatalog(filepath.Join(dir, "catalog.json"), rev, pieces); err != nil {
		return err
	}
	report(pieces, failed)
	if len(failed) > 0 {
		return fmt.Errorf("%d pieces did not describe", len(failed))
	}
	return nil
}

// excluded are the pieces the catalog does not carry, each with why. It is the
// one place a piece is left out, so the omission is a decision on record.
var excluded = map[string]string{
	"activepieces": "the upstream vendor's own platform API: a console of theirs, not an integration",
}

// discover lists every piece directory, core first then community. An id is
// the directory name, and two trees naming the same one is refused: an id is
// the key everything else is addressed by.
func discover(src string) ([]found, error) {
	var out []found
	seen := map[string]string{}
	for _, tree := range []string{"core", "community"} {
		root := filepath.Join(src, "packages", "pieces", tree)
		entries, err := os.ReadDir(root)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			dir := filepath.Join(root, e.Name())
			if !e.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, "src", "index.ts")); err != nil {
				continue
			}
			if why, ok := excluded[e.Name()]; ok {
				fmt.Printf("  excluded: %s — %s\n", e.Name(), why)
				continue
			}
			if prev, dup := seen[e.Name()]; dup {
				return nil, fmt.Errorf("piece id %q is in both %s and %s", e.Name(), prev, tree)
			}
			seen[e.Name()] = tree
			out = append(out, found{id: e.Name(), dir: dir, core: tree == "core"})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no pieces under %s/packages/pieces", src)
	}
	return out, nil
}

// declared is script.Describe's JSON.
type declared struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	LogoURL     string       `json:"logoUrl"`
	Categories  []string     `json:"categories"`
	Deprecated  bool         `json:"deprecated"`
	Auth        []piece.Auth `json:"auth"`
	Actions     []piece.Op   `json:"actions"`
	Triggers    []piece.Op   `json:"triggers"`
}

func ingest(d found) result {
	var pkg struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	raw, err := os.ReadFile(filepath.Join(d.dir, "package.json"))
	if err != nil {
		return result{err: fmt.Errorf("%s: %w", d.id, err)}
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return result{err: fmt.Errorf("%s: package.json: %w", d.id, err)}
	}
	entry := filepath.Join(d.dir, "src", "index.ts")
	open, err := script.BundleOpen(entry)
	if err != nil {
		return result{err: fmt.Errorf("%s: %w", d.id, err)}
	}
	desc, err := script.Describe(open)
	if err != nil {
		return result{err: fmt.Errorf("%s: %w", d.id, err)}
	}
	var dec declared
	if err := json.Unmarshal(desc, &dec); err != nil {
		return result{err: fmt.Errorf("%s: description: %w", d.id, err)}
	}
	p := piece.Piece{
		ID:          d.id,
		Package:     pkg.Name,
		Version:     pkg.Version,
		Name:        ours(strings.TrimSpace(dec.Name)),
		Description: ours(strings.TrimSpace(dec.Description)),
		Categories:  nonNil(dec.Categories),
		Auth:        auths(dec.Auth),
		Actions:     ops(dec.Actions),
		Triggers:    ops(dec.Triggers),
		Core:        d.core,
		Deprecated:  dec.Deprecated,
	}
	if p.Name == "" {
		return result{err: fmt.Errorf("%s: declares no name", d.id)}
	}
	p = callbacks(p)
	r := result{piece: p, logoURL: dec.LogoURL}
	if closed, err := script.Bundle(entry); err == nil && script.Loads(d.id, closed) == nil {
		r.program = closed
	}
	return r
}

func auths(in []piece.Auth) []piece.Auth {
	out := make([]piece.Auth, 0, len(in))
	for _, a := range in {
		a.Type = strings.ToLower(a.Type)
		a.DisplayName, a.Description = ours(a.DisplayName), ours(a.Description)
		a.Props = inputs(a.Props)
		if a.OAuth != nil {
			a.OAuth.Scopes = nonNil(a.OAuth.Scopes)
			if len(a.OAuth.Extra) == 0 {
				a.OAuth.Extra = nil
			}
		}
		if a.Type == "" {
			continue
		}
		out = append(out, a)
	}
	return out
}

func ops(in []piece.Op) []piece.Op {
	out := make([]piece.Op, 0, len(in))
	for _, o := range in {
		o.DisplayName, o.Description = ours(o.DisplayName), ours(o.Description)
		o.Props = inputs(o.Props)
		out = append(out, o)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func inputs(in []piece.Prop) []piece.Prop {
	out := make([]piece.Prop, 0, len(in))
	for _, p := range in {
		if p.Name == "" {
			continue
		}
		p.DisplayName, p.Description = ours(p.DisplayName), ours(p.Description)
		if d, ok := p.Default.(string); ok {
			p.Default = ours(d)
		}
		for i := range p.Options {
			p.Options[i].Label = ours(p.Options[i].Label)
		}
		out = append(out, p)
	}
	return out
}

// upstreamRedirect is the OAuth redirect the source's instructions tell a reader
// to register. Here a connector's OAuth returns to its own callback.
const upstreamRedirect = "https://cloud.activepieces.com/redirect"

// callbacks points every instruction that names the upstream redirect at this
// connector's own callback, which is the address a reader must register.
func callbacks(p piece.Piece) piece.Piece {
	raw, err := json.Marshal(p)
	if err != nil || !bytes.Contains(raw, []byte(upstreamRedirect)) {
		return p
	}
	raw = bytes.ReplaceAll(raw, []byte(upstreamRedirect), []byte("https://api.hanzo.ai/v1/provider/"+p.ID+"/callback"))
	var out piece.Piece
	if json.Unmarshal(raw, &out) != nil {
		return p
	}
	return out
}

// link matches an address inside a piece's prose.
var link = regexp.MustCompile(`https?://[^\s)\]"'>]+`)

// upstream is the upstream product's name as its prose spells it.
var upstream = strings.NewReplacer("ActivePieces", "Hanzo", "Activepieces", "Hanzo", "activepieces", "hanzo")

// ours is a piece's prose as it reads here: where it names the product it runs
// in ("paste the key into the Activepieces field"), that product is Hanzo.
// Addresses are left exactly as written — a link to the source's own docs
// still has to resolve.
func ours(s string) string {
	if !strings.Contains(strings.ToLower(s), "activepieces") {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range link.FindAllStringIndex(s, -1) {
		b.WriteString(upstream.Replace(s[last:m[0]]))
		b.WriteString(s[m[0]:m[1]])
		last = m[1]
	}
	b.WriteString(upstream.Replace(s[last:]))
	return b.String()
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// writeCatalog writes one piece per line: the file is large, and a line per
// piece is what makes a regeneration's diff say which pieces changed.
func writeCatalog(path, rev string, pieces []piece.Piece) error {
	var b bytes.Buffer
	head, _ := json.Marshal(rev)
	fmt.Fprintf(&b, "{\"source\":%s,\"count\":%d,\"pieces\":[\n", head, len(pieces))
	for i, p := range pieces {
		line, err := json.Marshal(p)
		if err != nil {
			return err
		}
		b.Write(line)
		if i < len(pieces)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString("]}\n")
	return os.WriteFile(path, b.Bytes(), 0o644)
}

func report(pieces []piece.Piece, failed []string) {
	kinds := map[string]int{}
	runnable, actions, triggers, logos := 0, 0, 0, 0
	for _, p := range pieces {
		if len(p.Auth) == 0 {
			kinds["none"]++
		}
		for _, a := range p.Auth {
			kinds[a.Type]++
		}
		if p.Runnable {
			runnable++
		}
		if p.Logo != "" {
			logos++
		}
		actions += len(p.Actions)
		triggers += len(p.Triggers)
	}
	fmt.Printf("pieces %d  runnable %d  actions %d  triggers %d  logos %d  auth %v\n",
		len(pieces), runnable, actions, triggers, logos, kinds)
	for _, f := range failed {
		fmt.Println("  failed:", f)
	}
}

// ── logos ────────────────────────────────────────────────────────────────────

const logoPx = 96

// logo returns the file name of the piece's logo under dir, fetching it first
// when asked. A logo that cannot be fetched or read is left out; the card
// falls back to a monogram rather than to someone else's host.
func logo(dir, cache, id, url string, fetch bool) string {
	for _, ext := range []string{".png", ".svg"} {
		if !fetch {
			if _, err := os.Stat(filepath.Join(dir, id+ext)); err == nil {
				return id + ext
			}
		}
	}
	if !fetch || url == "" {
		return ""
	}
	raw, err := cached(cache, url)
	if err != nil {
		fmt.Println("  logo:", id, err)
		return ""
	}
	name, body, err := normalize(id, url, raw)
	if err != nil {
		fmt.Println("  logo:", id, err)
		return ""
	}
	for _, ext := range []string{".png", ".svg"} {
		_ = os.Remove(filepath.Join(dir, id+ext))
	}
	if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
		fmt.Println("  logo:", id, err)
		return ""
	}
	return name
}

var client = &http.Client{Timeout: 30 * time.Second}

func cached(dir, url string) ([]byte, error) {
	sum := sha256.Sum256([]byte(url))
	path := filepath.Join(dir, hex.EncodeToString(sum[:]))
	if b, err := os.ReadFile(path); err == nil {
		return b, nil
	}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return b, os.WriteFile(path, b, 0o644)
}

// unsafeSVG matches what an SVG must not carry to be served from our origin:
// script, event handlers, embedded documents and external references. An
// <img> would not run them, but a logo URL opened directly would.
var unsafeSVG = regexp.MustCompile(`(?i)<script|<foreignobject|<iframe|<embed|<object|\son[a-z]+\s*=|javascript:|xlink:href\s*=\s*["']?https?:|href\s*=\s*["']?https?:|<!entity`)

func normalize(id, url string, raw []byte) (string, []byte, error) {
	if strings.HasSuffix(strings.ToLower(url), ".svg") || bytes.Contains(raw[:min(len(raw), 512)], []byte("<svg")) {
		if unsafeSVG.Match(raw) {
			return "", nil, errors.New("svg carries active content")
		}
		if len(raw) > 64<<10 {
			return "", nil, errors.New("svg larger than 64 KiB")
		}
		return id + ".svg", raw, nil
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return "", nil, err
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > logoPx || h > logoPx {
		if w >= h {
			w, h = logoPx, max(1, h*logoPx/w)
		} else {
			w, h = max(1, w*logoPx/h), logoPx
		}
	}
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, dst); err != nil {
		return "", nil, err
	}
	return id + ".png", buf.Bytes(), nil
}
