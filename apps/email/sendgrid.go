package email

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

const defaultSendGridEndpoint = "https://api.sendgrid.com/v3/mail/send"

// SendGrid implements outbound email dispatch through the SendGrid v3 Mail Send API.
//
// It is designed to operate with dedicated outreach / bulk-marketing SendGrid credentials
// (separate API key, separate sender domain/address, and optional dedicated IP pool)
// completely independent of the organization's standard transactional Twilio Email / SMS
// notify credentials.
type SendGrid struct {
	apiKey     string
	fromEmail  string
	fromName   string
	replyTo    string
	ipPoolName string
	asmGroupID int
	categories []string
	endpoint   string
	receivers  []string
	altText    string
	headers    map[string]string
	tags       map[string]string
	httpClient *http.Client
}

// NewSendGrid constructs a SendGrid v3 email sender from resolved provider credentials.
func NewSendGrid(creds map[string]string, to []string) *SendGrid {
	endpoint := strings.TrimSpace(creds["endpoint"])
	if endpoint == "" {
		endpoint = defaultSendGridEndpoint
	}
	asmGroup, _ := strconv.Atoi(strings.TrimSpace(creds["asm-group-id"]))

	var cats []string
	if rawCats := strings.TrimSpace(creds["categories"]); rawCats != "" {
		for _, c := range strings.Split(rawCats, ",") {
			if c = strings.TrimSpace(c); c != "" {
				cats = append(cats, c)
			}
		}
	}
	if len(cats) == 0 {
		cats = []string{defaultCategory}
	}

	return &SendGrid{
		apiKey:     strings.TrimSpace(creds["api-key"]),
		fromEmail:  strings.TrimSpace(creds["from-email"]),
		fromName:   strings.TrimSpace(creds["from-name"]),
		replyTo:    strings.TrimSpace(creds["reply-to"]),
		ipPoolName: strings.TrimSpace(creds["ip-pool"]),
		asmGroupID: asmGroup,
		categories: cats,
		endpoint:   endpoint,
		receivers:  append([]string(nil), to...),
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// AddReceivers appends recipient addresses to the SendGrid sender.
func (s *SendGrid) AddReceivers(to ...string) {
	s.receivers = append(s.receivers, to...)
}

// Alternative sets the plain-text alternative body when sending HTML email.
func (s *SendGrid) Alternative(text string) {
	s.altText = text
}

// SetHeaders sets optional custom headers to attach to the outbound SendGrid message.
func (s *SendGrid) SetHeaders(h map[string]string) {
	s.headers = h
}

// SetTags sets the custom args SendGrid carries back on every event about the
// message. A campaign is also the message's category, so SendGrid's own statistics
// split by it.
func (s *SendGrid) SetTags(t map[string]string) {
	s.tags = t
}

type sgMailSendRequest struct {
	Personalizations []sgPersonalization `json:"personalizations"`
	From             sgEmailAddress      `json:"from"`
	ReplyTo          *sgEmailAddress     `json:"reply_to,omitempty"`
	Subject          string              `json:"subject"`
	Content          []sgContent         `json:"content"`
	Headers          map[string]string   `json:"headers,omitempty"`
	Categories       []string            `json:"categories,omitempty"`
	CustomArgs       map[string]string   `json:"custom_args,omitempty"`
	IPPoolName       string              `json:"ip_pool_name,omitempty"`
	ASM              *sgASM              `json:"asm,omitempty"`
	TrackingSettings *sgTrackingSettings `json:"tracking_settings,omitempty"`
}

type sgPersonalization struct {
	To      []sgEmailAddress  `json:"to"`
	Subject string            `json:"subject,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

type sgEmailAddress struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type sgContent struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type sgASM struct {
	GroupID int `json:"group_id"`
}

type sgTrackingSettings struct {
	ClickTracking *sgClickTracking `json:"click_tracking,omitempty"`
	OpenTracking  *sgOpenTracking  `json:"open_tracking,omitempty"`
}

type sgClickTracking struct {
	Enable     bool `json:"enable"`
	EnableText bool `json:"enable_text"`
}

type sgOpenTracking struct {
	Enable bool `json:"enable"`
}

// Send dispatches the email through the SendGrid v3 Mail Send endpoint.
func (s *SendGrid) Send(ctx context.Context, subject, body string) error {
	_, err := s.SendWithID(ctx, subject, body)
	return err
}

// SendWithID dispatches the email through SendGrid v3 and returns the X-Message-Id header.
func (s *SendGrid) SendWithID(ctx context.Context, subject, body string) (string, error) {
	if s.apiKey == "" {
		return "", errors.New("sendgrid: missing api-key")
	}
	if s.fromEmail == "" {
		return "", errors.New("sendgrid: missing from-email")
	}
	if len(s.receivers) == 0 {
		return "", errors.New("sendgrid: at least one recipient is required")
	}

	tos := make([]sgEmailAddress, 0, len(s.receivers))
	for _, r := range s.receivers {
		r = strings.TrimSpace(r)
		if r != "" {
			tos = append(tos, sgEmailAddress{Email: r})
		}
	}
	if len(tos) == 0 {
		return "", errors.New("sendgrid: no valid recipient addresses")
	}

	var contents []sgContent
	isHTML := looksLikeHTML(body)
	if s.altText != "" && isHTML {
		// SendGrid requires text/plain BEFORE text/html in the content array
		contents = append(contents,
			sgContent{Type: "text/plain", Value: s.altText},
			sgContent{Type: "text/html", Value: body},
		)
	} else if isHTML {
		contents = append(contents,
			sgContent{Type: "text/plain", Value: stripHTMLTags(body)},
			sgContent{Type: "text/html", Value: body},
		)
	} else {
		contents = append(contents, sgContent{Type: "text/plain", Value: body})
	}

	hdrs := make(map[string]string, len(s.headers)+2)
	for k, v := range s.headers {
		if strings.TrimSpace(k) != "" && strings.TrimSpace(v) != "" {
			hdrs[k] = v
		}
	}
	// List-Unsubscribe is the sender's to give: a commercial message carries its
	// own signed link (POST /v1/email, marketing), a transactional one none.

	reqPayload := sgMailSendRequest{
		Personalizations: []sgPersonalization{
			{
				To:      tos,
				Subject: subject,
			},
		},
		From: sgEmailAddress{
			Email: s.fromEmail,
			Name:  s.fromName,
		},
		Subject:    subject,
		Content:    contents,
		Headers:    hdrs,
		Categories: s.categories,
		IPPoolName: s.ipPoolName,
		TrackingSettings: &sgTrackingSettings{
			ClickTracking: &sgClickTracking{Enable: true, EnableText: false},
			OpenTracking:  &sgOpenTracking{Enable: true},
		},
	}

	if s.replyTo != "" {
		reqPayload.ReplyTo = &sgEmailAddress{Email: s.replyTo}
	}
	if len(s.tags) > 0 {
		reqPayload.CustomArgs = s.tags
		if c := s.tags["campaign"]; c != "" && !slices.Contains(reqPayload.Categories, c) && len(reqPayload.Categories) < 10 {
			reqPayload.Categories = append(slices.Clone(reqPayload.Categories), c)
		}
	}
	if s.asmGroupID > 0 {
		reqPayload.ASM = &sgASM{GroupID: s.asmGroupID}
	}

	raw, err := json.Marshal(reqPayload)
	if err != nil {
		return "", fmt.Errorf("sendgrid: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("sendgrid: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := s.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("sendgrid: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("sendgrid: status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	msgID := strings.TrimSpace(resp.Header.Get("X-Message-Id"))
	return msgID, nil
}

// sgCredits is what SendGrid answers of the account's sending credits.
type sgCredits struct {
	Total  int64 `json:"total"`
	Remain int64 `json:"remain"`
	Used   int64 `json:"used"`
}

// Credits reads the account's credits from SendGrid (GET /v3/user/credits), on the
// API the endpoint credential names when it names one.
func (s *SendGrid) Credits(ctx context.Context) (*sgCredits, error) {
	if s.apiKey == "" {
		return nil, errors.New("sendgrid: missing api-key")
	}
	base := strings.TrimSuffix(strings.TrimRight(s.endpoint, "/"), "/mail/send")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/user/credits", nil)
	if err != nil {
		return nil, fmt.Errorf("sendgrid: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sendgrid: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sendgrid: credits answered %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	out := &sgCredits{}
	if err := json.Unmarshal(body, out); err != nil {
		return nil, fmt.Errorf("sendgrid: credits: %w", err)
	}
	return out, nil
}

// BuildSendGridDNSRecords returns the recommended SendGrid Domain Authentication (whitelabel),
// Link Branding, SPF, and DMARC records for a dedicated outreach domain.
func BuildSendGridDNSRecords(domain string) []SESDNSRecord {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		domain = "mail.hanzo.ai"
	}
	return []SESDNSRecord{
		{
			Purpose:  "SendGrid Automated Security (Return-Path CNAME)",
			Type:     "CNAME",
			Host:     "em7421." + domain,
			Value:    "u7421.wl.sendgrid.net",
			Verified: true,
		},
		{
			Purpose:  "SendGrid Domain Authentication DKIM Key #1",
			Type:     "CNAME",
			Host:     "s1._domainkey." + domain,
			Value:    "s1.domainkey.u7421.wl.sendgrid.net",
			Verified: true,
		},
		{
			Purpose:  "SendGrid Domain Authentication DKIM Key #2",
			Type:     "CNAME",
			Host:     "s2._domainkey." + domain,
			Value:    "s2.domainkey.u7421.wl.sendgrid.net",
			Verified: true,
		},
		{
			Purpose:  "SendGrid Branded Click/Open Tracking Link Domain",
			Type:     "CNAME",
			Host:     "url7421." + domain,
			Value:    "sendgrid.net",
			Verified: true,
		},
		{
			Purpose:  "SPF Alignment for SendGrid Outreach",
			Type:     "TXT",
			Host:     domain,
			Value:    "v=spf1 include:sendgrid.net ~all",
			Verified: true,
		},
		{
			Purpose:  "DMARC Policy for Bulk Outreach Domain",
			Type:     "TXT",
			Host:     "_dmarc." + domain,
			Value:    fmt.Sprintf("v=DMARC1; p=quarantine; rua=mailto:dmarc@%s; pct=100; adkim=r; aspf=r", domain),
			Verified: true,
		},
	}
}

func looksLikeHTML(s string) bool {
	return strings.Contains(s, "<") && strings.Contains(s, ">")
}

func stripHTMLTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch r {
		case '<':
			inTag = true
		case '>':
			inTag = false
		default:
			if !inTag {
				b.WriteRune(r)
			}
		}
	}
	return strings.TrimSpace(b.String())
}
