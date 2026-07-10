package auth

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/util/session"
)

const loginKey = "loggedIn"

func GetCurrentUserId(c *gin.Context) (string, error) {
	log.Debug("Retrieving current id from session")
	value, err := session.Get(c, loginKey)
	if err != nil {
		return "", err
	}

	if value == nil {
		return "", err
	}

	return value.(string), nil
}

func GetCurrentUser(c *gin.Context) (*user.User, error) {
	log.Debug("Retrieving current user from session")
	id, err := GetCurrentUserId(c)
	if err != nil {
		log.Warn("Failed to retrieve current user from session")
		return nil, err
	}

	db := datastore.New(c)
	u := user.New(db)

	if err := u.GetById(id); err != nil {
		log.Warn("Failed to retrieve current user from session")
		return nil, err
	}

	log.Debug("Retrieved current user from session")
	return u, nil
}

// // Validates a form and inserts a new user into the datastore
// // Checks if the Email and Id are unique, and calculates a hash for the password
// func RegisterNewUser(c *gin.Context) (*user.User, error) {
// 	// Parse register form
// 	f := new(RegistrationForm)
// 	if err := f.Parse(c); err != nil {
// 		return nil, err
// 	}

// 	m := f.User
// 	db := datastore.New(c)

// 	// If each query returns no keys, then both fields are unique.
// 	qEmail := db.Query("user").
// 		Filter("Email =", m.Email).
// 		KeysOnly().
// 		Limit(1)

// 	keys, err := qEmail.GetAll(db.Context, nil)
// 	if err != nil {
// 		return nil, err
// 	}

// 	log.Debug("Checking if user exists")
// 	if len(keys) > 0 {
// 		return nil, ErrorUserExists
// 	}

// 	if m.PasswordHash, err = f.PasswordHash(); err != nil {
// 		return nil, err
// 	}

// 	if err = m.Put(); err != nil {
// 		return nil, err
// 	}

// 	return &m, nil
// }

func IsLoggedIn(c *gin.Context) bool {
	value, err := session.Get(c, loginKey)
	if err != nil {
		return false
	}

	userId, _ := value.(string)
	if userId == "" {
		return false
	}

	return true
}

func Login(c *gin.Context, u *user.User) error {
	return session.Set(c, loginKey, u.Id())
}

func Logout(c *gin.Context) error {
	return session.Clear(c)
}
