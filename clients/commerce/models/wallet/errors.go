package wallet

import "errors"

var ErrorNoPrivateKeySet = errors.New("No private key is set.")
var ErrorNoSaltSetError = errors.New("No salt is set.")
var ErrorNoEncryptedKeyFound = errors.New("No encrypted key found.")
var ErrorInvalidTypeSpecified = errors.New("Invalid account type specified.")
var ErrorNameCollision = errors.New("Account with name already exists.")
