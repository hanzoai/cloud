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

// ../ap/packages/pieces/community/plunk/src/index.ts
var index_exports = {};
__export(index_exports, {
  plunk: () => plunk
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_framework7 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/plunk/src/lib/actions/send-email.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/plunk/src/lib/auth.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework"), PLUNK_BASE_URL = "https://next-api.useplunk.com", plunkAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: "Find both API keys in your Plunk project's API settings.\n\n  - Secret API key (`sk_*`) is required for sending email and managing contacts.\n  - Public API key (`pk_*`) is optional.It is only used by the Track Event action.",
  required: !0,
  props: {
    secretKey: import_pieces_framework.PieceAuth.SecretText({
      displayName: "Secret API Key",
      description: "Used for /v1/send, /v1/contacts, and the Custom API Call action.",
      required: !0
    }),
    publicKey: import_pieces_framework.Property.ShortText({
      displayName: "Public API Key",
      description: "Required only for the Track Event action.",
      required: !1
    })
  },
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${PLUNK_BASE_URL}/contacts`,
        authentication: {
          type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
          token: auth.secretKey
        }
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Could not authenticate with Plunk. Verify the secret API key is correct."
      };
    }
  }
});

// ../ap/packages/pieces/community/plunk/src/lib/actions/send-email.ts
var sendTransactionalEmail = (0, import_pieces_framework2.createAction)({
  auth: plunkAuth,
  name: "send_transactional_email",
  displayName: "Send Transactional Email",
  description: "Send a transactional email through Plunk. Recipient can be a single address or a comma-separated list.",
  audience: "both",
  aiMetadata: {
    description: "Sends a one-off transactional email through Plunk to one recipient or a comma-separated list of recipients, with an HTML body and optional sender name, from address (must be a verified Plunk address), and reply-to. Choose this for direct/triggered emails rather than marketing campaigns. Not idempotent: each call dispatches a new email, and enabling the subscribed flag will create the recipient as a subscribed contact if they do not already exist.",
    idempotent: !1
  },
  props: {
    to: import_pieces_framework2.Property.ShortText({
      displayName: "To",
      description: "Recipient email address. For multiple recipients, separate with commas.",
      required: !0
    }),
    subject: import_pieces_framework2.Property.ShortText({
      displayName: "Subject",
      required: !0
    }),
    body: import_pieces_framework2.Property.LongText({
      displayName: "Body",
      description: "HTML body of the email.",
      required: !0
    }),
    name: import_pieces_framework2.Property.ShortText({
      displayName: "Sender Name",
      description: "Optional sender name. Falls back to the project default.",
      required: !1
    }),
    from: import_pieces_framework2.Property.ShortText({
      displayName: "Sender Email (From)",
      description: "Optional sender email. Must be a verified address on your Plunk project.",
      required: !1
    }),
    reply: import_pieces_framework2.Property.ShortText({
      displayName: "Reply-To",
      description: "Optional reply-to email.",
      required: !1
    }),
    subscribed: import_pieces_framework2.Property.Checkbox({
      displayName: "Mark Recipient as Subscribed",
      description: "When enabled, Plunk creates the recipient as a subscribed contact if they do not already exist.",
      required: !1
    })
  },
  async run(context) {
    let { to, subject, body, name, from, reply, subscribed } = context.propsValue, recipients = to.split(",").map((r) => r.trim()).filter((r) => r.length > 0), requestBody = {
      to: recipients.length === 1 ? recipients[0] : recipients,
      subject,
      body
    };
    return name && (requestBody.name = name), from && (requestBody.from = from), reply && (requestBody.reply = reply), subscribed === !0 && (requestBody.subscribed = !0), (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: `${PLUNK_BASE_URL}/v1/send`,
      body: requestBody,
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: context.auth.props.secretKey
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/plunk/src/lib/actions/track-event.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var trackEvent = (0, import_pieces_framework3.createAction)({
  auth: plunkAuth,
  name: "track_event",
  displayName: "Track Event",
  description: "Track a Plunk event for a contact. Plunk auto-creates the contact if they do not exist. Requires the public API key.",
  audience: "both",
  aiMetadata: {
    description: "Records a named event (e.g. order.completed) against a contact identified by email, optionally with arbitrary event metadata, to drive Plunk automations. Use it to signal that something happened for a contact. Requires the Plunk public API key in the connection, and auto-creates the contact if it does not exist. Not idempotent: each call appends another occurrence of the event.",
    idempotent: !1
  },
  props: {
    event: import_pieces_framework3.Property.ShortText({
      displayName: "Event Name",
      description: "Identifier of the event being tracked, e.g. `order.completed`.",
      required: !0
    }),
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Contact Email",
      description: "Email address of the contact the event belongs to.",
      required: !0
    }),
    subscribed: import_pieces_framework3.Property.Checkbox({
      displayName: "Mark Contact as Subscribed",
      description: "When enabled, contacts auto-created by this event will be marked as subscribed.",
      required: !1
    }),
    data: import_pieces_framework3.Property.Object({
      displayName: "Event Data",
      description: "Optional metadata stored alongside the event.",
      required: !1
    })
  },
  async run(context) {
    let publicKey = context.auth.props.publicKey;
    if (!publicKey || publicKey.trim().length === 0)
      throw new Error(
        "Track Event requires a Plunk public API key. Add the public key in the piece authentication."
      );
    let { event, email, subscribed, data } = context.propsValue, requestBody = {
      event,
      email
    };
    return subscribed === !0 && (requestBody.subscribed = !0), data && Object.keys(data).length > 0 && (requestBody.data = data), (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: `${PLUNK_BASE_URL}/v1/track`,
      body: requestBody,
      authentication: {
        type: import_pieces_common3.AuthenticationType.BEARER_TOKEN,
        token: publicKey
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/plunk/src/lib/actions/get-contacts.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var MAX_PAGES = 200, getContacts = (0, import_pieces_framework4.createAction)({
  auth: plunkAuth,
  name: "get_all_contacts",
  displayName: "Get All Contacts",
  description: "Retrieve every contact in your Plunk project.",
  audience: "both",
  aiMetadata: {
    description: "Fetches the full list of contacts in the Plunk project, automatically paging through all results. Use it to enumerate or scan contacts when you do not already have a specific contact ID. Takes no input. Read-only and idempotent.",
    idempotent: !0
  },
  props: {},
  async run(context) {
    var _a;
    let items = [], cursor;
    for (let page = 0; page < MAX_PAGES; page++) {
      let queryParams = { limit: "100" };
      cursor && (queryParams.cursor = cursor);
      let body = (await import_pieces_common4.httpClient.sendRequest({
        method: import_pieces_common4.HttpMethod.GET,
        url: `${PLUNK_BASE_URL}/contacts`,
        queryParams,
        authentication: {
          type: import_pieces_common4.AuthenticationType.BEARER_TOKEN,
          token: context.auth.props.secretKey
        }
      })).body;
      if ((_a = body.data) != null && _a.length && items.push(...body.data), !body.hasMore || !body.cursor)
        break;
      cursor = body.cursor;
    }
    return { items };
  }
});

// ../ap/packages/pieces/community/plunk/src/lib/actions/get-contact.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var getContact = (0, import_pieces_framework5.createAction)({
  auth: plunkAuth,
  name: "get_contact",
  displayName: "Get Contact",
  description: "Retrieve a single contact by ID.",
  audience: "both",
  aiMetadata: {
    description: "Looks up a single Plunk contact by its contact ID. Use it when you already have the exact contact ID; to discover IDs first, list contacts with Get All Contacts. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    contactId: import_pieces_framework5.Property.ShortText({
      displayName: "Contact ID",
      description: "The Plunk contact ID.",
      required: !0
    })
  },
  async run(context) {
    let { contactId } = context.propsValue;
    return (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.GET,
      url: `${PLUNK_BASE_URL}/contacts/${encodeURIComponent(contactId)}`,
      authentication: {
        type: import_pieces_common5.AuthenticationType.BEARER_TOKEN,
        token: context.auth.props.secretKey
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/plunk/src/index.ts
var plunk = (0, import_pieces_framework6.createPiece)({
  displayName: "Plunk",
  description: "Open-source email platform for transactional emails, marketing campaigns, and contact management.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/plunk.png",
  categories: [import_pieces_framework7.PieceCategory.MARKETING, import_pieces_framework7.PieceCategory.COMMUNICATION],
  authors: ["fran-mora"],
  auth: plunkAuth,
  actions: [
    sendTransactionalEmail,
    trackEvent,
    getContacts,
    getContact,
    (0, import_pieces_common6.createCustomApiCallAction)({
      baseUrl: () => PLUNK_BASE_URL,
      auth: plunkAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.props.secretKey}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  plunk
});
