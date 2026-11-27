"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
};
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

// ../ap/packages/pieces/community/youtrack/src/index.ts
var index_exports = {};
__export(index_exports, {
  youtrack: () => youtrack,
  youtrackAuth: () => youtrackAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework23 = require("@activepieces/pieces-framework"), import_pieces_common22 = require("@activepieces/pieces-common"), import_pieces_framework24 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/youtrack/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), youtrackAuth = import_pieces_framework.PieceAuth.CustomAuth({
  displayName: "YouTrack Connection",
  description: `Connect your YouTrack instance.

**How to get your permanent token:**
1. Log in to your YouTrack instance
2. Click your avatar -> **Profile** -> **Authentication** tab
3. Click **New permanent token**
4. Name it (e.g. "Activepieces") and click **Create**
5. **Copy the token immediately** - it is shown only once
6. Paste it below with your Instance URL

Your **Instance URL** is your browser address, e.g. https://example.youtrack.cloud`,
  required: !0,
  props: {
    baseUrl: import_pieces_framework.Property.ShortText({
      displayName: "Instance URL",
      description: "Your YouTrack URL (e.g. https://example.youtrack.cloud). Do NOT include /api.",
      required: !0
    }),
    apiToken: import_pieces_framework.PieceAuth.SecretText({
      displayName: "Permanent Token",
      description: 'From Profile -> Authentication -> Permanent Tokens. Starts with "perm:".',
      required: !0
    })
  }
});

// ../ap/packages/pieces/community/youtrack/src/lib/actions/create-issue.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/youtrack/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var API_PATH = "/api";
async function youtrackApiCall({
  baseUrl,
  token,
  method,
  path,
  body,
  queryParams,
  headers
}) {
  let cleanBase = baseUrl.replace(/\/+$/, "");
  return await import_pieces_common.httpClient.sendRequest({
    method,
    url: cleanBase + API_PATH + path,
    headers: __spreadValues({
      Accept: "application/json",
      Authorization: "Bearer " + token
    }, headers != null ? headers : {}),
    queryParams,
    body
  });
}
function requireYoutrackAuth(auth) {
  let props = auth == null ? void 0 : auth.props;
  if (!(props != null && props.baseUrl) || !(props != null && props.apiToken))
    throw new Error(
      "No YouTrack connection selected. Choose a connection on this step before running it."
    );
  return { baseUrl: props.baseUrl, apiToken: props.apiToken };
}
async function getYoutrackUserTimeZoneId(baseUrl, token) {
  var _a, _b, _c, _d, _e;
  try {
    return (_e = (_d = (_c = (_b = (_a = (await youtrackApiCall({
      baseUrl,
      token,
      method: import_pieces_common.HttpMethod.GET,
      path: "/users/me",
      queryParams: { fields: "profiles(general(timezone(id)))" }
    })).body) == null ? void 0 : _a.profiles) == null ? void 0 : _b.general) == null ? void 0 : _c.timezone) == null ? void 0 : _d.id) != null ? _e : null;
  } catch (e) {
    return null;
  }
}
function formatYoutrackDateTimeLiteral(epochMs, timeZoneId) {
  let date = new Date(epochMs);
  if (!timeZoneId)
    return date.toISOString().slice(0, 19);
  try {
    let parts = new Intl.DateTimeFormat("en-US", {
      timeZone: timeZoneId,
      hourCycle: "h23",
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit"
    }).formatToParts(date), get = (type) => {
      var _a, _b;
      return (_b = (_a = parts.find((p) => p.type === type)) == null ? void 0 : _a.value) != null ? _b : "00";
    };
    return `${get("year")}-${get("month")}-${get("day")}T${get("hour")}:${get("minute")}:${get("second")}`;
  } catch (e) {
    return date.toISOString().slice(0, 19);
  }
}
function flattenObject(obj, prefix = "") {
  let result = {};
  for (let [key, value] of Object.entries(obj)) {
    if (key === "$type") continue;
    let flatKey = prefix ? prefix + "_" + key : key;
    value == null ? result[flatKey] = null : Array.isArray(value) ? value.length === 0 ? result[flatKey] = null : typeof value[0] == "object" && value[0] !== null ? result[flatKey] = value.map((v) => typeof v == "object" && v !== null && "name" in v ? v.name : JSON.stringify(v)).join(", ") : result[flatKey] = value.join(", ") : typeof value == "object" ? Object.assign(
      result,
      flattenObject(value, flatKey)
    ) : result[flatKey] = value;
  }
  return result;
}
function normalizeCustomFields(value) {
  if (value != null) {
    if (Array.isArray(value))
      return value.length > 0 ? value : void 0;
    if (!(typeof value == "object" && Object.keys(value).length === 0))
      throw new Error(
        'Custom Fields (JSON) must be an array of field objects, for example: [{ "name": "Priority", "$type": "SingleEnumIssueCustomField", "value": { "name": "Critical" } }]'
      );
  }
}
function customFieldValue(value) {
  if (value == null)
    return null;
  if (Array.isArray(value))
    return value.length === 0 ? null : value.map((entry) => customFieldValue(entry)).filter((entry) => entry !== null).join(", ");
  if (typeof value == "object") {
    let obj = value;
    for (let key of ["name", "fullName", "login", "text", "presentation"]) {
      let candidate = obj[key];
      if (typeof candidate == "string" && candidate !== "")
        return candidate;
    }
    return typeof obj.minutes == "number" ? obj.minutes : JSON.stringify(value);
  }
  return value;
}
function flattenIssue(issue) {
  let _a = issue, { customFields } = _a, rest = __objRest(_a, ["customFields"]), result = flattenObject(rest), values = {};
  if (Array.isArray(customFields))
    for (let field of customFields) {
      let name = field.name;
      typeof name == "string" && name !== "" && (values[name] = customFieldValue(field.value));
    }
  return result.customFields = values, result;
}
function dropdownError(prefix, error) {
  let message = error instanceof Error ? error.message : String(error);
  return { disabled: !0, options: [], placeholder: prefix + ": " + message };
}
var projectDropdown = import_pieces_framework2.Property.Dropdown({
  displayName: "Project",
  description: "Select the YouTrack project to work with.",
  required: !0,
  auth: youtrackAuth,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth) return { disabled: !0, options: [], placeholder: "Please connect your account first" };
    let { baseUrl, apiToken } = auth.props;
    try {
      let response = await youtrackApiCall({
        baseUrl,
        token: apiToken,
        method: import_pieces_common.HttpMethod.GET,
        path: "/admin/projects",
        queryParams: { fields: "id,name,shortName" }
      });
      return !response.body || response.body.length === 0 ? { disabled: !1, options: [], placeholder: "No projects found. Create one in YouTrack first." } : {
        disabled: !1,
        options: response.body.map((p) => ({
          label: p.name + " (" + p.shortName + ")",
          value: p.id
        }))
      };
    } catch (error) {
      return dropdownError("Failed to load projects", error);
    }
  }
}), issueDropdown = import_pieces_framework2.Property.Dropdown({
  displayName: "Issue",
  description: "Select the issue to work with.",
  required: !0,
  auth: youtrackAuth,
  refreshers: ["project"],
  options: async ({ auth, project }) => {
    if (!auth) return { disabled: !0, options: [], placeholder: "Please connect your account first" };
    let { baseUrl, apiToken } = auth.props;
    try {
      let queryParams = {
        fields: "id,idReadable,summary",
        $top: project ? "100" : "50"
      };
      if (project) {
        let projectResponse = await youtrackApiCall({
          baseUrl,
          token: apiToken,
          method: import_pieces_common.HttpMethod.GET,
          path: "/admin/projects/" + project,
          queryParams: { fields: "shortName" }
        });
        queryParams.query = "project: {" + projectResponse.body.shortName + "}";
      }
      let response = await youtrackApiCall({
        baseUrl,
        token: apiToken,
        method: import_pieces_common.HttpMethod.GET,
        path: "/issues",
        queryParams
      });
      return !response.body || response.body.length === 0 ? {
        disabled: !1,
        options: [],
        placeholder: project ? "No issues found in this project." : "No issues found."
      } : {
        disabled: !1,
        options: response.body.map((i) => ({
          label: i.idReadable + ": " + i.summary,
          value: i.id
        }))
      };
    } catch (error) {
      return dropdownError("Failed to load issues", error);
    }
  }
}), tagDropdown = import_pieces_framework2.Property.Dropdown({
  displayName: "Tag",
  description: "Select a tag to apply to the issue.",
  required: !0,
  refreshers: [],
  auth: youtrackAuth,
  options: async ({ auth }) => {
    if (!auth) return { disabled: !0, options: [], placeholder: "Please connect your account first" };
    let { baseUrl, apiToken } = auth.props;
    try {
      let response = await youtrackApiCall({
        baseUrl,
        token: apiToken,
        method: import_pieces_common.HttpMethod.GET,
        path: "/tags",
        queryParams: { fields: "id,name" }
      });
      return !response.body || response.body.length === 0 ? { disabled: !1, options: [], placeholder: "No tags found. Create one in YouTrack first." } : { disabled: !1, options: response.body.map((t) => ({ label: t.name, value: t.id })) };
    } catch (error) {
      return dropdownError("Failed to load tags", error);
    }
  }
}), userDropdown = import_pieces_framework2.Property.Dropdown({
  displayName: "User",
  description: "Select a YouTrack user.",
  required: !0,
  refreshers: [],
  auth: youtrackAuth,
  options: async ({ auth }) => {
    if (!auth) return { disabled: !0, options: [], placeholder: "Please connect your account first" };
    let { baseUrl, apiToken } = auth.props;
    try {
      let response = await youtrackApiCall({
        baseUrl,
        token: apiToken,
        method: import_pieces_common.HttpMethod.GET,
        path: "/users",
        queryParams: { fields: "id,name,login", $top: "200" }
      });
      return !response.body || response.body.length === 0 ? { disabled: !1, options: [], placeholder: "No users found." } : {
        disabled: !1,
        options: response.body.map((u) => ({
          label: u.name + " (" + u.login + ")",
          value: u.id
        }))
      };
    } catch (error) {
      return dropdownError("Failed to load users", error);
    }
  }
}), ISSUE_FIELDS = "id,idReadable,summary,description,created,updated,resolved,project(id,name,shortName),reporter(id,name,login),commentsCount,votes,customFields(id,name,projectCustomField(field(id,name)),value(id,name,login,fullName,text,minutes,presentation,isResolved))";

// ../ap/packages/pieces/community/youtrack/src/lib/output-schemas.ts
var issueFields = [
  { key: "idReadable", label: "Issue ID" },
  { key: "summary", label: "Summary" },
  { key: "description", label: "Description" },
  { key: "id", label: "Internal ID" },
  { key: "project_id", label: "Project ID" },
  { key: "project_name", label: "Project Name" },
  { key: "project_shortName", label: "Project Short Name" },
  { key: "reporter_id", label: "Reporter ID" },
  { key: "reporter_name", label: "Reporter Name" },
  { key: "reporter_login", label: "Reporter Login" },
  {
    key: "customFields",
    label: "Custom Fields",
    description: "Custom field values keyed by field name (Priority, State, Assignee, Estimation, ...). The available keys depend on the project.",
    dynamicKey: !0
  },
  { key: "created", label: "Created", format: "datetime" },
  { key: "updated", label: "Updated", format: "datetime" },
  { key: "resolved", label: "Resolved", format: "datetime" },
  { key: "commentsCount", label: "Comments Count", format: "number" },
  { key: "votes", label: "Votes", format: "number" }
], commentFields = [
  { key: "id", label: "Comment ID" },
  { key: "text", label: "Text" },
  { key: "author_name", label: "Author Name" },
  { key: "author_login", label: "Author Login" },
  { key: "created", label: "Created", format: "datetime" }
], commentListFields = [
  ...commentFields,
  { key: "updated", label: "Updated", format: "datetime" }
], attachmentFields = [
  { key: "id", label: "Attachment ID" },
  { key: "name", label: "File Name" },
  { key: "content_type", label: "Content Type" },
  { key: "size_bytes", label: "Size", format: "filesize" },
  { key: "author_name", label: "Author Name" },
  { key: "author_login", label: "Author Login" },
  { key: "created", label: "Created", format: "datetime" }
], uploadedAttachmentFields = [
  { key: "id", label: "Attachment ID" },
  { key: "name", label: "File Name" }
], tagBaseFields = [
  { key: "id", label: "Tag ID" },
  { key: "name", label: "Name" },
  { key: "owner_id", label: "Owner ID" },
  { key: "owner_name", label: "Owner Name" }
], activityFields = [
  { key: "type", label: "Activity Type" },
  { key: "timestamp", label: "Timestamp", format: "datetime" },
  { key: "author_name", label: "Author Name" },
  { key: "author_login", label: "Author Login" },
  { key: "added_values", label: "Added Values" },
  { key: "removed_values", label: "Removed Values" },
  { key: "comment_text", label: "Comment Text" }
], createIssueActionOutputSchema = { fields: issueFields }, getIssueActionOutputSchema = { fields: issueFields }, updateIssueActionOutputSchema = { fields: issueFields }, searchIssuesActionOutputSchema = {
  itemLabel: "{idReadable}: {summary}",
  fields: [
    { key: "issues", label: "Issues", value: "", listItems: issueFields }
  ]
}, addCommentActionOutputSchema = { fields: commentFields }, listCommentsActionOutputSchema = {
  itemLabel: "{author_name}: {text}",
  fields: [
    { key: "comments", label: "Comments", value: "", listItems: commentListFields }
  ]
}, createTagActionOutputSchema = {
  fields: [
    ...tagBaseFields,
    { key: "untagOnResolve", label: "Remove When Resolved", format: "boolean" }
  ]
}, addTagToIssueActionOutputSchema = {
  fields: [
    { key: "id", label: "Tag ID" },
    { key: "name", label: "Name" }
  ]
}, listTagsActionOutputSchema = {
  itemLabel: "{name}",
  fields: [
    {
      key: "tags",
      label: "Tags",
      value: "",
      listItems: [
        ...tagBaseFields,
        { key: "visible_for_name", label: "Visible For" },
        { key: "updateable_by_name", label: "Updateable By" },
        { key: "untag_on_resolve", label: "Remove When Resolved", format: "boolean" }
      ]
    }
  ]
}, removeTagFromIssueActionOutputSchema = {
  fields: [{ key: "success", label: "Success", format: "boolean" }]
}, listAttachmentsActionOutputSchema = {
  itemLabel: "{name}",
  fields: [
    { key: "attachments", label: "Attachments", value: "", listItems: attachmentFields }
  ]
}, uploadAttachmentActionOutputSchema = {
  itemLabel: "{name}",
  fields: [
    {
      key: "attachments",
      label: "Uploaded Attachments",
      value: "",
      listItems: uploadedAttachmentFields
    }
  ]
}, downloadAttachmentActionOutputSchema = {
  fields: [
    { key: "file_name", label: "File Name" },
    { key: "attachment_id", label: "Attachment ID" },
    { key: "content_type", label: "Content Type" },
    { key: "size_bytes", label: "Size", format: "filesize" },
    { key: "base64_content", label: "Base64 Content" }
  ]
}, deleteAttachmentActionOutputSchema = {
  fields: [{ key: "success", label: "Success", format: "boolean" }]
}, getIssueHistoryActionOutputSchema = {
  itemLabel: "{author_name}: {type}",
  fields: [
    { key: "activities", label: "Activities", value: "", listItems: activityFields }
  ]
}, applyCommandActionOutputSchema = {
  fields: [
    { key: "success", label: "Success", format: "boolean" },
    { key: "command", label: "Command" }
  ]
}, linkIssuesActionOutputSchema = {
  fields: [
    { key: "success", label: "Success", format: "boolean" },
    { key: "link_type", label: "Link Type" },
    { key: "target_issue", label: "Target Issue" }
  ]
}, addUserToTeamActionOutputSchema = {
  fields: [
    { key: "id", label: "User ID" },
    { key: "name", label: "Name" },
    { key: "login", label: "Login" }
  ]
}, newIssueTriggerOutputSchema = { fields: issueFields }, updatedIssueTriggerOutputSchema = { fields: issueFields };

// ../ap/packages/pieces/community/youtrack/src/lib/actions/create-issue.ts
var createIssueAction = (0, import_pieces_framework3.createAction)({
  auth: youtrackAuth,
  name: "create_issue",
  outputSchema: createIssueActionOutputSchema,
  displayName: "Create Issue",
  description: "Creates a new issue in a YouTrack project.",
  audience: "both",
  aiMetadata: { description: "Create a new issue in a YouTrack project, requiring the target project ID and a summary; optionally set a description and custom fields (priority, assignee, etc.) via a JSON array. Use to file a bug, task, or request. Not idempotent: each call creates a distinct issue.", idempotent: !1 },
  props: {
    project: projectDropdown,
    summary: import_pieces_framework3.Property.ShortText({
      displayName: "Summary",
      description: "A short, one-line title for the issue.",
      required: !0
    }),
    description: import_pieces_framework3.Property.LongText({
      displayName: "Description",
      description: "Detailed description. Supports Markdown.",
      required: !1
    }),
    customFieldsJson: import_pieces_framework3.Property.Json({
      displayName: "Custom Fields (JSON)",
      description: `Set custom field values on creation. Example:
[
  { "name": "Priority", "$type": "SingleEnumIssueCustomField", "value": { "name": "Critical" } },
  { "name": "Assignee", "$type": "SingleUserIssueCustomField", "value": { "login": "jane.doe" } }
]`,
      required: !1
    })
  },
  async run(context) {
    let { baseUrl, apiToken } = context.auth.props, body = {
      project: { id: context.propsValue.project },
      summary: context.propsValue.summary
    };
    context.propsValue.description && (body.description = context.propsValue.description);
    let customFields = normalizeCustomFields(context.propsValue.customFieldsJson);
    customFields && (body.customFields = customFields);
    let response = await youtrackApiCall({
      baseUrl,
      token: apiToken,
      method: import_pieces_common2.HttpMethod.POST,
      path: "/issues",
      queryParams: { fields: ISSUE_FIELDS },
      body
    });
    return flattenIssue(response.body);
  }
});

// ../ap/packages/pieces/community/youtrack/src/lib/actions/get-issue.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var getIssueAction = (0, import_pieces_framework4.createAction)({
  auth: youtrackAuth,
  name: "get_issue",
  outputSchema: getIssueActionOutputSchema,
  displayName: "Get Issue",
  description: "Retrieves full details of an issue including all custom field values.",
  audience: "both",
  aiMetadata: { description: "Fetch the full details of a single issue by its ID, including summary, description, and all custom field values. Use to read the current state of a known issue. Read-only and idempotent.", idempotent: !0 },
  props: { issue: issueDropdown },
  async run(context) {
    let { baseUrl, apiToken } = context.auth.props, response = await youtrackApiCall({
      baseUrl,
      token: apiToken,
      method: import_pieces_common3.HttpMethod.GET,
      path: "/issues/" + context.propsValue.issue,
      queryParams: { fields: ISSUE_FIELDS }
    });
    return flattenIssue(response.body);
  }
});

// ../ap/packages/pieces/community/youtrack/src/lib/actions/search-issues.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var searchIssuesAction = (0, import_pieces_framework5.createAction)({
  auth: youtrackAuth,
  name: "search_issues",
  outputSchema: searchIssuesActionOutputSchema,
  displayName: "Search Issues",
  description: "Searches for issues using YouTrack query syntax. Returns flat rows for spreadsheets.",
  audience: "both",
  aiMetadata: { description: "Search issues using YouTrack query syntax (e.g. #unresolved, assignee: me, Priority: Critical), optionally scoped to one project. Leave both the project and query empty to list all accessible issues, or supply filters to narrow results. Use to find issues matching criteria before acting on them. Read-only and idempotent.", idempotent: !0 },
  props: {
    project: import_pieces_framework5.Property.Dropdown({
      auth: youtrackAuth,
      displayName: "Project",
      description: "Optional: filter by project. Leave empty to search across all projects.",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth) return { disabled: !0, options: [], placeholder: "Please connect your account first" };
        let { baseUrl, apiToken } = auth.props;
        try {
          let response = await youtrackApiCall({
            baseUrl,
            token: apiToken,
            method: import_pieces_common4.HttpMethod.GET,
            path: "/admin/projects",
            queryParams: { fields: "id,name,shortName" }
          });
          return {
            disabled: !1,
            options: [
              { label: "[All projects]", value: "" },
              ...response.body.map((p) => ({ label: p.name + " (" + p.shortName + ")", value: p.shortName }))
            ]
          };
        } catch (error) {
          return dropdownError("Failed to load projects", error);
        }
      }
    }),
    query: import_pieces_framework5.Property.LongText({
      displayName: "Search Query",
      description: `YouTrack search query. Examples:
- #unresolved \u2014 all unresolved issues
- assignee: me \u2014 assigned to you
- Priority: Critical \u2014 critical priority
- created: this week \u2014 created this week
- summary: login \u2014 issues mentioning "login"`,
      required: !1
    }),
    limit: import_pieces_framework5.Property.Number({
      displayName: "Limit",
      description: "Maximum issues to return. Default is 100.",
      required: !1,
      defaultValue: 100
    })
  },
  async run(context) {
    var _a;
    let { baseUrl, apiToken } = context.auth.props, limit = (_a = context.propsValue.limit) != null ? _a : 100, queryParts = [];
    context.propsValue.project && queryParts.push("project: {" + context.propsValue.project + "}"), context.propsValue.query && queryParts.push(context.propsValue.query);
    let queryParams = { fields: ISSUE_FIELDS, $top: String(limit) };
    return queryParts.length > 0 && (queryParams.query = queryParts.join(" ")), ((await youtrackApiCall({
      baseUrl,
      token: apiToken,
      method: import_pieces_common4.HttpMethod.GET,
      path: "/issues",
      queryParams
    })).body || []).map((item) => flattenIssue(item));
  }
});

// ../ap/packages/pieces/community/youtrack/src/lib/actions/update-issue.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var updateIssueAction = (0, import_pieces_framework6.createAction)({
  auth: youtrackAuth,
  name: "update_issue",
  outputSchema: updateIssueActionOutputSchema,
  displayName: "Update Issue",
  description: "Updates an existing issue - summary, description, or custom fields.",
  audience: "both",
  aiMetadata: { description: "Update an existing issue identified by its ID, changing summary, description, and/or custom fields (priority, assignee, etc.); only the fields you provide are modified. Use to edit issue content or set field values. Idempotent: re-applying the same values leaves the issue in the same state.", idempotent: !0 },
  props: {
    issue: issueDropdown,
    summary: import_pieces_framework6.Property.ShortText({ displayName: "Summary", description: "New summary. Leave empty to keep.", required: !1 }),
    description: import_pieces_framework6.Property.LongText({ displayName: "Description", description: "New description. Leave empty to keep.", required: !1 }),
    customFieldsJson: import_pieces_framework6.Property.Json({
      displayName: "Custom Fields (JSON)",
      description: `Set custom fields. Example:
[{ "name": "Priority", "$type": "SingleEnumIssueCustomField", "value": { "name": "Critical" } }]`,
      required: !1
    })
  },
  async run(context) {
    let { baseUrl, apiToken } = context.auth.props, body = {};
    context.propsValue.summary !== void 0 && context.propsValue.summary !== "" && (body.summary = context.propsValue.summary), context.propsValue.description !== void 0 && context.propsValue.description !== "" && (body.description = context.propsValue.description);
    let customFields = normalizeCustomFields(context.propsValue.customFieldsJson);
    customFields && (body.customFields = customFields);
    let response = await youtrackApiCall({
      baseUrl,
      token: apiToken,
      method: import_pieces_common5.HttpMethod.POST,
      path: "/issues/" + context.propsValue.issue,
      queryParams: { fields: ISSUE_FIELDS },
      body
    });
    return flattenIssue(response.body);
  }
});

// ../ap/packages/pieces/community/youtrack/src/lib/actions/apply-command.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var applyCommandAction = (0, import_pieces_framework7.createAction)({
  auth: youtrackAuth,
  name: "apply_command",
  outputSchema: applyCommandActionOutputSchema,
  displayName: "Apply Command",
  description: "Applies a YouTrack command to an issue (e.g. change state, assign, set sprint).",
  audience: "both",
  aiMetadata: { description: 'Apply a free-text YouTrack command to an issue (e.g. "State Fixed", "assignee jane.doe", "Priority Critical") to change its state, assignment, fields, or sprint in one step, optionally with a comment and silent (no-notification) mode. Use for workflow transitions expressed as commands. Mutating; idempotency depends on the command \u2014 setting a state or field is idempotent, while appending a comment is not.', idempotent: !1 },
  props: {
    issue: issueDropdown,
    command: import_pieces_framework7.Property.ShortText({
      displayName: "Command",
      description: "YouTrack command. Examples: State Fixed | assignee jane.doe | Priority Critical | for me Critical",
      required: !0
    }),
    comment: import_pieces_framework7.Property.LongText({ displayName: "Comment", description: "Optional comment.", required: !1 }),
    silent: import_pieces_framework7.Property.Checkbox({ displayName: "Apply silently?", description: "No notifications if enabled.", required: !1, defaultValue: !1 })
  },
  async run(context) {
    let { baseUrl, apiToken } = context.auth.props, body = { query: context.propsValue.command, issues: [{ id: context.propsValue.issue }] };
    return context.propsValue.comment && (body.comment = context.propsValue.comment), context.propsValue.silent && (body.silent = !0), await youtrackApiCall({
      baseUrl,
      token: apiToken,
      method: import_pieces_common6.HttpMethod.POST,
      path: "/commands",
      body
    }), { success: !0, command: context.propsValue.command };
  }
});

// ../ap/packages/pieces/community/youtrack/src/lib/actions/add-tag-to-issue.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var addTagToIssueAction = (0, import_pieces_framework8.createAction)({
  auth: youtrackAuth,
  name: "add_tag_to_issue",
  outputSchema: addTagToIssueActionOutputSchema,
  displayName: "Add Tag to Issue",
  description: "Adds an existing tag to an issue.",
  audience: "both",
  aiMetadata: { description: "Attach an existing tag to an issue, given both the issue ID and the tag ID; the tag must already exist (use Create Tag first if needed). Idempotent: adding a tag already present leaves the issue unchanged.", idempotent: !0 },
  props: { issue: issueDropdown, tag: tagDropdown },
  async run(context) {
    let { baseUrl, apiToken } = context.auth.props, response = await youtrackApiCall({
      baseUrl,
      token: apiToken,
      method: import_pieces_common7.HttpMethod.POST,
      path: "/issues/" + context.propsValue.issue + "/tags",
      queryParams: { fields: "id,name" },
      body: { id: context.propsValue.tag }
    });
    return flattenObject(response.body);
  }
});

// ../ap/packages/pieces/community/youtrack/src/lib/actions/remove-tag-from-issue.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var removeTagFromIssueAction = (0, import_pieces_framework9.createAction)({
  auth: youtrackAuth,
  name: "remove_tag_from_issue",
  outputSchema: removeTagFromIssueActionOutputSchema,
  displayName: "Remove Tag from Issue",
  description: "Removes a tag from an issue.",
  audience: "both",
  aiMetadata: { description: "Detach a tag from an issue, given both the issue ID and the tag ID. Idempotent: removing a tag that is not present has no further effect.", idempotent: !0 },
  props: {
    issue: issueDropdown,
    tag: tagDropdown
  },
  async run(context) {
    let { baseUrl, apiToken } = context.auth.props;
    return await youtrackApiCall({
      baseUrl,
      token: apiToken,
      method: import_pieces_common8.HttpMethod.DELETE,
      path: "/issues/" + context.propsValue.issue + "/tags/" + context.propsValue.tag
    }), { success: !0 };
  }
});

// ../ap/packages/pieces/community/youtrack/src/lib/actions/create-tag.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var createTagAction = (0, import_pieces_framework10.createAction)({
  auth: youtrackAuth,
  name: "create_tag",
  outputSchema: createTagActionOutputSchema,
  displayName: "Create Tag",
  description: "Creates a new tag in YouTrack.",
  audience: "both",
  aiMetadata: { description: "Create a new tag in YouTrack with a given name, optionally set to auto-remove when an issue is resolved. Use to define a label before applying it to issues. Not idempotent: repeating the call creates another tag with the same name.", idempotent: !1 },
  props: {
    name: import_pieces_framework10.Property.ShortText({ displayName: "Tag Name", description: 'Name like "Regression" or "To deploy".', required: !0 }),
    untagOnResolve: import_pieces_framework10.Property.Checkbox({ displayName: "Remove when resolved?", required: !1, defaultValue: !1 })
  },
  async run(context) {
    let { baseUrl, apiToken } = context.auth.props, body = { name: context.propsValue.name };
    context.propsValue.untagOnResolve !== void 0 && (body.untagOnResolve = context.propsValue.untagOnResolve);
    let response = await youtrackApiCall({
      baseUrl,
      token: apiToken,
      method: import_pieces_common9.HttpMethod.POST,
      path: "/tags",
      queryParams: { fields: "id,name,owner(id,name),untagOnResolve" },
      body
    });
    return flattenObject(response.body);
  }
});

// ../ap/packages/pieces/community/youtrack/src/lib/actions/list-tags.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var listTagsAction = (0, import_pieces_framework11.createAction)({
  auth: youtrackAuth,
  name: "list_tags",
  outputSchema: listTagsActionOutputSchema,
  displayName: "List Tags",
  description: "Lists all tags visible to the current user.",
  audience: "both",
  aiMetadata: { description: "List tags visible to the current user, optionally filtered by a name substring; leave the filter empty to return all tags. Use to discover tag IDs before adding or removing tags on an issue. Read-only and idempotent.", idempotent: !0 },
  props: {
    query: import_pieces_framework11.Property.ShortText({ displayName: "Filter by Name", description: "Filter tags by name. Leave empty for all.", required: !1 }),
    limit: import_pieces_framework11.Property.Number({ displayName: "Limit", description: "Max tags. Default 100.", required: !1, defaultValue: 100 })
  },
  async run(context) {
    var _a;
    let { baseUrl, apiToken } = context.auth.props, limit = (_a = context.propsValue.limit) != null ? _a : 100, queryParams = {
      fields: "id,name,owner(id,name),visibleFor(id,name),updateableBy(id,name),untagOnResolve",
      $top: String(limit)
    };
    return context.propsValue.query && (queryParams.query = context.propsValue.query), ((await youtrackApiCall({
      baseUrl,
      token: apiToken,
      method: import_pieces_common10.HttpMethod.GET,
      path: "/tags",
      queryParams
    })).body || []).map((tag) => {
      var _a2, _b, _c, _d, _e, _f, _g, _h, _i;
      return {
        id: tag.id,
        name: tag.name,
        owner_name: (_b = (_a2 = tag.owner) == null ? void 0 : _a2.name) != null ? _b : null,
        owner_id: (_d = (_c = tag.owner) == null ? void 0 : _c.id) != null ? _d : null,
        visible_for_name: (_f = (_e = tag.visibleFor) == null ? void 0 : _e.name) != null ? _f : "Only me",
        updateable_by_name: (_h = (_g = tag.updateableBy) == null ? void 0 : _g.name) != null ? _h : "Only me",
        untag_on_resolve: (_i = tag.untagOnResolve) != null ? _i : !1
      };
    });
  }
});

// ../ap/packages/pieces/community/youtrack/src/lib/actions/upload-attachment.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var uploadAttachmentAction = (0, import_pieces_framework12.createAction)({
  auth: youtrackAuth,
  name: "upload_attachment",
  outputSchema: uploadAttachmentActionOutputSchema,
  displayName: "Upload Attachment",
  description: "Uploads one or more files as attachments to an existing issue.",
  audience: "both",
  aiMetadata: { description: "Attach one or more files to an existing issue, given the issue ID plus a file name and Base64-encoded content for each file. Use to add screenshots, logs, or documents to an issue. Not idempotent: each call appends new attachments.", idempotent: !1 },
  props: {
    issue: issueDropdown,
    files: import_pieces_framework12.Property.Array({
      displayName: "Files",
      description: "Add files to attach. Each needs a file name and base64-encoded content.",
      required: !0,
      properties: {
        fileName: import_pieces_framework12.Property.ShortText({
          displayName: "File Name",
          description: 'The name the file will have in YouTrack (e.g. "screenshot.png").',
          required: !0
        }),
        fileContent: import_pieces_framework12.Property.File({
          displayName: "File Content (Base64)",
          description: "The file content as a Base64 string. Use the Text Helper piece to encode files.",
          required: !0
        })
      }
    })
  },
  async run(context) {
    let { baseUrl, apiToken } = context.auth.props, results = [];
    for (let file of context.propsValue.files) {
      let binary = Buffer.from(file.fileContent.base64, "base64"), boundary = "----ActivepiecesBoundary" + Date.now(), parts = [];
      parts.push(Buffer.from(
        "--" + boundary + `\r
Content-Disposition: form-data; name="upload"; filename="` + file.fileName.replace(/"/g, '\\"').replace(/\r/g, "").replace(/\n/g, "") + `"\r
Content-Type: application/octet-stream\r
\r
`,
        "utf-8"
      )), parts.push(binary), parts.push(Buffer.from(`\r
--` + boundary + `--\r
`, "utf-8"));
      let body = Buffer.concat(parts), response = await youtrackApiCall({
        baseUrl,
        token: apiToken,
        method: import_pieces_common11.HttpMethod.POST,
        path: "/issues/" + context.propsValue.issue + "/attachments",
        queryParams: { fields: "id,name" },
        headers: { "Content-Type": "multipart/form-data; boundary=" + boundary },
        body
      });
      results.push(...(response.body || []).map((a) => ({ id: a.id, name: a.name })));
    }
    return results;
  }
});

// ../ap/packages/pieces/community/youtrack/src/lib/actions/delete-attachment.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common");
var deleteAttachmentAction = (0, import_pieces_framework13.createAction)({
  auth: youtrackAuth,
  name: "delete_attachment",
  outputSchema: deleteAttachmentActionOutputSchema,
  displayName: "Delete Attachment",
  description: "Deletes an attachment from an issue.",
  audience: "both",
  aiMetadata: { description: 'Delete a single attachment from an issue, given the issue ID and the attachment database ID (e.g. "134-31", found via List Attachments or Get Issue). Idempotent: deleting an already-removed attachment has no further effect.', idempotent: !0 },
  props: {
    issue: issueDropdown,
    attachmentId: import_pieces_framework13.Property.ShortText({
      displayName: "Attachment ID",
      description: 'The database ID of the attachment (e.g. "134-31"). You can find attachment IDs by using the "Get Issue" action \u2014 look for the attachment IDs in the output.',
      required: !0
    })
  },
  async run(context) {
    let { baseUrl, apiToken } = context.auth.props;
    return await youtrackApiCall({
      baseUrl,
      token: apiToken,
      method: import_pieces_common12.HttpMethod.DELETE,
      path: "/issues/" + context.propsValue.issue + "/attachments/" + context.propsValue.attachmentId
    }), { success: !0 };
  }
});

// ../ap/packages/pieces/community/youtrack/src/lib/actions/download-attachment.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_common13 = require("@activepieces/pieces-common");
var downloadAttachmentAction = (0, import_pieces_framework14.createAction)({
  auth: youtrackAuth,
  name: "download_attachment",
  outputSchema: downloadAttachmentActionOutputSchema,
  displayName: "Download Attachment",
  description: 'Downloads an attachment from an issue and returns it as a Base64-encoded string. Use this with the "Upload to Drive" or "Send Email" actions in your flow.',
  audience: "both",
  aiMetadata: { description: 'Download a single attachment from an issue and return its content as Base64 plus metadata, given the issue ID and attachment database ID (e.g. "134-31", found via List Attachments). Use to retrieve file bytes for forwarding to another step. Read-only and idempotent.', idempotent: !0 },
  props: {
    issue: issueDropdown,
    attachmentId: import_pieces_framework14.Property.ShortText({
      displayName: "Attachment ID",
      description: 'The attachment database ID (e.g. "134-31"). Use "List Attachments" to find it.',
      required: !0
    })
  },
  async run(context) {
    let { baseUrl, apiToken } = context.auth.props, base = baseUrl.replace(/\/+$/, ""), meta = (await youtrackApiCall({
      baseUrl,
      token: apiToken,
      method: import_pieces_common13.HttpMethod.GET,
      path: "/issues/" + context.propsValue.issue + "/attachments/" + context.propsValue.attachmentId,
      queryParams: { fields: "id,name,url,size,mimeType" }
    })).body, resolvedUrl = meta.url ? /^https?:\/\//.test(meta.url) ? meta.url : base + meta.url : void 0, downloadUrl = resolvedUrl != null ? resolvedUrl : base + "/api/issues/" + context.propsValue.issue + "/attachments/" + context.propsValue.attachmentId + "/file", fileResponse = await fetch(downloadUrl, {
      method: import_pieces_common13.HttpMethod.GET,
      headers: { Authorization: "Bearer " + apiToken }
    });
    if (!fileResponse.ok) {
      let e = await fileResponse.json().catch(() => ({}));
      throw new Error("Failed to download attachment file: " + JSON.stringify(e));
    }
    let buffer = await fileResponse.arrayBuffer(), base64 = Buffer.from(buffer).toString("base64"), contentType = meta.mimeType || fileResponse.headers.get("content-type") || "application/octet-stream";
    return {
      attachment_id: context.propsValue.attachmentId,
      file_name: meta.name,
      content_type: contentType,
      base64_content: base64,
      size_bytes: buffer.byteLength
    };
  }
});

// ../ap/packages/pieces/community/youtrack/src/lib/actions/get-issue-history.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework"), import_pieces_common14 = require("@activepieces/pieces-common");
var getIssueHistoryAction = (0, import_pieces_framework15.createAction)({
  auth: youtrackAuth,
  name: "get_issue_history",
  outputSchema: getIssueHistoryActionOutputSchema,
  displayName: "Get Issue History",
  description: "Retrieves the full change history (activity log) for a specific issue.",
  audience: "both",
  aiMetadata: { description: "Retrieve the change history (activity log) for an issue, given its ID, filtered to the selected activity types (custom field changes and/or comments). Use to audit what changed on an issue and by whom. Read-only and idempotent.", idempotent: !0 },
  props: {
    issue: issueDropdown,
    categories: import_pieces_framework15.Property.StaticMultiSelectDropdown({
      displayName: "Activity Types",
      description: "Select which types of activities to include.",
      required: !0,
      defaultValue: ["CustomFieldCategory", "CommentsCategory"],
      options: {
        options: [
          { label: "Custom Field Changes", value: "CustomFieldCategory" },
          { label: "Comments", value: "CommentsCategory" }
        ]
      }
    })
  },
  async run(context) {
    let { baseUrl, apiToken } = context.auth.props, categories = context.propsValue.categories.join(",");
    return ((await youtrackApiCall({
      baseUrl,
      token: apiToken,
      method: import_pieces_common14.HttpMethod.GET,
      path: "/issues/" + context.propsValue.issue + "/activities",
      queryParams: {
        categories,
        fields: "author(name,login),timestamp,target(id,text),added(name),removed(name)"
      }
    })).body || []).map((item) => {
      var _a, _b, _c, _d, _e, _f;
      return {
        type: item.$type,
        timestamp: item.timestamp,
        author_name: (_b = (_a = item.author) == null ? void 0 : _a.name) != null ? _b : null,
        author_login: (_d = (_c = item.author) == null ? void 0 : _c.login) != null ? _d : null,
        added_values: Array.isArray(item.added) ? item.added.map((x) => {
          var _a2;
          return (_a2 = x.name) != null ? _a2 : JSON.stringify(x);
        }).join(", ") : null,
        removed_values: Array.isArray(item.removed) ? item.removed.map((x) => {
          var _a2;
          return (_a2 = x.name) != null ? _a2 : JSON.stringify(x);
        }).join(", ") : null,
        comment_text: (_f = (_e = item.target) == null ? void 0 : _e.text) != null ? _f : null
      };
    });
  }
});

// ../ap/packages/pieces/community/youtrack/src/lib/actions/add-comment.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework"), import_pieces_common15 = require("@activepieces/pieces-common");
var addCommentAction = (0, import_pieces_framework16.createAction)({
  auth: youtrackAuth,
  name: "add_comment",
  outputSchema: addCommentActionOutputSchema,
  displayName: "Add Comment",
  description: "Adds a comment to an issue. Supports Markdown formatting.",
  audience: "both",
  aiMetadata: { description: "Post a comment on an issue, given the issue ID and Markdown text; optionally restrict visibility to a specific group. Use to add a note or reply on an issue. Not idempotent: each call appends a new comment.", idempotent: !1 },
  props: {
    issue: issueDropdown,
    text: import_pieces_framework16.Property.LongText({
      displayName: "Comment Text",
      description: "The comment text. Use Markdown for formatting. Mention users with @username.",
      required: !0
    }),
    visibleToGroup: import_pieces_framework16.Property.Dropdown({
      auth: youtrackAuth,
      displayName: "Visible to Group",
      description: "Restrict comment visibility to a group. Leave empty for everyone.",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth) return { disabled: !0, options: [], placeholder: "Connect your account first" };
        let { baseUrl, apiToken } = auth.props;
        try {
          let response = await youtrackApiCall({
            baseUrl,
            token: apiToken,
            method: import_pieces_common15.HttpMethod.GET,
            path: "/groups",
            queryParams: { fields: "id,name" }
          });
          return { disabled: !1, options: [{ label: "[Visible to everyone]", value: "" }, ...response.body.map((g) => ({ label: g.name, value: g.id }))] };
        } catch (error) {
          return dropdownError("Failed to load groups", error);
        }
      }
    })
  },
  async run(context) {
    var _a, _b, _c, _d;
    let { baseUrl, apiToken } = context.auth.props, body = { text: context.propsValue.text };
    context.propsValue.visibleToGroup && (body.visibility = { $type: "LimitedVisibility", permittedGroups: [{ id: context.propsValue.visibleToGroup }] });
    let response = await youtrackApiCall({
      baseUrl,
      token: apiToken,
      method: import_pieces_common15.HttpMethod.POST,
      path: "/issues/" + context.propsValue.issue + "/comments",
      queryParams: { fields: "id,text,author(name,login),created" },
      body
    });
    return {
      id: response.body.id,
      text: response.body.text,
      author_name: (_b = (_a = response.body.author) == null ? void 0 : _a.name) != null ? _b : null,
      author_login: (_d = (_c = response.body.author) == null ? void 0 : _c.login) != null ? _d : null,
      created: response.body.created
    };
  }
});

// ../ap/packages/pieces/community/youtrack/src/lib/actions/list-comments.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework"), import_pieces_common16 = require("@activepieces/pieces-common");
var listCommentsAction = (0, import_pieces_framework17.createAction)({
  auth: youtrackAuth,
  name: "list_comments",
  outputSchema: listCommentsActionOutputSchema,
  displayName: "List Comments",
  description: "Lists all comments on an issue with author details and timestamps.",
  audience: "both",
  aiMetadata: { description: "List the comments on an issue, given its ID, with author and timestamp details (capped by an optional limit). Use to read the discussion on a known issue. Read-only and idempotent.", idempotent: !0 },
  props: {
    issue: issueDropdown,
    limit: import_pieces_framework17.Property.Number({ displayName: "Limit", description: "Max comments. Default 100.", required: !1, defaultValue: 100 })
  },
  async run(context) {
    var _a;
    let { baseUrl, apiToken } = context.auth.props, limit = (_a = context.propsValue.limit) != null ? _a : 100;
    return ((await youtrackApiCall({
      baseUrl,
      token: apiToken,
      method: import_pieces_common16.HttpMethod.GET,
      path: "/issues/" + context.propsValue.issue + "/comments",
      queryParams: { fields: "id,text,author(name,login),created,updated", $top: String(limit) }
    })).body || []).map((c) => {
      var _a2, _b, _c, _d;
      return {
        id: c.id,
        text: c.text,
        author_name: (_b = (_a2 = c.author) == null ? void 0 : _a2.name) != null ? _b : null,
        author_login: (_d = (_c = c.author) == null ? void 0 : _c.login) != null ? _d : null,
        created: c.created,
        updated: c.updated
      };
    });
  }
});

// ../ap/packages/pieces/community/youtrack/src/lib/actions/add-user-to-team.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework"), import_pieces_common17 = require("@activepieces/pieces-common");
var addUserToTeamAction = (0, import_pieces_framework18.createAction)({
  auth: youtrackAuth,
  name: "add_user_to_team",
  outputSchema: addUserToTeamActionOutputSchema,
  displayName: "Add User to Project Team",
  description: "Adds a user as a direct member of a project team, giving them access to the project.",
  audience: "both",
  aiMetadata: { description: "Add a user as a direct member of a project team, granting them access to the project; requires the project ID and the user ID. Idempotent: adding a user who is already a team member leaves membership unchanged.", idempotent: !0 },
  props: { project: projectDropdown, user: userDropdown },
  async run(context) {
    let { baseUrl, apiToken } = context.auth.props, response = await youtrackApiCall({
      baseUrl,
      token: apiToken,
      method: import_pieces_common17.HttpMethod.POST,
      path: "/admin/projects/" + context.propsValue.project + "/team/ownUsers",
      queryParams: { fields: "id,login,name" },
      body: { id: context.propsValue.user }
    });
    return flattenObject(response.body);
  }
});

// ../ap/packages/pieces/community/youtrack/src/lib/actions/link-issues.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework"), import_pieces_common18 = require("@activepieces/pieces-common");
var linkIssuesAction = (0, import_pieces_framework19.createAction)({
  auth: youtrackAuth,
  name: "link_issues",
  outputSchema: linkIssuesActionOutputSchema,
  displayName: "Link Issues",
  description: 'Creates a relationship between two issues (e.g. "relates to", "depends on", "is duplicated by").',
  audience: "both",
  aiMetadata: { description: 'Create a directional relationship between two issues, given the source issue ID, the readable target issue ID (e.g. "NP-92"), and a link type (relates to, depends on, duplicates, parent for, subtask of, etc.). Idempotent: re-creating an existing link of the same type leaves the relationship unchanged.', idempotent: !0 },
  props: {
    sourceIssue: issueDropdown,
    targetIssueId: import_pieces_framework19.Property.ShortText({
      displayName: "Target Issue ID",
      description: 'The readable ID of the issue to link to (e.g. "NP-92"). You can find this in the issue URL or header.',
      required: !0
    }),
    linkType: import_pieces_framework19.Property.StaticDropdown({
      displayName: "Link Type",
      description: "The type of relationship between the issues.",
      required: !0,
      defaultValue: "relates to",
      options: {
        options: [
          { label: "Relates to", value: "relates to" },
          { label: "Depends on", value: "depends on" },
          { label: "Is duplicated by", value: "is duplicated by" },
          { label: "Duplicates", value: "duplicates" },
          { label: "Parent for", value: "parent for" },
          { label: "Subtask of", value: "subtask of" }
        ]
      }
    })
  },
  async run(context) {
    let { baseUrl, apiToken } = context.auth.props, body = {
      query: context.propsValue.linkType + " " + context.propsValue.targetIssueId,
      issues: [{ id: context.propsValue.sourceIssue }]
    };
    return await youtrackApiCall({
      baseUrl,
      token: apiToken,
      method: import_pieces_common18.HttpMethod.POST,
      path: "/commands",
      body
    }), { success: !0, link_type: context.propsValue.linkType, target_issue: context.propsValue.targetIssueId };
  }
});

// ../ap/packages/pieces/community/youtrack/src/lib/actions/list-attachments.ts
var import_pieces_framework20 = require("@activepieces/pieces-framework"), import_pieces_common19 = require("@activepieces/pieces-common");
var listAttachmentsAction = (0, import_pieces_framework20.createAction)({
  auth: youtrackAuth,
  name: "list_attachments",
  outputSchema: listAttachmentsActionOutputSchema,
  displayName: "List Attachments",
  description: "Lists all attachments on a specific issue with metadata (name, size, type).",
  audience: "both",
  aiMetadata: { description: "List the attachments on an issue, given its ID, with metadata (name, size, content type, author) capped by an optional limit. Use to discover attachment IDs before downloading or deleting them. Read-only and idempotent.", idempotent: !0 },
  props: {
    issue: issueDropdown,
    limit: import_pieces_framework20.Property.Number({ displayName: "Limit", description: "Max attachments. Default 50.", required: !1, defaultValue: 50 })
  },
  async run(context) {
    var _a;
    let { baseUrl, apiToken } = context.auth.props, limit = (_a = context.propsValue.limit) != null ? _a : 50;
    return ((await youtrackApiCall({
      baseUrl,
      token: apiToken,
      method: import_pieces_common19.HttpMethod.GET,
      path: "/issues/" + context.propsValue.issue + "/attachments",
      queryParams: { fields: "id,name,mimeType,size,author(name,login),created", $top: String(limit) }
    })).body || []).map((att) => {
      var _a2, _b, _c, _d, _e;
      return {
        id: att.id,
        name: att.name,
        content_type: (_a2 = att.mimeType) != null ? _a2 : null,
        size_bytes: att.size,
        author_name: (_c = (_b = att.author) == null ? void 0 : _b.name) != null ? _c : null,
        author_login: (_e = (_d = att.author) == null ? void 0 : _d.login) != null ? _e : null,
        created: att.created
      };
    });
  }
});

// ../ap/packages/pieces/community/youtrack/src/lib/triggers/new-issue.ts
var import_pieces_framework21 = require("@activepieces/pieces-framework"), import_pieces_common20 = require("@activepieces/pieces-common");
var PAGE_SIZE = 50, MAX_PAGES = 10, polling = {
  strategy: import_pieces_common20.DedupeStrategy.TIMEBASED,
  items: async ({ auth, lastFetchEpochMS }) => {
    let { baseUrl, apiToken } = requireYoutrackAuth(auth), fetchPage = async (query2, skip) => (await youtrackApiCall({
      baseUrl,
      token: apiToken,
      method: import_pieces_common20.HttpMethod.GET,
      path: "/issues",
      queryParams: {
        fields: ISSUE_FIELDS,
        query: query2,
        $skip: String(skip),
        $top: String(PAGE_SIZE)
      }
    })).body || [], toEvents = (issues) => issues.map((issue) => ({
      epochMilliSeconds: issue.created || 0,
      data: flattenIssue(issue)
    }));
    if (lastFetchEpochMS <= 0)
      return toEvents(await fetchPage("sort by: created desc", 0));
    let timeZoneId = await getYoutrackUserTimeZoneId(baseUrl, apiToken), query = `created: ${formatYoutrackDateTimeLiteral(lastFetchEpochMS, timeZoneId)} .. * sort by: created asc`, collected = [];
    for (let page = 0; page < MAX_PAGES; page++) {
      let batch = await fetchPage(query, page * PAGE_SIZE);
      if (collected.push(...batch), batch.length < PAGE_SIZE) break;
    }
    return toEvents(collected);
  }
}, newIssueTrigger = (0, import_pieces_framework21.createTrigger)({
  auth: youtrackAuth,
  name: "new_issue",
  outputSchema: newIssueTriggerOutputSchema,
  displayName: "New Issue",
  description: "Triggers when a new issue is created in any project you can access.",
  aiMetadata: {
    description: "Fires when a new issue is created in any YouTrack project the connected account can access. Each event represents one newly created issue with its summary, project, reporter, and field values."
  },
  props: {},
  sampleData: {
    idReadable: "SP-42",
    summary: "Fix login page crash",
    description: "Users cannot sign in after the latest deploy.",
    id: "3-19",
    project_id: "0-0",
    project_name: "Sample Project",
    project_shortName: "SP",
    reporter_id: "2-1",
    reporter_name: "Jane Doe",
    reporter_login: "jane.doe",
    customFields: {
      Priority: "Critical",
      Type: "Bug",
      State: "To do",
      Assignee: null,
      Estimation: "2h"
    },
    created: 1644916724088,
    updated: 1644916724088,
    resolved: null,
    commentsCount: 0,
    votes: 0
  },
  type: import_pieces_framework21.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common20.pollingHelper.test(polling, context);
  },
  async onEnable(context) {
    await import_pieces_common20.pollingHelper.onEnable(polling, context);
  },
  async onDisable(context) {
    await import_pieces_common20.pollingHelper.onDisable(polling, context);
  },
  async run(context) {
    return await import_pieces_common20.pollingHelper.poll(polling, context);
  }
});

// ../ap/packages/pieces/community/youtrack/src/lib/triggers/updated-issue.ts
var import_pieces_framework22 = require("@activepieces/pieces-framework"), import_pieces_common21 = require("@activepieces/pieces-common");
var PAGE_SIZE2 = 50, MAX_PAGES2 = 10, CREATED_VS_UPDATED_EPSILON_MS = 2e3, polling2 = {
  strategy: import_pieces_common21.DedupeStrategy.TIMEBASED,
  items: async ({ auth, lastFetchEpochMS }) => {
    let { baseUrl, apiToken } = requireYoutrackAuth(auth), fetchPage = async (query2, skip) => (await youtrackApiCall({
      baseUrl,
      token: apiToken,
      method: import_pieces_common21.HttpMethod.GET,
      path: "/issues",
      queryParams: {
        fields: ISSUE_FIELDS,
        query: query2,
        $skip: String(skip),
        $top: String(PAGE_SIZE2)
      }
    })).body || [], toEvents = (issues) => issues.filter((issue) => {
      let created = issue.created, updated = issue.updated;
      return typeof created == "number" && typeof updated == "number" && updated - created > CREATED_VS_UPDATED_EPSILON_MS;
    }).map((issue) => ({
      epochMilliSeconds: issue.updated || 0,
      data: flattenIssue(issue)
    }));
    if (lastFetchEpochMS <= 0)
      return toEvents(await fetchPage("sort by: updated desc", 0));
    let timeZoneId = await getYoutrackUserTimeZoneId(baseUrl, apiToken), query = `updated: ${formatYoutrackDateTimeLiteral(lastFetchEpochMS, timeZoneId)} .. * sort by: updated asc`, collected = [];
    for (let page = 0; page < MAX_PAGES2; page++) {
      let batch = await fetchPage(query, page * PAGE_SIZE2);
      if (collected.push(...batch), batch.length < PAGE_SIZE2) break;
    }
    return toEvents(collected);
  }
}, updatedIssueTrigger = (0, import_pieces_framework22.createTrigger)({
  auth: youtrackAuth,
  name: "updated_issue",
  outputSchema: updatedIssueTriggerOutputSchema,
  displayName: "Updated Issue",
  description: "Triggers when an existing issue is modified (summary, description, custom fields, etc.).",
  aiMetadata: {
    description: "Fires when an existing issue is modified \u2014 its summary, description, custom fields, or other attributes change. Each event represents one updated issue (issues created within the same polling window are excluded). Use to react to changes on already-existing issues rather than brand-new ones."
  },
  props: {},
  sampleData: {
    idReadable: "SP-42",
    summary: "Fixed login page crash",
    description: "Users cannot sign in after the latest deploy.",
    id: "3-19",
    project_id: "0-0",
    project_name: "Sample Project",
    project_shortName: "SP",
    reporter_id: "2-1",
    reporter_name: "Jane Doe",
    reporter_login: "jane.doe",
    customFields: {
      Priority: "Critical",
      Type: "Bug",
      State: "Done",
      Assignee: "John Smith",
      Estimation: "2h"
    },
    created: 1644916724088,
    updated: 1648110830229,
    resolved: 1648110830229,
    commentsCount: 3,
    votes: 0
  },
  type: import_pieces_framework22.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common21.pollingHelper.test(polling2, context);
  },
  async onEnable(context) {
    await import_pieces_common21.pollingHelper.onEnable(polling2, context);
  },
  async onDisable(context) {
    await import_pieces_common21.pollingHelper.onDisable(polling2, context);
  },
  async run(context) {
    return await import_pieces_common21.pollingHelper.poll(polling2, context);
  }
});

// ../ap/packages/pieces/community/youtrack/src/index.ts
var youtrack = (0, import_pieces_framework23.createPiece)({
  displayName: "YouTrack",
  description: "JetBrains project management and issue tracking for agile teams.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/youtrack.png",
  categories: [import_pieces_framework24.PieceCategory.DEVELOPER_TOOLS],
  auth: youtrackAuth,
  authors: ["cumonvip1"],
  actions: [
    createIssueAction,
    getIssueAction,
    searchIssuesAction,
    updateIssueAction,
    applyCommandAction,
    addTagToIssueAction,
    removeTagFromIssueAction,
    createTagAction,
    listTagsAction,
    uploadAttachmentAction,
    deleteAttachmentAction,
    downloadAttachmentAction,
    getIssueHistoryAction,
    addCommentAction,
    listCommentsAction,
    addUserToTeamAction,
    linkIssuesAction,
    listAttachmentsAction,
    (0, import_pieces_common22.createCustomApiCallAction)({
      baseUrl: (auth) => {
        var _a;
        return ((_a = auth == null ? void 0 : auth.props.baseUrl) != null ? _a : "").replace(/\/+$/, "") + "/api";
      },
      auth: youtrackAuth,
      authMapping: async (auth) => ({ Authorization: "Bearer " + auth.props.apiToken })
    })
  ],
  triggers: [newIssueTrigger, updatedIssueTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  youtrack,
  youtrackAuth
});
