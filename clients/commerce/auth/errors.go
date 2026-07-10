package auth

import "errors"

var ErrorUserExists = errors.New("User already exists.")
var ErrorPasswordMismatch = errors.New("Passwords do not match.")
