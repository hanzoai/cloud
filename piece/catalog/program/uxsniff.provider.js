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

// ../ap/packages/pieces/community/uxsniff/src/index.ts
var index_exports = {};
__export(index_exports, {
  uxsniff: () => uxsniff,
  uxsniffAuth: () => uxsniffAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/uxsniff/src/lib/triggers/new-feedback.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/uxsniff/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");
var BASE_URL = "https://api.uxsniff.com/v1";
async function uxsniffApiCall({
  apiKey,
  method,
  path,
  queryParams,
  body
}) {
  let cleanedQuery = {};
  if (queryParams)
    for (let [key, value] of Object.entries(queryParams))
      value != null && value !== "" && (cleanedQuery[key] = String(value));
  return await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${BASE_URL}${path}`,
    headers: {
      Authorization: apiKey
    },
    queryParams: cleanedQuery,
    body
  });
}
function toEpochMillis(value) {
  if (typeof value == "number")
    return value < 1e12 ? value * 1e3 : value;
  if (typeof value == "string") {
    let asNumber = Number(value);
    if (!Number.isNaN(asNumber) && value.trim() !== "")
      return asNumber < 1e12 ? asNumber * 1e3 : asNumber;
    let parsed = new Date(value).getTime();
    if (!Number.isNaN(parsed))
      return parsed;
  }
  return 0;
}
var ONE_DAY_MS = 1440 * 60 * 1e3;
function startDateFromEpoch(epochMillis) {
  return new Date(epochMillis - ONE_DAY_MS).toISOString().slice(0, 10);
}
var surveyDropdown = import_pieces_framework.Property.Dropdown({
  displayName: "Survey",
  description: "Select the survey to read responses from.",
  auth: uxsniffAuth,
  refreshers: [],
  required: !0,
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your UXsniff account first"
      };
    try {
      let response = await uxsniffApiCall({
        apiKey: auth.secret_text,
        method: import_pieces_common.HttpMethod.GET,
        path: "/list-survey",
        queryParams: { limit: 200 }
      }), surveys = Array.isArray(response.body) ? response.body : [];
      return surveys.length === 0 ? {
        disabled: !1,
        options: [],
        placeholder: "No surveys found. Create one in your UXsniff dashboard first."
      } : {
        disabled: !1,
        options: surveys.map((survey) => ({
          label: `${survey.survey_name}${survey.active ? "" : " (inactive)"}`,
          value: String(survey.survey_id)
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load surveys. Check your connection."
      };
    }
  }
}), uxsniffCommon = {
  apiCall: uxsniffApiCall,
  toEpochMillis,
  startDateFromEpoch,
  surveyDropdown
};

// ../ap/packages/pieces/community/uxsniff/src/lib/triggers/new-feedback.ts
var polling = {
  strategy: import_pieces_common2.DedupeStrategy.TIMEBASED,
  items: async ({ auth, lastFetchEpochMS }) => {
    let response = await uxsniffCommon.apiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common2.HttpMethod.GET,
      path: "/feedback",
      queryParams: {
        startdate: lastFetchEpochMS ? uxsniffCommon.startDateFromEpoch(lastFetchEpochMS) : void 0,
        limit: 50
      }
    });
    return (Array.isArray(response.body) ? response.body : []).map((item) => {
      var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m, _n, _o, _p, _q, _r, _s, _t, _u, _v, _w;
      return {
        epochMilliSeconds: uxsniffCommon.toEpochMillis(item.created),
        data: {
          feedback_id: (_a = item.feedback_id) != null ? _a : null,
          question: (_b = item.question) != null ? _b : null,
          comment: (_c = item.comment) != null ? _c : null,
          rating: (_d = item.rating) != null ? _d : null,
          created: (_e = item.created) != null ? _e : null,
          url: (_f = item.url) != null ? _f : null,
          screenshot: (_g = item.screenshot) != null ? _g : null,
          visitor_id: (_i = (_h = item.visitor) == null ? void 0 : _h.visitor_id) != null ? _i : null,
          session_id: (_k = (_j = item.visitor) == null ? void 0 : _j.session_id) != null ? _k : null,
          visitor_email: (_m = (_l = item.visitor) == null ? void 0 : _l.email) != null ? _m : null,
          device: (_o = (_n = item.visitor) == null ? void 0 : _n.device) != null ? _o : null,
          country: (_q = (_p = item.visitor) == null ? void 0 : _p.country) != null ? _q : null,
          browser: (_s = (_r = item.visitor) == null ? void 0 : _r.browser) != null ? _s : null,
          browser_size: (_u = (_t = item.visitor) == null ? void 0 : _t.browserSize) != null ? _u : null,
          os: (_w = (_v = item.visitor) == null ? void 0 : _v.os) != null ? _w : null
        }
      };
    });
  }
}, newFeedbackTrigger = (0, import_pieces_framework2.createTrigger)({
  auth: uxsniffAuth,
  name: "new_feedback",
  displayName: "New Feedback",
  description: "Triggers when a visitor submits new feedback on your website.",
  aiMetadata: {
    description: "Fires when a website visitor submits new feedback via the UXsniff feedback widget. Each event represents one feedback submission and includes the question shown, the visitor's comment and numeric rating, the page URL, an optional screenshot, and visitor context such as email, device, browser, operating system, and country."
  },
  props: {},
  type: import_pieces_framework2.TriggerStrategy.POLLING,
  sampleData: {
    feedback_id: 1,
    question: "How would you rate your experience?",
    comment: "Good comment",
    rating: 5,
    created: "2022-10-21 12:56:33",
    url: "https://uxsniff.com/",
    screenshot: "",
    visitor_id: "1666355009686.mhprr8t",
    session_id: "1666355009686.6nzbnflq",
    visitor_email: "john@doe.com",
    device: "Desktop",
    country: "Singapore",
    browser: "Safari",
    browser_size: "2560x1335",
    os: "MacOS"
  },
  async test(context) {
    return await import_pieces_common2.pollingHelper.test(polling, context);
  },
  async onEnable(context) {
    await import_pieces_common2.pollingHelper.onEnable(polling, context);
  },
  async onDisable(context) {
    await import_pieces_common2.pollingHelper.onDisable(polling, context);
  },
  async run(context) {
    return await import_pieces_common2.pollingHelper.poll(polling, context);
  }
});

// ../ap/packages/pieces/community/uxsniff/src/lib/triggers/new-survey-response.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var props = {
  survey_id: uxsniffCommon.surveyDropdown
}, polling2 = {
  strategy: import_pieces_common3.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue, lastFetchEpochMS }) => {
    var _a;
    return ((_a = (await uxsniffCommon.apiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common3.HttpMethod.GET,
      path: "/survey",
      queryParams: {
        id: propsValue.survey_id,
        startdate: lastFetchEpochMS ? uxsniffCommon.startDateFromEpoch(lastFetchEpochMS) : void 0,
        limit: 100
      }
    })).body.responses) != null ? _a : []).map((item) => {
      var _a2, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k;
      return {
        epochMilliSeconds: uxsniffCommon.toEpochMillis(item.created),
        data: {
          response_id: (_a2 = item.id) != null ? _a2 : null,
          client_id: (_b = item.client_id) != null ? _b : null,
          session_id: (_d = (_c = item.session_id) != null ? _c : item.sessio_id) != null ? _d : null,
          answers: item.answers === void 0 || item.answers === null ? null : typeof item.answers == "object" ? JSON.stringify(item.answers) : String(item.answers),
          url: (_e = item.url) != null ? _e : null,
          country: (_f = item.country) != null ? _f : null,
          country_name: (_g = item.countryName) != null ? _g : null,
          created: (_h = item.created) != null ? _h : null,
          os: (_i = item.os) != null ? _i : null,
          browser: (_j = item.browser) != null ? _j : null,
          referrer: (_k = item.referrer) != null ? _k : null
        }
      };
    });
  }
}, newSurveyResponseTrigger = (0, import_pieces_framework3.createTrigger)({
  auth: uxsniffAuth,
  name: "new_survey_response",
  displayName: "New Survey Response",
  description: "Triggers when a visitor submits a new response to the selected survey.",
  aiMetadata: {
    description: "Fires when a website visitor submits a new response to the selected UXsniff survey. Each event represents one completed survey response and includes the answers given (serialized as JSON), the page URL where the survey was answered, visitor context such as country, operating system, browser, and referrer, plus client and session identifiers."
  },
  props,
  type: import_pieces_framework3.TriggerStrategy.POLLING,
  sampleData: {
    response_id: "98765",
    client_id: "visitor_abc",
    session_id: "session_xyz",
    answers: '{"1":"Very satisfied","2":"Found everything I needed"}',
    url: "https://example.com/pricing",
    country: "US",
    country_name: "United States",
    created: "2024-05-01T10:30:00Z",
    os: "Windows",
    browser: "Chrome",
    referrer: "https://google.com"
  },
  async test(context) {
    return await import_pieces_common3.pollingHelper.test(polling2, context);
  },
  async onEnable(context) {
    await import_pieces_common3.pollingHelper.onEnable(polling2, context);
  },
  async onDisable(context) {
    await import_pieces_common3.pollingHelper.onDisable(polling2, context);
  },
  async run(context) {
    return await import_pieces_common3.pollingHelper.poll(polling2, context);
  }
});

// ../ap/packages/pieces/community/uxsniff/src/index.ts
var uxsniffAuth = import_pieces_framework4.PieceAuth.SecretText({
  displayName: "API Key",
  description: `To get your UXsniff API key:
1. Log in to your UXsniff account.
2. Open the [Account page](https://app.uxsniff.com/login?next=account).
3. Copy your API Key and paste it below.`,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common4.httpClient.sendRequest({
        method: import_pieces_common4.HttpMethod.GET,
        url: "https://api.uxsniff.com/v1/list-survey",
        headers: { Authorization: auth },
        queryParams: { limit: "1" }
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API Key. Find it on your UXsniff Account page."
      };
    }
  }
}), uxsniff = (0, import_pieces_framework4.createPiece)({
  displayName: "UXsniff",
  description: "AI-powered UX analytics: session recordings, heatmaps, feedback widgets, and surveys for your website.",
  auth: uxsniffAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/uxsniff.png",
  categories: [import_pieces_framework5.PieceCategory.BUSINESS_INTELLIGENCE, import_pieces_framework5.PieceCategory.MARKETING],
  authors: ["sanket-a11y"],
  actions: [
    (0, import_pieces_common4.createCustomApiCallAction)({
      baseUrl: () => "https://api.uxsniff.com/v1",
      auth: uxsniffAuth,
      authMapping: async (auth) => ({
        Authorization: auth.secret_text
      })
    })
  ],
  triggers: [newFeedbackTrigger, newSurveyResponseTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  uxsniff,
  uxsniffAuth
});
