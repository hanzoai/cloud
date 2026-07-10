package email

// Mailchimp configuration
type List struct {
	// External ID of email list
	Id string `json:"id"`

	// Provider integration ID
	ProviderId string `json:"providerId,omitempty"`

	// Respect double-optin or not
	DoubleOptin bool `json:"doubleOptin"`

	// Whether to update existing contacts
	UpdateExisting bool `json:"updateExisting"`

	// Whether to replace interests
	ReplaceInterests bool `json:"replaceInterests"`

	// Whether this list is enabled or not
	Enabled bool `json:"enabled"`
}
