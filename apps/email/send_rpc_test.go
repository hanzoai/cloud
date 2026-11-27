package email

import (
	"context"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
	luxlog "github.com/luxfi/log"
)

func TestPlaneSendIsTheOneCommercialSender(t *testing.T) {
	p := stubPlane(t)
	var sentFrom, sentTo string
	var sentHeaders map[string]string
	res := NewStaticCredentialResolver()
	res.Set("acme", "ses", "access-key-id", "AKIA")
	res.Set("acme", "ses", "secret-access-key", "s")
	res.Set("acme", "ses", "region", "us-east-1")
	res.Set("acme", "ses", "from-email", "default@acme.com")
	h := NewHandlers(nil, NewDeliveryService(res, func(_ context.Context, provider string, creds map[string]string, from string, to []string, subject, html, text string, headers, _ map[string]string) (string, error) {
		sentFrom, sentTo, sentHeaders = from, strings.Join(to, ","), headers
		return "0100", nil
	}, luxlog.Noop()), nil)
	_ = p
	var asked client.Addressee
	was := clearFn
	clearFn = func(ctx context.Context, in *client.Addressee) (*client.Clearance, error) {
		asked = *in
		return was(ctx, in)
	}
	t.Cleanup(func() { clearFn = was })

	out, err := h.planeSend(context.Background(), &client.EmailLetter{Org: "acme", To: "Ada@Example.com", From: "news@acme.com",
		Name: "Acme News", ReplyTo: "hi@acme.com", Subject: "Hello", Text: "Hi", List: "lst_a", Key: "brd:1"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Message != "ses:0100" || out.Refused != "" {
		t.Fatalf("posted %+v", out)
	}
	if asked.List != "lst_a" || asked.Address != "ada@example.com" {
		t.Fatalf("clearance asked %+v", asked)
	}
	if sentTo != "ada@example.com" || sentFrom != `"Acme News" <news@acme.com>` || sentHeaders["Reply-To"] != "hi@acme.com" || sentHeaders["List-Unsubscribe"] == "" {
		t.Fatalf("sent from %q to %q headers %v", sentFrom, sentTo, sentHeaders)
	}

	p.refuse["gone@example.com"] = "unsubscribed"
	out, err = h.planeSend(context.Background(), &client.EmailLetter{Org: "acme", To: "gone@example.com", Subject: "x", Text: "y"})
	if err != nil || out.Refused != "unsubscribed" || out.Message != "" {
		t.Fatalf("refused %+v %v", out, err)
	}
	if _, err := h.planeSend(context.Background(), &client.EmailLetter{To: "a@b.co"}); err == nil {
		t.Fatal("a letter with no org was sent")
	}
}
