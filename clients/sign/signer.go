package sign

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/digitorus/pdf"
	pdfsign "github.com/digitorus/pdfsign/sign"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// signer is the deployment's e-signature authority: the x509 certificate + RSA
// key used to apply a real PKCS#7/CMS digital signature to a sealed PDF. It is
// process-global (per deployment), NOT per-tenant — the platform is the sealing
// authority, exactly as Documenso's local P12 signer is one cert for the app.
//
// It is sourced, in order:
//  1. KMS-custodied PEM: CLOUD_SIGN_CERT_PEM + CLOUD_SIGN_KEY_PEM (base64 PEM,
//     injected from KMS) — the production path, cert never in plaintext code.
//  2. A cert+key previously persisted under {DataDir}/sign/signer.{crt,key}.
//  3. A freshly generated self-signed RSA-2048 cert, persisted to (2) so the
//     signing identity is stable across restarts — the zero-config dev default
//     that still yields a genuinely signed PDF.
//
// PRODUCTION GATE: paths (2) and (3) are DEV-ONLY. In a production environment
// the platform MUST seal legal documents with the KMS-custodied cert (1) — a
// self-signed cert, or a plaintext PEM sitting on the PVC, is an untrusted seal.
// newSigner REFUSES to boot in a production env without (1), failing closed.
type signer struct {
	cert  *x509.Certificate
	key   *rsa.PrivateKey
	chain []*x509.Certificate
}

// isProductionEnv reports whether env names a production deployment (any of the
// recognized aliases). The signer refuses a self-signed/disk cert in these envs.
func isProductionEnv(env string) bool {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "production", "prod", "mainnet", "main", "live":
		return true
	default:
		return false
	}
}

func newSigner(dataDir, env string) (*signer, error) {
	if certB64, keyB64 := os.Getenv("CLOUD_SIGN_CERT_PEM"), os.Getenv("CLOUD_SIGN_KEY_PEM"); certB64 != "" && keyB64 != "" {
		certPEM, err := base64.StdEncoding.DecodeString(strings.TrimSpace(certB64))
		if err != nil {
			return nil, fmt.Errorf("sign: decode CLOUD_SIGN_CERT_PEM: %w", err)
		}
		keyPEM, err := base64.StdEncoding.DecodeString(strings.TrimSpace(keyB64))
		if err != nil {
			return nil, fmt.Errorf("sign: decode CLOUD_SIGN_KEY_PEM: %w", err)
		}
		return signerFromPEM(certPEM, keyPEM)
	}

	// No KMS-custodied cert. In production this is fatal: a legal-document seal must
	// never be a self-signed cert or a plaintext PEM on the PVC. Require (1).
	if isProductionEnv(env) {
		return nil, fmt.Errorf("sign: refusing to boot in env %q without a KMS-custodied signing certificate — set CLOUD_SIGN_CERT_PEM + CLOUD_SIGN_KEY_PEM (base64 PEM injected from KMS); a self-signed cert must never seal legal documents in production", env)
	}

	dir := filepath.Join(dataDir, "sign")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("sign: mkdir %s: %w", dir, err)
	}
	crtPath, keyPath := filepath.Join(dir, "signer.crt"), filepath.Join(dir, "signer.key")
	if certPEM, err := os.ReadFile(crtPath); err == nil {
		if keyPEM, err := os.ReadFile(keyPath); err == nil {
			return signerFromPEM(certPEM, keyPEM)
		}
	}

	s, certPEM, keyPEM, err := generateSelfSigned()
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(crtPath, certPEM, 0o600); err != nil {
		return nil, fmt.Errorf("sign: persist cert: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("sign: persist key: %w", err)
	}
	return s, nil
}

func signerFromPEM(certPEM, keyPEM []byte) (*signer, error) {
	cb, _ := pem.Decode(certPEM)
	if cb == nil {
		return nil, fmt.Errorf("sign: no PEM CERTIFICATE block")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, fmt.Errorf("sign: parse certificate: %w", err)
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, fmt.Errorf("sign: no PEM PRIVATE KEY block")
	}
	key, err := parseRSAKey(kb.Bytes)
	if err != nil {
		return nil, err
	}
	return &signer{cert: cert, key: key, chain: []*x509.Certificate{cert}}, nil
}

func parseRSAKey(der []byte) (*rsa.PrivateKey, error) {
	if k, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("sign: parse private key: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("sign: private key is %T, want RSA", k)
	}
	return rk, nil
}

func generateSelfSigned() (*signer, []byte, []byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("sign: generate key: %w", err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "Hanzo Sign", Organization: []string{"Hanzo AI"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(5, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageContentCommitment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("sign: create certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("sign: parse created certificate: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return &signer{cert: cert, key: key, chain: []*x509.Certificate{cert}}, certPEM, keyPEM, nil
}

// signPDF applies a real PKCS#7/CMS certification signature to pdf, returning the
// signed bytes — the actual cryptographic seal, in-memory (no temp files).
func (s *signer) signPDF(in []byte) ([]byte, error) {
	rs := bytes.NewReader(in)
	rdr, err := pdf.NewReader(rs, int64(len(in)))
	if err != nil {
		return nil, fmt.Errorf("sign: parse pdf for signing: %w", err)
	}
	var out bytes.Buffer
	err = pdfsign.Sign(rs, &out, rdr, int64(len(in)), pdfsign.SignData{
		Signature: pdfsign.SignDataSignature{
			Info: pdfsign.SignDataSignatureInfo{
				Name:     "Hanzo Sign",
				Location: "Hanzo Cloud",
				Reason:   "Document execution",
				Date:     time.Now(),
			},
			CertType:   pdfsign.CertificationSignature,
			DocMDPPerm: pdfsign.DoNotAllowAnyChangesPerms,
		},
		Signer:            s.key,
		DigestAlgorithm:   crypto.SHA256,
		Certificate:       s.cert,
		CertificateChains: [][]*x509.Certificate{s.chain},
	})
	if err != nil {
		return nil, fmt.Errorf("sign: seal pdf: %w", err)
	}
	return out.Bytes(), nil
}

// stampSpec is one field value rendered onto the PDF. x/y/w/h are PERCENT (0-100)
// of the page, top-left origin (the Documenso field coordinate system).
type stampSpec struct {
	Page  int     `json:"page"`
	Kind  string  `json:"kind"` // "text" | "image"
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	W     float64 `json:"w"`
	H     float64 `json:"h"`
	Text  string  `json:"text,omitempty"`
	Image string  `json:"image,omitempty"` // base64 PNG/JPEG (no data URL prefix)
}

// stampPDF renders each field value onto its page via pdfcpu watermarks, then
// returns the flattened bytes. Percent field coordinates are converted to points
// against each page's real dimensions; the stamp is centered on the field box.
func stampPDF(in []byte, specs []stampSpec) ([]byte, error) {
	if len(specs) == 0 {
		return in, nil
	}
	conf := model.NewDefaultConfiguration()
	dims, err := api.PageDims(bytes.NewReader(in), conf)
	if err != nil {
		return nil, fmt.Errorf("sign: page dims: %w", err)
	}
	cur := in
	for _, sp := range specs {
		if sp.Page < 1 || sp.Page > len(dims) {
			continue
		}
		pw, ph := dims[sp.Page-1].Width, dims[sp.Page-1].Height
		// field box in points (top-left origin)
		bx := clampFrac(sp.X) * pw
		by := clampFrac(sp.Y) * ph
		bw := sp.W
		bh := sp.H
		boxW := 0.20 * pw
		boxH := 0.05 * ph
		if bw > 0 {
			boxW = clampFrac(bw) * pw
		}
		if bh > 0 {
			boxH = clampFrac(bh) * ph
		}
		// center of the box, in pdfcpu bottom-left coordinates
		cx := bx + boxW/2
		cy := ph - (by + boxH/2)

		var wm *model.Watermark
		switch sp.Kind {
		case "image":
			raw, derr := base64.StdEncoding.DecodeString(sp.Image)
			if derr != nil || len(raw) == 0 {
				continue
			}
			icfg, _, ierr := image.DecodeConfig(bytes.NewReader(raw))
			scale := 1.0
			if ierr == nil && icfg.Width > 0 {
				// fit the image to the box width (native px ≈ pt at 72dpi)
				scale = boxW / float64(icfg.Width)
			}
			desc := fmt.Sprintf("pos:bl, off:%.1f %.1f, scale:%.4f abs, rot:0", cx, cy, scale)
			wm, err = api.ImageWatermarkForReader(bytes.NewReader(raw), desc, true, false, types.POINTS)
		default: // text
			if strings.TrimSpace(sp.Text) == "" {
				continue
			}
			pts := int(boxH * 0.6)
			if pts < 8 {
				pts = 8
			}
			if pts > 24 {
				pts = 24
			}
			desc := fmt.Sprintf("pos:bl, off:%.1f %.1f, scale:1 abs, rot:0, fillc:#0A0A0A, points:%d", cx, cy, pts)
			wm, err = api.TextWatermark(sanitizeText(sp.Text), desc, true, false, types.POINTS)
		}
		if err != nil {
			return nil, fmt.Errorf("sign: build watermark: %w", err)
		}
		var buf bytes.Buffer
		if err := api.AddWatermarks(bytes.NewReader(cur), &buf, []string{fmt.Sprintf("%d", sp.Page)}, wm, conf); err != nil {
			return nil, fmt.Errorf("sign: stamp page %d: %w", sp.Page, err)
		}
		cur = buf.Bytes()
	}
	return cur, nil
}

func clampFrac(percent float64) float64 {
	f := percent / 100.0
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

// sanitizeText strips characters that break pdfcpu's watermark description parser
// (commas/newlines are description delimiters).
func sanitizeText(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, ",", " ")
	return strings.TrimSpace(s)
}

// ---- host-function objects injected into the goja runtime -------------------

// pdfHostObject builds the __pdf = { stamp, sign } host object (injected via
// NewBase Config.HostFns) over base64 PDF strings — the PDF/PKI primitive goja
// cannot do. A returned Go error surfaces in JS as a thrown Error the bundle
// catches. Signing ORCHESTRATION stays in the TS bundle; only stamp+seal is Go.
func (s *signer) pdfHostObject() map[string]any {
	return map[string]any{
		"stamp": func(pdfB64, stampsJSON string) (string, error) {
			in, err := base64.StdEncoding.DecodeString(pdfB64)
			if err != nil {
				return "", fmt.Errorf("sign: stamp: bad base64 pdf: %w", err)
			}
			var specs []stampSpec
			if strings.TrimSpace(stampsJSON) != "" {
				if err := json.Unmarshal([]byte(stampsJSON), &specs); err != nil {
					return "", fmt.Errorf("sign: stamp: bad stamps json: %w", err)
				}
			}
			out, err := stampPDF(in, specs)
			if err != nil {
				return "", err
			}
			return base64.StdEncoding.EncodeToString(out), nil
		},
		"sign": func(pdfB64 string) (string, error) {
			in, err := base64.StdEncoding.DecodeString(pdfB64)
			if err != nil {
				return "", fmt.Errorf("sign: sign: bad base64 pdf: %w", err)
			}
			out, err := s.signPDF(in)
			if err != nil {
				return "", err
			}
			return base64.StdEncoding.EncodeToString(out), nil
		},
	}
}
