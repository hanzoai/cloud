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

// ../ap/packages/pieces/community/tally/src/index.ts
var index_exports = {};
__export(index_exports, {
  tally: () => tally
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_framework8 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/tally/src/lib/auth.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/tally/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");
var TALLY_API_BASE = "https://api.tally.so", tallyApiClient = {
  validateApiKey,
  listForms,
  createWebhook,
  deleteWebhook,
  fetchRecentSubmissions
};
async function validateApiKey(apiKey) {
  await makeApiCall({
    method: import_pieces_common.HttpMethod.GET,
    path: "/users/me",
    apiKey
  });
}
async function listForms(apiKey) {
  let forms = [], page = 1, hasMore = !0;
  do {
    let data = await makeApiCall({
      method: import_pieces_common.HttpMethod.GET,
      path: "/forms",
      apiKey,
      queryParams: { page: page.toString(), limit: "100" }
    });
    for (let form of data.items)
      form.status !== "DELETED" && forms.push(form);
    hasMore = data.hasMore, page++;
  } while (hasMore);
  return forms;
}
async function createWebhook({
  apiKey,
  formId,
  webhookUrl
}) {
  return (await makeApiCall({
    method: import_pieces_common.HttpMethod.POST,
    path: "/webhooks",
    apiKey,
    body: {
      formId,
      url: webhookUrl,
      eventTypes: ["FORM_RESPONSE"]
    }
  })).id;
}
async function deleteWebhook({
  apiKey,
  webhookId
}) {
  await makeApiCall({
    method: import_pieces_common.HttpMethod.DELETE,
    path: `/webhooks/${webhookId}`,
    apiKey
  });
}
async function fetchRecentSubmissions({
  apiKey,
  formId
}) {
  return makeApiCall({
    method: import_pieces_common.HttpMethod.GET,
    path: `/forms/${formId}/submissions`,
    apiKey,
    queryParams: { limit: "5", filter: "completed" }
  });
}
async function makeApiCall({
  method,
  path,
  apiKey,
  body,
  queryParams
}) {
  var _a;
  let { data, error } = await (0, import_pieces_framework.tryCatch)(
    () => import_pieces_common.httpClient.sendRequest(__spreadValues(__spreadValues({
      method,
      url: `${TALLY_API_BASE}${path}`,
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: apiKey
      }
    }, body !== void 0 ? { body } : {}), queryParams ? { queryParams } : {}))
  );
  if (error) {
    if (error instanceof import_pieces_common.HttpError) {
      let status = error.response.status, responseBody = error.response.body;
      throw status === 401 ? new Error("Authentication failed. Check your API key.") : new Error(`API error (${status}): ${(_a = responseBody == null ? void 0 : responseBody.message) != null ? _a : "Unknown error"}`);
    }
    throw error;
  }
  return data.body;
}

// ../ap/packages/pieces/community/tally/src/lib/auth.ts
var tallyAuth = import_pieces_framework2.PieceAuth.SecretText({
  displayName: "API Key",
  description: "You can obtain API key from [Settings page](https://tally.so/settings/api-keys)",
  required: !0,
  validate: async ({ auth }) => {
    let { error } = await (0, import_pieces_framework3.tryCatch)(() => tallyApiClient.validateApiKey(auth));
    return error ? { valid: !1, error: "Invalid API key." } : { valid: !0 };
  }
});

// ../ap/packages/pieces/community/tally/src/lib/triggers/new-submission.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/tally/src/lib/common/props.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_framework5 = require("@activepieces/pieces-framework");
var formsDropdown = import_pieces_framework4.Property.Dropdown({
  auth: tallyAuth,
  displayName: "Form",
  required: !0,
  refreshers: [],
  async options({ auth }) {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      };
    let { data: forms, error } = await (0, import_pieces_framework5.tryCatch)(() => tallyApiClient.listForms(auth.secret_text));
    return error ? {
      disabled: !0,
      placeholder: "Failed to load forms \u2014 check your connection",
      options: []
    } : { disabled: !1, placeholder: "Select a form", options: forms.map((form) => ({
      label: form.name,
      value: form.id
    })) };
  }
});

// ../ap/packages/pieces/community/tally/src/lib/triggers/new-submission.ts
var TRIGGER_KEY = "new_submission", newSubmissionTrigger = (0, import_pieces_framework6.createTrigger)({
  name: "new-submission",
  classification: "READ",
  displayName: "New Submission",
  auth: tallyAuth,
  description: "Triggers when a form receives a new submission",
  aiMetadata: {
    description: "Fires when a respondent submits a Tally form, delivering the completed submission. The payload includes the response, submission, and respondent IDs, the form ID and name, the submission timestamp, and a fields map keyed by question label with each answer (choice/checkbox/dropdown/ranking options resolved to their text and matrix answers mapped to row/column labels). Use to react to new form entries such as contact requests, signups, or survey responses."
  },
  props: {
    form_id: formsDropdown
  },
  type: import_pieces_framework6.TriggerStrategy.WEBHOOK,
  sampleData: {
    responseId: "resp_001",
    submissionId: "sub_001",
    respondentId: "resp_001",
    formId: "form_001",
    formName: "Contact Form",
    createdAt: "2024-01-01T00:00:00.000Z",
    fields: {
      "Your Name": "John Doe",
      "Your Email": "john@example.com",
      "Which plan?": "Professional"
    }
  },
  async onEnable(context) {
    let webhookId = await tallyApiClient.createWebhook({
      apiKey: context.auth.secret_text,
      formId: context.propsValue.form_id,
      webhookUrl: context.webhookUrl
    });
    await context.store.put(TRIGGER_KEY, { webhookId });
  },
  async onDisable(context) {
    let stored = await context.store.get(TRIGGER_KEY);
    stored != null && stored.webhookId && await tallyApiClient.deleteWebhook({
      apiKey: context.auth.secret_text,
      webhookId: stored.webhookId
    });
  },
  async test(context) {
    let { questions, submissions } = await tallyApiClient.fetchRecentSubmissions({
      apiKey: context.auth.secret_text,
      formId: context.propsValue.form_id
    }), questionById = Object.fromEntries(questions.map((q) => [q.id, q]));
    return submissions.map((submission) => ({
      responseId: submission.id,
      submissionId: submission.id,
      respondentId: submission.respondentId,
      formId: submission.formId,
      formName: "",
      createdAt: submission.submittedAt,
      fields: buildSubmissionFields({ submission, questionById })
    }));
  },
  async run(context) {
    var _a, _b;
    let body = context.payload.body;
    return body.eventType !== "FORM_RESPONSE" ? [] : [__spreadProps(__spreadValues({}, body.data), { fields: buildWebhookFields((_b = (_a = body.data) == null ? void 0 : _a.fields) != null ? _b : []) })];
  }
});
function buildSubmissionFields({
  submission,
  questionById
}) {
  var _a, _b, _c;
  let fields = {}, seen = {};
  for (let response of submission.responses) {
    let question = questionById[response.questionId], label = (_a = question == null ? void 0 : question.title) != null ? _a : response.questionId, count = (_b = seen[label]) != null ? _b : 0;
    seen[label] = count + 1;
    let key = count === 0 ? label : `${label} (${count + 1})`;
    (question == null ? void 0 : question.type) === "MATRIX" ? fields[key] = isPlainObject(response.answer) ? buildMatrixObject({
      value: response.answer,
      rowEntries: ((_c = question.fields) != null ? _c : []).map((f) => ({
        id: f.uuid,
        text: stripQuestionPrefix({ title: f.title, questionTitle: question.title })
      }))
    }) : response.answer : fields[key] = response.answer;
  }
  return fields;
}
function buildWebhookFields(rawFields) {
  var _a, _b, _c;
  let fields = {}, seen = {};
  for (let field of rawFields) {
    if (field.type === "CHECKBOXES" && !field.options) continue;
    let count = (_a = seen[field.label]) != null ? _a : 0;
    seen[field.label] = count + 1;
    let key = count === 0 ? field.label : `${field.label} (${count + 1})`;
    switch (field.type) {
      case "MULTIPLE_CHOICE":
      case "CHECKBOXES":
      case "DROPDOWN":
      case "MULTI_SELECT":
      case "RANKING":
        fields[key] = resolveOptions(field.value, field.options);
        break;
      case "MATRIX":
        fields[key] = isPlainObject(field.value) ? buildMatrixObject({
          value: field.value,
          rowEntries: (_b = field.rows) != null ? _b : [],
          colEntries: (_c = field.columns) != null ? _c : []
        }) : field.value;
        break;
      default:
        fields[key] = field.value;
    }
  }
  return fields;
}
function buildMatrixObject({
  value,
  rowEntries,
  colEntries = []
}) {
  var _a;
  let rowTextById = Object.fromEntries(rowEntries.map((r) => [r.id, r.text])), colTextById = Object.fromEntries(colEntries.map((c) => [c.id, c.text])), result = {};
  for (let [rowId, colIds] of Object.entries(value))
    result[(_a = rowTextById[rowId]) != null ? _a : rowId] = Array.isArray(colIds) ? colIds.map((id) => {
      var _a2;
      return (_a2 = colTextById[id]) != null ? _a2 : id;
    }) : colIds;
  return result;
}
function stripQuestionPrefix({ title, questionTitle }) {
  let prefix = `${questionTitle} [`;
  return title.startsWith(prefix) && title.endsWith("]") ? title.slice(prefix.length, -1) : title;
}
function isPlainObject(value) {
  return typeof value == "object" && value !== null && !Array.isArray(value);
}
function resolveOptions(value, options) {
  if (!options || options.length === 0 || !Array.isArray(value)) return value;
  let optionMap = Object.fromEntries(options.map((o) => [o.id, o.text]));
  return value.map((id) => {
    var _a;
    return (_a = optionMap[id]) != null ? _a : id;
  });
}

// ../ap/packages/pieces/community/tally/src/index.ts
var tally = (0, import_pieces_framework7.createPiece)({
  displayName: "Tally",
  description: "Receive form submissions from Tally forms",
  auth: tallyAuth,
  minimumSupportedRelease: "0.27.1",
  logoUrl: "https://cdn.activepieces.com/pieces/tally.png",
  categories: [import_pieces_framework8.PieceCategory.FORMS_AND_SURVEYS],
  authors: ["kishanprmr", "abuaboud", "bst1n"],
  actions: [
    (0, import_pieces_common2.createCustomApiCallAction)({
      auth: tallyAuth,
      baseUrl: () => TALLY_API_BASE,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: [newSubmissionTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  tally
});
