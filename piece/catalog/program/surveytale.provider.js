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

// ../ap/packages/pieces/community/surveytale/src/index.ts
var index_exports = {};
__export(index_exports, {
  surveytale: () => surveytale
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/surveytale/src/lib/triggers/register.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/surveytale/src/lib/auth.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework"), authDesc = `
  **How to get your API Key:**
  1. Login to your SurveyTale account.
  2. On the bottom-left, click on your account dropdown.
  3. Select 'Organization' from the popup menu.
  4. Select the 'API Keys' tab.
  5. Click on 'Add API Key'.
  6. On the popup form, enter the 'API Key Label' and under Project Access click "Add Permission", select the desired project and assign "Read" permission.
  7. Copy the API key and paste it below.
`, surveyTaleAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: authDesc,
  validate: async ({ auth }) => {
    try {
      let apiKey = auth;
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${SURVEYTALE_BASE_URL}/api/v1/management/me`,
        headers: {
          "x-api-key": apiKey
        }
      }), {
        valid: !0
      };
    } catch (error) {
      return {
        valid: !1,
        error: "Please check your API key"
      };
    }
  }
}), SURVEYTALE_BASE_URL = "https://app.surveytale.com";

// ../ap/packages/pieces/community/surveytale/src/lib/triggers/register.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/surveytale/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var surveyIdProp = import_pieces_framework2.Property.MultiSelectDropdown({
  displayName: "Survey",
  description: "A selection of surveys that will trigger. Else, all surveys will trigger.",
  required: !0,
  refreshers: [],
  auth: surveyTaleAuth,
  options: async ({ auth }) => {
    if (!auth)
      return {
        options: [],
        disabled: !0,
        placeholder: "Please authenticate first"
      };
    let apiKey = auth.secret_text;
    try {
      return {
        disabled: !1,
        options: (await import_pieces_common2.httpClient.sendRequest({
          method: import_pieces_common2.HttpMethod.GET,
          url: `${SURVEYTALE_BASE_URL}/api/v1/management/surveys`,
          headers: {
            "x-api-key": apiKey
          }
        })).body.data.map((survey) => ({
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
});

// ../ap/packages/pieces/community/surveytale/src/lib/triggers/register.ts
var surveyTaleRegisterTrigger = ({
  name,
  displayName,
  eventType,
  description,
  sampleData,
  aiMetadata
}) => (0, import_pieces_framework3.createTrigger)({
  auth: surveyTaleAuth,
  name: `surveytale_trigger_${name}`,
  displayName,
  description,
  aiMetadata: aiMetadata != null ? aiMetadata : {
    description: "Fires when a SurveyTale survey-response webhook event occurs for the selected surveys. The event represents a change to a survey response (such as a response being created, updated, or finished), and the payload contains the response record including its id, surveyId, finished flag, collected answer data, respondent person attributes, and timestamps."
  },
  props: {
    surveyIds: surveyIdProp
  },
  sampleData,
  type: import_pieces_framework3.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    var _a;
    let apiKey = context.auth.secret_text, response = await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: `${SURVEYTALE_BASE_URL}/api/v1/webhooks`,
      body: {
        url: context.webhookUrl,
        triggers: [eventType],
        surveyIds: (_a = context.propsValue.surveyIds) != null ? _a : []
      },
      headers: {
        "x-api-key": apiKey
      }
    });
    await context.store.put(`surveytale_${name}_trigger`, response.body.data.id);
  },
  async onDisable(context) {
    let apiKey = context.auth.secret_text, webhook = await context.store.get(`surveytale_${name}_trigger`);
    if (!(0, import_pieces_framework4.isNil)(webhook)) {
      let request = {
        method: import_pieces_common3.HttpMethod.DELETE,
        url: `${SURVEYTALE_BASE_URL}/api/v1/webhooks/${webhook}`,
        headers: {
          "x-api-key": apiKey
        }
      };
      await import_pieces_common3.httpClient.sendRequest(request);
    }
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/surveytale/src/lib/triggers/index.ts
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
].map((props) => surveyTaleRegisterTrigger(props));

// ../ap/packages/pieces/community/surveytale/src/index.ts
var surveytale = (0, import_pieces_framework5.createPiece)({
  displayName: "SurveyTale",
  description: "Experience management platform for surveys and feedback",
  auth: surveyTaleAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/surveytale.png",
  categories: [import_pieces_framework6.PieceCategory.BUSINESS_INTELLIGENCE, import_pieces_framework6.PieceCategory.FORMS_AND_SURVEYS],
  authors: ["nag381"],
  actions: [
    (0, import_pieces_common4.createCustomApiCallAction)({
      auth: surveyTaleAuth,
      authMapping: async (auth) => ({
        "x-api-key": auth.secret_text
      }),
      baseUrl: () => `${SURVEYTALE_BASE_URL}/api/v1`
    })
  ],
  triggers
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  surveytale
});
