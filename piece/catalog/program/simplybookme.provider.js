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

// ../ap/packages/pieces/community/simplybookme/src/index.ts
var index_exports = {};
__export(index_exports, {
  simplybookme: () => simplybookme
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework19 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/simplybookme/src/lib/actions/cancel-booking.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/simplybookme/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), simplybookAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: "Enter your Simplybook admin credentials to access the Admin API.",
  props: {
    companyLogin: import_pieces_framework.Property.ShortText({
      displayName: "Company Login",
      description: 'Your Simplybook company login (e.g., "yourcompany" from yourcompany.simplybook.me)',
      required: !0
    }),
    userLogin: import_pieces_framework.Property.ShortText({
      displayName: "User Login",
      description: "Your admin user login associated with the company",
      required: !0
    }),
    userPassword: import_pieces_framework.Property.ShortText({
      displayName: "User Password",
      description: "Your admin user password",
      required: !0
    })
  },
  required: !0
});
async function getAccessToken(auth) {
  var _a, _b;
  try {
    let response = await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://user-api.simplybook.me/login",
      headers: {
        "Content-Type": "application/json"
      },
      body: {
        jsonrpc: "2.0",
        method: "getUserToken",
        params: [auth.companyLogin, auth.userLogin, auth.userPassword],
        id: 1
      },
      timeout: 15e3
    });
    if ((_a = response.body) != null && _a.error) {
      let errorCode = response.body.error.code, helpfulMessage = response.body.error.message;
      throw errorCode === -33001 ? helpfulMessage = "Company does not exist" : errorCode === -33002 ? helpfulMessage = "Wrong API key" : errorCode === -33003 ? helpfulMessage = "User with this login and password not found" : errorCode === -33004 ? helpfulMessage = "User is blocked" : errorCode === -33005 && (helpfulMessage = "You are not allowed to use this application when HIPAA Custom Feature is enabled"), new Error(
        `Simplybook authentication error: ${helpfulMessage} (code: ${errorCode})`
      );
    }
    if ((_b = response.body) != null && _b.result)
      return response.body.result;
    throw new Error("Failed to get access token: No result in response");
  } catch (error) {
    throw error.response ? new Error(
      `Failed to authenticate with Simplybook: ${error.response.status} - ${JSON.stringify(error.response.body)}`
    ) : error;
  }
}
async function makeJsonRpcCall(auth, method, params = [], usePublicEndpoint = !1) {
  var _a, _b;
  let token = await getAccessToken(auth), baseUrl = "https://user-api.simplybook.me", url = usePublicEndpoint ? baseUrl : `${baseUrl}/admin`, headers = {
    "Content-Type": "application/json",
    "X-Company-Login": auth.companyLogin,
    "X-User-Token": token
  };
  try {
    let response = await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url,
      headers,
      body: {
        jsonrpc: "2.0",
        method,
        params,
        id: 1
      },
      timeout: 2e4
    });
    if ((_a = response.body) != null && _a.error)
      throw new Error(
        `Simplybook API error: ${response.body.error.message} (code: ${response.body.error.code})${response.body.error.data ? ` - ${JSON.stringify(response.body.error.data)}` : ""}`
      );
    if (((_b = response.body) == null ? void 0 : _b.result) !== void 0)
      return response.body.result;
    throw new Error("Failed to get result from Simplybook API");
  } catch (error) {
    throw error.response ? new Error(
      `Simplybook API request failed: ${error.response.status} - ${JSON.stringify(error.response.body)}`
    ) : error;
  }
}

// ../ap/packages/pieces/community/simplybookme/src/lib/common/helpers.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var clientDropdown = import_pieces_framework2.Property.Dropdown({
  auth: simplybookAuth,
  displayName: "Client",
  description: "Select a client",
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
      return {
        disabled: !1,
        options: (await makeJsonRpcCall(
          auth.props,
          "getClientList",
          ["", null]
        )).map((client) => ({
          label: `${client.name}${client.email ? ` (${client.email})` : ""}${client.phone ? ` - ${client.phone}` : ""}`,
          value: client.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load clients"
      };
    }
  }
}), serviceDropdown = import_pieces_framework2.Property.Dropdown({
  auth: simplybookAuth,
  displayName: "Service",
  description: "Select a service",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your account first"
      };
    try {
      let response = await Promise.race([
        makeJsonRpcCall(
          auth.props,
          "getEventList",
          []
        ),
        new Promise(
          (_, reject) => setTimeout(() => reject(new Error("Request timeout after 20s")), 2e4)
        )
      ]), services = [];
      if (Array.isArray(response))
        services = response;
      else if (response && typeof response == "object") {
        let keys = Object.keys(response);
        keys.length > 0 && keys.every((k) => !isNaN(Number(k))) ? services = Object.values(response) : services = response.data || response.result || response.items || [];
      }
      return !Array.isArray(services) || services.length === 0 ? {
        disabled: !1,
        options: [],
        placeholder: services.length === 0 ? "No services found" : "Invalid response format"
      } : {
        disabled: !1,
        options: services.map((service) => ({
          label: service.name || `Service ${service.id}`,
          value: service.id
        }))
      };
    } catch (error) {
      return console.error("Failed to load services:", error), {
        disabled: !0,
        options: [],
        placeholder: `Error: ${((_a = error.message) == null ? void 0 : _a.substring(0, 50)) || "Failed to load"}`
      };
    }
  }
}), providerDropdown = import_pieces_framework2.Property.Dropdown({
  auth: simplybookAuth,
  displayName: "Provider",
  description: "Select a service provider",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your account first"
      };
    try {
      let response = await Promise.race([
        makeJsonRpcCall(
          auth.props,
          "getUnitList",
          []
        ),
        new Promise(
          (_, reject) => setTimeout(() => reject(new Error("Request timeout after 20s")), 2e4)
        )
      ]), providers = [];
      if (Array.isArray(response))
        providers = response;
      else if (response && typeof response == "object") {
        let keys = Object.keys(response);
        keys.length > 0 && keys.every((k) => !isNaN(Number(k))) ? providers = Object.values(response) : providers = response.data || response.result || response.items || [];
      }
      return !Array.isArray(providers) || providers.length === 0 ? {
        disabled: !1,
        options: [],
        placeholder: providers.length === 0 ? "No providers found" : "Invalid response format"
      } : {
        disabled: !1,
        options: providers.map((provider) => ({
          label: provider.name || `Provider ${provider.id}`,
          value: provider.id
        }))
      };
    } catch (error) {
      return console.error("Failed to load providers:", error), {
        disabled: !0,
        options: [],
        placeholder: `Error: ${((_a = error.message) == null ? void 0 : _a.substring(0, 50)) || "Failed to load"}`
      };
    }
  }
}), noteTypeDropdown = import_pieces_framework2.Property.Dropdown({
  auth: simplybookAuth,
  displayName: "Note Type",
  description: "Select a note type",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your account first"
      };
    try {
      let authData = auth.props, token = await getAccessToken(authData), responseData = (await Promise.race([
        import_pieces_common2.httpClient.sendRequest({
          method: import_pieces_common2.HttpMethod.GET,
          url: "https://user-api-v2.simplybook.me/admin/calendar-notes/types",
          headers: {
            "Content-Type": "application/json",
            "X-Company-Login": authData.companyLogin,
            "X-Token": token
          },
          timeout: 2e4
        }),
        new Promise(
          (_, reject) => setTimeout(() => reject(new Error("Request timeout after 20s")), 2e4)
        )
      ])).body, noteTypes = [];
      if (Array.isArray(responseData))
        noteTypes = responseData;
      else if (responseData && typeof responseData == "object") {
        let keys = Object.keys(responseData);
        keys.length > 0 && keys.every((k) => !isNaN(Number(k))) ? noteTypes = Object.values(responseData) : noteTypes = responseData.data || responseData.result || responseData.items || [];
      }
      return !Array.isArray(noteTypes) || noteTypes.length === 0 ? {
        disabled: !1,
        options: [],
        placeholder: noteTypes.length === 0 ? "No note types found" : "Invalid response format"
      } : {
        disabled: !1,
        options: noteTypes.map((noteType) => ({
          label: noteType.name || `Note Type ${noteType.id}`,
          value: noteType.id
        }))
      };
    } catch (error) {
      return console.error("Failed to load note types:", error), {
        disabled: !0,
        options: [],
        placeholder: `Error: ${((_a = error.message) == null ? void 0 : _a.substring(0, 50)) || "Failed to load"}`
      };
    }
  }
}), bookingDropdown = import_pieces_framework2.Property.Dropdown({
  auth: simplybookAuth,
  displayName: "Booking",
  description: "Select a booking",
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
      let today = /* @__PURE__ */ new Date(), thirtyDaysAgo = /* @__PURE__ */ new Date();
      thirtyDaysAgo.setDate(today.getDate() - 30);
      let dateFrom = thirtyDaysAgo.toISOString().split("T")[0], dateTo = today.toISOString().split("T")[0];
      return {
        disabled: !1,
        options: (await makeJsonRpcCall(
          auth.props,
          "getBookings",
          [{ date_from: dateFrom, date_to: dateTo, booking_type: "non_cancelled" }]
        )).map((booking) => ({
          label: [
            booking.code ? `#${booking.code}` : `ID: ${booking.id}`,
            booking.start_date,
            booking.start_time,
            booking.client_name,
            booking.event_name
          ].filter(Boolean).join(" - ") || `Booking ${booking.id}`,
          value: booking.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load bookings"
      };
    }
  }
});

// ../ap/packages/pieces/community/simplybookme/src/lib/actions/cancel-booking.ts
var cancelBooking = (0, import_pieces_framework3.createAction)({
  auth: simplybookAuth,
  name: "cancel_booking",
  displayName: "Cancel Booking",
  description: "Cancel an existing booking. Returns true on success.",
  audience: "both",
  aiMetadata: { description: "Cancels a single existing booking in SimplyBook.me, identified by its booking ID. Use to call off an appointment an agent or client no longer needs. Not idempotent: the underlying cancelBooking call changes booking state, and cancelling an already-cancelled or missing booking may fail.", idempotent: !1 },
  props: {
    bookingId: bookingDropdown
  },
  async run(context) {
    let auth = context.auth.props, { bookingId } = context.propsValue;
    return { success: await makeJsonRpcCall(auth, "cancelBooking", [bookingId]) };
  }
});

// ../ap/packages/pieces/community/simplybookme/src/lib/actions/create-booking.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var createBooking = (0, import_pieces_framework4.createAction)({
  auth: simplybookAuth,
  name: "create_booking",
  displayName: "Create Booking",
  description: "Create a new booking using the book API method. Returns appointment info or throws exception if time not available.",
  audience: "both",
  aiMetadata: { description: "Books a new appointment in SimplyBook.me for a given service, provider, and client across a start/end date-time window. Use to schedule a single appointment, a group batch (via count), or to append to an existing batch (via batchId) \u2014 count and batchId are mutually exclusive. The requested slot must be available and aligned to the company timeframe or the call throws. Not idempotent: each call creates a new booking.", idempotent: !1 },
  props: {
    eventId: serviceDropdown,
    unitId: providerDropdown,
    clientId: clientDropdown,
    startDate: import_pieces_framework4.Property.ShortText({
      displayName: "Start Date",
      description: "Booking start date (format: YYYY-MM-DD, e.g., 2024-03-01)",
      required: !0
    }),
    startTime: import_pieces_framework4.Property.ShortText({
      displayName: "Start Time",
      description: "Booking start time (format: HH:MM:SS, e.g., 09:00:00). Must be multiple of company timeframe.",
      required: !0
    }),
    endDate: import_pieces_framework4.Property.ShortText({
      displayName: "End Date",
      description: "Booking end date (format: YYYY-MM-DD, e.g., 2024-03-01). Should be later than start date.",
      required: !0
    }),
    endTime: import_pieces_framework4.Property.ShortText({
      displayName: "End Time",
      description: "Booking end time (format: HH:MM:SS, e.g., 10:00:00). Should be later than start time.",
      required: !0
    }),
    clientTimeOffset: import_pieces_framework4.Property.Number({
      displayName: "Client Time Offset (seconds)",
      description: "Difference between company and client time zone in seconds (e.g., -3600 for GMT+3 client with GMT+2 company)",
      required: !1,
      defaultValue: 0
    }),
    additionalFields: import_pieces_framework4.Property.Json({
      displayName: "Additional Fields",
      description: 'Additional params and fields as object (e.g., {"promocode": "CODE123", "location_id": "1"}). Use for promo codes, location ID, intake forms.',
      required: !1
    }),
    count: import_pieces_framework4.Property.Number({
      displayName: "Count",
      description: "Number of bookings for group booking batch (min. 1). Cannot be used with Batch ID.",
      required: !1,
      defaultValue: 1
    }),
    batchId: import_pieces_framework4.Property.Number({
      displayName: "Batch ID",
      description: "Add booking to existing group bookings batch. Cannot be used with Count > 1.",
      required: !1
    }),
    recurringData: import_pieces_framework4.Property.Json({
      displayName: "Recurring Data",
      description: "Make booking recurrent (optional, array format)",
      required: !1
    })
  },
  async run(context) {
    let auth = context.auth.props, {
      eventId,
      unitId,
      clientId,
      startDate,
      startTime,
      endDate,
      endTime,
      clientTimeOffset,
      additionalFields,
      count,
      batchId,
      recurringData
    } = context.propsValue;
    return await makeJsonRpcCall(auth, "book", [
      eventId,
      unitId,
      clientId,
      startDate,
      startTime,
      endDate,
      endTime,
      clientTimeOffset || 0,
      additionalFields || {},
      count || 1,
      batchId || null,
      recurringData || null
    ]);
  }
});

// ../ap/packages/pieces/community/simplybookme/src/lib/actions/create-booking-comment.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var createBookingComment = (0, import_pieces_framework5.createAction)({
  auth: simplybookAuth,
  name: "create_booking_comment",
  displayName: "Set Booking Comment",
  description: "Set a comment for a booking",
  audience: "both",
  aiMetadata: { description: "Sets (overwrites) the admin comment text on an existing booking in SimplyBook.me, identified by its booking ID. Use to attach or update an internal note on an appointment. Idempotent: re-sending the same booking ID and comment leaves the comment in the same state.", idempotent: !0 },
  props: {
    bookingId: bookingDropdown,
    comment: import_pieces_framework5.Property.LongText({
      displayName: "Comment",
      description: "The comment text to set for the booking",
      required: !0
    })
  },
  async run(context) {
    let auth = context.auth.props, { bookingId, comment } = context.propsValue;
    return { id: await makeJsonRpcCall(auth, "setBookingComment", [bookingId, comment]) };
  }
});

// ../ap/packages/pieces/community/simplybookme/src/lib/actions/create-client.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var createClient = (0, import_pieces_framework6.createAction)({
  auth: simplybookAuth,
  name: "create_client",
  displayName: "Add Client",
  description: "Add a new client with specified data. Email, phone, or both may be required (check company settings).",
  audience: "both",
  aiMetadata: { description: "Creates a new client record in SimplyBook.me from a name plus optional contact and address fields. Use when an agent needs a client to attach to a booking and the person does not yet exist (look them up with Get Client List first). Email, phone, or both may be required by company settings. Not idempotent: each call adds a new client even with identical data.", idempotent: !1 },
  props: {
    name: import_pieces_framework6.Property.ShortText({
      displayName: "Name",
      description: "Client name",
      required: !0
    }),
    email: import_pieces_framework6.Property.ShortText({
      displayName: "Email",
      description: "Client email address",
      required: !1
    }),
    phone: import_pieces_framework6.Property.ShortText({
      displayName: "Phone",
      description: "Client phone number (e.g., +1502-810-4521)",
      required: !1
    }),
    address1: import_pieces_framework6.Property.ShortText({
      displayName: "Address Line 1",
      description: "Client address line 1",
      required: !1
    }),
    address2: import_pieces_framework6.Property.ShortText({
      displayName: "Address Line 2",
      description: "Client address line 2",
      required: !1
    }),
    city: import_pieces_framework6.Property.ShortText({
      displayName: "City",
      description: "Client city",
      required: !1
    }),
    zip: import_pieces_framework6.Property.ShortText({
      displayName: "ZIP Code",
      description: "Client ZIP/postal code",
      required: !1
    }),
    countryId: import_pieces_framework6.Property.ShortText({
      displayName: "Country ID",
      description: "Client country ID",
      required: !1
    })
  },
  async run(context) {
    let auth = context.auth.props, {
      name,
      email,
      phone,
      address1,
      address2,
      city,
      zip,
      countryId
    } = context.propsValue, clientData = { name };
    return email && (clientData.email = email), phone && (clientData.phone = phone), address1 && (clientData.address1 = address1), address2 && (clientData.address2 = address2), city && (clientData.city = city), zip && (clientData.zip = zip), countryId && (clientData.country_id = countryId), { clientId: await makeJsonRpcCall(auth, "addClient", [clientData]) };
  }
});

// ../ap/packages/pieces/community/simplybookme/src/lib/actions/create-detailed-report.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var createDetailedReport = (0, import_pieces_framework7.createAction)({
  auth: simplybookAuth,
  name: "create_detailed_report",
  displayName: "Create Detailed Report",
  description: "Generate a detailed report (metrics, bookings, revenue)",
  audience: "both",
  aiMetadata: { description: "Generates a detailed admin report of bookings, metrics, and revenue in SimplyBook.me over an optional date range, with filters for service, provider, client, booking code, and cancelled vs. non-cancelled bookings. Use to pull analytics or export selected columns; leaving filters empty returns the broadest result set. Idempotent: it only reads and aggregates existing data, so repeating the same query returns the same report without side effects.", idempotent: !0 },
  props: {
    createdDateFrom: import_pieces_framework7.Property.ShortText({
      displayName: "Created Date From",
      description: "Filter by booking creation date from (format: YYYY-MM-DD)",
      required: !1
    }),
    createdDateTo: import_pieces_framework7.Property.ShortText({
      displayName: "Created Date To",
      description: "Filter by booking creation date to (format: YYYY-MM-DD)",
      required: !1
    }),
    dateFrom: import_pieces_framework7.Property.ShortText({
      displayName: "Date From",
      description: "Filter by booking date from (format: YYYY-MM-DD)",
      required: !1
    }),
    dateTo: import_pieces_framework7.Property.ShortText({
      displayName: "Date To",
      description: "Filter by booking date to (format: YYYY-MM-DD)",
      required: !1
    }),
    code: import_pieces_framework7.Property.ShortText({
      displayName: "Booking Code",
      description: "Filter by booking code",
      required: !1
    }),
    eventId: import_pieces_framework7.Property.Number({
      displayName: "Service ID",
      description: "Filter by service ID",
      required: !1
    }),
    unitGroupId: import_pieces_framework7.Property.Number({
      displayName: "Service Provider ID",
      description: "Filter by service provider ID",
      required: !1
    }),
    clientId: import_pieces_framework7.Property.Number({
      displayName: "Client ID",
      description: "Filter by client ID",
      required: !1
    }),
    bookingType: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Booking Type",
      description: "Filter by booking type",
      required: !1,
      options: {
        options: [
          { label: "Non-Cancelled", value: "non_cancelled" },
          { label: "Cancelled", value: "cancelled" }
        ]
      }
    }),
    orderField: import_pieces_framework7.Property.ShortText({
      displayName: "Order Field",
      description: "Field to order results by (e.g., record_date)",
      required: !1,
      defaultValue: "record_date"
    }),
    orderDirection: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Order Direction",
      description: "Sort order direction",
      required: !1,
      options: {
        options: [
          { label: "Ascending", value: "asc" },
          { label: "Descending", value: "desc" }
        ]
      },
      defaultValue: "asc"
    }),
    exportColumns: import_pieces_framework7.Property.Json({
      displayName: "Export Columns",
      description: "Array of columns to export (leave empty for all columns)",
      required: !1
    })
  },
  async run(context) {
    let auth = context.auth.props, accessToken = await getAccessToken(auth), filter = {};
    context.propsValue.createdDateFrom && (filter.created_date_from = context.propsValue.createdDateFrom), context.propsValue.createdDateTo && (filter.created_date_to = context.propsValue.createdDateTo), context.propsValue.dateFrom && (filter.date_from = context.propsValue.dateFrom), context.propsValue.dateTo && (filter.date_to = context.propsValue.dateTo), context.propsValue.code && (filter.code = context.propsValue.code), context.propsValue.eventId && (filter.event_id = context.propsValue.eventId.toString()), context.propsValue.unitGroupId && (filter.unit_group_id = context.propsValue.unitGroupId.toString()), context.propsValue.clientId && (filter.client_id = context.propsValue.clientId.toString()), context.propsValue.bookingType && (filter.booking_type = context.propsValue.bookingType);
    let requestBody = {
      filter,
      export_columns: context.propsValue.exportColumns || [],
      order_direction: context.propsValue.orderDirection || "asc",
      order_field: context.propsValue.orderField || "record_date"
    };
    try {
      return (await import_pieces_common3.httpClient.sendRequest({
        method: import_pieces_common3.HttpMethod.POST,
        url: "https://user-api-v2.simplybook.me/admin/detailed-report",
        headers: {
          "Content-Type": "application/json",
          "X-Company-Login": auth.companyLogin,
          "X-Token": accessToken
        },
        body: requestBody
      })).body;
    } catch (error) {
      throw error.response ? new Error(
        `Failed to generate detailed report: ${error.response.status} - ${JSON.stringify(error.response.body)}`
      ) : new Error(`Failed to generate detailed report: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/simplybookme/src/lib/actions/create-note.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var createNote = (0, import_pieces_framework8.createAction)({
  auth: simplybookAuth,
  name: "create_note",
  displayName: "Create Note",
  description: "Create a note (generic) in the system",
  audience: "both",
  aiMetadata: { description: "Creates a calendar note in SimplyBook.me over a start/end date-time window, scoped by mode (service, provider, or all) and an optional provider and service, and can optionally block that time. Use to add an admin annotation or to reserve/block time on the calendar. Not idempotent: each call adds a new note.", idempotent: !1 },
  props: {
    startDateTime: import_pieces_framework8.Property.ShortText({
      displayName: "Start Date Time",
      description: "Note start date and time (format: YYYY-MM-DD HH:MM:SS)",
      required: !0
    }),
    endDateTime: import_pieces_framework8.Property.ShortText({
      displayName: "End Date Time",
      description: "Note end date and time (format: YYYY-MM-DD HH:MM:SS)",
      required: !0
    }),
    noteTypeId: noteTypeDropdown,
    note: import_pieces_framework8.Property.LongText({
      displayName: "Note",
      description: "Note body/content",
      required: !0
    }),
    mode: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Mode",
      description: "Note mode - determines visibility scope",
      required: !0,
      options: {
        options: [
          { label: "Service", value: "service" },
          { label: "Provider", value: "provider" },
          { label: "All", value: "all" }
        ]
      }
    }),
    timeBlocked: import_pieces_framework8.Property.Checkbox({
      displayName: "Time Blocked",
      description: "Whether the time should be blocked",
      required: !1,
      defaultValue: !0
    }),
    providerId: import_pieces_framework8.Property.Dropdown({
      auth: simplybookAuth,
      displayName: "Provider",
      description: "Select a provider (optional)",
      required: !1,
      refreshers: [],
      options: providerDropdown.options
    }),
    serviceId: import_pieces_framework8.Property.Dropdown({
      auth: simplybookAuth,
      displayName: "Service",
      description: "Select a service (optional)",
      required: !1,
      refreshers: [],
      options: serviceDropdown.options
    })
  },
  async run(context) {
    var _a;
    let auth = context.auth.props, accessToken = await getAccessToken(auth), noteData = {
      start_date_time: context.propsValue.startDateTime,
      end_date_time: context.propsValue.endDateTime,
      note_type_id: context.propsValue.noteTypeId.toString(),
      note: context.propsValue.note,
      mode: context.propsValue.mode,
      time_blocked: (_a = context.propsValue.timeBlocked) != null ? _a : !0
    };
    context.propsValue.providerId && (noteData.provider_id = context.propsValue.providerId), context.propsValue.serviceId && (noteData.service_id = context.propsValue.serviceId);
    try {
      return (await import_pieces_common4.httpClient.sendRequest({
        method: import_pieces_common4.HttpMethod.POST,
        url: "https://user-api-v2.simplybook.me/admin/calendar-notes",
        headers: {
          "Content-Type": "application/json",
          "X-Company-Login": auth.companyLogin,
          "X-Token": accessToken
        },
        body: noteData
      })).body;
    } catch (error) {
      throw error.response ? new Error(
        `Failed to create note: ${error.response.status} - ${JSON.stringify(error.response.body)}`
      ) : new Error(`Failed to create note: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/simplybookme/src/lib/actions/delete-client.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var deleteClient = (0, import_pieces_framework9.createAction)({
  auth: simplybookAuth,
  name: "delete_client",
  displayName: "Delete Client",
  description: "Delete an existing client",
  audience: "both",
  aiMetadata: { description: "Permanently deletes a client record in SimplyBook.me, identified by its client ID. Use to remove a client an agent no longer needs; this is destructive and cannot be undone. Not idempotent: a repeat call on the same ID has no client left to delete and may error.", idempotent: !1 },
  props: {
    clientId: clientDropdown
  },
  async run(context) {
    let auth = context.auth.props, accessToken = await getAccessToken(auth), { clientId } = context.propsValue;
    try {
      let response = await import_pieces_common5.httpClient.sendRequest({
        method: import_pieces_common5.HttpMethod.DELETE,
        url: `https://user-api-v2.simplybook.me/admin/clients/${clientId}`,
        headers: {
          "Content-Type": "application/json",
          "X-Company-Login": auth.companyLogin,
          "X-Token": accessToken
        }
      });
      return {
        success: !0,
        message: `Client ${clientId} deleted successfully`,
        response: response.body
      };
    } catch (error) {
      throw error.response ? new Error(
        `Failed to delete client: ${error.response.status} - ${JSON.stringify(
          error.response.body
        )}`
      ) : new Error(`Failed to delete client: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/simplybookme/src/lib/actions/find-booking.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var findBooking = (0, import_pieces_framework10.createAction)({
  auth: simplybookAuth,
  name: "find_booking",
  displayName: "Get Bookings",
  description: "Returns list of bookings filtered by given parameters",
  audience: "both",
  aiMetadata: { description: "Lists bookings in SimplyBook.me, optionally narrowed by date/time range, provider, service, client, confirmation status, and booking type (e.g. all, cancelled, approved). Use to look up appointments or retrieve a booking ID for a follow-up action; leaving all filters empty returns the broadest list. Idempotent: read-only with no side effects.", idempotent: !0 },
  props: {
    dateFrom: import_pieces_framework10.Property.ShortText({
      displayName: "Date From",
      description: "Start date (format: YYYY-MM-DD, e.g., 2024-03-01)",
      required: !1
    }),
    timeFrom: import_pieces_framework10.Property.ShortText({
      displayName: "Time From",
      description: "Start time (format: HH:MM:SS, e.g., 09:00:00)",
      required: !1
    }),
    dateTo: import_pieces_framework10.Property.ShortText({
      displayName: "Date To",
      description: "End date (format: YYYY-MM-DD, e.g., 2024-03-31)",
      required: !1
    }),
    timeTo: import_pieces_framework10.Property.ShortText({
      displayName: "Time To",
      description: "End time (format: HH:MM:SS, e.g., 18:00:00)",
      required: !1
    }),
    createdDateFrom: import_pieces_framework10.Property.ShortText({
      displayName: "Created Date From",
      description: "Filter by creation date from (format: YYYY-MM-DD)",
      required: !1
    }),
    createdDateTo: import_pieces_framework10.Property.ShortText({
      displayName: "Created Date To",
      description: "Filter by creation date to (format: YYYY-MM-DD)",
      required: !1
    }),
    unitGroupId: import_pieces_framework10.Property.Dropdown({
      auth: simplybookAuth,
      displayName: "Provider",
      description: "Get bookings for a specific service provider (optional)",
      required: !1,
      refreshers: [],
      options: providerDropdown.options
    }),
    eventId: import_pieces_framework10.Property.Dropdown({
      auth: simplybookAuth,
      displayName: "Service",
      description: "Get bookings for a specific service (optional)",
      required: !1,
      refreshers: [],
      options: serviceDropdown.options
    }),
    isConfirmed: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Is Confirmed",
      description: "Filter by confirmation status",
      required: !1,
      options: {
        options: [
          { label: "Confirmed (1)", value: 1 },
          { label: "Not Confirmed (0)", value: 0 }
        ]
      }
    }),
    clientId: import_pieces_framework10.Property.Dropdown({
      auth: simplybookAuth,
      displayName: "Client",
      description: "Get bookings for a specific client (optional)",
      required: !1,
      refreshers: [],
      options: clientDropdown.options
    }),
    order: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Order By",
      description: "Sort order for results",
      required: !1,
      options: {
        options: [
          { label: "Date Start (Descending)", value: "date_start" },
          { label: "Date Start (Ascending)", value: "date_start_asc" },
          { label: "Record Date", value: "record_date" }
        ]
      }
    }),
    bookingType: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Booking Type",
      description: "Filter by booking type (depends on Approve booking plugin status)",
      required: !1,
      options: {
        options: [
          { label: "All", value: "all" },
          { label: "Cancelled", value: "cancelled" },
          { label: "Non Cancelled", value: "non_cancelled" },
          { label: "Cancelled by Client", value: "cancelled_by_client" },
          { label: "Cancelled by Admin", value: "cancelled_by_admin" },
          { label: "Non Approved Yet", value: "non_approved_yet" },
          { label: "Approved", value: "approved" }
        ]
      }
    })
  },
  async run(context) {
    let auth = context.auth.props, {
      dateFrom,
      timeFrom,
      dateTo,
      timeTo,
      createdDateFrom,
      createdDateTo,
      unitGroupId,
      eventId,
      isConfirmed,
      clientId,
      order,
      bookingType
    } = context.propsValue, filter = {};
    return dateFrom && (filter.date_from = dateFrom), timeFrom && (filter.time_from = timeFrom), dateTo && (filter.date_to = dateTo), timeTo && (filter.time_to = timeTo), createdDateFrom && (filter.created_date_from = createdDateFrom), createdDateTo && (filter.created_date_to = createdDateTo), unitGroupId && (filter.unit_group_id = unitGroupId), eventId && (filter.event_id = eventId), isConfirmed !== void 0 && (filter.is_confirmed = isConfirmed), clientId && (filter.client_id = clientId), order && (filter.order = order), bookingType && (filter.booking_type = bookingType), await makeJsonRpcCall(auth, "getBookings", [filter]);
  }
});

// ../ap/packages/pieces/community/simplybookme/src/lib/actions/find-client.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var findClient = (0, import_pieces_framework11.createAction)({
  auth: simplybookAuth,
  name: "find_client",
  displayName: "Get Client List",
  description: "Returns list of clients associated with company. Search by phone number, email address, or name.",
  audience: "both",
  aiMetadata: { description: "Lists clients of the SimplyBook.me company; an empty search string fetches all clients, while a non-empty value filters by phone, email, or name. Use to find a client or retrieve a client ID before booking, deleting, or filtering reports. Idempotent: read-only search with no side effects.", idempotent: !0 },
  props: {
    searchString: import_pieces_framework11.Property.ShortText({
      displayName: "Search String",
      description: "Search by phone number, email address, or name. Leave empty to get all clients.",
      required: !1
    }),
    limit: import_pieces_framework11.Property.Number({
      displayName: "Limit",
      description: "Maximum number of clients to return. Leave empty for no limit.",
      required: !1
    })
  },
  async run(context) {
    let auth = context.auth.props, { searchString, limit } = context.propsValue;
    return await makeJsonRpcCall(auth, "getClientList", [
      searchString || "",
      limit !== void 0 ? limit : null
    ]);
  }
});

// ../ap/packages/pieces/community/simplybookme/src/lib/actions/find-invoice.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var findInvoice = (0, import_pieces_framework12.createAction)({
  auth: simplybookAuth,
  name: "find_invoice",
  displayName: "Find Invoice",
  description: "Find invoices with filters and pagination",
  audience: "both",
  aiMetadata: { description: "Lists invoices/orders in SimplyBook.me with pagination and optional filters for client, date-time range, status, and booking code. Use to look up billing records or retrieve an invoice for a client or appointment; leaving filters empty returns the broadest paged list. Idempotent: read-only with no side effects.", idempotent: !0 },
  props: {
    page: import_pieces_framework12.Property.Number({
      displayName: "Page",
      description: "Page number in the list",
      required: !1,
      defaultValue: 1
    }),
    onPage: import_pieces_framework12.Property.Number({
      displayName: "Items Per Page",
      description: "Number of items per page",
      required: !1,
      defaultValue: 25
    }),
    clientId: import_pieces_framework12.Property.Dropdown({
      auth: simplybookAuth,
      displayName: "Client",
      description: "Filter by client (optional)",
      required: !1,
      refreshers: [],
      options: clientDropdown.options
    }),
    datetimeFrom: import_pieces_framework12.Property.ShortText({
      displayName: "Date Time From",
      description: "Order/invoice date and time from (format: YYYY-MM-DD HH:MM:SS)",
      required: !1
    }),
    datetimeTo: import_pieces_framework12.Property.ShortText({
      displayName: "Date Time To",
      description: "Order/invoice date and time to (format: YYYY-MM-DD HH:MM:SS)",
      required: !1
    }),
    status: import_pieces_framework12.Property.ShortText({
      displayName: "Status",
      description: "Order/invoice status",
      required: !1
    }),
    bookingCode: import_pieces_framework12.Property.ShortText({
      displayName: "Booking Code",
      description: "Filter by booking code",
      required: !1
    })
  },
  async run(context) {
    let auth = context.auth.props, accessToken = await getAccessToken(auth), queryParams = [];
    context.propsValue.page && queryParams.push(`page=${context.propsValue.page}`), context.propsValue.onPage && queryParams.push(`on_page=${context.propsValue.onPage}`), context.propsValue.clientId && queryParams.push(`filter[client_id]=${context.propsValue.clientId}`), context.propsValue.datetimeFrom && queryParams.push(`filter[datetime_from]=${encodeURIComponent(context.propsValue.datetimeFrom)}`), context.propsValue.datetimeTo && queryParams.push(`filter[datetime_to]=${encodeURIComponent(context.propsValue.datetimeTo)}`), context.propsValue.status && queryParams.push(`filter[status]=${encodeURIComponent(context.propsValue.status)}`), context.propsValue.bookingCode && queryParams.push(`filter[booking_code]=${encodeURIComponent(context.propsValue.bookingCode)}`);
    let queryString = queryParams.length > 0 ? `?${queryParams.join("&")}` : "";
    try {
      return (await import_pieces_common6.httpClient.sendRequest({
        method: import_pieces_common6.HttpMethod.GET,
        url: `https://user-api-v2.simplybook.me/admin/invoices${queryString}`,
        headers: {
          "Content-Type": "application/json",
          "X-Company-Login": auth.companyLogin,
          "X-Token": accessToken
        }
      })).body;
    } catch (error) {
      throw error.response ? new Error(
        `Failed to find invoices: ${error.response.status} - ${JSON.stringify(error.response.body)}`
      ) : new Error(`Failed to find invoices: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/simplybookme/src/lib/triggers/booking-canceled.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var bookingCanceled = (0, import_pieces_framework13.createTrigger)({
  auth: simplybookAuth,
  name: "booking_canceled",
  displayName: "Booking Cancellation",
  description: "Triggers when a booking is canceled in SimplyBook.me",
  aiMetadata: {
    description: 'Fires when an existing booking is canceled in SimplyBook.me (the platform webhook with notification_type "cancel"). Represents an appointment that has been called off, by either the client or an admin.'
  },
  type: import_pieces_framework13.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let body = context.payload.body;
    return body.notification_type !== "cancel" ? [] : [body];
  },
  sampleData: {
    booking_id: "4",
    booking_hash: "cfae2b34e23ec6e68545d60532234ae8",
    company: "examplecompany",
    notification_type: "cancel",
    webhook_timestamp: 1759919125,
    signature_algo: "sha256"
  }
});

// ../ap/packages/pieces/community/simplybookme/src/lib/triggers/booking-changed.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var bookingChanged = (0, import_pieces_framework14.createTrigger)({
  auth: simplybookAuth,
  name: "booking_changed",
  displayName: "Booking Change",
  description: "Triggers when a booking is changed in SimplyBook.me",
  aiMetadata: {
    description: 'Fires when an existing booking is modified in SimplyBook.me (the platform webhook with notification_type "change"), such as a rescheduled time, reassigned provider, or other edited appointment details.'
  },
  type: import_pieces_framework14.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let body = context.payload.body;
    return body.notification_type !== "change" ? [] : [body];
  },
  sampleData: {
    booking_id: "4",
    booking_hash: "cfae2b34e23ec6e68545d60532234ae8",
    company: "examplecompany",
    notification_type: "change",
    webhook_timestamp: 1759919125,
    signature_algo: "sha256"
  }
});

// ../ap/packages/pieces/community/simplybookme/src/lib/triggers/new-booking.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");
var newBooking = (0, import_pieces_framework15.createTrigger)({
  auth: simplybookAuth,
  name: "new_booking",
  displayName: "New Booking",
  description: "Triggers when a new booking is created in SimplyBook.me",
  aiMetadata: {
    description: 'Fires when a new booking is created in SimplyBook.me (the platform webhook with notification_type "create"), representing a newly scheduled appointment.'
  },
  type: import_pieces_framework15.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let body = context.payload.body;
    return body.notification_type !== "create" ? [] : [body];
  },
  sampleData: {
    booking_id: "4",
    booking_hash: "cfae2b34e23ec6e68545d60532234ae8",
    company: "examplecompany",
    notification_type: "create",
    webhook_timestamp: 1759919125,
    signature_algo: "sha256"
  }
});

// ../ap/packages/pieces/community/simplybookme/src/lib/triggers/new-client.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var polling = {
  strategy: import_pieces_common7.DedupeStrategy.LAST_ITEM,
  items: async ({ auth }) => {
    let clients = await makeJsonRpcCall(
      auth.props,
      "getClientList",
      ["", null]
    ), clientArray = [];
    if (Array.isArray(clients))
      clientArray = clients;
    else if (clients && typeof clients == "object") {
      let keys = Object.keys(clients);
      keys.length > 0 && keys.every((k) => !isNaN(Number(k))) && (clientArray = Object.values(clients));
    }
    return clientArray.sort((a, b) => b.id - a.id).map((client) => ({
      id: client.id,
      data: client
    }));
  }
}, newClient = (0, import_pieces_framework16.createTrigger)({
  auth: simplybookAuth,
  name: "new_client",
  displayName: "New Client",
  description: "Triggers when a new client is added (via booking or manually) in SimplyBook.me",
  aiMetadata: {
    description: "Fires when a new client is added to the SimplyBook.me company, whether created through a booking or entered manually. Polls the client list and emits each newly seen client."
  },
  type: import_pieces_framework16.TriggerStrategy.POLLING,
  props: {},
  async test(context) {
    return await import_pieces_common7.pollingHelper.test(polling, context);
  },
  async onEnable(context) {
    await import_pieces_common7.pollingHelper.onEnable(polling, context);
  },
  async onDisable(context) {
    await import_pieces_common7.pollingHelper.onDisable(polling, context);
  },
  async run(context) {
    return await import_pieces_common7.pollingHelper.poll(polling, context);
  },
  sampleData: {
    id: 12345,
    name: "John Doe",
    email: "john.doe@example.com",
    phone: "+1234567890",
    address: "123 Main St",
    city: "New York",
    zip: "10001",
    country: "USA",
    created_at: "2025-10-05T10:30:00.000Z"
  }
});

// ../ap/packages/pieces/community/simplybookme/src/lib/triggers/new-invoice.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_common9 = require("@activepieces/pieces-common");
var polling2 = {
  strategy: import_pieces_common8.DedupeStrategy.TIMEBASED,
  items: async ({ auth, lastFetchEpochMS }) => {
    let authData = auth, token = await getAccessToken(authData.props), now = /* @__PURE__ */ new Date(), lastFetch = lastFetchEpochMS ? new Date(lastFetchEpochMS) : new Date(now.getTime() - 720 * 60 * 60 * 1e3), formatDateTime = (date) => {
      let pad = (n) => n.toString().padStart(2, "0");
      return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
    }, datetimeFrom = formatDateTime(lastFetch), datetimeTo = formatDateTime(now), responseData = (await import_pieces_common9.httpClient.sendRequest({
      method: import_pieces_common9.HttpMethod.GET,
      url: `https://user-api-v2.simplybook.me/admin/invoices?filter[datetime_from]=${encodeURIComponent(datetimeFrom)}&filter[datetime_to]=${encodeURIComponent(datetimeTo)}`,
      headers: {
        "Content-Type": "application/json",
        "X-Company-Login": authData.props.companyLogin,
        "X-Token": token
      },
      timeout: 2e4
    })).body, invoiceArray = [];
    if (Array.isArray(responseData))
      invoiceArray = responseData;
    else if (responseData && typeof responseData == "object" && (invoiceArray = responseData.data || responseData.result || responseData.items || [], !Array.isArray(invoiceArray) && typeof invoiceArray == "object")) {
      let keys = Object.keys(invoiceArray);
      keys.length > 0 && keys.every((k) => !isNaN(Number(k))) && (invoiceArray = Object.values(invoiceArray));
    }
    return invoiceArray.filter((invoice) => !invoice.created_at && !invoice.date ? !1 : new Date(invoice.created_at || invoice.date).getTime() > lastFetchEpochMS).sort((a, b) => {
      let dateA = new Date(a.created_at || a.date || 0).getTime();
      return new Date(b.created_at || b.date || 0).getTime() - dateA;
    }).map((invoice) => ({
      epochMilliSeconds: new Date(invoice.created_at || invoice.date).getTime(),
      data: invoice
    }));
  }
}, newInvoice = (0, import_pieces_framework17.createTrigger)({
  auth: simplybookAuth,
  name: "new_invoice",
  displayName: "New Invoice",
  description: "Triggers when a new invoice is generated/paid in SimplyBook.me (requires Accept Payments feature)",
  aiMetadata: {
    description: "Fires when a new invoice/order is generated in SimplyBook.me, representing a billing record for a booking. Polls invoices by creation date; requires the Accept Payments feature to be enabled."
  },
  type: import_pieces_framework17.TriggerStrategy.POLLING,
  props: {},
  async test(context) {
    return await import_pieces_common8.pollingHelper.test(polling2, context);
  },
  async onEnable(context) {
    await import_pieces_common8.pollingHelper.onEnable(polling2, context);
  },
  async onDisable(context) {
    await import_pieces_common8.pollingHelper.onDisable(polling2, context);
  },
  async run(context) {
    return await import_pieces_common8.pollingHelper.poll(polling2, context);
  },
  sampleData: {
    id: 98765,
    booking_id: 123456,
    client_id: 12345,
    amount: 100,
    currency: "USD",
    status: "paid",
    payment_method: "credit_card",
    created_at: "2025-10-05T14:30:00.000Z",
    paid_at: "2025-10-05T14:35:00.000Z",
    invoice_number: "INV-2025-001",
    items: [
      {
        description: "Consultation Service",
        quantity: 1,
        unit_price: 100,
        total: 100
      }
    ]
  }
});

// ../ap/packages/pieces/community/simplybookme/src/lib/triggers/new-offer.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var polling3 = {
  strategy: import_pieces_common10.DedupeStrategy.TIMEBASED,
  items: async ({ auth, lastFetchEpochMS }) => {
    let authData = auth, now = /* @__PURE__ */ new Date(), dateFrom = (lastFetchEpochMS ? new Date(lastFetchEpochMS) : new Date(now.getTime() - 10080 * 60 * 1e3)).toISOString().split("T")[0], dateTo = now.toISOString().split("T")[0], bookings = await makeJsonRpcCall(authData.props, "getBookings", [
      {
        created_date_from: dateFrom,
        created_date_to: dateTo,
        booking_type: "non_cancelled",
        order: "record_date"
      }
    ]), bookingArray = [];
    if (Array.isArray(bookings))
      bookingArray = bookings;
    else if (bookings && typeof bookings == "object") {
      let keys = Object.keys(bookings);
      keys.length > 0 && keys.every((k) => !isNaN(Number(k))) && (bookingArray = Object.values(bookings));
    }
    return bookingArray.filter((booking) => {
      if (!booking.record_date) return !1;
      let recordTime = new Date(booking.record_date).getTime(), isOffer = booking.promo === !1;
      return recordTime > lastFetchEpochMS && isOffer;
    }).sort((a, b) => {
      let dateA = new Date(a.record_date || 0).getTime();
      return new Date(b.record_date || 0).getTime() - dateA;
    }).map((offer) => ({
      epochMilliSeconds: new Date(offer.record_date).getTime(),
      data: offer
    }));
  }
}, newOffer = (0, import_pieces_framework18.createTrigger)({
  auth: simplybookAuth,
  name: "new_offer",
  displayName: "New Offer",
  description: "Triggers when a new offer (proposal or quote) is created (bookings with promo: false)",
  aiMetadata: {
    description: "Fires when a new offer (proposal or quote) is created in SimplyBook.me, identified as a recently created booking with promo set to false. Polls bookings by creation date and emits each newly seen offer."
  },
  type: import_pieces_framework18.TriggerStrategy.POLLING,
  props: {},
  async test(context) {
    return await import_pieces_common10.pollingHelper.test(polling3, context);
  },
  async onEnable(context) {
    await import_pieces_common10.pollingHelper.onEnable(polling3, context);
  },
  async onDisable(context) {
    await import_pieces_common10.pollingHelper.onDisable(polling3, context);
  },
  async run(context) {
    return await import_pieces_common10.pollingHelper.poll(polling3, context);
  },
  sampleData: {
    id: 2,
    record_date: "2025-10-08 09:22:32",
    start_date: "2025-10-08 15:00:00",
    end_date: "2025-10-08 16:00:00",
    client_timezone: null,
    unit_id: "3",
    text: "client fort",
    client: "client fort",
    unit: "providertest",
    unit_email: "provider@example.com",
    event: "Consultation",
    event_id: "2",
    is_confirm: "1",
    client_id: "1",
    client_phone: "+1234567890",
    client_email: "client@example.com",
    offset: "0",
    comment: "Offer for consultation service",
    code: "23lt1xy7",
    event_duration: "60",
    promo: !1,
    promo_code: "OFFER2025",
    discount: 10
  }
});

// ../ap/packages/pieces/community/simplybookme/src/index.ts
var simplybookme = (0, import_pieces_framework19.createPiece)({
  displayName: "SimplyBook.me",
  auth: simplybookAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/simplybookme.png",
  authors: ["fortunamide", "sanket-a11y"],
  actions: [
    // Write Actions
    createBooking,
    createBookingComment,
    createClient,
    createNote,
    createDetailedReport,
    cancelBooking,
    deleteClient,
    // Search Actions
    findBooking,
    findClient,
    findInvoice
  ],
  triggers: [newBooking, bookingChanged, bookingCanceled, newClient, newInvoice, newOffer]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  simplybookme
});
