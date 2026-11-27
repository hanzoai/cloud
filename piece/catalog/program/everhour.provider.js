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

// ../ap/packages/pieces/community/everhour/src/index.ts
var index_exports = {};
__export(index_exports, {
  everhour: () => everhour
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/everhour/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/everhour/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.everhour.com";
async function everhourApiCall({
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
      "X-Api-Key": apiKey,
      "Content-Type": "application/json"
    },
    queryParams: qs,
    body
  };
  return (await import_pieces_common.httpClient.sendRequest(request)).body;
}

// ../ap/packages/pieces/community/everhour/src/lib/auth.ts
var everhourAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "You can find your API key by going to **Settings -> API** in Everhour.",
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await everhourApiCall({
        apiKey: auth,
        method: import_pieces_common2.HttpMethod.GET,
        resourceUri: "/users/me"
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API Key."
      };
    }
  }
});

// ../ap/packages/pieces/community/everhour/src/lib/actions/create-task.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var createTaskAction = (0, import_pieces_framework2.createAction)({
  auth: everhourAuth,
  name: "create-task",
  displayName: "Create Task",
  description: "Creates a new task in a project.",
  audience: "both",
  aiMetadata: { description: "Creates a new task inside an Everhour project, identified by its project ID, with the given task name. Use when an agent needs to add a trackable work item before logging time or starting a timer against it. Not idempotent: each call creates a separate task even with identical input.", idempotent: !1 },
  props: {
    projectId: import_pieces_framework2.Property.ShortText({
      displayName: "Project ID",
      description: "The ID of the project to create the task in.",
      required: !0
    }),
    name: import_pieces_framework2.Property.ShortText({
      displayName: "Task Name",
      description: "The name of the task.",
      required: !0
    })
  },
  async run(context) {
    let { projectId, name } = context.propsValue;
    return await everhourApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common3.HttpMethod.POST,
      resourceUri: `/projects/${projectId}/tasks`,
      body: {
        name
      }
    });
  }
});

// ../ap/packages/pieces/community/everhour/src/lib/actions/start-timer.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");
var startTimerAction = (0, import_pieces_framework3.createAction)({
  auth: everhourAuth,
  name: "start-timer",
  displayName: "Start Timer",
  description: "Starts a timer for a specific task.",
  audience: "both",
  aiMetadata: { description: "Starts a running Everhour time timer against the task identified by the given task ID. Use when an agent needs to begin live time tracking on a task. Requires a valid task ID; not idempotent, since each call starts a new timing session (and only one timer can run at a time).", idempotent: !1 },
  props: {
    taskId: import_pieces_framework3.Property.ShortText({
      displayName: "Task ID",
      description: "The ID of the task to start the timer for.",
      required: !0
    })
  },
  async run(context) {
    let { taskId } = context.propsValue;
    return await everhourApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common4.HttpMethod.POST,
      resourceUri: "/timers",
      body: {
        task: taskId
      }
    });
  }
});

// ../ap/packages/pieces/community/everhour/src/lib/actions/stop-timer.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var stopTimerAction = (0, import_pieces_framework4.createAction)({
  auth: everhourAuth,
  name: "stop-timer",
  displayName: "Stop Timer",
  description: "Stops the currently running timer. Fails if no timer is currently active.",
  audience: "both",
  aiMetadata: { description: "Stops the Everhour timer that is currently running and records its tracked time; takes no input. Use when an agent needs to end the active timing session. Not idempotent: it fails if no timer is active, so it cannot be safely repeated after the timer has already been stopped.", idempotent: !1 },
  props: {},
  async run(context) {
    return await everhourApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common5.HttpMethod.DELETE,
      resourceUri: "/timers/current"
    });
  }
});

// ../ap/packages/pieces/community/everhour/src/index.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), everhour = (0, import_pieces_framework5.createPiece)({
  displayName: "Everhour",
  description: "Time tracking software that integrates into project management tools to track billable hours, set budgets, and monitor spending.",
  logoUrl: "https://cdn.activepieces.com/pieces/everhour.png",
  minimumSupportedRelease: "0.36.1",
  authors: ["Slim-Hady"],
  auth: everhourAuth,
  actions: [
    createTaskAction,
    startTimerAction,
    stopTimerAction,
    (0, import_pieces_common6.createCustomApiCallAction)({
      baseUrl: () => "https://api.everhour.com/",
      auth: everhourAuth,
      authMapping: async (auth) => ({
        "X-Api-Key": `${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  everhour
});
