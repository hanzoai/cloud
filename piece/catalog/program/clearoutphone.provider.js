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

// ../ap/packages/pieces/community/clearoutphone/src/index.ts
var index_exports = {};
__export(index_exports, {
  clearoutphone: () => clearoutphone
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/clearoutphone/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), clearoutphoneAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `ClearoutPhone API Key

## Generating an API Token

Once signed up and logged in, click on **Dashboard -> API** tab available on left sidebar and then click on **"Generate API Token"**. 
.`,
  required: !0
});

// ../ap/packages/pieces/community/clearoutphone/src/lib/actions/find-phone-number-carrier.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var findPhoneNumberCarrier = (0, import_pieces_framework2.createAction)({
  auth: clearoutphoneAuth,
  name: "findPhoneNumberCarrier",
  classification: "READ",
  displayName: "Find Phone Number Carrier",
  description: "Find the carrier of a phone number using ClearoutPhone API",
  audience: "both",
  aiMetadata: { description: "Look up the telecom carrier of a single phone number via ClearoutPhone, also returning line type and country details. Use when an agent needs to identify which provider owns a number. Requires the number in international format (e.g. +447766733573); this is a read-only lookup and is idempotent.", idempotent: !0 },
  props: {
    phoneNumber: import_pieces_framework2.Property.ShortText({
      displayName: "Phone Number",
      description: "The phone number to validate (e.g., +447766733573 )",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    var _a;
    let headers = {
      Authorization: `Bearer:${auth.secret_text}`,
      "Content-Type": "application/json"
    }, body = {
      number: propsValue.phoneNumber
    }, result = (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://api.clearoutphone.io/v1/phonenumber/validate",
      headers,
      body
    })).body;
    return result.status === "success" && ((_a = result.data) != null && _a.carrier) ? {
      carrier: result.data.carrier,
      lineType: result.data.line_type,
      location: result.data.location,
      countryName: result.data.country_name,
      countryCode: result.data.country_code,
      status: result.data.status
    } : {
      carrier: null,
      response: result
    };
  }
});

// ../ap/packages/pieces/community/clearoutphone/src/lib/actions/find-phone-number-is-mobile.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var findPhoneNumberIsMobile = (0, import_pieces_framework3.createAction)({
  auth: clearoutphoneAuth,
  name: "findPhoneNumberIsMobile",
  classification: "READ",
  displayName: "Find Phone Number Is Mobile",
  description: "Determine if a phone number is a mobile number using ClearoutPhone API",
  audience: "both",
  aiMetadata: { description: "Check whether a single phone number is a mobile (vs. landline or other) line type via ClearoutPhone. Use when an agent needs to decide if a number can receive SMS or routing depends on mobile vs. fixed lines. Requires the number in international format (e.g. +447766733573); this is a read-only lookup and is idempotent.", idempotent: !0 },
  props: {
    phoneNumber: import_pieces_framework3.Property.ShortText({
      displayName: "Phone Number",
      description: "The phone number to validate (e.g., +447766733573 )",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    var _a;
    let headers = {
      Authorization: `Bearer:${auth.secret_text}`,
      "Content-Type": "application/json"
    }, body = {
      number: propsValue.phoneNumber
    }, result = (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://api.clearoutphone.io/v1/phonenumber/validate",
      headers,
      body
    })).body;
    return result.status === "success" && ((_a = result.data) != null && _a.line_type) ? {
      isMobile: result.data.line_type.toUpperCase() === "MOBILE",
      lineType: result.data.line_type,
      carrier: result.data.carrier,
      location: result.data.location,
      countryName: result.data.country_name,
      countryCode: result.data.country_code,
      status: result.data.status
    } : {
      isMobile: !1,
      response: result
    };
  }
});

// ../ap/packages/pieces/community/clearoutphone/src/lib/actions/validate-phone-number.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var validatePhoneNumber = (0, import_pieces_framework4.createAction)({
  auth: clearoutphoneAuth,
  name: "validatePhoneNumber",
  classification: "READ",
  displayName: "Validate Phone Number",
  description: "Validate a phone number and retrieve comprehensive information using ClearoutPhone API",
  audience: "both",
  aiMetadata: { description: "Validate a single phone number via ClearoutPhone and retrieve comprehensive details including validity, line type, carrier, location, country, timezone, and formatted representations (international, local, E.164). Use this as the broadest lookup when an agent needs full number metadata rather than just carrier or mobile status. Requires the number in international format (e.g. +447766733573); this is a read-only lookup and is idempotent.", idempotent: !0 },
  props: {
    phoneNumber: import_pieces_framework4.Property.ShortText({
      displayName: "Phone Number",
      description: "The phone number to validate (e.g., +447766733573 )",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let headers = {
      Authorization: `Bearer:${auth.secret_text}`,
      "Content-Type": "application/json"
    }, body = {
      number: propsValue.phoneNumber
    }, result = (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: "https://api.clearoutphone.io/v1/phonenumber/validate",
      headers,
      body
    })).body;
    return result.status === "success" && result.data ? {
      isValid: result.data.status === "valid",
      status: result.data.status,
      lineType: result.data.line_type,
      carrier: result.data.carrier,
      location: result.data.location,
      countryName: result.data.country_name,
      countryTimezone: result.data.country_timezone,
      countryUtcOffset: result.data.country_utcoffset,
      countryDstObservedHours: result.data.country_dstobservedhrs,
      countryCode: result.data.country_code,
      internationalFormat: result.data.international_format,
      localFormat: result.data.local_format,
      e164Format: result.data.e164_format,
      canBeInternationallyDialled: result.data.can_be_internationally_dialled,
      validatedOn: result.data.validated_on,
      timeTaken: result.data.time_taken
    } : result;
  }
});

// ../ap/packages/pieces/community/clearoutphone/src/index.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), clearoutphone = (0, import_pieces_framework5.createPiece)({
  displayName: "ClearoutPhone",
  auth: clearoutphoneAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/clearoutphone.png",
  authors: ["sanket-a11y"],
  categories: [import_pieces_framework6.PieceCategory.COMMUNICATION],
  description: "ClearoutPhone is a phone number validation and verification service that helps businesses ensure the accuracy and validity of their phone number data.",
  actions: [
    findPhoneNumberCarrier,
    findPhoneNumberIsMobile,
    validatePhoneNumber
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  clearoutphone
});
