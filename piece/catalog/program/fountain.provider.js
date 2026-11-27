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

// ../ap/packages/pieces/community/fountain/src/index.ts
var index_exports = {};
__export(index_exports, {
  API_BASE_URL_DEFAULT: () => API_BASE_URL_DEFAULT2,
  fountain: () => fountain
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework20 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/fountain/src/lib/actions/create-applicant.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/fountain/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), API_BASE_URL_DEFAULT = "https://api.fountain.com", fountainAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: "Enter your Fountain API key and base URL",
  required: !0,
  props: {
    apiKey: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API Key",
      description: "Enter your Fountain API key from Profile > Manage API Keys or Settings > Integrations & API Keys",
      required: !0
    }),
    baseUrl: import_pieces_framework.Property.ShortText({
      displayName: "Base URL",
      description: `The base URL for your Fountain API (default: ${API_BASE_URL_DEFAULT}). For example: us-2.fountain.com/api/v2`,
      required: !1
    })
  }
});

// ../ap/packages/pieces/community/fountain/src/lib/common/auth.ts
function getBaseUrl(auth) {
  let baseUrl = auth.props.baseUrl;
  if (baseUrl) {
    let url = baseUrl;
    return !baseUrl.startsWith("http://") && !baseUrl.startsWith("https://") && (url = `https://${baseUrl}`), url.replace(/\/$/, "");
  }
  return API_BASE_URL_DEFAULT;
}
function getApiUrl(auth, endpoint) {
  let baseUrl = getBaseUrl(auth);
  if (baseUrl.includes("/api/v2") || baseUrl.endsWith("/v2")) {
    let cleanEndpoint2 = endpoint.startsWith("/") ? endpoint : `/${endpoint}`;
    return `${baseUrl}${cleanEndpoint2}`;
  }
  let cleanEndpoint = endpoint.startsWith("/") ? endpoint : `/${endpoint}`;
  return `${baseUrl}/v2${cleanEndpoint}`;
}
function getAuthHeaders(auth) {
  return {
    Authorization: `Bearer ${auth.props.apiKey}`,
    "Content-Type": "application/json"
  };
}

// ../ap/packages/pieces/community/fountain/src/lib/actions/create-applicant.ts
var fountainCreateApplicant = (0, import_pieces_framework2.createAction)({
  name: "create_applicant",
  auth: fountainAuth,
  displayName: "Create Applicant",
  description: "Add a new applicant to your hiring pipeline",
  audience: "both",
  aiMetadata: { description: "Creates a new applicant record in Fountain from a name, email, and phone number. Use to add a candidate into the hiring pipeline. Optionally enable the duplicate check to apply the company's configured duplicate-blocking rules. Not idempotent: each call creates a new applicant.", idempotent: !1 },
  props: {
    name: import_pieces_framework2.Property.ShortText({
      displayName: "Full Name",
      description: "Applicant's full name",
      required: !0
    }),
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email",
      description: "Applicant's email address",
      required: !0
    }),
    phone_number: import_pieces_framework2.Property.ShortText({
      displayName: "Phone Number",
      description: "Applicant's complete phone number",
      required: !0
    }),
    check_if_applicant_is_duplicate: import_pieces_framework2.Property.Checkbox({
      displayName: "Check for Duplicates",
      description: "Apply additional duplicate applicant blocking rules specified in company duplicate settings",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let requestBody = {
      name: context.propsValue.name,
      email: context.propsValue.email,
      phone_number: context.propsValue.phone_number,
      check_if_applicant_is_duplicate: context.propsValue.check_if_applicant_is_duplicate
    };
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: getApiUrl(context.auth, "/applicants"),
      headers: getAuthHeaders(context.auth),
      body: requestBody
    })).body;
  }
});

// ../ap/packages/pieces/community/fountain/src/lib/actions/delete-applicant.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
async function getApplicantsDropdown(auth) {
  var _a;
  try {
    return (((_a = (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.GET,
      url: getApiUrl(auth, "/applicants"),
      headers: getAuthHeaders(auth),
      queryParams: { per_page: "50" }
    })).body) == null ? void 0 : _a.applicants) || []).map((applicant) => ({
      label: `${applicant.name} (${applicant.email})`,
      value: applicant.id
    }));
  } catch (error) {
    return [];
  }
}
var fountainDeleteApplicant = (0, import_pieces_framework3.createAction)({
  name: "delete_applicant",
  auth: fountainAuth,
  displayName: "Delete Applicant",
  description: "Delete an applicant by their ID",
  audience: "both",
  aiMetadata: { description: "Permanently deletes an applicant from Fountain by their applicant ID. Use to remove a candidate from the system. Idempotent in effect: once the applicant is gone, repeating the call has no further effect, though it is a destructive mutation.", idempotent: !0 },
  props: {
    id: import_pieces_framework3.Property.Dropdown({
      auth: fountainAuth,
      displayName: "Applicant",
      description: "The applicant to delete (shows 50 most recent applicants)",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => auth ? { disabled: !1, options: await getApplicantsDropdown(auth) } : { disabled: !0, options: [], placeholder: "Connect account first" }
    })
  },
  async run(context) {
    let applicantId = context.propsValue.id, response = await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.DELETE,
      url: getApiUrl(context.auth, `/applicants/${applicantId}`),
      headers: getAuthHeaders(context.auth)
    });
    return {
      success: response.status === 204,
      status: response.status,
      message: response.status === 204 ? "Applicant deleted successfully" : "Failed to delete applicant"
    };
  }
});

// ../ap/packages/pieces/community/fountain/src/lib/actions/get-applicant-details.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
async function getApplicantsDropdown2(auth) {
  var _a;
  try {
    return (((_a = (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.GET,
      url: getApiUrl(auth, "/applicants"),
      headers: getAuthHeaders(auth),
      queryParams: { per_page: "50" }
    })).body) == null ? void 0 : _a.applicants) || []).map((applicant) => ({
      label: `${applicant.name} (${applicant.email})`,
      value: applicant.id
    }));
  } catch (error) {
    return [];
  }
}
var fountainGetApplicantDetails = (0, import_pieces_framework4.createAction)({
  name: "get_applicant_details",
  auth: fountainAuth,
  displayName: "Get Applicant Details",
  description: "Get complete applicant information",
  audience: "both",
  aiMetadata: { description: "Retrieves the full record for a single applicant by their applicant ID. Use to look up a candidate's details, contact info, and current pipeline state. Read-only and idempotent.", idempotent: !0 },
  props: {
    id: import_pieces_framework4.Property.Dropdown({
      displayName: "Applicant",
      description: "The applicant to delete (shows 50 most recent applicants)",
      required: !0,
      refreshers: [],
      auth: fountainAuth,
      options: async ({ auth }) => auth ? { disabled: !1, options: await getApplicantsDropdown2(auth) } : { disabled: !0, options: [], placeholder: "Connect account first" }
    })
  },
  async run(context) {
    let applicantId = context.propsValue.id;
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.GET,
      url: getApiUrl(context.auth, `/applicants/${applicantId}`),
      headers: getAuthHeaders(context.auth)
    })).body;
  }
});

// ../ap/packages/pieces/community/fountain/src/lib/actions/get-interview-sessions.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
async function getApplicantsDropdown3(auth) {
  var _a;
  try {
    return (((_a = (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.GET,
      url: getApiUrl(auth, "/applicants"),
      headers: getAuthHeaders(auth),
      queryParams: { per_page: "50" }
    })).body) == null ? void 0 : _a.applicants) || []).map((applicant) => ({
      label: `${applicant.name} (${applicant.email})`,
      value: applicant.id
    }));
  } catch (error) {
    return [];
  }
}
var fountainGetInterviewSessions = (0, import_pieces_framework5.createAction)({
  name: "get_interview_sessions",
  auth: fountainAuth,
  displayName: "Get Interview Sessions",
  description: "Returns the applicant's interview sessions",
  audience: "both",
  aiMetadata: { description: "Lists the booked interview slots/sessions for a single applicant, identified by their applicant ID. Use to check a candidate's scheduled interviews. Read-only and idempotent.", idempotent: !0 },
  props: {
    id: import_pieces_framework5.Property.Dropdown({
      displayName: "Applicant",
      description: "The applicant to get interview sessions for (shows 50 most recent applicants)",
      required: !0,
      refreshers: [],
      auth: fountainAuth,
      options: async ({ auth }) => auth ? { disabled: !1, options: await getApplicantsDropdown3(auth) } : { disabled: !0, options: [], placeholder: "Connect account first" }
    })
  },
  async run(context) {
    let applicantId = context.propsValue.id;
    return (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.GET,
      url: getApiUrl(context.auth, `/applicants/${applicantId}/booked_slots`),
      headers: getAuthHeaders(context.auth)
    })).body;
  }
});

// ../ap/packages/pieces/community/fountain/src/lib/actions/get-opening.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/fountain/src/lib/common/dropdowns.ts
var import_pieces_common5 = require("@activepieces/pieces-common");
async function getFunnelsDropdown(auth) {
  var _a;
  try {
    return (((_a = (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.GET,
      url: getApiUrl(auth, "/funnels"),
      headers: getAuthHeaders(auth),
      queryParams: { per_page: "100" }
    })).body) == null ? void 0 : _a.funnels) || []).map((funnel) => ({
      label: funnel.title || `Opening ${funnel.id}`,
      value: funnel.id
    }));
  } catch (error) {
    return [];
  }
}
async function getLocationsDropdown(auth) {
  var _a;
  try {
    let funnels = ((_a = (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.GET,
      url: getApiUrl(auth, "/funnels"),
      headers: getAuthHeaders(auth),
      queryParams: { per_page: "100" }
    })).body) == null ? void 0 : _a.funnels) || [], locations = /* @__PURE__ */ new Map();
    return funnels.forEach((funnel) => {
      funnel.location && funnel.location.id && !locations.has(funnel.location.id) && locations.set(funnel.location.id, {
        label: funnel.location.name || `Location ${funnel.location.id}`,
        value: funnel.location.id
      });
    }), Array.from(locations.values());
  } catch (error) {
    return [];
  }
}
async function getUsersDropdown(auth) {
  var _a;
  try {
    let funnels = ((_a = (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.GET,
      url: getApiUrl(auth, "/funnels"),
      headers: getAuthHeaders(auth),
      queryParams: { per_page: "100" }
    })).body) == null ? void 0 : _a.funnels) || [], users = /* @__PURE__ */ new Map();
    return funnels.forEach((funnel) => {
      funnel.owner && funnel.owner.id && !users.has(funnel.owner.id) && users.set(funnel.owner.id, {
        label: funnel.owner.name || funnel.owner.email || `User ${funnel.owner.id}`,
        value: funnel.owner.id
      });
    }), Array.from(users.values());
  } catch (error) {
    return [];
  }
}
async function getStagesForFunnelDropdown(auth, funnelId) {
  var _a;
  if (!funnelId)
    return [];
  try {
    return (((_a = (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.GET,
      url: getApiUrl(auth, `/funnels/${funnelId}/stages`),
      headers: getAuthHeaders(auth)
    })).body) == null ? void 0 : _a.stages) || []).map((stage) => ({
      label: stage.title || `Stage ${stage.id}`,
      value: stage.id
    }));
  } catch (error) {
    return [];
  }
}

// ../ap/packages/pieces/community/fountain/src/lib/actions/get-opening.ts
var fountainGetOpening = (0, import_pieces_framework6.createAction)({
  name: "get_opening",
  auth: fountainAuth,
  displayName: "Get Opening",
  description: "Get details for a specific job opening",
  audience: "both",
  aiMetadata: { description: "Retrieves details for a single job opening (Fountain funnel) by its ID. Use to inspect a specific opening before working with its stages or applicants. Read-only and idempotent.", idempotent: !0 },
  props: {
    id: import_pieces_framework6.Property.Dropdown({
      displayName: "Opening",
      description: "The opening to retrieve details for",
      required: !0,
      refreshers: [],
      auth: fountainAuth,
      options: async ({ auth }) => auth ? { disabled: !1, options: await getFunnelsDropdown(auth) } : { disabled: !0, options: [], placeholder: "Connect account first" }
    })
  },
  async run(context) {
    let openingId = context.propsValue.id;
    return (await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.GET,
      url: getApiUrl(context.auth, `/funnels/${openingId}`),
      headers: getAuthHeaders(context.auth)
    })).body;
  }
});

// ../ap/packages/pieces/community/fountain/src/lib/actions/get-stage.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var fountainGetStage = (0, import_pieces_framework7.createAction)({
  name: "get_stage",
  auth: fountainAuth,
  displayName: "Get Stage",
  description: "Retrieves a specific Stage",
  audience: "both",
  aiMetadata: { description: "Retrieves details for a single hiring-pipeline stage by its stage ID (the opening/funnel is selected only to scope the stage picker). Use to inspect a specific stage within an opening. Read-only and idempotent.", idempotent: !0 },
  props: {
    funnel_id: import_pieces_framework7.Property.Dropdown({
      displayName: "Opening",
      description: "The opening that contains the stage",
      required: !0,
      refreshers: [],
      auth: fountainAuth,
      options: async ({ auth }) => auth ? { disabled: !1, options: await getFunnelsDropdown(auth) } : { disabled: !0, options: [], placeholder: "Connect account first" }
    }),
    id: import_pieces_framework7.Property.Dropdown({
      displayName: "Stage",
      description: "The stage to retrieve details for",
      required: !0,
      refreshers: ["funnel_id"],
      auth: fountainAuth,
      options: async ({ auth, funnel_id }) => auth ? funnel_id ? { disabled: !1, options: await getStagesForFunnelDropdown(auth, funnel_id) } : { disabled: !0, options: [], placeholder: "Select opening first" } : { disabled: !0, options: [], placeholder: "Connect account first" }
    })
  },
  async run(context) {
    let stageId = context.propsValue.id;
    return (await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.GET,
      url: getApiUrl(context.auth, `/stages/${stageId}`),
      headers: getAuthHeaders(context.auth)
    })).body;
  }
});

// ../ap/packages/pieces/community/fountain/src/lib/actions/list-applicants.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var fountainListApplicants = (0, import_pieces_framework8.createAction)({
  name: "list_applicants",
  auth: fountainAuth,
  displayName: "List All Applicant Details",
  description: "List applicants with optional filters",
  audience: "both",
  aiMetadata: { description: "Lists applicants in Fountain, returning all applicants when no filters are set or narrowing the results when any are supplied (by opening, location, stage, stage name, labels, or phone number). Use to find or enumerate candidates; supports cursor-based pagination via per_page and cursor. Read-only and idempotent.", idempotent: !0 },
  props: {
    funnel_id: import_pieces_framework8.Property.Dropdown({
      displayName: "Opening",
      description: "Filter applicants by opening",
      required: !1,
      refreshers: [],
      auth: fountainAuth,
      options: async ({ auth }) => auth ? { disabled: !1, options: await getFunnelsDropdown(auth) } : { disabled: !0, options: [], placeholder: "Connect account first" }
    }),
    location_id: import_pieces_framework8.Property.Dropdown({
      displayName: "Location",
      description: "Filter applicants by location",
      required: !1,
      refreshers: [],
      auth: fountainAuth,
      options: async ({ auth }) => auth ? { disabled: !1, options: await getLocationsDropdown(auth) } : { disabled: !0, options: [], placeholder: "Connect account first" }
    }),
    stage_id: import_pieces_framework8.Property.Dropdown({
      displayName: "Stage",
      description: "Filter applicants by stage",
      required: !1,
      refreshers: ["funnel_id"],
      auth: fountainAuth,
      options: async ({ auth, funnel_id }) => auth ? funnel_id ? { disabled: !1, options: await getStagesForFunnelDropdown(auth, funnel_id) } : { disabled: !0, options: [], placeholder: "Select opening first" } : { disabled: !0, options: [], placeholder: "Connect account first" }
    }),
    stage: import_pieces_framework8.Property.ShortText({
      displayName: "Stage",
      description: "Filter applicants by stage name",
      required: !1
    }),
    labels: import_pieces_framework8.Property.ShortText({
      displayName: "Labels",
      description: "Filter applicants by labels",
      required: !1
    }),
    phone: import_pieces_framework8.Property.ShortText({
      displayName: "Phone Number",
      description: "Filter applicants by phone number",
      required: !1
    }),
    exclude_temporary: import_pieces_framework8.Property.Checkbox({
      displayName: "Exclude Temporary",
      description: "Whether to exclude temporary/test applicants",
      required: !1,
      defaultValue: !1
    }),
    per_page: import_pieces_framework8.Property.Number({
      displayName: "Results Per Page",
      description: "Number of results per page (default: 25)",
      required: !1,
      defaultValue: 25
    }),
    cursor: import_pieces_framework8.Property.ShortText({
      displayName: "Cursor",
      description: "Cursor for pagination",
      required: !1
    }),
    include_subaccounts: import_pieces_framework8.Property.Checkbox({
      displayName: "Include Sub-accounts",
      description: "Whether to include applicants from sub-accounts",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let queryParams = {};
    return context.propsValue.funnel_id && (queryParams.funnel_id = context.propsValue.funnel_id), context.propsValue.location_id && (queryParams.location_id = context.propsValue.location_id), context.propsValue.stage_id && (queryParams.stage_id = context.propsValue.stage_id), context.propsValue.stage && (queryParams.stage = context.propsValue.stage), context.propsValue.labels && (queryParams.labels = context.propsValue.labels), context.propsValue.phone && (queryParams.phone = context.propsValue.phone), context.propsValue.exclude_temporary !== void 0 && (queryParams.exclude_temporary = context.propsValue.exclude_temporary), context.propsValue.per_page && (queryParams.per_page = context.propsValue.per_page), context.propsValue.cursor && (queryParams.cursor = context.propsValue.cursor), context.propsValue.include_subaccounts !== void 0 && (queryParams.include_subaccounts = context.propsValue.include_subaccounts), (await import_pieces_common8.httpClient.sendRequest({
      method: import_pieces_common8.HttpMethod.GET,
      url: getApiUrl(context.auth, "/applicants"),
      headers: getAuthHeaders(context.auth),
      queryParams
    })).body;
  }
});

// ../ap/packages/pieces/community/fountain/src/lib/actions/list-openings.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var fountainListOpenings = (0, import_pieces_framework9.createAction)({
  name: "list_openings",
  auth: fountainAuth,
  displayName: "List All Opening Details",
  description: "List job openings with optional filters",
  audience: "both",
  aiMetadata: { description: "Lists job openings (Fountain funnels), returning all openings when no filters are set or narrowing the results when any are supplied (active-only, location, hiring vs. sourcing funnel, private-only, or owner). Use to find or enumerate openings; supports cursor-based pagination via per_page and cursor. Read-only and idempotent.", idempotent: !0 },
  props: {
    active: import_pieces_framework9.Property.Checkbox({
      displayName: "Active Only",
      description: "Filter to only active openings",
      required: !1
    }),
    location_id: import_pieces_framework9.Property.Dropdown({
      displayName: "Location",
      description: "Filter openings by location",
      required: !1,
      refreshers: [],
      auth: fountainAuth,
      options: async ({ auth }) => auth ? { disabled: !1, options: await getLocationsDropdown(auth) } : { disabled: !0, options: [], placeholder: "Connect account first" }
    }),
    is_hiring_funnel: import_pieces_framework9.Property.Checkbox({
      displayName: "Hiring Funnels Only",
      description: "Filter to only hiring funnels",
      required: !1
    }),
    is_sourcing_funnel: import_pieces_framework9.Property.Checkbox({
      displayName: "Sourcing Funnels Only",
      description: "Filter to only sourcing funnels",
      required: !1
    }),
    is_private: import_pieces_framework9.Property.Checkbox({
      displayName: "Private Only",
      description: "Filter to only private openings",
      required: !1
    }),
    owner_id: import_pieces_framework9.Property.Dropdown({
      displayName: "Owner",
      description: "Filter openings by owner",
      required: !1,
      refreshers: [],
      auth: fountainAuth,
      options: async ({ auth }) => auth ? { disabled: !1, options: await getUsersDropdown(auth) } : { disabled: !0, options: [], placeholder: "Connect account first" }
    }),
    per_page: import_pieces_framework9.Property.Number({
      displayName: "Results Per Page",
      description: "Number of results per page (default: 25)",
      required: !1,
      defaultValue: 25
    }),
    cursor: import_pieces_framework9.Property.ShortText({
      displayName: "Cursor",
      description: "Cursor for pagination",
      required: !1
    })
  },
  async run(context) {
    let queryParams = {};
    return context.propsValue.active !== void 0 && (queryParams.active = context.propsValue.active), context.propsValue.location_id && (queryParams.location_id = context.propsValue.location_id), context.propsValue.is_hiring_funnel !== void 0 && (queryParams.is_hiring_funnel = context.propsValue.is_hiring_funnel), context.propsValue.is_sourcing_funnel !== void 0 && (queryParams.is_sourcing_funnel = context.propsValue.is_sourcing_funnel), context.propsValue.is_private !== void 0 && (queryParams.is_private = context.propsValue.is_private), context.propsValue.owner_id && (queryParams.owner_id = context.propsValue.owner_id), context.propsValue.per_page && (queryParams.per_page = context.propsValue.per_page), context.propsValue.cursor && (queryParams.cursor = context.propsValue.cursor), (await import_pieces_common9.httpClient.sendRequest({
      method: import_pieces_common9.HttpMethod.GET,
      url: getApiUrl(context.auth, "/funnels"),
      headers: getAuthHeaders(context.auth),
      queryParams
    })).body;
  }
});

// ../ap/packages/pieces/community/fountain/src/lib/actions/list-stages.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var fountainListStages = (0, import_pieces_framework10.createAction)({
  name: "list_stages",
  auth: fountainAuth,
  displayName: "List All Stages",
  description: "Retrieves an Opening's stages",
  audience: "both",
  aiMetadata: { description: "Lists all hiring-pipeline stages for a given opening (Fountain funnel), identified by its funnel ID. Use to discover the stages of an opening, e.g. before moving or filtering applicants. Read-only and idempotent.", idempotent: !0 },
  props: {
    funnel_id: import_pieces_framework10.Property.Dropdown({
      displayName: "Opening",
      description: "The opening to get stages for",
      required: !0,
      refreshers: [],
      auth: fountainAuth,
      options: async ({ auth }) => auth ? { disabled: !1, options: await getFunnelsDropdown(auth) } : { disabled: !0, options: [], placeholder: "Connect account first" }
    })
  },
  async run(context) {
    let funnelId = context.propsValue.funnel_id;
    return (await import_pieces_common10.httpClient.sendRequest({
      method: import_pieces_common10.HttpMethod.GET,
      url: getApiUrl(context.auth, `/funnels/${funnelId}/stages`),
      headers: getAuthHeaders(context.auth)
    })).body;
  }
});

// ../ap/packages/pieces/community/fountain/src/lib/actions/update-applicant.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
async function getApplicantsDropdown4(auth) {
  var _a;
  try {
    return (((_a = (await import_pieces_common11.httpClient.sendRequest({
      method: import_pieces_common11.HttpMethod.GET,
      url: getApiUrl(auth, "/applicants"),
      headers: getAuthHeaders(auth),
      queryParams: { per_page: "50" }
    })).body) == null ? void 0 : _a.applicants) || []).map((applicant) => ({
      label: `${applicant.name} (${applicant.email})`,
      value: applicant.id
    }));
  } catch (error) {
    return [];
  }
}
var fountainUpdateApplicant = (0, import_pieces_framework11.createAction)({
  name: "update_applicant",
  auth: fountainAuth,
  displayName: "Update Applicant Info",
  description: "Update an applicant's information",
  audience: "both",
  aiMetadata: { description: "Updates an existing applicant by their applicant ID, setting any of name, email, phone number, custom data, secure data, or rejection reason (only supplied fields are sent). Use to edit a candidate's record. Effectively idempotent: re-sending the same field values leaves the applicant in the same state.", idempotent: !0 },
  props: {
    id: import_pieces_framework11.Property.Dropdown({
      displayName: "Applicant",
      description: "The applicant to update (shows 50 most recent applicants)",
      required: !0,
      refreshers: [],
      auth: fountainAuth,
      options: async ({ auth }) => auth ? { disabled: !1, options: await getApplicantsDropdown4(auth) } : { disabled: !0, options: [], placeholder: "Connect account first" }
    }),
    name: import_pieces_framework11.Property.ShortText({
      displayName: "Name",
      description: "Applicant's full name",
      required: !1
    }),
    email: import_pieces_framework11.Property.ShortText({
      displayName: "Email",
      description: "Applicant's email address",
      required: !1
    }),
    phone_number: import_pieces_framework11.Property.ShortText({
      displayName: "Phone Number",
      description: "Applicant's complete phone number",
      required: !1
    }),
    data: import_pieces_framework11.Property.Object({
      displayName: "Custom Data",
      description: "Custom data fields to update (key-value pairs)",
      required: !1
    }),
    secure_data: import_pieces_framework11.Property.Object({
      displayName: "Secure Data",
      description: "Secure data fields to update (key-value pairs like SSN, etc.)",
      required: !1
    }),
    rejection_reason: import_pieces_framework11.Property.ShortText({
      displayName: "Rejection Reason",
      description: "Manually set reason for rejection",
      required: !1
    })
  },
  async run(context) {
    let applicantId = context.propsValue.id, requestBody = {};
    return context.propsValue.name && (requestBody.name = context.propsValue.name), context.propsValue.email && (requestBody.email = context.propsValue.email), context.propsValue.phone_number && (requestBody.phone_number = context.propsValue.phone_number), context.propsValue.data && (requestBody.data = context.propsValue.data), context.propsValue.secure_data && (requestBody.secure_data = context.propsValue.secure_data), context.propsValue.rejection_reason && (requestBody.rejection_reason = context.propsValue.rejection_reason), (await import_pieces_common11.httpClient.sendRequest({
      method: import_pieces_common11.HttpMethod.PUT,
      url: getApiUrl(context.auth, `/applicants/${applicantId}`),
      headers: getAuthHeaders(context.auth),
      body: requestBody
    })).body;
  }
});

// ../ap/packages/pieces/community/fountain/src/lib/triggers/applicant-webhook.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_framework13 = require("@activepieces/pieces-framework"), setupMarkdown = `
## Setup Instructions

1. Go to your Fountain account Settings > Automations
2. Click "Create automation"
3. Select "Applicant" as the source
4. Choose your trigger conditions (e.g., when applicant status changes)
5. Select "Send a webhook..." as the action
6. Copy and paste the webhook URL below into the URL field
7. Set the Authentication key (this will be sent as Authorization header)
8. Make sure to select "Send applicant payload" in the webhook options

**Webhook URL:**
\`\`\`text
{{webhookUrl}}
\`\`\`
`, fountainApplicantWebhook = (0, import_pieces_framework12.createTrigger)({
  name: "applicant_webhook",
  displayName: "Applicant Webhook",
  description: "Triggers when Fountain sends applicant webhook events",
  aiMetadata: {
    description: "Fires when a Fountain automation sends an applicant webhook event (e.g. an applicant is created or their status/stage changes), per the conditions configured on the Fountain automation. The event payload represents the applicant. Requires a webhook to be set up in Fountain pointing at this trigger's URL."
  },
  props: {
    setup: import_pieces_framework12.Property.MarkDown({
      value: setupMarkdown,
      variant: import_pieces_framework13.MarkdownVariant.INFO
    }),
    verify_auth: import_pieces_framework12.Property.Checkbox({
      displayName: "Verify Authentication",
      description: "Verify the Authorization header matches the expected key",
      required: !1,
      defaultValue: !1
    }),
    auth_key: import_pieces_framework12.Property.ShortText({
      displayName: "Authentication Key",
      description: "The authentication key set in Fountain webhook configuration",
      required: !1
    })
  },
  sampleData: {
    applicant: {
      id: "12345",
      name: "John Doe",
      email: "john.doe@example.com",
      phone_number: "+1234567890",
      created_at: "2024-01-15T10:30:00Z",
      updated_at: "2024-01-15T10:30:00Z",
      funnel: {
        id: "67890",
        title: "Software Engineer Position"
      },
      stage: {
        id: "abc123",
        title: "Interview Scheduled"
      }
    }
  },
  type: import_pieces_framework12.TriggerStrategy.WEBHOOK,
  async onEnable() {
  },
  async onDisable() {
  },
  async run(context) {
    let payload = context.payload.body;
    if (context.propsValue.verify_auth && context.propsValue.auth_key) {
      let authHeader = context.payload.headers.authorization;
      if (!authHeader || authHeader !== context.propsValue.auth_key)
        throw new Error("Authentication failed");
    }
    return [payload];
  }
});

// ../ap/packages/pieces/community/fountain/src/lib/triggers/worker-webhook.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_framework15 = require("@activepieces/pieces-framework"), setupMarkdown2 = `
## Setup Instructions

1. Go to your Fountain account Settings > Automations
2. Click "Create automation"
3. Select "Worker" as the source
4. Choose your trigger conditions (e.g., when worker status changes)
5. Select "Send a webhook..." as the action
6. Copy and paste the webhook URL below into the URL field
7. Set the Authentication key (this will be sent as Authorization header)
8. Make sure to select "Send worker payload" in the webhook options

**Webhook URL:**
\`\`\`text
{{webhookUrl}}
\`\`\`
`, fountainWorkerWebhook = (0, import_pieces_framework14.createTrigger)({
  name: "worker_webhook",
  displayName: "Worker Webhook",
  description: "Triggers when Fountain sends worker webhook events",
  aiMetadata: {
    description: "Fires when a Fountain automation sends a worker webhook event (e.g. a worker is created or their status changes), per the conditions configured on the Fountain automation. The event payload represents the worker. Requires a webhook to be set up in Fountain pointing at this trigger's URL."
  },
  props: {
    setup: import_pieces_framework14.Property.MarkDown({
      value: setupMarkdown2,
      variant: import_pieces_framework15.MarkdownVariant.INFO
    }),
    verify_auth: import_pieces_framework14.Property.Checkbox({
      displayName: "Verify Authentication",
      description: "Verify the Authorization header matches the expected key",
      required: !1,
      defaultValue: !1
    }),
    auth_key: import_pieces_framework14.Property.ShortText({
      displayName: "Authentication Key",
      description: "The authentication key set in Fountain webhook configuration",
      required: !1
    })
  },
  sampleData: {
    worker: {
      _id: "6916f97536b4690e60234ecf",
      createdAt: "2025-11-14T09:42:13.646Z",
      firstname: "Test",
      lastname: "Profile",
      displayFullName: "Test Profile",
      personalEmail: {
        email: "whatever@example.com"
      },
      employmentStatus: {
        type: "created",
        subtype: "employee"
      },
      customAttributes: [
        {
          customAttributeUuid: "56606812-8f05-4ea3-8a17-642da842317c",
          key: "requirement_drivers_license_status",
          value: "OUT"
        }
      ],
      company: {
        uuid: "4651064c-ac9a-4063-9fe1-1c356843a93c",
        name: "Fountain"
      }
    }
  },
  type: import_pieces_framework14.TriggerStrategy.WEBHOOK,
  async onEnable() {
  },
  async onDisable() {
  },
  async run(context) {
    let payload = context.payload.body;
    if (context.propsValue.verify_auth && context.propsValue.auth_key) {
      let authHeader = context.payload.headers.authorization;
      if (!authHeader || authHeader !== context.propsValue.auth_key)
        throw new Error("Authentication failed");
    }
    return [payload];
  }
});

// ../ap/packages/pieces/community/fountain/src/lib/triggers/custom-attribute-webhook.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework"), import_pieces_framework17 = require("@activepieces/pieces-framework"), setupMarkdown3 = `
## Setup Instructions

1. Go to your Fountain account Settings > Worker Attributes
2. Find the custom attribute you want to monitor
3. Click "..." > Manage Webhooks
4. In the modal, click "Add Webhook"
5. Copy and paste the webhook URL below into the URL field
6. Save the webhook configuration

**Webhook URL:**
\`\`\`text
{{webhookUrl}}
\`\`\`

This trigger will fire whenever the specified custom attribute's value changes for any worker.
`, fountainCustomAttributeWebhook = (0, import_pieces_framework16.createTrigger)({
  name: "custom_attribute_webhook",
  displayName: "Custom Attribute Webhook",
  description: "Triggers when Fountain worker custom attribute values change",
  aiMetadata: {
    description: "Fires when the value of a monitored worker custom attribute changes in Fountain, carrying both the previous and new state of that attribute for the affected worker. Requires the webhook to be registered on the specific custom attribute in Fountain settings."
  },
  props: {
    setup: import_pieces_framework16.Property.MarkDown({
      value: setupMarkdown3,
      variant: import_pieces_framework17.MarkdownVariant.INFO
    })
  },
  sampleData: {
    webhookUuid: "36ea7dc3-2378-4769-a749-54387dea22c6",
    companyUuid: "b69527e2-cf03-45e4-b667-48db36133391",
    trigger: {
      type: "customAttribute",
      resourceIdentifier: "583ff255-459d-47b5-9f9f-b3a300c22c02"
    },
    action: "update",
    timestamp: "2024-07-28T13:06:11.443Z",
    workerUuid: "3b8d5848-c96a-4639-ac7d-132fe36a43f0",
    previousState: {
      customAttributeUuid: "583ff255-459d-47b5-9f9f-b3a300c22c02",
      customAttribute: {
        label: "test",
        dataType: "string",
        key: "test",
        readOnly: !1,
        hidden: !1,
        protected: !1,
        uuid: "583ff255-459d-47b5-9f9f-b3a300c22c02"
      },
      value: "before"
    },
    newState: {
      customAttributeUuid: "583ff255-459d-47b5-9f9f-b3a300c22c02",
      customAttribute: {
        label: "test",
        dataType: "string",
        key: "test",
        readOnly: !1,
        hidden: !1,
        protected: !1,
        uuid: "583ff255-459d-47b5-9f9f-b3a300c22c02"
      },
      value: "after"
    }
  },
  type: import_pieces_framework16.TriggerStrategy.WEBHOOK,
  async onEnable() {
  },
  async onDisable() {
  },
  async run(context) {
    let payload = context.payload.body;
    return !payload.trigger || payload.trigger.type !== "customAttribute" ? [] : [payload];
  }
});

// ../ap/packages/pieces/community/fountain/src/lib/triggers/universal-tasks-webhook.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework"), import_pieces_framework19 = require("@activepieces/pieces-framework"), setupMarkdown4 = `
## Setup Instructions

1. Go to your Fountain Onboard system
2. Add a Universal Task with Webhook integration
3. Select the trigger type (task completion or flow end)
4. Copy and paste the webhook URL below into the webhook configuration
5. Publish the flow

**Webhook URL:**
\`\`\`text
{{webhookUrl}}
\`\`\`

This trigger will fire when workers complete tasks or reach flow endpoints in Onboard.
`, fountainUniversalTasksWebhook = (0, import_pieces_framework18.createTrigger)({
  name: "universal_tasks_webhook",
  displayName: "Universal Tasks Webhook",
  description: "Triggers when workers complete tasks or reach flow endpoints in Fountain Onboard",
  aiMetadata: {
    description: "Fires when a worker completes a Universal Task or reaches a flow endpoint in Fountain Onboard, carrying the worker and the affected task(s). Requires a Universal Task with webhook integration configured in Fountain Onboard pointing at this trigger's URL."
  },
  props: {
    setup: import_pieces_framework18.Property.MarkDown({
      value: setupMarkdown4,
      variant: import_pieces_framework19.MarkdownVariant.INFO
    })
  },
  sampleData: {
    worker: {
      uuid: "b897bd4d-3bd3-4e98-aa94-69cba600c679",
      firstName: "Harry",
      lastName: "Potter",
      startDate: "2024-08-07",
      email: "harry.potter+fyd9xrr@example.com",
      customAttributes: {
        "Compliance Drivers License": {
          url: null,
          complianceCheckTemplateUuid: "85fb6026-be74-4fe2-9054-f806f577a852"
        },
        "In Compliance": !1,
        "Onboard completed": !1
      },
      workerPageUrl: "http://localhost:8200/employer/workers/b897bd4d-3bd3-4e98-aa94-69cba600c679"
    },
    tasks: [
      {
        title: "Welcome to the team!",
        uuid: "5bed4436-2ef0-4f0f-a49f-539cf7e4671a",
        status: "completed",
        type: "informationSharing",
        data: {
          inputData: [],
          w4Profile: null,
          i9Profile: null,
          hirePapiProfile: null
        }
      }
    ],
    flowTitle: "webhook"
  },
  type: import_pieces_framework18.TriggerStrategy.WEBHOOK,
  async onEnable() {
  },
  async onDisable() {
  },
  async run(context) {
    let payload = context.payload.body;
    return !payload.worker || !payload.tasks || !payload.flowTitle ? [] : [payload];
  }
});

// ../ap/packages/pieces/community/fountain/src/index.ts
var API_BASE_URL_DEFAULT2 = "https://api.fountain.com", fountain = (0, import_pieces_framework20.createPiece)({
  displayName: "Fountain",
  description: "Automate your complete HR hiring and onboarding workflow. Manage applicants, job openings, interview scheduling, and track progress through hiring stages with real-time webhooks.",
  auth: fountainAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/fountain.png",
  authors: ["onyedikachi-david"],
  actions: [fountainCreateApplicant, fountainDeleteApplicant, fountainGetApplicantDetails, fountainGetInterviewSessions, fountainGetOpening, fountainGetStage, fountainListApplicants, fountainListOpenings, fountainListStages, fountainUpdateApplicant],
  triggers: [fountainApplicantWebhook, fountainWorkerWebhook, fountainCustomAttributeWebhook, fountainUniversalTasksWebhook]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  API_BASE_URL_DEFAULT,
  fountain
});
