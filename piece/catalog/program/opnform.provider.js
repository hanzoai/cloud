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

// ../ap/packages/pieces/community/opnform/src/index.ts
var index_exports = {};
__export(index_exports, {
  opnform: () => opnform
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/opnform/src/lib/common/index.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/opnform/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");
var import_pieces_framework2 = require("@activepieces/pieces-framework"), opnformAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: "Please use your Opnform API Key. [Click here for create API Key](https://opnform.com/home?user-settings=access-tokens)",
  required: !0,
  props: {
    baseApiUrl: import_pieces_framework.Property.ShortText({
      displayName: "Base URL",
      description: "Default value is 'https://api.opnform.com'.",
      required: !1
    }),
    apiKey: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API Key",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    try {
      return await opnformCommon.validateAuth({
        props: auth,
        type: import_pieces_framework2.AppConnectionType.CUSTOM_AUTH
      }) ? { valid: !0 } : { valid: !1, error: "Invalid API Key" };
    } catch (e) {
      return { valid: !1, error: "Invalid API Key" };
    }
  }
});

// ../ap/packages/pieces/community/opnform/src/lib/common/index.ts
var API_URL_DEFAULT = "https://api.opnform.com", workspaceIdProp = import_pieces_framework3.Property.Dropdown({
  displayName: "Workspace",
  description: "Workspace Name",
  required: !0,
  refreshers: [],
  auth: opnformAuth,
  async options({ auth }) {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect Opnform account",
        options: []
      };
    let accessToken = auth.props.apiKey, options = [], request = {
      method: import_pieces_common.HttpMethod.GET,
      url: `${opnformCommon.getBaseUrl(auth)}/open/workspaces`,
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: accessToken
      }
    }, response = await import_pieces_common.httpClient.sendRequest(request);
    for (let workspace of response.body)
      options.push({ label: workspace.name, value: workspace.id });
    return {
      disabled: !1,
      placeholder: "Select workspace",
      options
    };
  }
}), formIdProp = import_pieces_framework3.Property.Dropdown({
  displayName: "Form",
  description: "Form Name",
  required: !0,
  refreshers: ["workspaceId"],
  auth: opnformAuth,
  async options({ auth, workspaceId }) {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect Opnform account",
        options: []
      };
    if (!workspaceId)
      return {
        disabled: !0,
        placeholder: "Select workspace",
        options: []
      };
    let accessToken = auth.props.apiKey, options = [], hasMore = !0, page = 1;
    do {
      let request = {
        method: import_pieces_common.HttpMethod.GET,
        url: `${opnformCommon.getBaseUrl(auth)}/open/workspaces/${workspaceId}/forms`,
        authentication: {
          type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
          token: accessToken
        },
        queryParams: {
          page: page.toString()
        }
      }, response = await import_pieces_common.httpClient.sendRequest(request);
      for (let form of response.body.data)
        options.push({ label: form.title, value: form.id });
      hasMore = response.body.meta != null && response.body.meta.current_page < response.body.meta.last_page, page++;
    } while (hasMore);
    return {
      disabled: !1,
      placeholder: "Select form",
      options
    };
  }
}), opnformCommon = {
  getBaseUrl: (auth) => auth.props.baseApiUrl || API_URL_DEFAULT,
  validateAuth: async (auth) => (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.GET,
    url: `${opnformCommon.getBaseUrl(auth)}/open/workspaces`,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token: auth.props.apiKey
    }
  })).status === 200,
  checkExistsIntegration: async (auth, formId, flowUrl) => (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.GET,
    url: `${opnformCommon.getBaseUrl(auth)}/open/forms/${formId}/integrations`,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token: auth.props.apiKey
    }
  })).body.find(
    (integration2) => {
      var _a;
      return integration2.integration_id === "activepieces" && ((_a = integration2.data) == null ? void 0 : _a.provider_url) === flowUrl;
    }
  ) || null,
  createIntegration: async (auth, formId, webhookUrl, flowUrl) => {
    let request = {
      method: import_pieces_common.HttpMethod.POST,
      url: `${opnformCommon.getBaseUrl(auth)}/open/forms/${formId}/integrations`,
      headers: {
        "Content-Type": "application/json"
      },
      body: {
        integration_id: "activepieces",
        status: "active",
        data: {
          webhook_url: webhookUrl,
          provider_url: flowUrl
        }
      },
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: auth.props.apiKey
      },
      queryParams: {}
    };
    return (await import_pieces_common.httpClient.sendRequest(request)).body.form_integration.id || null;
  },
  createOrUpdateIntegration: async (auth, formId, webhookUrl, flowUrl) => {
    var _a;
    let existingIntegration = await opnformCommon.checkExistsIntegration(
      auth,
      formId,
      flowUrl
    );
    if (existingIntegration) {
      if (((_a = existingIntegration.data) == null ? void 0 : _a.webhook_url) === webhookUrl)
        return existingIntegration.id;
      await opnformCommon.deleteIntegration(auth, formId, existingIntegration.id);
    }
    return await opnformCommon.createIntegration(auth, formId, webhookUrl, flowUrl);
  },
  deleteIntegration: async (auth, formId, integrationId) => {
    let request = {
      method: import_pieces_common.HttpMethod.DELETE,
      url: `${opnformCommon.getBaseUrl(
        auth
      )}/open/forms/${formId}/integrations/${integrationId}`,
      headers: {
        "Content-Type": "application/json"
      },
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: auth.props.apiKey
      }
    };
    return await import_pieces_common.httpClient.sendRequest(request);
  }
};

// ../ap/packages/pieces/community/opnform/src/lib/triggers/new-submission.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var opnformNewSubmission = (0, import_pieces_framework4.createTrigger)({
  auth: opnformAuth,
  name: "new_submission",
  displayName: "New Submission",
  description: "Triggers when Opnform receives a new submission.",
  aiMetadata: {
    description: "Fires whenever the specified OpnForm form receives a new submission (someone fills out and submits the form). The event payload contains the form title, slug, and the submitted answers keyed by field id, each with its value and field name. Use it to react to incoming form responses such as contact requests, signups, or survey answers."
  },
  props: {
    workspaceId: workspaceIdProp,
    formId: formIdProp
  },
  type: import_pieces_framework4.TriggerStrategy.WEBHOOK,
  sampleData: {
    form_title: "My Form",
    form_slug: "my-form-vuep24",
    data: {
      "6cc0dcf4-0ca8-43e4-b31a-b3f3413f859a": {
        value: "This is test",
        name: "Name"
      },
      "6e171bce-3eab-47a4-a289-20a0cb8ec693": {
        value: "abc@example.com",
        name: "Email"
      }
    }
  },
  async onEnable(context) {
    var _a;
    let formId = context.propsValue.formId, webhookUrl = context.webhookUrl;
    if (!formId)
      throw new Error("Form is required");
    let flowUrl = `${new URL(context.server.publicUrl).origin}/projects/${context.project.id}/flows/${context.flows.current.id}`, integrationId = await opnformCommon.createOrUpdateIntegration(
      context.auth,
      formId,
      webhookUrl,
      flowUrl
    );
    if (integrationId)
      await ((_a = context.store) == null ? void 0 : _a.put("_new_submission_trigger", {
        integrationId
      }));
    else
      throw new Error("Failed to create integration");
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get("_new_submission_trigger"));
    if (response != null && response.integrationId) {
      let formId = context.propsValue.formId;
      if (!formId)
        throw new Error("Form is required");
      await opnformCommon.deleteIntegration(context.auth, formId, response.integrationId);
    }
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/opnform/src/index.ts
var import_pieces_common2 = require("@activepieces/pieces-common");
var opnform = (0, import_pieces_framework5.createPiece)({
  displayName: "Opnform",
  description: "Create beautiful online forms and surveys with unlimited fields and submissions",
  auth: opnformAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/opnform.png",
  categories: [import_pieces_framework6.PieceCategory.FORMS_AND_SURVEYS],
  authors: ["JhumanJ", "chiragchhatrala"],
  actions: [
    (0, import_pieces_common2.createCustomApiCallAction)({
      auth: opnformAuth,
      baseUrl: (auth) => (auth == null ? void 0 : auth.props.baseApiUrl) || API_URL_DEFAULT,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.props.apiKey}`
      })
    })
  ],
  triggers: [opnformNewSubmission]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  opnform
});
