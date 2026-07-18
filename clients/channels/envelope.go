package channels

import (
	"fmt"
	"strings"
)

// envelope.go is the portable chat envelope — the ONE message shape every
// transport normalizes into and renders out of (the OpenClaw contract port).
// Every union is closed and kind-tagged; nothing here infers meaning from
// string shape.

// RoomKind classifies where a message lives.
type RoomKind string

const (
	RoomDM     RoomKind = "dm"
	RoomGroup  RoomKind = "group"
	RoomThread RoomKind = "thread"
)

func (k RoomKind) valid() bool {
	switch k {
	case RoomDM, RoomGroup, RoomThread:
		return true
	}
	return false
}

// ActionKind tags the closed Action union.
type ActionKind string

const (
	ActionCommand  ActionKind = "command"
	ActionURL      ActionKind = "url"
	ActionSelect   ActionKind = "select"
	ActionApproval ActionKind = "approval"
)

// AttachmentKind is the closed attachment media class.
type AttachmentKind string

const (
	AttachmentImage AttachmentKind = "image"
	AttachmentAudio AttachmentKind = "audio"
	AttachmentVideo AttachmentKind = "video"
	AttachmentFile  AttachmentKind = "file"
)

// Sender identifies who sent an inbound message. UserID is the bound Hanzo
// subject (integrations.LinkedSubject) and may be empty when the platform user
// has not linked. Org is filled on ingress and ignored on egress.
type Sender struct {
	ExternalID string `json:"externalId"`
	Display    string `json:"display,omitempty"`
	UserID     string `json:"userId,omitempty"`
	Org        string `json:"org,omitempty"`
}

// Room is the conversation a message lives in.
type Room struct {
	ID   string   `json:"id"`
	Kind RoomKind `json:"kind,omitempty"`
}

// Attachment is a URL-addressed media item.
type Attachment struct {
	Kind AttachmentKind `json:"kind"`
	URL  string         `json:"url"`
	MIME string         `json:"mime,omitempty"`
}

func (a Attachment) validate() error {
	switch a.Kind {
	case AttachmentImage, AttachmentAudio, AttachmentVideo, AttachmentFile:
	default:
		return fmt.Errorf("attachment: unknown kind %q", a.Kind)
	}
	if a.URL == "" {
		return fmt.Errorf("attachment %s: url required", a.Kind)
	}
	return nil
}

// SelectOption is one choice of a select action.
type SelectOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// Approval names the approval request an approval action refers to.
type Approval struct {
	ID string `json:"id"`
}

// Action is the kind-tagged closed union (command | url | select | approval);
// exactly the fields of its Kind are set — validate enforces per-kind
// exclusivity so a channel never has to guess from string shape.
type Action struct {
	Kind     ActionKind     `json:"kind"`
	Label    string         `json:"label,omitempty"`
	Command  string         `json:"command,omitempty"`
	URL      string         `json:"url,omitempty"`
	Options  []SelectOption `json:"options,omitempty"`
	Approval *Approval      `json:"approval,omitempty"`
}

func (a Action) validate() error {
	switch a.Kind {
	case ActionCommand:
		if a.Command == "" {
			return fmt.Errorf("action command: command required")
		}
		if a.URL != "" || len(a.Options) > 0 || a.Approval != nil {
			return fmt.Errorf("action command: only command may be set")
		}
	case ActionURL:
		if a.URL == "" {
			return fmt.Errorf("action url: url required")
		}
		if a.Command != "" || len(a.Options) > 0 || a.Approval != nil {
			return fmt.Errorf("action url: only url may be set")
		}
	case ActionSelect:
		if len(a.Options) == 0 {
			return fmt.Errorf("action select: at least one option required")
		}
		for _, o := range a.Options {
			if o.Label == "" || o.Value == "" {
				return fmt.Errorf("action select: option label and value required")
			}
		}
		if a.Command != "" || a.URL != "" || a.Approval != nil {
			return fmt.Errorf("action select: only options may be set")
		}
	case ActionApproval:
		if a.Approval == nil || a.Approval.ID == "" {
			return fmt.Errorf("action approval: approval id required")
		}
		if a.Command != "" || a.URL != "" || len(a.Options) > 0 {
			return fmt.Errorf("action approval: only approval may be set")
		}
	default:
		return fmt.Errorf("action: unknown kind %q", a.Kind)
	}
	return nil
}

// Message is the normalized envelope. Account is informational — the lowercased
// external id of the org's connected platform account; the policy key is
// (org, channel) only.
type Message struct {
	Channel     string       `json:"channel"`
	Account     string       `json:"account,omitempty"`
	Sender      Sender       `json:"sender"`
	Room        Room         `json:"room"`
	Text        string       `json:"text,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	Actions     []Action     `json:"actions,omitempty"`
	ReplyTo     string       `json:"replyTo,omitempty"`
	Idempotency string       `json:"idempotency,omitempty"`
}

func (m *Message) validate() error {
	if m.Channel == "" {
		return fmt.Errorf("message: channel required")
	}
	if m.Room.ID == "" {
		return fmt.Errorf("message: room id required")
	}
	if !m.Room.Kind.valid() {
		return fmt.Errorf("message: unknown room kind %q", m.Room.Kind)
	}
	if err := validateContent(m.Text, m.Attachments, m.Actions); err != nil {
		return err
	}
	m.Account = strings.ToLower(m.Account)
	return nil
}

// SendRequest is the narrow body of POST /v1/channels/:channel/send — the
// envelope's outbound projection. Identity fields (Sender, Account, Channel)
// are not decodable here: the route path names the channel and the caller's
// authenticated org supplies the tenant.
type SendRequest struct {
	Room        Room         `json:"room"`
	Text        string       `json:"text,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	Actions     []Action     `json:"actions,omitempty"`
	ReplyTo     string       `json:"replyTo,omitempty"`
	Idempotency string       `json:"idempotency,omitempty"`
}

func (r SendRequest) validate() error {
	// Room.Kind may be empty on egress: the transport doors address a room by
	// id alone; kind is an ingress classification.
	if r.Room.ID == "" {
		return fmt.Errorf("send: room id required")
	}
	if r.Room.Kind != "" && !r.Room.Kind.valid() {
		return fmt.Errorf("send: unknown room kind %q", r.Room.Kind)
	}
	return validateContent(r.Text, r.Attachments, r.Actions)
}

// validateContent is the shared content rule: something to say, and every
// attachment/action well-formed.
func validateContent(text string, attachments []Attachment, actions []Action) error {
	if text == "" && len(attachments) == 0 {
		return fmt.Errorf("text or attachments required")
	}
	for _, a := range attachments {
		if err := a.validate(); err != nil {
			return err
		}
	}
	for _, a := range actions {
		if err := a.validate(); err != nil {
			return err
		}
	}
	return nil
}

// Delivery is a transport's send receipt. Timestamp is Unix seconds.
type Delivery struct {
	MessageID string `json:"messageId"`
	Timestamp int64  `json:"timestamp"`
}

// renderText is the ONE deterministic downgrade renderer: all four transports
// advertise media:false / actions:false this pass, so attachments and actions
// flatten to one line each after the text. Native rendering is a named
// follow-up. Called only on validated messages (an approval action carries a
// non-nil Approval).
func renderText(m Message) string {
	var b strings.Builder
	b.WriteString(m.Text)
	for _, a := range m.Attachments {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(string(a.Kind) + ": " + a.URL)
		if a.MIME != "" {
			b.WriteString(" (" + a.MIME + ")")
		}
	}
	for _, a := range m.Actions {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		switch a.Kind {
		case ActionCommand:
			b.WriteString("[" + a.Label + "] " + a.Command)
		case ActionURL:
			b.WriteString("[" + a.Label + "] " + a.URL)
		case ActionSelect:
			labels := make([]string, 0, len(a.Options))
			for _, o := range a.Options {
				labels = append(labels, o.Label)
			}
			b.WriteString("[" + a.Label + "] " + strings.Join(labels, " | "))
		case ActionApproval:
			b.WriteString("[" + a.Label + "] approval requested: " + a.Approval.ID)
		}
	}
	return b.String()
}
