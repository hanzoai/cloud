package bot

// Identity is Hanzo IAM's answer, reported here — never minted, approved or
// revoked.
//
// OpenClaw pairs a device: the client mints an Ed25519 key, asks to be let in,
// an operator approves the request, and the gateway issues that device a token.
// None of that happens in this cloud. A client arrives carrying a credential
// the identity boundary has already validated, or it is refused before a frame
// is read (bot.go, caller). Between "unknown" and "admitted" there is no state
// left for an operator to decide, so there is no pairing request to approve, no
// token to rotate, and no credential of this surface's own to revoke. Access is
// granted and withdrawn in IAM.
//
// What remains is real, and is what this file serves: which clients are
// identified to this org right now, what each of them may do, a name an
// operator can give one, and the ability to end one's sessions.
//
//	device.pair.list    who is connected, and what IAM lets them do
//	device.pair.rename  the operator's own name for one
//	device.pair.remove  end that client's sessions and forget its name
//
// device.pair.approve, device.pair.reject, device.token.rotate,
// device.token.revoke and the whole node.pair.* family are absent rather than
// refused. hello.features.methods is how this protocol states what a gateway
// does, and the UI hides a control whose method is not listed, so a method that
// could only ever answer "there is no such request" is better not offered than
// offered and always failing.
//
// The shapes are src/gateway/device-pairing-list.types.ts: DevicePairingList
// is { pending, paired }, and the UI reads a row's operatorLabel, displayName,
// scopes, connected and lastSeenAtMs (ui/src/lib/nodes/inventory.ts:213-243).

import (
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

func init() {
	Register("device.pair.list", Pairing, devicePairList)
	Register("device.pair.rename", Pairing, devicePairRename)
	Register("device.pair.remove", Pairing, devicePairRemove)
	Declare(deviceChanged)
}

const (
	// deviceDocs is the collection an operator's labels live in, keyed by the
	// device the label names.
	deviceDocs = "device"

	// deviceChanged tells the other operators watching that the roster moved,
	// so their view refetches instead of going stale. It is the protocol's own
	// event name (src/gateway/events.ts:10), and the devices page reloads on it
	// (ui/src/pages/devices/devices-page.ts:216).
	deviceChanged = "device.pair.changed"

	// maxDeviceLabel is DevicePairLabelString's bound, counted in characters
	// the way the schema counts them.
	maxDeviceLabel = 64
)

// pairedDevice is one identified client, in the shape the UI calls
// PairedDevice. Every field it carries is something IAM or the connection
// answered for.
//
// tokens is deliberately absent. There is no stored credential to summarise,
// and a token row in that list draws a rotate and a revoke control
// (ui/src/pages/devices/view-inventory.ts:334) for two methods this surface
// does not offer. Role and scopes say what the client may do without implying a
// secret this gateway holds.
type pairedDevice struct {
	DeviceID      string   `json:"deviceId"`
	DisplayName   string   `json:"displayName,omitempty"`
	OperatorLabel string   `json:"operatorLabel,omitempty"`
	Role          string   `json:"role"`
	Scopes        []string `json:"scopes"`
	Connected     bool     `json:"connected"`
	LastSeenAtMs  int64    `json:"lastSeenAtMs"`
}

// devicePairing is DevicePairingList. Pending is always empty and is always
// present: a client is admitted by IAM before it reaches this surface, so no
// request is ever waiting, and the UI reads the length of this array to decide
// whether to raise its pairing overlay (ui/src/app/overlays-access.ts:76).
type devicePairing struct {
	Pending []any          `json:"pending"`
	Paired  []pairedDevice `json:"paired"`
}

// deviceRenamed is what a rename answers, matching the TypeScript handler
// (src/gateway/server-methods/devices.ts:570).
type deviceRenamed struct {
	DeviceID string `json:"deviceId"`
	Label    string `json:"label"`
}

// deviceLabel is a stored operator label: the whole of what this surface
// durably knows about a device.
type deviceLabel struct {
	Label   string `json:"label"`
	Updated int64  `json:"updated"`
}

type devicePairListParams struct{}

type devicePairRenameParams struct {
	DeviceID string `json:"deviceId"`
	Label    string `json:"label"`
}

type devicePairRemoveParams struct {
	DeviceID string `json:"deviceId"`
}

// deviceOf names one identified client: the IAM user, and the bot the client
// bound itself to. Two browser windows of the same person are one device,
// because nothing here tells them apart — device keys were not ported, so the
// finest identity available is the one IAM answers with. The name is stable
// across reconnects, which is what lets an operator's label outlive a socket.
func deviceOf(user, bot string) string {
	if bot == "" {
		return user
	}
	return user + "/" + bot
}

// deviceStore opens the file the roster's labels live in: the org's own, never
// a bot's. The list spans every connection the org has open, so a label on one
// of them must not depend on which bot the operator's own connection happened
// to be bound to.
func deviceStore(c *Call) (*Store, error) {
	st, err := c.svc.State.stores.For(c.Org(), "")
	if err != nil {
		c.Log().Error("open bot store", "org", c.Org(), "err", err)
		return nil, Unavailable("the store could not be opened")
	}
	return st, nil
}

// devicePairList answers who is connected. Every row is a live client, so every
// row reports connected, and an org with nobody on it answers with an empty
// roster rather than with remembered names.
func devicePairList(c *Call) (any, error) {
	var p devicePairListParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	out := []pairedDevice{}
	seen := map[string]bool{}
	for _, k := range c.svc.State.hub.reach(c.Org()) {
		id := deviceOf(k.me.user, k.me.bot)
		if seen[id] {
			continue // one row per identity, however many sockets it holds
		}
		seen[id] = true
		out = append(out, pairedDevice{
			DeviceID:     id,
			DisplayName:  k.me.user,
			Role:         role,
			Scopes:       k.held().strings(),
			Connected:    true,
			LastSeenAtMs: now,
		})
	}
	// The hub is a map, so its order is arbitrary; the roster is sorted to hold
	// still between refreshes.
	slices.SortFunc(out, func(a, b pairedDevice) int { return strings.Compare(a.DeviceID, b.DeviceID) })

	st, err := deviceStore(c)
	if err != nil {
		return nil, err
	}
	for i := range out {
		var l deviceLabel
		err := st.Get(c.Context(), deviceDocs, out[i].DeviceID, &l)
		if errors.Is(err, ErrNoDoc) {
			continue // nobody has named this one
		}
		if err != nil {
			return nil, err
		}
		out[i].OperatorLabel = l.Label
	}
	return devicePairing{Pending: []any{}, Paired: out}, nil
}

// devicePairRename records an operator's name for a device. The name is the one
// thing this surface keeps about a client, and it outlives the client's socket
// because it is stored under the identity rather than under the connection.
func devicePairRename(c *Call) (any, error) {
	var p devicePairRenameParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(p.DeviceID)
	label := strings.TrimSpace(p.Label)
	if label == "" {
		return nil, Invalid("label required")
	}
	if utf8.RuneCountInString(label) > maxDeviceLabel {
		return nil, Invalid("label exceeds %d characters", maxDeviceLabel)
	}
	// The id has to name a device that is here before it is written anywhere:
	// what a live connection answers with is already bounded, so a hostile id
	// never reaches the store.
	if sockets(c, id) == 0 {
		return nil, Invalid("unknown deviceId")
	}
	st, err := deviceStore(c)
	if err != nil {
		return nil, err
	}
	if err := st.Put(c.Context(), deviceDocs, id, deviceLabel{Label: label, Updated: time.Now().UnixMilli()}); err != nil {
		return nil, err
	}
	PublishOrg(c.Org(), "", deviceChanged, struct{}{})
	return deviceRenamed{DeviceID: id, Label: label}, nil
}

// devicePairRemove ends a device's sessions and forgets its name — everything
// this surface holds about it. It does not withdraw the device's access: the
// credential it presented is IAM's, so the client may come back, and the place
// to stop that is IAM. What the operator gets here is the session ended now.
//
// The connection asking is left running, so that it hears this answer. When the
// operator removes the device they are using, the reply says so by reporting
// the device still connected.
func devicePairRemove(c *Call) (any, error) {
	var p devicePairRemoveParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(p.DeviceID)

	var name string
	var held []string
	matched, ended := 0, 0
	for _, k := range c.svc.State.hub.reach(c.Org()) {
		if deviceOf(k.me.user, k.me.bot) != id {
			continue
		}
		matched++
		name, held = k.me.user, k.held().strings()
		if k == c.conn {
			continue
		}
		// drop, not stop: the roster is the hub, so a device removed from one
		// has to leave the other in the same breath.
		c.svc.State.hub.drop(k)
		ended++
	}
	if matched == 0 {
		return nil, Invalid("unknown deviceId")
	}

	st, err := deviceStore(c)
	if err != nil {
		return nil, err
	}
	if err := st.Delete(c.Context(), deviceDocs, id); err != nil {
		return nil, err
	}
	PublishOrg(c.Org(), "", deviceChanged, struct{}{})
	return pairedDevice{
		DeviceID:     id,
		DisplayName:  name,
		Role:         role,
		Scopes:       held,
		Connected:    ended < matched,
		LastSeenAtMs: time.Now().UnixMilli(),
	}, nil
}

// sockets counts the org's open connections that belong to one device.
func sockets(c *Call, id string) int {
	n := 0
	for _, k := range c.svc.State.hub.reach(c.Org()) {
		if deviceOf(k.me.user, k.me.bot) == id {
			n++
		}
	}
	return n
}
