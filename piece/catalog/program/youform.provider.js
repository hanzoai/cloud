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

// ../ap/packages/pieces/community/youform/src/index.ts
var index_exports = {};
__export(index_exports, {
  youform: () => youform
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/youform/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/youform/src/lib/common/constants.ts
var BASE_URL = "https://app.youform.com/api";

// ../ap/packages/pieces/community/youform/src/lib/common/auth.ts
var youformAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Token",
  description: "You can obtain API token from [Account Settings](https://app.youform.com/account).",
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: BASE_URL + "/me",
        authentication: {
          type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
          token: auth
        }
      }), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API Token"
      };
    }
  }
});

// ../ap/packages/pieces/community/youform/src/index.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/youform/src/lib/triggers/new-form-submission.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/youform/src/lib/common/props.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var formIdDropdown = import_pieces_framework2.Property.Dropdown({
  auth: youformAuth,
  displayName: "Form",
  refreshers: [],
  required: !0,
  options: async ({ auth }) => auth ? {
    disabled: !1,
    options: (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.GET,
      url: BASE_URL + "/forms",
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.secret_text
      }
    })).body.data.data.map((form) => ({
      label: form.name,
      value: form.slug
    }))
  } : {
    placeholder: "Please connect your account first.",
    options: [],
    disabled: !1
  }
});

// ../ap/packages/pieces/community/youform/src/lib/triggers/new-form-submission.ts
var import_pieces_common3 = require("@activepieces/pieces-common");
var import_pieces_framework4 = require("@activepieces/pieces-framework"), TRIGGER_KEY = "youform-new-submission-trigger", newSubmissionTrigger = (0, import_pieces_framework3.createTrigger)({
  name: "new-submission",
  auth: youformAuth,
  displayName: "New Submission",
  description: "Triggers When a new submission is recieved.",
  aiMetadata: {
    description: "Fires when a respondent submits the selected Youform form. The event represents a single completed form submission and emits its full payload, including the submitted answers and submission metadata."
  },
  type: import_pieces_framework3.TriggerStrategy.WEBHOOK,
  props: {
    formId: formIdDropdown
  },
  async onEnable(context) {
    let { formId } = context.propsValue, webhookId = (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: BASE_URL + "/webhooks",
      authentication: {
        type: import_pieces_common3.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      },
      body: {},
      queryParams: {
        form_id: formId.toString(),
        webhook_url: context.webhookUrl
      }
    })).body.data.id;
    await context.store.put(TRIGGER_KEY, webhookId);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(TRIGGER_KEY);
    (0, import_pieces_framework4.isNil)(webhookId) || await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.DELETE,
      url: BASE_URL + `/webhooks/${webhookId}`,
      authentication: {
        type: import_pieces_common3.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      }
    });
  },
  async run(context) {
    return [context.payload.body];
  },
  async test(context) {
    return [context.payload.body];
  },
  sampleData: void 0
});

// ../ap/packages/pieces/community/youform/src/index.ts
var youform = (0, import_pieces_framework5.createPiece)({
  displayName: "Youform",
  auth: youformAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/youform.png",
  authors: ["kishanprmr"],
  categories: [import_pieces_framework6.PieceCategory.FORMS_AND_SURVEYS],
  actions: [],
  triggers: [newSubmissionTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  youform
});
