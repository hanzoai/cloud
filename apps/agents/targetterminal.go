package agents

import "time"

// A machine's terminal is a fact about the MACHINE, not about any session on it.
// `hanzo link` publishes a laptop's shell and the machine already beats, so the
// terminal rides the beat: a write stating `terminal` publishes it and stamps
// TerminalAt from the server's clock, `""` clears it, and omitting it leaves it
// alone. The session reaper ends a link's idle session after an hour; the
// machine's terminal does not depend on that session.

// LiveTerminal is the terminal URL a reader may show: the stored one, only while
// the machine is online AND stated it within LiveWindow — the one window this
// file already uses for "the machine is here". Otherwise "".
func (t Target) LiveTerminal(now time.Time) string {
	if t.Terminal == "" || t.TerminalAt == 0 || t.EffectiveStatus(now) != TargetOnline {
		return ""
	}
	if now.Sub(time.Unix(t.TerminalAt, 0)) > LiveWindow {
		return ""
	}
	return t.Terminal
}

// publish applies a request's optional terminal: nil leaves both columns alone,
// "" clears them, a URL is checked as a session's terminal is (sessionTerminal)
// and stamped at now.
func (t *Target) publish(in *string, now int64) error {
	if in == nil {
		return nil
	}
	u, err := sessionTerminal(*in)
	if err != nil {
		return err
	}
	t.Terminal, t.TerminalAt = u, 0
	if u != "" {
		t.TerminalAt = now
	}
	return nil
}
