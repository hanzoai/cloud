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

// ../ap/packages/pieces/community/wootric/src/index.ts
var index_exports = {};
__export(index_exports, {
  WOOTRIC_API_URL: () => WOOTRIC_API_URL2,
  WOOTRIC_IMAGE_URL: () => WOOTRIC_IMAGE_URL,
  wootric: () => wootric
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/wootric/src/lib/actions/create-survey.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/wootric/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_framework2 = require("@activepieces/pieces-framework"), WOOTRIC_API_URL = "https://api.wootric.com";
var wootricAuth = import_pieces_framework.PieceAuth.OAuth2({
  required: !0,
  grantType: import_pieces_framework2.OAuth2GrantType.CLIENT_CREDENTIALS,
  authUrl: "",
  tokenUrl: `${WOOTRIC_API_URL}/oauth/token`,
  scope: []
});

// ../ap/packages/pieces/community/wootric/src/lib/actions/create-survey.ts
var import_pieces_common = require("@activepieces/pieces-common"), sendSurvey = async (surveyRequestPayload) => {
  let EMAIL_SURVEY = `${WOOTRIC_API_URL}/v1/email_survey`;
  return await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.POST,
    url: EMAIL_SURVEY,
    body: surveyRequestPayload
  });
}, createWootricSurvey = (0, import_pieces_framework3.createAction)({
  name: "trigger_wootric_survey",
  auth: wootricAuth,
  displayName: "Trigger Wootric Survey",
  description: "Trigger a survey from Wootric",
  audience: "both",
  aiMetadata: {
    description: "Dispatches a Wootric (InMoment) NPS email survey to a list of end-user email addresses. Use this to send/send-out customer satisfaction surveys to specific recipients. Set survey-immediately to true to bypass Wootric eligibility/throttle checks and send right away, or false to respect them. Not idempotent: each call sends new survey emails, so repeating it re-sends to the same recipients.",
    idempotent: !1
  },
  props: {
    emails: import_pieces_framework3.Property.Array({
      displayName: "Emails",
      description: "End user emails, where you want the survey to be received",
      required: !0,
      defaultValue: []
    }),
    surveyImmediately: import_pieces_framework3.Property.Checkbox({
      displayName: "Survey Immediately",
      description: 'Enter "true" to survey immediately to bypass checks, otherwise "false"',
      required: !0
    })
  },
  async run(context) {
    let { surveyImmediately, emails } = context.propsValue, { access_token } = context.auth;
    return (await sendSurvey({
      emails,
      survey_immediately: surveyImmediately,
      access_token
    })).body;
  }
});

// ../ap/packages/pieces/community/wootric/src/index.ts
var WOOTRIC_API_URL2 = "https://api.wootric.com", WOOTRIC_IMAGE_URL = "https://assets-production.wootric.com/assets/wootric-is-now-inmoment-250x108-85cb4900c62ff4d33200abafee7d63372d410abc5bf0cab90e80a07d4f4e5a31.png", wootric = (0, import_pieces_framework4.createPiece)({
  displayName: "Wootric",
  description: "Measure and boost customer happiness",
  auth: wootricAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: WOOTRIC_IMAGE_URL,
  authors: ["abuaboud"],
  actions: [createWootricSurvey],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  WOOTRIC_API_URL,
  WOOTRIC_IMAGE_URL,
  wootric
});
