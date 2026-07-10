package sha256

import (
	"crypto/sha256"
	"encoding/hex"
)

func Hash(text string) string {
	algorithm := sha256.New224()
	algorithm.Write([]byte(text))
	return hex.EncodeToString(algorithm.Sum(nil))
}
