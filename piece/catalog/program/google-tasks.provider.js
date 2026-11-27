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

// ../ap/packages/pieces/community/google-tasks/src/index.ts
var index_exports = {};
__export(index_exports, {
  googleTasks: () => googleTasks
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common11 = require("@activepieces/pieces-common"), import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_framework15 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/google-tasks/src/lib/actions/new-task.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/google-tasks/src/lib/common/index.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/google-tasks/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), googleTasksAuth = import_pieces_framework.PieceAuth.OAuth2({
  description: "",
  authUrl: "https://accounts.google.com/o/oauth2/auth",
  tokenUrl: "https://oauth2.googleapis.com/token",
  required: !0,
  scope: ["https://www.googleapis.com/auth/tasks", "email"]
});

// ../ap/packages/pieces/community/google-tasks/src/lib/common/index.ts
var googleTasksCommon = {
  baseUrl: "https://tasks.googleapis.com",
  /**
   * @property Target task list ID where the new task will be created
   */
  tasksList: import_pieces_framework2.Property.Dropdown({
    displayName: "Tasks List",
    auth: googleTasksAuth,
    refreshers: [],
    required: !0,
    options: async ({ auth }) => auth ? {
      disabled: !1,
      options: (await getLists(auth)).map((list) => ({
        label: list.title,
        value: list.id
      }))
    } : {
      disabled: !0,
      placeholder: "Please connect your account first",
      options: []
    }
  }),
  title: import_pieces_framework2.Property.ShortText({
    displayName: "Title",
    required: !0
  }),
  notes: import_pieces_framework2.Property.LongText({
    displayName: "Notes",
    required: !1
  }),
  due: import_pieces_framework2.Property.DateTime({
    displayName: "Due Date",
    description: "Due date of the task (YYYY-MM-DD)",
    required: !1
  }),
  completed: import_pieces_framework2.Property.Checkbox({
    displayName: "Completed",
    description: "Mark task as completed",
    required: !1
  })
};
async function getLists(authProp) {
  let request = {
    method: import_pieces_common.HttpMethod.GET,
    url: `${googleTasksCommon.baseUrl}/tasks/v1/users/@me/lists`,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token: authProp.access_token
    }
  };
  return (await import_pieces_common.httpClient.sendRequest(request)).body.items;
}
async function getTasks(authProp, tasklist, params) {
  var _a;
  let request = {
    method: import_pieces_common.HttpMethod.GET,
    url: `${googleTasksCommon.baseUrl}/tasks/v1/lists/${tasklist}/tasks`,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token: authProp.access_token
    },
    queryParams: params
  };
  return (_a = (await import_pieces_common.httpClient.sendRequest(request)).body.items) != null ? _a : [];
}
async function createTask(authProp, taskListId, task) {
  let request = {
    method: import_pieces_common.HttpMethod.POST,
    url: `${googleTasksCommon.baseUrl}/tasks/v1/lists/${taskListId}/tasks`,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token: authProp.access_token
    },
    body: task
  };
  return import_pieces_common.httpClient.sendRequest(request);
}

// ../ap/packages/pieces/community/google-tasks/src/lib/output-schemas.ts
var taskFields = [
  { key: "title", label: "Title" },
  { key: "status", label: "Status" },
  { key: "id", label: "Task ID" },
  { key: "notes", label: "Notes" },
  { key: "due", label: "Due Date", format: "date" },
  { key: "completed", label: "Completed At", format: "datetime" },
  { key: "updated", label: "Updated At", format: "datetime" },
  { key: "parent", label: "Parent Task ID" },
  { key: "position", label: "Position" },
  { key: "webViewLink", label: "Task Link", format: "url" }
], taskListFields = [
  { key: "title", label: "Title" },
  { key: "id", label: "Task List ID" },
  { key: "updated", label: "Updated At", format: "datetime" }
], googleTasksGetTaskOutputSchema = {
  fields: taskFields
}, googleTasksCompleteTaskOutputSchema = {
  fields: taskFields
}, googleTasksCreateTaskOutputSchema = {
  fields: taskFields
}, googleTasksMoveTaskOutputSchema = {
  fields: taskFields
}, googleTasksUpdateTaskOutputSchema = {
  fields: taskFields
}, googleTasksAddTaskOutputSchema = {
  fields: [{ key: "body", label: "Task", value: "body", children: taskFields }]
}, googleTasksFindTasksOutputSchema = {
  fields: [
    { key: "tasks", label: "Tasks", listItems: taskFields, labelKey: "title" },
    { key: "count", label: "Count", format: "number" }
  ]
}, googleTasksListTaskListsOutputSchema = {
  fields: [
    { key: "taskLists", label: "Task Lists", listItems: taskListFields, labelKey: "title" },
    { key: "count", label: "Count", format: "number" }
  ]
}, googleTasksClearCompletedTasksOutputSchema = {
  fields: [{ key: "cleared", label: "Cleared", format: "boolean" }]
}, googleTasksDeleteTaskOutputSchema = {
  fields: [
    { key: "deleted", label: "Deleted", format: "boolean" },
    { key: "alreadyAbsent", label: "Already Absent", format: "boolean" }
  ]
}, googleTasksNewTaskTriggerOutputSchema = {
  fields: taskFields
};

// ../ap/packages/pieces/community/google-tasks/src/lib/actions/new-task.ts
var googleTasksAddNewTaskAction = (0, import_pieces_framework3.createAction)({
  auth: googleTasksAuth,
  name: "add_task",
  classification: "WRITE",
  description: "Add a new task to a specified task list",
  audience: "human",
  aiMetadata: {
    description: "Creates a new task in a specific Google Tasks list, with optional notes, due date, and a completed flag. Use to add a to-do item for the authenticated user; requires a valid task-list ID (resolve it first if you only have a list name). Not idempotent: each call appends a separate task even with identical input.",
    idempotent: !1
  },
  displayName: "Add Task",
  outputSchema: googleTasksAddTaskOutputSchema,
  props: {
    tasks_list: googleTasksCommon.tasksList,
    title: googleTasksCommon.title,
    notes: googleTasksCommon.notes,
    due: googleTasksCommon.due,
    completed: googleTasksCommon.completed
  },
  async run({ auth, propsValue }) {
    let task = {
      kind: "tasks#task",
      status: propsValue.completed ? "completed" /* COMPLETED */ : "needsAction" /* NEEDS_ACTION */,
      title: propsValue.title,
      completed: propsValue.completed ? (/* @__PURE__ */ new Date()).toISOString() : "",
      notes: propsValue.notes,
      due: propsValue.due ? `${new Date(propsValue.due).toISOString().split("T")[0]}T00:00:00Z` : void 0
    };
    return createTask(auth, propsValue.tasks_list, task);
  }
});

// ../ap/packages/pieces/community/google-tasks/src/lib/actions/find-tasks.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var googleTasksFindTasksAction = (0, import_pieces_framework4.createAction)({
  auth: googleTasksAuth,
  name: "find_tasks",
  classification: "SEARCH",
  displayName: "Find Tasks",
  description: "Search and list tasks in a task list with optional date-range and status filters.",
  audience: "ai",
  aiMetadata: {
    description: "List tasks in a task list, filtered by due/completed/updated date ranges and status. The piece is otherwise write-only \u2014 call this first to find a task's id before completing, updating, or deleting it. Read-only.",
    idempotent: !0
  },
  outputSchema: googleTasksFindTasksOutputSchema,
  props: {
    tasks_list: googleTasksCommon.tasksList,
    show_completed: import_pieces_framework4.Property.Checkbox({
      displayName: "Show Completed",
      description: "Include completed tasks in results. Defaults to true.",
      required: !1,
      defaultValue: !0
    }),
    show_hidden: import_pieces_framework4.Property.Checkbox({
      displayName: "Show Hidden",
      description: "Include hidden tasks (tasks that have been completed and a day has passed). Defaults to false.",
      required: !1,
      defaultValue: !1
    }),
    due_min: import_pieces_framework4.Property.DateTime({
      displayName: "Due Date From",
      description: "Return tasks due on or after this date. RFC 3339 timestamp, e.g. 2026-06-01T00:00:00Z.",
      required: !1
    }),
    due_max: import_pieces_framework4.Property.DateTime({
      displayName: "Due Date To",
      description: "Return tasks due before this date (exclusive). RFC 3339 timestamp, e.g. 2026-07-01T00:00:00Z.",
      required: !1
    }),
    completed_min: import_pieces_framework4.Property.DateTime({
      displayName: "Completed Date From",
      description: "Return tasks completed on or after this date. RFC 3339 timestamp.",
      required: !1
    }),
    completed_max: import_pieces_framework4.Property.DateTime({
      displayName: "Completed Date To",
      description: "Return tasks completed before this date (exclusive). RFC 3339 timestamp.",
      required: !1
    }),
    updated_min: import_pieces_framework4.Property.DateTime({
      displayName: "Updated Since",
      description: 'Return tasks last modified on or after this date. Useful for "since last run" incremental reads. RFC 3339 timestamp.',
      required: !1
    }),
    max_results: import_pieces_framework4.Property.Number({
      displayName: "Max Results",
      description: "Maximum number of tasks to return (1\u2013100). Defaults to 20 when omitted.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let authProp = auth, params = {};
    propsValue.show_completed !== void 0 && propsValue.show_completed !== null && (params.showCompleted = propsValue.show_completed), propsValue.show_hidden !== void 0 && propsValue.show_hidden !== null && (params.showHidden = propsValue.show_hidden), propsValue.due_min && (params.dueMin = new Date(propsValue.due_min).toISOString()), propsValue.due_max && (params.dueMax = new Date(propsValue.due_max).toISOString()), propsValue.completed_min && (params.completedMin = new Date(propsValue.completed_min).toISOString()), propsValue.completed_max && (params.completedMax = new Date(propsValue.completed_max).toISOString()), propsValue.updated_min && (params.updatedMin = new Date(propsValue.updated_min).toISOString()), propsValue.max_results && (params.maxResults = propsValue.max_results);
    let tasks = await getTasks(authProp, propsValue.tasks_list, params);
    return {
      tasks,
      count: tasks.length
    };
  }
});

// ../ap/packages/pieces/community/google-tasks/src/lib/actions/complete-task.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var googleTasksCompleteTaskAction = (0, import_pieces_framework5.createAction)({
  auth: googleTasksAuth,
  name: "complete_task",
  classification: "WRITE",
  displayName: "Mark Task Complete",
  description: "Mark an existing task as completed.",
  audience: "ai",
  aiMetadata: {
    description: "Mark an existing task as completed (by task id). Idempotent \u2014 re-completing a done task is a no-op. Use Update Task to reopen or edit other fields.",
    idempotent: !0
  },
  outputSchema: googleTasksCompleteTaskOutputSchema,
  props: {
    tasks_list: googleTasksCommon.tasksList,
    task_id: import_pieces_framework5.Property.ShortText({
      displayName: "Task ID",
      description: "The ID of the task to mark as completed. Obtain the id from Find Tasks.",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let authProp = auth, { tasks_list, task_id } = propsValue;
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.PATCH,
      url: `${googleTasksCommon.baseUrl}/tasks/v1/lists/${tasks_list}/tasks/${task_id}`,
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: authProp.access_token
      },
      body: { status: "completed" }
    })).body;
  }
});

// ../ap/packages/pieces/community/google-tasks/src/lib/actions/update-task.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var googleTasksUpdateTaskAction = (0, import_pieces_framework6.createAction)({
  auth: googleTasksAuth,
  name: "update_task",
  classification: "WRITE",
  displayName: "Update Task",
  description: "Edit an existing task \u2014 title, notes, due date, or status.",
  audience: "ai",
  aiMetadata: {
    description: "Edit an existing task's title, notes, due date, or status (e.g. rename, change due date, reopen). Partial update \u2014 only provided fields change. Not idempotent (use Mark Task Complete for the common complete case).",
    idempotent: !1
  },
  outputSchema: googleTasksUpdateTaskOutputSchema,
  props: {
    tasks_list: googleTasksCommon.tasksList,
    task_id: import_pieces_framework6.Property.ShortText({
      displayName: "Task ID",
      description: "The ID of the task to update. Obtain the id from Find Tasks.",
      required: !0
    }),
    title: import_pieces_framework6.Property.ShortText({
      displayName: "Title",
      description: "New title for the task.",
      required: !1
    }),
    notes: import_pieces_framework6.Property.LongText({
      displayName: "Notes",
      description: "New notes / description for the task.",
      required: !1
    }),
    due: import_pieces_framework6.Property.DateTime({
      displayName: "Due Date",
      description: "New due date for the task. RFC 3339 timestamp, e.g. 2026-07-15T00:00:00Z.",
      required: !1
    }),
    status: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Status",
      description: "Task status. To simply complete a task prefer Mark Task Complete; set needsAction here to reopen a completed task.",
      required: !1,
      options: {
        options: [
          { label: "Needs Action", value: "needsAction" },
          { label: "Completed", value: "completed" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    let authProp = auth, { tasks_list, task_id, title, notes, due, status } = propsValue, patch = {};
    return title != null && title !== "" && (patch.title = title), notes != null && (patch.notes = notes), due != null && (patch.due = `${new Date(due).toISOString().split("T")[0]}T00:00:00Z`), status != null && (patch.status = status), (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.PATCH,
      url: `${googleTasksCommon.baseUrl}/tasks/v1/lists/${tasks_list}/tasks/${task_id}`,
      authentication: {
        type: import_pieces_common3.AuthenticationType.BEARER_TOKEN,
        token: authProp.access_token
      },
      body: patch
    })).body;
  }
});

// ../ap/packages/pieces/community/google-tasks/src/lib/actions/delete-task.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var googleTasksDeleteTaskAction = (0, import_pieces_framework7.createAction)({
  auth: googleTasksAuth,
  name: "delete_task",
  classification: "DESTRUCTIVE",
  displayName: "Delete Task",
  description: "Permanently delete a task by ID.",
  audience: "ai",
  aiMetadata: {
    description: "Delete a task by id. Idempotent \u2014 deleting an already-removed task succeeds (converges to absent).",
    idempotent: !0
  },
  outputSchema: googleTasksDeleteTaskOutputSchema,
  props: {
    tasks_list: googleTasksCommon.tasksList,
    task_id: import_pieces_framework7.Property.ShortText({
      displayName: "Task ID",
      description: "The ID of the task to delete. Obtain the id from Find Tasks.",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    var _a;
    let authProp = auth, { tasks_list, task_id } = propsValue;
    try {
      await import_pieces_common4.httpClient.sendRequest({
        method: import_pieces_common4.HttpMethod.DELETE,
        url: `${googleTasksCommon.baseUrl}/tasks/v1/lists/${tasks_list}/tasks/${task_id}`,
        authentication: {
          type: import_pieces_common4.AuthenticationType.BEARER_TOKEN,
          token: authProp.access_token
        }
      });
    } catch (error) {
      if (((_a = error == null ? void 0 : error.response) == null ? void 0 : _a.status) === 404 || (error == null ? void 0 : error.status) === 404)
        return { deleted: !0, alreadyAbsent: !0 };
      throw error;
    }
    return { deleted: !0, alreadyAbsent: !1 };
  }
});

// ../ap/packages/pieces/community/google-tasks/src/lib/actions/create-task.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var googleTasksCreateTaskAction = (0, import_pieces_framework8.createAction)({
  auth: googleTasksAuth,
  name: "create_task",
  classification: "WRITE",
  displayName: "Create Task",
  description: "Create a new task in a task list, optionally as a subtask and/or positioned after a sibling.",
  audience: "ai",
  aiMetadata: {
    description: "Create a new task in a task list, optionally as a subtask of a parent task (parent_task_id) and/or positioned after a sibling (previous_task_id); resolve those ids via Find Tasks. Use to add a single to-do; for marking an existing task done use Mark Task Complete. Not idempotent \u2014 each call creates a separate task.",
    idempotent: !1
  },
  outputSchema: googleTasksCreateTaskOutputSchema,
  props: {
    tasks_list: googleTasksCommon.tasksList,
    title: googleTasksCommon.title,
    notes: googleTasksCommon.notes,
    due_date: googleTasksCommon.due,
    parent_task_id: import_pieces_framework8.Property.ShortText({
      displayName: "Parent Task ID",
      description: "Make this a subtask of the task with this id (must be in the same list). Obtain the id from Find Tasks. Omit to create a top-level task.",
      required: !1
    }),
    previous_task_id: import_pieces_framework8.Property.ShortText({
      displayName: "Previous Task ID",
      description: "Place the new task immediately after the sibling with this id. Obtain the id from Find Tasks. Omit to place it first among its siblings.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let authProp = auth, { tasks_list, title, notes, due_date, parent_task_id, previous_task_id } = propsValue, task = {
      kind: "tasks#task",
      status: "needsAction" /* NEEDS_ACTION */,
      title,
      notes,
      due: due_date ? `${new Date(due_date).toISOString().split("T")[0]}T00:00:00Z` : void 0
    }, queryParams = {};
    parent_task_id && (queryParams.parent = parent_task_id), previous_task_id && (queryParams.previous = previous_task_id);
    let request = {
      method: import_pieces_common5.HttpMethod.POST,
      url: `${googleTasksCommon.baseUrl}/tasks/v1/lists/${tasks_list}/tasks`,
      authentication: {
        type: import_pieces_common5.AuthenticationType.BEARER_TOKEN,
        token: authProp.access_token
      },
      body: task,
      queryParams
    };
    return (await import_pieces_common5.httpClient.sendRequest(request)).body;
  }
});

// ../ap/packages/pieces/community/google-tasks/src/lib/actions/get-task.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var googleTasksGetTaskAction = (0, import_pieces_framework9.createAction)({
  auth: googleTasksAuth,
  name: "get_task",
  classification: "READ",
  displayName: "Get Task",
  description: "Fetch a single task by its ID.",
  audience: "ai",
  aiMetadata: {
    description: "Fetch a single task's full details (title, notes, due, status, parent, position, links) by its id. Use when you already have a specific task id and want its current state; to find tasks by criteria use Find Tasks instead. Read-only.",
    idempotent: !0
  },
  outputSchema: googleTasksGetTaskOutputSchema,
  props: {
    tasks_list: googleTasksCommon.tasksList,
    task_id: import_pieces_framework9.Property.ShortText({
      displayName: "Task ID",
      description: "The ID of the task to fetch. Obtain the id from Find Tasks.",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let authProp = auth, { tasks_list, task_id } = propsValue;
    return (await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.GET,
      url: `${googleTasksCommon.baseUrl}/tasks/v1/lists/${tasks_list}/tasks/${task_id}`,
      authentication: {
        type: import_pieces_common6.AuthenticationType.BEARER_TOKEN,
        token: authProp.access_token
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/google-tasks/src/lib/actions/move-task.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var googleTasksMoveTaskAction = (0, import_pieces_framework10.createAction)({
  auth: googleTasksAuth,
  name: "move_task",
  classification: "WRITE",
  displayName: "Move Task",
  description: "Reposition or reparent a task within its list, or move it to another list.",
  audience: "ai",
  aiMetadata: {
    description: "Move a task to a new position within its list \u2014 reparent it under another task (parent_task_id) and/or place it after a sibling (previous_task_id); omit both to move it to the top level. Optionally move it to a different list (destination_tasklist_id). All ids come from Find Tasks, and parent/previous must reference tasks in the destination list. Use this for reordering/nesting; use Update Task to change a task's fields. Not idempotent \u2014 position depends on current order.",
    idempotent: !1
  },
  outputSchema: googleTasksMoveTaskOutputSchema,
  props: {
    tasks_list: googleTasksCommon.tasksList,
    task_id: import_pieces_framework10.Property.ShortText({
      displayName: "Task ID",
      description: "The ID of the task to move. Obtain the id from Find Tasks.",
      required: !0
    }),
    parent_task_id: import_pieces_framework10.Property.ShortText({
      displayName: "Parent Task ID",
      description: "Reparent the task under the task with this id (must be in the destination list). Obtain the id from Find Tasks. Omit to move it to the top level.",
      required: !1
    }),
    previous_task_id: import_pieces_framework10.Property.ShortText({
      displayName: "Previous Task ID",
      description: "Place the task immediately after the sibling with this id (must be in the destination list). Obtain the id from Find Tasks. Omit to place it first among its siblings; to move it to the end, pass the last sibling's id (read the current order with Find Tasks).",
      required: !1
    }),
    destination_tasklist_id: import_pieces_framework10.Property.ShortText({
      displayName: "Destination Task List ID",
      description: "Move the task to this task list. Omit to keep it in the current list. Recurring tasks cannot be moved between lists.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let authProp = auth, {
      tasks_list,
      task_id,
      parent_task_id,
      previous_task_id,
      destination_tasklist_id
    } = propsValue, queryParams = {};
    parent_task_id && (queryParams.parent = parent_task_id), previous_task_id && (queryParams.previous = previous_task_id), destination_tasklist_id && (queryParams.destinationTasklist = destination_tasklist_id);
    let request = {
      method: import_pieces_common7.HttpMethod.POST,
      url: `${googleTasksCommon.baseUrl}/tasks/v1/lists/${tasks_list}/tasks/${task_id}/move`,
      authentication: {
        type: import_pieces_common7.AuthenticationType.BEARER_TOKEN,
        token: authProp.access_token
      },
      queryParams
    };
    return (await import_pieces_common7.httpClient.sendRequest(request)).body;
  }
});

// ../ap/packages/pieces/community/google-tasks/src/lib/actions/clear-completed-tasks.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var googleTasksClearCompletedTasksAction = (0, import_pieces_framework11.createAction)({
  auth: googleTasksAuth,
  name: "clear_completed_tasks",
  classification: "DESTRUCTIVE",
  displayName: "Clear Completed Tasks",
  description: "Hide all completed tasks in a task list.",
  audience: "ai",
  aiMetadata: {
    description: "Hide all completed tasks in a task list (they are marked hidden and drop out of the default view, not permanently deleted). Use to tidy a list after a batch of completions. Affects every completed task at once \u2014 there is no per-task selection; to remove a single task use Delete Task. Not idempotent \u2014 operates on whatever is completed at call time.",
    idempotent: !1
  },
  outputSchema: googleTasksClearCompletedTasksOutputSchema,
  props: {
    tasks_list: googleTasksCommon.tasksList
  },
  async run({ auth, propsValue }) {
    let authProp = auth, { tasks_list } = propsValue;
    return await import_pieces_common8.httpClient.sendRequest({
      method: import_pieces_common8.HttpMethod.POST,
      url: `${googleTasksCommon.baseUrl}/tasks/v1/lists/${tasks_list}/clear`,
      authentication: {
        type: import_pieces_common8.AuthenticationType.BEARER_TOKEN,
        token: authProp.access_token
      }
    }), { cleared: !0 };
  }
});

// ../ap/packages/pieces/community/google-tasks/src/lib/actions/list-task-lists.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var googleTasksListTaskListsAction = (0, import_pieces_framework12.createAction)({
  auth: googleTasksAuth,
  name: "list_task_lists",
  classification: "SEARCH",
  displayName: "List Task Lists",
  description: "List the authenticated user's task lists (id and title).",
  audience: "ai",
  aiMetadata: {
    description: "List the authenticated user's task lists, returning each list's id and title. Use to resolve a list name to its id before operating on tasks, or to discover available lists. Read-only.",
    idempotent: !0
  },
  outputSchema: googleTasksListTaskListsOutputSchema,
  props: {
    max_results: import_pieces_framework12.Property.Number({
      displayName: "Max Results",
      description: "Maximum number of task lists to return (1\u2013100). Defaults to 20 when omitted.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    var _a;
    let authProp = auth, { max_results } = propsValue, queryParams = {};
    max_results && (queryParams.maxResults = String(max_results));
    let request = {
      method: import_pieces_common9.HttpMethod.GET,
      url: `${googleTasksCommon.baseUrl}/tasks/v1/users/@me/lists`,
      authentication: {
        type: import_pieces_common9.AuthenticationType.BEARER_TOKEN,
        token: authProp.access_token
      },
      queryParams
    }, taskLists = (_a = (await import_pieces_common9.httpClient.sendRequest(request)).body.items) != null ? _a : [];
    return {
      taskLists,
      count: taskLists.length
    };
  }
});

// ../ap/packages/pieces/community/google-tasks/src/lib/triggers/new-task.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var import_pieces_common10 = require("@activepieces/pieces-common");
var props = {
  tasks_list: googleTasksCommon.tasksList
}, polling = {
  strategy: import_pieces_common10.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue, lastFetchEpochMS }) => (await getTasks(auth, propsValue.tasks_list)).filter((record) => {
    var _a;
    return Date.parse((_a = record.updated) != null ? _a : "0") > lastFetchEpochMS;
  }).map((record) => {
    var _a;
    return {
      epochMilliSeconds: Date.parse((_a = record.updated) != null ? _a : "0"),
      data: record
    };
  })
}, newTaskTrigger = (0, import_pieces_framework13.createTrigger)({
  auth: googleTasksAuth,
  name: "new_task",
  classification: "READ",
  displayName: "New Task",
  description: "Triggers when a task is created",
  aiMetadata: {
    description: "Fires when a task in the selected Google Tasks list is newly created or updated since the last poll, emitting the task that changed. Use to react to additions or edits in a specific task list."
  },
  type: import_pieces_framework13.TriggerStrategy.POLLING,
  props,
  outputSchema: googleTasksNewTaskTriggerOutputSchema,
  sampleData: {},
  async test(context) {
    let store = context.store, auth = context.auth, propsValue = context.propsValue;
    return await import_pieces_common10.pollingHelper.test(polling, { store, auth, propsValue, files: context.files });
  },
  async onEnable(context) {
    let store = context.store, auth = context.auth, propsValue = context.propsValue;
    await import_pieces_common10.pollingHelper.onEnable(polling, { store, auth, propsValue });
  },
  async onDisable(context) {
    let store = context.store, auth = context.auth, propsValue = context.propsValue;
    await import_pieces_common10.pollingHelper.onEnable(polling, { store, auth, propsValue });
  },
  async run(context) {
    let store = context.store, auth = context.auth, propsValue = context.propsValue;
    return await import_pieces_common10.pollingHelper.poll(polling, { store, auth, propsValue, files: context.files });
  }
});

// ../ap/packages/pieces/community/google-tasks/src/index.ts
var googleTasks = (0, import_pieces_framework14.createPiece)({
  minimumSupportedRelease: "0.86.4",
  logoUrl: "https://cdn.activepieces.com/pieces/google-tasks.png",
  categories: [import_pieces_framework15.PieceCategory.PRODUCTIVITY],
  actions: [
    googleTasksAddNewTaskAction,
    googleTasksFindTasksAction,
    googleTasksCompleteTaskAction,
    googleTasksUpdateTaskAction,
    googleTasksDeleteTaskAction,
    googleTasksCreateTaskAction,
    googleTasksGetTaskAction,
    googleTasksMoveTaskAction,
    googleTasksClearCompletedTasksAction,
    googleTasksListTaskListsAction,
    (0, import_pieces_common11.createCustomApiCallAction)({
      baseUrl: () => googleTasksCommon.baseUrl,
      auth: googleTasksAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.access_token}`
      })
    })
  ],
  displayName: "Google Tasks",
  description: "Task list management application",
  authors: ["Salem-Alaa", "kishanprmr", "MoShizzle", "khaledmashaly", "abuaboud"],
  triggers: [newTaskTrigger],
  auth: googleTasksAuth
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  googleTasks
});
