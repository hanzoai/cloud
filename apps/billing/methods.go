package billing

// methods.go serves the saved-card family of /v1/billing — the cards and
// accounts a customer has on file, at the two addresses that answer for them.
//
// TWO ADDRESSES, ONE ANSWER. /v1/billing/methods is the customer's own; the
// portal spelling is the same list under the name a hosted checkout addresses it
// by. Both reach one core in the process that owns the rows, so a card added on
// one cannot be missing from the other — which is the whole reason the second
// spelling is a route here rather than a second implementation.
//
// The wire is RENDERED rather than typed, and that is deliberate: a saved method
// carries five kinds of processor detail plus a billing address, all of them the
// store's own models, so a mirror in this package would be sixty fields whose
// only job is to agree with something else. The bytes the store produced cross
// whole. A value the endpoint DECIDES is typed; a document it forwards is bytes.

import (
	"context"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/plane"
	commercepeer "github.com/hanzoai/cloud/plane/commerce"
	"github.com/zap-proto/zip"
)

// mountMethods registers the saved-card family at both of its addresses. Called
// from routes.
func mountMethods(app cloud.Router, o ops) {
	// The two READS and the SAVE are byte relays — they answer the document
	// commerce rendered, verbatim — so they stay raw and declare their prose
	// beside the routes (init, below). The DETACH answers a value, so it is a
	// typed op; it is registered TWICE explicitly rather than from the loop
	// because cmd/zipdoc refuses a computed path, and a route it cannot place is
	// prose silently dropped from the document and the MCP tool.
	for _, at := range []string{"/v1/billing/methods", "/v1/billing/portal/methods"} {
		app.Get(at, cloud.Handle(o.s, listMethods))
		app.Post(at, cloud.Handle(o.s, saveMethod))
	}
	zapp := cloud.ZipApp(app)
	zip.Delete(zapp, "/v1/billing/methods/:id", o.detachMethod)
	zip.Delete(zapp, "/v1/billing/portal/methods/:id", o.detachPortalMethod)
}

// The family is RAW at both addresses because its answer is a rendered document
// rather than a value this endpoint decides, so its prose is declared beside the
// routes. Declared through the same registry Register uses, so each renders only
// while the router actually serves it.
func init() {
	const saved = "A saved method is a card or account VAULTED at the processor: what is " +
		"stored here is the processor's token for it plus the last four digits and the " +
		"expiry a customer recognises it by, never a card number.\n\n" +
		"The list is the caller's OWN — the wallet this request bills from, resolved " +
		"server-side — so a query cannot widen it to another customer of the same org.\n\n" +
		"`/v1/billing/portal/methods` answers the same list under the name a hosted " +
		"checkout addresses it by. One set of rows, two spellings; a card added at " +
		"either is present at both."

	for _, at := range []string{"/v1/billing/methods", "/v1/billing/portal/methods"} {
		openapi.Describe(at, http.MethodGet,
			"Cards and accounts on file for the caller",
			"Answers every payment method the caller has saved, newest first.\n\n"+saved+"\n\n"+
				"A store that cannot be read answers an EMPTY LIST rather than a failure: the "+
				"saved-cards panel renders empty instead of breaking the page around it.")
		openapi.Describe(at, http.MethodPost,
			"Save a card or account for the caller",
			"Vaults the instrument at the processor and stores the row.\n\n"+saved+"\n\n"+
				"Saving a card ALREADY on file answers with the row that already holds it "+
				"rather than stacking a duplicate — 200 for that, 201 for a genuinely new "+
				"row, so a client can tell which happened. A card the processor declines is "+
				"402 and nothing is stored.")
		openapi.Describe(at+"/:id", http.MethodDelete,
			"Remove one saved card or account",
			"Detaches the method at the processor and drops the row.\n\n"+
				"A method the caller does not own is NOT FOUND rather than refused — the same "+
				"answer whether the id names nothing or names somebody else's card — so an id "+
				"cannot be probed for existence.\n\n"+
				"A platform operator or the trusted in-process service token may act on any "+
				"subject inside the org; everyone else may only remove their own.")
	}
}

// listMethods answers the caller's saved methods.
//
// Raw rather than typed, for the reason the file header gives: the body is the
// store's rendered document and is forwarded whole.
func listMethods(s *cloud.Service[state], c *zip.Ctx) error {
	org, subject, err := payerOf(c)
	if err != nil {
		return err
	}
	out, aerr := ask(c.Context(), org, "methods", func(ctx context.Context) (*plane.Rendered, error) {
		return commercepeer.BillingMethods(ctx, &plane.MethodsIn{Subject: subject, Kind: c.Query("type")})
	})
	if aerr != nil {
		return aerr
	}
	return document(c, http.StatusOK, out.Body)
}

// saveMethod vaults an instrument and stores the row.
//
// The body crosses VERBATIM: the vaulting path has to see what the browser
// produced, and re-shaping it here would be this endpoint deciding which
// processor details are worth keeping.
func saveMethod(s *cloud.Service[state], c *zip.Ctx) error {
	org, subject, err := payerOf(c)
	if err != nil {
		return err
	}
	out, aerr := ask(c.Context(), org, "save method", func(ctx context.Context) (*plane.Rendered, error) {
		return commercepeer.BillingMethodSave(ctx, &plane.MethodSaveIn{
			Subject: subject,
			// The caller's OWN address, off its credential: it names the
			// processor's customer profile, and the store must never read an
			// identity it was not handed.
			Email: callerEmail(c),
			Body:  c.Body(),
		})
	})
	if aerr != nil {
		return aerr
	}
	// 201 only for a row that did not exist. A duplicate save answers with the
	// row that already holds the card, and 200 states "nothing new" honestly.
	status := http.StatusOK
	if out.Created {
		status = http.StatusCreated
	}
	return document(c, status, out.Body)
}

// callerEmail is the caller's OWN address, off the identity the edge minted.
//
// It names the processor's customer profile, so it is read here and handed over
// rather than resolved in the store: a store that read an identity it was not
// given would be vaulting a card against a profile nobody proved. Empty is
// legitimate — a machine credential has no mailbox — and the processor names the
// profile some other way.
func callerEmail(c *zip.Ctx) string { return cloud.Who(c.Context()).Email }

// document writes a rendered answer back as the store produced it.
//
// It is one function so the four raw endpoints in this package cannot come to
// disagree about the header, and it writes the bytes untouched — re-marshalling
// them would make this endpoint a second renderer of somebody else's document.
func document(c *zip.Ctx, status int, body []byte) error {
	c.SetHeader("Content-Type", "application/json")
	return c.Bytes(status, body)
}
