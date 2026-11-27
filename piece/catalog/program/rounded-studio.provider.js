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

// ../ap/packages/pieces/community/rounded-studio/src/index.ts
var index_exports = {};
__export(index_exports, {
  callRounded: () => callRounded,
  callRoundedAuth: () => callRoundedAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), callRoundedAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "Please enter the API Key obtained from Call Rounded."
}), callRounded = (0, import_pieces_framework.createPiece)({
  displayName: "Call-rounded",
  auth: callRoundedAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/call-rounded.png",
  authors: ["perrine-pullicino-alan"],
  actions: [
    (0, import_pieces_common.createCustomApiCallAction)({
      baseUrl: () => "https://api.callrounded.com/v1",
      auth: callRoundedAuth,
      authMapping: async (auth) => ({
        "x-app": "activepieces",
        "x-api-key": auth.secret_text
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  callRounded,
  callRoundedAuth
});
