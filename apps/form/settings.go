package form

// settings.go — the SuperAdmin surface over the platform's settings of forms:
// which field types forms may ask and how large each answer may be, the limits
// every form and submission is held to, the sinks a new form takes, and how long
// a response is kept. The document is product `form`'s fleet configuration in
// the settings store, which records it before and after on the audit trail; the
// boundary audits every request under /v1/admin.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	settingspeer "github.com/hanzoai/cloud/client/settings"
	"github.com/hanzoai/cloud/sdk"
	forms "github.com/hanzoai/form"
	"github.com/zap-proto/zip"
)

// adminPrefix is the address. A constant because cmd/zipdoc resolves a typed op's
// path from the constant value of its Group argument.
const adminPrefix = "/v1/admin/form"

// product is the settings store's name for the document.
const product = "form"

// adminRoutes registers the two ops. The group asks platform authority first on
// the routed path, and each op asks again, because MCP and the call plane invoke
// an op with no route in front of it.
func adminRoutes(app cloud.Router) {
	g := app.Group(adminPrefix, cloud.Gate(cloud.Super))
	g.Use(cloud.Bridge())
	g.Get("/settings", getSettings, zip.WithOperationID("get_form_settings"))
	g.Put("/settings", setSettings, zip.WithOperationID("set_form_settings"))
}

// FormSettings is the platform's settings of forms, and whether they are saved.
type FormSettings struct {
	// Saved is false when nothing is stored and the defaults are in force.
	Saved bool `json:"saved"`
	// Settings are the settings in force.
	Settings forms.Settings `json:"settings"`
}

// super admits platform authority, owner == "admin", and nobody else.
func super(ctx context.Context) error {
	if !cloud.Super.Admits(cloud.AuthorityIn(ctx)) {
		return cloud.Super.Refusal()
	}
	return nil
}

// peerBudget bounds a call to the settings store.
const peerBudget = 10 * time.Second

// fetch reads the saved document; saved is false when none is.
func fetch(ctx context.Context) (forms.Settings, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, peerBudget)
	defer cancel()
	out, err := settingspeer.SettingsFleet(ctx, &client.Product{Product: product})
	if err != nil {
		return forms.Settings{}, false, err
	}
	raw := ""
	if out != nil {
		raw = strings.TrimSpace(out.Config)
	}
	if raw == "" {
		return forms.Defaults(), false, nil
	}
	// Read onto the defaults: a key the document leaves out (one saved before
	// the key existed) is the default, never zero.
	st := forms.Defaults()
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return forms.Defaults(), false, nil
	}
	if clean, problems := st.Check(); len(problems) == 0 {
		return clean, true, nil
	}
	return forms.Defaults(), false, nil
}

// Reads the platform's settings of forms: every field type with whether forms may
// ask it and its bound, the limits every form and submission is held to (fields,
// hidden values, body bytes, submissions per address per window, webhooks), the
// sinks a new form takes when it names none, and the days a response is kept.
// `saved` is false when nothing is stored and the defaults are in force.
//
// SuperAdmin only. 503 when the settings store does not answer.
func getSettings(ctx context.Context, _ *cloud.Unit) (*FormSettings, error) {
	if err := super(ctx); err != nil {
		return nil, err
	}
	st, saved, err := fetch(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "form: the settings could not be read: %v", err)
	}
	return &FormSettings{Saved: saved, Settings: st}, nil
}

// Replaces the platform's settings of forms whole and answers them as stored.
// Every field type is listed once, enabled or not, each bound inside what the
// store holds; every limit is inside its range; the default sinks name no field.
// A type turned off stays answerable on the forms that already ask it, and is
// refused on a form that adds it. The forms service reads the change within 30
// seconds.
//
// SuperAdmin only. 400 names every problem at once; 503 when the settings store
// does not take the change.
//
// A limit the document leaves out, forward and welcome among them, is the
// default.
//
// Example: {"types":[{"type":"short_text","enabled":true,"max":255}],"limits":{"fields":100,"hidden":10,"body":65536,"rate":30,"window":3600,"webhooks":10,"forward":60,"welcome":60},"sinks":{"events":true,"crm":{}},"retention":0}
func setSettings(ctx context.Context, in *forms.Settings) (*FormSettings, error) {
	if err := super(ctx); err != nil {
		return nil, err
	}
	if err := cloud.CSRF(ctx); err != nil {
		return nil, err
	}
	st, problems := in.Check()
	if len(problems) > 0 {
		return nil, zip.ErrBadRequest("form: " + strings.Join(problems, "; "))
	}
	doc, err := json.Marshal(st)
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, peerBudget)
	defer cancel()
	if _, err := settingspeer.SettingsFleetSet(pctx, &client.FleetConfig{Product: product, Config: string(doc)}); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "form: the settings store did not take the change: %v", err)
	}
	cached.Store(&held{st: st, at: time.Now()})
	return &FormSettings{Saved: true, Settings: st}, nil
}
