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

// ../ap/packages/pieces/community/beebole/src/index.ts
var index_exports = {};
__export(index_exports, {
  beebole: () => beebole
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/beebole/src/lib/actions/create-person.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/beebole/src/lib/common/auth.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework"), beeboleAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Token",
  description: `To get your Beebole API token:

1. Log in to your Beebole account at https://beebole-apps.com
2. Go to **Customize \u2192 API Token**
3. Generate a new API token 
4. Copy the token and paste it below

Need help? See https://beebole.com/help/api`,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.POST,
        url: "https://beebole-apps.com/api/v2",
        authentication: {
          type: import_pieces_common.AuthenticationType.BASIC,
          username: auth,
          password: "x"
        },
        body: { service: "company.list" }
      }), { valid: !0 };
    } catch (e) {
      return { valid: !1, error: "Invalid Beebole API token." };
    }
  }
});

// ../ap/packages/pieces/community/beebole/src/lib/common/client.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), BASE_URL = "https://beebole-apps.com/api/v2";
async function callBeebole({
  token,
  body
}) {
  return await import_pieces_common2.httpClient.sendRequest({
    method: import_pieces_common2.HttpMethod.POST,
    url: BASE_URL,
    authentication: {
      type: import_pieces_common2.AuthenticationType.BASIC,
      username: token,
      password: "x"
    },
    body
  });
}
var beeboleClient = {
  call: callBeebole
};

// ../ap/packages/pieces/community/beebole/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
async function listCompanies(token) {
  var _a;
  return (_a = (await beeboleClient.call({
    token,
    body: { service: "company.list" }
  })).body.companies) != null ? _a : [];
}
async function listProjects(token, companyId) {
  var _a;
  return (_a = (await beeboleClient.call({
    token,
    body: { service: "project.list", company: { id: companyId } }
  })).body.projects) != null ? _a : [];
}
async function listSubprojects(token, projectId) {
  var _a;
  return (_a = (await beeboleClient.call({
    token,
    body: { service: "subproject.list", project: { id: projectId } }
  })).body.subprojects) != null ? _a : [];
}
async function listPeople(token, companyId) {
  var _a, _b;
  let response = await beeboleClient.call({
    token,
    body: { service: "person.list", company: { id: companyId } }
  });
  return (_b = (_a = response.body.people) != null ? _a : response.body.persons) != null ? _b : [];
}
async function listAbsences(token) {
  var _a;
  return (_a = (await beeboleClient.call({
    token,
    body: { service: "absence.list" }
  })).body.absences) != null ? _a : [];
}
var beeboleProps = {
  companyDropdown: (params) => {
    var _a;
    return import_pieces_framework2.Property.Dropdown({
      auth: beeboleAuth,
      displayName: "Company",
      description: (_a = params.description) != null ? _a : "Select the company (customer).",
      required: params.required,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return { disabled: !0, options: [], placeholder: "Please connect your Beebole account first." };
        try {
          return {
            disabled: !1,
            options: (await listCompanies(auth.secret_text)).filter((c) => c.active !== !1).map((c) => ({ label: c.name, value: c.id }))
          };
        } catch (e) {
          return { disabled: !0, options: [], placeholder: "Failed to load companies. Check your API token." };
        }
      }
    });
  },
  projectDropdown: (params) => {
    var _a;
    return import_pieces_framework2.Property.Dropdown({
      auth: beeboleAuth,
      displayName: "Project",
      description: (_a = params.description) != null ? _a : "Select the project. Choose a company first.",
      required: params.required,
      refreshers: ["company"],
      options: async ({ auth, company }) => {
        if (!auth)
          return { disabled: !0, options: [], placeholder: "Please connect your Beebole account first." };
        if (!company)
          return { disabled: !0, options: [], placeholder: "Please select a company first." };
        try {
          return {
            disabled: !1,
            options: (await listProjects(auth.secret_text, company)).filter((p) => p.active !== !1).map((p) => ({ label: p.name, value: p.id }))
          };
        } catch (e) {
          return { disabled: !0, options: [], placeholder: "Failed to load projects. Check your API token." };
        }
      }
    });
  },
  subprojectDropdown: (params) => {
    var _a;
    return import_pieces_framework2.Property.Dropdown({
      auth: beeboleAuth,
      displayName: "Subproject",
      description: (_a = params.description) != null ? _a : "Select the subproject. Choose a project first.",
      required: params.required,
      refreshers: ["project"],
      options: async ({ auth, project }) => {
        if (!auth)
          return { disabled: !0, options: [], placeholder: "Please connect your Beebole account first." };
        if (!project)
          return { disabled: !0, options: [], placeholder: "Please select a project first." };
        try {
          return {
            disabled: !1,
            options: (await listSubprojects(
              auth.secret_text,
              project
            )).filter((s) => s.active !== !1).map((s) => ({ label: s.name, value: s.id }))
          };
        } catch (e) {
          return { disabled: !0, options: [], placeholder: "Failed to load subprojects. Check your API token." };
        }
      }
    });
  },
  personDropdown: (params) => {
    var _a;
    return import_pieces_framework2.Property.Dropdown({
      auth: beeboleAuth,
      displayName: "Person",
      description: (_a = params.description) != null ? _a : "Select the person. Choose a company first.",
      required: params.required,
      refreshers: ["company"],
      options: async ({ auth, company }) => {
        if (!auth)
          return { disabled: !0, options: [], placeholder: "Please connect your Beebole account first." };
        if (!company)
          return { disabled: !0, options: [], placeholder: "Please select a company first." };
        try {
          return {
            disabled: !1,
            options: (await listPeople(auth.secret_text, company)).filter((p) => p.active !== !1).map((p) => ({
              label: p.email ? `${p.name} (${p.email})` : p.name,
              value: p.id
            }))
          };
        } catch (e) {
          return { disabled: !0, options: [], placeholder: "Failed to load people. Check your API token." };
        }
      }
    });
  },
  absenceDropdown: (params) => {
    var _a;
    return import_pieces_framework2.Property.Dropdown({
      auth: beeboleAuth,
      displayName: "Absence Type",
      description: (_a = params.description) != null ? _a : "Select the absence type (e.g. Vacation, Sick Leave).",
      required: params.required,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return { disabled: !0, options: [], placeholder: "Please connect your Beebole account first." };
        try {
          return {
            disabled: !1,
            options: (await listAbsences(auth.secret_text)).filter((a) => a.active !== !1).map((a) => ({ label: a.name, value: a.id }))
          };
        } catch (e) {
          return { disabled: !0, options: [], placeholder: "Failed to load absence types." };
        }
      }
    });
  }
};

// ../ap/packages/pieces/community/beebole/src/lib/actions/create-person.ts
var createPersonAction = (0, import_pieces_framework3.createAction)({
  auth: beeboleAuth,
  name: "create_person",
  classification: "WRITE",
  displayName: "Create Person",
  description: "Creates a new person in Beebole. An available license is required for the person to be active.",
  audience: "both",
  aiMetadata: {
    description: "Creates a person (employee, project lead, or admin) under a given company in Beebole, optionally emailing them a login invitation when an email is provided. Use to onboard a new team member or contact. Not idempotent: each call creates a new person record and may re-send an invite, with no de-duplication on name or email.",
    idempotent: !1
  },
  props: {
    company: beeboleProps.companyDropdown({
      required: !0,
      description: "The company this person belongs to."
    }),
    name: import_pieces_framework3.Property.ShortText({
      displayName: "Full Name",
      description: 'The full name of the person (e.g. "Jane Doe").',
      required: !0
    }),
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Email",
      description: "Email address. Required if you want to invite the person to log in to Beebole.",
      required: !1
    }),
    invite: import_pieces_framework3.Property.Checkbox({
      displayName: "Send Invitation Email",
      description: "Send an invitation email so the person can log in to Beebole. Requires an email address.",
      required: !1,
      defaultValue: !1
    }),
    userGroup: import_pieces_framework3.Property.StaticDropdown({
      displayName: "User Group",
      description: 'The permission level for this person. "Employee" can log time; "Project Lead" can also manage projects; "Admin" has full access.',
      required: !1,
      defaultValue: "employee",
      options: {
        options: [
          { label: "Employee", value: "employee" },
          { label: "Project Lead", value: "project_lead" },
          { label: "Administrator", value: "admin" }
        ]
      }
    })
  },
  async run(context) {
    var _a;
    let person = {
      name: context.propsValue.name,
      company: { id: context.propsValue.company }
    };
    context.propsValue.email && (person.email = context.propsValue.email), context.propsValue.invite !== void 0 && (person.invite = context.propsValue.invite), context.propsValue.userGroup && (person.userGroup = context.propsValue.userGroup);
    let response = await beeboleClient.call({
      token: context.auth.secret_text,
      body: { service: "person.create", person }
    });
    if (response.body.status !== "ok")
      throw new Error(`Beebole returned an error: ${(_a = response.body.message) != null ? _a : "Unknown error"}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/beebole/src/lib/actions/create-company.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var createCompanyAction = (0, import_pieces_framework4.createAction)({
  auth: beeboleAuth,
  name: "create_company",
  classification: "WRITE",
  displayName: "Create Company",
  description: "Creates a new company (customer) in Beebole.",
  audience: "both",
  aiMetadata: {
    description: "Creates a company in Beebole, either an external customer or, when marked internal/corporate, your own organization. Use as the top-level container before creating projects under it. Not idempotent: each call creates a new company even if one with the same name already exists.",
    idempotent: !1
  },
  props: {
    name: import_pieces_framework4.Property.ShortText({
      displayName: "Company Name",
      description: "The name of the company (customer) to create.",
      required: !0
    }),
    corporate: import_pieces_framework4.Property.Checkbox({
      displayName: "Internal Company",
      description: "Enable if this company represents your own organization (internal/corporate) rather than a customer.",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    var _a, _b;
    let response = await beeboleClient.call({
      token: context.auth.secret_text,
      body: {
        service: "company.create",
        company: {
          name: context.propsValue.name,
          corporate: (_a = context.propsValue.corporate) != null ? _a : !1
        }
      }
    });
    if (response.body.status !== "ok")
      throw new Error(`Beebole returned an error: ${(_b = response.body.message) != null ? _b : "Unknown error"}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/beebole/src/lib/actions/create-project.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var createProjectAction = (0, import_pieces_framework5.createAction)({
  auth: beeboleAuth,
  name: "create_project",
  classification: "WRITE",
  displayName: "Create Project",
  description: "Creates a new project under a company in Beebole.",
  audience: "both",
  aiMetadata: {
    description: "Creates a project owned by an existing company in Beebole, with an optional start date and description. Use after the company exists and before adding subprojects or logging time. Not idempotent: each call creates a new project even with identical details.",
    idempotent: !1
  },
  props: {
    company: beeboleProps.companyDropdown({
      required: !0,
      description: "The company (customer) that owns this project."
    }),
    name: import_pieces_framework5.Property.ShortText({
      displayName: "Project Name",
      description: 'The name of the new project (e.g. "Website Redesign").',
      required: !0
    }),
    startDate: import_pieces_framework5.Property.ShortText({
      displayName: "Start Date",
      description: 'The project start date in YYYY-MM-DD format (e.g. "2026-01-15"). Leave empty for no start date.',
      required: !1
    }),
    description: import_pieces_framework5.Property.LongText({
      displayName: "Description",
      description: "Optional description of the project.",
      required: !1
    })
  },
  async run(context) {
    var _a;
    let project = {
      name: context.propsValue.name,
      company: { id: context.propsValue.company }
    };
    context.propsValue.startDate && (project.startDate = context.propsValue.startDate), context.propsValue.description && (project.description = context.propsValue.description);
    let response = await beeboleClient.call({
      token: context.auth.secret_text,
      body: { service: "project.create", project }
    });
    if (response.body.status !== "ok")
      throw new Error(`Beebole returned an error: ${(_a = response.body.message) != null ? _a : "Unknown error"}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/beebole/src/lib/actions/create-subproject.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var createSubprojectAction = (0, import_pieces_framework6.createAction)({
  auth: beeboleAuth,
  name: "create_subproject",
  classification: "WRITE",
  displayName: "Create Subproject",
  description: "Creates a new subproject under an existing project in Beebole.",
  audience: "both",
  aiMetadata: {
    description: "Creates a subproject inside an existing project (under its company) in Beebole. Use to break a project into work streams that time can be logged against. Not idempotent: each call creates a new subproject even with the same name.",
    idempotent: !1
  },
  props: {
    company: beeboleProps.companyDropdown({
      required: !0,
      description: "The company that owns the parent project."
    }),
    project: beeboleProps.projectDropdown({
      required: !0,
      description: "The project that will contain this subproject."
    }),
    name: import_pieces_framework6.Property.ShortText({
      displayName: "Subproject Name",
      description: 'The name of the new subproject (e.g. "Prototype", "Phase 1").',
      required: !0
    })
  },
  async run(context) {
    var _a;
    let response = await beeboleClient.call({
      token: context.auth.secret_text,
      body: {
        service: "subproject.create",
        subproject: {
          name: context.propsValue.name,
          project: { id: context.propsValue.project }
        }
      }
    });
    if (response.body.status !== "ok")
      throw new Error(`Beebole returned an error: ${(_a = response.body.message) != null ? _a : "Unknown error"}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/beebole/src/lib/actions/deactivate-subproject.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var deactivateSubprojectAction = (0, import_pieces_framework7.createAction)({
  auth: beeboleAuth,
  name: "deactivate_subproject",
  classification: "DESTRUCTIVE",
  displayName: "Deactivate Subproject",
  description: "Marks a subproject as inactive in Beebole. Inactive subprojects are hidden from new time entries.",
  audience: "both",
  aiMetadata: {
    description: "Deactivates a subproject in Beebole so it is hidden from new time entries while existing entries are preserved. Use to retire a completed or unused work stream. Effectively idempotent: a subproject already inactive stays inactive, though the call still hits the API each time.",
    idempotent: !0
  },
  props: {
    company: beeboleProps.companyDropdown({
      required: !0,
      description: "The company that owns the project."
    }),
    project: beeboleProps.projectDropdown({
      required: !0,
      description: "The project containing the subproject to deactivate."
    }),
    subproject: beeboleProps.subprojectDropdown({
      required: !0,
      description: "The subproject to deactivate."
    })
  },
  async run(context) {
    var _a;
    let response = await beeboleClient.call({
      token: context.auth.secret_text,
      body: {
        service: "subproject.deactivate",
        id: context.propsValue.subproject
      }
    });
    if (response.body.status !== "ok")
      throw new Error(`Beebole returned an error: ${(_a = response.body.message) != null ? _a : "Unknown error"}`);
    return {
      subproject_id: context.propsValue.subproject,
      project_id: context.propsValue.project,
      company_id: context.propsValue.company,
      status: response.body.status,
      deactivated: !0
    };
  }
});

// ../ap/packages/pieces/community/beebole/src/lib/actions/create-multiple-time-entries.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var dateRegex = /^\d{4}-\d{2}-\d{2}$/, createMultipleTimeEntriesAction = (0, import_pieces_framework8.createAction)({
  auth: beeboleAuth,
  name: "create_multiple_time_entries",
  classification: "WRITE",
  displayName: "Create Multiple Time Entries",
  description: "Creates time entries (working time or absence) across multiple days in Beebole.",
  audience: "both",
  aiMetadata: {
    description: "Logs the same number of hours on each of several dates in Beebole, in one of two modes: working time against a chosen subproject, or an absence of a chosen type (e.g. vacation, sick leave). Use to bulk-log time across a date range; per-date failures are collected rather than aborting unless continue-on-error is disabled. Not idempotent: each call appends new time entries, so repeating it duplicates them.",
    idempotent: !1
  },
  props: {
    entryType: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Entry Type",
      description: "Choose whether you are logging working time on a subproject or an absence (e.g. vacation, sick leave).",
      required: !0,
      defaultValue: "working_time",
      options: {
        options: [
          { label: "Working Time", value: "working_time" },
          { label: "Absence", value: "absence" }
        ]
      }
    }),
    absence: beeboleProps.absenceDropdown({
      required: !1,
      description: "The absence type (e.g. Vacation, Sick Leave) to log on the selected dates."
    }),
    company: beeboleProps.companyDropdown({
      required: !1,
      description: "The company that owns the project."
    }),
    project: beeboleProps.projectDropdown({
      required: !1,
      description: "The project containing the subproject."
    }),
    subproject: beeboleProps.subprojectDropdown({
      required: !1,
      description: "The subproject to log working time against."
    }),
    dates: import_pieces_framework8.Property.Array({
      displayName: "Dates",
      description: 'The dates to create time entries for, each in YYYY-MM-DD format (e.g. "2026-05-21").',
      required: !0
    }),
    hours: import_pieces_framework8.Property.Number({
      displayName: "Hours per Day",
      description: "The number of hours to log for each date (e.g. 8 for a full working day).",
      required: !0,
      defaultValue: 8
    }),
    comment: import_pieces_framework8.Property.LongText({
      displayName: "Comment",
      description: "Optional comment applied to every created time entry.",
      required: !1
    }),
    continueOnError: import_pieces_framework8.Property.Checkbox({
      displayName: "Continue on Error",
      description: "If enabled, a failure on one date will not stop creation for the remaining dates.",
      required: !1,
      defaultValue: !0
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e;
    let { entryType, dates, hours, comment, continueOnError } = context.propsValue, dateList = dates.map((d) => typeof d == "string" ? d.trim() : "").filter((d) => d.length > 0);
    if (dateList.length === 0)
      throw new Error("At least one date is required.");
    let invalidDates = dateList.filter((d) => !dateRegex.test(d));
    if (invalidDates.length > 0)
      throw new Error(
        `Invalid date format (expected YYYY-MM-DD): ${invalidDates.join(", ")}`
      );
    if (entryType === "absence" && !context.propsValue.absence)
      throw new Error("Absence type is required when Entry Type is Absence.");
    if (entryType === "working_time" && !context.propsValue.subproject)
      throw new Error(
        "Subproject is required when Entry Type is Working Time."
      );
    let targetBody = {};
    entryType === "absence" ? targetBody.absence = { id: context.propsValue.absence } : targetBody.subproject = { id: context.propsValue.subproject };
    let created = [], failed = [];
    for (let date of dateList) {
      let body = __spreadProps(__spreadValues({
        service: "time_entry.create"
      }, targetBody), {
        date,
        hours
      });
      comment && (body.comment = comment);
      try {
        let response = await beeboleClient.call({
          token: context.auth.secret_text,
          body
        });
        if (response.body.status !== "ok")
          throw new Error((_a = response.body.message) != null ? _a : "Unknown error");
        let entry = response.body.time_entry;
        created.push({
          time_entry_id: (_b = entry == null ? void 0 : entry.id) != null ? _b : null,
          date,
          hours: (_c = entry == null ? void 0 : entry.hours) != null ? _c : hours,
          comment: (_e = (_d = entry == null ? void 0 : entry.comment) != null ? _d : comment) != null ? _e : null,
          status: "ok"
        });
      } catch (error) {
        let message = error instanceof Error ? error.message : String(error);
        if (!continueOnError)
          throw new Error(
            `Failed to create time entry for ${date}: ${message}`
          );
        failed.push({ date, error: message });
      }
    }
    return {
      created_count: created.length,
      failed_count: failed.length,
      created,
      failed
    };
  }
});

// ../ap/packages/pieces/community/beebole/src/lib/actions/delete-multiple-time-entries.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var dateRegex2 = /^\d{4}-\d{2}-\d{2}$/, deleteMultipleTimeEntriesAction = (0, import_pieces_framework9.createAction)({
  auth: beeboleAuth,
  name: "delete_multiple_time_entries",
  classification: "DESTRUCTIVE",
  displayName: "Delete Multiple Time Entries",
  description: "Deletes multiple time entries (working time or absence) by their IDs and dates.",
  audience: "both",
  aiMetadata: {
    description: "Deletes a batch of time entries in Beebole, each identified by its numeric time-entry ID plus the date it was logged on. Use to remove previously created entries; per-entry failures are collected rather than aborting unless continue-on-error is disabled. Idempotent in effect: once an entry is deleted, re-running with the same IDs has no further effect (already-removed entries simply fail).",
    idempotent: !0
  },
  props: {
    entries: import_pieces_framework9.Property.Array({
      displayName: "Time Entries to Delete",
      description: "List of time entries to delete. Each row requires the time entry ID and the date it was logged on (YYYY-MM-DD).",
      required: !0,
      properties: {
        id: import_pieces_framework9.Property.Number({
          displayName: "Time Entry ID",
          description: 'The numeric ID of the time entry to delete. You can get this from a "List Time Entries" step or the Beebole API response when the entry was created.',
          required: !0
        }),
        date: import_pieces_framework9.Property.ShortText({
          displayName: "Date",
          description: 'The date the entry was logged, in YYYY-MM-DD format (e.g. "2026-05-21").',
          required: !0
        })
      }
    }),
    continueOnError: import_pieces_framework9.Property.Checkbox({
      displayName: "Continue on Error",
      description: "If enabled, a failure on one entry will not stop deletion for the remaining entries.",
      required: !1,
      defaultValue: !0
    })
  },
  async run(context) {
    var _a, _b;
    let { entries, continueOnError } = context.propsValue, rows = entries;
    if (!rows || rows.length === 0)
      throw new Error("At least one time entry is required.");
    let deleted = [], failed = [];
    for (let row of rows) {
      let id = typeof row.id == "string" ? Number(row.id) : row.id, date = typeof row.date == "string" ? row.date.trim() : "";
      if (!id || Number.isNaN(id)) {
        let message = "Missing or invalid time entry ID.";
        if (!continueOnError)
          throw new Error(message);
        failed.push({ time_entry_id: (_a = row.id) != null ? _a : null, date: date || null, error: message });
        continue;
      }
      if (!dateRegex2.test(date)) {
        let message = `Invalid date format (expected YYYY-MM-DD): "${date}"`;
        if (!continueOnError)
          throw new Error(message);
        failed.push({ time_entry_id: id, date: date || null, error: message });
        continue;
      }
      try {
        let response = await beeboleClient.call({
          token: context.auth.secret_text,
          body: {
            service: "time_entry.delete",
            id,
            date
          }
        });
        if (response.body.status !== "ok")
          throw new Error((_b = response.body.message) != null ? _b : "Unknown error");
        deleted.push({ time_entry_id: id, date, status: "ok" });
      } catch (error) {
        let message = error instanceof Error ? error.message : String(error);
        if (!continueOnError)
          throw new Error(`Failed to delete time entry ${id} on ${date}: ${message}`);
        failed.push({ time_entry_id: id, date, error: message });
      }
    }
    return {
      deleted_count: deleted.length,
      failed_count: failed.length,
      deleted,
      failed
    };
  }
});

// ../ap/packages/pieces/community/beebole/src/index.ts
var beebole = (0, import_pieces_framework10.createPiece)({
  displayName: "Beebole",
  description: "Time tracking and project management. Create companies, projects, people, and log time entries.",
  auth: beeboleAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/beebole.png",
  categories: [import_pieces_framework11.PieceCategory.PRODUCTIVITY, import_pieces_framework11.PieceCategory.HUMAN_RESOURCES],
  authors: ["sanket-a11y"],
  actions: [
    createPersonAction,
    createCompanyAction,
    createProjectAction,
    createSubprojectAction,
    deactivateSubprojectAction,
    createMultipleTimeEntriesAction,
    deleteMultipleTimeEntriesAction,
    (0, import_pieces_common3.createCustomApiCallAction)({
      baseUrl: () => "https://beebole-apps.com/api/v2",
      auth: beeboleAuth,
      authMapping: async (auth) => ({
        Authorization: `Basic ${Buffer.from(
          `${auth}:x`
        ).toString("base64")}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  beebole
});
