package provider

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/secret"
)

// memKMS keeps what is put in it.
type memKMS struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (k *memKMS) GetSecret(_ context.Context, ref string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if v, ok := k.m[ref]; ok {
		return v, nil
	}
	return nil, secret.ErrNone
}

func (k *memKMS) PutSecret(_ context.Context, ref string, v []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.m == nil {
		k.m = map[string][]byte{}
	}
	k.m[ref] = v
	return nil
}

func (k *memKMS) DeleteSecret(_ context.Context, ref string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.m, ref)
	return nil
}

func (k *memKMS) Sign(context.Context, string, []byte) ([]byte, error) { return nil, secret.ErrNone }

// A member of a workspace the org connected chats as the person who installed it
// until they link their own account; their own link, once made, is theirs. With
// no installer linked, the member gets their connect prompt. A command stays the
// linked person's own.
func TestAWorkspaceMemberChatsAsTheInstaller(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := linkService(&memKMS{})
		s.State.store = testStore(t)
		ctx := context.Background()
		if err := s.State.store.Upsert(ctx, Connection{Org: "hanzo", Provider: "slack", ExternalID: "TWORK", Installer: "UZ"}); err != nil {
			t.Fatal(err)
		}

		if _, say, private := chatIdentity(s, "hanzo", "slack", "TWORK", "UASHLEY"); say == "" || !private {
			t.Fatalf("with nobody linked, a member must get their own connect prompt: %q private=%v", say, private)
		}

		if err := putUserLink(s, "hanzo", "slack", "UZ", userLink{Subject: "hanzo/z"}); err != nil {
			t.Fatal(err)
		}
		link, say, _ := chatIdentity(s, "hanzo", "slack", "TWORK", "UASHLEY")
		if say != "" || link.Subject != "hanzo/z" {
			t.Fatalf("an unlinked member = %+v %q, want to chat as the installer hanzo/z", link, say)
		}
		if _, say, _ := channelIdentity(s, "hanzo", "slack", "TWORK", "UASHLEY"); !strings.Contains(say, "not linked") {
			t.Fatalf("a command must stay the linked person's own, got %q", say)
		}
		if _, say, _ := chatIdentity(s, "hanzo", "slack", "TOTHER", "UASHLEY"); say == "" {
			t.Fatal("a workspace this org did not connect lent its member the installer")
		}

		if err := putUserLink(s, "hanzo", "slack", "UASHLEY", userLink{Subject: "hanzo/ashley"}); err != nil {
			t.Fatal(err)
		}
		if link, _, _ := chatIdentity(s, "hanzo", "slack", "TWORK", "UASHLEY"); link.Subject != "hanzo/ashley" {
			t.Fatalf("a linked member = %q, want their own hanzo/ashley", link.Subject)
		}
	})
}
