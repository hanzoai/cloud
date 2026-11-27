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

// ../ap/packages/pieces/community/drip/src/index.ts
var index_exports = {};
__export(index_exports, {
  drip: () => drip
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/drip/src/lib/actions/add-subscriber-to-campaign.action.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/drip/src/lib/common/index.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/drip/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), dripAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "Get it from https://www.getdrip.com/user/edit"
});

// ../ap/packages/pieces/community/drip/src/lib/common/index.ts
var dripCommon = {
  baseUrl: (accountId) => `https://api.getdrip.com/v2/${accountId}`,
  account_id: import_pieces_framework2.Property.Dropdown({
    auth: dripAuth,
    displayName: "Account",
    required: !0,
    refreshers: [],
    options: async ({ auth }) => {
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please fill in API key first"
        };
      let request = {
        method: import_pieces_common.HttpMethod.GET,
        url: "https://api.getdrip.com/v2/accounts",
        headers: {
          Authorization: `Basic ${Buffer.from(auth.secret_text).toString(
            "base64"
          )}`
        }
      };
      return {
        disabled: !1,
        options: (await import_pieces_common.httpClient.sendRequest(request)).body.accounts.map((acc) => ({ value: acc.id, label: acc.name }))
      };
    }
  }),
  subscriber: import_pieces_framework2.Property.ShortText({
    required: !0,
    displayName: "Subscriber Email",
    description: "Email of the subscriber"
  }),
  tags: import_pieces_framework2.Property.Array({
    displayName: "tags",
    required: !1,
    description: "Tags to apply to subscriber"
  }),
  custom_fields: import_pieces_framework2.Property.Object({
    displayName: "Custom Fields",
    required: !1,
    description: "Custom field data about the subscriber"
  }),
  authorizationHeader: (apiKey) => `Basic ${Buffer.from(apiKey.secret_text).toString("base64")}`
};

// ../ap/packages/pieces/community/drip/src/lib/actions/add-subscriber-to-campaign.action.ts
var dripAddSubscriberToCampaign = (0, import_pieces_framework3.createAction)({
  auth: dripAuth,
  name: "add_subscriber_to_campaign",
  description: "Add a subscriber to a campaign (Email series)",
  audience: "both",
  aiMetadata: { description: "Subscribes a contact (by email) to a Drip email-series campaign in the given account, optionally attaching tags and custom fields. Use to enroll someone into an automated email sequence. Requires an existing campaign id (selectable from the account). Not idempotent: each call re-subscribes and can re-trigger the series.", idempotent: !1 },
  displayName: "Add a subscriber to a campaign",
  props: {
    account_id: dripCommon.account_id,
    campaign_id: import_pieces_framework3.Property.Dropdown({
      displayName: "Email Series Campaign",
      auth: dripAuth,
      refreshers: ["account_id"],
      required: !0,
      options: async ({ auth, account_id }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please fill in API key first"
          };
        if (!account_id)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please select an account first"
          };
        let request = {
          method: import_pieces_common2.HttpMethod.GET,
          url: `${dripCommon.baseUrl(account_id)}/campaigns`,
          headers: {
            Authorization: `Basic ${Buffer.from(auth.secret_text).toString(
              "base64"
            )}`
          }
        }, opts = (await import_pieces_common2.httpClient.sendRequest(request)).body.campaigns.map((campaign) => ({ value: campaign.id, label: campaign.name }));
        return opts.length === 0 ? {
          disabled: !1,
          options: [],
          placeholder: "Please create an email series campaign"
        } : {
          disabled: !1,
          options: opts
        };
      }
    }),
    subscriber: dripCommon.subscriber,
    tags: dripCommon.tags,
    custom_fields: dripCommon.custom_fields
  },
  async run({ auth, propsValue }) {
    let request = {
      method: import_pieces_common2.HttpMethod.POST,
      url: `${dripCommon.baseUrl(propsValue.account_id)}/campaigns/${propsValue.campaign_id}/subscribers`,
      body: {
        subscribers: [
          {
            email: propsValue.subscriber,
            tags: propsValue.tags,
            custom_fields: propsValue.custom_fields
          }
        ]
      },
      headers: {
        Authorization: dripCommon.authorizationHeader(auth)
      },
      queryParams: {}
    };
    return await import_pieces_common2.httpClient.sendRequest(request);
  }
});

// ../ap/packages/pieces/community/drip/src/lib/actions/apply-tag-to-subscriber.action.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var dripApplyTagToSubscriber = (0, import_pieces_framework4.createAction)({
  auth: dripAuth,
  name: "apply_tag_to_subscriber",
  description: "Apply a tag to a subscriber",
  audience: "both",
  aiMetadata: { description: "Applies a tag to a subscriber in a Drip account, identifying the subscriber by email address. Use to label or segment a contact. Repeating with the same email and tag has no additional effect, so it is idempotent.", idempotent: !0 },
  displayName: "Apply a tag to subscriber",
  props: {
    account_id: dripCommon.account_id,
    subscriber: dripCommon.subscriber,
    tag: import_pieces_framework4.Property.ShortText({
      displayName: "Tag",
      required: !0,
      description: "Tag to apply"
    })
  },
  async run({ auth, propsValue }) {
    let request = {
      method: import_pieces_common3.HttpMethod.POST,
      url: `${dripCommon.baseUrl(propsValue.account_id)}/tags`,
      body: {
        tags: [
          {
            email: propsValue.subscriber,
            tag: propsValue.tag
          }
        ]
      },
      headers: {
        Authorization: dripCommon.authorizationHeader(auth)
      },
      queryParams: {}
    };
    return await import_pieces_common3.httpClient.sendRequest(request);
  }
});

// ../ap/packages/pieces/community/drip/src/lib/actions/upsert-subscriber.action.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var dripUpsertSubscriberAction = (0, import_pieces_framework5.createAction)({
  auth: dripAuth,
  name: "upsert_subscriber",
  description: "Create or Update Subscriber",
  audience: "both",
  aiMetadata: { description: "Creates a subscriber in a Drip account, or updates the existing one matched by email address, setting tags, custom fields, and contact details (name, address, phone, location). Use to add a new contact or keep an existing contact in sync. Idempotent: matched on the stable email, so repeating with the same input converges to the same record.", idempotent: !0 },
  displayName: "Create or Update Subscriber",
  props: {
    account_id: dripCommon.account_id,
    subscriber: dripCommon.subscriber,
    tags: dripCommon.tags,
    custom_fields: dripCommon.custom_fields,
    first_name: import_pieces_framework5.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    last_name: import_pieces_framework5.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    zip: import_pieces_framework5.Property.ShortText({
      displayName: "Zip Code",
      description: "Postal code in which the subscriber resides",
      required: !1
    }),
    country: import_pieces_framework5.Property.ShortText({
      displayName: "Country",
      description: "The country in which the subscriber resides",
      required: !1
    }),
    state: import_pieces_framework5.Property.ShortText({
      displayName: "State",
      description: "The region in which the subscriber resides",
      required: !1
    }),
    city: import_pieces_framework5.Property.ShortText({
      displayName: "City",
      description: "The city in which the subscriber resides",
      required: !1
    }),
    phone: import_pieces_framework5.Property.ShortText({
      displayName: "Phone",
      description: "The subscriber's primary phone number",
      required: !1
    }),
    address: import_pieces_framework5.Property.ShortText({
      displayName: "Address",
      description: "The subscriber's mailing address",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let request = {
      method: import_pieces_common4.HttpMethod.POST,
      url: `${dripCommon.baseUrl(propsValue.account_id)}/subscribers`,
      body: {
        subscribers: [
          {
            email: propsValue.subscriber,
            tags: propsValue.tags,
            custom_fields: propsValue.custom_fields,
            country: propsValue.country,
            address1: propsValue.address,
            city: propsValue.city,
            state: propsValue.state,
            zip: propsValue.zip,
            phone: propsValue.phone,
            first_name: propsValue.first_name,
            last_name: propsValue.last_name
          }
        ]
      },
      headers: {
        Authorization: dripCommon.authorizationHeader(auth)
      },
      queryParams: {}
    };
    return await import_pieces_common4.httpClient.sendRequest(request);
  }
});

// ../ap/packages/pieces/community/drip/src/lib/trigger/new-subscriber.trigger.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var triggerNameInStore = "drip_new_subscriber_trigger", dripNewSubscriberEvent = (0, import_pieces_framework6.createTrigger)({
  auth: dripAuth,
  name: "new_subscriber",
  displayName: "New Subscriber",
  description: "Triggers when a subscriber is created in your Drip account.",
  aiMetadata: {
    description: "Fires when a new subscriber is created in the selected Drip account, emitting the subscriber.created event. Represents a contact being added to the list."
  },
  props: {
    account_id: dripCommon.account_id
  },
  sampleData: {
    event: "subscriber.created",
    data: {
      account_id: "9999999",
      subscriber: {}
    },
    occurred_at: "2013-06-21T10:31:58Z"
  },
  type: import_pieces_framework6.TriggerStrategy.WEBHOOK,
  async onEnable({ auth, propsValue, webhookUrl, store }) {
    let request = {
      method: import_pieces_common5.HttpMethod.POST,
      url: `${dripCommon.baseUrl(propsValue.account_id)}/webhooks`,
      body: {
        webhooks: [{ post_url: webhookUrl, events: ["subscriber.created"] }]
      },
      headers: {
        Authorization: dripCommon.authorizationHeader(auth)
      },
      queryParams: {}
    }, { body } = await import_pieces_common5.httpClient.sendRequest(request);
    await store.put(triggerNameInStore, {
      webhookId: body.webhooks[0].id,
      userId: propsValue.account_id
    });
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(
      triggerNameInStore
    ));
    if (response != null) {
      let request = {
        method: import_pieces_common5.HttpMethod.DELETE,
        url: `${dripCommon.baseUrl(response.userId)}/webhooks/${response.webhookId}`,
        headers: {
          Authorization: dripCommon.authorizationHeader(context.auth)
        }
      };
      await import_pieces_common5.httpClient.sendRequest(request);
    }
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/drip/src/lib/trigger/new-tag.trigger.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework");
var triggerNameInStore2 = "drip_tag_applied_to_subscriber_trigger", dripTagAppliedEvent = (0, import_pieces_framework7.createTrigger)({
  auth: dripAuth,
  name: "tag_applied_to_subscribers",
  displayName: "Tag Applied",
  description: "Triggers when a tag is applied.",
  aiMetadata: {
    description: "Fires when a tag is applied to a subscriber in the selected Drip account, emitting the subscriber.applied_tag event. Represents a contact being labeled or segmented; the payload includes the applied tag."
  },
  props: {
    account_id: dripCommon.account_id
  },
  sampleData: {
    event: "subscriber.applied_tag",
    data: {
      account_id: "9999999",
      subscriber: {},
      properties: {
        tag: "Customer"
      }
    },
    occurred_at: "2013-06-21T10:31:58Z"
  },
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    var _a;
    let request = {
      method: import_pieces_common6.HttpMethod.POST,
      url: `${dripCommon.baseUrl(context.propsValue.account_id)}/webhooks`,
      body: {
        webhooks: [
          { post_url: context.webhookUrl, events: ["subscriber.applied_tag"] }
        ]
      },
      headers: {
        Authorization: dripCommon.authorizationHeader(context.auth)
      },
      queryParams: {}
    }, { body } = await import_pieces_common6.httpClient.sendRequest(request);
    await ((_a = context.store) == null ? void 0 : _a.put(triggerNameInStore2, {
      webhookId: body.webhooks[0].id,
      userId: context.propsValue.account_id
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(
      triggerNameInStore2
    ));
    if (response != null) {
      let request = {
        method: import_pieces_common6.HttpMethod.DELETE,
        url: `${dripCommon.baseUrl(response.userId)}/webhooks/${response.webhookId}`,
        headers: {
          Authorization: dripCommon.authorizationHeader(context.auth)
        }
      };
      await import_pieces_common6.httpClient.sendRequest(request);
    }
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/drip/src/index.ts
var drip = (0, import_pieces_framework8.createPiece)({
  displayName: "Drip",
  description: "E-commerce CRM for B2B marketers",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/drip.png",
  authors: ["kishanprmr", "MoShizzle", "AbdulTheActivePiecer", "khaledmashaly", "abuaboud"],
  categories: [import_pieces_framework9.PieceCategory.MARKETING],
  auth: dripAuth,
  actions: [
    dripApplyTagToSubscriber,
    dripAddSubscriberToCampaign,
    dripUpsertSubscriberAction,
    (0, import_pieces_common7.createCustomApiCallAction)({
      baseUrl: () => "https://api.getdrip.com/v2/",
      auth: dripAuth,
      authMapping: async (auth) => ({
        Authorization: `Basic ${Buffer.from(auth.secret_text).toString(
          "base64"
        )}`
      })
    })
  ],
  triggers: [dripNewSubscriberEvent, dripTagAppliedEvent]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  drip
});
