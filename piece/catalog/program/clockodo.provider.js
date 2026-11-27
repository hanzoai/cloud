"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames;
var __hasOwnProp = Object.prototype.hasOwnProperty;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value;
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
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key != "symbol" ? key + "" : key, value);

// ../ap/packages/pieces/community/clockodo/src/index.ts
var index_exports = {};
__export(index_exports, {
  clockodo: () => clockodo
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework38 = require("@activepieces/pieces-framework"), import_pieces_framework39 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/clockodo/src/lib/actions/entry/create-entry.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/clockodo/src/lib/common/index.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/clockodo/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/clockodo/src/lib/common/models/common.ts
function emptyValueFilter(accessor) {
  return (key) => {
    let val = accessor(key);
    return val != null && (typeof val != "string" || val.length > 0);
  };
}
function prepareListRequest(request) {
  let params = {}, requestObj = request;
  return Object.keys(request).filter((k) => k != "filter").filter(emptyValueFilter((k) => requestObj[k])).forEach((k) => {
    params[k] = request[k].toString();
  }), request.filter && Object.keys(request.filter).filter(emptyValueFilter((k) => request.filter[k])).forEach((k) => {
    params["filter[" + k + "]"] = request.filter[k].toString();
  }), params;
}

// ../ap/packages/pieces/community/clockodo/src/lib/common/client.ts
var ClockodoClient = class {
  constructor(email, token, clientName, clientEmail) {
    __publicField(this, "email", email);
    __publicField(this, "token", token);
    __publicField(this, "clientIdentification");
    __publicField(this, "language", "en");
    this.clientIdentification = clientName + ";" + clientEmail;
  }
  setLanguage(language) {
    this.language = language;
  }
  async makeRequest(method, url, query, body) {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: "https://my.clockodo.com/api" + url,
      queryParams: query,
      body,
      headers: {
        "X-ClockodoApiUser": this.email,
        "X-ClockodoApiKey": this.token,
        "X-Clockodo-External-Application": this.clientIdentification,
        "Accept-Language": this.language
      }
    })).body;
  }
  listUsers() {
    return this.makeRequest(import_pieces_common.HttpMethod.GET, "/v2/users");
  }
  getUser(id) {
    return this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/v2/users/" + id
    );
  }
  createUser(request) {
    return this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      "/v2/users",
      void 0,
      request
    );
  }
  updateUser(id, request) {
    return this.makeRequest(
      import_pieces_common.HttpMethod.PUT,
      "/v2/users/" + id,
      void 0,
      request
    );
  }
  deleteUser(id) {
    return this.makeRequest(import_pieces_common.HttpMethod.DELETE, "/v2/users/" + id);
  }
  listTeams() {
    return this.makeRequest(import_pieces_common.HttpMethod.GET, "/v2/teams");
  }
  getTeam(id) {
    return this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/v2/teams/" + id
    );
  }
  createTeam(request) {
    return this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      "/v2/teams",
      void 0,
      request
    );
  }
  updateTeam(id, request) {
    return this.makeRequest(
      import_pieces_common.HttpMethod.PUT,
      "/v2/teams/" + id,
      void 0,
      request
    );
  }
  deleteTeam(id) {
    return this.makeRequest(import_pieces_common.HttpMethod.DELETE, "/v2/teams/" + id);
  }
  listCustomers(request = {}) {
    return this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/v2/customers",
      prepareListRequest(request)
    );
  }
  async listAllCustomers(filter = {}) {
    let totalPages = 999999, all = [];
    for (let page = 0; page < totalPages; page++) {
      let res = await this.listCustomers({
        page: page + 1,
        filter
      });
      totalPages = res.paging.count_pages, res.customers.forEach((e) => all.push(e));
    }
    return all;
  }
  getCustomer(id) {
    return this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/v2/customers/" + id
    );
  }
  createCustomer(request) {
    return this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      "/v2/customers",
      void 0,
      request
    );
  }
  updateCustomer(id, request) {
    return this.makeRequest(
      import_pieces_common.HttpMethod.PUT,
      "/v2/customers/" + id,
      void 0,
      request
    );
  }
  deleteCustomer(id) {
    return this.makeRequest(import_pieces_common.HttpMethod.DELETE, "/v2/customers/" + id);
  }
  listProjects(request = {}) {
    return this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/v2/projects",
      prepareListRequest(request)
    );
  }
  async listAllProjects(filter = {}) {
    let totalPages = 999999, all = [];
    for (let page = 0; page < totalPages; page++) {
      let res = await this.listProjects({
        page: page + 1,
        filter
      });
      totalPages = res.paging.count_pages, res.projects.forEach((e) => all.push(e));
    }
    return all;
  }
  getProject(id) {
    return this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/v2/projects/" + id
    );
  }
  createProject(request) {
    return this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      "/v2/projects",
      void 0,
      request
    );
  }
  updateProject(id, request) {
    return this.makeRequest(
      import_pieces_common.HttpMethod.PUT,
      "/v2/projects/" + id,
      void 0,
      request
    );
  }
  deleteProject(id) {
    return this.makeRequest(import_pieces_common.HttpMethod.DELETE, "/v2/projects/" + id);
  }
  listServices() {
    return this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/v2/services"
    );
  }
  getService(id) {
    return this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/v2/services/" + id
    );
  }
  createService(request) {
    return this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      "/v2/services",
      void 0,
      request
    );
  }
  updateService(id, request) {
    return this.makeRequest(
      import_pieces_common.HttpMethod.PUT,
      "/v2/services/" + id,
      void 0,
      request
    );
  }
  deleteService(id) {
    return this.makeRequest(import_pieces_common.HttpMethod.DELETE, "/v2/services/" + id);
  }
  listEntries(request) {
    return this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/v2/entries",
      prepareListRequest(request)
    );
  }
  async listAllEntries(time_since, time_until, filter = {}) {
    let totalPages = 999999, all = [];
    for (let page = 0; page < totalPages; page++) {
      let res = await this.listEntries({
        page: page + 1,
        time_since,
        time_until,
        filter
      });
      totalPages = res.paging.count_pages, res.entries.forEach((e) => all.push(e));
    }
    return all;
  }
  getEntry(id) {
    return this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/v2/entries/" + id
    );
  }
  createEntry(request) {
    return this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      "/v2/entries",
      void 0,
      request
    );
  }
  updateEntry(id, request) {
    return this.makeRequest(
      import_pieces_common.HttpMethod.PUT,
      "/v2/entries/" + id,
      void 0,
      request
    );
  }
  deleteEntry(id) {
    return this.makeRequest(import_pieces_common.HttpMethod.DELETE, "/v2/entries/" + id);
  }
  listAbsences(request) {
    let query = {
      year: request.year.toString()
    };
    return request.users_id && (query.users_id = request.users_id.toString()), this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/absences",
      query
    );
  }
  getAbsence(id) {
    return this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/absences/" + id
    );
  }
  createAbsence(request) {
    return this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      "/absences",
      void 0,
      request
    );
  }
  updateAbsence(id, request) {
    return this.makeRequest(
      import_pieces_common.HttpMethod.PUT,
      "/absences/" + id,
      void 0,
      request
    );
  }
  deleteAbsence(id) {
    return this.makeRequest(import_pieces_common.HttpMethod.DELETE, "/absences/" + id);
  }
};

// ../ap/packages/pieces/community/clockodo/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), clockodoAuth = import_pieces_framework.PieceAuth.CustomAuth({
  required: !0,
  props: {
    email: import_pieces_framework.Property.ShortText({
      displayName: "E-Mail",
      required: !0,
      description: "The email of your clockodo user"
    }),
    token: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API-Token",
      description: "Your api token (can be found in profile settings)",
      required: !0
    }),
    company_name: import_pieces_framework.Property.ShortText({
      displayName: "Company Name",
      description: "Your company name or app name",
      required: !0
    }),
    company_email: import_pieces_framework.Property.ShortText({
      displayName: "Company E-Mail",
      description: "A contact email for your company or app",
      required: !0
    })
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/common/index.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
function makeClient(auth) {
  return new ClockodoClient(
    auth.email,
    auth.token,
    auth.company_name,
    auth.company_email
  );
}
var clockodoCommon = {
  absenceType: (required = !0) => import_pieces_framework2.Property.StaticDropdown({
    displayName: "Type",
    required,
    options: {
      options: [
        { value: 1, label: "Regular holiday" },
        { value: 2, label: "Special leaves" },
        { value: 3, label: "Reduction of overtime" },
        { value: 4, label: "Sick day" },
        { value: 5, label: "Sick day of a child" },
        { value: 6, label: "School / further education" },
        { value: 7, label: "Maternity protection" },
        { value: 8, label: "Home office (planned hours are applied)" },
        { value: 9, label: "Work out of office (planned hours are applied)" },
        { value: 10, label: "Special leaves (unpaid)" },
        { value: 11, label: "Sick day (unpaid)" },
        { value: 12, label: "Sick day of child (unpaid)" },
        { value: 13, label: "Quarantine" },
        {
          value: 14,
          label: "Military / alternative service (only full days)"
        },
        { value: 15, label: "Sick day (sickness benefit)" }
      ]
    }
  }),
  customer_id: (required = !0, active = !0) => import_pieces_framework2.Property.Dropdown({
    auth: clockodoAuth,
    description: "The ID of the customer",
    displayName: "Customer",
    required,
    refreshers: [],
    options: async ({ auth }) => (0, import_pieces_framework3.isNil)(auth) ? {
      disabled: !0,
      placeholder: "setup authentication first",
      options: []
    } : {
      disabled: !1,
      options: (await makeClient(auth.props).listAllCustomers({
        active: active === null ? void 0 : active
      })).map((customer) => ({
        label: customer.name,
        value: customer.id
      }))
    }
  }),
  project_id: (required = !0, requiresCustomer = !0, active = !0) => import_pieces_framework2.Property.Dropdown({
    auth: clockodoAuth,
    description: "The ID of the project",
    displayName: "Project",
    required,
    refreshers: [...requiresCustomer ? ["customer_id"] : []],
    options: async ({ auth, customer_id }) => (0, import_pieces_framework3.isNil)(auth) ? {
      disabled: !0,
      placeholder: "setup authentication first",
      options: []
    } : requiresCustomer && !customer_id ? {
      disabled: !0,
      placeholder: "select a customer first",
      options: []
    } : {
      disabled: !1,
      options: (await makeClient(auth.props).listAllProjects({
        active: active === null ? void 0 : active,
        customers_id: requiresCustomer ? parseInt(customer_id) : void 0
      })).map((project) => ({
        label: project.name,
        value: project.id
      }))
    }
  }),
  user_id: (required = !0, active = !0) => import_pieces_framework2.Property.Dropdown({
    auth: clockodoAuth,
    description: "The ID of the user",
    displayName: "User",
    required,
    refreshers: [],
    options: async ({ auth }) => (0, import_pieces_framework3.isNil)(auth) ? {
      disabled: !0,
      placeholder: "setup authentication first",
      options: []
    } : {
      disabled: !1,
      options: (await makeClient(auth.props).listUsers()).users.filter((u) => active === null || u.active === active).map((user) => ({
        label: user.name,
        value: user.id
      }))
    }
  }),
  team_id: (required = !0) => import_pieces_framework2.Property.Dropdown({
    auth: clockodoAuth,
    description: "The ID of the team",
    displayName: "Team",
    required,
    refreshers: [],
    options: async ({ auth }) => (0, import_pieces_framework3.isNil)(auth) ? {
      disabled: !0,
      placeholder: "setup authentication first",
      options: []
    } : {
      disabled: !1,
      options: (await makeClient(auth.props).listTeams()).teams.map((team) => ({
        label: team.name,
        value: team.id
      }))
    }
  }),
  service_id: (required = !0, active = !0) => import_pieces_framework2.Property.Dropdown({
    auth: clockodoAuth,
    description: "The ID of the service",
    displayName: "Service",
    required,
    refreshers: [],
    options: async ({ auth }) => (0, import_pieces_framework3.isNil)(auth) ? {
      disabled: !0,
      placeholder: "setup authentication first",
      options: []
    } : {
      disabled: !1,
      options: (await makeClient(auth.props).listServices()).services.filter((s) => active === null || s.active === active).map((service) => ({
        label: service.name,
        value: service.id
      }))
    }
  }),
  language: (required = !0) => import_pieces_framework2.Property.StaticDropdown({
    displayName: "Language",
    required,
    options: {
      options: [
        { label: "German", value: "de" },
        { label: "English", value: "en" },
        { label: "French", value: "fr" }
      ]
    }
  }),
  color: (required = !0) => import_pieces_framework2.Property.StaticDropdown({
    displayName: "Color",
    required,
    options: {
      options: [
        { label: "Orange", value: 15634787 },
        { label: "Yellow", value: 15783768 },
        { label: "Green", value: 10347338 },
        { label: "Caribean", value: 3794634 },
        { label: "Lightblue", value: 5687033 },
        { label: "Blue", value: 3561463 },
        { label: "Purple", value: 8080359 },
        { label: "Magenta", value: 13657574 },
        { label: "Pink", value: 16544209 }
      ]
    }
  })
};
function emptyToNull(val) {
  return val === void 0 ? val : val || null;
}
function currentYear() {
  return (/* @__PURE__ */ new Date()).getFullYear();
}
function reformatDateTime(s) {
  if (s)
    return s.replace(/\.[0-9]{3}/, "");
}
function reformatDate(s) {
  if (s)
    return s.split("T", 2)[0];
}

// ../ap/packages/pieces/community/clockodo/src/lib/actions/entry/create-entry.ts
var create_entry_default = (0, import_pieces_framework4.createAction)({
  auth: clockodoAuth,
  name: "create_entry",
  displayName: "Create Entry",
  description: "Creates an entry in clockodo",
  audience: "both",
  aiMetadata: {
    description: "Logs a new time-record entry in clockodo for a customer and service over a start/end time window, optionally tagged to a project, user, hourly rate, and description. Use to record tracked work after the fact; requires customer_id, service_id, and both start and end times. Not idempotent: each call creates a separate entry, so guard against duplicates.",
    idempotent: !1
  },
  props: {
    customer_id: clockodoCommon.customer_id(),
    project_id: clockodoCommon.project_id(!1),
    service_id: clockodoCommon.service_id(),
    time_since: import_pieces_framework4.Property.DateTime({
      displayName: "Start Time",
      required: !0
    }),
    time_until: import_pieces_framework4.Property.DateTime({
      displayName: "End Time",
      required: !0
    }),
    text: import_pieces_framework4.Property.LongText({
      displayName: "Description",
      required: !1
    }),
    hourly_rate: import_pieces_framework4.Property.Number({
      displayName: "Hourly Rate",
      required: !1
    }),
    user_id: clockodoCommon.user_id(!1)
  },
  async run({ auth, propsValue }) {
    return (await makeClient(auth.props).createEntry({
      customers_id: propsValue.customer_id,
      projects_id: propsValue.project_id,
      services_id: propsValue.service_id,
      time_since: reformatDateTime(propsValue.time_since),
      time_until: reformatDateTime(propsValue.time_until),
      text: propsValue.text,
      hourly_rate: propsValue.hourly_rate,
      users_id: propsValue.user_id
    })).entry;
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/entry/get-entry.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var get_entry_default = (0, import_pieces_framework5.createAction)({
  auth: clockodoAuth,
  name: "get_entry",
  displayName: "Get Entry",
  description: "Retrieves a single entry from clockodo",
  audience: "both",
  aiMetadata: {
    description: "Fetches one clockodo time-record entry by its numeric entry_id and returns that entry. Use to read the current details of a known entry before updating or deleting it; to find entries by date range or customer instead, use Get Entries. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    entry_id: import_pieces_framework5.Property.Number({
      displayName: "Entry ID",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    return (await makeClient(auth.props).getEntry(propsValue.entry_id)).entry;
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/entry/update-entry.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var update_entry_default = (0, import_pieces_framework6.createAction)({
  auth: clockodoAuth,
  name: "update_entry",
  displayName: "Update Entry",
  description: "Updates an entry in clockodo",
  audience: "both",
  aiMetadata: {
    description: "Modifies an existing clockodo time-record entry identified by entry_id, changing any of its customer, project, service, user, time window, or description. Use to correct or amend an already-logged entry rather than creating a new one (use Create Entry for new records). Idempotent: applying the same field values to the same entry_id converges to the same state.",
    idempotent: !0
  },
  props: {
    entry_id: import_pieces_framework6.Property.Number({
      displayName: "Entry ID",
      required: !0
    }),
    customer_id: clockodoCommon.customer_id(!1),
    project_id: clockodoCommon.project_id(!1),
    service_id: clockodoCommon.service_id(!1),
    time_since: import_pieces_framework6.Property.DateTime({
      displayName: "Start Time",
      required: !1
    }),
    time_until: import_pieces_framework6.Property.DateTime({
      displayName: "End Time",
      required: !1
    }),
    text: import_pieces_framework6.Property.LongText({
      displayName: "Description",
      required: !1
    }),
    user_id: clockodoCommon.user_id(!1)
  },
  async run({ auth, propsValue }) {
    return (await makeClient(auth.props).updateEntry(propsValue.entry_id, {
      customers_id: propsValue.customer_id,
      projects_id: propsValue.project_id,
      services_id: propsValue.service_id,
      users_id: propsValue.user_id,
      text: propsValue.text,
      time_since: reformatDateTime(propsValue.time_since),
      time_until: reformatDateTime(propsValue.time_until)
    })).entry;
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/entry/list-entries.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
function calculateBillable(billable, billed) {
  if (!(billable === void 0 && billed === void 0))
    return billed ? 2 : billable ? 1 : 0;
}
var list_entries_default = (0, import_pieces_framework7.createAction)({
  auth: clockodoAuth,
  name: "list_entries",
  displayName: "Get Entries",
  description: "Fetches entries from clockodo",
  audience: "both",
  aiMetadata: {
    description: "Fetches clockodo time-record entries within a required start/end date range, optionally narrowed by user, customer, project, service, and billable/billed status. Use to find or report on entries by time period; supports two modes: leave page empty to retrieve all matching entries across pages, or set a page number to read just that single page. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    time_since: import_pieces_framework7.Property.DateTime({
      displayName: "Start Date",
      required: !0
    }),
    time_until: import_pieces_framework7.Property.DateTime({
      displayName: "End Date",
      required: !0
    }),
    user_id_filter: import_pieces_framework7.Property.Number({
      displayName: "Customer ID Filter",
      description: "Filter entries by their user",
      required: !1
    }),
    customer_id_filter: import_pieces_framework7.Property.Number({
      displayName: "Customer ID Filter",
      description: "Filter entries by their customer",
      required: !1
    }),
    project_id_filter: import_pieces_framework7.Property.Number({
      displayName: "Project ID Filter",
      description: "Filter entries by their project",
      required: !1
    }),
    service_id_filter: import_pieces_framework7.Property.Number({
      displayName: "Service ID Filter",
      description: "Filter entries by their service",
      required: !1
    }),
    billable_filter: import_pieces_framework7.Property.Checkbox({
      displayName: "Billable",
      description: "Only show entries that are billable",
      required: !1
    }),
    billed_filter: import_pieces_framework7.Property.Checkbox({
      displayName: "Billed",
      description: "Only show entries that are already billed",
      required: !1
    }),
    enhanced_list: import_pieces_framework7.Property.Checkbox({
      displayName: "Enhanced List",
      description: "Retrieves additional information about the entries",
      required: !1
    }),
    page: import_pieces_framework7.Property.Number({
      displayName: "Page",
      description: "Reads only the specified page",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let client = makeClient(auth.props), filter = {
      users_id: propsValue.user_id_filter,
      customers_id: propsValue.customer_id_filter,
      projects_id: propsValue.project_id_filter,
      services_id: propsValue.service_id_filter,
      billable: calculateBillable(
        propsValue.billable_filter,
        propsValue.billed_filter
      )
    }, time_since = reformatDateTime(propsValue.time_since), time_until = reformatDateTime(propsValue.time_until);
    if (propsValue.page !== void 0) {
      let res = await client.listEntries({
        time_since,
        time_until,
        enhanced_list: propsValue.enhanced_list,
        page: propsValue.page,
        filter
      });
      return {
        pagination: res.paging,
        entries: res.entries
      };
    } else
      return {
        entries: await client.listAllEntries(
          time_since,
          time_until,
          filter
        )
      };
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/entry/delete-entry.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var delete_entry_default = (0, import_pieces_framework8.createAction)({
  auth: clockodoAuth,
  name: "delete_entry",
  displayName: "Delete Entry",
  description: "Deletes an entry in clockodo",
  audience: "both",
  aiMetadata: {
    description: "Permanently deletes a clockodo time-record entry identified by its numeric entry_id. Use to remove a logged entry; locate the entry_id first via Get Entries if unknown. Idempotent: the entry reaches the same deleted end state on repeated calls.",
    idempotent: !0
  },
  props: {
    entry_id: import_pieces_framework8.Property.Number({
      displayName: "Entry ID",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    await makeClient(auth.props).deleteEntry(propsValue.entry_id);
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/entry/index.ts
var entry_default = [
  create_entry_default,
  get_entry_default,
  list_entries_default,
  update_entry_default,
  delete_entry_default
];

// ../ap/packages/pieces/community/clockodo/src/lib/actions/customer/create-customer.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var create_customer_default = (0, import_pieces_framework9.createAction)({
  auth: clockodoAuth,
  name: "create_customer",
  displayName: "Create Customer",
  description: "Creates a customer in clockodo",
  audience: "both",
  aiMetadata: { description: "Create a new clockodo customer with a name and optional number, active flag, billable default, note, and color. Not idempotent: each call adds another customer even with identical input, so guard against duplicates. To edit an existing customer use Update Customer.", idempotent: !1 },
  props: {
    name: import_pieces_framework9.Property.ShortText({
      displayName: "Name",
      required: !0
    }),
    number: import_pieces_framework9.Property.ShortText({
      displayName: "Number",
      required: !1
    }),
    active: import_pieces_framework9.Property.Checkbox({
      displayName: "Active",
      required: !1
    }),
    billable: import_pieces_framework9.Property.Checkbox({
      displayName: "Billable",
      required: !1
    }),
    note: import_pieces_framework9.Property.LongText({
      displayName: "Note",
      required: !1
    }),
    color: clockodoCommon.color(!1)
  },
  async run({ auth, propsValue }) {
    return (await makeClient(auth.props).createCustomer({
      name: propsValue.name,
      number: emptyToNull(propsValue.number),
      active: propsValue.active,
      billable_default: propsValue.billable,
      note: emptyToNull(propsValue.note),
      color: propsValue.color
    })).customer;
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/customer/get-customer.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), get_customer_default = (0, import_pieces_framework10.createAction)({
  auth: clockodoAuth,
  name: "get_customer",
  displayName: "Get Customer",
  description: "Retrieves a single customer from clockodo",
  audience: "both",
  aiMetadata: {
    description: "Looks up one clockodo customer by its numeric customer_id and returns that customer record. Use to confirm a customer exists or read its current details before creating an entry or updating it; requires a known customer_id. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    customer_id: clockodoCommon.customer_id(!0, void 0)
  },
  async run({ auth, propsValue }) {
    return (await makeClient(auth.props).getCustomer(propsValue.customer_id)).customer;
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/customer/update-customer.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var update_customer_default = (0, import_pieces_framework11.createAction)({
  auth: clockodoAuth,
  name: "update_customer",
  displayName: "Update Customer",
  description: "Updates a customer in clockodo",
  audience: "both",
  aiMetadata: { description: "Update an existing clockodo customer identified by their numeric customer ID, changing only the fields you supply (name, number, active flag, billable default, note, color). Idempotent: re-sending the same values leaves the customer unchanged. Requires a customer ID; use Create Customer to add a new one.", idempotent: !0 },
  props: {
    customer_id: clockodoCommon.customer_id(!0, void 0),
    name: import_pieces_framework11.Property.ShortText({
      displayName: "Name",
      required: !1
    }),
    number: import_pieces_framework11.Property.ShortText({
      displayName: "Number",
      required: !1
    }),
    active: import_pieces_framework11.Property.Checkbox({
      displayName: "Active",
      required: !1
    }),
    billable: import_pieces_framework11.Property.Checkbox({
      displayName: "Billable",
      required: !1
    }),
    note: import_pieces_framework11.Property.LongText({
      displayName: "Note",
      required: !1
    }),
    color: clockodoCommon.color(!1)
  },
  async run({ auth, propsValue }) {
    return (await makeClient(auth.props).updateCustomer(propsValue.customer_id, {
      name: propsValue.name,
      number: emptyToNull(propsValue.number),
      active: propsValue.active,
      billable_default: propsValue.billable,
      note: emptyToNull(propsValue.note),
      color: propsValue.color
    })).customer;
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/customer/list-customers.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var list_customers_default = (0, import_pieces_framework12.createAction)({
  auth: clockodoAuth,
  name: "list_customers",
  displayName: "Get Customers",
  description: "Fetches customers from clockodo",
  audience: "both",
  aiMetadata: { description: "List clockodo customers, optionally filtered by active status. Read-only and repeatable. Use to discover customers or resolve a customer ID by name before another call; supply a page number to read one page at a time, or omit it to retrieve all matching customers.", idempotent: !0 },
  props: {
    active_filter: import_pieces_framework12.Property.Checkbox({
      displayName: "Active Filter",
      description: "Filter customers by their active status",
      required: !1,
      defaultValue: !0
    }),
    page: import_pieces_framework12.Property.Number({
      displayName: "Page",
      description: "Reads only the specified page",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let client = makeClient(auth.props), filter = {
      active: propsValue.active_filter
    };
    if (propsValue.page !== void 0) {
      let res = await client.listCustomers({
        page: propsValue.page,
        filter
      });
      return {
        pagination: res.paging,
        customers: res.customers
      };
    } else
      return {
        customers: await client.listAllCustomers(filter)
      };
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/customer/delete-customer.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var delete_customer_default = (0, import_pieces_framework13.createAction)({
  auth: clockodoAuth,
  name: "delete_customer",
  displayName: "Delete Customer",
  description: "Deletes a customer in clockodo",
  audience: "both",
  aiMetadata: {
    description: "Permanently deletes a clockodo customer identified by its numeric customer_id. Use when removing a client record entirely; deletion may fail if the customer still has linked projects or time entries. Idempotent: the customer reaches the same deleted end state on repeated calls (later calls fail because it no longer exists).",
    idempotent: !0
  },
  props: {
    customer_id: clockodoCommon.customer_id(!0, !1)
  },
  async run({ auth, propsValue }) {
    await makeClient(auth.props).deleteCustomer(propsValue.customer_id);
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/customer/index.ts
var customer_default = [
  create_customer_default,
  get_customer_default,
  update_customer_default,
  list_customers_default,
  delete_customer_default
];

// ../ap/packages/pieces/community/clockodo/src/lib/actions/project/create-project.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var create_project_default = (0, import_pieces_framework14.createAction)({
  auth: clockodoAuth,
  name: "create_project",
  displayName: "Create Project",
  description: "Creates a project in clockodo",
  audience: "both",
  aiMetadata: { description: "Create a new clockodo project under a customer, with optional number, budget, billing defaults, and note. Not idempotent: each call adds another project even with identical input, so guard against duplicates. To change an existing project use Update Project.", idempotent: !1 },
  props: {
    name: import_pieces_framework14.Property.ShortText({
      displayName: "Name",
      required: !0
    }),
    customer_id: clockodoCommon.customer_id(),
    number: import_pieces_framework14.Property.ShortText({
      displayName: "Number",
      required: !1
    }),
    active: import_pieces_framework14.Property.Checkbox({
      displayName: "Active",
      required: !1
    }),
    billable: import_pieces_framework14.Property.Checkbox({
      displayName: "Billable",
      required: !1
    }),
    budget: import_pieces_framework14.Property.Number({
      displayName: "Budget",
      required: !1
    }),
    budget_is_hours: import_pieces_framework14.Property.Checkbox({
      displayName: "Budget in hours?",
      required: !1
    }),
    budget_is_not_strict: import_pieces_framework14.Property.Checkbox({
      displayName: "Soft Budget",
      required: !1
    }),
    note: import_pieces_framework14.Property.LongText({
      displayName: "Note",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    return (await makeClient(auth.props).createProject({
      name: propsValue.name,
      customers_id: propsValue.customer_id,
      number: emptyToNull(propsValue.number),
      active: propsValue.active,
      billable_default: propsValue.billable,
      note: emptyToNull(propsValue.note),
      budget_money: propsValue.budget,
      budget_is_hours: propsValue.budget_is_hours,
      budget_is_not_strict: propsValue.budget_is_not_strict
    })).project;
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/project/get-project.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework"), get_project_default = (0, import_pieces_framework15.createAction)({
  auth: clockodoAuth,
  name: "get_project",
  displayName: "Get Project",
  description: "Retrieves a single project from clockodo",
  audience: "both",
  aiMetadata: { description: "Fetch one clockodo project by its numeric project ID. Read-only and repeatable. Use when you already have the project ID and need its current fields (name, customer, budget, active/completed state); to find a project by name or list many, use Get Projects instead.", idempotent: !0 },
  props: {
    project_id: clockodoCommon.project_id(!0, !1, null)
  },
  async run({ auth, propsValue }) {
    return (await makeClient(auth.props).getProject(propsValue.project_id)).project;
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/project/update-project.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework");
var update_project_default = (0, import_pieces_framework16.createAction)({
  auth: clockodoAuth,
  name: "update_project",
  displayName: "Update Project",
  description: "Updates a project in clockodo",
  audience: "both",
  aiMetadata: { description: "Update an existing clockodo project identified by its numeric project ID, changing only the fields you supply (name, customer, budget, billing, completed/billed status, note). Idempotent: re-sending the same values leaves the project unchanged. Requires a project ID; use Create Project to add a new one.", idempotent: !0 },
  props: {
    project_id: clockodoCommon.project_id(!0, !1, null),
    customer_id: clockodoCommon.customer_id(!1),
    name: import_pieces_framework16.Property.ShortText({
      displayName: "Name",
      required: !1
    }),
    number: import_pieces_framework16.Property.ShortText({
      displayName: "Number",
      required: !1
    }),
    active: import_pieces_framework16.Property.Checkbox({
      displayName: "Active",
      required: !1
    }),
    billable: import_pieces_framework16.Property.Checkbox({
      displayName: "Billable",
      required: !1
    }),
    budget: import_pieces_framework16.Property.Number({
      displayName: "Budget",
      required: !1
    }),
    budget_is_hours: import_pieces_framework16.Property.Checkbox({
      displayName: "Budget in hours?",
      required: !1
    }),
    budget_is_not_strict: import_pieces_framework16.Property.Checkbox({
      displayName: "Soft Budget",
      required: !1
    }),
    note: import_pieces_framework16.Property.LongText({
      displayName: "Note",
      required: !1
    }),
    completed: import_pieces_framework16.Property.Checkbox({
      displayName: "Completed",
      required: !1
    }),
    billed_amount: import_pieces_framework16.Property.Number({
      displayName: "Billed Amount",
      required: !1
    }),
    billing_complete: import_pieces_framework16.Property.Checkbox({
      displayName: "Billing Complete",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    return (await makeClient(auth.props).updateProject(propsValue.project_id, {
      name: propsValue.name,
      customers_id: propsValue.customer_id,
      number: emptyToNull(propsValue.number),
      active: propsValue.active,
      billable_default: propsValue.billable,
      note: emptyToNull(propsValue.note),
      budget_money: propsValue.budget,
      budget_is_hours: propsValue.budget_is_hours,
      budget_is_not_strict: propsValue.budget_is_not_strict,
      completed: propsValue.completed,
      billed_money: propsValue.billed_amount,
      billed_completely: propsValue.billing_complete
    })).project;
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/project/list-projects.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework");
var list_projects_default = (0, import_pieces_framework17.createAction)({
  auth: clockodoAuth,
  name: "list_projects",
  displayName: "Get Projects",
  description: "Fetches projects from clockodo",
  audience: "both",
  aiMetadata: { description: "List clockodo projects, optionally filtered by customer ID and active status. Read-only and repeatable. Use to discover projects or resolve a project ID by name before another call; supply a page number to read one page at a time, or omit it to retrieve all matching projects.", idempotent: !0 },
  props: {
    customer_id_filter: import_pieces_framework17.Property.Number({
      displayName: "Customer ID Filter",
      description: "Filter projects by their customer",
      required: !1
    }),
    active_filter: import_pieces_framework17.Property.Checkbox({
      displayName: "Active Filter",
      description: "Filter customers by their active status",
      required: !1,
      defaultValue: !0
    }),
    page: import_pieces_framework17.Property.Number({
      displayName: "Page",
      description: "Reads only the specified page",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let client = makeClient(auth.props), filter = {
      customers_id: propsValue.customer_id_filter,
      active: propsValue.active_filter
    };
    if (propsValue.page !== void 0) {
      let res = await client.listProjects({
        page: propsValue.page,
        filter
      });
      return {
        pagination: res.paging,
        projects: res.projects
      };
    } else
      return {
        projects: await client.listAllProjects(filter)
      };
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/project/delete-project.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework"), delete_project_default = (0, import_pieces_framework18.createAction)({
  auth: clockodoAuth,
  name: "delete_project",
  displayName: "Delete Project",
  description: "Deletes a project in clockodo",
  audience: "both",
  aiMetadata: { description: "Permanently delete a clockodo project by its numeric project ID. Destructive and not safely repeatable: a second call for the same ID fails because the project no longer exists. Confirm the correct ID before calling.", idempotent: !1 },
  props: {
    project_id: clockodoCommon.project_id(!0, !1, !1)
  },
  async run({ auth, propsValue }) {
    await makeClient(auth.props).deleteProject(propsValue.project_id);
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/project/index.ts
var project_default = [
  create_project_default,
  get_project_default,
  list_projects_default,
  update_project_default,
  delete_project_default
];

// ../ap/packages/pieces/community/clockodo/src/lib/actions/service/create-service.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework");
var create_service_default = (0, import_pieces_framework19.createAction)({
  auth: clockodoAuth,
  name: "create_service",
  displayName: "Create Service",
  description: "Creates a service in clockodo",
  audience: "both",
  aiMetadata: { description: "Create a new clockodo service (a billable activity type) with a name and optional number, active flag, and note. Not idempotent: each call adds another service even with identical input, so guard against duplicates. To edit an existing service use Update Service.", idempotent: !1 },
  props: {
    name: import_pieces_framework19.Property.ShortText({
      displayName: "Name",
      required: !0
    }),
    number: import_pieces_framework19.Property.ShortText({
      displayName: "Number",
      required: !1
    }),
    active: import_pieces_framework19.Property.Checkbox({
      displayName: "Active",
      required: !1
    }),
    note: import_pieces_framework19.Property.LongText({
      displayName: "Note",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    return (await makeClient(auth.props).createService({
      name: propsValue.name,
      number: emptyToNull(propsValue.number),
      active: propsValue.active,
      note: emptyToNull(propsValue.note)
    })).service;
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/service/get-service.ts
var import_pieces_framework20 = require("@activepieces/pieces-framework"), get_service_default = (0, import_pieces_framework20.createAction)({
  auth: clockodoAuth,
  name: "get_service",
  displayName: "Get Service",
  description: "Retrieves a single service from clockodo",
  audience: "both",
  aiMetadata: { description: "Fetch one clockodo service (a billable activity type) by its numeric service ID. Read-only and repeatable. Use when you already hold the service ID; to browse or resolve a service by name use Get Services instead.", idempotent: !0 },
  props: {
    service_id: clockodoCommon.service_id(!0, null)
  },
  async run({ auth, propsValue }) {
    return (await makeClient(auth.props).getService(propsValue.service_id)).service;
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/service/update-service.ts
var import_pieces_framework21 = require("@activepieces/pieces-framework");
var update_service_default = (0, import_pieces_framework21.createAction)({
  auth: clockodoAuth,
  name: "update_service",
  displayName: "Update Service",
  description: "Updates a service in clockodo",
  audience: "both",
  aiMetadata: { description: "Update an existing clockodo service identified by its numeric service ID, changing only the fields you supply (name, number, active flag, note). Idempotent: re-sending the same values leaves the service unchanged. Requires a service ID; use Create Service to add a new one.", idempotent: !0 },
  props: {
    service_id: clockodoCommon.service_id(!0, null),
    name: import_pieces_framework21.Property.ShortText({
      displayName: "Name",
      required: !1
    }),
    number: import_pieces_framework21.Property.ShortText({
      displayName: "Number",
      required: !1
    }),
    active: import_pieces_framework21.Property.Checkbox({
      displayName: "Active",
      required: !1
    }),
    note: import_pieces_framework21.Property.LongText({
      displayName: "Note",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    return (await makeClient(auth.props).updateService(propsValue.service_id, {
      name: propsValue.name,
      number: emptyToNull(propsValue.number),
      active: propsValue.active,
      note: emptyToNull(propsValue.note)
    })).service;
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/service/list-services.ts
var import_pieces_framework22 = require("@activepieces/pieces-framework"), list_services_default = (0, import_pieces_framework22.createAction)({
  auth: clockodoAuth,
  name: "list_services",
  displayName: "Get Services",
  description: "Fetches services from clockodo",
  audience: "both",
  aiMetadata: { description: "List all clockodo services (billable activity types). Read-only, repeatable, and takes no filters. Use to discover available services or resolve a service ID by name before another call.", idempotent: !0 },
  props: {},
  async run({ auth }) {
    return {
      services: (await makeClient(auth.props).listServices()).services
    };
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/service/delete-service.ts
var import_pieces_framework23 = require("@activepieces/pieces-framework"), delete_service_default = (0, import_pieces_framework23.createAction)({
  auth: clockodoAuth,
  name: "delete_service",
  displayName: "Delete Service",
  description: "Deletes a service in clockodo",
  audience: "both",
  aiMetadata: { description: "Permanently delete a clockodo service by its numeric service ID. Destructive and not safely repeatable: a second call for the same ID fails because the service is already gone. Confirm the correct ID before calling.", idempotent: !1 },
  props: {
    service_id: clockodoCommon.service_id(!0, !1)
  },
  async run({ auth, propsValue }) {
    await makeClient(auth.props).deleteService(propsValue.service_id);
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/service/index.ts
var service_default = [
  create_service_default,
  get_service_default,
  update_service_default,
  list_services_default,
  delete_service_default
];

// ../ap/packages/pieces/community/clockodo/src/lib/actions/team/get-team.ts
var import_pieces_framework24 = require("@activepieces/pieces-framework"), get_team_default = (0, import_pieces_framework24.createAction)({
  auth: clockodoAuth,
  name: "get_team",
  displayName: "Get Team",
  description: "Retrieves a single team from clockodo",
  audience: "both",
  aiMetadata: { description: "Fetch one clockodo team by its numeric team ID. Read-only and repeatable. Use when you already have the team ID; to browse teams or resolve a team ID by name use Get Teams instead.", idempotent: !0 },
  props: {
    team_id: clockodoCommon.team_id()
  },
  async run({ auth, propsValue }) {
    return (await makeClient(auth.props).getTeam(propsValue.team_id)).team;
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/team/list-teams.ts
var import_pieces_framework25 = require("@activepieces/pieces-framework"), list_teams_default = (0, import_pieces_framework25.createAction)({
  auth: clockodoAuth,
  name: "list_teams",
  displayName: "Get Teams",
  description: "Fetches teams from clockodo",
  audience: "both",
  aiMetadata: { description: "List all clockodo teams. Read-only, repeatable, and takes no filters. Use to discover teams or resolve a team ID by name before assigning a user or another call.", idempotent: !0 },
  props: {},
  async run({ auth }) {
    return {
      teams: (await makeClient(auth.props).listTeams()).teams
    };
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/team/index.ts
var team_default = [get_team_default, list_teams_default];

// ../ap/packages/pieces/community/clockodo/src/lib/actions/user/get-user.ts
var import_pieces_framework26 = require("@activepieces/pieces-framework"), get_user_default = (0, import_pieces_framework26.createAction)({
  auth: clockodoAuth,
  name: "get_user",
  displayName: "Get User",
  description: "Retrieves a single user from clockodo",
  audience: "both",
  aiMetadata: { description: "Fetch one clockodo user by their numeric user ID. Read-only and repeatable. Use when you already hold the user ID; to find a user by name or email or list many use Get Users instead.", idempotent: !0 },
  props: {
    user_id: clockodoCommon.user_id(!0, null)
  },
  async run({ auth, propsValue }) {
    return (await makeClient(auth.props).getUser(propsValue.user_id)).user;
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/user/list-users.ts
var import_pieces_framework27 = require("@activepieces/pieces-framework"), list_users_default = (0, import_pieces_framework27.createAction)({
  auth: clockodoAuth,
  name: "list_users",
  displayName: "Get Users",
  description: "Fetches users from clockodo",
  audience: "both",
  aiMetadata: { description: "List all clockodo users. Read-only, repeatable, and takes no filters. Use to discover users or resolve a user ID by name or email before another call.", idempotent: !0 },
  props: {},
  async run({ auth }) {
    return {
      users: (await makeClient(auth.props).listUsers()).users
    };
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/user/create-user.ts
var import_pieces_framework28 = require("@activepieces/pieces-framework");
var create_user_default = (0, import_pieces_framework28.createAction)({
  auth: clockodoAuth,
  name: "create_user",
  displayName: "Create User",
  description: "Creates a user in clockodo",
  audience: "both",
  aiMetadata: { description: "Create a new clockodo user with required name, email, and role, plus optional team, language, wage type, and permissions. Not idempotent: each call adds another user even with the same email, so guard against duplicates. To change an existing user use Update User.", idempotent: !1 },
  props: {
    name: import_pieces_framework28.Property.ShortText({
      displayName: "Name",
      required: !0
    }),
    email: import_pieces_framework28.Property.ShortText({
      displayName: "E-Mail",
      required: !0
    }),
    role: import_pieces_framework28.Property.ShortText({
      displayName: "Role",
      required: !0
    }),
    number: import_pieces_framework28.Property.ShortText({
      displayName: "Number",
      required: !1
    }),
    team_id: clockodoCommon.team_id(!1),
    language: clockodoCommon.language(!1),
    wage_type: import_pieces_framework28.Property.StaticDropdown({
      displayName: "Wage Type",
      required: !1,
      options: {
        options: [
          { label: "Salary", value: 1 },
          { label: "Hourly wage", value: 2 }
        ]
      }
    }),
    can_generally_see_absences: import_pieces_framework28.Property.Checkbox({
      displayName: "Can see absences",
      required: !1
    }),
    can_generally_manage_absences: import_pieces_framework28.Property.Checkbox({
      displayName: "Can manage absences",
      required: !1
    }),
    can_add_customers: import_pieces_framework28.Property.Checkbox({
      displayName: "Can add customers",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    return (await makeClient(auth.props).createUser({
      name: propsValue.name,
      email: propsValue.email,
      role: propsValue.role,
      number: emptyToNull(propsValue.number),
      teams_id: propsValue.team_id,
      language: propsValue.language,
      wage_type: propsValue.wage_type,
      can_generally_see_absences: propsValue.can_generally_see_absences,
      can_generally_manage_absences: propsValue.can_generally_manage_absences,
      can_add_customers: propsValue.can_add_customers
    })).user;
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/user/update-user.ts
var import_pieces_framework29 = require("@activepieces/pieces-framework");
var update_user_default = (0, import_pieces_framework29.createAction)({
  auth: clockodoAuth,
  name: "update_user",
  displayName: "Update User",
  description: "Updates a user in clockodo",
  audience: "both",
  aiMetadata: { description: "Update an existing clockodo user identified by their numeric user ID, changing only the fields you supply (name, email, role, team, language, wage type, absence/customer permissions, active flag). Idempotent: re-sending the same values leaves the user unchanged. Requires a user ID; use Create User to add a new one.", idempotent: !0 },
  props: {
    user_id: clockodoCommon.user_id(!0, null),
    name: import_pieces_framework29.Property.ShortText({
      displayName: "Name",
      required: !1
    }),
    email: import_pieces_framework29.Property.ShortText({
      displayName: "E-Mail",
      required: !1
    }),
    role: import_pieces_framework29.Property.ShortText({
      displayName: "Role",
      required: !1
    }),
    number: import_pieces_framework29.Property.ShortText({
      displayName: "Number",
      required: !1
    }),
    active: import_pieces_framework29.Property.Checkbox({
      displayName: "Active",
      required: !1
    }),
    team_id: clockodoCommon.team_id(!1),
    language: clockodoCommon.language(!1),
    wage_type: import_pieces_framework29.Property.StaticDropdown({
      displayName: "Wage Type",
      required: !1,
      options: {
        options: [
          { label: "Salary", value: 1 },
          { label: "Hourly wage", value: 2 }
        ]
      }
    }),
    can_generally_see_absences: import_pieces_framework29.Property.Checkbox({
      displayName: "Can see absences",
      required: !1
    }),
    can_generally_manage_absences: import_pieces_framework29.Property.Checkbox({
      displayName: "Can manage absences",
      required: !1
    }),
    can_add_customers: import_pieces_framework29.Property.Checkbox({
      displayName: "Can add customers",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    return (await makeClient(auth.props).updateUser(propsValue.user_id, {
      name: propsValue.name,
      email: propsValue.email,
      role: propsValue.role,
      number: emptyToNull(propsValue.number),
      active: propsValue.active,
      teams_id: propsValue.team_id,
      language: propsValue.language,
      wage_type: propsValue.wage_type,
      can_generally_see_absences: propsValue.can_generally_see_absences,
      can_generally_manage_absences: propsValue.can_generally_manage_absences,
      can_add_customers: propsValue.can_add_customers
    })).user;
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/user/delete-user.ts
var import_pieces_framework30 = require("@activepieces/pieces-framework"), delete_user_default = (0, import_pieces_framework30.createAction)({
  auth: clockodoAuth,
  name: "delete_user",
  displayName: "Delete User",
  description: "Deletes a user in clockodo",
  audience: "both",
  aiMetadata: { description: "Permanently delete a clockodo user by their numeric user ID. Destructive and not safely repeatable: a second call for the same ID fails because the user no longer exists. Confirm the correct ID before calling.", idempotent: !1 },
  props: {
    user_id: clockodoCommon.user_id(!0, !1)
  },
  async run({ auth, propsValue }) {
    await makeClient(auth.props).deleteUser(propsValue.user_id);
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/user/index.ts
var user_default = [
  get_user_default,
  list_users_default,
  create_user_default,
  update_user_default,
  delete_user_default
];

// ../ap/packages/pieces/community/clockodo/src/lib/actions/absence/create-absence.ts
var import_pieces_framework31 = require("@activepieces/pieces-framework");
var create_absence_default = (0, import_pieces_framework31.createAction)({
  auth: clockodoAuth,
  name: "create_absence",
  displayName: "Create Absence",
  description: "Creates a absence in clockodo",
  audience: "both",
  aiMetadata: {
    description: "Creates a new clockodo absence (e.g. vacation, sick leave) for a user over a start/end date range, with an absence type and optional approved/half-day/sick-note flags. Use to request or record time off; requires a date range and absence type, and defaults to the requested status unless approved is set. Not idempotent: each call creates a separate absence record.",
    idempotent: !1
  },
  props: {
    date_since: import_pieces_framework31.Property.DateTime({
      displayName: "Start Date",
      required: !0
    }),
    date_until: import_pieces_framework31.Property.DateTime({
      displayName: "End Date",
      required: !0
    }),
    type: clockodoCommon.absenceType(!0),
    user_id: clockodoCommon.user_id(!1),
    half_days: import_pieces_framework31.Property.Checkbox({
      displayName: "Half Days",
      required: !1
    }),
    approved: import_pieces_framework31.Property.Checkbox({
      displayName: "Approved",
      required: !1
    }),
    note: import_pieces_framework31.Property.LongText({
      displayName: "Note",
      required: !1
    }),
    sick_note: import_pieces_framework31.Property.Checkbox({
      displayName: "Sick Note",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    return (await makeClient(auth.props).createAbsence({
      date_since: reformatDate(propsValue.date_since),
      date_until: reformatDate(propsValue.date_until),
      type: propsValue.type,
      users_id: propsValue.user_id,
      count_days: propsValue.half_days ? 0.5 : 1,
      status: propsValue.approved ? 1 /* APPROVED */ : 0 /* REQUESTED */,
      note: emptyToNull(propsValue.note),
      sick_note: propsValue.sick_note
    })).absence;
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/absence/get-absence.ts
var import_pieces_framework32 = require("@activepieces/pieces-framework");
var get_absence_default = (0, import_pieces_framework32.createAction)({
  auth: clockodoAuth,
  name: "get_absence",
  displayName: "Get Absence",
  description: "Retrieves a single absence from clockodo",
  audience: "both",
  aiMetadata: {
    description: "Fetches one clockodo absence record by its numeric absence_id and returns that absence. Use to read the current details or status of a known absence before updating or deleting it; to find absences for a year instead, use Get Absences. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    absence_id: import_pieces_framework32.Property.Number({
      displayName: "Absence ID",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    return (await makeClient(auth.props).getAbsence(propsValue.absence_id)).absence;
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/absence/update-absence.ts
var import_pieces_framework33 = require("@activepieces/pieces-framework");
var update_absence_default = (0, import_pieces_framework33.createAction)({
  auth: clockodoAuth,
  name: "update_absence",
  displayName: "Update Absence",
  description: "Updates an absence in clockodo",
  audience: "both",
  aiMetadata: {
    description: "Modifies an existing clockodo absence identified by absence_id, changing its date range, type, status (e.g. approve or decline a request), half-day flag, note, or sick-note flag. Use to amend or approve an absence rather than creating a new one (use Create Absence for new records). Idempotent: applying the same field values to the same absence_id converges to the same state.",
    idempotent: !0
  },
  props: {
    absence_id: import_pieces_framework33.Property.Number({
      displayName: "Absence ID",
      required: !0
    }),
    date_since: import_pieces_framework33.Property.DateTime({
      displayName: "Start Date",
      required: !1
    }),
    date_until: import_pieces_framework33.Property.DateTime({
      displayName: "End Date",
      required: !1
    }),
    type: clockodoCommon.absenceType(!1),
    status: import_pieces_framework33.Property.StaticDropdown({
      displayName: "Status",
      required: !1,
      options: {
        options: [
          { value: 0, label: "Requested" },
          { value: 1, label: "Approved" },
          { value: 2, label: "Declined" },
          { value: 3, label: "Approval cancelled" },
          { value: 4, label: "Request cancelled" }
        ]
      }
    }),
    half_days: import_pieces_framework33.Property.Checkbox({
      displayName: "Half Days",
      required: !1
    }),
    note: import_pieces_framework33.Property.LongText({
      displayName: "Note",
      required: !1
    }),
    sick_note: import_pieces_framework33.Property.Checkbox({
      displayName: "Sick Note",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    return (await makeClient(auth.props).updateAbsence(propsValue.absence_id, {
      date_since: reformatDate(propsValue.date_since),
      date_until: reformatDate(propsValue.date_until),
      type: propsValue.type,
      status: propsValue.status,
      count_days: propsValue.half_days ? 0.5 : 1,
      note: emptyToNull(propsValue.note),
      sick_note: propsValue.sick_note
    })).absence;
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/absence/list-absence.ts
var import_pieces_framework34 = require("@activepieces/pieces-framework");
var list_absence_default = (0, import_pieces_framework34.createAction)({
  auth: clockodoAuth,
  name: "list_absences",
  displayName: "Get Absences",
  description: "Fetches absences from clockodo",
  audience: "both",
  aiMetadata: {
    description: "Fetches clockodo absence records for a required calendar year, optionally filtered to a single user. Use to list or report on time off within a year; to read one absence by id instead, use Get Absence. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    year: import_pieces_framework34.Property.Number({
      displayName: "Year",
      required: !0
    }),
    user_id: clockodoCommon.user_id(!1)
  },
  async run({ auth, propsValue }) {
    return {
      absences: (await makeClient(auth.props).listAbsences({
        year: propsValue.year,
        users_id: propsValue.user_id
      })).absences
    };
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/absence/delete-absence.ts
var import_pieces_framework35 = require("@activepieces/pieces-framework");
var delete_absence_default = (0, import_pieces_framework35.createAction)({
  auth: clockodoAuth,
  name: "delete_absence",
  displayName: "Delete Absence",
  description: "Deletes an absence in clockodo",
  audience: "both",
  aiMetadata: {
    description: "Permanently deletes a clockodo absence identified by its numeric absence_id. Use to remove an absence record (e.g. a cancelled leave request); locate the absence_id first via Get Absences if unknown. Idempotent: the absence reaches the same deleted end state on repeated calls.",
    idempotent: !0
  },
  props: {
    absence_id: import_pieces_framework35.Property.Number({
      displayName: "Absence ID",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    await makeClient(auth.props).deleteAbsence(propsValue.absence_id);
  }
});

// ../ap/packages/pieces/community/clockodo/src/lib/actions/absence/index.ts
var absence_default = [
  create_absence_default,
  get_absence_default,
  update_absence_default,
  list_absence_default,
  delete_absence_default
];

// ../ap/packages/pieces/community/clockodo/src/lib/actions/index.ts
var import_pieces_common2 = require("@activepieces/pieces-common");
var actions_default = [
  ...entry_default,
  ...customer_default,
  ...project_default,
  ...service_default,
  ...team_default,
  ...user_default,
  ...absence_default,
  (0, import_pieces_common2.createCustomApiCallAction)({
    baseUrl: () => "https://my.clockodo.com/api",
    // Replace with the actual base URL
    auth: clockodoAuth,
    authMapping: async (auth) => {
      var _a, _b, _c;
      return {
        "X-ClockodoApiUser": ((_a = auth == null ? void 0 : auth.props) == null ? void 0 : _a.email) || "",
        "X-ClockodoApiKey": ((_b = auth == null ? void 0 : auth.props) == null ? void 0 : _b.token) || "",
        "X-Clockodo-External-Application": ((_c = auth == null ? void 0 : auth.props) == null ? void 0 : _c.company_name) || "",
        "Accept-Language": "en"
      };
    }
  })
];

// ../ap/packages/pieces/community/clockodo/src/lib/triggers/new-entry.ts
var import_pieces_framework36 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var polling = {
  strategy: import_pieces_common3.DedupeStrategy.LAST_ITEM,
  items: async ({ auth }) => {
    let { email, token, company_name, company_email } = auth.props, client = new ClockodoClient(
      email,
      token,
      company_name,
      company_email
    ), time_since = currentYear() - 1 + "-01-01T00:00:00Z", time_until = currentYear() + 1 + "-12-31T23:59:59Z", res = await client.listEntries({ time_since, time_until });
    return res.paging.count_pages > 1 && (res = await client.listEntries({
      time_since,
      time_until,
      page: res.paging.count_pages
    })), res.entries.sort((a, b) => b.id - a.id).map((a) => ({
      id: a.id,
      data: a
    }));
  }
}, new_entry_default = (0, import_pieces_framework36.createTrigger)({
  auth: clockodoAuth,
  name: "new_entry",
  displayName: "New Entry",
  description: "Triggers when a new time entry is created",
  aiMetadata: {
    description: "Fires when a new time entry is created in Clockodo, such as logged work time, a lump-sum service, or a recorded expense. Emits the newly created entry record, including details like the associated customer, project, service, duration, and billing information."
  },
  type: import_pieces_framework36.TriggerStrategy.POLLING,
  props: {},
  sampleData: {},
  onEnable: async (context) => {
    await import_pieces_common3.pollingHelper.onEnable(polling, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  onDisable: async (context) => {
    await import_pieces_common3.pollingHelper.onDisable(polling, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  run: async (context) => await import_pieces_common3.pollingHelper.poll(polling, {
    auth: context.auth,
    store: context.store,
    propsValue: context.propsValue,
    files: context.files
  }),
  test: async (context) => await import_pieces_common3.pollingHelper.test(polling, {
    auth: context.auth,
    store: context.store,
    propsValue: context.propsValue,
    files: context.files
  })
});

// ../ap/packages/pieces/community/clockodo/src/lib/triggers/new-absence-enquiry.ts
var import_pieces_framework37 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var polling2 = {
  strategy: import_pieces_common4.DedupeStrategy.LAST_ITEM,
  items: async ({ auth }) => (await new ClockodoClient(
    auth.props.email,
    auth.props.token,
    auth.props.company_name,
    auth.props.company_email
  ).listAbsences({ year: currentYear() })).absences.sort((a, b) => b.id - a.id).map((a) => ({
    id: a.id,
    data: a
  }))
}, new_absence_enquiry_default = (0, import_pieces_framework37.createTrigger)({
  auth: clockodoAuth,
  name: "new_absence_enquiry",
  displayName: "New Absence Enquiry",
  description: "Triggers when a new absence enquiry is created",
  aiMetadata: {
    description: "Fires when a new absence enquiry (such as a vacation, sick leave, or other time-off request) is created in Clockodo for the current year. Emits the newly created absence record, including its details such as the absence type, date range, and requesting employee."
  },
  type: import_pieces_framework37.TriggerStrategy.POLLING,
  props: {},
  sampleData: {},
  onEnable: async (context) => {
    await import_pieces_common4.pollingHelper.onEnable(polling2, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  onDisable: async (context) => {
    await import_pieces_common4.pollingHelper.onDisable(polling2, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  run: async (context) => await import_pieces_common4.pollingHelper.poll(polling2, {
    auth: context.auth,
    store: context.store,
    propsValue: context.propsValue,
    files: context.files
  }),
  test: async (context) => await import_pieces_common4.pollingHelper.test(polling2, {
    auth: context.auth,
    store: context.store,
    propsValue: context.propsValue,
    files: context.files
  })
});

// ../ap/packages/pieces/community/clockodo/src/lib/triggers/index.ts
var triggers_default = [new_entry_default, new_absence_enquiry_default];

// ../ap/packages/pieces/community/clockodo/src/index.ts
var clockodo = (0, import_pieces_framework38.createPiece)({
  displayName: "Clockodo",
  description: "Time tracking made easy",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/clockodo.png",
  categories: [import_pieces_framework39.PieceCategory.PRODUCTIVITY],
  authors: ["JanHolger", "kishanprmr", "MoShizzle", "khaledmashaly", "abuaboud"],
  auth: clockodoAuth,
  actions: actions_default,
  triggers: triggers_default
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  clockodo
});
