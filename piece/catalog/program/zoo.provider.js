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

// ../ap/packages/pieces/community/zoo/src/index.ts
var index_exports = {};
__export(index_exports, {
  textToCad: () => textToCad
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework83 = require("@activepieces/pieces-framework"), import_pieces_framework84 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/zoo/src/lib/actions/ml/generate-cad-model.action.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/zoo/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), zooAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "Your Zoo API Key (Bearer Token)."
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/ml/generate-cad-model.action.ts
var import_pieces_common = require("@activepieces/pieces-common"), generateCadModelAction = (0, import_pieces_framework2.createAction)({
  name: "generate_cad_model",
  displayName: "Generate CAD Model",
  description: "Generate a 3D model from text prompt",
  audience: "both",
  aiMetadata: { description: "Submit a text prompt to Zoo's text-to-CAD ML model to generate a new 3D model in a chosen export format (FBX, GLB, GLTF, OBJ, PLY, STEP, or STL). Each call starts a new generation job, so it is not idempotent. Optionally include KCL source in the output; retrieve finished results with the get CAD model action.", idempotent: !1 },
  auth: zooAuth,
  // category: 'Machine Learning (ML)',
  props: {
    outputFormat: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Output Format",
      required: !0,
      options: {
        options: [
          { label: "FBX", value: "fbx" },
          { label: "GLB", value: "glb" },
          { label: "GLTF", value: "gltf" },
          { label: "OBJ", value: "obj" },
          { label: "PLY", value: "ply" },
          { label: "STEP", value: "step" },
          { label: "STL", value: "stl" }
        ]
      }
    }),
    outputKcl: import_pieces_framework2.Property.Checkbox({
      displayName: "Include KCL Output",
      required: !1
    }),
    prompt: import_pieces_framework2.Property.ShortText({
      displayName: "Prompt",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: `https://api.zoo.dev/ai/text-to-cad/${propsValue.outputFormat}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      body: {
        output_kcl: propsValue.outputKcl,
        prompt: propsValue.prompt
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/ml/kcl-completions.action.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common2 = require("@activepieces/pieces-common"), kclCompletionsAction = (0, import_pieces_framework3.createAction)({
  name: "kcl_completions",
  displayName: "KCL Code Completions",
  description: "Get code completions for KCL (Kernel Configuration Language)",
  audience: "both",
  aiMetadata: { description: "Generate KCL (Zoo Kernel Configuration Language) code completions from a prompt using Zoo's ML model. Pick when authoring or extending KCL scripts for CAD geometry. Output varies between calls (non-deterministic) and is tunable via temperature, max tokens, and stop sequences.", idempotent: !1 },
  auth: zooAuth,
  // category: 'Machine Learning (ML)',
  props: {
    prompt: import_pieces_framework3.Property.LongText({
      displayName: "Prompt",
      required: !0,
      description: "The KCL code prompt to get completions for"
    }),
    temperature: import_pieces_framework3.Property.Number({
      displayName: "Temperature",
      required: !1,
      description: "Controls randomness in completion generation (0.0 to 1.0)"
    }),
    maxTokens: import_pieces_framework3.Property.Number({
      displayName: "Max Tokens",
      required: !1,
      description: "Maximum number of tokens to generate"
    }),
    stop: import_pieces_framework3.Property.Array({
      displayName: "Stop Sequences",
      required: !1,
      description: "Sequences where the API will stop generating further tokens"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://api.zoo.dev/ml/kcl/completions",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      body: {
        prompt: propsValue.prompt,
        temperature: propsValue.temperature,
        max_tokens: propsValue.maxTokens,
        stop: propsValue.stop
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/ml/text-to-cad-iteration.action.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common"), textToCadIterationAction = (0, import_pieces_framework4.createAction)({
  name: "text_to_cad_iteration",
  displayName: "Iterate CAD Model",
  description: "Create a new iteration of an existing 3D model",
  audience: "both",
  aiMetadata: { description: "Generate a new CAD model iteration from a text prompt describing desired changes. Use to refine or evolve a design via natural language; to rate an existing result instead, use give-model-feedback. Not idempotent: each call starts a fresh generation, so avoid blind retries.", idempotent: !1 },
  auth: zooAuth,
  // category: 'Machine Learning (ML)',
  props: {
    prompt: import_pieces_framework4.Property.ShortText({
      displayName: "Prompt",
      required: !0,
      description: "The prompt describing desired changes"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: "https://api.zoo.dev/ml/text-to-cad/iteration",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      body: {
        prompt: propsValue.prompt
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/ml/list-cad-models.action.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common");
var listCadModelsAction = (0, import_pieces_framework5.createAction)({
  name: "list_cad_models",
  displayName: "List CAD Models",
  description: "Retrieve a list of your generated 3D models",
  audience: "both",
  aiMetadata: { description: "List the text-to-CAD 3D models generated by the authenticated user, with optional limit/offset paging. Use to browse past generations or find a model ID to feed into give-model-feedback or an iteration. Read-only with no side effects.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Machine Learning (ML)',
  props: {
    limit: import_pieces_framework5.Property.Number({
      displayName: "Limit",
      required: !1,
      description: "Maximum number of models to return"
    }),
    offset: import_pieces_framework5.Property.Number({
      displayName: "Offset",
      required: !1,
      description: "Number of models to skip"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.GET,
      url: "https://api.zoo.dev/user/text-to-cad",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      queryParams: __spreadValues(__spreadValues({}, propsValue.limit && { limit: propsValue.limit.toString() }), propsValue.offset && { offset: propsValue.offset.toString() })
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/ml/get-cad-model.action.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common"), getCadModelAction = (0, import_pieces_framework6.createAction)({
  name: "get_cad_model",
  displayName: "Get CAD Model",
  description: "Retrieve details of a specific 3D model",
  audience: "both",
  aiMetadata: { description: "Look up a single previously generated text-to-CAD model by its model ID, including its status and conversion results. Read-only and repeatable. Use to poll or fetch a model created by the generate CAD model action; requires a known model ID.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Machine Learning (ML)',
  props: {
    modelId: import_pieces_framework6.Property.ShortText({
      displayName: "Model ID",
      required: !0,
      description: "The ID of the model to retrieve"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.GET,
      url: `https://api.zoo.dev/user/text-to-cad/${propsValue.modelId}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/ml/give-model-feedback.action.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common"), giveModelFeedbackAction = (0, import_pieces_framework7.createAction)({
  name: "give_model_feedback",
  displayName: "Give Model Feedback",
  description: "Provide feedback on a generated 3D model",
  audience: "both",
  aiMetadata: { description: "Record feedback on a text-to-CAD model identified by its model ID, choosing thumbs_up, thumbs_down, accepted, or rejected. Use to rate a generation; this mutates the model's stored feedback, so repeated calls overwrite or re-submit the rating rather than being a no-op.", idempotent: !1 },
  auth: zooAuth,
  // category: 'Machine Learning (ML)',
  props: {
    modelId: import_pieces_framework7.Property.ShortText({
      displayName: "Model ID",
      required: !0,
      description: "The ID of the model to give feedback on"
    }),
    feedback: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Feedback Type",
      required: !0,
      options: {
        options: [
          { label: "Thumbs Up", value: "thumbs_up" },
          { label: "Thumbs Down", value: "thumbs_down" },
          { label: "Accepted", value: "accepted" },
          { label: "Rejected", value: "rejected" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.POST,
      url: `https://api.zoo.dev/user/text-to-cad/${propsValue.modelId}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      body: {
        feedback: propsValue.feedback
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/index.ts
var ML_ACTIONS = [
  generateCadModelAction,
  kclCompletionsAction,
  textToCadIterationAction,
  listCadModelsAction,
  getCadModelAction,
  giveModelFeedbackAction
];

// ../ap/packages/pieces/community/zoo/src/lib/actions/api-calls/get-async-operation.action.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common"), getAsyncOperationAction = (0, import_pieces_framework8.createAction)({
  name: "get_async_operation",
  displayName: "Get Async Operation",
  description: "Retrieve details of an asynchronous operation",
  audience: "both",
  aiMetadata: { description: "Fetch the current state of a long-running async operation by its operation ID, including status and result when complete. Use to poll the outcome of asynchronous jobs such as conversions or model generation. Read-only; safe to call repeatedly while polling.", idempotent: !0 },
  auth: zooAuth,
  // category: 'API Calls',
  props: {
    operationId: import_pieces_framework8.Property.ShortText({
      displayName: "Operation ID",
      required: !0,
      description: "The ID of the async operation to retrieve"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.GET,
      url: `https://api.zoo.dev/async/operations/${propsValue.operationId}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/api-calls/list-org-api-calls.action.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var import_pieces_common8 = require("@activepieces/pieces-common");
var listOrgApiCallsAction = (0, import_pieces_framework9.createAction)({
  name: "list_org_api_calls",
  displayName: "List Organization API Calls",
  description: "List API calls made by your organization",
  audience: "both",
  aiMetadata: { description: "List API calls billed to the authenticated organization, with optional limit/offset paging. Use the org scope here; for calls tied to your own user use list-user-api-calls, and to fetch one known call use get-org-api-call. Read-only with no side effects.", idempotent: !0 },
  auth: zooAuth,
  // category: 'API Calls',
  props: {
    limit: import_pieces_framework9.Property.Number({
      displayName: "Limit",
      required: !1,
      description: "Maximum number of API calls to return"
    }),
    offset: import_pieces_framework9.Property.Number({
      displayName: "Offset",
      required: !1,
      description: "Number of API calls to skip"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common8.httpClient.sendRequest({
      method: import_pieces_common8.HttpMethod.GET,
      url: "https://api.zoo.dev/org/api-calls",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      queryParams: __spreadValues(__spreadValues({}, propsValue.limit && { limit: propsValue.limit.toString() }), propsValue.offset && { offset: propsValue.offset.toString() })
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/api-calls/get-org-api-call.action.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var import_pieces_common9 = require("@activepieces/pieces-common"), getOrgApiCallAction = (0, import_pieces_framework10.createAction)({
  name: "get_org_api_call",
  displayName: "Get Organization API Call",
  description: "Retrieve details of a specific API call made by your organization",
  audience: "both",
  aiMetadata: { description: "Fetch a single API call billed to the authenticated organization by its call ID, including status, cost, and timing. Use the org scope here; for a call tied to your own user use get-user-api-call, and to browse without an ID use list-org-api-calls. Read-only lookup with no side effects.", idempotent: !0 },
  auth: zooAuth,
  // category: 'API Calls',
  props: {
    callId: import_pieces_framework10.Property.ShortText({
      displayName: "Call ID",
      required: !0,
      description: "The ID of the API call to retrieve"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common9.httpClient.sendRequest({
      method: import_pieces_common9.HttpMethod.GET,
      url: `https://api.zoo.dev/org/api-calls/${propsValue.callId}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/api-calls/list-user-api-calls.action.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var import_pieces_common10 = require("@activepieces/pieces-common");
var listUserApiCallsAction = (0, import_pieces_framework11.createAction)({
  name: "list_user_api_calls",
  displayName: "List User API Calls",
  description: "List API calls made by your user account",
  audience: "both",
  aiMetadata: { description: "List API calls in the authenticated user's history, with optional limit/offset paging. Use the user scope here; for calls billed to the whole organization use list-org-api-calls, and to fetch one known call use get-user-api-call. Read-only with no side effects.", idempotent: !0 },
  auth: zooAuth,
  // category: 'API Calls',
  props: {
    limit: import_pieces_framework11.Property.Number({
      displayName: "Limit",
      required: !1,
      description: "Maximum number of API calls to return"
    }),
    offset: import_pieces_framework11.Property.Number({
      displayName: "Offset",
      required: !1,
      description: "Number of API calls to skip"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common10.httpClient.sendRequest({
      method: import_pieces_common10.HttpMethod.GET,
      url: "https://api.zoo.dev/user/api-calls",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      queryParams: __spreadValues(__spreadValues({}, propsValue.limit && { limit: propsValue.limit.toString() }), propsValue.offset && { offset: propsValue.offset.toString() })
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/api-calls/get-user-api-call.action.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var import_pieces_common11 = require("@activepieces/pieces-common"), getUserApiCallAction = (0, import_pieces_framework12.createAction)({
  name: "get_user_api_call",
  displayName: "Get User API Call",
  description: "Retrieve details of a specific API call made by your user account",
  audience: "both",
  aiMetadata: { description: "Fetch a single API call from the authenticated user's history by its call ID, including status, cost, and timing. Use the user scope here; for a call billed to the organization use get-org-api-call, and to browse without an ID use list-user-api-calls. Read-only lookup with no side effects.", idempotent: !0 },
  auth: zooAuth,
  // category: 'API Calls',
  props: {
    callId: import_pieces_framework12.Property.ShortText({
      displayName: "Call ID",
      required: !0,
      description: "The ID of the API call to retrieve"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common11.httpClient.sendRequest({
      method: import_pieces_common11.HttpMethod.GET,
      url: `https://api.zoo.dev/user/api-calls/${propsValue.callId}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/api-calls/index.ts
var API_CALLS_ACTIONS = [
  getAsyncOperationAction,
  listOrgApiCallsAction,
  getOrgApiCallAction,
  listUserApiCallsAction,
  getUserApiCallAction
];

// ../ap/packages/pieces/community/zoo/src/lib/actions/api-tokens/list-api-tokens.action.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var import_pieces_common12 = require("@activepieces/pieces-common");
var listApiTokensAction = (0, import_pieces_framework13.createAction)({
  name: "list_api_tokens",
  displayName: "List API Tokens",
  description: "List all API tokens for your user account",
  audience: "both",
  aiMetadata: { description: "List the API tokens on the authenticated user's account, with optional limit and offset paging. Use to enumerate tokens or find a token value to pass to the get or delete API token actions. Read-only and idempotent.", idempotent: !0 },
  auth: zooAuth,
  // category: 'API Tokens',
  props: {
    limit: import_pieces_framework13.Property.Number({
      displayName: "Limit",
      required: !1,
      description: "Maximum number of tokens to return"
    }),
    offset: import_pieces_framework13.Property.Number({
      displayName: "Offset",
      required: !1,
      description: "Number of tokens to skip"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common12.httpClient.sendRequest({
      method: import_pieces_common12.HttpMethod.GET,
      url: "https://api.zoo.dev/user/api-tokens",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      queryParams: __spreadValues(__spreadValues({}, propsValue.limit && { limit: propsValue.limit.toString() }), propsValue.offset && { offset: propsValue.offset.toString() })
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/api-tokens/create-api-token.action.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var import_pieces_common13 = require("@activepieces/pieces-common"), createApiTokenAction = (0, import_pieces_framework14.createAction)({
  name: "create_api_token",
  displayName: "Create API Token",
  description: "Create a new API token for your user account",
  audience: "both",
  aiMetadata: { description: "Create a new API token on the authenticated user's account with the given name and return the token value. Use to provision programmatic access for the user. Not idempotent: each call mints a distinct token even if the name repeats.", idempotent: !1 },
  auth: zooAuth,
  // category: 'API Tokens',
  props: {
    name: import_pieces_framework14.Property.ShortText({
      displayName: "Token Name",
      required: !0,
      description: "A name to identify this token"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common13.httpClient.sendRequest({
      method: import_pieces_common13.HttpMethod.POST,
      url: "https://api.zoo.dev/user/api-tokens",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      body: {
        name: propsValue.name
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/api-tokens/get-api-token.action.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");
var import_pieces_common14 = require("@activepieces/pieces-common"), getApiTokenAction = (0, import_pieces_framework15.createAction)({
  name: "get_api_token",
  displayName: "Get API Token",
  description: "Retrieve details of a specific API token",
  audience: "both",
  aiMetadata: { description: "Look up one API token by its value on the authenticated user's account and return its details. Use when you already have the token value; to discover tokens first use the list API tokens action. Read-only and idempotent.", idempotent: !0 },
  auth: zooAuth,
  // category: 'API Tokens',
  props: {
    token: import_pieces_framework15.Property.ShortText({
      displayName: "Token",
      required: !0,
      description: "The token to retrieve details for"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common14.httpClient.sendRequest({
      method: import_pieces_common14.HttpMethod.GET,
      url: `https://api.zoo.dev/user/api-tokens/${propsValue.token}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/api-tokens/delete-api-token.action.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework");
var import_pieces_common15 = require("@activepieces/pieces-common"), deleteApiTokenAction = (0, import_pieces_framework16.createAction)({
  name: "delete_api_token",
  displayName: "Delete API Token",
  description: "Delete an API token from your user account",
  audience: "both",
  aiMetadata: { description: "Permanently revoke the API token identified by its value from the authenticated user's account. Use to invalidate a token; destructive. Not strictly idempotent: a first call deletes it and a repeat may error if the token no longer exists.", idempotent: !1 },
  auth: zooAuth,
  // category: 'API Tokens',
  props: {
    token: import_pieces_framework16.Property.ShortText({
      displayName: "Token",
      required: !0,
      description: "The token to delete"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common15.httpClient.sendRequest({
      method: import_pieces_common15.HttpMethod.DELETE,
      url: `https://api.zoo.dev/user/api-tokens/${propsValue.token}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/api-tokens/index.ts
var API_TOKENS_ACTIONS = [
  listApiTokensAction,
  createApiTokenAction,
  getApiTokenAction,
  deleteApiTokenAction
];

// ../ap/packages/pieces/community/zoo/src/lib/actions/file/get-center-of-mass.action.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework");
var import_pieces_common16 = require("@activepieces/pieces-common"), getCenterOfMassAction = (0, import_pieces_framework17.createAction)({
  name: "get_center_of_mass",
  displayName: "Get Center of Mass",
  description: "Calculate the center of mass of a CAD file",
  audience: "both",
  aiMetadata: { description: "Compute the center-of-mass coordinates of a 3D solid from an uploaded CAD file (FBX, GLB, glTF, OBJ, PLY, STEP, or STL). Pick this for the centroid/balance-point specifically; sibling actions cover mass, density, volume, and surface area. Read-only analysis that does not modify the file and returns the same value for the same input.", idempotent: !0 },
  auth: zooAuth,
  // category: 'File Operations',
  props: {
    file: import_pieces_framework17.Property.File({
      displayName: "CAD File",
      required: !0,
      description: "The CAD file to analyze"
    })
  },
  async run({ auth, propsValue }) {
    let formData = new FormData();
    return formData.append("file", new Blob([propsValue.file.data]), propsValue.file.filename), (await import_pieces_common16.httpClient.sendRequest({
      method: import_pieces_common16.HttpMethod.POST,
      url: "https://api.zoo.dev/file/center-of-mass",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      body: formData
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/file/convert-cad-file.action.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework");
var import_pieces_common17 = require("@activepieces/pieces-common"), convertCadFileAction = (0, import_pieces_framework18.createAction)({
  name: "convert_cad_file",
  displayName: "Convert CAD File",
  description: "Convert a CAD file from one format to another",
  audience: "both",
  aiMetadata: { description: "Convert an uploaded CAD file from one 3D format to another; both source and output formats must be chosen from FBX, GLB, glTF, OBJ, PLY, STEP, and STL. Use this for format translation only, not for measuring mass properties (see the get-mass/volume/density/surface-area/center-of-mass actions). The source file is left untouched and the conversion is deterministic for the same inputs.", idempotent: !0 },
  auth: zooAuth,
  // category: 'File Operations',
  props: {
    file: import_pieces_framework18.Property.File({
      displayName: "CAD File",
      required: !0,
      description: "The CAD file to convert"
    }),
    sourceFormat: import_pieces_framework18.Property.StaticDropdown({
      displayName: "Source Format",
      required: !0,
      options: {
        options: [
          { label: "FBX", value: "fbx" },
          { label: "GLB", value: "glb" },
          { label: "GLTF", value: "gltf" },
          { label: "OBJ", value: "obj" },
          { label: "PLY", value: "ply" },
          { label: "STEP", value: "step" },
          { label: "STL", value: "stl" }
        ]
      }
    }),
    outputFormat: import_pieces_framework18.Property.StaticDropdown({
      displayName: "Output Format",
      required: !0,
      options: {
        options: [
          { label: "FBX", value: "fbx" },
          { label: "GLB", value: "glb" },
          { label: "GLTF", value: "gltf" },
          { label: "OBJ", value: "obj" },
          { label: "PLY", value: "ply" },
          { label: "STEP", value: "step" },
          { label: "STL", value: "stl" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    let formData = new FormData();
    return formData.append("file", new Blob([propsValue.file.data]), propsValue.file.filename), (await import_pieces_common17.httpClient.sendRequest({
      method: import_pieces_common17.HttpMethod.POST,
      url: `https://api.zoo.dev/file/conversion/${propsValue.sourceFormat}/${propsValue.outputFormat}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      body: formData
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/file/get-density.action.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework");
var import_pieces_common18 = require("@activepieces/pieces-common"), getDensityAction = (0, import_pieces_framework19.createAction)({
  name: "get_density",
  displayName: "Get Density",
  description: "Calculate the density of a CAD file",
  audience: "both",
  aiMetadata: { description: "Compute the density of a 3D solid from an uploaded CAD file (FBX, GLB, glTF, OBJ, PLY, STEP, or STL). Pick this for the mass-per-unit-volume mass property specifically; sibling actions cover mass, volume, surface area, and center of mass. Read-only analysis that does not modify the file and returns the same value for the same input.", idempotent: !0 },
  auth: zooAuth,
  // category: 'File Operations',
  props: {
    file: import_pieces_framework19.Property.File({
      displayName: "CAD File",
      required: !0,
      description: "The CAD file to analyze"
    })
  },
  async run({ auth, propsValue }) {
    let formData = new FormData();
    return formData.append("file", new Blob([propsValue.file.data]), propsValue.file.filename), (await import_pieces_common18.httpClient.sendRequest({
      method: import_pieces_common18.HttpMethod.POST,
      url: "https://api.zoo.dev/file/density",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      body: formData
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/file/get-mass.action.ts
var import_pieces_framework20 = require("@activepieces/pieces-framework");
var import_pieces_common19 = require("@activepieces/pieces-common"), getMassAction = (0, import_pieces_framework20.createAction)({
  name: "get_mass",
  displayName: "Get Mass",
  description: "Calculate the mass of a CAD file",
  audience: "both",
  aiMetadata: { description: "Compute the mass of a 3D solid from an uploaded CAD file (FBX, GLB, glTF, OBJ, PLY, STEP, or STL). Pick this for total mass specifically; sibling actions cover density, volume, surface area, and center of mass. Read-only analysis that does not modify the file and returns the same value for the same input.", idempotent: !0 },
  auth: zooAuth,
  // category: 'File Operations',
  props: {
    file: import_pieces_framework20.Property.File({
      displayName: "CAD File",
      required: !0,
      description: "The CAD file to analyze"
    })
  },
  async run({ auth, propsValue }) {
    let formData = new FormData();
    return formData.append("file", new Blob([propsValue.file.data]), propsValue.file.filename), (await import_pieces_common19.httpClient.sendRequest({
      method: import_pieces_common19.HttpMethod.POST,
      url: "https://api.zoo.dev/file/mass",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      body: formData
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/file/get-surface-area.action.ts
var import_pieces_framework21 = require("@activepieces/pieces-framework");
var import_pieces_common20 = require("@activepieces/pieces-common"), getSurfaceAreaAction = (0, import_pieces_framework21.createAction)({
  name: "get_surface_area",
  displayName: "Get Surface Area",
  description: "Calculate the surface area of a CAD file",
  audience: "both",
  aiMetadata: { description: "Compute the total surface area of a 3D solid from an uploaded CAD file (FBX, GLB, glTF, OBJ, PLY, STEP, or STL). Pick this for surface area specifically; sibling actions cover mass, density, volume, and center of mass. Read-only analysis that does not modify the file and returns the same value for the same input.", idempotent: !0 },
  auth: zooAuth,
  // category: 'File Operations',
  props: {
    file: import_pieces_framework21.Property.File({
      displayName: "CAD File",
      required: !0,
      description: "The CAD file to analyze"
    })
  },
  async run({ auth, propsValue }) {
    let formData = new FormData();
    return formData.append("file", new Blob([propsValue.file.data]), propsValue.file.filename), (await import_pieces_common20.httpClient.sendRequest({
      method: import_pieces_common20.HttpMethod.POST,
      url: "https://api.zoo.dev/file/surface-area",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      body: formData
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/file/get-volume.action.ts
var import_pieces_framework22 = require("@activepieces/pieces-framework");
var import_pieces_common21 = require("@activepieces/pieces-common"), getVolumeAction = (0, import_pieces_framework22.createAction)({
  name: "get_volume",
  displayName: "Get Volume",
  description: "Calculate the volume of a CAD file",
  audience: "both",
  aiMetadata: { description: "Compute the enclosed volume of a 3D solid from an uploaded CAD file (FBX, GLB, glTF, OBJ, PLY, STEP, or STL). Pick this for volume specifically; sibling actions cover mass, density, surface area, and center of mass. Read-only analysis that does not modify the file and returns the same value for the same input.", idempotent: !0 },
  auth: zooAuth,
  // category: 'File Operations',
  props: {
    file: import_pieces_framework22.Property.File({
      displayName: "CAD File",
      required: !0,
      description: "The CAD file to analyze"
    })
  },
  async run({ auth, propsValue }) {
    let formData = new FormData();
    return formData.append("file", new Blob([propsValue.file.data]), propsValue.file.filename), (await import_pieces_common21.httpClient.sendRequest({
      method: import_pieces_common21.HttpMethod.POST,
      url: "https://api.zoo.dev/file/volume",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      body: formData
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/file/index.ts
var FILE_ACTIONS = [
  getCenterOfMassAction,
  convertCadFileAction,
  getDensityAction,
  getMassAction,
  getSurfaceAreaAction,
  getVolumeAction
];

// ../ap/packages/pieces/community/zoo/src/lib/actions/meta/get-openapi-schema.action.ts
var import_pieces_framework23 = require("@activepieces/pieces-framework");
var import_pieces_common22 = require("@activepieces/pieces-common"), getOpenApiSchemaAction = (0, import_pieces_framework23.createAction)({
  name: "get_openapi_schema",
  displayName: "Get OpenAPI Schema",
  description: "Retrieve the OpenAPI schema for the Zoo API",
  audience: "both",
  aiMetadata: { description: "Fetch the full OpenAPI schema describing the Zoo API. Read-only and repeatable; takes no input. Use to discover available endpoints, parameters, and response shapes.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Meta',
  props: {},
  async run({ auth }) {
    return (await import_pieces_common22.httpClient.sendRequest({
      method: import_pieces_common22.HttpMethod.GET,
      url: "https://api.zoo.dev/",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/meta/return-pong.action.ts
var import_pieces_framework24 = require("@activepieces/pieces-framework");
var import_pieces_common23 = require("@activepieces/pieces-common"), returnPongAction = (0, import_pieces_framework24.createAction)({
  name: "return_pong",
  displayName: "Return Pong",
  description: 'Health check endpoint that returns "pong"',
  audience: "both",
  aiMetadata: { description: "Ping the Zoo API to verify connectivity and that credentials are valid. Read-only and repeatable; takes no input. Use as a lightweight health or auth check before running other actions.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Meta',
  props: {},
  async run({ auth }) {
    return (await import_pieces_common23.httpClient.sendRequest({
      method: import_pieces_common23.HttpMethod.GET,
      url: "https://api.zoo.dev/ping",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/meta/index.ts
var META_ACTIONS = [
  getOpenApiSchemaAction,
  returnPongAction
];

// ../ap/packages/pieces/community/zoo/src/lib/actions/modeling/send-modeling-command.action.ts
var import_pieces_framework25 = require("@activepieces/pieces-framework");
var import_pieces_common24 = require("@activepieces/pieces-common"), sendModelingCommandAction = (0, import_pieces_framework25.createAction)({
  name: "send_modeling_command",
  displayName: "Send Modeling Command",
  description: "Send a command to the modeling WebSocket endpoint",
  audience: "both",
  aiMetadata: { description: "Resolve the Zoo modeling WebSocket endpoint and pair it with a modeling command for a client to dispatch over that connection. Use when you need the live WebSocket URL to drive geometry/CAD modeling commands; this only fetches the endpoint URL and echoes the command back, so it does not itself execute the command and is safe to call repeatedly.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Modeling',
  props: {
    command: import_pieces_framework25.Property.Object({
      displayName: "Command",
      required: !0,
      description: "The modeling command to send"
    })
  },
  async run({ auth, propsValue }) {
    return {
      websocketUrl: (await import_pieces_common24.httpClient.sendRequest({
        method: import_pieces_common24.HttpMethod.GET,
        url: "https://api.zoo.dev/ws/modeling/commands",
        headers: {
          Authorization: `Bearer ${auth.secret_text}`
        }
      })).body.url,
      command: propsValue.command
    };
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/modeling/index.ts
var MODELING_ACTIONS = [
  sendModelingCommandAction
];

// ../ap/packages/pieces/community/zoo/src/lib/actions/orgs/get-org.action.ts
var import_pieces_framework26 = require("@activepieces/pieces-framework");
var import_pieces_common25 = require("@activepieces/pieces-common"), getOrgAction = (0, import_pieces_framework26.createAction)({
  name: "get_org",
  displayName: "Get Organization",
  description: "Retrieve details of your organization",
  audience: "both",
  aiMetadata: { description: "Retrieve details of the organization tied to the authenticated account (name, settings, billing context). Takes no input and reads the single org for the current credentials. Read-only with no side effects.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Organizations',
  props: {},
  async run({ auth }) {
    return (await import_pieces_common25.httpClient.sendRequest({
      method: import_pieces_common25.HttpMethod.GET,
      url: "https://api.zoo.dev/org",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/orgs/update-org.action.ts
var import_pieces_framework27 = require("@activepieces/pieces-framework");
var import_pieces_common26 = require("@activepieces/pieces-common"), updateOrgAction = (0, import_pieces_framework27.createAction)({
  name: "update_org",
  displayName: "Update Organization",
  description: "Update your organization details",
  audience: "both",
  aiMetadata: { description: "Update the authenticated user's existing organization, setting its name to the provided value. Use to rename or edit an org that already exists, not to provision one (use create-org for that). Idempotent: applying the same name repeatedly leaves the org in the same state.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Organizations',
  props: {
    name: import_pieces_framework27.Property.ShortText({
      displayName: "Organization Name",
      required: !0,
      description: "The new name for your organization"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common26.httpClient.sendRequest({
      method: import_pieces_common26.HttpMethod.PUT,
      url: "https://api.zoo.dev/org",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      body: {
        name: propsValue.name
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/orgs/create-org.action.ts
var import_pieces_framework28 = require("@activepieces/pieces-framework");
var import_pieces_common27 = require("@activepieces/pieces-common"), createOrgAction = (0, import_pieces_framework28.createAction)({
  name: "create_org",
  displayName: "Create Organization",
  description: "Create a new organization",
  audience: "both",
  aiMetadata: { description: "Create a brand-new organization with the given name. Use only to provision a new org; to rename an existing one use update-org. Not idempotent: each call creates another organization, so do not retry blindly.", idempotent: !1 },
  auth: zooAuth,
  // category: 'Organizations',
  props: {
    name: import_pieces_framework28.Property.ShortText({
      displayName: "Organization Name",
      required: !0,
      description: "The name for the new organization"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common27.httpClient.sendRequest({
      method: import_pieces_common27.HttpMethod.POST,
      url: "https://api.zoo.dev/org",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      body: {
        name: propsValue.name
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/orgs/list-org-members.action.ts
var import_pieces_framework29 = require("@activepieces/pieces-framework");
var import_pieces_common28 = require("@activepieces/pieces-common");
var listOrgMembersAction = (0, import_pieces_framework29.createAction)({
  name: "list_org_members",
  displayName: "List Organization Members",
  description: "List all members of your organization",
  audience: "both",
  aiMetadata: { description: "List members of the authenticated user's organization, with optional limit/offset paging. Use this to discover members or look up a user ID when you only know a name or email; for a single known user ID, prefer the get-org-member action. Read-only and has no side effects.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Organizations',
  props: {
    limit: import_pieces_framework29.Property.Number({
      displayName: "Limit",
      required: !1,
      description: "Maximum number of members to return"
    }),
    offset: import_pieces_framework29.Property.Number({
      displayName: "Offset",
      required: !1,
      description: "Number of members to skip"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common28.httpClient.sendRequest({
      method: import_pieces_common28.HttpMethod.GET,
      url: "https://api.zoo.dev/org/members",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      queryParams: __spreadValues(__spreadValues({}, propsValue.limit && { limit: propsValue.limit.toString() }), propsValue.offset && { offset: propsValue.offset.toString() })
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/orgs/add-org-member.action.ts
var import_pieces_framework30 = require("@activepieces/pieces-framework");
var import_pieces_common29 = require("@activepieces/pieces-common"), addOrgMemberAction = (0, import_pieces_framework30.createAction)({
  name: "add_org_member",
  displayName: "Add Organization Member",
  description: "Add a new member to your organization",
  audience: "both",
  aiMetadata: { description: "Add a user to the organization by email with a role of admin or member. Use to invite or grant access to a new member. Not idempotent: repeated calls for the same email may error or create duplicate invitations, so check existing membership first.", idempotent: !1 },
  auth: zooAuth,
  // category: 'Organizations',
  props: {
    email: import_pieces_framework30.Property.ShortText({
      displayName: "Email",
      required: !0,
      description: "Email address of the user to add"
    }),
    role: import_pieces_framework30.Property.StaticDropdown({
      displayName: "Role",
      required: !0,
      options: {
        options: [
          { label: "Admin", value: "admin" },
          { label: "Member", value: "member" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common29.httpClient.sendRequest({
      method: import_pieces_common29.HttpMethod.POST,
      url: "https://api.zoo.dev/org/members",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      body: {
        email: propsValue.email,
        role: propsValue.role
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/orgs/get-org-member.action.ts
var import_pieces_framework31 = require("@activepieces/pieces-framework");
var import_pieces_common30 = require("@activepieces/pieces-common"), getOrgMemberAction = (0, import_pieces_framework31.createAction)({
  name: "get_org_member",
  displayName: "Get Organization Member",
  description: "Get details of a specific organization member",
  audience: "both",
  aiMetadata: { description: "Fetch a single organization member by their user ID, including role and profile details. Use when you already have the user ID; to browse or discover members without an ID, use the list-org-members action instead. Read-only lookup with no side effects.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Organizations',
  props: {
    userId: import_pieces_framework31.Property.ShortText({
      displayName: "User ID",
      required: !0,
      description: "ID of the member to retrieve"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common30.httpClient.sendRequest({
      method: import_pieces_common30.HttpMethod.GET,
      url: `https://api.zoo.dev/org/members/${propsValue.userId}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/orgs/index.ts
var ORGS_ACTIONS = [
  getOrgAction,
  updateOrgAction,
  createOrgAction,
  listOrgMembersAction,
  addOrgMemberAction,
  getOrgMemberAction
];

// ../ap/packages/pieces/community/zoo/src/lib/actions/payments/get-org-payment.action.ts
var import_pieces_framework32 = require("@activepieces/pieces-framework");
var import_pieces_common31 = require("@activepieces/pieces-common"), getOrgPaymentAction = (0, import_pieces_framework32.createAction)({
  name: "get_org_payment",
  displayName: "Get Organization Payment Info",
  description: "Retrieve payment information for your organization",
  audience: "both",
  aiMetadata: { description: "Read the organization's current payment information. Use to inspect the active billing details for the org before updating or deleting them. Read-only and idempotent; takes no inputs.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Payments',
  props: {},
  async run({ auth }) {
    return (await import_pieces_common31.httpClient.sendRequest({
      method: import_pieces_common31.HttpMethod.GET,
      url: "https://api.zoo.dev/org/payment",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/payments/update-org-payment.action.ts
var import_pieces_framework33 = require("@activepieces/pieces-framework");
var import_pieces_common32 = require("@activepieces/pieces-common"), updateOrgPaymentAction = (0, import_pieces_framework33.createAction)({
  name: "update_org_payment",
  displayName: "Update Organization Payment Info",
  description: "Update payment information for your organization",
  audience: "both",
  aiMetadata: { description: "Set the organization's active payment method to the supplied payment method ID. Use to change which card or method bills the org; the per-user equivalent is the user payment action. Not idempotent: it overwrites the stored payment method on each call.", idempotent: !1 },
  auth: zooAuth,
  // category: 'Payments',
  props: {
    paymentMethodId: import_pieces_framework33.Property.ShortText({
      displayName: "Payment Method ID",
      required: !0,
      description: "ID of the payment method to use"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common32.httpClient.sendRequest({
      method: import_pieces_common32.HttpMethod.PUT,
      url: "https://api.zoo.dev/org/payment",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      body: {
        payment_method_id: propsValue.paymentMethodId
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/payments/create-org-payment.action.ts
var import_pieces_framework34 = require("@activepieces/pieces-framework");
var import_pieces_common33 = require("@activepieces/pieces-common"), createOrgPaymentAction = (0, import_pieces_framework34.createAction)({
  name: "create_org_payment",
  displayName: "Create Organization Payment Info",
  description: "Create payment information for your organization",
  audience: "both",
  aiMetadata: { description: "Attach a payment method to the organization billing profile, given an existing payment method ID. Use for the org (the user has separate payment actions); this writes new billing data, so calling again may create duplicate or conflicting records.", idempotent: !1 },
  auth: zooAuth,
  // category: 'Payments',
  props: {
    paymentMethodId: import_pieces_framework34.Property.ShortText({
      displayName: "Payment Method ID",
      required: !0,
      description: "ID of the payment method to use"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common33.httpClient.sendRequest({
      method: import_pieces_common33.HttpMethod.POST,
      url: "https://api.zoo.dev/org/payment",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      body: {
        payment_method_id: propsValue.paymentMethodId
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/payments/delete-org-payment.action.ts
var import_pieces_framework35 = require("@activepieces/pieces-framework");
var import_pieces_common34 = require("@activepieces/pieces-common"), deleteOrgPaymentAction = (0, import_pieces_framework35.createAction)({
  name: "delete_org_payment",
  displayName: "Delete Organization Payment Info",
  description: "Delete payment information for your organization",
  audience: "both",
  aiMetadata: { description: "Remove the organization's stored payment information. Use to clear the org's billing method; destructive and takes no inputs. Not strictly idempotent: a first call deletes the record and a repeat may error if none remains.", idempotent: !1 },
  auth: zooAuth,
  // category: 'Payments',
  props: {},
  async run({ auth }) {
    return (await import_pieces_common34.httpClient.sendRequest({
      method: import_pieces_common34.HttpMethod.DELETE,
      url: "https://api.zoo.dev/org/payment",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/payments/get-org-balance.action.ts
var import_pieces_framework36 = require("@activepieces/pieces-framework");
var import_pieces_common35 = require("@activepieces/pieces-common"), getOrgBalanceAction = (0, import_pieces_framework36.createAction)({
  name: "get_org_balance",
  displayName: "Get Organization Balance",
  description: "Retrieve the current balance for your organization",
  audience: "both",
  aiMetadata: { description: "Read the current account balance for the organization. Use for org-level billing; the per-user equivalent is the get user balance action. Read-only and idempotent; takes no inputs.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Payments',
  props: {},
  async run({ auth }) {
    return (await import_pieces_common35.httpClient.sendRequest({
      method: import_pieces_common35.HttpMethod.GET,
      url: "https://api.zoo.dev/org/payment/balance",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/payments/list-org-invoices.action.ts
var import_pieces_framework37 = require("@activepieces/pieces-framework");
var import_pieces_common36 = require("@activepieces/pieces-common");
var listOrgInvoicesAction = (0, import_pieces_framework37.createAction)({
  name: "list_org_invoices",
  displayName: "List Organization Invoices",
  description: "List all invoices for your organization",
  audience: "both",
  aiMetadata: { description: "List billing invoices for the organization, with optional limit and offset paging. Use for org-level invoices; the per-user equivalent is the list user invoices action. Read-only and idempotent.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Payments',
  props: {
    limit: import_pieces_framework37.Property.Number({
      displayName: "Limit",
      required: !1,
      description: "Maximum number of invoices to return"
    }),
    offset: import_pieces_framework37.Property.Number({
      displayName: "Offset",
      required: !1,
      description: "Number of invoices to skip"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common36.httpClient.sendRequest({
      method: import_pieces_common36.HttpMethod.GET,
      url: "https://api.zoo.dev/org/payment/invoices",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      queryParams: __spreadValues(__spreadValues({}, propsValue.limit && { limit: propsValue.limit.toString() }), propsValue.offset && { offset: propsValue.offset.toString() })
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/payments/list-org-payment-methods.action.ts
var import_pieces_framework38 = require("@activepieces/pieces-framework");
var import_pieces_common37 = require("@activepieces/pieces-common");
var listOrgPaymentMethodsAction = (0, import_pieces_framework38.createAction)({
  name: "list_org_payment_methods",
  displayName: "List Organization Payment Methods",
  description: "List all payment methods for your organization",
  audience: "both",
  aiMetadata: { description: "List the payment methods registered on the organization, with optional limit and offset paging. Use to enumerate available methods or find a payment method ID to pass to the org payment update action. Read-only and idempotent.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Payments',
  props: {
    limit: import_pieces_framework38.Property.Number({
      displayName: "Limit",
      required: !1,
      description: "Maximum number of payment methods to return"
    }),
    offset: import_pieces_framework38.Property.Number({
      displayName: "Offset",
      required: !1,
      description: "Number of payment methods to skip"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common37.httpClient.sendRequest({
      method: import_pieces_common37.HttpMethod.GET,
      url: "https://api.zoo.dev/org/payment/methods",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      queryParams: __spreadValues(__spreadValues({}, propsValue.limit && { limit: propsValue.limit.toString() }), propsValue.offset && { offset: propsValue.offset.toString() })
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/payments/get-org-subscription.action.ts
var import_pieces_framework39 = require("@activepieces/pieces-framework");
var import_pieces_common38 = require("@activepieces/pieces-common"), getOrgSubscriptionAction = (0, import_pieces_framework39.createAction)({
  name: "get_org_subscription",
  displayName: "Get Organization Subscription",
  description: "Retrieve the current subscription for your organization",
  audience: "both",
  aiMetadata: { description: "Read the current subscription for the organization tied to the authenticated account (plan, status, billing period). Use this for the org-level subscription; for the individual user use Get User Subscription instead. Read-only and safe to repeat.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Payments',
  props: {},
  async run({ auth }) {
    return (await import_pieces_common38.httpClient.sendRequest({
      method: import_pieces_common38.HttpMethod.GET,
      url: "https://api.zoo.dev/org/payment/subscriptions",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/payments/update-org-subscription.action.ts
var import_pieces_framework40 = require("@activepieces/pieces-framework");
var import_pieces_common39 = require("@activepieces/pieces-common"), updateOrgSubscriptionAction = (0, import_pieces_framework40.createAction)({
  name: "update_org_subscription",
  displayName: "Update Organization Subscription",
  description: "Update the subscription for your organization",
  audience: "both",
  aiMetadata: { description: "Change the organization's existing subscription to the given plan ID. Use to switch the org's plan once a subscription exists; to start one from scratch use the create org subscription action. Not idempotent: it writes the plan and may trigger billing changes.", idempotent: !1 },
  auth: zooAuth,
  // category: 'Payments',
  props: {
    planId: import_pieces_framework40.Property.ShortText({
      displayName: "Plan ID",
      required: !0,
      description: "ID of the subscription plan"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common39.httpClient.sendRequest({
      method: import_pieces_common39.HttpMethod.PUT,
      url: "https://api.zoo.dev/org/payment/subscriptions",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      body: {
        plan_id: propsValue.planId
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/payments/create-org-subscription.action.ts
var import_pieces_framework41 = require("@activepieces/pieces-framework");
var import_pieces_common40 = require("@activepieces/pieces-common"), createOrgSubscriptionAction = (0, import_pieces_framework41.createAction)({
  name: "create_org_subscription",
  displayName: "Create Organization Subscription",
  description: "Create a new subscription for your organization",
  audience: "both",
  aiMetadata: { description: "Start a new billing subscription for the organization using the given plan ID. Use when the org has no subscription yet; to change an existing one use the org subscription update action instead. Not idempotent: calling it creates a subscription and may initiate billing.", idempotent: !1 },
  auth: zooAuth,
  // category: 'Payments',
  props: {
    planId: import_pieces_framework41.Property.ShortText({
      displayName: "Plan ID",
      required: !0,
      description: "ID of the subscription plan"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common40.httpClient.sendRequest({
      method: import_pieces_common40.HttpMethod.POST,
      url: "https://api.zoo.dev/org/payment/subscriptions",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      body: {
        plan_id: propsValue.planId
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/payments/get-user-payment.action.ts
var import_pieces_framework42 = require("@activepieces/pieces-framework");
var import_pieces_common41 = require("@activepieces/pieces-common"), getUserPaymentAction = (0, import_pieces_framework42.createAction)({
  name: "get_user_payment",
  displayName: "Get User Payment Info",
  description: "Retrieve payment information for your user account",
  audience: "both",
  aiMetadata: { description: "Read the stored billing/payment information for the authenticated user account. Use this for the single current record; to enumerate all saved cards use List User Payment Methods. Read-only and safe to repeat.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Payments',
  props: {},
  async run({ auth }) {
    return (await import_pieces_common41.httpClient.sendRequest({
      method: import_pieces_common41.HttpMethod.GET,
      url: "https://api.zoo.dev/user/payment",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/payments/update-user-payment.action.ts
var import_pieces_framework43 = require("@activepieces/pieces-framework");
var import_pieces_common42 = require("@activepieces/pieces-common"), updateUserPaymentAction = (0, import_pieces_framework43.createAction)({
  name: "update_user_payment",
  displayName: "Update User Payment Info",
  description: "Update payment information for your user account",
  audience: "both",
  aiMetadata: { description: "Set or replace the payment method on the authenticated user account, given an existing payment method ID. Use for the individual user (the org has its own payment actions); this mutates the stored billing record, so confirm the ID before calling.", idempotent: !1 },
  auth: zooAuth,
  // category: 'Payments',
  props: {
    paymentMethodId: import_pieces_framework43.Property.ShortText({
      displayName: "Payment Method ID",
      required: !0,
      description: "ID of the payment method to use"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common42.httpClient.sendRequest({
      method: import_pieces_common42.HttpMethod.PUT,
      url: "https://api.zoo.dev/user/payment",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      body: {
        payment_method_id: propsValue.paymentMethodId
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/payments/create-user-payment.action.ts
var import_pieces_framework44 = require("@activepieces/pieces-framework");
var import_pieces_common43 = require("@activepieces/pieces-common"), createUserPaymentAction = (0, import_pieces_framework44.createAction)({
  name: "create_user_payment",
  displayName: "Create User Payment Info",
  description: "Create payment information for your user account",
  audience: "both",
  aiMetadata: { description: "Attach a payment method to the authenticated user's own Zoo account using the given payment method ID. Use for the individual user; the organization equivalent is the org payment update action. Not idempotent: each call registers payment information.", idempotent: !1 },
  auth: zooAuth,
  // category: 'Payments',
  props: {
    paymentMethodId: import_pieces_framework44.Property.ShortText({
      displayName: "Payment Method ID",
      required: !0,
      description: "ID of the payment method to use"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common43.httpClient.sendRequest({
      method: import_pieces_common43.HttpMethod.POST,
      url: "https://api.zoo.dev/user/payment",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      body: {
        payment_method_id: propsValue.paymentMethodId
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/payments/delete-user-payment.action.ts
var import_pieces_framework45 = require("@activepieces/pieces-framework");
var import_pieces_common44 = require("@activepieces/pieces-common"), deleteUserPaymentAction = (0, import_pieces_framework45.createAction)({
  name: "delete_user_payment",
  displayName: "Delete User Payment Info",
  description: "Delete payment information for your user account",
  audience: "both",
  aiMetadata: { description: "Remove the stored payment information from the authenticated user account. Destructive and not reversible; confirm intent before calling. Applies to the individual user, not the organization.", idempotent: !1 },
  auth: zooAuth,
  // category: 'Payments',
  props: {},
  async run({ auth }) {
    return (await import_pieces_common44.httpClient.sendRequest({
      method: import_pieces_common44.HttpMethod.DELETE,
      url: "https://api.zoo.dev/user/payment",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/payments/get-user-balance.action.ts
var import_pieces_framework46 = require("@activepieces/pieces-framework");
var import_pieces_common45 = require("@activepieces/pieces-common"), getUserBalanceAction = (0, import_pieces_framework46.createAction)({
  name: "get_user_balance",
  displayName: "Get User Balance",
  description: "Retrieve the current balance for your user account",
  audience: "both",
  aiMetadata: { description: "Read the current account balance for the authenticated user. Use for the individual user; the org-level equivalent is the get organization balance action. Read-only and idempotent; takes no inputs.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Payments',
  props: {},
  async run({ auth }) {
    return (await import_pieces_common45.httpClient.sendRequest({
      method: import_pieces_common45.HttpMethod.GET,
      url: "https://api.zoo.dev/user/payment/balance",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/payments/list-user-invoices.action.ts
var import_pieces_framework47 = require("@activepieces/pieces-framework");
var import_pieces_common46 = require("@activepieces/pieces-common");
var listUserInvoicesAction = (0, import_pieces_framework47.createAction)({
  name: "list_user_invoices",
  displayName: "List User Invoices",
  description: "List all invoices for your user account",
  audience: "both",
  aiMetadata: { description: "List billing invoices for the authenticated user's own account, with optional limit and offset paging. Use for individual-user invoices; the org-level equivalent is the list organization invoices action. Read-only and idempotent.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Payments',
  props: {
    limit: import_pieces_framework47.Property.Number({
      displayName: "Limit",
      required: !1,
      description: "Maximum number of invoices to return"
    }),
    offset: import_pieces_framework47.Property.Number({
      displayName: "Offset",
      required: !1,
      description: "Number of invoices to skip"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common46.httpClient.sendRequest({
      method: import_pieces_common46.HttpMethod.GET,
      url: "https://api.zoo.dev/user/payment/invoices",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      queryParams: __spreadValues(__spreadValues({}, propsValue.limit && { limit: propsValue.limit.toString() }), propsValue.offset && { offset: propsValue.offset.toString() })
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/payments/list-user-payment-methods.action.ts
var import_pieces_framework48 = require("@activepieces/pieces-framework");
var import_pieces_common47 = require("@activepieces/pieces-common");
var listUserPaymentMethodsAction = (0, import_pieces_framework48.createAction)({
  name: "list_user_payment_methods",
  displayName: "List User Payment Methods",
  description: "List all payment methods for your user account",
  audience: "both",
  aiMetadata: { description: "List the saved payment methods on the authenticated user account, with optional limit/offset paging. Use this to enumerate or look up a payment method ID; for a single stored billing record use Get User Payment Info. Read-only and safe to repeat.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Payments',
  props: {
    limit: import_pieces_framework48.Property.Number({
      displayName: "Limit",
      required: !1,
      description: "Maximum number of payment methods to return"
    }),
    offset: import_pieces_framework48.Property.Number({
      displayName: "Offset",
      required: !1,
      description: "Number of payment methods to skip"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common47.httpClient.sendRequest({
      method: import_pieces_common47.HttpMethod.GET,
      url: "https://api.zoo.dev/user/payment/methods",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      queryParams: __spreadValues(__spreadValues({}, propsValue.limit && { limit: propsValue.limit.toString() }), propsValue.offset && { offset: propsValue.offset.toString() })
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/payments/get-user-subscription.action.ts
var import_pieces_framework49 = require("@activepieces/pieces-framework");
var import_pieces_common48 = require("@activepieces/pieces-common"), getUserSubscriptionAction = (0, import_pieces_framework49.createAction)({
  name: "get_user_subscription",
  displayName: "Get User Subscription",
  description: "Retrieve the current subscription for your user account",
  audience: "both",
  aiMetadata: { description: "Read the current subscription tied to the authenticated user account (plan, status, billing period). Use this for the individual user; for the organization-level subscription use Get Organization Subscription instead. Read-only and safe to repeat.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Payments',
  props: {},
  async run({ auth }) {
    return (await import_pieces_common48.httpClient.sendRequest({
      method: import_pieces_common48.HttpMethod.GET,
      url: "https://api.zoo.dev/user/payment/subscriptions",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/payments/update-user-subscription.action.ts
var import_pieces_framework50 = require("@activepieces/pieces-framework");
var import_pieces_common49 = require("@activepieces/pieces-common"), updateUserSubscriptionAction = (0, import_pieces_framework50.createAction)({
  name: "update_user_subscription",
  displayName: "Update User Subscription",
  description: "Update the subscription for your user account",
  audience: "both",
  aiMetadata: { description: "Change the subscription plan on the authenticated user's own Zoo account by setting it to the given plan ID. Use for the individual user, not the organization (use the org subscription action for that). Not idempotent: each call writes the plan and may trigger billing changes.", idempotent: !1 },
  auth: zooAuth,
  // category: 'Payments',
  props: {
    planId: import_pieces_framework50.Property.ShortText({
      displayName: "Plan ID",
      required: !0,
      description: "ID of the subscription plan"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common49.httpClient.sendRequest({
      method: import_pieces_common49.HttpMethod.PUT,
      url: "https://api.zoo.dev/user/payment/subscriptions",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      body: {
        plan_id: propsValue.planId
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/payments/create-user-subscription.action.ts
var import_pieces_framework51 = require("@activepieces/pieces-framework");
var import_pieces_common50 = require("@activepieces/pieces-common"), createUserSubscriptionAction = (0, import_pieces_framework51.createAction)({
  name: "create_user_subscription",
  displayName: "Create User Subscription",
  description: "Create a new subscription for your user account",
  audience: "both",
  aiMetadata: { description: "Start a new subscription on the authenticated user account for a given plan ID. Use for the individual user; this creates a billable subscription, so calling again may create duplicate subscriptions. Check Get User Subscription first to avoid re-subscribing.", idempotent: !1 },
  auth: zooAuth,
  // category: 'Payments',
  props: {
    planId: import_pieces_framework51.Property.ShortText({
      displayName: "Plan ID",
      required: !0,
      description: "ID of the subscription plan"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common50.httpClient.sendRequest({
      method: import_pieces_common50.HttpMethod.POST,
      url: "https://api.zoo.dev/user/payment/subscriptions",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      body: {
        plan_id: propsValue.planId
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/payments/index.ts
var PAYMENTS_ACTIONS = [
  getOrgPaymentAction,
  updateOrgPaymentAction,
  createOrgPaymentAction,
  deleteOrgPaymentAction,
  getOrgBalanceAction,
  listOrgInvoicesAction,
  listOrgPaymentMethodsAction,
  getOrgSubscriptionAction,
  updateOrgSubscriptionAction,
  createOrgSubscriptionAction,
  getUserPaymentAction,
  updateUserPaymentAction,
  createUserPaymentAction,
  deleteUserPaymentAction,
  getUserBalanceAction,
  listUserInvoicesAction,
  listUserPaymentMethodsAction,
  getUserSubscriptionAction,
  updateUserSubscriptionAction,
  createUserSubscriptionAction
];

// ../ap/packages/pieces/community/zoo/src/lib/actions/service-accounts/list-service-accounts.action.ts
var import_pieces_framework52 = require("@activepieces/pieces-framework");
var import_pieces_common51 = require("@activepieces/pieces-common");
var listServiceAccountsAction = (0, import_pieces_framework52.createAction)({
  name: "list_service_accounts",
  displayName: "List Service Accounts",
  description: "List all service accounts for your organization",
  audience: "both",
  aiMetadata: { description: "List the service accounts belonging to the organization, with optional limit and offset paging. Use to enumerate service accounts or find a token to pass to the get or delete service account actions. Read-only and idempotent.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Service Accounts',
  props: {
    limit: import_pieces_framework52.Property.Number({
      displayName: "Limit",
      required: !1,
      description: "Maximum number of service accounts to return"
    }),
    offset: import_pieces_framework52.Property.Number({
      displayName: "Offset",
      required: !1,
      description: "Number of service accounts to skip"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common51.httpClient.sendRequest({
      method: import_pieces_common51.HttpMethod.GET,
      url: "https://api.zoo.dev/org/service-accounts",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      queryParams: __spreadValues(__spreadValues({}, propsValue.limit && { limit: propsValue.limit.toString() }), propsValue.offset && { offset: propsValue.offset.toString() })
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/service-accounts/create-service-account.action.ts
var import_pieces_framework53 = require("@activepieces/pieces-framework");
var import_pieces_common52 = require("@activepieces/pieces-common"), createServiceAccountAction = (0, import_pieces_framework53.createAction)({
  name: "create_service_account",
  displayName: "Create Service Account",
  description: "Create a new service account for your organization",
  audience: "both",
  aiMetadata: { description: "Create a new organization service account with the given name and return its credentials. Use to provision programmatic access for the org. Not idempotent: each call creates a distinct service account even if the name repeats.", idempotent: !1 },
  auth: zooAuth,
  // category: 'Service Accounts',
  props: {
    name: import_pieces_framework53.Property.ShortText({
      displayName: "Name",
      required: !0,
      description: "Name for the service account"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common52.httpClient.sendRequest({
      method: import_pieces_common52.HttpMethod.POST,
      url: "https://api.zoo.dev/org/service-accounts",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      body: {
        name: propsValue.name
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/service-accounts/get-service-account.action.ts
var import_pieces_framework54 = require("@activepieces/pieces-framework");
var import_pieces_common53 = require("@activepieces/pieces-common"), getServiceAccountAction = (0, import_pieces_framework54.createAction)({
  name: "get_service_account",
  displayName: "Get Service Account",
  description: "Retrieve details of a specific service account",
  audience: "both",
  aiMetadata: { description: "Look up one organization service account by its token and return its details. Use when you already have the token; to discover tokens first use the list service accounts action. Read-only and idempotent.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Service Accounts',
  props: {
    token: import_pieces_framework54.Property.ShortText({
      displayName: "Token",
      required: !0,
      description: "Token of the service account to retrieve"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common53.httpClient.sendRequest({
      method: import_pieces_common53.HttpMethod.GET,
      url: `https://api.zoo.dev/org/service-accounts/${propsValue.token}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/service-accounts/delete-service-account.action.ts
var import_pieces_framework55 = require("@activepieces/pieces-framework");
var import_pieces_common54 = require("@activepieces/pieces-common"), deleteServiceAccountAction = (0, import_pieces_framework55.createAction)({
  name: "delete_service_account",
  displayName: "Delete Service Account",
  description: "Delete a service account from your organization",
  audience: "both",
  aiMetadata: { description: "Permanently remove the organization service account identified by its token. Use to revoke a service account; destructive. Not strictly idempotent: a first call deletes it and a repeat may error if the token no longer exists.", idempotent: !1 },
  auth: zooAuth,
  // category: 'Service Accounts',
  props: {
    token: import_pieces_framework55.Property.ShortText({
      displayName: "Token",
      required: !0,
      description: "Token of the service account to delete"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common54.httpClient.sendRequest({
      method: import_pieces_common54.HttpMethod.DELETE,
      url: `https://api.zoo.dev/org/service-accounts/${propsValue.token}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/service-accounts/index.ts
var SERVICE_ACCOUNTS_ACTIONS = [
  listServiceAccountsAction,
  createServiceAccountAction,
  getServiceAccountAction,
  deleteServiceAccountAction
];

// ../ap/packages/pieces/community/zoo/src/lib/actions/shortlinks/list-org-shortlinks.action.ts
var import_pieces_framework56 = require("@activepieces/pieces-framework");
var import_pieces_common55 = require("@activepieces/pieces-common");
var listOrgShortlinksAction = (0, import_pieces_framework56.createAction)({
  name: "list_org_shortlinks",
  displayName: "List Organization Shortlinks",
  description: "List all shortlinks for your organization",
  audience: "both",
  aiMetadata: { description: "List the shortlinks belonging to the authenticated user's Zoo organization, with optional limit and offset for paging. Read-only and repeatable. Use the user shortlinks action to scope the list to just the current user.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Shortlinks',
  props: {
    limit: import_pieces_framework56.Property.Number({
      displayName: "Limit",
      required: !1,
      description: "Maximum number of shortlinks to return"
    }),
    offset: import_pieces_framework56.Property.Number({
      displayName: "Offset",
      required: !1,
      description: "Number of shortlinks to skip"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common55.httpClient.sendRequest({
      method: import_pieces_common55.HttpMethod.GET,
      url: "https://api.zoo.dev/org/shortlinks",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      queryParams: __spreadValues(__spreadValues({}, propsValue.limit && { limit: propsValue.limit.toString() }), propsValue.offset && { offset: propsValue.offset.toString() })
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/shortlinks/list-user-shortlinks.action.ts
var import_pieces_framework57 = require("@activepieces/pieces-framework");
var import_pieces_common56 = require("@activepieces/pieces-common");
var listUserShortlinksAction = (0, import_pieces_framework57.createAction)({
  name: "list_user_shortlinks",
  displayName: "List User Shortlinks",
  description: "List all shortlinks for your user account",
  audience: "both",
  aiMetadata: { description: "List the shortlinks owned by the authenticated Zoo user, with optional limit and offset for paging. Read-only and repeatable. Use the organization shortlinks action to list links across the whole org instead of just this user.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Shortlinks',
  props: {
    limit: import_pieces_framework57.Property.Number({
      displayName: "Limit",
      required: !1,
      description: "Maximum number of shortlinks to return"
    }),
    offset: import_pieces_framework57.Property.Number({
      displayName: "Offset",
      required: !1,
      description: "Number of shortlinks to skip"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common56.httpClient.sendRequest({
      method: import_pieces_common56.HttpMethod.GET,
      url: "https://api.zoo.dev/user/shortlinks",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      queryParams: __spreadValues(__spreadValues({}, propsValue.limit && { limit: propsValue.limit.toString() }), propsValue.offset && { offset: propsValue.offset.toString() })
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/shortlinks/create-shortlink.action.ts
var import_pieces_framework58 = require("@activepieces/pieces-framework");
var import_pieces_common57 = require("@activepieces/pieces-common");
var createShortlinkAction = (0, import_pieces_framework58.createAction)({
  name: "create_shortlink",
  displayName: "Create Shortlink",
  description: "Create a new shortlink for your user account",
  audience: "both",
  aiMetadata: { description: "Create a new Zoo shortlink for the authenticated user that redirects to a target URL, optionally with a custom key. Not idempotent: repeated calls create distinct shortlinks (or conflict on a reused custom key). Use the update shortlink action to change an existing link's destination.", idempotent: !1 },
  auth: zooAuth,
  // category: 'Shortlinks',
  props: {
    url: import_pieces_framework58.Property.ShortText({
      displayName: "URL",
      required: !0,
      description: "The URL to shorten"
    }),
    key: import_pieces_framework58.Property.ShortText({
      displayName: "Key",
      required: !1,
      description: "Custom key for the shortlink (optional)"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common57.httpClient.sendRequest({
      method: import_pieces_common57.HttpMethod.POST,
      url: "https://api.zoo.dev/user/shortlinks",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      body: __spreadValues({
        url: propsValue.url
      }, propsValue.key && { key: propsValue.key })
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/shortlinks/update-shortlink.action.ts
var import_pieces_framework59 = require("@activepieces/pieces-framework");
var import_pieces_common58 = require("@activepieces/pieces-common"), updateShortlinkAction = (0, import_pieces_framework59.createAction)({
  name: "update_shortlink",
  displayName: "Update Shortlink",
  description: "Update an existing shortlink",
  audience: "both",
  aiMetadata: { description: "Change the destination URL of an existing Zoo shortlink, identified by its key. Idempotent: re-applying the same key and URL leaves the shortlink unchanged. Use the create shortlink action to make a new one instead.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Shortlinks',
  props: {
    key: import_pieces_framework59.Property.ShortText({
      displayName: "Key",
      required: !0,
      description: "The key of the shortlink to update"
    }),
    url: import_pieces_framework59.Property.ShortText({
      displayName: "URL",
      required: !0,
      description: "The new URL for the shortlink"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common58.httpClient.sendRequest({
      method: import_pieces_common58.HttpMethod.PUT,
      url: `https://api.zoo.dev/user/shortlinks/${propsValue.key}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      body: {
        url: propsValue.url
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/shortlinks/delete-shortlink.action.ts
var import_pieces_framework60 = require("@activepieces/pieces-framework");
var import_pieces_common59 = require("@activepieces/pieces-common"), deleteShortlinkAction = (0, import_pieces_framework60.createAction)({
  name: "delete_shortlink",
  displayName: "Delete Shortlink",
  description: "Delete an existing shortlink",
  audience: "both",
  aiMetadata: { description: "Permanently delete a Zoo shortlink identified by its key. Destructive; the first call removes the link and later calls for the same key have no further effect. Requires the exact shortlink key.", idempotent: !1 },
  auth: zooAuth,
  // category: 'Shortlinks',
  props: {
    key: import_pieces_framework60.Property.ShortText({
      displayName: "Key",
      required: !0,
      description: "The key of the shortlink to delete"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common59.httpClient.sendRequest({
      method: import_pieces_common59.HttpMethod.DELETE,
      url: `https://api.zoo.dev/user/shortlinks/${propsValue.key}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/shortlinks/index.ts
var SHORTLINKS_ACTIONS = [
  listOrgShortlinksAction,
  listUserShortlinksAction,
  createShortlinkAction,
  updateShortlinkAction,
  deleteShortlinkAction
];

// ../ap/packages/pieces/community/zoo/src/lib/actions/unit/convert-angle.action.ts
var import_pieces_framework61 = require("@activepieces/pieces-framework");
var import_pieces_common60 = require("@activepieces/pieces-common"), convertAngleAction = (0, import_pieces_framework61.createAction)({
  name: "convert_angle",
  displayName: "Convert Angle",
  description: "Convert angle measurements between different units",
  audience: "both",
  aiMetadata: { description: "Convert a single angle value between degrees, radians, and gradians. Use only for angles; other quantities have their own dedicated convert actions. Read-only calculation that returns the same result for the same inputs.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Unit Conversion',
  props: {
    value: import_pieces_framework61.Property.Number({
      displayName: "Value",
      required: !0,
      description: "The angle value to convert"
    }),
    inputUnit: import_pieces_framework61.Property.StaticDropdown({
      displayName: "Input Unit",
      required: !0,
      options: {
        options: [
          { label: "Degrees", value: "deg" },
          { label: "Radians", value: "rad" },
          { label: "Gradians", value: "grad" }
        ]
      }
    }),
    outputUnit: import_pieces_framework61.Property.StaticDropdown({
      displayName: "Output Unit",
      required: !0,
      options: {
        options: [
          { label: "Degrees", value: "deg" },
          { label: "Radians", value: "rad" },
          { label: "Gradians", value: "grad" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common60.httpClient.sendRequest({
      method: import_pieces_common60.HttpMethod.GET,
      url: `https://api.zoo.dev/unit/conversion/angle/${propsValue.inputUnit}/${propsValue.outputUnit}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      queryParams: {
        value: propsValue.value.toString()
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/unit/convert-area.action.ts
var import_pieces_framework62 = require("@activepieces/pieces-framework");
var import_pieces_common61 = require("@activepieces/pieces-common"), convertAreaAction = (0, import_pieces_framework62.createAction)({
  name: "convert_area",
  displayName: "Convert Area",
  description: "Convert area measurements between different units",
  audience: "both",
  aiMetadata: { description: "Convert a single area value between units such as square meters/feet/inches/yards/kilometers/miles, hectares, and acres. Use only for area; other quantities have their own dedicated convert actions. Read-only calculation that returns the same result for the same inputs.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Unit Conversion',
  props: {
    value: import_pieces_framework62.Property.Number({
      displayName: "Value",
      required: !0,
      description: "The area value to convert"
    }),
    inputUnit: import_pieces_framework62.Property.StaticDropdown({
      displayName: "Input Unit",
      required: !0,
      options: {
        options: [
          { label: "Square Meters", value: "m2" },
          { label: "Square Feet", value: "ft2" },
          { label: "Square Inches", value: "in2" },
          { label: "Square Yards", value: "yd2" },
          { label: "Square Kilometers", value: "km2" },
          { label: "Square Miles", value: "mi2" },
          { label: "Hectares", value: "ha" },
          { label: "Acres", value: "ac" }
        ]
      }
    }),
    outputUnit: import_pieces_framework62.Property.StaticDropdown({
      displayName: "Output Unit",
      required: !0,
      options: {
        options: [
          { label: "Square Meters", value: "m2" },
          { label: "Square Feet", value: "ft2" },
          { label: "Square Inches", value: "in2" },
          { label: "Square Yards", value: "yd2" },
          { label: "Square Kilometers", value: "km2" },
          { label: "Square Miles", value: "mi2" },
          { label: "Hectares", value: "ha" },
          { label: "Acres", value: "ac" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common61.httpClient.sendRequest({
      method: import_pieces_common61.HttpMethod.GET,
      url: `https://api.zoo.dev/unit/conversion/area/${propsValue.inputUnit}/${propsValue.outputUnit}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      queryParams: {
        value: propsValue.value.toString()
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/unit/convert-current.action.ts
var import_pieces_framework63 = require("@activepieces/pieces-framework");
var import_pieces_common62 = require("@activepieces/pieces-common"), convertCurrentAction = (0, import_pieces_framework63.createAction)({
  name: "convert_current",
  displayName: "Convert Current",
  description: "Convert electrical current measurements between different units",
  audience: "both",
  aiMetadata: { description: "Convert a single electrical current value between amperes, milliamperes, and kiloamperes. Use only for electrical current; other quantities have their own dedicated convert actions. Read-only calculation that returns the same result for the same inputs.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Unit Conversion',
  props: {
    value: import_pieces_framework63.Property.Number({
      displayName: "Value",
      required: !0,
      description: "The current value to convert"
    }),
    inputUnit: import_pieces_framework63.Property.StaticDropdown({
      displayName: "Input Unit",
      required: !0,
      options: {
        options: [
          { label: "Amperes", value: "A" },
          { label: "Milliamperes", value: "mA" },
          { label: "Kiloamperes", value: "kA" }
        ]
      }
    }),
    outputUnit: import_pieces_framework63.Property.StaticDropdown({
      displayName: "Output Unit",
      required: !0,
      options: {
        options: [
          { label: "Amperes", value: "A" },
          { label: "Milliamperes", value: "mA" },
          { label: "Kiloamperes", value: "kA" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common62.httpClient.sendRequest({
      method: import_pieces_common62.HttpMethod.GET,
      url: `https://api.zoo.dev/unit/conversion/current/${propsValue.inputUnit}/${propsValue.outputUnit}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      queryParams: {
        value: propsValue.value.toString()
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/unit/convert-energy.action.ts
var import_pieces_framework64 = require("@activepieces/pieces-framework");
var import_pieces_common63 = require("@activepieces/pieces-common"), convertEnergyAction = (0, import_pieces_framework64.createAction)({
  name: "convert_energy",
  displayName: "Convert Energy",
  description: "Convert energy measurements between different units",
  audience: "both",
  aiMetadata: { description: "Convert a single energy value between units such as joules, kilojoules, calories, kilocalories, watt-hours, kilowatt-hours, and BTU. Use only for energy; other quantities have their own dedicated convert actions. Read-only calculation that returns the same result for the same inputs.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Unit Conversion',
  props: {
    value: import_pieces_framework64.Property.Number({
      displayName: "Value",
      required: !0,
      description: "The energy value to convert"
    }),
    inputUnit: import_pieces_framework64.Property.StaticDropdown({
      displayName: "Input Unit",
      required: !0,
      options: {
        options: [
          { label: "Joules", value: "J" },
          { label: "Kilojoules", value: "kJ" },
          { label: "Calories", value: "cal" },
          { label: "Kilocalories", value: "kcal" },
          { label: "Watt Hours", value: "Wh" },
          { label: "Kilowatt Hours", value: "kWh" },
          { label: "British Thermal Units", value: "BTU" }
        ]
      }
    }),
    outputUnit: import_pieces_framework64.Property.StaticDropdown({
      displayName: "Output Unit",
      required: !0,
      options: {
        options: [
          { label: "Joules", value: "J" },
          { label: "Kilojoules", value: "kJ" },
          { label: "Calories", value: "cal" },
          { label: "Kilocalories", value: "kcal" },
          { label: "Watt Hours", value: "Wh" },
          { label: "Kilowatt Hours", value: "kWh" },
          { label: "British Thermal Units", value: "BTU" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common63.httpClient.sendRequest({
      method: import_pieces_common63.HttpMethod.GET,
      url: `https://api.zoo.dev/unit/conversion/energy/${propsValue.inputUnit}/${propsValue.outputUnit}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      queryParams: {
        value: propsValue.value.toString()
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/unit/convert-force.action.ts
var import_pieces_framework65 = require("@activepieces/pieces-framework");
var import_pieces_common64 = require("@activepieces/pieces-common"), convertForceAction = (0, import_pieces_framework65.createAction)({
  name: "convert_force",
  displayName: "Convert Force",
  description: "Convert force measurements between different units",
  audience: "both",
  aiMetadata: { description: "Convert a single force value between newtons, kilonewtons, pound-force, and dynes. Use only for force; other quantities have their own dedicated convert actions. Read-only calculation that returns the same result for the same inputs.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Unit Conversion',
  props: {
    value: import_pieces_framework65.Property.Number({
      displayName: "Value",
      required: !0,
      description: "The force value to convert"
    }),
    inputUnit: import_pieces_framework65.Property.StaticDropdown({
      displayName: "Input Unit",
      required: !0,
      options: {
        options: [
          { label: "Newtons", value: "N" },
          { label: "Kilonewtons", value: "kN" },
          { label: "Pound-force", value: "lbf" },
          { label: "Dynes", value: "dyn" }
        ]
      }
    }),
    outputUnit: import_pieces_framework65.Property.StaticDropdown({
      displayName: "Output Unit",
      required: !0,
      options: {
        options: [
          { label: "Newtons", value: "N" },
          { label: "Kilonewtons", value: "kN" },
          { label: "Pound-force", value: "lbf" },
          { label: "Dynes", value: "dyn" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common64.httpClient.sendRequest({
      method: import_pieces_common64.HttpMethod.GET,
      url: `https://api.zoo.dev/unit/conversion/force/${propsValue.inputUnit}/${propsValue.outputUnit}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      queryParams: {
        value: propsValue.value.toString()
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/unit/convert-frequency.action.ts
var import_pieces_framework66 = require("@activepieces/pieces-framework");
var import_pieces_common65 = require("@activepieces/pieces-common"), convertFrequencyAction = (0, import_pieces_framework66.createAction)({
  name: "convert_frequency",
  displayName: "Convert Frequency",
  description: "Convert frequency measurements between different units",
  audience: "both",
  aiMetadata: { description: "Convert a single frequency value between hertz, kilohertz, megahertz, and gigahertz. Use only for frequency; other quantities have their own dedicated convert actions. Read-only calculation that returns the same result for the same inputs.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Unit Conversion',
  props: {
    value: import_pieces_framework66.Property.Number({
      displayName: "Value",
      required: !0,
      description: "The frequency value to convert"
    }),
    inputUnit: import_pieces_framework66.Property.StaticDropdown({
      displayName: "Input Unit",
      required: !0,
      options: {
        options: [
          { label: "Hertz", value: "Hz" },
          { label: "Kilohertz", value: "kHz" },
          { label: "Megahertz", value: "MHz" },
          { label: "Gigahertz", value: "GHz" }
        ]
      }
    }),
    outputUnit: import_pieces_framework66.Property.StaticDropdown({
      displayName: "Output Unit",
      required: !0,
      options: {
        options: [
          { label: "Hertz", value: "Hz" },
          { label: "Kilohertz", value: "kHz" },
          { label: "Megahertz", value: "MHz" },
          { label: "Gigahertz", value: "GHz" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common65.httpClient.sendRequest({
      method: import_pieces_common65.HttpMethod.GET,
      url: `https://api.zoo.dev/unit/conversion/frequency/${propsValue.inputUnit}/${propsValue.outputUnit}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      queryParams: {
        value: propsValue.value.toString()
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/unit/convert-length.action.ts
var import_pieces_framework67 = require("@activepieces/pieces-framework");
var import_pieces_common66 = require("@activepieces/pieces-common"), convertLengthAction = (0, import_pieces_framework67.createAction)({
  name: "convert_length",
  displayName: "Convert Length",
  description: "Convert length measurements between different units",
  audience: "both",
  aiMetadata: { description: "Convert a single length value between units such as meters, kilometers, centimeters, millimeters, inches, feet, yards, and miles. Use only for length/distance; other quantities have their own dedicated convert actions. Read-only calculation that returns the same result for the same inputs.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Unit Conversion',
  props: {
    value: import_pieces_framework67.Property.Number({
      displayName: "Value",
      required: !0,
      description: "The length value to convert"
    }),
    inputUnit: import_pieces_framework67.Property.StaticDropdown({
      displayName: "Input Unit",
      required: !0,
      options: {
        options: [
          { label: "Meters", value: "m" },
          { label: "Kilometers", value: "km" },
          { label: "Centimeters", value: "cm" },
          { label: "Millimeters", value: "mm" },
          { label: "Inches", value: "in" },
          { label: "Feet", value: "ft" },
          { label: "Yards", value: "yd" },
          { label: "Miles", value: "mi" }
        ]
      }
    }),
    outputUnit: import_pieces_framework67.Property.StaticDropdown({
      displayName: "Output Unit",
      required: !0,
      options: {
        options: [
          { label: "Meters", value: "m" },
          { label: "Kilometers", value: "km" },
          { label: "Centimeters", value: "cm" },
          { label: "Millimeters", value: "mm" },
          { label: "Inches", value: "in" },
          { label: "Feet", value: "ft" },
          { label: "Yards", value: "yd" },
          { label: "Miles", value: "mi" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common66.httpClient.sendRequest({
      method: import_pieces_common66.HttpMethod.GET,
      url: `https://api.zoo.dev/unit/conversion/length/${propsValue.inputUnit}/${propsValue.outputUnit}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      queryParams: {
        value: propsValue.value.toString()
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/unit/convert-mass.action.ts
var import_pieces_framework68 = require("@activepieces/pieces-framework");
var import_pieces_common67 = require("@activepieces/pieces-common"), convertMassAction = (0, import_pieces_framework68.createAction)({
  name: "convert_mass",
  displayName: "Convert Mass",
  description: "Convert mass measurements between different units",
  audience: "both",
  aiMetadata: { description: "Convert a single mass/weight value between units such as kilograms, grams, milligrams, pounds, ounces, and metric tons. Use only for mass; other quantities have their own dedicated convert actions. Read-only calculation that returns the same result for the same inputs.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Unit Conversion',
  props: {
    value: import_pieces_framework68.Property.Number({
      displayName: "Value",
      required: !0,
      description: "The mass value to convert"
    }),
    inputUnit: import_pieces_framework68.Property.StaticDropdown({
      displayName: "Input Unit",
      required: !0,
      options: {
        options: [
          { label: "Kilograms", value: "kg" },
          { label: "Grams", value: "g" },
          { label: "Milligrams", value: "mg" },
          { label: "Pounds", value: "lb" },
          { label: "Ounces", value: "oz" },
          { label: "Metric Tons", value: "t" }
        ]
      }
    }),
    outputUnit: import_pieces_framework68.Property.StaticDropdown({
      displayName: "Output Unit",
      required: !0,
      options: {
        options: [
          { label: "Kilograms", value: "kg" },
          { label: "Grams", value: "g" },
          { label: "Milligrams", value: "mg" },
          { label: "Pounds", value: "lb" },
          { label: "Ounces", value: "oz" },
          { label: "Metric Tons", value: "t" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common67.httpClient.sendRequest({
      method: import_pieces_common67.HttpMethod.GET,
      url: `https://api.zoo.dev/unit/conversion/mass/${propsValue.inputUnit}/${propsValue.outputUnit}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      queryParams: {
        value: propsValue.value.toString()
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/unit/convert-power.action.ts
var import_pieces_framework69 = require("@activepieces/pieces-framework");
var import_pieces_common68 = require("@activepieces/pieces-common"), convertPowerAction = (0, import_pieces_framework69.createAction)({
  name: "convert_power",
  displayName: "Convert Power",
  description: "Convert power measurements between different units",
  audience: "both",
  aiMetadata: { description: "Convert a power value between units (watts, kilowatts, megawatts, or horsepower) using Zoo's unit conversion API. Read-only and deterministic for the same inputs. Pick the power-specific action when converting power quantities rather than other unit types.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Unit Conversion',
  props: {
    value: import_pieces_framework69.Property.Number({
      displayName: "Value",
      required: !0,
      description: "The power value to convert"
    }),
    inputUnit: import_pieces_framework69.Property.StaticDropdown({
      displayName: "Input Unit",
      required: !0,
      options: {
        options: [
          { label: "Watts", value: "W" },
          { label: "Kilowatts", value: "kW" },
          { label: "Megawatts", value: "MW" },
          { label: "Horsepower", value: "hp" }
        ]
      }
    }),
    outputUnit: import_pieces_framework69.Property.StaticDropdown({
      displayName: "Output Unit",
      required: !0,
      options: {
        options: [
          { label: "Watts", value: "W" },
          { label: "Kilowatts", value: "kW" },
          { label: "Megawatts", value: "MW" },
          { label: "Horsepower", value: "hp" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common68.httpClient.sendRequest({
      method: import_pieces_common68.HttpMethod.GET,
      url: `https://api.zoo.dev/unit/conversion/power/${propsValue.inputUnit}/${propsValue.outputUnit}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      queryParams: {
        value: propsValue.value.toString()
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/unit/convert-pressure.action.ts
var import_pieces_framework70 = require("@activepieces/pieces-framework");
var import_pieces_common69 = require("@activepieces/pieces-common"), convertPressureAction = (0, import_pieces_framework70.createAction)({
  name: "convert_pressure",
  displayName: "Convert Pressure",
  description: "Convert pressure measurements between different units",
  audience: "both",
  aiMetadata: { description: "Convert a single pressure value between pascal, kilopascal, bar, atmosphere, and psi. Use only for pressure; other quantities have their own dedicated convert actions. Read-only calculation that returns the same result for the same inputs.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Unit Conversion',
  props: {
    value: import_pieces_framework70.Property.Number({
      displayName: "Value",
      required: !0,
      description: "The pressure value to convert"
    }),
    inputUnit: import_pieces_framework70.Property.StaticDropdown({
      displayName: "Input Unit",
      required: !0,
      options: {
        options: [
          { label: "Pascal", value: "Pa" },
          { label: "Kilopascal", value: "kPa" },
          { label: "Bar", value: "bar" },
          { label: "Atmosphere", value: "atm" },
          { label: "Pounds per Square Inch", value: "psi" }
        ]
      }
    }),
    outputUnit: import_pieces_framework70.Property.StaticDropdown({
      displayName: "Output Unit",
      required: !0,
      options: {
        options: [
          { label: "Pascal", value: "Pa" },
          { label: "Kilopascal", value: "kPa" },
          { label: "Bar", value: "bar" },
          { label: "Atmosphere", value: "atm" },
          { label: "Pounds per Square Inch", value: "psi" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common69.httpClient.sendRequest({
      method: import_pieces_common69.HttpMethod.GET,
      url: `https://api.zoo.dev/unit/conversion/pressure/${propsValue.inputUnit}/${propsValue.outputUnit}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      queryParams: {
        value: propsValue.value.toString()
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/unit/convert-temperature.action.ts
var import_pieces_framework71 = require("@activepieces/pieces-framework");
var import_pieces_common70 = require("@activepieces/pieces-common"), convertTemperatureAction = (0, import_pieces_framework71.createAction)({
  name: "convert_temperature",
  displayName: "Convert Temperature",
  description: "Convert temperature measurements between different units",
  audience: "both",
  aiMetadata: { description: "Convert a single temperature value between Celsius, Fahrenheit, and Kelvin. Pick this only for temperature; other quantities (mass, length, energy, etc.) have their own dedicated convert actions. Read-only calculation that returns the same result for the same inputs.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Unit Conversion',
  props: {
    value: import_pieces_framework71.Property.Number({
      displayName: "Value",
      required: !0,
      description: "The temperature value to convert"
    }),
    inputUnit: import_pieces_framework71.Property.StaticDropdown({
      displayName: "Input Unit",
      required: !0,
      options: {
        options: [
          { label: "Celsius", value: "C" },
          { label: "Fahrenheit", value: "F" },
          { label: "Kelvin", value: "K" }
        ]
      }
    }),
    outputUnit: import_pieces_framework71.Property.StaticDropdown({
      displayName: "Output Unit",
      required: !0,
      options: {
        options: [
          { label: "Celsius", value: "C" },
          { label: "Fahrenheit", value: "F" },
          { label: "Kelvin", value: "K" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common70.httpClient.sendRequest({
      method: import_pieces_common70.HttpMethod.GET,
      url: `https://api.zoo.dev/unit/conversion/temperature/${propsValue.inputUnit}/${propsValue.outputUnit}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      queryParams: {
        value: propsValue.value.toString()
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/unit/convert-torque.action.ts
var import_pieces_framework72 = require("@activepieces/pieces-framework");
var import_pieces_common71 = require("@activepieces/pieces-common"), convertTorqueAction = (0, import_pieces_framework72.createAction)({
  name: "convert_torque",
  displayName: "Convert Torque",
  description: "Convert torque measurements between different units",
  audience: "both",
  aiMetadata: { description: "Convert a single torque value between newton-meter, pound-foot, and kilogram-force-meter. Use only for torque; other quantities have their own dedicated convert actions. Read-only calculation that returns the same result for the same inputs.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Unit Conversion',
  props: {
    value: import_pieces_framework72.Property.Number({
      displayName: "Value",
      required: !0,
      description: "The torque value to convert"
    }),
    inputUnit: import_pieces_framework72.Property.StaticDropdown({
      displayName: "Input Unit",
      required: !0,
      options: {
        options: [
          { label: "Newton Meter", value: "Nm" },
          { label: "Pound Foot", value: "lbft" },
          { label: "Kilogram Force Meter", value: "kgfm" }
        ]
      }
    }),
    outputUnit: import_pieces_framework72.Property.StaticDropdown({
      displayName: "Output Unit",
      required: !0,
      options: {
        options: [
          { label: "Newton Meter", value: "Nm" },
          { label: "Pound Foot", value: "lbft" },
          { label: "Kilogram Force Meter", value: "kgfm" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common71.httpClient.sendRequest({
      method: import_pieces_common71.HttpMethod.GET,
      url: `https://api.zoo.dev/unit/conversion/torque/${propsValue.inputUnit}/${propsValue.outputUnit}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      queryParams: {
        value: propsValue.value.toString()
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/unit/convert-volume.action.ts
var import_pieces_framework73 = require("@activepieces/pieces-framework");
var import_pieces_common72 = require("@activepieces/pieces-common"), convertVolumeAction = (0, import_pieces_framework73.createAction)({
  name: "convert_volume",
  displayName: "Convert Volume",
  description: "Convert volume measurements between different units",
  audience: "both",
  aiMetadata: { description: "Convert a single volume value between units such as cubic meters/feet/inches, liters, milliliters, gallons, and fluid ounces. Use only for volume; other quantities have their own dedicated convert actions. Read-only calculation that returns the same result for the same inputs.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Unit Conversion',
  props: {
    value: import_pieces_framework73.Property.Number({
      displayName: "Value",
      required: !0,
      description: "The volume value to convert"
    }),
    inputUnit: import_pieces_framework73.Property.StaticDropdown({
      displayName: "Input Unit",
      required: !0,
      options: {
        options: [
          { label: "Cubic Meters", value: "m3" },
          { label: "Cubic Feet", value: "ft3" },
          { label: "Cubic Inches", value: "in3" },
          { label: "Liters", value: "L" },
          { label: "Gallons", value: "gal" },
          { label: "Milliliters", value: "mL" },
          { label: "Fluid Ounces", value: "fl_oz" }
        ]
      }
    }),
    outputUnit: import_pieces_framework73.Property.StaticDropdown({
      displayName: "Output Unit",
      required: !0,
      options: {
        options: [
          { label: "Cubic Meters", value: "m3" },
          { label: "Cubic Feet", value: "ft3" },
          { label: "Cubic Inches", value: "in3" },
          { label: "Liters", value: "L" },
          { label: "Gallons", value: "gal" },
          { label: "Milliliters", value: "mL" },
          { label: "Fluid Ounces", value: "fl_oz" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common72.httpClient.sendRequest({
      method: import_pieces_common72.HttpMethod.GET,
      url: `https://api.zoo.dev/unit/conversion/volume/${propsValue.inputUnit}/${propsValue.outputUnit}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      queryParams: {
        value: propsValue.value.toString()
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/unit/index.ts
var UNIT_ACTIONS = [
  convertAngleAction,
  convertAreaAction,
  convertCurrentAction,
  convertEnergyAction,
  convertForceAction,
  convertFrequencyAction,
  convertLengthAction,
  convertMassAction,
  convertPowerAction,
  convertPressureAction,
  convertTemperatureAction,
  convertTorqueAction,
  convertVolumeAction
];

// ../ap/packages/pieces/community/zoo/src/lib/actions/users/get-user.action.ts
var import_pieces_framework74 = require("@activepieces/pieces-framework");
var import_pieces_common73 = require("@activepieces/pieces-common"), getUserAction = (0, import_pieces_framework74.createAction)({
  name: "get_user",
  displayName: "Get User",
  description: "Retrieve your user information",
  audience: "both",
  aiMetadata: { description: "Fetch the profile of the currently authenticated Zoo user (name, email, and core account fields). Read-only and repeatable. Use the get extended user info action when you need additional metadata beyond the basic profile.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Users',
  props: {},
  async run({ auth }) {
    return (await import_pieces_common73.httpClient.sendRequest({
      method: import_pieces_common73.HttpMethod.GET,
      url: "https://api.zoo.dev/user",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/users/update-user.action.ts
var import_pieces_framework75 = require("@activepieces/pieces-framework");
var import_pieces_common74 = require("@activepieces/pieces-common");
var updateUserAction = (0, import_pieces_framework75.createAction)({
  name: "update_user",
  displayName: "Update User",
  description: "Update your user information",
  audience: "both",
  aiMetadata: { description: "Update the authenticated Zoo user's profile, setting a new display name and/or email. Only the provided fields are changed, and applying the same values again yields the same state (idempotent). Affects the current account only, not arbitrary users.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Users',
  props: {
    name: import_pieces_framework75.Property.ShortText({
      displayName: "Name",
      required: !1,
      description: "Your new display name"
    }),
    email: import_pieces_framework75.Property.ShortText({
      displayName: "Email",
      required: !1,
      description: "Your new email address"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common74.httpClient.sendRequest({
      method: import_pieces_common74.HttpMethod.PUT,
      url: "https://api.zoo.dev/user",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      body: __spreadValues(__spreadValues({}, propsValue.name && { name: propsValue.name }), propsValue.email && { email: propsValue.email })
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/users/delete-user.action.ts
var import_pieces_framework76 = require("@activepieces/pieces-framework");
var import_pieces_common75 = require("@activepieces/pieces-common"), deleteUserAction = (0, import_pieces_framework76.createAction)({
  name: "delete_user",
  displayName: "Delete User",
  description: "Delete your user account",
  audience: "both",
  aiMetadata: { description: "Permanently delete the authenticated Zoo user account. Destructive and irreversible; the first call removes the account and subsequent calls have no further effect. Use only when intentionally closing the current account.", idempotent: !1 },
  auth: zooAuth,
  // category: 'Users',
  props: {},
  async run({ auth }) {
    return (await import_pieces_common75.httpClient.sendRequest({
      method: import_pieces_common75.HttpMethod.DELETE,
      url: "https://api.zoo.dev/user",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/users/get-extended-user.action.ts
var import_pieces_framework77 = require("@activepieces/pieces-framework");
var import_pieces_common76 = require("@activepieces/pieces-common"), getExtendedUserAction = (0, import_pieces_framework77.createAction)({
  name: "get_extended_user",
  displayName: "Get Extended User Info",
  description: "Retrieve extended information about your user account",
  audience: "both",
  aiMetadata: { description: "Fetch extended account details for the authenticated Zoo user beyond the basic profile. Read-only and repeatable. Prefer the plain get user action when only the core profile is needed.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Users',
  props: {},
  async run({ auth }) {
    return (await import_pieces_common76.httpClient.sendRequest({
      method: import_pieces_common76.HttpMethod.GET,
      url: "https://api.zoo.dev/user/extended",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/users/get-oauth2-providers.action.ts
var import_pieces_framework78 = require("@activepieces/pieces-framework");
var import_pieces_common77 = require("@activepieces/pieces-common"), getOAuth2ProvidersAction = (0, import_pieces_framework78.createAction)({
  name: "get_oauth2_providers",
  displayName: "Get OAuth2 Providers",
  description: "Get the OAuth2 providers available for your user account",
  audience: "both",
  aiMetadata: { description: "List the OAuth2 identity providers available for the authenticated Zoo user account. Read-only and repeatable; takes no input.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Users',
  props: {},
  async run({ auth }) {
    return (await import_pieces_common77.httpClient.sendRequest({
      method: import_pieces_common77.HttpMethod.GET,
      url: "https://api.zoo.dev/user/oauth2/providers",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/users/get-user-org.action.ts
var import_pieces_framework79 = require("@activepieces/pieces-framework");
var import_pieces_common78 = require("@activepieces/pieces-common"), getUserOrgAction = (0, import_pieces_framework79.createAction)({
  name: "get_user_org",
  displayName: "Get User Organization",
  description: "Get the organization associated with your user account",
  audience: "both",
  aiMetadata: { description: "Retrieve the organization that the authenticated Zoo user belongs to. Read-only and repeatable; takes no input.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Users',
  props: {},
  async run({ auth }) {
    return (await import_pieces_common78.httpClient.sendRequest({
      method: import_pieces_common78.HttpMethod.GET,
      url: "https://api.zoo.dev/user/org",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/users/get-privacy-settings.action.ts
var import_pieces_framework80 = require("@activepieces/pieces-framework");
var import_pieces_common79 = require("@activepieces/pieces-common"), getPrivacySettingsAction = (0, import_pieces_framework80.createAction)({
  name: "get_privacy_settings",
  displayName: "Get Privacy Settings",
  description: "Get your user privacy settings",
  audience: "both",
  aiMetadata: { description: "Read the privacy settings for the authenticated Zoo user, such as whether generated data may be used for training. Read-only and repeatable. Pair with the update privacy settings action to change these values.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Users',
  props: {},
  async run({ auth }) {
    return (await import_pieces_common79.httpClient.sendRequest({
      method: import_pieces_common79.HttpMethod.GET,
      url: "https://api.zoo.dev/user/privacy",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/users/update-privacy-settings.action.ts
var import_pieces_framework81 = require("@activepieces/pieces-framework");
var import_pieces_common80 = require("@activepieces/pieces-common"), updatePrivacySettingsAction = (0, import_pieces_framework81.createAction)({
  name: "update_privacy_settings",
  displayName: "Update Privacy Settings",
  description: "Update your user privacy settings",
  audience: "both",
  aiMetadata: { description: "Replace the privacy settings for the authenticated Zoo user with a supplied settings object (e.g. data-retention or training-use preferences). Applying the same settings repeatedly yields the same state (idempotent). Read current values first with the get privacy settings action.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Users',
  props: {
    settings: import_pieces_framework81.Property.Object({
      displayName: "Privacy Settings",
      required: !0,
      description: "The new privacy settings to apply"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common80.httpClient.sendRequest({
      method: import_pieces_common80.HttpMethod.PUT,
      url: "https://api.zoo.dev/user/privacy",
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      body: propsValue.settings
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/users/get-user-session.action.ts
var import_pieces_framework82 = require("@activepieces/pieces-framework");
var import_pieces_common81 = require("@activepieces/pieces-common"), getUserSessionAction = (0, import_pieces_framework82.createAction)({
  name: "get_user_session",
  displayName: "Get User Session",
  description: "Get details about a specific user session",
  audience: "both",
  aiMetadata: { description: "Look up details of a specific Zoo user session by its session token. Read-only and repeatable. Requires the session token of the session you want to inspect.", idempotent: !0 },
  auth: zooAuth,
  // category: 'Users',
  props: {
    token: import_pieces_framework82.Property.ShortText({
      displayName: "Session Token",
      required: !0,
      description: "The token of the session to retrieve"
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common81.httpClient.sendRequest({
      method: import_pieces_common81.HttpMethod.GET,
      url: `https://api.zoo.dev/user/session/${propsValue.token}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/zoo/src/lib/actions/users/index.ts
var USER_ACTIONS = [
  getUserAction,
  updateUserAction,
  deleteUserAction,
  getExtendedUserAction,
  getOAuth2ProvidersAction,
  getUserOrgAction,
  getPrivacySettingsAction,
  updatePrivacySettingsAction,
  getUserSessionAction
];

// ../ap/packages/pieces/community/zoo/src/index.ts
var textToCad = (0, import_pieces_framework83.createPiece)({
  displayName: "Zoo",
  description: "Generate and iterate on 3D models from text descriptions using ML endpoints.",
  logoUrl: "https://cdn.activepieces.com/pieces/zoo.jpg",
  auth: zooAuth,
  categories: [import_pieces_framework84.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  minimumSupportedRelease: "0.20.0",
  authors: ["ahmad-swanblocks"],
  actions: [...ML_ACTIONS, ...API_CALLS_ACTIONS, ...API_TOKENS_ACTIONS, ...FILE_ACTIONS, ...META_ACTIONS, ...MODELING_ACTIONS, ...ORGS_ACTIONS, ...PAYMENTS_ACTIONS, ...SERVICE_ACCOUNTS_ACTIONS, ...SHORTLINKS_ACTIONS, ...UNIT_ACTIONS, ...USER_ACTIONS],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  textToCad
});
