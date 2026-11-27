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

// ../ap/packages/pieces/community/omnisend/src/index.ts
var index_exports = {};
__export(index_exports, {
  omnisend: () => omnisend
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/omnisend/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/omnisend/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), OMNISEND_API_BASE = "https://api.omnisend.com/api";
async function omnisendRequest(method, path, apiKey, body, queryParams) {
  return (await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${OMNISEND_API_BASE}${path}`,
    headers: {
      Authorization: `Omnisend-API-Key ${apiKey}`,
      "Omnisend-Version": "2026-03-15"
    },
    body,
    queryParams
  })).body;
}

// ../ap/packages/pieces/community/omnisend/src/lib/auth.ts
var authDescription = `
To obtain your Omnisend API key:
1. Log in to your [Omnisend account](https://app.omnisend.com).
2. Go to **Store settings \u2192 Integrations & API \u2192 API Keys**.
3. Click **Create API Key**, give it a name.
4. Copy the generated key and paste it here.
`, omnisendAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: authDescription,
  required: !0,
  validate: async ({ auth }) => {
    var _a, _b;
    try {
      return await omnisendRequest(import_pieces_common2.HttpMethod.GET, "/contacts", auth), { valid: !0 };
    } catch (e) {
      let err = e;
      return ((_a = err == null ? void 0 : err.response) == null ? void 0 : _a.status) === 401 || ((_b = err == null ? void 0 : err.response) == null ? void 0 : _b.status) === 403 ? {
        valid: !1,
        error: "Invalid API key. Please check your Omnisend API credentials."
      } : {
        valid: !1,
        error: "Unable to validate API key. Please try again."
      };
    }
  }
});

// ../ap/packages/pieces/community/omnisend/src/lib/actions/create-or-update-contact.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var createOrUpdateContactAction = (0, import_pieces_framework2.createAction)({
  auth: omnisendAuth,
  name: "create_or_update_contact",
  displayName: "Create or Update Contact",
  description: "Create a new contact or update an existing contact in Omnisend. If a contact with the same email already exists, it will be updated.",
  audience: "both",
  aiMetadata: {
    description: "Upsert a contact in Omnisend keyed on the email address: creates the contact if new, otherwise updates fields (name, location, tags) and the email subscription status. Use to add subscribers or sync contact data. Idempotent for an existing contact; re-running with the same input has no extra effect, but enabling Send Welcome Message can trigger a one-time welcome email on first creation.",
    idempotent: !0
  },
  props: {
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email",
      description: "Contact email address (used as identifier).",
      required: !0
    }),
    emailStatus: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Email Subscription Status",
      description: "The subscription status for the email channel.",
      required: !0,
      options: {
        options: [
          { label: "Subscribed", value: "subscribed" },
          { label: "Unsubscribed", value: "unsubscribed" },
          { label: "Non-subscribed", value: "nonSubscribed" }
        ]
      }
    }),
    firstName: import_pieces_framework2.Property.ShortText({
      displayName: "First Name",
      description: "Contact first name.",
      required: !1
    }),
    lastName: import_pieces_framework2.Property.ShortText({
      displayName: "Last Name",
      description: "Contact last name.",
      required: !1
    }),
    country: import_pieces_framework2.Property.ShortText({
      displayName: "Country",
      description: "Contact country name.",
      required: !1
    }),
    countryCode: import_pieces_framework2.Property.ShortText({
      displayName: "Country Code",
      description: "Two-letter country code (ISO 3166-1 alpha-2).",
      required: !1
    }),
    city: import_pieces_framework2.Property.ShortText({
      displayName: "City",
      description: "Contact city.",
      required: !1
    }),
    tags: import_pieces_framework2.Property.Array({
      displayName: "Tags",
      description: "Tags to assign to the contact.",
      required: !1
    }),
    sendWelcomeMessage: import_pieces_framework2.Property.Checkbox({
      displayName: "Send Welcome Message",
      description: "Send a welcome email to the contact upon creation.",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let {
      email,
      emailStatus,
      firstName,
      lastName,
      country,
      countryCode,
      city,
      tags,
      sendWelcomeMessage
    } = context.propsValue, body = {
      identifiers: [
        {
          type: "email",
          id: email,
          channels: {
            email: {
              status: emailStatus
            }
          }
        }
      ],
      sendWelcomeMessage: sendWelcomeMessage != null ? sendWelcomeMessage : !1
    };
    return firstName && (body.firstName = firstName), lastName && (body.lastName = lastName), country && (body.country = country), countryCode && (body.countryCode = countryCode), city && (body.city = city), tags && tags.length > 0 && (body.tags = tags), omnisendRequest(
      import_pieces_common3.HttpMethod.POST,
      "/contacts",
      context.auth.secret_text,
      body
    );
  }
});

// ../ap/packages/pieces/community/omnisend/src/lib/actions/get-contact.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var getContactAction = (0, import_pieces_framework3.createAction)({
  auth: omnisendAuth,
  name: "get_contact",
  displayName: "Get Contact",
  description: "Retrieve a contact by their contact ID.",
  audience: "both",
  aiMetadata: {
    description: "Fetch a single Omnisend contact by its contact ID. Use when you already have the contact ID and need its full profile; to find a contact by email instead, use List Contacts with an email filter. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    contactId: import_pieces_framework3.Property.ShortText({
      displayName: "Contact ID",
      required: !0
    })
  },
  async run(context) {
    let { contactId } = context.propsValue;
    return omnisendRequest(
      import_pieces_common4.HttpMethod.GET,
      `/contacts/${contactId}`,
      context.auth.secret_text
    );
  }
});

// ../ap/packages/pieces/community/omnisend/src/lib/actions/list-contacts.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var listContactsAction = (0, import_pieces_framework4.createAction)({
  auth: omnisendAuth,
  name: "list_contacts",
  displayName: "List Contacts",
  description: "Retrieve a paginated list of contacts from Omnisend.",
  audience: "both",
  aiMetadata: {
    description: "List contacts from Omnisend with limit/offset pagination. Leave the email filter empty to browse all contacts, or set it to look up a specific contact by email address (use this rather than Get Contact when you only have an email, not a contact ID). Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    limit: import_pieces_framework4.Property.Number({
      displayName: "Limit",
      description: "Number of contacts to return (max 250).",
      required: !1,
      defaultValue: 50
    }),
    offset: import_pieces_framework4.Property.Number({
      displayName: "Offset",
      description: "Number of contacts to skip for pagination.",
      required: !1,
      defaultValue: 0
    }),
    email: import_pieces_framework4.Property.ShortText({
      displayName: "Filter by Email",
      description: "Filter contacts by email address.",
      required: !1
    })
  },
  async run(context) {
    let { limit, offset, email } = context.propsValue, queryParams = {};
    return limit != null && (queryParams.limit = String(limit)), offset != null && (queryParams.offset = String(offset)), email && (queryParams.email = email), omnisendRequest(
      import_pieces_common5.HttpMethod.GET,
      "/contacts",
      context.auth.secret_text,
      void 0,
      queryParams
    );
  }
});

// ../ap/packages/pieces/community/omnisend/src/lib/actions/send-customer-event.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var sendCustomerEventAction = (0, import_pieces_framework5.createAction)({
  auth: omnisendAuth,
  name: "send_customer_event",
  displayName: "Send Customer Event",
  description: "Send a customer event to Omnisend. Events track customer behavior and trigger automations (e.g. welcome emails, abandoned cart reminders). Use predefined events or create custom ones.",
  audience: "both",
  aiMetadata: {
    description: 'Record a customer event for a contact (identified by email) in Omnisend, which can trigger automations such as welcome emails or abandoned-cart reminders. The event name may be a predefined Omnisend event (e.g. "placed order") or a custom name, with optional key-value properties. Not idempotent: each call appends a new event, so repeating it can re-fire automations.',
    idempotent: !1
  },
  props: {
    email: import_pieces_framework5.Property.ShortText({
      displayName: "Contact Email",
      description: "Email address of the contact to send the event for.",
      required: !0
    }),
    eventName: import_pieces_framework5.Property.ShortText({
      displayName: "Event Name",
      description: 'The name of the event. Use predefined names (e.g. "added product to cart", "placed order") or a custom event name.',
      required: !0
    }),
    properties: import_pieces_framework5.Property.Object({
      displayName: "Event Properties",
      description: 'Custom key-value properties to include with the event (e.g. {"orderId": "12345", "totalPrice": 99.99}).',
      required: !1
    })
  },
  async run(context) {
    let { email, eventName, properties } = context.propsValue, body = {
      contact: {
        email
      },
      eventName,
      origin: "api"
    };
    return properties && Object.keys(properties).length > 0 && (body.properties = properties), omnisendRequest(
      import_pieces_common6.HttpMethod.POST,
      "/events",
      context.auth.secret_text,
      body
    );
  }
});

// ../ap/packages/pieces/community/omnisend/src/lib/actions/list-campaigns.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var listCampaignsAction = (0, import_pieces_framework6.createAction)({
  auth: omnisendAuth,
  name: "list_campaigns",
  displayName: "List Campaigns",
  description: "Retrieve a list of email campaigns.",
  audience: "both",
  aiMetadata: {
    description: "List email campaigns from Omnisend with limit/offset pagination. Leave Status empty to return campaigns of all statuses, or set it to filter to a single state (draft, scheduled, sending, or sent). Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    limit: import_pieces_framework6.Property.Number({
      displayName: "Limit",
      description: "Number of campaigns to return.",
      required: !1,
      defaultValue: 25
    }),
    offset: import_pieces_framework6.Property.Number({
      displayName: "Offset",
      description: "Number of campaigns to skip for pagination.",
      required: !1,
      defaultValue: 0
    }),
    status: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Status",
      description: "Filter campaigns by status.",
      required: !1,
      options: {
        options: [
          { label: "Draft", value: "draft" },
          { label: "Scheduled", value: "scheduled" },
          { label: "Sending", value: "sending" },
          { label: "Sent", value: "sent" }
        ]
      }
    })
  },
  async run(context) {
    let { limit, offset, status } = context.propsValue, queryParams = {};
    return limit != null && (queryParams.limit = String(limit)), offset != null && (queryParams.offset = String(offset)), status && (queryParams.status = status), omnisendRequest(
      import_pieces_common7.HttpMethod.GET,
      "/campaigns",
      context.auth.secret_text,
      void 0,
      queryParams
    );
  }
});

// ../ap/packages/pieces/community/omnisend/src/index.ts
var omnisend = (0, import_pieces_framework7.createPiece)({
  displayName: "Omnisend",
  description: "All-in-one ecommerce marketing platform for email, SMS, and push notification campaigns with powerful automation and segmentation.",
  auth: omnisendAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/omnisend.png",
  categories: [import_pieces_framework8.PieceCategory.MARKETING, import_pieces_framework8.PieceCategory.SALES_AND_CRM],
  authors: ["Harmatta"],
  actions: [
    createOrUpdateContactAction,
    getContactAction,
    listContactsAction,
    sendCustomerEventAction,
    listCampaignsAction,
    (0, import_pieces_common8.createCustomApiCallAction)({
      auth: omnisendAuth,
      baseUrl: () => OMNISEND_API_BASE,
      authMapping: async (auth) => ({
        Authorization: `Omnisend-API-Key ${auth.secret_text}`,
        "Omnisend-Version": "2026-03-15"
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  omnisend
});
