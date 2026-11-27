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

// ../ap/packages/pieces/community/simpliroute/src/index.ts
var index_exports = {};
__export(index_exports, {
  simpliroute: () => simpliroute
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common36 = require("@activepieces/pieces-common"), import_pieces_framework36 = require("@activepieces/pieces-framework"), import_pieces_framework37 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/account/get-me.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/simpliroute/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), simplirouteAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "Your SimpliRoute API token. You can find this in your SimpliRoute account settings under API section.",
  validate: async (auth) => {
    try {
      console.log("Auth validation - received auth:", typeof auth, auth);
      let token = typeof auth == "string" ? auth : (auth == null ? void 0 : auth.auth) || auth;
      if (console.log("Auth validation - extracted token:", token), !token || typeof token != "string")
        return {
          valid: !1,
          error: "Invalid token format"
        };
      let response = await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `https://api.simpliroute.com/v1/accounts/api-token/${token}/validate/`,
        headers: {
          "Content-Type": "application/json",
          Accept: "application/json"
        }
      });
      return response.status === 200 && response.body && response.body.isvalid === !0 ? {
        valid: !0
      } : {
        valid: !1,
        error: "Invalid API token or authentication failed"
      };
    } catch (error) {
      return {
        valid: !1,
        error: "Failed to validate API token. Please check your token and try again."
      };
    }
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/common/constants.ts
var API_BASE_URL = "https://api.simpliroute.com", commonHeaders = {
  "Content-Type": "application/json",
  Accept: "application/json"
};

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/account/get-me.ts
var get_me = (0, import_pieces_framework2.createAction)({
  name: "get_me",
  auth: simplirouteAuth,
  displayName: "Get Account Details",
  description: "Returns account information of the authenticated user via API token.",
  audience: "both",
  aiMetadata: { description: "Return the account/profile of the user owning the API token. Read-only and idempotent; takes no input. Use to confirm credentials or discover the current account context before other operations.", idempotent: !0 },
  props: {},
  async run(context) {
    let url = `${API_BASE_URL}/v1/accounts/me/`, response = await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.GET,
      url,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/clients/bulk-delete-clients.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var bulk_delete_clients = (0, import_pieces_framework3.createAction)({
  name: "bulk_delete_clients",
  auth: simplirouteAuth,
  displayName: "Bulk Delete Clients",
  description: "Delete multiple clients in a single call.",
  audience: "both",
  aiMetadata: { description: "Permanently remove several clients at once by passing a list of their numeric client IDs. Use for batch cleanup rather than deleting one at a time; destructive and irreversible. Re-running with already-deleted IDs has no further effect.", idempotent: !1 },
  props: {
    ids: import_pieces_framework3.Property.Array({
      displayName: "IDs",
      description: "List of client IDs to delete.",
      required: !0,
      properties: {
        id: import_pieces_framework3.Property.Number({ displayName: "ID", required: !0 })
      }
    })
  },
  async run(context) {
    let body = context.propsValue.ids.map((client) => client.id), response = await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.DELETE,
      url: `${API_BASE_URL}/v1/accounts/clients/`,
      body,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/clients/create-client-property.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var create_client_property = (0, import_pieces_framework4.createAction)({
  name: "create_client_property",
  auth: simplirouteAuth,
  displayName: "Create Client Custom Property",
  description: "Create a new custom attribute for clients.",
  audience: "both",
  aiMetadata: { description: "Define a new custom property (str, int, float or bool) on the client schema so subsequent client records can store that attribute. Use when an attribute you need does not yet exist on clients; not idempotent (each call adds another property definition).", idempotent: !1 },
  props: {
    label: import_pieces_framework4.Property.ShortText({
      displayName: "label",
      description: "Label of the new custom property.",
      required: !0
    }),
    type: import_pieces_framework4.Property.ShortText({
      displayName: "type",
      description: "Property type (str, int, float or bool).",
      required: !0
    })
  },
  async run(context) {
    let body = context.propsValue, response = await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: `${API_BASE_URL}/v1/planner/client-properties/`,
      body,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/clients/create-clients.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var create_clients = (0, import_pieces_framework5.createAction)({
  name: "create_clients",
  auth: simplirouteAuth,
  displayName: "Create Clients",
  description: "Create one or multiple new clients in the account.",
  audience: "both",
  aiMetadata: { description: "Add one or many client (delivery destination) records, each with title, address, coordinates, time windows and contact details, in a single call. Use when registering new clients to later schedule visits against; not idempotent, so each call inserts new records even if a similar client exists.", idempotent: !1 },
  props: {
    clients: import_pieces_framework5.Property.Array({
      displayName: "Clients",
      description: "Array of client objects to create.",
      required: !0,
      properties: {
        key: import_pieces_framework5.Property.ShortText({ displayName: "key", description: "External identifier or unique key for the client.", required: !1 }),
        title: import_pieces_framework5.Property.ShortText({ displayName: "title", description: "Name or title of the client.", required: !0 }),
        address: import_pieces_framework5.Property.ShortText({ displayName: "address", description: "Client address.", required: !1 }),
        latitude: import_pieces_framework5.Property.Number({ displayName: "latitude", description: "Geographic latitude.", required: !1 }),
        longitude: import_pieces_framework5.Property.Number({ displayName: "longitude", description: "Geographic longitude.", required: !1 }),
        load: import_pieces_framework5.Property.Number({ displayName: "load", description: "Load associated (unit 1).", required: !1 }),
        load_2: import_pieces_framework5.Property.Number({ displayName: "load_2", description: "Load associated (unit 2).", required: !1 }),
        load_3: import_pieces_framework5.Property.Number({ displayName: "load_3", description: "Load associated (unit 3).", required: !1 }),
        window_start: import_pieces_framework5.Property.ShortText({ displayName: "window_start", description: "Preferred time window start (HH:MM:SS).", required: !1 }),
        window_end: import_pieces_framework5.Property.ShortText({ displayName: "window_end", description: "Preferred time window end.", required: !1 }),
        window_start_2: import_pieces_framework5.Property.ShortText({ displayName: "window_start_2", description: "Second time window start.", required: !1 }),
        window_end_2: import_pieces_framework5.Property.ShortText({ displayName: "window_end_2", description: "Second time window end.", required: !1 }),
        duration: import_pieces_framework5.Property.ShortText({ displayName: "duration", description: "Estimated visit duration (HH:MM:SS).", required: !1 }),
        contact_name: import_pieces_framework5.Property.ShortText({ displayName: "contact_name", description: "Client contact name.", required: !1 }),
        contact_phone: import_pieces_framework5.Property.ShortText({ displayName: "contact_phone", description: "Client contact phone.", required: !1 }),
        contact_email: import_pieces_framework5.Property.ShortText({ displayName: "contact_email", description: "Client contact email.", required: !1 }),
        notes: import_pieces_framework5.Property.ShortText({ displayName: "notes", description: "Additional notes about the client.", required: !1 }),
        priority_level: import_pieces_framework5.Property.Number({ displayName: "priority_level", description: "Client priority level.", required: !1 })
      }
    })
  },
  async run(context) {
    let body = context.propsValue.clients, response = await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.POST,
      url: `${API_BASE_URL}/v1/accounts/clients/`,
      body,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/clients/get-clients.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var get_clients = (0, import_pieces_framework6.createAction)({
  name: "get_clients",
  auth: simplirouteAuth,
  displayName: "Get Clients",
  description: "Retrieves the list of clients associated with the account. Can filter by client key.",
  audience: "both",
  aiMetadata: { description: "Look up account clients, filtered to the given unique client key. Use to find a client and its stored details (id, address, coordinates, contacts) before creating visits or routes. Read-only and idempotent.", idempotent: !0 },
  props: {
    key: import_pieces_framework6.Property.ShortText({
      displayName: "Client Key",
      description: "Unique client key to filter results.",
      required: !0
    })
  },
  async run(context) {
    let queryString = "";
    context.propsValue.key && (queryString += (queryString ? "&" : "?") + "key=" + context.propsValue.key);
    let url = `${API_BASE_URL}/v1/accounts/clients/${queryString}`;
    console.log(url);
    let response = await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.GET,
      url,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/vehicles/create-vehicle.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var create_vehicle = (0, import_pieces_framework7.createAction)({
  name: "create_vehicle",
  auth: simplirouteAuth,
  displayName: "Create Vehicle",
  description: "Register a new vehicle in the account.",
  audience: "both",
  aiMetadata: { description: "Register a new vehicle, requiring name, capacity, and start/end location coordinates. Not idempotent: each call creates another vehicle, so avoid retrying. Use the get-vehicles action first if you need to check whether a vehicle already exists.", idempotent: !1 },
  props: {
    name: import_pieces_framework7.Property.ShortText({ displayName: "name", description: "The name of the vehicle.", required: !0 }),
    capacity: import_pieces_framework7.Property.Number({ displayName: "capacity", description: "The maximum capacity of the vehicle.", required: !0 }),
    default_driver: import_pieces_framework7.Property.ShortText({ displayName: "default_driver", description: "The assigned driver ID or null if not assigned.", required: !1 }),
    location_start_address: import_pieces_framework7.Property.ShortText({ displayName: "location_start_address", description: "The starting address of the vehicle.", required: !0 }),
    location_start_latitude: import_pieces_framework7.Property.Number({ displayName: "location_start_latitude", description: "The latitude of the starting location.", required: !0 }),
    location_start_longitude: import_pieces_framework7.Property.Number({ displayName: "location_start_longitude", description: "The longitude of the starting location.", required: !0 }),
    location_end_address: import_pieces_framework7.Property.ShortText({ displayName: "location_end_address", description: "The ending address of the vehicle.", required: !0 }),
    location_end_latitude: import_pieces_framework7.Property.Number({ displayName: "location_end_latitude", description: "The latitude of the ending location.", required: !0 }),
    location_end_longitude: import_pieces_framework7.Property.Number({ displayName: "location_end_longitude", description: "The longitude of the ending location.", required: !0 }),
    skills: import_pieces_framework7.Property.Json({ displayName: "skills", description: "a list of skill IDs (JSON format [1, 2]).", required: !1 }),
    capacity2: import_pieces_framework7.Property.ShortText({ displayName: "capacity2", description: "Secondary vehicle capacity.", required: !1 }),
    capacity3: import_pieces_framework7.Property.ShortText({ displayName: "capacity3", description: "Third vehicle capacity.", required: !1 }),
    cost: import_pieces_framework7.Property.ShortText({ displayName: "cost", description: "Operational cost of the vehicle.", required: !1 }),
    shift_start: import_pieces_framework7.Property.ShortText({ displayName: "shift_start", description: "Start time of the vehicle shift.", required: !1 }),
    shift_end: import_pieces_framework7.Property.ShortText({ displayName: "shift_end", description: "End time of the vehicle shift.", required: !1 }),
    reference_id: import_pieces_framework7.Property.ShortText({ displayName: "reference_id", description: "External reference ID of the vehicle.", required: !1 }),
    license_plate: import_pieces_framework7.Property.ShortText({ displayName: "license_plate", description: "License plate or registration of the vehicle.", required: !1 }),
    min_load: import_pieces_framework7.Property.Number({ displayName: "min_load", description: "Minimum load (unit 1) required to dispatch.", required: !1 }),
    min_load_2: import_pieces_framework7.Property.Number({ displayName: "min_load_2", description: "Minimum load (unit 2) required.", required: !1 }),
    min_load_3: import_pieces_framework7.Property.Number({ displayName: "min_load_3", description: "Minimum load (unit 3) required.", required: !1 }),
    max_visit: import_pieces_framework7.Property.Number({ displayName: "max_visit", description: "Maximum number of visits that the vehicle can perform.", required: !1 }),
    max_time: import_pieces_framework7.Property.Number({ displayName: "max_time", description: "Maximum duration of route (in seconds).", required: !1 }),
    rest_time_start: import_pieces_framework7.Property.ShortText({ displayName: "rest_time_start", description: "Start time of rest.", required: !1 }),
    rest_time_end: import_pieces_framework7.Property.ShortText({ displayName: "rest_time_end", description: "End time of rest.", required: !1 }),
    rest_time_duration: import_pieces_framework7.Property.ShortText({ displayName: "rest_time_duration", description: "Duration of rest.", required: !1 })
  },
  async run(context) {
    let body = context.propsValue, response = await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.POST,
      url: `${API_BASE_URL}/v1/routes/vehicles/`,
      body,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/vehicles/delete-vehicle.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var delete_vehicle = (0, import_pieces_framework8.createAction)({
  name: "delete_vehicle",
  auth: simplirouteAuth,
  displayName: "Delete Vehicle",
  description: "Delete a vehicle by its ID.",
  audience: "both",
  aiMetadata: { description: "Permanently delete a vehicle by its ID. Destructive and not safely repeatable: a repeat call fails once the vehicle is gone, so confirm the ID first.", idempotent: !1 },
  props: {
    vehicle_id: import_pieces_framework8.Property.Number({
      displayName: "vehicle_id",
      description: "Vehicle ID to delete.",
      required: !0
    })
  },
  async run(context) {
    let url = `${API_BASE_URL}/v1/routes/vehicles/${context.propsValue.vehicle_id}/`, response = await import_pieces_common8.httpClient.sendRequest({
      method: import_pieces_common8.HttpMethod.DELETE,
      url,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/vehicles/get-vehicle.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var get_vehicle = (0, import_pieces_framework9.createAction)({
  name: "get_vehicle",
  auth: simplirouteAuth,
  displayName: "Get Vehicle",
  description: "Retrieve information of a specific vehicle.",
  audience: "both",
  aiMetadata: { description: "Retrieve a single vehicle by its ID. Read-only and idempotent. Use when you already know the vehicle ID; to list all vehicles use the get-vehicles action.", idempotent: !0 },
  props: {
    vehicle_id: import_pieces_framework9.Property.Number({
      displayName: "vehicle_id",
      description: "Vehicle ID.",
      required: !0
    })
  },
  async run(context) {
    let url = `${API_BASE_URL}/v1/routes/vehicles/${context.propsValue.vehicle_id}/`, response = await import_pieces_common9.httpClient.sendRequest({
      method: import_pieces_common9.HttpMethod.GET,
      url,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/vehicles/get-vehicles.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var get_vehicles = (0, import_pieces_framework10.createAction)({
  name: "get_vehicles",
  auth: simplirouteAuth,
  displayName: "Get Vehicles",
  description: "Retrieve the list of vehicles registered in the account.",
  audience: "both",
  aiMetadata: { description: "List all vehicles registered in the account. Read-only and idempotent; takes no input. Use to discover vehicle IDs or check the fleet before creating, fetching, or deleting a specific vehicle.", idempotent: !0 },
  props: {},
  async run(context) {
    let url = `${API_BASE_URL}/v1/routes/vehicles/`, response = await import_pieces_common10.httpClient.sendRequest({
      method: import_pieces_common10.HttpMethod.GET,
      url,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/visits/add-visit-items.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var add_visit_items = (0, import_pieces_framework11.createAction)({
  name: "add_visit_items",
  auth: simplirouteAuth,
  displayName: "Add Visit Items",
  description: "Add items to an existing visit.",
  audience: "both",
  aiMetadata: { description: "Append one or more line items (products/cargo) to an existing visit identified by visit_id. Not idempotent: each call adds items, so re-running duplicates them. Use only after the visit exists.", idempotent: !1 },
  props: {
    visit_id: import_pieces_framework11.Property.Number({ displayName: "visit_id", description: "ID of the visit to add items to.", required: !0 }),
    items: import_pieces_framework11.Property.Array({
      displayName: "items",
      description: "Array of items to add to the visit.",
      required: !0,
      properties: {
        title: import_pieces_framework11.Property.ShortText({ displayName: "title", description: "Name or description of the item.", required: !0 }),
        load: import_pieces_framework11.Property.Number({ displayName: "load", description: "Unit of load that the item occupies.", required: !1 }),
        load_2: import_pieces_framework11.Property.Number({ displayName: "load_2", description: "Second load unit of the item.", required: !1 }),
        load_3: import_pieces_framework11.Property.Number({ displayName: "load_3", description: "Third load unit of the item.", required: !1 }),
        reference: import_pieces_framework11.Property.ShortText({ displayName: "reference", description: "Internal reference of the item.", required: !1 }),
        notes: import_pieces_framework11.Property.ShortText({ displayName: "notes", description: "Additional notes about the item.", required: !1 }),
        quantity_planned: import_pieces_framework11.Property.Number({ displayName: "quantity_planned", description: "Planned quantity of the item.", required: !1 }),
        quantity_delivered: import_pieces_framework11.Property.Number({ displayName: "quantity_delivered", description: "Delivered quantity of the item (if applicable).", required: !1 })
      }
    })
  },
  async run(context) {
    let _a = context.propsValue, { visit_id } = _a, body = __objRest(_a, ["visit_id"]).items, response = await import_pieces_common11.httpClient.sendRequest({
      method: import_pieces_common11.HttpMethod.POST,
      url: `${API_BASE_URL}/v1/routes/visits/${visit_id}/items/`,
      body,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/visits/create-visits.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common");
var create_visits = (0, import_pieces_framework12.createAction)({
  name: "create_visits",
  auth: simplirouteAuth,
  displayName: "Create Visits",
  description: "Create visits. Multiple visits can be created in a single request.",
  audience: "both",
  aiMetadata: { description: "Create one or more delivery visits (stops) in a single batch request, each requiring a title, address, planned date, and load. Not idempotent: re-running creates duplicate visits, so do not retry blindly. To change an existing visit use the update actions instead.", idempotent: !1 },
  props: {
    visits: import_pieces_framework12.Property.Array({
      displayName: "visits",
      description: "Array of visit objects to create.",
      required: !0,
      properties: {
        title: import_pieces_framework12.Property.ShortText({ displayName: "title", description: "String to identify the delivery. Usually company or person's name.", required: !0 }),
        address: import_pieces_framework12.Property.ShortText({ displayName: "address", description: "Address text. Best practice is to use Googlemaps format.", required: !0 }),
        planned_date: import_pieces_framework12.Property.ShortText({ displayName: "planned_date", description: "Date when the visit will be delivered. (YYYY-MM-DD)", required: !0 }),
        latitude: import_pieces_framework12.Property.Number({ displayName: "latitude", description: "Visit's latitude location.", required: !1 }),
        longitude: import_pieces_framework12.Property.Number({ displayName: "longitude", description: "Visit's longitude location.", required: !1 }),
        load: import_pieces_framework12.Property.Number({ displayName: "load", description: "Space of the truck that visit uses.", required: !0, defaultValue: 1 }),
        load_2: import_pieces_framework12.Property.Number({ displayName: "load_2", description: "Space of the truck that visit uses.", required: !1 }),
        load_3: import_pieces_framework12.Property.Number({ displayName: "load_3", description: "Space of the truck that visit uses.", required: !1 }),
        window_start: import_pieces_framework12.Property.ShortText({ displayName: "window_start", description: "Initial Hour when the visit can be visit (HH:MM:SS).", required: !1 }),
        window_end: import_pieces_framework12.Property.ShortText({ displayName: "window_end", description: "Final Hour when the visit can be visit (HH:MM:SS).", required: !1 }),
        window_start_2: import_pieces_framework12.Property.ShortText({ displayName: "window_start_2", description: "Initial Hour when the visit can be visit (HH:MM:SS).", required: !1 }),
        window_end_2: import_pieces_framework12.Property.ShortText({ displayName: "window_end_2", description: "Final Hour when the visit can be visit (HH:MM:SS).", required: !1 }),
        duration: import_pieces_framework12.Property.ShortText({ displayName: "duration", description: "Time spent in the delivery (HH:mm:ss).", required: !1 }),
        order: import_pieces_framework12.Property.Number({ displayName: "order", description: "Visit order when the visit already belongs to a delivery route.", required: !1 }),
        skills_required: import_pieces_framework12.Property.Json({ displayName: "skills_required", description: "A list of skills or capabilities required (JSON format [1, 2]).", required: !1 }),
        skills_optional: import_pieces_framework12.Property.Json({ displayName: "skills_optional", description: "A list of skills or capabilities optionals (JSON format [1, 2]).", required: !1 }),
        tags: import_pieces_framework12.Property.Json({ displayName: "tags", description: "A list of tags (JSON format [1, 2]).", required: !1 }),
        contact_name: import_pieces_framework12.Property.ShortText({ displayName: "contact_name", description: "Name of who will receive the delivery.", required: !1 }),
        contact_phone: import_pieces_framework12.Property.ShortText({ displayName: "contact_phone", description: "Phone of who will receive the delivery.", required: !1 }),
        contact_email: import_pieces_framework12.Property.ShortText({ displayName: "contact_email", description: "E-mail of who will receive the delivery.", required: !1 }),
        reference: import_pieces_framework12.Property.ShortText({ displayName: "reference", description: "Reference or internal identifier of the company. Example: Invoice or order number.", required: !1 }),
        notes: import_pieces_framework12.Property.ShortText({ displayName: "notes", description: "Information to help the driver.", required: !1 }),
        priority_level: import_pieces_framework12.Property.Number({ displayName: "priority_level", description: "If the visit is more important than others. It goes from 1 to 5.", required: !1 }),
        visit_type: import_pieces_framework12.Property.ShortText({ displayName: "visit_type", description: "Visit type associated with the visit.", required: !1 }),
        fleet: import_pieces_framework12.Property.Number({ displayName: "fleet", description: "Fleet ID.", required: !1 })
      }
    })
  },
  async run(context) {
    let body = context.propsValue.visits, response = await import_pieces_common12.httpClient.sendRequest({
      method: import_pieces_common12.HttpMethod.POST,
      url: `${API_BASE_URL}/v1/routes/visits/`,
      body,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/visits/delete-visit.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common13 = require("@activepieces/pieces-common");
var delete_visit = (0, import_pieces_framework13.createAction)({
  name: "delete_visit",
  auth: simplirouteAuth,
  displayName: "Delete Visit",
  description: "Delete a visit by its ID.",
  audience: "both",
  aiMetadata: { description: "Permanently delete a single visit by its ID. Destructive and not safely repeatable: once deleted the visit is gone and a repeat call will fail, so confirm the ID before calling.", idempotent: !1 },
  props: {
    visit_id: import_pieces_framework13.Property.Number({
      displayName: "visit_id",
      description: "ID of the visit to delete.",
      required: !0
    })
  },
  async run(context) {
    let url = `${API_BASE_URL}/v1/routes/visits/${context.propsValue.visit_id}/`, response = await import_pieces_common13.httpClient.sendRequest({
      method: import_pieces_common13.HttpMethod.DELETE,
      url,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/visits/get-visit.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_common14 = require("@activepieces/pieces-common");
var get_visit = (0, import_pieces_framework14.createAction)({
  name: "get_visit",
  auth: simplirouteAuth,
  displayName: "Get Visit",
  description: "Retrieve details of a specific visit by ID.",
  audience: "both",
  aiMetadata: { description: "Retrieve a single visit record by its ID. Read-only and idempotent; use for the basic visit fields. For the richer plan-level breakdown use get-visit-detail instead.", idempotent: !0 },
  props: {
    visit_id: import_pieces_framework14.Property.Number({
      displayName: "visit_id",
      description: "Visit ID.",
      required: !0
    })
  },
  async run(context) {
    let url = `${API_BASE_URL}/v1/routes/visits/${context.propsValue.visit_id}/`, response = await import_pieces_common14.httpClient.sendRequest({
      method: import_pieces_common14.HttpMethod.GET,
      url,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/visits/get-visit-detail.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework"), import_pieces_common15 = require("@activepieces/pieces-common");
var get_visit_detail = (0, import_pieces_framework15.createAction)({
  name: "get_visit_detail",
  auth: simplirouteAuth,
  displayName: "Get Visit Detail",
  description: "Get detailed information about a specific visit.",
  audience: "both",
  aiMetadata: { description: "Fetch the extended detail view of one visit by ID. Read-only and idempotent; pick this over get-visit when you need the fuller plan-level detail payload rather than the basic visit record.", idempotent: !0 },
  props: {
    visit_id: import_pieces_framework15.Property.Number({
      displayName: "visit_id",
      description: "ID of the visit to get details for.",
      required: !0
    })
  },
  async run(context) {
    let url = `${API_BASE_URL}/v1/plans/visits/${context.propsValue.visit_id}/detail/`, response = await import_pieces_common15.httpClient.sendRequest({
      method: import_pieces_common15.HttpMethod.GET,
      url,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/visits/get-visits.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework"), import_pieces_common16 = require("@activepieces/pieces-common");
var get_visits = (0, import_pieces_framework16.createAction)({
  name: "get_visits",
  auth: simplirouteAuth,
  displayName: "Get Visits",
  description: "Retrieve all registered visits. Can be filtered by planned visit date.",
  audience: "both",
  aiMetadata: { description: "List registered visits filtered by planned date. Read-only and idempotent; use to enumerate the visits scheduled for a day. For a single visit by ID use the get-visit or get-visit-detail action instead.", idempotent: !0 },
  props: {
    planned_date: import_pieces_framework16.Property.ShortText({
      displayName: "planned_date",
      description: "Filter visits by planned date (YYYY-MM-DD format).",
      required: !0
    })
  },
  async run(context) {
    let queryString = "";
    context.propsValue.planned_date && (queryString += (queryString ? "&" : "?") + "planned_date=" + context.propsValue.planned_date);
    let url = `${API_BASE_URL}/v1/routes/visits/${queryString}`, response = await import_pieces_common16.httpClient.sendRequest({
      method: import_pieces_common16.HttpMethod.GET,
      url,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/visits/update-visit.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework"), import_pieces_common17 = require("@activepieces/pieces-common");
var update_visit = (0, import_pieces_framework17.createAction)({
  name: "update_visit",
  auth: simplirouteAuth,
  displayName: "Update Visit (Complete)",
  description: "Completely update an existing visit with all fields.",
  audience: "both",
  aiMetadata: { description: "Fully replace an existing visit (PUT) by ID, requiring title, address, and planned date even for unchanged fields. Idempotent for a fixed payload. Pick this when overwriting the whole visit; use the partial-update action to change only a few fields without resending the rest.", idempotent: !0 },
  props: {
    visit_id: import_pieces_framework17.Property.Number({ displayName: "visit_id", description: "ID of the visit to update.", required: !0 }),
    title: import_pieces_framework17.Property.ShortText({ displayName: "title", description: "Visit title.", required: !0 }),
    address: import_pieces_framework17.Property.ShortText({ displayName: "address", description: "Visit address.", required: !0 }),
    latitude: import_pieces_framework17.Property.Number({ displayName: "latitude", description: "Visit latitude.", required: !1 }),
    longitude: import_pieces_framework17.Property.Number({ displayName: "longitude", description: "Visit longitude.", required: !1 }),
    load: import_pieces_framework17.Property.Number({ displayName: "load", description: "Visit load (main unit).", required: !1 }),
    load_2: import_pieces_framework17.Property.Number({ displayName: "load_2", description: "Visit load (unit 2).", required: !1 }),
    load_3: import_pieces_framework17.Property.Number({ displayName: "load_3", description: "Visit load (unit 3).", required: !1 }),
    duration: import_pieces_framework17.Property.ShortText({ displayName: "duration", description: "Visit duration (HH:MM:SS).", required: !1 }),
    contact_name: import_pieces_framework17.Property.ShortText({ displayName: "contact_name", description: "Visit contact name.", required: !1 }),
    contact_phone: import_pieces_framework17.Property.ShortText({ displayName: "contact_phone", description: "Visit contact phone.", required: !1 }),
    notes: import_pieces_framework17.Property.ShortText({ displayName: "notes", description: "Visit notes.", required: !1 }),
    planned_date: import_pieces_framework17.Property.ShortText({ displayName: "planned_date", description: "Visit planned date (YYYY-MM-DD).", required: !0 })
  },
  async run(context) {
    let _a = context.propsValue, { visit_id } = _a, body = __objRest(_a, ["visit_id"]), response = await import_pieces_common17.httpClient.sendRequest({
      method: import_pieces_common17.HttpMethod.PUT,
      url: `${API_BASE_URL}/v1/routes/visits/${visit_id}/`,
      body,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/visits/update-visit-partial.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework"), import_pieces_common18 = require("@activepieces/pieces-common");
var update_visit_partial = (0, import_pieces_framework18.createAction)({
  name: "update_visit_partial",
  auth: simplirouteAuth,
  displayName: "Update Visit (Partial)",
  description: "Partially update fields of an existing visit.",
  audience: "both",
  aiMetadata: { description: "Patch only the supplied fields of an existing visit by ID, leaving other fields untouched. Idempotent for a fixed payload. Prefer this over the complete-update action when changing just a few fields; only visit_id is required.", idempotent: !0 },
  props: {
    visit_id: import_pieces_framework18.Property.Number({ displayName: "visit_id", description: "ID of the visit to update.", required: !0 }),
    title: import_pieces_framework18.Property.ShortText({ displayName: "title", description: "Visit title (if you want to change it).", required: !1 }),
    address: import_pieces_framework18.Property.ShortText({ displayName: "address", description: "Visit address (if you want to change it).", required: !1 }),
    load: import_pieces_framework18.Property.Number({ displayName: "load", description: "Visit load (main unit).", required: !1 }),
    load_2: import_pieces_framework18.Property.Number({ displayName: "load_2", description: "Visit load (unit 2).", required: !1 }),
    load_3: import_pieces_framework18.Property.Number({ displayName: "load_3", description: "Visit load (unit 3).", required: !1 }),
    duration: import_pieces_framework18.Property.ShortText({ displayName: "duration", description: "Visit duration (HH:MM:SS).", required: !1 }),
    contact_name: import_pieces_framework18.Property.ShortText({ displayName: "contact_name", description: "Visit contact name.", required: !1 }),
    contact_phone: import_pieces_framework18.Property.ShortText({ displayName: "contact_phone", description: "Visit contact phone.", required: !1 }),
    notes: import_pieces_framework18.Property.ShortText({ displayName: "notes", description: "Visit notes.", required: !1 }),
    planned_date: import_pieces_framework18.Property.ShortText({ displayName: "planned_date", description: "Visit planned date (YYYY-MM-DD).", required: !1 })
  },
  async run(context) {
    let _a = context.propsValue, { visit_id } = _a, body = __objRest(_a, ["visit_id"]), response = await import_pieces_common18.httpClient.sendRequest({
      method: import_pieces_common18.HttpMethod.PATCH,
      url: `${API_BASE_URL}/v1/routes/visits/${visit_id}/`,
      body,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/routes/create-route.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework"), import_pieces_common19 = require("@activepieces/pieces-common");
var create_route = (0, import_pieces_framework19.createAction)({
  name: "create_route",
  auth: simplirouteAuth,
  displayName: "Create Route",
  description: "Create a new route manually by assigning vehicle, driver and date.",
  audience: "both",
  aiMetadata: { description: "Manually create a route under an existing plan, assigning a vehicle, driver, planned date, estimated time window and start/end locations. Use to build a route by hand rather than via automatic planning; not idempotent, so each call creates another route. Requires the parent plan UUID and the vehicle/driver ids.", idempotent: !1 },
  props: {
    vehicle: import_pieces_framework19.Property.Number({ displayName: "vehicle", description: "Vehicle ID for the route.", required: !0 }),
    driver: import_pieces_framework19.Property.Number({ displayName: "driver", description: "Driver ID for the route.", required: !0 }),
    planned_date: import_pieces_framework19.Property.ShortText({ displayName: "planned_date", description: "Planned route date (YYYY-MM-DD).", required: !0 }),
    estimated_time_start: import_pieces_framework19.Property.ShortText({ displayName: "estimated_time_start", description: "Estimated start time (HH:MM:SS).", required: !0 }),
    estimated_time_end: import_pieces_framework19.Property.ShortText({ displayName: "estimated_time_end", description: "Estimated end time (HH:MM:SS).", required: !0 }),
    location_start_address: import_pieces_framework19.Property.ShortText({ displayName: "location_start_address", description: "Route start address.", required: !0 }),
    location_start_latitude: import_pieces_framework19.Property.ShortText({ displayName: "location_start_latitude", description: "Route start latitude.", required: !0 }),
    location_start_longitude: import_pieces_framework19.Property.ShortText({ displayName: "location_start_longitude", description: "Route start longitude.", required: !0 }),
    location_end_address: import_pieces_framework19.Property.ShortText({ displayName: "location_end_address", description: "Route end address.", required: !0 }),
    location_end_latitude: import_pieces_framework19.Property.ShortText({ displayName: "location_end_latitude", description: "Route end latitude.", required: !0 }),
    location_end_longitude: import_pieces_framework19.Property.ShortText({ displayName: "location_end_longitude", description: "Route end longitude.", required: !0 }),
    plan: import_pieces_framework19.Property.ShortText({ displayName: "plan", description: "Plan (execution) ID to associate the route (UUID).", required: !0 })
  },
  async run(context) {
    let body = context.propsValue, response = await import_pieces_common19.httpClient.sendRequest({
      method: import_pieces_common19.HttpMethod.POST,
      url: `${API_BASE_URL}/v1/routes/routes/`,
      body,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/routes/delete-route.ts
var import_pieces_framework20 = require("@activepieces/pieces-framework"), import_pieces_common20 = require("@activepieces/pieces-common");
var delete_route = (0, import_pieces_framework20.createAction)({
  name: "delete_route",
  auth: simplirouteAuth,
  displayName: "Delete Route",
  description: "Delete a route by its ID.",
  audience: "both",
  aiMetadata: { description: "Permanently delete a single route identified by its UUID. Use to remove an unwanted or mistaken route; destructive and irreversible. Deleting an already-removed route id simply has no further effect.", idempotent: !1 },
  props: {
    route_id: import_pieces_framework20.Property.ShortText({
      displayName: "route_id",
      description: "Route ID to delete (UUID).",
      required: !0
    })
  },
  async run(context) {
    let url = `${API_BASE_URL}/v1/routes/routes/${context.propsValue.route_id}/`, response = await import_pieces_common20.httpClient.sendRequest({
      method: import_pieces_common20.HttpMethod.DELETE,
      url,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/routes/get-route.ts
var import_pieces_framework21 = require("@activepieces/pieces-framework"), import_pieces_common21 = require("@activepieces/pieces-common");
var get_route = (0, import_pieces_framework21.createAction)({
  name: "get_route",
  auth: simplirouteAuth,
  displayName: "Get Route",
  description: "Retrieve details of a specific route.",
  audience: "both",
  aiMetadata: { description: "Fetch the full details of one route by its UUID, including assigned vehicle, driver and stops. Use when you already have a route id; to find routes by date use Get Routes instead. Read-only and idempotent.", idempotent: !0 },
  props: {
    route_id: import_pieces_framework21.Property.ShortText({
      displayName: "route_id",
      description: "Route ID (UUID).",
      required: !0
    })
  },
  async run(context) {
    let url = `${API_BASE_URL}/v1/routes/routes/${context.propsValue.route_id}/`, response = await import_pieces_common21.httpClient.sendRequest({
      method: import_pieces_common21.HttpMethod.GET,
      url,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/routes/get-routes.ts
var import_pieces_framework22 = require("@activepieces/pieces-framework"), import_pieces_common22 = require("@activepieces/pieces-common");
var get_routes = (0, import_pieces_framework22.createAction)({
  name: "get_routes",
  auth: simplirouteAuth,
  displayName: "Get Routes",
  description: "Retrieve the list of existing routes.",
  audience: "both",
  aiMetadata: { description: "List all routes planned for a given date (YYYY-MM-DD), returning each route with its assigned vehicle, driver and stops. Use to review or pick routes for a specific day; for a single known route id use Get Route instead. Read-only and idempotent.", idempotent: !0 },
  props: { planned_date: import_pieces_framework22.Property.ShortText({ displayName: "planned_date", description: "Visit planned date (YYYY-MM-DD).", required: !0 }) },
  async run(context) {
    let body = context.propsValue, url = `${API_BASE_URL}/v1/routes/routes/?planned_date=${body.planned_date}`, response = await import_pieces_common22.httpClient.sendRequest({
      method: import_pieces_common22.HttpMethod.GET,
      url,
      body,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/plans/create-plan.ts
var import_pieces_framework23 = require("@activepieces/pieces-framework"), import_pieces_common23 = require("@activepieces/pieces-common");
var create_plan = (0, import_pieces_framework23.createAction)({
  name: "create_plan",
  auth: simplirouteAuth,
  displayName: "Create an empty plan",
  description: "Create an empty plan (execution) without routes (reserve a planning day).",
  audience: "both",
  aiMetadata: { description: "Create a new empty routing plan (execution) for a given date range, reserving a planning day before routes are added. Not idempotent: each call creates a separate plan, so guard against duplicate plans for the same date.", idempotent: !1 },
  props: {
    name: import_pieces_framework23.Property.ShortText({ displayName: "name", description: "Plan name.", required: !0 }),
    start_date: import_pieces_framework23.Property.ShortText({ displayName: "start_date", description: "Start date (YYYY-MM-DD).", required: !0 }),
    end_date: import_pieces_framework23.Property.ShortText({ displayName: "end_date", description: "End date (YYYY-MM-DD).", required: !0 })
  },
  async run(context) {
    let body = context.propsValue, response = await import_pieces_common23.httpClient.sendRequest({
      method: import_pieces_common23.HttpMethod.POST,
      url: `${API_BASE_URL}/v1/plans/create-plan/`,
      body,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/plans/get-plan-vehicles.ts
var import_pieces_framework24 = require("@activepieces/pieces-framework"), import_pieces_common24 = require("@activepieces/pieces-common");
var get_plan_vehicles = (0, import_pieces_framework24.createAction)({
  name: "get_plan_vehicles",
  auth: simplirouteAuth,
  displayName: "Get vehicles with routes on date",
  description: "Returns the vehicles that have planned routes on the indicated date.",
  audience: "both",
  aiMetadata: { description: "Look up which vehicles have planned routes on a specific date. Read-only and idempotent; use when you need the routed-vehicle subset for a day rather than the full plan or the entire vehicle fleet.", idempotent: !0 },
  props: {
    planned_date: import_pieces_framework24.Property.ShortText({ displayName: "planned_date", description: "Planned date (YYYY-MM-DD).", required: !0 })
  },
  async run(context) {
    let url = `${API_BASE_URL}/v1/plans/${context.propsValue.planned_date}/vehicles/`, response = await import_pieces_common24.httpClient.sendRequest({
      method: import_pieces_common24.HttpMethod.GET,
      url,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/plans/get-plans.ts
var import_pieces_framework25 = require("@activepieces/pieces-framework"), import_pieces_common25 = require("@activepieces/pieces-common");
var get_plans = (0, import_pieces_framework25.createAction)({
  name: "get_plans",
  auth: simplirouteAuth,
  displayName: "Get Plans",
  description: "Retrieve the list of saved routing plans (executions).",
  audience: "both",
  aiMetadata: { description: "Retrieve the saved routing plans (executions) for a given date. Read-only and idempotent; the date is required, so use it to inspect what plans exist for a specific day.", idempotent: !0 },
  props: { date: import_pieces_framework25.Property.ShortText({ displayName: "date", description: "Planned date (YYYY-MM-DD).", required: !0 }) },
  async run(context) {
    let url = `${API_BASE_URL}/v1/plans/${context.propsValue.date}/`, response = await import_pieces_common25.httpClient.sendRequest({
      method: import_pieces_common25.HttpMethod.GET,
      url,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/users/create-users.ts
var import_pieces_framework26 = require("@activepieces/pieces-framework"), import_pieces_common26 = require("@activepieces/pieces-common");
var create_users = (0, import_pieces_framework26.createAction)({
  name: "create_users",
  auth: simplirouteAuth,
  displayName: "Create User",
  description: "Create new user/driver in the account.",
  audience: "both",
  aiMetadata: { description: "Create a new user/driver, requiring username, password, and the is_driver flag. Not idempotent: each call creates another account, so do not retry blindly. To change an existing user use the update-user action.", idempotent: !1 },
  props: {
    username: import_pieces_framework26.Property.ShortText({ displayName: "username", description: "Nombre de usuario (login).", required: !0 }),
    name: import_pieces_framework26.Property.ShortText({ displayName: "name", description: "Nombre completo del usuario.", required: !1 }),
    phone: import_pieces_framework26.Property.ShortText({ displayName: "phone", description: "N\xFAmero de tel\xE9fono.", required: !1 }),
    email: import_pieces_framework26.Property.ShortText({ displayName: "email", description: "Correo electr\xF3nico.", required: !1 }),
    is_admin: import_pieces_framework26.Property.Checkbox({ displayName: "is_admin", description: "Whether the user is an administrator.", required: !1, defaultValue: !1 }),
    password: import_pieces_framework26.Property.ShortText({ displayName: "password", description: "User password.", required: !0 }),
    is_driver: import_pieces_framework26.Property.Checkbox({ displayName: "is_driver", description: "Whether the user is a driver.", required: !0, defaultValue: !1 }),
    is_router_jr: import_pieces_framework26.Property.Checkbox({ displayName: "is_router_jr", description: "Whether the user is a router junior.", required: !1, defaultValue: !1 }),
    is_router: import_pieces_framework26.Property.Checkbox({ displayName: "is_router", description: "Whether the user is a router.", required: !1, defaultValue: !1 }),
    is_monitor: import_pieces_framework26.Property.Checkbox({ displayName: "is_monitor", description: "Whether the user is a monitor.", required: !1, defaultValue: !1 }),
    is_coordinator: import_pieces_framework26.Property.Checkbox({ displayName: "is_coordinator", description: "Whether the user is a coordinator.", required: !1, defaultValue: !1 })
  },
  async run(context) {
    let body = context.propsValue, response = await import_pieces_common26.httpClient.sendRequest({
      method: import_pieces_common26.HttpMethod.POST,
      url: `${API_BASE_URL}/v1/accounts/drivers/`,
      body,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/users/get-drivers.ts
var import_pieces_framework27 = require("@activepieces/pieces-framework"), import_pieces_common27 = require("@activepieces/pieces-common");
var get_drivers = (0, import_pieces_framework27.createAction)({
  name: "get_drivers",
  auth: simplirouteAuth,
  displayName: "Get Drivers",
  description: "Retrieve the list of drivers registered in the account.",
  audience: "both",
  aiMetadata: { description: "List all drivers/users registered in the account. Read-only and idempotent; takes no input. Use to discover user IDs before fetching, updating, or assigning a specific driver.", idempotent: !0 },
  props: {},
  async run(context) {
    let url = `${API_BASE_URL}/v1/accounts/drivers/`, response = await import_pieces_common27.httpClient.sendRequest({
      method: import_pieces_common27.HttpMethod.GET,
      url,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/users/get-user.ts
var import_pieces_framework28 = require("@activepieces/pieces-framework"), import_pieces_common28 = require("@activepieces/pieces-common");
var get_user = (0, import_pieces_framework28.createAction)({
  name: "get_user",
  auth: simplirouteAuth,
  displayName: "Get User",
  description: "Retrieve information of a specific user by ID.",
  audience: "both",
  aiMetadata: { description: "Retrieve a single user/driver by its ID. Read-only and idempotent. Use when you already know the user ID; to list all drivers use the get-drivers action.", idempotent: !0 },
  props: {
    user_id: import_pieces_framework28.Property.Number({
      displayName: "user_id",
      description: "User ID.",
      required: !0
    })
  },
  async run(context) {
    let url = `${API_BASE_URL}/v1/accounts/drivers/${context.propsValue.user_id}/`, response = await import_pieces_common28.httpClient.sendRequest({
      method: import_pieces_common28.HttpMethod.GET,
      url,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/users/update-user.ts
var import_pieces_framework29 = require("@activepieces/pieces-framework"), import_pieces_common29 = require("@activepieces/pieces-common");
var update_user = (0, import_pieces_framework29.createAction)({
  name: "update_user",
  auth: simplirouteAuth,
  displayName: "Update User",
  description: "Update information of an existing user.",
  audience: "both",
  aiMetadata: { description: "Fully update an existing user/driver (PUT) by ID, requiring username and name along with the user_id. Idempotent for a fixed payload since it overwrites the user record. Use the get-drivers action to find the user_id first.", idempotent: !0 },
  props: {
    user_id: import_pieces_framework29.Property.Number({ displayName: "user_id", description: "User ID to update.", required: !0 }),
    username: import_pieces_framework29.Property.ShortText({ displayName: "username", description: "Username (login).", required: !0 }),
    name: import_pieces_framework29.Property.ShortText({ displayName: "name", description: "Full name of the user.", required: !0 }),
    phone: import_pieces_framework29.Property.ShortText({ displayName: "phone", description: "User phone number.", required: !1 }),
    email: import_pieces_framework29.Property.ShortText({ displayName: "email", description: "User email address.", required: !1 }),
    is_admin: import_pieces_framework29.Property.Checkbox({ displayName: "is_admin", description: "Whether the user is an administrator.", required: !1, defaultValue: !1 }),
    password: import_pieces_framework29.Property.ShortText({ displayName: "password", description: "User password.", required: !1 }),
    is_driver: import_pieces_framework29.Property.Checkbox({ displayName: "is_driver", description: "Whether the user is a driver.", required: !1, defaultValue: !1 }),
    is_router_jr: import_pieces_framework29.Property.Checkbox({ displayName: "is_router_jr", description: "Whether the user is a router junior.", required: !1, defaultValue: !1 }),
    is_router: import_pieces_framework29.Property.Checkbox({ displayName: "is_router", description: "Whether the user is a router.", required: !1, defaultValue: !1 }),
    is_monitor: import_pieces_framework29.Property.Checkbox({ displayName: "is_monitor", description: "Whether the user is a monitor.", required: !1, defaultValue: !1 }),
    is_coordinator: import_pieces_framework29.Property.Checkbox({ displayName: "is_coordinator", description: "Whether the user is a coordinator.", required: !1, defaultValue: !1 })
  },
  async run(context) {
    let _a = context.propsValue, { user_id } = _a, body = __objRest(_a, ["user_id"]), response = await import_pieces_common29.httpClient.sendRequest({
      method: import_pieces_common29.HttpMethod.PUT,
      url: `${API_BASE_URL}/v1/accounts/drivers/${user_id}/`,
      body,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/metadata/get-fleets.ts
var import_pieces_framework30 = require("@activepieces/pieces-framework"), import_pieces_common30 = require("@activepieces/pieces-common");
var get_fleets = (0, import_pieces_framework30.createAction)({
  name: "get_fleets",
  auth: simplirouteAuth,
  displayName: "Get Fleets",
  description: "Retrieve the list of fleets available in the account.",
  audience: "both",
  aiMetadata: { description: "List all fleets configured in the account, returning their ids and names. Use to resolve a valid fleet reference before grouping vehicles or filtering planning by fleet. Read-only and idempotent; takes no inputs.", idempotent: !0 },
  props: {},
  async run(context) {
    let url = `${API_BASE_URL}/v1/fleets/`, response = await import_pieces_common30.httpClient.sendRequest({
      method: import_pieces_common30.HttpMethod.GET,
      url,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/metadata/get-observations.ts
var import_pieces_framework31 = require("@activepieces/pieces-framework"), import_pieces_common31 = require("@activepieces/pieces-common");
var get_observations = (0, import_pieces_framework31.createAction)({
  name: "get_observations",
  auth: simplirouteAuth,
  displayName: "Get Observations",
  description: "Retrieve the list of observations available in the account.",
  audience: "both",
  aiMetadata: { description: "List the route observation types defined in the account, returning their ids and labels. Use to resolve a valid observation reference before applying it to a route or visit. Read-only and idempotent; takes no inputs.", idempotent: !0 },
  props: {},
  async run(context) {
    let url = `${API_BASE_URL}/v1/routes/observations/`, response = await import_pieces_common31.httpClient.sendRequest({
      method: import_pieces_common31.HttpMethod.GET,
      url,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/metadata/get-sellers.ts
var import_pieces_framework32 = require("@activepieces/pieces-framework"), import_pieces_common32 = require("@activepieces/pieces-common");
var get_sellers = (0, import_pieces_framework32.createAction)({
  name: "get_sellers",
  auth: simplirouteAuth,
  displayName: "Get Sellers",
  description: "Retrieve the list of sellers available in the account.",
  audience: "both",
  aiMetadata: { description: "List all sellers configured in the account, returning their ids and names. Use to resolve a seller reference before tagging clients or visits with one. Read-only and idempotent; takes no inputs.", idempotent: !0 },
  props: {},
  async run(context) {
    let url = `${API_BASE_URL}/v1/sellers/`, response = await import_pieces_common32.httpClient.sendRequest({
      method: import_pieces_common32.HttpMethod.GET,
      url,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/metadata/get-skills.ts
var import_pieces_framework33 = require("@activepieces/pieces-framework"), import_pieces_common33 = require("@activepieces/pieces-common");
var get_skills = (0, import_pieces_framework33.createAction)({
  name: "get_skills",
  auth: simplirouteAuth,
  displayName: "Get Skills",
  description: "Retrieve the list of skills available in the account.",
  audience: "both",
  aiMetadata: { description: "List all driver/vehicle skills defined in the account, returning their ids and names. Use to resolve a valid skill reference before requiring it on visits or assigning it to resources. Read-only and idempotent; takes no inputs.", idempotent: !0 },
  props: {},
  async run(context) {
    let url = `${API_BASE_URL}/v1/routes/skills/`, response = await import_pieces_common33.httpClient.sendRequest({
      method: import_pieces_common33.HttpMethod.GET,
      url,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/metadata/get-tags.ts
var import_pieces_framework34 = require("@activepieces/pieces-framework"), import_pieces_common34 = require("@activepieces/pieces-common");
var get_tags = (0, import_pieces_framework34.createAction)({
  name: "get_tags",
  auth: simplirouteAuth,
  displayName: "Get Tags",
  description: "Retrieve the list of tags available in the account.",
  audience: "both",
  aiMetadata: { description: "List all route tags defined in the account, returning their ids and names. Use to resolve a valid tag reference before applying tags to routes or visits. Read-only and idempotent; takes no inputs.", idempotent: !0 },
  props: {},
  async run(context) {
    let url = `${API_BASE_URL}/v1/routes/tags/`, response = await import_pieces_common34.httpClient.sendRequest({
      method: import_pieces_common34.HttpMethod.GET,
      url,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/lib/actions/metadata/get-zones.ts
var import_pieces_framework35 = require("@activepieces/pieces-framework"), import_pieces_common35 = require("@activepieces/pieces-common");
var get_zones = (0, import_pieces_framework35.createAction)({
  name: "get_zones",
  auth: simplirouteAuth,
  displayName: "Get Zones",
  description: "Retrieve the list of zones available in the account.",
  audience: "both",
  aiMetadata: { description: "List all geographic zones defined in the account, returning their ids and names. Use to resolve a zone reference before assigning it to clients or visits. Read-only and idempotent; takes no inputs.", idempotent: !0 },
  props: {},
  async run(context) {
    let url = `${API_BASE_URL}/v1/zones/`, response = await import_pieces_common35.httpClient.sendRequest({
      method: import_pieces_common35.HttpMethod.GET,
      url,
      headers: __spreadProps(__spreadValues({}, commonHeaders), {
        Authorization: `Token ${context.auth.secret_text}`
      })
    });
    return {
      status: response.status,
      data: response.body
    };
  }
});

// ../ap/packages/pieces/community/simpliroute/src/index.ts
var simpliroute = (0, import_pieces_framework36.createPiece)({
  displayName: "SimpliRoute",
  description: "Connect with SimpliRoute, the last-mile delivery optimization platform. Manage clients, vehicles, visits, routes, and optimize your delivery operations.",
  logoUrl: "https://cdn.activepieces.com/pieces/simpliroute.png",
  minimumSupportedRelease: "0.30.0",
  categories: [import_pieces_framework37.PieceCategory.PRODUCTIVITY, import_pieces_framework37.PieceCategory.BUSINESS_INTELLIGENCE],
  authors: ["rfjaimes-simpli"],
  auth: simplirouteAuth,
  actions: [
    get_me,
    get_clients,
    create_clients,
    bulk_delete_clients,
    create_client_property,
    get_vehicles,
    create_vehicle,
    get_vehicle,
    delete_vehicle,
    get_visits,
    create_visits,
    get_visit,
    update_visit_partial,
    update_visit,
    delete_visit,
    add_visit_items,
    get_routes,
    create_route,
    get_route,
    delete_route,
    get_plans,
    create_plan,
    get_plan_vehicles,
    get_visit_detail,
    get_drivers,
    create_users,
    get_user,
    update_user,
    get_skills,
    get_observations,
    get_tags,
    get_zones,
    get_fleets,
    get_sellers,
    (0, import_pieces_common36.createCustomApiCallAction)({
      baseUrl: () => "https://api.simpliroute.com",
      auth: simplirouteAuth,
      authMapping: async (auth) => ({
        Authorization: `Token ${auth}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  simpliroute
});
