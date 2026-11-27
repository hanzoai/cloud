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

// ../ap/packages/pieces/community/oncehub/src/index.ts
var index_exports = {};
__export(index_exports, {
  oncehub: () => oncehub
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework13 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/oncehub/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/oncehub/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.oncehub.com/v2";
async function makeRequest(api_key, method, path, body) {
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers: {
        "API-Key": `${api_key}`,
        "Content-Type": "application/json"
      },
      body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/oncehub/src/lib/common/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), oncehubAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Oncehub API Key",
  required: !0,
  validate: async ({ auth }) => {
    try {
      return console.log("auth", auth), await makeRequest(auth, import_pieces_common2.HttpMethod.GET, "/test"), { valid: !0 };
    } catch (error) {
      return {
        valid: !1,
        error: "Invalid API credentials"
      };
    }
  }
});

// ../ap/packages/pieces/community/oncehub/src/index.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/oncehub/src/lib/actions/find-contact.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common"), findContact = (0, import_pieces_framework2.createAction)({
  auth: oncehubAuth,
  name: "findContact",
  displayName: "Find Contact",
  description: "Search for contacts in Oncehub by various criteria",
  audience: "both",
  aiMetadata: {
    description: "Look up existing OnceHub contacts filtered by email address. Use to check whether a contact already exists or to retrieve a contact before referencing it elsewhere. Read-only and repeatable; the email filter is required.",
    idempotent: !0
  },
  props: {
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email",
      description: "Filter contacts by email address",
      required: !0
    })
  },
  async run(context) {
    let { email } = context.propsValue, api_key = context.auth.secret_text, params = new URLSearchParams();
    email && params.append("email", email);
    let queryString = params.toString(), path = `/contacts${queryString ? `?${queryString}` : ""}`;
    return await makeRequest(api_key, import_pieces_common3.HttpMethod.GET, path);
  }
});

// ../ap/packages/pieces/community/oncehub/src/lib/actions/create-contact.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common"), createContact = (0, import_pieces_framework3.createAction)({
  auth: oncehubAuth,
  name: "createContact",
  displayName: "Create Contact",
  description: "Create a new contact in Oncehub. Either email or mobile_phone is required.",
  audience: "both",
  aiMetadata: {
    description: "Create a new contact record in OnceHub with name, contact details, company, and status fields. First name and last name are required, plus at least one of email or mobile phone. Not idempotent: each call creates another contact, so look up the contact first if you need to avoid duplicates.",
    idempotent: !1
  },
  props: {
    first_name: import_pieces_framework3.Property.ShortText({
      displayName: "First Name",
      description: "First name of the contact",
      required: !0
    }),
    last_name: import_pieces_framework3.Property.ShortText({
      displayName: "Last Name",
      description: "Last name of the contact",
      required: !0
    }),
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Email",
      description: "Email address of the contact (at least email or mobile_phone required)",
      required: !1
    }),
    mobile_phone: import_pieces_framework3.Property.ShortText({
      displayName: "Mobile Phone",
      description: "Mobile phone number in E.164 format (at least email or mobile_phone required)",
      required: !1
    }),
    phone: import_pieces_framework3.Property.ShortText({
      displayName: "Phone",
      description: "Phone number in E.164 format",
      required: !1
    }),
    company: import_pieces_framework3.Property.ShortText({
      displayName: "Company",
      description: "Company name",
      required: !1
    }),
    job_title: import_pieces_framework3.Property.ShortText({
      displayName: "Job Title",
      description: "Job title of the contact",
      required: !1
    }),
    street_address: import_pieces_framework3.Property.ShortText({
      displayName: "Street Address",
      description: "Street address",
      required: !1
    }),
    city: import_pieces_framework3.Property.ShortText({
      displayName: "City",
      description: "City of the contact",
      required: !1
    }),
    state: import_pieces_framework3.Property.ShortText({
      displayName: "State",
      description: "State or province",
      required: !1
    }),
    post_code: import_pieces_framework3.Property.ShortText({
      displayName: "Postal Code",
      description: "Postal code",
      required: !1
    }),
    country: import_pieces_framework3.Property.ShortText({
      displayName: "Country",
      description: "Country of the contact",
      required: !1
    }),
    company_size: import_pieces_framework3.Property.ShortText({
      displayName: "Company Size",
      description: "Size of the contact's company",
      required: !1
    }),
    employees: import_pieces_framework3.Property.Number({
      displayName: "Employees",
      description: "Number of employees",
      required: !1
    }),
    salutation: import_pieces_framework3.Property.ShortText({
      displayName: "Salutation",
      description: "Salutation (e.g., Mr., Ms., Dr.)",
      required: !1
    }),
    status: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Status",
      description: "Status of the contact",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Qualified", value: "Qualified" },
          { label: "Sales qualified", value: "Sales qualified" },
          { label: "Marketing qualified", value: "Marketing qualified" },
          { label: "Disqualified", value: "Disqualified" }
        ]
      }
    })
  },
  async run(context) {
    let {
      first_name,
      last_name,
      email,
      mobile_phone,
      phone,
      company,
      job_title,
      street_address,
      city,
      state,
      post_code,
      country,
      company_size,
      employees,
      salutation,
      status
    } = context.propsValue, api_key = context.auth.secret_text;
    if (!email && !mobile_phone)
      throw new Error("Either email or mobile_phone is required");
    let body = {
      first_name,
      last_name
    };
    return email && (body.email = email), mobile_phone && (body.mobile_phone = mobile_phone), phone && (body.phone = phone), company && (body.company = company), job_title && (body.job_title = job_title), street_address && (body.street_address = street_address), city && (body.city = city), state && (body.state = state), post_code && (body.post_code = post_code), country && (body.country = country), company_size && (body.company_size = company_size), employees !== void 0 && (body.employees = employees), salutation && (body.salutation = salutation), status && (body.status = status), await makeRequest(
      api_key,
      import_pieces_common4.HttpMethod.POST,
      "/contacts",
      body
    );
  }
});

// ../ap/packages/pieces/community/oncehub/src/index.ts
var import_pieces_common14 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/oncehub/src/lib/triggers/booking-canceled.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common"), bookingCanceled = (0, import_pieces_framework4.createTrigger)({
  auth: oncehubAuth,
  name: "bookingCanceled",
  displayName: "Booking Canceled",
  description: "Triggered when User or Customer cancels a booking",
  aiMetadata: {
    description: "Fires when a booking is canceled in OnceHub, by either the user (host) or the customer. The event carries the canceled booking with its contact, owner, and conversation details."
  },
  props: {},
  sampleData: {
    id: "EVNT-KN56U3YL7C",
    object: "event",
    creation_time: "2020-03-22T09:49:12Z",
    type: "booking.canceled",
    api_version: "v2",
    data: {
      object: "booking",
      id: "BKNG-J4FR05BKEWEX",
      tracking_id: "D36E0002",
      subject: "Live demo",
      status: "scheduled",
      in_trash: !1,
      creation_time: "2020-03-22T09:48:48Z",
      starting_time: "2020-03-22T04:30:00Z",
      customer_timezone: "America/New_York",
      last_updated_time: "2020-03-22T09:48:48Z",
      "...": "[Additional Properties Truncated]",
      owner: {
        first_name: "Andrea",
        last_name: "Hartie",
        email: "AndreaHartie@example.com",
        role_name: "Member",
        timezone: "America/Chicago",
        teams: ["TM-GCJU8DLBTPY1"]
      },
      contact: {
        object: "contact",
        id: "CTC-J4FR05BKEW",
        creation_time: "2020-03-22T09:48:48Z",
        last_updated_time: "2020-03-22T09:48:48Z",
        last_interacted_time: null,
        owner: "USR-FSD423423",
        status: "Qualified",
        city: "New York",
        company_size: "50-100",
        company: "Acme Inc",
        country: "United States",
        email: "carrie.customer@gmail.com",
        employees: 1,
        first_name: "Carrie",
        has_consent: !1,
        job_title: "Executive Assistant",
        last_name: "Customer",
        mobile_phone: "+12025550195",
        phone: "+12025550100",
        post_code: "10001",
        salutation: "Ms.",
        state: "New York",
        street_address: "123 Main Street",
        terms_of_sevice: !1,
        timezone: "America/New_York",
        custom_fields: []
      },
      conversation: {
        id: "CVR-022EAEA41C",
        object: "conversation",
        creation_time: "2021-07-13T12:28:24Z",
        initiated_by: "contact",
        last_updated_time: "2021-07-13T12:33:54Z",
        last_interacted_time: "2021-07-13T12:33:54Z",
        contact: "CTC-9QEG09XXYN",
        owner: "USR-GNSBE50D6A",
        status: "closed",
        bot: "BOT-62774A40FB",
        "...": "[Additional Properties Truncated]"
      },
      cancel_reschedule_url: "https://oncehub.com/m/BKNG-3KM0HY2BF9SL"
    }
  },
  type: import_pieces_framework4.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let api_key = context.auth.secret_text, { webhookUrl } = context, response = await makeRequest(api_key, import_pieces_common5.HttpMethod.POST, "/webhooks", {
      url: webhookUrl,
      name: `Booking Canceled Webhook - ${(/* @__PURE__ */ new Date()).getTime()}`,
      events: ["booking.canceled"]
    });
    await context.store.put("webhookId_bookingCanceled", response.id);
  },
  async onDisable(context) {
    let api_key = context.auth.secret_text, webhookId = await context.store.get(
      "webhookId_bookingCanceled"
    );
    webhookId && await makeRequest(api_key, import_pieces_common5.HttpMethod.DELETE, `/webhooks/${webhookId}`), await context.store.delete("webhookId_bookingCanceled");
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/oncehub/src/lib/triggers/booking-canceled-then-rescheduled.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common"), bookingCanceledThenRescheduled = (0, import_pieces_framework5.createTrigger)({
  auth: oncehubAuth,
  name: "bookingCanceledThenRescheduled",
  displayName: "Booking Canceled Then Rescheduled",
  description: "Triggered when Customer cancels a booking and then reschedules on a different booking page.",
  aiMetadata: {
    description: "Fires when a customer cancels a booking and then reschedules it on a different booking page in OnceHub. The event carries the rescheduled booking with its contact, owner, and conversation details. Distinct from a plain reschedule on the same booking page."
  },
  props: {},
  sampleData: {
    id: "EVNT-KN56U3YL7C",
    object: "event",
    creation_time: "2020-03-22T09:49:12Z",
    type: "booking.canceled_then_rescheduled",
    api_version: "v2",
    data: {
      object: "booking",
      id: "BKNG-J4FR05BKEWEX",
      tracking_id: "D36E0002",
      subject: "Live demo",
      status: "scheduled",
      in_trash: !1,
      creation_time: "2020-03-22T09:48:48Z",
      starting_time: "2020-03-22T04:30:00Z",
      customer_timezone: "America/New_York",
      last_updated_time: "2020-03-22T09:48:48Z",
      "...": "[Additional Properties Truncated]",
      owner: {
        first_name: "Andrea",
        last_name: "Hartie",
        email: "AndreaHartie@example.com",
        role_name: "Member",
        timezone: "America/Chicago",
        teams: ["TM-GCJU8DLBTPY1"]
      },
      contact: {
        object: "contact",
        id: "CTC-J4FR05BKEW",
        creation_time: "2020-03-22T09:48:48Z",
        last_updated_time: "2020-03-22T09:48:48Z",
        last_interacted_time: null,
        owner: "USR-FSD423423",
        status: "Qualified",
        city: "New York",
        company_size: "50-100",
        company: "Acme Inc",
        country: "United States",
        email: "carrie.customer@gmail.com",
        employees: 1,
        first_name: "Carrie",
        has_consent: !1,
        job_title: "Executive Assistant",
        last_name: "Customer",
        mobile_phone: "+12025550195",
        phone: "+12025550100",
        post_code: "10001",
        salutation: "Ms.",
        state: "New York",
        street_address: "123 Main Street",
        terms_of_sevice: !1,
        timezone: "America/New_York",
        custom_fields: []
      },
      conversation: {
        id: "CVR-022EAEA41C",
        object: "conversation",
        creation_time: "2021-07-13T12:28:24Z",
        initiated_by: "contact",
        last_updated_time: "2021-07-13T12:33:54Z",
        last_interacted_time: "2021-07-13T12:33:54Z",
        contact: "CTC-9QEG09XXYN",
        owner: "USR-GNSBE50D6A",
        status: "closed",
        bot: "BOT-62774A40FB",
        "...": "[Additional Properties Truncated]"
      },
      cancel_reschedule_url: "https://oncehub.com/m/BKNG-3KM0HY2BF9SL"
    }
  },
  type: import_pieces_framework5.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let api_key = context.auth.secret_text, { webhookUrl } = context, response = await makeRequest(api_key, import_pieces_common6.HttpMethod.POST, "/webhooks", {
      url: webhookUrl,
      name: `Booking Canceled Then Rescheduled Webhook - ${(/* @__PURE__ */ new Date()).getTime()}`,
      events: ["booking.canceled_then_rescheduled"]
    });
    await context.store.put("webhookId_canceled_then_rescheduled", response.id);
  },
  async onDisable(context) {
    let api_key = context.auth.secret_text, webhookId = await context.store.get(
      "webhookId_canceled_then_rescheduled"
    );
    webhookId && await makeRequest(api_key, import_pieces_common6.HttpMethod.DELETE, `/webhooks/${webhookId}`), await context.store.delete("webhookId_canceled_then_rescheduled");
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/oncehub/src/lib/triggers/booking-completed.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common"), bookingCompleted = (0, import_pieces_framework6.createTrigger)({
  auth: oncehubAuth,
  name: "bookingCompleted",
  displayName: "Booking Completed",
  description: "Triggered when booking end time has passed.",
  aiMetadata: {
    description: "Fires when a booking has finished, i.e. its scheduled end time has passed in OnceHub. Use to run follow-up actions after a meeting concludes."
  },
  props: {},
  sampleData: {},
  type: import_pieces_framework6.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let api_key = context.auth.secret_text, { webhookUrl } = context, response = await makeRequest(api_key, import_pieces_common7.HttpMethod.POST, "/webhooks", {
      url: webhookUrl,
      name: `Booking Completed Webhook - ${(/* @__PURE__ */ new Date()).getTime()}`,
      events: ["booking.completed"]
    });
    await context.store.put("webhookId_bookingCompleted", response.id);
  },
  async onDisable(context) {
    let api_key = context.auth.secret_text, webhookId = await context.store.get(
      "webhookId_bookingCompleted"
    );
    webhookId && await makeRequest(api_key, import_pieces_common7.HttpMethod.DELETE, `/webhooks/${webhookId}`), await context.store.delete("webhookId_bookingCompleted");
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/oncehub/src/lib/triggers/booking-no-show.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var import_pieces_common8 = require("@activepieces/pieces-common");
var bookingNoshow = (0, import_pieces_framework7.createTrigger)({
  auth: oncehubAuth,
  name: "bookingNoshow",
  displayName: "Booking No-Show",
  description: "Triggered when User sets the completed booking to No-show",
  aiMetadata: {
    description: "Fires when a user marks a completed booking as a no-show in OnceHub, indicating the customer did not attend. The event carries the affected booking with its contact and owner details."
  },
  props: {},
  sampleData: {
    id: "EVNT-KN56U3YL7C",
    object: "event",
    creation_time: "2020-03-22T09:49:12Z",
    type: "booking.no_show",
    api_version: "v2",
    data: {
      object: "booking",
      id: "BKNG-J4FR05BKEWEX",
      tracking_id: "D36E0002",
      subject: "Live demo",
      status: "scheduled",
      in_trash: !1,
      creation_time: "2020-03-22T09:48:48Z",
      starting_time: "2020-03-22T04:30:00Z",
      customer_timezone: "America/New_York",
      last_updated_time: "2020-03-22T09:48:48Z",
      "...": "[Additional Properties Truncated]",
      owner: {
        first_name: "Andrea",
        last_name: "Hartie",
        email: "AndreaHartie@example.com",
        role_name: "Member",
        timezone: "America/Chicago",
        teams: ["TM-GCJU8DLBTPY1"]
      },
      contact: {
        object: "contact",
        id: "CTC-J4FR05BKEW",
        creation_time: "2020-03-22T09:48:48Z",
        last_updated_time: "2020-03-22T09:48:48Z",
        last_interacted_time: null,
        owner: "USR-FSD423423",
        status: "Qualified",
        city: "New York",
        company_size: "50-100",
        company: "Acme Inc",
        country: "United States",
        email: "carrie.customer@gmail.com",
        employees: 1,
        first_name: "Carrie",
        has_consent: !1,
        job_title: "Executive Assistant",
        last_name: "Customer",
        mobile_phone: "+12025550195",
        phone: "+12025550100",
        post_code: "10001",
        salutation: "Ms.",
        state: "New York",
        street_address: "123 Main Street",
        terms_of_sevice: !1,
        timezone: "America/New_York",
        custom_fields: []
      },
      conversation: {
        id: "CVR-022EAEA41C",
        object: "conversation",
        creation_time: "2021-07-13T12:28:24Z",
        initiated_by: "contact",
        last_updated_time: "2021-07-13T12:33:54Z",
        last_interacted_time: "2021-07-13T12:33:54Z",
        contact: "CTC-9QEG09XXYN",
        owner: "USR-GNSBE50D6A",
        status: "closed",
        bot: "BOT-62774A40FB",
        "...": "[Additional Properties Truncated]"
      },
      cancel_reschedule_url: "https://oncehub.com/m/BKNG-3KM0HY2BF9SL"
    }
  },
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let api_key = context.auth.secret_text, { webhookUrl } = context, response = await makeRequest(api_key, import_pieces_common8.HttpMethod.POST, "/webhooks", {
      url: webhookUrl,
      name: `Booking No-Show Webhook - ${(/* @__PURE__ */ new Date()).getTime()}`,
      events: ["booking.no_show"]
    });
    await context.store.put("webhookId_bookingNoshow", response.id);
  },
  async onDisable(context) {
    let api_key = context.auth.secret_text, webhookId = await context.store.get(
      "webhookId_bookingNoshow"
    );
    webhookId && await makeRequest(api_key, import_pieces_common8.HttpMethod.DELETE, `/webhooks/${webhookId}`), await context.store.delete("webhookId_bookingNoshow");
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/oncehub/src/lib/triggers/booking-rescheduled.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var import_pieces_common9 = require("@activepieces/pieces-common"), bookingRescheduled = (0, import_pieces_framework8.createTrigger)({
  auth: oncehubAuth,
  name: "bookingRescheduled",
  displayName: "Booking Rescheduled",
  description: "Triggered when Customer reschedules a booking on the same booking page and Customer reschedules a booking following a request from the User to reschedule",
  aiMetadata: {
    description: "Fires when a customer reschedules a booking on the same booking page, including reschedules made in response to a user request. The event carries the rescheduled booking with its contact and owner details. For reschedules onto a different booking page, use the Booking Canceled Then Rescheduled trigger."
  },
  props: {},
  sampleData: {
    id: "EVNT-KN56U3YL7C",
    object: "event",
    creation_time: "2020-03-22T09:49:12Z",
    type: "booking.rescheduled",
    api_version: "v2",
    data: {
      object: "booking",
      id: "BKNG-J4FR05BKEWEX",
      tracking_id: "D36E0002",
      subject: "Live demo",
      status: "scheduled",
      in_trash: !1,
      creation_time: "2020-03-22T09:48:48Z",
      starting_time: "2020-03-22T04:30:00Z",
      customer_timezone: "America/New_York",
      last_updated_time: "2020-03-22T09:48:48Z",
      "...": "[Additional Properties Truncated]",
      owner: {
        first_name: "Andrea",
        last_name: "Hartie",
        email: "AndreaHartie@example.com",
        role_name: "Member",
        timezone: "America/Chicago",
        teams: ["TM-GCJU8DLBTPY1"]
      },
      contact: {
        object: "contact",
        id: "CTC-J4FR05BKEW",
        creation_time: "2020-03-22T09:48:48Z",
        last_updated_time: "2020-03-22T09:48:48Z",
        last_interacted_time: null,
        owner: "USR-FSD423423",
        status: "Qualified",
        city: "New York",
        company_size: "50-100",
        company: "Acme Inc",
        country: "United States",
        email: "carrie.customer@gmail.com",
        employees: 1,
        first_name: "Carrie",
        has_consent: !1,
        job_title: "Executive Assistant",
        last_name: "Customer",
        mobile_phone: "+12025550195",
        phone: "+12025550100",
        post_code: "10001",
        salutation: "Ms.",
        state: "New York",
        street_address: "123 Main Street",
        terms_of_sevice: !1,
        timezone: "America/New_York",
        custom_fields: []
      },
      conversation: {
        id: "CVR-022EAEA41C",
        object: "conversation",
        creation_time: "2021-07-13T12:28:24Z",
        initiated_by: "contact",
        last_updated_time: "2021-07-13T12:33:54Z",
        last_interacted_time: "2021-07-13T12:33:54Z",
        contact: "CTC-9QEG09XXYN",
        owner: "USR-GNSBE50D6A",
        status: "closed",
        bot: "BOT-62774A40FB",
        "...": "[Additional Properties Truncated]"
      },
      cancel_reschedule_url: "https://oncehub.com/m/BKNG-3KM0HY2BF9SL"
    }
  },
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let api_key = context.auth.secret_text, { webhookUrl } = context, response = await makeRequest(api_key, import_pieces_common9.HttpMethod.POST, "/webhooks", {
      url: webhookUrl,
      name: `Booking Rescheduled Webhook - ${(/* @__PURE__ */ new Date()).getTime()}`,
      events: ["booking.rescheduled"]
    });
    await context.store.put("webhookId_bookingRescheduled", response.id);
  },
  async onDisable(context) {
    let api_key = context.auth.secret_text, webhookId = await context.store.get(
      "webhookId_bookingRescheduled"
    );
    webhookId && await makeRequest(api_key, import_pieces_common9.HttpMethod.DELETE, `/webhooks/${webhookId}`), await context.store.delete("webhookId_bookingRescheduled");
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/oncehub/src/lib/triggers/booking-scheduled.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var import_pieces_common10 = require("@activepieces/pieces-common"), bookingScheduled = (0, import_pieces_framework9.createTrigger)({
  auth: oncehubAuth,
  name: "bookingScheduled",
  displayName: "Booking Scheduled",
  description: "Triggered when Customer schedules a booking and when User approves a booking requested by a Customer",
  aiMetadata: {
    description: "Fires when a new booking is scheduled in OnceHub, either when a customer books directly or when a user approves a booking the customer requested. The event carries the new booking with its contact and owner details."
  },
  props: {},
  sampleData: {
    id: "EVNT-KN56U3YL7C",
    object: "event",
    creation_time: "2020-03-22T09:49:12Z",
    type: "booking.scheduled",
    api_version: "v2",
    data: {
      object: "booking",
      id: "BKNG-J4FR05BKEWEX",
      tracking_id: "D36E0002",
      subject: "Live demo",
      status: "scheduled",
      in_trash: !1,
      creation_time: "2020-03-22T09:48:48Z",
      starting_time: "2020-03-22T04:30:00Z",
      customer_timezone: "America/New_York",
      last_updated_time: "2020-03-22T09:48:48Z",
      "...": "[Additional Properties Truncated]",
      owner: {
        first_name: "Andrea",
        last_name: "Hartie",
        email: "AndreaHartie@example.com",
        role_name: "Member",
        timezone: "America/Chicago",
        teams: ["TM-GCJU8DLBTPY1"]
      },
      contact: {
        object: "contact",
        id: "CTC-J4FR05BKEW",
        creation_time: "2020-03-22T09:48:48Z",
        last_updated_time: "2020-03-22T09:48:48Z",
        last_interacted_time: null,
        owner: "USR-FSD423423",
        status: "Qualified",
        city: "New York",
        company_size: "50-100",
        company: "Acme Inc",
        country: "United States",
        email: "carrie.customer@gmail.com",
        employees: 1,
        first_name: "Carrie",
        has_consent: !1,
        job_title: "Executive Assistant",
        last_name: "Customer",
        mobile_phone: "+12025550195",
        phone: "+12025550100",
        post_code: "10001",
        salutation: "Ms.",
        state: "New York",
        street_address: "123 Main Street",
        terms_of_sevice: !1,
        timezone: "America/New_York",
        custom_fields: []
      },
      conversation: {
        id: "CVR-022EAEA41C",
        object: "conversation",
        creation_time: "2021-07-13T12:28:24Z",
        initiated_by: "contact",
        last_updated_time: "2021-07-13T12:33:54Z",
        last_interacted_time: "2021-07-13T12:33:54Z",
        contact: "CTC-9QEG09XXYN",
        owner: "USR-GNSBE50D6A",
        status: "closed",
        bot: "BOT-62774A40FB",
        "...": "[Additional Properties Truncated]"
      },
      cancel_reschedule_url: "https://oncehub.com/m/BKNG-3KM0HY2BF9SL"
    }
  },
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let api_key = context.auth.secret_text, { webhookUrl } = context, response = await makeRequest(api_key, import_pieces_common10.HttpMethod.POST, "/webhooks", {
      url: webhookUrl,
      name: `Booking Scheduled Webhook - ${(/* @__PURE__ */ new Date()).getTime()}`,
      events: ["booking.scheduled"]
    });
    await context.store.put("webhookId_bookingScheduled", response.id);
  },
  async onDisable(context) {
    let api_key = context.auth.secret_text, webhookId = await context.store.get(
      "webhookId_bookingScheduled"
    );
    webhookId && await makeRequest(api_key, import_pieces_common10.HttpMethod.DELETE, `/webhooks/${webhookId}`), await context.store.delete("webhookId_bookingScheduled");
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/oncehub/src/lib/triggers/conversation-abandoned.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var import_pieces_common11 = require("@activepieces/pieces-common"), conversationAbandoned = (0, import_pieces_framework10.createTrigger)({
  auth: oncehubAuth,
  name: "conversationAbandoned",
  displayName: "Conversation Abandoned",
  description: "Triggered when website visitor stops interacting with a bot for more than 10 minutes.",
  aiMetadata: {
    description: "Fires when a website visitor stops interacting with a OnceHub chatbot for more than 10 minutes, marking the conversation as abandoned. The event carries the conversation with its contact and owner details."
  },
  props: {},
  sampleData: {
    id: "EVNT-KN56U3YL7C",
    object: "event",
    creation_time: "2020-03-22T09:49:12Z",
    type: "conversation.abandoned",
    api_version: "v2",
    data: {
      id: "CVR-022EAEA41C",
      object: "conversation",
      creation_time: "2021-07-13T12:28:24Z",
      initiated_by: "contact",
      last_updated_time: "2021-07-13T12:33:54Z",
      last_interacted_time: "2021-07-13T12:33:54Z",
      contact: {
        object: "contact",
        id: "CTC-J4FR05BKEW",
        creation_time: "2020-03-22T09:48:48Z",
        last_updated_time: "2020-03-22T09:48:48Z",
        last_interacted_time: null,
        owner: "USR-FSD423423",
        status: "Qualified",
        city: "New York",
        company_size: "50-100",
        company: "Acme Inc",
        country: "United States",
        email: "carrie.customer@gmail.com",
        employees: 1,
        first_name: "Carrie",
        has_consent: !1,
        job_title: "Executive Assistant",
        last_name: "Customer",
        mobile_phone: "+12025550195",
        phone: "+12025550100",
        post_code: "10001",
        salutation: "Ms.",
        state: "New York",
        street_address: "123 Main Street",
        terms_of_sevice: !1,
        timezone: "America/New_York",
        custom_fields: []
      },
      owner: {
        first_name: "Andrea",
        last_name: "Hartie",
        email: "AndreaHartie@example.com",
        role_name: "Member",
        timezone: "America/Chicago",
        teams: ["TM-GCJU8DLBTPY1"]
      },
      status: "closed",
      bot: "BOT-62774A40FB",
      "...": "[Additional Properties Truncated]"
    }
  },
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let api_key = context.auth.secret_text, { webhookUrl } = context, response = await makeRequest(api_key, import_pieces_common11.HttpMethod.POST, "/webhooks", {
      url: webhookUrl,
      name: `conversation abandoned Webhook - ${(/* @__PURE__ */ new Date()).getTime()}`,
      events: ["conversation.abandoned"]
    });
    await context.store.put("webhookId_conversationAbandoned", response.id);
  },
  async onDisable(context) {
    let api_key = context.auth.secret_text, webhookId = await context.store.get(
      "webhookId_conversationAbandoned"
    );
    webhookId && await makeRequest(api_key, import_pieces_common11.HttpMethod.DELETE, `/webhooks/${webhookId}`), await context.store.delete("webhookId_conversationAbandoned");
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/oncehub/src/lib/triggers/conversation-closed.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var import_pieces_common12 = require("@activepieces/pieces-common"), conversationClosed = (0, import_pieces_framework11.createTrigger)({
  auth: oncehubAuth,
  name: "conversationClosed",
  displayName: "Conversation Closed",
  description: "Triggered when: Website visitor reaches the end of the conversation flow and when Website visitor starts a new conversation with a different chatbot",
  aiMetadata: {
    description: "Fires when a OnceHub chatbot conversation closes, either when the visitor reaches the end of the conversation flow or starts a new conversation with a different chatbot. The event carries the conversation with its contact and owner details."
  },
  props: {},
  sampleData: {
    id: "EVNT-KN56U3YL7C",
    object: "event",
    creation_time: "2020-03-22T09:49:12Z",
    type: "conversation.closed",
    api_version: "v2",
    data: {
      id: "CVR-022EAEA41C",
      object: "conversation",
      creation_time: "2021-07-13T12:28:24Z",
      initiated_by: "contact",
      last_updated_time: "2021-07-13T12:33:54Z",
      last_interacted_time: "2021-07-13T12:33:54Z",
      contact: {
        object: "contact",
        id: "CTC-J4FR05BKEW",
        creation_time: "2020-03-22T09:48:48Z",
        last_updated_time: "2020-03-22T09:48:48Z",
        last_interacted_time: null,
        owner: "USR-FSD423423",
        status: "Qualified",
        city: "New York",
        company_size: "50-100",
        company: "Acme Inc",
        country: "United States",
        email: "carrie.customer@gmail.com",
        employees: 1,
        first_name: "Carrie",
        has_consent: !1,
        job_title: "Executive Assistant",
        last_name: "Customer",
        mobile_phone: "+12025550195",
        phone: "+12025550100",
        post_code: "10001",
        salutation: "Ms.",
        state: "New York",
        street_address: "123 Main Street",
        terms_of_sevice: !1,
        timezone: "America/New_York",
        custom_fields: []
      },
      owner: {
        first_name: "Andrea",
        last_name: "Hartie",
        email: "AndreaHartie@example.com",
        role_name: "Member",
        timezone: "America/Chicago",
        teams: ["TM-GCJU8DLBTPY1"]
      },
      status: "closed",
      bot: "BOT-62774A40FB",
      "...": "[Additional Properties Truncated]"
    }
  },
  type: import_pieces_framework11.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let api_key = context.auth.secret_text, { webhookUrl } = context, response = await makeRequest(api_key, import_pieces_common12.HttpMethod.POST, "/webhooks", {
      url: webhookUrl,
      name: `conversation closed Webhook - ${(/* @__PURE__ */ new Date()).getTime()}`,
      events: ["conversation.closed"]
    });
    await context.store.put("webhookId_conversationClosed", response.id);
  },
  async onDisable(context) {
    let api_key = context.auth.secret_text, webhookId = await context.store.get(
      "webhookId_conversationClosed"
    );
    webhookId && await makeRequest(api_key, import_pieces_common12.HttpMethod.DELETE, `/webhooks/${webhookId}`), await context.store.delete("webhookId_conversationClosed");
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/oncehub/src/lib/triggers/conversation-started.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var import_pieces_common13 = require("@activepieces/pieces-common"), conversationStarted = (0, import_pieces_framework12.createTrigger)({
  auth: oncehubAuth,
  name: "conversationStarted",
  displayName: "Conversation Started",
  description: "Triggered when website visitor starts interacting with a chatbot.",
  aiMetadata: {
    description: "Fires when a website visitor begins interacting with a OnceHub chatbot, starting a new conversation. The event carries the conversation with its contact and owner details."
  },
  props: {},
  sampleData: {
    id: "EVNT-KN56U3YL7C",
    object: "event",
    creation_time: "2020-03-22T09:49:12Z",
    type: "conversation.started",
    api_version: "v2",
    data: {
      id: "CVR-022EAEA41C",
      object: "conversation",
      creation_time: "2021-07-13T12:28:24Z",
      initiated_by: "contact",
      last_updated_time: "2021-07-13T12:33:54Z",
      last_interacted_time: "2021-07-13T12:33:54Z",
      contact: {
        object: "contact",
        id: "CTC-J4FR05BKEW",
        creation_time: "2020-03-22T09:48:48Z",
        last_updated_time: "2020-03-22T09:48:48Z",
        last_interacted_time: null,
        owner: "USR-FSD423423",
        status: "Qualified",
        city: "New York",
        company_size: "50-100",
        company: "Acme Inc",
        country: "United States",
        email: "carrie.customer@gmail.com",
        employees: 1,
        first_name: "Carrie",
        has_consent: !1,
        job_title: "Executive Assistant",
        last_name: "Customer",
        mobile_phone: "+12025550195",
        phone: "+12025550100",
        post_code: "10001",
        salutation: "Ms.",
        state: "New York",
        street_address: "123 Main Street",
        terms_of_sevice: !1,
        timezone: "America/New_York",
        custom_fields: []
      },
      owner: {
        first_name: "Andrea",
        last_name: "Hartie",
        email: "AndreaHartie@example.com",
        role_name: "Member",
        timezone: "America/Chicago",
        teams: ["TM-GCJU8DLBTPY1"]
      },
      status: "closed",
      bot: "BOT-62774A40FB",
      "...": "[Additional Properties Truncated]"
    }
  },
  type: import_pieces_framework12.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let api_key = context.auth.secret_text, { webhookUrl } = context, response = await makeRequest(api_key, import_pieces_common13.HttpMethod.POST, "/webhooks", {
      url: webhookUrl,
      name: `conversation started Webhook - ${(/* @__PURE__ */ new Date()).getTime()}`,
      events: ["conversation.started"]
    });
    await context.store.put("webhookId_conversationStarted", response.id);
  },
  async onDisable(context) {
    let api_key = context.auth.secret_text, webhookId = await context.store.get(
      "webhookId_conversationStarted"
    );
    webhookId && await makeRequest(api_key, import_pieces_common13.HttpMethod.DELETE, `/webhooks/${webhookId}`), await context.store.delete("webhookId_conversationStarted");
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/oncehub/src/index.ts
var oncehub = (0, import_pieces_framework13.createPiece)({
  displayName: "Oncehub",
  auth: oncehubAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/oncehub.png",
  categories: [import_pieces_framework14.PieceCategory.SALES_AND_CRM],
  description: "Build meaningful on-brand scheduling experiences with OnceHubs online appointment booking software",
  authors: ["sanket-a11y"],
  actions: [
    findContact,
    createContact,
    (0, import_pieces_common14.createCustomApiCallAction)({
      auth: oncehubAuth,
      baseUrl: () => "https://api.oncehub.com/v2",
      authMapping: async (auth) => ({
        "API-Key": `${auth.secret_text}`
      })
    })
  ],
  triggers: [
    bookingCanceledThenRescheduled,
    bookingCanceled,
    bookingCompleted,
    bookingNoshow,
    bookingRescheduled,
    bookingScheduled,
    conversationAbandoned,
    conversationClosed,
    conversationStarted
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  oncehub
});
