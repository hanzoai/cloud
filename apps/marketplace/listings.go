package marketplace

// listings.go — an org's offers: publish, edit, unpublish, and the proof behind
// each that the org owns what it sells.
//
// A listing sells a real thing, and the app that owns each kind of thing is
// asked, as the seller, whether it is the seller's: agents for an agent or a
// persona, projects for an app, the tool plane for a skill, an MCP server or a
// tool. The seller's word is never the proof, and a thing that does not exist and
// one another org owns are refused alike.
//
// A tool is sold per call, and a call runs in the CALLER's scope: an org's own
// function, agent or server is listed to that org alone, so no other org could
// install or call one, and a per-call offer of it would be an offer nobody can
// take. What every org reaches is the platform's — its connector actions and
// first-party services — and only the platform sells those. An org's own things
// are sold as what they are: an agent or persona, a skill, an MCP server, an app.

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/client"
	agentpeer "github.com/hanzoai/cloud/client/agent"
	projectpeer "github.com/hanzoai/cloud/client/project"
	toolpeer "github.com/hanzoai/cloud/client/tool"
	trainpeer "github.com/hanzoai/cloud/client/train"
	walletpeer "github.com/hanzoai/cloud/client/wallet"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/bus"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/text"
	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"
)

// listingPage is the caller org's own published listings.
type listingPage struct {
	// Listings is every listing this org has published, private ones included
	// (Public says which are discoverable by others).
	Listings []Listing `json:"listings"`
}

// listQuery narrows the caller org's own listings.
type listQuery struct {
	// Kind keeps one kind: agent, persona, app, skill, mcp, tool, expert or capability.
	Kind string `json:"kind"`
}

// ListListings returns the listings the caller's own org has published — what this
// org is offering, not what it can buy — optionally of one kind. A publisher only
// ever sees its own rows.
func (o marketOps) listListings(ctx context.Context, in *listQuery) (*listingPage, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if in.Kind != "" && !slices.Contains(Kinds, in.Kind) {
		return nil, zip.ErrBadRequest("kind is one of " + strings.Join(Kinds, ", "))
	}
	rows, err := o.s.State.store.ListByOrg(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	rows = slices.DeleteFunc(rows, func(l Listing) bool { return in.Kind != "" && l.Kind != in.Kind })
	if rows == nil {
		rows = []Listing{}
	}
	dressed := o.s.State.expertise.dress(ctx, rows)
	for i := range rows {
		mine(ctx, &rows[i])
		if d, ok := dressed[rows[i].ID]; ok {
			d.into(rows[i].Expert)
		}
	}
	return &listingPage{Listings: rows}, nil
}

// publishReq offers one thing on the marketplace.
type publishReq struct {
	// Kind is what is sold: agent, persona, app, skill, mcp, tool (the default),
	// expert or capability. A tool is a platform tool, sold per call by the
	// platform alone; an expert is the caller, a person making their time
	// available; a capability is a trained Kai capability adapter of the org's.
	Kind string `json:"kind"`
	// Tool names the thing sold, in the seller's own org: an agent's name or id,
	// an app's project slug or id, a skill's name or id, a Kai capability's name,
	// an MCP server's name or id registered by hand, or a platform tool's registry
	// name. It must be the seller's — there are no phantom listings. An expert's
	// is the caller's own IAM identity, and is left out.
	Tool string `json:"tool"`
	// Title is the shop-window name, 1-200 characters. Required.
	Title string `json:"title"`
	// Description is the long copy, at most 4096 characters.
	Description string `json:"description"`
	// Category groups the listing in the shop window.
	Category string `json:"category"`
	// Price is the price as a decimal USD string, exact to 18 places — per call
	// for a tool or capability, what a job is offered at otherwise. "0.0025" is a
	// quarter of a cent and stays one. Empty or "0" publishes it free; a positive
	// price requires Recipient.
	Price string `json:"price"`
	// Currency denominates Price.
	Currency string `json:"currency"`
	// Recipient is the seller's payout wallet id, in the publishing org — the
	// wallet x402 pays. Required for a monetized listing.
	Recipient string `json:"recipient"`
	// Public makes the listing discoverable by other orgs. Private otherwise.
	Public bool `json:"public"`
	// Docs is where the listing's documentation lives: an https URL or a path on
	// docs.hanzo.ai.
	Docs string `json:"docs"`
	// Expert is the person behind a listing of kind expert — their hourly rate,
	// areas, languages, time zone and weekly hours. Required for kind expert and
	// refused for any other. The price is then the fixed engagement price; an
	// expert sets a rate, a price or both.
	Expert *expertIn `json:"expert"`
	// Capability carries optional publisher preferences for a listing of kind
	// capability (such as its open-source license). Refused on any other kind.
	Capability *capabilityIn `json:"capability"`
}

// Publish offers one thing the caller's org owns on the marketplace — an agent,
// persona, app, skill, Kai capability or MCP server, or, for the platform, a tool
// sold per call — optionally monetized. An expert is the exception to the org's
// admin: any member lists themself, once, as a person (kind expert), and is the
// listing's owner beside the org's admins. The owning app is asked, as the org,
// whether the thing is the org's, so a listing never sells something that does
// not exist or belongs to someone else: a tool every org reaches is the
// platform's, and a server enabled off the public shelf is the shelf's. A
// monetized listing names a payout wallet of the org's own, which wallets
// confirms, so a price is never unpayable. The price is exact to 18 decimal
// places, so a per-call price below a cent is a real price and not a rounded-away
// zero. An org admin publishes, at most 1000 listings an org; it answers 201 with
// the created row.
//
// Example: {"kind": "agent", "tool": "researcher", "title": "Research analyst", "price": "250", "recipient": "wal_9f2", "public": true}
func (o marketOps) publish(ctx context.Context, in *publishReq) (*Listing, error) {
	kind := strings.TrimSpace(in.Kind)
	var org, person string
	var err error
	if kind == KindExpert {
		org, person, err = self(ctx)
	} else {
		org, err = publisher(ctx)
	}
	if err != nil {
		return nil, err
	}
	if kind == "" {
		kind = KindTool
	}
	if !slices.Contains(Kinds, kind) {
		return nil, zip.ErrBadRequest("kind is one of " + strings.Join(Kinds, ", "))
	}
	thing := strings.TrimSpace(in.Tool)
	switch {
	case kind == KindExpert && thing != "" && thing != person:
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "unknown expert: %s", thing)
	case kind == KindExpert:
		thing = person
	case thing == "" || len(thing) > maxName:
		return nil, zip.ErrBadRequest("tool is required")
	}
	l := Listing{PublisherOrg: org, Kind: kind, Tool: thing, Currency: strings.TrimSpace(in.Currency),
		Recipient: strings.TrimSpace(in.Recipient), Public: in.Public}
	if err := l.edit(in.Title, in.Description, in.Category, in.Price, in.Docs); err != nil {
		return nil, err
	}
	if kind == KindExpert {
		l.Expert = &Expert{Person: person}
		if err := l.Expert.apply(in.Expert); err != nil {
			return nil, err
		}
		if err := l.sells(); err != nil {
			return nil, err
		}
	} else if in.Expert != nil {
		return nil, zip.ErrBadRequest("expert is for a listing of kind expert")
	}
	if kind != KindCapability && in.Capability != nil {
		return nil, zip.ErrBadRequest("capability is for a listing of kind capability")
	}
	if err := l.payable(ctx); err != nil {
		return nil, err
	}
	switch kind {
	case KindExpert:
		l.Ref = person
	case KindCapability:
		capOwned, err := trainpeer.TrainCapabilityOwned(ctx, &client.CapabilityOwnedIn{Name: thing})
		if err != nil {
			return nil, peerErr("train", err)
		}
		if !capOwned.Mine {
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "unknown capability: %s", thing)
		}
		l.Tool = capOwned.Name
		l.Ref = client.CapabilityResource(org, capOwned.SHA256)
		l.Capability, err = capabilityFromOwned(capOwned, l.Price, in.Capability)
		if err != nil {
			return nil, err
		}
	default:
		if l.Ref, err = own(ctx, org, kind, thing, projectOf(ctx)); err != nil {
			return nil, err
		}
	}
	st := o.s.State.store
	if kind == KindExpert {
		if listed, err := st.taken(ctx, org, person); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "publish: %v", err)
		} else if listed {
			return nil, zip.Errorf(http.StatusConflict, "you are already listed as an expert in this org: edit that listing")
		}
	}
	if n, err := st.counted(ctx, org); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "publish: %v", err)
	} else if n >= maxShop {
		return nil, zip.Errorf(http.StatusConflict, "an org publishes at most %d listings; unpublish one first", maxShop)
	}
	created, err := st.Create(ctx, l)
	switch {
	case dialect.Unique(err):
		return nil, zip.Errorf(http.StatusConflict, "you are already listed as an expert in this org: edit that listing")
	case err != nil:
		return nil, zip.Errorf(http.StatusInternalServerError, "publish: %v", err)
	}
	record(ctx, o.s, org, "listing:"+created.ID, "published", http.StatusCreated)
	mine(ctx, &created)
	if created.Expert != nil {
		o.state(org, eventListed, created.ID, person, append(listingFacts(created), client.Signal{Name: "public", Value: strconv.FormatBool(created.Public)}))
		if created.Public {
			// The public feed's line for it: an expert listed. It names the org and no one else.
			go func() {
				_ = bus.Announce(bus.Pulse{Kind: "expert", Key: created.ID, Org: org, Title: "Expert listed"})
			}()
		}
	}
	return &created, nil
}

// patchReq edits a listing. Every field is optional: an absent one keeps its
// value. What a listing sells — its kind and the thing — is never edited; that is
// a new listing.
type patchReq struct {
	// ID is the listing to edit, from the path.
	ID string `json:"id"`
	// Title is the shop-window name, 1-200 characters.
	Title *string `json:"title"`
	// Description is the long copy, at most 4096 characters.
	Description *string `json:"description"`
	// Category groups the listing in the shop window.
	Category *string `json:"category"`
	// Price is the decimal USD price, exact to 18 places; "0" makes it free.
	Price *string `json:"price"`
	// Currency denominates Price.
	Currency *string `json:"currency"`
	// Recipient is the payout wallet id, in the publishing org.
	Recipient *string `json:"recipient"`
	// Public makes the listing discoverable by other orgs, or withdraws it.
	Public *bool `json:"public"`
	// Docs is where the listing's documentation lives; empty clears it.
	Docs *string `json:"docs"`
	// Expert replaces an expert listing's whole profile — rate, areas, languages,
	// time zone and weekly hours; absent keeps it. Refused on any other kind.
	Expert *expertIn `json:"expert"`
	// Capability updates a capability listing's license; refused on any other kind.
	Capability *capabilityIn `json:"capability"`
}

// Edits one of the caller org's listings — its copy, price, payout wallet,
// visibility and documentation, and an expert's profile — under the same rules
// publish applies, and answers the listing as it now stands. Another org's
// listing and one that does not exist are the same 404. An org admin edits, and
// an expert edits their own listing.
//
// Example: {"id": "lst_4f1c", "price": "300", "public": true}
func (o marketOps) update(ctx context.Context, in *patchReq) (*Listing, error) {
	org, err := o.editor(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	st := o.s.State.store
	l, found, err := st.Get(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "update: %v", err)
	}
	if !found {
		return nil, zip.ErrNotFound("listing not found")
	}
	title, description, category, price, docs := l.Title, l.Description, l.Category, l.Price.String(), l.Docs
	for _, f := range []struct {
		to   *string
		from *string
	}{{&title, in.Title}, {&description, in.Description}, {&category, in.Category}, {&price, in.Price}, {&docs, in.Docs},
		{&l.Currency, in.Currency}, {&l.Recipient, in.Recipient}} {
		if f.from != nil {
			*f.to = strings.TrimSpace(*f.from)
		}
	}
	if in.Public != nil {
		l.Public = *in.Public
	}
	if err := l.edit(title, description, category, price, docs); err != nil {
		return nil, err
	}
	switch {
	case in.Expert != nil && l.Expert == nil:
		return nil, zip.ErrBadRequest("expert is for a listing of kind expert")
	case in.Expert != nil:
		if err := l.Expert.apply(in.Expert); err != nil {
			return nil, err
		}
	}
	if in.Capability != nil && l.Kind != KindCapability {
		return nil, zip.ErrBadRequest("capability is for a listing of kind capability")
	}
	if l.Kind == KindCapability && l.Capability != nil {
		lic, err := resolveLicense(l.Price, in.Capability, l.Capability.License)
		if err != nil {
			return nil, err
		}
		l.Capability.License = lic
	}
	if err := l.sells(); err != nil {
		return nil, err
	}
	if err := l.payable(ctx); err != nil {
		return nil, err
	}
	if l.Currency == "" {
		l.Currency = "USD"
	}
	changed, err := st.Update(ctx, l)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "update: %v", err)
	}
	if !changed {
		return nil, zip.ErrNotFound("listing not found")
	}
	record(ctx, o.s, org, "listing:"+l.ID, "updated", http.StatusOK)
	out, _, err := st.Get(ctx, org, l.ID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "update: %v", err)
	}
	mine(ctx, &out)
	return &out, nil
}

// listingRef addresses one listing. The id is the path segment: the URL is the
// addressing authority, so it binds from there whatever else arrives.
type listingRef struct {
	// ID is the listing, from the path.
	ID string `json:"id"`
}

// Unpublish withdraws one of the caller org's listings from the marketplace and
// answers 204. Only the publishing org can remove its own listing; an id that is
// unknown, or belongs to another org, is the same 404, so a probe learns nothing
// about what exists. Removing a listing removes its price from per-call enforcement;
// it does not uninstall the tool for anyone who already installed it, and a job
// already opened through it runs to its end. An org admin unpublishes, and an
// expert unpublishes their own listing.
//
// Example: {"id": "lst_1"}
func (o marketOps) unpublish(ctx context.Context, in *listingRef) (*cloud.Unit, error) {
	org, err := o.editor(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	removed, err := o.s.State.store.Delete(ctx, org, id)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "unpublish: %v", err)
	}
	if !removed {
		return nil, zip.ErrNotFound("listing not found")
	}
	record(ctx, o.s, org, "listing:"+id, "unpublished", http.StatusOK)
	return nil, nil
}

// publisher is the org a listing or seller act is for: the caller's own, and only by one of
// its admins, or a SuperAdmin.
func publisher(ctx context.Context) (string, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return "", err
	}
	if !cloud.Admin.Admits(cloud.AuthorityIn(ctx)) {
		return "", zip.ErrForbidden("an org admin publishes and edits the org's listings")
	}
	return org, nil
}

// edit sets a listing's copy and price, refusing what publish refuses.
func (l *Listing) edit(title, description, category, price, docs string) error {
	title = strings.TrimSpace(title)
	if title == "" || len(title) > maxTitle {
		return zip.ErrBadRequest("title is required (<=200 chars)")
	}
	if len(description) > maxText {
		return zip.ErrBadRequest("description too long")
	}
	amount, err := money.ParseUSD(strings.TrimSpace(price))
	if err != nil {
		return zip.ErrBadRequest("price must be a decimal USD amount with at most 18 decimals")
	}
	if amount.IsNeg() {
		return zip.ErrBadRequest("price must be >= 0")
	}
	docs = strings.TrimSpace(docs)
	if !validDocs(docs) {
		return zip.ErrBadRequest("docs is an https URL or a path on docs.hanzo.ai")
	}
	l.Title, l.Description, l.Category = title, text.Trim(description, maxText), text.Trim(category, maxText)
	l.Price, l.Docs = amount, docs
	return nil
}

// payable refuses a price nobody could pay: a monetized listing names a payout
// wallet, and the wallet is one of the publishing org's own — wallets is asked,
// as that org, and resolves an id only within it.
func (l *Listing) payable(ctx context.Context) error {
	if l.Price.Sign() <= 0 && (l.Expert == nil || l.Expert.Rate.Sign() <= 0) {
		return nil
	}
	if l.Recipient == "" {
		return zip.ErrBadRequest("a monetized listing requires a recipient wallet")
	}
	p, err := walletpeer.WalletsPayee(ctx, &client.PayeeIn{WalletID: l.Recipient})
	if err != nil {
		return peerErr("wallets", err)
	}
	if !p.Found {
		return zip.Errorf(http.StatusUnprocessableEntity, "recipient %s is not a wallet of this org", l.Recipient)
	}
	return nil
}

// validDocs admits an https URL or an absolute path, and nothing a page could
// turn into script or another host: no scheme but https, no protocol-relative
// path, nothing past 2048 bytes.
func validDocs(docs string) bool {
	switch {
	case docs == "":
		return true
	case len(docs) > maxURL, strings.ContainsAny(docs, " \t\r\n\"'<>\\"):
		return false
	case strings.HasPrefix(docs, "https://"):
		return len(docs) > len("https://")
	}
	return strings.HasPrefix(docs, "/") && !strings.HasPrefix(docs, "//")
}

// own proves the org on ctx owns thing, a thing of kind, asking the app that
// owns that kind. It answers the id that app knows the thing by — for a tool, the
// resource a call of it is priced by. A thing that does not exist and one another
// org owns are the same 422.
func own(ctx context.Context, org, kind, thing, project string) (string, error) {
	unknown := zip.Errorf(http.StatusUnprocessableEntity, "unknown %s: %s", kind, thing)
	switch kind {
	case KindAgent, KindPersona:
		roster, err := agentpeer.AgentsRoster(ctx, &client.RosterIn{})
		if err != nil {
			return "", peerErr("agents", err)
		}
		for _, a := range roster.Agents {
			if a.ID == thing || a.Name == thing {
				return a.ID, nil
			}
		}
		return "", unknown
	case KindApp:
		o, err := projectpeer.ProjectsOwnership(ctx, &client.OwnerIn{IDOrSlug: thing})
		if err != nil {
			return "", peerErr("projects", err)
		}
		if !o.Mine {
			return "", unknown
		}
		return thing, nil
	}
	of := map[string]string{KindSkill: client.OwnedSkill, KindMCP: client.OwnedMCP, KindTool: client.OwnedTool}[kind]
	o, err := toolpeer.ToolsOwned(ctx, &client.OwnedIn{Kind: of, Ref: thing, Project: project})
	if err != nil {
		return "", peerErr("the tool plane", err)
	}
	if kind != KindTool {
		if !o.Mine {
			return "", unknown
		}
		return o.ID, nil
	}
	switch {
	case o.Platform && asPlatform(ctx, org):
		return client.ToolResource(o.Source, o.ID), nil
	case o.Platform:
		return "", zip.ErrForbidden(fmt.Sprintf("%s is a platform tool every org reaches: only the platform sells it", thing))
	case o.Mine:
		return "", zip.Errorf(http.StatusUnprocessableEntity,
			"%s is your org's own %s, which no other org can reach to call: sell it as an agent, persona, skill or mcp listing", thing, o.Source)
	}
	return "", unknown
}

// asPlatform is whether the caller publishes for the platform itself: a SuperAdmin
// acting in the reserved admin org, never in an org it is inspecting.
func asPlatform(ctx context.Context, org string) bool {
	return org == authz.AdminOrg && cloud.Super.Admits(cloud.AuthorityIn(ctx))
}
