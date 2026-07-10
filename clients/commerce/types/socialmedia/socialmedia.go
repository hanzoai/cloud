package socialmedia

type SocialMedia struct {
	// Just basic links to their profiles
	Facebook string `json:"facebook,omitempty"`
	Twitter string `json:"twitter,omitempty"`
	Instagram string `json:"instagram,omitempty"`
	Gplus string `json:"gplus,omitempty"`
}
