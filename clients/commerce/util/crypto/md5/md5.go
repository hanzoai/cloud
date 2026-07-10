package md5

import (
	"crypto/md5"
	"encoding/hex"
)

func Hash(text string) string {
	algorithm := md5.New()
	algorithm.Write([]byte(text))
	return hex.EncodeToString(algorithm.Sum(nil))
}
