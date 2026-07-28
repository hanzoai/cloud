// Package runtime is the transport to the bot runtime service — the TS bot that
// executes channels and skills.
//
// It knows how to MOVE BYTES to that service and nothing about what they mean.
// There is no run here, no coding task, no tenant policy: the domains own their
// own wire contracts (clients/bots' stop, clients/coding's task) and express them
// as a Call. So exactly ONE place resolves the base address, mints the
// server-originated identity, frames the stream, bounds a call, and decides
// whether a cleartext hop is allowed.
//
// The edge is transport-agnostic on purpose. A caller states WHAT it wants done
// (Call) and gets back a domain-shaped outcome — never an *http.Response, a
// status code, a header map, or a framing detail. Today those bytes move over
// HTTP; per HIP-0106/HIP-0120 they should move over ZAP, and that swap is meant
// to be a change to THIS package's internals plus each domain's one stub file,
// not a rewrite of the domains.
//
// Dependencies point one way: bots -> runtime, coding -> runtime. runtime imports
// neither, and must not: the moment it knows what a run is, it has stopped being
// a transport.
package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	// urlEnv is the runtime base — the in-cluster service the control plane calls
	// server-side AND the target the /v1/bot/* ops face relays to. ONE knob: a
	// second would let the two disagree about which runtime is "the" runtime.
	urlEnv     = "BOT_GATEWAY_URL"
	defaultURL = "http://bot-gateway.hanzo.svc"

	// plaintextEnv is the operator's explicit opt-out from the cleartext refusal,
	// for a deployment whose runtime hop is secured by mesh mTLS.
	plaintextEnv = "BOT_GATEWAY_ALLOW_PLAINTEXT"

	// tokenEnv is the shared service bearer (KMS-injected) the runtime gates on.
	tokenEnv = "BOT_GATEWAY_TOKEN"

	// lineCap bounds one streamed message: a diffstat or log line can be large but
	// never unbounded, so a hostile or huge message cannot exhaust cloud memory.
	lineCap = 1 << 20

	// errBodyCap bounds the failure detail read back for an error message.
	errBodyCap = 64 << 10

	// readCap bounds a query's answer.
	readCap = 8 << 20

	// callTimeout bounds a unary Call so a hung runtime cannot stall a
	// control-plane request. A Stream is bounded by its ctx instead: it
	// legitimately runs for minutes.
	callTimeout = 15 * time.Second
)

// ErrNotFound reports that the runtime ANSWERED that it holds no such target —
// a structured reply from an operation that exists. It is the transport-agnostic
// form of "absent": a caller decides what absence MEANS for its domain, and that
// decision stays in the domain rather than being read off a status code.
//
// ErrNotServed reports that the runtime does not serve the operation AT ALL — it
// never answered about the target, so nothing was learned about it. The two are
// separated because conflating them is a correctness lie: a runtime build without
// the operation reports absent for EVERY target, and treating that as "already
// gone" turns permanent failure into permanent success. Absence is only ever
// meaningful from a callee that could have said otherwise.
var (
	ErrNotFound  = errors.New("runtime: no such target")
	ErrNotServed = errors.New("runtime: operation not served")
)

// Call is one operation on the runtime.
//
// Op addresses the operation. It is the caller's own identity for what it is
// invoking and is opaque here — the transport carries it, it never interprets it.
// (Under HTTP it is a path; under ZAP it becomes a generated method id. Either
// way it belongs to the domain's stub, which is why runtime holds no table of
// operations and therefore no domain knowledge.)
//
// Org/User are the tenant context cloud has ALREADY resolved and authorized.
// Body is the payload, encoded by the transport; nil sends none. Secret declares
// that Body carries a credential, which forbids a cleartext hop.
type Call struct {
	Op     string
	Org    string
	User   string
	Body   any
	Secret bool
}

// Do invokes c and discards any response payload — the command form. It returns
// ErrNotFound when the runtime answers that the target is absent, ErrNotServed
// when it does not serve the operation, and a bounded descriptive error
// otherwise. Bounded by callTimeout.
func Do(ctx context.Context, c Call) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := send(ctx, c, http.MethodPost, "")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return status(resp)
}

// Read invokes c and decodes its answer into out — the query form. Same error
// vocabulary as Do. Bounded by callTimeout.
func Read(ctx context.Context, c Call, out any) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := send(ctx, c, http.MethodGet, "application/json")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := status(resp); err != nil {
		return err
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, readCap))
	if err != nil {
		return fmt.Errorf("runtime: read answer: %w", err)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("runtime: decode answer: %w", err)
	}
	return nil
}

// Stream invokes c and hands each response message to fn as it arrives — the
// streaming form. Framing is ours; what a message MEANS is the caller's contract,
// so fn receives one encoded message and decodes it itself. A message over
// lineCap ends the stream with an error rather than growing the buffer. The
// deadline is the caller's ctx: a stream legitimately runs for minutes.
func Stream(ctx context.Context, c Call, fn func(msg []byte)) error {
	resp, err := send(ctx, c, http.MethodPost, "application/x-ndjson")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := status(resp); err != nil {
		return err
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), lineCap)
	for sc.Scan() {
		if msg := bytes.TrimSpace(sc.Bytes()); len(msg) > 0 {
			fn(msg)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("runtime: stream read: %w", err)
	}
	return nil
}

// send builds and performs the call: address resolution, the cleartext refusal for
// a credential-bearing payload, payload encoding, and the server-originated
// identity. The identity is MINTED here, never copied from an inbound header, so a
// forged org cannot ride out on a call cloud originates. An absent bearer is left
// off: the runtime then fails the request closed at its own auth gate.
func send(ctx context.Context, c Call, method, accept string) (*http.Response, error) {
	if c.Secret {
		if err := requireSecure(); err != nil {
			return nil, err
		}
	}
	var body io.Reader
	if c.Body != nil {
		b, err := json.Marshal(c.Body)
		if err != nil {
			return nil, fmt.Errorf("runtime: encode payload: %w", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url()+c.Op, body)
	if err != nil {
		return nil, fmt.Errorf("runtime: build call: %w", err)
	}
	if c.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	req.Header.Set("X-Org-Id", c.Org)
	if c.User != "" {
		req.Header.Set("X-User-Id", c.User)
	}
	if tok := getenv(tokenEnv); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("runtime: unreachable: %w", err)
	}
	return resp, nil
}

// client has NO timeout of its own: a unary Do bounds itself with callTimeout and
// a Stream is bounded by its caller's ctx, so a deadline here would cut a
// legitimate long stream.
var client = &http.Client{}

// status maps the runtime's answer onto the transport-agnostic outcome: success,
// ErrNotFound, ErrNotServed, or a bounded descriptive failure.
//
// The absent case is the load-bearing one. A 404 alone proves nothing: an
// unrouted path, a proxy, or an older build with no such operation all answer 404
// for EVERY target, so reading it as "the target is gone" would make absence
// unfalsifiable. Only a STRUCTURED reply — the operation answering, in its own
// error vocabulary, that it holds no such target — is ErrNotFound. A bare 404 is
// ErrNotServed: we learned nothing about the target, and the caller must not
// pretend otherwise.
func status(resp *http.Response) error {
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusNotFound:
		if answered(resp) {
			return ErrNotFound
		}
		return ErrNotServed
	default:
		return fmt.Errorf("runtime: call failed (%d): %s", resp.StatusCode, ErrBody(resp))
	}
}

// answered reports whether a 404 is the OPERATION replying (a JSON object naming
// the error) rather than the transport refusing a path it does not route. The
// runtime's own surfaces answer {"error":"…"}; an unrouted path answers HTML,
// nothing, or a body with no error field.
func answered(resp *http.Response) bool {
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		return false
	}
	var body struct {
		Error string `json:"error"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, errBodyCap))
	if json.Unmarshal(b, &body) != nil {
		return false
	}
	return strings.TrimSpace(body.Error) != ""
}

// ErrBody reads a bounded prefix of a body for an error message.
func ErrBody(resp *http.Response) string {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, errBodyCap))
	return strings.TrimSpace(string(b))
}

// url resolves the runtime base, no trailing slash.
func url() string {
	if v := getenv(urlEnv); v != "" {
		return strings.TrimRight(v, "/")
	}
	return defaultURL
}

// requireSecure refuses a cleartext runtime target for a credential-bearing
// payload. The bearer alone rides every call and is accepted in-cluster, but a
// tenant's OWN secret must not travel in the clear. plaintextEnv=1 is the
// operator's assertion that the hop is mesh-mTLS secured — explicit, documented,
// never a silent default.
//
// This guard exists because the transport does not authenticate its peer. A ZAP
// entry point pins X25519MLKEM768 and refuses a classical-only peer structurally,
// which is what makes this check unnecessary rather than merely satisfied.
func requireSecure() error {
	u := url()
	if strings.HasPrefix(u, "https://") || getenv(plaintextEnv) == "1" {
		return nil
	}
	return fmt.Errorf("runtime: refusing to send a credential over cleartext %q (set %s to https, or %s=1 if the hop is mesh-mTLS secured)",
		u, urlEnv, plaintextEnv)
}

func getenv(key string) string { return strings.TrimSpace(os.Getenv(key)) }
