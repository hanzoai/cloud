package rand

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	mathrand "math/rand"
	"strings"
	"time"
)

// Returns short, url-friendly Id
func ShortPassword() string {
	size := 16

	rb := make([]byte, size)
	if _, err := rand.Read(rb); err != nil {
		fmt.Printf("Failed to genrate random characters: %v", err)
	}

	return strings.Trim(base64.URLEncoding.EncodeToString(rb), "=")
}

// Returns short, url-friendly Id
func ShortId() string {
	size := 8

	rb := make([]byte, size)
	if _, err := rand.Read(rb); err != nil {
		fmt.Printf("Failed to genrate random characters: %v", err)
	}

	return strings.Trim(base64.URLEncoding.EncodeToString(rb), "=")
}

func SecretKey() string {
	// 75% of 256 bytes
	size := 192

	rb := make([]byte, size)
	if _, err := rand.Read(rb); err != nil {
		fmt.Printf("Failed to genrate random characters: %v", err)
	}

	return strings.Trim(base64.URLEncoding.EncodeToString(rb), "=")
}

func Int() int {
	return mathrand.Int()
}

func Int32() int32 {
	return mathrand.Int31()
}

func Int64() int64 {
	return mathrand.Int63()
}

func init() {
	mathrand.Seed(time.Now().UTC().UnixNano())
}
