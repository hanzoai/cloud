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

// ../ap/packages/pieces/community/teamwork/src/index.ts
var index_exports = {};
__export(index_exports, {
  teamwork: () => teamwork
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework27 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/teamwork/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), teamworkAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: `
	Enter your Teamwork username (email address) and password.
	`,
  props: {
    username: import_pieces_framework.Property.ShortText({
      displayName: "Username",
      description: "Your Teamwork email address.",
      required: !0
    }),
    password: import_pieces_framework.PieceAuth.SecretText({
      displayName: "Password",
      required: !0
    }),
    subdomain: import_pieces_framework.Property.ShortText({
      displayName: "Subdomain",
      description: "Your Teamwork site subdomain (e.g., mycompany for mycompany.teamwork.com)",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `https://${auth.subdomain}.teamwork.com/projects.json`,
        authentication: {
          type: import_pieces_common.AuthenticationType.BASIC,
          username: auth.username,
          password: auth.password
        }
      }), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid username, password, or subdomain."
      };
    }
  },
  required: !0
});

// ../ap/packages/pieces/community/teamwork/src/lib/actions/create-project.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/teamwork/src/lib/common/client.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), MAX_RETRIES = 5, INITIAL_DELAY_MS = 500;
function buildUrl(base, path, query) {
  let url = new URL(path.startsWith("http") ? path : `${base}${path}`);
  return query && Object.entries(query).filter(([, v]) => v !== void 0).forEach(([k, v]) => url.searchParams.set(k, String(v))), url.toString();
}
async function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
async function teamworkRequest(auth, options) {
  var _a, _b, _c;
  let base = `https://${auth.props.subdomain}.teamwork.com`, url = buildUrl(base, options.path, options.query), attempt = 0, delay = INITIAL_DELAY_MS, retrying = !0;
  for (; retrying; )
    try {
      let res = await import_pieces_common2.httpClient.sendRequest({
        method: options.method,
        url,
        headers: { "Content-Type": "application/json" },
        body: options.body,
        authentication: {
          type: import_pieces_common2.AuthenticationType.BASIC,
          username: auth.props.username,
          password: auth.props.password
        }
      });
      return normalizeResponse(res.body);
    } catch (e) {
      let status = (_a = e == null ? void 0 : e.response) == null ? void 0 : _a.status;
      if (status && (status === 429 || status >= 500 && status < 600) && attempt < MAX_RETRIES) {
        let retryAfter = Number((_c = (_b = e == null ? void 0 : e.response) == null ? void 0 : _b.headers) == null ? void 0 : _c["retry-after"]);
        await sleep(Number.isFinite(retryAfter) ? retryAfter * 1e3 : delay), attempt++, delay = Math.min(delay * 2, 8e3);
      } else
        throw retrying = !1, e;
    }
  throw new Error("Teamwork request failed after multiple retries.");
}
function normalizeResponse(data) {
  var _a, _b, _c, _d, _e, _f, _g;
  if (!data || typeof data != "object")
    return { success: !0, data };
  let id = data.id || data.todoItemId || ((_a = data.task) == null ? void 0 : _a.id) || ((_b = data.company) == null ? void 0 : _b.id) || ((_c = data.project) == null ? void 0 : _c.id) || ((_d = data.message) == null ? void 0 : _d.id) || ((_e = data.file) == null ? void 0 : _e.id), url = data.link || data.url || ((_f = data == null ? void 0 : data.task) == null ? void 0 : _f.link) || ((_g = data == null ? void 0 : data.project) == null ? void 0 : _g.link);
  return { success: !0, id, url, data };
}

// ../ap/packages/pieces/community/teamwork/src/lib/actions/create-project.ts
var createProject = (0, import_pieces_framework2.createAction)({
  name: "create_project",
  displayName: "Create Project",
  description: "Create a new project (name, description, belongs to company, dates, etc.).",
  audience: "both",
  aiMetadata: { description: "Creates a new Teamwork project under a company. Use to spin up a project workspace before adding task lists, tasks, or milestones; requires a name and a company to associate it with, and accepts optional category, tags, dates, owner, and custom fields. Not idempotent \u2014 each call creates a separate project even with identical input.", idempotent: !1 },
  auth: teamworkAuth,
  props: {
    name: import_pieces_framework2.Property.ShortText({
      displayName: "Name",
      description: "The name of the project.",
      required: !0
    }),
    description: import_pieces_framework2.Property.LongText({
      displayName: "Description",
      description: "A description for the project.",
      required: !1
    }),
    companyId: import_pieces_framework2.Property.Dropdown({
      auth: teamworkAuth,
      displayName: "Company",
      description: "The company to associate the project with.",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common3.HttpMethod.GET,
          path: "/companies.json"
        })).data.companies.map((c) => ({
          label: c.name,
          value: c.id
        }))
      } : {
        disabled: !0,
        placeholder: "Please authenticate first.",
        options: []
      }
    }),
    "category-id": import_pieces_framework2.Property.Dropdown({
      auth: teamworkAuth,
      displayName: "Category",
      description: "The category to assign the project to.",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common3.HttpMethod.GET,
          path: "/projects/api/v3/projectcategories.json"
        })).data.projectCategories.map((c) => ({
          label: c.name,
          value: c.id
        }))
      } : {
        disabled: !0,
        placeholder: "Please authenticate first.",
        options: []
      }
    }),
    tagIds: import_pieces_framework2.Property.MultiSelectDropdown({
      auth: teamworkAuth,
      displayName: "Tags",
      description: "Tags to associate with the project.",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common3.HttpMethod.GET,
          path: "/tags.json"
        })).data.tags.map((t) => ({
          label: t.name,
          value: t.id
        }))
      } : {
        disabled: !0,
        placeholder: "Please authenticate first.",
        options: []
      }
    }),
    "start-date": import_pieces_framework2.Property.DateTime({
      displayName: "Start Date",
      description: "The start date of the project.",
      required: !1
    }),
    "end-date": import_pieces_framework2.Property.DateTime({
      displayName: "End Date",
      description: "The end date of the project.",
      required: !1
    }),
    projectOwnerId: import_pieces_framework2.Property.Dropdown({
      auth: teamworkAuth,
      displayName: "Project Owner",
      description: "The user to assign as the project owner.",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common3.HttpMethod.GET,
          path: "/people.json"
        })).data.people.map((p) => ({
          label: `${p["first-name"]} ${p["last-name"]}`,
          value: p.id
        }))
      } : {
        disabled: !0,
        placeholder: "Please authenticate first.",
        options: []
      }
    }),
    customFields: import_pieces_framework2.Property.DynamicProperties({
      displayName: "Custom Fields",
      description: "Custom fields for this project.",
      required: !1,
      refreshers: [],
      auth: teamworkAuth,
      props: async ({ auth }) => {
        var _a;
        if (!auth) return {};
        let fields = {}, res = await teamworkRequest(auth, {
          method: import_pieces_common3.HttpMethod.GET,
          path: "/projects/api/v3/customfields.json",
          query: {
            entities: "projects"
          }
        });
        if ((_a = res.data) != null && _a.customfields)
          for (let field of res.data.customfields)
            fields[field.id] = import_pieces_framework2.Property.ShortText({
              displayName: field.name,
              required: field.required
            });
        return fields;
      }
    })
  },
  async run({ auth, propsValue }) {
    var _a, _b;
    let formatDate = (date) => {
      if (date)
        return new Date(date).toISOString().slice(0, 10).replace(/-/g, "");
    }, customFields = Object.entries((_a = propsValue.customFields) != null ? _a : {}).map(
      ([customFieldId, value]) => ({ customFieldId: parseInt(customFieldId), value })
    ), body = {
      project: {
        name: propsValue.name,
        description: propsValue.description,
        companyId: propsValue.companyId,
        "category-id": propsValue["category-id"],
        tagIds: (_b = propsValue.tagIds) == null ? void 0 : _b.join(","),
        "start-date": formatDate(propsValue["start-date"]),
        "end-date": formatDate(propsValue["end-date"]),
        projectOwnerId: propsValue.projectOwnerId,
        customFields: customFields.length > 0 ? customFields : void 0
      }
    };
    return await teamworkRequest(auth, {
      method: import_pieces_common3.HttpMethod.POST,
      path: "/projects.json",
      body
    });
  }
});

// ../ap/packages/pieces/community/teamwork/src/lib/actions/create-task-list.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common");
var createTaskList = (0, import_pieces_framework3.createAction)({
  name: "create_task_list",
  displayName: "Create Task List",
  description: "Add a new task list under a project.",
  audience: "both",
  aiMetadata: { description: "Creates a new task list inside an existing Teamwork project to group related tasks. Use after a project exists and before creating tasks that should live in a specific list; requires the target project and a list name, and can set default priority/tags for tasks added later. Not idempotent \u2014 each call adds another list even with the same name.", idempotent: !1 },
  auth: teamworkAuth,
  props: {
    projectId: import_pieces_framework3.Property.Dropdown({
      auth: teamworkAuth,
      displayName: "Project",
      description: "The project to create the task list in.",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common4.HttpMethod.GET,
          path: "/projects.json"
        })).data.projects.map((p) => ({
          label: p.name,
          value: p.id
        }))
      } : {
        disabled: !0,
        placeholder: "Please authenticate first.",
        options: []
      }
    }),
    name: import_pieces_framework3.Property.ShortText({
      displayName: "Name",
      description: "The name of the task list.",
      required: !0
    }),
    description: import_pieces_framework3.Property.LongText({
      displayName: "Description",
      description: "A description for the task list.",
      required: !1
    }),
    private: import_pieces_framework3.Property.Checkbox({
      displayName: "Private",
      description: "Set to true to make the task list private.",
      required: !1
    }),
    priority: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Default Task Priority",
      description: "The default priority for new tasks in this list.",
      required: !1,
      options: {
        options: [
          { label: "None", value: "" },
          { label: "Low", value: "low" },
          { label: "Medium", value: "medium" },
          { label: "High", value: "high" }
        ]
      }
    }),
    tags: import_pieces_framework3.Property.MultiSelectDropdown({
      auth: teamworkAuth,
      displayName: "Default Task Tags",
      description: "Default tags for new tasks in this list.",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common4.HttpMethod.GET,
          path: "/tags.json"
        })).data.tags.map((t) => ({
          label: t.name,
          value: t.id
        }))
      } : {
        disabled: !0,
        placeholder: "Please authenticate first.",
        options: []
      }
    })
  },
  async run({ auth, propsValue }) {
    var _a;
    let body = {
      "todo-list": {
        name: propsValue.name,
        description: propsValue.description,
        private: propsValue.private,
        "new-task-defaults": {
          priority: propsValue.priority,
          tags: (_a = propsValue.tags) == null ? void 0 : _a.map((tagId) => ({ id: tagId }))
        }
      }
    };
    return await teamworkRequest(auth, {
      method: import_pieces_common4.HttpMethod.POST,
      path: `/projects/${propsValue.projectId}/tasklists.json`,
      body
    });
  }
});

// ../ap/packages/pieces/community/teamwork/src/lib/actions/create-task.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common");
var createTask = (0, import_pieces_framework4.createAction)({
  name: "create_task",
  displayName: "Create Task",
  description: "Add a new task under a project with description, due date, assignee, tags, etc.",
  audience: "both",
  aiMetadata: { description: "Creates a task within a project and task list in Teamwork. Use to add an actionable item with content, optional due/start dates, assignees, priority, tags, custom fields, and an optional file attachment (uploaded via presigned URL before the task is created). Requires the target project and task list. Not idempotent \u2014 each call creates a new task.", idempotent: !1 },
  auth: teamworkAuth,
  props: {
    projectId: import_pieces_framework4.Property.Dropdown({
      auth: teamworkAuth,
      displayName: "Project",
      description: "The project to create the task in.",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common5.HttpMethod.GET,
          path: "/projects.json"
        })).data.projects.map((p) => ({
          label: p.name,
          value: p.id
        }))
      } : {
        disabled: !0,
        placeholder: "Please authenticate first.",
        options: []
      }
    }),
    tasklistId: import_pieces_framework4.Property.Dropdown({
      auth: teamworkAuth,
      displayName: "Task List",
      description: "The task list to add the task to.",
      required: !0,
      refreshers: ["projectId"],
      options: async ({ auth, projectId }) => !auth || !projectId ? {
        disabled: !0,
        placeholder: "Please select a project.",
        options: []
      } : {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common5.HttpMethod.GET,
          path: `/projects/${projectId}/tasklists.json`
        })).data.tasklists.map((tl) => ({
          label: tl.name,
          value: tl.id
        }))
      }
    }),
    content: import_pieces_framework4.Property.ShortText({
      displayName: "Content",
      description: "The content of the task.",
      required: !0
    }),
    "responsible-party-id": import_pieces_framework4.Property.MultiSelectDropdown({
      auth: teamworkAuth,
      displayName: "Responsible Parties",
      description: "The users responsible for the task.",
      required: !1,
      refreshers: ["projectId"],
      options: async ({ auth, projectId }) => !auth || !projectId ? {
        disabled: !0,
        placeholder: "Please select a project.",
        options: []
      } : {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common5.HttpMethod.GET,
          path: `/projects/${projectId}/people.json`
        })).data.people.map((p) => ({
          label: `${p["first-name"]} ${p["last-name"]}`,
          value: p.id
        }))
      }
    }),
    "start-date": import_pieces_framework4.Property.DateTime({
      displayName: "Start Date",
      description: "The start date of the task.",
      required: !1
    }),
    "due-date": import_pieces_framework4.Property.DateTime({
      displayName: "Due Date",
      description: "The due date of the task.",
      required: !1
    }),
    description: import_pieces_framework4.Property.LongText({
      displayName: "Description",
      description: "A description for the task.",
      required: !1
    }),
    priority: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Priority",
      description: "The priority of the task.",
      required: !1,
      options: {
        options: [
          { label: "None", value: "" },
          { label: "Low", value: "low" },
          { label: "Medium", value: "medium" },
          { label: "High", value: "high" }
        ]
      }
    }),
    tagIds: import_pieces_framework4.Property.MultiSelectDropdown({
      auth: teamworkAuth,
      displayName: "Tags",
      description: "Tags to associate with the task.",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common5.HttpMethod.GET,
          path: "/tags.json"
        })).data.tags.map((t) => ({
          label: t.name,
          value: t.id
        }))
      } : {
        disabled: !0,
        placeholder: "Please authenticate first.",
        options: []
      }
    }),
    attachment: import_pieces_framework4.Property.File({
      displayName: "Attachment",
      description: "A file to attach to the task.",
      required: !1
    }),
    customFields: import_pieces_framework4.Property.DynamicProperties({
      auth: teamworkAuth,
      displayName: "Custom Fields",
      description: "Custom fields for this task.",
      required: !1,
      refreshers: [],
      props: async ({ auth }) => {
        var _a;
        if (!auth) return {};
        let fields = {}, res = await teamworkRequest(auth, {
          method: import_pieces_common5.HttpMethod.GET,
          path: "/projects/api/v3/customfields.json",
          query: {
            entities: "tasks"
          }
        });
        if ((_a = res.data) != null && _a.customfields)
          for (let field of res.data.customfields)
            fields[field.id] = import_pieces_framework4.Property.ShortText({
              displayName: field.name,
              required: field.required
            });
        return fields;
      }
    })
  },
  async run({ auth, propsValue }) {
    var _a, _b, _c;
    let pendingFileAttachments;
    if (propsValue.attachment) {
      let presignedUrlRes = await teamworkRequest(auth, {
        method: import_pieces_common5.HttpMethod.GET,
        path: "/projects/api/v1/pendingfiles/presignedurl.json",
        query: {
          fileName: propsValue.attachment.filename,
          fileSize: propsValue.attachment.data.length.toString()
        }
      }), { ref, url } = presignedUrlRes.data;
      await import_pieces_common5.httpClient.sendRequest({
        method: import_pieces_common5.HttpMethod.PUT,
        url,
        body: propsValue.attachment.data,
        headers: {
          "X-Amz-Acl": "public-read",
          "Content-Length": String(propsValue.attachment.data.length)
        }
      }), pendingFileAttachments = ref;
    }
    let formatDate = (date) => {
      if (date)
        return new Date(date).toISOString().slice(0, 10).replace(/-/g, "");
    }, customFields = Object.entries((_a = propsValue.customFields) != null ? _a : {}).map(
      ([customFieldId, value]) => ({ customFieldId: parseInt(customFieldId), value })
    ), body = {
      "todo-item": {
        content: propsValue.content,
        tasklistId: propsValue.tasklistId,
        "responsible-party-id": (_b = propsValue["responsible-party-id"]) == null ? void 0 : _b.join(","),
        "start-date": formatDate(propsValue["start-date"]),
        "due-date": formatDate(propsValue["due-date"]),
        description: propsValue.description,
        priority: propsValue.priority,
        tagIds: (_c = propsValue.tagIds) == null ? void 0 : _c.join(","),
        pendingFileAttachments,
        customFields: customFields.length > 0 ? customFields : void 0
      }
    };
    return await teamworkRequest(auth, {
      method: import_pieces_common5.HttpMethod.POST,
      path: `/projects/${propsValue.projectId}/tasks.json`,
      body
    });
  }
});

// ../ap/packages/pieces/community/teamwork/src/lib/actions/mark-task-complete.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common"), markTaskComplete = (0, import_pieces_framework5.createAction)({
  name: "mark_task_complete",
  displayName: "Mark Task Complete",
  description: "Set a task\u2019s status to complete.",
  audience: "both",
  aiMetadata: { description: "Marks an existing Teamwork task as complete by its task ID. Use to close out a task once its work is done. Idempotent \u2014 re-running on an already-completed task leaves it completed with no additional effect.", idempotent: !0 },
  auth: teamworkAuth,
  props: {
    taskId: import_pieces_framework5.Property.Dropdown({
      auth: teamworkAuth,
      displayName: "Task",
      description: "The task to mark as complete.",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common6.HttpMethod.GET,
          path: "/tasks.json",
          query: {
            includeCompletedTasks: "false"
          }
        })).data["todo-items"].map((task) => ({
          label: task.content,
          value: task.id
        }))
      } : {
        disabled: !0,
        placeholder: "Please authenticate first.",
        options: []
      }
    })
  },
  async run({ auth, propsValue }) {
    return await teamworkRequest(auth, {
      method: import_pieces_common6.HttpMethod.PUT,
      path: `/tasks/${propsValue.taskId}/complete.json`
    });
  }
});

// ../ap/packages/pieces/community/teamwork/src/lib/triggers/new-task.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common"), newTask = (0, import_pieces_framework6.createTrigger)({
  name: "new_task",
  displayName: "New Task",
  description: "Fires when a task is created.",
  aiMetadata: {
    description: "Fires when a task is created in Teamwork (TASK.CREATED webhook), optionally filtered to a single project. Each event represents one newly created task."
  },
  auth: teamworkAuth,
  props: {
    projectId: import_pieces_framework6.Property.Dropdown({
      auth: teamworkAuth,
      displayName: "Project",
      description: "The project to watch for new tasks. If not specified, all projects will be watched.",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common7.HttpMethod.GET,
          path: "/projects.json"
        })).data.projects.map((p) => ({
          label: p.name,
          value: p.id
        }))
      } : {
        disabled: !0,
        placeholder: "Please authenticate first.",
        options: []
      }
    })
  },
  type: import_pieces_framework6.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let res = await teamworkRequest(context.auth, {
      method: import_pieces_common7.HttpMethod.POST,
      path: "/webhooks.json",
      body: {
        webhook: {
          event: "TASK.CREATED",
          url: context.webhookUrl,
          version: "v3"
        }
      }
    });
    await context.store.put("webhookId", res.data.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get("webhookId");
    webhookId && await teamworkRequest(context.auth, {
      method: import_pieces_common7.HttpMethod.DELETE,
      path: `/webhooks/${webhookId}.json`
    });
  },
  async run(context) {
    var _a;
    let payload = context.payload.body;
    return context.propsValue.projectId ? String((_a = payload == null ? void 0 : payload.task) == null ? void 0 : _a.projectId) === context.propsValue.projectId ? [payload] : [] : [payload];
  },
  sampleData: {}
});

// ../ap/packages/pieces/community/teamwork/src/lib/actions/create-company.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var import_pieces_common8 = require("@activepieces/pieces-common"), createCompany = (0, import_pieces_framework7.createAction)({
  name: "create_company",
  displayName: "Create Company",
  description: "Create a new company in Teamwork.",
  audience: "both",
  aiMetadata: { description: "Creates a new company record in Teamwork. Use to register a client or organization before associating projects or people with it; requires a name and accepts optional address, contact, profile, and custom-field details. Not idempotent \u2014 each call creates a separate company even with the same name.", idempotent: !1 },
  auth: teamworkAuth,
  props: {
    name: import_pieces_framework7.Property.ShortText({ displayName: "Name", description: "Name of the company.", required: !0 }),
    website: import_pieces_framework7.Property.ShortText({ displayName: "Website", description: "Company's website address.", required: !1 }),
    addressOne: import_pieces_framework7.Property.ShortText({ displayName: "Address 1", description: "Primary address line.", required: !1 }),
    addressTwo: import_pieces_framework7.Property.ShortText({ displayName: "Address 2", description: "Secondary address line.", required: !1 }),
    city: import_pieces_framework7.Property.ShortText({ displayName: "City", description: "City name.", required: !1 }),
    state: import_pieces_framework7.Property.ShortText({ displayName: "State", description: "State or province.", required: !1 }),
    zip: import_pieces_framework7.Property.ShortText({ displayName: "Zip Code", description: "Postal or zip code.", required: !1 }),
    countrycode: import_pieces_framework7.Property.ShortText({
      displayName: "Country Code",
      description: "2-letter ISO country code (e.g., US, GB).",
      required: !1
    }),
    phone: import_pieces_framework7.Property.ShortText({ displayName: "Phone", description: "Primary phone number.", required: !1 }),
    fax: import_pieces_framework7.Property.ShortText({ displayName: "Fax", description: "Fax number.", required: !1 }),
    emailOne: import_pieces_framework7.Property.ShortText({ displayName: "Email 1", description: "Primary email address.", required: !1 }),
    emailTwo: import_pieces_framework7.Property.ShortText({ displayName: "Email 2", description: "Secondary email address.", required: !1 }),
    emailThree: import_pieces_framework7.Property.ShortText({ displayName: "Email 3", description: "Tertiary email address.", required: !1 }),
    profile: import_pieces_framework7.Property.LongText({ displayName: "Profile", description: "Public company profile or 'About Us' text.", required: !1 }),
    privateNotes: import_pieces_framework7.Property.LongText({ displayName: "Private Notes", description: "Notes visible only to internal users.", required: !1 }),
    customFields: import_pieces_framework7.Property.DynamicProperties({
      auth: teamworkAuth,
      displayName: "Custom Fields",
      description: "Custom fields for this company.",
      required: !1,
      refreshers: [],
      props: async ({ auth }) => {
        var _a;
        if (!auth) return {};
        let fields = {}, res = await teamworkRequest(auth, {
          method: import_pieces_common8.HttpMethod.GET,
          path: "/projects/api/v3/customfields.json",
          query: {
            entities: "companies"
          }
        });
        if ((_a = res.data) != null && _a.customfields)
          for (let field of res.data.customfields)
            fields[field.id] = import_pieces_framework7.Property.ShortText({
              displayName: field.name,
              required: field.required
            });
        return fields;
      }
    })
  },
  async run({ auth, propsValue }) {
    var _a;
    let customFields = Object.entries((_a = propsValue.customFields) != null ? _a : {}).map(
      ([customfieldId, value]) => ({ customfieldId: parseInt(customfieldId), value })
    ), companyData = {
      name: propsValue.name,
      website: propsValue.website,
      addressOne: propsValue.addressOne,
      addressTwo: propsValue.addressTwo,
      city: propsValue.city,
      state: propsValue.state,
      zip: propsValue.zip,
      countrycode: propsValue.countrycode,
      phone: propsValue.phone,
      fax: propsValue.fax,
      emailOne: propsValue.emailOne,
      emailTwo: propsValue.emailTwo,
      emailThree: propsValue.emailThree,
      profile: propsValue.profile,
      privateNotes: propsValue.privateNotes
    };
    customFields.length > 0 && (companyData.customFields = {
      Values: customFields
    });
    let body = { company: companyData };
    return await teamworkRequest(auth, {
      method: import_pieces_common8.HttpMethod.POST,
      path: "/projects/api/v3/companies.json",
      body
    });
  }
});

// ../ap/packages/pieces/community/teamwork/src/lib/actions/create-person.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var import_pieces_common9 = require("@activepieces/pieces-common"), createPerson = (0, import_pieces_framework8.createAction)({
  name: "create_person",
  displayName: "Create Person",
  description: "Create a new user/contact (name, email, invite options).",
  audience: "both",
  aiMetadata: { description: "Creates a new person in Teamwork as a standard user, collaborator, or contact (chosen via user type). Use to add a team member or external contact, optionally tied to a company and optionally sent an invite email. Requires first name, last name, email, and user type. Not idempotent \u2014 each call creates a new person.", idempotent: !1 },
  auth: teamworkAuth,
  props: {
    "first-name": import_pieces_framework8.Property.ShortText({
      displayName: "First Name",
      description: "The user's first name.",
      required: !0
    }),
    "last-name": import_pieces_framework8.Property.ShortText({
      displayName: "Last Name",
      description: "The user's last name.",
      required: !0
    }),
    "email-address": import_pieces_framework8.Property.ShortText({
      displayName: "Email",
      description: "The user's email address.",
      required: !0
    }),
    "user-type": import_pieces_framework8.Property.StaticDropdown({
      displayName: "User Type",
      description: "The type of user to create.",
      required: !0,
      options: {
        options: [
          { label: "Standard User", value: "account" },
          { label: "Collaborator", value: "collaborator" },
          { label: "Contact", value: "contact" }
        ]
      }
    }),
    "company-id": import_pieces_framework8.Property.Dropdown({
      auth: teamworkAuth,
      displayName: "Company",
      description: "The company to associate the user with.",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common9.HttpMethod.GET,
          path: "/companies.json"
        })).data.companies.map((c) => ({
          label: c.name,
          value: c.id
        }))
      } : {
        disabled: !0,
        placeholder: "Please authenticate first.",
        options: []
      }
    }),
    sendInvite: import_pieces_framework8.Property.Checkbox({
      displayName: "Send Invite",
      description: "Send an invitation email to the new user.",
      required: !1
    }),
    title: import_pieces_framework8.Property.ShortText({
      displayName: "Title",
      description: "The user's job title.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let body = {
      person: {
        "first-name": propsValue["first-name"],
        "last-name": propsValue["last-name"],
        "email-address": propsValue["email-address"],
        "user-type": propsValue["user-type"],
        "company-id": propsValue["company-id"],
        sendInvite: propsValue.sendInvite,
        title: propsValue.title
      }
    };
    return await teamworkRequest(auth, {
      method: import_pieces_common9.HttpMethod.POST,
      path: "/people.json",
      body
    });
  }
});

// ../ap/packages/pieces/community/teamwork/src/lib/actions/update-task.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var import_pieces_common10 = require("@activepieces/pieces-common");
async function getAllTasks(auth) {
  let allTasks = [], page = 1, moreTasks = !0;
  for (; moreTasks; ) {
    let res = await teamworkRequest(auth, {
      method: import_pieces_common10.HttpMethod.GET,
      path: "/tasks.json",
      query: {
        page: page.toString(),
        pageSize: "250"
      }
    });
    res.data["todo-items"] && res.data["todo-items"].length > 0 ? (allTasks = allTasks.concat(res.data["todo-items"]), page++) : moreTasks = !1;
  }
  return allTasks;
}
var updateTask = (0, import_pieces_framework9.createAction)({
  name: "update_task",
  displayName: "Update Task",
  description: "Modify a task\u2019s fields (due date, assignee, content, priority, etc.).",
  audience: "both",
  aiMetadata: { description: "Updates fields on an existing Teamwork task identified by task ID \u2014 only the fields you supply (content, description, assignees, start/due dates, priority, tags) are changed. Use to edit a task after it exists. Idempotent \u2014 repeating the same update yields the same final task state.", idempotent: !0 },
  auth: teamworkAuth,
  props: {
    taskId: import_pieces_framework9.Property.Dropdown({
      auth: teamworkAuth,
      displayName: "Task",
      description: "The task to update.",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await getAllTasks(auth)).map((task) => ({
          label: task.content,
          value: task.id
        }))
      } : {
        disabled: !0,
        placeholder: "Please authenticate first.",
        options: []
      }
    }),
    content: import_pieces_framework9.Property.ShortText({
      displayName: "Content",
      description: "The new content of the task.",
      required: !1
    }),
    description: import_pieces_framework9.Property.LongText({
      displayName: "Description",
      description: "The new description for the task.",
      required: !1
    }),
    "responsible-party-id": import_pieces_framework9.Property.MultiSelectDropdown({
      auth: teamworkAuth,
      displayName: "Responsible Parties",
      description: "The new users responsible for the task.",
      required: !1,
      refreshers: ["taskId"],
      options: async ({ auth, taskId }) => {
        if (!auth || !taskId)
          return {
            disabled: !0,
            placeholder: "Please select a task.",
            options: []
          };
        let projectId = (await teamworkRequest(auth, {
          method: import_pieces_common10.HttpMethod.GET,
          path: `/tasks/${taskId}.json`
        })).data["todo-item"]["project-id"];
        return projectId ? {
          disabled: !1,
          options: (await teamworkRequest(auth, {
            method: import_pieces_common10.HttpMethod.GET,
            path: `/projects/${projectId}/people.json`
          })).data.people.map((p) => ({
            label: `${p["first-name"]} ${p["last-name"]}`,
            value: p.id
          }))
        } : { disabled: !0, placeholder: "Could not determine project.", options: [] };
      }
    }),
    "start-date": import_pieces_framework9.Property.DateTime({
      displayName: "Start Date",
      description: "The new start date of the task.",
      required: !1
    }),
    "due-date": import_pieces_framework9.Property.DateTime({
      displayName: "Due Date",
      description: "The new due date of the task.",
      required: !1
    }),
    priority: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Priority",
      description: "The new priority of the task.",
      required: !1,
      options: {
        options: [
          { label: "None", value: "" },
          { label: "Low", value: "low" },
          { label: "Medium", value: "medium" },
          { label: "High", value: "high" }
        ]
      }
    }),
    tagIds: import_pieces_framework9.Property.MultiSelectDropdown({
      auth: teamworkAuth,
      displayName: "Tags",
      description: "New tags to associate with the task.",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common10.HttpMethod.GET,
          path: "/tags.json"
        })).data.tags.map((t) => ({
          label: t.name,
          value: t.id
        }))
      } : {
        disabled: !0,
        placeholder: "Please authenticate first.",
        options: []
      }
    })
  },
  async run({ auth, propsValue }) {
    let formatDate = (date) => {
      if (date)
        return new Date(date).toISOString().slice(0, 10).replace(/-/g, "");
    }, body = { "todo-item": {} }, item = body["todo-item"];
    return propsValue.content && (item.content = propsValue.content), propsValue.description && (item.description = propsValue.description), propsValue["responsible-party-id"] && (item["responsible-party-id"] = propsValue["responsible-party-id"].join(",")), propsValue["start-date"] && (item["start-date"] = formatDate(propsValue["start-date"])), propsValue["due-date"] && (item["due-date"] = formatDate(propsValue["due-date"])), propsValue.priority && (item.priority = propsValue.priority), propsValue.tagIds && (item.tagIds = propsValue.tagIds.join(",")), await teamworkRequest(auth, {
      method: import_pieces_common10.HttpMethod.PUT,
      path: `/tasks/${propsValue.taskId}.json`,
      body
    });
  }
});

// ../ap/packages/pieces/community/teamwork/src/lib/actions/create-task-comment.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var import_pieces_common11 = require("@activepieces/pieces-common"), createTaskComment = (0, import_pieces_framework10.createAction)({
  name: "create_task_comment",
  displayName: "Create Task Comment",
  description: "Leave a comment in a task.",
  audience: "both",
  aiMetadata: { description: "Posts a comment on an existing Teamwork task, optionally private and with notification (nobody, followers, or all project users) and an optional file attachment uploaded via presigned URL. Use to add a note or reply on a task. Requires the task ID and comment body. Not idempotent \u2014 each call adds another comment.", idempotent: !1 },
  auth: teamworkAuth,
  props: {
    taskId: import_pieces_framework10.Property.Dropdown({
      auth: teamworkAuth,
      displayName: "Task",
      description: "The task to add a comment to.",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common11.HttpMethod.GET,
          path: "/tasks.json"
        })).data["todo-items"].map((task) => ({
          label: task.content,
          value: task.id
        }))
      } : {
        disabled: !0,
        placeholder: "Please authenticate first.",
        options: []
      }
    }),
    body: import_pieces_framework10.Property.LongText({
      displayName: "Comment",
      description: "The content of the comment.",
      required: !0
    }),
    attachment: import_pieces_framework10.Property.File({
      displayName: "Attachment",
      description: "A file to attach to the comment.",
      required: !1
    }),
    notify: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Notify",
      description: "Who to notify about this comment.",
      required: !1,
      options: {
        options: [
          { label: "Nobody", value: "" },
          { label: "Followers", value: "true" },
          { label: "All Project Users", value: "all" }
        ]
      }
    }),
    isprivate: import_pieces_framework10.Property.Checkbox({
      displayName: "Private",
      description: "Set to true to make the comment private.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let pendingFileRef;
    if (propsValue.attachment) {
      let presignedUrlRes = await teamworkRequest(auth, {
        method: import_pieces_common11.HttpMethod.GET,
        path: "/projects/api/v1/pendingfiles/presignedurl.json",
        query: {
          fileName: propsValue.attachment.filename,
          fileSize: propsValue.attachment.data.length.toString()
        }
      }), { ref, url } = presignedUrlRes.data;
      await import_pieces_common11.httpClient.sendRequest({
        method: import_pieces_common11.HttpMethod.PUT,
        url,
        body: propsValue.attachment.data,
        headers: {
          "X-Amz-Acl": "public-read",
          "Content-Length": String(propsValue.attachment.data.length)
        }
      }), pendingFileRef = ref;
    }
    let body = {
      comment: {
        body: propsValue.body,
        notify: propsValue.notify,
        isprivate: propsValue.isprivate,
        pendingFileAttachments: pendingFileRef
      }
    };
    return await teamworkRequest(auth, {
      method: import_pieces_common11.HttpMethod.POST,
      path: `/tasks/${propsValue.taskId}/comments.json`,
      body
    });
  }
});

// ../ap/packages/pieces/community/teamwork/src/lib/actions/find-task.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var import_pieces_common12 = require("@activepieces/pieces-common"), findTask = (0, import_pieces_framework11.createAction)({
  name: "find_task",
  displayName: "Find Task",
  description: "Search for tasks.",
  audience: "both",
  aiMetadata: { description: "Searches Teamwork tasks by keyword, optionally scoped to a single project. Use to locate a task and obtain its ID before updating, commenting on, or completing it. Requires a search term. Idempotent \u2014 a read-only search with no side effects.", idempotent: !0 },
  auth: teamworkAuth,
  props: {
    searchTerm: import_pieces_framework11.Property.ShortText({
      displayName: "Search Term",
      description: "The keyword to search for.",
      required: !0
    }),
    projectId: import_pieces_framework11.Property.Dropdown({
      auth: teamworkAuth,
      displayName: "Project",
      description: "Limit the search to a specific project.",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common12.HttpMethod.GET,
          path: "/projects.json"
        })).data.projects.map((p) => ({
          label: p.name,
          value: p.id
        }))
      } : {
        disabled: !0,
        placeholder: "Please authenticate first.",
        options: []
      }
    })
  },
  async run({ auth, propsValue }) {
    return (await teamworkRequest(auth, {
      method: import_pieces_common12.HttpMethod.GET,
      path: "/search.json",
      query: {
        searchFor: "tasks",
        searchTerm: propsValue.searchTerm,
        projectId: propsValue.projectId
      }
    })).data;
  }
});

// ../ap/packages/pieces/community/teamwork/src/lib/actions/find-company.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var import_pieces_common13 = require("@activepieces/pieces-common"), findCompany = (0, import_pieces_framework12.createAction)({
  name: "find_company",
  displayName: "Find Company",
  description: "Search for a company by name or domain.",
  audience: "both",
  aiMetadata: { description: "Searches Teamwork companies by name or domain, optionally scoped to a single project. Use to look up a company and obtain its ID before associating projects or people with it. Requires a search term. Idempotent \u2014 a read-only search with no side effects.", idempotent: !0 },
  auth: teamworkAuth,
  props: {
    searchTerm: import_pieces_framework12.Property.ShortText({
      displayName: "Search Term",
      description: "The name or domain to search for.",
      required: !0
    }),
    projectId: import_pieces_framework12.Property.Dropdown({
      auth: teamworkAuth,
      displayName: "Project",
      description: "Limit the search to a specific project.",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common13.HttpMethod.GET,
          path: "/projects.json"
        })).data.projects.map((p) => ({
          label: p.name,
          value: p.id
        }))
      } : {
        disabled: !0,
        placeholder: "Please authenticate first.",
        options: []
      }
    })
  },
  async run({ auth, propsValue }) {
    return (await teamworkRequest(auth, {
      method: import_pieces_common13.HttpMethod.GET,
      path: "/search.json",
      query: {
        searchFor: "companies",
        searchTerm: propsValue.searchTerm,
        projectId: propsValue.projectId
      }
    })).data;
  }
});

// ../ap/packages/pieces/community/teamwork/src/lib/actions/find-milestone.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var import_pieces_common14 = require("@activepieces/pieces-common"), findMilestone = (0, import_pieces_framework13.createAction)({
  name: "find_milestone",
  displayName: "Find Milestone",
  description: "Find a milestone by name or due date in a project.",
  audience: "both",
  aiMetadata: { description: "Searches Teamwork milestones by name or due date, optionally scoped to a single project. Use to locate a milestone and obtain its details/ID. Requires a search term. Idempotent \u2014 a read-only search with no side effects.", idempotent: !0 },
  auth: teamworkAuth,
  props: {
    searchTerm: import_pieces_framework13.Property.ShortText({
      displayName: "Search Term",
      description: "The name or due date to search for.",
      required: !0
    }),
    projectId: import_pieces_framework13.Property.Dropdown({
      auth: teamworkAuth,
      displayName: "Project",
      description: "Limit the search to a specific project.",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common14.HttpMethod.GET,
          path: "/projects.json"
        })).data.projects.map((p) => ({
          label: p.name,
          value: p.id
        }))
      } : {
        disabled: !0,
        placeholder: "Please authenticate first.",
        options: []
      }
    })
  },
  async run({ auth, propsValue }) {
    return (await teamworkRequest(auth, {
      method: import_pieces_common14.HttpMethod.GET,
      path: "/search.json",
      query: {
        searchFor: "milestones",
        searchTerm: propsValue.searchTerm,
        projectId: propsValue.projectId
      }
    })).data;
  }
});

// ../ap/packages/pieces/community/teamwork/src/lib/actions/find-notebook-or-comment.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var import_pieces_common15 = require("@activepieces/pieces-common"), findNotebookOrComment = (0, import_pieces_framework14.createAction)({
  name: "find_notebook_or_comment",
  displayName: "Find Notebook / Notebook Comment",
  description: "Locate notebooks or note comments by search parameters.",
  audience: "both",
  aiMetadata: { description: "Searches Teamwork by keyword for either notebooks or notebook comments (selected via the search-for mode), optionally scoped to a single project. Use to locate a notebook or its comments and obtain their details. Requires choosing the item type and a search term. Idempotent \u2014 a read-only search with no side effects.", idempotent: !0 },
  auth: teamworkAuth,
  props: {
    searchFor: import_pieces_framework14.Property.StaticDropdown({
      displayName: "Search For",
      description: "The type of item to search for.",
      required: !0,
      options: {
        options: [
          { label: "Notebook", value: "notebooks" },
          { label: "Notebook Comment", value: "notebookComments" }
        ]
      }
    }),
    searchTerm: import_pieces_framework14.Property.ShortText({
      displayName: "Search Term",
      description: "The keyword to search for.",
      required: !0
    }),
    projectId: import_pieces_framework14.Property.Dropdown({
      auth: teamworkAuth,
      displayName: "Project",
      description: "Limit the search to a specific project.",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common15.HttpMethod.GET,
          path: "/projects.json"
        })).data.projects.map((p) => ({
          label: p.name,
          value: p.id
        }))
      } : {
        disabled: !0,
        placeholder: "Please authenticate first.",
        options: []
      }
    })
  },
  async run({ auth, propsValue }) {
    return (await teamworkRequest(auth, {
      method: import_pieces_common15.HttpMethod.GET,
      path: "/search.json",
      query: {
        searchFor: propsValue.searchFor,
        searchTerm: propsValue.searchTerm,
        projectId: propsValue.projectId
      }
    })).data;
  }
});

// ../ap/packages/pieces/community/teamwork/src/lib/triggers/new-person.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");
var import_pieces_common16 = require("@activepieces/pieces-common"), newPerson = (0, import_pieces_framework15.createTrigger)({
  name: "new_person",
  displayName: "New Person",
  description: "Fires when a new person (user/contact) is added.",
  aiMetadata: {
    description: "Fires when a new person \u2014 a user, collaborator, or contact \u2014 is added in Teamwork (USER.CREATED webhook). Each event represents one newly created person across the account."
  },
  auth: teamworkAuth,
  props: {},
  type: import_pieces_framework15.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let res = await teamworkRequest(context.auth, {
      method: import_pieces_common16.HttpMethod.POST,
      path: "/webhooks.json",
      body: {
        webhook: {
          event: "USER.CREATED",
          url: context.webhookUrl,
          version: "v3"
        }
      }
    });
    await context.store.put("webhookId", res.data.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get("webhookId");
    webhookId && await teamworkRequest(context.auth, {
      method: import_pieces_common16.HttpMethod.DELETE,
      path: `/webhooks/${webhookId}.json`
    });
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {}
});

// ../ap/packages/pieces/community/teamwork/src/lib/actions/create-time-entry-on-task.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework");
var import_pieces_common17 = require("@activepieces/pieces-common"), createTimeEntryOnTask = (0, import_pieces_framework16.createAction)({
  name: "create_time_entry_on_task",
  displayName: "Create Time Entry on Task",
  description: "Log time spent on a task with duration, description.",
  audience: "both",
  aiMetadata: { description: "Logs a time entry against an existing Teamwork task, recording hours and minutes for a given date (optionally a start time, description, and billable flag). Use to track work time on a task. Requires the task ID, date, hours, and minutes. Not idempotent \u2014 each call appends a new time log.", idempotent: !1 },
  auth: teamworkAuth,
  props: {
    taskId: import_pieces_framework16.Property.Dropdown({
      auth: teamworkAuth,
      displayName: "Task",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common17.HttpMethod.GET,
          path: "/tasks.json"
        })).data["todo-items"].map((task) => ({
          label: task.content,
          value: task.id
        }))
      } : {
        disabled: !0,
        placeholder: "Please authenticate first.",
        options: []
      }
    }),
    date: import_pieces_framework16.Property.DateTime({
      displayName: "Date",
      description: "Date of the time entry (yyyy-mm-dd)",
      required: !0
    }),
    time: import_pieces_framework16.Property.ShortText({
      displayName: "Time",
      description: "Time of the entry (hh:mm:ss)",
      required: !1
    }),
    hours: import_pieces_framework16.Property.Number({
      displayName: "Hours",
      required: !0
    }),
    minutes: import_pieces_framework16.Property.Number({
      displayName: "Minutes",
      description: "Duration in minutes",
      required: !0
    }),
    description: import_pieces_framework16.Property.LongText({
      displayName: "Description",
      required: !1
    }),
    isBillable: import_pieces_framework16.Property.Checkbox({
      displayName: "Is Billable",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let body = {
      timelog: {
        date: new Date(propsValue.date).toISOString().slice(0, 10),
        time: propsValue.time,
        description: propsValue.description,
        hasStartTime: !!propsValue.time,
        hours: propsValue.hours,
        minutes: propsValue.minutes,
        isBillable: propsValue.isBillable
      }
    };
    return await teamworkRequest(auth, {
      method: import_pieces_common17.HttpMethod.POST,
      path: `/projects/api/v3/tasks/${propsValue.taskId}/time.json`,
      body
    });
  }
});

// ../ap/packages/pieces/community/teamwork/src/lib/actions/create-expense.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework");
var import_pieces_common18 = require("@activepieces/pieces-common"), createExpense = (0, import_pieces_framework17.createAction)({
  name: "create_expense",
  displayName: "Create Expense",
  description: "Log new expense in a project with cost, description, date.",
  audience: "both",
  aiMetadata: { description: "Records an expense against a Teamwork project with a name, cost, and date (and optional description). Use to log a project cost for billing or tracking. Requires the project, name, cost, and date. Not idempotent \u2014 each call creates a new expense.", idempotent: !1 },
  auth: teamworkAuth,
  props: {
    "project-id": import_pieces_framework17.Property.Dropdown({
      auth: teamworkAuth,
      displayName: "Project",
      description: "The project to log the expense against.",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common18.HttpMethod.GET,
          path: "/projects/api/v3/projects.json"
        })).data.projects.map((p) => ({
          label: p.name,
          value: p.id
        }))
      } : {
        disabled: !0,
        placeholder: "Please authenticate first.",
        options: []
      }
    }),
    name: import_pieces_framework17.Property.ShortText({
      displayName: "Name",
      description: "The name of the expense.",
      required: !0
    }),
    cost: import_pieces_framework17.Property.Number({
      displayName: "Cost",
      description: "The cost of the expense.",
      required: !0
    }),
    date: import_pieces_framework17.Property.DateTime({
      displayName: "Date",
      description: "The date of the expense.",
      required: !0
    }),
    description: import_pieces_framework17.Property.LongText({
      displayName: "Description",
      description: "A description for the expense.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let date = new Date(propsValue.date).toISOString().slice(0, 10).replace(/-/g, ""), body = {
      expense: {
        "project-id": propsValue["project-id"],
        name: propsValue.name,
        cost: String(propsValue.cost),
        date,
        description: propsValue.description
      }
    };
    return await teamworkRequest(auth, {
      method: import_pieces_common18.HttpMethod.POST,
      path: "/expenses.json",
      body
    });
  }
});

// ../ap/packages/pieces/community/teamwork/src/lib/actions/upload-file-to-project.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework");
var import_pieces_common19 = require("@activepieces/pieces-common"), uploadFileToProject = (0, import_pieces_framework18.createAction)({
  name: "upload_file_to_project",
  displayName: "Upload File to Project",
  description: "Upload a file to a Teamwork project.",
  audience: "both",
  aiMetadata: { description: "Uploads a file to a Teamwork project (via presigned URL, then finalized as a project file), optionally with a description, category, and private flag. Use to attach a document to a project. Requires the target project and the file. Not idempotent \u2014 each call uploads a new file entry.", idempotent: !1 },
  auth: teamworkAuth,
  props: {
    projectId: import_pieces_framework18.Property.Dropdown({
      auth: teamworkAuth,
      displayName: "Project",
      description: "The project to upload the file to.",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common19.HttpMethod.GET,
          path: "/projects/api/v3/projects.json"
        })).data.projects.map((p) => ({
          label: p.name,
          value: p.id
        }))
      } : {
        disabled: !0,
        placeholder: "Please authenticate first.",
        options: []
      }
    }),
    file: import_pieces_framework18.Property.File({
      displayName: "File",
      description: "The file to upload.",
      required: !0
    }),
    description: import_pieces_framework18.Property.LongText({
      displayName: "Description",
      description: "A description for the file.",
      required: !1
    }),
    categoryId: import_pieces_framework18.Property.Dropdown({
      auth: teamworkAuth,
      displayName: "Category",
      description: "The category to assign the file to.",
      required: !1,
      refreshers: ["projectId"],
      options: async ({ auth, projectId }) => !auth || !projectId ? {
        disabled: !0,
        placeholder: "Please select a project.",
        options: []
      } : {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common19.HttpMethod.GET,
          path: `/projects/api/v3/projects/${projectId}/filecategories.json`
        })).data.filecategories.map((c) => ({
          label: c.name,
          value: c.id
        }))
      }
    }),
    private: import_pieces_framework18.Property.Checkbox({
      displayName: "Private",
      description: "Set to true to make the file private.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let presignedUrlRes = await teamworkRequest(auth, {
      method: import_pieces_common19.HttpMethod.GET,
      path: "/projects/api/v1/pendingfiles/presignedurl.json",
      query: {
        fileName: propsValue.file.filename,
        fileSize: propsValue.file.data.length.toString()
      }
    }), { ref, url } = presignedUrlRes.data;
    await import_pieces_common19.httpClient.sendRequest({
      method: import_pieces_common19.HttpMethod.PUT,
      url,
      body: propsValue.file.data,
      headers: {
        "X-Amz-Acl": "public-read",
        "Content-Length": String(propsValue.file.data.length)
      }
    });
    let body = {
      file: {
        pendingFileRef: ref,
        description: propsValue.description,
        "category-id": propsValue.categoryId,
        private: propsValue.private ? 1 : 0
      }
    };
    return await teamworkRequest(auth, {
      method: import_pieces_common19.HttpMethod.POST,
      path: `/projects/${propsValue.projectId}/files.json`,
      body
    });
  }
});

// ../ap/packages/pieces/community/teamwork/src/lib/actions/create-message-reply.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework");
var import_pieces_common20 = require("@activepieces/pieces-common"), createMessageReply = (0, import_pieces_framework19.createAction)({
  name: "create_message_reply",
  displayName: "Create Message Reply",
  description: "Post a reply in a message thread.",
  audience: "both",
  aiMetadata: { description: "Posts a reply to an existing Teamwork message thread, optionally notifying all project users. Use to respond within a message/discussion. Requires the message ID and reply body. Not idempotent \u2014 each call adds another reply.", idempotent: !1 },
  auth: teamworkAuth,
  props: {
    messageId: import_pieces_framework19.Property.Dropdown({
      auth: teamworkAuth,
      displayName: "Message",
      description: "The message to reply to.",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common20.HttpMethod.GET,
          path: "/posts.json"
        })).data.posts.map((p) => ({
          label: p.title,
          value: p.id
        }))
      } : {
        disabled: !0,
        placeholder: "Please authenticate first.",
        options: []
      }
    }),
    body: import_pieces_framework19.Property.LongText({
      displayName: "Body",
      description: "The content of the reply.",
      required: !0
    }),
    notify: import_pieces_framework19.Property.Checkbox({
      displayName: "Notify All",
      description: "Notify all project users of this reply.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let body = {
      messagereply: {
        body: propsValue.body,
        notify: propsValue.notify ? "All" : void 0
      }
    };
    return await teamworkRequest(auth, {
      method: import_pieces_common20.HttpMethod.POST,
      path: `/messages/${propsValue.messageId}/messageReplies.json`,
      body
    });
  }
});

// ../ap/packages/pieces/community/teamwork/src/lib/actions/create-milestone.ts
var import_pieces_framework20 = require("@activepieces/pieces-framework");
var import_pieces_common21 = require("@activepieces/pieces-common"), createMilestone = (0, import_pieces_framework20.createAction)({
  name: "create_milestone",
  displayName: "Create Milestone",
  description: "Add a milestone with due date, description, responsible user, etc.",
  audience: "both",
  aiMetadata: { description: "Creates a milestone in a Teamwork project with a title, deadline, and responsible parties (optionally a description, notify flag, and private flag). Use to set a dated project goal. Requires the project, title, deadline, and at least one responsible party. Not idempotent \u2014 each call creates a new milestone.", idempotent: !1 },
  auth: teamworkAuth,
  props: {
    projectId: import_pieces_framework20.Property.Dropdown({
      auth: teamworkAuth,
      displayName: "Project",
      description: "The project to create the milestone in.",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common21.HttpMethod.GET,
          path: "/projects.json"
        })).data.projects.map((p) => ({
          label: p.name,
          value: p.id
        }))
      } : {
        disabled: !0,
        placeholder: "Please authenticate first.",
        options: []
      }
    }),
    title: import_pieces_framework20.Property.ShortText({
      displayName: "Title",
      description: "The title of the milestone.",
      required: !0
    }),
    deadline: import_pieces_framework20.Property.DateTime({
      displayName: "Deadline",
      description: "The due date of the milestone.",
      required: !0
    }),
    "responsible-party-ids": import_pieces_framework20.Property.MultiSelectDropdown({
      auth: teamworkAuth,
      displayName: "Responsible Parties",
      description: "The users responsible for the milestone.",
      required: !0,
      refreshers: ["projectId"],
      options: async ({ auth, projectId }) => !auth || !projectId ? {
        disabled: !0,
        placeholder: "Please select a project.",
        options: []
      } : {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common21.HttpMethod.GET,
          path: `/projects/${projectId}/people.json`
        })).data.people.map((p) => ({
          label: `${p["first-name"]} ${p["last-name"]}`,
          value: p.id
        }))
      }
    }),
    description: import_pieces_framework20.Property.LongText({
      displayName: "Description",
      description: "A description for the milestone.",
      required: !1
    }),
    notify: import_pieces_framework20.Property.Checkbox({
      displayName: "Notify",
      description: "Notify responsible parties about the milestone.",
      required: !1
    }),
    private: import_pieces_framework20.Property.Checkbox({
      displayName: "Private",
      description: "Set to true to make the milestone private.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let deadline = new Date(propsValue.deadline).toISOString().slice(0, 10).replace(/-/g, ""), body = {
      milestone: {
        title: propsValue.title,
        deadline,
        "responsible-party-ids": propsValue["responsible-party-ids"].join(","),
        description: propsValue.description,
        notify: propsValue.notify,
        private: propsValue.private
      }
    };
    return await teamworkRequest(auth, {
      method: import_pieces_common21.HttpMethod.POST,
      path: `/projects/${propsValue.projectId}/milestones.json`,
      body
    });
  }
});

// ../ap/packages/pieces/community/teamwork/src/lib/actions/add-people-to-project.ts
var import_pieces_framework21 = require("@activepieces/pieces-framework");
var import_pieces_common22 = require("@activepieces/pieces-common"), addPeopleToProject = (0, import_pieces_framework21.createAction)({
  name: "add_people_to_project",
  displayName: "Add People to Project",
  description: "Add existing users to a project.",
  audience: "both",
  aiMetadata: { description: "Adds existing Teamwork users to a project as members. Use to grant people access to a project after both the project and the users exist. Requires the target project and a list of user IDs. Idempotent \u2014 re-adding users already on the project leaves the membership unchanged.", idempotent: !0 },
  auth: teamworkAuth,
  props: {
    projectId: import_pieces_framework21.Property.Dropdown({
      auth: teamworkAuth,
      displayName: "Project",
      description: "The project to add people to.",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common22.HttpMethod.GET,
          path: "/projects.json"
        })).data.projects.map((p) => ({
          label: p.name,
          value: p.id
        }))
      } : {
        disabled: !0,
        placeholder: "Please authenticate first.",
        options: []
      }
    }),
    userIdList: import_pieces_framework21.Property.MultiSelectDropdown({
      auth: teamworkAuth,
      displayName: "Users",
      description: "The users to add to the project.",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common22.HttpMethod.GET,
          path: "/people.json"
        })).data.people.map((p) => ({
          label: `${p["first-name"]} ${p["last-name"]}`,
          value: p.id
        }))
      } : {
        disabled: !0,
        placeholder: "Please authenticate first.",
        options: []
      }
    })
  },
  async run({ auth, propsValue }) {
    let body = {
      add: {
        userIdList: propsValue.userIdList.join(",")
      }
    };
    return await teamworkRequest(auth, {
      method: import_pieces_common22.HttpMethod.POST,
      path: `/projects/${propsValue.projectId}/people.json`,
      body
    });
  }
});

// ../ap/packages/pieces/community/teamwork/src/lib/triggers/new-comment.ts
var import_pieces_framework22 = require("@activepieces/pieces-framework");
var import_pieces_common23 = require("@activepieces/pieces-common"), newComment = (0, import_pieces_framework22.createTrigger)({
  name: "new_comment",
  displayName: "New Comment",
  description: "Fires when a new comment is posted.",
  aiMetadata: {
    description: "Fires when a comment is posted in Teamwork (COMMENT.CREATED webhook), optionally filtered to a single project. Each event represents one newly posted comment."
  },
  auth: teamworkAuth,
  props: {
    projectId: import_pieces_framework22.Property.Dropdown({
      auth: teamworkAuth,
      displayName: "Project",
      description: "The project to watch for new comments. If not specified, all projects will be watched.",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common23.HttpMethod.GET,
          path: "/projects.json"
        })).data.projects.map((p) => ({
          label: p.name,
          value: p.id
        }))
      } : {
        disabled: !0,
        placeholder: "Please authenticate first.",
        options: []
      }
    })
  },
  type: import_pieces_framework22.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let res = await teamworkRequest(context.auth, {
      method: import_pieces_common23.HttpMethod.POST,
      path: "/webhooks.json",
      body: {
        webhook: {
          event: "COMMENT.CREATED",
          url: context.webhookUrl,
          version: "v3"
        }
      }
    });
    await context.store.put("webhookId", res.data.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get("webhookId");
    webhookId && await teamworkRequest(context.auth, {
      method: import_pieces_common23.HttpMethod.DELETE,
      path: `/webhooks/${webhookId}.json`
    });
  },
  async run(context) {
    var _a;
    let payload = context.payload.body;
    return context.propsValue.projectId ? String((_a = payload == null ? void 0 : payload.comment) == null ? void 0 : _a.projectId) === context.propsValue.projectId ? [payload] : [] : [payload];
  },
  sampleData: {}
});

// ../ap/packages/pieces/community/teamwork/src/lib/triggers/new-message.ts
var import_pieces_framework23 = require("@activepieces/pieces-framework");
var import_pieces_common24 = require("@activepieces/pieces-common"), newMessage = (0, import_pieces_framework23.createTrigger)({
  name: "new_message",
  displayName: "New Message",
  description: "Fires when a new message is posted (in project messages or discussions).",
  aiMetadata: {
    description: "Fires when a message is posted in Teamwork project messages or discussions (MESSAGE.CREATED webhook), optionally filtered to a single project. Each event represents one newly posted message."
  },
  auth: teamworkAuth,
  props: {
    projectId: import_pieces_framework23.Property.Dropdown({
      auth: teamworkAuth,
      displayName: "Project",
      description: "The project to watch for new messages. If not specified, all projects will be watched.",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common24.HttpMethod.GET,
          path: "/projects.json"
        })).data.projects.map((p) => ({
          label: p.name,
          value: p.id
        }))
      } : {
        disabled: !0,
        placeholder: "Please authenticate first.",
        options: []
      }
    })
  },
  type: import_pieces_framework23.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let res = await teamworkRequest(context.auth, {
      method: import_pieces_common24.HttpMethod.POST,
      path: "/webhooks.json",
      body: {
        webhook: {
          event: "MESSAGE.CREATED",
          url: context.webhookUrl,
          version: "v3"
        }
      }
    });
    await context.store.put("webhookId", res.data.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get("webhookId");
    webhookId && await teamworkRequest(context.auth, {
      method: import_pieces_common24.HttpMethod.DELETE,
      path: `/webhooks/${webhookId}.json`
    });
  },
  async run(context) {
    var _a;
    let payload = context.payload.body;
    return context.propsValue.projectId ? String((_a = payload == null ? void 0 : payload.message) == null ? void 0 : _a.projectId) === context.propsValue.projectId ? [payload] : [] : [payload];
  },
  sampleData: {}
});

// ../ap/packages/pieces/community/teamwork/src/lib/triggers/new-file.ts
var import_pieces_framework24 = require("@activepieces/pieces-framework");
var import_pieces_common25 = require("@activepieces/pieces-common"), newFile = (0, import_pieces_framework24.createTrigger)({
  name: "new_file",
  displayName: "New File",
  description: "Fires when a new file is added.",
  aiMetadata: {
    description: "Fires when a file is added in Teamwork (FILE.CREATED webhook), optionally filtered to a single project. Each event represents one newly uploaded file."
  },
  auth: teamworkAuth,
  props: {
    projectId: import_pieces_framework24.Property.Dropdown({
      auth: teamworkAuth,
      displayName: "Project",
      description: "The project to watch for new files. If not specified, all projects will be watched.",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common25.HttpMethod.GET,
          path: "/projects.json"
        })).data.projects.map((p) => ({
          label: p.name,
          value: p.id
        }))
      } : {
        disabled: !0,
        placeholder: "Please authenticate first.",
        options: []
      }
    })
  },
  type: import_pieces_framework24.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let res = await teamworkRequest(context.auth, {
      method: import_pieces_common25.HttpMethod.POST,
      path: "/webhooks.json",
      body: {
        webhook: {
          event: "FILE.CREATED",
          url: context.webhookUrl,
          version: "v3"
        }
      }
    });
    await context.store.put("webhookId", res.data.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get("webhookId");
    webhookId && await teamworkRequest(context.auth, {
      method: import_pieces_common25.HttpMethod.DELETE,
      path: `/webhooks/${webhookId}.json`
    });
  },
  async run(context) {
    var _a;
    let payload = context.payload.body;
    return context.propsValue.projectId ? String((_a = payload == null ? void 0 : payload.file) == null ? void 0 : _a.projectId) === context.propsValue.projectId ? [payload] : [] : [payload];
  },
  sampleData: {}
});

// ../ap/packages/pieces/community/teamwork/src/lib/triggers/new-expense.ts
var import_pieces_framework25 = require("@activepieces/pieces-framework");
var import_pieces_common26 = require("@activepieces/pieces-common"), newExpense = (0, import_pieces_framework25.createTrigger)({
  name: "new_expense",
  displayName: "New Expense",
  description: "Fires when a new expense entry is added.",
  aiMetadata: {
    description: "Fires when an expense entry is added in Teamwork (EXPENSE.CREATED webhook), optionally filtered to a single project. Each event represents one newly logged expense."
  },
  auth: teamworkAuth,
  props: {
    projectId: import_pieces_framework25.Property.Dropdown({
      auth: teamworkAuth,
      displayName: "Project",
      description: "The project to watch for new expenses. If not specified, all projects will be watched.",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common26.HttpMethod.GET,
          path: "/projects.json"
        })).data.projects.map((p) => ({
          label: p.name,
          value: p.id
        }))
      } : {
        disabled: !0,
        placeholder: "Please authenticate first.",
        options: []
      }
    })
  },
  type: import_pieces_framework25.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let res = await teamworkRequest(context.auth, {
      method: import_pieces_common26.HttpMethod.POST,
      path: "/webhooks.json",
      body: {
        webhook: {
          event: "EXPENSE.CREATED",
          url: context.webhookUrl,
          version: "v3"
        }
      }
    });
    await context.store.put("webhookId", res.data.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get("webhookId");
    webhookId && await teamworkRequest(context.auth, {
      method: import_pieces_common26.HttpMethod.DELETE,
      path: `/webhooks/${webhookId}.json`
    });
  },
  async run(context) {
    var _a;
    let payload = context.payload.body;
    return context.propsValue.projectId ? String((_a = payload == null ? void 0 : payload.expense) == null ? void 0 : _a.projectId) === context.propsValue.projectId ? [payload] : [] : [payload];
  },
  sampleData: {}
});

// ../ap/packages/pieces/community/teamwork/src/lib/triggers/new-invoice.ts
var import_pieces_framework26 = require("@activepieces/pieces-framework");
var import_pieces_common27 = require("@activepieces/pieces-common"), newInvoice = (0, import_pieces_framework26.createTrigger)({
  name: "new_invoice",
  displayName: "New Invoice",
  description: "Fires when a new invoice is created.",
  aiMetadata: {
    description: "Fires when an invoice is created in Teamwork (INVOICE.CREATED webhook), optionally filtered to a single project. Each event represents one newly created invoice."
  },
  auth: teamworkAuth,
  props: {
    projectId: import_pieces_framework26.Property.Dropdown({
      auth: teamworkAuth,
      displayName: "Project",
      description: "The project to watch for new invoices. If not specified, all projects will be watched.",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        disabled: !1,
        options: (await teamworkRequest(auth, {
          method: import_pieces_common27.HttpMethod.GET,
          path: "/projects.json"
        })).data.projects.map((p) => ({
          label: p.name,
          value: p.id
        }))
      } : {
        disabled: !0,
        placeholder: "Please authenticate first.",
        options: []
      }
    })
  },
  type: import_pieces_framework26.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let res = await teamworkRequest(context.auth, {
      method: import_pieces_common27.HttpMethod.POST,
      path: "/webhooks.json",
      body: {
        webhook: {
          event: "INVOICE.CREATED",
          url: context.webhookUrl,
          version: "v3"
        }
      }
    });
    await context.store.put("webhookId", res.data.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get("webhookId");
    webhookId && await teamworkRequest(context.auth, {
      method: import_pieces_common27.HttpMethod.DELETE,
      path: `/webhooks/${webhookId}.json`
    });
  },
  async run(context) {
    var _a;
    let payload = context.payload.body;
    return context.propsValue.projectId ? String((_a = payload == null ? void 0 : payload.invoice) == null ? void 0 : _a.projectId) === context.propsValue.projectId ? [payload] : [] : [payload];
  },
  sampleData: {}
});

// ../ap/packages/pieces/community/teamwork/src/index.ts
var import_pieces_framework28 = require("@activepieces/pieces-framework"), teamwork = (0, import_pieces_framework27.createPiece)({
  displayName: "Teamwork",
  description: "Teamwork is a work and project management tool that helps teams improve collaboration, visibility, and accountability.",
  auth: teamworkAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/teamwork.png",
  authors: ["sparkybug", "onyedikachi-david"],
  categories: [import_pieces_framework28.PieceCategory.PRODUCTIVITY],
  actions: [
    createProject,
    createTaskList,
    createTask,
    markTaskComplete,
    createCompany,
    createPerson,
    updateTask,
    createTaskComment,
    createTimeEntryOnTask,
    createExpense,
    uploadFileToProject,
    createMessageReply,
    createMilestone,
    addPeopleToProject,
    findTask,
    findCompany,
    findMilestone,
    findNotebookOrComment
  ],
  triggers: [newTask, newPerson, newComment, newMessage, newFile, newExpense, newInvoice]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  teamwork
});
