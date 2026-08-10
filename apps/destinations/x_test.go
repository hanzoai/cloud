package destinations

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// xStubOAuth pins the nonce + timestamp for a deterministic signature, restoring both
// on the returned cleanup.
func xStubOAuth(nonce string, ts int64) func() {
	on, ot := xNonce, xTimestamp
	xNonce = func() string { return nonce }
	xTimestamp = func() int64 { return ts }
	return func() { xNonce = on; xTimestamp = ot }
}

// xSigFromHeader extracts and percent-decodes the oauth_signature from an OAuth header.
func xSigFromHeader(hdr string) string {
	const mark = `oauth_signature="`
	_, rest, ok := strings.Cut(hdr, mark)
	if !ok {
		return ""
	}
	raw, _, closed := strings.Cut(rest, `"`)
	if !closed {
		return ""
	}
	dec, err := url.PathUnescape(raw)
	if err != nil {
		return ""
	}
	return dec
}

// TestXOAuthSignature cross-checks the OAuth 1.0a signer: it independently reconstructs
// the signature base string + signing key and recomputes the HMAC-SHA1, so a bug in the
// base-string assembly (param ordering, separators, a double-encode) fails here rather
// than silently producing a 401 in production.
func TestXOAuthSignature(t *testing.T) {
	defer xStubOAuth("nonceABC", 1700000000)()
	endpoint := "https://ads-api.x.com/12/measurement/conversions/o1abc"
	c := xCreds{ConsumerKey: "CK", ConsumerSecret: "CS", AccessToken: "AT", AccessSecret: "ATS"}
	hdr := xAuthHeader(http.MethodPost, endpoint, c)

	// The six oauth_* params in sorted order, "k=v" joined by & — every value here is
	// already in the RFC-3986 unreserved set, so it encodes to itself.
	pstr := strings.Join([]string{
		"oauth_consumer_key=CK",
		"oauth_nonce=nonceABC",
		"oauth_signature_method=HMAC-SHA1",
		"oauth_timestamp=1700000000",
		"oauth_token=AT",
		"oauth_version=1.0",
	}, "&")
	base := "POST&" + oauthEnc(endpoint) + "&" + oauthEnc(pstr)
	mac := hmac.New(sha1.New, []byte("CS&ATS"))
	mac.Write([]byte(base))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if got := xSigFromHeader(hdr); got != want {
		t.Fatalf("oauth_signature = %q, want %q\nheader: %s", got, want, hdr)
	}
	for _, must := range []string{
		"OAuth ", `oauth_consumer_key="CK"`, `oauth_nonce="nonceABC"`,
		`oauth_signature_method="HMAC-SHA1"`, `oauth_token="AT"`, `oauth_version="1.0"`,
	} {
		if !strings.Contains(hdr, must) {
			t.Errorf("header missing %q: %s", must, hdr)
		}
	}
}

func TestXSendEndToEnd(t *testing.T) {
	defer xStubOAuth("fixednonce123", 1700000000)()
	var gotAuth, gotPath string
	var gotBody xConvBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	old := xAdsAPI
	xAdsAPI = srv.URL
	defer func() { xAdsAPI = old }()

	creds := `{"consumer_key":"CK","consumer_secret":"CS","access_token":"AT","access_token_secret":"ATS"}`
	res, err := xDest{}.Send(context.Background(), Config{"pixelId": "o1abc"}, creds,
		[]Conversion{{Standard: EventPurchase, Name: "order_completed", Value: 9, Currency: "USD", EventID: "e1",
			User: UserData{Email: "a@b.com", Clicks: map[string]string{"twclid": "tw1"}}}})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Sent != 1 {
		t.Fatalf("sent = %d", res.Sent)
	}
	if gotPath != "/measurement/conversions/o1abc" {
		t.Errorf("path = %q, want /measurement/conversions/o1abc", gotPath)
	}
	// The request is OAuth1-signed with our pinned nonce/timestamp and a signature.
	if !strings.HasPrefix(gotAuth, "OAuth ") ||
		!strings.Contains(gotAuth, `oauth_nonce="fixednonce123"`) ||
		!strings.Contains(gotAuth, `oauth_timestamp="1700000000"`) ||
		!strings.Contains(gotAuth, "oauth_signature=") {
		t.Errorf("oauth header malformed: %s", gotAuth)
	}
	if len(gotBody.Conversions) != 1 || gotBody.Conversions[0].EventID != "e1" || gotBody.Conversions[0].Value != "9.00" {
		t.Errorf("body: %+v", gotBody)
	}
}

func TestXNumItems(t *testing.T) {
	convs := xBuild([]Conversion{{Standard: EventPurchase, Value: 30, Currency: "USD",
		User:  UserData{Email: "a@b.com"},
		Items: []Item{{ID: "s1", Quantity: 2}, {ID: "s2", Quantity: 1}}}})
	if convs[0].NumberItems != 3 {
		t.Errorf("number_items = %d, want 3 (2+1)", convs[0].NumberItems)
	}
	// No items ⇒ number_items omitted (0).
	none := xBuild([]Conversion{{Standard: EventLead, User: UserData{Email: "a@b.com"}}})
	if none[0].NumberItems != 0 {
		t.Errorf("no items ⇒ number_items 0, got %d", none[0].NumberItems)
	}
}
