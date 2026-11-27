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

// ../ap/packages/pieces/community/mysendingbox/src/index.ts
var index_exports = {};
__export(index_exports, {
  mySendingBoxPieceAuth: () => mySendingBoxPieceAuth,
  mysendingbox: () => mysendingbox
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), markdown = `
MySendingBox API keyis available under the developer portal.
(https://app.mysendingbox.fr/account/keys)`, mySendingBoxPieceAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: markdown,
  required: !0,
  props: {
    apiKey: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API Key",
      required: !0
    })
  },
  validate: async ({ auth }) => auth ? (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.GET,
    url: "https://api.mysendingbox.fr/",
    headers: {
      Authorization: `Basic ${btoa(`${auth.apiKey}:`)}`
    }
  }), {
    valid: !0
  }) : {
    valid: !1,
    error: "Invalid Api Key"
  }
}), mysendingbox = (0, import_pieces_framework.createPiece)({
  displayName: "Mysendingbox",
  auth: mySendingBoxPieceAuth,
  minimumSupportedRelease: "0.78.0",
  logoUrl: "https://cdn.activepieces.com/pieces/mysendingbox.png",
  authors: ["Blightwidow"],
  actions: [
    (0, import_pieces_common.createCustomApiCallAction)({
      baseUrl: () => "https://api.mysendingbox.fr",
      auth: mySendingBoxPieceAuth,
      authMapping: async (auth) => ({
        Authorization: `Basic ${btoa(`${auth.props.apiKey}:`)}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  mySendingBoxPieceAuth,
  mysendingbox
});
