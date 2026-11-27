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

// ../ap/packages/pieces/community/manus/src/index.ts
var index_exports = {};
__export(index_exports, {
  manus: () => manus
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/manus/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), manusAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Your Manus API key from https://app.manus.ai/settings/api-keys",
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        url: "https://api.manus.ai/v1/tasks",
        method: import_pieces_common.HttpMethod.POST,
        headers: {
          accept: "application/json",
          "content-type": "application/json",
          API_KEY: auth
        },
        body: {
          prompt: "test",
          mode: "speed"
        }
      }), {
        valid: !0
      };
    } catch (error) {
      return {
        valid: !1,
        error: "Invalid API key"
      };
    }
  }
});

// ../ap/packages/pieces/community/manus/src/lib/actions/create-task.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var createTask = (0, import_pieces_framework2.createAction)({
  name: "create_task",
  auth: manusAuth,
  displayName: "Create Task",
  description: "Create a new task for Manus AI to execute",
  audience: "both",
  aiMetadata: { description: "Submits a prompt to the Manus AI agent to run as a task, optionally with an agent profile, interaction mode (chat/adaptive/agent), connectors, and attachments. Use to kick off new agent work, or pass an existing task ID to continue that task as a multi-turn conversation. Not idempotent: each call without a task ID creates a new task.", idempotent: !1 },
  props: {
    prompt: import_pieces_framework2.Property.LongText({
      displayName: "Prompt",
      description: "The task prompt or instruction for the Manus agent",
      required: !0
    }),
    agentProfile: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Agent Profile",
      description: "The AI model profile to use for the task",
      required: !0,
      defaultValue: "manus-1.5",
      options: {
        options: [
          {
            label: "Manus 1.5 (Recommended)",
            value: "manus-1.5"
          },
          {
            label: "Manus 1.5 Lite (Faster)",
            value: "manus-1.5-lite"
          },
          {
            label: "Speed (Deprecated)",
            value: "speed"
          },
          {
            label: "Quality (Deprecated)",
            value: "quality"
          }
        ]
      }
    }),
    taskMode: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Task Mode",
      description: "The interaction mode for the task",
      required: !1,
      options: {
        options: [
          {
            label: "Chat",
            value: "chat"
          },
          {
            label: "Adaptive",
            value: "adaptive"
          },
          {
            label: "Agent",
            value: "agent"
          }
        ]
      }
    }),
    connectors: import_pieces_framework2.Property.Array({
      displayName: "Connectors",
      description: "List of connector IDs to enable for this task",
      required: !1
    }),
    hideInTaskList: import_pieces_framework2.Property.Checkbox({
      displayName: "Hide in Task List",
      description: "Whether to hide this task from the Manus webapp task list",
      required: !1,
      defaultValue: !1
    }),
    createShareableLink: import_pieces_framework2.Property.Checkbox({
      displayName: "Create Shareable Link",
      description: "Whether to make the chat publicly accessible to others on the Manus website",
      required: !1,
      defaultValue: !1
    }),
    taskId: import_pieces_framework2.Property.ShortText({
      displayName: "Task ID",
      description: "For continuing existing tasks (multi-turn conversations)",
      required: !1
    }),
    locale: import_pieces_framework2.Property.ShortText({
      displayName: "Locale",
      description: `Your default locale that you've set on Manus (e.g., "en-US", "zh-CN")`,
      required: !1
    }),
    attachments: import_pieces_framework2.Property.Array({
      displayName: "Attachments",
      description: "Array of file/image attachments (URLs or base64 data)",
      required: !1
    })
  },
  async run(context) {
    let body = {
      prompt: context.propsValue.prompt,
      agentProfile: context.propsValue.agentProfile
    };
    return context.propsValue.taskMode && (body.taskMode = context.propsValue.taskMode), context.propsValue.connectors && context.propsValue.connectors.length > 0 && (body.connectors = context.propsValue.connectors), context.propsValue.hideInTaskList !== void 0 && (body.hideInTaskList = context.propsValue.hideInTaskList), context.propsValue.createShareableLink !== void 0 && (body.createShareableLink = context.propsValue.createShareableLink), context.propsValue.taskId && (body.taskId = context.propsValue.taskId), context.propsValue.locale && (body.locale = context.propsValue.locale), context.propsValue.attachments && context.propsValue.attachments.length > 0 && (body.attachments = context.propsValue.attachments), (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://api.manus.ai/v1/tasks",
      headers: {
        accept: "application/json",
        "content-type": "application/json",
        API_KEY: context.auth.secret_text
      },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/manus/src/lib/actions/get-task.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
async function getTasksDropdown(auth) {
  var _a;
  try {
    return (((_a = (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.GET,
      url: "https://api.manus.ai/v1/tasks",
      headers: {
        accept: "application/json",
        API_KEY: auth
      },
      queryParams: {
        limit: "50",
        order: "desc",
        orderBy: "created_at"
      }
    })).body) == null ? void 0 : _a.data) || []).map((task) => {
      var _a2;
      return {
        label: `${((_a2 = task.instructions) == null ? void 0 : _a2.substring(0, 50)) || "Untitled Task"} (${task.status})`,
        value: task.id
      };
    });
  } catch (error) {
    return [];
  }
}
var getTask = (0, import_pieces_framework3.createAction)({
  name: "get_task",
  auth: manusAuth,
  displayName: "Get Task",
  description: "Retrieve detailed information about a specific task by its ID",
  audience: "both",
  aiMetadata: { description: "Fetches the full details and current status of a single Manus task by its ID. Use to check progress or read back the result of a task you created or know the ID of. Read-only and idempotent.", idempotent: !0 },
  props: {
    taskId: import_pieces_framework3.Property.Dropdown({
      auth: manusAuth,
      displayName: "Task",
      description: "Select the task to retrieve details for",
      required: !0,
      refreshers: ["auth"],
      options: async ({ auth }) => auth ? { disabled: !1, options: await getTasksDropdown(auth.secret_text) } : { disabled: !0, options: [], placeholder: "Connect account first" }
    })
  },
  async run(context) {
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.GET,
      url: `https://api.manus.ai/v1/tasks/${context.propsValue.taskId}`,
      headers: {
        accept: "application/json",
        API_KEY: context.auth.secret_text
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/manus/src/lib/actions/update-task.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
async function getTasksDropdown2(auth) {
  var _a;
  try {
    return (((_a = (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.GET,
      url: "https://api.manus.ai/v1/tasks",
      headers: {
        accept: "application/json",
        API_KEY: auth
      },
      queryParams: {
        limit: "50",
        order: "desc",
        orderBy: "created_at"
      }
    })).body) == null ? void 0 : _a.data) || []).map((task) => {
      var _a2;
      return {
        label: `${((_a2 = task.instructions) == null ? void 0 : _a2.substring(0, 50)) || "Untitled Task"} (${task.status})`,
        value: task.id
      };
    });
  } catch (error) {
    return [];
  }
}
var updateTask = (0, import_pieces_framework4.createAction)({
  name: "update_task",
  auth: manusAuth,
  displayName: "Update Task",
  description: "Update a task's metadata such as title, sharing settings, and visibility in the task list",
  audience: "both",
  aiMetadata: { description: "Updates metadata on an existing Manus task identified by its ID \u2014 its title, public-sharing toggle, and visibility in the task list. Use to rename a task or change its sharing/visibility, not to alter what the agent does. Idempotent: re-applying the same values leaves the task in the same state.", idempotent: !0 },
  props: {
    taskId: import_pieces_framework4.Property.Dropdown({
      auth: manusAuth,
      displayName: "Task",
      description: "Select the task to update",
      required: !0,
      refreshers: ["auth"],
      options: async ({ auth }) => auth ? { disabled: !1, options: await getTasksDropdown2(auth.secret_text) } : { disabled: !0, options: [], placeholder: "Connect account first" }
    }),
    title: import_pieces_framework4.Property.ShortText({
      displayName: "Title",
      description: "New title for the task",
      required: !1
    }),
    enableShared: import_pieces_framework4.Property.Checkbox({
      displayName: "Enable Sharing",
      description: "Whether to enable public sharing",
      required: !1,
      defaultValue: !1
    }),
    enableVisibleInTaskList: import_pieces_framework4.Property.Checkbox({
      displayName: "Visible in Task List",
      description: "Whether the task should be visible in the task list",
      required: !1,
      defaultValue: !0
    })
  },
  async run(context) {
    let body = {};
    return context.propsValue.title && (body.title = context.propsValue.title), context.propsValue.enableShared !== void 0 && (body.enableShared = context.propsValue.enableShared), context.propsValue.enableVisibleInTaskList !== void 0 && (body.enableVisibleInTaskList = context.propsValue.enableVisibleInTaskList), (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.PUT,
      url: `https://api.manus.ai/v1/tasks/${context.propsValue.taskId}`,
      headers: {
        accept: "application/json",
        "content-type": "application/json",
        API_KEY: context.auth.secret_text
      },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/manus/src/lib/actions/delete-task.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
async function getTasksDropdown3(auth) {
  var _a;
  try {
    return (((_a = (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.GET,
      url: "https://api.manus.ai/v1/tasks",
      headers: {
        accept: "application/json",
        API_KEY: auth
      },
      queryParams: {
        limit: "50",
        order: "desc",
        orderBy: "created_at"
      }
    })).body) == null ? void 0 : _a.data) || []).map((task) => {
      var _a2;
      return {
        label: `${((_a2 = task.instructions) == null ? void 0 : _a2.substring(0, 50)) || "Untitled Task"} (${task.status})`,
        value: task.id
      };
    });
  } catch (error) {
    return [];
  }
}
var deleteTask = (0, import_pieces_framework5.createAction)({
  name: "delete_task",
  auth: manusAuth,
  displayName: "Delete Task",
  description: "Permanently delete a task by its ID. This action cannot be undone.",
  audience: "both",
  aiMetadata: { description: "Permanently deletes a Manus task by its ID; this cannot be undone. Use to remove a task entirely. Not idempotent: a repeat call targets an already-deleted task and will fail.", idempotent: !1 },
  props: {
    taskId: import_pieces_framework5.Property.Dropdown({
      auth: manusAuth,
      displayName: "Task",
      description: "Select the task to delete",
      required: !0,
      refreshers: ["auth"],
      options: async ({ auth }) => auth ? { disabled: !1, options: await getTasksDropdown3(auth.secret_text) } : { disabled: !0, options: [], placeholder: "Connect account first" }
    })
  },
  async run(context) {
    return (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.DELETE,
      url: `https://api.manus.ai/v1/tasks/${context.propsValue.taskId}`,
      headers: {
        accept: "application/json",
        API_KEY: context.auth.secret_text
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/manus/src/lib/triggers/new-task-created.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var newTaskCreated = (0, import_pieces_framework6.createTrigger)({
  name: "new_task_created",
  displayName: "New Task Created",
  description: "Triggers when a new task is successfully created in Manus",
  aiMetadata: {
    description: "Fires when a new task is successfully created in the connected Manus account, carrying the new task's ID, title, and URL. Use to react to newly started Manus work."
  },
  props: {},
  auth: manusAuth,
  sampleData: {
    event_id: "task_created_task_abc123",
    event_type: "task_created",
    task_detail: {
      task_id: "task_abc123",
      task_title: "Generate quarterly sales report",
      task_url: "https://manus.im/app/task_abc123"
    }
  },
  type: import_pieces_framework6.TriggerStrategy.WEBHOOK,
  async onEnable({ webhookUrl, store, auth }) {
    try {
      let webhookId = (await import_pieces_common6.httpClient.sendRequest({
        method: import_pieces_common6.HttpMethod.POST,
        url: "https://api.manus.ai/v1/webhooks",
        headers: {
          accept: "application/json",
          "content-type": "application/json",
          API_KEY: auth.secret_text
        },
        body: {
          webhook: {
            url: webhookUrl
          }
        }
      })).body.webhook_id;
      await store.put("webhook_id", webhookId), await store.put("webhook_url", webhookUrl);
    } catch (error) {
      throw new Error(`Failed to create webhook: ${error}`);
    }
  },
  async onDisable({ store, auth }) {
    try {
      let webhookId = await store.get("webhook_id");
      webhookId && await import_pieces_common6.httpClient.sendRequest({
        method: import_pieces_common6.HttpMethod.DELETE,
        url: `https://api.manus.ai/v1/webhooks/${webhookId}`,
        headers: {
          accept: "application/json",
          API_KEY: auth.secret_text
        }
      });
    } catch (error) {
      console.error("Failed to delete webhook:", error);
    }
  },
  async run(context) {
    let payload = context.payload.body;
    return !payload || payload.event_type !== "task_created" ? [] : [payload];
  }
});

// ../ap/packages/pieces/community/manus/src/lib/triggers/task-stopped.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var taskStopped = (0, import_pieces_framework7.createTrigger)({
  name: "task_stopped",
  displayName: "Task Stopped",
  description: "Triggers when a task reaches a stopping point - either completion or requiring user input",
  aiMetadata: {
    description: "Fires when a Manus task reaches a stopping point \u2014 either it finished or it is paused waiting for user input \u2014 carrying the task ID, the agent's message, any output attachments, and a stop reason. Use to act on a task's result or to respond when the agent needs more input."
  },
  props: {},
  auth: manusAuth,
  sampleData: {
    event_id: "task_stopped_task_abc123",
    event_type: "task_stopped",
    task_detail: {
      task_id: "task_abc123",
      task_title: "Generate quarterly sales report",
      task_url: "https://manus.im/app/task_abc123",
      message: "I've completed the quarterly sales report analysis...",
      attachments: [
        {
          file_name: "q4-sales-report.pdf",
          url: "https://s3.amazonaws.com/manus-files/reports/q4-sales-report.pdf",
          size_bytes: 2048576
        }
      ],
      stop_reason: "finish"
    }
  },
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  async onEnable({ webhookUrl, store, auth }) {
    try {
      let webhookId = (await import_pieces_common7.httpClient.sendRequest({
        method: import_pieces_common7.HttpMethod.POST,
        url: "https://api.manus.ai/v1/webhooks",
        headers: {
          accept: "application/json",
          "content-type": "application/json",
          API_KEY: auth.secret_text
        },
        body: {
          webhook: {
            url: webhookUrl
          }
        }
      })).body.webhook_id;
      await store.put("webhook_id", webhookId), await store.put("webhook_url", webhookUrl);
    } catch (error) {
      throw new Error(`Failed to create webhook: ${error}`);
    }
  },
  async onDisable({ store, auth }) {
    try {
      let webhookId = await store.get("webhook_id");
      webhookId && await import_pieces_common7.httpClient.sendRequest({
        method: import_pieces_common7.HttpMethod.DELETE,
        url: `https://api.manus.ai/v1/webhooks/${webhookId}`,
        headers: {
          accept: "application/json",
          API_KEY: auth.secret_text
        }
      });
    } catch (error) {
      console.error("Failed to delete webhook:", error);
    }
  },
  async run(context) {
    let payload = context.payload.body;
    return !payload || payload.event_type !== "task_stopped" ? [] : [payload];
  }
});

// ../ap/packages/pieces/community/manus/src/index.ts
var manus = (0, import_pieces_framework8.createPiece)({
  displayName: "Manus",
  description: "AI-powered automation and task execution platform",
  auth: manusAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/manus.png",
  categories: [import_pieces_framework9.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  authors: ["onyedikachi-david"],
  actions: [
    createTask,
    getTask,
    updateTask,
    deleteTask,
    (0, import_pieces_common8.createCustomApiCallAction)({
      baseUrl: () => "https://api.manus.ai/v1",
      auth: manusAuth,
      authMapping: async (auth) => ({
        API_KEY: auth.secret_text
      })
    })
  ],
  triggers: [
    newTaskCreated,
    taskStopped
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  manus
});
