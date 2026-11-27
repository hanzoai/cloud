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

// ../ap/packages/pieces/community/assembled/src/index.ts
var index_exports = {};
__export(index_exports, {
  assembled: () => assembled
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_framework11 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/assembled/src/lib/actions/custom-api-call.ts
var import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/assembled/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), assembledAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "You can obtain API key by navigating to [Settings->API](https://app.assembledhq.com/settings/api) page.",
  required: !0
});

// ../ap/packages/pieces/community/assembled/src/lib/actions/custom-api-call.ts
var customApiCall = (0, import_pieces_common.createCustomApiCallAction)({
  auth: assembledAuth,
  name: "custom_api_call",
  displayName: "Custom API Call",
  description: "Make custom API calls to Assembled endpoints",
  baseUrl: () => "https://api.assembledhq.com/v0",
  authMapping: async (auth) => ({
    Authorization: `Basic ${Buffer.from(auth.secret_text + ":").toString("base64")}`,
    "Content-Type": "application/json"
  })
});

// ../ap/packages/pieces/community/assembled/src/lib/actions/custom-graphql.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/assembled/src/lib/common/index.ts
var import_pieces_common2 = require("@activepieces/pieces-common");
var assembledCommon = {
  baseUrl: "https://api.assembledhq.com/v0",
  async makeRequest(auth, method, endpoint, body, headers) {
    let url = endpoint.startsWith("http") ? endpoint : `${this.baseUrl}${endpoint}`;
    return await import_pieces_common2.httpClient.sendRequest({
      method,
      url,
      headers: __spreadValues({
        Authorization: `Basic ${Buffer.from(auth + ":").toString("base64")}`,
        "Content-Type": "application/json"
      }, headers),
      body
    });
  },
  formatDateTime(date) {
    return new Date(date).toISOString();
  },
  formatDate(date) {
    return new Date(date).toISOString().split("T")[0];
  },
  makeClient(auth) {
    return {
      rawRequest: async (query, variables) => (await this.makeRequest(
        auth,
        import_pieces_common2.HttpMethod.POST,
        "/graphql",
        { query, variables }
      )).body
    };
  }
};

// ../ap/packages/pieces/community/assembled/src/lib/actions/custom-graphql.ts
var customGraphql = (0, import_pieces_framework2.createAction)({
  name: "custom_graphql",
  classification: "WRITE",
  displayName: "Custom GraphQL",
  description: "Perform a custom GraphQL query",
  audience: "both",
  aiMetadata: { description: "Sends a raw GraphQL request to the Assembled API with an operation string and optional variables. Use as an escape hatch when no dedicated action covers the query or mutation you need. The supplied operation can read or write data, so it is not safe to assume repeating the call has no side effect.", idempotent: !1 },
  auth: assembledAuth,
  props: {
    query: import_pieces_framework2.Property.LongText({ displayName: "Query", required: !0 }),
    variables: import_pieces_framework2.Property.Object({ displayName: "Parameters", required: !1 })
  },
  async run({ auth, propsValue }) {
    return await assembledCommon.makeClient(auth.secret_text).rawRequest(
      propsValue.query,
      propsValue.variables
    );
  }
});

// ../ap/packages/pieces/community/assembled/src/lib/actions/ooo.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common");
var OOO = (0, import_pieces_framework3.createAction)({
  auth: assembledAuth,
  name: "OOO",
  classification: "WRITE",
  displayName: "Create OOO Request",
  description: "Create an Out of Office request in Assembled.",
  audience: "both",
  aiMetadata: { description: "Creates a time-off (out-of-office) request for a user in Assembled over the given start/end window, requiring the user ID and an activity type ID (a UUID from the activity types endpoint). A mock mode prop can return a fabricated response without hitting the API for testing. Each call submits a new request, so repeating it creates duplicates.", idempotent: !1 },
  props: {
    mock_mode: import_pieces_framework3.Property.Checkbox({
      displayName: "Mock Mode",
      description: "Use mock data for testing",
      required: !1,
      defaultValue: !0
    }),
    user_id: import_pieces_framework3.Property.ShortText({
      displayName: "User ID",
      description: "ID of the user requesting time off",
      required: !0
    }),
    start_date: import_pieces_framework3.Property.DateTime({
      displayName: "Start Date",
      description: "Start date of the OOO period",
      required: !0
    }),
    end_date: import_pieces_framework3.Property.DateTime({
      displayName: "End Date",
      description: "End date of the OOO period",
      required: !0
    }),
    activity_type_id: import_pieces_framework3.Property.ShortText({
      displayName: "Activity Type ID",
      description: "UUID of the activity type for time off (can be retrieved from activity types endpoints)",
      required: !0
    }),
    all_day: import_pieces_framework3.Property.Checkbox({
      displayName: "All Day Event",
      description: "Whether this is an all-day OOO event",
      required: !1,
      defaultValue: !0
    }),
    reason: import_pieces_framework3.Property.LongText({
      displayName: "Reason",
      description: "Reason for the OOO request",
      required: !1
    })
  },
  async run(context) {
    let { mock_mode, user_id, start_date, end_date, activity_type_id, all_day, reason } = context.propsValue;
    if (mock_mode)
      return {
        success: !0,
        message: "Mock OOO created successfully",
        data: {
          id: `mock_ooo_${Date.now()}`,
          user_id,
          start_date,
          end_date,
          activity_type_id: activity_type_id || "mock-activity-type-id",
          status: "pending",
          created_at: (/* @__PURE__ */ new Date()).toISOString()
        }
      };
    try {
      let oooData = {
        user_id,
        start_time: Math.floor(new Date(start_date).getTime() / 1e3),
        end_time: Math.floor(new Date(end_date).getTime() / 1e3),
        activity_type_id,
        all_day: all_day != null ? all_day : !0,
        description: reason || ""
      }, response = await assembledCommon.makeRequest(
        context.auth.secret_text,
        import_pieces_common3.HttpMethod.POST,
        "/time_off",
        oooData
      );
      return {
        success: !0,
        ooo_id: response.body.id,
        message: "OOO request created successfully",
        data: response.body
      };
    } catch (error) {
      throw new Error(`Failed to create OOO request: ${error instanceof Error ? error.message : "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/assembled/src/lib/actions/update-OOO.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common");
var updateOOO = (0, import_pieces_framework4.createAction)({
  auth: assembledAuth,
  name: "update_OOO",
  classification: "DESTRUCTIVE",
  displayName: "Update OOO Request",
  description: "Updates an existing OOO request.",
  audience: "both",
  aiMetadata: { description: "Updates an existing time-off request identified by its OOO ID. Because Assembled has no update endpoint, this cancels the original request and creates a fresh one with the new details, so user ID and activity type ID are required. This mutates state and creates a new request each call, so it is not safe to repeat.", idempotent: !1 },
  props: {
    OOO_id: import_pieces_framework4.Property.ShortText({
      displayName: "OOO ID",
      required: !0
    }),
    start_date: import_pieces_framework4.Property.DateTime({
      displayName: "Start Date",
      required: !1
    }),
    end_date: import_pieces_framework4.Property.DateTime({
      displayName: "End Date",
      required: !1
    }),
    status: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Status",
      required: !1,
      options: {
        options: [
          { label: "Pending", value: "pending" },
          { label: "Approved", value: "approved" },
          { label: "Rejected", value: "rejected" },
          { label: "Cancelled", value: "cancelled" }
        ]
      }
    }),
    reason: import_pieces_framework4.Property.LongText({
      displayName: "Reason",
      required: !1
    }),
    user_id: import_pieces_framework4.Property.ShortText({
      displayName: "User ID",
      description: "ID of the user for the time off request (required for creating new request)",
      required: !0
    }),
    activity_type_id: import_pieces_framework4.Property.ShortText({
      displayName: "Activity Type ID",
      description: "UUID of the activity type for time off (required for creating new request)",
      required: !0
    })
  },
  async run(context) {
    let { OOO_id, start_date, end_date, status, reason, user_id, activity_type_id } = context.propsValue;
    try {
      console.log(`Canceling existing time off request: ${OOO_id}`), await assembledCommon.makeRequest(
        context.auth.secret_text,
        import_pieces_common4.HttpMethod.POST,
        `/time_off/${OOO_id}/cancel`
      ), console.log("Creating new time off request with updated details");
      let newRequestData = {
        user_id,
        activity_type_id,
        all_day: !0
      };
      start_date && (newRequestData.start_time = Math.floor(new Date(start_date).getTime() / 1e3)), end_date && (newRequestData.end_time = Math.floor(new Date(end_date).getTime() / 1e3)), reason && (newRequestData.description = reason);
      let response = await assembledCommon.makeRequest(
        context.auth.secret_text,
        import_pieces_common4.HttpMethod.POST,
        "/time_off",
        newRequestData
      );
      return {
        success: !0,
        message: "Time off request updated successfully (canceled old and created new)",
        canceled_request_id: OOO_id,
        new_request_id: response.body.id,
        data: response.body
      };
    } catch (error) {
      throw new Error(`Failed to update time off request: ${error instanceof Error ? error.message : "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/assembled/src/lib/actions/delete-OOO.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common");
var deleteOOO = (0, import_pieces_framework5.createAction)({
  auth: assembledAuth,
  name: "delete_OOO",
  classification: "DESTRUCTIVE",
  displayName: "Delete OOO Request",
  description: "Cancel/delete a OOO request.",
  audience: "both",
  aiMetadata: { description: "Cancels (deletes) a time-off request in Assembled, identified by its OOO ID. Use to revoke a previously submitted out-of-office request. Because the cancellation is keyed on the request ID, repeating the call leaves the request cancelled, so it is idempotent.", idempotent: !0 },
  props: {
    OOO_id: import_pieces_framework5.Property.ShortText({
      displayName: "OOO ID",
      required: !0
    })
  },
  async run(context) {
    let { OOO_id } = context.propsValue;
    return {
      success: !0,
      message: "OOO request deleted successfully",
      data: (await assembledCommon.makeRequest(
        context.auth.secret_text,
        import_pieces_common5.HttpMethod.POST,
        `/time_off/${OOO_id}/cancel`,
        {}
      )).body
    };
  }
});

// ../ap/packages/pieces/community/assembled/src/lib/actions/add-shift.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common");
var addShift = (0, import_pieces_framework6.createAction)({
  auth: assembledAuth,
  name: "add_shift",
  classification: "WRITE",
  displayName: "Add Shift on Assembled",
  description: "Add a new shift to a user's schedule in Assembled",
  audience: "both",
  aiMetadata: { description: "Adds a scheduled shift (activity) to an agent's schedule in Assembled over a given start/end time. Requires the agent ID (the agent_id field from the people endpoint, not the person ID) and an activity type ID UUID. Each call creates a new activity, so repeating it produces duplicate shifts.", idempotent: !1 },
  props: {
    agent_id: import_pieces_framework6.Property.ShortText({
      displayName: "Agent ID",
      description: "Agent ID of the person to assign the shift to (use the agent_id field from /people endpoint, not the person id)",
      required: !0
    }),
    start_time: import_pieces_framework6.Property.DateTime({
      displayName: "Start Time",
      description: "Start time of the shift",
      required: !0
    }),
    end_time: import_pieces_framework6.Property.DateTime({
      displayName: "End Time",
      description: "End time of the shift",
      required: !0
    }),
    shift_type: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Shift Type",
      description: "Type of shift",
      required: !1,
      defaultValue: "regular",
      options: {
        options: [
          { label: "Regular", value: "regular" },
          { label: "Overtime", value: "overtime" },
          { label: "On-call", value: "on_call" },
          { label: "Training", value: "training" }
        ]
      }
    }),
    activity_type_id: import_pieces_framework6.Property.ShortText({
      displayName: "Activity Type ID",
      description: "UUID of the activity type for this shift (get from /activity_types endpoint)",
      required: !0
    }),
    notes: import_pieces_framework6.Property.LongText({
      displayName: "Notes",
      description: "Additional notes for the shift",
      required: !1
    })
  },
  async run(context) {
    let { agent_id, start_time, end_time, shift_type, activity_type_id, notes } = context.propsValue;
    try {
      let startTimestamp = Math.floor(new Date(start_time).getTime() / 1e3), endTimestamp = Math.floor(new Date(end_time).getTime() / 1e3), shiftData = {
        agent_id,
        type_id: activity_type_id,
        start_time: startTimestamp,
        end_time: endTimestamp,
        description: notes || `${shift_type || "regular"} shift`
      }, response = await assembledCommon.makeRequest(
        context.auth.secret_text,
        import_pieces_common6.HttpMethod.POST,
        "/activities",
        shiftData
      );
      return {
        success: !0,
        shift_id: response.body.id,
        message: "Shift added successfully",
        data: response.body
      };
    } catch (error) {
      throw new Error(`Failed to add shift: ${error instanceof Error ? error.message : "Unknown error"}`);
    }
  }
});

// ../ap/packages/pieces/community/assembled/src/lib/triggers/schedule-updated.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common");
var scheduleUpdated = (0, import_pieces_framework7.createTrigger)({
  auth: assembledAuth,
  name: "schedule_updated",
  classification: "READ",
  displayName: "Schedule Updated",
  description: "Triggers when user schedule is modified.",
  aiMetadata: {
    description: "Fires when a user's schedule changes in Assembled (for example a shift added or modified), polling for schedule-update events since the last check. Use to react to scheduling changes such as added or edited shifts."
  },
  type: import_pieces_framework7.TriggerStrategy.POLLING,
  props: {},
  sampleData: {
    user_id: "user_456",
    schedule_id: "sched_123",
    changes: ["shift_added", "shift_modified"],
    updated_at: "2025-01-15T10:00:00Z"
  },
  async onEnable(context) {
    await context.store.put("lastScheduleCheck", (/* @__PURE__ */ new Date()).toISOString());
  },
  async onDisable() {
  },
  async run(context) {
    let lastCheck = await context.store.get("lastScheduleCheck") || new Date(Date.now() - 36e5).toISOString(), scheduleUpdates = (await assembledCommon.makeRequest(
      context.auth.secret_text,
      import_pieces_common7.HttpMethod.GET,
      `/events?type=schedule_updated&after=${lastCheck}&limit=100`
    )).body.data || [];
    return scheduleUpdates.length > 0 && await context.store.put("lastScheduleCheck", (/* @__PURE__ */ new Date()).toISOString()), scheduleUpdates;
  }
});

// ../ap/packages/pieces/community/assembled/src/lib/triggers/OOO-status-changed.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var import_pieces_common8 = require("@activepieces/pieces-common");
var timeOffStatusChanged = (0, import_pieces_framework8.createTrigger)({
  auth: assembledAuth,
  name: "OOO_status_changed",
  classification: "READ",
  displayName: "OOO Status Changed",
  description: "Triggers on approval/rejection of OOO.",
  aiMetadata: {
    description: "Fires when an existing time-off (out-of-office) request changes status, such as being approved or rejected, polling for time-off updates since the last check. Use to react to decisions on submitted OOO requests."
  },
  type: import_pieces_framework8.TriggerStrategy.POLLING,
  props: {},
  sampleData: {
    id: "<uuid>",
    time_off_request_id: "<uuid>",
    created_at: 1546303260,
    comment: "Enjoy your vacation",
    type: "approve",
    time_off_request: {
      id: "<uuid>",
      agent_id: "<uuid>",
      start_time: 1546303260,
      end_time: 1546303270,
      created_at: 1546303260,
      description: "Going to the dentist",
      status: "approved",
      activity_type_id: "<uuid>"
    }
  },
  async onEnable(context) {
    await context.store.put("lastStatusCheck", Math.floor(Date.now() / 1e3));
  },
  async onDisable() {
  },
  async run(context) {
    let lastCheck = await context.store.get("lastStatusCheck") || Math.floor(Date.now() / 1e3) - 86400, response = await assembledCommon.makeRequest(
      context.auth.secret_text,
      import_pieces_common8.HttpMethod.GET,
      `/time_off/updates?updated_since=${lastCheck}&type=approve`
    ), timeOffUpdates = response.body.time_off_updates || {}, timeOffRequests = response.body.time_off_requests || {}, statusChanges = Object.values(timeOffUpdates).map((update) => {
      let request = timeOffRequests[update.time_off_request_id];
      return __spreadProps(__spreadValues({}, update), {
        time_off_request: request
      });
    });
    return statusChanges.length > 0 && await context.store.put("lastStatusCheck", Math.floor(Date.now() / 1e3)), statusChanges;
  }
});

// ../ap/packages/pieces/community/assembled/src/lib/triggers/new-OOO-request.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var import_pieces_common9 = require("@activepieces/pieces-common");
var newTimeOffRequest = (0, import_pieces_framework9.createTrigger)({
  auth: assembledAuth,
  name: "new_OOO_request",
  classification: "READ",
  displayName: "New OOO Request",
  description: "Triggers when a new OOO request is created.",
  aiMetadata: {
    description: "Fires when a new time-off (out-of-office) request is created in Assembled, polling for time-off requests updated since the last check. Use to react to newly submitted OOO requests, for example to route them for approval."
  },
  type: import_pieces_framework9.TriggerStrategy.POLLING,
  props: {},
  sampleData: {
    id: "<uuid>",
    agent_id: "<uuid>",
    start_time: 1546303260,
    end_time: 1546303270,
    created_at: 1546303260,
    description: "Going to the dentist",
    status: "approved",
    activity_type_id: "<uuid>"
  },
  async onEnable(context) {
    await context.store.put("lastCheck", Math.floor(Date.now() / 1e3));
  },
  async onDisable() {
  },
  async run(context) {
    let lastCheck = await context.store.get("lastCheck") || Math.floor(Date.now() / 1e3) - 86400, timeOffRequests = (await assembledCommon.makeRequest(
      context.auth.secret_text,
      import_pieces_common9.HttpMethod.GET,
      `/time_off/requests?updated_since=${lastCheck}&limit=100`
    )).body.time_off_requests || {}, newRequests = Object.values(timeOffRequests);
    return newRequests.length > 0 && await context.store.put("lastCheck", Math.floor(Date.now() / 1e3)), newRequests;
  }
});

// ../ap/packages/pieces/community/assembled/src/index.ts
var assembled = (0, import_pieces_framework10.createPiece)({
  displayName: "Assembled",
  description: "Workforce management platform for scheduling and forecasting",
  auth: assembledAuth,
  minimumSupportedRelease: "0.20.0",
  logoUrl: "https://cdn.activepieces.com/pieces/assembled.png",
  categories: [import_pieces_framework11.PieceCategory.PRODUCTIVITY, import_pieces_framework11.PieceCategory.HUMAN_RESOURCES],
  authors: ["meenulekha-premakumar"],
  actions: [
    customApiCall,
    customGraphql,
    OOO,
    addShift,
    updateOOO,
    deleteOOO
  ],
  triggers: [
    newTimeOffRequest,
    timeOffStatusChanged,
    scheduleUpdated
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  assembled
});
