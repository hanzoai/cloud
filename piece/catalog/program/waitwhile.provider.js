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

// ../ap/packages/pieces/community/waitwhile/src/index.ts
var index_exports = {};
__export(index_exports, {
  waitwhile: () => waitwhile
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/waitwhile/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/waitwhile/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.waitwhile.com/v2";
async function makeRequest(api_key, method, path, body) {
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers: {
        "Content-Type": "application/json",
        apikey: api_key
      },
      body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/waitwhile/src/lib/common/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), waitwhileAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `
To get your Waitwhile API Key:

1. Go to the [Integrations Settings](https://v2.waitwhile.com/account/integrations) page in your Waitwhile account
2. Create a new API Key (or use an existing one)
3. Copy the API Key and paste it here
 `,
  required: !0,
  validate: async ({ auth }) => {
    if (auth)
      try {
        return await makeRequest(auth, import_pieces_common2.HttpMethod.GET, "/locations"), {
          valid: !0
        };
      } catch (e) {
        return {
          valid: !1,
          error: "Invalid Api Key"
        };
      }
    return {
      valid: !1,
      error: "Invalid Api Key"
    };
  }
});

// ../ap/packages/pieces/community/waitwhile/src/lib/actions/create-a-customer.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common"), createACustomer = (0, import_pieces_framework2.createAction)({
  auth: waitwhileAuth,
  name: "createACustomer",
  displayName: "Create a customer",
  description: "Create a new customer in Waitwhile",
  audience: "both",
  aiMetadata: {
    description: "Creates a new customer record in Waitwhile from the provided contact details (first name required; last name, phone, email, and tags optional). Choose this when adding a person to the system before booking or queuing them. Not idempotent: each call creates a separate customer even with identical input.",
    idempotent: !1
  },
  props: {
    firstName: import_pieces_framework2.Property.ShortText({
      displayName: "First Name",
      description: "Customer first name",
      required: !0
    }),
    lastName: import_pieces_framework2.Property.ShortText({
      displayName: "Last Name",
      description: "Customer last name",
      required: !1
    }),
    phone: import_pieces_framework2.Property.ShortText({
      displayName: "Phone",
      description: "Customer phone number",
      required: !1
    }),
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email",
      description: "Customer email address",
      required: !1
    }),
    tags: import_pieces_framework2.Property.Array({
      displayName: "Tags",
      description: "Tags associated with the customer",
      required: !1
    })
  },
  async run(context) {
    let { firstName, lastName, phone, email, tags } = context.propsValue, api_key = context.auth.secret_text, body = {
      firstName
    };
    return lastName && (body.lastName = lastName), phone && (body.phone = phone), email && (body.email = email), tags && (body.tags = tags), await makeRequest(
      api_key,
      import_pieces_common3.HttpMethod.POST,
      "/customers",
      body
    );
  }
});

// ../ap/packages/pieces/community/waitwhile/src/lib/actions/create-or-update-a-visit.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/waitwhile/src/lib/common/props.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common"), visitIdDropdown = import_pieces_framework3.Property.Dropdown({
  auth: waitwhileAuth,
  displayName: "Visit ID",
  description: "Select a Visit ID",
  required: !1,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: []
      };
    let api_key = auth.secret_text;
    return {
      disabled: !1,
      options: (await makeRequest(
        api_key,
        import_pieces_common4.HttpMethod.GET,
        "/visits"
      )).results.map((visit) => ({
        label: `${visit.name} - ${visit.notes}`,
        value: visit.id
      }))
    };
  }
}), customerIdDropdown = import_pieces_framework3.Property.Dropdown({
  auth: waitwhileAuth,
  displayName: "Customer ID",
  description: "Select a Customer ID",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: []
      };
    let api_key = auth.secret_text;
    return {
      disabled: !1,
      options: (await makeRequest(
        api_key,
        import_pieces_common4.HttpMethod.GET,
        "/customers"
      )).results.map((customer) => ({
        label: customer.name,
        value: customer.id
      }))
    };
  }
}), locationIdDropdown = import_pieces_framework3.Property.Dropdown({
  auth: waitwhileAuth,
  displayName: "Location",
  description: "Select a Location",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: []
      };
    let api_key = auth.secret_text;
    return {
      disabled: !1,
      options: (await makeRequest(
        api_key,
        import_pieces_common4.HttpMethod.GET,
        "/locations"
      )).results.map((location) => ({
        label: location.name,
        value: location.id
      }))
    };
  }
});

// ../ap/packages/pieces/community/waitwhile/src/lib/actions/create-or-update-a-visit.ts
var createOrUpdateAVisit = (0, import_pieces_framework4.createAction)({
  auth: waitwhileAuth,
  name: "createOrUpdateAVisit",
  displayName: "Create or Update a Visit",
  description: "Create a new visit or update an existing visit in Waitwhile",
  audience: "both",
  aiMetadata: {
    description: "Creates a new waitlist/appointment visit or updates an existing one in Waitwhile, optionally linking a customer and location and setting notes, party size, and start time. Use to add someone to a queue or modify their visit; supply an existing visit id to update, or omit it to create. Idempotent when keyed on a stable visit id (re-running with the same id and inputs converges to the same record).",
    idempotent: !0
  },
  props: {
    id: visitIdDropdown,
    customerId: customerIdDropdown,
    locationId: locationIdDropdown,
    notes: import_pieces_framework4.Property.LongText({
      displayName: "Notes",
      description: "Visit notes",
      required: !1
    }),
    partySize: import_pieces_framework4.Property.Number({
      displayName: "Party Size",
      description: "Number of people in the party",
      required: !1
    }),
    startDate: import_pieces_framework4.Property.ShortText({
      displayName: "Start Date",
      description: "Start date/time in ISO format (e.g., 2024-01-01T10:00:00Z)",
      required: !1
    })
  },
  async run(context) {
    let {
      id,
      customerId,
      locationId,
      notes,
      partySize,
      startDate
    } = context.propsValue, api_key = context.auth.secret_text, body = {
      id
    };
    return customerId && (body.customerId = customerId), locationId && (body.locationId = locationId), notes && (body.notes = notes), partySize != null && (body.partySize = partySize), startDate && (body.startDate = startDate), await makeRequest(
      api_key,
      import_pieces_common5.HttpMethod.POST,
      "/visits",
      body
    );
  }
});

// ../ap/packages/pieces/community/waitwhile/src/lib/actions/delete-a-visit.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common"), deleteAVisit = (0, import_pieces_framework5.createAction)({
  auth: waitwhileAuth,
  name: "deleteAVisit",
  displayName: "Delete a visit",
  description: "Permanently remove a visit from the database. The visit will no longer be retrievable and will not be factored into analytics.",
  audience: "both",
  aiMetadata: {
    description: "Permanently deletes a visit in Waitwhile by its visit id; the visit becomes unretrievable and is excluded from analytics. Use to purge a queue or appointment entry. Requires a valid visit id and is destructive: re-running on the same id will error once the visit is gone.",
    idempotent: !1
  },
  props: {
    visitId: visitIdDropdown
  },
  async run(context) {
    let { visitId } = context.propsValue, api_key = context.auth.secret_text;
    return await makeRequest(
      api_key,
      import_pieces_common6.HttpMethod.DELETE,
      `/visits/${visitId}`
    );
  }
});

// ../ap/packages/pieces/community/waitwhile/src/lib/actions/search-customers.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common"), searchCustomers = (0, import_pieces_framework6.createAction)({
  auth: waitwhileAuth,
  name: "searchCustomers",
  displayName: "Search customers",
  description: "Search for customers by name, phone, email, or identifier",
  audience: "both",
  aiMetadata: {
    description: "Searches Waitwhile customers by a prefix-match query (name, phone, email, or customer identifier) and optional filters for location, visit state, tag, and a date range, with limit/page pagination. Use to look up an existing customer or filter a customer list before acting; with no query or filters it returns the broader set. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    q: import_pieces_framework6.Property.ShortText({
      displayName: "Search Query",
      description: "Search query for name, phone, email, or customer identifier (prefix match)",
      required: !1
    }),
    locationId: import_pieces_framework6.Property.ShortText({
      displayName: "Location ID",
      description: "Filter by location identifier",
      required: !1
    }),
    state: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Visit State",
      description: "Filter by visit state",
      required: !1,
      options: {
        options: [
          { label: "Pending", value: "PENDING" },
          { label: "Draft", value: "DRAFT" },
          { label: "Waiting", value: "WAITING" },
          { label: "Cancelled", value: "BOOKED" },
          { label: "Serving", value: "SERVING" },
          { label: "Completed", value: "COMPLETE" }
        ]
      }
    }),
    tag: import_pieces_framework6.Property.ShortText({
      displayName: "Tag",
      description: "Filter by tag associated with visit",
      required: !1
    }),
    limit: import_pieces_framework6.Property.Number({
      displayName: "Limit",
      description: "Max number of results (1-100, defaults to 20)",
      required: !1
    }),
    page: import_pieces_framework6.Property.Number({
      displayName: "Page",
      description: "Page in results (defaults to 1)",
      required: !1
    }),
    fromDate: import_pieces_framework6.Property.ShortText({
      displayName: "From Date",
      description: "Start date in ISO-8601 format (e.g., 2024-01-01T00:00:00Z)",
      required: !1
    }),
    toDate: import_pieces_framework6.Property.ShortText({
      displayName: "To Date",
      description: "End date in ISO-8601 format (e.g., 2024-12-31T23:59:59Z)",
      required: !1
    })
  },
  async run(context) {
    let { q, locationId, state, tag, limit, page, fromDate, toDate } = context.propsValue, api_key = context.auth.secret_text, params = new URLSearchParams();
    q && params.append("q", q), locationId && params.append("locationId", locationId), state && params.append("state", state), tag && params.append("tag", tag), limit != null && params.append("limit", limit.toString()), page != null && params.append("page", page.toString()), fromDate && params.append("fromDate", fromDate), toDate && params.append("toDate", toDate);
    let queryString = params.toString(), path = queryString ? `/customers/search?${queryString}` : "/customers/search";
    return await makeRequest(api_key, import_pieces_common7.HttpMethod.GET, path);
  }
});

// ../ap/packages/pieces/community/waitwhile/src/lib/triggers/new-or-updated-message.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var import_pieces_common8 = require("@activepieces/pieces-common"), newOrUpdatedMessage = (0, import_pieces_framework7.createTrigger)({
  auth: waitwhileAuth,
  name: "newOrUpdatedMessage",
  displayName: "New Or Updated Message",
  description: "Triggers when an SMS or EMAIL notification is created or its delivery status changes",
  aiMetadata: {
    description: "Fires when a Waitwhile SMS or email notification is created or its delivery status changes (subscribes to message.created and message.updated webhook events), representing the outbound/inbound message and its current state."
  },
  props: {},
  sampleData: {
    id: "0T6229ytZ6Ud3AS2OCsE",
    created: "2022-05-16T20:41:36.627Z",
    accountId: "itiTKblwsrcJsAxO8kFz",
    type: "message.created",
    data: {
      id: "dIaq4Ek6xoxnveDBDocI",
      channel: "SMS",
      providerId: "TWILIO",
      isExternalProvider: !1,
      providerAccountId: null,
      recipient: "guest@email.com",
      recipientName: "Email Test",
      sender: "waitlist@v2-email.waitwhile.com",
      senderName: "My Waitlist",
      locationId: "dnVVslEybbg4SQmCw9kg",
      customerId: "vdeDJPsfRQppoAJclB1Kqd",
      externalCustomerId: null,
      visitId: "mmKbn1nxOcXG5PUZ80K3",
      userId: null,
      type: "WAITLIST-ALERT",
      body: `<p>Hi Email!</p>
<p>An Academic Advisor is now preparing your file and awaiting your return, please check in with the Secretary when you arrive.
If you cannot make it, please <a href="https://waitwhile.com/l/mywaitlist2/mmKbn1">cancel yourself</a>.</p>
<p>We look forward to seeing you soon!</p>
<p><em>My Waitlist</em></p>
`,
      externalId: null,
      isIncoming: !1,
      isAnonymized: !1,
      isRead: !0,
      state: "PENDING",
      updated: "2022-05-16T20:41:36.231Z",
      created: "2022-05-16T20:41:36.197Z",
      updatedBy: "RUlfUPgKD3NVd7YFIc8sYImlZIf1",
      createdBy: "RUlfUPgKD3NVd7YFIc8sYImlZIf1",
      subject: "It's your turn at My Waitlist",
      templateId: "guest-alert",
      substitutions: {
        showEmailConfirmationLink: !1,
        messageType: "WAITLIST-ALERT",
        clientHostname: "app.waitwhile.com",
        publicClientHostname: "waitwhile.com",
        locationId: "dnVVslEybbf5FQmCw9kg",
        businessName: "My Waitlist",
        waitlistId: "mywaitlist",
        shortName: "mywaitlist",
        showBranding: !0,
        replyTo: "waitlist@v2-email.waitwhile.com,email@email.com",
        publicId: "mmKbn1",
        isBooking: !1,
        qrCodeImageLink: "https://api.waitwhile.com/v2/public/visits/mywaitlist2/mmKbn1/qrcode",
        body: `<p>Hi Email!</p>
<p>An Academic Advisor is now preparing your file and awaiting your return, please check in with the Secretary when you arrive.
If you cannot make it, please <a href="https://waitwhile.com/l/mywaitlist2/mmKbn1">cancel yourself</a>.</p>
<p>We look forward to seeing you soon!</p>
<p><em>My Waitlist</em></p>
`,
        message: `<p>Hi Email!</p>
<p>An Academic Advisor is now preparing your file and awaiting your return, please check in with the Secretary when you arrive.
If you cannot make it, please <a href="https://waitwhile.com/l/mywaitlist2/mmKbn1">cancel yourself</a>.</p>
<p>We look forward to seeing you soon!</p>
<p><em>My Waitlist</em></p>
`,
        subject: "It's your turn at My Waitlist",
        sendingDomain: "v2-email.waitwhile.com"
      },
      numSegments: 1,
      accountId: "itiDKblwIrcJZ1AxO8kFz"
    }
  },
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let api_key = context.auth.secret_text, body = {
      url: context.webhookUrl,
      eventTypes: ["message.created", "message.updated"]
    }, response = await makeRequest(
      api_key,
      import_pieces_common8.HttpMethod.POST,
      "/webhooks",
      body
    );
    await context.store.put("webhookId", response.id);
  },
  async onDisable(context) {
    let api_key = context.auth.secret_text, webhookId = await context.store.get("webhookId");
    webhookId && await makeRequest(api_key, import_pieces_common8.HttpMethod.DELETE, `/webhooks/${webhookId}`);
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/waitwhile/src/lib/triggers/new-or-updated-visit.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var import_pieces_common9 = require("@activepieces/pieces-common"), newOrUpdatedVisit = (0, import_pieces_framework8.createTrigger)({
  auth: waitwhileAuth,
  name: "newOrUpdatedVisit",
  displayName: "New Or Updated Visit",
  description: "Triggers when a visit is created or updated",
  aiMetadata: {
    description: "Fires when a Waitwhile visit is created or updated (subscribes to visit.created and visit.updated webhook events), representing a waitlist or appointment entry and its current state, customer, location, and timing."
  },
  props: {},
  sampleData: {
    id: "0T6HwEtZs8ASfL52phLj",
    created: "2022-05-19T13:58:26.925Z",
    accountId: "itiDKblwIrcJsAxO8kGz",
    type: "visit.created",
    data: {
      id: "9QvKDkhrXIxNLdS9xMkf",
      locationId: "P3HCgTiw7IjFhxM2qmjt",
      state: "BOOKED",
      customerId: "CsgoBO4eR1iNyg6PF2PSkt",
      date: "2022-05-19T10:00",
      duration: 1800,
      firstName: "",
      lastName: "",
      name: "",
      externalCustomerId: "",
      notes: "",
      publicId: "9QvSDk",
      isAnonymized: !1,
      created: "2022-05-19T13:58:26.528Z",
      createdBy: "RUlfUPgKD3NVd7YFIc8sYImlZIf1",
      updated: "2022-05-19T13:58:26.528Z",
      updatedBy: "RUlfUPgKD3NVd7YFIc8sYImlZIf1",
      remoteIp: "",
      country: "US",
      region: "ny",
      city: "city name",
      isBlock: !1,
      source: "WEB-APP",
      locale: null,
      partySize: 1,
      resourceIds: [],
      serviceIds: [],
      dataFields: [],
      tags: [],
      bookingTime: "2022-05-19T13:58:26.528Z",
      waitlistTime: null,
      serveTime: null,
      completedTime: null,
      ticket: "A100",
      numVisits: 1
    }
  },
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let api_key = context.auth.secret_text, body = {
      url: context.webhookUrl,
      eventTypes: ["visit.created", "visit.updated"]
    }, response = await makeRequest(
      api_key,
      import_pieces_common9.HttpMethod.POST,
      "/webhooks",
      body
    );
    await context.store.put("webhookId", response.id);
  },
  async onDisable(context) {
    let api_key = context.auth.secret_text, webhookId = await context.store.get("webhookId");
    webhookId && await makeRequest(api_key, import_pieces_common9.HttpMethod.DELETE, `/webhooks/${webhookId}`);
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/waitwhile/src/index.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common"), waitwhile = (0, import_pieces_framework9.createPiece)({
  displayName: "WaitWhile",
  auth: waitwhileAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/waitwhile.png",
  description: "WaitWhile is an appointment scheduling and queue management platform.",
  categories: [import_pieces_framework10.PieceCategory.COMMERCE],
  authors: ["sanket-a11y"],
  actions: [
    createACustomer,
    createOrUpdateAVisit,
    deleteAVisit,
    searchCustomers,
    (0, import_pieces_common10.createCustomApiCallAction)({
      auth: waitwhileAuth,
      baseUrl: () => "https://api.waitwhile.com/v2",
      authMapping: async (auth) => ({
        apikey: auth.secret_text
      })
    })
  ],
  triggers: [newOrUpdatedMessage, newOrUpdatedVisit]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  waitwhile
});
