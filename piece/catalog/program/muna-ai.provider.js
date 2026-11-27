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

// ../ap/packages/pieces/community/muna-ai/src/index.ts
var index_exports = {};
__export(index_exports, {
  munaAi: () => munaAi
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/muna-ai/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), munaAiAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Go to [Muna Settings \u2192 Developer](https://muna.ai/settings/developer) to generate an access key.",
  required: !0
});

// ../ap/packages/pieces/community/muna-ai/src/lib/actions/create-prediction.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var createPrediction = (0, import_pieces_framework2.createAction)({
  audience: "both",
  name: "create_prediction",
  displayName: "Create Prediction",
  description: "Run a predictor and get back prediction resources.",
  aiMetadata: {
    description: "Creates a prediction against a named Muna predictor and returns the prediction record plus the resource URLs needed to run that predictor on-device; it provisions the run rather than returning model inference output, and it is the only purpose-built Muna action, so reach for the custom API call action for any other Muna endpoint. Requires the predictor tag (such as @org/my-model) and a client ID identifying the calling device or user; the optional configuration, device, and prior prediction IDs only steer which implementation Muna hands back. Not idempotent: each call creates a new prediction with a new ID.",
    idempotent: !1
  },
  auth: munaAiAuth,
  props: {
    tag: import_pieces_framework2.Property.ShortText({
      displayName: "Predictor Tag",
      description: "The tag of the predictor to run (e.g. `@org/my-model`).",
      required: !0
    }),
    clientId: import_pieces_framework2.Property.ShortText({
      displayName: "Client ID",
      description: "A unique identifier for the client making the prediction. Used to track usage per device or user.",
      required: !0
    }),
    configurationId: import_pieces_framework2.Property.ShortText({
      displayName: "Configuration ID",
      description: "Optional predictor configuration identifier.",
      required: !1
    }),
    deviceId: import_pieces_framework2.Property.ShortText({
      displayName: "Device ID",
      description: "Optional device identifier. Muna uses this to choose the optimal implementation to respond with.",
      required: !1
    }),
    predictionId: import_pieces_framework2.Property.ShortText({
      displayName: "Prediction ID",
      description: "For embedded predictors \u2014 providing the original prediction ID ensures the same implementation is delivered.",
      required: !1
    })
  },
  async run(context) {
    var _a;
    let { tag, clientId, configurationId, deviceId, predictionId } = context.propsValue, body = { tag, clientId };
    configurationId && (body.configurationId = configurationId), deviceId && (body.deviceId = deviceId), predictionId && (body.predictionId = predictionId);
    let prediction = (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://api.muna.ai/v1/predictions",
      headers: {
        Authorization: `Bearer ${context.auth.secret_text}`,
        "Content-Type": "application/json"
      },
      body
    })).body;
    return {
      id: prediction.id,
      tag: prediction.tag,
      configuration: (_a = prediction.configuration) != null ? _a : null,
      created: prediction.created,
      resources: prediction.resources.map((r) => {
        var _a2;
        return {
          type: r.type,
          url: r.url,
          name: (_a2 = r.name) != null ? _a2 : null
        };
      })
    };
  }
});

// ../ap/packages/pieces/community/muna-ai/src/index.ts
var munaAi = (0, import_pieces_framework3.createPiece)({
  displayName: "Muna",
  description: "Run on-device AI predictions using Muna predictors.",
  auth: munaAiAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/muna-ai.png",
  categories: [import_pieces_framework4.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  authors: ["sanket-a11y"],
  actions: [
    createPrediction,
    (0, import_pieces_common2.createCustomApiCallAction)({
      baseUrl: () => "https://api.muna.ai/v1",
      auth: munaAiAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  munaAi
});
