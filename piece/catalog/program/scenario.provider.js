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

// ../ap/packages/pieces/community/scenario/src/index.ts
var index_exports = {};
__export(index_exports, {
  scenario: () => scenario,
  scenarioAuth: () => scenarioAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), scenarioAuth = import_pieces_framework.PieceAuth.BasicAuth({
  description: "Follow [these instructions](https://docs.scenario.com/docs/get-api-key) to get your API key",
  required: !0,
  username: import_pieces_framework.Property.ShortText({
    displayName: "API access key",
    description: 'Starts with "api_"',
    required: !0
  }),
  password: import_pieces_framework.PieceAuth.SecretText({
    displayName: "API secret",
    required: !0
  })
}), scenario = (0, import_pieces_framework.createPiece)({
  displayName: "Scenario",
  auth: scenarioAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/scenario.png",
  authors: ["AdamSelene"],
  actions: [
    (0, import_pieces_common.createCustomApiCallAction)({
      baseUrl: () => "https://api.cloud.scenario.com/v1/",
      auth: scenarioAuth,
      authMapping: async (auth) => {
        let { username, password } = auth;
        return {
          Authorization: `Basic ${Buffer.from(
            `${username}:${password}`
          ).toString("base64")}`
        };
      }
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  scenario,
  scenarioAuth
});
