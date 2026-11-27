package forgeimport

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
)

// ErrTooLarge is a pack over the importer's limit.
var ErrTooLarge = errors.New("forgeimport: pack is larger than the importer takes")

// Remote is one repository on a forge, read over smart HTTP. It speaks protocol v0,
// which every git server answers, with side-band-64k and no progress.
type Remote struct {
	URL      string // https://git.hanzo.ai/owner/repo.git
	User     string // basic auth; empty reads anonymously
	Password string
	HTTP     *http.Client
	Throttle *Throttle
	// MaxPack bounds the pack a Fetch will hold; 0 is unbounded.
	MaxPack int64
}

// Advert is what a server says it has.
type Advert struct {
	// Refs is every ref but HEAD, with peeled tag lines left out.
	Refs map[string]plumbing.Hash
	// Head is the branch HEAD names ("refs/heads/main"), "" when the server says none.
	Head string
	// HeadHash is what HEAD resolves to, zero for an unborn HEAD.
	HeadHash plumbing.Hash
	caps     map[string]bool
}

func (r *Remote) client() *http.Client {
	if r.HTTP != nil {
		return r.HTTP
	}
	return http.DefaultClient
}

func (r *Remote) do(ctx context.Context, method, path, ctype string, body []byte) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(r.URL, "/")+path, rd)
	if err != nil {
		return nil, err
	}
	if r.User != "" || r.Password != "" {
		req.SetBasicAuth(r.User, r.Password)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
		req.Header.Set("Accept", strings.Replace(ctype, "-request", "-result", 1))
	}
	req.Header.Set("User-Agent", "hanzo-forgeimport")
	resp, err := r.client().Do(req)
	if err != nil {
		return nil, r.redact(err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("forgeimport: %s %s: status %d", method, r.URL, resp.StatusCode)
	}
	return resp, nil
}

func (r *Remote) redact(err error) error {
	if r.Password != "" {
		return errors.New(strings.ReplaceAll(err.Error(), r.Password, "***"))
	}
	return err
}

// Refs asks the server what it has (git ls-remote).
func (r *Remote) Refs(ctx context.Context) (*Advert, error) {
	resp, err := r.do(ctx, http.MethodGet, "/info/refs?service=git-upload-pack", "", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return nil, r.redact(err)
	}
	a := &Advert{Refs: map[string]plumbing.Hash{}, caps: map[string]bool{}}
	first := true
	for len(body) > 0 {
		line, rest, flush, err := pkt(body)
		if err != nil {
			return nil, err
		}
		body = rest
		if flush || strings.HasPrefix(string(line), "# service=") {
			continue
		}
		text := strings.TrimSuffix(string(line), "\n")
		if first {
			first = false
			if i := strings.IndexByte(text, 0); i >= 0 {
				for _, c := range strings.Fields(text[i+1:]) {
					k, v, _ := strings.Cut(c, "=")
					a.caps[k] = true
					if k == "symref" {
						if from, to, ok := strings.Cut(v, ":"); ok && from == "HEAD" {
							a.Head = to
						}
					}
				}
				text = text[:i]
			}
		}
		oid, name, ok := strings.Cut(text, " ")
		if !ok || len(oid) != 40 {
			return nil, fmt.Errorf("forgeimport: bad ref line %q", text)
		}
		if strings.HasSuffix(name, "^{}") {
			continue
		}
		h := plumbing.NewHash(oid)
		if name == "HEAD" {
			a.HeadHash = h
			continue
		}
		a.Refs[name] = h
	}
	return a, nil
}

// Fetch asks for a pack holding everything wants reach that haves do not. The pack is
// held in memory, bounded by MaxPack.
func (r *Remote) Fetch(ctx context.Context, a *Advert, wants, haves []plumbing.Hash) ([]byte, error) {
	if len(wants) == 0 {
		return nil, errors.New("forgeimport: fetch with nothing wanted")
	}
	if !a.caps["side-band-64k"] {
		return nil, errors.New("forgeimport: the server does not speak side-band-64k")
	}
	var caps []string
	for _, c := range []string{"side-band-64k", "ofs-delta", "no-progress", "thin-pack"} {
		if c == "thin-pack" && len(haves) == 0 {
			continue
		}
		if a.caps[c] {
			caps = append(caps, c)
		}
	}
	var req bytes.Buffer
	for i, w := range wants {
		line := "want " + w.String()
		if i == 0 {
			line += " " + strings.Join(append(caps, "agent=hanzo-forgeimport"), " ")
		}
		writePkt(&req, line+"\n")
	}
	req.WriteString("0000")
	for _, h := range haves {
		writePkt(&req, "have "+h.String()+"\n")
	}
	writePkt(&req, "done\n")
	resp, err := r.do(ctx, http.MethodPost, "/git-upload-pack", "application/x-git-upload-pack-request", req.Bytes())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body := io.Reader(resp.Body)
	if r.Throttle != nil {
		body = r.Throttle.Reader(ctx, body)
	}
	var pack bytes.Buffer
	var hdr [4]byte
	for {
		if _, err := io.ReadFull(body, hdr[:]); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("forgeimport: reading pack: %w", r.redact(err))
		}
		n, err := strconv.ParseUint(string(hdr[:]), 16, 16)
		if err != nil {
			return nil, fmt.Errorf("forgeimport: bad packet length %q", hdr[:])
		}
		if n == 0 {
			continue
		}
		if n < 4 {
			return nil, fmt.Errorf("forgeimport: bad packet length %d", n)
		}
		data := make([]byte, n-4)
		if _, err := io.ReadFull(body, data); err != nil {
			return nil, fmt.Errorf("forgeimport: reading pack: %w", r.redact(err))
		}
		if len(data) == 0 {
			continue
		}
		switch {
		case bytes.HasPrefix(data, []byte("NAK")), bytes.HasPrefix(data, []byte("ACK ")):
		case data[0] == 1:
			if r.MaxPack > 0 && int64(pack.Len()+len(data)) > r.MaxPack {
				return nil, ErrTooLarge
			}
			pack.Write(data[1:])
		case data[0] == 2:
		case data[0] == 3:
			return nil, fmt.Errorf("forgeimport: server: %s", strings.TrimSpace(string(data[1:])))
		default:
			return nil, fmt.Errorf("forgeimport: unexpected packet %q", data[:min(16, len(data))])
		}
	}
	if pack.Len() < 32 || string(pack.Bytes()[:4]) != "PACK" {
		return nil, fmt.Errorf("forgeimport: no pack in the answer (%d bytes)", pack.Len())
	}
	return pack.Bytes(), nil
}

// PackObjects is the object count a pack's header states.
func PackObjects(pack []byte) int64 {
	if len(pack) < 12 {
		return 0
	}
	return int64(binary.BigEndian.Uint32(pack[8:12]))
}

func writePkt(b *bytes.Buffer, s string) { fmt.Fprintf(b, "%04x%s", len(s)+4, s) }

// pkt reads one packet line off b.
func pkt(b []byte) (line, rest []byte, flush bool, err error) {
	if len(b) < 4 {
		return nil, nil, false, errors.New("forgeimport: short packet")
	}
	n, err := strconv.ParseUint(string(b[:4]), 16, 16)
	if err != nil {
		return nil, nil, false, fmt.Errorf("forgeimport: bad packet length %q", b[:4])
	}
	if n == 0 {
		return nil, b[4:], true, nil
	}
	if n < 4 || int(n) > len(b) {
		return nil, nil, false, errors.New("forgeimport: truncated packet")
	}
	return b[4:n], b[n:], false, nil
}

// Throttle bounds the bytes per second read through it, across every reader.
type Throttle struct {
	mu   sync.Mutex
	rate float64 // bytes per second
	next time.Time
}

// NewThrottle bounds reads to bytesPerSec; 0 or less is no bound (nil).
func NewThrottle(bytesPerSec int64) *Throttle {
	if bytesPerSec <= 0 {
		return nil
	}
	return &Throttle{rate: float64(bytesPerSec)}
}

// Reader wraps r.
func (t *Throttle) Reader(ctx context.Context, r io.Reader) io.Reader {
	return &throttled{t: t, ctx: ctx, r: r}
}

type throttled struct {
	t   *Throttle
	ctx context.Context
	r   io.Reader
}

func (w *throttled) Read(p []byte) (int, error) {
	if len(p) > 64<<10 {
		p = p[:64<<10]
	}
	n, err := w.r.Read(p)
	if n > 0 {
		w.t.mu.Lock()
		now := time.Now()
		if w.t.next.Before(now) {
			w.t.next = now
		}
		wait := time.Until(w.t.next)
		w.t.next = w.t.next.Add(time.Duration(float64(n) / w.t.rate * float64(time.Second)))
		w.t.mu.Unlock()
		if wait > 0 {
			select {
			case <-time.After(wait):
			case <-w.ctx.Done():
				return n, w.ctx.Err()
			}
		}
	}
	return n, err
}
