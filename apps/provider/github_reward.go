package provider

// github_reward.go — GitHub's deliveries, told to the rewards program.
//
// A pull_request, issues, star, watch or fork delivery this webhook has verified is
// handed, as GitHub signed it, to the rewards capability (apps/reward), which decides
// whether it earns, matches the GitHub account to the Hanzo person who linked it, and
// states it to the rules engine with its evidence. The delivery's own work does not
// wait on it: it is told on its own goroutine, and a rewards capability that does not
// answer costs that claim and says so in the log, never GitHub a retry.

import (
	"context"
	"encoding/json"
	"time"

	"github.com/hanzoai/cloud/client"
	rewardpeer "github.com/hanzoai/cloud/client/reward"
	"github.com/hanzoai/cloud/sdk"
)

// rewardBudget bounds telling the rewards program one delivery.
const rewardBudget = 60 * time.Second

// rewarded are the deliveries that can earn.
var rewarded = map[string]bool{"pull_request": true, "issues": true, "star": true, "watch": true, "fork": true}

// rewardFn tells the program one delivery, a variable so a test can hear it.
var rewardFn = rewardpeer.RewardGitHub

// tellReward hands a verified delivery to the rewards program.
func tellReward(s *cloud.Service[state], event, delivery string, body []byte) {
	if !rewarded[event] {
		return
	}
	in := &client.GitHubDelivery{Event: event, Delivery: delivery, Body: json.RawMessage(append([]byte(nil), body...))}
	go func() {
		ctx, cancel := context.WithTimeout(cloud.For(context.Background(), cloud.Brand()), rewardBudget)
		defer cancel()
		out, err := rewardFn(ctx, in)
		if err != nil {
			s.Log.Warn("github: a delivery was not told to the rewards program", "event", event, "delivery", delivery, "err", err)
			return
		}
		if out.Events > 0 {
			s.Log.Info("github: delivery stated to the rewards program", "event", event, "delivery", delivery, "events", out.Events)
		}
	}()
}

// rewardEventFn states one event to the rewards program, a variable so a test can hear it.
var rewardEventFn = rewardpeer.RewardEvent

// tellJoined states that a person linked their Discord account to their Hanzo
// account in a server we run: discord.joined, once for each Discord account. The
// Discord user is the one Discord's own identify exchange proved and IAM's sign-in
// bound, never one a link named. Best-effort, on its own goroutine: a rewards
// program that does not answer costs that claim and says so in the log.
func tellJoined(s *cloud.Service[state], subject, org, discordUser string) {
	go func() {
		ctx, cancel := context.WithTimeout(cloud.For(context.Background(), cloud.Brand()), rewardBudget)
		defer cancel()
		if _, err := rewardEventFn(ctx, &client.RewardEventIn{
			ID: "discord.joined:" + discordUser, Type: "discord.joined", Subject: subject, Org: org, Fields: []client.RewardField{},
		}); err != nil {
			s.Log.Warn("discord: a link was not told to the rewards program", "err", err)
		}
	}()
}
