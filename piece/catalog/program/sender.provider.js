"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames;
var __hasOwnProp = Object.prototype.hasOwnProperty;
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

// ../ap/packages/pieces/community/sender/src/index.ts
var index_exports = {};
__export(index_exports, {
  sender: () => sender
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework16 = require("@activepieces/pieces-framework"), import_pieces_framework17 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/sender/src/lib/common/common.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework"), senderAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Token",
  description: "Enter your Sender API Token",
  required: !0
}), SENDER_API_BASE_URL = "https://api.sender.net/v2";
async function makeSenderRequest(auth, endpoint, method = import_pieces_common.HttpMethod.GET, body) {
  return await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${SENDER_API_BASE_URL}${endpoint}`,
    headers: {
      Authorization: `Bearer ${auth}`,
      "Content-Type": "application/json",
      Accept: "application/json"
    },
    body
  });
}
var groupIdDropdown = import_pieces_framework.Property.Dropdown({
  auth: senderAuth,
  displayName: "Groups",
  description: "Select one or more groups",
  required: !1,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your Sender account first"
      };
    try {
      return {
        disabled: !1,
        options: ((await makeSenderRequest(
          auth.secret_text,
          "/groups",
          import_pieces_common.HttpMethod.GET
        )).body.data || []).map((group) => ({
          label: group.title,
          value: group.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Error loading groups"
      };
    }
  }
}), groupIdsDropdown = import_pieces_framework.Property.MultiSelectDropdown({
  auth: senderAuth,
  displayName: "Groups",
  description: "Select one or more groups",
  required: !1,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your Sender account first"
      };
    try {
      return {
        disabled: !1,
        options: ((await makeSenderRequest(
          auth.secret_text,
          "/groups",
          import_pieces_common.HttpMethod.GET
        )).body.data || []).map((group) => ({
          label: group.title,
          value: group.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Error loading groups"
      };
    }
  }
}), subscribersDropdown = import_pieces_framework.Property.MultiSelectDropdown({
  auth: senderAuth,
  displayName: "Subscribers",
  description: "Select one or more subscribers to delete",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your Sender account first"
      };
    try {
      return {
        disabled: !1,
        options: ((await makeSenderRequest(
          auth.secret_text,
          "/subscribers?limit=50",
          import_pieces_common.HttpMethod.GET
        )).body.data || []).map((sub) => ({
          label: sub.email,
          value: sub.email
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Error loading subscribers"
      };
    }
  }
}), subscriberDropdownSingle = import_pieces_framework.Property.Dropdown({
  auth: senderAuth,
  displayName: "Subscriber",
  description: "Select a subscriber",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your Sender account first"
      };
    try {
      return {
        disabled: !1,
        options: ((await makeSenderRequest(
          auth.secret_text,
          "/subscribers?limit=50",
          import_pieces_common.HttpMethod.GET
        )).body.data || []).map((sub) => ({
          label: sub.email,
          value: sub.email
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Error loading subscribers"
      };
    }
  }
}), campaignDropdown = import_pieces_framework.Property.Dropdown({
  auth: senderAuth,
  displayName: "Campaign",
  description: "Select a campaign",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your Sender account first"
      };
    try {
      return {
        disabled: !1,
        options: ((await makeSenderRequest(
          auth.secret_text,
          "/campaigns?limit=50&status=DRAFT",
          import_pieces_common.HttpMethod.GET
        )).body.data || []).map((c) => ({
          label: c.title || c.subject || c.id,
          value: c.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Error loading campaigns"
      };
    }
  }
});

// ../ap/packages/pieces/community/sender/src/lib/actions/add-subscriber.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common2 = require("@activepieces/pieces-common"), addUpdateSubscriberAction = (0, import_pieces_framework2.createAction)({
  auth: senderAuth,
  name: "add_update_subscriber",
  classification: "WRITE",
  displayName: "Add / Update Subscriber",
  description: "Add a new subscriber or update existing subscriber's data",
  audience: "both",
  aiMetadata: { description: "Upserts a subscriber in a Sender account by email: creates the contact if the email is new, otherwise updates its name, phone, group membership, and custom fields. Use to ensure a contact exists with given details before campaigns or grouping. Keyed on email, so repeating the same call is idempotent (no duplicate contact); note the optional flag that triggers automation workflows on the subscriber.", idempotent: !0 },
  props: {
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email",
      description: "Subscriber email address",
      required: !0
    }),
    firstname: import_pieces_framework2.Property.ShortText({
      displayName: "First Name",
      description: "Subscriber first name",
      required: !1
    }),
    lastname: import_pieces_framework2.Property.ShortText({
      displayName: "Last Name",
      description: "Subscriber last name",
      required: !1
    }),
    phone: import_pieces_framework2.Property.ShortText({
      displayName: "Phone",
      description: "Subscriber phone number",
      required: !1
    }),
    groups: groupIdsDropdown,
    customFields: import_pieces_framework2.Property.Json({
      displayName: "Custom Fields",
      description: "JSON object with custom field keys and values",
      required: !1
    }),
    triggerAutomation: import_pieces_framework2.Property.Checkbox({
      displayName: "Trigger Automation",
      description: "Whether to trigger automation workflows",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let subscriberData = {
      email: context.propsValue.email
    };
    return context.propsValue.firstname && (subscriberData.firstname = context.propsValue.firstname), context.propsValue.lastname && (subscriberData.lastname = context.propsValue.lastname), context.propsValue.phone && (subscriberData.phone = context.propsValue.phone), context.propsValue.groups && (subscriberData.groups = context.propsValue.groups), context.propsValue.customFields && (subscriberData.fields = context.propsValue.customFields), context.propsValue.triggerAutomation && (subscriberData.trigger_automation = !0), (await makeSenderRequest(
      context.auth.secret_text,
      "/subscribers",
      import_pieces_common2.HttpMethod.POST,
      subscriberData
    )).body;
  }
});

// ../ap/packages/pieces/community/sender/src/lib/actions/create-campaign.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common"), createCampaignAction = (0, import_pieces_framework3.createAction)({
  auth: senderAuth,
  name: "create_campaign",
  classification: "WRITE",
  displayName: "Create Campaign",
  description: "Creates a draft campaign in Sender",
  audience: "both",
  aiMetadata: { description: 'Creates a new draft email campaign in a Sender account targeting one or more subscriber groups, with subject, sender name, reply-to, and content type. Use to set up a campaign for later sending (it is not sent here). Not idempotent: each call creates a separate draft, so repeating produces duplicates. Content type must be one of "editor", "html", or "text".', idempotent: !1 },
  props: {
    title: import_pieces_framework3.Property.ShortText({
      displayName: "Campaign Name",
      description: "The name of the campaign",
      required: !1
    }),
    subject: import_pieces_framework3.Property.ShortText({
      displayName: "Email Subject",
      description: "The subject line of the email",
      required: !0
    }),
    from: import_pieces_framework3.Property.ShortText({
      displayName: "From Name",
      description: "Sender name",
      required: !0
    }),
    reply_to: import_pieces_framework3.Property.ShortText({
      displayName: "Reply To",
      description: "Reply-to email address",
      required: !0
    }),
    content_type: import_pieces_framework3.Property.LongText({
      displayName: "Content Type",
      description: 'The value must be one of "editor", "html", or "text"',
      required: !0
    }),
    groups: groupIdsDropdown
  },
  async run(context) {
    let campaignData = {
      title: context.propsValue.title,
      subject: context.propsValue.subject,
      from: context.propsValue.from,
      content_type: context.propsValue.content_type,
      groups: context.propsValue.groups
    };
    return context.propsValue.reply_to && (campaignData.reply_to = context.propsValue.reply_to), (await makeSenderRequest(
      context.auth.secret_text,
      "/campaigns",
      import_pieces_common3.HttpMethod.POST,
      campaignData
    )).body;
  }
});

// ../ap/packages/pieces/community/sender/src/lib/actions/unsubscribe-subscriber.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common"), unsubscribeSubscriberAction = (0, import_pieces_framework4.createAction)({
  auth: senderAuth,
  name: "unsubscribe_subscriber",
  classification: "DESTRUCTIVE",
  displayName: "Unsubscribe Subscriber",
  description: "Mark an email address as unsubscribed globally or from a group",
  audience: "both",
  aiMetadata: { description: "Marks a subscriber as unsubscribed in a Sender account so they stop receiving campaigns. Use to honor an opt-out or suppress a contact. Identify the subscriber by their Sender subscriber ID. Idempotent: re-running on an already-unsubscribed contact leaves them unsubscribed with no further effect.", idempotent: !0 },
  props: {
    subscriber: subscriberDropdownSingle
  },
  async run(context) {
    let requestBody = {
      subscribers: [context.propsValue.subscriber]
    };
    return (await makeSenderRequest(
      context.auth.secret_text,
      "/subscribers",
      import_pieces_common4.HttpMethod.DELETE,
      requestBody
    )).body;
  }
});

// ../ap/packages/pieces/community/sender/src/lib/actions/add-subscriber-to-group.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common"), addSubscriberToGroupAction = (0, import_pieces_framework5.createAction)({
  auth: senderAuth,
  name: "add_subscriber_to_group",
  classification: "WRITE",
  displayName: "Add Subscriber to Group",
  description: "Add an existing or new subscriber into one or more groups",
  audience: "both",
  aiMetadata: { description: "Assigns a single subscriber to one or more groups/lists in a Sender account, identified by their subscriber ID. Use to segment a contact for targeted campaigns. Idempotent: adding a contact already in a group leaves membership unchanged.", idempotent: !0 },
  props: {
    subscriber: subscriberDropdownSingle,
    groups: groupIdsDropdown
  },
  async run(context) {
    let subscriberData = {
      groups: context.propsValue.groups
    };
    return (await makeSenderRequest(
      context.auth.secret_text,
      `/subscribers/${context.propsValue.subscriber}`,
      import_pieces_common5.HttpMethod.PATCH,
      subscriberData
    )).body;
  }
});

// ../ap/packages/pieces/community/sender/src/lib/actions/remove-subscriber-from-group.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common"), removeSubscriberFromGroupAction = (0, import_pieces_framework6.createAction)({
  auth: senderAuth,
  name: "remove_subscriber_from_group",
  classification: "WRITE",
  displayName: "Remove Subscriber from Group",
  description: "Remove a subscriber from a specific group",
  audience: "both",
  aiMetadata: { description: "Removes one or more subscribers from a specific group/list in a Sender account, identified by group ID and subscriber IDs. Use to unsegment contacts without unsubscribing them globally. Idempotent: removing contacts no longer in the group leaves the group unchanged.", idempotent: !0 },
  props: {
    subscribers: subscribersDropdown,
    groupId: groupIdDropdown
  },
  async run(context) {
    let subscribers = context.propsValue.subscribers, groupId = context.propsValue.groupId, requestBody = {
      subscribers
    }, response = await makeSenderRequest(
      context.auth.secret_text,
      `/subscribers/groups/${groupId}`,
      import_pieces_common6.HttpMethod.DELETE,
      requestBody
    );
    return {
      success: !0,
      subscribers,
      groupId,
      removedAt: (/* @__PURE__ */ new Date()).toISOString(),
      response: response.body
    };
  }
});

// ../ap/packages/pieces/community/sender/src/lib/actions/send-campaign.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common"), sendCampaignAction = (0, import_pieces_framework7.createAction)({
  auth: senderAuth,
  name: "send_campaign",
  classification: "WRITE",
  displayName: "Send Campaign",
  description: "Trigger sending of a drafted campaign to its recipient list",
  audience: "both",
  aiMetadata: { description: "Sends an existing draft campaign in a Sender account to its configured recipient groups, identified by campaign ID. Use only after a campaign draft is created and ready to deliver. Not idempotent: this dispatches email to recipients, so re-running may re-send or error; call once.", idempotent: !1 },
  props: {
    campaignId: campaignDropdown
  },
  async run(context) {
    let campaignId = context.propsValue.campaignId;
    return (await makeSenderRequest(
      context.auth.secret_text,
      `/campaigns/${campaignId}/send`,
      import_pieces_common7.HttpMethod.POST
    )).body;
  }
});

// ../ap/packages/pieces/community/sender/src/lib/triggers/new-campaign.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var import_pieces_common8 = require("@activepieces/pieces-common"), newCampaignTrigger = (0, import_pieces_framework8.createTrigger)({
  auth: senderAuth,
  name: "new_campaign",
  classification: "READ",
  displayName: "New Campaign",
  description: "Fires when a new campaign is created in Sender",
  aiMetadata: {
    description: "Fires when a new campaign is created in the connected Sender account, representing the newly created campaign (including drafts)."
  },
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let webhookData = {
      url: context.webhookUrl,
      topic: "campaigns/new"
    }, response = await makeSenderRequest(
      context.auth.secret_text,
      "/account/webhooks",
      import_pieces_common8.HttpMethod.POST,
      webhookData
    );
    await context.store.put("webhookId", response.body.data.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get("webhookId");
    webhookId && await makeSenderRequest(
      context.auth.secret_text,
      `/account/webhooks/${webhookId}`,
      import_pieces_common8.HttpMethod.DELETE
    ), await context.store.delete("webhookId");
  },
  async run(context) {
    return [context.payload.body];
  },
  async test(context) {
    return (await makeSenderRequest(
      context.auth.secret_text,
      "/campaigns?limit=1",
      import_pieces_common8.HttpMethod.GET
    )).body.data || [];
  },
  sampleData: {}
});

// ../ap/packages/pieces/community/sender/src/lib/triggers/new-group.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var import_pieces_common9 = require("@activepieces/pieces-common"), newGroupTrigger = (0, import_pieces_framework9.createTrigger)({
  auth: senderAuth,
  name: "new_group",
  classification: "READ",
  displayName: "New Group",
  description: "Fires when a new group/list is created",
  aiMetadata: {
    description: "Fires when a new subscriber group/list is created in the connected Sender account, representing the newly created group."
  },
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let webhookData = {
      url: context.webhookUrl,
      topic: "groups/new"
    }, response = await makeSenderRequest(
      context.auth.secret_text,
      "/account/webhooks",
      import_pieces_common9.HttpMethod.POST,
      webhookData
    );
    await context.store.put("webhookId", response.body.data.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get("webhookId");
    webhookId && await makeSenderRequest(
      context.auth.secret_text,
      `/account/webhooks/${webhookId}`,
      import_pieces_common9.HttpMethod.DELETE
    ), await context.store.delete("webhookId");
  },
  async run(context) {
    return [context.payload.body];
  },
  async test(context) {
    return (await makeSenderRequest(
      context.auth.secret_text,
      "/groups?limit=1",
      import_pieces_common9.HttpMethod.GET
    )).body.data || [];
  },
  sampleData: {}
});

// ../ap/packages/pieces/community/sender/src/lib/triggers/new-subscriber.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var import_pieces_common10 = require("@activepieces/pieces-common"), newSubscriberTrigger = (0, import_pieces_framework10.createTrigger)({
  auth: senderAuth,
  name: "new_subscriber",
  classification: "READ",
  displayName: "New Subscriber",
  description: "Fires when a subscriber is added to any group or to account",
  aiMetadata: {
    description: "Fires when a new subscriber is added anywhere in the connected Sender account (to any group or to the account directly), representing the new contact."
  },
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let webhookData = {
      url: context.webhookUrl,
      topic: "subscribers/new"
    }, response = await makeSenderRequest(
      context.auth.secret_text,
      "/account/webhooks",
      import_pieces_common10.HttpMethod.POST,
      webhookData
    );
    await context.store.put("webhookId", response.body.data.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get("webhookId");
    webhookId && await makeSenderRequest(
      context.auth.secret_text,
      `/account/webhooks/${webhookId}`,
      import_pieces_common10.HttpMethod.DELETE
    ), await context.store.delete("webhookId");
  },
  async run(context) {
    return [context.payload.body];
  },
  async test(context) {
    return (await makeSenderRequest(
      context.auth.secret_text,
      "/subscribers?limit=1",
      import_pieces_common10.HttpMethod.GET
    )).body.data;
  },
  sampleData: {}
});

// ../ap/packages/pieces/community/sender/src/lib/triggers/new-subscriber-in-group.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var import_pieces_common11 = require("@activepieces/pieces-common"), newSubscriberInGroupTrigger = (0, import_pieces_framework11.createTrigger)({
  auth: senderAuth,
  name: "new_subscriber_in_group",
  classification: "READ",
  displayName: "New Subscriber in Group",
  description: "Fires when a subscriber is added to a specific group/list",
  aiMetadata: {
    description: "Fires when a subscriber is added to one specific group/list (identified by group ID) in the connected Sender account, representing that contact joining the monitored group."
  },
  type: import_pieces_framework11.TriggerStrategy.WEBHOOK,
  props: {
    groupId: import_pieces_framework11.Property.ShortText({
      displayName: "Group ID",
      description: "The ID of the group to monitor",
      required: !0
    })
  },
  async onEnable(context) {
    let webhookUrl = context.webhookUrl, groupId = context.propsValue.groupId, webhookData = {
      url: webhookUrl,
      topic: "groups/new-subscriber",
      relation_id: groupId
    }, response = await makeSenderRequest(
      context.auth.secret_text,
      "/account/webhooks",
      import_pieces_common11.HttpMethod.POST,
      webhookData
    );
    await context.store.put("webhookId", response.body.data.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get("webhookId");
    webhookId && await makeSenderRequest(
      context.auth.secret_text,
      `/account/webhooks/${webhookId}`,
      import_pieces_common11.HttpMethod.DELETE
    ), await context.store.delete("webhookId");
  },
  async run(context) {
    return [context.payload.body];
  },
  async test(context) {
    let groupId = context.propsValue.groupId;
    return (await makeSenderRequest(
      context.auth.secret_text,
      `/groups/${groupId}/subscribers?limit=1`
    )).body.data || [];
  },
  sampleData: {}
});

// ../ap/packages/pieces/community/sender/src/lib/triggers/updated-subscriber.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var import_pieces_common12 = require("@activepieces/pieces-common"), updatedSubscriberTrigger = (0, import_pieces_framework12.createTrigger)({
  auth: senderAuth,
  name: "updated_subscriber",
  classification: "READ",
  displayName: "Updated Subscriber",
  description: "Fires when a subscriber's data (fields) is updated",
  aiMetadata: {
    description: "Fires when an existing subscriber's data or custom fields are updated in the connected Sender account, representing the changed contact."
  },
  type: import_pieces_framework12.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let webhookData = {
      url: context.webhookUrl,
      topic: "subscribers/updated"
    }, response = await makeSenderRequest(
      context.auth.secret_text,
      "/account/webhooks",
      import_pieces_common12.HttpMethod.POST,
      webhookData
    );
    await context.store.put("webhookId", response.body.data.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get("webhookId");
    webhookId && await makeSenderRequest(
      context.auth.secret_text,
      `/account/webhooks/${webhookId}`,
      import_pieces_common12.HttpMethod.DELETE
    ), await context.store.delete("webhookId");
  },
  async run(context) {
    return [context.payload.body];
  },
  async test(context) {
    return (await makeSenderRequest(
      context.auth.secret_text,
      "/subscribers?limit=1",
      import_pieces_common12.HttpMethod.GET
    )).body.data || [];
  },
  sampleData: {}
});

// ../ap/packages/pieces/community/sender/src/lib/triggers/new-unsubscriber.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var import_pieces_common13 = require("@activepieces/pieces-common"), newUnsubscriberTrigger = (0, import_pieces_framework13.createTrigger)({
  auth: senderAuth,
  name: "new_unsubscriber",
  classification: "READ",
  displayName: "New Unsubscriber",
  description: "Fires when someone unsubscribes globally",
  aiMetadata: {
    description: "Fires when a subscriber unsubscribes globally from the connected Sender account, representing the contact that opted out of all communications."
  },
  type: import_pieces_framework13.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let webhookData = {
      url: context.webhookUrl,
      topic: "subscribers/unsubscribed"
    }, response = await makeSenderRequest(
      context.auth.secret_text,
      "/account/webhooks",
      import_pieces_common13.HttpMethod.POST,
      webhookData
    );
    await context.store.put("webhookId", response.body.data.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get("webhookId");
    webhookId && await makeSenderRequest(
      context.auth.secret_text,
      `/account/webhooks/${webhookId}`,
      import_pieces_common13.HttpMethod.DELETE
    ), await context.store.delete("webhookId");
  },
  async run(context) {
    return [context.payload.body];
  },
  async test(context) {
    return [];
  },
  sampleData: {}
});

// ../ap/packages/pieces/community/sender/src/lib/triggers/new-unsubscriber-from-group.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var import_pieces_common14 = require("@activepieces/pieces-common"), newUnsubscriberFromGroupTrigger = (0, import_pieces_framework14.createTrigger)({
  auth: senderAuth,
  name: "new_unsubscriber_from_group",
  classification: "READ",
  displayName: "New Unsubscriber From Group",
  description: "Fires when a subscriber is removed/unsubscribed from a specific group",
  aiMetadata: {
    description: "Fires when a subscriber is removed from or unsubscribes from one specific group/list (identified by group ID) in the connected Sender account, representing that contact leaving the monitored group."
  },
  type: import_pieces_framework14.TriggerStrategy.WEBHOOK,
  props: {
    groupId: import_pieces_framework14.Property.ShortText({
      displayName: "Group ID",
      description: "The ID of the group to monitor",
      required: !0
    })
  },
  async onEnable(context) {
    let webhookUrl = context.webhookUrl, groupId = context.propsValue.groupId, webhookData = {
      url: webhookUrl,
      topic: "groups/unsubscribed",
      relation_id: groupId
    }, response = await makeSenderRequest(
      context.auth.secret_text,
      "/account/webhooks",
      import_pieces_common14.HttpMethod.POST,
      webhookData
    );
    await context.store.put("webhookId", response.body.data.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get("webhookId");
    webhookId && await makeSenderRequest(
      context.auth.secret_text,
      `/account/webhooks/${webhookId}`,
      import_pieces_common14.HttpMethod.DELETE
    ), await context.store.delete("webhookId");
  },
  async run(context) {
    return [context.payload.body];
  },
  async test(context) {
    return [];
  },
  sampleData: {}
});

// ../ap/packages/pieces/community/sender/src/lib/actions/update-subscriber.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");
var import_pieces_common15 = require("@activepieces/pieces-common"), updateSubscriberAction = (0, import_pieces_framework15.createAction)({
  auth: senderAuth,
  name: "update_subscriber",
  classification: "WRITE",
  displayName: "Update Subscriber",
  description: "Update an existing subscriber's data",
  audience: "both",
  aiMetadata: { description: "Updates an existing subscriber's name, phone, or custom fields in a Sender account, identified by subscriber ID. Use when the contact already exists and you only need to change attributes; to create-or-update by email use Add / Update Subscriber instead. Idempotent: applying the same values repeatedly yields the same result.", idempotent: !0 },
  props: {
    subscriber: subscriberDropdownSingle,
    email: import_pieces_framework15.Property.ShortText({
      displayName: "Email",
      description: "Subscriber email address to update",
      required: !0
    }),
    firstname: import_pieces_framework15.Property.ShortText({
      displayName: "First Name",
      description: "New first name",
      required: !1
    }),
    lastname: import_pieces_framework15.Property.ShortText({
      displayName: "Last Name",
      description: "New last name",
      required: !1
    }),
    phone: import_pieces_framework15.Property.ShortText({
      displayName: "Phone",
      description: "New phone number",
      required: !1
    }),
    customFields: import_pieces_framework15.Property.Json({
      displayName: "Custom Fields",
      description: "JSON object with custom field keys and values to update",
      required: !1
    })
  },
  async run(context) {
    let email = context.propsValue.email, phone = context.propsValue.phone, { subscriber } = context.propsValue, subscriberId = subscriber, updateData = {};
    return context.propsValue.firstname && (updateData.firstname = context.propsValue.firstname), context.propsValue.lastname && (updateData.lastname = context.propsValue.lastname), context.propsValue.phone && (updateData.phone = context.propsValue.phone), context.propsValue.customFields && (updateData.fields = context.propsValue.customFields), (await makeSenderRequest(
      context.auth.secret_text,
      `/subscribers/${subscriberId}`,
      import_pieces_common15.HttpMethod.PATCH,
      updateData
    )).body;
  }
});

// ../ap/packages/pieces/community/sender/src/index.ts
var sender = (0, import_pieces_framework16.createPiece)({
  displayName: "Sender",
  auth: senderAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/sender.png",
  authors: ["Ani-4x", "sanket-a11y"],
  categories: [import_pieces_framework17.PieceCategory.MARKETING],
  actions: [
    addUpdateSubscriberAction,
    createCampaignAction,
    unsubscribeSubscriberAction,
    addSubscriberToGroupAction,
    removeSubscriberFromGroupAction,
    sendCampaignAction,
    updateSubscriberAction
  ],
  triggers: [
    newCampaignTrigger,
    newGroupTrigger,
    newSubscriberTrigger,
    newSubscriberInGroupTrigger,
    updatedSubscriberTrigger,
    newUnsubscriberTrigger,
    newUnsubscriberFromGroupTrigger
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  sender
});
