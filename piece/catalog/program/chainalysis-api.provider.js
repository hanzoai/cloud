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

// ../ap/packages/pieces/community/chainalysis-api/src/index.ts
var index_exports = {};
__export(index_exports, {
  chainalysisApi: () => chainalysisApi,
  chainalysisApiAuth: () => chainalysisApiAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/chainalysis-api/src/lib/actions/check-address-sanction.ts
var import_pieces_framework = require("@activepieces/pieces-framework");
var checkAddressSanction = (0, import_pieces_framework.createAction)({
  name: "checkAddressSanction",
  classification: "READ",
  displayName: "Check Address Sanctions",
  description: "Check if an address is sanctioned",
  audience: "both",
  aiMetadata: { description: "Screens a single blockchain address against Chainalysis sanctions data to determine whether it is associated with a sanctioned entity. Use this when you need to verify the sanctions/compliance status of a wallet address before transacting or onboarding. Takes one address string; this is a read-only lookup and is safe to repeat.", idempotent: !0 },
  auth: chainalysisApiAuth,
  requireAuth: !0,
  props: {
    address: import_pieces_framework.Property.ShortText({
      displayName: "Address",
      description: "Address to check for sanctions",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let address = propsValue.address, apiKey = auth.secret_text;
    try {
      let response = await fetch(`https://public.chainalysis.com/api/v1/address/${address}`, {
        method: "GET",
        headers: {
          "X-API-Key": apiKey,
          Accept: "application/json"
        }
      });
      if (!response.ok)
        throw new Error(`Error: ${response.status} ${response.statusText}`);
      return await response.json();
    } catch (error) {
      throw console.error("[chainalysis-api-checkAddressSanction] Error checking address sanction:", error), error;
    }
  }
});

// ../ap/packages/pieces/community/chainalysis-api/src/index.ts
var chainalysisApiAuth = import_pieces_framework2.PieceAuth.SecretText({
  displayName: "API Key",
  description: "API Key for Chainalysis Screening API",
  required: !0
}), chainalysisApi = (0, import_pieces_framework2.createPiece)({
  displayName: "Chainalysis Screening API",
  description: "Chainalysis Screening API allows you to check if a blockchain address is sanctioned.",
  auth: chainalysisApiAuth,
  minimumSupportedRelease: "0.20.0",
  categories: [],
  logoUrl: "https://cdn.activepieces.com/pieces/chainalysis-api.jpg",
  authors: ["ahmad-swanblocks"],
  actions: [checkAddressSanction],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  chainalysisApi,
  chainalysisApiAuth
});
