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

// ../ap/packages/pieces/community/frame/src/index.ts
var index_exports = {};
__export(index_exports, {
  frame: () => frame,
  frameAuth: () => frameAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/frame/src/lib/triggers/register-trigger.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var frameRegisterTrigger = ({
  name,
  displayName,
  eventType,
  description,
  aiMetadata,
  sampleData
}) => (0, import_pieces_framework.createTrigger)({
  auth: frameAuth,
  name: `frame_trigger_${name}`,
  displayName,
  description,
  aiMetadata: aiMetadata != null ? aiMetadata : {
    description: `Fires via a Frame.io webhook whenever the "${eventType}" event occurs on the selected team. Each event delivers the matching Frame.io resource (project, asset, comment, etc.) so an agent can react to changes in a Frame.io account in real time.`
  },
  props: {
    account_id: import_pieces_framework.Property.Dropdown({
      displayName: "Account",
      description: "Accounts accessible via a given User",
      required: !0,
      refreshers: [],
      auth: frameAuth,
      options: async ({ auth }) => {
        if (!auth)
          return {
            options: [],
            disabled: !0,
            placeholder: "Please authenticate first"
          };
        let response = await import_pieces_common.httpClient.sendRequest({
          method: import_pieces_common.HttpMethod.GET,
          url: "https://api.frame.io/v2/accounts",
          authentication: {
            type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
            token: auth.secret_text
          },
          queryParams: {}
        });
        try {
          return {
            disabled: !1,
            options: response.body.map((account) => ({
              label: account.display_name,
              value: account.id
            }))
          };
        } catch (error) {
          return {
            options: [],
            disabled: !0,
            placeholder: `Couldn't load Accounts:
${error}`
          };
        }
      }
    }),
    team_id: import_pieces_framework.Property.Dropdown({
      displayName: "Team",
      description: "Teams accessible via a given Account",
      required: !0,
      refreshers: ["account_id"],
      auth: frameAuth,
      options: async ({ auth, account_id }) => {
        if (!auth)
          return {
            options: [],
            disabled: !0,
            placeholder: "Please authenticate first"
          };
        if (!account_id)
          return {
            options: [],
            disabled: !0,
            placeholder: "Please select an account first"
          };
        let response = await import_pieces_common.httpClient.sendRequest({
          method: import_pieces_common.HttpMethod.GET,
          url: `https://api.frame.io/v2/accounts/${account_id}/teams`,
          authentication: {
            type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
            token: auth.secret_text
          },
          queryParams: {}
        });
        try {
          return {
            disabled: !1,
            options: response.body.map((team) => ({
              label: team.name,
              value: team.id
            }))
          };
        } catch (error) {
          return {
            options: [],
            disabled: !0,
            placeholder: `Couldn't load Teams:
${error}`
          };
        }
      }
    })
  },
  sampleData,
  type: import_pieces_framework.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let response = await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: `https://api.frame.com/api/v2/teams/${context.propsValue.team_id}/hooks`,
      body: {
        name: displayName,
        url: context.webhookUrl,
        events: [eventType]
      },
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      }
    });
    await context.store.put(
      `frame_${name}_trigger`,
      response.body
    );
  },
  async onDisable(context) {
    let webhook = await context.store.get(
      `frame_${name}_trigger`
    );
    if (webhook != null) {
      let request = {
        method: import_pieces_common.HttpMethod.DELETE,
        url: `https://api.frame.io/v2/hooks/${webhook.id}`,
        authentication: {
          type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
          token: context.auth.secret_text
        }
      };
      await import_pieces_common.httpClient.sendRequest(request);
    }
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/frame/src/lib/triggers/index.ts
var triggers = [
  {
    name: "project_created",
    eventType: "project.created",
    displayName: "Project Created",
    description: "Triggered when a new project is created.",
    sampleData: {
      account_id: "449e7a5c-69d3-4b8a-aaaf-5c9b713ebc65",
      active: !0,
      app_id: "affd1d10-9538-4fc8-9e0b-4594a28c1335",
      deleted_at: "2019-08-24T14:15:22Z",
      events: ["project.created"],
      id: "497f6eca-6276-4993-bfeb-53cbbbba6f08",
      inserted_at: "2019-08-24T14:15:22Z",
      name: "string",
      project_id: "405d8375-3514-403b-8c43-83ae74cfe0e9",
      secret: "string",
      team_id: "810007d0-bec5-486c-b5d1-28fcd8a079ba",
      updated_at: "2019-08-24T14:15:22Z",
      url: "string"
    }
  },
  {
    name: "asset_created",
    eventType: "asset.created",
    displayName: "Asset Created",
    description: "Triggered when a new asset is created.",
    sampleData: {
      account_id: "449e7a5c-69d3-4b8a-aaaf-5c9b713ebc65",
      active: !0,
      app_id: "affd1d10-9538-4fc8-9e0b-4594a28c1335",
      deleted_at: "2019-08-24T14:15:22Z",
      events: ["asset.created"],
      id: "497f6eca-6276-4993-bfeb-53cbbbba6f08",
      inserted_at: "2019-08-24T14:15:22Z",
      name: "string",
      project_id: "405d8375-3514-403b-8c43-83ae74cfe0e9",
      secret: "string",
      team_id: "810007d0-bec5-486c-b5d1-28fcd8a079ba",
      updated_at: "2019-08-24T14:15:22Z",
      url: "string"
    }
  },
  {
    name: "comment_created",
    eventType: "comment.created",
    displayName: "Comment Created",
    description: "Triggered when a new comment is created.",
    sampleData: {
      account_id: "449e7a5c-69d3-4b8a-aaaf-5c9b713ebc65",
      active: !0,
      app_id: "affd1d10-9538-4fc8-9e0b-4594a28c1335",
      deleted_at: "2019-08-24T14:15:22Z",
      events: ["comment.created"],
      id: "497f6eca-6276-4993-bfeb-53cbbbba6f08",
      inserted_at: "2019-08-24T14:15:22Z",
      name: "string",
      project_id: "405d8375-3514-403b-8c43-83ae74cfe0e9",
      secret: "string",
      team_id: "810007d0-bec5-486c-b5d1-28fcd8a079ba",
      updated_at: "2019-08-24T14:15:22Z",
      url: "string"
    }
  }
].map((props) => frameRegisterTrigger(props));

// ../ap/packages/pieces/community/frame/src/index.ts
var markdownPropertyDescription = `
**Enable Basic Authentication:**

1. Go to https://developer.frame.io/app/tokens
2. Click on 'Create a Token'
3. Provide a Description for your use.
4. Depending on your usage, you may 'Select all scopes'
5. Submit the form
6. The token will be created. Copy the token and paste it in the 'Token' input
`, frameAuth = import_pieces_framework2.PieceAuth.SecretText({
  displayName: "Token",
  description: markdownPropertyDescription,
  required: !0
}), frame = (0, import_pieces_framework2.createPiece)({
  displayName: "Frame",
  description: "Collaborative workspace platform",
  auth: frameAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/frameio.png",
  categories: [import_pieces_framework3.PieceCategory.MARKETING],
  authors: ["kanarelo", "kishanprmr", "MoShizzle", "abuaboud"],
  actions: [
    (0, import_pieces_common2.createCustomApiCallAction)({
      baseUrl: () => "https://api.frame.io/v2",
      auth: frameAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  frame,
  frameAuth
});
