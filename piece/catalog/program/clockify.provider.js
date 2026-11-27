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

// ../ap/packages/pieces/community/clockify/src/index.ts
var index_exports = {};
__export(index_exports, {
  clockify: () => clockify
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common14 = require("@activepieces/pieces-common"), import_pieces_framework16 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/clockify/src/lib/actions/create-task.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/clockify/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/clockify/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.clockify.me/api/v1";
async function clockifyApiCall({
  apiKey,
  method,
  resourceUri,
  query,
  body
}) {
  let qs = {};
  if (query)
    for (let [key, value] of Object.entries(query))
      value != null && (qs[key] = String(value));
  let request = {
    method,
    url: BASE_URL + resourceUri,
    headers: {
      "X-Api-Key": apiKey
    },
    queryParams: qs,
    body
  };
  return (await import_pieces_common.httpClient.sendRequest(request)).body;
}

// ../ap/packages/pieces/community/clockify/src/lib/auth.ts
var clockifyAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "You can obtain your API key by navigating to **Preferences->Advanced**.",
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await clockifyApiCall({
        apiKey: auth,
        method: import_pieces_common2.HttpMethod.GET,
        resourceUri: "/user"
      }), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API Key."
      };
    }
  }
});

// ../ap/packages/pieces/community/clockify/src/lib/common/props.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var workspaceId = (params) => import_pieces_framework2.Property.Dropdown({
  auth: clockifyAuth,
  displayName: params.displayName,
  description: params.description,
  required: params.required,
  refreshers: [],
  options: async ({ auth }) => auth ? {
    disabled: !1,
    options: (await clockifyApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common3.HttpMethod.GET,
      resourceUri: "/workspaces"
    })).map((workspace) => ({
      label: workspace.name,
      value: workspace.id
    }))
  } : {
    disabled: !0,
    options: [],
    placeholder: "Please connect your account first."
  }
}), projectId = (params) => import_pieces_framework2.Property.Dropdown({
  auth: clockifyAuth,
  displayName: params.displayName,
  description: params.description,
  required: params.required,
  refreshers: ["workspaceId"],
  options: async ({ auth, workspaceId: workspaceId2 }) => auth ? workspaceId2 ? {
    disabled: !1,
    options: (await clockifyApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common3.HttpMethod.GET,
      resourceUri: `/workspaces/${workspaceId2}/projects`
    })).map((project) => ({
      label: project.name,
      value: project.id
    }))
  } : {
    disabled: !0,
    options: [],
    placeholder: "Please select workspace first."
  } : {
    disabled: !0,
    options: [],
    placeholder: "Please connect your account first."
  }
}), assigneeIds = (params) => import_pieces_framework2.Property.MultiSelectDropdown({
  auth: clockifyAuth,
  displayName: params.displayName,
  description: params.description,
  required: params.required,
  refreshers: ["workspaceId"],
  options: async ({ auth, workspaceId: workspaceId2 }) => auth ? workspaceId2 ? {
    disabled: !1,
    options: (await clockifyApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common3.HttpMethod.GET,
      resourceUri: `/workspaces/${workspaceId2}/users`
    })).map((user) => ({
      label: user.email,
      value: user.id
    }))
  } : {
    disabled: !0,
    options: [],
    placeholder: "Please select workspace first."
  } : {
    disabled: !0,
    options: [],
    placeholder: "Please connect your account first."
  }
}), taskId = (params) => import_pieces_framework2.Property.Dropdown({
  auth: clockifyAuth,
  displayName: params.displayName,
  description: params.description,
  required: params.required,
  refreshers: ["workspaceId", "projectId"],
  options: async ({ auth, workspaceId: workspaceId2, projectId: projectId2 }) => auth ? workspaceId2 ? projectId2 ? {
    disabled: !1,
    options: (await clockifyApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common3.HttpMethod.GET,
      resourceUri: `/workspaces/${workspaceId2}/projects/${projectId2}/tasks`
    })).map((task) => ({
      label: task.name,
      value: task.id
    }))
  } : {
    disabled: !0,
    options: [],
    placeholder: "Please select project first."
  } : {
    disabled: !0,
    options: [],
    placeholder: "Please select workspace first."
  } : {
    disabled: !0,
    options: [],
    placeholder: "Please connect your account first."
  }
}), tagIds = (params) => import_pieces_framework2.Property.MultiSelectDropdown({
  auth: clockifyAuth,
  displayName: params.displayName,
  description: params.description,
  required: params.required,
  refreshers: ["workspaceId"],
  options: async ({ auth, workspaceId: workspaceId2 }) => auth ? workspaceId2 ? {
    disabled: !1,
    options: (await clockifyApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common3.HttpMethod.GET,
      resourceUri: `/workspaces/${workspaceId2}/tags`
    })).map((tag) => ({
      label: tag.name,
      value: tag.id
    }))
  } : {
    disabled: !0,
    options: [],
    placeholder: "Please select workspace first."
  } : {
    disabled: !0,
    options: [],
    placeholder: "Please connect your account first."
  }
});

// ../ap/packages/pieces/community/clockify/src/lib/actions/create-task.ts
var createTaskAction = (0, import_pieces_framework3.createAction)({
  auth: clockifyAuth,
  name: "create-task",
  displayName: "Create Task",
  description: "Creates a new in a specific project.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new task inside a specific Clockify project, optionally setting a status and assignees. Requires the workspace and project to target. Not idempotent: each call creates a separate task even with identical inputs.",
    idempotent: !1
  },
  props: {
    workspaceId: workspaceId({
      displayName: "Workspace",
      required: !0
    }),
    projectId: projectId({
      displayName: "Project",
      required: !0
    }),
    name: import_pieces_framework3.Property.ShortText({
      displayName: "Task Name",
      required: !0
    }),
    status: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Status",
      required: !1,
      options: {
        disabled: !1,
        options: [
          {
            label: "Active",
            value: "ACTIVE"
          },
          {
            label: "Done",
            value: "DONE"
          },
          {
            label: "All",
            value: "ALL"
          }
        ]
      }
    }),
    assigneeIds: assigneeIds({
      displayName: "Assignee",
      required: !1
    })
  },
  async run(context) {
    var _a;
    let { workspaceId: workspaceId2, projectId: projectId2, name, status } = context.propsValue, assigneeIds2 = (_a = context.propsValue.assigneeIds) != null ? _a : [];
    return await clockifyApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common4.HttpMethod.POST,
      resourceUri: `/workspaces/${workspaceId2}/projects/${projectId2}/tasks`,
      body: {
        name,
        status,
        assigneeIds: assigneeIds2
      }
    });
  }
});

// ../ap/packages/pieces/community/clockify/src/lib/actions/create-time-entry.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var createTimeEntryAction = (0, import_pieces_framework4.createAction)({
  auth: clockifyAuth,
  name: "create-time-entry",
  displayName: "Create Time Entry",
  description: "Creates a new time entry.",
  audience: "both",
  aiMetadata: {
    description: "Logs a completed time entry on a Clockify workspace for an explicit start and end datetime, optionally tied to a project, task, tags, and billable flag. Use to record past or fixed-duration work rather than to start a live timer (use Start Timer for that). Not idempotent: each call adds a new entry.",
    idempotent: !1
  },
  props: {
    workspaceId: workspaceId({
      displayName: "Workspace",
      required: !0
    }),
    start: import_pieces_framework4.Property.DateTime({
      displayName: "Start Datetime",
      required: !0
    }),
    end: import_pieces_framework4.Property.DateTime({
      displayName: "End Datetime",
      required: !0
    }),
    description: import_pieces_framework4.Property.LongText({
      displayName: "Entry Description",
      required: !1
    }),
    projectId: projectId({
      displayName: "Project",
      required: !1
    }),
    taskId: taskId({
      displayName: "Task",
      required: !1
    }),
    billable: import_pieces_framework4.Property.Checkbox({
      displayName: "Billable",
      required: !1
    }),
    tagIds: tagIds({
      displayName: "Tags",
      required: !1
    })
  },
  async run(context) {
    var _a;
    let { workspaceId: workspaceId2, projectId: projectId2, start, end, description, billable, taskId: taskId2 } = context.propsValue, tagIds2 = (_a = context.propsValue.tagIds) != null ? _a : [];
    return await clockifyApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common5.HttpMethod.POST,
      resourceUri: `/workspaces/${workspaceId2}/time-entries`,
      body: {
        billable,
        description,
        start,
        end,
        projectId: projectId2,
        taskId: taskId2,
        tagIds: tagIds2,
        type: "REGULAR"
      }
    });
  }
});

// ../ap/packages/pieces/community/clockify/src/lib/actions/find-running-timer.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");
var findRunningTimerAction = (0, import_pieces_framework5.createAction)({
  auth: clockifyAuth,
  name: "find-running-timer",
  displayName: "Find Running Timer",
  description: "Finds currently running timer on specified workspace.",
  audience: "both",
  aiMetadata: {
    description: "Returns the in-progress (currently running) time entries for the authenticated user on the given workspace. Use to check whether a timer is active before starting or stopping one. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    workspaceId: workspaceId({
      displayName: "Workspace",
      required: !0
    })
  },
  async run(context) {
    let { workspaceId: workspaceId2 } = context.propsValue;
    return await clockifyApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common6.HttpMethod.GET,
      resourceUri: `/workspaces/${workspaceId2}/time-entries/status/in-progress`
    });
  }
});

// ../ap/packages/pieces/community/clockify/src/lib/actions/find-task.ts
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var findTaskAction = (0, import_pieces_framework6.createAction)({
  auth: clockifyAuth,
  name: "find-task",
  displayName: "Find Task",
  description: "Finds an existing task in a specific project.",
  audience: "both",
  aiMetadata: {
    description: "Searches for tasks by name within a specific Clockify project and returns matches. Toggle Exact Match for strict name matching versus a looser substring search. Use to resolve a task id or confirm a task exists before creating time entries. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    workspaceId: workspaceId({
      displayName: "Workspace",
      required: !0
    }),
    projectId: projectId({
      displayName: "Project",
      required: !0
    }),
    name: import_pieces_framework6.Property.ShortText({
      displayName: "Task Name",
      required: !0
    }),
    exactMatch: import_pieces_framework6.Property.Checkbox({
      displayName: "Exact Match ?",
      required: !1
    })
  },
  async run(context) {
    let { workspaceId: workspaceId2, projectId: projectId2, name, exactMatch } = context.propsValue;
    return await clockifyApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common7.HttpMethod.GET,
      resourceUri: `/workspaces/${workspaceId2}/projects/${projectId2}/tasks`,
      query: {
        name,
        "strict-name-search": exactMatch ? "true" : "false"
      }
    });
  }
});

// ../ap/packages/pieces/community/clockify/src/lib/actions/find-time-entry.ts
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework");
var findTimeEntryAction = (0, import_pieces_framework7.createAction)({
  auth: clockifyAuth,
  name: "find-time-entry",
  displayName: "Find Time Entry",
  description: "Finds a time entry by description, start datetime or end datetime.",
  audience: "both",
  aiMetadata: {
    description: "Searches the authenticated user's time entries on a workspace, optionally filtered by description, start/end datetime, project, or task. With no filters it returns recent entries; supplied filters narrow the results. Use to locate an existing entry or verify logged time. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    workspaceId: workspaceId({
      displayName: "Workspace",
      required: !0
    }),
    start: import_pieces_framework7.Property.DateTime({
      displayName: "Start Datetime",
      required: !1
    }),
    end: import_pieces_framework7.Property.DateTime({
      displayName: "End Datetime",
      required: !1
    }),
    description: import_pieces_framework7.Property.LongText({
      displayName: "Entry Description",
      required: !1
    }),
    projectId: projectId({
      displayName: "Project",
      required: !1
    }),
    taskId: taskId({
      displayName: "Task",
      required: !1
    })
  },
  async run(context) {
    let { workspaceId: workspaceId2, projectId: projectId2, start, end, description, taskId: taskId2 } = context.propsValue, userId = (await clockifyApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common8.HttpMethod.GET,
      resourceUri: "/user"
    })).id, qs = { hydrated: "true" };
    return description && (qs.description = description), start && (qs.start = start), end && (qs.end = end), projectId2 && (qs.project = projectId2), taskId2 && (qs.task = taskId2), await clockifyApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common8.HttpMethod.GET,
      resourceUri: `/workspaces/${workspaceId2}/user/${userId}/time-entries`,
      query: qs
    });
  }
});

// ../ap/packages/pieces/community/clockify/src/lib/actions/start-timer.ts
var import_pieces_common9 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework");
var startTimerAction = (0, import_pieces_framework8.createAction)({
  auth: clockifyAuth,
  name: "start-timer",
  displayName: "Start Timer",
  description: "Starts a new time entry.",
  audience: "both",
  aiMetadata: {
    description: "Starts a live, currently-running timer on a Clockify workspace from the current moment, optionally tied to a project, task, tags, and billable flag. Use to begin tracking ongoing work; use Stop Timer to end it and Create Time Entry to log a finished interval. Not idempotent: each call starts a new running entry.",
    idempotent: !1
  },
  props: {
    workspaceId: workspaceId({
      displayName: "Workspace",
      required: !0
    }),
    description: import_pieces_framework8.Property.LongText({
      displayName: "Entry Description",
      required: !1
    }),
    projectId: projectId({
      displayName: "Project",
      required: !1
    }),
    taskId: taskId({
      displayName: "Task",
      required: !1
    }),
    billable: import_pieces_framework8.Property.Checkbox({
      displayName: "Billable",
      required: !1
    }),
    tagIds: tagIds({
      displayName: "Tags",
      required: !1
    })
  },
  async run(context) {
    var _a;
    let { workspaceId: workspaceId2, projectId: projectId2, description, billable, taskId: taskId2 } = context.propsValue, tagIds2 = (_a = context.propsValue.tagIds) != null ? _a : [];
    return await clockifyApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common9.HttpMethod.POST,
      resourceUri: `/workspaces/${workspaceId2}/time-entries`,
      body: {
        billable,
        description,
        start: (/* @__PURE__ */ new Date()).toISOString(),
        projectId: projectId2,
        taskId: taskId2,
        tagIds: tagIds2,
        type: "REGULAR"
      }
    });
  }
});

// ../ap/packages/pieces/community/clockify/src/lib/actions/stop-timer.ts
var import_pieces_common10 = require("@activepieces/pieces-common"), import_pieces_framework9 = require("@activepieces/pieces-framework");
var stopTimerAction = (0, import_pieces_framework9.createAction)({
  auth: clockifyAuth,
  name: "stop-timer",
  displayName: "Stop Timer",
  description: "Stops currently running timer on specified workspace.",
  audience: "both",
  aiMetadata: {
    description: "Stops the authenticated user's currently running timer on the given workspace by setting its end time to now. Use after Start Timer to finalize tracked work. Not idempotent in effect: if no timer is running there is nothing to stop, and the stopped time depends on when it is called.",
    idempotent: !1
  },
  props: {
    workspaceId: workspaceId({
      displayName: "Workspace",
      required: !0
    })
  },
  async run(context) {
    let { workspaceId: workspaceId2 } = context.propsValue, userId = (await clockifyApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common10.HttpMethod.GET,
      resourceUri: "/user"
    })).id;
    return await clockifyApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common10.HttpMethod.PATCH,
      resourceUri: `/workspaces/${workspaceId2}/user/${userId}/time-entries`,
      body: {
        end: (/* @__PURE__ */ new Date()).toISOString()
      }
    });
  }
});

// ../ap/packages/pieces/community/clockify/src/lib/triggers/new-task.ts
var import_pieces_common11 = require("@activepieces/pieces-common"), import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_framework11 = require("@activepieces/pieces-framework");
var TRIGGER_KEY = "new-task-trigger", newTaskTrigger = (0, import_pieces_framework10.createTrigger)({
  auth: clockifyAuth,
  name: "new-task",
  displayName: "New Task",
  description: "Triggers when a new task is created in specified project.",
  aiMetadata: {
    description: "Fires when a new task is created in the specified Clockify project, emitting the created task. Scoped to one workspace and project."
  },
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  props: {
    workspaceId: workspaceId({
      displayName: "Workspace",
      required: !0
    }),
    projectId: projectId({
      displayName: "Project",
      required: !0
    })
  },
  async onEnable(context) {
    let { workspaceId: workspaceId2, projectId: projectId2 } = context.propsValue, response = await clockifyApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common11.HttpMethod.POST,
      resourceUri: `/workspaces/${workspaceId2}/webhooks`,
      body: {
        url: context.webhookUrl,
        webhookEvent: "NEW_TASK",
        triggerSourceType: "PROJECT_ID",
        triggerSource: [projectId2]
      }
    });
    await context.store.put(TRIGGER_KEY, response.id);
  },
  async onDisable(context) {
    let { workspaceId: workspaceId2 } = context.propsValue, webhookId = await context.store.get(TRIGGER_KEY);
    (0, import_pieces_framework11.isNil)(webhookId) || await clockifyApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common11.HttpMethod.DELETE,
      resourceUri: `/workspaces/${workspaceId2}/webhooks/${webhookId}`
    });
  },
  async test(context) {
    let { workspaceId: workspaceId2, projectId: projectId2 } = context.propsValue;
    return await clockifyApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common11.HttpMethod.GET,
      resourceUri: `/workspaces/${workspaceId2}/projects/${projectId2}/tasks`,
      query: {
        page: "1",
        "page-size": 5
      }
    });
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    id: "684538940300f917a02f642f",
    name: "Test",
    projectId: "68444b15551a9934b5034263",
    workspaceId: "684446430300f917a02c198b",
    assigneeIds: [],
    assigneeId: "",
    userGroupIds: [],
    estimate: "PT0S",
    status: "ACTIVE",
    budgetEstimate: 0,
    billable: !0,
    hourlyRate: null,
    costRate: null,
    progress: null,
    duration: "PT0S"
  }
});

// ../ap/packages/pieces/community/clockify/src/lib/triggers/new-time-entry.ts
var import_pieces_common12 = require("@activepieces/pieces-common"), import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_framework13 = require("@activepieces/pieces-framework");
var TRIGGER_KEY2 = "new-time-entry-trigger", newTimeEntryTrigger = (0, import_pieces_framework12.createTrigger)({
  auth: clockifyAuth,
  name: "new-time-entry",
  displayName: "New Time Entry",
  description: "Triggers when a new time entry is created.",
  aiMetadata: {
    description: "Fires when a new time entry is created, emitting the entry. Scope is narrowed by the optional project and task: with neither set it watches the whole workspace, with a project it watches that project, and with a task it watches that task."
  },
  type: import_pieces_framework12.TriggerStrategy.WEBHOOK,
  props: {
    workspaceId: workspaceId({
      displayName: "Workspace",
      required: !0
    }),
    projectId: projectId({
      displayName: "Project",
      required: !1
    }),
    taskId: taskId({
      displayName: "Task",
      required: !1
    })
  },
  async onEnable(context) {
    let { workspaceId: workspaceId2, projectId: projectId2, taskId: taskId2 } = context.propsValue, payload = {
      url: context.webhookUrl,
      webhookEvent: "NEW_TIME_ENTRY"
    };
    workspaceId2 && (payload.triggerSourceType = "WORKSPACE_ID", payload.triggerSource = [workspaceId2]), projectId2 && (payload.triggerSourceType = "PROJECT_ID", payload.triggerSource = [projectId2]), taskId2 && (payload.triggerSourceType = "TASK_ID", payload.triggerSource = [taskId2]);
    let response = await clockifyApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common12.HttpMethod.POST,
      resourceUri: `/workspaces/${workspaceId2}/webhooks`,
      body: payload
    });
    await context.store.put(TRIGGER_KEY2, response.id);
  },
  async onDisable(context) {
    let { workspaceId: workspaceId2 } = context.propsValue, webhookId = await context.store.get(TRIGGER_KEY2);
    (0, import_pieces_framework13.isNil)(webhookId) || await clockifyApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common12.HttpMethod.DELETE,
      resourceUri: `/workspaces/${workspaceId2}/webhooks/${webhookId}`
    });
  },
  async test(context) {
    let { workspaceId: workspaceId2, projectId: projectId2, taskId: taskId2 } = context.propsValue, userId = (await clockifyApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common12.HttpMethod.GET,
      resourceUri: "/user"
    })).id, qs = { hydrated: "true", page: "1", "page-size": "5" };
    return projectId2 && (qs.project = projectId2), taskId2 && (qs.task = taskId2), await clockifyApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common12.HttpMethod.GET,
      resourceUri: `/workspaces/${workspaceId2}/user/${userId}/time-entries`,
      query: qs
    });
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    id: "68453d0cf0c88d522704ab76",
    description: "Test",
    userId: "684446430300f917a02c198a",
    billable: !0,
    projectId: "68444b15551a9934b5034263",
    timeInterval: {
      start: "2025-06-16T07:05:00Z",
      end: "2025-06-16T18:53:00Z",
      duration: "PT11H48M",
      timeZone: "Asia/Calcutta",
      offsetStart: 19800,
      offsetEnd: 19800,
      zonedStart: "2025-06-16T12:35:00",
      zonedEnd: "2025-06-17T00:23:00"
    },
    workspaceId: "684446430300f917a02c198b",
    isLocked: !1,
    hourlyRate: null,
    costRate: null,
    customFieldValues: [],
    type: "REGULAR",
    kioskId: null,
    approvalStatus: null,
    projectCurrency: null,
    currentlyRunning: !1,
    project: {
      name: "Test",
      clientId: "",
      workspaceId: "684446430300f917a02c198b",
      billable: !0,
      estimate: {
        estimate: "PT0S",
        type: "AUTO"
      },
      color: "#039BE5",
      archived: !1,
      clientName: "",
      duration: "PT47H12M45S",
      note: "",
      activeEstimate: "NONE",
      timeEstimate: {
        includeNonBillable: !0,
        estimate: 0,
        type: "AUTO",
        resetOption: null
      },
      budgetEstimate: null,
      id: "68444b15551a9934b5034263",
      public: !0,
      template: !1
    },
    task: {
      name: "test",
      projectId: "68444b15551a9934b5034263",
      assigneeId: "684446430300f917a02c198a",
      assigneeIds: ["684446430300f917a02c198a"],
      userGroupIds: [],
      estimate: "PT0S",
      status: "ACTIVE",
      workspaceId: "684446430300f917a02c198b",
      budgetEstimate: 0,
      billable: !0,
      hourlyRate: null,
      costRate: null,
      auditMetadata: {
        updatedAt: 1749306307e3
      },
      id: "68444badb7688b61f151284d",
      duration: "PT35H24M41S"
    },
    user: {
      id: "684446430300f917a02c198a",
      name: "johndoe",
      status: "ACTIVE"
    },
    tags: [
      {
        name: "new",
        workspaceId: "684446430300f917a02c198b",
        archived: !1,
        id: "68452a1b0300f917a02f30bc"
      }
    ]
  }
});

// ../ap/packages/pieces/community/clockify/src/lib/triggers/new-timer-started.ts
var import_pieces_common13 = require("@activepieces/pieces-common"), import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_framework15 = require("@activepieces/pieces-framework");
var TRIGGER_KEY3 = "new-timer-started-trigger", newTimerStartedTrigger = (0, import_pieces_framework14.createTrigger)({
  auth: clockifyAuth,
  name: "new-timer-started",
  displayName: "New Timer Started",
  description: "Triggers when a new entry is started and running.",
  aiMetadata: {
    description: "Fires when a timer is started and begins running on the specified workspace, emitting the newly started time entry. Use to react to live timers as they begin."
  },
  type: import_pieces_framework14.TriggerStrategy.WEBHOOK,
  props: {
    workspaceId: workspaceId({
      displayName: "Workspace",
      required: !0
    })
  },
  async onEnable(context) {
    let { workspaceId: workspaceId2 } = context.propsValue, response = await clockifyApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common13.HttpMethod.POST,
      resourceUri: `/workspaces/${workspaceId2}/webhooks`,
      body: {
        url: context.webhookUrl,
        webhookEvent: "NEW_TIMER_STARTED",
        triggerSourceType: "WORKSPACE_ID",
        triggerSource: [workspaceId2]
      }
    });
    await context.store.put(TRIGGER_KEY3, response.id);
  },
  async onDisable(context) {
    let { workspaceId: workspaceId2 } = context.propsValue, webhookId = await context.store.get(TRIGGER_KEY3);
    (0, import_pieces_framework15.isNil)(webhookId) || await clockifyApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common13.HttpMethod.DELETE,
      resourceUri: `/workspaces/${workspaceId2}/webhooks/${webhookId}`
    });
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    id: "684538940300f917a02f642f",
    name: "Test",
    projectId: "68444b15551a9934b5034263",
    workspaceId: "684446430300f917a02c198b",
    assigneeIds: [],
    assigneeId: "",
    userGroupIds: [],
    estimate: "PT0S",
    status: "ACTIVE",
    budgetEstimate: 0,
    billable: !0,
    hourlyRate: null,
    costRate: null,
    progress: null,
    duration: "PT0S"
  }
});

// ../ap/packages/pieces/community/clockify/src/index.ts
var clockify = (0, import_pieces_framework16.createPiece)({
  displayName: "Clockify",
  auth: clockifyAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/clockify.png",
  authors: ["rimjhimyadav", "kishanprmr"],
  actions: [
    createTaskAction,
    createTimeEntryAction,
    startTimerAction,
    stopTimerAction,
    findTaskAction,
    findTimeEntryAction,
    findRunningTimerAction,
    (0, import_pieces_common14.createCustomApiCallAction)({
      auth: clockifyAuth,
      baseUrl: () => BASE_URL,
      authMapping: async (auth) => ({
        "X-Api-Key": auth.secret_text
      })
    })
  ],
  triggers: [newTaskTrigger, newTimeEntryTrigger, newTimerStartedTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  clockify
});
