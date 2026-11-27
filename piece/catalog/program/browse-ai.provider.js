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

// ../ap/packages/pieces/community/browse-ai/src/index.ts
var index_exports = {};
__export(index_exports, {
  browseAi: () => browseAi
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common9 = require("@activepieces/pieces-common"), import_pieces_framework10 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/browse-ai/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/browse-ai/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
async function browseAiApiCall({
  method,
  resourceUri,
  query,
  body,
  auth
}) {
  var _a, _b;
  let { apiKey } = auth;
  if (!apiKey)
    throw new Error("Browse AI API key is required for authentication");
  let queryParams = {};
  if (query)
    for (let [key, value] of Object.entries(query))
      value != null && (queryParams[key] = String(value));
  let request = {
    method,
    url: `https://api.browse.ai/v2${resourceUri}`,
    headers: {
      Authorization: `Bearer ${apiKey}`,
      "Content-Type": "application/json"
    },
    queryParams,
    body
  };
  try {
    return (await import_pieces_common.httpClient.sendRequest(request)).body;
  } catch (error) {
    let statusCode = (_a = error.response) == null ? void 0 : _a.status, errorData = (_b = error.response) == null ? void 0 : _b.data;
    switch (statusCode) {
      case 400:
        throw new Error(`Bad Request: ${(errorData == null ? void 0 : errorData.message) || "Invalid parameters"}`);
      case 401:
        throw new Error("Unauthorized: Invalid API key. Please check your credentials.");
      case 403:
        throw new Error("Forbidden: You do not have permission to access this resource.");
      case 404:
        throw new Error("Not Found: The requested resource does not exist.");
      case 429:
        throw new Error("Rate Limit Exceeded: Please slow down your requests.");
      case 500:
        throw new Error("Internal Server Error: Something went wrong on Browse AI\u2019s side.");
      default: {
        let message = (errorData == null ? void 0 : errorData.message) || error.message || "Unknown error";
        throw new Error(`Browse AI API Error (${statusCode || "Unknown"}): ${message}`);
      }
    }
  }
}

// ../ap/packages/pieces/community/browse-ai/src/lib/common/auth.ts
var browseAiAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "You can find your Browse AI API key on the dashboard under Settings \u2192 API Key.",
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await browseAiApiCall({
        method: import_pieces_common2.HttpMethod.GET,
        resourceUri: "/status",
        auth: { apiKey: auth }
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API Key. Please check your Browse AI credentials."
      };
    }
  }
});

// ../ap/packages/pieces/community/browse-ai/src/lib/actions/get-task-details.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/browse-ai/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var robotIdDropdown = import_pieces_framework2.Property.Dropdown({
  auth: browseAiAuth,
  displayName: "Robot",
  description: "Select a robot from your Browse AI account",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    var _a, _b;
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your Browse AI account first."
      };
    try {
      let response = await browseAiApiCall({
        method: import_pieces_common3.HttpMethod.GET,
        resourceUri: "/robots",
        auth: { apiKey: auth.secret_text }
      }), robots = (_b = (_a = response == null ? void 0 : response.robots) == null ? void 0 : _a.items) != null ? _b : [];
      return robots.length === 0 ? {
        disabled: !0,
        options: [],
        placeholder: "No robots found in your account."
      } : {
        disabled: !1,
        options: robots.map((robot) => ({
          label: robot.name,
          value: robot.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load robots. Please check your API key and try again."
      };
    }
  }
}), taskIdDropdown = import_pieces_framework2.Property.Dropdown({
  auth: browseAiAuth,
  displayName: "Task",
  description: "Select a task associated with the selected robot",
  required: !0,
  refreshers: ["robotId"],
  options: async ({ auth, robotId }) => {
    var _a, _b, _c;
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your Browse AI account."
      };
    if (!robotId)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please select a robot first."
      };
    try {
      let tasks = (_c = (_b = (_a = (await browseAiApiCall({
        method: import_pieces_common3.HttpMethod.GET,
        resourceUri: `/robots/${robotId}/tasks`,
        auth: { apiKey: auth.secret_text }
      })).result) == null ? void 0 : _a.robotTasks) == null ? void 0 : _b.items) != null ? _c : [];
      return tasks.length === 0 ? {
        disabled: !0,
        options: [],
        placeholder: "No tasks found for the selected robot."
      } : {
        disabled: !1,
        options: tasks.map((task) => {
          let createdDate = task.createdAt ? new Date(task.createdAt).toLocaleDateString() : "Unknown date";
          return {
            label: `${task.id} - ${task.status} (${createdDate})`,
            value: task.id
          };
        })
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: `Error fetching tasks: ${e instanceof Error ? e.message : "Unknown error"}`
      };
    }
  }
}), robotParameters = import_pieces_framework2.Property.DynamicProperties({
  auth: browseAiAuth,
  displayName: "Input Parameters",
  refreshers: ["robotId"],
  required: !0,
  props: async ({ auth, robotId }) => {
    var _a;
    if (!auth || !robotId) return {};
    try {
      let response = await browseAiApiCall({
        method: import_pieces_common3.HttpMethod.GET,
        resourceUri: `/robots/${robotId}`,
        auth: { apiKey: auth.secret_text }
      }), props = {}, params = (_a = response.robot.inputParameters) != null ? _a : [];
      for (let param of params)
        switch (param.type) {
          case "number":
            props[param.name] = import_pieces_framework2.Property.Number({
              displayName: param.label,
              required: param.required
            });
            break;
          case "url":
          case "string":
            props[param.name] = import_pieces_framework2.Property.ShortText({
              displayName: param.label,
              required: param.required
            });
            break;
          case "select":
            props[param.name] = import_pieces_framework2.Property.StaticDropdown({
              displayName: param.label,
              required: param.required,
              options: {
                disabled: !1,
                options: param.options ? param.options : []
              }
            });
            break;
          default:
            break;
        }
      return props;
    } catch (e) {
      return {};
    }
  }
});

// ../ap/packages/pieces/community/browse-ai/src/lib/actions/get-task-details.ts
var getTaskDetailsAction = (0, import_pieces_framework3.createAction)({
  name: "get-task-details",
  classification: "READ",
  auth: browseAiAuth,
  displayName: "Get Task Details",
  description: "Retrieves the details of a specific task executed by a Browse AI robot.",
  audience: "both",
  aiMetadata: {
    description: "Fetches the full record of one previously-run Browse AI scraping task by robot ID and task ID, including its status and any captured/extracted data. Use to check the outcome or pull results of a known task. Requires both the robot ID and the task ID; read-only and idempotent.",
    idempotent: !0
  },
  props: {
    robotId: robotIdDropdown,
    taskId: taskIdDropdown
  },
  async run(context) {
    var _a, _b;
    let { robotId, taskId } = context.propsValue;
    try {
      return await browseAiApiCall({
        auth: { apiKey: context.auth.secret_text },
        method: import_pieces_common4.HttpMethod.GET,
        resourceUri: `/robots/${robotId}/tasks/${taskId}`
      });
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 404 ? new Error(
        "Task not found. Please verify the Robot ID and Task ID."
      ) : ((_b = error.response) == null ? void 0 : _b.status) === 401 ? new Error("Authentication failed. Please check your API key.") : new Error(
        `Failed to fetch task details: ${error.message || "Unknown error occurred"}`
      );
    }
  }
});

// ../ap/packages/pieces/community/browse-ai/src/lib/actions/list-robots.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var listRobotsAction = (0, import_pieces_framework4.createAction)({
  name: "list-robots",
  classification: "SEARCH",
  auth: browseAiAuth,
  displayName: "List Robots",
  description: "Retrieves all robots available in your account.",
  audience: "both",
  aiMetadata: {
    description: "Lists all Browse AI robots in the connected account, returning each robot and its ID. Use to discover the available robots or resolve a robot name to the ID needed by other Browse AI actions. Takes no input; read-only and idempotent.",
    idempotent: !0
  },
  props: {},
  async run(context) {
    var _a, _b;
    try {
      return await browseAiApiCall({
        auth: { apiKey: context.auth.secret_text },
        method: import_pieces_common5.HttpMethod.GET,
        resourceUri: "/robots"
      });
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 401 ? new Error("Authentication failed. Please check your API key.") : ((_b = error.response) == null ? void 0 : _b.status) === 429 ? new Error("Rate limit exceeded. Please wait before retrying.") : new Error(
        `Failed to fetch robots: ${error.message || "Unknown error occurred"}`
      );
    }
  }
});

// ../ap/packages/pieces/community/browse-ai/src/lib/actions/run-robot.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");
var runRobotAction = (0, import_pieces_framework5.createAction)({
  name: "run-robot",
  classification: "WRITE",
  auth: browseAiAuth,
  displayName: "Run a Robot",
  description: "Runs a robot on-demand with custom input parameters.",
  audience: "both",
  aiMetadata: {
    description: "Starts a new on-demand run of a Browse AI robot, creating a fresh scraping task with the robot-specific input parameters (the required inputs vary per robot and are pulled from its definition). Use to trigger a scrape/extraction; results arrive asynchronously, so poll Get Task Details or use a task-finished trigger for the outcome. Not idempotent: each call queues another task run.",
    idempotent: !1
  },
  props: {
    robotId: robotIdDropdown,
    recordVideo: import_pieces_framework5.Property.Checkbox({
      displayName: "Record Video",
      description: "Try to record a video while running the task.",
      required: !1,
      defaultValue: !1
    }),
    robotParams: robotParameters
  },
  async run(context) {
    var _a, _b, _c, _d;
    let { robotId, recordVideo } = context.propsValue, inputParameters = (_a = context.propsValue.robotParams) != null ? _a : {};
    try {
      return await browseAiApiCall({
        method: import_pieces_common6.HttpMethod.POST,
        resourceUri: `/robots/${robotId}/tasks`,
        auth: { apiKey: context.auth.secret_text },
        body: {
          recordVideo: recordVideo || !1,
          inputParameters
        }
      });
    } catch (error) {
      throw ((_b = error.response) == null ? void 0 : _b.status) === 401 ? new Error("Authentication failed. Please check your API key.") : ((_c = error.response) == null ? void 0 : _c.status) === 404 ? new Error("Robot not found. Please verify the robot ID.") : ((_d = error.response) == null ? void 0 : _d.status) === 429 ? new Error("Rate limit exceeded. Please wait before retrying.") : new Error(
        `Failed to run robot: ${error.message || "Unknown error occurred"}`
      );
    }
  }
});

// ../ap/packages/pieces/community/browse-ai/src/lib/triggers/task-finished-with-error.ts
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_framework7 = require("@activepieces/pieces-framework");
var TRIGGER_KEY = "browse-ai-task_finished_with_error", taskFinishedWithErrorTrigger = (0, import_pieces_framework6.createTrigger)({
  auth: browseAiAuth,
  name: "task_finished_with_error",
  classification: "READ",
  displayName: "Task Finished with Error",
  description: "Triggers when a robot task run fails with an error.",
  aiMetadata: {
    description: "Fires when a task run for the selected Browse AI robot finishes in a failed/error state, delivering the failed task record. Use to detect and react to scrapes that did not complete successfully."
  },
  type: import_pieces_framework6.TriggerStrategy.WEBHOOK,
  props: {
    robotId: robotIdDropdown
  },
  async onEnable(context) {
    var _a, _b, _c, _d, _e, _f;
    let { robotId } = context.propsValue, apiKey = context.auth.secret_text;
    try {
      await browseAiApiCall({
        method: import_pieces_common7.HttpMethod.GET,
        auth: { apiKey },
        resourceUri: `/robots/${robotId}`
      });
      let response = await browseAiApiCall({
        method: import_pieces_common7.HttpMethod.POST,
        auth: { apiKey },
        resourceUri: `/robots/${robotId}/webhooks`,
        body: {
          hookUrl: context.webhookUrl,
          eventType: "taskFinishedWithError"
        }
      });
      await context.store.put(TRIGGER_KEY, response.webhook.id);
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 404 ? new Error(
        `Robot not found: The robot with ID "${robotId}" does not exist or you do not have access to it. Please verify the robot ID and your permissions.`
      ) : ((_b = error.response) == null ? void 0 : _b.status) === 403 ? new Error(
        "Access denied: You do not have permission to set up webhooks for this robot. Please check your Browse AI account permissions and ensure you have webhook access."
      ) : ((_c = error.response) == null ? void 0 : _c.status) === 400 ? new Error(
        `Invalid webhook configuration: ${((_e = (_d = error.response) == null ? void 0 : _d.data) == null ? void 0 : _e.message) || error.message}. Please check your webhook URL and robot ID.`
      ) : ((_f = error.response) == null ? void 0 : _f.status) === 429 ? new Error(
        "Rate limit exceeded: Too many webhook requests. Please wait a moment and try again."
      ) : new Error(
        `Failed to set up webhook: ${error.message || "Unknown error occurred"}. Please check your robot ID and try again.`
      );
    }
  },
  async onDisable(context) {
    let { robotId } = context.propsValue, webhookId = await context.store.get(TRIGGER_KEY), apiKey = context.auth.secret_text;
    if (!(0, import_pieces_framework7.isNil)(webhookId))
      try {
        await browseAiApiCall({
          method: import_pieces_common7.HttpMethod.DELETE,
          auth: { apiKey },
          resourceUri: `/robots/${robotId}/webhooks/${webhookId}`
        });
      } catch (error) {
        console.warn(
          `Warning: Failed to clean up webhook ${webhookId}:`,
          error.message
        ), await context.store.delete(TRIGGER_KEY);
      }
  },
  async run(context) {
    let payload = context.payload.body;
    return payload.event !== "task.finishedWithError" ? [] : [payload.task];
  },
  async test(context) {
    let { robotId } = context.propsValue, apiKey = context.auth.secret_text;
    return (await browseAiApiCall({
      method: import_pieces_common7.HttpMethod.GET,
      auth: { apiKey },
      resourceUri: `/robots/${robotId}/tasks`,
      query: { status: "failed", sort: "-createdAt" }
    })).result.robotTasks.items;
  },
  sampleData: {}
});

// ../ap/packages/pieces/community/browse-ai/src/lib/triggers/task-finished-successfully.ts
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_framework9 = require("@activepieces/pieces-framework");
var TRIGGER_KEY2 = "browse-ai-task_finished_successfully", taskFinishedSuccessfullyTrigger = (0, import_pieces_framework8.createTrigger)({
  auth: browseAiAuth,
  name: "task_finished_successfully",
  classification: "READ",
  displayName: "Task Finished Successfully",
  description: "Triggers when a robot finishes a task successfully.",
  aiMetadata: {
    description: "Fires when a task run for the selected Browse AI robot completes successfully, delivering the finished task record with its captured/extracted data. Use to process scrape results as soon as they are ready."
  },
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  props: {
    robotId: robotIdDropdown
  },
  async onEnable(context) {
    var _a, _b, _c, _d, _e, _f;
    let { robotId } = context.propsValue, apiKey = context.auth.secret_text;
    try {
      await browseAiApiCall({
        method: import_pieces_common8.HttpMethod.GET,
        auth: { apiKey },
        resourceUri: `/robots/${robotId}`
      });
      let response = await browseAiApiCall({
        method: import_pieces_common8.HttpMethod.POST,
        auth: { apiKey },
        resourceUri: `/robots/${robotId}/webhooks`,
        body: {
          hookUrl: context.webhookUrl,
          eventType: "taskFinishedSuccessfully"
        }
      });
      await context.store.put(TRIGGER_KEY2, response.webhook.id);
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 404 ? new Error(
        `Robot not found: The robot with ID "${robotId}" does not exist or you do not have access to it. Please verify the robot ID and your permissions.`
      ) : ((_b = error.response) == null ? void 0 : _b.status) === 403 ? new Error(
        "Access denied: You do not have permission to set up webhooks for this robot. Please check your Browse AI account permissions and ensure you have webhook access."
      ) : ((_c = error.response) == null ? void 0 : _c.status) === 400 ? new Error(
        `Invalid webhook configuration: ${((_e = (_d = error.response) == null ? void 0 : _d.data) == null ? void 0 : _e.message) || error.message}. Please check your webhook URL and robot ID.`
      ) : ((_f = error.response) == null ? void 0 : _f.status) === 429 ? new Error(
        "Rate limit exceeded: Too many webhook requests. Please wait a moment and try again."
      ) : new Error(
        `Failed to set up webhook: ${error.message || "Unknown error occurred"}. Please check your robot ID and try again.`
      );
    }
  },
  async onDisable(context) {
    let { robotId } = context.propsValue, webhookId = await context.store.get(TRIGGER_KEY2), apiKey = context.auth.secret_text;
    if (!(0, import_pieces_framework9.isNil)(webhookId))
      try {
        await browseAiApiCall({
          method: import_pieces_common8.HttpMethod.DELETE,
          auth: { apiKey },
          resourceUri: `/robots/${robotId}/webhooks/${webhookId}`
        });
      } catch (error) {
        console.warn(
          `Warning: Failed to clean up webhook ${webhookId}:`,
          error.message
        ), await context.store.delete(TRIGGER_KEY2);
      }
  },
  async run(context) {
    let payload = context.payload.body;
    return payload.event !== "task.finishedSuccessfully" ? [] : [payload.task];
  },
  async test(context) {
    let { robotId } = context.propsValue, apiKey = context.auth.secret_text;
    return (await browseAiApiCall({
      method: import_pieces_common8.HttpMethod.GET,
      auth: { apiKey },
      resourceUri: `/robots/${robotId}/tasks`,
      query: { status: "successful", sort: "-createdAt" }
    })).result.robotTasks.items;
  },
  sampleData: {}
});

// ../ap/packages/pieces/community/browse-ai/src/index.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), browseAi = (0, import_pieces_framework10.createPiece)({
  displayName: "Browse AI",
  auth: browseAiAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/browse-ai.png",
  categories: [import_pieces_framework11.PieceCategory.PRODUCTIVITY],
  authors: ["aryel780"],
  actions: [
    getTaskDetailsAction,
    listRobotsAction,
    runRobotAction,
    (0, import_pieces_common9.createCustomApiCallAction)({
      auth: browseAiAuth,
      baseUrl: () => "https://api.browse.ai/v2",
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: [
    taskFinishedWithErrorTrigger,
    taskFinishedSuccessfullyTrigger
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  browseAi
});
