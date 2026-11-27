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

// ../ap/packages/pieces/community/fliqr-ai/src/index.ts
var index_exports = {};
__export(index_exports, {
  fliqrAi: () => fliqrAi
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/fliqr-ai/src/lib/actions/get-account-details.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/fliqr-ai/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/fliqr-ai/src/lib/common/models.ts
var fliqrConfig = {
  baseUrl: "https://app.fliqr.ai/api",
  accessTokenHeaderKey: "X-ACCESS-TOKEN"
};

// ../ap/packages/pieces/community/fliqr-ai/src/lib/auth.ts
var fliqrAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Fliqr API Access Token",
  required: !0,
  description: `
      To obtain your Fliqr API access token, follow these steps:

      1. Log in to your Fliqr account.
      2. Navigate to Fliqr API Access Token Settings.
      3. Under the Integrations section, find the Fliqr API Access Token.
      4. Click on Copy Token to copy your existing token or click on Generate Token to create a new one.
      5. Copy the token and paste it below in "Fliqr API Access Token".
    `,
  validate: async (auth) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${fliqrConfig.baseUrl}/accounts/me`,
        headers: {
          [fliqrConfig.accessTokenHeaderKey]: auth.auth
        }
      }), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid personal access token"
      };
    }
  }
});

// ../ap/packages/pieces/community/fliqr-ai/src/lib/actions/get-account-details.ts
var getFliqrAccountDetails = (0, import_pieces_framework2.createAction)({
  // auth: check https://www.activepieces.com/docs/developers/piece-reference/authentication,
  name: "get_fliqr_account_details",
  auth: fliqrAuth,
  displayName: "Get Business Account details",
  description: "Get basic account details of business",
  audience: "both",
  aiMetadata: { description: "Fetches the basic profile/details of the authenticated Fliqr AI business account (the account that owns the connected API token). Use to retrieve account identity or configuration before acting on the account. Read-only; repeating the call is safe.", idempotent: !0 },
  props: {},
  async run(context) {
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.GET,
      url: `${fliqrConfig.baseUrl}/accounts/me`,
      headers: {
        [fliqrConfig.accessTokenHeaderKey]: context.auth.secret_text
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/fliqr-ai/src/lib/actions/get-account-flows.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");
var getFliqrAccountFlows = (0, import_pieces_framework3.createAction)({
  // auth: check https://www.activepieces.com/docs/developers/piece-reference/authentication,
  name: "get_fliqr_account_flows",
  auth: fliqrAuth,
  displayName: "Get Account Flows",
  description: "Get all flows from the account",
  audience: "both",
  aiMetadata: { description: "Lists all chatbot flows configured on the authenticated Fliqr AI account. Use to discover the available flows (e.g. to find a flow id or confirm what automations exist) before referencing or triggering one. Read-only; repeating the call is safe.", idempotent: !0 },
  props: {},
  async run(context) {
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.GET,
      url: `${fliqrConfig.baseUrl}/accounts/flows`,
      headers: {
        [fliqrConfig.accessTokenHeaderKey]: context.auth.secret_text
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/fliqr-ai/src/index.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var fliqrAi = (0, import_pieces_framework4.createPiece)({
  displayName: "Fliqr AI",
  description: "Omnichannel AI chatbot enhancing customer interactions across WhatsApp, Facebook, Instagram, Telegram, and 6 other platforms.",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/fliqr-ai.png",
  authors: ["drona2938"],
  categories: [import_pieces_framework5.PieceCategory.COMMUNICATION, import_pieces_framework5.PieceCategory.CUSTOMER_SUPPORT, import_pieces_framework5.PieceCategory.MARKETING],
  auth: fliqrAuth,
  actions: [
    getFliqrAccountDetails,
    getFliqrAccountFlows,
    (0, import_pieces_common4.createCustomApiCallAction)({
      baseUrl: () => fliqrConfig.baseUrl,
      auth: fliqrAuth,
      authMapping: async (auth) => ({
        [fliqrConfig.accessTokenHeaderKey]: `${auth}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  fliqrAi
});
