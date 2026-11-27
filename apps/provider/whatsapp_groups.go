package provider

import (
	"context"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// whatsappGroupIn creates one group on the caller's business number.
type whatsappGroupIn struct {
	// Subject is the group's name, shown to its members. Required, at most 128 characters.
	Subject string `json:"subject" url:"-"`
	// Description is the group's description. Optional, at most 2048 characters.
	Description string `json:"description,omitempty" url:"-"`
}

// whatsappGroupOut is a group this number owns, and the link that joins it.
type whatsappGroupOut struct {
	// ID is the group id. A reply addresses it with recipient_type group.
	ID string `json:"id"`
	// InviteLink is the link a person opens to join. Anyone holding it can join,
	// so it is answered only to an org admin.
	InviteLink string `json:"invite_link,omitempty"`
}

// whatsappInviteIn names the group whose invite link to read.
type whatsappInviteIn struct {
	// Group is the group id returned when the group was created.
	Group string `json:"group"`
}

// whatsappCreateGroup creates a group on the org's WhatsApp number and returns
// its invite link.
//
// Org admin, because the group speaks as the org's number and the invite link
// admits anyone who holds it.
func (o ops) whatsappCreateGroup(ctx context.Context, in *whatsappGroupIn) (*whatsappGroupOut, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if !orgAdmin(ctx) {
		return nil, zip.ErrForbidden("creating a WhatsApp group requires org admin")
	}
	if in == nil || strings.TrimSpace(in.Subject) == "" {
		return nil, zip.ErrBadRequest("subject is required")
	}
	id, err := CreateWhatsAppGroup(ctx, org, in.Subject, in.Description)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "%s", err.Error())
	}
	link, lerr := WhatsAppInviteLink(ctx, org, id)
	if lerr != nil {
		// The group exists. The link is a second read, and its failure is said
		// with the id so the caller can ask again rather than create another.
		return nil, zip.Errorf(http.StatusBadGateway, "group %s was created but its invite link could not be read", id)
	}
	return &whatsappGroupOut{ID: id, InviteLink: link}, nil
}

// whatsappGroupLink reads the invite link of a group this org's number owns.
//
// Org admin, for the same reason creating one is: the link admits anyone who
// holds it.
func (o ops) whatsappGroupLink(ctx context.Context, in *whatsappInviteIn) (*whatsappGroupOut, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if !orgAdmin(ctx) {
		return nil, zip.ErrForbidden("reading a WhatsApp invite link requires org admin")
	}
	if in == nil || strings.TrimSpace(in.Group) == "" {
		return nil, zip.ErrBadRequest("group is required")
	}
	link, err := WhatsAppInviteLink(ctx, org, in.Group)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "%s", err.Error())
	}
	return &whatsappGroupOut{ID: in.Group, InviteLink: link}, nil
}
