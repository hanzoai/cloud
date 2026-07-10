package jwt

type Header struct {
	Type string `json:"typ"`

	Algorithm string `json:"alg"`
}
