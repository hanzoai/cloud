package principals

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/iam"
	"github.com/hanzoai/cloud/sdk/secret"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// Concurrent rotations lose no key: every key a rotation answered is in the set
// KMS holds, and the set holds exactly one per rotation.
func TestConcurrentRotationsLoseNoKey(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		const n = 8
		var wg sync.WaitGroup
		kids := make(chan string, n)
		codes := make(chan int, n)
		for range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rq := httptest.NewRequest(http.MethodPost, "/v1/principal/keys", nil)
				rq.Header.Set("X-Org-Id", "admin")
				rq.Header.Set("X-User-Id", "reviewer")
				rq.Header.Set("X-User-Owner", "admin")
				rq.Header.Set("X-User-IsAdmin", "true")
				resp, err := e.app.Test(rq, zip.TestConfig{Timeout: 60 * time.Second, FailOnTimeout: true})
				if err != nil {
					codes <- 0
					return
				}
				defer func() { _ = resp.Body.Close() }()
				codes <- resp.StatusCode
				var set iam.JWKS
				if resp.StatusCode == http.StatusCreated && json.NewDecoder(resp.Body).Decode(&set) == nil && len(set.Keys) > 0 {
					kids <- set.Keys[len(set.Keys)-1].Kid
				}
			}()
		}
		wg.Wait()
		close(kids)
		close(codes)
		for c := range codes {
			if c != http.StatusCreated && c != http.StatusConflict {
				t.Fatalf("a rotation answered %d", c)
			}
		}
		raw, err := mounted.State.keys.kms.GetSecret(context.Background(), keyRef)
		if err != nil {
			t.Fatal(err)
		}
		var held struct {
			Keys []struct{ Kid string } `json:"keys"`
		}
		if err := json.Unmarshal(raw, &held); err != nil {
			t.Fatal(err)
		}
		in := map[string]bool{}
		for _, k := range held.Keys {
			in[k.Kid] = true
		}
		answered := 0
		for kid := range kids {
			answered++
			if !in[kid] {
				t.Fatalf("a rotation answered key %s, which KMS does not hold: its statements would never verify", kid)
			}
		}
		if len(held.Keys) != answered {
			t.Fatalf("KMS holds %d keys for %d rotations that answered", len(held.Keys), answered)
		}
	})
}

// Only the key that signs keeps a private half: a rotation destroys the retired
// key's seed, and no formatter prints key material.
func TestRetiredKeysKeepNoSeed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		e.must(201, http.MethodPost, "/v1/principal/keys", reviewer("admin"), nil, nil)
		e.must(201, http.MethodPost, "/v1/principal/keys", reviewer("admin"), nil, nil)
		raw, err := mounted.State.keys.kms.GetSecret(context.Background(), keyRef)
		if err != nil {
			t.Fatal(err)
		}
		var set keyset
		if err := json.Unmarshal(raw, &set); err != nil {
			t.Fatal(err)
		}
		if len(set.Keys) != 2 || set.Keys[0].Seed != nil || len(set.Keys[1].Seed) == 0 || set.Keys[0].Retired == 0 {
			t.Fatalf("set = %d keys, retired seed %d bytes, signing seed %d bytes", len(set.Keys), len(set.Keys[0].Seed), len(set.Keys[1].Seed))
		}
		seed := set.Keys[1].Seed
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%x"} {
			out := fmt.Sprintf(verb, set) + fmt.Sprintf(verb, set.Keys[1])
			for _, enc := range []string{fmt.Sprintf("%x", seed), base64.StdEncoding.EncodeToString(seed), fmt.Sprint([]byte(seed))} {
				if strings.Contains(out, enc) {
					t.Fatalf("%s prints the signing key's seed", verb)
				}
			}
		}
	})
}

// A key set KMS does not hold is told from one KMS could not read by the error's
// type, never its words.
func TestKeySetAbsenceIsTyped(t *testing.T) {
	ring := func(err error) *keyring {
		return &keyring{kms: failing{err}, log: luxlog.New("test")}
	}
	set, err := ring(fmt.Errorf("kms get: %w", secret.ErrNone)).published(context.Background())
	if err != nil || len(set.Keys) != 0 {
		t.Fatalf("an absent set = %+v, %v; want the empty set", set, err)
	}
	if _, err := ring(errors.New("dial unix kms.sock: secret not found in response")).published(context.Background()); err == nil {
		t.Fatal("an outage whose words say 'secret not found' was read as an absent set")
	}
}

// failing is a KMS whose every read fails with err.
type failing struct{ err error }

func (f failing) GetSecret(context.Context, string) ([]byte, error)    { return nil, f.err }
func (f failing) PutSecret(context.Context, string, []byte) error      { return f.err }
func (f failing) DeleteSecret(context.Context, string) error           { return f.err }
func (f failing) Sign(context.Context, string, []byte) ([]byte, error) { return nil, f.err }
