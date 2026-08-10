// appearance.go is the signed-in caller's own reading of the Hanzo design system —
// text size, spacing density and the one accent hue — stored ON THEIR IAM ACCOUNT
// so it follows them across devices and every Hanzo surface (hanzo.app, hanzo.chat,
// the console, the id account pages), and set from any of them.
//
// It is the SHARED write path for the browser surfaces that cannot hold IAM's
// confidential credential: the console and hanzo.chat are static SPAs and the id
// pages are their own frontends, so none can call IAM's privileged update-user
// directly. hanzo.app's own Next BFF does this same read-merge-write for a Next
// app; this is that logic at the unified api host, as the confidential
// `hanzo-console` client, always targeting the ALREADY-VALIDATED caller's own row.
//
// The preference lives in the IAM user row's `properties` bag — the map that
// already holds a person's oauth tokens — under one JSON key, so nothing in the
// IAM schema changes to gain it. The write is the SAME whole-row re-submit
// setAvatar uses: read the full row, change ONE property, write it back, so every
// other field (the password hash above all) is preserved. And the accent is
// validated as a real colour token before it is stored, because a surface renders
// it into a `<style>` body and the value is owner-chosen — a crafted `;}` must
// never inject CSS.

package account

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/zap-proto/zip"
)

// appearance is a person's own reading of the design system. Every field is
// OPTIONAL: an unset axis is ABSENT, never a neutral value — the same law
// @hanzo/appearance's storage keeps, so an empty preference cannot stamp a scale
// over a brand that set its own.
type appearance struct {
	// Type is the text-size multiplier, clamped to the ramp window [0.85, 1.4].
	// Absent (0) leaves the published default.
	Type float64 `json:"type,omitempty"`
	// Density is the spacing step: "compact", "default" or "comfortable".
	Density string `json:"density,omitempty"`
	// Accent is the one hue — a CSS colour token (a hex, or a bounded functional
	// colour like rgb()/oklch()). Anything else is dropped rather than stored.
	Accent string `json:"accent,omitempty"`
}

// accentGrammar is the SAME colour grammar hanzo.app's writer enforces: a hex, or
// a bounded functional-colour form. Anything carrying ; { } < or url( cannot
// match, so an owner-chosen accent can never break out of the `<style>` body a
// surface renders it into.
var accentGrammar = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3,4}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$|^(?:rgb|rgba|hsl|hsla|oklch|oklab|lab|lch|color)\([0-9a-zA-Z.,%\s/+-]{1,60}\)$`)

// cleanAppearance keeps only valid axes and drops the rest — one bad axis narrows
// the result, it never refuses the whole thing. The type scale is clamped to the
// window @hanzo/design (and the head boot script) enforce.
func cleanAppearance(in appearance) appearance {
	var out appearance
	if in.Type != 0 && !math.IsNaN(in.Type) && !math.IsInf(in.Type, 0) {
		out.Type = math.Min(1.4, math.Max(0.85, in.Type))
	}
	switch in.Density {
	case "compact", "default", "comfortable":
		out.Density = in.Density
	}
	if a := strings.TrimSpace(in.Accent); accentGrammar.MatchString(a) {
		out.Accent = a
	}
	return out
}

// GetAppearance returns the signed-in caller's own appearance preference — text
// size, density and accent — read from their IAM account so it is the same on
// every device and every Hanzo surface. An unset preference is an empty object.
//
// A transient IAM read failure reports the empty preference rather than a 5xx, so
// a surface applies its published default and never error-toasts on load — the
// same fail-soft the key read uses.
func (o ops) getAppearance(ctx context.Context, _ *noInput) (*appearance, error) {
	cr, c, ok := requestCaller(ctx, false)
	if !ok {
		return nil, zip.ErrForbidden("sign in to read your appearance")
	}
	if !o.s.State.iam.configured() {
		return nil, notConfigured("appearance")
	}
	pref, err := o.s.State.iam.getAppearance(c.Context(), cr.id)
	if err != nil {
		o.s.Log.Warn("get appearance: iam read failed (reporting default)", "err", err)
		return &appearance{}, nil
	}
	cleaned := cleanAppearance(pref)
	return &cleaned, nil
}

// SetAppearance stores the caller's appearance preference on their IAM account,
// preserving every other field of the row. The accent is validated as a real
// colour token before it is stored; an unset or invalid axis is dropped rather
// than stored.
//
// Example: {"type": 1.15, "density": "compact", "accent": "#8b5cf6"}
func (o ops) setAppearance(ctx context.Context, in *appearance) (*appearance, error) {
	cr, c, ok := requestCaller(ctx, false)
	if !ok {
		return nil, zip.ErrForbidden("sign in to save your appearance")
	}
	if !o.s.State.iam.configured() {
		return nil, notConfigured("appearance")
	}
	cleaned := cleanAppearance(*in)
	if err := o.s.State.iam.setAppearance(c.Context(), cr.id, cleaned); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "could not save your appearance: %v", err)
	}
	return &cleaned, nil
}

// getAppearance reads the caller's stored preference out of their IAM row's
// properties bag. An absent or unreadable value is an empty preference, never an
// error the caller has to handle.
func (c *iamClient) getAppearance(ctx context.Context, id string) (appearance, error) {
	owner, name := splitID(id)
	rowRaw, err := c.getUser(ctx, owner, name)
	if err != nil {
		return appearance{}, err
	}
	var row map[string]any
	if err := json.Unmarshal(rowRaw, &row); err != nil {
		return appearance{}, fmt.Errorf("iam get-user: decode: %w", err)
	}
	props, _ := row["properties"].(map[string]any)
	raw, _ := props["appearance"].(string)
	if raw == "" {
		return appearance{}, nil
	}
	var pref appearance
	if json.Unmarshal([]byte(raw), &pref) != nil {
		return appearance{}, nil
	}
	return pref, nil
}

// setAppearance records the preference on the caller's IAM row, in the properties
// bag under one JSON key — the SAME whole-row re-submit setAvatar uses, so every
// other field (the password hash, oauth tokens) is preserved. update-user
// overwrites the default column set, so a partial body would blank the row; this
// reads the full row first and refuses to write one it could not read.
func (c *iamClient) setAppearance(ctx context.Context, id string, pref appearance) error {
	owner, name := splitID(id)
	rowRaw, err := c.getUser(ctx, owner, name)
	if err != nil {
		return err
	}
	var row map[string]any
	if err := json.Unmarshal(rowRaw, &row); err != nil {
		return fmt.Errorf("iam get-user: decode: %w", err)
	}
	props, _ := row["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
	}
	blob, err := json.Marshal(pref)
	if err != nil {
		return err
	}
	props["appearance"] = string(blob)
	row["properties"] = props
	body, err := json.Marshal(row)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodPost, "/v1/iam/update-user", url.Values{"id": {id}}, body)
	return err
}
