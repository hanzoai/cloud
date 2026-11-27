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

// ../ap/packages/pieces/community/taskade/src/index.ts
var index_exports = {};
__export(index_exports, {
  taskade: () => taskade
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/taskade/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), taskadeAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Personal Token",
  required: !0,
  description: `
	1. Navigate to https://taskade.com/settings/password and scroll down to Personal Access Tokens.
	2. Create your personal access token with any name.`
});

// ../ap/packages/pieces/community/taskade/src/lib/actions/create-task.action.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/taskade/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/taskade/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
var TaskadeAPIClient = class {
  constructor(personalToken) {
    __publicField(this, "personalToken", personalToken);
  }
  async makeRequest(method, resourceUri, query, body = void 0) {
    let baseUrl = "https://www.taskade.com/api/v1", qs = {};
    if (query)
      for (let [key, value] of Object.entries(query))
        value != null && (qs[key] = String(value));
    let request = {
      method,
      url: baseUrl + resourceUri,
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: this.personalToken
      },
      queryParams: qs,
      body
    };
    return (await import_pieces_common.httpClient.sendRequest(request)).body;
  }
  async listWorkspaces() {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, "/workspaces");
  }
  async listWorkspaceFolders(workspace_id) {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, `/workspaces/${workspace_id}/folders`);
  }
  async listProjects(folder_id) {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, `/folders/${folder_id}/projects`);
  }
  async createTask(projectId, params) {
    return await this.makeRequest(import_pieces_common.HttpMethod.POST, `/projects/${projectId}/tasks`, void 0, {
      tasks: [params]
    });
  }
  async createTaskDate(projectId, taskId, params) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.PUT,
      `/projects/${projectId}/tasks/${taskId}/date`,
      void 0,
      params
    );
  }
  async listTasks(projectId, params) {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, `/projects/${projectId}/tasks`, params);
  }
  async completeTask(projectId, taskId) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      `/projects/${projectId}/tasks/${taskId}/complete`,
      void 0,
      {}
    );
  }
  async deleteTask(projectId, taskId) {
    return await this.makeRequest(import_pieces_common.HttpMethod.DELETE, `/projects/${projectId}/tasks/${taskId}`);
  }
};

// ../ap/packages/pieces/community/taskade/src/lib/common/props.ts
var createEmptyOptions = (placeholder) => ({
  disabled: !0,
  options: [],
  placeholder
}), taskadeProps = {
  workspace_id: import_pieces_framework2.Property.Dropdown({
    auth: taskadeAuth,
    displayName: "Workspace",
    refreshers: [],
    required: !0,
    options: async ({ auth }) => {
      if (!auth)
        return createEmptyOptions("Please connect account first.");
      let response = await new TaskadeAPIClient(auth.secret_text).listWorkspaces(), options = [];
      for (let workspace of response.items)
        options.push({ label: workspace.name, value: workspace.id });
      return {
        disabled: !1,
        options
      };
    }
  }),
  folder_id: import_pieces_framework2.Property.Dropdown({
    auth: taskadeAuth,
    displayName: "Folder",
    refreshers: ["workspace_id"],
    required: !1,
    options: async ({ auth, workspace_id }) => {
      if (!auth)
        return createEmptyOptions("Please connect account first.");
      if (!workspace_id)
        return createEmptyOptions("Please select workspace.");
      let response = await new TaskadeAPIClient(auth.secret_text).listWorkspaceFolders(workspace_id), options = [];
      for (let folder of response.items)
        options.push({ label: folder.name, value: folder.id });
      return {
        disabled: !1,
        options
      };
    }
  }),
  project_id: import_pieces_framework2.Property.Dropdown({
    auth: taskadeAuth,
    displayName: "Project",
    refreshers: ["workspace_id", "folder_id"],
    required: !0,
    options: async ({ auth, workspace_id, folder_id }) => {
      if (!auth)
        return createEmptyOptions("Please connect account first.");
      if (!workspace_id)
        return createEmptyOptions("Please select workspace.");
      let workspaceId = workspace_id, folderId = folder_id != null ? folder_id : workspaceId, response = await new TaskadeAPIClient(auth.secret_text).listProjects(folderId), options = [];
      for (let project of response.items)
        options.push({ label: project.name, value: project.id });
      return {
        disabled: !1,
        options
      };
    }
  }),
  task_id: import_pieces_framework2.Property.Dropdown({
    auth: taskadeAuth,
    displayName: "Task",
    refreshers: ["project_id"],
    required: !0,
    options: async ({ auth, project_id }) => {
      if (!auth)
        return createEmptyOptions("Please connect account first.");
      if (!project_id)
        return createEmptyOptions("Please select project.");
      let client = new TaskadeAPIClient(auth.secret_text), options = [], after, moreTasks = !0;
      for (; moreTasks; ) {
        let response = await client.listTasks(project_id, { limit: 100, after });
        if (response.items.length === 0)
          moreTasks = !1;
        else {
          after = response.items[response.items.length - 1].id;
          for (let task of response.items)
            options.push({ label: task.text, value: task.id });
        }
      }
      return {
        disabled: !1,
        options
      };
    }
  })
};

// ../ap/packages/pieces/community/taskade/src/lib/actions/create-task.action.ts
var createTaskAction = (0, import_pieces_framework3.createAction)({
  auth: taskadeAuth,
  name: "taskade-create-task",
  displayName: "Create Task",
  description: "Creates a new task.",
  audience: "both",
  aiMetadata: { description: "Adds a new task to a Taskade project, with the task body supplied as markdown or plain text and positioned at the start or end of the project. Use to append work items to an existing project; requires the target project id. Creates a new task on every call, so it is not idempotent.", idempotent: !1 },
  props: {
    workspace_id: taskadeProps.workspace_id,
    folder_id: taskadeProps.folder_id,
    project_id: taskadeProps.project_id,
    content_type: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Content Type",
      required: !0,
      defaultValue: "text/markdown",
      options: {
        disabled: !1,
        options: [
          {
            label: "text/markdown",
            value: "text/markdown"
          },
          {
            label: "text/plain",
            value: "text/plain"
          }
        ]
      }
    }),
    content: import_pieces_framework3.Property.LongText({
      displayName: "Task Content",
      required: !0
    }),
    placement: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Placement",
      description: "Placement of task in block",
      required: !0,
      defaultValue: "afterbegin",
      options: {
        disabled: !1,
        options: [
          {
            label: "afterbegin",
            value: "afterbegin"
          },
          {
            label: "beforeend",
            value: "beforeend"
          }
        ]
      }
    })
  },
  async run(context) {
    let { project_id, content_type, content, placement } = context.propsValue;
    return await new TaskadeAPIClient(context.auth.secret_text).createTask(project_id, {
      content,
      contentType: content_type,
      placement
    });
  }
});

// ../ap/packages/pieces/community/taskade/src/index.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/taskade/src/lib/actions/complete-task.action.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var completeTaskAction = (0, import_pieces_framework4.createAction)({
  auth: taskadeAuth,
  name: "taskade-complete-task",
  displayName: "Complete Task",
  description: "Complete a task in a project.",
  audience: "both",
  aiMetadata: { description: "Marks an existing Taskade task as completed within a given project. Use when an agent needs to check off or close out a task; requires the project id and task id. Idempotent, since re-running leaves the task in the same completed state.", idempotent: !0 },
  props: {
    workspace_id: taskadeProps.workspace_id,
    folder_id: taskadeProps.folder_id,
    project_id: taskadeProps.project_id,
    task_id: taskadeProps.task_id
  },
  async run(context) {
    let { project_id, task_id } = context.propsValue;
    return await new TaskadeAPIClient(context.auth.secret_text).completeTask(project_id, task_id);
  }
});

// ../ap/packages/pieces/community/taskade/src/lib/actions/delete-task.action.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var deleteTaskAction = (0, import_pieces_framework5.createAction)({
  auth: taskadeAuth,
  name: "taskade-delete-task",
  displayName: "Delete Task",
  description: "Delete an existing task in a project.",
  audience: "both",
  aiMetadata: { description: "Permanently removes a task from a Taskade project, identified by project id and task id. Use to discard a task an agent no longer needs; this is destructive and cannot be undone. Idempotent, since deleting an already-removed task leaves the project in the same state.", idempotent: !0 },
  props: {
    workspace_id: taskadeProps.workspace_id,
    folder_id: taskadeProps.folder_id,
    project_id: taskadeProps.project_id,
    task_id: taskadeProps.task_id
  },
  async run(context) {
    let { project_id, task_id } = context.propsValue;
    return await new TaskadeAPIClient(context.auth.secret_text).deleteTask(project_id, task_id);
  }
});

// ../ap/packages/pieces/community/taskade/src/index.ts
var import_pieces_common2 = require("@activepieces/pieces-common");
var taskade = (0, import_pieces_framework6.createPiece)({
  displayName: "Taskade",
  auth: taskadeAuth,
  minimumSupportedRelease: "0.30.0",
  categories: [import_pieces_framework7.PieceCategory.PRODUCTIVITY],
  description: "collaboration platform for remote teams to organize and manage projects",
  logoUrl: "https://cdn.activepieces.com/pieces/taskade.png",
  authors: ["kishanprmr"],
  actions: [
    createTaskAction,
    completeTaskAction,
    deleteTaskAction,
    (0, import_pieces_common2.createCustomApiCallAction)({
      baseUrl: () => "https://www.taskade.com/api/v1",
      auth: taskadeAuth,
      authMapping: async (auth) => ({ Authorization: `Bearer ${auth.secret_text}` })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  taskade
});
