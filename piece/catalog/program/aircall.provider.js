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

// ../ap/packages/pieces/community/aircall/src/index.ts
var index_exports = {};
__export(index_exports, {
  aircall: () => aircall
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework15 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/aircall/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/aircall/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.aircall.io/v1";
async function makeRequest(auth, method, path, body) {
  var _a;
  try {
    let response = await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      authentication: {
        type: import_pieces_common.AuthenticationType.BASIC,
        username: auth.username,
        password: auth.password
      },
      headers: {
        "Content-Type": "application/json"
      },
      body
    });
    if (response.status >= 200 && response.status < 300)
      return response.body;
    switch (response.status) {
      case 400:
        throw new Error(
          "Bad Request: The request could not be processed. Please check your input parameters."
        );
      case 403:
        throw new Error(
          "Forbidden: Invalid authentication credentials or insufficient permissions."
        );
      case 404:
        throw new Error(
          "Not Found: The requested resource could not be found."
        );
      case 405:
        throw new Error(
          "Method Not Allowed: The HTTP method is not supported for this endpoint."
        );
      case 500:
        throw new Error(
          "Internal Server Error: Aircall server encountered an unexpected condition."
        );
      default:
        throw new Error(
          `HTTP ${response.status}: ${((_a = response.body) == null ? void 0 : _a.message) || "Unknown error occurred"}`
        );
    }
  } catch (error) {
    throw error.message.includes("Bad Request") || error.message.includes("Forbidden") || error.message.includes("Not Found") || error.message.includes("Method Not Allowed") || error.message.includes("Internal Server Error") ? error : new Error(`Request failed: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/aircall/src/lib/common/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), aircallAuth = import_pieces_framework.PieceAuth.BasicAuth({
  description: "You can create API key by naviagting to **Integrations & API** menu.",
  required: !0,
  username: {
    displayName: "API ID"
  },
  password: {
    displayName: "API Token"
  },
  validate: async ({ auth }) => {
    try {
      return await makeRequest(auth, import_pieces_common2.HttpMethod.GET, "/ping"), { valid: !0 };
    } catch (error) {
      return { valid: !1, error: "Invalid Credentials" };
    }
  }
});

// ../ap/packages/pieces/community/aircall/src/lib/actions/comment-a-call.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/aircall/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common");
var callIdDropdown = import_pieces_framework2.Property.Dropdown({
  auth: aircallAuth,
  displayName: "Call",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your account first"
      };
    try {
      let response = await makeRequest(
        auth,
        import_pieces_common3.HttpMethod.GET,
        "/calls"
      ), { calls } = response;
      return {
        disabled: !1,
        options: calls.map((call) => ({
          label: `${call.raw_digits} - ${call.direction}`,
          value: call.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Error loading calls."
      };
    }
  }
}), numberIdDropdown = import_pieces_framework2.Property.Dropdown({
  auth: aircallAuth,
  displayName: "Number ID",
  required: !0,
  refreshers: ["auth"],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your account first"
      };
    try {
      return {
        disabled: !1,
        options: (await makeRequest(
          auth,
          import_pieces_common3.HttpMethod.GET,
          "/numbers"
        )).numbers.map((number) => ({
          label: number.name,
          value: number.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Error loading numbers"
      };
    }
  }
}), contactIdDropdown = import_pieces_framework2.Property.Dropdown({
  auth: aircallAuth,
  displayName: "Contact ID",
  description: "Select the contact to update",
  required: !0,
  refreshers: ["auth"],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your account first"
      };
    try {
      let response = await makeRequest(
        auth,
        import_pieces_common3.HttpMethod.GET,
        "/contacts"
      ), { contacts } = response;
      return {
        disabled: !1,
        options: contacts.map((contact) => ({
          label: `${contact.first_name || ""} ${contact.last_name || ""}` || contact.id,
          value: contact.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Error loading contacts"
      };
    }
  }
}), tagIdDropdown = import_pieces_framework2.Property.MultiSelectDropdown({
  auth: aircallAuth,
  displayName: "Tags",
  required: !1,
  refreshers: ["auth"],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your account first"
      };
    try {
      let response = await makeRequest(
        auth,
        import_pieces_common3.HttpMethod.GET,
        "/tags"
      ), { tags } = response;
      return {
        disabled: !1,
        options: tags.map((tag) => ({
          label: tag.name,
          value: tag.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Error loading tags"
      };
    }
  }
});

// ../ap/packages/pieces/community/aircall/src/lib/actions/comment-a-call.ts
var commentACall = (0, import_pieces_framework3.createAction)({
  auth: aircallAuth,
  name: "commentACall",
  classification: "WRITE",
  displayName: "Comment a Call",
  description: "Adds a comment (note) to a specific call.",
  audience: "both",
  aiMetadata: { description: "Appends a free-text comment (note) to an Aircall call identified by its call ID. Use to annotate a call with context after it happens. Not idempotent: each call adds a new comment, so repeating it duplicates the note.", idempotent: !1 },
  props: {
    callId: callIdDropdown,
    content: import_pieces_framework3.Property.LongText({
      displayName: "Comment Content",
      required: !0
    })
  },
  async run(context) {
    let { callId, content } = context.propsValue, auth = context.auth, response = await makeRequest(
      auth,
      import_pieces_common4.HttpMethod.POST,
      `/calls/${callId}/comments`,
      { content }
    );
    return {
      status: "success",
      message: `Comment added successfully to call ${callId}.`,
      comment: content,
      data: response
    };
  }
});

// ../ap/packages/pieces/community/aircall/src/lib/actions/create-a-contact.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common"), createAContact = (0, import_pieces_framework4.createAction)({
  auth: aircallAuth,
  name: "createAContact",
  classification: "WRITE",
  displayName: "Create a Contact",
  description: "Creates a new contact.",
  audience: "both",
  aiMetadata: { description: "Creates a new contact in Aircall with at least one phone number, plus optional name, company, emails, and free-text information. Use when adding a person not yet in the address book; to change an existing one use Update Contact instead. Not idempotent: each call creates a separate contact even with identical details.", idempotent: !1 },
  props: {
    phone_numbers: import_pieces_framework4.Property.Array({
      displayName: "Phone Numbers",
      required: !0,
      properties: {
        label: import_pieces_framework4.Property.ShortText({
          displayName: "Label",
          description: "Label for the phone number (e.g., Work, Mobile, Home).",
          required: !0
        }),
        value: import_pieces_framework4.Property.ShortText({
          displayName: "Phone Number",
          description: "Phone number with country code (e.g., +19001112222).",
          required: !0
        })
      }
    }),
    first_name: import_pieces_framework4.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    last_name: import_pieces_framework4.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    company_name: import_pieces_framework4.Property.ShortText({
      displayName: "Company Name",
      required: !1
    }),
    information: import_pieces_framework4.Property.LongText({
      displayName: "Information",
      description: "Additional information about the contact.",
      required: !1
    }),
    emails: import_pieces_framework4.Property.Array({
      displayName: "Email Addresses",
      description: "Array of email addresses (optional, max 20)",
      required: !1,
      properties: {
        label: import_pieces_framework4.Property.ShortText({
          displayName: "Label",
          description: "Label for the email (e.g., Office, Personal)",
          required: !0
        }),
        value: import_pieces_framework4.Property.ShortText({
          displayName: "Email Address",
          description: "Email address",
          required: !0
        })
      }
    })
  },
  async run(context) {
    let {
      first_name,
      last_name,
      company_name,
      information,
      phone_numbers,
      emails
    } = context.propsValue, requestBody = {
      phone_numbers
    };
    return first_name && (requestBody.first_name = first_name), last_name && (requestBody.last_name = last_name), company_name && (requestBody.company_name = company_name), information && (requestBody.information = information), emails && emails.length > 0 && (requestBody.emails = emails), await makeRequest(
      context.auth,
      import_pieces_common5.HttpMethod.POST,
      "/contacts",
      requestBody
    );
  }
});

// ../ap/packages/pieces/community/aircall/src/lib/actions/find-calls.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common");
var findCalls = (0, import_pieces_framework5.createAction)({
  auth: aircallAuth,
  name: "findCalls",
  classification: "SEARCH",
  displayName: "Find Call",
  description: "Finds specific call based on provided filter.",
  audience: "both",
  aiMetadata: { description: "Searches Aircall calls and returns the single most recent match, optionally narrowed by direction (inbound/outbound), phone number, and tag IDs; with no filters it returns the latest call overall. Use to look up a call before commenting, tagging, or fetching its full details. Read-only and idempotent.", idempotent: !0 },
  props: {
    direction: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Call Direction",
      description: "Filter by call direction",
      required: !1,
      options: {
        options: [
          { label: "Inbound", value: "inbound" },
          { label: "Outbound", value: "outbound" }
        ]
      }
    }),
    phone_number: import_pieces_framework5.Property.ShortText({
      displayName: "Phone Number",
      description: "The calling or receiving phone number of calls.",
      required: !1
    }),
    tags: tagIdDropdown
  },
  async run(context) {
    let { direction, phone_number, tags } = context.propsValue, queryParams = new URLSearchParams();
    queryParams.set("order", "desc"), queryParams.set("fetch_contact", "true"), queryParams.set("fetch_short_urls", "true"), direction && queryParams.set("direction", direction), phone_number && queryParams.set("phone_number", phone_number), tags && tags.length > 0 && tags.forEach((tagId) => queryParams.append("tags[]", tagId.toString()));
    let queryString = queryParams.toString(), path = `/calls/search${queryString ? `?${queryString}` : ""}`, response = await makeRequest(context.auth, import_pieces_common6.HttpMethod.GET, path), { calls } = response;
    return {
      found: calls.length > 0,
      data: calls.length > 0 ? calls[0] : {}
    };
  }
});

// ../ap/packages/pieces/community/aircall/src/lib/actions/find-contact.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common"), findContact = (0, import_pieces_framework6.createAction)({
  auth: aircallAuth,
  name: "findContact",
  classification: "READ",
  displayName: "Find Contact",
  description: "Finds contact based on phone or email.",
  audience: "both",
  aiMetadata: { description: "Searches Aircall contacts by phone number and/or email and returns the single best match. Use to resolve a contact ID before updating a contact or to check whether someone already exists. Provide at least one of phone number or email. Read-only and idempotent.", idempotent: !0 },
  props: {
    phone_number: import_pieces_framework6.Property.ShortText({
      displayName: "Phone Number",
      description: "Search by phone number (with country code, e.g., +1234567890).",
      required: !1
    }),
    email: import_pieces_framework6.Property.ShortText({
      displayName: "Email",
      description: "Search by email address.",
      required: !1
    })
  },
  async run(context) {
    let { phone_number, email } = context.propsValue, queryParams = new URLSearchParams();
    phone_number && queryParams.set("phone_number", phone_number), email && queryParams.set("email", email);
    let queryString = queryParams.toString(), path = `/contacts/search${queryString ? `?${queryString}` : ""}`, response = await makeRequest(context.auth, import_pieces_common7.HttpMethod.GET, path), { contacts } = response;
    return {
      found: contacts.length > 0,
      data: contacts.length > 0 ? contacts[0] : {}
    };
  }
});

// ../ap/packages/pieces/community/aircall/src/lib/actions/get-call.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var import_pieces_common8 = require("@activepieces/pieces-common");
var getCall = (0, import_pieces_framework7.createAction)({
  auth: aircallAuth,
  name: "getCall",
  classification: "READ",
  displayName: "Get Call",
  description: "Retrieves details about a specific call.",
  audience: "both",
  aiMetadata: { description: "Retrieves the full details of one Aircall call by its call ID, including the associated contact, short URLs, and call timeline. Use when you already have a call ID and need its complete record. Read-only and idempotent.", idempotent: !0 },
  props: {
    callId: callIdDropdown
  },
  async run(context) {
    let { callId } = context.propsValue, queryParams = new URLSearchParams();
    queryParams.set("fetch_contact", "true"), queryParams.set("fetch_short_urls", "true"), queryParams.set("fetch_call_timeline", "true");
    let queryString = queryParams.toString(), path = `/calls/${callId}${queryString ? `?${queryString}` : ""}`;
    return await makeRequest(context.auth, import_pieces_common8.HttpMethod.GET, path);
  }
});

// ../ap/packages/pieces/community/aircall/src/lib/actions/tag-a-call.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var import_pieces_common9 = require("@activepieces/pieces-common");
var tagACall = (0, import_pieces_framework8.createAction)({
  auth: aircallAuth,
  name: "tagACall",
  classification: "WRITE",
  displayName: "Tag a Call",
  description: "Add tags to a specific call.",
  audience: "both",
  aiMetadata: { description: "Applies one or more existing tags (by tag ID) to an Aircall call identified by its call ID. Use to categorize or label a call. The supplied tag set replaces the call's current tags, so passing the same tags again leaves the result unchanged; effectively idempotent for a given tag set.", idempotent: !0 },
  props: {
    callId: callIdDropdown,
    tags: tagIdDropdown
  },
  async run(context) {
    let { callId, tags } = context.propsValue, response = await makeRequest(
      context.auth,
      import_pieces_common9.HttpMethod.POST,
      `/calls/${callId}/tags`,
      { tags }
    );
    return {
      status: "success",
      message: `Tags added successfully to call ${callId}.`,
      data: response
    };
  }
});

// ../ap/packages/pieces/community/aircall/src/lib/actions/update-contact.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var import_pieces_common10 = require("@activepieces/pieces-common");
var updateContact = (0, import_pieces_framework9.createAction)({
  auth: aircallAuth,
  name: "updateContact",
  classification: "WRITE",
  displayName: "Update Contact",
  description: "Update an existing contact.",
  audience: "both",
  aiMetadata: { description: "Updates fields (first name, last name, company name, information) on an existing Aircall contact identified by its contact ID; only the fields you supply are changed. Use to edit a known contact rather than create one. Idempotent: applying the same values repeatedly leaves the contact in the same state.", idempotent: !0 },
  props: {
    contactId: contactIdDropdown,
    first_name: import_pieces_framework9.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    last_name: import_pieces_framework9.Property.ShortText({
      displayName: "Last Name",
      description: "Last name of the contact",
      required: !1
    }),
    company_name: import_pieces_framework9.Property.ShortText({
      displayName: "Company Name",
      required: !1
    }),
    information: import_pieces_framework9.Property.LongText({
      displayName: "Information",
      description: "Additional information about the contact",
      required: !1
    })
  },
  async run(context) {
    let { contactId, first_name, last_name, company_name, information } = context.propsValue, requestBody = {};
    return first_name && (requestBody.first_name = first_name), last_name && (requestBody.last_name = last_name), company_name && (requestBody.company_name = company_name), information && (requestBody.information = information), await makeRequest(
      context.auth,
      import_pieces_common10.HttpMethod.POST,
      `/contacts/${contactId}`,
      requestBody
    );
  }
});

// ../ap/packages/pieces/community/aircall/src/lib/triggers/call-ended.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var import_pieces_common11 = require("@activepieces/pieces-common"), TRIGGER_KEY = "trigger_call-ended", callEnded = (0, import_pieces_framework10.createTrigger)({
  auth: aircallAuth,
  name: "callEnded",
  classification: "READ",
  displayName: "Call Ended",
  description: "Triggers when a call ends.",
  aiMetadata: {
    description: "Fires when an Aircall call finishes (the call.ended webhook event), emitting the completed call with its direction, status, duration, participants, recording, comments, tags, and contact."
  },
  props: {},
  sampleData: {
    id: 12345,
    direction: "inbound",
    status: "answered",
    started_at: "2023-07-31T10:30:00Z",
    answered_at: "2023-07-31T10:30:05Z",
    ended_at: "2023-07-31T10:35:00Z",
    duration: 300,
    from: "+1234567890",
    to: "+0987654321",
    via: "+0987654321",
    recording: {
      id: 456,
      url: "https://api.aircall.io/v1/calls/12345/recording",
      filename: "call_12345_recording.mp3"
    },
    comments: [
      {
        id: 789,
        content: "Customer inquiry resolved",
        posted_at: "2023-07-31T10:36:00Z",
        posted_by: {
          id: 456,
          name: "John Smith",
          email: "john.smith@company.com"
        }
      }
    ],
    tags: [
      {
        id: 101,
        name: "Customer Support",
        color: "#ff0000"
      }
    ],
    contact: {
      id: 456,
      first_name: "Jane",
      last_name: "Doe",
      phone_numbers: [
        {
          id: 789,
          label: "Mobile",
          value: "+1234567890"
        }
      ]
    },
    user: {
      id: 123,
      name: "Agent Smith",
      email: "agent@company.com"
    }
  },
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let webhookUrl = context.webhookUrl, response = await makeRequest(
      context.auth,
      import_pieces_common11.HttpMethod.POST,
      "/webhooks",
      {
        url: webhookUrl,
        events: ["call.ended"]
      }
    ), { webhook } = response;
    await context.store.put(TRIGGER_KEY, webhook.webhook_id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(TRIGGER_KEY);
    webhookId && await makeRequest(
      context.auth,
      import_pieces_common11.HttpMethod.DELETE,
      `/webhooks/${webhookId}`
    );
  },
  async run(context) {
    let payload = context.payload.body;
    return payload.event === "call.ended" ? [payload.data] : [];
  },
  async test(context) {
    let response = await makeRequest(
      context.auth,
      import_pieces_common11.HttpMethod.GET,
      "/calls?order=desc&per_page=10"
    ), { calls } = response;
    return calls;
  }
});

// ../ap/packages/pieces/community/aircall/src/lib/triggers/new-contact.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var import_pieces_common12 = require("@activepieces/pieces-common"), TRIGGER_KEY2 = "trigger_new-contact", newContact = (0, import_pieces_framework11.createTrigger)({
  auth: aircallAuth,
  name: "newContact",
  classification: "READ",
  displayName: "New Contact",
  description: "Triggers when a new contact is created.",
  aiMetadata: {
    description: "Fires when a new contact is created in Aircall (the contact.created webhook event), emitting the newly created contact with its name, company, phone numbers, and emails."
  },
  props: {},
  sampleData: {
    id: 456,
    first_name: "John",
    last_name: "Doe",
    company_name: "Acme Corp",
    information: "Important client contact",
    is_shared: !0,
    created_at: "2023-07-31T10:30:00Z",
    updated_at: "2023-07-31T10:30:00Z",
    phone_numbers: [
      {
        id: 789,
        label: "Work",
        value: "+1234567890"
      },
      {
        id: 790,
        label: "Mobile",
        value: "+1234567891"
      }
    ],
    emails: [
      {
        id: 123,
        label: "Office",
        value: "john.doe@acme.com"
      }
    ]
  },
  type: import_pieces_framework11.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let webhookUrl = context.webhookUrl, response = await makeRequest(
      context.auth,
      import_pieces_common12.HttpMethod.POST,
      "/webhooks",
      {
        url: webhookUrl,
        events: ["contact.created"]
      }
    ), { webhook } = response;
    await context.store.put(TRIGGER_KEY2, webhook.webhook_id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(TRIGGER_KEY2);
    webhookId && await makeRequest(
      context.auth,
      import_pieces_common12.HttpMethod.DELETE,
      `/webhooks/${webhookId}`
    );
  },
  async run(context) {
    let payload = context.payload.body;
    return payload.event === "contact.created" ? [payload.data] : [];
  },
  async test(context) {
    let response = await makeRequest(
      context.auth,
      import_pieces_common12.HttpMethod.GET,
      "/contacts?order=desc&per_page=10"
    ), { contacts } = response;
    return contacts;
  }
});

// ../ap/packages/pieces/community/aircall/src/lib/triggers/new-note.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var import_pieces_common13 = require("@activepieces/pieces-common"), TRIGGER_KEY3 = "trigger_new-note", newNote = (0, import_pieces_framework12.createTrigger)({
  auth: aircallAuth,
  name: "newNote",
  classification: "READ",
  displayName: "New Note",
  description: "Triggers when a new note is added to a call.",
  aiMetadata: {
    description: "Fires when a comment (note) is added to an Aircall call (the call.commented webhook event), emitting the call with its comments and associated contact."
  },
  props: {},
  sampleData: {
    id: 12345,
    direction: "inbound",
    status: "answered",
    started_at: "2023-07-31T10:30:00Z",
    answered_at: "2023-07-31T10:30:05Z",
    ended_at: "2023-07-31T10:35:00Z",
    duration: 300,
    from: "+1234567890",
    to: "+0987654321",
    via: "+0987654321",
    comments: [
      {
        id: 789,
        content: "Customer called about billing inquiry",
        posted_at: "2023-07-31T10:36:00Z",
        posted_by: {
          id: 456,
          name: "John Smith",
          email: "john.smith@company.com"
        }
      }
    ],
    contact: {
      id: 456,
      first_name: "Jane",
      last_name: "Doe",
      phone_numbers: [
        {
          id: 789,
          label: "Mobile",
          value: "+1234567890"
        }
      ]
    }
  },
  type: import_pieces_framework12.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let webhookUrl = context.webhookUrl, response = await makeRequest(
      context.auth,
      import_pieces_common13.HttpMethod.POST,
      "/webhooks",
      {
        url: webhookUrl,
        events: ["call.commented"]
      }
    ), { webhook } = response;
    await context.store.put(TRIGGER_KEY3, webhook.webhook_id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(TRIGGER_KEY3);
    webhookId && await makeRequest(
      context.auth,
      import_pieces_common13.HttpMethod.DELETE,
      `/webhooks/${webhookId}`
    );
  },
  async run(context) {
    let payload = context.payload.body;
    return payload.event === "call.commented" ? [payload.data] : [];
  }
});

// ../ap/packages/pieces/community/aircall/src/lib/triggers/new-number-created.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var import_pieces_common14 = require("@activepieces/pieces-common"), TRIGGER_KEY4 = "trigger_new-number", newNumberCreated = (0, import_pieces_framework13.createTrigger)({
  auth: aircallAuth,
  name: "newNumberCreated",
  classification: "READ",
  displayName: "New Number Created",
  description: "Triggers when a new number is created.",
  aiMetadata: {
    description: "Fires when a new phone number is provisioned in Aircall (the number.created webhook event), emitting the number with its name, digits, country, time zone, and IVR messages."
  },
  props: {},
  sampleData: {
    id: 123,
    name: "Main Office Line",
    digits: "+1234567890",
    country: "US",
    time_zone: "America/New_York",
    created_at: "2023-07-31T10:30:00Z",
    open: !0,
    priority: 1,
    messages: {
      welcome: "Welcome to our company",
      waiting: "Please hold while we connect you",
      voicemail: "Please leave a message after the tone"
    }
  },
  type: import_pieces_framework13.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let webhookUrl = context.webhookUrl, response = await makeRequest(
      context.auth,
      import_pieces_common14.HttpMethod.POST,
      "/webhooks",
      {
        url: webhookUrl,
        events: ["number.created"]
      }
    ), { webhook } = response;
    await context.store.put(TRIGGER_KEY4, webhook.webhook_id);
  },
  async onDisable(context) {
    var _a;
    let webhookId = await ((_a = context.store) == null ? void 0 : _a.get(TRIGGER_KEY4));
    webhookId && await makeRequest(
      context.auth,
      import_pieces_common14.HttpMethod.DELETE,
      `/webhooks/${webhookId}`
    );
  },
  async run(context) {
    let payload = context.payload.body;
    return payload.event === "number.created" ? [payload.data] : [];
  }
});

// ../ap/packages/pieces/community/aircall/src/lib/triggers/new-sms.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var import_pieces_common15 = require("@activepieces/pieces-common"), TRIGGER_KEY5 = "trigger_new-sms", newSms = (0, import_pieces_framework14.createTrigger)({
  auth: aircallAuth,
  name: "newSms",
  classification: "READ",
  displayName: "New SMS",
  description: "Triggers when a new SMS message is received.",
  aiMetadata: {
    description: "Fires when an inbound SMS message is received in Aircall (the message.received webhook event), emitting the message body, direction, sender/recipient numbers, the Aircall number, and the matched contact."
  },
  props: {},
  sampleData: {
    id: 12345,
    direction: "inbound",
    body: "Hello, this is a test SMS message",
    sent_at: "2023-07-31T10:30:00Z",
    from: "+1234567890",
    to: "+0987654321",
    number: {
      id: 123,
      name: "Main Number",
      digits: "+0987654321"
    },
    contact: {
      id: 456,
      first_name: "John",
      last_name: "Doe",
      phone_numbers: [
        {
          id: 789,
          label: "Mobile",
          value: "+1234567890"
        }
      ]
    }
  },
  type: import_pieces_framework14.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let webhookUrl = context.webhookUrl, response = await makeRequest(
      context.auth,
      import_pieces_common15.HttpMethod.POST,
      "/webhooks",
      {
        url: webhookUrl,
        events: ["message.received"]
      }
    ), { webhook } = response;
    await context.store.put(TRIGGER_KEY5, webhook.webhook_id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(TRIGGER_KEY5);
    webhookId && await makeRequest(
      context.auth,
      import_pieces_common15.HttpMethod.DELETE,
      `/webhooks/${webhookId}`
    );
  },
  async run(context) {
    let payload = context.payload.body;
    return payload.event === "message.received" ? [payload.data] : [];
  }
});

// ../ap/packages/pieces/community/aircall/src/index.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework"), import_pieces_common16 = require("@activepieces/pieces-common");
var aircall = (0, import_pieces_framework15.createPiece)({
  displayName: "Aircall",
  auth: aircallAuth,
  minimumSupportedRelease: "0.36.1",
  categories: [import_pieces_framework16.PieceCategory.BUSINESS_INTELLIGENCE, import_pieces_framework16.PieceCategory.COMMUNICATION],
  logoUrl: "https://cdn.activepieces.com/pieces/aircall.png",
  authors: ["Sanket6652"],
  actions: [
    commentACall,
    createAContact,
    findCalls,
    findContact,
    getCall,
    tagACall,
    updateContact,
    (0, import_pieces_common16.createCustomApiCallAction)({
      auth: aircallAuth,
      baseUrl: () => BASE_URL,
      authMapping: async (auth) => ({
        Authorization: `Basic ${Buffer.from(`${auth.username}:${auth.password}`).toString(
          "base64"
        )}`
      })
    })
  ],
  triggers: [
    callEnded,
    newContact,
    newNote,
    newNumberCreated,
    newSms
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  aircall
});
