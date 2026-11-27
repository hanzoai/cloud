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

// ../ap/packages/pieces/community/loops/src/index.ts
var index_exports = {};
__export(index_exports, {
  loops: () => loops
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/loops/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), LOOPS_BASE_URL = "https://app.loops.so/api/v1", loopsAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Your Loops API key. Generate one at [Settings \u2192 API](https://app.loops.so/settings?page=api) in your Loops dashboard.",
  required: !0,
  validate: async ({ auth }) => {
    var _a;
    try {
      return (await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${LOOPS_BASE_URL}/api-key`,
        headers: {
          Authorization: `Bearer ${auth}`,
          Accept: "application/json"
        }
      })).body.success ? { valid: !0 } : { valid: !1, error: "API key validation failed." };
    } catch (e) {
      let err = e;
      return ((_a = err == null ? void 0 : err.response) == null ? void 0 : _a.status) === 401 ? { valid: !1, error: "Invalid API key. Please check your Loops API key." } : { valid: !1, error: "Could not reach Loops API. Check your network." };
    }
  }
});

// ../ap/packages/pieces/community/loops/src/lib/actions/create-contact.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var RESERVED_FIELDS = /* @__PURE__ */ new Set([
  "email",
  "firstName",
  "lastName",
  "userId",
  "subscribed",
  "userGroup",
  "source"
]), createContact = (0, import_pieces_framework2.createAction)({
  name: "create_contact",
  displayName: "Create Contact",
  description: "Creates a new contact in Loops. Returns an error if a contact with the given email already exists.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new contact in Loops keyed on email, optionally setting name, internal user ID, marketing subscription status, user group, source, mailing-list memberships, and arbitrary custom properties. Use to add someone to the Loops audience; it errors if a contact with that email already exists (use the update/upsert path elsewhere for existing contacts). Not idempotent: a second call with the same email fails rather than no-op.",
    idempotent: !1
  },
  auth: loopsAuth,
  props: {
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email",
      description: "The email address of the contact.",
      required: !0
    }),
    firstName: import_pieces_framework2.Property.ShortText({
      displayName: "First Name",
      description: "The contact's first name.",
      required: !1
    }),
    lastName: import_pieces_framework2.Property.ShortText({
      displayName: "Last Name",
      description: "The contact's last name.",
      required: !1
    }),
    userId: import_pieces_framework2.Property.ShortText({
      displayName: "User ID",
      description: "Your internal user ID for this contact.",
      required: !1
    }),
    subscribed: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Subscribed to Marketing",
      description: 'Set subscription status. Leave as "No Change" to preserve existing status on update.',
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "No Change", value: "no_change" },
          { label: "Subscribe", value: "true" },
          { label: "Unsubscribe", value: "false" }
        ]
      }
    }),
    userGroup: import_pieces_framework2.Property.ShortText({
      displayName: "User Group",
      description: "A group/segment to assign to this contact.",
      required: !1
    }),
    source: import_pieces_framework2.Property.ShortText({
      displayName: "Source",
      description: 'Where this contact came from (e.g. "signup-form", "import").',
      required: !1
    }),
    mailingLists: import_pieces_framework2.Property.Object({
      displayName: "Mailing Lists",
      description: "Subscribe or unsubscribe this contact from mailing lists. Provide list IDs as keys with `true` (subscribe) or `false` (unsubscribe) as values.",
      required: !1
    }),
    customProperties: import_pieces_framework2.Property.Object({
      displayName: "Custom Properties",
      description: "Additional custom contact properties as key-value pairs. Reserved fields (email, firstName, lastName, userId, subscribed, userGroup, source) are ignored here.",
      required: !1
    })
  },
  async run(context) {
    let {
      email,
      firstName,
      lastName,
      userId,
      subscribed,
      userGroup,
      source,
      mailingLists,
      customProperties
    } = context.propsValue, body = { email };
    if (firstName && (body.firstName = firstName), lastName && (body.lastName = lastName), userId && (body.userId = userId), subscribed === "true" && (body.subscribed = !0), subscribed === "false" && (body.subscribed = !1), userGroup && (body.userGroup = userGroup), source && (body.source = source), mailingLists && typeof mailingLists == "object" && (body.mailingLists = mailingLists), customProperties && typeof customProperties == "object")
      for (let [key, value] of Object.entries(customProperties))
        RESERVED_FIELDS.has(key) || (body[key] = value);
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: `${LOOPS_BASE_URL}/contacts/create`,
      headers: {
        Authorization: `Bearer ${context.auth.secret_text}`,
        "Content-Type": "application/json",
        Accept: "application/json"
      },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/loops/src/lib/actions/send-event.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var sendEvent = (0, import_pieces_framework3.createAction)({
  name: "send_event",
  displayName: "Send Event",
  description: "Sends an event to Loops for a contact. Events can trigger email automations configured in your Loops dashboard.",
  audience: "both",
  aiMetadata: {
    description: "Sends a named event for a contact to Loops, which can trigger email automations configured in the dashboard; the contact is identified by email or internal user ID (at least one is required), and the call can also attach event properties and update mailing-list memberships or contact properties. Use to fire behavioral events like signup or purchase. The event name must match one set up in Loops. Not idempotent: each call records a new event occurrence.",
    idempotent: !1
  },
  auth: loopsAuth,
  props: {
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Email",
      description: "The email address of the contact to send the event for.",
      required: !1
    }),
    userId: import_pieces_framework3.Property.ShortText({
      displayName: "User ID",
      description: "Your internal user ID of the contact (used if email is not provided).",
      required: !1
    }),
    eventName: import_pieces_framework3.Property.ShortText({
      displayName: "Event Name",
      description: 'The name of the event to send (e.g. "signup", "purchase", "password-reset"). Must match an event set up in Loops.',
      required: !0
    }),
    eventProperties: import_pieces_framework3.Property.Object({
      displayName: "Event Properties",
      description: "Optional key-value pairs to attach to the event (used in email templates).",
      required: !1
    }),
    mailingLists: import_pieces_framework3.Property.Object({
      displayName: "Mailing Lists",
      description: "Subscribe or unsubscribe this contact from mailing lists. Provide list IDs as keys with `true` (subscribe) or `false` (unsubscribe) as values.",
      required: !1
    }),
    contactProperties: import_pieces_framework3.Property.Object({
      displayName: "Contact Properties",
      description: "Optional contact property updates to apply when the event is sent.",
      required: !1
    })
  },
  async run(context) {
    let { email, userId, eventName, eventProperties, mailingLists, contactProperties } = context.propsValue;
    if (!email && !userId)
      throw new Error('Either "Email" or "User ID" must be provided.');
    let body = { eventName };
    return email && (body.email = email), userId && (body.userId = userId), eventProperties && typeof eventProperties == "object" && (body.eventProperties = eventProperties), mailingLists && typeof mailingLists == "object" && (body.mailingLists = mailingLists), contactProperties && typeof contactProperties == "object" && Object.assign(body, contactProperties), (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: `${LOOPS_BASE_URL}/events/send`,
      headers: {
        Authorization: `Bearer ${context.auth.secret_text}`,
        "Content-Type": "application/json",
        Accept: "application/json"
      },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/loops/src/lib/actions/send-transactional-email.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var sendTransactionalEmail = (0, import_pieces_framework4.createAction)({
  name: "send_transactional_email",
  displayName: "Send Transactional Email",
  description: "Sends a transactional email to a contact using a pre-built template in Loops.",
  audience: "both",
  aiMetadata: {
    description: "Sends a transactional email to a single recipient using a pre-built Loops template, populating the template via data variables and optionally adding the recipient to the audience if they are not already a contact. Use for one-off, triggered emails like receipts or password resets. Requires the recipient email and the transactional template ID. Not idempotent: each call delivers another email.",
    idempotent: !1
  },
  auth: loopsAuth,
  props: {
    email: import_pieces_framework4.Property.ShortText({
      displayName: "To Email",
      description: "The recipient's email address.",
      required: !0
    }),
    transactionalId: import_pieces_framework4.Property.ShortText({
      displayName: "Transactional Email ID",
      description: "The ID of the transactional email template in Loops (find it in your Loops dashboard).",
      required: !0
    }),
    addToAudience: import_pieces_framework4.Property.Checkbox({
      displayName: "Add to Audience",
      description: "If true, the recipient will be added to your Loops audience if they are not already a contact.",
      required: !1,
      defaultValue: !1
    }),
    dataVariables: import_pieces_framework4.Property.Object({
      displayName: "Data Variables",
      description: 'Key-value pairs to populate dynamic variables in your email template (e.g. { "name": "Alice", "resetUrl": "https://..." }).',
      required: !1
    })
  },
  async run(context) {
    let { email, transactionalId, addToAudience, dataVariables } = context.propsValue, body = {
      email,
      transactionalId
    };
    return typeof addToAudience == "boolean" && (body.addToAudience = addToAudience), dataVariables && typeof dataVariables == "object" && (body.dataVariables = dataVariables), (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: `${LOOPS_BASE_URL}/transactional`,
      headers: {
        Authorization: `Bearer ${context.auth.secret_text}`,
        "Content-Type": "application/json",
        Accept: "application/json"
      },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/loops/src/lib/actions/find-contact.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var findContact = (0, import_pieces_framework5.createAction)({
  name: "find_contact",
  displayName: "Find Contact",
  description: "Finds a contact in Loops by their email address or user ID.",
  audience: "both",
  aiMetadata: {
    description: "Looks up a contact in Loops by a single search field, chosen via a Search By selector that switches between matching on email or on internal user ID (the API allows only one search parameter). Use to check whether a contact exists or to retrieve their details before acting. Read-only and idempotent.",
    idempotent: !0
  },
  auth: loopsAuth,
  props: {
    searchBy: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Search By",
      description: "The field to search by. The Loops API only allows one search parameter.",
      required: !0,
      options: {
        disabled: !1,
        options: [
          { label: "Email", value: "email" },
          { label: "User ID", value: "userId" }
        ]
      }
    }),
    searchValue: import_pieces_framework5.Property.ShortText({
      displayName: "Value",
      description: "The email address or user ID to search for.",
      required: !0
    })
  },
  async run(context) {
    let { searchBy, searchValue } = context.propsValue, params = new URLSearchParams();
    params.set(searchBy, searchValue);
    let data = (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.GET,
      url: `${LOOPS_BASE_URL}/contacts/find?${params.toString()}`,
      headers: {
        Authorization: `Bearer ${context.auth.secret_text}`,
        Accept: "application/json"
      }
    })).body;
    return {
      contacts: data,
      count: Array.isArray(data) ? data.length : 0
    };
  }
});

// ../ap/packages/pieces/community/loops/src/lib/actions/delete-contact.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var deleteContact = (0, import_pieces_framework6.createAction)({
  name: "delete_contact",
  displayName: "Delete Contact",
  description: "Permanently deletes a contact from Loops by their email address or user ID.",
  audience: "both",
  aiMetadata: {
    description: "Permanently deletes a contact from Loops, identified by email or internal user ID (at least one is required). Use to remove someone from the Loops audience. Idempotent: repeating the call for an already-deleted contact converges on the same removed state.",
    idempotent: !0
  },
  auth: loopsAuth,
  props: {
    email: import_pieces_framework6.Property.ShortText({
      displayName: "Email",
      description: "The email address of the contact to delete.",
      required: !1
    }),
    userId: import_pieces_framework6.Property.ShortText({
      displayName: "User ID",
      description: "Your internal user ID of the contact to delete.",
      required: !1
    })
  },
  async run(context) {
    let { email, userId } = context.propsValue;
    if (!email && !userId)
      throw new Error('At least one of "Email" or "User ID" must be provided.');
    let body = {};
    return email && (body.email = email), userId && (body.userId = userId), (await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.DELETE,
      url: `${LOOPS_BASE_URL}/contacts/delete`,
      headers: {
        Authorization: `Bearer ${context.auth.secret_text}`,
        "Content-Type": "application/json",
        Accept: "application/json"
      },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/loops/src/index.ts
var loops = (0, import_pieces_framework7.createPiece)({
  displayName: "Loops",
  description: "Loops is an email platform for sending beautiful transactional and marketing emails. Manage contacts, trigger automations with events, and send transactional emails from your workflows.",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/loops.png",
  authors: ["Harmatta"],
  categories: [import_pieces_framework8.PieceCategory.COMMUNICATION, import_pieces_framework8.PieceCategory.MARKETING],
  auth: loopsAuth,
  actions: [
    createContact,
    sendEvent,
    sendTransactionalEmail,
    findContact,
    deleteContact,
    (0, import_pieces_common7.createCustomApiCallAction)({
      baseUrl: () => LOOPS_BASE_URL,
      auth: loopsAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  loops
});
