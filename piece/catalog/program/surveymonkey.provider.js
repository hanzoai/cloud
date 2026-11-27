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

// ../ap/packages/pieces/community/surveymonkey/src/index.ts
var index_exports = {};
__export(index_exports, {
  smAuth: () => smAuth,
  surveymonkey: () => surveymonkey
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/surveymonkey/src/lib/triggers/new-response.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/surveymonkey/src/lib/common/index.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var smCommon = {
  baseUrl: "https://api.surveymonkey.com/v3",
  survey: import_pieces_framework.Property.Dropdown({
    auth: smAuth,
    displayName: "Survey",
    required: !0,
    refreshers: [],
    options: async (context) => {
      if (!context.auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "Connect your account"
        };
      let authProp = context.auth;
      return {
        options: await smCommon.getSurveys(authProp.access_token),
        placeholder: "Choose survey to connect"
      };
    }
  }),
  getSurveys: async (accessToken) => {
    let request = {
      method: import_pieces_common.HttpMethod.GET,
      url: `${smCommon.baseUrl}/surveys`,
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: accessToken
      }
    };
    return (await import_pieces_common.httpClient.sendRequest(request)).body.data.map((survey) => ({
      label: survey.title,
      value: survey.id
    }));
  },
  subscribeWebhook: async (surveyId, webhookUrl, accessToken) => {
    let request = {
      method: import_pieces_common.HttpMethod.POST,
      url: `${smCommon.baseUrl}/webhooks`,
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: accessToken
      },
      body: {
        name: "New Response Webhook",
        event_type: "response_created",
        object_type: "survey",
        object_ids: [surveyId],
        subscription_url: webhookUrl
      }
    };
    return (await import_pieces_common.httpClient.sendRequest(request)).body.id;
  },
  unsubscribeWebhook: async (webhookId, accessToken) => {
    let request = {
      method: import_pieces_common.HttpMethod.DELETE,
      url: `${smCommon.baseUrl}/webhooks/${webhookId}`,
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: accessToken
      }
    };
    return await import_pieces_common.httpClient.sendRequest(request);
  },
  async getResponseDetails(accessToken, surveyId, responseId) {
    let request = {
      method: import_pieces_common.HttpMethod.GET,
      url: `${smCommon.baseUrl}/surveys/${surveyId}/responses/${responseId}/details`,
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: accessToken
      }
    };
    return (await import_pieces_common.httpClient.sendRequest(request)).body;
  }
};

// ../ap/packages/pieces/community/surveymonkey/src/lib/triggers/new-response.ts
var newResponse = (0, import_pieces_framework2.createTrigger)({
  auth: smAuth,
  name: "new_response",
  displayName: "New Response",
  description: "Triggers when a new response is submitted",
  aiMetadata: {
    description: "Fires when a respondent submits a new response to the selected SurveyMonkey survey. Emits the full submitted response, including the survey identifier, response ID, and the respondent answers, fetched from the SurveyMonkey API."
  },
  type: import_pieces_framework2.TriggerStrategy.WEBHOOK,
  sampleData: {},
  props: {
    survey: smCommon.survey
  },
  //Create the webhook in SurveyMonkey and save the webhook ID in store for disable behavior
  async onEnable(context) {
    var _a;
    let webhookId = await smCommon.subscribeWebhook(
      context.propsValue.survey,
      context.webhookUrl,
      context.auth.access_token
    );
    await ((_a = context.store) == null ? void 0 : _a.put("_new_response_trigger", {
      webhookId
    }));
  },
  //Delete the webhook from SurveyMonkey
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get("_new_response_trigger"));
    response != null && await smCommon.unsubscribeWebhook(
      response.webhookId,
      context.auth.access_token
    );
  },
  //Return new response
  async run(context) {
    let payloadBody = context.payload.body;
    return [await smCommon.getResponseDetails(
      context.auth.access_token,
      context.propsValue.survey,
      payloadBody.object_id
    )];
  }
});

// ../ap/packages/pieces/community/surveymonkey/src/index.ts
var smAuth = import_pieces_framework3.PieceAuth.OAuth2({
  authUrl: "https://api.surveymonkey.com/oauth/authorize",
  tokenUrl: "https://api.surveymonkey.com/oauth/token",
  required: !0,
  scope: [
    "responses_read",
    "responses_read_detail",
    "webhooks_read",
    "webhooks_write",
    "surveys_read"
  ]
}), surveymonkey = (0, import_pieces_framework3.createPiece)({
  displayName: "SurveyMonkey",
  description: "Receive survey responses from SurveyMonkey",
  auth: smAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/surveymonkey.png",
  categories: [import_pieces_framework4.PieceCategory.FORMS_AND_SURVEYS],
  authors: ["kishanprmr", "MoShizzle", "khaledmashaly", "abuaboud"],
  actions: [
    (0, import_pieces_common2.createCustomApiCallAction)({
      baseUrl: () => "https://api.surveymonkey.com/v3",
      auth: smAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.access_token}`
      })
    })
  ],
  triggers: [newResponse]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  smAuth,
  surveymonkey
});
