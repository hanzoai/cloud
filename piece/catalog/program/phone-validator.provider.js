"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames;
var __hasOwnProp = Object.prototype.hasOwnProperty;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value;
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
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key != "symbol" ? key + "" : key, value);

// ../ap/packages/pieces/community/phone-validator/src/index.ts
var index_exports = {};
__export(index_exports, {
  phoneValidator: () => phoneValidator
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/phone-validator/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), phoneValidatorAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `
To get your API Key:

1. Sign up and create an account at Phone Validator
2. Visit your account information to obtain your API key
3. Copy your API key and paste it here
`,
  required: !0
});

// ../ap/packages/pieces/community/phone-validator/src/lib/actions/validate-phone.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/phone-validator/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
var BASE_URL = "https://api.phonevalidator.com", PhoneValidatorClient = class {
  constructor(apiKey) {
    __publicField(this, "apiKey");
    this.apiKey = apiKey;
  }
  async validatePhone(phone, type = "basic") {
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: `${BASE_URL}/api/v3/phonesearch`,
      queryParams: {
        apikey: this.apiKey,
        phone,
        type
      }
    })).body;
  }
};

// ../ap/packages/pieces/community/phone-validator/src/lib/actions/validate-phone.ts
var validatePhone = (0, import_pieces_framework2.createAction)({
  auth: phoneValidatorAuth,
  name: "validatePhone",
  displayName: "Validate Phone Number",
  description: "Validate a phone number and retrieve line type, carrier, and location information",
  audience: "both",
  aiMetadata: { description: 'Looks up a phone number via the PhoneValidator API to determine whether it is valid and real. Choose the search type: "basic" returns line type, carrier, location, and a fake-number check, while "fake" runs only the fake/disposable-number check. Use it to verify or enrich a phone number before contacting it; the number can be passed in various formats (e.g. 7029270000 or 1-702-927-0000). This is a read-only lookup, safe to repeat.', idempotent: !0 },
  props: {
    phoneNumber: import_pieces_framework2.Property.ShortText({
      displayName: "Phone Number",
      description: "The phone number to validate (e.g., 7029270000, 1-702-927-0000)",
      required: !0
    }),
    searchType: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Search Type",
      description: "Type of phone lookup to perform",
      required: !0,
      options: {
        options: [
          {
            label: "Basic (Line Type + Carrier + Location + Fake Check)",
            value: "basic"
          },
          {
            label: "Fake Number Check Only",
            value: "fake"
          }
        ]
      },
      defaultValue: "basic"
    })
  },
  async run({ auth, propsValue }) {
    let response = await new PhoneValidatorClient(auth.secret_text).validatePhone(propsValue.phoneNumber, propsValue.searchType);
    if (response.StatusCode === "200") {
      let result = {
        phoneNumber: response.PhoneNumber,
        cost: response.Cost,
        searchDate: response.SearchDate,
        statusCode: response.StatusCode,
        statusMessage: response.StatusMessage
      };
      return response.PhoneBasic && (result.phoneBasic = {
        phoneNumber: response.PhoneBasic.PhoneNumber,
        reportDate: response.PhoneBasic.ReportDate,
        lineType: response.PhoneBasic.LineType,
        phoneCompany: response.PhoneBasic.PhoneCompany,
        phoneLocation: response.PhoneBasic.PhoneLocation,
        fakeNumber: response.PhoneBasic.FakeNumber,
        fakeNumberReason: response.PhoneBasic.FakeNumberReason || response.PhoneBasic.FakeReason || "",
        errorCode: response.PhoneBasic.ErrorCode,
        errorDescription: response.PhoneBasic.ErrorDescription
      }), result;
    }
    return response;
  }
});

// ../ap/packages/pieces/community/phone-validator/src/index.ts
var phoneValidator = (0, import_pieces_framework3.createPiece)({
  displayName: "Phone Validator",
  description: "Validate phone numbers and retrieve line type, carrier, and location information",
  auth: phoneValidatorAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/phone-validator.png",
  categories: [import_pieces_framework4.PieceCategory.COMMUNICATION],
  authors: ["onyedikachi-david"],
  actions: [validatePhone],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  phoneValidator
});
