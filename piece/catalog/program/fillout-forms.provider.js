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

// ../ap/packages/pieces/community/fillout-forms/src/index.ts
var index_exports = {};
__export(index_exports, {
  filloutForms: () => filloutForms
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework8 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/fillout-forms/src/lib/actions/get-form-responses.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/fillout-forms/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.fillout.com/v1/api";
async function makeRequest(apiKey, method, path, queryParams, body) {
  var _a, _b, _c, _d;
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers: {
        Authorization: `Bearer ${apiKey}`
      },
      queryParams,
      body
    })).body;
  } catch (error) {
    throw ((_a = error.response) == null ? void 0 : _a.status) === 401 ? new Error("Invalid API Key. Please check your authentication credentials.") : ((_b = error.response) == null ? void 0 : _b.status) === 403 ? new Error("Access denied. Please check your API key permissions.") : ((_c = error.response) == null ? void 0 : _c.status) === 404 ? new Error("Resource not found. Please check the form ID or submission ID.") : ((_d = error.response) == null ? void 0 : _d.status) >= 500 ? new Error("Fillout service error. Please try again later.") : new Error(`Request failed: ${error.message || "Unknown error"}`);
  }
}

// ../ap/packages/pieces/community/fillout-forms/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/fillout-forms/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var filloutFormsAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: `To get your API key:
1. Go to your Fillout account settings.
2. Click on "Developer settings".
3. Generate and copy your API key.
4. Paste it here.`,
  validate: async ({ auth }) => {
    try {
      return await makeRequest(auth, import_pieces_common2.HttpMethod.GET, "/forms", void 0), { valid: !0 };
    } catch (error) {
      return {
        valid: !1,
        error: error.message || "Invalid API Key. Please check your API key and try again."
      };
    }
  }
});

// ../ap/packages/pieces/community/fillout-forms/src/lib/common/props.ts
var formIdDropdown = import_pieces_framework2.Property.Dropdown({
  auth: filloutFormsAuth,
  displayName: "Form",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please connect your Fillout Forms account.",
        options: []
      };
    let apiKey = auth.secret_text;
    return {
      disabled: !1,
      options: (await makeRequest(apiKey, import_pieces_common3.HttpMethod.GET, "/forms", void 0)).map((form) => ({
        label: form.name,
        value: form.formId
      }))
    };
  }
}), submissionIdDropdown = import_pieces_framework2.Property.Dropdown({
  auth: filloutFormsAuth,
  displayName: "Submission",
  description: "Select from the 50 most recent submissions for the chosen form.",
  required: !0,
  refreshers: ["formId"],
  options: async ({ auth, formId }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please connect your Fillout Forms account",
        options: []
      };
    if (!formId)
      return {
        disabled: !0,
        placeholder: "Please select a form first",
        options: []
      };
    try {
      let apiKey = auth.secret_text, options = ((await makeRequest(
        apiKey,
        import_pieces_common3.HttpMethod.GET,
        `/forms/${formId}/submissions`,
        { limit: 50 }
      )).responses || []).map((submission) => {
        let firstQuestion = submission.questions && submission.questions[0], firstValue = firstQuestion ? firstQuestion.value : "No data";
        return {
          label: `${new Date(submission.submissionTime).toLocaleString()} - ${firstValue}`,
          value: submission.submissionId
        };
      });
      return {
        disabled: !1,
        options,
        placeholder: options.length === 0 ? "No submissions found" : "Select a submission"
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Error loading submissions",
        options: []
      };
    }
  }
});

// ../ap/packages/pieces/community/fillout-forms/src/lib/actions/get-form-responses.ts
var getFormResponses = (0, import_pieces_framework3.createAction)({
  auth: filloutFormsAuth,
  name: "getFormResponses",
  classification: "SEARCH",
  displayName: "Get Form Responses",
  description: "Fetch all responses for a Fillout form, with optional filters.",
  audience: "both",
  aiMetadata: { description: "Lists submissions for a specific Fillout form (identified by form ID), optionally narrowed by date range, search text, completion status (finished by default, or in-progress), sort order, limit, and offset for pagination. With no filters it returns all submissions; supply filters to fetch only matching ones. Use to retrieve or page through collected form responses. Read-only and idempotent.", idempotent: !0 },
  props: {
    formId: formIdDropdown,
    limit: import_pieces_framework3.Property.Number({
      displayName: "Limit",
      required: !1,
      description: "Max number of submissions to retrieve (1-150)."
    }),
    afterDate: import_pieces_framework3.Property.DateTime({
      displayName: "After Date",
      required: !1,
      description: "Filter submissions after this date (YYYY-MM-DDTHH:mm:ss.sssZ)."
    }),
    beforeDate: import_pieces_framework3.Property.DateTime({
      displayName: "Before Date",
      required: !1,
      description: "Filter submissions before this date (YYYY-MM-DDTHH:mm:ss.sssZ)."
    }),
    offset: import_pieces_framework3.Property.Number({
      displayName: "Offset",
      required: !1,
      description: "Starting position for fetching submissions."
    }),
    status: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Status",
      required: !1,
      options: {
        options: [
          { label: "In Progress (unfinished)", value: "in_progress" }
        ]
      },
      description: 'By default, only finished submissions are returned. Select "In Progress" to get unfinished submissions instead.'
    }),
    includeEditLink: import_pieces_framework3.Property.Checkbox({
      displayName: "Include Edit Link",
      required: !1,
      description: "Include a link to edit the submission."
    }),
    includePreview: import_pieces_framework3.Property.Checkbox({
      displayName: "Include Preview",
      required: !1,
      description: "Include preview responses."
    }),
    sort: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Sort",
      required: !1,
      options: {
        options: [
          { label: "Ascending", value: "asc" },
          { label: "Descending", value: "desc" }
        ]
      },
      description: "Sort order."
    }),
    search: import_pieces_framework3.Property.ShortText({
      displayName: "Search",
      required: !1,
      description: "Filter for submissions containing this text."
    })
  },
  async run(context) {
    let apiKey = context.auth.secret_text, formId = context.propsValue.formId, queryParams = {};
    return context.propsValue.limit !== void 0 && (queryParams.limit = context.propsValue.limit), context.propsValue.afterDate !== void 0 && (queryParams.afterDate = context.propsValue.afterDate), context.propsValue.beforeDate !== void 0 && (queryParams.beforeDate = context.propsValue.beforeDate), context.propsValue.offset !== void 0 && (queryParams.offset = context.propsValue.offset), context.propsValue.status !== void 0 && (queryParams.status = context.propsValue.status), context.propsValue.includeEditLink !== void 0 && (queryParams.includeEditLink = context.propsValue.includeEditLink), context.propsValue.includePreview !== void 0 && (queryParams.includePreview = context.propsValue.includePreview), context.propsValue.sort !== void 0 && (queryParams.sort = context.propsValue.sort), context.propsValue.search !== void 0 && (queryParams.search = context.propsValue.search), await makeRequest(
      apiKey,
      import_pieces_common4.HttpMethod.GET,
      `/forms/${formId}/submissions`,
      queryParams
    );
  }
});

// ../ap/packages/pieces/community/fillout-forms/src/lib/actions/get-single-response.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var getSingleResponse = (0, import_pieces_framework4.createAction)({
  auth: filloutFormsAuth,
  name: "getSingleResponse",
  classification: "READ",
  displayName: "Get Single Response",
  description: "Retrieves a specific submission from a form.",
  audience: "both",
  aiMetadata: { description: "Retrieves one specific submission from a Fillout form, keyed by form ID and submission ID. Use when you already know the submission ID and need its full details, rather than listing all responses. Read-only and idempotent.", idempotent: !0 },
  props: {
    formId: formIdDropdown,
    submissionId: submissionIdDropdown,
    includeEditLink: import_pieces_framework4.Property.Checkbox({
      displayName: "Include Edit Link",
      required: !1,
      description: "Include a link to edit the submission."
    })
  },
  async run(context) {
    let apiKey = context.auth.secret_text, { formId, submissionId } = context.propsValue, queryParams = {};
    return context.propsValue.includeEditLink !== void 0 && (queryParams.includeEditLink = context.propsValue.includeEditLink), await makeRequest(
      apiKey,
      import_pieces_common5.HttpMethod.GET,
      `/forms/${formId}/submissions/${submissionId}`,
      queryParams
    );
  }
});

// ../ap/packages/pieces/community/fillout-forms/src/lib/actions/find-form-by-title.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var findFormByTitle = (0, import_pieces_framework5.createAction)({
  auth: filloutFormsAuth,
  name: "findFormByTitle",
  classification: "SEARCH",
  displayName: "Find Form by Title",
  description: "Finds an existing forms by title.",
  audience: "both",
  aiMetadata: { description: "Searches the account's Fillout forms for those whose title contains the given text (case-insensitive partial match) and returns the matches. Use to resolve a form name to its form ID before calling response-fetching actions. Read-only and idempotent.", idempotent: !0 },
  props: {
    title: import_pieces_framework5.Property.ShortText({
      displayName: "Form Title",
      required: !0,
      description: "The (partial or full) title of the form to search for."
    })
  },
  async run(context) {
    let apiKey = context.auth.secret_text, forms = await makeRequest(apiKey, import_pieces_common6.HttpMethod.GET, "/forms", void 0), searchTitle = context.propsValue.title.toLowerCase(), matches = forms.filter(
      (f) => f.name.toLowerCase().includes(searchTitle)
    );
    return {
      found: matches.length > 0,
      result: matches
    };
  }
});

// ../ap/packages/pieces/community/fillout-forms/src/lib/triggers/new-form-response.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework"), TRIGGER_KEY = "new-form-response-trigger", newFormResponse = (0, import_pieces_framework6.createTrigger)({
  auth: filloutFormsAuth,
  name: "new-form-response",
  classification: "READ",
  displayName: "New Form Response",
  description: "Triggers when a new submission is received for a selected Fillout form.",
  aiMetadata: {
    description: "Fires when a new submission is received for the selected Fillout form, delivering the submitted answers, calculations, and metadata. Use to start a workflow whenever someone completes the form."
  },
  props: {
    formId: formIdDropdown
  },
  type: import_pieces_framework6.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let { formId } = context.propsValue, apiKey = context.auth.secret_text, response = await makeRequest(
      apiKey,
      import_pieces_common7.HttpMethod.POST,
      "/webhook/create",
      void 0,
      {
        formId,
        url: context.webhookUrl
      }
    );
    await context.store.put(TRIGGER_KEY, response.id);
  },
  async onDisable(context) {
    let apiKey = context.auth.secret_text, webhookId = await context.store.get(TRIGGER_KEY);
    (0, import_pieces_framework7.isNil)(webhookId) || await makeRequest(apiKey, import_pieces_common7.HttpMethod.POST, "/webhook/delete", void 0, {
      webhookId
    });
  },
  async test(context) {
    let { formId } = context.propsValue, apiKey = context.auth.secret_text;
    return (await makeRequest(
      apiKey,
      import_pieces_common7.HttpMethod.GET,
      `/forms/${formId}/submissions`,
      {
        limit: "10",
        sort: "desc"
      }
    )).responses;
  },
  async run(context) {
    return [JSON.parse(context.payload.body).submission];
  },
  sampleData: {
    questions: [
      {
        id: "5AtgG35AAZVcrSVfRubvp1",
        name: "What is your name?",
        type: "ShortAnswer",
        value: "John Doe"
      },
      {
        id: "gRBWVbE2fut2oiAMprdZpY",
        name: "What is your email?",
        type: "Email",
        value: "john@example.com"
      },
      {
        id: "hP4bHA1CgvyD2LKhBnnGHy",
        name: "Pick your favorite color",
        type: "MultipleChoice",
        value: "Blue"
      }
    ],
    calculations: [
      {
        id: "abcdef",
        name: "Price",
        type: "number",
        value: "12.50"
      }
    ],
    urlParameters: [
      {
        id: "email",
        name: "email",
        value: "john@example.com"
      }
    ],
    submissionId: "abc123",
    submissionTime: "2024-05-16T23:20:05.324Z"
  }
});

// ../ap/packages/pieces/community/fillout-forms/src/index.ts
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework9 = require("@activepieces/pieces-framework");
var filloutForms = (0, import_pieces_framework8.createPiece)({
  displayName: "Fillout Forms",
  description: "Create interactive forms and automate workflows with Fillout",
  auth: filloutFormsAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/fillout-forms.png",
  categories: [import_pieces_framework9.PieceCategory.FORMS_AND_SURVEYS],
  authors: ["Sanket6652", "onyedikachi-david"],
  actions: [
    getFormResponses,
    getSingleResponse,
    findFormByTitle,
    (0, import_pieces_common8.createCustomApiCallAction)({
      auth: filloutFormsAuth,
      baseUrl: () => "https://api.fillout.com/v1/api",
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: [newFormResponse]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  filloutForms
});
