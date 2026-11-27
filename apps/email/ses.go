package email

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// SES implements Amazon Simple Email Service (SES v2) outbound email dispatch
// using AWS Signature Version 4 over HTTPS.
type SES struct {
	accessKeyID      string
	secretAccessKey  string
	sessionToken     string
	region           string
	fromEmail        string
	fromName         string
	configurationSet string
	endpoint         string
	receivers        []string
	altText          string
	headers          map[string]string
	tags             map[string]string
	httpClient       *http.Client
}

// regionShape is an AWS region's name (us-east-1, ap-southeast-2, us-gov-west-1):
// the region credential is spelled into the SES host, so nothing else may stand
// there and point it off amazonaws.com.
var regionShape = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]{1,2}$`)

// NewSES constructs an Amazon SES v2 email sender from resolved provider credentials.
func NewSES(creds map[string]string, to []string) *SES {
	region := strings.TrimSpace(creds["region"])
	if !regionShape.MatchString(region) {
		region = "us-east-1"
	}
	return &SES{
		accessKeyID:     strings.TrimSpace(creds["access-key-id"]),
		secretAccessKey: strings.TrimSpace(creds["secret-access-key"]),
		sessionToken:    strings.TrimSpace(creds["session-token"]),
		region:          region,
		fromEmail:       strings.TrimSpace(creds["from-email"]),
		fromName:        strings.TrimSpace(creds["from-name"]),
		// Named only when the credential names one: a set the account does not
		// hold fails every send, and with none SES publishes no events.
		configurationSet: strings.TrimSpace(creds["configuration-set"]),
		endpoint:         strings.TrimSpace(creds["endpoint"]),
		receivers:        append([]string(nil), to...),
		httpClient:       &http.Client{Timeout: 15 * time.Second},
	}
}

// AddReceivers appends recipient addresses to the SES sender.
func (s *SES) AddReceivers(to ...string) {
	s.receivers = append(s.receivers, to...)
}

// Alternative sets the plain-text alternative body when sending HTML email.
func (s *SES) Alternative(text string) {
	s.altText = text
}

// SetHeaders sets optional custom headers to attach to the outbound SES message.
func (s *SES) SetHeaders(h map[string]string) {
	s.headers = h
}

// SetTags sets the message tags SES carries back on every event it publishes about
// the message (mail.tags). A tag's name and value are letters, digits, '_' and '-'.
func (s *SES) SetTags(t map[string]string) {
	s.tags = t
}

type sesSendEmailRequest struct {
	FromEmailAddress     string          `json:"FromEmailAddress"`
	Destination          sesDestination  `json:"Destination"`
	Content              sesEmailContent `json:"Content"`
	ConfigurationSetName string          `json:"ConfigurationSetName,omitempty"`
	EmailTags            []sesMessageTag `json:"EmailTags,omitempty"`
}

type sesDestination struct {
	ToAddresses []string `json:"ToAddresses"`
}

type sesEmailContent struct {
	Simple *sesMessage `json:"Simple,omitempty"`
}

type sesMessage struct {
	Subject sesContent  `json:"Subject"`
	Body    sesBody     `json:"Body"`
	Headers []sesHeader `json:"Headers,omitempty"`
}

type sesContent struct {
	Data    string `json:"Data"`
	Charset string `json:"Charset,omitempty"`
}

type sesBody struct {
	Html *sesContent `json:"Html,omitempty"`
	Text *sesContent `json:"Text,omitempty"`
}

type sesHeader struct {
	Name  string `json:"Name"`
	Value string `json:"Value"`
}

type sesMessageTag struct {
	Name  string `json:"Name"`
	Value string `json:"Value"`
}

type sesSendEmailResponse struct {
	MessageId string `json:"MessageId"`
}

// Send dispatches the email through the Amazon SES v2 SendEmail endpoint.
func (s *SES) Send(ctx context.Context, subject, body string) error {
	_, err := s.SendWithID(ctx, subject, body)
	return err
}

// SendWithID dispatches the email through Amazon SES v2 and returns the SES MessageId.
func (s *SES) SendWithID(ctx context.Context, subject, body string) (string, error) {
	if s.accessKeyID == "" || s.secretAccessKey == "" {
		return "", errors.New("ses: access-key-id and secret-access-key are required")
	}
	if s.fromEmail == "" {
		return "", errors.New("ses: from-email is required")
	}
	if len(s.receivers) == 0 {
		return "", errors.New("ses: at least one recipient is required")
	}

	fromAddr := s.fromEmail
	if s.fromName != "" && !strings.Contains(s.fromEmail, "<") {
		fromAddr = (&mail.Address{Name: s.fromName, Address: s.fromEmail}).String()
	}

	msgBody := sesBody{}
	isHTML := strings.Contains(body, "<") && strings.Contains(body, ">")
	if isHTML || s.altText != "" {
		if body != "" {
			msgBody.Html = &sesContent{Data: body, Charset: "UTF-8"}
		}
		if s.altText != "" {
			msgBody.Text = &sesContent{Data: s.altText, Charset: "UTF-8"}
		}
	} else {
		msgBody.Text = &sesContent{Data: body, Charset: "UTF-8"}
	}

	var customHeaders []sesHeader
	if len(s.headers) > 0 {
		keys := make([]string, 0, len(s.headers))
		for k := range s.headers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if v := strings.TrimSpace(s.headers[k]); v != "" {
				customHeaders = append(customHeaders, sesHeader{Name: k, Value: v})
			}
		}
	}

	payload := sesSendEmailRequest{
		FromEmailAddress: fromAddr,
		Destination: sesDestination{
			ToAddresses: s.receivers,
		},
		Content: sesEmailContent{
			Simple: &sesMessage{
				Subject: sesContent{Data: subject, Charset: "UTF-8"},
				Body:    msgBody,
				Headers: customHeaders,
			},
		},
		ConfigurationSetName: s.configurationSet,
	}
	for _, k := range slices.Sorted(maps.Keys(s.tags)) {
		if v := strings.TrimSpace(s.tags[k]); v != "" {
			payload.EmailTags = append(payload.EmailTags, sesMessageTag{Name: k, Value: v})
		}
	}

	rawJSON, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("ses: marshal payload: %w", err)
	}

	baseURL := s.endpoint
	if baseURL == "" {
		baseURL = fmt.Sprintf("https://email.%s.amazonaws.com", s.region)
	}
	reqURL := strings.TrimRight(baseURL, "/") + "/v2/email/outbound-emails"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(rawJSON))
	if err != nil {
		return "", fmt.Errorf("ses: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	signAWSSigV4(req, rawJSON, s.accessKeyID, s.secretAccessKey, s.sessionToken, s.region, "ses", time.Now().UTC())

	client := s.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ses: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("ses: status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBytes)))
	}

	var out sesSendEmailResponse
	if err := json.Unmarshal(respBytes, &out); err == nil && out.MessageId != "" {
		return out.MessageId, nil
	}
	return "", nil
}

// sesAccount is what SES v2 GetAccount answers of the account's sending: its quota,
// whether it is out of the sandbox, and whether it may send at all.
type sesAccount struct {
	SendQuota struct {
		Max24HourSend   float64 `json:"Max24HourSend"`
		MaxSendRate     float64 `json:"MaxSendRate"`
		SentLast24Hours float64 `json:"SentLast24Hours"`
	} `json:"SendQuota"`
	ProductionAccessEnabled bool `json:"ProductionAccessEnabled"`
	SendingEnabled          bool `json:"SendingEnabled"`
}

// Account reads the account's sending limits from SES (GET /v2/email/account).
func (s *SES) Account(ctx context.Context) (*sesAccount, error) {
	if s.accessKeyID == "" || s.secretAccessKey == "" {
		return nil, errors.New("ses: access-key-id and secret-access-key are required")
	}
	base := s.endpoint
	if base == "" {
		base = fmt.Sprintf("https://email.%s.amazonaws.com", s.region)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/v2/email/account", nil)
	if err != nil {
		return nil, fmt.Errorf("ses: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	signAWSSigV4(req, nil, s.accessKeyID, s.secretAccessKey, s.sessionToken, s.region, "ses", time.Now().UTC())
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ses: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ses: GetAccount answered %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	out := &sesAccount{}
	if err := json.Unmarshal(body, out); err != nil {
		return nil, fmt.Errorf("ses: GetAccount: %w", err)
	}
	return out, nil
}

// signAWSSigV4 signs an HTTP request using AWS Signature Version 4 for the given service and region.
func signAWSSigV4(req *http.Request, body []byte, accessKey, secretKey, sessionToken, region, service string, now time.Time) {
	amzDate := now.UTC().Format("20060102T150405Z")
	dateStamp := now.UTC().Format("20060102")

	payloadHash := sha256Hex(body)
	host := req.URL.Host
	req.Header.Set("Host", host)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if sessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", sessionToken)
	}

	canonicalURI := req.URL.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	canonicalQuery := canonicalQueryString(req.URL.Query())

	signedHeaders := []string{"content-type", "host", "x-amz-content-sha256", "x-amz-date"}
	if sessionToken != "" {
		signedHeaders = append(signedHeaders, "x-amz-security-token")
	}
	sort.Strings(signedHeaders)

	var canonicalHeaders strings.Builder
	for _, h := range signedHeaders {
		var val string
		if h == "host" {
			val = host
		} else {
			val = strings.TrimSpace(req.Header.Get(h))
		}
		canonicalHeaders.WriteString(h)
		canonicalHeaders.WriteByte(':')
		canonicalHeaders.WriteString(val)
		canonicalHeaders.WriteByte('\n')
	}

	signedHeadersStr := strings.Join(signedHeaders, ";")
	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI,
		canonicalQuery,
		canonicalHeaders.String(),
		signedHeadersStr,
		payloadHash,
	}, "\n")

	credentialScope := fmt.Sprintf("%s/%s/%s/aws4_request", dateStamp, region, service)
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	kDate := hmacSHA256([]byte("AWS4"+secretKey), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	kSigning := hmacSHA256(kService, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))

	authHeader := fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKey, credentialScope, signedHeadersStr, signature,
	)
	req.Header.Set("Authorization", authHeader)
}

func canonicalQueryString(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vals := q[k]
		sort.Strings(vals)
		for _, v := range vals {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(data))
	return h.Sum(nil)
}

// BuildSESDNSRecords generates the recommended Amazon SES v2 Easy DKIM, SPF, DMARC,
// and custom MAIL FROM DNS records for a sending domain.
func BuildSESDNSRecords(domain, region string) []SESDNSRecord {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		domain = "hanzo.ai"
	}
	if at := strings.LastIndexByte(domain, '@'); at != -1 {
		domain = domain[at+1:]
	}
	region = strings.TrimSpace(region)
	if region == "" {
		region = "us-east-1"
	}
	mailFromDomain := "bounce." + domain
	return []SESDNSRecord{
		{
			Purpose:  "DKIM",
			Type:     "CNAME",
			Host:     "ses1._domainkey." + domain,
			Value:    "ses1." + domain + ".dkim.amazonses.com",
			Verified: true,
		},
		{
			Purpose:  "DKIM",
			Type:     "CNAME",
			Host:     "ses2._domainkey." + domain,
			Value:    "ses2." + domain + ".dkim.amazonses.com",
			Verified: true,
		},
		{
			Purpose:  "DKIM",
			Type:     "CNAME",
			Host:     "ses3._domainkey." + domain,
			Value:    "ses3." + domain + ".dkim.amazonses.com",
			Verified: true,
		},
		{
			Purpose:  "MAIL FROM MX",
			Type:     "MX",
			Host:     mailFromDomain,
			Value:    "10 feedback-smtp." + region + ".amazonses.com",
			Priority: 10,
			Verified: true,
		},
		{
			Purpose:  "MAIL FROM SPF",
			Type:     "TXT",
			Host:     mailFromDomain,
			Value:    "v=spf1 include:amazonses.com ~all",
			Verified: true,
		},
		{
			Purpose:  "DMARC",
			Type:     "TXT",
			Host:     "_dmarc." + domain,
			Value:    "v=DMARC1; p=quarantine; rua=mailto:dmarc@" + domain + "; pct=100; adkim=s; aspf=s",
			Verified: true,
		},
	}
}
