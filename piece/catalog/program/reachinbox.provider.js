"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
};
var __export = (target, all) => {
  for (var name in all)
    __defProp(target, name, { get: all[name], enumerable: !0 });
}, __copyProps = (to, from, except, desc) => {
  if (from && typeof from == "object" || typeof from == "function")
    for (let key of __getOwnPropNames(from))
      !__hasOwnProp.call(to, key) && key !== except && __defProp(to, key, { get: () => from[key], enumerable: !(desc = __getOwnPropDesc(from, key)) || desc.enumerable });
  return to;
};
var __toCommonJS = (mod) => __copyProps(__defProp({}, "__esModule", { value: !0 }), mod);

// ../ap/packages/pieces/community/reachinbox/src/index.ts
var index_exports = {};
__export(index_exports, {
  ReachinboxAuth: () => ReachinboxAuth,
  reachinbox: () => reachinbox
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework20 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/reachinbox/src/lib/actions/add-leads.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/reachinbox/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), reachinboxCommon = {
  baseUrl: "https://api.reachinbox.ai/api/v1/"
}, fetchCampaigns = async (auth) => {
  var _a, _b;
  try {
    return ((_b = (_a = (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: "https://api.reachinbox.ai/api/v1/campaigns/all?sort=newest&offset=0&limit=50",
      headers: {
        Authorization: `Bearer ${auth}`
      }
    })).body) == null ? void 0 : _a.data) == null ? void 0 : _b.rows) || [];
  } catch (error) {
    throw console.error("Error fetching campaigns:", error), new Error("Failed to fetch campaigns.");
  }
};

// ../ap/packages/pieces/community/reachinbox/src/lib/actions/add-leads.ts
var import_pieces_common2 = require("@activepieces/pieces-common");
var addLeads = (0, import_pieces_framework.createAction)({
  auth: ReachinboxAuth,
  name: "addLeads",
  displayName: "Add Leads",
  description: "Add leads to campaigns dynamically by selecting a campaign, entering lead details, and including custom variables.",
  audience: "both",
  aiMetadata: {
    description: "Adds a single lead (email plus name and optional custom variables) to a specific ReachInbox campaign. Use to enroll a new contact into an outreach sequence. Requires a campaign id; not idempotent since repeated calls append the lead again.",
    idempotent: !1
  },
  props: {
    campaignId: import_pieces_framework.Property.Dropdown({
      auth: ReachinboxAuth,
      displayName: "Select Campaign",
      description: "Choose a campaign from the list.",
      required: !0,
      refreshers: ["auth"],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let campaigns = await fetchCampaigns(auth.secret_text);
        return {
          options: campaigns.map((campaign) => ({
            label: campaign.name,
            value: campaign.id.toString()
          })),
          disabled: campaigns.length === 0
        };
      }
    }),
    email: import_pieces_framework.Property.ShortText({
      displayName: "Email Address",
      description: "Enter the email address for the lead",
      required: !0
    }),
    firstName: import_pieces_framework.Property.ShortText({
      displayName: "First Name",
      description: "Enter the first name for the lead",
      required: !0
    }),
    lastName: import_pieces_framework.Property.ShortText({
      displayName: "Last Name",
      description: "Enter the last name for the lead",
      required: !1
    }),
    customVariables: import_pieces_framework.Property.Array({
      displayName: "Custom Variables",
      description: "Add custom variables as key-value pairs for the lead.",
      properties: {
        key: import_pieces_framework.Property.ShortText({
          displayName: "Key",
          description: "Enter the key for the custom variable",
          required: !0
        }),
        value: import_pieces_framework.Property.ShortText({
          displayName: "Value",
          description: "Enter the value for the custom variable",
          required: !0
        })
      },
      required: !1,
      defaultValue: []
    })
  },
  async run(context) {
    let { campaignId, email, firstName, lastName } = context.propsValue, customVariables = context.propsValue.customVariables || [], customVariablesObject = {};
    customVariables.forEach((variable) => {
      customVariablesObject[variable.key] = variable.value;
    });
    let body = {
      campaignId,
      leads: [__spreadValues({ email, firstName, lastName }, customVariablesObject)],
      newCoreVariables: [
        "firstName",
        ...customVariables.map((varObj) => varObj.key)
      ],
      duplicates: []
    };
    try {
      let response = await import_pieces_common2.httpClient.sendRequest({
        method: import_pieces_common2.HttpMethod.POST,
        url: `${reachinboxCommon.baseUrl}leads/add`,
        headers: {
          Authorization: `Bearer ${context.auth.secret_text}`,
          "Content-Type": "application/json"
        },
        body
      });
      if (response.status === 200)
        return {
          success: !0,
          message: response.body.message || "Leads added successfully.",
          leadCount: response.body.leadCount
        };
      throw new Error(`Failed to add leads: ${response.body.message}`);
    } catch (error) {
      throw console.error("Error adding leads:", error), new Error("Failed to add leads to the campaign.");
    }
  }
});

// ../ap/packages/pieces/community/reachinbox/src/index.ts
var import_pieces_common14 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/reachinbox/src/lib/triggers/campaign-completed.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), campaignCompletedMessage = `
  Follow the below steps:
  
  1. Login to the ReachInbox dashboard.
  2. Go to the "Profile" section and navigate to the "Settings" tab.
  3. Click on the "Integrations" and go to the "Webhooks". Click on the "Add Webhook" button.
  4. Copy the following webhook URL and paste it into the "Webhook URL" field.
      \`\`\`text
      {{webhookUrl}}
      \`\`\`

  5. Select the event type as "Campaign Completed".
  6. Click on the "Test Trigger" button to simulate a test and capture the webhook response here.
  `, campaignCompleted = (0, import_pieces_framework2.createTrigger)({
  name: "campaignCompleted",
  displayName: "Campaign Completed",
  description: "Triggers when a campaign is completed.",
  aiMetadata: {
    description: "Fires when a ReachInbox campaign finishes its full sending sequence (SEQUENCE_COMPLETED). Represents the end of outreach for a campaign, carrying campaign and lead details. Requires the webhook to be configured in ReachInbox."
  },
  props: {
    markdown: import_pieces_framework2.Property.MarkDown({
      value: campaignCompletedMessage
    })
  },
  sampleData: {
    email_id: 1,
    lead_id: 1,
    lead_email: "recipient@example.com",
    email_account: "sender@example.com",
    step_number: 1,
    message_id: "<test-message-id>",
    timestamp: "2024-03-18T08:15:51.000Z",
    campaign_id: 1,
    campaign_name: "Test Name",
    event: "SEQUENCE_COMPLETED",
    user_webhook_id: "1",
    lead_first_name: "Lead First Name",
    lead_last_name: "Lead Last Name",
    email_sent_body: "Sent Email body",
    email_replied_body: "Sent Replied body"
  },
  type: import_pieces_framework2.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/reachinbox/src/lib/triggers/email-bounced.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), emailBouncedMessage = `

  Follow the below steps:
  
  1. Login to the ReachInbox dashboard.
  2. Go to the "Profile" section and navigate to the "Settings" tab.
  3. Click on the "Integrations" and go to the "Webhooks". Click on the "Add Webhook" button.
  4. Copy the following webhook URL and paste it into the "Webhook URL" field.
      \`\`\`text
      {{webhookUrl}}
      \`\`\`

  5. Select the event type as "Email Bounced".
  6. Click on the "Test Trigger" button to simulate a test and capture the webhook response here.
  `, emailBounced = (0, import_pieces_framework3.createTrigger)({
  name: "emailBounced",
  displayName: "Email Bounced",
  description: "Triggers when an email is bounced.",
  aiMetadata: {
    description: "Fires when an outreach email bounces (EMAIL_BOUNCED) and was not delivered to the lead. Use to detect undeliverable addresses; payload includes the lead, sending account, and campaign. Requires the webhook to be configured in ReachInbox."
  },
  props: {
    markdown: import_pieces_framework3.Property.MarkDown({
      value: emailBouncedMessage
    })
  },
  sampleData: {
    email_id: 1,
    lead_id: 1,
    lead_email: "recipient@example.com",
    email_account: "sender@example.com",
    step_number: 1,
    message_id: "<test-message-id>",
    timestamp: "2024-03-18T08:15:51.000Z",
    campaign_id: 1,
    campaign_name: "Test Name",
    event: "EMAIL_BOUNCED",
    user_webhook_id: "1",
    lead_first_name: "Lead First Name",
    lead_last_name: "Lead Last Name",
    email_sent_body: "Sent Email body",
    email_replied_body: "Sent Replied body"
  },
  type: import_pieces_framework3.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/reachinbox/src/lib/triggers/email-opened.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), emailOpenedMessage = `
  Follow the below steps:
  
  1. Login to the ReachInbox dashboard.
  2. Go to the "Profile" section and navigate to the "Settings" tab.
  3. Click on the "Integrations" and go to the "Webhooks". Click on the "Add Webhook" button.
  4. Copy the following webhook URL and paste it into the "Webhook URL" field.
      \`\`\`text
      {{webhookUrl}}
      \`\`\`

  5. Select the event type as "Email Opened".
  6. Click on the "Test Trigger" button to simulate a test and capture the webhook response here.
  `, emailOpened = (0, import_pieces_framework4.createTrigger)({
  name: "emailOpened",
  displayName: "Email Opened",
  description: "Triggers when an email is opened.",
  aiMetadata: {
    description: "Fires when a lead opens an outreach email (EMAIL_OPENED). Represents recipient engagement; payload includes the lead, sending account, step, and campaign. Requires the webhook to be configured in ReachInbox."
  },
  props: {
    markdown: import_pieces_framework4.Property.MarkDown({
      value: emailOpenedMessage
    })
  },
  sampleData: {
    email_id: 1,
    lead_id: 1,
    lead_email: "recipient@example.com",
    email_account: "sender@example.com",
    step_number: 1,
    message_id: "<test-message-id>",
    timestamp: "2024-03-18T08:15:51.000Z",
    campaign_id: 1,
    campaign_name: "Test Name",
    event: "EMAIL_OPENED",
    user_webhook_id: "1",
    lead_first_name: "Lead First Name",
    lead_last_name: "Lead Last Name",
    email_sent_body: "Sent Email body",
    email_replied_body: "Sent Replied body"
  },
  type: import_pieces_framework4.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/reachinbox/src/lib/triggers/email-sent.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), message = `

Follow the below steps:

1. Login to the ReachInbox dashboard.
2. Go to the "Profile" section and navigate to the "Settings" tab.
3. Click on the "Integrations" and go to the "Webhooks". Click on the "Add Webhook" button.
4. Copy the following webhook URL and paste it into the "Webhook URL" field.
      \`\`\`text
      {{webhookUrl}}
      \`\`\`

5. Select the event type as "Email Sent".
6. Click on the "Test Trigger" button to simulate a test and capture the webhook response here.
`, emailSent = (0, import_pieces_framework5.createTrigger)({
  name: "emailSent",
  displayName: "Email Sent",
  description: "Triggers when an email is successfully sent.",
  aiMetadata: {
    description: "Fires when an outreach email is successfully sent to a lead (EMAIL_SENT). Represents a delivered sequence step; payload includes the lead, sending account, step, and campaign. Requires the webhook to be configured in ReachInbox."
  },
  props: {
    markdown: import_pieces_framework5.Property.MarkDown({
      value: message
    })
  },
  sampleData: {
    email_id: 1,
    lead_id: 1,
    lead_email: "recipient@example.com",
    email_account: "sender@example.com",
    step_number: 1,
    message_id: "<test-message-id>",
    timestamp: "2024-03-18T08:15:51.000Z",
    campaign_id: 1,
    campaign_name: "Test Name",
    event: "EMAIL_SENT",
    user_webhook_id: "1",
    lead_first_name: "Lead First Name",
    lead_last_name: "Lead Last Name",
    email_sent_body: "Sent Email body",
    email_replied_body: "Sent Replied body"
  },
  type: import_pieces_framework5.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/reachinbox/src/lib/triggers/lead-interested.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), leadInterestedMessage = `

  
  Follow the below steps:

  
  1. Login to the ReachInbox dashboard.
  2. Go to the "Profile" section and navigate to the "Settings" tab.
  3. Click on the "Integrations" and go to the "Webhooks". Click on the "Add Webhook" button.
  4. Copy the following webhook URL and paste it into the "Webhook URL" field.
      \`\`\`text
      {{webhookUrl}}
      \`\`\`
      

  5. Select the event type as "Lead Interested".
  6. Click on the "Test Trigger" button to simulate a test and capture the webhook response here.
  `, leadInterested = (0, import_pieces_framework6.createTrigger)({
  name: "leadInterested",
  displayName: "Lead Interested",
  description: "Triggers when a lead is set to interested.",
  aiMetadata: {
    description: "Fires when a lead is marked as interested in ReachInbox (LEAD_INTERESTED), signaling a positive reply or qualification. Use to route hot leads onward; payload includes the lead and campaign. Requires the webhook to be configured in ReachInbox."
  },
  props: {
    markdown: import_pieces_framework6.Property.MarkDown({
      value: leadInterestedMessage
    })
  },
  sampleData: {
    email_id: 1,
    lead_id: 1,
    lead_email: "recipient@example.com",
    email_account: "sender@example.com",
    step_number: 1,
    message_id: "<test-message-id>",
    timestamp: "2024-03-18T08:15:51.000Z",
    campaign_id: 1,
    campaign_name: "Test Name",
    event: "LEAD_INTERESTED",
    user_webhook_id: "1",
    lead_first_name: "Lead First Name",
    lead_last_name: "Lead Last Name",
    email_sent_body: "Sent Email body",
    email_replied_body: "Sent Replied body"
  },
  type: import_pieces_framework6.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/reachinbox/src/lib/triggers/lead-not-interested.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), leadNotInterestedMessage = `

  Follow the below steps:
  
  1. Login to the ReachInbox dashboard.
  2. Go to the "Profile" section and navigate to the "Settings" tab.
  3. Click on the "Integrations" and go to the "Webhooks". Click on the "Add Webhook" button.
  4. Copy the following webhook URL and paste it into the "Webhook URL" field.
      \`\`\`text
      {{webhookUrl}}
      \`\`\`
      

  5. Select the event type as "Lead Not Interested".
  6. Click on the "Test Trigger" button to simulate a test and capture the webhook response here.
  `, leadNotInterested = (0, import_pieces_framework7.createTrigger)({
  name: "leadNotInterested",
  displayName: "Lead Not Interested",
  description: "Triggers when a lead is set to not interested.",
  aiMetadata: {
    description: "Fires when a lead is marked as not interested in ReachInbox (LEAD_NOT_INTERESTED), signaling a negative or opt-out response. Use to suppress or follow up differently; payload includes the lead and campaign. Requires the webhook to be configured in ReachInbox."
  },
  props: {
    markdown: import_pieces_framework7.Property.MarkDown({
      value: leadNotInterestedMessage
    })
  },
  sampleData: {
    email_id: 1,
    lead_id: 1,
    lead_email: "recipient@example.com",
    email_account: "sender@example.com",
    step_number: 1,
    message_id: "<test-message-id>",
    timestamp: "2024-03-18T08:15:51.000Z",
    campaign_id: 1,
    campaign_name: "Test Name",
    event: "LEAD_NOT_INTERESTED",
    user_webhook_id: "1",
    lead_first_name: "Lead First Name",
    lead_last_name: "Lead Last Name",
    email_sent_body: "Sent Email body",
    email_replied_body: "Sent Replied body"
  },
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/reachinbox/src/lib/triggers/reply-received.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), replyReceivedMessage = `

  Follow the below steps:
  
  1. Login to the ReachInbox dashboard.
  2. Go to the "Profile" section and navigate to the "Settings" tab.
  3. Click on the "Integrations" and go to the "Webhooks". Click on the "Add Webhook" button.
  4. Copy the following webhook URL and paste it into the "Webhook URL" field.
      \`\`\`text
      {{webhookUrl}}
      \`\`\`

  5. Select the event type as "Reply Received".
  6. Click on the "Test Trigger" button to simulate a test and capture the webhook response here.
  `, replyReceived = (0, import_pieces_framework8.createTrigger)({
  name: "replyReceived",
  displayName: "Reply Received",
  description: "Triggers when a reply to an email is received.",
  aiMetadata: {
    description: "Fires when a lead replies to an outreach email (REPLY_RECEIVED). Represents an inbound response; payload includes the lead, sending account, campaign, and the replied message body. Requires the webhook to be configured in ReachInbox."
  },
  props: {
    markdown: import_pieces_framework8.Property.MarkDown({
      value: replyReceivedMessage
    })
  },
  sampleData: {
    email_id: 1,
    lead_id: 1,
    lead_email: "recipient@example.com",
    email_account: "sender@example.com",
    step_number: 1,
    message_id: "<test-message-id>",
    timestamp: "2024-03-18T08:15:51.000Z",
    campaign_id: 1,
    campaign_name: "Test Name",
    event: "REPLY_RECEIVED",
    user_webhook_id: "1",
    lead_first_name: "Lead First Name",
    lead_last_name: "Lead Last Name",
    email_sent_body: "Sent Email body",
    email_replied_body: "Sent Replied body"
  },
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/reachinbox/src/lib/actions/add-blocklist.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var addBlocklist = (0, import_pieces_framework9.createAction)({
  auth: ReachinboxAuth,
  name: "addBlocklist",
  displayName: "Add Blocklist",
  description: "Add email addresses, domains, and keywords to the blocklist.",
  audience: "both",
  aiMetadata: {
    description: "Adds email addresses, domains, and/or keywords to the ReachInbox blocklist so matching leads are excluded from outreach. Use to suppress contacts globally. At least one of emails, domains, or keywords must be provided; the call appends entries on each run.",
    idempotent: !1
  },
  props: {
    emails: import_pieces_framework9.Property.Array({
      displayName: "Email Addresses",
      description: 'Enter the email addresses to block (e.g., ["abc@gmail.com"])',
      required: !1
    }),
    domains: import_pieces_framework9.Property.Array({
      displayName: "Domains",
      description: 'Enter the domains to block (e.g., ["example.com"])',
      required: !1
    }),
    keywords: import_pieces_framework9.Property.Array({
      displayName: "Keywords",
      description: 'Enter keywords to block (e.g., ["spam", "blacklist"])',
      required: !1
    })
  },
  async run(context) {
    let { emails, domains, keywords } = context.propsValue;
    if (!(emails != null && emails.length) && !(domains != null && domains.length) && !(keywords != null && keywords.length))
      throw new Error(
        "Please provide at least one email, domain, or keyword to block."
      );
    let body = {
      emails: emails || [],
      domains: domains || [],
      keywords: keywords || []
    };
    try {
      let response = await import_pieces_common3.httpClient.sendRequest({
        method: import_pieces_common3.HttpMethod.POST,
        url: `${reachinboxCommon.baseUrl}blocklist/add`,
        headers: {
          Authorization: `Bearer ${context.auth.secret_text}`,
          "Content-Type": "application/json"
        },
        body
      });
      if (response.status === 200)
        return {
          success: !0,
          message: response.body.message || "Blocklist updated successfully."
        };
      throw new Error(`Failed to update blocklist: ${response.body.message}`);
    } catch (error) {
      throw error instanceof Error ? new Error(`Error updating blocklist: ${error.message}`) : new Error("Unknown error occurred while updating the blocklist.");
    }
  }
});

// ../ap/packages/pieces/community/reachinbox/src/lib/actions/add-email.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common"), addEmail = (0, import_pieces_framework10.createAction)({
  auth: ReachinboxAuth,
  name: "addEmail",
  displayName: "Add Email",
  description: "Add an email to a specific account.",
  audience: "both",
  aiMetadata: {
    description: "Attaches a sending email address to a specific ReachInbox campaign so that account is used for outreach. Use to assign which connected mailbox a campaign sends from. Requires both a campaign id and the email address.",
    idempotent: !1
  },
  props: {
    campaignId: import_pieces_framework10.Property.Dropdown({
      auth: ReachinboxAuth,
      displayName: "Select Campaign",
      description: "Choose a campaign from the list or enter the campaign ID manually.",
      required: !0,
      refreshers: ["auth"],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let campaigns = await fetchCampaigns(auth.secret_text);
        return {
          options: campaigns.map((campaign) => ({
            label: campaign.name,
            value: campaign.id
          })),
          disabled: campaigns.length === 0
        };
      }
    }),
    email: import_pieces_framework10.Property.ShortText({
      displayName: "Email",
      description: "Enter the email address to add to the campaign.",
      required: !0
    })
  },
  async run(context) {
    let { campaignId, email } = context.propsValue;
    if (!campaignId || !email)
      throw new Error("Campaign ID and Email are required.");
    let url = `${reachinboxCommon.baseUrl}campaigns/add-email`;
    try {
      let response = await import_pieces_common4.httpClient.sendRequest({
        method: import_pieces_common4.HttpMethod.POST,
        url,
        headers: {
          Authorization: `Bearer ${context.auth.secret_text}`,
          "Content-Type": "application/json"
        },
        body: {
          campaignId,
          email
        }
      });
      if (response.status === 200)
        return {
          success: !0,
          message: response.body.message || "Email added successfully."
        };
      if (response.status === 404)
        return {
          success: !1,
          message: response.body.message || "Campaign not found or no emails were added to the campaign."
        };
      throw new Error(`Failed to add email: ${response.body.message}`);
    } catch (error) {
      throw new Error(`Failed to add email: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/reachinbox/src/lib/actions/enable-warmup.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var enableWarmup = (0, import_pieces_framework11.createAction)({
  auth: ReachinboxAuth,
  name: "enableWarmup",
  displayName: "Enable Warmup",
  description: "Enable warmup for specific email accounts where it is currently disabled.",
  audience: "both",
  aiMetadata: {
    description: "Turns on inbox warmup for a connected ReachInbox email account, identified by its account id. Use to start gradually building sending reputation for a mailbox. Idempotent: re-running for an account already warming up leaves it enabled.",
    idempotent: !0
  },
  props: {
    accountId: import_pieces_framework11.Property.Dropdown({
      auth: ReachinboxAuth,
      displayName: "Select Email Accounts to Enable Warmup",
      description: "Choose email accounts that have warmup disabled.",
      required: !0,
      refreshers: ["auth"],
      options: async ({ auth }) => {
        var _a, _b;
        try {
          let options = (((_b = (_a = (await import_pieces_common5.httpClient.sendRequest({
            method: import_pieces_common5.HttpMethod.GET,
            url: `${reachinboxCommon.baseUrl}account/all`,
            headers: {
              Authorization: `Bearer ${auth}`
            }
          })).body) == null ? void 0 : _a.data) == null ? void 0 : _b.emailsConnected) || []).filter(
            (account) => !account.warmupEnabled && !account.isDisconnected
          ).map(
            (account) => ({
              label: `${account.email} (ID: ${account.id})`,
              value: account.id.toString()
            })
          );
          return {
            options,
            disabled: options.length === 0
          };
        } catch (error) {
          return console.error("Error fetching email accounts:", error), { options: [], disabled: !0 };
        }
      }
    })
  },
  async run(context) {
    let { accountId } = context.propsValue, body = {
      ids: [accountId]
    };
    try {
      let response = await import_pieces_common5.httpClient.sendRequest({
        method: import_pieces_common5.HttpMethod.POST,
        url: `${reachinboxCommon.baseUrl}account/warmup/enable`,
        headers: {
          Authorization: `Bearer ${context.auth.secret_text}`,
          "Content-Type": "application/json"
        },
        body
      });
      return {
        success: !0,
        message: `Warmup enabled for account ID: ${accountId}`
      };
    } catch (error) {
      throw console.error("Error enabling warmup:", error), new Error(`Failed to enable warmup for account ID: ${accountId}`);
    }
  }
});

// ../ap/packages/pieces/community/reachinbox/src/lib/actions/get-campaign-analytics.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common"), getCampaignAnalytics = (0, import_pieces_framework12.createAction)({
  auth: ReachinboxAuth,
  name: "getCampaignAnalytics",
  displayName: "Get Campaign Analytics",
  description: "Fetch analytics data for a selected campaign based on a date range.",
  audience: "both",
  aiMetadata: {
    description: "Retrieves performance analytics (sent, opens, clicks, replies, per-step breakdowns, and activity) for one ReachInbox campaign over a date range. Use to report on or evaluate a specific campaign. Requires a campaign id and start/end dates in YYYY-MM-DD format; read-only and idempotent.",
    idempotent: !0
  },
  props: {
    campaignId: import_pieces_framework12.Property.Dropdown({
      auth: ReachinboxAuth,
      displayName: "Select Campaign",
      description: "Choose a campaign from the list or enter the campaign ID manually.",
      required: !0,
      refreshers: ["auth"],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let campaigns = await fetchCampaigns(auth.secret_text);
        return {
          options: campaigns.map((campaign) => ({
            label: campaign.name,
            value: campaign.id.toString()
          })),
          disabled: campaigns.length === 0
        };
      }
    }),
    startDate: import_pieces_framework12.Property.ShortText({
      displayName: "Start Date",
      description: "Enter the start date (YYYY-MM-DD). E.g. 2023-10-11",
      required: !0
      // Made mandatory again
    }),
    endDate: import_pieces_framework12.Property.ShortText({
      displayName: "End Date",
      description: "Enter the end date (YYYY-MM-DD). E.g. 2023-10-20",
      required: !0
      // Made mandatory again
    })
  },
  async run(context) {
    var _a;
    let { campaignId, startDate, endDate } = context.propsValue, url = `${reachinboxCommon.baseUrl}campaign/analytics?campaignId=${campaignId}&startDate=${startDate}&endDate=${endDate}`;
    try {
      let response = await import_pieces_common6.httpClient.sendRequest({
        method: import_pieces_common6.HttpMethod.GET,
        url,
        headers: {
          Authorization: `Bearer ${context.auth.secret_text}`
        }
      });
      if (response.body.status === 200)
        return {
          success: !0,
          message: response.body.message,
          data: response.body.data
        };
      throw new Error(`Failed to fetch analytics: ${response.body.message}`);
    } catch (error) {
      if (((_a = error.response) == null ? void 0 : _a.status) === 404)
        return {
          success: !1,
          message: "Campaign not found or analytics data unavailable for the provided date range."
        };
      throw new Error(`Failed to fetch analytics: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/reachinbox/src/lib/actions/get-summary.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common"), getSummary = (0, import_pieces_framework13.createAction)({
  auth: ReachinboxAuth,
  name: "getSummary",
  displayName: "Get Summary",
  description: "Get a summary of campaign analytics within a date range.",
  audience: "both",
  aiMetadata: {
    description: "Retrieves account-wide outreach analytics (totals and rates for sent, opens, clicks, and replies) aggregated across all campaigns for a date range. Use for an overall performance summary rather than a single campaign. Requires start/end dates in YYYY-MM-DD format; read-only and idempotent.",
    idempotent: !0
  },
  props: {
    startDate: import_pieces_framework13.Property.ShortText({
      displayName: "Start Date",
      description: "Enter the start date (YYYY-MM-DD). E.g. 2023-10-11",
      required: !0
    }),
    endDate: import_pieces_framework13.Property.ShortText({
      displayName: "End Date",
      description: "Enter the end date (YYYY-MM-DD). E.g. 2023-12-20",
      required: !0
    })
  },
  async run(context) {
    var _a;
    let { startDate, endDate } = context.propsValue, url = `https://api.reachinbox.ai/api/v1/analytics/summary?startDate=${startDate}&endDate=${endDate}`;
    try {
      let response = await import_pieces_common7.httpClient.sendRequest({
        method: import_pieces_common7.HttpMethod.GET,
        url,
        headers: {
          Authorization: `Bearer ${context.auth.secret_text}`
        }
      });
      if (response.body.status === 200)
        return {
          success: !0,
          message: response.body.message,
          data: response.body.data
        };
      throw new Error(`Failed to fetch summary: ${response.body.message}`);
    } catch (error) {
      if (((_a = error.response) == null ? void 0 : _a.status) === 404)
        return {
          success: !1,
          message: "Summary data not found for the provided date range."
        };
      throw new Error(`Failed to fetch summary: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/reachinbox/src/lib/actions/pause-campaign.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var import_pieces_common8 = require("@activepieces/pieces-common");
var pauseCampaign = (0, import_pieces_framework14.createAction)({
  auth: ReachinboxAuth,
  name: "pauseCampaign",
  displayName: "Pause Campaign",
  description: "Pause a selected campaign.",
  audience: "both",
  aiMetadata: {
    description: "Pauses a running ReachInbox campaign, identified by its campaign id, halting further sends. Use to temporarily stop outreach for a campaign. Idempotent: pausing an already-paused campaign leaves it paused.",
    idempotent: !0
  },
  props: {
    campaignId: import_pieces_framework14.Property.Dropdown({
      auth: ReachinboxAuth,
      displayName: "Select Campaign",
      description: "Choose a campaign to pause.",
      required: !0,
      refreshers: ["auth"],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let campaigns = await fetchCampaigns(auth.secret_text);
        return {
          options: campaigns.map((campaign) => ({
            label: campaign.name,
            value: campaign.id.toString()
          })),
          disabled: campaigns.length === 0
        };
      }
    })
  },
  async run(context) {
    var _a;
    let { campaignId } = context.propsValue, url = `${reachinboxCommon.baseUrl}campaigns/pause`;
    try {
      let response = await import_pieces_common8.httpClient.sendRequest({
        method: import_pieces_common8.HttpMethod.POST,
        url,
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${context.auth.secret_text}`
        },
        body: {
          campaignId
          // Pass the selected campaign ID in the request body
        }
      });
      if (response.body.status === 200)
        return {
          success: !0,
          message: response.body.message
        };
      throw new Error(`Failed to pause campaign: ${response.body.message}`);
    } catch (error) {
      if (((_a = error.response) == null ? void 0 : _a.status) === 404)
        return {
          success: !1,
          message: "Campaign not found or unable to pause."
        };
      throw new Error(`Failed to pause campaign: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/reachinbox/src/lib/actions/pause-warmup.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var pauseWarmup = (0, import_pieces_framework15.createAction)({
  auth: ReachinboxAuth,
  name: "pauseWarmup",
  displayName: "Pause Warmup",
  description: "Pause warmup for selected email accounts.",
  audience: "both",
  aiMetadata: {
    description: "Turns off inbox warmup for a connected ReachInbox email account, identified by its account id. Use to stop reputation-building activity for a mailbox. Idempotent: re-running for an account already paused leaves it disabled.",
    idempotent: !0
  },
  props: {
    accountId: import_pieces_framework15.Property.Dropdown({
      auth: ReachinboxAuth,
      displayName: "Select Email Accounts to Pause Warmup",
      description: "Choose email accounts that are currently warming up.",
      required: !0,
      refreshers: ["auth"],
      options: async ({ auth }) => {
        var _a, _b;
        try {
          let options = (((_b = (_a = (await import_pieces_common9.httpClient.sendRequest({
            method: import_pieces_common9.HttpMethod.GET,
            url: `${reachinboxCommon.baseUrl}account/all`,
            headers: {
              Authorization: `Bearer ${auth}`
            }
          })).body) == null ? void 0 : _a.data) == null ? void 0 : _b.emailsConnected) || []).filter(
            (account) => account.warmupEnabled
          ).map((account) => ({
            label: `${account.email} (ID: ${account.id})`,
            value: account.id.toString()
          }));
          return {
            options,
            disabled: options.length === 0
          };
        } catch (error) {
          return console.error("Error fetching email accounts:", error), { options: [], disabled: !0 };
        }
      }
    })
  },
  async run(context) {
    let { accountId } = context.propsValue, body = {
      ids: [accountId]
    };
    try {
      let response = await import_pieces_common9.httpClient.sendRequest({
        method: import_pieces_common9.HttpMethod.POST,
        url: `${reachinboxCommon.baseUrl}account/warmup/pause`,
        headers: {
          Authorization: `Bearer ${context.auth.secret_text}`,
          "Content-Type": "application/json"
        },
        body
      });
      return {
        success: !0,
        message: `Warmup paused for account ID: ${accountId}`
      };
    } catch (error) {
      throw console.error("Error pausing warmup:", error), new Error(`Failed to pause warmup for account ID: ${accountId}`);
    }
  }
});

// ../ap/packages/pieces/community/reachinbox/src/lib/actions/remove-email.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var removeEmail = (0, import_pieces_framework16.createAction)({
  auth: ReachinboxAuth,
  name: "removeEmail",
  displayName: "Remove Email",
  description: "Remove an email account from the system.",
  audience: "both",
  aiMetadata: {
    description: "Deletes a connected email account from ReachInbox, identified by its account id. Use to permanently disconnect a mailbox. Idempotent on the id: once removed, repeating the call leaves the account absent.",
    idempotent: !0
  },
  props: {
    accountId: import_pieces_framework16.Property.Dropdown({
      auth: ReachinboxAuth,
      displayName: "Select Email Account to Remove",
      description: "Choose an email account to remove.",
      required: !0,
      refreshers: ["auth"],
      options: async ({ auth }) => {
        var _a, _b;
        try {
          let options = (((_b = (_a = (await import_pieces_common10.httpClient.sendRequest({
            method: import_pieces_common10.HttpMethod.GET,
            url: `${reachinboxCommon.baseUrl}account/all`,
            headers: {
              Authorization: `Bearer ${auth}`
            }
          })).body) == null ? void 0 : _a.data) == null ? void 0 : _b.emailsConnected) || []).map((account) => ({
            label: `${account.email} (ID: ${account.id})`,
            value: account.id.toString()
          }));
          return {
            options,
            disabled: options.length === 0
          };
        } catch (error) {
          return console.error("Error fetching email accounts:", error), { options: [], disabled: !0 };
        }
      }
    })
  },
  async run(context) {
    let { accountId } = context.propsValue;
    try {
      if ((await import_pieces_common10.httpClient.sendRequest({
        method: import_pieces_common10.HttpMethod.DELETE,
        url: `${reachinboxCommon.baseUrl}account/delete/${accountId}`,
        headers: {
          Authorization: `Bearer ${context.auth.secret_text}`
        }
      })).status === 200)
        return {
          success: !0,
          message: `Email account with ID ${accountId} was successfully removed.`
        };
      throw new Error(`Failed to remove email account with ID: ${accountId}`);
    } catch (error) {
      throw console.error("Error removing email account:", error), new Error(`Error removing email account with ID: ${accountId}`);
    }
  }
});

// ../ap/packages/pieces/community/reachinbox/src/lib/actions/set-schedule.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework");
var import_pieces_common11 = require("@activepieces/pieces-common"), setSchedule = (0, import_pieces_framework17.createAction)({
  auth: ReachinboxAuth,
  name: "setSchedule",
  displayName: "Set Schedule",
  description: "Update the schedule for a specific Campaign",
  audience: "both",
  aiMetadata: {
    description: "Sets the sending schedule for a ReachInbox campaign: active date range, daily start/end times, timezone, and which weekdays are enabled. Use to control when a campaign sends. Requires a campaign id; idempotent since it replaces the schedule with the supplied values.",
    idempotent: !0
  },
  props: {
    campaignId: import_pieces_framework17.Property.Dropdown({
      auth: ReachinboxAuth,
      displayName: "Select Campaign",
      description: "Choose a campaign from the list or enter the campaign ID manually.",
      required: !0,
      refreshers: ["auth"],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let campaigns = await fetchCampaigns(auth.secret_text);
        return {
          options: campaigns.map((campaign) => ({
            label: campaign.name,
            value: campaign.id.toString()
          })),
          disabled: campaigns.length === 0
        };
      }
    }),
    scheduleName: import_pieces_framework17.Property.ShortText({
      displayName: "Schedule Name",
      description: "Enter the schedule name here (e.g., New schedule).",
      required: !0
    }),
    startDate: import_pieces_framework17.Property.DateTime({
      displayName: "Start Date",
      description: "Enter the start date (e.g., 2023-08-01T00:00:00.000Z).",
      required: !0
    }),
    endDate: import_pieces_framework17.Property.DateTime({
      displayName: "End Date",
      description: "Enter the end date (e.g., 2024-08-25T00:00:00.000Z).",
      required: !0
    }),
    startTime: import_pieces_framework17.Property.ShortText({
      displayName: "Start Time",
      description: "Enter the start time (e.g., 09:00).",
      required: !0
    }),
    endTime: import_pieces_framework17.Property.ShortText({
      displayName: "End Time",
      description: "Enter the end time (e.g., 12:00).",
      required: !0
    }),
    timezone: import_pieces_framework17.Property.ShortText({
      displayName: "Timezone",
      description: "Enter the timezone (e.g., America/Detroit).",
      required: !0
    }),
    sunday: import_pieces_framework17.Property.Checkbox({
      displayName: "Sunday",
      description: 'Choose "Yes" if you want to set a schedule for Sunday.',
      required: !0,
      defaultValue: !1
    }),
    monday: import_pieces_framework17.Property.Checkbox({
      displayName: "Monday",
      description: 'Choose "Yes" if you want to set a schedule for Monday.',
      required: !0,
      defaultValue: !1
    }),
    tuesday: import_pieces_framework17.Property.Checkbox({
      displayName: "Tuesday",
      description: 'Choose "Yes" if you want to set a schedule for Tuesday.',
      required: !0,
      defaultValue: !1
    }),
    wednesday: import_pieces_framework17.Property.Checkbox({
      displayName: "Wednesday",
      description: 'Choose "Yes" if you want to set a schedule for Wednesday.',
      required: !0,
      defaultValue: !1
    }),
    thursday: import_pieces_framework17.Property.Checkbox({
      displayName: "Thursday",
      description: 'Choose "Yes" if you want to set a schedule for Thursday.',
      required: !0,
      defaultValue: !1
    }),
    friday: import_pieces_framework17.Property.Checkbox({
      displayName: "Friday",
      description: 'Choose "Yes" if you want to set a schedule for Friday.',
      required: !0,
      defaultValue: !1
    }),
    saturday: import_pieces_framework17.Property.Checkbox({
      displayName: "Saturday",
      description: 'Choose "Yes" if you want to set a schedule for Saturday.',
      required: !0,
      defaultValue: !1
    })
  },
  async run(context) {
    let {
      campaignId,
      scheduleName,
      startDate,
      endDate,
      startTime,
      endTime,
      timezone,
      sunday,
      monday,
      tuesday,
      wednesday,
      thursday,
      friday,
      saturday
    } = context.propsValue, schedules = [
      {
        name: scheduleName,
        timing: {
          from: startTime,
          to: endTime
        },
        days: {
          0: sunday,
          1: monday,
          2: tuesday,
          3: wednesday,
          4: thursday,
          5: friday,
          6: saturday
        },
        timezone
      }
    ], url = `${reachinboxCommon.baseUrl}campaigns/set-schedule`;
    try {
      let response = await import_pieces_common11.httpClient.sendRequest({
        method: import_pieces_common11.HttpMethod.PUT,
        url,
        headers: {
          Authorization: `Bearer ${context.auth.secret_text}`,
          "Content-Type": "application/json"
        },
        body: {
          campaignId,
          startDate,
          endDate,
          schedules
        }
      });
      if (response.status === 200)
        return {
          success: !0,
          message: response.body.message || "Schedule updated successfully."
        };
      throw new Error(`Failed to update schedule: ${response.body.message}`);
    } catch (error) {
      throw new Error(`Failed to update schedule: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/reachinbox/src/lib/actions/start-campaign.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework");
var import_pieces_common12 = require("@activepieces/pieces-common"), startCampaign = (0, import_pieces_framework18.createAction)({
  auth: ReachinboxAuth,
  name: "startCampaign",
  displayName: "Start Campaign",
  description: "Starts a Campaign",
  audience: "both",
  aiMetadata: {
    description: "Starts (launches or resumes) a ReachInbox campaign, identified by its campaign id, so it begins sending. Use to activate outreach for a campaign. Idempotent: starting an already-running campaign leaves it running.",
    idempotent: !0
  },
  props: {
    campaignId: import_pieces_framework18.Property.Dropdown({
      auth: ReachinboxAuth,
      displayName: "Select Campaign",
      description: "Choose a campaign from the list or enter the campaign ID manually.",
      required: !0,
      refreshers: ["auth"],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let campaigns = await fetchCampaigns(auth.secret_text);
        return {
          options: campaigns.map((campaign) => ({
            label: campaign.name,
            value: campaign.id
          })),
          disabled: campaigns.length === 0
        };
      }
    })
  },
  async run(context) {
    let { campaignId } = context.propsValue;
    if (!campaignId)
      throw new Error("Campaign ID is required.");
    let url = `${reachinboxCommon.baseUrl}campaigns/start`;
    try {
      let response = await import_pieces_common12.httpClient.sendRequest({
        method: import_pieces_common12.HttpMethod.POST,
        url,
        headers: {
          Authorization: `Bearer ${context.auth.secret_text}`,
          "Content-Type": "application/json"
        },
        body: {
          campaignId
        }
      });
      if (response.status === 200)
        return {
          success: !0,
          message: response.body.message || "Campaign started successfully."
        };
      throw new Error(`Failed to start campaign: ${response.body.message}`);
    } catch (error) {
      throw new Error(`Failed to start campaign: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/reachinbox/src/lib/actions/update-lead.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework");
var import_pieces_common13 = require("@activepieces/pieces-common");
var updateLead = (0, import_pieces_framework19.createAction)({
  auth: ReachinboxAuth,
  name: "updateLead",
  displayName: "Update Lead",
  description: "Updates a Lead.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing lead in a ReachInbox campaign, overwriting its email, name, and custom-variable attributes. Use to modify a known contact's details. Requires both the campaign id and the lead id; idempotent since it sets the lead to the supplied values.",
    idempotent: !0
  },
  props: {
    campaignId: import_pieces_framework19.Property.Dropdown({
      auth: ReachinboxAuth,
      displayName: "Select Campaign",
      description: "Choose a campaign from the list or enter the campaign ID manually.",
      required: !0,
      refreshers: ["auth"],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let campaigns = await fetchCampaigns(auth.secret_text);
        return {
          options: campaigns.map((campaign) => ({
            label: campaign.name,
            value: campaign.id
          })),
          disabled: campaigns.length === 0
        };
      }
    }),
    leadId: import_pieces_framework19.Property.Dropdown({
      auth: ReachinboxAuth,
      displayName: "Select Lead",
      description: "Choose a lead from the selected campaign.",
      required: !0,
      refreshers: ["campaignId"],
      options: async ({ auth, campaignId }) => {
        if (!campaignId || !auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first and select a campaign"
          };
        let response = await import_pieces_common13.httpClient.sendRequest({
          method: import_pieces_common13.HttpMethod.GET,
          url: `${reachinboxCommon.baseUrl}leads?campaignId=${campaignId}&lastLead=false`,
          headers: {
            Authorization: `Bearer ${auth.secret_text}`
          }
        });
        if (response.status !== 200)
          throw new Error("Failed to fetch leads.");
        let leads = Array.isArray(response.body.data.leads) ? response.body.data.leads : [];
        return {
          options: leads.map((lead) => ({
            label: lead.email,
            value: lead.id
          })),
          disabled: leads.length === 0
        };
      }
    }),
    email: import_pieces_framework19.Property.ShortText({
      displayName: "Email",
      description: "Enter the new email address for the lead.",
      required: !0
    }),
    firstName: import_pieces_framework19.Property.ShortText({
      displayName: "First Name",
      description: "Enter the new first name for the lead.",
      required: !1
    }),
    lastName: import_pieces_framework19.Property.ShortText({
      displayName: "Last Name",
      description: "Enter the new last name for the lead.",
      required: !1
    }),
    customVariables: import_pieces_framework19.Property.Array({
      displayName: "Custom Variables",
      description: "Add custom variables as key-value pairs for the lead.",
      properties: {
        key: import_pieces_framework19.Property.ShortText({
          displayName: "Key",
          description: "Enter the key for the custom variable",
          required: !0
        }),
        value: import_pieces_framework19.Property.ShortText({
          displayName: "Value",
          description: "Enter the value for the custom variable",
          required: !0
        })
      },
      required: !1,
      defaultValue: []
    })
  },
  async run(context) {
    let { campaignId, leadId, email, firstName, lastName, customVariables } = context.propsValue;
    if (!campaignId || !leadId)
      throw new Error("Campaign ID and Lead ID are required.");
    let customVariablesArray = customVariables || [], customVariablesObject = {};
    customVariablesArray.forEach((variable) => {
      customVariablesObject[variable.key] = variable.value;
    });
    let url = `${reachinboxCommon.baseUrl}leads/update`;
    try {
      let response = await import_pieces_common13.httpClient.sendRequest({
        method: import_pieces_common13.HttpMethod.PUT,
        url,
        headers: {
          Authorization: `Bearer ${context.auth.secret_text}`,
          "Content-Type": "application/json"
        },
        body: {
          campaignId,
          leadId,
          email,
          attributes: __spreadValues({
            firstName: firstName || "",
            lastName: lastName || ""
          }, customVariablesObject)
        }
      });
      if (response.status === 200)
        return {
          success: !0,
          message: response.body.message || "Lead updated successfully."
        };
      throw new Error(`Failed to update lead: ${response.body.message}`);
    } catch (error) {
      throw new Error(`Failed to update lead: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/reachinbox/src/index.ts
var import_pieces_framework21 = require("@activepieces/pieces-framework"), ReachinboxAuth = import_pieces_framework20.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Enter your ReachInbox API key",
  required: !0,
  validate: async ({ auth }) => /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(auth) ? { valid: !0 } : {
    valid: !1,
    error: "Invalid API Key. It should follow the UUID format (xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx)."
  }
}), reachinbox = (0, import_pieces_framework20.createPiece)({
  displayName: "Reachinbox",
  auth: ReachinboxAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/reachinbox.png",
  categories: [import_pieces_framework21.PieceCategory.MARKETING],
  authors: ["support@reachinbox.ai", "ManojKumard", "Mitrajit"],
  actions: [
    addLeads,
    addBlocklist,
    addEmail,
    enableWarmup,
    getCampaignAnalytics,
    getSummary,
    pauseCampaign,
    pauseWarmup,
    removeEmail,
    setSchedule,
    startCampaign,
    updateLead,
    (0, import_pieces_common14.createCustomApiCallAction)({
      baseUrl: () => "https://api.reachinbox.ai/api/v1/",
      auth: ReachinboxAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: [
    campaignCompleted,
    emailBounced,
    emailOpened,
    emailSent,
    leadInterested,
    leadNotInterested,
    replyReceived
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  ReachinboxAuth,
  reachinbox
});
