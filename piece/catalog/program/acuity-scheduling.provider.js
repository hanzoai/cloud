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

// ../ap/packages/pieces/community/acuity-scheduling/src/index.ts
var index_exports = {};
__export(index_exports, {
  acuityScheduling: () => acuityScheduling
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework12 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/acuity-scheduling/src/lib/actions/create-appointment.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/acuity-scheduling/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), acuitySchedulingAuth = import_pieces_framework.PieceAuth.OAuth2({
  required: !0,
  authUrl: "https://acuityscheduling.com/oauth2/authorize",
  tokenUrl: "https://acuityscheduling.com/oauth2/token",
  scope: ["api-v1"]
});

// ../ap/packages/pieces/community/acuity-scheduling/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), API_URL = "https://acuityscheduling.com/api/v1";
async function getAppointmentDetails(appointmentId, accessToken) {
  let request = {
    method: import_pieces_common.HttpMethod.GET,
    url: `${API_URL}/appointments/${appointmentId}`,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token: accessToken
    }
  };
  return (await import_pieces_common.httpClient.sendRequest(request)).body;
}
async function fetchAppointmentTypes(accessToken, includeDeleted = !1) {
  let queryParams = {};
  includeDeleted && (queryParams.includeDeleted = "true");
  let request = {
    method: import_pieces_common.HttpMethod.GET,
    url: `${API_URL}/appointment-types`,
    queryParams,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token: accessToken
    }
  }, response = await import_pieces_common.httpClient.sendRequest(request);
  return Array.isArray(response.body) ? response.body.filter((type) => includeDeleted || type.active === !0 || type.active === "true").map((type) => ({ label: type.name, value: type.id })) : [];
}
async function fetchCalendars(accessToken) {
  let request = {
    method: import_pieces_common.HttpMethod.GET,
    url: `${API_URL}/calendars`,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token: accessToken
    }
  }, response = await import_pieces_common.httpClient.sendRequest(request);
  return Array.isArray(response.body) ? response.body.map((calendar) => ({ label: calendar.name, value: calendar.id })) : [];
}
async function fetchAddons(accessToken, appointmentTypeId) {
  let allAddonsRequest = {
    method: import_pieces_common.HttpMethod.GET,
    url: `${API_URL}/appointment-addons`,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token: accessToken
    }
  }, allAddonsResponse = await import_pieces_common.httpClient.sendRequest(
    allAddonsRequest
  );
  if (!Array.isArray(allAddonsResponse.body))
    return [];
  let compatibleAddonIds = null;
  if (appointmentTypeId) {
    let appointmentTypeRequest = {
      method: import_pieces_common.HttpMethod.GET,
      url: `${API_URL}/appointment-types/${appointmentTypeId}`,
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: accessToken
      }
    };
    try {
      let appointmentTypeResponse = await import_pieces_common.httpClient.sendRequest(
        appointmentTypeRequest
      );
      appointmentTypeResponse.body && Array.isArray(appointmentTypeResponse.body.addonIDs) && (compatibleAddonIds = appointmentTypeResponse.body.addonIDs);
    } catch (e) {
      console.warn(
        `Could not fetch compatible addons for appointment type ${appointmentTypeId}, returning all addons. Error: ${e}`
      );
    }
  }
  let allAddons = allAddonsResponse.body.map((addon) => ({ label: addon.name, value: addon.id }));
  return compatibleAddonIds ? allAddons.filter((addon) => compatibleAddonIds.includes(addon.value)) : allAddons;
}
async function fetchLabels(accessToken) {
  let request = {
    method: import_pieces_common.HttpMethod.GET,
    url: `${API_URL}/labels`,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token: accessToken
    }
  }, response = await import_pieces_common.httpClient.sendRequest(
    request
  );
  return Array.isArray(response.body) ? response.body.map((label) => ({
    label: `${label.name} (${label.color})`,
    value: label.id
  })) : [];
}

// ../ap/packages/pieces/community/acuity-scheduling/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var appointmentTypeIdDropdown = (params) => import_pieces_framework2.Property.Dropdown({
  auth: acuitySchedulingAuth,
  displayName: params.displayName,
  description: params.description,
  required: params.required,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please authenticate first",
        options: []
      };
    let { access_token } = auth;
    return {
      disabled: !1,
      options: await fetchAppointmentTypes(access_token)
    };
  }
}), calendarIdDropdown = (params) => import_pieces_framework2.Property.Dropdown({
  auth: acuitySchedulingAuth,
  displayName: params.displayName,
  description: params.description,
  required: params.required,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please authenticate first",
        options: []
      };
    let { access_token } = auth;
    return {
      disabled: !1,
      options: await fetchCalendars(access_token)
    };
  }
}), addonIdsDropdown = (params) => import_pieces_framework2.Property.MultiSelectDropdown({
  auth: acuitySchedulingAuth,
  displayName: params.displayName,
  description: params.description,
  required: params.required,
  refreshers: ["appointmentTypeID"],
  options: async ({ auth, appointmentTypeID }) => {
    if (!auth || !appointmentTypeID)
      return {
        disabled: !0,
        placeholder: "Please authenticate first",
        options: []
      };
    let { access_token } = auth;
    return {
      disabled: !1,
      options: await fetchAddons(access_token, appointmentTypeID)
    };
  }
}), labelIdDropdown = (params) => import_pieces_framework2.Property.Dropdown({
  auth: acuitySchedulingAuth,
  displayName: params.displayName,
  description: params.description,
  required: params.required,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please authenticate first",
        options: []
      };
    let { access_token } = auth;
    return {
      disabled: !1,
      options: await fetchLabels(access_token)
    };
  }
});

// ../ap/packages/pieces/community/acuity-scheduling/src/lib/actions/create-appointment.ts
var createAppointmentAction = (0, import_pieces_framework3.createAction)({
  auth: acuitySchedulingAuth,
  name: "create_appointment",
  classification: "WRITE",
  displayName: "Create Appointment",
  description: "Creates a new appointment.",
  audience: "both",
  aiMetadata: {
    description: "Books a new appointment for a client on Acuity given a datetime, appointment type, and client name. Operates in two modes: standard booking enforces availability/attribute checks and auto-assigns a calendar, while admin booking (enable Book as Admin) bypasses those checks, allows setting notes, and requires a calendar ID. Use to schedule a client; not idempotent \u2014 each call creates a separate appointment.",
    idempotent: !1
  },
  props: {
    datetime: import_pieces_framework3.Property.DateTime({
      displayName: "DateTime",
      description: "Date and time of the appointment.",
      required: !0
    }),
    appointmentTypeID: appointmentTypeIdDropdown({
      displayName: "Appointment Type",
      description: "Select the type of appointment.",
      required: !0
    }),
    firstName: import_pieces_framework3.Property.ShortText({
      displayName: "First Name",
      description: "Client's first name.",
      required: !0
    }),
    lastName: import_pieces_framework3.Property.ShortText({
      displayName: "Last Name",
      description: "Client's last name.",
      required: !0
    }),
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Email",
      description: "Client's email address. (Optional if booking as admin).",
      required: !1
    }),
    phone: import_pieces_framework3.Property.ShortText({
      displayName: "Phone",
      description: "Client's phone number.",
      required: !1
    }),
    timezone: import_pieces_framework3.Property.ShortText({
      displayName: "Timezone",
      description: "Client's timezone (e.g., America/New_York). Required for accurate availability checking.",
      required: !0,
      defaultValue: "UTC"
    }),
    adminBooking: import_pieces_framework3.Property.Checkbox({
      displayName: "Book as Admin",
      description: "Set to true to book as an admin. Disables availability/attribute validations, allows setting notes, and makes Calendar ID required.",
      required: !1,
      defaultValue: !1
    }),
    calendarID: calendarIdDropdown({
      displayName: "Calendar ID",
      description: "Numeric ID of the calendar. Required if booking as admin. If not provided, Acuity tries to find an available calendar automatically for non-admin bookings.",
      required: !1
    }),
    noEmail: import_pieces_framework3.Property.Checkbox({
      displayName: "Suppress Confirmation Email/SMS",
      description: "If true, confirmation emails or SMS will not be sent.",
      required: !1,
      defaultValue: !1
    }),
    certificate: import_pieces_framework3.Property.ShortText({
      displayName: "Certificate Code",
      description: "Package or coupon certificate code.",
      required: !1
    }),
    notes: import_pieces_framework3.Property.LongText({
      displayName: "Notes",
      description: "Appointment notes. Only settable if booking as admin.",
      required: !1
    }),
    smsOptIn: import_pieces_framework3.Property.Checkbox({
      displayName: "SMS Opt-In",
      description: "Indicates whether the client has explicitly given permission to receive SMS messages.",
      required: !1,
      defaultValue: !1
    }),
    addonIDs: addonIdsDropdown({
      displayName: "Addons",
      description: "Select addons for the appointment. Addons are filtered by selected Appointment Type if available.",
      required: !1
    }),
    labelId: labelIdDropdown({
      displayName: "Label",
      description: "Apply a label to the appointment. The API currently supports one label.",
      required: !1
    })
  },
  async run(context) {
    let props = context.propsValue, queryParams = {};
    props.adminBooking && (queryParams.admin = "true"), props.noEmail && (queryParams.noEmail = "true");
    let body = {
      datetime: props.datetime,
      appointmentTypeID: props.appointmentTypeID,
      firstName: props.firstName,
      lastName: props.lastName,
      email: props.email
    };
    if (props.calendarID && (body.calendarID = props.calendarID), props.phone && (body.phone = props.phone), props.timezone && (body.timezone = props.timezone), props.certificate && (body.certificate = props.certificate), props.adminBooking && props.notes && (body.notes = props.notes), props.smsOptIn && (body.smsOptIn = props.smsOptIn), props.addonIDs && props.addonIDs.length > 0 && (body.addonIDs = props.addonIDs), props.labelId && (body.labelID = [{ id: props.labelId }]), props.adminBooking && !props.calendarID)
      throw new Error("Calendar ID is required when booking as admin.");
    return props.adminBooking && props.email === "" && delete body.email, (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: `${API_URL}/appointments`,
      queryParams,
      body,
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: context.auth.access_token
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/acuity-scheduling/src/lib/actions/reschedule-appointment.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var rescheduleAppointmentAction = (0, import_pieces_framework4.createAction)({
  auth: acuitySchedulingAuth,
  name: "reschedule_appointment",
  classification: "WRITE",
  displayName: "Reschedule Appointment",
  description: "Reschedules an existing appointment to a new date/time.",
  audience: "both",
  aiMetadata: {
    description: "Moves an existing Acuity appointment (by numeric appointment ID) to a new datetime and timezone, optionally onto a different calendar. Admin reschedule bypasses availability checks. Use to change the time of an already-booked appointment rather than creating a new one. Treated as non-idempotent \u2014 repeating may resend rescheduling notifications and revalidate availability.",
    idempotent: !1
  },
  props: {
    id: import_pieces_framework4.Property.Number({
      displayName: "Appointment ID",
      description: "The ID of the appointment to reschedule.",
      required: !0
    }),
    appointmentTypeID: appointmentTypeIdDropdown({
      displayName: "Appointment Type",
      description: "Select the type of appointment (used for finding new available slots).",
      required: !0
    }),
    datetime: import_pieces_framework4.Property.DateTime({
      displayName: "DateTime",
      description: "New Date and time of the appointment.",
      required: !0
    }),
    timezone: import_pieces_framework4.Property.ShortText({
      displayName: "Timezone",
      description: "Client's timezone (e.g., America/New_York).",
      required: !0,
      defaultValue: "UTC"
    }),
    calendarID: calendarIdDropdown({
      displayName: "New Calendar ID",
      description: "Numeric ID of the new calendar to reschedule to. If blank, stays on current calendar. Submit 0 to auto-assign.",
      required: !1
    }),
    adminReschedule: import_pieces_framework4.Property.Checkbox({
      displayName: "Reschedule as Admin",
      description: "Set to true to reschedule as an admin. Disables availability validations.",
      required: !1,
      defaultValue: !1
    }),
    noEmail: import_pieces_framework4.Property.Checkbox({
      displayName: "Suppress Rescheduling Email/SMS",
      description: "If true, rescheduling emails or SMS will not be sent.",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let props = context.propsValue, queryParams = {};
    props.adminReschedule && (queryParams.admin = "true"), props.noEmail && (queryParams.noEmail = "true");
    let body = {
      datetime: props.datetime
    };
    return props.calendarID !== void 0 && (body.calendarID = props.calendarID === 0 ? null : props.calendarID), props.timezone && (body.timezone = props.timezone), (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.PUT,
      url: `${API_URL}/appointments/${props.id}/reschedule`,
      queryParams,
      body,
      authentication: {
        type: import_pieces_common3.AuthenticationType.BEARER_TOKEN,
        token: context.auth.access_token
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/acuity-scheduling/src/lib/actions/create-client.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var createClientAction = (0, import_pieces_framework5.createAction)({
  auth: acuitySchedulingAuth,
  name: "create_client",
  classification: "WRITE",
  displayName: "Create Client",
  description: "Creates a new client.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new client record in Acuity from a first and last name, with optional phone, email, and notes. Use to add a contact to the client list before or independent of booking. Not idempotent \u2014 repeating creates duplicate client records since there is no dedupe on identity.",
    idempotent: !1
  },
  props: {
    firstName: import_pieces_framework5.Property.ShortText({
      displayName: "First Name",
      description: "Client's first name.",
      required: !0
    }),
    lastName: import_pieces_framework5.Property.ShortText({
      displayName: "Last Name",
      description: "Client's last name.",
      required: !0
    }),
    phone: import_pieces_framework5.Property.ShortText({
      displayName: "Phone",
      description: "Client's phone number.",
      required: !1
    }),
    email: import_pieces_framework5.Property.ShortText({
      displayName: "Email",
      description: "Client's email address.",
      required: !1
    }),
    notes: import_pieces_framework5.Property.LongText({
      displayName: "Notes",
      description: "Notes about the client.",
      required: !1
    })
  },
  async run(context) {
    let props = context.propsValue, body = {
      firstName: props.firstName,
      lastName: props.lastName
    };
    return props.phone && (body.phone = props.phone), props.email && (body.email = props.email), props.notes && (body.notes = props.notes), (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: `${API_URL}/clients`,
      body,
      authentication: {
        type: import_pieces_common4.AuthenticationType.BEARER_TOKEN,
        token: context.auth.access_token
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/acuity-scheduling/src/lib/actions/update-client.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var updateClientAction = (0, import_pieces_framework6.createAction)({
  auth: acuitySchedulingAuth,
  name: "update_client",
  classification: "WRITE",
  displayName: "Update Client",
  description: "Updates an existing client.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing Acuity client's details (name, email, phone, notes), identifying the client by their current first and last name (and optionally current phone to disambiguate). At least one new field must be supplied. Use to edit a known client rather than create one. Idempotent \u2014 applying the same field overwrites repeatedly yields the same client state.",
    idempotent: !0
  },
  props: {
    currentFirstName: import_pieces_framework6.Property.ShortText({
      displayName: "Current First Name (Identifier)",
      description: "The current first name of the client to update.",
      required: !0
    }),
    currentLastName: import_pieces_framework6.Property.ShortText({
      displayName: "Current Last Name (Identifier)",
      description: "The current last name of the client to update.",
      required: !0
    }),
    currentPhone: import_pieces_framework6.Property.ShortText({
      displayName: "Current Phone (Identifier, Optional)",
      description: "The current phone number of the client to update. Helps identify the client if names are not unique.",
      required: !1
    }),
    newFirstName: import_pieces_framework6.Property.ShortText({
      displayName: "New First Name",
      description: "Client's new first name. Leave blank to keep current.",
      required: !1
    }),
    newLastName: import_pieces_framework6.Property.ShortText({
      displayName: "New Last Name",
      description: "Client's new last name. Leave blank to keep current.",
      required: !1
    }),
    newEmail: import_pieces_framework6.Property.ShortText({
      displayName: "New Email",
      description: "Client's new email address. Leave blank to keep current.",
      required: !1
    }),
    newPhone: import_pieces_framework6.Property.ShortText({
      displayName: "New Phone",
      description: "Client's new phone number. Leave blank to keep current.",
      required: !1
    }),
    newNotes: import_pieces_framework6.Property.LongText({
      displayName: "New Notes",
      description: "New notes about the client. Leave blank to keep current.",
      required: !1
    })
  },
  async run(context) {
    let props = context.propsValue, queryParams = {
      firstName: props.currentFirstName,
      lastName: props.currentLastName
    };
    props.currentPhone && (queryParams.phone = props.currentPhone);
    let body = {};
    if (props.newFirstName && (body.firstName = props.newFirstName), props.newLastName && (body.lastName = props.newLastName), props.newEmail && (body.email = props.newEmail), props.newPhone && (body.phone = props.newPhone), props.newNotes && (body.notes = props.newNotes), Object.keys(body).length === 0)
      throw new Error(
        "At least one field to update (New First Name, New Last Name, etc.) must be provided."
      );
    return (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.PUT,
      url: `${API_URL}/clients`,
      queryParams,
      body,
      authentication: {
        type: import_pieces_common5.AuthenticationType.BEARER_TOKEN,
        token: context.auth.access_token
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/acuity-scheduling/src/lib/actions/add-blocked-time.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var addBlockedTimeAction = (0, import_pieces_framework7.createAction)({
  auth: acuitySchedulingAuth,
  name: "add_blocked_time",
  classification: "WRITE",
  displayName: "Add Blocked Off Time",
  description: "Block off a specific time range on a calendar.",
  audience: "both",
  aiMetadata: {
    description: "Marks a time range on a specific Acuity calendar as unavailable for booking, optionally with notes. Use to reserve or hold off slots (e.g. breaks, holidays). Requires start time, end time, and a numeric calendar ID; end must be after start. Not idempotent \u2014 each call creates a new block.",
    idempotent: !1
  },
  props: {
    start: import_pieces_framework7.Property.DateTime({
      displayName: "Start Time",
      description: "The start date and time for the block (ISO 8601 format).",
      required: !0
    }),
    end: import_pieces_framework7.Property.DateTime({
      displayName: "End Time",
      description: "The end date and time for the block (ISO 8601 format).",
      required: !0
    }),
    calendarID: calendarIdDropdown({
      displayName: "Calendar ID",
      description: "The numeric ID of the calendar to add this block to.",
      required: !0
    }),
    notes: import_pieces_framework7.Property.LongText({
      displayName: "Notes",
      description: "Optional notes for the blocked off time.",
      required: !1
    })
  },
  async run(context) {
    let props = context.propsValue;
    if (new Date(props.start) >= new Date(props.end))
      throw new Error("End time must be after start time.");
    let body = {
      start: props.start,
      end: props.end,
      calendarID: props.calendarID
    };
    return props.notes && (body.notes = props.notes), (await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.POST,
      url: `${API_URL}/blocks`,
      body,
      authentication: {
        type: import_pieces_common6.AuthenticationType.BEARER_TOKEN,
        token: context.auth.access_token
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/acuity-scheduling/src/lib/actions/find-appointments.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var findAppointmentAction = (0, import_pieces_framework8.createAction)({
  auth: acuitySchedulingAuth,
  name: "find_appointment",
  classification: "SEARCH",
  displayName: "Find Appointment(s)",
  description: "Find appointments based on various criteria, including client information.",
  audience: "both",
  aiMetadata: {
    description: "Searches Acuity appointments and returns matches with a found flag. Filter by client name/email/phone, date range, calendar, appointment type, and status (scheduled, canceled, or all); providing no filters fetches all appointments up to the result limit rather than a targeted match. Use to look up existing appointments or resolve an appointment ID. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    // Client Info Filters
    firstName: import_pieces_framework8.Property.ShortText({
      displayName: "Client First Name",
      description: "Filter appointments by client first name.",
      required: !1
    }),
    lastName: import_pieces_framework8.Property.ShortText({
      displayName: "Client Last Name",
      description: "Filter appointments by client last name.",
      required: !1
    }),
    email: import_pieces_framework8.Property.ShortText({
      displayName: "Client Email",
      description: "Filter appointments by client e-mail address.",
      required: !1
    }),
    phone: import_pieces_framework8.Property.ShortText({
      displayName: "Client Phone",
      description: "Filter appointments by client phone number. URL encode '+' if using country codes (e.g., %2B1234567890).",
      required: !1
    }),
    // Date Filters
    minDate: import_pieces_framework8.Property.DateTime({
      displayName: "Min Date",
      description: "Only get appointments on or after this date.",
      required: !1
    }),
    maxDate: import_pieces_framework8.Property.DateTime({
      displayName: "Max Date",
      description: "Only get appointments on or before this date.",
      required: !1
    }),
    // Other Filters
    calendarID: calendarIdDropdown({
      displayName: "Calendar ID",
      description: "Show only appointments on the calendar with this ID.",
      required: !1
    }),
    appointmentTypeID: appointmentTypeIdDropdown({
      displayName: "Appointment Type",
      description: "Show only appointments of this type.",
      required: !1
    }),
    status: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Appointment Status",
      description: "Filter by appointment status.",
      required: !1,
      defaultValue: "scheduled",
      options: {
        options: [
          { label: "Scheduled", value: "scheduled" },
          { label: "Canceled", value: "canceled" },
          { label: "All (Scheduled & Canceled)", value: "all" }
        ]
      }
    }),
    // Result Control
    maxResults: import_pieces_framework8.Property.Number({
      displayName: "Max Results",
      description: "Maximum number of results to return (default 100).",
      required: !1
    }),
    direction: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Sort Direction",
      description: "Sort direction for the results.",
      required: !1,
      defaultValue: "DESC",
      options: {
        options: [
          { label: "Descending (DESC)", value: "DESC" },
          { label: "Ascending (ASC)", value: "ASC" }
        ]
      }
    })
  },
  async run(context) {
    let props = context.propsValue, queryParams = {};
    props.firstName && (queryParams.firstName = props.firstName), props.lastName && (queryParams.lastName = props.lastName), props.email && (queryParams.email = props.email), props.phone && (queryParams.phone = props.phone), props.minDate && (queryParams.minDate = props.minDate.split("T")[0]), props.maxDate && (queryParams.maxDate = props.maxDate.split("T")[0]), props.calendarID && (queryParams.calendarID = props.calendarID.toString()), props.appointmentTypeID && (queryParams.appointmentTypeID = props.appointmentTypeID.toString()), props.status === "canceled" ? queryParams.canceled = "true" : props.status === "all" && (queryParams.showall = "true"), props.maxResults && (queryParams.max = props.maxResults.toString()), props.direction && (queryParams.direction = props.direction), !props.firstName && !props.lastName && !props.email && !props.phone && !props.calendarID && !props.appointmentTypeID && console.warn(
      "Acuity Scheduling 'Find Appointments': No specific client or calendar/type filters provided. This might return a large number of appointments up to the maximum limit."
    );
    let response = await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.GET,
      url: `${API_URL}/appointments`,
      queryParams,
      authentication: {
        type: import_pieces_common7.AuthenticationType.BEARER_TOKEN,
        token: context.auth.access_token
      }
    });
    return {
      found: response.body.length > 0,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/acuity-scheduling/src/lib/actions/find-client.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var findClientAction = (0, import_pieces_framework9.createAction)({
  auth: acuitySchedulingAuth,
  name: "find_client",
  classification: "SEARCH",
  displayName: "Find Client",
  description: "Finds client based on seach term.",
  audience: "both",
  aiMetadata: {
    description: "Searches the Acuity client list by a required search term matched against first name, last name, or phone number, returning matches with a found flag. Use to look up an existing client before booking or updating. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    search: import_pieces_framework9.Property.ShortText({
      displayName: "Search Term",
      description: "Filter client list by first name, last name, or phone number.",
      required: !0
    })
  },
  async run(context) {
    let props = context.propsValue, queryParams = {};
    props.search && (queryParams.search = props.search);
    let response = await import_pieces_common8.httpClient.sendRequest({
      method: import_pieces_common8.HttpMethod.GET,
      url: `${API_URL}/clients`,
      queryParams,
      authentication: {
        type: import_pieces_common8.AuthenticationType.BEARER_TOKEN,
        token: context.auth.access_token
      }
    });
    return {
      found: response.body.length > 0,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/acuity-scheduling/src/index.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/acuity-scheduling/src/lib/triggers/appointment-scheduled.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var TRIGGER_KEY = "trigger_new_appointment", appointmentScheduledTrigger = (0, import_pieces_framework10.createTrigger)({
  auth: acuitySchedulingAuth,
  name: "new_appointment",
  classification: "READ",
  displayName: "New Appointment",
  description: "Triggers when a new appointment is scheduled.",
  aiMetadata: {
    description: "Fires when a new appointment is scheduled in Acuity, emitting the new appointment details. Can be scoped to a specific calendar and/or appointment type; events outside the configured scope are ignored."
  },
  props: {
    calendarId: calendarIdDropdown({
      displayName: "Calendar",
      required: !1
    }),
    appointmentTypeId: appointmentTypeIdDropdown({
      displayName: "Appointment Type",
      required: !1
    })
  },
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let request = {
      method: import_pieces_common9.HttpMethod.POST,
      url: `${API_URL}/webhooks`,
      body: {
        target: context.webhookUrl,
        event: "appointment.scheduled"
      },
      authentication: {
        type: import_pieces_common9.AuthenticationType.BEARER_TOKEN,
        token: context.auth.access_token
      }
    }, response = await import_pieces_common9.httpClient.sendRequest(request);
    await context.store.put(TRIGGER_KEY, response.body.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(TRIGGER_KEY);
    if (webhookId) {
      let request = {
        method: import_pieces_common9.HttpMethod.DELETE,
        url: `${API_URL}/webhooks/${webhookId}`,
        authentication: {
          type: import_pieces_common9.AuthenticationType.BEARER_TOKEN,
          token: context.auth.access_token
        }
      };
      await import_pieces_common9.httpClient.sendRequest(request), await context.store.delete(TRIGGER_KEY);
    }
  },
  async test(context) {
    let { calendarId, appointmentTypeId } = context.propsValue, qs = {
      max: "10"
    };
    return calendarId && (qs.calendarID = calendarId.toString()), appointmentTypeId && (qs.appointmentTypeID = appointmentTypeId.toString()), (await import_pieces_common9.httpClient.sendRequest({
      method: import_pieces_common9.HttpMethod.GET,
      url: `${API_URL}/appointments`,
      queryParams: qs,
      authentication: {
        type: import_pieces_common9.AuthenticationType.BEARER_TOKEN,
        token: context.auth.access_token
      }
    })).body;
  },
  async run(context) {
    let { calendarId, appointmentTypeId } = context.propsValue, payload = context.payload.body;
    if (payload.action === "appointment.scheduled" && payload.id && (!calendarId || calendarId === payload.calendarID) && (!appointmentTypeId || appointmentTypeId === payload.appointmentTypeID))
      try {
        return [await getAppointmentDetails(
          payload.id.toString(),
          context.auth.access_token
        )];
      } catch (error) {
        return console.error(`Failed to fetch appointment details for ID ${payload.id}:`, error), [];
      }
    else
      return console.log("Received webhook for non-scheduled event or missing ID:", payload.action), [];
  },
  sampleData: {
    id: 12345,
    firstName: "John",
    lastName: "Doe",
    email: "john.doe@example.com",
    phone: "555-1234",
    date: "2023-12-01",
    time: "10:00 AM",
    datetime: "2023-12-01T10:00:00-0500",
    endTime: "11:00 AM",
    datetimeCreated: "2023-11-28T14:30:00-0500",
    appointmentTypeID: 101,
    calendarID: 1,
    notes: "First appointment.",
    price: "50.00",
    paid: "yes",
    status: "scheduled"
  }
});

// ../ap/packages/pieces/community/acuity-scheduling/src/lib/triggers/appointment-canceled.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var TRIGGER_KEY2 = "trigger_appointment_canceled", appointmentCanceledTrigger = (0, import_pieces_framework11.createTrigger)({
  auth: acuitySchedulingAuth,
  name: "appointment_canceled",
  classification: "READ",
  displayName: "Appointment Canceled",
  description: "Triggers when an appointment is canceled.",
  aiMetadata: {
    description: "Fires when an appointment is canceled in Acuity, emitting the canceled appointment details. Can be scoped to a specific calendar and/or appointment type; events outside the configured scope are ignored."
  },
  props: {
    calendarId: calendarIdDropdown({
      displayName: "Calendar",
      required: !1
    }),
    appointmentTypeId: appointmentTypeIdDropdown({
      displayName: "Appointment Type",
      required: !1
    })
  },
  type: import_pieces_framework11.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let request = {
      method: import_pieces_common10.HttpMethod.POST,
      url: `${API_URL}/webhooks`,
      body: {
        target: context.webhookUrl,
        event: "appointment.canceled"
      },
      authentication: {
        type: import_pieces_common10.AuthenticationType.BEARER_TOKEN,
        token: context.auth.access_token
      }
    }, response = await import_pieces_common10.httpClient.sendRequest(request);
    await context.store.put(TRIGGER_KEY2, response.body.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(TRIGGER_KEY2);
    if (webhookId) {
      let request = {
        method: import_pieces_common10.HttpMethod.DELETE,
        url: `${API_URL}/webhooks/${webhookId}`,
        authentication: {
          type: import_pieces_common10.AuthenticationType.BEARER_TOKEN,
          token: context.auth.access_token
        }
      };
      await import_pieces_common10.httpClient.sendRequest(request), await context.store.delete(TRIGGER_KEY2);
    }
  },
  async test(context) {
    let { calendarId, appointmentTypeId } = context.propsValue, qs = {
      max: "10",
      canceled: "true"
    };
    return calendarId && (qs.calendarID = calendarId.toString()), appointmentTypeId && (qs.appointmentTypeID = appointmentTypeId.toString()), (await import_pieces_common10.httpClient.sendRequest({
      method: import_pieces_common10.HttpMethod.GET,
      url: `${API_URL}/appointments`,
      queryParams: qs,
      authentication: {
        type: import_pieces_common10.AuthenticationType.BEARER_TOKEN,
        token: context.auth.access_token
      }
    })).body;
  },
  async run(context) {
    let { calendarId, appointmentTypeId } = context.propsValue, payload = context.payload.body;
    if (payload.action === "appointment.canceled" && payload.id && (!calendarId || calendarId === payload.calendarID) && (!appointmentTypeId || appointmentTypeId === payload.appointmentTypeID))
      try {
        return [await getAppointmentDetails(
          payload.id.toString(),
          context.auth.access_token
        )];
      } catch (error) {
        return console.error(`Failed to fetch appointment details for ID ${payload.id}:`, error), [];
      }
    else
      return console.log("Received webhook for non-canceled event or missing ID:", payload.action), [];
  },
  sampleData: {
    id: 67890,
    firstName: "Jane",
    lastName: "Smith",
    email: "jane.smith@example.com",
    phone: "555-5678",
    date: "2023-12-05",
    time: "02:00 PM",
    datetime: "2023-12-05T14:00:00-0500",
    endTime: "03:00 PM",
    datetimeCreated: "2023-11-30T10:15:00-0500",
    appointmentTypeID: 102,
    calendarID: 2,
    notes: "Follow-up meeting.",
    price: "75.00",
    paid: "no",
    status: "canceled",
    noShow: !1
  }
});

// ../ap/packages/pieces/community/acuity-scheduling/src/index.ts
var acuityScheduling = (0, import_pieces_framework12.createPiece)({
  displayName: "Acuity Scheduling",
  logoUrl: "https://cdn.activepieces.com/pieces/acuity-scheduling.png",
  auth: acuitySchedulingAuth,
  categories: [import_pieces_framework13.PieceCategory.PRODUCTIVITY, import_pieces_framework13.PieceCategory.SALES_AND_CRM],
  minimumSupportedRelease: "0.36.1",
  authors: ["onyedikachi-david", "kishanprmr"],
  actions: [
    addBlockedTimeAction,
    createAppointmentAction,
    createClientAction,
    rescheduleAppointmentAction,
    updateClientAction,
    findAppointmentAction,
    findClientAction,
    (0, import_pieces_common11.createCustomApiCallAction)({
      auth: acuitySchedulingAuth,
      baseUrl: () => API_URL,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.access_token}`
      })
    })
  ],
  triggers: [appointmentCanceledTrigger, appointmentScheduledTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  acuityScheduling
});
