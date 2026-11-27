package email

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // SNS signs SignatureVersion 1 with SHA-1; it is verified here, never produced.
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// snsEnvelope is the message Amazon SNS posts to an HTTPS subscription: a
// Notification carrying an SES event, or the SubscriptionConfirmation that
// precedes it. Every field is the string SNS signed.
type snsEnvelope struct {
	Type             string `json:"Type"`
	MessageID        string `json:"MessageId"`
	Token            string `json:"Token"`
	TopicARN         string `json:"TopicArn"`
	Subject          string `json:"Subject"`
	Message          string `json:"Message"`
	Timestamp        string `json:"Timestamp"`
	SignatureVersion string `json:"SignatureVersion"`
	Signature        string `json:"Signature"`
	SigningCertURL   string `json:"SigningCertURL"`
	SubscribeURL     string `json:"SubscribeURL"`
}

// errSNS is every refusal of an SNS message. The receiver answers one 401 for
// all of them, so a caller learns nothing about which check it failed.
var errSNS = errors.New("email: SNS message not verified")

const (
	// snsMaxCert bounds the certificate document fetched from SigningCertURL.
	snsMaxCert = 64 << 10
	// snsFetch bounds one fetch from SNS.
	snsFetch = 5 * time.Second
	// snsCerts bounds the certificates held; the host pin makes the set tiny.
	snsCerts = 32
)

// SNS verifies the messages Amazon SNS delivers: the signature over the
// canonical string under the certificate SigningCertURL names, a certificate
// fetched only from sns.<region>.amazonaws.com for the region of the topic the
// organization subscribed. Anything else is refused before a byte is fetched.
type SNS struct {
	client *http.Client
	// host reports whether host is a place SNS in region may be fetched from.
	host func(host, region string) bool
	now  func() time.Time

	mu    sync.Mutex
	certs map[string]*x509.Certificate
}

// NewSNS returns a verifier fetching through client; nil is a client that
// follows no redirect and gives up after five seconds.
func NewSNS(client *http.Client) *SNS {
	if client == nil {
		client = &http.Client{Timeout: snsFetch}
	}
	// A redirect is a way past the host pin, so none is followed.
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &SNS{client: &c, host: snsHost, now: time.Now, certs: map[string]*x509.Certificate{}}
}

// snsHost is the one pin: the SNS endpoint of the topic's own region.
func snsHost(host, region string) bool {
	if region == "" {
		return false
	}
	host = strings.ToLower(host)
	return host == "sns."+region+".amazonaws.com" || host == "sns."+region+".amazonaws.com.cn"
}

// topicRegion is the region of an SNS topic ARN (arn:aws:sns:<region>:<account>:<name>).
func topicRegion(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) != 6 || parts[0] != "arn" || parts[2] != "sns" || parts[3] == "" {
		return ""
	}
	return parts[3]
}

// checkURL is raw as a url when it is an https address with no credentials, no
// port but 443, no query and a host the pin accepts.
func (s *SNS) checkURL(raw, region string, cert bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host == "" || u.Fragment != "" {
		return nil, errSNS
	}
	if p := u.Port(); p != "" && p != "443" {
		return nil, errSNS
	}
	if !s.host(u.Hostname(), region) {
		return nil, errSNS
	}
	if cert && (u.RawQuery != "" || !strings.HasSuffix(u.Path, ".pem")) {
		return nil, errSNS
	}
	return u, nil
}

// Verify checks e against the topic the organization configured: the topic must
// be that one, the signature version known, the certificate fetched from the
// topic's region, and the signature the certificate's over the canonical string.
func (s *SNS) Verify(ctx context.Context, e *snsEnvelope, topic string) error {
	region := topicRegion(topic)
	if e == nil || region == "" || e.TopicARN != topic {
		return errSNS
	}
	var hash crypto.Hash
	var sum []byte
	text := canonical(e)
	if text == "" {
		return errSNS
	}
	switch e.SignatureVersion {
	case "1":
		h := sha1.Sum([]byte(text)) //nolint:gosec // see the import.
		hash, sum = crypto.SHA1, h[:]
	case "2":
		h := sha256.Sum256([]byte(text))
		hash, sum = crypto.SHA256, h[:]
	default:
		return errSNS
	}
	sig, err := base64.StdEncoding.DecodeString(e.Signature)
	if err != nil || len(sig) == 0 {
		return errSNS
	}
	u, err := s.checkURL(e.SigningCertURL, region, true)
	if err != nil {
		return err
	}
	cert, err := s.cert(ctx, u)
	if err != nil {
		return errSNS
	}
	now := s.now()
	if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
		return errSNS
	}
	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok || rsa.VerifyPKCS1v15(pub, hash, sum, sig) != nil {
		return errSNS
	}
	return nil
}

// canonical is the string SNS signs: name, newline, value, newline for each of
// the fields its type names, in alphabetical order.
func canonical(e *snsEnvelope) string {
	var pairs [][2]string
	switch e.Type {
	case "Notification":
		pairs = [][2]string{{"Message", e.Message}, {"MessageId", e.MessageID}}
		if e.Subject != "" {
			pairs = append(pairs, [2]string{"Subject", e.Subject})
		}
		pairs = append(pairs, [2]string{"Timestamp", e.Timestamp}, [2]string{"TopicArn", e.TopicARN}, [2]string{"Type", e.Type})
	case "SubscriptionConfirmation", "UnsubscribeConfirmation":
		pairs = [][2]string{
			{"Message", e.Message}, {"MessageId", e.MessageID}, {"SubscribeURL", e.SubscribeURL},
			{"Timestamp", e.Timestamp}, {"Token", e.Token}, {"TopicArn", e.TopicARN}, {"Type", e.Type},
		}
	default:
		return ""
	}
	var b strings.Builder
	for _, p := range pairs {
		b.WriteString(p[0] + "\n" + p[1] + "\n")
	}
	return b.String()
}

// cert is the certificate at u, held once fetched.
func (s *SNS) cert(ctx context.Context, u *url.URL) (*x509.Certificate, error) {
	key := u.String()
	s.mu.Lock()
	c := s.certs[key]
	s.mu.Unlock()
	if c != nil {
		return c, nil
	}
	body, err := s.get(ctx, u, snsMaxCert)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(body)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errSNS
	}
	c, err = x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, errSNS
	}
	s.mu.Lock()
	if len(s.certs) >= snsCerts {
		s.certs = map[string]*x509.Certificate{}
	}
	s.certs[key] = c
	s.mu.Unlock()
	return c, nil
}

// Confirm completes a subscription by fetching the SubscribeURL the verified
// message carries, which must be the topic's own region's SNS endpoint.
func (s *SNS) Confirm(ctx context.Context, e *snsEnvelope, topic string) error {
	u, err := s.checkURL(e.SubscribeURL, topicRegion(topic), false)
	if err != nil {
		return err
	}
	if _, err := s.get(ctx, u, 4<<10); err != nil {
		return fmt.Errorf("email: confirm subscription: %w", err)
	}
	return nil
}

// get reads at most limit bytes of a 2xx answer from u.
func (s *SNS) get(ctx context.Context, u *url.URL, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, snsFetch)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		return nil, errSNS
	}
	return body, nil
}
