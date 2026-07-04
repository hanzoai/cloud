package automations

import (
	"encoding/json"
	"fmt"
)

// FlowOperation apply over the LINEAR step chain (trigger → action → action …).
// This is the exact model the durable engine executes in Phase 1, so the operations
// implemented here are complete and orthogonal to it. A tree-restructuring op that
// the linear model cannot represent (branch/loop/router) is honestly REJECTED
// (errUnsupportedOp) — never silently faked — so the builder gets a truthful answer.

// errUnsupportedOp is returned for a well-formed operation whose semantics require
// the branch/loop/router tree the Phase-1 linear engine does not execute. Handlers
// render it 422.
var errUnsupportedOp = fmt.Errorf("automations: operation not supported by the phase-1 linear flow model")

// applyVersionOperation mutates a version's step tree per op and returns it. It
// covers the version-scoped ops (UPDATE_TRIGGER, ADD_ACTION, UPDATE_ACTION,
// DELETE_ACTION, MOVE_ACTION, CHANGE_NAME). CHANGE_STATUS is flow-scoped and handled
// by the operations HTTP handler, not here.
func applyVersionOperation(v *FlowVersion, op FlowOperation) (*FlowVersion, error) {
	switch op.Type {
	case OpChangeName:
		var r ChangeNameRequest
		if err := json.Unmarshal(op.Request, &r); err != nil {
			return nil, fmt.Errorf("decode CHANGE_NAME: %w", err)
		}
		v.DisplayName = r.DisplayName
		return v, nil

	case OpUpdateTrigger:
		var r UpdateTriggerRequest
		if err := json.Unmarshal(op.Request, &r); err != nil {
			return nil, fmt.Errorf("decode UPDATE_TRIGGER: %w", err)
		}
		trig := FlowTrigger(r)
		// Preserve the existing action chain unless the request supplies a new one.
		if trig.NextAction == nil && v.Trigger != nil {
			trig.NextAction = v.Trigger.NextAction
		}
		v.Trigger = &trig
		return v, nil

	case OpAddAction:
		var r AddActionRequest
		if err := json.Unmarshal(op.Request, &r); err != nil {
			return nil, fmt.Errorf("decode ADD_ACTION: %w", err)
		}
		if r.Action == nil {
			return nil, fmt.Errorf("ADD_ACTION: action is required")
		}
		if err := requireLinear(r.Action.Type); err != nil {
			return nil, err
		}
		if err := insertAfter(v, r.ParentStep, r.Action); err != nil {
			return nil, err
		}
		return v, nil

	case OpUpdateAction:
		var r UpdateActionRequest
		if err := json.Unmarshal(op.Request, &r); err != nil {
			return nil, fmt.Errorf("decode UPDATE_ACTION: %w", err)
		}
		a := findAction(v, r.Name)
		if a == nil {
			return nil, errNotFound
		}
		if r.Type != "" {
			if err := requireLinear(r.Type); err != nil {
				return nil, err
			}
			a.Type = r.Type
		}
		a.Settings = r.Settings
		return v, nil

	case OpDeleteAction:
		var r DeleteActionRequest
		if err := json.Unmarshal(op.Request, &r); err != nil {
			return nil, fmt.Errorf("decode DELETE_ACTION: %w", err)
		}
		for _, name := range r.Names {
			if _, ok := detachAction(v, name); !ok {
				return nil, errNotFound
			}
		}
		return v, nil

	case OpMoveAction:
		var r MoveActionRequest
		if err := json.Unmarshal(op.Request, &r); err != nil {
			return nil, fmt.Errorf("decode MOVE_ACTION: %w", err)
		}
		node, ok := detachAction(v, r.Name)
		if !ok {
			return nil, errNotFound
		}
		if err := insertAfter(v, r.NewParentStep, node); err != nil {
			return nil, err
		}
		return v, nil

	default:
		// A recognized builder op the linear model does not execute.
		return nil, errUnsupportedOp
	}
}

// requireLinear rejects the tree action types the Phase-1 linear engine cannot
// execute, so ADD/UPDATE never plants a step that would silently never run.
func requireLinear(actionType string) error {
	switch actionType {
	case ActionTypePiece, ActionTypeCode, "":
		return nil
	default: // ROUTER / LOOP_ON_ITEMS / BRANCH
		return errUnsupportedOp
	}
}

// ── linear-chain surgery ──────────────────────────────────────────────────────

// findAction returns the action named name in the chain, or nil.
func findAction(v *FlowVersion, name string) *FlowAction {
	if v == nil || v.Trigger == nil {
		return nil
	}
	for a := v.Trigger.NextAction; a != nil; a = a.NextAction {
		if a.Name == name {
			return a
		}
	}
	return nil
}

// insertAfter links node into the chain immediately after the step named parent.
// parent=="" (or the trigger's name) inserts node as the trigger's first action,
// pushing the existing chain down. Returns errNotFound if parent is named but absent.
func insertAfter(v *FlowVersion, parent string, node *FlowAction) error {
	if v == nil || v.Trigger == nil {
		return errNotFound
	}
	if parent == "" || parent == v.Trigger.Name {
		node.NextAction = v.Trigger.NextAction
		v.Trigger.NextAction = node
		return nil
	}
	p := findAction(v, parent)
	if p == nil {
		return errNotFound
	}
	node.NextAction = p.NextAction
	p.NextAction = node
	return nil
}

// detachAction removes the step named name from the chain, relinking around it, and
// returns the detached node (its NextAction cleared) plus whether it was found.
func detachAction(v *FlowVersion, name string) (*FlowAction, bool) {
	if v == nil || v.Trigger == nil {
		return nil, false
	}
	// Head of the chain (trigger's direct child).
	if head := v.Trigger.NextAction; head != nil && head.Name == name {
		v.Trigger.NextAction = head.NextAction
		head.NextAction = nil
		return head, true
	}
	for p := v.Trigger.NextAction; p != nil && p.NextAction != nil; p = p.NextAction {
		if p.NextAction.Name == name {
			node := p.NextAction
			p.NextAction = node.NextAction
			node.NextAction = nil
			return node, true
		}
	}
	return nil, false
}
