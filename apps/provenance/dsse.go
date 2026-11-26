// SPDX-License-Identifier: Apache-2.0

package provenance

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// AlgorithmEd25519 identifies Ed25519 signatures over raw DSSE PAE bytes.
	AlgorithmEd25519 = "ed25519"

	// AlgorithmECDSAP256 identifies ECDSA P-256 + SHA-256 ASN.1 DER signatures over DSSE PAE bytes.
	AlgorithmECDSAP256 = "ecdsa-p256-sha256"

	// DefaultCertTTL is the default Fulcio-style short-lived certificate lifetime (10 minutes).
	DefaultCertTTL = 10 * time.Minute

	// MaxCertTTL caps keyless short-lived code-signing certificate lifetimes.
	MaxCertTTL = 24 * time.Hour
)

// Sigstore Fulcio-compatible X.509 v3 OID extensions (1.3.6.1.4.1.57264.1.*).
var (
	OIDFulcioIssuer           = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 1}
	OIDFulcioIssuerV2         = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 8}
	OIDFulcioBuildSignerURI   = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 9}
	OIDFulcioSourceRepoURI    = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 12}
	OIDFulcioSourceRepoDigest = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 13}
)

// DSSEEnvelope is a Dead Simple Signing Envelope (DSSEv1).
type DSSEEnvelope struct {
	PayloadType string          `json:"payloadType"`
	Payload     string          `json:"payload"`
	Signatures  []DSSESignature `json:"signatures"`
}

// Clone returns a deep copy of DSSEEnvelope.
func (e DSSEEnvelope) Clone() DSSEEnvelope {
	out := e
	if e.Signatures != nil {
		out.Signatures = make([]DSSESignature, len(e.Signatures))
		copy(out.Signatures, e.Signatures)
	}
	return out
}

// DSSESignature holds one signature over PAE(payloadType, payload).
type DSSESignature struct {
	KeyID     string `json:"keyid,omitempty"`
	Sig       string `json:"sig"`
	Algorithm string `json:"algorithm,omitempty"`
	CertPEM   string `json:"certPem,omitempty"`
}

// PAE returns the canonical DSSEv1 Pre-Authentication Encoding:
//
//	"DSSEv1" SP LEN(payloadType) SP payloadType SP LEN(payload) SP payload
//
// where LEN(x) is the base-10 ASCII byte length of x and SP is 0x20.
func PAE(payloadType string, payload []byte) []byte {
	typeLen := strconv.Itoa(len(payloadType))
	bodyLen := strconv.Itoa(len(payload))
	total := len("DSSEv1 ") + len(typeLen) + 1 + len(payloadType) + 1 + len(bodyLen) + 1 + len(payload)
	buf := make([]byte, 0, total)
	buf = append(buf, "DSSEv1 "...)
	buf = append(buf, typeLen...)
	buf = append(buf, ' ')
	buf = append(buf, payloadType...)
	buf = append(buf, ' ')
	buf = append(buf, bodyLen...)
	buf = append(buf, ' ')
	buf = append(buf, payload...)
	return buf
}

// Signer signs arbitrary DSSE PAE messages.
type Signer interface {
	KeyID() string
	Algorithm() string
	PublicKey() crypto.PublicKey
	SignMessage(msg []byte) ([]byte, error)
}

// KeyPair implements Signer for Ed25519 or ECDSA P-256 keys.
type KeyPair struct {
	keyID string
	alg   string
	pub   crypto.PublicKey
	priv  crypto.Signer
}

// GenerateEd25519KeyPair creates a fresh Ed25519 signing key pair.
func GenerateEd25519KeyPair() (*KeyPair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("provenance: generate ed25519 key: %w", err)
	}
	kid, err := KeyIDFromPublicKey(pub)
	if err != nil {
		return nil, err
	}
	return &KeyPair{
		keyID: kid,
		alg:   AlgorithmEd25519,
		pub:   pub,
		priv:  priv,
	}, nil
}

// GenerateECDSAP256KeyPair creates a fresh ECDSA P-256 signing key pair.
func GenerateECDSAP256KeyPair() (*KeyPair, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("provenance: generate ecdsa-p256 key: %w", err)
	}
	kid, err := KeyIDFromPublicKey(&priv.PublicKey)
	if err != nil {
		return nil, err
	}
	return &KeyPair{
		keyID: kid,
		alg:   AlgorithmECDSAP256,
		pub:   &priv.PublicKey,
		priv:  priv,
	}, nil
}

// KeyID returns the deterministic SPKI SHA-256 identifier of the public key.
func (k *KeyPair) KeyID() string { return k.keyID }

// Algorithm returns the DSSE signature algorithm identifier.
func (k *KeyPair) Algorithm() string { return k.alg }

// PublicKey returns the public key.
func (k *KeyPair) PublicKey() crypto.PublicKey { return k.pub }

// SignMessage signs msg using the key pair's algorithm.
func (k *KeyPair) SignMessage(msg []byte) ([]byte, error) {
	switch priv := k.priv.(type) {
	case ed25519.PrivateKey:
		return ed25519.Sign(priv, msg), nil
	case *ecdsa.PrivateKey:
		digest := sha256.Sum256(msg)
		return ecdsa.SignASN1(rand.Reader, priv, digest[:])
	default:
		return nil, errors.New("provenance: unsupported private key type")
	}
}

// KeyIDFromPublicKey computes the stable key identifier ("sha256:<hex>") from PKIX SPKI bytes.
func KeyIDFromPublicKey(pub crypto.PublicKey) (string, error) {
	if pub == nil {
		return "", errors.New("provenance: nil public key")
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("provenance: marshal public key: %w", err)
	}
	sum := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// EncodePublicKeyPEM encodes an Ed25519 or ECDSA P-256 public key as PKIX PEM.
func EncodePublicKeyPEM(pub crypto.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}

// ParsePublicKeyPEM parses a PKIX PEM public key (Ed25519 or ECDSA P-256).
func ParsePublicKeyPEM(pemStr string) (crypto.PublicKey, error) {
	block, rest := pem.Decode([]byte(strings.TrimSpace(pemStr)))
	if block == nil || block.Type != "PUBLIC KEY" || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, errors.New("provenance: invalid PUBLIC KEY PEM")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("provenance: parse public key: %w", err)
	}
	switch k := pub.(type) {
	case ed25519.PublicKey:
		if len(k) != ed25519.PublicKeySize {
			return nil, errors.New("provenance: invalid ed25519 public key length")
		}
		return k, nil
	case *ecdsa.PublicKey:
		if k.Curve != elliptic.P256() {
			return nil, errors.New("provenance: only P-256 ECDSA keys are supported")
		}
		return k, nil
	default:
		return nil, errors.New("provenance: unsupported public key type")
	}
}

// VerifySignature verifies sig over msg with pub, enforcing key-type and algorithm consistency.
func VerifySignature(pub crypto.PublicKey, alg string, msg, sig []byte) error {
	if pub == nil || len(sig) == 0 {
		return fmt.Errorf("%w: empty public key or signature", ErrInvalidSignature)
	}
	switch k := pub.(type) {
	case ed25519.PublicKey:
		if alg != "" && alg != AlgorithmEd25519 {
			return fmt.Errorf("%w: algorithm %q incompatible with ed25519 key", ErrInvalidSignature, alg)
		}
		if len(sig) != ed25519.SignatureSize {
			return fmt.Errorf("%w: invalid ed25519 signature length %d", ErrInvalidSignature, len(sig))
		}
		if !ed25519.Verify(k, msg, sig) {
			return fmt.Errorf("%w: ed25519 verification failed", ErrInvalidSignature)
		}
		return nil
	case *ecdsa.PublicKey:
		if k.Curve != elliptic.P256() {
			return fmt.Errorf("%w: unsupported ecdsa curve", ErrInvalidSignature)
		}
		if alg != "" && alg != AlgorithmECDSAP256 {
			return fmt.Errorf("%w: algorithm %q incompatible with ecdsa-p256 key", ErrInvalidSignature, alg)
		}
		digest := sha256.Sum256(msg)
		if !ecdsa.VerifyASN1(k, digest[:], sig) {
			return fmt.Errorf("%w: ecdsa-p256 verification failed", ErrInvalidSignature)
		}
		return nil
	default:
		return fmt.Errorf("%w: unsupported public key type", ErrInvalidSignature)
	}
}

// SignDSSE signs payload with canonical DSSEv1 PAE and returns a DSSEEnvelope.
func SignDSSE(payloadType string, payload []byte, signer Signer, certPEM string) (*DSSEEnvelope, error) {
	if strings.TrimSpace(payloadType) == "" {
		return nil, errors.New("provenance: empty DSSE payloadType")
	}
	if len(payload) == 0 {
		return nil, errors.New("provenance: empty DSSE payload")
	}
	if signer == nil {
		return nil, errors.New("provenance: nil signer")
	}
	pae := PAE(payloadType, payload)
	sigBytes, err := signer.SignMessage(pae)
	if err != nil {
		return nil, fmt.Errorf("provenance: sign DSSE PAE: %w", err)
	}
	return &DSSEEnvelope{
		PayloadType: payloadType,
		Payload:     base64.StdEncoding.EncodeToString(payload),
		Signatures: []DSSESignature{
			{
				KeyID:     signer.KeyID(),
				Sig:       base64.StdEncoding.EncodeToString(sigBytes),
				Algorithm: signer.Algorithm(),
				CertPEM:   certPEM,
			},
		},
	}, nil
}

// VerifyDSSE validates payloadType, decodes payload, and verifies a matching DSSE signature.
func VerifyDSSE(env *DSSEEnvelope, expectedPayloadType string, resolveKey func(sig DSSESignature) (crypto.PublicKey, error)) ([]byte, DSSESignature, error) {
	if env == nil {
		return nil, DSSESignature{}, fmt.Errorf("%w: nil envelope", ErrInvalidSignature)
	}
	if env.PayloadType != expectedPayloadType {
		return nil, DSSESignature{}, fmt.Errorf("%w: got %q, want %q", ErrPayloadTypeMismatch, env.PayloadType, expectedPayloadType)
	}
	if len(env.Signatures) == 0 {
		return nil, DSSESignature{}, fmt.Errorf("%w: envelope carries no signatures", ErrInvalidSignature)
	}
	rawPayload, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil || len(rawPayload) == 0 {
		return nil, DSSESignature{}, fmt.Errorf("%w: invalid base64 payload", ErrInvalidSignature)
	}
	pae := PAE(env.PayloadType, rawPayload)

	var lastErr error
	for _, sig := range env.Signatures {
		sigBytes, err := base64.StdEncoding.DecodeString(sig.Sig)
		if err != nil {
			lastErr = fmt.Errorf("%w: invalid base64 signature", ErrInvalidSignature)
			continue
		}
		pub, err := resolveKey(sig)
		if err != nil {
			lastErr = err
			continue
		}
		if err := VerifySignature(pub, sig.Algorithm, pae, sigBytes); err != nil {
			lastErr = err
			continue
		}
		return rawPayload, sig, nil
	}
	if lastErr == nil {
		lastErr = ErrInvalidSignature
	}
	return nil, DSSESignature{}, lastErr
}

// canonicalLeafPayload commits a transparency log entry to the exact DSSE envelope.
type canonicalLeafPayload struct {
	Version       string             `json:"version"`
	PayloadType   string             `json:"payloadType"`
	PayloadSHA256 string             `json:"payloadSha256"`
	Signatures    []canonicalLeafSig `json:"signatures"`
}

type canonicalLeafSig struct {
	KeyID      string `json:"keyid"`
	Algorithm  string `json:"algorithm"`
	SigSHA256  string `json:"sigSha256"`
	CertSHA256 string `json:"certSha256,omitempty"`
}

// CanonicalLogLeaf produces deterministic canonical bytes committing to the full DSSE envelope.
func CanonicalLogLeaf(env *DSSEEnvelope) ([]byte, error) {
	if env == nil {
		return nil, errors.New("provenance: nil envelope")
	}
	if strings.TrimSpace(env.PayloadType) == "" || len(env.Signatures) == 0 {
		return nil, errors.New("provenance: incomplete envelope")
	}
	rawPayload, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil || len(rawPayload) == 0 {
		return nil, errors.New("provenance: invalid envelope payload")
	}
	sigs := make([]canonicalLeafSig, len(env.Signatures))
	for i, s := range env.Signatures {
		rawSig, err := base64.StdEncoding.DecodeString(s.Sig)
		if err != nil || len(rawSig) == 0 {
			return nil, errors.New("provenance: invalid envelope signature")
		}
		entry := canonicalLeafSig{
			KeyID:     s.KeyID,
			Algorithm: s.Algorithm,
			SigSHA256: DigestBytes(rawSig),
		}
		if s.CertPEM != "" {
			entry.CertSHA256 = DigestBytes([]byte(s.CertPEM))
		}
		sigs[i] = entry
	}
	leaf := canonicalLeafPayload{
		Version:       "hanzo.provenance.tlog.v1",
		PayloadType:   env.PayloadType,
		PayloadSHA256: DigestBytes(rawPayload),
		Signatures:    sigs,
	}
	return json.Marshal(leaf)
}

// OIDCIdentity describes the authenticated builder/agent identity embedded in a Fulcio-style cert.
type OIDCIdentity struct {
	Subject                string
	Issuer                 string
	BuildSignerURI         string
	SourceRepositoryURI    string
	SourceRepositoryDigest string
}

// VerifiedCertIdentity holds the verified claims extracted from a short-lived X.509 code-signing cert.
type VerifiedCertIdentity struct {
	Subject                string
	Issuer                 string
	BuildSignerURI         string
	SourceRepositoryURI    string
	SourceRepositoryDigest string
	SerialHex              string
	KeyID                  string
	PublicKey              crypto.PublicKey
	NotBefore              time.Time
	NotAfter               time.Time
}

// EphemeralCA is a clean-room Fulcio-style Certificate Authority issuing short-lived
// X.509 code-signing certificates bound to OIDC builder/agent identities.
type EphemeralCA struct {
	mu       sync.RWMutex
	rootCert *x509.Certificate
	rootPEM  string
	rootPool *x509.CertPool
	signer   *ecdsa.PrivateKey
}

// NewEphemeralCA initializes a self-signed ECDSA P-256 Root CA valid around now.
func NewEphemeralCA(commonName string, now time.Time) (*EphemeralCA, error) {
	if strings.TrimSpace(commonName) == "" {
		commonName = "Hanzo Forge Ephemeral Provenance Root CA"
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("provenance: generate CA key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"Hanzo AI"},
			CommonName:   commonName,
		},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, fmt.Errorf("provenance: create root CA cert: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("provenance: parse root CA cert: %w", err)
	}
	rootPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &EphemeralCA{
		rootCert: cert,
		rootPEM:  rootPEM,
		rootPool: pool,
		signer:   priv,
	}, nil
}

// RootPEM returns the PEM-encoded Root CA certificate.
func (ca *EphemeralCA) RootPEM() string {
	ca.mu.RLock()
	defer ca.mu.RUnlock()
	return ca.rootPEM
}

// CertPool returns a fresh copy of the Root CA certificate pool.
func (ca *EphemeralCA) CertPool() *x509.CertPool {
	ca.mu.RLock()
	defer ca.mu.RUnlock()
	pool := x509.NewCertPool()
	pool.AddCert(ca.rootCert)
	return pool
}

// IssueCodeSigningCert mints a short-lived Fulcio-style X.509 code-signing certificate for pub.
func (ca *EphemeralCA) IssueCodeSigningCert(pub crypto.PublicKey, id OIDCIdentity, notBefore time.Time, ttl time.Duration) (*x509.Certificate, string, error) {
	if pub == nil {
		return nil, "", errors.New("provenance: nil subject public key")
	}
	if strings.TrimSpace(id.Subject) == "" || strings.TrimSpace(id.Issuer) == "" {
		return nil, "", errors.New("provenance: OIDC subject and issuer are required")
	}
	if ttl <= 0 {
		ttl = DefaultCertTTL
	}
	if ttl > MaxCertTTL {
		return nil, "", fmt.Errorf("provenance: cert TTL %v exceeds max %v", ttl, MaxCertTTL)
	}
	if notBefore.IsZero() {
		notBefore = time.Now().UTC()
	}
	notBefore = notBefore.UTC().Truncate(time.Second)

	serial, err := randomSerial()
	if err != nil {
		return nil, "", err
	}

	var uris []*url.URL
	var emails []string
	if parsed, err := url.Parse(id.Subject); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		uris = append(uris, parsed)
	} else if strings.Contains(id.Subject, "@") {
		emails = append(emails, id.Subject)
	} else {
		return nil, "", errors.New("provenance: OIDC subject must be a valid URI or email address")
	}

	exts := make([]pkix.Extension, 0, 5)
	exts = append(exts, pkix.Extension{Id: OIDFulcioIssuer, Value: []byte(id.Issuer)})
	if v, err := asn1.MarshalWithParams(id.Issuer, "utf8"); err == nil {
		exts = append(exts, pkix.Extension{Id: OIDFulcioIssuerV2, Value: v})
	}
	if id.BuildSignerURI != "" {
		if v, err := asn1.MarshalWithParams(id.BuildSignerURI, "utf8"); err == nil {
			exts = append(exts, pkix.Extension{Id: OIDFulcioBuildSignerURI, Value: v})
		}
	}
	if id.SourceRepositoryURI != "" {
		if v, err := asn1.MarshalWithParams(id.SourceRepositoryURI, "utf8"); err == nil {
			exts = append(exts, pkix.Extension{Id: OIDFulcioSourceRepoURI, Value: v})
		}
	}
	if id.SourceRepositoryDigest != "" {
		if v, err := asn1.MarshalWithParams(id.SourceRepositoryDigest, "utf8"); err == nil {
			exts = append(exts, pkix.Extension{Id: OIDFulcioSourceRepoDigest, Value: v})
		}
	}

	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		NotBefore:             notBefore,
		NotAfter:              notBefore.Add(ttl),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		BasicConstraintsValid: true,
		IsCA:                  false,
		URIs:                  uris,
		EmailAddresses:        emails,
		ExtraExtensions:       exts,
	}

	ca.mu.RLock()
	rootCert := ca.rootCert
	signer := ca.signer
	ca.mu.RUnlock()

	der, err := x509.CreateCertificate(rand.Reader, tmpl, rootCert, pub, signer)
	if err != nil {
		return nil, "", fmt.Errorf("provenance: create leaf cert: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, "", fmt.Errorf("provenance: parse leaf cert: %w", err)
	}
	leafPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return leaf, leafPEM, nil
}

// VerifyCertificateChain validates a Fulcio-style leaf code-signing certificate against roots at time at.
// In the Sigstore keyless model, at MUST be the signed transparency log IntegratedTime.
func VerifyCertificateChain(certPEM string, roots *x509.CertPool, at time.Time) (*VerifiedCertIdentity, error) {
	if roots == nil {
		return nil, fmt.Errorf("%w: nil root cert pool", ErrCertInvalid)
	}
	if at.IsZero() {
		return nil, fmt.Errorf("%w: verification timestamp is required", ErrCertInvalid)
	}
	block, rest := pem.Decode([]byte(strings.TrimSpace(certPEM)))
	if block == nil || block.Type != "CERTIFICATE" || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, fmt.Errorf("%w: invalid CERTIFICATE PEM", ErrCertInvalid)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: parse certificate: %v", ErrCertInvalid, err)
	}
	if cert.IsCA {
		return nil, fmt.Errorf("%w: CA certificate cannot sign artifacts", ErrCertInvalid)
	}
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:       roots,
		CurrentTime: at,
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
	}); err != nil {
		return nil, fmt.Errorf("%w: x509 verify at %s failed: %v", ErrCertInvalid, at.UTC().Format(time.RFC3339), err)
	}

	var subject string
	if len(cert.URIs) > 0 {
		subject = cert.URIs[0].String()
	} else if len(cert.EmailAddresses) > 0 {
		subject = cert.EmailAddresses[0]
	} else {
		return nil, fmt.Errorf("%w: certificate missing SAN URI or email identity", ErrCertInvalid)
	}

	out := &VerifiedCertIdentity{
		Subject:   subject,
		SerialHex: strings.ToLower(cert.SerialNumber.Text(16)),
		PublicKey: cert.PublicKey,
		NotBefore: cert.NotBefore,
		NotAfter:  cert.NotAfter,
	}
	kid, err := KeyIDFromPublicKey(cert.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("%w: derive keyid: %v", ErrCertInvalid, err)
	}
	out.KeyID = kid

	var issuerV1 string
	for _, ext := range cert.Extensions {
		switch {
		case ext.Id.Equal(OIDFulcioIssuerV2):
			if s, ok := decodeASN1String(ext.Value); ok {
				out.Issuer = s
			}
		case ext.Id.Equal(OIDFulcioIssuer):
			issuerV1 = string(ext.Value)
		case ext.Id.Equal(OIDFulcioBuildSignerURI):
			if s, ok := decodeASN1String(ext.Value); ok {
				out.BuildSignerURI = s
			}
		case ext.Id.Equal(OIDFulcioSourceRepoURI):
			if s, ok := decodeASN1String(ext.Value); ok {
				out.SourceRepositoryURI = s
			}
		case ext.Id.Equal(OIDFulcioSourceRepoDigest):
			if s, ok := decodeASN1String(ext.Value); ok {
				out.SourceRepositoryDigest = s
			}
		}
	}
	if out.Issuer == "" {
		out.Issuer = issuerV1
	}
	if out.Issuer == "" {
		return nil, fmt.Errorf("%w: certificate missing Fulcio OIDC issuer extension", ErrCertInvalid)
	}
	return out, nil
}

func decodeASN1String(raw []byte) (string, bool) {
	var s string
	rest, err := asn1.Unmarshal(raw, &s)
	if err == nil && len(rest) == 0 {
		return s, true
	}
	return "", false
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("provenance: generate serial: %w", err)
	}
	if n.Sign() <= 0 {
		n.SetInt64(1)
	}
	return n, nil
}
