package aes

import "golang.org/x/crypto/scrypt"

func AES128KeyFromPassword(withPassword, salt []byte) ([]byte, error) {
	return scrypt.Key(withPassword, salt, 32768, 8, 1, 16)
}
