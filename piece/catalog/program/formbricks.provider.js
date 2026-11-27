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

// ../ap/packages/pieces/community/formbricks/src/index.ts
var index_exports = {};
__export(index_exports, {
  formBricksAuth: () => formBricksAuth,
  formbricks: () => formbricks
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/formbricks/src/lib/triggers/register.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var formBricksRegisterTrigger = ({
  name,
  displayName,
  eventType,
  description,
  aiMetadata,
  sampleData
}) => (0, import_pieces_framework.createTrigger)({
  auth: formBricksAuth,
  name: `formbricks_trigger_${name}`,
  displayName,
  description,
  aiMetadata,
  props: {
    workspaceId: import_pieces_framework.Property.ShortText({
      displayName: "Workspace ID",
      description: "The Workspace ID can be found in the URL when you are logged into Formbricks. For example, if the URL is https://app.formbricks.com/workspaces/cljold01t0000qh8ewzigzmjk, then the Workspace ID is cljold01t0000qh8ewzigzmjk.",
      required: !0
    }),
    survey_id: import_pieces_framework.Property.MultiSelectDropdown({
      auth: formBricksAuth,
      displayName: "Survey",
      description: "A selection of surveys that will trigger. Else, all surveys will trigger.",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            options: [],
            disabled: !0,
            placeholder: "Please authenticate first"
          };
        let authValue = auth, response = await import_pieces_common.httpClient.sendRequest({
          method: import_pieces_common.HttpMethod.GET,
          url: `${auth.props.appUrl}/api/v1/management/surveys`,
          headers: {
            "x-api-key": auth.props.apiKey
          }
        });
        try {
          return {
            disabled: !1,
            options: response.body.data.map((survey) => ({
              label: survey.name,
              value: survey.id
            }))
          };
        } catch (error) {
          return {
            options: [],
            disabled: !0,
            placeholder: `Couldn't load Surveys:
${error}`
          };
        }
      }
    })
  },
  sampleData,
  type: import_pieces_framework.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    var _a;
    let response = await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: `${context.auth.props.appUrl}/api/v1/webhooks`,
      body: {
        name: `Activepieces ${name} trigger`,
        workspaceId: context.propsValue.workspaceId,
        // formbricks docs dont have this field but it is required in the API
        url: context.webhookUrl,
        triggers: [eventType],
        surveyIds: (_a = context.propsValue.survey_id) != null ? _a : []
      },
      headers: {
        "x-api-key": context.auth.props.apiKey
      }
    });
    await context.store.put(
      `formbricks_${name}_trigger`,
      response.body
    );
  },
  async onDisable(context) {
    let webhook = await context.store.get(
      `formbricks_${name}_trigger`
    );
    if ((webhook == null ? void 0 : webhook.data.id) != null) {
      let request = {
        method: import_pieces_common.HttpMethod.DELETE,
        url: `${context.auth.props.appUrl}/api/v1/webhooks/${webhook.data.id}`,
        headers: {
          "x-api-key": context.auth.props.apiKey
        }
      };
      await import_pieces_common.httpClient.sendRequest(request);
    }
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/formbricks/src/lib/triggers/index.ts
var triggers = [
  {
    name: "response_created",
    eventType: "responseCreated",
    displayName: "Response Created",
    description: "Triggered when a new response is created.",
    sampleData: {
      webhookId: "cljwxvjos0003qhnvj2jg4k5i",
      event: "responseCreated",
      data: {
        id: "cljwy2m8r0001qhclco1godnu",
        createdAt: "2023-07-10T14:14:17.115Z",
        updatedAt: "2023-07-10T14:14:17.115Z",
        surveyId: "cljsf3d7a000019cv9apt2t27",
        finished: !1,
        data: {
          qumbk3fkr6cky8850bvvq5z1: "Executive"
        },
        meta: {
          userAgent: {
            os: "Mac OS",
            browser: "Chrome"
          }
        },
        personAttributes: {
          email: "test@web.com",
          userId: "THIS-IS-A-VERY-LONG-USER-ID-FOR-TESTING"
        },
        person: {
          id: "cljold01t0000qh8ewzigzmjk",
          attributes: {
            email: "test@web.com",
            userId: "THIS-IS-A-VERY-LONG-USER-ID-FOR-TESTING"
          },
          createdAt: "2023-07-04T17:56:17.154Z",
          updatedAt: "2023-07-04T17:56:17.154Z"
        },
        notes: [],
        tags: []
      }
    }
  },
  {
    name: "response_updated",
    eventType: "responseUpdated",
    displayName: "Response Updated",
    description: "Triggered when a new response is updated.",
    sampleData: {
      webhookId: "cljwxvjos0003qhnvj2jg4k5i",
      event: "responseUpdated",
      data: {
        id: "cljwy2m8r0001qhclco1godnu",
        createdAt: "2023-07-10T14:14:17.115Z",
        updatedAt: "2023-07-10T14:14:17.115Z",
        surveyId: "cljsf3d7a000019cv9apt2t27",
        finished: !1,
        data: {
          qumbk3fkr6cky8850bvvq5z1: "Executive"
        },
        meta: {
          userAgent: {
            os: "Mac OS",
            browser: "Chrome"
          }
        },
        personAttributes: {
          email: "test@web.com",
          userId: "THIS-IS-A-VERY-LONG-USER-ID-FOR-TESTING"
        },
        person: {
          id: "cljold01t0000qh8ewzigzmjk",
          attributes: {
            email: "test@web.com",
            userId: "THIS-IS-A-VERY-LONG-USER-ID-FOR-TESTING"
          },
          createdAt: "2023-07-04T17:56:17.154Z",
          updatedAt: "2023-07-04T17:56:17.154Z"
        },
        notes: [],
        tags: []
      }
    }
  },
  {
    name: "response_finished",
    eventType: "responseFinished",
    displayName: "Response Finished",
    description: "Triggered when a new response is finished.",
    sampleData: {
      webhookId: "cljwxvjos0003qhnvj2jg4k5i",
      event: "responseFinished",
      data: {
        id: "cljwy2m8r0001qhclco1godnu",
        createdAt: "2023-07-10T14:14:17.115Z",
        updatedAt: "2023-07-10T14:14:17.115Z",
        surveyId: "cljsf3d7a000019cv9apt2t27",
        finished: !1,
        data: {
          qumbk3fkr6cky8850bvvq5z1: "Executive"
        },
        meta: {
          userAgent: {
            os: "Mac OS",
            browser: "Chrome"
          }
        },
        personAttributes: {
          email: "test@web.com",
          userId: "THIS-IS-A-VERY-LONG-USER-ID-FOR-TESTING"
        },
        person: {
          id: "cljold01t0000qh8ewzigzmjk",
          attributes: {
            email: "test@web.com",
            userId: "THIS-IS-A-VERY-LONG-USER-ID-FOR-TESTING"
          },
          createdAt: "2023-07-04T17:56:17.154Z",
          updatedAt: "2023-07-04T17:56:17.154Z"
        },
        notes: [],
        tags: []
      }
    }
  }
].map((props) => formBricksRegisterTrigger(props));

// ../ap/packages/pieces/community/formbricks/src/index.ts
var markdownPropertyDescription = `
  **Enable Basic Authentication:**
  1. Login to your Formbricks account
  2. On the bottom-left, click on your account dropdown
  3. Select 'Account Settings' -> 'Setting'
  4. select 'API Keys'
  5. Click on 'Add API Key'
  6. On the popup form, enter the 'API Key Label' to name the Key
  7. Copy the API key and paste it below.

  **APP URL:**
  - The API URL for Formbricks example the cloud is at https://app.formbricks.com
  - **Note: make sure there is no trailing slash and no /api**
`, formBricksAuth = import_pieces_framework2.PieceAuth.CustomAuth({
  required: !0,
  description: markdownPropertyDescription,
  props: {
    appUrl: import_pieces_framework2.Property.ShortText({
      displayName: "APP URL",
      required: !0,
      defaultValue: "https://app.formbricks.com"
    }),
    apiKey: import_pieces_framework2.Property.ShortText({
      displayName: "API Key",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    try {
      let authValue = auth;
      return await import_pieces_common2.httpClient.sendRequest({
        method: import_pieces_common2.HttpMethod.GET,
        url: `${authValue.appUrl}/api/v1/management/me`,
        headers: {
          "x-api-key": authValue.apiKey
        }
      }), {
        valid: !0
      };
    } catch (error) {
      return {
        valid: !1,
        error: "Please provide correct APP URL and API key."
      };
    }
  }
}), formbricks = (0, import_pieces_framework2.createPiece)({
  displayName: "Formbricks",
  description: "Open source Survey Platform",
  auth: formBricksAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/formbricks.png",
  categories: [import_pieces_framework3.PieceCategory.BUSINESS_INTELLIGENCE],
  authors: ["kanarelo", "kishanprmr", "MoShizzle", "abuaboud"],
  actions: [
    (0, import_pieces_common2.createCustomApiCallAction)({
      auth: formBricksAuth,
      authMapping: async (auth) => ({
        "x-api-key": auth.props.apiKey
      }),
      baseUrl: (auth) => {
        var _a, _b;
        return `${(_b = (_a = auth == null ? void 0 : auth.props) == null ? void 0 : _a.appUrl) != null ? _b : ""}/api/v1`;
      }
    })
  ],
  triggers
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  formBricksAuth,
  formbricks
});
