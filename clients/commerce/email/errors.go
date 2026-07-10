package email

import (
	"errors"
)

var IntegrationShouldNotBeNilError = errors.New("No email providers have been set up.")
