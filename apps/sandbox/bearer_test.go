package sandbox

// bearer_test.go — a plane lease that carries the person's own bearer signs its pod
// in as them, exactly as the HTTP lease does; one that carries none starts with no
// session, as every plane lease did before (#907).

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"
)

// exchanges is an IAM that answers RFC 8693 exchanges and remembers each subject
// token it was handed.
type exchanges struct {
	mu       sync.Mutex
	subjects []string
}

func (x *exchanges) seen() []string {
	x.mu.Lock()
	defer x.mu.Unlock()
	return append([]string(nil), x.subjects...)
}

// minted is the token the fake IAM hands back: a different credential from the one it
// was given, naming the same person.
var minted = "eyJhbGciOiJSUzI1NiJ9." +
	base64.RawURLEncoding.EncodeToString([]byte(`{"owner":"acme","name":"alice","email":"alice@acme.test"}`)) + ".bWludGVk"

// iamFor serves exchanges as the deployment's IAM and wires the sandbox client.
func iamFor(t *testing.T) *exchanges {
	t.Helper()
	x := &exchanges{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/iam/oauth/token" || r.ParseForm() != nil || r.Form.Get("client_id") != "hanzo-sandbox" {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		}
		x.mu.Lock()
		x.subjects = append(x.subjects, r.Form.Get("subject_token"))
		x.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"`+minted+`","expires_in":3600}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("IAM_URL", srv.URL)
	t.Setenv("IAM_SANDBOX_CLIENT_ID", "hanzo-sandbox")
	t.Setenv("IAM_SANDBOX_CLIENT_SECRET", "secret")
	return x
}

// tap records every exec into a pod — what ran and what it was handed on stdin — and
// passes it on to the cluster underneath.
type tap struct {
	streamer
	mu    sync.Mutex
	lines []string
	stdin []string
}

func (p *tap) stream(ctx context.Context, ns, pod string, argv []string, stdin io.Reader, stdout, stderr io.Writer) error {
	var in []byte
	if stdin != nil {
		in, _ = io.ReadAll(stdin)
	}
	p.mu.Lock()
	p.lines = append(p.lines, strings.Join(argv, " "))
	p.stdin = append(p.stdin, string(in))
	p.mu.Unlock()
	return p.streamer.stream(ctx, ns, pod, argv, strings.NewReader(string(in)), stdout, stderr)
}

// signIns answers what each `hanzo auth login` into a pod was handed on stdin.
func (p *tap) signIns() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for i, l := range p.lines {
		if strings.Contains(l, "hanzo auth login") {
			out = append(out, p.stdin[i])
		}
	}
	return out
}

func inAcme(sub, name string, admin bool) context.Context {
	owner := "acme"
	if admin {
		owner = "admin"
	}
	return zip.WithCaller(context.Background(), zip.Caller{Org: "acme", Owner: owner, User: sub, Name: name, Admin: admin})
}

// A plane lease carrying the person's bearer reaches sessionFor with it — the token
// is the SUBJECT of the exchange — and the pod is signed in with what IAM minted, on
// stdin, never in argv. Nothing about the bearer comes back in the answer.
//
// MUTATION: hand Lease "" in planeLease and IAM is never asked.
func TestAPlaneLeaseWithABearerSignsThePodIn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, s := owning(t)
		x := iamFor(t)
		p := &tap{streamer: s.State.rt.str}
		s.State.rt.str = p
		const bearer = "person.own.token"

		out, err := planeLease(inAcme("sub-alice", "alice", false), &client.LeaseIn{Class: "exec", Bearer: bearer})
		if err != nil {
			t.Fatalf("plane lease: %v", err)
		}
		if got := x.seen(); len(got) != 1 || got[0] != bearer {
			t.Fatalf("IAM exchanged %q; want the relayed bearer once", got)
		}
		if got := p.signIns(); len(got) != 1 || got[0] != minted {
			t.Fatalf("the pod was signed in with %q; want the minted token on stdin", got)
		}
		p.mu.Lock()
		for _, l := range p.lines {
			if strings.Contains(l, minted) || strings.Contains(l, bearer) {
				t.Errorf("a token reached argv: %s", l)
			}
		}
		p.mu.Unlock()
		if out.ID == "" || strings.Contains(out.Workdir+out.Status+out.Runtime, bearer) {
			t.Errorf("the lease answer is %+v", out)
		}
	})
}

// CONTROL: a plane lease with no bearer starts as every plane lease always has —
// IAM is not asked, nothing signs the pod in, and the lease still succeeds.
func TestAPlaneLeaseWithoutABearerStartsWithNoSession(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, s := owning(t)
		x := iamFor(t)
		p := &tap{streamer: s.State.rt.str}
		s.State.rt.str = p

		if _, err := planeLease(inAcme("sub-alice", "alice", false), &client.LeaseIn{Class: "exec"}); err != nil {
			t.Fatalf("plane lease: %v", err)
		}
		if got := x.seen(); len(got) != 0 {
			t.Fatalf("IAM was asked to exchange %q for a lease that carried no bearer", got)
		}
		if got := p.signIns(); len(got) != 0 {
			t.Fatalf("a lease with no bearer signed its pod in %d times", len(got))
		}
	})
}

// A sandbox first leased with no bearer — a chat session's tool call, say — is signed
// in when its lessee resumes it WITH one, though its pod is already running; resumed
// without one it is left as it was. An admin taking it back with their own bearer does
// not replace its owner's identity with theirs.
//
// MUTATION: drop refreshSession from the warm path and the second assertion fails;
// drop its leased() check and the last one does.
func TestARunningSandboxIsSignedInByItsLesseesBearer(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, s := owning(t)
		x := iamFor(t)
		p := &tap{streamer: s.State.rt.str}
		s.State.rt.str = p
		alice := inAcme("sub-alice", "alice", false)

		first, err := planeLease(alice, &client.LeaseIn{Class: "exec"})
		if err != nil {
			t.Fatalf("plane lease: %v", err)
		}
		if _, err := planeLease(alice, &client.LeaseIn{ID: first.ID}); err != nil {
			t.Fatalf("resume without a bearer: %v", err)
		}
		if len(x.seen()) != 0 || len(p.signIns()) != 0 {
			t.Fatal("a resume with no bearer signed the running pod in")
		}

		again, err := planeLease(alice, &client.LeaseIn{ID: first.ID, Bearer: "alice.own.token"})
		if err != nil {
			t.Fatalf("resume with a bearer: %v", err)
		}
		if again.ID != first.ID {
			t.Fatalf("the resume answered %s, want the running %s", again.ID, first.ID)
		}
		if got := x.seen(); len(got) != 1 || got[0] != "alice.own.token" {
			t.Fatalf("IAM exchanged %q; want alice's bearer", got)
		}
		if got := p.signIns(); len(got) != 1 || got[0] != minted {
			t.Fatalf("the running pod was signed in with %q; want the minted token", got)
		}

		if _, err := planeLease(inAcme("sub-dave", "dave", true), &client.LeaseIn{ID: first.ID, Bearer: "dave.own.token"}); err != nil {
			t.Fatalf("an admin's resume: %v", err)
		}
		if got := x.seen(); len(got) != 1 {
			t.Fatalf("an admin's resume of alice's sandbox exchanged %q", got[1:])
		}
		if got := p.signIns(); len(got) != 1 {
			t.Fatal("an admin's resume signed alice's sandbox in as the admin")
		}
	})
}
