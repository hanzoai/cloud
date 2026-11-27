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

// ../ap/packages/pieces/community/magical-api/src/index.ts
var index_exports = {};
__export(index_exports, {
  magicalApi: () => magicalApi
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/magical-api/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/magical-api/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://gw.magicalapi.com";
async function makeRequest(apiKey, method, path, body) {
  var _a;
  try {
    let headers = {
      "api-key": apiKey.secret_text,
      "Content-Type": "application/json"
    };
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers,
      body
    })).body;
  } catch (error) {
    throw new Error(
      `MagicalAPI request failed: ${JSON.stringify(
        ((_a = error.response) == null ? void 0 : _a.body) || error.message || error
      )}`
    );
  }
}

// ../ap/packages/pieces/community/magical-api/src/lib/common/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework"), magicalApiAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Magical API Key",
  description: "Enter your Magical API key from your MagicalAPI dashboard.",
  required: !0,
  validate: async ({ auth }) => {
    var _a;
    if (!auth)
      return {
        valid: !1,
        error: "API Key is required."
      };
    try {
      let response = await makeRequest(
        { type: import_pieces_framework2.AppConnectionType.SECRET_TEXT, secret_text: auth },
        import_pieces_common2.HttpMethod.POST,
        "/profile-data",
        { profile_name: "activepieces_validation" }
      );
      return response && ((_a = response.data) != null && _a.request_id) ? { valid: !0 } : {
        valid: !1,
        error: "Invalid API key."
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Authentication failed: Invalid API key."
      };
    }
  }
});

// ../ap/packages/pieces/community/magical-api/src/lib/actions/review-resume.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms)), reviewResume = (0, import_pieces_framework3.createAction)({
  auth: magicalApiAuth,
  name: "review_resume",
  displayName: "Review Resume",
  description: "Analyze parsed resume using predefined criteria.",
  audience: "both",
  aiMetadata: { description: "Analyzes a publicly accessible resume file (PDF/DOC/DOCX) at a given URL against predefined review criteria, returning an assessment with a score. Use to evaluate resume quality without a specific job description. Either supply the resume URL to start a job, or pass the Request ID from a prior timed-out run to retrieve that result; the call polls until the review completes. Idempotent: re-running with the same URL re-evaluates the same resume.", idempotent: !0 },
  props: {
    url: import_pieces_framework3.Property.ShortText({
      displayName: "Resume URL",
      description: "Provide a direct URL to a publicly accessible resume file (PDF, DOC, or DOCX).",
      required: !1
    }),
    request_id: import_pieces_framework3.Property.ShortText({
      displayName: "Request ID (for retries)",
      description: "If a previous run timed out, paste the Request ID here to get the result.",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c;
    let { url, request_id } = context.propsValue, apiKey = context.auth, REVIEW_RESUME_ENDPOINT = "/resume-review", requestIdToPoll = request_id, response;
    if (requestIdToPoll)
      response = await makeRequest(apiKey, import_pieces_common3.HttpMethod.POST, REVIEW_RESUME_ENDPOINT, { request_id: requestIdToPoll });
    else {
      if (!url)
        throw new Error("To start a new request, you must provide a Resume URL.");
      response = await makeRequest(apiKey, import_pieces_common3.HttpMethod.POST, REVIEW_RESUME_ENDPOINT, { url });
    }
    if (((_a = response.data) == null ? void 0 : _a.score) !== void 0)
      return response.data;
    if (requestIdToPoll = (_b = response.data) == null ? void 0 : _b.request_id, !requestIdToPoll)
      throw new Error("Failed to start or retrieve the request. The API did not return a request_id or the final data.");
    let maxAttempts = 20, pollInterval = 3e3;
    for (let attempt = 0; attempt < maxAttempts; attempt++) {
      await sleep(pollInterval);
      let pollResponse = await makeRequest(apiKey, import_pieces_common3.HttpMethod.POST, REVIEW_RESUME_ENDPOINT, { request_id: requestIdToPoll });
      if (((_c = pollResponse.data) == null ? void 0 : _c.score) !== void 0)
        return pollResponse.data;
    }
    throw new Error(
      "Timeout: Resume review is taking longer than expected. You can check the status later with this Request ID: " + requestIdToPoll
    );
  }
});

// ../ap/packages/pieces/community/magical-api/src/lib/actions/parse-resume.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var sleep2 = (ms) => new Promise((resolve) => setTimeout(resolve, ms)), parseResume = (0, import_pieces_framework4.createAction)({
  auth: magicalApiAuth,
  name: "parse_resume",
  displayName: "Parse Resume",
  description: "Extract structured data (name, email, experience, skills, etc.) from a resume file.",
  audience: "both",
  aiMetadata: { description: "Parses a publicly accessible resume file (PDF/DOC/DOCX) at a given URL into structured fields such as name, contact info, experience, education, and skills. Use to turn an unstructured resume into machine-readable data. Either supply the resume URL to start a job, or pass the Request ID from a prior timed-out run to retrieve that result instead; the call polls until parsing completes. Idempotent: re-running with the same URL re-parses the same document and yields the same data.", idempotent: !0 },
  props: {
    url: import_pieces_framework4.Property.ShortText({
      displayName: "Resume URL",
      description: "Provide the direct link to a publicly accessible resume file (PDF, DOC, or DOCX)",
      required: !1
    }),
    request_id: import_pieces_framework4.Property.ShortText({
      displayName: "Request ID (for retries)",
      description: "If a previous run timed out, paste the Request ID here to get the result.",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c;
    let { url, request_id } = context.propsValue, apiKey = context.auth, RESUME_PARSER_ENDPOINT = "/resume-parser", requestIdToPoll = request_id, response;
    if (requestIdToPoll)
      response = await makeRequest(apiKey, import_pieces_common4.HttpMethod.POST, RESUME_PARSER_ENDPOINT, { request_id: requestIdToPoll });
    else {
      if (!url)
        throw new Error("To start a new request, you must provide a Resume URL.");
      response = await makeRequest(apiKey, import_pieces_common4.HttpMethod.POST, RESUME_PARSER_ENDPOINT, { url });
    }
    if ((_a = response.data) != null && _a.basic)
      return response.data;
    if (requestIdToPoll = (_b = response.data) == null ? void 0 : _b.request_id, !requestIdToPoll)
      throw new Error("Failed to start or retrieve the request. The API did not return a request_id or the final data.");
    let maxAttempts = 20, pollInterval = 3e3;
    for (let attempt = 0; attempt < maxAttempts; attempt++) {
      await sleep2(pollInterval);
      let pollResponse = await makeRequest(apiKey, import_pieces_common4.HttpMethod.POST, RESUME_PARSER_ENDPOINT, { request_id: requestIdToPoll });
      if ((_c = pollResponse.data) != null && _c.basic)
        return pollResponse.data;
    }
    throw new Error(
      "Timeout: Resume parsing is taking longer than expected. You can check the status later with this Request ID: " + requestIdToPoll
    );
  }
});

// ../ap/packages/pieces/community/magical-api/src/lib/actions/get-profile-data.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var sleep3 = (ms) => new Promise((resolve) => setTimeout(resolve, ms)), getProfileData = (0, import_pieces_framework5.createAction)({
  auth: magicalApiAuth,
  name: "get_profile_data",
  displayName: "Get Profile Data",
  description: "Given a LinkedIn profile username, retrieve comprehensive profile data.",
  audience: "both",
  aiMetadata: { description: 'Retrieves comprehensive public LinkedIn profile data (experience, education, headline, etc.) for a given profile username \u2014 the slug from the profile URL, e.g. "williamhgates", not a full URL or display name. Use to enrich a person record from their LinkedIn handle. Either supply the profile username to start a lookup, or pass the Request ID from a prior timed-out run to retrieve that result; the call polls until data is ready. Idempotent: it is a read-only lookup that returns the same profile for the same username.', idempotent: !0 },
  props: {
    profile_name: import_pieces_framework5.Property.ShortText({
      displayName: "LinkedIn Profile Username",
      description: "Provide the username from the LinkedIn profile URL (e.g., 'williamhgates').",
      required: !1
    }),
    request_id: import_pieces_framework5.Property.ShortText({
      displayName: "Request ID (for retries)",
      description: "If a previous run timed out, paste the Request ID here to get the result.",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c;
    let { profile_name, request_id } = context.propsValue, apiKey = context.auth, PROFILE_DATA_ENDPOINT = "/profile-data", requestIdToPoll = request_id, response;
    if (requestIdToPoll)
      response = await makeRequest(apiKey, import_pieces_common5.HttpMethod.POST, PROFILE_DATA_ENDPOINT, { request_id });
    else {
      if (!profile_name)
        throw new Error("To start a new request, you must provide a LinkedIn Profile Username.");
      response = await makeRequest(apiKey, import_pieces_common5.HttpMethod.POST, PROFILE_DATA_ENDPOINT, { profile_name });
    }
    if ((_a = response.data) != null && _a.profile)
      return response.data;
    if (requestIdToPoll = (_b = response.data) == null ? void 0 : _b.request_id, !requestIdToPoll)
      throw new Error("Failed to start or retrieve the request. The API did not return a request_id or the final data.");
    let maxAttempts = 20, pollInterval = 3e3;
    for (let attempt = 0; attempt < maxAttempts; attempt++) {
      await sleep3(pollInterval);
      let pollResponse = await makeRequest(apiKey, import_pieces_common5.HttpMethod.POST, PROFILE_DATA_ENDPOINT, { request_id: requestIdToPoll });
      if ((_c = pollResponse.data) != null && _c.profile)
        return pollResponse.data;
    }
    throw new Error("Timeout: The request is still processing. You can try again later with this Request ID: " + requestIdToPoll);
  }
});

// ../ap/packages/pieces/community/magical-api/src/lib/actions/get-company-data.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var sleep4 = (ms) => new Promise((resolve) => setTimeout(resolve, ms)), getCompanyData = (0, import_pieces_framework6.createAction)({
  auth: magicalApiAuth,
  name: "get_company_data",
  displayName: "Get Company Data",
  description: "Given a company name, LinkedIn username, or website, fetch comprehensive company info.",
  audience: "both",
  aiMetadata: { description: 'Fetches comprehensive public company data from any one of three identifiers: a company name (e.g. "Microsoft"), its LinkedIn username slug (e.g. "microsoft"), or its website URL. Use to enrich a company record when you have any of those handles. At least one identifier is required; alternatively pass the Request ID from a prior timed-out run to retrieve that result. The call polls until data is ready. Idempotent: it is a read-only lookup returning the same company for the same identifier.', idempotent: !0 },
  props: {
    company_name: import_pieces_framework6.Property.ShortText({
      displayName: "Company Name",
      description: "Provide the company's legal name (e.g., 'Microsoft').",
      required: !1
    }),
    company_username: import_pieces_framework6.Property.ShortText({
      displayName: "Company LinkedIn Username",
      description: "Provide the username from the company's LinkedIn URL (e.g., 'microsoft').",
      required: !1
    }),
    company_website: import_pieces_framework6.Property.ShortText({
      displayName: "Company Website",
      description: "Provide the company's official website URL.",
      required: !1
    }),
    request_id: import_pieces_framework6.Property.ShortText({
      displayName: "Request ID (for retries)",
      description: "If a previous run timed out, paste the Request ID here to get the result.",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c;
    let { company_name, company_username, company_website, request_id } = context.propsValue, apiKey = context.auth, COMPANY_DATA_ENDPOINT = "/company-data", requestIdToPoll = request_id, response;
    if (requestIdToPoll)
      response = await makeRequest(apiKey, import_pieces_common6.HttpMethod.POST, COMPANY_DATA_ENDPOINT, { request_id: requestIdToPoll });
    else {
      if (!company_name && !company_username && !company_website)
        throw new Error("To start a new request, you must provide a Company Name, LinkedIn Username, or Website.");
      let initialBody = {};
      company_name && (initialBody.company_name = company_name), company_username && (initialBody.company_username = company_username), company_website && (initialBody.company_website = company_website), response = await makeRequest(apiKey, import_pieces_common6.HttpMethod.POST, COMPANY_DATA_ENDPOINT, initialBody);
    }
    if ((_a = response.data) != null && _a.company_name)
      return response.data;
    if (requestIdToPoll = (_b = response.data) == null ? void 0 : _b.request_id, !requestIdToPoll)
      throw new Error("Failed to start or retrieve the request. The API did not return a request_id or the final data.");
    let maxAttempts = 20, pollInterval = 3e3;
    for (let attempt = 0; attempt < maxAttempts; attempt++) {
      await sleep4(pollInterval);
      let pollResponse = await makeRequest(apiKey, import_pieces_common6.HttpMethod.POST, COMPANY_DATA_ENDPOINT, { request_id: requestIdToPoll });
      if ((_c = pollResponse.data) != null && _c.company_name)
        return pollResponse.data;
    }
    throw new Error(
      "Timeout: The request is still processing. You can try again later with this Request ID: " + requestIdToPoll
    );
  }
});

// ../ap/packages/pieces/community/magical-api/src/lib/actions/score-resume.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var sleep5 = (ms) => new Promise((resolve) => setTimeout(resolve, ms)), scoreResume = (0, import_pieces_framework7.createAction)({
  auth: magicalApiAuth,
  name: "score_resume",
  displayName: "Score Resume",
  description: "Scores a resume against a job description and provides a reason.",
  audience: "both",
  aiMetadata: { description: "Scores how well a publicly accessible resume file (PDF/DOC/DOCX) at a given URL matches a supplied job description, returning a fit score with reasoning. Use to rank or screen candidates against a specific role. Both the resume URL and the job description are required to start a job; alternatively pass the Request ID from a prior timed-out run to retrieve that result. The call polls until scoring completes. Idempotent: re-running with the same resume and job description yields the same score.", idempotent: !0 },
  props: {
    url: import_pieces_framework7.Property.ShortText({
      displayName: "Resume URL",
      description: "Provide a direct URL to a publicly accessible resume file (PDF, DOC, or DOCX).",
      required: !1
    }),
    job_description: import_pieces_framework7.Property.LongText({
      displayName: "Job Description",
      description: "Provide the job description to score the resume against.",
      required: !1
    }),
    request_id: import_pieces_framework7.Property.ShortText({
      displayName: "Request ID (for retries)",
      description: "If a previous run timed out, paste the Request ID here to get the result.",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c;
    let { url, job_description, request_id } = context.propsValue, apiKey = context.auth, RESUME_SCORE_ENDPOINT = "/resume-score", requestIdToPoll = request_id, response;
    if (requestIdToPoll)
      response = await makeRequest(apiKey, import_pieces_common7.HttpMethod.POST, RESUME_SCORE_ENDPOINT, { request_id: requestIdToPoll });
    else {
      if (!url || !job_description)
        throw new Error("To start a new request, you must provide both a Resume URL and a Job Description.");
      response = await makeRequest(apiKey, import_pieces_common7.HttpMethod.POST, RESUME_SCORE_ENDPOINT, { url, job_description });
    }
    if (((_a = response.data) == null ? void 0 : _a.score) !== void 0)
      return response.data;
    if (requestIdToPoll = (_b = response.data) == null ? void 0 : _b.request_id, !requestIdToPoll)
      throw new Error("Failed to start or retrieve the request. The API did not return a request_id or the final data.");
    let maxAttempts = 20, pollInterval = 3e3;
    for (let attempt = 0; attempt < maxAttempts; attempt++) {
      await sleep5(pollInterval);
      let pollResponse = await makeRequest(apiKey, import_pieces_common7.HttpMethod.POST, RESUME_SCORE_ENDPOINT, { request_id: requestIdToPoll });
      if (((_c = pollResponse.data) == null ? void 0 : _c.score) !== void 0)
        return pollResponse.data;
    }
    throw new Error(
      "Timeout: Resume scoring is taking longer than expected. You can check the status later with this Request ID: " + requestIdToPoll
    );
  }
});

// ../ap/packages/pieces/community/magical-api/src/index.ts
var magicalApi = (0, import_pieces_framework8.createPiece)({
  displayName: "Magical API",
  description: "Automate resume parsing, review, scoring, and LinkedIn profile/company data retrieval with Magical API.",
  auth: magicalApiAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/magical-api.png",
  authors: ["Pranith124", "sanket-a11y"],
  categories: [
    import_pieces_framework9.PieceCategory.HUMAN_RESOURCES,
    import_pieces_framework9.PieceCategory.SALES_AND_CRM,
    import_pieces_framework9.PieceCategory.BUSINESS_INTELLIGENCE
  ],
  actions: [
    parseResume,
    reviewResume,
    getProfileData,
    getCompanyData,
    scoreResume,
    (0, import_pieces_common8.createCustomApiCallAction)({
      auth: magicalApiAuth,
      baseUrl: () => BASE_URL,
      authMapping: async (auth) => ({
        "api-key": auth.secret_text
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  magicalApi
});
