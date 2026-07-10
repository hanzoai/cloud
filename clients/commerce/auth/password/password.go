package password

import "golang.org/x/crypto/bcrypt"

func HashAndCompare(hash []byte, password string) bool {
	return bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil
}

func Hash(password string) ([]byte, error) {
	if hash, err := bcrypt.GenerateFromPassword([]byte(password), 12); err != nil {
		return nil, err
	} else {
		return hash, nil
	}
}
