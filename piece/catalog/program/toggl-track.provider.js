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

// ../ap/packages/pieces/community/toggl-track/src/index.ts
var index_exports = {};
__export(index_exports, {
  togglTrack: () => togglTrack,
  togglTrackAuth: () => togglTrackAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework22 = require("@activepieces/pieces-framework"), import_pieces_framework23 = require("@activepieces/pieces-framework"), import_pieces_common22 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/toggl-track/src/lib/actions/create-client.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/toggl-track/src/lib/common/index.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var togglCommon = {
  organization_id: import_pieces_framework.Property.Dropdown({
    auth: togglTrackAuth,
    displayName: "Organization",
    description: "The organization to operate in.",
    required: !0,
    refreshers: [],
    options: async ({ auth }) => {
      if (!auth)
        return {
          disabled: !0,
          placeholder: "Connect your account first",
          options: []
        };
      let apiToken = auth.secret_text, response = await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: "https://api.track.toggl.com/api/v9/me/organizations",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Basic ${Buffer.from(
            `${apiToken}:api_token`
          ).toString("base64")}`
        }
      });
      return response.status === 200 ? {
        disabled: !1,
        options: (response.body || []).map((org) => ({
          label: org.name,
          value: org.id
        }))
      } : {
        disabled: !0,
        placeholder: "Error fetching organizations",
        options: []
      };
    }
  }),
  workspace_id: import_pieces_framework.Property.Dropdown({
    auth: togglTrackAuth,
    displayName: "Workspace",
    description: "The workspace to operate in.",
    required: !0,
    refreshers: [],
    options: async ({ auth }) => {
      if (!auth)
        return {
          disabled: !0,
          placeholder: "Connect your account first",
          options: []
        };
      let apiToken = auth.secret_text, response = await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: "https://api.track.toggl.com/api/v9/me/workspaces",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Basic ${Buffer.from(
            `${apiToken}:api_token`
          ).toString("base64")}`
        }
      });
      return response.status === 200 ? {
        disabled: !1,
        options: (response.body || []).map((workspace) => ({
          label: workspace.name,
          value: workspace.id
        }))
      } : {
        disabled: !0,
        placeholder: "Error fetching workspaces",
        options: []
      };
    }
  }),
  client_id: import_pieces_framework.Property.Dropdown({
    auth: togglTrackAuth,
    displayName: "Client",
    description: "The client to associate the project with.",
    required: !1,
    refreshers: ["workspace_id"],
    options: async ({ auth, workspace_id }) => {
      if (!auth || !workspace_id)
        return {
          disabled: !0,
          placeholder: "Select a workspace first",
          options: []
        };
      let apiToken = auth.secret_text, response = await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `https://api.track.toggl.com/api/v9/workspaces/${workspace_id}/clients`,
        headers: {
          "Content-Type": "application/json",
          Authorization: `Basic ${Buffer.from(
            `${apiToken}:api_token`
          ).toString("base64")}`
        }
      });
      return response.status === 200 ? {
        disabled: !1,
        options: (response.body || []).map((client) => ({
          label: client.name,
          value: client.id
        }))
      } : {
        disabled: !0,
        placeholder: "Error fetching clients",
        options: []
      };
    }
  }),
  project_id: import_pieces_framework.Property.Dropdown({
    auth: togglTrackAuth,
    displayName: "Project",
    description: "The project to create the task under.",
    required: !0,
    refreshers: ["workspace_id"],
    options: async ({ auth, workspace_id }) => {
      if (!auth || !workspace_id)
        return {
          disabled: !0,
          placeholder: "Select a workspace first",
          options: []
        };
      let apiToken = auth.secret_text, response = await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `https://api.track.toggl.com/api/v9/workspaces/${workspace_id}/projects`,
        headers: {
          "Content-Type": "application/json",
          Authorization: `Basic ${Buffer.from(
            `${apiToken}:api_token`
          ).toString("base64")}`
        }
      });
      return response.status === 200 ? {
        disabled: !1,
        options: (response.body || []).map((project) => ({
          label: project.name,
          value: project.id
        }))
      } : {
        disabled: !0,
        placeholder: "Error fetching projects",
        options: []
      };
    }
  }),
  optional_project_id: import_pieces_framework.Property.Dropdown({
    auth: togglTrackAuth,
    displayName: "Project",
    description: "The project to associate the time entry with.",
    required: !1,
    refreshers: ["workspace_id"],
    options: async ({ auth, workspace_id }) => {
      if (!auth || !workspace_id)
        return {
          disabled: !0,
          placeholder: "Select a workspace first",
          options: []
        };
      let apiToken = auth.secret_text, response = await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `https://api.track.toggl.com/api/v9/workspaces/${workspace_id}/projects`,
        headers: {
          "Content-Type": "application/json",
          Authorization: `Basic ${Buffer.from(
            `${apiToken}:api_token`
          ).toString("base64")}`
        }
      });
      return response.status === 200 ? {
        disabled: !1,
        options: (response.body || []).map((project) => ({
          label: project.name,
          value: project.id
        }))
      } : {
        disabled: !0,
        placeholder: "Error fetching projects",
        options: []
      };
    }
  }),
  tags: import_pieces_framework.Property.MultiSelectDropdown({
    auth: togglTrackAuth,
    displayName: "Tags",
    description: "Tags to associate with the time entry. You can create new tags by typing them.",
    required: !1,
    refreshers: ["workspace_id"],
    options: async ({ auth, workspace_id }) => {
      if (!auth || !workspace_id)
        return {
          disabled: !0,
          placeholder: "Select a workspace first",
          options: []
        };
      let apiToken = auth.secret_text, response = await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `https://api.track.toggl.com/api/v9/workspaces/${workspace_id}/tags`,
        headers: {
          "Content-Type": "application/json",
          Authorization: `Basic ${Buffer.from(
            `${apiToken}:api_token`
          ).toString("base64")}`
        }
      });
      return response.status === 200 ? {
        disabled: !1,
        options: (response.body || []).map((tag) => ({
          label: tag.name,
          value: tag.name
        }))
      } : {
        disabled: !0,
        placeholder: "Error fetching tags",
        options: []
      };
    }
  }),
  optional_task_id: import_pieces_framework.Property.Dropdown({
    auth: togglTrackAuth,
    displayName: "Task",
    description: "The task to select.",
    required: !1,
    refreshers: ["workspace_id", "optional_project_id"],
    options: async ({ auth, workspace_id, optional_project_id }) => {
      if (!auth || !workspace_id || !optional_project_id)
        return {
          disabled: !0,
          placeholder: "Select a workspace and project first",
          options: []
        };
      let apiToken = auth.secret_text, response = await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `https://api.track.toggl.com/api/v9/workspaces/${workspace_id}/tasks`,
        queryParams: {
          pid: optional_project_id
        },
        headers: {
          "Content-Type": "application/json",
          Authorization: `Basic ${Buffer.from(
            `${apiToken}:api_token`
          ).toString("base64")}`
        }
      });
      return response.status === 200 ? {
        disabled: !1,
        options: (response.body.data || []).map((task) => ({
          label: task.name,
          value: task.id
        }))
      } : {
        disabled: !0,
        placeholder: "Error fetching tasks",
        options: []
      };
    }
  })
};

// ../ap/packages/pieces/community/toggl-track/src/lib/actions/create-client.ts
var createClient = (0, import_pieces_framework2.createAction)({
  auth: togglTrackAuth,
  name: "create_client",
  classification: "WRITE",
  displayName: "Create Client",
  description: "Create a new client in a workspace.",
  audience: "both",
  aiMetadata: { description: "Creates a new client record in a Toggl Track workspace, given a workspace ID and client name (optional external reference and notes). Use when an agent needs to register a billable/organizational client before associating projects with it. Not idempotent: each call creates a new client even if the name already exists.", idempotent: !1 },
  props: {
    workspace_id: togglCommon.workspace_id,
    name: import_pieces_framework2.Property.ShortText({
      displayName: "Client Name",
      description: "The name of the new client.",
      required: !0
    }),
    external_reference: import_pieces_framework2.Property.ShortText({
      displayName: "External Reference",
      description: "External reference to link this client with external systems.",
      required: !1
    }),
    notes: import_pieces_framework2.Property.LongText({
      displayName: "Notes",
      description: "Notes for the client.",
      required: !1
    })
  },
  async run(context) {
    let { workspace_id, name, external_reference, notes } = context.propsValue, apiToken = context.auth;
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: `https://api.track.toggl.com/api/v9/workspaces/${workspace_id}/clients`,
      headers: {
        "Content-Type": "application/json",
        Authorization: `Basic ${Buffer.from(`${apiToken}:api_token`).toString(
          "base64"
        )}`
      },
      body: {
        name,
        external_reference,
        notes
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/toggl-track/src/lib/actions/create-project.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var createProject = (0, import_pieces_framework3.createAction)({
  auth: togglTrackAuth,
  name: "create_project",
  classification: "WRITE",
  displayName: "Create Project",
  description: "Create a new project in a workspace.",
  audience: "both",
  aiMetadata: { description: "Creates a new project in a Toggl Track workspace, given a workspace ID and project name; optionally links a client and sets billing, rate, color, and timeframe (several rate/estimate options require a premium plan). Use when an agent needs to set up a project to track time against. Not idempotent: each call creates a new project regardless of name.", idempotent: !1 },
  props: {
    workspace_id: togglCommon.workspace_id,
    name: import_pieces_framework3.Property.ShortText({
      displayName: "Project Name",
      description: "The name of the new project.",
      required: !0
    }),
    client_id: togglCommon.client_id,
    is_private: import_pieces_framework3.Property.Checkbox({
      displayName: "Private",
      description: "Whether the project is private or not.",
      required: !1,
      defaultValue: !1
    }),
    billable: import_pieces_framework3.Property.Checkbox({
      displayName: "Billable",
      description: "Whether the project is billable. (Premium feature)",
      required: !1,
      defaultValue: !1
    }),
    template: import_pieces_framework3.Property.Checkbox({
      displayName: "Is Template",
      description: "Whether the project is a template. (Premium feature)",
      required: !1,
      defaultValue: !1
    }),
    external_reference: import_pieces_framework3.Property.ShortText({
      displayName: "External Reference",
      description: "External reference to link this project with external systems.",
      required: !1
    }),
    color: import_pieces_framework3.Property.ShortText({
      displayName: "Project Color",
      description: "Project color in hex format (e.g. #ff0000).",
      required: !1
    }),
    active: import_pieces_framework3.Property.Checkbox({
      displayName: "Active",
      description: "Whether the project is active.",
      required: !1,
      defaultValue: !0
    }),
    auto_estimates: import_pieces_framework3.Property.Checkbox({
      displayName: "Auto Estimates",
      description: "Whether estimates are based on task hours. (Premium feature)",
      required: !1,
      defaultValue: !1
    }),
    estimated_hours: import_pieces_framework3.Property.Number({
      displayName: "Estimated Hours",
      description: "Estimated hours for the project. (Premium feature)",
      required: !1
    }),
    rate: import_pieces_framework3.Property.Number({
      displayName: "Hourly Rate",
      description: "Hourly rate for the project. (Premium feature)",
      required: !1
    }),
    fixed_fee: import_pieces_framework3.Property.Number({
      displayName: "Fixed Fee",
      description: "Project fixed fee. (Premium feature)",
      required: !1
    }),
    start_date: import_pieces_framework3.Property.ShortText({
      displayName: "Start Date",
      description: "Start date of project timeframe (YYYY-MM-DD).",
      required: !1
    }),
    end_date: import_pieces_framework3.Property.ShortText({
      displayName: "End Date",
      description: "End date of project timeframe (YYYY-MM-DD).",
      required: !1
    })
  },
  async run(context) {
    let {
      workspace_id,
      name,
      client_id,
      is_private,
      billable,
      template,
      external_reference,
      color,
      active,
      auto_estimates,
      estimated_hours,
      rate,
      fixed_fee,
      start_date,
      end_date
    } = context.propsValue, apiToken = context.auth, body = __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({
      name,
      is_private,
      billable,
      template,
      active,
      auto_estimates
    }, client_id && { client_id }), external_reference && { external_reference }), color && { color }), estimated_hours && { estimated_hours }), rate && { rate }), fixed_fee && { fixed_fee }), start_date && { start_date }), end_date && { end_date });
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: `https://api.track.toggl.com/api/v9/workspaces/${workspace_id}/projects`,
      headers: {
        "Content-Type": "application/json",
        Authorization: `Basic ${Buffer.from(`${apiToken}:api_token`).toString(
          "base64"
        )}`
      },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/toggl-track/src/lib/actions/create-task.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var createTask = (0, import_pieces_framework4.createAction)({
  auth: togglTrackAuth,
  name: "create_task",
  classification: "WRITE",
  displayName: "Create Task",
  description: "Create a new task under a project.",
  audience: "both",
  aiMetadata: { description: "Creates a new task under a specific project in a Toggl Track workspace, given workspace ID, project ID, and task name. Use when an agent needs a sub-item of a project to track time against. Not idempotent: each call creates a new task even if one with the same name exists.", idempotent: !1 },
  props: {
    workspace_id: togglCommon.workspace_id,
    project_id: togglCommon.project_id,
    name: import_pieces_framework4.Property.ShortText({
      displayName: "Task Name",
      description: "The name of the new task.",
      required: !0
    }),
    estimated_seconds: import_pieces_framework4.Property.Number({
      displayName: "Estimated Seconds",
      description: "The estimated time for the task in seconds.",
      required: !1
    }),
    external_reference: import_pieces_framework4.Property.ShortText({
      displayName: "External Reference",
      description: "External reference to link this task with external systems.",
      required: !1
    }),
    active: import_pieces_framework4.Property.Checkbox({
      displayName: "Active",
      description: "Whether the task is active. Use false to mark as done.",
      required: !1,
      defaultValue: !0
    }),
    user_id: import_pieces_framework4.Property.Number({
      displayName: "Creator User ID",
      description: "Creator ID, if omitted, will use requester user ID.",
      required: !1
    })
  },
  async run(context) {
    let {
      workspace_id,
      project_id,
      name,
      estimated_seconds,
      external_reference,
      active,
      user_id
    } = context.propsValue, apiToken = context.auth;
    return (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: `https://api.track.toggl.com/api/v9/workspaces/${workspace_id}/projects/${project_id}/tasks`,
      headers: {
        "Content-Type": "application/json",
        Authorization: `Basic ${Buffer.from(`${apiToken}:api_token`).toString(
          "base64"
        )}`
      },
      body: __spreadValues(__spreadValues(__spreadValues({
        name,
        active
      }, estimated_seconds && { estimated_seconds }), external_reference && { external_reference }), user_id && { user_id })
    })).body;
  }
});

// ../ap/packages/pieces/community/toggl-track/src/lib/actions/create-tag.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var createTag = (0, import_pieces_framework5.createAction)({
  auth: togglTrackAuth,
  name: "create_tag",
  classification: "WRITE",
  displayName: "Create Tag",
  description: "Create a new tag in the workspace.",
  audience: "both",
  aiMetadata: { description: "Creates a new tag in a Toggl Track workspace, given a workspace ID and tag name. Use when an agent needs a label to later attach to time entries. Not idempotent: each call creates a new tag even if the name already exists.", idempotent: !1 },
  props: {
    workspace_id: togglCommon.workspace_id,
    name: import_pieces_framework5.Property.ShortText({
      displayName: "Tag Name",
      description: "The name of the new tag.",
      required: !0
    })
  },
  async run(context) {
    let { workspace_id, name } = context.propsValue, apiToken = context.auth;
    return (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.POST,
      url: `https://api.track.toggl.com/api/v9/workspaces/${workspace_id}/tags`,
      headers: {
        "Content-Type": "application/json",
        Authorization: `Basic ${Buffer.from(`${apiToken}:api_token`).toString(
          "base64"
        )}`
      },
      body: {
        name
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/toggl-track/src/lib/actions/create-time-entry.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var createTimeEntry = (0, import_pieces_framework6.createAction)({
  auth: togglTrackAuth,
  name: "create_time_entry",
  classification: "WRITE",
  displayName: "Create Time Entry",
  description: "Create a new time entry in a workspace.",
  audience: "both",
  aiMetadata: { description: "Creates a time entry in a Toggl Track workspace with a start time and duration in seconds; can record a completed entry (positive duration or explicit stop) or start a running timer (pass a negative duration such as -1). Optionally links project, task, tags, and billable flag. Use to log work after the fact or begin tracking. Not idempotent: each call creates a new entry.", idempotent: !1 },
  props: {
    workspace_id: togglCommon.workspace_id,
    description: import_pieces_framework6.Property.LongText({
      displayName: "Description",
      required: !1
    }),
    start: import_pieces_framework6.Property.DateTime({
      displayName: "Start Time",
      description: "The start time of the entry in UTC.",
      required: !0
    }),
    duration: import_pieces_framework6.Property.Number({
      displayName: "Duration (in seconds)",
      description: "Duration of the time entry. Use a negative number (e.g., -1) to start a running timer.",
      required: !0
    }),
    stop: import_pieces_framework6.Property.DateTime({
      displayName: "Stop Time",
      description: "The stop time of the entry in UTC. Can be omitted if still running.",
      required: !1
    }),
    task_id: togglCommon.optional_task_id,
    project_id: togglCommon.optional_project_id,
    tags: togglCommon.tags,
    billable: import_pieces_framework6.Property.Checkbox({
      displayName: "Billable",
      description: "Whether the time entry is marked as billable.",
      required: !1,
      defaultValue: !1
    }),
    user_id: import_pieces_framework6.Property.Number({
      displayName: "Creator User ID",
      description: "Time entry creator ID. If omitted, will use requester user ID.",
      required: !1
    })
  },
  async run(context) {
    let {
      workspace_id,
      description,
      start,
      duration,
      stop,
      project_id,
      task_id,
      tags,
      billable,
      user_id
    } = context.propsValue, apiToken = context.auth;
    return (await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.POST,
      url: `https://api.track.toggl.com/api/v9/workspaces/${workspace_id}/time_entries`,
      headers: {
        "Content-Type": "application/json",
        Authorization: `Basic ${Buffer.from(`${apiToken}:api_token`).toString(
          "base64"
        )}`
      },
      body: __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({
        workspace_id: Number(workspace_id),
        description,
        start: new Date(start).toISOString(),
        duration,
        created_with: "Activepieces",
        billable
      }, stop && { stop: new Date(stop).toISOString() }), project_id && { project_id }), task_id && { task_id }), tags && { tags }), user_id && { user_id })
    })).body;
  }
});

// ../ap/packages/pieces/community/toggl-track/src/lib/actions/start-time-entry.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var startTimeEntry = (0, import_pieces_framework7.createAction)({
  auth: togglTrackAuth,
  name: "start_time_entry",
  classification: "WRITE",
  displayName: "Start Time Entry",
  description: "Start a new time entry (live timer).",
  audience: "both",
  aiMetadata: { description: "Starts a live running timer in a Toggl Track workspace, beginning now with an open-ended duration; optionally links project, task, tags, and billable flag. Use to begin tracking time in real time (use Stop Time Entry to end it); prefer Create Time Entry to log a completed entry with an explicit start/stop. Not idempotent: each call starts a new running entry.", idempotent: !1 },
  props: {
    workspace_id: togglCommon.workspace_id,
    description: import_pieces_framework7.Property.LongText({
      displayName: "Description",
      required: !1
    }),
    project_id: togglCommon.optional_project_id,
    tags: togglCommon.tags,
    billable: import_pieces_framework7.Property.Checkbox({
      displayName: "Billable",
      description: "Whether the time entry is marked as billable.",
      required: !1,
      defaultValue: !1
    }),
    task_id: togglCommon.optional_task_id
  },
  async run(context) {
    let { workspace_id, description, project_id, tags, billable, task_id } = context.propsValue, apiToken = context.auth;
    return (await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.POST,
      url: `https://api.track.toggl.com/api/v9/workspaces/${workspace_id}/time_entries`,
      headers: {
        "Content-Type": "application/json",
        Authorization: `Basic ${Buffer.from(`${apiToken}:api_token`).toString(
          "base64"
        )}`
      },
      body: __spreadValues(__spreadValues(__spreadValues({
        workspace_id: Number(workspace_id),
        description,
        start: (/* @__PURE__ */ new Date()).toISOString(),
        duration: -1,
        created_with: "Activepieces",
        billable
      }, project_id && { project_id }), task_id && { task_id }), tags && { tags })
    })).body;
  }
});

// ../ap/packages/pieces/community/toggl-track/src/lib/actions/stop-time-entry.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var stopTimeEntry = (0, import_pieces_framework8.createAction)({
  auth: togglTrackAuth,
  name: "stop_time_entry",
  classification: "WRITE",
  displayName: "Stop Time Entry",
  description: "Stops the currently running time entry.",
  audience: "both",
  aiMetadata: { description: "Looks up the current user's running time entry and stops it, setting its stop time in the given workspace. Use to end an active timer started by Start Time Entry. Returns a no-op message if no entry is running; effectively idempotent once stopped, but treated as a state-mutating call.", idempotent: !1 },
  props: {
    workspace_id: togglCommon.workspace_id
  },
  async run(context) {
    let { workspace_id } = context.propsValue, apiToken = context.auth, runningEntryId;
    try {
      runningEntryId = (await import_pieces_common8.httpClient.sendRequest({
        method: import_pieces_common8.HttpMethod.GET,
        url: "https://api.track.toggl.com/api/v9/me/time_entries/current",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Basic ${Buffer.from(`${apiToken}:api_token`).toString(
            "base64"
          )}`
        }
      })).body.id;
    } catch (e) {
      if (e instanceof import_pieces_common8.HttpError && e.response.status === 404)
        return { success: !1, message: "No time entry is currently running." };
      throw e;
    }
    return (await import_pieces_common8.httpClient.sendRequest({
      method: import_pieces_common8.HttpMethod.PATCH,
      url: `https://api.track.toggl.com/api/v9/workspaces/${workspace_id}/time_entries/${runningEntryId}/stop`,
      headers: {
        "Content-Type": "application/json",
        Authorization: `Basic ${Buffer.from(`${apiToken}:api_token`).toString(
          "base64"
        )}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/toggl-track/src/lib/actions/find-user.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var findUser = (0, import_pieces_framework9.createAction)({
  auth: togglTrackAuth,
  name: "find_user",
  classification: "SEARCH",
  displayName: "Find User",
  description: "Find a user in a workspace by their name or email.",
  audience: "both",
  aiMetadata: { description: "Looks up workspace members in a Toggl Track workspace, given organization ID and workspace ID; an empty search term returns all members, while a name or email filters to matches. Use to resolve a user before assigning work or attributing time. Read-only and idempotent.", idempotent: !0 },
  props: {
    workspace_id: togglCommon.workspace_id,
    organization_id: togglCommon.organization_id,
    search_term: import_pieces_framework9.Property.ShortText({
      displayName: "Name or Email",
      description: "The name or email of the user to find.",
      required: !1
    }),
    active: import_pieces_framework9.Property.Checkbox({
      displayName: "Active Users Only",
      description: "Return only active users.",
      required: !1,
      defaultValue: !0
    }),
    page: import_pieces_framework9.Property.Number({
      displayName: "Page Number",
      description: "Page number for pagination.",
      required: !1
    }),
    per_page: import_pieces_framework9.Property.Number({
      displayName: "Items Per Page",
      description: "Number of items per page.",
      required: !1
    })
  },
  async run(context) {
    let {
      workspace_id,
      organization_id,
      search_term,
      active,
      page,
      per_page
    } = context.propsValue, apiToken = context.auth, queryParams = {};
    return search_term && (queryParams.search = search_term), active !== void 0 && (queryParams.active = active.toString()), page && (queryParams.page = page.toString()), per_page && (queryParams.per_page = per_page.toString()), (await import_pieces_common9.httpClient.sendRequest({
      method: import_pieces_common9.HttpMethod.GET,
      url: `https://api.track.toggl.com/api/v9/organizations/${organization_id}/workspaces/${workspace_id}/workspace_users`,
      headers: {
        "Content-Type": "application/json",
        Authorization: `Basic ${Buffer.from(`${apiToken}:api_token`).toString(
          "base64"
        )}`
      },
      queryParams
    })).body;
  }
});

// ../ap/packages/pieces/community/toggl-track/src/lib/actions/find-project.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var findProject = (0, import_pieces_framework10.createAction)({
  auth: togglTrackAuth,
  name: "find_project",
  classification: "SEARCH",
  displayName: "Find Project",
  description: "Find a project in a workspace by its name.",
  audience: "both",
  aiMetadata: { description: "Lists projects in a Toggl Track workspace, optionally filtered by name, active/inactive/both status, billable, ownership, or template flag; omitting the name returns all matching projects. Use to resolve a project ID before creating tasks or time entries against it. Read-only and idempotent.", idempotent: !0 },
  props: {
    workspace_id: togglCommon.workspace_id,
    name: import_pieces_framework10.Property.ShortText({
      displayName: "Project Name",
      description: "The name of the project to find.",
      required: !1
    }),
    active: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Project Status",
      description: "Return active, inactive, or both types of projects.",
      required: !1,
      defaultValue: "true",
      options: {
        disabled: !1,
        options: [
          { label: "Active", value: "true" },
          { label: "Inactive", value: "false" },
          { label: "Both", value: "both" }
        ]
      }
    }),
    billable: import_pieces_framework10.Property.Checkbox({
      displayName: "Billable Only",
      description: "Return only billable projects.",
      required: !1
    }),
    only_me: import_pieces_framework10.Property.Checkbox({
      displayName: "My Projects Only",
      description: "Get only projects assigned to the current user.",
      required: !1,
      defaultValue: !1
    }),
    only_templates: import_pieces_framework10.Property.Checkbox({
      displayName: "Templates Only",
      description: "Return only template projects.",
      required: !1,
      defaultValue: !1
    }),
    page: import_pieces_framework10.Property.Number({
      displayName: "Page Number",
      description: "Page number for pagination.",
      required: !1
    }),
    per_page: import_pieces_framework10.Property.Number({
      displayName: "Items Per Page",
      description: "Number of items per page (max 200).",
      required: !1
    })
  },
  async run(context) {
    let {
      workspace_id,
      name,
      active,
      billable,
      only_me,
      only_templates,
      page,
      per_page
    } = context.propsValue, apiToken = context.auth, queryParams = {};
    return name && (queryParams.name = name), active !== void 0 && (queryParams.active = active), billable !== void 0 && (queryParams.billable = billable.toString()), only_me !== void 0 && (queryParams.only_me = only_me.toString()), only_templates !== void 0 && (queryParams.only_templates = only_templates.toString()), page && (queryParams.page = page.toString()), per_page && (queryParams.per_page = per_page.toString()), (await import_pieces_common10.httpClient.sendRequest({
      method: import_pieces_common10.HttpMethod.GET,
      url: `https://api.track.toggl.com/api/v9/workspaces/${workspace_id}/projects`,
      headers: {
        "Content-Type": "application/json",
        Authorization: `Basic ${Buffer.from(`${apiToken}:api_token`).toString(
          "base64"
        )}`
      },
      queryParams
    })).body;
  }
});

// ../ap/packages/pieces/community/toggl-track/src/lib/actions/find-task.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var findTask = (0, import_pieces_framework11.createAction)({
  auth: togglTrackAuth,
  name: "find_task",
  classification: "SEARCH",
  displayName: "Find Task",
  description: "Find a task by name and status.",
  audience: "both",
  aiMetadata: { description: "Lists tasks in a Toggl Track workspace, optionally filtered by name, project ID, active/inactive/both status, and date range, with sorting and pagination; omitting filters returns all tasks. Use to resolve a task ID before logging time against it. Read-only and idempotent.", idempotent: !0 },
  props: {
    workspace_id: togglCommon.workspace_id,
    search: import_pieces_framework11.Property.ShortText({
      displayName: "Task Name",
      description: "Search by task name.",
      required: !1
    }),
    project_id: import_pieces_framework11.Property.Number({
      displayName: "Project ID",
      description: "Filter by project ID.",
      required: !1
    }),
    active: import_pieces_framework11.Property.StaticDropdown({
      displayName: "Status",
      description: "Filter by active state.",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Active", value: "true" },
          { label: "Inactive", value: "false" },
          { label: "Both", value: "both" }
        ]
      }
    }),
    page: import_pieces_framework11.Property.Number({
      displayName: "Page Number",
      description: "Page number for pagination.",
      required: !1
    }),
    per_page: import_pieces_framework11.Property.Number({
      displayName: "Items Per Page",
      description: "Number of items per page (default 50).",
      required: !1
    }),
    sort_field: import_pieces_framework11.Property.StaticDropdown({
      displayName: "Sort Field",
      description: "Field used for sorting.",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Name", value: "name" },
          { label: "Created At", value: "created_at" }
        ]
      }
    }),
    sort_order: import_pieces_framework11.Property.StaticDropdown({
      displayName: "Sort Order",
      description: "Sort order.",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Ascending", value: "ASC" },
          { label: "Descending", value: "DESC" }
        ]
      }
    }),
    start_date: import_pieces_framework11.Property.ShortText({
      displayName: "Start Date",
      description: "Smallest boundary date (YYYY-MM-DD).",
      required: !1
    }),
    end_date: import_pieces_framework11.Property.ShortText({
      displayName: "End Date",
      description: "Biggest boundary date (YYYY-MM-DD).",
      required: !1
    })
  },
  async run(context) {
    let {
      workspace_id,
      search,
      project_id,
      active,
      page,
      per_page,
      sort_field,
      sort_order,
      start_date,
      end_date
    } = context.propsValue, apiToken = context.auth, queryParams = {};
    return search && (queryParams.search = search), project_id && (queryParams.pid = project_id.toString()), active && (queryParams.active = active), page && (queryParams.page = page.toString()), per_page && (queryParams.per_page = per_page.toString()), sort_field && (queryParams.sort_field = sort_field), sort_order && (queryParams.sort_order = sort_order), start_date && (queryParams.start_date = start_date), end_date && (queryParams.end_date = end_date), (await import_pieces_common11.httpClient.sendRequest({
      method: import_pieces_common11.HttpMethod.GET,
      url: `https://api.track.toggl.com/api/v9/workspaces/${workspace_id}/tasks`,
      headers: {
        "Content-Type": "application/json",
        Authorization: `Basic ${Buffer.from(`${apiToken}:api_token`).toString(
          "base64"
        )}`
      },
      queryParams
    })).body;
  }
});

// ../ap/packages/pieces/community/toggl-track/src/lib/actions/find-client.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common");
var findClient = (0, import_pieces_framework12.createAction)({
  auth: togglTrackAuth,
  name: "find_client",
  classification: "SEARCH",
  displayName: "Find Client",
  description: "Find a client by name or status in a workspace.",
  audience: "both",
  aiMetadata: { description: "Lists clients in a Toggl Track workspace, optionally filtered by a case-insensitive name and by active/archived/both status; omitting both returns all clients. Use to resolve a client ID before linking it to a project. Read-only and idempotent.", idempotent: !0 },
  props: {
    workspace_id: togglCommon.workspace_id,
    name: import_pieces_framework12.Property.ShortText({
      displayName: "Client Name",
      description: "The name of the client to find (case-insensitive).",
      required: !1
    }),
    status: import_pieces_framework12.Property.StaticDropdown({
      displayName: "Status",
      description: "Filter clients by their status.",
      required: !1,
      options: {
        options: [
          { label: "Active", value: "active" },
          { label: "Archived", value: "archived" },
          { label: "Both", value: "both" }
        ]
      }
    })
  },
  async run(context) {
    let { workspace_id, name, status } = context.propsValue, apiToken = context.auth, queryParams = {};
    return name && (queryParams.name = name), status && (queryParams.status = status), (await import_pieces_common12.httpClient.sendRequest({
      method: import_pieces_common12.HttpMethod.GET,
      url: `https://api.track.toggl.com/api/v9/workspaces/${workspace_id}/clients`,
      headers: {
        "Content-Type": "application/json",
        Authorization: `Basic ${Buffer.from(`${apiToken}:api_token`).toString(
          "base64"
        )}`
      },
      queryParams
    })).body;
  }
});

// ../ap/packages/pieces/community/toggl-track/src/lib/actions/find-tag.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common13 = require("@activepieces/pieces-common");
var findTag = (0, import_pieces_framework13.createAction)({
  auth: togglTrackAuth,
  name: "find_tag",
  classification: "SEARCH",
  displayName: "Find Tag",
  description: "Find a tag by name in a workspace.",
  audience: "both",
  aiMetadata: { description: "Lists tags in a Toggl Track workspace, optionally filtered by a name search with pagination; omitting the search returns all tags. Use to resolve a tag before attaching it to a time entry, or to check whether a tag already exists. Read-only and idempotent.", idempotent: !0 },
  props: {
    workspace_id: togglCommon.workspace_id,
    search: import_pieces_framework13.Property.ShortText({
      displayName: "Tag Name",
      description: "Search by tag name.",
      required: !1
    }),
    page: import_pieces_framework13.Property.Number({
      displayName: "Page Number",
      description: "Page number for pagination.",
      required: !1
    }),
    per_page: import_pieces_framework13.Property.Number({
      displayName: "Items Per Page",
      description: "Number of items per page.",
      required: !1
    })
  },
  async run(context) {
    let { workspace_id, search, page, per_page } = context.propsValue, apiToken = context.auth, queryParams = {};
    return search && (queryParams.search = search), page && (queryParams.page = page.toString()), per_page && (queryParams.per_page = per_page.toString()), (await import_pieces_common13.httpClient.sendRequest({
      method: import_pieces_common13.HttpMethod.GET,
      url: `https://api.track.toggl.com/api/v9/workspaces/${workspace_id}/tags`,
      headers: {
        "Content-Type": "application/json",
        Authorization: `Basic ${Buffer.from(`${apiToken}:api_token`).toString(
          "base64"
        )}`
      },
      queryParams
    })).body;
  }
});

// ../ap/packages/pieces/community/toggl-track/src/lib/actions/find-time-entry.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_common14 = require("@activepieces/pieces-common");
var findTimeEntry = (0, import_pieces_framework14.createAction)({
  auth: togglTrackAuth,
  name: "find_time_entry",
  classification: "SEARCH",
  displayName: "Find Time Entry",
  description: "Find time entries by description.",
  audience: "both",
  aiMetadata: { description: "Retrieves the current user's time entries, optionally bounded by date range or modified-since timestamp; when a description text is provided it filters client-side to entries whose description contains that text (case-insensitive), otherwise all entries in range are returned. Use to locate existing entries before reporting or updating. Read-only and idempotent.", idempotent: !0 },
  props: {
    description: import_pieces_framework14.Property.ShortText({
      displayName: "Description Contains",
      description: "Search for time entries containing this text in description.",
      required: !1
    }),
    start_date: import_pieces_framework14.Property.ShortText({
      displayName: "Start Date",
      description: "Get entries from start_date (YYYY-MM-DD or RFC3339 format).",
      required: !1
    }),
    end_date: import_pieces_framework14.Property.ShortText({
      displayName: "End Date",
      description: "Get entries until end_date (YYYY-MM-DD or RFC3339 format).",
      required: !1
    }),
    before: import_pieces_framework14.Property.ShortText({
      displayName: "Before Date",
      description: "Get entries before given date (YYYY-MM-DD or RFC3339 format).",
      required: !1
    }),
    since: import_pieces_framework14.Property.Number({
      displayName: "Since Timestamp",
      description: "Get entries modified since this UNIX timestamp.",
      required: !1
    }),
    meta: import_pieces_framework14.Property.Checkbox({
      displayName: "Include Meta Data",
      description: "Should the response contain data for meta entities.",
      required: !1,
      defaultValue: !1
    }),
    include_sharing: import_pieces_framework14.Property.Checkbox({
      displayName: "Include Sharing",
      description: "Include sharing details in the response.",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let {
      description,
      start_date,
      end_date,
      before,
      since,
      meta,
      include_sharing
    } = context.propsValue, apiToken = context.auth, queryParams = {};
    start_date && (queryParams.start_date = start_date), end_date && (queryParams.end_date = end_date), before && (queryParams.before = before), since && (queryParams.since = since.toString()), meta !== void 0 && (queryParams.meta = meta.toString()), include_sharing !== void 0 && (queryParams.include_sharing = include_sharing.toString());
    let response = await import_pieces_common14.httpClient.sendRequest({
      method: import_pieces_common14.HttpMethod.GET,
      url: "https://api.track.toggl.com/api/v9/me/time_entries",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Basic ${Buffer.from(`${apiToken}:api_token`).toString(
          "base64"
        )}`
      },
      queryParams
    }), timeEntries = response.body.items || [];
    return description ? { items: timeEntries.filter(
      (entry) => entry.description && entry.description.toLowerCase().includes(description.toLowerCase())
    ) } : response.body;
  }
});

// ../ap/packages/pieces/community/toggl-track/src/lib/triggers/new-client.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework"), import_pieces_common15 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/toggl-track/src/lib/common/webhook-instructions.ts
function generateTogglWebhookInstructions(eventName, triggerName, description, eventDetails) {
  return `
## Setup Instructions

**IMPORTANT: Publish Your Flow First**

Before you can set up the webhook in Toggl Track, you must **publish this flow in ActivePieces first**. The webhook URL will only be available after publishing.

### Step 1: Publish Your Flow in ActivePieces
1. Click the **"Publish"**
2.  Once published, the webhook URL will be available in this trigger's setup
---

### Step 2: Access Toggl Track Webhooks
- Log in to your Toggl Track account
- Go to **Integrations > Webhooks** (Admin access required)
- Click **"Create Webhook"** or **"Create your first webhook"**

### Step 3: Configure the Webhook
1. **Name**: Enter a descriptive name (e.g., "Activepieces ${triggerName}")
2. **Events**: Select **"${eventName}"** from the events dropdown
3. **URL Endpoint**: Paste this webhook URL:
\`\`\`text
{{webhookUrl}}
\`\`\`
4. **Secret**: Enter a secret key for security (optional but recommended)

### Step 4: Complete Setup
- Click **"Add Webhook"**
- Toggl Track will validate your endpoint
- The webhook will appear in your webhooks list when ready

### Step 5: Verification
- Your webhook status should show as **"Validated"** (happens automatically)
- You can use the **"Test"** option to verify it's working
- ${description}

---

**Requirements:**
- Admin permissions in your Toggl Track workspace
- Limit: 5 webhooks per workspace user

**Event Details:**
${eventDetails}

**Troubleshooting:**
- If status shows **"Not validated"**, click "Test" to trigger auto-validation
- If status shows **"Disabled"**, re-enable it from the 3-dot menu
- If events aren't received, verify you selected "${eventName}" event

**Note:** This trigger uses manual webhook setup as recommended by Toggl Track. **Validation happens automatically** when you click "Test" or when Toggl sends the first ping. The webhook will be managed through your Toggl Track dashboard, not programmatically through ActivePieces.
  `;
}
var TOGGL_WEBHOOK_EVENTS = {
  CLIENT_CREATED: "Client created",
  CLIENT_UPDATED: "Client updated",
  CLIENT_DELETED: "Client deleted",
  PROJECT_CREATED: "Project created",
  PROJECT_UPDATED: "Project updated",
  PROJECT_DELETED: "Project deleted",
  TAG_CREATED: "Tag created",
  TAG_UPDATED: "Tag updated",
  TAG_DELETED: "Tag deleted",
  TASK_CREATED: "Task created",
  TASK_UPDATED: "Task updated",
  TASK_DELETED: "Task deleted",
  TIME_ENTRY_CREATED: "Time entry created",
  TIME_ENTRY_UPDATED: "Time entry updated",
  TIME_ENTRY_DELETED: "Time entry deleted",
  WORKSPACE_CREATED: "Workspace created",
  WORKSPACE_UPDATED: "Workspace updated",
  WORKSPACE_DELETED: "Workspace deleted",
  WORKSPACE_USER_CREATED: "Workspace user created",
  WORKSPACE_USER_UPDATED: "Workspace user updated",
  WORKSPACE_USER_DELETED: "Workspace user deleted",
  PROJECT_GROUP_CREATED: "Project group created",
  PROJECT_GROUP_UPDATED: "Project group updated",
  PROJECT_GROUP_DELETED: "Project group deleted",
  PROJECT_USER_CREATED: "Project user created",
  PROJECT_USER_UPDATED: "Project user updated",
  PROJECT_USER_DELETED: "Project user deleted"
};

// ../ap/packages/pieces/community/toggl-track/src/lib/triggers/new-client.ts
var newClient = (0, import_pieces_framework15.createTrigger)({
  auth: togglTrackAuth,
  name: "new_client",
  classification: "READ",
  displayName: "New Client",
  description: "Fires when a new client is created in a workspace.",
  aiMetadata: {
    description: "Fires when a new client is created in the configured Toggl Track workspace, delivering the created client (id, name, notes, workspace). Delivered via a Toggl webhook the user must set up manually."
  },
  props: {
    workspace_id: togglCommon.workspace_id,
    setupInstructions: import_pieces_framework15.Property.MarkDown({
      value: generateTogglWebhookInstructions(
        TOGGL_WEBHOOK_EVENTS.CLIENT_CREATED,
        "New Client",
        "Create a test client to ensure events are received",
        `This trigger will fire when clients are created and will include:
- Client ID and details
- Workspace information
- Creation timestamp
- Creator information
- Client notes and status`
      )
    })
  },
  sampleData: {
    id: 123456,
    name: "Acme Corporation",
    wid: 987654,
    archived: !1,
    at: "2025-08-29T10:15:30+00:00",
    creator_id: 6,
    notes: "Important client for Q4 projects"
  },
  type: import_pieces_framework15.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let payload = context.payload.body;
    if ((payload == null ? void 0 : payload.payload) === "ping" || payload != null && payload.validation_code || payload != null && payload.validation_code_url) {
      try {
        return await import_pieces_common15.httpClient.sendRequest({
          method: import_pieces_common15.HttpMethod.GET,
          url: payload.validation_code_url,
          headers: {
            "Content-Type": "application/json"
          }
        }), [
          {
            message: "Webhook validation successful",
            timestamp: (/* @__PURE__ */ new Date()).toISOString(),
            status: "validated"
          }
        ];
      } catch (error) {
        console.error("Failed to auto-validate webhook:", error);
      }
      return [];
    }
    return [payload];
  }
});

// ../ap/packages/pieces/community/toggl-track/src/lib/triggers/new-workspace.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework");
var import_pieces_common16 = require("@activepieces/pieces-common"), polling = {
  strategy: import_pieces_common16.DedupeStrategy.TIMEBASED,
  items: async ({ auth }) => {
    let authHeader = `Basic ${Buffer.from(`${auth.secret_text}:api_token`).toString("base64")}`;
    return (await import_pieces_common16.httpClient.sendRequest({
      method: import_pieces_common16.HttpMethod.GET,
      url: "https://api.track.toggl.com/api/v9/workspaces",
      headers: {
        "Content-Type": "application/json",
        Authorization: authHeader
      }
    })).body.map((workspace) => ({
      epochMilliSeconds: new Date(workspace.at || workspace.last_modified).getTime(),
      data: workspace
    }));
  }
}, newWorkspace = (0, import_pieces_framework16.createTrigger)({
  auth: togglTrackAuth,
  name: "new_workspace",
  classification: "READ",
  displayName: "New or Updated Workspace",
  description: "Fires when a workspace is created or updated (Toggl only supports workspace updated events).",
  aiMetadata: {
    description: "Fires when a workspace accessible to the authenticated user is created or modified, delivering the workspace record. Polls Toggl periodically and emits each workspace whose last-modified timestamp is new since the previous poll."
  },
  props: {},
  sampleData: {
    id: 20763798,
    organization_id: 20764737,
    name: "Workspace",
    premium: !0,
    business_ws: !0,
    admin: !0,
    role: "admin",
    suspended_at: null,
    server_deleted_at: null,
    default_hourly_rate: null,
    rate_last_updated: null,
    default_currency: "USD",
    only_admins_may_create_projects: !1,
    only_admins_may_create_tags: !1,
    only_admins_see_team_dashboard: !1,
    projects_billable_by_default: !0,
    projects_private_by_default: !0,
    projects_enforce_billable: !1,
    limit_public_project_data: !1,
    last_modified: "2025-09-01T00:00:00Z",
    reports_collapse: !0,
    rounding: 1,
    rounding_minutes: 0,
    api_token: "72565784d2250b9ba2f2d61039ba9fee",
    at: "2025-09-01T09:23:02+00:00",
    logo_url: "https://assets.track.toggl.com/images/workspace.jpg",
    ical_enabled: !0,
    working_hours_in_minutes: null,
    active_project_count: 1
  },
  type: import_pieces_framework16.TriggerStrategy.POLLING,
  async onEnable(context) {
    await import_pieces_common16.pollingHelper.onEnable(polling, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async onDisable(context) {
    await import_pieces_common16.pollingHelper.onDisable(polling, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async run(context) {
    return await import_pieces_common16.pollingHelper.poll(polling, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue,
      files: context.files
    });
  },
  async test(context) {
    return await import_pieces_common16.pollingHelper.test(polling, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue,
      files: context.files
    });
  }
});

// ../ap/packages/pieces/community/toggl-track/src/lib/triggers/new-project.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework"), import_pieces_common17 = require("@activepieces/pieces-common");
var newProject = (0, import_pieces_framework17.createTrigger)({
  auth: togglTrackAuth,
  name: "new_project",
  classification: "READ",
  displayName: "New Project",
  description: "Fires when a new project is added.",
  aiMetadata: {
    description: "Fires when a new project is created in the configured Toggl Track workspace, delivering the created project (id, name, client association, billing/color settings). Delivered via a Toggl webhook the user must set up manually."
  },
  props: {
    workspace_id: togglCommon.workspace_id,
    setupInstructions: import_pieces_framework17.Property.MarkDown({
      value: generateTogglWebhookInstructions(
        TOGGL_WEBHOOK_EVENTS.PROJECT_CREATED,
        "New Project",
        "Create a test project to ensure events are received",
        `This trigger will fire when projects are created and will include:
- Project ID and details
- Workspace information
- Client association (if any)
- Creation timestamp
- Creator information
- Project settings (color, billable status, etc.)`
      )
    })
  },
  sampleData: {
    id: 123456789,
    workspace_id: 987654,
    client_id: 456789,
    name: "New Website Development",
    is_private: !1,
    active: !0,
    at: "2025-08-29T10:15:30+00:00",
    color: "#3750b5",
    billable: !0,
    template: !1,
    auto_estimates: !1,
    estimated_hours: 40,
    rate: 75,
    rate_last_updated: null,
    currency: "USD",
    recurring: !1,
    recurring_parameters: null,
    current_period: null,
    fixed_fee: null,
    actual_hours: 0,
    actual_seconds: 0
  },
  type: import_pieces_framework17.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let payload = context.payload.body;
    if ((payload == null ? void 0 : payload.payload) === "ping" || payload != null && payload.validation_code || payload != null && payload.validation_code_url) {
      try {
        await import_pieces_common17.httpClient.sendRequest({
          method: import_pieces_common17.HttpMethod.GET,
          url: payload.validation_code_url,
          headers: {
            "Content-Type": "application/json"
          }
        });
      } catch (error) {
        console.error("Failed to auto-validate webhook:", error);
      }
      return [
        {
          message: "Webhook validation successful",
          timestamp: (/* @__PURE__ */ new Date()).toISOString(),
          status: "validated"
        }
      ];
    }
    return [payload];
  }
});

// ../ap/packages/pieces/community/toggl-track/src/lib/triggers/new-task.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework"), import_pieces_common18 = require("@activepieces/pieces-common");
var newTask = (0, import_pieces_framework18.createTrigger)({
  auth: togglTrackAuth,
  name: "new_task",
  classification: "READ",
  displayName: "New Task",
  description: "Fires when a new task is created.",
  aiMetadata: {
    description: "Fires when a new task is created in the configured Toggl Track workspace, delivering the created task (id, name, project association, estimated time). Can be narrowed to a single project via the optional project filter. Delivered via a Toggl webhook the user must set up manually."
  },
  props: {
    workspace_id: togglCommon.workspace_id,
    optional_project_id: togglCommon.optional_project_id,
    setupInstructions: import_pieces_framework18.Property.MarkDown({
      value: generateTogglWebhookInstructions(
        TOGGL_WEBHOOK_EVENTS.TASK_CREATED,
        "New Task",
        "Create a test task to ensure events are received",
        `This trigger will fire when tasks are created and will include:
- Task ID and details
- Project association
- Workspace information
- Creation timestamp
- Creator information
- Task settings (estimated time, active status, etc.)

**Note:** You can filter tasks by project using the optional project filter above.`
      )
    })
  },
  sampleData: {
    id: 789123456,
    name: "Design homepage mockup",
    project_id: 123456789,
    workspace_id: 987654,
    user_id: 6,
    estimated_seconds: 7200,
    active: !0,
    at: "2025-08-29T10:15:30+00:00",
    tracked_seconds: 0
  },
  type: import_pieces_framework18.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let payload = context.payload.body;
    if ((payload == null ? void 0 : payload.payload) === "ping" || payload != null && payload.validation_code || payload != null && payload.validation_code_url) {
      try {
        await import_pieces_common18.httpClient.sendRequest({
          method: import_pieces_common18.HttpMethod.GET,
          url: payload.validation_code_url,
          headers: {
            "Content-Type": "application/json"
          }
        });
      } catch (error) {
        console.error("Failed to auto-validate webhook:", error);
      }
      return [
        {
          message: "Webhook validation successful",
          timestamp: (/* @__PURE__ */ new Date()).toISOString(),
          status: "validated"
        }
      ];
    }
    return context.propsValue.optional_project_id && (payload != null && payload.project_id) && payload.project_id !== context.propsValue.optional_project_id ? [] : [payload];
  }
});

// ../ap/packages/pieces/community/toggl-track/src/lib/triggers/new-time-entry.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework"), import_pieces_common19 = require("@activepieces/pieces-common");
var newTimeEntry = (0, import_pieces_framework19.createTrigger)({
  auth: togglTrackAuth,
  name: "new_time_entry",
  classification: "READ",
  displayName: "New Time Entry",
  description: "Fires when a new time entry is added (with optional project/task filter).",
  aiMetadata: {
    description: "Fires when a new time entry is created in the configured Toggl Track workspace, delivering the entry (start/stop, duration, description, tags, project/task, billable). Can be narrowed to a specific project and/or task via the optional filters. Delivered via a Toggl webhook the user must set up manually."
  },
  props: {
    workspace_id: togglCommon.workspace_id,
    optional_project_id: togglCommon.optional_project_id,
    task_id: togglCommon.optional_task_id,
    setupInstructions: import_pieces_framework19.Property.MarkDown({
      value: generateTogglWebhookInstructions(
        TOGGL_WEBHOOK_EVENTS.TIME_ENTRY_CREATED,
        "New Time Entry",
        "Create a test time entry to ensure events are received",
        `This trigger will fire when time entries are created and will include:
- Time entry ID and details
- Start/stop times and duration
- Project and task associations (if any)
- Workspace information
- Description and tags
- Creator information
- Billable status

**Note:** You can filter time entries by project and task using the optional filters above.`
      )
    })
  },
  sampleData: {
    id: 1234567890,
    workspace_id: 987654,
    project_id: 123987456,
    task_id: 789123456,
    billable: !1,
    start: "2025-08-29T11:00:00Z",
    stop: "2025-08-29T11:30:00Z",
    duration: 1800,
    description: "Weekly team meeting",
    tags: ["meeting", "internal"],
    at: "2025-08-29T11:30:00+00:00",
    user_id: 6,
    created_with: "Toggl Track"
  },
  type: import_pieces_framework19.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let payload = context.payload.body;
    if ((payload == null ? void 0 : payload.payload) === "ping" || payload != null && payload.validation_code || payload != null && payload.validation_code_url) {
      try {
        await import_pieces_common19.httpClient.sendRequest({
          method: import_pieces_common19.HttpMethod.GET,
          url: payload.validation_code_url,
          headers: {
            "Content-Type": "application/json"
          }
        });
      } catch (error) {
        console.error("Failed to auto-validate webhook:", error);
      }
      return [
        {
          message: "Webhook validation successful",
          timestamp: (/* @__PURE__ */ new Date()).toISOString(),
          status: "validated"
        }
      ];
    }
    return context.propsValue.optional_project_id && (payload != null && payload.project_id) && payload.project_id !== context.propsValue.optional_project_id ? [] : context.propsValue.task_id && (payload != null && payload.task_id) && payload.task_id !== context.propsValue.task_id ? [] : [payload];
  }
});

// ../ap/packages/pieces/community/toggl-track/src/lib/triggers/new-time-entry-started.ts
var import_pieces_framework20 = require("@activepieces/pieces-framework");
var import_pieces_common20 = require("@activepieces/pieces-common"), polling2 = {
  strategy: import_pieces_common20.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue }) => {
    let authHeader = `Basic ${Buffer.from(`${auth.secret_text}:api_token`).toString("base64")}`;
    return (await import_pieces_common20.httpClient.sendRequest({
      method: import_pieces_common20.HttpMethod.GET,
      url: "https://api.track.toggl.com/api/v9/me/time_entries",
      headers: {
        "Content-Type": "application/json",
        Authorization: authHeader
      }
    })).body.filter((entry) => {
      let isRunning = entry.duration < 0, isInWorkspace = !propsValue.workspace_id || entry.workspace_id === propsValue.workspace_id;
      return isRunning && isInWorkspace;
    }).map((entry) => ({
      epochMilliSeconds: new Date(entry.start).getTime(),
      data: entry
    }));
  }
}, newTimeEntryStarted = (0, import_pieces_framework20.createTrigger)({
  auth: togglTrackAuth,
  name: "new_time_entry_started",
  classification: "READ",
  displayName: "New Time Entry Started",
  description: "Fires when a new time entry is started and is currently running.",
  aiMetadata: {
    description: "Fires when a time entry is started and is currently running (open-ended, negative duration) for the authenticated user, delivering the running entry. Polls Toggl periodically and emits newly started running entries, optionally scoped to the configured workspace."
  },
  props: {
    workspace_id: togglCommon.workspace_id
  },
  sampleData: {
    id: 1234567891,
    workspace_id: 987654,
    project_id: 123987456,
    task_id: null,
    billable: !1,
    start: "2025-08-29T11:15:00Z",
    stop: null,
    duration: -1734567890,
    description: "Working on API integration",
    tags: ["development", "api"],
    at: "2025-08-29T11:15:00+00:00",
    user_id: 6,
    created_with: "Toggl Track"
  },
  type: import_pieces_framework20.TriggerStrategy.POLLING,
  async onEnable(context) {
    await import_pieces_common20.pollingHelper.onEnable(polling2, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async onDisable(context) {
    await import_pieces_common20.pollingHelper.onDisable(polling2, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async run(context) {
    return await import_pieces_common20.pollingHelper.poll(polling2, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue,
      files: context.files
    });
  },
  async test(context) {
    return await import_pieces_common20.pollingHelper.test(polling2, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue,
      files: context.files
    });
  }
});

// ../ap/packages/pieces/community/toggl-track/src/lib/triggers/new-tag.ts
var import_pieces_framework21 = require("@activepieces/pieces-framework"), import_pieces_common21 = require("@activepieces/pieces-common");
var newTag = (0, import_pieces_framework21.createTrigger)({
  auth: togglTrackAuth,
  name: "new_tag",
  classification: "READ",
  displayName: "New Tag",
  description: "Triggers when a new tag is created",
  aiMetadata: {
    description: "Fires when a new tag is created in the configured Toggl Track workspace, delivering the created tag (id, name, workspace). Delivered via a Toggl webhook the user must set up manually."
  },
  props: {
    workspace_id: togglCommon.workspace_id,
    setupInstructions: import_pieces_framework21.Property.MarkDown({
      value: generateTogglWebhookInstructions(
        TOGGL_WEBHOOK_EVENTS.TAG_CREATED,
        "New Tag",
        "Create a test tag to ensure events are received",
        `This trigger will fire when tags are created and will include:
- Tag ID and details
- Tag name
- Workspace information
- Creation timestamp
- Creator information`
      )
    })
  },
  sampleData: {
    id: 456789,
    name: "development",
    workspace_id: 987654,
    at: "2025-08-29T10:15:30+00:00",
    creator_id: 6
  },
  type: import_pieces_framework21.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let payload = context.payload.body;
    if ((payload == null ? void 0 : payload.payload) === "ping" || payload != null && payload.validation_code || payload != null && payload.validation_code_url) {
      try {
        await import_pieces_common21.httpClient.sendRequest({
          method: import_pieces_common21.HttpMethod.GET,
          url: payload.validation_code_url,
          headers: {
            "Content-Type": "application/json"
          }
        });
      } catch (error) {
        console.error("Failed to auto-validate webhook:", error);
      }
      return [
        {
          message: "Webhook validation successful",
          timestamp: (/* @__PURE__ */ new Date()).toISOString(),
          status: "validated"
        }
      ];
    }
    return [payload];
  }
});

// ../ap/packages/pieces/community/toggl-track/src/index.ts
var validateAuth = async ({ auth }) => {
  try {
    return await import_pieces_common22.httpClient.sendRequest({
      method: import_pieces_common22.HttpMethod.GET,
      url: "https://api.track.toggl.com/api/v9/me",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Basic ${Buffer.from(`${auth}:api_token`).toString(
          "base64"
        )}`
      }
    }), {
      valid: !0
    };
  } catch (e) {
    return {
      valid: !1,
      error: "Invalid API token."
    };
  }
}, togglTrackAuth = import_pieces_framework22.PieceAuth.SecretText({
  displayName: "API Token",
  description: `
  1. Log in to your Toggl Track account.
  2. Go to your **Profile settings**.
  3. Find the **API Token** at the bottom of the page and copy it.
  `,
  required: !0,
  validate: validateAuth
}), togglTrack = (0, import_pieces_framework22.createPiece)({
  displayName: "Toggl Track",
  description: "Toggl Track is a time tracking application that allows users to track their daily activities across different platforms.",
  auth: togglTrackAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/toggl-track.png",
  categories: [import_pieces_framework23.PieceCategory.PRODUCTIVITY],
  authors: ["Pranith124", "onyedikachi-david"],
  actions: [
    createClient,
    createProject,
    createTask,
    createTag,
    createTimeEntry,
    startTimeEntry,
    stopTimeEntry,
    findUser,
    findProject,
    findTask,
    findClient,
    findTag,
    findTimeEntry
  ],
  triggers: [
    newClient,
    newWorkspace,
    newProject,
    newTask,
    newTimeEntry,
    newTimeEntryStarted,
    newTag
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  togglTrack,
  togglTrackAuth
});
