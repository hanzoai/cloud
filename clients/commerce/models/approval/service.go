package approval

import "errors"

// Actions that drive an approval's state machine.
const (
	ActionApprove = "approve"
	ActionReject  = "reject"
)

// ErrAlreadyResolved is returned when an action is taken on an approval that
// has already left the pending state.
var ErrAlreadyResolved = errors.New("approval: already resolved")

// ErrUnknownAction is returned for an action that is neither approve nor reject.
var ErrUnknownAction = errors.New("approval: unknown action")

// NextStatus computes the status an approval moves to when action is applied to
// its current status. Only a pending approval is transitionable: "approve"
// yields approved, "reject" yields rejected. Any action on an already-resolved
// approval returns ErrAlreadyResolved; an unrecognized action returns
// ErrUnknownAction.
func NextStatus(current string, action string) (string, error) {
	if current != StatusPending {
		return current, ErrAlreadyResolved
	}
	switch action {
	case ActionApprove:
		return StatusApproved, nil
	case ActionReject:
		return StatusRejected, nil
	default:
		return current, ErrUnknownAction
	}
}
