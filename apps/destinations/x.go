package destinations

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// x.go forwards conversions to X (Twitter) via the Ads API web-events measurement
// endpoint. Config: the web-event tag pixelId. The X Ads API authenticates with OAuth
// 1.0a USER-context request signing — a consumer key/secret PLUS an access token/secret
// — not a single bearer token. resolveSecret hands an adapter exactly one primary
// secret, so X's four OAuth1 parts ride as ONE composite KMS secret: a JSON object
// {consumer_key, consumer_secret, access_token, access_token_secret}. The signer below
// is the only thing X needs beyond the shared interface; the translator + payload
// builder are unchanged. PII match keys are SHA-256 hashed by xBuild; the shared
// event_id dedups against the browser tag.

const xID = "x"

// xAdsAPI is the X Ads API base (version-pinned). A package var so a test points it at
// a mock server; never mutated in production.
var xAdsAPI = "https://ads-api.x.com/12"

// xNonce / xTimestamp are the OAuth1 nonce and timestamp sources — package vars so a
// test pins them for a deterministic signature. Production reads crypto/rand + wall
// clock. xTimestamp is injected (not time.Now directly) so a signed request is
// reproducible under test.
var (
	xNonce     = func() string { b := make([]byte, 16); _, _ = rand.Read(b); return hex.EncodeToString(b) }
	xTimestamp = func() int64 { return time.Now().Unix() }
)

type xDest struct{}

func init() { register(xDest{}) }

func (xDest) ID() string       { return xID }
func (xDest) Name() string     { return "X (Twitter)" }
func (xDest) Category() string { return categoryAdvertising }

func (xDest) Spec() Spec {
	return Spec{
		Fields: []DestinationField{
			{Key: "pixelId", Label: "Pixel / Event Tag ID", Required: true, Example: "o1abc"},
		},
		// One composite secret: a JSON object carrying the four OAuth1 parts, because
		// the fan-out resolves and passes a single primary secret per destination.
		Secrets: []string{"oauth1"},
	}
}

// xCreds are the OAuth 1.0a user-context credentials, parsed from the single composite
// KMS secret the connect flow seals for X.
type xCreds struct {
	ConsumerKey    string `json:"consumer_key"`
	ConsumerSecret string `json:"consumer_secret"`
	AccessToken    string `json:"access_token"`
	AccessSecret   string `json:"access_token_secret"`
}

func (c xCreds) complete() bool {
	return c.ConsumerKey != "" && c.ConsumerSecret != "" && c.AccessToken != "" && c.AccessSecret != ""
}

// xConvBody wraps the conversions in the Ads-API measurement request body.
type xConvBody struct {
	Conversions []xConversion `json:"conversions"`
}

type xConversion struct {
	ConversionTime string   `json:"conversion_time"`
	EventID        string   `json:"event_id,omitempty"`
	Identifiers    []xIDent `json:"identifiers"`
	NumberItems    int      `json:"number_items,omitempty"`
	Value          string   `json:"value,omitempty"`
	PriceCurrency  string   `json:"price_currency,omitempty"`
}

type xIDent struct {
	HashedEmail string `json:"hashed_email,omitempty"`
	TwClickID   string `json:"twclid,omitempty"`
}

// xBuild renders the batch into X's conversions payload. Pure — tests assert the hashed
// identifiers + value formatting without a network call.
func xBuild(batch []Conversion) []xConversion {
	out := make([]xConversion, 0, len(batch))
	for _, cv := range batch {
		ids := make([]xIDent, 0, 2)
		if h := hashEmail(cv.User.Email); h != "" {
			ids = append(ids, xIDent{HashedEmail: h})
		}
		if tw := cv.User.click("twclid"); tw != "" {
			ids = append(ids, xIDent{TwClickID: tw})
		}
		c := xConversion{ConversionTime: redditTime(cv.Time), EventID: cv.EventID, Identifiers: ids}
		if cv.Value > 0 {
			c.Value = fmt.Sprintf("%.2f", cv.Value)
			c.PriceCurrency = cv.Currency
		}
		out = append(out, c)
	}
	return out
}

// oauthEnc percent-encodes per RFC 3986 (OAuth 1.0a §3.6): the unreserved set stays
// literal, everything else is %-encoded upper-hex. net/url does NOT do this exactly
// (it leaves some sub-delims), so OAuth1 needs its own encoder.
func oauthEnc(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// xAuthHeader builds the OAuth 1.0a Authorization header for a request. A JSON body is
// NOT part of the signature base string (only form-encoded params and query params
// are), so the base string is the method, the URL, and the sorted oauth_* params.
func xAuthHeader(method, endpoint string, c xCreds) string {
	params := map[string]string{
		"oauth_consumer_key":     c.ConsumerKey,
		"oauth_nonce":            xNonce(),
		"oauth_signature_method": "HMAC-SHA1",
		"oauth_timestamp":        strconv.FormatInt(xTimestamp(), 10),
		"oauth_token":            c.AccessToken,
		"oauth_version":          "1.0",
	}

	// Signature base string: METHOD & enc(url) & enc(sorted "k=v" params).
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, oauthEnc(k)+"="+oauthEnc(params[k]))
	}
	base := strings.ToUpper(method) + "&" + oauthEnc(endpoint) + "&" + oauthEnc(strings.Join(pairs, "&"))
	signingKey := oauthEnc(c.ConsumerSecret) + "&" + oauthEnc(c.AccessSecret)
	mac := hmac.New(sha1.New, []byte(signingKey))
	mac.Write([]byte(base))
	params["oauth_signature"] = base64.StdEncoding.EncodeToString(mac.Sum(nil))

	// Header: OAuth k="v", … over the same params incl. the signature.
	hkeys := make([]string, 0, len(params))
	for k := range params {
		hkeys = append(hkeys, k)
	}
	sort.Strings(hkeys)
	parts := make([]string, 0, len(hkeys))
	for _, k := range hkeys {
		parts = append(parts, oauthEnc(k)+`="`+oauthEnc(params[k])+`"`)
	}
	return "OAuth " + strings.Join(parts, ", ")
}

func (d xDest) Send(ctx context.Context, cfg Config, secret string, batch []Conversion) (Result, error) {
	pixel := cfg.get("pixelId")
	if pixel == "" {
		return Result{}, fmt.Errorf("x: pixelId is required")
	}
	var c xCreds
	if err := json.Unmarshal([]byte(strings.TrimSpace(secret)), &c); err != nil {
		return Result{}, fmt.Errorf("x: credentials must be a JSON object {consumer_key, consumer_secret, access_token, access_token_secret}")
	}
	if !c.complete() {
		return Result{}, fmt.Errorf("x: OAuth1 credentials are incomplete")
	}
	if len(batch) == 0 {
		return Result{}, nil
	}
	endpoint := xAdsAPI + "/measurement/conversions/" + pixel
	body := xConvBody{Conversions: xBuild(batch)}
	headers := map[string]string{"Authorization": xAuthHeader(http.MethodPost, endpoint, c)}
	if err := postJSON(ctx, xID, endpoint, headers, body, nil); err != nil {
		return Result{}, err
	}
	return Result{Sent: len(batch)}, nil
}
