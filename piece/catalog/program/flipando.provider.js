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

// ../ap/packages/pieces/community/flipando/src/index.ts
var index_exports = {};
__export(index_exports, {
  flipando: () => flipando
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/flipando/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), flipandoAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Flipando API Key",
  required: !0
});

// ../ap/packages/pieces/community/flipando/src/lib/actions/run-app.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/flipando/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://flipando-backend.herokuapp.com/api/v1";
async function makeRequest(apiKey, method, path, queryParams, body) {
  var _a, _b, _c, _d;
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers: {
        Authorization: `Bearer ${apiKey}`
      },
      queryParams,
      body
    })).body;
  } catch (error) {
    throw ((_a = error.response) == null ? void 0 : _a.status) === 401 ? new Error(
      "Invalid API Key. Please check your authentication credentials."
    ) : ((_b = error.response) == null ? void 0 : _b.status) === 403 ? new Error("Access denied. Please check your API key permissions.") : ((_c = error.response) == null ? void 0 : _c.status) === 404 ? new Error(
      "Resource not found. Please check the form ID or submission ID."
    ) : ((_d = error.response) == null ? void 0 : _d.status) >= 500 ? new Error("Fillout service error. Please try again later.") : new Error(`Request failed: ${error.message || "Unknown error"}`);
  }
}

// ../ap/packages/pieces/community/flipando/src/lib/actions/run-app.ts
var runApp = (0, import_pieces_framework2.createAction)({
  auth: flipandoAuth,
  name: "runApp",
  displayName: "Run App",
  description: "Triggers the execution of a specific Flipando application by ID, initiating a background job for application completion.",
  audience: "both",
  aiMetadata: {
    description: "Starts a Flipando AI application identified by its numeric application ID, kicking off an asynchronous completion job (retrieve the result later via Get Task). Use it to run a known app with optional JSON input variables and an optional document upload. A file_description is required whenever a file is attached. Not idempotent: each call launches a new run.",
    idempotent: !1
  },
  props: {
    application_id: import_pieces_framework2.Property.Number({
      displayName: "Application ID",
      description: "ID of the application to be completed.",
      required: !0
    }),
    inputs_data: import_pieces_framework2.Property.LongText({
      displayName: "Inputs Data",
      description: "JSON string of key-value pairs for application input variables.",
      required: !1
    }),
    file: import_pieces_framework2.Property.File({
      displayName: "File",
      description: "File to be uploaded, if the application requires documents. Supports PDF, DOCX, images, and other document formats.",
      required: !1
    }),
    file_description: import_pieces_framework2.Property.ShortText({
      displayName: "File Description",
      description: "Description of the file being uploaded. Required when uploading a file.",
      required: !1
    })
  },
  async run(context) {
    let apiKey = context.auth.secret_text, applicationId = context.propsValue.application_id, inputsData = context.propsValue.inputs_data, file = context.propsValue.file, fileDescription = context.propsValue.file_description;
    if (file && !fileDescription)
      throw new Error(
        "file_description is required when uploading a file."
      );
    let formData = new FormData();
    if (inputsData && formData.append("inputs_data", inputsData), file) {
      let blob = new Blob([file.data], { type: "application/octet-stream" });
      formData.append("file", blob, file.filename), fileDescription && formData.append("file_description", fileDescription);
    }
    return await makeRequest(
      apiKey,
      import_pieces_common2.HttpMethod.POST,
      `/integrations/applications/${applicationId}/completion`,
      void 0,
      formData
    );
  }
});

// ../ap/packages/pieces/community/flipando/src/lib/actions/get-task.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var getTask = (0, import_pieces_framework3.createAction)({
  auth: flipandoAuth,
  name: "getTask",
  displayName: "Get Task",
  description: "Retrieves the result of a specific Flipando application task by its task ID.",
  audience: "both",
  aiMetadata: {
    description: "Fetches the current state and result of a Flipando task by its task ID, typically the job returned by Run App or Run App Generator. Use it to poll for or read back the outcome of an asynchronous application run. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    task_id: import_pieces_framework3.Property.ShortText({
      displayName: "Task ID",
      description: "ID of the task to be retrieved.",
      required: !0
    })
  },
  async run(context) {
    let apiKey = context.auth.secret_text, taskId = context.propsValue.task_id;
    return await makeRequest(
      apiKey,
      import_pieces_common3.HttpMethod.GET,
      `/integrations/tasks/${taskId}`
    );
  }
});

// ../ap/packages/pieces/community/flipando/src/lib/actions/run-app-generator.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var runAppGenerator = (0, import_pieces_framework4.createAction)({
  auth: flipandoAuth,
  name: "runAppGenerator",
  displayName: "Run App Generator",
  description: "Triggers the execution of Flipando's Special App Generator, initiating a background job for application completion.",
  audience: "both",
  aiMetadata: {
    description: "Runs Flipando's Special App Generator to build a new application, starting an asynchronous completion job (retrieve the result later via Get Task). Use it to create a new app from JSON input variables rather than running an existing one; the is_new_app_private flag controls whether the generated app is private or public. Not idempotent: each call generates a new app.",
    idempotent: !1
  },
  props: {
    inputs_data: import_pieces_framework4.Property.LongText({
      displayName: "Inputs Data",
      description: "JSON string of key-value pairs for application input variables.",
      required: !1
    }),
    is_new_app_private: import_pieces_framework4.Property.Checkbox({
      displayName: "Is New App Private",
      description: "Whether the new app to be created should be private or public.",
      required: !0,
      defaultValue: !0
    })
  },
  async run(context) {
    let apiKey = context.auth.secret_text, inputsData = context.propsValue.inputs_data, isNewAppPrivate = context.propsValue.is_new_app_private, formData = new FormData();
    return inputsData && formData.append("inputs_data", inputsData), formData.append("is_new_app_private", String(isNewAppPrivate)), await makeRequest(
      apiKey,
      import_pieces_common4.HttpMethod.POST,
      "/integrations/applications/app-generator/completion",
      void 0,
      formData
    );
  }
});

// ../ap/packages/pieces/community/flipando/src/lib/actions/get-all-apps.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var getAllApps = (0, import_pieces_framework5.createAction)({
  auth: flipandoAuth,
  name: "getAllApps",
  displayName: "Get All Apps",
  description: "Retrieves all the applications created by the authenticated user.",
  audience: "both",
  aiMetadata: {
    description: "Lists the Flipando AI applications owned by the authenticated account (capped at 20 results). Use it to discover available app IDs before running an app or to enumerate what the account can execute. Read-only and idempotent.",
    idempotent: !0
  },
  props: {},
  async run(context) {
    let apiKey = context.auth.secret_text;
    return await makeRequest(
      apiKey,
      import_pieces_common5.HttpMethod.GET,
      "/integrations/applications",
      {
        max_results: 20
      }
    );
  }
});

// ../ap/packages/pieces/community/flipando/src/index.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework"), flipando = (0, import_pieces_framework6.createPiece)({
  displayName: "Flipando AI",
  auth: flipandoAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/flipando.png",
  description: "Autonomous AI Agents Revolutionizing Compliance with Smart, Adaptive Workflows.",
  categories: [import_pieces_framework7.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  authors: ["sanket-a11y"],
  actions: [
    getAllApps,
    runApp,
    getTask,
    runAppGenerator,
    (0, import_pieces_common6.createCustomApiCallAction)({
      auth: flipandoAuth,
      baseUrl: () => "https://api.flipando.com/v1",
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  flipando
});
