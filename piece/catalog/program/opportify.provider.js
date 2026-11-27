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

// ../ap/packages/pieces/community/opportify/src/index.ts
var index_exports = {};
__export(index_exports, {
  opportify: () => opportify
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/opportify/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), opportifyAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "You can obtain your API key from [API Key List](https://app.opportify.ai/api-keys/list)."
});

// ../ap/packages/pieces/community/opportify/src/lib/actions/analyze-email.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var analyzeEmailAction = (0, import_pieces_framework2.createAction)({
  displayName: "Analyze Email",
  name: "analyze-email",
  description: "Validates an email address and returns its deliverability status.",
  audience: "both",
  aiMetadata: {
    description: "Validates a single email address via the Opportify API and reports its deliverability, with optional auto-correction of likely typos and optional AI-assisted analysis. Use when an agent needs to verify whether an email is valid or deliverable before sending or storing it. Requires the email string; this is a read-only lookup and is safe to repeat.",
    idempotent: !0
  },
  auth: opportifyAuth,
  props: {
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email",
      required: !0
    }),
    enableAutoCorrection: import_pieces_framework2.Property.Checkbox({
      displayName: "Enable Auto Correction?",
      required: !1
    }),
    enableAI: import_pieces_framework2.Property.Checkbox({
      displayName: "Enable AI?",
      required: !1
    })
  },
  async run(context) {
    let { email, enableAI, enableAutoCorrection } = context.propsValue;
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://api.opportify.ai/insights/v1/email/analyze",
      headers: {
        "x-opportify-token": context.auth.secret_text
      },
      body: {
        email,
        enableAI,
        enableAutoCorrection
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/opportify/src/lib/actions/analyze-ip-address.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common2 = require("@activepieces/pieces-common"), analyzeIpAddressAction = (0, import_pieces_framework3.createAction)({
  displayName: "Analyze IP Address",
  name: "analyze-ip-address",
  description: "Provides comprehensive insights into a specified IP address.",
  audience: "both",
  aiMetadata: {
    description: "Looks up a single IP address via the Opportify API and returns insights about it (such as geolocation, network, and risk signals), with optional AI-assisted analysis. Use when an agent needs to enrich, geolocate, or assess the risk of an IP. Requires the IP address string; this is a read-only lookup and is safe to repeat.",
    idempotent: !0
  },
  auth: opportifyAuth,
  props: {
    ip: import_pieces_framework3.Property.ShortText({
      displayName: "Ip Address",
      required: !0
    }),
    enableAI: import_pieces_framework3.Property.Checkbox({
      displayName: "Enable AI?",
      required: !1
    })
  },
  async run(context) {
    let { ip, enableAI } = context.propsValue;
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://api.opportify.ai/insights/v1/ip/analyze",
      headers: {
        "x-opportify-token": context.auth.secret_text
      },
      body: {
        ip,
        enableAI
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/opportify/src/index.ts
var opportify = (0, import_pieces_framework4.createPiece)({
  displayName: "Opportify",
  auth: opportifyAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/opportify.png",
  authors: [],
  actions: [analyzeEmailAction, analyzeIpAddressAction],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  opportify
});
