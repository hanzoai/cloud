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

// ../ap/packages/pieces/community/gender-api/src/index.ts
var index_exports = {};
__export(index_exports, {
  genderApi: () => genderApi
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/gender-api/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), genderApiAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "The API key for accessing the Gender-api service",
  required: !0
});

// ../ap/packages/pieces/community/gender-api/src/lib/actions/get-statistics.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common = require("@activepieces/pieces-common"), getStatistics = (0, import_pieces_framework2.createAction)({
  auth: genderApiAuth,
  name: "getStatistics",
  displayName: "Get Statistics",
  description: "Get account statistics including remaining credits and usage information",
  audience: "both",
  aiMetadata: {
    description: "Retrieves the Gender API account statistics, including remaining credits and usage information. Use to check quota before running batches of name lookups. Takes no input; read-only and safe to repeat.",
    idempotent: !0
  },
  props: {},
  async run(context) {
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: "https://gender-api.com/v2/statistic",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${context.auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/gender-api/src/lib/actions/get-gender-by-full-name.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common2 = require("@activepieces/pieces-common"), getGenderByFullName = (0, import_pieces_framework3.createAction)({
  auth: genderApiAuth,
  name: "getGenderByFullName",
  displayName: "Get Gender by Full Name",
  description: "Predict the gender of a person based on their full name",
  audience: "both",
  aiMetadata: {
    description: "Predicts the likely gender of a person from a full name (first and last) via the Gender API, which extracts the first name internally. Use when you have the complete name rather than only a given name; optionally pass a country code and/or locale to improve accuracy for region-specific names. Read-only lookup that is safe to repeat.",
    idempotent: !0
  },
  props: {
    full_name: import_pieces_framework3.Property.ShortText({
      displayName: "Full Name",
      description: "The full name (first and last name) to query",
      required: !0
    }),
    country_code: import_pieces_framework3.Property.ShortText({
      displayName: "Country Code",
      description: 'ISO 3166-1 alpha-2 country code to improve accuracy (e.g., "US", "DE")',
      required: !1
    }),
    locale: import_pieces_framework3.Property.ShortText({
      displayName: "Locale",
      description: 'Browser locale for localization (e.g., "en-US", "de-DE")',
      required: !1
    })
  },
  async run(context) {
    let payload = {
      full_name: context.propsValue.full_name
    };
    return context.propsValue.country_code && (payload.country_code = context.propsValue.country_code), context.propsValue.locale && (payload.locale = context.propsValue.locale), (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://gender-api.com/v2/gender",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${context.auth.secret_text}`
      },
      body: payload
    })).body;
  }
});

// ../ap/packages/pieces/community/gender-api/src/lib/actions/get-gender-by-first-name.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common"), getGenderByFirstName = (0, import_pieces_framework4.createAction)({
  auth: genderApiAuth,
  name: "getGenderByFirstName",
  displayName: "Get Gender by First Name",
  description: "Predict the gender of a person based on their first name",
  audience: "both",
  aiMetadata: {
    description: "Predicts the likely gender of a person from a single first name via the Gender API. Use when you have only a given name (not a full name); optionally pass a country code and/or locale to improve accuracy for region-specific names. Read-only lookup that is safe to repeat.",
    idempotent: !0
  },
  props: {
    first_name: import_pieces_framework4.Property.ShortText({
      displayName: "First Name",
      description: "The first name to query",
      required: !0
    }),
    country_code: import_pieces_framework4.Property.ShortText({
      displayName: "Country Code",
      description: 'ISO 3166-1 alpha-2 country code to improve accuracy (e.g., "US", "DE")',
      required: !1
    }),
    locale: import_pieces_framework4.Property.ShortText({
      displayName: "Locale",
      description: 'Browser locale for localization (e.g., "en-US", "de-DE")',
      required: !1
    })
  },
  async run(context) {
    let payload = {
      first_name: context.propsValue.first_name
    };
    return context.propsValue.country_code && (payload.country_code = context.propsValue.country_code), context.propsValue.locale && (payload.locale = context.propsValue.locale), (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: "https://gender-api.com/v2/gender",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${context.auth.secret_text}`
      },
      body: payload
    })).body;
  }
});

// ../ap/packages/pieces/community/gender-api/src/index.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), genderApi = (0, import_pieces_framework5.createPiece)({
  displayName: "Gender API",
  auth: genderApiAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/gender-api.png",
  authors: ["sanket-a11y"],
  description: "Predict the gender of a person based on their name using Gender-api service.",
  actions: [
    getGenderByFirstName,
    getGenderByFullName,
    getStatistics,
    (0, import_pieces_common4.createCustomApiCallAction)({
      auth: genderApiAuth,
      baseUrl: () => "https://gender-api.com/v2",
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  genderApi
});
