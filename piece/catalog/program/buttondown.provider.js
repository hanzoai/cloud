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

// ../ap/packages/pieces/community/buttondown/src/index.ts
var index_exports = {};
__export(index_exports, {
  buttondown: () => buttondown
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_framework7 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/buttondown/src/lib/common/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/buttondown/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
var BUTTONDOWN_BASE_URL = "https://api.buttondown.com/v1", sanitizeHeaders = (headers = {}) => Object.fromEntries(
  Object.entries(headers).filter(([, value]) => value !== void 0)
), serializeQueryParams = (query) => {
  if (!query)
    return;
  let result = {};
  for (let [key, value] of Object.entries(query))
    if (value !== void 0) {
      if (Array.isArray(value)) {
        if (value.length === 0)
          continue;
        result[key] = value.map((item) => String(item)).join(",");
        continue;
      }
      result[key] = String(value);
    }
  return Object.keys(result).length > 0 ? result : void 0;
}, buttondownRequest = async ({
  auth,
  method,
  path,
  query,
  body,
  headers
}) => (await import_pieces_common.httpClient.sendRequest({
  method,
  url: `${BUTTONDOWN_BASE_URL}${path}`,
  headers: __spreadValues({
    Authorization: `Token ${auth}`,
    Accept: "application/json",
    "Content-Type": "application/json"
  }, sanitizeHeaders(headers)),
  queryParams: serializeQueryParams(query),
  body
})).body;

// ../ap/packages/pieces/community/buttondown/src/lib/common/auth.ts
var buttondownAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Create an API key from https://buttondown.com/keys and paste it here.",
  required: !0,
  validate: async ({ auth }) => {
    var _a, _b, _c, _d, _e, _f;
    if (!auth)
      return {
        valid: !1,
        error: "API key is required."
      };
    try {
      return await import_pieces_common2.httpClient.sendRequest({
        method: import_pieces_common2.HttpMethod.GET,
        url: `${BUTTONDOWN_BASE_URL}/newsletters`,
        headers: {
          Authorization: `Token ${auth}`
        }
      }), {
        valid: !0
      };
    } catch (unknownError) {
      let error = unknownError, status = (_a = error.response) == null ? void 0 : _a.status, body = (_c = (_b = error.response) == null ? void 0 : _b.body) != null ? _c : {};
      return {
        valid: !1,
        error: (_f = (_e = (_d = body.detail) != null ? _d : body.error) != null ? _e : body.message) != null ? _f : status === 401 ? "Invalid API key." : "Failed to verify the API key."
      };
    }
  }
});

// ../ap/packages/pieces/community/buttondown/src/lib/actions/create-subscriber.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/buttondown/src/lib/common/options.ts
var subscriberTypeOptions = [
  { label: "Regular", value: "regular" },
  { label: "Premium", value: "premium" },
  { label: "Unactivated", value: "unactivated" },
  { label: "Paused", value: "paused" },
  { label: "Trialed", value: "trialed" },
  { label: "Upcoming", value: "upcoming" },
  { label: "Churning", value: "churning" },
  { label: "Churned", value: "churned" },
  { label: "Unsubscribed", value: "unsubscribed" },
  { label: "Undeliverable", value: "undeliverable" },
  { label: "Blocked", value: "blocked" },
  { label: "Complained", value: "complained" },
  { label: "Gifted", value: "gifted" },
  { label: "Past Due", value: "past_due" },
  { label: "Removed", value: "removed" },
  { label: "Unpaid", value: "unpaid" }
], subscriberSourceOptions = [
  { label: "Admin", value: "admin" },
  { label: "API", value: "api" },
  { label: "Carrd", value: "carrd" },
  { label: "Comment", value: "comment" },
  { label: "Embedded form", value: "embedded_form" },
  { label: "Hosted form", value: "form" },
  { label: "Import", value: "import" },
  { label: "Memberful", value: "memberful" },
  { label: "Organic", value: "organic" },
  { label: "Patreon", value: "patreon" },
  { label: "Stripe", value: "stripe" },
  { label: "User portal", value: "user" },
  { label: "Zapier", value: "zapier" }
], subscriberOrderingOptions = [
  { label: "Newest first", value: "-creation_date" },
  { label: "Oldest first", value: "creation_date" },
  { label: "Email address (A-Z)", value: "email_address" },
  { label: "Email address (Z-A)", value: "-email_address" },
  { label: "Last opened (recent first)", value: "-last_open_date" },
  { label: "Last opened (oldest first)", value: "last_open_date" }
], emailStatusOptions = [
  { label: "Send immediately (about_to_send)", value: "about_to_send" },
  { label: "Draft", value: "draft" },
  { label: "Scheduled", value: "scheduled" }
], emailTypeOptions = [
  { label: "Public", value: "public" },
  { label: "Private", value: "private" },
  { label: "Premium", value: "premium" },
  { label: "Free", value: "free" },
  { label: "Churned", value: "churned" },
  { label: "Archival", value: "archival" }
];

// ../ap/packages/pieces/community/buttondown/src/lib/actions/create-subscriber.ts
var createSubscriber = (0, import_pieces_framework2.createAction)({
  auth: buttondownAuth,
  name: "createSubscriber",
  classification: "WRITE",
  displayName: "Create Subscriber",
  description: "Create a new subscriber in Buttondown.",
  audience: "both",
  aiMetadata: {
    description: 'Adds a subscriber to a Buttondown newsletter by email address, with optional tags, metadata, UTM attribution, and referral data. Use "regular" subscriber type to skip double opt-in. Not idempotent: each call creates a subscriber; control duplicate-email handling via collision behavior (default keeps the existing subscriber, or overwrite/merge tags).',
    idempotent: !1
  },
  props: {
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email Address",
      required: !0,
      description: "The email address of the subscriber."
    }),
    type: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Subscriber Type",
      description: 'Set the subscriber status. Use "regular" to bypass double opt-in.',
      required: !1,
      options: {
        options: subscriberTypeOptions
      }
    }),
    notes: import_pieces_framework2.Property.LongText({
      displayName: "Internal Notes",
      required: !1
    }),
    tags: import_pieces_framework2.Property.Array({
      displayName: "Tags",
      description: "Add one or more tags to assign to the subscriber.",
      required: !1,
      properties: {
        tag: import_pieces_framework2.Property.ShortText({
          displayName: "Tag",
          required: !0
        })
      }
    }),
    metadata: import_pieces_framework2.Property.Json({
      displayName: "Metadata",
      description: "Custom metadata to store on the subscriber (JSON object).",
      required: !1
    }),
    referrerUrl: import_pieces_framework2.Property.ShortText({
      displayName: "Referrer URL",
      required: !1
    }),
    utmSource: import_pieces_framework2.Property.ShortText({
      displayName: "UTM Source",
      required: !1
    }),
    utmMedium: import_pieces_framework2.Property.ShortText({
      displayName: "UTM Medium",
      required: !1
    }),
    utmCampaign: import_pieces_framework2.Property.ShortText({
      displayName: "UTM Campaign",
      required: !1
    }),
    referringSubscriberId: import_pieces_framework2.Property.ShortText({
      displayName: "Referring Subscriber ID",
      description: "ID of the subscriber who referred this contact.",
      required: !1
    }),
    ipAddress: import_pieces_framework2.Property.ShortText({
      displayName: "IP Address",
      description: "The original IP address of the subscriber. Helps Buttondown evaluate spam.",
      required: !1
    }),
    collisionBehavior: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Collision Behavior",
      description: "Control how Buttondown handles duplicate email addresses. Leave empty to keep the existing subscriber.",
      required: !1,
      options: {
        options: [
          { label: "Overwrite existing subscriber", value: "overwrite" },
          { label: "Merge tags with existing subscriber", value: "add" }
        ]
      }
    }),
    bypassFirewall: import_pieces_framework2.Property.Checkbox({
      displayName: "Bypass Firewall",
      description: "Set to true when the request originates from a trusted internal system without the subscriber's IP address.",
      required: !1,
      defaultValue: !1
    })
  },
  async run({ auth, propsValue }) {
    var _a;
    let payload = {
      email_address: propsValue.email
    };
    propsValue.notes && (payload.notes = propsValue.notes);
    let tags = (_a = propsValue.tags) == null ? void 0 : _a.map((entry) => {
      var _a2;
      return (_a2 = entry == null ? void 0 : entry.tag) == null ? void 0 : _a2.trim();
    }).filter((tag) => !!tag);
    if (tags && tags.length > 0 && (payload.tags = tags), propsValue.metadata) {
      if (typeof propsValue.metadata != "object" || Array.isArray(propsValue.metadata))
        throw new Error("Metadata must be a JSON object.");
      payload.metadata = propsValue.metadata;
    }
    propsValue.referrerUrl && (payload.referrer_url = propsValue.referrerUrl), propsValue.utmSource && (payload.utm_source = propsValue.utmSource), propsValue.utmMedium && (payload.utm_medium = propsValue.utmMedium), propsValue.utmCampaign && (payload.utm_campaign = propsValue.utmCampaign), propsValue.referringSubscriberId && (payload.referring_subscriber_id = propsValue.referringSubscriberId), propsValue.type && (payload.type = propsValue.type), propsValue.ipAddress && (payload.ip_address = propsValue.ipAddress);
    let headers = {
      "X-Buttondown-Collision-Behavior": propsValue.collisionBehavior,
      "X-Buttondown-Bypass-Firewall": propsValue.bypassFirewall ? "true" : void 0
    };
    return await buttondownRequest({
      auth: auth.secret_text,
      method: import_pieces_common3.HttpMethod.POST,
      path: "/subscribers",
      body: payload,
      headers
    });
  }
});

// ../ap/packages/pieces/community/buttondown/src/lib/actions/list-subscribers.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");
var listSubscribers = (0, import_pieces_framework3.createAction)({
  auth: buttondownAuth,
  name: "listSubscribers",
  classification: "SEARCH",
  displayName: "List Subscribers",
  description: "Retrieve subscribers from Buttondown.",
  audience: "both",
  aiMetadata: {
    description: "Lists subscribers from a Buttondown newsletter, optionally filtered by type, source, tags, email substring, or referrer URL; with no filters it returns all subscribers. Use to look up or enumerate subscribers. Read-only and idempotent. Returns one page (default 100); pass the returned cursor to page through results.",
    idempotent: !0
  },
  props: {
    limit: import_pieces_framework3.Property.Number({
      displayName: "Page Size",
      description: "Maximum number of subscribers to return (1-1000).",
      required: !1,
      defaultValue: 100
    }),
    cursor: import_pieces_framework3.Property.ShortText({
      displayName: "Cursor",
      description: "Provide the cursor value from a previous response to fetch the next page.",
      required: !1
    }),
    type: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Subscriber Type",
      required: !1,
      options: {
        options: subscriberTypeOptions
      }
    }),
    source: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Source",
      required: !1,
      options: {
        options: subscriberSourceOptions
      }
    }),
    tags: import_pieces_framework3.Property.Array({
      displayName: "Tags",
      description: "Filter by subscribers who have at least one of these tags.",
      required: !1,
      properties: {
        tag: import_pieces_framework3.Property.ShortText({
          displayName: "Tag",
          required: !0
        })
      }
    }),
    emailAddress: import_pieces_framework3.Property.ShortText({
      displayName: "Email Contains",
      description: "Return subscribers whose email address contains this value.",
      required: !1
    }),
    referrerUrl: import_pieces_framework3.Property.ShortText({
      displayName: "Referrer URL Contains",
      required: !1
    }),
    ordering: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Ordering",
      required: !1,
      options: {
        options: subscriberOrderingOptions
      }
    })
  },
  async run({ auth, propsValue }) {
    var _a;
    let query = {};
    propsValue.limit && (query.page_size = propsValue.limit), propsValue.cursor && (query.cursor = propsValue.cursor), propsValue.type && (query.type = [propsValue.type]), propsValue.source && (query.source = [propsValue.source]);
    let tags = (_a = propsValue.tags) == null ? void 0 : _a.map((entry) => {
      var _a2;
      return (_a2 = entry.tag) == null ? void 0 : _a2.trim();
    }).filter((tag) => !!tag);
    return tags && tags.length > 0 && (query.tag = tags), propsValue.emailAddress && (query.email_address = propsValue.emailAddress), propsValue.referrerUrl && (query.referrer_url = propsValue.referrerUrl), propsValue.ordering && (query.ordering = propsValue.ordering), await buttondownRequest({
      auth: auth.secret_text,
      method: import_pieces_common4.HttpMethod.GET,
      path: "/subscribers",
      query
    });
  }
});

// ../ap/packages/pieces/community/buttondown/src/lib/actions/send-email.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var sendEmail = (0, import_pieces_framework4.createAction)({
  auth: buttondownAuth,
  name: "sendEmail",
  classification: "WRITE",
  displayName: "Send Email",
  description: "Send an email to your Buttondown subscribers.",
  audience: "both",
  aiMetadata: {
    description: "Creates an email in Buttondown from a subject and Markdown/HTML body, then handles it per the chosen delivery status (e.g. send now vs. schedule for a future publish date, or save as draft). Use to broadcast a newsletter or queue one for later. A publish date is required when status is scheduled. Not idempotent: each call creates and may dispatch a new email.",
    idempotent: !1
  },
  props: {
    subject: import_pieces_framework4.Property.ShortText({
      displayName: "Subject",
      required: !0
    }),
    body: import_pieces_framework4.Property.LongText({
      displayName: "Body",
      description: "Write your email content in Markdown or HTML. Buttondown will detect the format automatically.",
      required: !0
    }),
    status: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Delivery Status",
      description: "Choose how Buttondown should treat this email after creation.",
      required: !1,
      defaultValue: "about_to_send",
      options: {
        options: emailStatusOptions
      }
    }),
    publishDate: import_pieces_framework4.Property.DateTime({
      displayName: "Publish Date",
      description: "Required when scheduling an email. Use ISO 8601 format.",
      required: !1
    }),
    emailType: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Email Type",
      required: !1,
      defaultValue: "public",
      options: {
        options: emailTypeOptions
      }
    }),
    description: import_pieces_framework4.Property.LongText({
      displayName: "Description",
      required: !1
    }),
    canonicalUrl: import_pieces_framework4.Property.ShortText({
      displayName: "Canonical URL",
      required: !1
    }),
    image: import_pieces_framework4.Property.ShortText({
      displayName: "Primary Image URL",
      required: !1
    }),
    slug: import_pieces_framework4.Property.ShortText({
      displayName: "Slug",
      description: "Override the automatic slug used for the archive URL.",
      required: !1
    }),
    metadata: import_pieces_framework4.Property.Json({
      displayName: "Metadata",
      description: "Custom metadata to attach to the email (JSON object).",
      required: !1
    }),
    attachments: import_pieces_framework4.Property.Array({
      displayName: "Attachment IDs",
      description: "IDs of attachments uploaded via the Buttondown API.",
      required: !1,
      properties: {
        id: import_pieces_framework4.Property.ShortText({
          displayName: "Attachment ID",
          required: !0
        })
      }
    }),
    triggerBilling: import_pieces_framework4.Property.Checkbox({
      displayName: "Trigger Pay-per-email Billing",
      description: "Enable when this email should count against pay-per-email billing for paid subscribers.",
      required: !1,
      defaultValue: !1
    })
  },
  async run({ auth, propsValue }) {
    var _a, _b;
    let status = (_a = propsValue.status) != null ? _a : "about_to_send";
    if (status === "scheduled" && !propsValue.publishDate)
      throw new Error("Publish date is required when scheduling an email.");
    let payload = {
      subject: propsValue.subject,
      body: propsValue.body,
      status
    };
    if (propsValue.publishDate && (payload.publish_date = propsValue.publishDate), propsValue.emailType && (payload.email_type = propsValue.emailType), propsValue.description && (payload.description = propsValue.description), propsValue.canonicalUrl && (payload.canonical_url = propsValue.canonicalUrl), propsValue.image && (payload.image = propsValue.image), propsValue.slug && (payload.slug = propsValue.slug), propsValue.metadata) {
      if (typeof propsValue.metadata != "object" || Array.isArray(propsValue.metadata))
        throw new Error("Metadata must be provided as a JSON object.");
      payload.metadata = propsValue.metadata;
    }
    let attachments = (_b = propsValue.attachments) == null ? void 0 : _b.map((entry) => {
      var _a2;
      return (_a2 = entry.id) == null ? void 0 : _a2.trim();
    }).filter((id) => !!id);
    return attachments && attachments.length > 0 && (payload.attachments = attachments), propsValue.triggerBilling && (payload.should_trigger_pay_per_email_billing = !0), await buttondownRequest({
      auth: auth.secret_text,
      method: import_pieces_common5.HttpMethod.POST,
      path: "/emails",
      body: payload
    });
  }
});

// ../ap/packages/pieces/community/buttondown/src/lib/actions/custom-api-call.ts
var import_pieces_common6 = require("@activepieces/pieces-common");
var customApiCall = (0, import_pieces_common6.createCustomApiCallAction)({
  auth: buttondownAuth,
  baseUrl: () => BUTTONDOWN_BASE_URL,
  authMapping: async (auth) => ({
    Authorization: `Token ${auth.secret_text}`
  })
});

// ../ap/packages/pieces/community/buttondown/src/lib/triggers/new-subscriber.ts
var import_pieces_common8 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/buttondown/src/lib/common/webhook.ts
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");
var createButtondownWebhookTrigger = ({
  name,
  displayName,
  description,
  aiMetadata,
  eventType,
  sampleData,
  enrich
}) => (0, import_pieces_framework5.createTrigger)({
  auth: buttondownAuth,
  name,
  classification: "READ",
  displayName,
  description,
  aiMetadata,
  type: import_pieces_framework5.TriggerStrategy.WEBHOOK,
  props: {
    webhookDescription: import_pieces_framework5.Property.ShortText({
      displayName: "Webhook Description",
      description: "Optional description stored with the webhook in Buttondown.",
      required: !1
    }),
    signingKey: import_pieces_framework5.Property.ShortText({
      displayName: "Webhook Signing Key",
      description: "Optional HMAC signing key to validate webhook requests.",
      required: !1
    })
  },
  sampleData,
  async onEnable(context) {
    let webhook = await buttondownRequest({
      auth: context.auth.secret_text,
      method: import_pieces_common7.HttpMethod.POST,
      path: "/webhooks",
      body: {
        url: context.webhookUrl,
        event_types: [eventType],
        description: context.propsValue.webhookDescription,
        signing_key: context.propsValue.signingKey
      }
    });
    await context.store.put("webhookId", webhook.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get("webhookId");
    webhookId && await buttondownRequest({
      auth: context.auth.secret_text,
      method: import_pieces_common7.HttpMethod.DELETE,
      path: `/webhooks/${webhookId}`
    });
  },
  async run(context) {
    let payload = context.payload.body;
    if ((payload == null ? void 0 : payload.event_type) !== eventType)
      return [];
    let enriched;
    return enrich && (enriched = await enrich({
      apiKey: context.auth.secret_text,
      payload
    })), [
      __spreadValues(__spreadValues({}, payload), enriched != null ? enriched : {})
    ];
  }
});

// ../ap/packages/pieces/community/buttondown/src/lib/triggers/new-subscriber.ts
var buttondownNewSubscriber = createButtondownWebhookTrigger({
  name: "buttondown_new_subscriber",
  displayName: "New Subscriber",
  description: "Triggers when a new subscriber is created.",
  aiMetadata: {
    description: "Fires when a subscriber is added to the Buttondown newsletter, regardless of confirmation status. Represents a newly created subscriber record; the payload is enriched with the full subscriber details."
  },
  eventType: "subscriber.created",
  sampleData: {
    event_type: "subscriber.created",
    data: {
      subscriber: "sub_123456789"
    }
  },
  enrich: async ({ apiKey, payload }) => {
    var _a;
    let subscriberData = (_a = payload.data) == null ? void 0 : _a.subscriber, subscriber;
    return typeof subscriberData == "string" ? subscriber = await buttondownRequest({
      auth: apiKey,
      method: import_pieces_common8.HttpMethod.GET,
      path: `/subscribers/${encodeURIComponent(subscriberData)}`
    }) : subscriberData && typeof subscriberData == "object" && (subscriber = subscriberData), {
      subscriber
    };
  }
});

// ../ap/packages/pieces/community/buttondown/src/lib/triggers/subscriber-confirmed.ts
var import_pieces_common9 = require("@activepieces/pieces-common");
var buttondownSubscriberConfirmed = createButtondownWebhookTrigger({
  name: "buttondown_subscriber_confirmed",
  displayName: "Subscriber Confirmed",
  description: "Triggers when a subscriber confirms their email.",
  aiMetadata: {
    description: "Fires when a subscriber completes double opt-in and confirms their email address in Buttondown. Represents a subscriber transitioning to confirmed status; the payload is enriched with the full subscriber details."
  },
  eventType: "subscriber.confirmed",
  sampleData: {
    event_type: "subscriber.confirmed",
    data: {
      subscriber: "sub_123456789"
    }
  },
  enrich: async ({ apiKey, payload }) => {
    var _a;
    let subscriberData = (_a = payload.data) == null ? void 0 : _a.subscriber, subscriber;
    return typeof subscriberData == "string" ? subscriber = await buttondownRequest({
      auth: apiKey,
      method: import_pieces_common9.HttpMethod.GET,
      path: `/subscribers/${encodeURIComponent(subscriberData)}`
    }) : subscriberData && typeof subscriberData == "object" && (subscriber = subscriberData), {
      subscriber
    };
  }
});

// ../ap/packages/pieces/community/buttondown/src/lib/triggers/email-sent.ts
var import_pieces_common10 = require("@activepieces/pieces-common");
var buttondownEmailSent = createButtondownWebhookTrigger({
  name: "buttondown_email_sent",
  displayName: "Email Sent",
  description: "Triggers when an email is sent from Buttondown.",
  aiMetadata: {
    description: "Fires when an email is dispatched to subscribers from Buttondown. Represents a sent newsletter/email; the payload is enriched with the full email details."
  },
  eventType: "email.sent",
  sampleData: {
    event_type: "email.sent",
    data: {
      email: "eml_123456789"
    }
  },
  enrich: async ({ apiKey, payload }) => {
    var _a;
    let emailData = (_a = payload.data) == null ? void 0 : _a.email, email;
    return typeof emailData == "string" ? email = await buttondownRequest({
      auth: apiKey,
      method: import_pieces_common10.HttpMethod.GET,
      path: `/emails/${encodeURIComponent(emailData)}`
    }) : emailData && typeof emailData == "object" && (email = emailData), {
      email
    };
  }
});

// ../ap/packages/pieces/community/buttondown/src/index.ts
var buttondown = (0, import_pieces_framework6.createPiece)({
  displayName: "Buttondown",
  description: "Automate your Buttondown newsletter workflows.",
  auth: buttondownAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/buttondown.png",
  authors: ["jacksbox-cassandra"],
  categories: [import_pieces_framework7.PieceCategory.MARKETING, import_pieces_framework7.PieceCategory.COMMUNICATION],
  actions: [createSubscriber, listSubscribers, sendEmail, customApiCall],
  triggers: [
    buttondownNewSubscriber,
    buttondownSubscriberConfirmed,
    buttondownEmailSent
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  buttondown
});
