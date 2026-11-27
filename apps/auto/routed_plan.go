package auto

// routable refuses, when an automation is saved, a machine target beside ask: ask
// runs a plan in Hanzo's sandbox, and apps/coding refuses to route a plan to a
// machine, so every run would fail at start. It is not part of check, because
// definitionOf runs check on stored rows and drops those that fail: an automation
// saved before this refusal must still list, so its owner can fix it.
func (d definition) routable() error {
	if d.Target != "" && d.Permissions == PermissionAsk {
		return invalid(`an automation that runs on a machine needs permissions "auto": "ask" runs a plan in Hanzo's sandbox, which cannot be routed to a machine`)
	}
	return nil
}
