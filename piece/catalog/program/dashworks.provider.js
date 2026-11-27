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

// ../ap/packages/pieces/community/dashworks/src/index.ts
var index_exports = {};
__export(index_exports, {
  dashworks: () => dashworks
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/dashworks/src/lib/actions/generate-answer.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/dashworks/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), dashworksAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "You can obtain API key from [API Settings](https://web.dashworks.ai/admin/api-keys)."
});

// ../ap/packages/pieces/community/dashworks/src/lib/actions/generate-answer.ts
var import_pieces_common = require("@activepieces/pieces-common"), generateAnswerAction = (0, import_pieces_framework2.createAction)({
  name: "generate-answer",
  auth: dashworksAuth,
  displayName: "Generate Answer",
  description: "Generates an answer from a Dashworks bot.",
  audience: "both",
  aiMetadata: { description: "Send a question or prompt to a specific Dashworks bot and get back a generated, knowledge-grounded answer. Use when an agent needs Dashworks to answer using its connected company knowledge sources. Requires the target bot ID; optionally toggle inline markdown source citations in the answer. Not idempotent \u2014 each call generates a fresh answer.", idempotent: !1 },
  props: {
    botId: import_pieces_framework2.Property.ShortText({
      displayName: "Bot ID",
      required: !0,
      description: "You can find this by going to Dashworks > Bots, clicking on an existing Bot, and using the 'Copy Bot ID' button on the top right."
    }),
    message: import_pieces_framework2.Property.LongText({
      displayName: "Message",
      required: !0,
      description: "The message (e.g., a question or prompt) that you want a response to."
    }),
    inlineSources: import_pieces_framework2.Property.Checkbox({
      displayName: "Inline Sources",
      required: !1,
      description: "When true (default), sources will be cited inline in markdown formatting within the answer text.When false, the answer will not contain inline citations."
    })
  },
  async run(context) {
    let { botId, message, inlineSources } = context.propsValue;
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://api.dashworks.ai/v1/answer",
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      },
      body: {
        message,
        bot_id: botId,
        inline_sources: inlineSources
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/dashworks/src/index.ts
var import_pieces_common2 = require("@activepieces/pieces-common");
var dashworks = (0, import_pieces_framework3.createPiece)({
  displayName: "Dashworks",
  categories: [import_pieces_framework4.PieceCategory.PRODUCTIVITY],
  auth: dashworksAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/dashworks.png",
  authors: ["kishanprmr"],
  actions: [
    generateAnswerAction,
    (0, import_pieces_common2.createCustomApiCallAction)({
      auth: dashworksAuth,
      baseUrl: () => "https://api.dashworks.ai/v1/",
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  dashworks
});
