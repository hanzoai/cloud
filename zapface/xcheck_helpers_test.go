package zapface

import (
	"encoding/hex"
	"os"
	"testing"
)

func testingEnv(k string) string { return os.Getenv(k) }

func mustHex(t *testing.T, s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex: %v", err)
	}
	return b
}

func hexEncode(b []byte) string { return hex.EncodeToString(b) }

func writeStdout(s string) { _, _ = os.Stdout.WriteString(s) }

func orNull(b []byte) []byte {
	if len(b) == 0 {
		return []byte("null")
	}
	return b
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
