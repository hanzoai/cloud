package website


type Type string

const (
	// Analytics
	Test Type = "test"
	Staging Type = "staging"
	Production Type = "prod"
)
type Website struct {
	// Just basic links to their profiles
	Url string `json:"url,omitempty"`
	PasswordResetPath string `json:"passwordResetPath,omitempty"`
	EmailConfirmPath string `json:"emailConfirmPath,omitempty"`
	Type Type `json:"type,omitempty"`
}
