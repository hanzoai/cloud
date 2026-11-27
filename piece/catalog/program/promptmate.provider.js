"use strict";
var __defProp = Object.defineProperty, __defProps = Object.defineProperties, __getOwnPropDesc = Object.getOwnPropertyDescriptor, __getOwnPropDescs = Object.getOwnPropertyDescriptors, __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
}, __spreadProps = (a, b) => __defProps(a, __getOwnPropDescs(b));
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

// ../ap/packages/pieces/community/promptmate/src/index.ts
var index_exports = {};
__export(index_exports, {
  promptmate: () => promptmate,
  promptmateAuth: () => promptmateAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common11 = require("@activepieces/pieces-common"), import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_framework11 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/promptmate/src/lib/actions/list-apps.ts
var import_pieces_framework = require("@activepieces/pieces-framework");
var import_pieces_common = require("@activepieces/pieces-common"), listApps = (0, import_pieces_framework.createAction)({
  auth: promptmateAuth,
  name: "list_apps",
  displayName: "List Apps",
  description: "Retrieve a list of available apps",
  audience: "both",
  aiMetadata: { description: "Lists all PromptMate apps available to the authenticated account, returning each app id, name, credit estimate, and its data fields. Use it to discover which app to run or to look up an appId before calling run, get-job-status, get-app-details, or get-last-results. Read-only and safe to repeat.", idempotent: !0 },
  props: {},
  async run({ auth }) {
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: "https://api.promptmate.io/v1/apps",
      headers: {
        "x-api-key": auth.secret_text
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/promptmate/src/lib/actions/run-app.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/promptmate/src/lib/common/index.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), getApps = async (auth) => (await import_pieces_common2.httpClient.sendRequest({
  method: import_pieces_common2.HttpMethod.GET,
  url: "https://api.promptmate.io/v1/apps",
  headers: {
    "x-api-key": auth
  }
})).body, getAppDropdownOptions = async (auth) => {
  if (!auth)
    return {
      disabled: !0,
      placeholder: "Enter your API key first",
      options: []
    };
  try {
    return {
      disabled: !1,
      options: (await getApps(auth)).map((app) => ({
        label: app.appName,
        value: app.appId
      }))
    };
  } catch (error) {
    return {
      disabled: !0,
      options: [],
      placeholder: "Couldn't load apps, API key is invalid"
    };
  }
};

// ../ap/packages/pieces/community/promptmate/src/lib/actions/run-app.ts
var runApp = (0, import_pieces_framework2.createAction)({
  auth: promptmateAuth,
  name: "run_app",
  displayName: "Run PromptMate App",
  description: "Runs a PromptMate app with the specified data",
  audience: "both",
  aiMetadata: { description: "Submits a job to run a PromptMate app over an array of data objects, where each object must supply the data fields the chosen app expects (look these up via list-apps or get-app-details). Use it to actually execute an app and produce results; the call returns a job that you then poll with get-job-status. Optionally accepts a callback URL for async delivery. Not idempotent \u2014 each call starts a new job and consumes credits.", idempotent: !1 },
  props: {
    appId: import_pieces_framework2.Property.Dropdown({
      auth: promptmateAuth,
      displayName: "App",
      description: "Select the PromptMate app to run",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => auth ? await getAppDropdownOptions(auth.secret_text) : {
        options: [],
        disabled: !0
      }
    }),
    data: import_pieces_framework2.Property.Array({
      displayName: "Data",
      description: "Array of data objects to process. Each object must contain the required data fields for the selected app.",
      required: !0
    }),
    callBackUrl: import_pieces_framework2.Property.ShortText({
      displayName: "Callback URL",
      description: "URL to which the result of the job will be sent (optional)",
      required: !1
    }),
    noMailOnFinish: import_pieces_framework2.Property.Checkbox({
      displayName: "Disable Email Notification",
      description: "Send an email to the user when the job has finished",
      required: !1,
      defaultValue: !0
    }),
    config: import_pieces_framework2.Property.Object({
      displayName: "Configuration",
      description: "Configuration settings for the job (language, country, etc.)",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let { appId, data, callBackUrl, noMailOnFinish, config } = propsValue, requestBody = {
      appId,
      data
    };
    return callBackUrl && (requestBody.callBackUrl = callBackUrl), noMailOnFinish !== void 0 && (requestBody.noMailOnFinish = noMailOnFinish), config && (requestBody.config = config), (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: "https://api.promptmate.io/v1/app-jobs",
      headers: {
        "x-api-key": auth.secret_text,
        "Content-Type": "application/json"
      },
      body: requestBody
    })).body;
  }
});

// ../ap/packages/pieces/community/promptmate/src/lib/actions/get-job-status.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common");
var getJobStatus = (0, import_pieces_framework3.createAction)({
  auth: promptmateAuth,
  name: "get_job_status",
  displayName: "Get Job Status and Result",
  description: "Retrieve the status and results of a PromptMate app job",
  audience: "both",
  aiMetadata: { description: "Looks up the current status and any available results for a single PromptMate app job, given its appId and jobId (the jobId comes from a prior run-app call). Use it to poll a submitted job until it completes and to fetch its output; optionally restrict to the default result fields only. Read-only and safe to repeat.", idempotent: !0 },
  props: {
    appId: import_pieces_framework3.Property.Dropdown({
      displayName: "App",
      description: "Select the app for which to get job status",
      required: !0,
      refreshers: [],
      auth: promptmateAuth,
      options: async ({ auth }) => auth ? await getAppDropdownOptions(auth.secret_text) : {
        options: [],
        disabled: !0
      }
    }),
    jobId: import_pieces_framework3.Property.ShortText({
      displayName: "Job ID",
      description: "The ID of the job to check",
      required: !0
    }),
    onlyDefaultResultFields: import_pieces_framework3.Property.Checkbox({
      displayName: "Only Default Result Fields",
      description: "Return only the default result fields",
      required: !1,
      defaultValue: !0
    })
  },
  async run({ auth, propsValue }) {
    let { appId, jobId, onlyDefaultResultFields } = propsValue, queryParams = {
      appId,
      jobId
    };
    return onlyDefaultResultFields !== void 0 && (queryParams.onlyDefaultResultFields = onlyDefaultResultFields.toString()), (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.GET,
      url: "https://api.promptmate.io/v1/app-jobs",
      headers: {
        "x-api-key": auth.secret_text
      },
      queryParams
    })).body;
  }
});

// ../ap/packages/pieces/community/promptmate/src/lib/actions/get-app-details.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common");
var getAppDetails = (0, import_pieces_framework4.createAction)({
  auth: promptmateAuth,
  name: "get_app_details",
  displayName: "Get App Details",
  description: "Retrieve detailed information about a specific PromptMate app",
  audience: "both",
  aiMetadata: { description: "Fetches the full configuration and metadata of one PromptMate app by appId. Use it to inspect an app's expected data fields and settings before constructing input for run-app. Read-only and safe to repeat.", idempotent: !0 },
  props: {
    appId: import_pieces_framework4.Property.Dropdown({
      displayName: "App",
      description: "Select the app to get details for",
      required: !0,
      refreshers: [],
      auth: promptmateAuth,
      options: async ({ auth }) => auth ? await getAppDropdownOptions(auth.secret_text) : {
        options: [],
        disabled: !0
      }
    })
  },
  async run({ auth, propsValue }) {
    let { appId } = propsValue;
    return (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.GET,
      url: "https://api.promptmate.io/v1/app",
      headers: {
        "x-api-key": auth.secret_text
      },
      queryParams: {
        appId
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/promptmate/src/lib/actions/get-last-results.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common");
var getLastResults = (0, import_pieces_framework5.createAction)({
  auth: promptmateAuth,
  name: "get_last_results",
  displayName: "Get Last Result Rows",
  description: "Retrieve the last result rows of a PromptMate app for examples or testing",
  audience: "both",
  aiMetadata: { description: "Retrieves the most recent result rows produced by a given PromptMate app (appId), capped by an optional limit, optionally returning only the default result fields. Use it to sample an app's recent output for examples, testing, or to inspect its result shape without running a new job. Read-only and safe to repeat.", idempotent: !0 },
  props: {
    appId: import_pieces_framework5.Property.Dropdown({
      displayName: "App",
      description: "Select the app to get last results for",
      required: !0,
      refreshers: [],
      auth: promptmateAuth,
      options: async ({ auth }) => auth ? await getAppDropdownOptions(auth.secret_text) : {
        options: [],
        disabled: !0
      }
    }),
    onlyDefaultResultFields: import_pieces_framework5.Property.Checkbox({
      displayName: "Only Default Result Fields",
      description: "Return only the default result fields",
      required: !1,
      defaultValue: !0
    }),
    limit: import_pieces_framework5.Property.Number({
      displayName: "Limit",
      description: "Maximum number of result rows to return",
      required: !1,
      defaultValue: 20
    })
  },
  async run({ auth, propsValue }) {
    let { appId, onlyDefaultResultFields, limit } = propsValue, queryParams = {
      appId
    };
    return onlyDefaultResultFields !== void 0 && (queryParams.onlyDefaultResultFields = onlyDefaultResultFields.toString()), limit !== void 0 && (queryParams.limit = limit.toString()), (await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.GET,
      url: "https://api.promptmate.io/v1/app-results",
      headers: {
        "x-api-key": auth.secret_text
      },
      queryParams
    })).body;
  }
});

// ../ap/packages/pieces/community/promptmate/src/lib/actions/use-template.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common"), useTemplate = (0, import_pieces_framework6.createAction)({
  auth: promptmateAuth,
  name: "use_template",
  displayName: "Use Template",
  description: "Create an app from a specific PromptMate template",
  audience: "both",
  aiMetadata: { description: "Creates a new PromptMate app from a template, identified by its templateId. Use it to provision an app from a prebuilt template before running it. Not idempotent \u2014 each call creates a new app.", idempotent: !1 },
  props: {
    templateId: import_pieces_framework6.Property.ShortText({
      displayName: "Template ID",
      description: "The ID of the template to use",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let { templateId } = propsValue;
    return (await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.POST,
      url: "https://api.promptmate.io/v1/templates/use",
      headers: {
        "x-api-key": auth.secret_text,
        "Content-Type": "application/json"
      },
      body: {
        templateId
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/promptmate/src/lib/actions/get-user-info.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var import_pieces_common8 = require("@activepieces/pieces-common"), getUserInfo = (0, import_pieces_framework7.createAction)({
  auth: promptmateAuth,
  name: "get_user_info",
  displayName: "Get User Information",
  description: "Retrieve user information associated with the API key",
  audience: "both",
  aiMetadata: { description: "Returns the account/user profile tied to the authenticated PromptMate API key. Use it to identify the current account or verify that credentials are valid. Takes no input; read-only and safe to repeat.", idempotent: !0 },
  props: {},
  async run({ auth }) {
    return (await import_pieces_common8.httpClient.sendRequest({
      method: import_pieces_common8.HttpMethod.GET,
      url: "https://api.promptmate.io/v1/userInfo",
      headers: {
        "x-api-key": auth.secret_text
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/promptmate/src/lib/actions/list-projects.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var import_pieces_common9 = require("@activepieces/pieces-common"), listProjects = (0, import_pieces_framework8.createAction)({
  auth: promptmateAuth,
  name: "list_projects",
  displayName: "List Projects",
  description: "Retrieve a list of available PromptMate projects",
  audience: "both",
  aiMetadata: { description: "Lists the PromptMate projects available to the authenticated account. Use it to discover which projects exist for organizing apps. Takes no input; read-only and safe to repeat.", idempotent: !0 },
  props: {},
  async run({ auth }) {
    return (await import_pieces_common9.httpClient.sendRequest({
      method: import_pieces_common9.HttpMethod.GET,
      url: "https://api.promptmate.io/v1/projects",
      headers: {
        "x-api-key": auth.secret_text
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/promptmate/src/lib/triggers/new-app-result.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var import_pieces_common10 = require("@activepieces/pieces-common");
var newAppResult = (0, import_pieces_framework9.createTrigger)({
  auth: promptmateAuth,
  name: "new_app_result",
  displayName: "New App Result",
  description: "Triggers when there is a new app result from PromptMate",
  aiMetadata: {
    description: "Fires via webhook when a PromptMate app produces a new result, either at the row level (once per individual result row) or the job level (once when a whole job finishes), depending on the configured trigger level. Can optionally be restricted to specific apps; left unrestricted it fires for results from any app on the account."
  },
  props: {
    restrictedAppIds: import_pieces_framework9.Property.MultiSelectDropdown({
      displayName: "Restrict to Apps",
      description: "Only trigger for results from these specific apps. Leave empty to trigger for all apps.",
      required: !1,
      refreshers: [],
      auth: promptmateAuth,
      options: async ({ auth }) => {
        if (!auth)
          return {
            options: [],
            disabled: !0
          };
        let options = await getAppDropdownOptions(auth.secret_text);
        return __spreadProps(__spreadValues({}, options), {
          options: options.options || []
        });
      }
    }),
    webhookType: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Trigger Level",
      description: "Choose when to trigger the webhook",
      required: !0,
      defaultValue: "row",
      options: {
        options: [
          {
            label: "Row Level",
            value: "row"
          },
          {
            label: "Job Level",
            value: "job"
          }
        ]
      }
    }),
    webhookReference: import_pieces_framework9.Property.ShortText({
      displayName: "Webhook Reference",
      description: "Optional reference for this webhook (used for identification)",
      required: !1
    })
  },
  sampleData: {
    jobId: "job_123456",
    jobStatus: "completed",
    results: [
      {
        input: { field1: "value1", field2: "value2" },
        output: { result: "processed data" },
        status: "success"
      }
    ]
  },
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let { restrictedAppIds, webhookType, webhookReference } = context.propsValue, webhookName = `ActivePieces-${webhookReference || `webhook-${Date.now()}`}`, requestBody = {
      webhookName,
      webhookType,
      endpointUrl: context.webhookUrl
    };
    webhookReference && (requestBody.webhookReference = webhookReference), restrictedAppIds && restrictedAppIds.length > 0 && (requestBody.restrictedAppIds = restrictedAppIds);
    let webhook = (await import_pieces_common10.httpClient.sendRequest({
      method: import_pieces_common10.HttpMethod.POST,
      url: "https://api.promptmate.io/v1/webhooks",
      headers: {
        "x-api-key": context.auth.secret_text,
        "Content-Type": "application/json"
      },
      body: requestBody
    })).body[0];
    if (!webhook)
      throw new Error("Failed to create webhook");
    await context.store.put("promptmate_webhook", {
      webhookId: webhook.webhookId,
      webhookReference: webhookReference || webhookName
    });
  },
  async onDisable(context) {
    let webhookInfo = await context.store.get("promptmate_webhook");
    webhookInfo && await import_pieces_common10.httpClient.sendRequest({
      method: import_pieces_common10.HttpMethod.DELETE,
      url: "https://api.promptmate.io/v1/webhooks",
      headers: {
        "x-api-key": context.auth.secret_text,
        "Content-Type": "application/json"
      },
      body: {
        webhookId: webhookInfo.webhookId
      }
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/promptmate/src/index.ts
var promptmateAuth = import_pieces_framework10.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Your PromptMate API key",
  required: !0,
  validate: async (auth) => {
    try {
      return await import_pieces_common11.httpClient.sendRequest({
        url: "https://api.promptmate.io/v1/apps",
        method: import_pieces_common11.HttpMethod.GET,
        headers: {
          "x-api-key": auth.auth
        }
      }), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API key"
      };
    }
  }
}), promptmate = (0, import_pieces_framework10.createPiece)({
  displayName: "PromptMate",
  description: "AI-powered automation platform",
  auth: promptmateAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/promptmate.png",
  categories: [import_pieces_framework11.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  authors: ["onyedikachi-david"],
  actions: [
    listApps,
    runApp,
    getJobStatus,
    getAppDetails,
    getLastResults,
    useTemplate,
    getUserInfo,
    listProjects,
    (0, import_pieces_common11.createCustomApiCallAction)({
      baseUrl: () => "https://api.promptmate.io/v1",
      auth: promptmateAuth,
      authMapping: async (auth) => ({
        "x-api-key": auth.secret_text
      })
    })
  ],
  triggers: [newAppResult]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  promptmate,
  promptmateAuth
});
