// Command host is the local network daemon spawned by `hanzo network start` (#4).
//
// It shares the fused composition root with cmd/cloud (`cloud.Serve(apps.Wire(), nil)`),
// while setting user-writable local defaults for the state directory, ZAP unix
// socket (`host.zap.sock`), and persistent master key when launched without
// environment overrides.
package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps"
)

func hostStateDir() string {
	if xdg := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdg != "" {
		return filepath.Join(xdg, "hanzo", "host")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "hanzo", "host")
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "hanzo", "host")
	}
	return filepath.Join(home, ".local", "share", "hanzo", "host")
}

func ensureLocalHostEnv() {
	stateDir := hostStateDir()
	_ = os.MkdirAll(stateDir, 0o700)

	if os.Getenv("CLOUD_DATA_DIR") == "" {
		dataDir := filepath.Join(stateDir, "data")
		_ = os.MkdirAll(dataDir, 0o700)
		_ = os.Setenv("CLOUD_DATA_DIR", dataDir)
	}

	if os.Getenv("CLOUD_ZAP_LISTEN") == "" && runtime.GOOS != "windows" {
		_ = os.Setenv("CLOUD_ZAP_LISTEN", filepath.Join(stateDir, "host.zap.sock"))
	}

	if os.Getenv("CLOUD_ENABLE_STAGED") == "" {
		_ = os.Setenv("CLOUD_ENABLE_STAGED", "functions")
	}

	if os.Getenv("CLOUD_KMS_MASTER_KEY_REF") == "" && os.Getenv("CLOUD_DEV_UNENCRYPTED") == "" {
		keyPath := filepath.Join(stateDir, "master.key")
		if b, err := os.ReadFile(keyPath); err == nil {
			if k := strings.TrimSpace(string(b)); k != "" {
				_ = os.Setenv("CLOUD_KMS_MASTER_KEY_REF", k)
				return
			}
		}
		var raw [32]byte
		if _, err := rand.Read(raw[:]); err == nil {
			encoded := base64.StdEncoding.EncodeToString(raw[:])
			_ = os.WriteFile(keyPath, []byte(encoded+"\n"), 0o600)
			_ = os.Setenv("CLOUD_KMS_MASTER_KEY_REF", encoded)
		}
	}
}

func main() {
	ensureLocalHostEnv()
	if err := cloud.Serve(apps.Wire(), nil); err != nil {
		fmt.Fprintf(os.Stderr, "host: %v\n", err)
		os.Exit(1)
	}
}
