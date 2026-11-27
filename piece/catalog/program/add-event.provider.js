"use strict";
var __defProp = Object.defineProperty, __defProps = Object.defineProperties, __getOwnPropDesc = Object.getOwnPropertyDescriptor, __getOwnPropDescs = Object.getOwnPropertyDescriptors, __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
}, __spreadProps = (a, b) => __defProps(a, __getOwnPropDescs(b));
var __objRest = (source, exclude) => {
  var target = {};
  for (var prop in source)
    __hasOwnProp.call(source, prop) && exclude.indexOf(prop) < 0 && (target[prop] = source[prop]);
  if (source != null && __getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(source))
      exclude.indexOf(prop) < 0 && __propIsEnum.call(source, prop) && (target[prop] = source[prop]);
  return target;
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

// ../ap/packages/pieces/community/add-event/src/index.ts
var index_exports = {};
__export(index_exports, {
  addEvent: () => addEvent
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common14 = require("@activepieces/pieces-common"), import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_framework14 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/add-event/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/add-event/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
var BASE_URL = "https://api.addevent.com/calevent/v2", MAX_PAGE_SIZE = 20, MAX_POLL_PAGES = 100;
async function apiCall({
  apiKey,
  method,
  resourceUri,
  query,
  body
}) {
  let queryParams = {};
  if (query)
    for (let [key, value] of Object.entries(query))
      value != null && (queryParams[key] = Array.isArray(value) ? value.join(",") : String(value));
  let request = {
    method,
    url: `${BASE_URL}${resourceUri}`,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token: apiKey
    },
    queryParams,
    body: compactBody(body)
  };
  return (await import_pieces_common.httpClient.sendRequest(request)).body;
}
async function getAllPages({
  apiKey,
  resourceUri,
  select,
  query,
  maxPages
}) {
  let results = [], page = 1, totalPages = 1;
  do {
    let response = await apiCall({
      apiKey,
      method: import_pieces_common.HttpMethod.GET,
      resourceUri,
      query: __spreadProps(__spreadValues({}, query), { page, page_size: MAX_PAGE_SIZE })
    });
    results.push(...select(response)), totalPages = response.pagination.total_pages, page += 1;
  } while (page <= totalPages && (maxPages === void 0 || page <= maxPages));
  return results;
}
async function getItemsSince({
  apiKey,
  resourceUri,
  select,
  getCreated,
  sinceEpochMs,
  query,
  maxPages = MAX_POLL_PAGES
}) {
  let results = [], page = 1, totalPages = 1, reachedCutoff = !1;
  do {
    let response = await apiCall({
      apiKey,
      method: import_pieces_common.HttpMethod.GET,
      resourceUri,
      query: __spreadProps(__spreadValues({}, query), {
        page,
        page_size: MAX_PAGE_SIZE,
        sort_by: "created",
        sort_order: "desc"
      })
    });
    for (let item of select(response))
      toEpochMs(getCreated(item)) > sinceEpochMs ? results.push(item) : reachedCutoff = !0;
    totalPages = response.pagination.total_pages, page += 1;
  } while (!reachedCutoff && page <= totalPages && page <= maxPages);
  return results;
}
function toEpochMs(created) {
  return new Date(created.replace(" ", "T")).getTime();
}
function compactBody(body) {
  if (body === null || typeof body != "object" || Array.isArray(body))
    return body;
  let entries = Object.entries(body).filter(
    ([, value]) => !isNilOrEmptyObject(value)
  );
  return Object.fromEntries(entries);
}
function isNilOrEmptyObject(value) {
  return value == null ? !0 : typeof value == "object" && !Array.isArray(value) && Object.keys(value).length === 0;
}
var addEventApi = {
  baseUrl: BASE_URL,
  maxPageSize: MAX_PAGE_SIZE,
  call: apiCall,
  getAllPages,
  getItemsSince,
  toEpochMs
};

// ../ap/packages/pieces/community/add-event/src/lib/auth.ts
var markdownDescription = `
**Authenticate with your AddEvent API key.**

1. Sign in to your AddEvent account at https://dashboard.addevent.com.
2. Open **Account \u2192 Settings** (https://dashboard.addevent.com/account/settings).
3. Copy your **API key** and paste it below.

Note: API access requires a paid AddEvent plan \u2014 the free Hobby plan does not support the API.
`, addEventAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: markdownDescription,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await addEventApi.call({
        apiKey: auth,
        method: import_pieces_common2.HttpMethod.GET,
        resourceUri: "/calendars",
        query: { page_size: 1 }
      }), { valid: !0 };
    } catch (e) {
      return e instanceof import_pieces_common2.HttpError && e.response.status === 403 ? {
        valid: !1,
        error: "This API key is valid, but your AddEvent plan does not allow API access. The free Hobby plan has no API access \u2014 upgrade your plan to use this integration."
      } : {
        valid: !1,
        error: "Invalid API key. Please check the key in your AddEvent dashboard and try again."
      };
    }
  }
});

// ../ap/packages/pieces/community/add-event/src/lib/actions/create-event.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/add-event/src/lib/common/props.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var calendarId = ({ required = !0 } = {}) => import_pieces_framework2.Property.Dropdown({
  displayName: "Calendar",
  description: "The calendar to use.",
  auth: addEventAuth,
  required,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your AddEvent account first."
      };
    try {
      let calendars = await addEventApi.getAllPages({
        apiKey: auth.secret_text,
        resourceUri: "/calendars",
        select: (page) => {
          var _a;
          return (_a = page.calendars) != null ? _a : [];
        }
      });
      return calendars.length === 0 ? {
        disabled: !1,
        options: [],
        placeholder: "No calendars found in your AddEvent account."
      } : {
        disabled: !1,
        options: calendars.map((calendar) => ({
          label: calendar.is_default_calendar ? `${calendar.title} (Default)` : calendar.title,
          value: calendar.id
        }))
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Could not load calendars. Check your connection."
      };
    }
  }
}), eventId = ({ required = !0 } = {}) => import_pieces_framework2.Property.Dropdown({
  displayName: "Event",
  description: "Start typing to search your events by title.",
  auth: addEventAuth,
  required,
  refreshers: [],
  options: async ({ auth, searchValue }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your AddEvent account first."
      };
    try {
      let search = typeof searchValue == "string" ? searchValue : void 0, events = (_a = (await addEventApi.call({
        apiKey: auth.secret_text,
        method: import_pieces_common3.HttpMethod.GET,
        resourceUri: "/events",
        query: {
          search,
          page_size: addEventApi.maxPageSize,
          sort_by: "datetime_start",
          sort_order: "desc"
        }
      })).events) != null ? _a : [];
      return events.length === 0 ? {
        disabled: !1,
        options: [],
        placeholder: "No events found."
      } : {
        disabled: !1,
        options: events.map((event) => ({
          label: `${event.title} (${event.datetime_start})`,
          value: event.id
        }))
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Could not load events. Check your connection."
      };
    }
  }
}), subscriberId = ({ required = !0 } = {}) => import_pieces_framework2.Property.Dropdown({
  displayName: "Subscriber",
  description: "Pick a calendar above to narrow this list.",
  auth: addEventAuth,
  required,
  refreshers: ["calendar_id"],
  options: async ({ auth, calendar_id }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your AddEvent account first."
      };
    try {
      let calId = typeof calendar_id == "string" ? calendar_id : void 0, subscribers = await addEventApi.getAllPages({
        apiKey: auth.secret_text,
        resourceUri: "/subscribers",
        select: (page) => {
          var _a;
          return (_a = page.subscribers) != null ? _a : [];
        },
        query: calId ? { calendar_ids: [calId] } : void 0,
        maxPages: 10
      });
      return subscribers.length === 0 ? {
        disabled: !1,
        options: [],
        placeholder: "No subscribers found."
      } : {
        disabled: !1,
        options: subscribers.map((subscriber) => {
          var _a;
          let data = (_a = subscriber.subscriber_form_data) != null ? _a : {}, emailValue = data.email, nameValue = data.name;
          return {
            label: `${typeof emailValue == "string" && emailValue || typeof nameValue == "string" && nameValue || subscriber.id} (${subscriber.calendar_type})`,
            value: subscriber.id
          };
        })
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Could not load subscribers. Check your connection."
      };
    }
  }
}), timezone = ({ required = !1 } = {}) => import_pieces_framework2.Property.Dropdown({
  displayName: "Timezone",
  description: "The event's timezone. Defaults to the calendar's timezone if left blank.",
  auth: addEventAuth,
  required,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your AddEvent account first."
      };
    try {
      return {
        disabled: !1,
        options: (await addEventApi.call({
          apiKey: auth.secret_text,
          method: import_pieces_common3.HttpMethod.GET,
          resourceUri: "/timezones"
        })).map((tz) => ({
          label: `${tz.name} (UTC${tz.utc_offset_hours})`,
          value: tz.name
        }))
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Could not load timezones. Check your connection."
      };
    }
  }
}), eventFields = ({ mode }) => {
  let isCreate = mode === "create";
  return {
    title: import_pieces_framework2.Property.ShortText({
      displayName: "Title",
      description: "The event title.",
      required: isCreate
    }),
    calendar_id: calendarId({ required: !1 }),
    datetime_start: import_pieces_framework2.Property.ShortText({
      displayName: "Start Date/Time",
      description: "Format: 'YYYY-MM-DD HH:mm:ss' (24-hour), interpreted in the event's timezone. Example: 2025-06-15 14:30:00. A date alone (YYYY-MM-DD) is also accepted.",
      required: isCreate
    }),
    datetime_end: import_pieces_framework2.Property.ShortText({
      displayName: "End Date/Time",
      description: "Same format as the start. Defaults to one hour after the start.",
      required: !1
    }),
    all_day_event: isCreate ? import_pieces_framework2.Property.Checkbox({
      displayName: "All-day Event",
      required: !1,
      defaultValue: !1
    }) : import_pieces_framework2.Property.StaticDropdown({
      displayName: "All-day Event",
      description: "Leave blank to keep the current value.",
      required: !1,
      options: {
        options: [
          { label: "Yes", value: !0 },
          { label: "No", value: !1 }
        ]
      }
    }),
    timezone: timezone({ required: !1 }),
    description: import_pieces_framework2.Property.LongText({
      displayName: "Description",
      description: "Plain text or HTML (around 500 characters recommended).",
      required: !1
    }),
    location: import_pieces_framework2.Property.ShortText({
      displayName: "Location",
      description: "A physical address or a URL. Leave blank if you set a saved Location ID instead.",
      required: !1
    }),
    location_id: import_pieces_framework2.Property.Number({
      displayName: "Saved Location ID",
      description: "The ID of a saved location in AddEvent. Use this instead of Location.",
      required: !1
    }),
    organizer_name: import_pieces_framework2.Property.ShortText({
      displayName: "Organizer Name",
      description: "Required if you set an organizer email.",
      required: !1
    }),
    organizer_email: import_pieces_framework2.Property.ShortText({
      displayName: "Organizer Email",
      description: "Required if you set an organizer name.",
      required: !1
    }),
    reminder: import_pieces_framework2.Property.Number({
      displayName: "Reminder (minutes before)",
      description: "Minutes before the event to remind attendees (0\u201310800).",
      required: !1,
      defaultValue: isCreate ? 30 : void 0
    }),
    color: import_pieces_framework2.Property.Number({
      displayName: "Color",
      description: "Color from the calendar palette (1\u201320).",
      required: !1,
      defaultValue: isCreate ? 1 : void 0
    }),
    free_busy: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Show As",
      description: "How the event appears in attendees' calendars.",
      required: !1,
      defaultValue: isCreate ? "default" : void 0,
      options: {
        options: [
          { label: "Default", value: "default" },
          { label: "Busy", value: "busy" },
          { label: "Free", value: "free" }
        ]
      }
    }),
    recurring_rule: import_pieces_framework2.Property.ShortText({
      displayName: "Recurrence Rule",
      description: "An iCalendar RRULE, e.g. FREQ=WEEKLY;BYDAY=MO. Leave blank for a one-time event.",
      required: !1
    }),
    internal_name: import_pieces_framework2.Property.ShortText({
      displayName: "Internal Name",
      description: "A private label for your reference; not shown to attendees.",
      required: !1
    }),
    landing_page_template_id: import_pieces_framework2.Property.ShortText({
      displayName: "Landing Page Template ID",
      description: "A custom landing page template ID, or 'default'.",
      required: !1
    }),
    rsvp_enabled: isCreate ? import_pieces_framework2.Property.Checkbox({
      displayName: "Enable RSVP",
      required: !1,
      defaultValue: !1
    }) : import_pieces_framework2.Property.StaticDropdown({
      displayName: "Enable RSVP",
      description: "Leave blank to keep the current value.",
      required: !1,
      options: {
        options: [
          { label: "Yes", value: !0 },
          { label: "No", value: !1 }
        ]
      }
    }),
    rsvp_form_id: import_pieces_framework2.Property.ShortText({
      displayName: "RSVP Form ID",
      description: "A custom RSVP form ID, or 'default'. Only used when RSVP is enabled.",
      required: !1
    }),
    custom_data: import_pieces_framework2.Property.Object({
      displayName: "Custom Data",
      description: "Arbitrary key-value pairs (use snake_case keys).",
      required: !1
    })
  };
}, addEventProps = {
  calendarId,
  eventId,
  subscriberId,
  timezone,
  eventFields
};

// ../ap/packages/pieces/community/add-event/src/lib/actions/create-event.ts
var addEventCreateEventAction = (0, import_pieces_framework3.createAction)({
  auth: addEventAuth,
  name: "create_event",
  classification: "WRITE",
  displayName: "Create Event",
  description: "Creates a new event on your AddEvent calendar.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new event on an AddEvent calendar from the supplied title, schedule, and details. Use when an agent needs to add a brand-new calendar event. Not idempotent: each call creates a separate event, so repeating it produces duplicates; to avoid that, use Find or Create Event instead.",
    idempotent: !1
  },
  props: __spreadValues({}, addEventProps.eventFields({ mode: "create" })),
  async run(context) {
    return await addEventApi.call({
      apiKey: context.auth.secret_text,
      method: import_pieces_common4.HttpMethod.POST,
      resourceUri: "/events",
      body: __spreadValues({}, context.propsValue)
    });
  }
});

// ../ap/packages/pieces/community/add-event/src/lib/actions/update-event.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var addEventUpdateEventAction = (0, import_pieces_framework4.createAction)({
  auth: addEventAuth,
  name: "update_event",
  classification: "WRITE",
  displayName: "Update Event",
  description: "Updates an event on your AddEvent calendar. Only the fields you fill in are changed.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing AddEvent event identified by its event ID, applying a partial change where only the fields you provide are modified. Use when an agent needs to edit details of an event that already exists. Requires a valid event ID; idempotent, since reapplying the same field values to the same event leaves it in the same state.",
    idempotent: !0
  },
  props: __spreadValues({
    event_id: addEventProps.eventId({ required: !0 })
  }, addEventProps.eventFields({ mode: "update" })),
  async run(context) {
    let _a = context.propsValue, { event_id } = _a, body = __objRest(_a, ["event_id"]);
    return await addEventApi.call({
      apiKey: context.auth.secret_text,
      method: import_pieces_common5.HttpMethod.PATCH,
      resourceUri: `/events/${event_id}`,
      body
    });
  }
});

// ../ap/packages/pieces/community/add-event/src/lib/actions/delete-event.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");
var addEventDeleteEventAction = (0, import_pieces_framework5.createAction)({
  auth: addEventAuth,
  name: "delete_event",
  classification: "DESTRUCTIVE",
  displayName: "Delete Event",
  description: "Deletes an event on your AddEvent calendar.",
  audience: "both",
  aiMetadata: {
    description: "Permanently deletes an AddEvent event identified by its event ID. Use when an agent needs to remove an event entirely. Requires a valid event ID; idempotent, since once the event is deleted, repeating the call leaves it absent.",
    idempotent: !0
  },
  props: {
    event_id: addEventProps.eventId({ required: !0 })
  },
  async run(context) {
    let { event_id } = context.propsValue;
    return await addEventApi.call({
      apiKey: context.auth.secret_text,
      method: import_pieces_common6.HttpMethod.DELETE,
      resourceUri: `/events/${event_id}`
    }), { success: !0, id: event_id };
  }
});

// ../ap/packages/pieces/community/add-event/src/lib/actions/find-event.ts
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var addEventFindEventAction = (0, import_pieces_framework6.createAction)({
  auth: addEventAuth,
  name: "find_event",
  classification: "SEARCH",
  displayName: "Find Event",
  description: "Finds events matching a search term. Returns up to 20 of the best matches.",
  audience: "both",
  aiMetadata: {
    description: "Searches AddEvent events by a required search term matched against title, internal name, description, and location, returning up to 20 matches. Use when an agent needs to look up existing events; optionally narrow by calendar and by an earliest/latest start datetime. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    search: import_pieces_framework6.Property.ShortText({
      displayName: "Search Term",
      description: "Searches the title, internal name, description, and location of your events.",
      required: !0
    }),
    calendar_id: addEventProps.calendarId({ required: !1 }),
    datetime_min: import_pieces_framework6.Property.ShortText({
      displayName: "Earliest Start",
      description: "Only return events starting on or after this time. Format: 'YYYY-MM-DD HH:mm:ss' or 'YYYY-MM-DD'.",
      required: !1
    }),
    datetime_max: import_pieces_framework6.Property.ShortText({
      displayName: "Latest Start",
      description: "Only return events starting on or before this time. Format: 'YYYY-MM-DD HH:mm:ss' or 'YYYY-MM-DD'.",
      required: !1
    })
  },
  async run(context) {
    var _a;
    let { search, calendar_id, datetime_min, datetime_max } = context.propsValue, events = (_a = (await addEventApi.call({
      apiKey: context.auth.secret_text,
      method: import_pieces_common7.HttpMethod.GET,
      resourceUri: "/events",
      query: {
        search,
        calendar_ids: calendar_id ? [calendar_id] : void 0,
        datetime_min,
        datetime_max,
        page_size: addEventApi.maxPageSize,
        sort_by: "datetime_start",
        sort_order: "desc"
      }
    })).events) != null ? _a : [];
    return {
      found: events.length > 0,
      events
    };
  }
});

// ../ap/packages/pieces/community/add-event/src/lib/actions/find-or-create-event.ts
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework");
var addEventFindOrCreateEventAction = (0, import_pieces_framework7.createAction)({
  auth: addEventAuth,
  name: "find_or_create_event",
  classification: "WRITE",
  displayName: "Find or Create Event",
  description: "Searches for an event by a term. If no match is found, creates a new event with the details below.",
  audience: "both",
  aiMetadata: {
    description: "Searches AddEvent events by a term and returns the first match; if none is found, creates a new event from the supplied details. Use when an agent wants an event to exist without risking a duplicate. Not strictly idempotent: the search term is not a guaranteed unique key, so concurrent or near-duplicate inputs can still create more than one event.",
    idempotent: !1
  },
  props: __spreadValues({
    search: import_pieces_framework7.Property.ShortText({
      displayName: "Search Term",
      description: "Searches the title, internal name, description, and location of your events. The first match is returned.",
      required: !0
    })
  }, addEventProps.eventFields({ mode: "create" })),
  async run(context) {
    var _b;
    let _a = context.propsValue, { search } = _a, createBody = __objRest(_a, ["search"]), apiKey = context.auth.secret_text, existing = (_b = (await addEventApi.call({
      apiKey,
      method: import_pieces_common8.HttpMethod.GET,
      resourceUri: "/events",
      query: {
        search,
        calendar_ids: createBody.calendar_id ? [createBody.calendar_id] : void 0,
        page_size: 1
      }
    })).events) == null ? void 0 : _b[0];
    return existing ? { found: !0, created: !1, event: existing } : { found: !1, created: !0, event: await addEventApi.call({
      apiKey,
      method: import_pieces_common8.HttpMethod.POST,
      resourceUri: "/events",
      body: createBody
    }) };
  }
});

// ../ap/packages/pieces/community/add-event/src/lib/actions/create-rsvp-attendee.ts
var import_pieces_common9 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework");
var addEventCreateRsvpAttendeeAction = (0, import_pieces_framework8.createAction)({
  auth: addEventAuth,
  name: "create_rsvp_attendee",
  classification: "WRITE",
  displayName: "Create RSVP Attendee",
  description: "Creates a new RSVP attendee on your AddEvent event.",
  audience: "both",
  aiMetadata: {
    description: "Adds an RSVP attendee (email, name, and response of going/maybe/not-going) to a specific AddEvent event. Use when an agent needs to register someone for an event. Requires a valid event ID; not idempotent, since each call creates another attendee record and, if notifications are enabled, sends a confirmation and organizer email.",
    idempotent: !1
  },
  props: {
    event_id: addEventProps.eventId({ required: !0 }),
    email: import_pieces_framework8.Property.ShortText({
      displayName: "Email",
      description: "The attendee's email address. Event updates and reminders are sent here.",
      required: !0
    }),
    name: import_pieces_framework8.Property.ShortText({
      displayName: "Name",
      description: "The attendee's name (required by the default RSVP form).",
      required: !0
    }),
    attending: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Attending",
      description: "The attendee's response.",
      required: !1,
      defaultValue: "going",
      options: {
        options: [
          { label: "Going", value: "going" },
          { label: "Maybe", value: "maybe" },
          { label: "Not Going", value: "not-going" }
        ]
      }
    }),
    notify: import_pieces_framework8.Property.Checkbox({
      displayName: "Send Notification Emails",
      description: "Send a confirmation email to the attendee and a notification to the organizer.",
      required: !1,
      defaultValue: !1
    }),
    rsvp_form_data: import_pieces_framework8.Property.Object({
      displayName: "Additional Form Fields",
      description: "Extra RSVP form values for custom forms, as key-value pairs. The name is set by the Name field above.",
      required: !1
    })
  },
  async run(context) {
    let { event_id, email, name, attending, notify, rsvp_form_data } = context.propsValue;
    return await addEventApi.call({
      apiKey: context.auth.secret_text,
      method: import_pieces_common9.HttpMethod.POST,
      resourceUri: `/events/${event_id}/rsvps`,
      body: {
        email,
        attending,
        notify: notify ? "active" : void 0,
        rsvp_form_data: __spreadValues({ name }, rsvp_form_data != null ? rsvp_form_data : {})
      }
    });
  }
});

// ../ap/packages/pieces/community/add-event/src/lib/actions/delete-calendar-subscriber.ts
var import_pieces_common10 = require("@activepieces/pieces-common"), import_pieces_framework9 = require("@activepieces/pieces-framework");
var addEventDeleteCalendarSubscriberAction = (0, import_pieces_framework9.createAction)({
  auth: addEventAuth,
  name: "delete_calendar_subscriber",
  classification: "DESTRUCTIVE",
  displayName: "Delete Calendar Subscriber",
  description: "Deletes a subscriber from your AddEvent calendar.",
  audience: "both",
  aiMetadata: {
    description: "Removes a subscriber from an AddEvent calendar, identified by subscriber ID. Use when an agent needs to unsubscribe someone from a calendar. Requires a valid subscriber ID; idempotent, since once removed, repeating the call leaves the subscriber absent.",
    idempotent: !0
  },
  props: {
    calendar_id: addEventProps.calendarId({ required: !1 }),
    subscriber_id: addEventProps.subscriberId({ required: !0 })
  },
  async run(context) {
    let { subscriber_id } = context.propsValue;
    return await addEventApi.call({
      apiKey: context.auth.secret_text,
      method: import_pieces_common10.HttpMethod.DELETE,
      resourceUri: `/subscribers/${subscriber_id}`
    }), { success: !0, id: subscriber_id };
  }
});

// ../ap/packages/pieces/community/add-event/src/lib/actions/create-add-to-calendar-links.ts
var import_pieces_common11 = require("@activepieces/pieces-common"), import_pieces_framework10 = require("@activepieces/pieces-framework");
var addEventCreateAddToCalendarLinksAction = (0, import_pieces_framework10.createAction)({
  auth: addEventAuth,
  name: "create_add_to_calendar_links",
  classification: "WRITE",
  displayName: "Create Dynamic Add to Calendar Links",
  description: 'Creates an AddEvent event and returns its shareable "Add to Calendar" links. Opening a link lets recipients add the event to Apple, Google, Outlook, and other calendars. Each run produces a unique link, so links can be personalized per recipient.',
  audience: "both",
  aiMetadata: {
    description: 'Creates an AddEvent event and returns shareable "Add to Calendar" links that let recipients add it to Apple, Google, Outlook, and other calendars. Use when an agent needs distributable per-recipient calendar links rather than a managed calendar entry. Requires a title and start datetime; not idempotent, since each call creates a new event and a unique set of links.',
    idempotent: !1
  },
  props: {
    calendar_id: addEventProps.calendarId({ required: !1 }),
    title: import_pieces_framework10.Property.ShortText({
      displayName: "Title",
      description: "The event title shown in the calendar.",
      required: !0
    }),
    datetime_start: import_pieces_framework10.Property.ShortText({
      displayName: "Start Date/Time",
      description: "Format: 'YYYY-MM-DD HH:mm:ss' (24-hour), interpreted in the event's timezone. Example: 2025-06-15 14:30:00. A date alone (YYYY-MM-DD) is also accepted.",
      required: !0
    }),
    datetime_end: import_pieces_framework10.Property.ShortText({
      displayName: "End Date/Time",
      description: "Same format as the start. Defaults to one hour after the start.",
      required: !1
    }),
    all_day_event: import_pieces_framework10.Property.Checkbox({
      displayName: "All-day Event",
      required: !1,
      defaultValue: !1
    }),
    timezone: addEventProps.timezone({ required: !1 }),
    description: import_pieces_framework10.Property.LongText({
      displayName: "Description",
      description: "Plain text or HTML (around 500 characters recommended).",
      required: !1
    }),
    location: import_pieces_framework10.Property.ShortText({
      displayName: "Location",
      description: "A physical address or a URL.",
      required: !1
    })
  },
  async run(context) {
    let event = await addEventApi.call({
      apiKey: context.auth.secret_text,
      method: import_pieces_common11.HttpMethod.POST,
      resourceUri: "/events",
      body: __spreadValues({}, context.propsValue)
    });
    return {
      id: event.id,
      title: event.title,
      link_long: event.link_long,
      link_short: event.link_short,
      datetime_start: event.datetime_start,
      datetime_end: event.datetime_end,
      timezone: event.timezone
    };
  }
});

// ../ap/packages/pieces/community/add-event/src/lib/triggers/new-calendar-subscriber.ts
var import_pieces_common12 = require("@activepieces/pieces-common"), import_pieces_framework11 = require("@activepieces/pieces-framework");
var props = {
  calendar_id: addEventProps.calendarId({ required: !1 })
}, polling = {
  strategy: import_pieces_common12.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue, lastFetchEpochMS }) => (await addEventApi.getItemsSince({
    apiKey: auth.secret_text,
    resourceUri: "/subscribers",
    select: (page) => {
      var _a;
      return (_a = page.subscribers) != null ? _a : [];
    },
    getCreated: (subscriber) => subscriber.created,
    sinceEpochMs: lastFetchEpochMS,
    query: propsValue.calendar_id ? { calendar_ids: [propsValue.calendar_id] } : void 0
  })).map((subscriber) => ({
    epochMilliSeconds: addEventApi.toEpochMs(subscriber.created),
    data: subscriber
  }))
}, addEventNewCalendarSubscriberTrigger = (0, import_pieces_framework11.createTrigger)({
  auth: addEventAuth,
  name: "new_calendar_subscriber",
  classification: "READ",
  displayName: "New Calendar Subscriber",
  description: "Triggers when a new subscriber is added to your AddEvent calendar. Leave the calendar blank to watch all of them.",
  aiMetadata: {
    description: "Fires when a new subscriber is added to an AddEvent calendar, representing a person who has subscribed to receive the calendar. Watches a single calendar when one is specified, or every calendar when left blank."
  },
  type: import_pieces_framework11.TriggerStrategy.POLLING,
  props,
  sampleData: {
    id: "sub_abc123",
    calendar_id: "cal_abc123",
    subscriber_status: "active",
    calendar_type: "google",
    sync_count: 3,
    synced: "2026-06-01 10:15:00",
    subscriber_form_data: { name: "Jane Doe", email: "jane@example.com" },
    subscriber_form_labels: { name: "Name", email: "Email" },
    geo_location: {
      ip: "203.0.113.10",
      city: "Austin",
      region: "Texas",
      country: "United States",
      location: "30.2672,-97.7431",
      postal: "73301"
    },
    created: "2026-06-01 10:14:32"
  },
  async onEnable(context) {
    await import_pieces_common12.pollingHelper.onEnable(polling, context);
  },
  async onDisable(context) {
    await import_pieces_common12.pollingHelper.onDisable(polling, context);
  },
  async test(context) {
    return await import_pieces_common12.pollingHelper.test(polling, context);
  },
  async run(context) {
    return await import_pieces_common12.pollingHelper.poll(polling, context);
  }
});

// ../ap/packages/pieces/community/add-event/src/lib/triggers/new-rsvp-attendee.ts
var import_pieces_common13 = require("@activepieces/pieces-common"), import_pieces_framework12 = require("@activepieces/pieces-framework");
var props2 = {
  event_id: addEventProps.eventId({ required: !0 })
}, polling2 = {
  strategy: import_pieces_common13.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue, lastFetchEpochMS }) => propsValue.event_id ? (await addEventApi.getItemsSince({
    apiKey: auth.secret_text,
    resourceUri: "/rsvps",
    select: (page) => {
      var _a;
      return (_a = page.rsvps) != null ? _a : [];
    },
    getCreated: (rsvp) => rsvp.created,
    sinceEpochMs: lastFetchEpochMS,
    query: { event_ids: [propsValue.event_id] }
  })).map((rsvp) => ({
    epochMilliSeconds: addEventApi.toEpochMs(rsvp.created),
    data: rsvp
  })) : []
}, addEventNewRsvpAttendeeTrigger = (0, import_pieces_framework12.createTrigger)({
  auth: addEventAuth,
  name: "new_rsvp_attendee",
  classification: "READ",
  displayName: "New RSVP Attendee",
  description: "Triggers when a new attendee RSVPs to your AddEvent event.",
  aiMetadata: {
    description: "Fires when a new attendee RSVPs to a specific AddEvent event, representing one RSVP response (going, maybe, or not-going) from a person. Scoped to the single event identified by the configured event ID."
  },
  type: import_pieces_framework12.TriggerStrategy.POLLING,
  props: props2,
  sampleData: {
    id: "rsvp_abc123",
    event_id: "evt_abc123",
    email: "jane@example.com",
    attending: "going",
    rsvp_form_data: { name: "Jane Doe" },
    rsvp_form_labels: { name: "Name" },
    geo_location: {
      ip: "203.0.113.10",
      city: "Austin",
      region: "Texas",
      country: "United States",
      location: "30.2672,-97.7431",
      postal: "73301"
    },
    created: "2026-06-01 10:14:32",
    modified: "2026-06-01 10:14:32"
  },
  async onEnable(context) {
    await import_pieces_common13.pollingHelper.onEnable(polling2, context);
  },
  async onDisable(context) {
    await import_pieces_common13.pollingHelper.onDisable(polling2, context);
  },
  async test(context) {
    return await import_pieces_common13.pollingHelper.test(polling2, context);
  },
  async run(context) {
    return await import_pieces_common13.pollingHelper.poll(polling2, context);
  }
});

// ../ap/packages/pieces/community/add-event/src/index.ts
var addEvent = (0, import_pieces_framework13.createPiece)({
  displayName: "AddEvent",
  description: "Create and manage calendars, events, and RSVPs with AddEvent \u2014 the add-to-calendar service.",
  auth: addEventAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/add-event.png",
  categories: [import_pieces_framework14.PieceCategory.PRODUCTIVITY],
  authors: ["onyedikachi-david"],
  actions: [
    addEventCreateEventAction,
    addEventUpdateEventAction,
    addEventDeleteEventAction,
    addEventFindEventAction,
    addEventFindOrCreateEventAction,
    addEventCreateRsvpAttendeeAction,
    addEventDeleteCalendarSubscriberAction,
    addEventCreateAddToCalendarLinksAction,
    (0, import_pieces_common14.createCustomApiCallAction)({
      baseUrl: () => addEventApi.baseUrl,
      auth: addEventAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: [
    addEventNewCalendarSubscriberTrigger,
    addEventNewRsvpAttendeeTrigger
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  addEvent
});
