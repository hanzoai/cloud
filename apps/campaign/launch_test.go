package campaign

import (
	"context"
	"errors"
	"testing"
)

// resetChannels clears the executor registry between tests (the registry is a
// package global shared across the test binary).
func resetChannels() {
	regMu.Lock()
	defer regMu.Unlock()
	reg = map[string]Channel{}
}

// recordingChannel is a fake executor that records what the orchestrator handed
// it and returns scripted outcomes — the seam the fan-out is tested against,
// standing in for the real ads/publish/marketing executors.
type recordingChannel struct {
	kind       string
	gotOrg     string
	gotPlan    Plan
	launched   int
	paused     int
	ref        Ref
	launchErr  error
	spendCents int64
	spendErr   error
	pauseErr   error
}

func (r *recordingChannel) Kind() string { return r.kind }
func (r *recordingChannel) Launch(_ context.Context, org string, p Plan) (Ref, error) {
	r.gotOrg = org
	r.gotPlan = p
	r.launched++
	if r.launchErr != nil {
		return Ref{}, r.launchErr
	}
	return r.ref, nil
}
func (r *recordingChannel) Spend(_ context.Context, _ string, _ Ref) (int64, error) {
	return r.spendCents, r.spendErr
}
func (r *recordingChannel) Pause(_ context.Context, _ string, _ Ref) error {
	r.paused++
	return r.pauseErr
}

func draftWith(specs ...ChannelSpec) Campaign {
	return Campaign{
		ID: "cmp_test", Org: "acme", Name: "GTM Test", Status: StatusDraft,
		Content: []string{"creative-a"}, Channels: specs,
	}
}

// TestFanOut_LaunchesRegisteredChannel: a campaign with a paid channel fans out to
// the registered executor, which receives the org + a plan derived from the
// campaign, and its ref (external id + live) is recorded onto the channel.
func TestFanOut_LaunchesRegisteredChannel(t *testing.T) {
	resetChannels()
	rc := &recordingChannel{kind: KindPaid, ref: Ref{ExternalID: "ext_meta_1", Account: "act_123", Status: chanLive}}
	RegisterChannel(rc)

	camp := draftWith(ChannelSpec{Kind: KindPaid, Platform: "meta", Account: "act_123", Status: chanPending})
	out := fanOut(context.Background(), "acme", camp)

	if rc.launched != 1 {
		t.Fatalf("executor Launch calls want 1, got %d", rc.launched)
	}
	if rc.gotOrg != "acme" {
		t.Fatalf("executor must receive the campaign org, got %q", rc.gotOrg)
	}
	if rc.gotPlan.CampaignID != "cmp_test" || rc.gotPlan.Platform != "meta" || rc.gotPlan.Account != "act_123" {
		t.Fatalf("plan not derived from campaign: %+v", rc.gotPlan)
	}
	if out.Status != StatusLive {
		t.Fatalf("campaign status want live, got %q", out.Status)
	}
	if out.Channels[0].Status != chanLive || out.Channels[0].ExternalID != "ext_meta_1" {
		t.Fatalf("channel outcome not recorded: %+v", out.Channels[0])
	}
}

// TestFanOut_ConnectorDisabledRecordsFailed: when the executor fails closed
// (org has not connected the connector), the channel is recorded failed with an
// honest detail, NO external id is fabricated, and the campaign is failed.
func TestFanOut_ConnectorDisabledRecordsFailed(t *testing.T) {
	resetChannels()
	rc := &recordingChannel{kind: KindPaid, launchErr: errors.New("ads: ad account not connected for org")}
	RegisterChannel(rc)

	camp := draftWith(ChannelSpec{Kind: KindPaid, Platform: "meta", Status: chanPending})
	out := fanOut(context.Background(), "acme", camp)

	if out.Channels[0].Status != chanFailed {
		t.Fatalf("channel status want failed, got %q", out.Channels[0].Status)
	}
	if out.Channels[0].ExternalID != "" {
		t.Fatalf("no external id may be fabricated on a failed launch, got %q", out.Channels[0].ExternalID)
	}
	if out.Channels[0].Detail == "" {
		t.Fatalf("failed channel must carry an honest detail")
	}
	if out.Status != StatusFailed {
		t.Fatalf("campaign status want failed, got %q", out.Status)
	}
}

// TestFanOut_NoExecutorUnavailable: a channel kind with no registered executor is
// recorded unavailable (honest degrade), not launched, not fabricated.
func TestFanOut_NoExecutorUnavailable(t *testing.T) {
	resetChannels() // nothing registered

	camp := draftWith(ChannelSpec{Kind: KindOrganic, Platform: "x", Status: chanPending})
	out := fanOut(context.Background(), "acme", camp)

	if out.Channels[0].Status != chanUnavailable {
		t.Fatalf("channel status want unavailable, got %q", out.Channels[0].Status)
	}
	if out.Status != StatusFailed {
		t.Fatalf("campaign with only an unavailable channel is failed, got %q", out.Status)
	}
}

// TestFanOut_TenantIsolationOrgPassthrough: the org the orchestrator passes to the
// executor is EXACTLY the campaign's caller org — never another tenant's. This is
// the seam that keeps a campaign from resolving another org's connector token.
func TestFanOut_TenantIsolationOrgPassthrough(t *testing.T) {
	resetChannels()
	rc := &recordingChannel{kind: KindPaid, ref: Ref{ExternalID: "ext_1", Status: chanLive}}
	RegisterChannel(rc)

	camp := draftWith(ChannelSpec{Kind: KindPaid, Platform: "meta", Status: chanPending})
	_ = fanOut(context.Background(), "maxpower", camp)
	if rc.gotOrg != "maxpower" {
		t.Fatalf("executor received org %q, want the caller org maxpower", rc.gotOrg)
	}
}

// TestFanOut_IdempotentSkipsLiveChannel: a channel already live is not re-launched.
func TestFanOut_IdempotentSkipsLiveChannel(t *testing.T) {
	resetChannels()
	rc := &recordingChannel{kind: KindPaid, ref: Ref{ExternalID: "ext_1", Status: chanLive}}
	RegisterChannel(rc)

	camp := draftWith(ChannelSpec{Kind: KindPaid, Platform: "meta", ExternalID: "ext_prev", Status: chanLive})
	out := fanOut(context.Background(), "acme", camp)

	if rc.launched != 0 {
		t.Fatalf("a live channel must not be re-launched, got %d launches", rc.launched)
	}
	if out.Status != StatusLive || out.Channels[0].ExternalID != "ext_prev" {
		t.Fatalf("live channel must be preserved: %+v status=%s", out.Channels[0], out.Status)
	}
}

// TestPauseAll_PausesLiveChannels: pause fans out to live channels' executors and
// moves them (and the campaign) to paused.
func TestPauseAll_PausesLiveChannels(t *testing.T) {
	resetChannels()
	rc := &recordingChannel{kind: KindPaid}
	RegisterChannel(rc)

	camp := draftWith(ChannelSpec{Kind: KindPaid, Platform: "meta", ExternalID: "ext_1", Status: chanLive})
	out := pauseAll(context.Background(), "acme", camp)

	if rc.paused != 1 {
		t.Fatalf("executor Pause calls want 1, got %d", rc.paused)
	}
	if out.Channels[0].Status != chanPaused || out.Status != StatusPaused {
		t.Fatalf("pause not applied: channel=%s campaign=%s", out.Channels[0].Status, out.Status)
	}
}

// TestChannelSpend_FanInFailSoft: spend is summed across live channels; a channel
// whose connector spend read errors contributes 0 with an honest SpendError and
// never fails the whole read.
func TestChannelSpend_FanInFailSoft(t *testing.T) {
	resetChannels()
	ok := &recordingChannel{kind: KindPaid, spendCents: 4200}
	bad := &recordingChannel{kind: KindEmail, spendErr: errors.New("provider: insights unavailable")}
	RegisterChannel(ok)
	RegisterChannel(bad)

	camp := Campaign{
		ID: "cmp_test", Org: "acme", Status: StatusLive,
		Channels: []ChannelSpec{
			{Kind: KindPaid, Platform: "meta", ExternalID: "ext_1", Status: chanLive},
			{Kind: KindEmail, Platform: "sendgrid", ExternalID: "ext_2", Status: chanLive},
		},
	}
	total, metrics := channelSpend(context.Background(), "acme", camp)
	if total != 4200 {
		t.Fatalf("spend total want 4200 (bad channel contributes 0), got %d", total)
	}
	if len(metrics) != 2 {
		t.Fatalf("want 2 channel metrics, got %d", len(metrics))
	}
	var paid, email ChannelMetric
	for _, m := range metrics {
		switch m.Kind {
		case KindPaid:
			paid = m
		case KindEmail:
			email = m
		}
	}
	if paid.SpendCents != 4200 || paid.SpendError != "" {
		t.Fatalf("paid channel spend: %+v", paid)
	}
	if email.SpendCents != 0 || email.SpendError == "" {
		t.Fatalf("email channel must record honest spend error, got: %+v", email)
	}
}
