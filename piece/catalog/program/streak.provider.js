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

// ../ap/packages/pieces/community/streak/src/index.ts
var index_exports = {};
__export(index_exports, {
  streak: () => streak
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common15 = require("@activepieces/pieces-common"), import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_framework15 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/streak/src/lib/common/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/streak/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
async function streakApiCall({
  apiKey,
  method,
  path,
  queryParams,
  body,
  contentType
}) {
  let headers = {};
  return contentType && (headers["Content-Type"] = contentType), await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${STREAK_BASE_URL}${path}`,
    authentication: {
      type: import_pieces_common.AuthenticationType.BASIC,
      username: apiKey,
      password: ""
    },
    queryParams,
    headers,
    body
  });
}
var STREAK_BASE_URL = "https://api.streak.com";

// ../ap/packages/pieces/community/streak/src/lib/common/auth.ts
var streakAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `Your personal Streak API key.

**How to get your API key:**
1. Open Gmail with the Streak Chrome extension installed.
2. Click the Streak icon in the right sidebar.
3. Click the **Settings** (gear) button, then go to **Integrations**.
4. In the **Streak API** section, click **Create New Key** and copy the key.

Your API key gives full access to your Streak account \u2014 keep it secret.`,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await streakApiCall({
        apiKey: auth,
        method: import_pieces_common2.HttpMethod.GET,
        path: "/api/v1/users/me"
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid Streak API Key. Make sure the key is active in Streak under Integrations."
      };
    }
  }
});

// ../ap/packages/pieces/community/streak/src/lib/actions/create-box.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/streak/src/lib/common/flatten.ts
function flattenStreakBox(box) {
  var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k;
  return {
    box_key: (_a = box.boxKey) != null ? _a : box.key,
    name: box.name,
    pipeline_key: box.pipelineKey,
    stage_key: (_b = box.stageKey) != null ? _b : null,
    creator_key: (_c = box.creatorKey) != null ? _c : null,
    notes: (_d = box.notes) != null ? _d : null,
    follower_count: (_e = box.followerCount) != null ? _e : null,
    comment_count: (_f = box.commentCount) != null ? _f : null,
    task_total: (_g = box.taskTotal) != null ? _g : null,
    file_count: (_h = box.fileCount) != null ? _h : null,
    gmail_thread_count: (_i = box.gmailThreadCount) != null ? _i : null,
    creation_timestamp: (_j = box.creationTimestamp) != null ? _j : null,
    last_updated_timestamp: (_k = box.lastUpdatedTimestamp) != null ? _k : null,
    followers: Array.isArray(box.followerKeys) ? box.followerKeys.join(", ") : null,
    fields_json: box.fields && Object.keys(box.fields).length > 0 ? JSON.stringify(box.fields) : null
  };
}

// ../ap/packages/pieces/community/streak/src/lib/common/props.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var pipelineDropdown = import_pieces_framework2.Property.Dropdown({
  auth: streakAuth,
  displayName: "Pipeline",
  description: "The pipeline to use. Pipelines are your workflows in Streak (e.g. Sales CRM, Hiring, Project Tracker).",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your Streak account first"
      };
    try {
      return {
        disabled: !1,
        options: ((_a = (await streakApiCall({
          apiKey: auth.secret_text,
          method: import_pieces_common3.HttpMethod.GET,
          path: "/api/v1/pipelines"
        })).body) != null ? _a : []).map((p) => {
          var _a2;
          return {
            label: p.name,
            value: (_a2 = p.pipelineKey) != null ? _a2 : p.key
          };
        })
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load pipelines. Check your API key."
      };
    }
  }
}), stageDropdown = import_pieces_framework2.Property.Dropdown({
  auth: streakAuth,
  displayName: "Stage",
  description: "The stage that the box should be in. Stages are the columns of a pipeline (e.g. Lead, Negotiating, Closed Won).",
  required: !1,
  refreshers: ["pipelineKey"],
  options: async ({ auth, pipelineKey }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your Streak account first"
      };
    if (!pipelineKey)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please select a pipeline first"
      };
    try {
      let response = await streakApiCall({
        apiKey: auth.secret_text,
        method: import_pieces_common3.HttpMethod.GET,
        path: `/api/v1/pipelines/${pipelineKey}/stages`
      });
      return {
        disabled: !1,
        options: Object.values((_a = response.body) != null ? _a : {}).map((s) => ({ label: s.name, value: s.key }))
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load stages for this pipeline."
      };
    }
  }
}), boxDropdown = import_pieces_framework2.Property.Dropdown({
  auth: streakAuth,
  displayName: "Box",
  description: "The box (record) inside the selected pipeline to operate on.",
  required: !0,
  refreshers: ["pipelineKey"],
  options: async ({ auth, pipelineKey }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your Streak account first"
      };
    if (!pipelineKey)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please select a pipeline first"
      };
    try {
      return {
        disabled: !1,
        options: ((_a = (await streakApiCall({
          apiKey: auth.secret_text,
          method: import_pieces_common3.HttpMethod.GET,
          path: `/api/v1/pipelines/${pipelineKey}/boxes`,
          queryParams: {
            sortBy: "lastUpdatedTimestamp",
            limit: "200"
          }
        })).body) != null ? _a : []).map((b) => {
          var _a2;
          return {
            label: b.name,
            value: (_a2 = b.boxKey) != null ? _a2 : b.key
          };
        })
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load boxes for this pipeline."
      };
    }
  }
}), teamDropdown = import_pieces_framework2.Property.Dropdown({
  auth: streakAuth,
  displayName: "Team",
  description: "The Streak team that owns this contact or organization. Contacts and organizations are shared at the team level.",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    var _a, _b;
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your Streak account first"
      };
    try {
      return {
        disabled: !1,
        options: ((_b = (_a = (await streakApiCall({
          apiKey: auth.secret_text,
          method: import_pieces_common3.HttpMethod.GET,
          path: "/api/v2/users/me/teams"
        })).body) == null ? void 0 : _a.results) != null ? _b : []).map((t) => ({
          label: t.name,
          value: t.key
        }))
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load teams. Check your API key."
      };
    }
  }
});

// ../ap/packages/pieces/community/streak/src/lib/actions/create-box.ts
var createBoxAction = (0, import_pieces_framework3.createAction)({
  auth: streakAuth,
  name: "create_box",
  displayName: "Create Box",
  description: "Create a new box (record) in a pipeline.",
  audience: "both",
  aiMetadata: {
    description: "Create a new box (Streak's CRM record, e.g. a deal, lead, or candidate) inside a pipeline. Use when an agent needs to add a new record to a Streak pipeline; requires the target pipeline and a name, with optional starting stage, notes, and assignee emails. Not idempotent: each call creates another box even with identical inputs.",
    idempotent: !1
  },
  props: {
    pipelineKey: pipelineDropdown,
    name: import_pieces_framework3.Property.ShortText({
      displayName: "Name",
      description: "The display name for this box (e.g. the deal name or the candidate name).",
      required: !0
    }),
    stageKey: stageDropdown,
    notes: import_pieces_framework3.Property.LongText({
      displayName: "Notes",
      description: "Free-form notes attached to the box.",
      required: !1
    }),
    assignedToEmails: import_pieces_framework3.Property.Array({
      displayName: "Assigned To (Emails)",
      description: "Email addresses of Streak users to assign to this box. Leave empty for no assignees.",
      required: !1
    })
  },
  async run(context) {
    let { pipelineKey, name, stageKey, notes, assignedToEmails } = context.propsValue, body = { name };
    stageKey && (body.stageKey = stageKey), notes && (body.notes = notes), assignedToEmails && assignedToEmails.length > 0 && (body.assignedToSharingEntries = assignedToEmails.map(
      (email) => ({ email })
    ));
    let response = await streakApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common4.HttpMethod.POST,
      path: `/api/v2/pipelines/${pipelineKey}/boxes`,
      contentType: "application/json",
      body
    });
    return flattenStreakBox(response.body);
  }
});

// ../ap/packages/pieces/community/streak/src/lib/actions/create-comment.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var createCommentAction = (0, import_pieces_framework4.createAction)({
  auth: streakAuth,
  name: "create_comment",
  displayName: "Create Comment",
  description: "Add a comment to a box.",
  audience: "both",
  aiMetadata: {
    description: "Post a comment with the given message text onto a specific box. Use when an agent needs to record a note or update on a Streak record's activity feed; requires the box key and message. Not idempotent: each call appends another comment.",
    idempotent: !1
  },
  props: {
    pipelineKey: pipelineDropdown,
    boxKey: boxDropdown,
    message: import_pieces_framework4.Property.LongText({
      displayName: "Message",
      description: "The comment text to post on the box.",
      required: !0
    })
  },
  async run(context) {
    var _a, _b, _c;
    let { boxKey, message } = context.propsValue, response = await streakApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common5.HttpMethod.POST,
      path: `/api/v2/boxes/${boxKey}/comments`,
      contentType: "application/json",
      body: { message }
    });
    return {
      comment_key: response.body.key,
      box_key: (_a = response.body.boxKey) != null ? _a : boxKey,
      message: response.body.message,
      creator_key: (_b = response.body.creatorKey) != null ? _b : null,
      creation_date_epoch_ms: (_c = response.body.creationDate) != null ? _c : null
    };
  }
});

// ../ap/packages/pieces/community/streak/src/lib/actions/create-contact.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");
var createContactAction = (0, import_pieces_framework5.createAction)({
  auth: streakAuth,
  name: "create_contact",
  displayName: "Create or Find Contact",
  description: "Create a contact in a team. If a contact with the same email already exists, return that contact instead of creating a duplicate.",
  audience: "both",
  aiMetadata: {
    description: "Create a person contact in a team, or return the existing one if a contact with the same email already exists (get-if-existing upsert keyed on email). Use when an agent needs to ensure a contact exists for a given email before linking it elsewhere; requires the team and at least one email address. Idempotent: repeating with the same email returns the same contact rather than duplicating.",
    idempotent: !0
  },
  props: {
    teamKey: teamDropdown,
    emailAddresses: import_pieces_framework5.Property.Array({
      displayName: "Email Addresses",
      description: "One or more email addresses for this contact.",
      required: !0
    }),
    givenName: import_pieces_framework5.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    familyName: import_pieces_framework5.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    title: import_pieces_framework5.Property.ShortText({
      displayName: "Title",
      description: 'Job title or role (e.g. "VP of Sales").',
      required: !1
    }),
    phoneNumbers: import_pieces_framework5.Property.Array({
      displayName: "Phone Numbers",
      required: !1
    }),
    addresses: import_pieces_framework5.Property.Array({
      displayName: "Addresses",
      required: !1
    }),
    twitterHandle: import_pieces_framework5.Property.ShortText({
      displayName: "Twitter Handle",
      description: 'Without the leading @ (e.g. "janedoe").',
      required: !1
    }),
    linkedinHandle: import_pieces_framework5.Property.ShortText({
      displayName: "LinkedIn Handle",
      required: !1
    }),
    other: import_pieces_framework5.Property.LongText({
      displayName: "Notes",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j;
    let {
      teamKey,
      emailAddresses,
      givenName,
      familyName,
      title,
      phoneNumbers,
      addresses,
      twitterHandle,
      linkedinHandle,
      other
    } = context.propsValue, body = {
      emailAddresses
    };
    givenName && (body.givenName = givenName), familyName && (body.familyName = familyName), title && (body.title = title), phoneNumbers && phoneNumbers.length > 0 && (body.phoneNumbers = phoneNumbers), addresses && addresses.length > 0 && (body.addresses = addresses), twitterHandle && (body.twitterHandle = twitterHandle), linkedinHandle && (body.linkedinHandle = linkedinHandle), other && (body.other = other);
    let contact = (await streakApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common6.HttpMethod.POST,
      path: `/api/v2/teams/${teamKey}/contacts`,
      queryParams: { getIfExisting: "true" },
      contentType: "application/json",
      body
    })).body;
    return {
      contact_key: contact.key,
      given_name: (_a = contact.givenName) != null ? _a : null,
      family_name: (_b = contact.familyName) != null ? _b : null,
      title: (_c = contact.title) != null ? _c : null,
      email_addresses: Array.isArray(contact.emailAddresses) ? contact.emailAddresses.join(", ") : null,
      phone_numbers: Array.isArray(contact.phoneNumbers) ? contact.phoneNumbers.join(", ") : null,
      addresses: Array.isArray(contact.addresses) ? contact.addresses.join(", ") : null,
      twitter_handle: (_d = contact.twitterHandle) != null ? _d : null,
      facebook_handle: (_e = contact.facebookHandle) != null ? _e : null,
      linkedin_handle: (_f = contact.linkedinHandle) != null ? _f : null,
      photo_url: (_g = contact.photoUrl) != null ? _g : null,
      other: (_h = contact.other) != null ? _h : null,
      creation_date_epoch_ms: (_i = contact.creationDate) != null ? _i : null,
      last_saved_timestamp_epoch_ms: (_j = contact.lastSavedTimestamp) != null ? _j : null
    };
  }
});

// ../ap/packages/pieces/community/streak/src/lib/actions/create-organization.ts
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var createOrganizationAction = (0, import_pieces_framework6.createAction)({
  auth: streakAuth,
  name: "create_organization",
  displayName: "Create or Find Organization",
  description: "Create an organization in a team. If an organization with the same domain already exists, return that organization instead of creating a duplicate.",
  audience: "both",
  aiMetadata: {
    description: "Create a company/organization record in a team, or return the existing one if an organization with the same domain already exists (get-if-existing upsert keyed on domain). Use when an agent needs to ensure a company exists before associating contacts or boxes; requires the team plus at least one of name or domains. Idempotent when matched by domain: repeating returns the same organization rather than duplicating.",
    idempotent: !0
  },
  props: {
    teamKey: teamDropdown,
    name: import_pieces_framework6.Property.ShortText({
      displayName: "Organization Name",
      description: "The company name. Required unless a domain is provided (at least one of name or domains must be set).",
      required: !1
    }),
    domains: import_pieces_framework6.Property.Array({
      displayName: "Domains",
      description: 'Website domains for this organization (e.g. "acme.com"). Used to match existing organizations.',
      required: !1
    }),
    industry: import_pieces_framework6.Property.ShortText({
      displayName: "Industry",
      required: !1
    }),
    employeeCount: import_pieces_framework6.Property.Number({
      displayName: "Employee Count",
      required: !1
    }),
    phoneNumbers: import_pieces_framework6.Property.Array({
      displayName: "Phone Numbers",
      required: !1
    }),
    addresses: import_pieces_framework6.Property.Array({
      displayName: "Addresses",
      required: !1
    }),
    description: import_pieces_framework6.Property.LongText({
      displayName: "Description",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j;
    let {
      teamKey,
      name,
      domains,
      industry,
      employeeCount,
      phoneNumbers,
      addresses,
      description
    } = context.propsValue, body = {};
    name && (body.name = name), domains && domains.length > 0 && (body.domains = domains), industry && (body.industry = industry), employeeCount != null && (body.employeeCount = employeeCount), phoneNumbers && phoneNumbers.length > 0 && (body.phoneNumbers = phoneNumbers), addresses && addresses.length > 0 && (body.addresses = addresses), description && (body.description = description);
    let org = (await streakApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common7.HttpMethod.POST,
      path: `/api/v2/teams/${teamKey}/organizations`,
      queryParams: { getIfExisting: "true" },
      contentType: "application/json",
      body
    })).body;
    return {
      organization_key: org.key,
      name: (_a = org.name) != null ? _a : null,
      domains: Array.isArray(org.domains) ? org.domains.join(", ") : null,
      industry: (_b = org.industry) != null ? _b : null,
      employee_count: (_c = org.employeeCount) != null ? _c : null,
      phone_numbers: Array.isArray(org.phoneNumbers) ? org.phoneNumbers.join(", ") : null,
      addresses: Array.isArray(org.addresses) ? org.addresses.join(", ") : null,
      twitter_handle: (_d = org.twitterHandle) != null ? _d : null,
      facebook_handle: (_e = org.facebookHandle) != null ? _e : null,
      linkedin_handle: (_f = org.linkedinHandle) != null ? _f : null,
      logo_url: (_g = org.logoURL) != null ? _g : null,
      description: (_h = org.description) != null ? _h : null,
      creation_date_epoch_ms: (_i = org.creationDate) != null ? _i : null,
      last_saved_timestamp_epoch_ms: (_j = org.lastSavedTimestamp) != null ? _j : null
    };
  }
});

// ../ap/packages/pieces/community/streak/src/lib/actions/create-stage.ts
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework");
var createStageAction = (0, import_pieces_framework7.createAction)({
  auth: streakAuth,
  name: "create_stage",
  displayName: "Create Stage",
  description: "Add a new stage (column) to a pipeline.",
  audience: "both",
  aiMetadata: {
    description: `Add a new stage (a pipeline column representing a step like "Negotiating") to the specified pipeline. Use when an agent needs to extend a pipeline's set of stages before placing boxes in them; requires the target pipeline and a stage name. Not idempotent: each call appends another stage even with the same name.`,
    idempotent: !1
  },
  props: {
    pipelineKey: pipelineDropdown,
    name: import_pieces_framework7.Property.ShortText({
      displayName: "Stage Name",
      description: 'The name of the new stage. Must be unique within the pipeline (e.g. "Negotiating").',
      required: !0
    })
  },
  async run(context) {
    let response = await streakApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common8.HttpMethod.PUT,
      path: `/api/v1/pipelines/${context.propsValue.pipelineKey}/stages`,
      contentType: "application/x-www-form-urlencoded",
      body: new URLSearchParams({ name: context.propsValue.name }).toString()
    });
    return {
      stage_key: response.body.key,
      name: response.body.name
    };
  }
});

// ../ap/packages/pieces/community/streak/src/lib/actions/create-task.ts
var import_pieces_common9 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework");
var createTaskAction = (0, import_pieces_framework8.createAction)({
  auth: streakAuth,
  name: "create_task",
  displayName: "Create Task",
  description: "Create a new task on a box.",
  audience: "both",
  aiMetadata: {
    description: "Create a new task (a to-do item) attached to a specific box, with task text and an optional due date and assignee emails. Use when an agent needs to add a follow-up or action item to a Streak record; requires the box key and task text. Not idempotent: each call creates another task on the box.",
    idempotent: !1
  },
  props: {
    pipelineKey: pipelineDropdown,
    boxKey: boxDropdown,
    text: import_pieces_framework8.Property.ShortText({
      displayName: "Task Text",
      description: 'The task description, shown in Streak (e.g. "Send follow-up email").',
      required: !0
    }),
    dueDate: import_pieces_framework8.Property.DateTime({
      displayName: "Due Date",
      description: "When the task is due. Leave empty for no due date.",
      required: !1
    }),
    assignedToEmails: import_pieces_framework8.Property.Array({
      displayName: "Assigned To (Emails)",
      description: "Email addresses of Streak users to assign this task to. Leave empty to leave unassigned.",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f;
    let { boxKey, text, dueDate, assignedToEmails } = context.propsValue, body = { key: boxKey, text };
    dueDate && (body.dueDate = new Date(dueDate).getTime()), assignedToEmails && assignedToEmails.length > 0 && (body.assignedToSharingEntries = assignedToEmails.map(
      (email) => ({ email })
    ));
    let task = (await streakApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common9.HttpMethod.POST,
      path: `/api/v2/boxes/${boxKey}/tasks`,
      contentType: "application/json",
      body
    })).body;
    return {
      task_key: task.key,
      box_key: (_a = task.boxKey) != null ? _a : boxKey,
      text: task.text,
      status: (_b = task.status) != null ? _b : null,
      due_date_epoch_ms: (_c = task.dueDate) != null ? _c : null,
      creator_key: (_d = task.creatorKey) != null ? _d : null,
      creation_date_epoch_ms: (_e = task.creationDate) != null ? _e : null,
      last_updated_date_epoch_ms: (_f = task.lastUpdatedDate) != null ? _f : null,
      assigned_to_emails: Array.isArray(task.assignedToSharingEntries) ? task.assignedToSharingEntries.map((a) => a.email).filter((e) => !!e).join(", ") : null
    };
  }
});

// ../ap/packages/pieces/community/streak/src/lib/actions/find-box.ts
var import_pieces_common10 = require("@activepieces/pieces-common"), import_pieces_framework9 = require("@activepieces/pieces-framework");
var findBoxAction = (0, import_pieces_framework9.createAction)({
  auth: streakAuth,
  name: "find_box",
  displayName: "Find Boxes",
  description: "Search boxes by name or query. Returns matching boxes across all pipelines, optionally filtered to one pipeline.",
  audience: "both",
  aiMetadata: {
    description: "Search for boxes by a text query matched against box name, notes, emails, and text custom fields, returning the matching boxes; by default it spans all pipelines, or it can be restricted to a single pipeline. Use when an agent needs to look up a box by name or content rather than by a known box key. Read-only and idempotent; a search query is required.",
    idempotent: !0
  },
  props: {
    query: import_pieces_framework9.Property.ShortText({
      displayName: "Search Query",
      description: "The text to search for. Matches against box name, notes, emails, and text custom fields.",
      required: !0
    }),
    pipelineKey: __spreadProps(__spreadValues({}, pipelineDropdown), { required: !1 })
  },
  async run(context) {
    var _a, _b;
    let { query, pipelineKey } = context.propsValue, queryParams = { query };
    return pipelineKey && (queryParams.pipelineKey = pipelineKey), ((_b = (_a = (await streakApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common10.HttpMethod.GET,
      path: "/api/v1/search",
      queryParams
    })).body.results) == null ? void 0 : _a.boxes) != null ? _b : []).map((b) => {
      var _a2, _b2, _c;
      return {
        box_key: b.boxKey,
        name: b.name,
        pipeline_key: (_a2 = b.pipelineKey) != null ? _a2 : null,
        stage_key: (_b2 = b.stageKey) != null ? _b2 : null,
        last_updated_timestamp_epoch_ms: (_c = b.lastUpdatedTimestamp) != null ? _c : null
      };
    });
  }
});

// ../ap/packages/pieces/community/streak/src/lib/actions/get-box.ts
var import_pieces_common11 = require("@activepieces/pieces-common"), import_pieces_framework10 = require("@activepieces/pieces-framework");
var getBoxAction = (0, import_pieces_framework10.createAction)({
  auth: streakAuth,
  name: "get_box",
  displayName: "Get Box",
  description: "Fetch a single box with its current stage, fields, and counts.",
  audience: "both",
  aiMetadata: {
    description: "Retrieve a single box by its box key, including its current stage, custom field values, and counts. Use when an agent already has a specific box key (e.g. from a trigger or a Find Boxes result) and needs its full current details. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    pipelineKey: pipelineDropdown,
    boxKey: boxDropdown
  },
  async run(context) {
    let response = await streakApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common11.HttpMethod.GET,
      path: `/api/v1/boxes/${context.propsValue.boxKey}`
    });
    return flattenStreakBox(response.body);
  }
});

// ../ap/packages/pieces/community/streak/src/lib/actions/get-current-user.ts
var import_pieces_common12 = require("@activepieces/pieces-common"), import_pieces_framework11 = require("@activepieces/pieces-framework");
var getCurrentUserAction = (0, import_pieces_framework11.createAction)({
  auth: streakAuth,
  name: "get_current_user",
  displayName: "Get Current User",
  description: "Get the Streak user associated with the connected API key.",
  audience: "both",
  aiMetadata: {
    description: "Retrieve the Streak user account tied to the connected API key, including its user key, email, and display name. Use when an agent needs the current user's identity or user key (for example to set assignees or attribute actions). Takes no input; read-only and idempotent.",
    idempotent: !0
  },
  props: {},
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h;
    let user = (await streakApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common12.HttpMethod.GET,
      path: "/api/v1/users/me"
    })).body;
    return {
      user_key: (_a = user.userKey) != null ? _a : user.key,
      email: (_b = user.email) != null ? _b : null,
      lowercase_email: (_c = user.lowercaseEmail) != null ? _c : null,
      display_name: (_d = user.displayName) != null ? _d : null,
      is_oauth_complete: (_e = user.isOauthComplete) != null ? _e : null,
      creation_timestamp_epoch_ms: (_f = user.creationTimestamp) != null ? _f : null,
      last_updated_timestamp_epoch_ms: (_g = user.lastUpdatedTimestamp) != null ? _g : null,
      last_seen_timestamp_epoch_ms: (_h = user.lastSeenTimestamp) != null ? _h : null
    };
  }
});

// ../ap/packages/pieces/community/streak/src/lib/actions/update-box.ts
var import_pieces_common13 = require("@activepieces/pieces-common"), import_pieces_framework12 = require("@activepieces/pieces-framework");
var updateBoxAction = (0, import_pieces_framework12.createAction)({
  auth: streakAuth,
  name: "update_box",
  displayName: "Update Box",
  description: "Update the name, notes, stage, assignees, or custom fields on an existing box.",
  audience: "both",
  aiMetadata: {
    description: "Update fields on an existing box identified by its box key, setting any of name, stage, notes, assignee emails, or custom fields (only provided fields change). Use when an agent needs to modify or advance an existing Streak record, such as moving a deal to a new stage. Idempotent: re-applying the same values leaves the box in the same state.",
    idempotent: !0
  },
  props: {
    pipelineKey: pipelineDropdown,
    boxKey: boxDropdown,
    name: import_pieces_framework12.Property.ShortText({
      displayName: "New Name",
      description: "Leave empty to keep the existing name.",
      required: !1
    }),
    stageKey: stageDropdown,
    notes: import_pieces_framework12.Property.LongText({
      displayName: "Notes",
      description: "Replaces the existing notes. Leave empty to keep them unchanged.",
      required: !1
    }),
    assignedToEmails: import_pieces_framework12.Property.Array({
      displayName: "Assigned To (Emails)",
      description: "Replaces the current assignees with these emails. Leave empty to keep assignees unchanged.",
      required: !1
    }),
    fields: import_pieces_framework12.Property.Object({
      displayName: "Custom Fields",
      description: 'Map of custom field key to value (e.g. {"1007": "Closed", "1039": 42}). Find the field keys in Streak under Pipeline Settings \u2192 Fields.',
      required: !1
    })
  },
  async run(context) {
    let { boxKey, name, stageKey, notes, assignedToEmails, fields } = context.propsValue, body = {};
    name && (body.name = name), stageKey && (body.stageKey = stageKey), notes && (body.notes = notes), assignedToEmails && assignedToEmails.length > 0 && (body.assignedToSharingEntries = assignedToEmails.map(
      (email) => ({ email })
    )), fields && Object.keys(fields).length > 0 && (body.fields = fields);
    let response = await streakApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common13.HttpMethod.POST,
      path: `/api/v1/boxes/${boxKey}`,
      contentType: "application/json",
      body
    });
    return flattenStreakBox(response.body);
  }
});

// ../ap/packages/pieces/community/streak/src/lib/common/trigger-factory.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/streak/src/lib/common/webhooks.ts
var import_pieces_common14 = require("@activepieces/pieces-common");
async function createPipelineWebhook({
  apiKey,
  pipelineKey,
  event,
  targetUrl
}) {
  return (await streakApiCall({
    apiKey,
    method: import_pieces_common14.HttpMethod.POST,
    path: "/api/v2/webhooks",
    queryParams: { pipelineKey },
    contentType: "application/json",
    body: { event, targetUrl }
  })).body;
}
async function createTeamWebhook({
  apiKey,
  teamKey,
  event,
  targetUrl
}) {
  return (await streakApiCall({
    apiKey,
    method: import_pieces_common14.HttpMethod.POST,
    path: "/api/v2/webhooks",
    queryParams: { teamKey },
    contentType: "application/json",
    body: { event, targetUrl }
  })).body;
}
async function deleteWebhook({
  apiKey,
  webhookKey
}) {
  await streakApiCall({
    apiKey,
    method: import_pieces_common14.HttpMethod.DELETE,
    path: `/api/v2/webhooks/${webhookKey}`
  });
}

// ../ap/packages/pieces/community/streak/src/lib/common/trigger-factory.ts
function createPipelineWebhookTrigger({
  name,
  displayName,
  description,
  aiMetadata,
  event,
  sampleData
}) {
  return (0, import_pieces_framework13.createTrigger)({
    auth: streakAuth,
    name,
    displayName,
    description,
    aiMetadata,
    type: import_pieces_framework13.TriggerStrategy.WEBHOOK,
    props: {
      pipelineKey: pipelineDropdown
    },
    sampleData,
    async onEnable(context) {
      let webhook = await createPipelineWebhook({
        apiKey: context.auth.secret_text,
        pipelineKey: context.propsValue.pipelineKey,
        event,
        targetUrl: context.webhookUrl
      });
      await context.store.put(STORE_KEY, { webhookKey: webhook.key });
    },
    async onDisable(context) {
      let stored = await context.store.get(STORE_KEY);
      stored != null && stored.webhookKey && await deleteWebhook({
        apiKey: context.auth.secret_text,
        webhookKey: stored.webhookKey
      });
    },
    async run(context) {
      return [context.payload.body];
    }
  });
}
function createTeamWebhookTrigger({
  name,
  displayName,
  description,
  aiMetadata,
  event,
  sampleData
}) {
  return (0, import_pieces_framework13.createTrigger)({
    auth: streakAuth,
    name,
    displayName,
    description,
    aiMetadata,
    type: import_pieces_framework13.TriggerStrategy.WEBHOOK,
    props: {
      teamKey: teamDropdown
    },
    sampleData,
    async onEnable(context) {
      let webhook = await createTeamWebhook({
        apiKey: context.auth.secret_text,
        teamKey: context.propsValue.teamKey,
        event,
        targetUrl: context.webhookUrl
      });
      await context.store.put(STORE_KEY, { webhookKey: webhook.key });
    },
    async onDisable(context) {
      let stored = await context.store.get(STORE_KEY);
      stored != null && stored.webhookKey && await deleteWebhook({
        apiKey: context.auth.secret_text,
        webhookKey: stored.webhookKey
      });
    },
    async run(context) {
      return [context.payload.body];
    }
  });
}
var STORE_KEY = "streak_webhook";

// ../ap/packages/pieces/community/streak/src/lib/triggers/box-pipeline-changed.ts
var boxPipelineChangedTrigger = createPipelineWebhookTrigger({
  name: "box_pipeline_changed",
  displayName: "Box Moved to Different Pipeline",
  description: "Triggers when a box is moved out of (or into) the selected pipeline.",
  aiMetadata: {
    description: "Fires when a box is moved between pipelines relative to the selected pipeline (moved into or out of it), representing a record being reassigned to a different pipeline; the event includes the previous pipeline key."
  },
  event: "BOX_CHANGE_PIPELINE",
  sampleData: {
    boxKey: "agxzfm1haWxmb29nYWVyMQsSA0JveBiAgIDAj-iiCgw",
    name: "Acme Inc",
    pipelineKey: "agxzfm1haWxmb29nYWVyMQsSCFBpcGVsaW5lGOyXFAw",
    previousPipelineKey: "agxzfm1haWxmb29nYWVyMQsSCFBpcGVsaW5lGOyXFAx",
    lastUpdatedTimestamp: 171408e7
  }
});

// ../ap/packages/pieces/community/streak/src/lib/triggers/box-stage-changed.ts
var boxStageChangedTrigger = createPipelineWebhookTrigger({
  name: "box_stage_changed",
  displayName: "Box Stage Changed",
  description: "Triggers when a box moves between stages in the selected pipeline.",
  aiMetadata: {
    description: "Fires when a box in the selected pipeline moves from one stage to another, representing progression through the pipeline; the event includes both the new and previous stage keys."
  },
  event: "BOX_CHANGE_STAGE",
  sampleData: {
    boxKey: "agxzfm1haWxmb29nYWVyMQsSA0JveBiAgIDAj-iiCgw",
    name: "Acme Inc",
    pipelineKey: "agxzfm1haWxmb29nYWVyMQsSCFBpcGVsaW5lGOyXFAw",
    stageKey: "5002",
    previousStageKey: "5001",
    lastUpdatedTimestamp: 171408e7
  }
});

// ../ap/packages/pieces/community/streak/src/lib/triggers/new-box.ts
var newBoxTrigger = createPipelineWebhookTrigger({
  name: "new_box",
  displayName: "New Box",
  description: "Triggers when a new box is created in the selected pipeline.",
  aiMetadata: {
    description: "Fires when a new box (CRM record such as a deal or lead) is created in the selected Streak pipeline, representing a freshly added record."
  },
  event: "BOX_CREATE",
  sampleData: {
    boxKey: "agxzfm1haWxmb29nYWVyMQsSDE9yZ2FuaXphdGlvbiIHc3RyZWFrMAwLEgRVc2VyGNwGDAsSA0JveBiAgIDAj-iiCgw",
    name: "Acme Inc",
    pipelineKey: "agxzfm1haWxmb29nYWVyMQsSDE9yZ2FuaXphdGlvbiIHc3RyZWFrMAwLEgRVc2VyGNwGDAsSCFBpcGVsaW5lGOyXFAw",
    stageKey: "5001",
    creatorKey: "agxzfm1haWxmb29nYWVyMQsSDE9yZ2FuaXphdGlvbiIHc3RyZWFrMAwLEgRVc2VyGNwGDA",
    creationTimestamp: 171408e7,
    lastUpdatedTimestamp: 171408e7
  }
});

// ../ap/packages/pieces/community/streak/src/lib/triggers/new-comment.ts
var newCommentTrigger = createPipelineWebhookTrigger({
  name: "new_comment",
  displayName: "New Comment",
  description: "Triggers when a comment is added to a box in the selected pipeline.",
  aiMetadata: {
    description: "Fires when a new comment is posted on a box in the selected pipeline, representing a note or update added to a CRM record's activity feed."
  },
  event: "COMMENT_CREATE",
  sampleData: {
    key: "agxzfm1haWxmb29nYWVyMQsSB0NvbW1lbnQYgICAwI_oogow",
    boxKey: "agxzfm1haWxmb29nYWVyMQsSA0JveBiAgIDAj-iiCgw",
    message: "Heads up, just got off the phone with the buyer.",
    creatorKey: "agxzfm1haWxmb29nYWVyMQsSBFVzZXIY3AYM",
    creationDate: 171408e7
  }
});

// ../ap/packages/pieces/community/streak/src/lib/triggers/new-contact.ts
var newContactTrigger = createTeamWebhookTrigger({
  name: "new_contact",
  displayName: "New Contact",
  description: "Triggers when a new contact is created in the selected team.",
  aiMetadata: {
    description: "Fires when a new person contact is created in the selected team, representing a freshly added individual in the team's shared CRM."
  },
  event: "CONTACT_CREATE",
  sampleData: {
    key: "agxzfm1haWxmb29nYWVyMQsSB0NvbnRhY3QYgICAwI_oogow",
    emailAddresses: ["jane@acme.com"],
    givenName: "Jane",
    familyName: "Doe",
    title: "VP of Sales",
    creationDate: 171408e7
  }
});

// ../ap/packages/pieces/community/streak/src/lib/triggers/new-meeting.ts
var newMeetingTrigger = createPipelineWebhookTrigger({
  name: "new_meeting_or_call_log",
  displayName: "New Meeting or Call Log",
  description: "Triggers when a meeting note or call log is created on a box in the selected pipeline.",
  aiMetadata: {
    description: "Fires when a new meeting note or call log is created on a box in the selected pipeline, representing a logged interaction (such as a discovery call) on a CRM record."
  },
  event: "MEETING_CREATE",
  sampleData: {
    key: "agxzfm1haWxmb29nYWVyMQsSB01lZXRpbmcYgICAwI_oogow",
    boxKey: "agxzfm1haWxmb29nYWVyMQsSA0JveBiAgIDAj-iiCgw",
    name: "Discovery call",
    notes: "Discussed pricing and integration timeline.",
    timestamp: 171408e7
  }
});

// ../ap/packages/pieces/community/streak/src/lib/triggers/new-task.ts
var newTaskTrigger = createPipelineWebhookTrigger({
  name: "new_task",
  displayName: "New Task",
  description: "Triggers when a task is created on a box in the selected pipeline.",
  aiMetadata: {
    description: "Fires when a new task (to-do item) is created on a box in the selected pipeline, representing a freshly added action item on a CRM record."
  },
  event: "TASK_CREATE",
  sampleData: {
    key: "agxzfm1haWxmb29nYWVyMQsSBFRhc2sYgICAwI_oogow",
    boxKey: "agxzfm1haWxmb29nYWVyMQsSA0JveBiAgIDAj-iiCgw",
    text: "Send follow-up email to buyer.",
    status: "NOT_DONE",
    dueDate: 1714248e6,
    creatorKey: "agxzfm1haWxmb29nYWVyMQsSBFVzZXIY3AYM",
    creationDate: 171408e7
  }
});

// ../ap/packages/pieces/community/streak/src/lib/triggers/task-completed.ts
var taskCompletedTrigger = createPipelineWebhookTrigger({
  name: "task_completed",
  displayName: "Task Completed",
  description: "Triggers when a task on a box in the selected pipeline is marked complete.",
  aiMetadata: {
    description: "Fires when a task on a box in the selected pipeline is marked complete, representing a to-do item being finished on a CRM record."
  },
  event: "TASK_COMPLETE",
  sampleData: {
    key: "agxzfm1haWxmb29nYWVyMQsSBFRhc2sYgICAwI_oogow",
    boxKey: "agxzfm1haWxmb29nYWVyMQsSA0JveBiAgIDAj-iiCgw",
    text: "Send follow-up email to buyer.",
    status: "DONE",
    completedAt: 1714248e6
  }
});

// ../ap/packages/pieces/community/streak/src/lib/triggers/task-due.ts
var taskDueTrigger = createPipelineWebhookTrigger({
  name: "task_due",
  displayName: "Task Due",
  description: "Triggers when a task becomes due (but has not yet been completed) on a box in the selected pipeline.",
  aiMetadata: {
    description: "Fires when a still-incomplete task on a box in the selected pipeline reaches its due date, representing an outstanding action item that has come due on a CRM record."
  },
  event: "TASK_DUE",
  sampleData: {
    key: "agxzfm1haWxmb29nYWVyMQsSBFRhc2sYgICAwI_oogow",
    boxKey: "agxzfm1haWxmb29nYWVyMQsSA0JveBiAgIDAj-iiCgw",
    text: "Send follow-up email to buyer.",
    status: "NOT_DONE",
    dueDate: 1714248e6
  }
});

// ../ap/packages/pieces/community/streak/src/lib/triggers/updated-box.ts
var updatedBoxTrigger = createPipelineWebhookTrigger({
  name: "updated_box",
  displayName: "Updated Box",
  description: "Triggers when a field on a box is updated in the selected pipeline.",
  aiMetadata: {
    description: "Fires when any field on an existing box in the selected pipeline is edited, representing a change to a CRM record. Note this fires on general edits, while separate triggers cover stage changes and pipeline moves."
  },
  event: "BOX_EDIT",
  sampleData: {
    boxKey: "agxzfm1haWxmb29nYWVyMQsSDE9yZ2FuaXphdGlvbiIHc3RyZWFrMAwLEgNCb3gYgICAwI_oogow",
    name: "Acme Inc",
    pipelineKey: "agxzfm1haWxmb29nYWVyMQsSDE9yZ2FuaXphdGlvbiIHc3RyZWFrMAwLEgRVc2VyGNwGDAsSCFBpcGVsaW5lGOyXFAw",
    stageKey: "5001",
    lastUpdatedTimestamp: 171408e7
  }
});

// ../ap/packages/pieces/community/streak/src/lib/triggers/updated-contact.ts
var updatedContactTrigger = createTeamWebhookTrigger({
  name: "updated_contact",
  displayName: "Updated Contact",
  description: "Triggers when a contact in the selected team is updated.",
  aiMetadata: {
    description: "Fires when an existing person contact in the selected team is updated, representing a change to that individual's details in the team's shared CRM."
  },
  event: "CONTACT_UPDATE",
  sampleData: {
    key: "agxzfm1haWxmb29nYWVyMQsSB0NvbnRhY3QYgICAwI_oogow",
    emailAddresses: ["jane@acme.com"],
    givenName: "Jane",
    familyName: "Doe",
    title: "VP of Sales",
    lastSavedTimestamp: 171408e7
  }
});

// ../ap/packages/pieces/community/streak/src/index.ts
var streak = (0, import_pieces_framework14.createPiece)({
  displayName: "Streak",
  description: "CRM built into Gmail. Manage pipelines, boxes, tasks, comments, contacts and organizations from your flows.",
  auth: streakAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/streak.png",
  categories: [import_pieces_framework15.PieceCategory.SALES_AND_CRM],
  authors: ["sanket-a11y"],
  actions: [
    createBoxAction,
    updateBoxAction,
    getBoxAction,
    findBoxAction,
    createStageAction,
    createTaskAction,
    createCommentAction,
    createContactAction,
    createOrganizationAction,
    getCurrentUserAction,
    (0, import_pieces_common15.createCustomApiCallAction)({
      baseUrl: () => STREAK_BASE_URL,
      auth: streakAuth,
      authMapping: async (auth) => ({
        Authorization: `Basic ${Buffer.from(`${auth.secret_text}:`).toString("base64")}`
      })
    })
  ],
  triggers: [
    newBoxTrigger,
    updatedBoxTrigger,
    boxStageChangedTrigger,
    boxPipelineChangedTrigger,
    newCommentTrigger,
    newTaskTrigger,
    taskCompletedTrigger,
    taskDueTrigger,
    newMeetingTrigger,
    newContactTrigger,
    updatedContactTrigger
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  streak
});
