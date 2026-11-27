"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
};
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

// ../ap/packages/pieces/community/dataforb2b/src/index.ts
var index_exports = {};
__export(index_exports, {
  dataforb2b: () => dataforb2b
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/dataforb2b/src/lib/common/index.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), DATAFORB2B_API_BASE_URL = "https://api.dataforb2b.ai", dataforb2bAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "Your DataForB2B API key. Get it from https://app.dataforb2b.ai",
  validate: async ({ auth }) => {
    var _a;
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${DATAFORB2B_API_BASE_URL}/account`,
        headers: { api_key: auth }
      }), { valid: !0 };
    } catch (e) {
      let status = (_a = e == null ? void 0 : e.response) == null ? void 0 : _a.status;
      return status === 401 || status === 403 ? {
        valid: !1,
        error: "Invalid API key. Check it at https://app.dataforb2b.ai"
      } : {
        valid: !1,
        error: `Could not reach DataForB2B to validate the key (status ${status != null ? status : "network error"}). Please try again.`
      };
    }
  }
});
async function dataForB2BRequest(apiKey, endpoint, body) {
  return (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.POST,
    url: `${DATAFORB2B_API_BASE_URL}${endpoint}`,
    headers: {
      api_key: apiKey,
      "Content-Type": "application/json"
    },
    body
  })).body;
}
async function dataForB2BGet(apiKey, endpoint, queryParams) {
  return (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.GET,
    url: `${DATAFORB2B_API_BASE_URL}${endpoint}`,
    headers: { api_key: apiKey },
    queryParams
  })).body;
}
var operatorMap = {
  eq: "=",
  neq: "!=",
  gt: ">",
  gte: ">=",
  lt: "<",
  lte: "<=",
  like: "like",
  not_like: "not_like",
  in: "in",
  not_in: "not_in",
  between: "between"
}, operatorOptions = [
  { label: "Equals", value: "eq" },
  { label: "Not Equals", value: "neq" },
  { label: "Contains", value: "like" },
  { label: "Not Contains", value: "not_like" },
  { label: "In List (comma-separated)", value: "in" },
  { label: "Not In List (comma-separated)", value: "not_in" },
  { label: "Greater Than", value: "gt" },
  { label: "Greater Than or Equal", value: "gte" },
  { label: "Less Than", value: "lt" },
  { label: "Less Than or Equal", value: "lte" },
  { label: "Between (value, value2)", value: "between" }
], peopleFilterFields = [
  // Profile
  { label: "First Name", value: "first_name" },
  { label: "Last Name", value: "last_name" },
  { label: "Profile Location", value: "profile_location" },
  { label: "Profile Country", value: "profile_country" },
  { label: "Profile Industry", value: "profile_industry" },
  { label: "Follower Count", value: "follower_count" },
  { label: "Keyword (Full-text)", value: "keyword" },
  // Current Job
  { label: "Current Company", value: "current_company" },
  { label: "Current Title", value: "current_title" },
  { label: "Current Job Location", value: "current_job_location" },
  { label: "Current Company Industry", value: "current_company_industry" },
  { label: "Current Company Category", value: "current_company_category" },
  { label: "Current Company Size", value: "current_company_size" },
  { label: "Current Company ID", value: "current_company_id" },
  { label: "Current Employment Type", value: "current_employment_type" },
  { label: "Years in Current Position", value: "years_in_current_position" },
  { label: "Years at Current Company", value: "years_at_current_company" },
  {
    label: "Current Company Has Funding",
    value: "current_company_has_funding"
  },
  {
    label: "Current Company Funding Stage",
    value: "current_company_funding_stage"
  },
  { label: "Current Company Investor", value: "current_company_investor" },
  // Past Jobs
  { label: "Past Company", value: "past_company" },
  { label: "Past Title", value: "past_title" },
  { label: "Past Job Location", value: "past_job_location" },
  { label: "Past Company Industry", value: "past_company_industry" },
  { label: "Past Company Size", value: "past_company_size" },
  { label: "Past Company ID", value: "past_company_id" },
  { label: "Past Employment Type", value: "past_employment_type" },
  { label: "Years at Past Company", value: "years_at_past_company" },
  // Skills
  { label: "Skill", value: "skill" },
  // Education
  { label: "School", value: "school" },
  { label: "Degree", value: "degree" },
  { label: "Degree Level", value: "degree_level" },
  { label: "Field of Study", value: "field_of_study" },
  // Languages
  { label: "Language", value: "language" },
  { label: "Language ISO", value: "language_iso" },
  { label: "Language Proficiency", value: "language_proficiency" },
  // Certifications
  { label: "Certification", value: "certification" },
  { label: "Certification Authority", value: "certification_authority" },
  // Experience
  { label: "Years of Experience", value: "years_of_experience" },
  { label: "Number of Total Jobs", value: "num_total_jobs" },
  { label: "Is Currently Employed", value: "is_currently_employed" }
], companyFilterFields = [
  // Basic Info
  { label: "Name", value: "name" },
  { label: "Tagline", value: "tagline" },
  { label: "Description", value: "description" },
  { label: "Domain", value: "domain" },
  { label: "Universal Name", value: "universal_name" },
  { label: "Keyword (Full-text)", value: "keyword" },
  { label: "Industry", value: "industry" },
  // Size
  { label: "Employee Count", value: "employee_count" },
  // Headquarters
  { label: "Country ISO Code", value: "country_iso_code" },
  { label: "City", value: "city" },
  { label: "Region", value: "region" },
  // Offices
  { label: "Office Country", value: "office_country" },
  { label: "Office City", value: "office_city" },
  { label: "Office Region", value: "office_region" },
  // Growth
  { label: "Employee Growth 1M (%)", value: "employee_growth_1m" },
  { label: "Employee Growth 6M (%)", value: "employee_growth_6m" },
  { label: "Employee Growth 12M (%)", value: "employee_growth_12m" },
  { label: "Recent Hires Count", value: "recent_hires_count" },
  // Metadata
  { label: "Founded Year", value: "founded_year" },
  { label: "Company Type", value: "company_type" },
  { label: "Follower Count", value: "follower_count" },
  { label: "Page Verified", value: "page_verified" },
  { label: "Category", value: "category" },
  // Funding
  { label: "Last Funding Amount (USD)", value: "last_funding_amount_usd" },
  { label: "Last Funding Date", value: "last_funding_date" },
  { label: "Funding Stage", value: "funding_stage_normalized" },
  { label: "Has Funding", value: "has_funding" }
], filterLogicProp = import_pieces_framework.Property.StaticDropdown({
  displayName: "Filter Logic",
  description: "How to combine multiple filters",
  required: !1,
  defaultValue: "and",
  options: {
    options: [
      { label: "AND (all conditions must match)", value: "and" },
      { label: "OR (any condition can match)", value: "or" }
    ]
  }
});
function buildFiltersProp(fields, defaultField) {
  return import_pieces_framework.Property.Array({
    displayName: "Filters",
    description: "Conditions used to filter the search",
    required: !1,
    properties: {
      field: import_pieces_framework.Property.StaticDropdown({
        displayName: "Field",
        required: !0,
        defaultValue: defaultField,
        options: { options: fields }
      }),
      operator: import_pieces_framework.Property.StaticDropdown({
        displayName: "Operator",
        required: !0,
        defaultValue: "like",
        options: { options: operatorOptions }
      }),
      value: import_pieces_framework.Property.ShortText({
        displayName: "Value",
        description: 'For "In List" / "Not In List" use comma-separated values',
        required: !0
      }),
      value2: import_pieces_framework.Property.ShortText({
        displayName: "Value 2 (for Between)",
        description: 'Second value, only used with the "Between" operator',
        required: !1
      })
    }
  });
}
function buildConditions(filters) {
  return (filters || []).map((cond) => {
    let apiOperator = operatorMap[cond.operator] || cond.operator, condition = {
      column: cond.field,
      type: apiOperator,
      value: apiOperator === "in" || apiOperator === "not_in" ? cond.value.split(",").map((v) => v.trim()) : cond.value
    };
    return cond.operator === "between" && cond.value2 && (condition.value2 = cond.value2), condition;
  });
}
function finalizeFilters(conditions, match, advanced) {
  let op = match === "or" ? "or" : "and", group = conditions.length > 0 ? { op, conditions } : void 0;
  if (advanced && typeof advanced == "object") {
    let adv = Array.isArray(advanced) ? { op: "and", conditions: advanced } : "conditions" in advanced ? advanced : { op: "and", conditions: [advanced] };
    return group ? { op: "and", conditions: [group, adv] } : adv;
  }
  return group;
}
var advancedFiltersProp = import_pieces_framework.Property.Json({
  displayName: "Advanced Filters (JSON)",
  description: 'Optional raw filter group, e.g. {"op":"or","conditions":[{"column":"skill","type":"like","value":"python"}]}. Merged with the filters above.',
  required: !1
}), typeaheadTypeOptions = [
  { label: "Company (returns org_id)", value: "company" },
  { label: "People industry (capitalized)", value: "people_industry" },
  { label: "Company industry (lowercase)", value: "company_industry" },
  { label: "Category (lowercase)", value: "category" },
  { label: "Location (profiles)", value: "location" },
  { label: "City (companies)", value: "city" },
  { label: "Region/state (companies)", value: "region" },
  { label: "School", value: "school" },
  { label: "Job title", value: "title" },
  { label: "Skill", value: "skill" },
  { label: "Investor", value: "investor" }
];

// ../ap/packages/pieces/community/dataforb2b/src/lib/actions/search-people.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var searchPeople = (0, import_pieces_framework2.createAction)({
  auth: dataforb2bAuth,
  name: "search_people",
  displayName: "Search People",
  description: "Find professionals using 50+ filters (title, company, LinkedIn URL, skills, location, education, funding...).",
  audience: "both",
  aiMetadata: {
    description: "Search the DataForB2B people database with structured filter conditions across 50+ profile fields (current and past title, company, skills, location, education, languages, certifications, experience), combined by AND or OR and optionally merged with a raw advanced-filter JSON group. Use to build prospect or candidate lists by criteria, then pass a returned identifier to Enrich Profile for email and phone; prefer Reasoning Search when the criteria are only expressible in plain language. Requires at least one filter or an advanced filter group; read-only and idempotent.",
    idempotent: !0
  },
  props: {
    match: filterLogicProp,
    filters: buildFiltersProp(peopleFilterFields, "current_title"),
    advancedFilters: advancedFiltersProp,
    count: import_pieces_framework2.Property.Number({
      displayName: "Count",
      description: "Number of results to return (max 1000)",
      required: !1,
      defaultValue: 25
    }),
    offset: import_pieces_framework2.Property.Number({
      displayName: "Offset",
      description: "Pagination offset - number of results to skip",
      required: !1,
      defaultValue: 0
    }),
    enrichLive: import_pieces_framework2.Property.Checkbox({
      displayName: "Enrich Live",
      description: "Enrich results with fresh live data",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let { match, filters, advancedFilters, count, offset, enrichLive } = context.propsValue, conditions = buildConditions(filters != null ? filters : []), finalFilters = finalizeFilters(conditions, match != null ? match : "and", advancedFilters);
    if (!finalFilters)
      throw new Error("Provide at least one filter or an advanced filter group.");
    return dataForB2BRequest(context.auth.secret_text, "/search/people", {
      filters: finalFilters,
      count: count != null ? count : 25,
      offset: offset != null ? offset : 0,
      enrich_live: enrichLive != null ? enrichLive : !1
    });
  }
});

// ../ap/packages/pieces/community/dataforb2b/src/lib/actions/search-companies.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var searchCompanies = (0, import_pieces_framework3.createAction)({
  auth: dataforb2bAuth,
  name: "search_companies",
  displayName: "Search Companies",
  description: "Find companies with advanced filters (industry, LinkedIn URL, size, location, growth, funding, investor...).",
  audience: "both",
  aiMetadata: {
    description: "Search the DataForB2B company database with structured filter conditions over firmographic fields (name, domain, industry, employee count, location, growth, funding stage, investor), combined by AND or OR and optionally merged with a raw advanced-filter JSON group. Use to discover companies by criteria or to resolve a company to an identifier for Enrich Company; prefer Reasoning Search when the criteria are only expressible in plain language. Requires at least one filter or an advanced filter group; read-only and idempotent.",
    idempotent: !0
  },
  props: {
    match: filterLogicProp,
    filters: buildFiltersProp(companyFilterFields, "name"),
    advancedFilters: advancedFiltersProp,
    count: import_pieces_framework3.Property.Number({
      displayName: "Count",
      description: "Number of results to return (max 1000)",
      required: !1,
      defaultValue: 25
    }),
    offset: import_pieces_framework3.Property.Number({
      displayName: "Offset",
      description: "Pagination offset - number of results to skip",
      required: !1,
      defaultValue: 0
    }),
    enrichLive: import_pieces_framework3.Property.Checkbox({
      displayName: "Enrich Live",
      description: "Enrich results with fresh live data",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let { match, filters, advancedFilters, count, offset, enrichLive } = context.propsValue, conditions = buildConditions(filters != null ? filters : []), finalFilters = finalizeFilters(conditions, match != null ? match : "and", advancedFilters);
    if (!finalFilters)
      throw new Error("Provide at least one filter or an advanced filter group.");
    return dataForB2BRequest(context.auth.secret_text, "/search/companies", {
      filters: finalFilters,
      count: count != null ? count : 25,
      offset: offset != null ? offset : 0,
      enrich_live: enrichLive != null ? enrichLive : !1
    });
  }
});

// ../ap/packages/pieces/community/dataforb2b/src/lib/actions/reasoning-search.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var reasoningSearch = (0, import_pieces_framework4.createAction)({
  auth: dataforb2bAuth,
  name: "reasoning_search",
  displayName: "Reasoning Search",
  description: 'Describe who you are looking for in plain language. An AI agent builds the best filters and runs the search. It may return status "needs_input" with questions \u2014 answer by calling again with the returned session_id and an answers object.',
  audience: "both",
  aiMetadata: {
    description: "Run a plain-language people or company search in which a DataForB2B agent builds the filters, over two possible turns: a first call carrying the query, or a follow-up call carrying the session ID plus an answers object when a prior turn returned needs_input. Use when the criteria are fuzzy or unstructured; prefer Search People or Search Companies when the criteria map to explicit field filters. Requires either a query or a session ID with answers; read-only and idempotent.",
    idempotent: !0
  },
  props: {
    query: import_pieces_framework4.Property.LongText({
      displayName: "Query",
      description: 'Plain-language description, e.g. "Heads of Sales at French fintech scale-ups" (max 3000 chars). Required on the first call.',
      required: !1
    }),
    category: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Category",
      description: "Whether to search people or companies",
      required: !1,
      defaultValue: "people",
      options: {
        options: [
          { label: "People", value: "people" },
          { label: "Companies", value: "companies" }
        ]
      }
    }),
    maxResults: import_pieces_framework4.Property.Number({
      displayName: "Max Results",
      description: "Number of results for the final search (1-100)",
      required: !1,
      defaultValue: 25
    }),
    sessionId: import_pieces_framework4.Property.ShortText({
      displayName: "Session ID",
      description: "Session id returned by a previous call. Send it back to answer questions or refine the same search.",
      required: !1
    }),
    answers: import_pieces_framework4.Property.Json({
      displayName: "Answers (JSON)",
      description: 'Answers to a needs_input turn, e.g. {"company_size":"201-500"}. Requires Session ID.',
      required: !1
    }),
    enrichLive: import_pieces_framework4.Property.Checkbox({
      displayName: "Enrich Live",
      description: "Enrich results with fresh live data instead of cached data",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let { query, category, maxResults, sessionId, answers, enrichLive } = context.propsValue;
    if (!query && !(sessionId && answers))
      throw new Error(
        "Provide 'query' (first call) or 'session_id' + 'answers' (to resolve a needs_input turn)."
      );
    let body = {
      category: category != null ? category : "people",
      max_results: maxResults != null ? maxResults : 25,
      enrich_live: enrichLive != null ? enrichLive : !1
    };
    return query && (body.query = query), sessionId && (body.session_id = sessionId), answers && (body.answers = answers), dataForB2BRequest(context.auth.secret_text, "/search/reasoning", body);
  }
});

// ../ap/packages/pieces/community/dataforb2b/src/lib/actions/typeahead.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var typeahead = (0, import_pieces_framework5.createAction)({
  auth: dataforb2bAuth,
  name: "typeahead",
  displayName: "Typeahead",
  description: "Resolve the exact stored value for a search filter (company, industry, category, location, school, title, skill, investor). Use it before filtering, or when a search returns few/no results.",
  audience: "both",
  aiMetadata: {
    description: "Autocomplete a partial string against one DataForB2B reference vocabulary selected by the type prop: company (returns org_id), people industry, company industry, category, location, city, region, school, job title, skill, or investor. Use it before Search People or Search Companies to resolve a filter value to the exact stored spelling, or after a search returns few or no matches. Requires a type and a query of 1-100 characters; read-only and idempotent.",
    idempotent: !0
  },
  props: {
    type: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Type",
      description: "The category to autocomplete",
      required: !0,
      options: { options: typeaheadTypeOptions }
    }),
    q: import_pieces_framework5.Property.ShortText({
      displayName: "Query",
      description: 'Partial text to autocomplete (1-100 characters), e.g. "pyth"',
      required: !0
    }),
    limit: import_pieces_framework5.Property.Number({
      displayName: "Limit",
      description: "Maximum number of results (1-20)",
      required: !1,
      defaultValue: 20
    })
  },
  async run(context) {
    let { type, q, limit } = context.propsValue, clamped = Math.max(1, Math.min(Number(limit != null ? limit : 20), 20));
    return dataForB2BGet(context.auth.secret_text, "/typeahead", {
      type,
      q,
      limit: String(clamped)
    });
  }
});

// ../ap/packages/pieces/community/dataforb2b/src/lib/actions/enrich-profile.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var enrichProfile = (0, import_pieces_framework6.createAction)({
  auth: dataforb2bAuth,
  name: "enrich_profile",
  displayName: "Enrich Profile",
  description: "Retrieve detailed professional data, work/personal email and phone for a person.",
  audience: "both",
  aiMetadata: {
    description: "Look up one person in the DataForB2B database by LinkedIn URL, public ID, or encoded ID, with separate toggles selecting which enrichments run: full profile (the default when no toggle is set), work email, personal email, phone, and GitHub. Use when you already hold a person identifier and need their profile or contact details; use Search People instead to find people matching criteria. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    profileIdentifier: import_pieces_framework6.Property.ShortText({
      displayName: "Profile Identifier",
      description: "LinkedIn URL, public ID, or encoded ID",
      required: !0
    }),
    includeProfile: import_pieces_framework6.Property.Checkbox({
      displayName: "Enrich Profile",
      description: "Retrieve full profile data",
      required: !1,
      defaultValue: !0
    }),
    enrichWorkEmail: import_pieces_framework6.Property.Checkbox({
      displayName: "Enrich Work Email",
      description: "Retrieve professional email",
      required: !1,
      defaultValue: !1
    }),
    enrichPersonalEmail: import_pieces_framework6.Property.Checkbox({
      displayName: "Enrich Personal Email",
      description: "Retrieve personal email",
      required: !1,
      defaultValue: !1
    }),
    enrichPhone: import_pieces_framework6.Property.Checkbox({
      displayName: "Enrich Phone",
      description: "Retrieve phone number",
      required: !1,
      defaultValue: !1
    }),
    enrichGithub: import_pieces_framework6.Property.Checkbox({
      displayName: "Enrich GitHub",
      description: "Retrieve GitHub profile and repositories",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let {
      profileIdentifier,
      includeProfile,
      enrichWorkEmail,
      enrichPersonalEmail,
      enrichPhone,
      enrichGithub
    } = context.propsValue, flags = {
      enrich_profile: includeProfile != null ? includeProfile : !1,
      enrich_work_email: enrichWorkEmail != null ? enrichWorkEmail : !1,
      enrich_personal_email: enrichPersonalEmail != null ? enrichPersonalEmail : !1,
      enrich_phone: enrichPhone != null ? enrichPhone : !1,
      enrich_github: enrichGithub != null ? enrichGithub : !1
    };
    return Object.values(flags).some(Boolean) || (flags.enrich_profile = !0), dataForB2BRequest(context.auth.secret_text, "/enrich/profile", __spreadValues({
      profile_identifier: profileIdentifier
    }, flags));
  }
});

// ../ap/packages/pieces/community/dataforb2b/src/lib/actions/enrich-company.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var enrichCompany = (0, import_pieces_framework7.createAction)({
  auth: dataforb2bAuth,
  name: "enrich_company",
  displayName: "Enrich Company",
  description: "Retrieve comprehensive information about a company.",
  audience: "both",
  aiMetadata: {
    description: "Look up one company in the DataForB2B database by slug, LinkedIn company URL, or encoded company ID, returning its full firmographic record. Use when you already hold a company identifier; use Search Companies to find companies by criteria, or Typeahead to resolve a company name to its stored identifier first. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    companyIdentifier: import_pieces_framework7.Property.ShortText({
      displayName: "Company Identifier",
      description: "Company slug, LinkedIn URL, or encoded ID",
      required: !0
    })
  },
  async run(context) {
    let { companyIdentifier } = context.propsValue;
    return dataForB2BRequest(context.auth.secret_text, "/enrich/company", {
      company_identifier: companyIdentifier
    });
  }
});

// ../ap/packages/pieces/community/dataforb2b/src/index.ts
var dataforb2b = (0, import_pieces_framework8.createPiece)({
  displayName: "DataForB2B",
  description: "Power your sales or recruiting AI agent with live B2B data. DataForB2B is a people and company search API with 70+ filters including job title, skills, company size, LinkedIn URL, funding stage, investor, past employers, certifications, years of experience, GitHub repositories, languages and more.",
  auth: dataforb2bAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/dataforb2b.png",
  categories: [import_pieces_framework9.PieceCategory.SALES_AND_CRM],
  authors: ["claude-dev-code", "sanket-a11y"],
  actions: [
    searchPeople,
    searchCompanies,
    reasoningSearch,
    typeahead,
    enrichProfile,
    enrichCompany,
    (0, import_pieces_common2.createCustomApiCallAction)({
      auth: dataforb2bAuth,
      baseUrl: () => DATAFORB2B_API_BASE_URL,
      authMapping: async (auth) => ({
        api_key: auth.secret_text
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  dataforb2b
});
