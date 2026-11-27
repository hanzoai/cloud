"use strict";
var __defProp = Object.defineProperty, __defProps = Object.defineProperties, __getOwnPropDesc = Object.getOwnPropertyDescriptor, __getOwnPropDescs = Object.getOwnPropertyDescriptors, __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
}, __spreadProps = (a, b) => __defProps(a, __getOwnPropDescs(b));
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

// ../ap/packages/pieces/community/jotform/src/index.ts
var index_exports = {};
__export(index_exports, {
  jotform: () => jotform,
  jotformAuth: () => jotformAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/jotform/src/lib/common/index.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var jotformCommon = {
  baseUrl: (region) => region === "eu" ? "https://eu-api.jotform.com" : region === "hipaa" ? "https://hipaa-api.jotform.com" : "https://api.jotform.com",
  form: import_pieces_framework.Property.Dropdown({
    auth: jotformAuth,
    displayName: "Form",
    required: !0,
    refreshers: [],
    options: async ({ auth }) => {
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "Enter API Key"
        };
      let authProp = auth.props;
      return {
        options: await jotformCommon.getUserForms(
          authProp.apiKey,
          authProp.region
        ),
        placeholder: "Choose form to connect"
      };
    }
  }),
  getUserForms: async (apiKey, region) => {
    let request = {
      method: import_pieces_common.HttpMethod.GET,
      url: `${jotformCommon.baseUrl(region)}/user/forms`,
      headers: {
        APIKEY: apiKey
      }
    };
    return (await import_pieces_common.httpClient.sendRequest(request)).body.content.map((form) => ({
      label: form.title,
      value: form.id
    }));
  },
  subscribeWebhook: async (formId, webhookUrl, authentication) => {
    let request = {
      method: import_pieces_common.HttpMethod.POST,
      url: `${jotformCommon.baseUrl(
        authentication.props.region
      )}/form/${formId}/webhooks`,
      headers: {
        APIKEY: authentication.props.apiKey,
        "Content-Type": "multipart/form-data"
      },
      body: {
        webhookURL: webhookUrl
      }
    };
    await import_pieces_common.httpClient.sendRequest(request);
  },
  unsubscribeWebhook: async (formId, webhookUrl, authentication) => {
    let getWebhooksRequest = {
      method: import_pieces_common.HttpMethod.GET,
      url: `${jotformCommon.baseUrl(
        authentication.props.region
      )}/form/${formId}/webhooks`,
      headers: {
        APIKEY: authentication.props.apiKey
      }
    }, response = await import_pieces_common.httpClient.sendRequest(getWebhooksRequest), webhookId;
    Object.entries(response.body.content).forEach(([key, value]) => {
      value == webhookUrl && (webhookId = key);
    });
    let request = {
      method: import_pieces_common.HttpMethod.DELETE,
      url: `${jotformCommon.baseUrl(
        authentication.props.region
      )}/form/${formId}/webhooks/${webhookId}`,
      headers: {
        APIKEY: authentication.props.apiKey
      }
    };
    return await import_pieces_common.httpClient.sendRequest(request);
  }
};

// ../ap/packages/pieces/community/jotform/src/lib/triggers/new-submission.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var newSubmission = (0, import_pieces_framework2.createTrigger)({
  auth: jotformAuth,
  name: "new_submission",
  displayName: "New Submission",
  description: "Triggers when someone submits a response to your form. Each form field is returned as a separate field using the question label as the key.",
  aiMetadata: {
    description: "Fires in real time (via webhook) when a respondent submits the selected JotForm form. The event represents one completed form submission and returns submission metadata (form ID, submission ID, submitter IP, submission type) along with each answered question flattened as its own field, keyed by a snake_cased version of the question label."
  },
  type: import_pieces_framework2.TriggerStrategy.WEBHOOK,
  sampleData: {
    form_id: "31751954731962",
    submission_id: "237955080346633702",
    ip: "123.123.123.123",
    type: "WEB",
    pretty: `Name: Bart Simpson
Your Message: \xA1Ay, caramba!`,
    name: "Bart Simpson",
    your_message: "\xA1Ay, caramba!"
  },
  props: {
    formId: jotformCommon.form
  },
  async onEnable(context) {
    var _a;
    await jotformCommon.subscribeWebhook(
      context.propsValue.formId,
      context.webhookUrl,
      context.auth
    ), await ((_a = context.store) == null ? void 0 : _a.put(
      "_new_jotform_submission_trigger",
      { jotformWebhook: context.webhookUrl }
    ));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(
      "_new_jotform_submission_trigger"
    ));
    response != null && await jotformCommon.unsubscribeWebhook(
      context.propsValue.formId,
      response.jotformWebhook,
      context.auth
    );
  },
  async run(context) {
    var _a, _b, _c, _d, _e;
    let body = context.payload.body, answers = parseAnswers(body.rawRequest);
    return [
      __spreadProps(__spreadValues({}, answers), {
        form_id: (_a = body.formID) != null ? _a : null,
        submission_id: (_b = body.submissionID) != null ? _b : null,
        ip: (_c = body.ip) != null ? _c : null,
        type: (_d = body.type) != null ? _d : null,
        pretty: (_e = body.pretty) != null ? _e : null
      })
    ];
  }
});
function parseAnswers(rawRequest) {
  let parsed = null;
  if (typeof rawRequest == "string")
    try {
      parsed = JSON.parse(rawRequest);
    } catch (e) {
      return {};
    }
  else rawRequest !== null && typeof rawRequest == "object" && (parsed = rawRequest);
  if (!parsed) return {};
  let result = {};
  for (let field of Object.values(parsed)) {
    if (!field.text) continue;
    let key = toSnakeCase(field.text);
    if (!key) continue;
    let { answer, prettyFormat } = field;
    prettyFormat ? result[key] = prettyFormat : typeof answer == "string" || typeof answer == "number" || typeof answer == "boolean" ? result[key] = answer : Array.isArray(answer) ? result[key] = answer.join(", ") : answer != null ? result[key] = Object.values(answer).filter(Boolean).join(" ") : result[key] = null;
  }
  return result;
}
function toSnakeCase(str) {
  return str.toLowerCase().replace(/[^a-z0-9]+/g, "_").replace(/^_+|_+$/g, "");
}

// ../ap/packages/pieces/community/jotform/src/index.ts
var markdownDescription = `
To obtain api key, follow the steps below:
1. Go to Settings -> API
2. Click on "Create New Key" button
3. Change the permissions to "Full Access"
4. Copy the API Key and paste it in the API Key field
`, jotformAuth = import_pieces_framework3.PieceAuth.CustomAuth({
  required: !0,
  description: markdownDescription,
  props: {
    apiKey: import_pieces_framework3.PieceAuth.SecretText({
      displayName: "API Key",
      required: !0
    }),
    region: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Region",
      required: !0,
      options: {
        options: [
          {
            label: "US (api.jotform.com)",
            value: "us"
          },
          {
            label: "EU (eu-api.jotform.com)",
            value: "eu"
          },
          {
            label: "HIPAA (hipaa-api.jotform.com)",
            value: "hipaa"
          }
        ]
      }
    })
  }
}), jotform = (0, import_pieces_framework3.createPiece)({
  displayName: "Jotform",
  description: "Create online forms and surveys",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/jotform.svg",
  categories: [import_pieces_framework4.PieceCategory.FORMS_AND_SURVEYS],
  authors: ["kishanprmr", "MoShizzle", "khaledmashaly", "abuaboud", "PFernandez98"],
  auth: jotformAuth,
  actions: [
    (0, import_pieces_common2.createCustomApiCallAction)({
      baseUrl: (auth) => auth ? jotformCommon.baseUrl(auth.props.region) : "",
      auth: jotformAuth,
      authMapping: async (auth) => ({
        APIKEY: auth.props.apiKey
      })
    })
  ],
  triggers: [newSubmission]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  jotform,
  jotformAuth
});
