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

// ../ap/packages/pieces/community/metatext/src/index.ts
var index_exports = {};
__export(index_exports, {
  metatext: () => metatext
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/metatext/src/lib/actions/extract-text.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/metatext/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), metatextAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Contact support@metatext.io to get your API key.",
  required: !0
});

// ../ap/packages/pieces/community/metatext/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), METATEXT_API_URL = "https://api.metatext.ai";
async function metatextApiCall({
  apiKey,
  method,
  endpoint,
  baseUrl = METATEXT_API_URL,
  authType = "x-api-key",
  query,
  body
}) {
  let qs = {};
  if (query)
    for (let [key, value] of Object.entries(query))
      value != null && (qs[key] = String(value));
  let headers = authType === "bearer" ? {
    Authorization: `Bearer ${apiKey}`,
    "Content-Type": "application/json"
  } : {
    "x-api-key": apiKey,
    "Content-Type": "application/json"
  };
  return (await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${baseUrl}${endpoint}`,
    headers,
    queryParams: qs,
    body
  })).body;
}

// ../ap/packages/pieces/community/metatext/src/lib/actions/extract-text.ts
var extractText = (0, import_pieces_framework2.createAction)({
  auth: metatextAuth,
  name: "extract_text",
  displayName: "Extract Text",
  description: "Extract information from text (entities, keywords, custom models)",
  audience: "both",
  aiMetadata: { description: "Run a Metatext extraction project against a block of text to pull out structured information such as entities or keywords. Use when you have a configured extraction project (referenced by its project ID) and want to extract fields from raw text; optionally target a specific model and version. Requires the project ID and the text; this is a read-only analysis call that returns the same result for the same input, so it is safe to repeat.", idempotent: !0 },
  props: {
    project_id: import_pieces_framework2.Property.ShortText({
      displayName: "Project ID",
      description: "The ID of your extraction project",
      required: !0
    }),
    text: import_pieces_framework2.Property.LongText({
      displayName: "Text",
      description: "The text to extract information from",
      required: !0
    }),
    model: import_pieces_framework2.Property.ShortText({
      displayName: "Model",
      description: "The model to use for extraction",
      required: !1
    }),
    refresh: import_pieces_framework2.Property.Checkbox({
      displayName: "Refresh",
      description: "Force refresh the extraction",
      required: !1,
      defaultValue: !1
    }),
    version: import_pieces_framework2.Property.ShortText({
      displayName: "Version",
      description: "Model version to use",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let { project_id, text, model, refresh, version } = propsValue, body = { text };
    return model && (body.model = model), (refresh !== void 0 || version) && (body.options = __spreadValues(__spreadValues({}, refresh !== void 0 && { refresh }), version && { version })), await metatextApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common2.HttpMethod.POST,
      endpoint: `/v2/extract/${project_id}`,
      body
    });
  }
});

// ../ap/packages/pieces/community/metatext/src/lib/actions/classify-text.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var classifyText = (0, import_pieces_framework3.createAction)({
  auth: metatextAuth,
  name: "classify_text",
  displayName: "Classify Text",
  description: "Classify text into categories (sentiment, intent, topic, custom models)",
  audience: "both",
  aiMetadata: { description: "Run a Metatext classification project against a block of text to assign it to categories such as sentiment, intent, or topic. Use when you have a configured classification project (referenced by its project ID) and want predicted labels for raw text; optionally tune which model/version to use, a minimum confidence threshold, and how many top labels to return. Requires the project ID and the text; this is a read-only analysis call that returns the same result for the same input, so it is safe to repeat.", idempotent: !0 },
  props: {
    project_id: import_pieces_framework3.Property.ShortText({
      displayName: "Project ID",
      description: "The ID of your classification project",
      required: !0
    }),
    text: import_pieces_framework3.Property.LongText({
      displayName: "Text",
      description: "The text to classify",
      required: !0
    }),
    model: import_pieces_framework3.Property.ShortText({
      displayName: "Model",
      description: "The model to use for classification",
      required: !1
    }),
    threshold: import_pieces_framework3.Property.Number({
      displayName: "Threshold",
      description: "Minimum confidence score for predictions",
      required: !1
    }),
    top_labels: import_pieces_framework3.Property.Number({
      displayName: "Top Labels",
      description: "Maximum number of labels to return",
      required: !1
    }),
    refresh: import_pieces_framework3.Property.Checkbox({
      displayName: "Refresh",
      description: "Force refresh the classification",
      required: !1,
      defaultValue: !1
    }),
    version: import_pieces_framework3.Property.ShortText({
      displayName: "Version",
      description: "Model version to use",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let { project_id, text, model, threshold, top_labels, refresh, version } = propsValue, body = { text };
    model && (body.model = model);
    let options = {};
    return threshold !== void 0 && (options.threshold = threshold), top_labels !== void 0 && (options.top_labels = top_labels), refresh !== void 0 && (options.refresh = refresh), version && (options.version = version), Object.keys(options).length > 0 && (body.options = options), await metatextApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common3.HttpMethod.POST,
      endpoint: `/v2/classify/${project_id}`,
      body
    });
  }
});

// ../ap/packages/pieces/community/metatext/src/lib/actions/finetune-model.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var finetuneModel = (0, import_pieces_framework4.createAction)({
  auth: metatextAuth,
  name: "finetune_model",
  displayName: "Fine-tune Model",
  description: "Initiate a fine-tuning job for your model",
  audience: "both",
  aiMetadata: { description: "Start a new Metatext fine-tuning job for a project, training a base model on the project data for a chosen task type (classification, extraction, or generation). Use when you want to kick off model training rather than run inference. Requires the project ID, task type, and base model name; optional hyperparameters tune the run. Not idempotent: each call launches a separate training job.", idempotent: !1 },
  props: {
    project_id: import_pieces_framework4.Property.ShortText({
      displayName: "Project ID",
      description: "The ID of your project",
      required: !0
    }),
    taskType: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Task Type",
      description: "The type of task for fine-tuning",
      required: !0,
      options: {
        options: [
          { label: "Classification", value: "classification" },
          { label: "Extraction", value: "extraction" },
          { label: "Generation", value: "generation" }
        ]
      }
    }),
    modelBase: import_pieces_framework4.Property.ShortText({
      displayName: "Base Model",
      description: "The base model to fine-tune (e.g., BERT)",
      required: !0
    }),
    hyperparameters: import_pieces_framework4.Property.Object({
      displayName: "Hyperparameters",
      description: "Custom hyperparameters for fine-tuning",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let { project_id, taskType, modelBase, hyperparameters } = propsValue, body = {
      taskType,
      modelBase
    };
    return hyperparameters && Object.keys(hyperparameters).length > 0 && (body.hyperparameters = hyperparameters), await metatextApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common4.HttpMethod.POST,
      endpoint: `/v2/projects/${project_id}/finetune`,
      authType: "bearer",
      body
    });
  }
});

// ../ap/packages/pieces/community/metatext/src/index.ts
var metatext = (0, import_pieces_framework5.createPiece)({
  displayName: "Metatext",
  description: "AI content moderation and safety guard API",
  auth: metatextAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/metatext.png",
  categories: [import_pieces_framework6.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  authors: ["onyedikachi-david"],
  actions: [extractText, classifyText, finetuneModel],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  metatext
});
