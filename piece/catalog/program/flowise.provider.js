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

// ../ap/packages/pieces/community/flowise/src/index.ts
var index_exports = {};
__export(index_exports, {
  flowise: () => flowise,
  flowisePredict: () => flowisePredict
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_framework2 = require("@activepieces/pieces-framework"), flowiseAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: "Enter your Flowise URL and API Key",
  props: {
    base_url: import_pieces_framework.Property.ShortText({
      displayName: "Base URL",
      description: "Enter the base URL",
      required: !0
    }),
    access_token: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API Key",
      description: "Enter the API Key",
      required: !0
    })
  },
  required: !0
}), flowisePredict = (0, import_pieces_framework.createAction)({
  name: "make_prediction",
  displayName: "Make Prediction",
  description: "Run Flowise Predict",
  audience: "both",
  aiMetadata: { description: "Sends a question/input to a specific Flowise chatflow by its Chatflow ID and returns the prediction, optionally passing prior conversation history and runtime override config. Use to invoke a deployed Flowise AI workflow (chatbot, RAG, agent) for inference. Requires the target Chatflow ID; not idempotent since each call generates a fresh model response.", idempotent: !1 },
  auth: flowiseAuth,
  props: {
    chatflow_id: import_pieces_framework.Property.ShortText({
      displayName: "Chatflow ID",
      description: "Enter the Chatflow ID",
      required: !0
    }),
    input: import_pieces_framework.Property.ShortText({
      displayName: "Input/Question",
      description: "Enter the Input/Question",
      required: !0
    }),
    history: import_pieces_framework.Property.Json({
      displayName: "History",
      description: "Enter the History",
      required: !1
    }),
    overrideConfig: import_pieces_framework.Property.Json({
      displayName: "Override Config",
      description: "Enter the Override Config",
      required: !1
    })
  },
  async run(ctx) {
    let { base_url, access_token } = ctx.auth.props, chatflow_id = ctx.propsValue.chatflow_id, input = ctx.propsValue.input, url = `${base_url}/api/v1/prediction/${chatflow_id}`, headers = {
      "Content-Type": "application/json",
      Authorization: `Bearer ${access_token}`
    }, body = {
      question: input,
      history: ctx.propsValue.history,
      overrideConfig: ctx.propsValue.overrideConfig
    };
    return await (await fetch(url, {
      method: "POST",
      headers,
      body: JSON.stringify(body)
    })).json();
  }
}), flowise = (0, import_pieces_framework.createPiece)({
  displayName: "Flowise",
  description: "No-Code AI workflow builder",
  logoUrl: "https://cdn.activepieces.com/pieces/flowise.png",
  auth: flowiseAuth,
  minimumSupportedRelease: "0.30.0",
  categories: [import_pieces_framework2.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  authors: ["aasimsani", "kishanprmr", "MoShizzle", "abuaboud"],
  actions: [
    flowisePredict,
    (0, import_pieces_common.createCustomApiCallAction)({
      baseUrl: (auth) => {
        var _a;
        return (_a = auth == null ? void 0 : auth.props.base_url) != null ? _a : "";
      },
      auth: flowiseAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.props.access_token}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  flowise,
  flowisePredict
});
