// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// Package mpcseal is cloud's client-side-CEK sealing client for the SEPARATE
// MPC node ring (ghcr.io/luxfi/mpc). It is the minimal inlined subset of the
// former github.com/hanzoai/kms/sdk/go client that clients/fleet and
// clients/provisioning use to seal per-org secrets (BYO kubeconfigs,
// provisioned-resource passwords) onto the MPC nodes so the nodes only ever
// store ciphertext.
//
// WHY INLINED (HIP-0106 alignment). cloud embeds the canonical luxfi/kms
// SecretStore in-process (clients/kms — the deps.KMS / types.KMSClient), and
// MPC stays its own separate nodes on luxfi/mpc. Dropping that external
// dependency leaves cloud depending only on luxfi/kms (embedded) +
// hanzoai/iam (embedded). This package reproduces the
// SDK's wire protocol and key schedule VERBATIM, so the on-node ciphertext
// format is byte-for-byte unchanged — a pure dependency move, not a behavior
// change.
//
// Zero-knowledge model: all encryption/decryption happens client-side with a
// Customer Encryption Key (CEK) derived from an admin passphrase; the CEK never
// leaves this process, and the MPC nodes only ever store encrypted blobs.
//
//	Passphrase -> Argon2id -> Master Key -> HKDF -> CEK (AES-256-GCM)
//
// FOLLOW-UP (a separate, TESTED change — not this dependency move): fold
// fleet/provisioning sealing into cloud's embedded deps.KMS once
// types.KMSClient gains a Delete verb and the swap is verified against the live
// MPC ring, so there is exactly one KMS surface.
package mpc

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/hkdf"
)

const (
	// cekSaltSuffix is appended to the org slug to derive the Argon2id salt.
	cekSaltSuffix = "hanzo-kms-cek-v1"
	// cekHKDFInfo is the HKDF info parameter for CEK derivation.
	cekHKDFInfo = "cek-aes256gcm"
	// cekSize is the CEK/master-key size in bytes (256-bit AES key).
	cekSize = 32

	// Argon2id parameters per the KMS architecture spec: m=256MiB, t=4, p=2.
	argon2Memory  = 256 * 1024 // 256 MiB in KiB
	argon2Time    = 4
	argon2Threads = 2

	// gcmNonceSize is the standard AES-GCM nonce size.
	gcmNonceSize = 12

	// defaultHTTPTimeout bounds each node call when Config.HTTPClient is nil.
	defaultHTTPTimeout = 30 * time.Second
)

// Config configures a client. Nodes/OrgSlug/Threshold are required; HTTPClient
// is optional (a 30-second-timeout client is used when nil).
type Config struct {
	// Nodes is the list of MPC node addresses
	// (e.g. ["https://kms-mpc-0:9999", "https://kms-mpc-1:9999"]).
	Nodes []string
	// OrgSlug is the organization identifier; it is the AES-GCM AAD and the
	// path scope, so it binds every ciphertext to exactly one tenant.
	OrgSlug string
	// Threshold is the minimum number of nodes required for an operation (t-of-n).
	Threshold int
	// HTTPClient is an optional custom client. nil ⇒ a default 30s-timeout client.
	HTTPClient *http.Client
}

// Client connects to the MPC node ring and provides zero-knowledge secret
// management. All secret data is encrypted client-side with the CEK; the MPC
// nodes only store encrypted blobs. Goroutine-safe.
//
// The zero value is not usable; construct with NewClient.
type Client struct {
	mu        sync.RWMutex
	nodes     []string
	threshold int
	orgSlug   string
	cek       []byte // CEK — client-side only, never transmitted
	http      *http.Client
}

// NewClient creates a client. It is initially locked; call Unlock before any
// Set/Get/Delete.
func NewClient(cfg Config) (*Client, error) {
	if len(cfg.Nodes) == 0 {
		return nil, errors.New("mpcseal: at least one node address is required")
	}
	if cfg.OrgSlug == "" {
		return nil, errors.New("mpcseal: org slug is required")
	}
	if cfg.Threshold < 1 {
		return nil, errors.New("mpcseal: threshold must be at least 1")
	}
	if cfg.Threshold > len(cfg.Nodes) {
		return nil, fmt.Errorf("mpcseal: threshold %d exceeds node count %d", cfg.Threshold, len(cfg.Nodes))
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	return &Client{
		nodes:     cfg.Nodes,
		threshold: cfg.Threshold,
		orgSlug:   cfg.OrgSlug,
		http:      httpClient,
	}, nil
}

// Unlock derives the CEK from the passphrase (client-side only) and holds it in
// memory. The passphrase is never transmitted.
func (c *Client) Unlock(passphrase string) error {
	masterKey, err := deriveMasterKey(passphrase, c.orgSlug)
	if err != nil {
		return fmt.Errorf("mpcseal: unlock: %w", err)
	}
	defer clear(masterKey)
	cek, err := deriveCEK(masterKey, c.orgSlug)
	if err != nil {
		return fmt.Errorf("mpcseal: unlock: %w", err)
	}
	c.mu.Lock()
	c.cek = cek
	c.mu.Unlock()
	return nil
}

// getCEK returns a copy of the CEK or an error if the client is locked.
func (c *Client) getCEK() ([]byte, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.cek == nil {
		return nil, errors.New("mpcseal: client is locked — call Unlock first")
	}
	cekCopy := make([]byte, len(c.cek))
	copy(cekCopy, c.cek)
	return cekCopy, nil
}

// encryptedSecret is the JSON payload exchanged with the MPC nodes; both the
// secret name and value are ciphertext.
type encryptedSecret struct {
	Key   []byte `json:"key"`
	Value []byte `json:"value"`
}

// Set encrypts value client-side with the CEK and broadcasts the encrypted blob
// (encrypted name + value) to the MPC nodes.
func (c *Client) Set(key string, value []byte) error {
	cek, err := c.getCEK()
	if err != nil {
		return err
	}
	defer clear(cek)
	aad := []byte(c.orgSlug)

	encKey, err := sealAESGCM(cek, []byte(key), aad)
	if err != nil {
		return fmt.Errorf("mpcseal: encrypt key: %w", err)
	}
	encValue, err := sealAESGCM(cek, value, aad)
	if err != nil {
		return fmt.Errorf("mpcseal: encrypt value: %w", err)
	}
	data, err := json.Marshal(encryptedSecret{Key: encKey, Value: encValue})
	if err != nil {
		return fmt.Errorf("mpcseal: marshal secret: %w", err)
	}
	path := fmt.Sprintf("/v1/orgs/%s/zk/secrets", url.PathEscape(c.orgSlug))
	return c.broadcastPost(path, data)
}

// Get retrieves an encrypted blob from the MPC nodes and decrypts it
// client-side with the CEK.
func (c *Client) Get(key string) ([]byte, error) {
	cek, err := c.getCEK()
	if err != nil {
		return nil, err
	}
	defer clear(cek)
	aad := []byte(c.orgSlug)

	encKey, err := sealAESGCM(cek, []byte(key), aad)
	if err != nil {
		return nil, fmt.Errorf("mpcseal: encrypt key for lookup: %w", err)
	}
	path := fmt.Sprintf("/v1/orgs/%s/zk/secrets/%s",
		url.PathEscape(c.orgSlug),
		url.PathEscape(base64URLEncode(encKey)),
	)
	body, err := c.quorumGet(path)
	if err != nil {
		return nil, fmt.Errorf("mpcseal: get secret: %w", err)
	}
	var es encryptedSecret
	if err := json.Unmarshal(body, &es); err != nil {
		return nil, fmt.Errorf("mpcseal: unmarshal secret response: %w", err)
	}
	plaintext, err := openAESGCM(cek, es.Value, aad)
	if err != nil {
		return nil, fmt.Errorf("mpcseal: decrypt value: %w", err)
	}
	return plaintext, nil
}

// Delete removes a secret from the MPC nodes. Succeeds when at least threshold
// nodes acknowledge.
func (c *Client) Delete(key string) error {
	cek, err := c.getCEK()
	if err != nil {
		return err
	}
	defer clear(cek)
	aad := []byte(c.orgSlug)

	encKey, err := sealAESGCM(cek, []byte(key), aad)
	if err != nil {
		return fmt.Errorf("mpcseal: encrypt key for delete: %w", err)
	}
	path := fmt.Sprintf("/v1/orgs/%s/zk/secrets/%s",
		url.PathEscape(c.orgSlug),
		url.PathEscape(base64URLEncode(encKey)),
	)

	type result struct{ err error }
	ch := make(chan result, len(c.nodes))
	for _, node := range c.nodes {
		go func(addr string) { ch <- result{err: c.deleteRequest(addr + path)} }(node)
	}
	var successes int
	var lastErr error
	for range c.nodes {
		if r := <-ch; r.err != nil {
			lastErr = r.err
		} else {
			successes++
		}
	}
	if successes < c.threshold {
		return fmt.Errorf("mpcseal: delete: only %d of %d nodes responded (need %d): %w",
			successes, len(c.nodes), c.threshold, lastErr)
	}
	return nil
}

// ── node transport ───────────────────────────────────────────────────────────

// broadcastPost POSTs body to every node and succeeds when at least threshold
// nodes respond 2xx.
func (c *Client) broadcastPost(path string, body []byte) error {
	type result struct {
		node string
		err  error
	}
	ch := make(chan result, len(c.nodes))
	for _, node := range c.nodes {
		go func(addr string) { ch <- result{node: addr, err: c.postJSON(addr+path, body)} }(node)
	}
	var successes int
	var lastErr error
	for range c.nodes {
		r := <-ch
		if r.err != nil {
			lastErr = fmt.Errorf("node %s: %w", r.node, r.err)
		} else {
			successes++
		}
	}
	if successes < c.threshold {
		return fmt.Errorf("mpcseal: only %d of %d nodes responded (need %d): %w",
			successes, len(c.nodes), c.threshold, lastErr)
	}
	return nil
}

// quorumGet GETs path from the nodes and returns the first 2xx body once at
// least threshold nodes have responded successfully.
func (c *Client) quorumGet(path string) ([]byte, error) {
	type result struct {
		body []byte
		err  error
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan result, len(c.nodes))
	for _, node := range c.nodes {
		go func(addr string) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr+path, nil)
			if err != nil {
				ch <- result{err: err}
				return
			}
			resp, err := c.http.Do(req)
			if err != nil {
				ch <- result{err: err}
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				ch <- result{err: fmt.Errorf("status %d", resp.StatusCode)}
				return
			}
			data, err := io.ReadAll(resp.Body)
			ch <- result{body: data, err: err}
		}(node)
	}
	var firstBody []byte
	var successes int
	var lastErr error
	for range c.nodes {
		r := <-ch
		if r.err != nil {
			lastErr = r.err
			continue
		}
		successes++
		if firstBody == nil {
			firstBody = r.body
		}
		if successes >= c.threshold {
			return firstBody, nil
		}
	}
	return nil, fmt.Errorf("mpcseal: only %d of %d nodes responded (need %d): %w",
		successes, len(c.nodes), c.threshold, lastErr)
}

func (c *Client) postJSON(u string, body []byte) error {
	resp, err := c.http.Post(u, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

func (c *Client) deleteRequest(u string) error {
	req, err := http.NewRequest(http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// ── key schedule + AES-256-GCM (client-side, verbatim from the SDK) ───────────

// deriveMasterKey derives the 256-bit master key from a passphrase via Argon2id.
func deriveMasterKey(passphrase, orgSlug string) ([]byte, error) {
	if passphrase == "" {
		return nil, errors.New("mpcseal: passphrase must not be empty")
	}
	if orgSlug == "" {
		return nil, errors.New("mpcseal: org slug must not be empty")
	}
	salt := deriveSalt(orgSlug)
	return argon2.IDKey([]byte(passphrase), salt, argon2Time, argon2Memory, argon2Threads, cekSize), nil
}

// deriveCEK derives the CEK from the master key via HKDF-SHA256.
func deriveCEK(masterKey []byte, orgSlug string) ([]byte, error) {
	if len(masterKey) != cekSize {
		return nil, fmt.Errorf("mpcseal: master key must be %d bytes, got %d", cekSize, len(masterKey))
	}
	r := hkdf.New(sha256.New, masterKey, []byte(orgSlug), []byte(cekHKDFInfo))
	cek := make([]byte, cekSize)
	if _, err := io.ReadFull(r, cek); err != nil {
		return nil, fmt.Errorf("mpcseal: hkdf derive cek: %w", err)
	}
	return cek, nil
}

// deriveSalt = SHA-256(orgSlug || cekSaltSuffix).
func deriveSalt(orgSlug string) []byte {
	h := sha256.Sum256([]byte(orgSlug + cekSaltSuffix))
	return h[:]
}

// sealAESGCM encrypts plaintext with AES-256-GCM. Returns nonce || ciphertext || tag.
func sealAESGCM(key, plaintext, aad []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcmNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("mpcseal: generate nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, aad), nil
}

// openAESGCM decrypts a nonce || ciphertext || tag blob with AES-256-GCM.
func openAESGCM(key, data, aad []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(data) < gcmNonceSize {
		return nil, errors.New("mpcseal: ciphertext too short")
	}
	nonce, ct := data[:gcmNonceSize], data[gcmNonceSize:]
	pt, err := gcm.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, fmt.Errorf("mpcseal: decrypt: %w", err)
	}
	return pt, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != cekSize {
		return nil, fmt.Errorf("mpcseal: aes-256-gcm requires %d-byte key, got %d", cekSize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("mpcseal: aes cipher: %w", err)
	}
	return cipher.NewGCM(block)
}

// base64URLEncode encodes data to URL-safe base64 without padding (the on-wire
// encoding of the encrypted lookup key).
func base64URLEncode(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}
