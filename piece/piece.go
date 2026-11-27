package piece

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Catalog is the generated file: its provenance and every piece.
type Catalog struct {
	// Source is the pieces tree the catalog was generated from, as repo@commit.
	Source string `json:"source"`
	// Count is len(Pieces), stated so a reader of the file need not count.
	Count  int     `json:"count"`
	Pieces []Piece `json:"pieces"`
}

// Piece is one connector as its source declares it.
type Piece struct {
	// ID is the piece's directory name in the source tree ("slack",
	// "google-sheets") — stable, unique, and the connector id everywhere.
	ID string `json:"id"`
	// Package is the npm name the source publishes it under.
	Package string `json:"package"`
	// Version is the piece's own version at the catalog's revision.
	Version     string   `json:"version"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Categories  []string `json:"categories"`
	// Logo is the file under logo/ ("slack.png"); empty when none was fetched.
	Logo string `json:"logo,omitempty"`
	// Auth is every way the piece accepts a credential, in its declared order.
	// Empty means it needs none.
	Auth     []Auth `json:"auth"`
	Actions  []Op   `json:"actions"`
	Triggers []Op   `json:"triggers"`
	// Core marks a built-in utility (the source's packages/pieces/core):
	// HTTP, schedule, delay, CSV and the like, which reach no third party.
	Core bool `json:"core,omitempty"`
	// Runnable is whether the piece's program is vendored into script and so
	// runs its actions in-process. A piece whose actions need an npm SDK the
	// runtime does not carry is catalogued and connectable, not runnable.
	Runnable bool `json:"runnable,omitempty"`
	// Deprecated is the source's own flag.
	Deprecated bool `json:"deprecated,omitempty"`
}

// Auth kinds, lower-cased from the source's PropertyType.
const (
	OAuth2     = "oauth2"
	SecretText = "secret_text"
	BasicAuth  = "basic_auth"
	CustomAuth = "custom_auth"
	OIDC       = "oidc"
)

// Auth is one credential a piece accepts.
type Auth struct {
	Type        string `json:"type"`
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required"`
	// OAuth is the authorization-code endpoints, for an oauth2 auth.
	OAuth *OAuth `json:"oauth,omitempty"`
	// Props are the fields the credential is made of: every field of a
	// custom_auth, and the extra fields an oauth2 asks for before consent.
	Props []Prop `json:"props,omitempty"`
	// Validate is whether the piece checks a credential live itself.
	Validate bool `json:"validate,omitempty"`
}

// OAuth is an oauth2 auth's protocol declaration.
type OAuth struct {
	AuthURL  string   `json:"authUrl"`
	TokenURL string   `json:"tokenUrl"`
	Scopes   []string `json:"scopes"`
	PKCE     bool     `json:"pkce,omitempty"`
	// PKCEMethod is "plain" or "S256"; empty means S256.
	PKCEMethod string `json:"pkceMethod,omitempty"`
	Prompt     string `json:"prompt,omitempty"`
	// Method is where the client credential goes at the token endpoint:
	// "HEADER" (Basic) or "BODY"; empty means BODY.
	Method string `json:"method,omitempty"`
	// Grant is the grant type when it is not authorization_code.
	Grant string            `json:"grant,omitempty"`
	Extra map[string]string `json:"extra,omitempty"`
}

// Templated reports whether an endpoint is a template filled from the
// connecting account's own fields ("https://{subdomain}.zendesk.com/..."), so
// a deployment-wide OAuth app cannot serve it.
func (o *OAuth) Templated() bool {
	return strings.Contains(o.AuthURL+o.TokenURL, "{")
}

// Op is one action or trigger.
type Op struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Description string `json:"description,omitempty"`
	Props       []Prop `json:"props"`
	// Strategy is a trigger's: POLLING, WEBHOOK, APP_WEBHOOK or MANUAL.
	Strategy string `json:"strategy,omitempty"`
}

// Prop is one input.
type Prop struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
	// Type is the source's PropertyType (SHORT_TEXT, NUMBER, DROPDOWN, ...).
	Type     string   `json:"type"`
	Required bool     `json:"required,omitempty"`
	Options  []Option `json:"options,omitempty"`
	// Default is the value the piece fills in when none is given.
	Default any `json:"default,omitempty"`
}

// Option is one choice of a static dropdown.
type Option struct {
	Label string `json:"label"`
	Value any    `json:"value"`
}

// Secret reports whether a prop holds a secret, so a form masks it and
// nothing ever reads it back.
func (p Prop) Secret() bool { return p.Type == "SECRET_TEXT" }

// Input reports whether a prop is something a caller supplies. A MARKDOWN
// prop is instructions shown beside the form, not a field.
func (p Prop) Input() bool { return p.Type != "MARKDOWN" }

// Parse reads a generated catalog and checks it agrees with itself: the
// count it states, and one piece per id.
func Parse(data []byte) (Catalog, error) {
	var c Catalog
	if err := json.Unmarshal(data, &c); err != nil {
		return Catalog{}, fmt.Errorf("piece: catalog: %w", err)
	}
	if c.Count != len(c.Pieces) {
		return Catalog{}, fmt.Errorf("piece: catalog says %d pieces and holds %d", c.Count, len(c.Pieces))
	}
	seen := make(map[string]bool, len(c.Pieces))
	for _, p := range c.Pieces {
		if seen[p.ID] {
			return Catalog{}, fmt.Errorf("piece: duplicate id %q", p.ID)
		}
		seen[p.ID] = true
	}
	return c, nil
}
