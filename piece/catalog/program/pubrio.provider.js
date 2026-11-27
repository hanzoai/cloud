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

// ../ap/packages/pieces/community/pubrio/src/index.ts
var index_exports = {};
__export(index_exports, {
  pubrio: () => pubrio,
  pubrioAuth: () => pubrioAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework29 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/pubrio/src/lib/actions/search-companies.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/pubrio/src/lib/common.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.pubrio.com";
async function pubrioRequest(apiKey, method, endpoint, body) {
  let key = String(apiKey);
  return (await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${BASE_URL}${endpoint}`,
    headers: {
      "pubrio-api-key": key,
      "Content-Type": "application/json"
    },
    body: body && Object.keys(body).length > 0 ? body : void 0
  })).body;
}

// ../ap/packages/pieces/community/pubrio/src/lib/actions/search-companies.ts
var searchCompanies = (0, import_pieces_framework.createAction)({
  auth: pubrioAuth,
  name: "search_companies",
  displayName: "Search Companies",
  description: "Search B2B companies by name, domain, location, industry, technology, or headcount",
  audience: "both",
  aiMetadata: {
    description: "Discover B2B companies matching many optional filters: name, domain, location, industry vertical, technology stack, headcount, revenue, founding year, hiring/job signals, news, and ad activity, with pagination (max 25 per page) and optional similarity search. Read-only and repeatable. Pick this for criteria-based prospecting; use Enrich Company when you already have one company identifier or Lookup Lookalike to find peers of a single seed company.",
    idempotent: !0
  },
  props: {
    company_name: import_pieces_framework.Property.ShortText({
      displayName: "Company Name",
      required: !1
    }),
    domains: import_pieces_framework.Property.Array({
      displayName: "Domains",
      required: !1
    }),
    linkedin_urls: import_pieces_framework.Property.Array({
      displayName: "LinkedIn URLs",
      description: "LinkedIn company URLs",
      required: !1
    }),
    locations: import_pieces_framework.Property.Array({
      displayName: "Locations",
      description: "Location codes",
      required: !1
    }),
    exclude_locations: import_pieces_framework.Property.Array({
      displayName: "Exclude Locations",
      description: "ISO country codes to exclude",
      required: !1
    }),
    places: import_pieces_framework.Property.Array({
      displayName: "Places",
      description: "Place names",
      required: !1
    }),
    exclude_places: import_pieces_framework.Property.Array({
      displayName: "Exclude Places",
      description: "Place names to exclude",
      required: !1
    }),
    keywords: import_pieces_framework.Property.Array({
      displayName: "Keywords",
      required: !1
    }),
    verticals: import_pieces_framework.Property.Array({
      displayName: "Verticals",
      description: "Industry verticals",
      required: !1
    }),
    vertical_categories: import_pieces_framework.Property.Array({
      displayName: "Vertical Categories",
      description: "Vertical category IDs",
      required: !1
    }),
    vertical_sub_categories: import_pieces_framework.Property.Array({
      displayName: "Vertical Sub-Categories",
      description: "Vertical sub-category IDs",
      required: !1
    }),
    technologies: import_pieces_framework.Property.Array({
      displayName: "Technologies",
      required: !1
    }),
    categories: import_pieces_framework.Property.Array({
      displayName: "Technology Categories",
      description: "Technology category IDs",
      required: !1
    }),
    companies: import_pieces_framework.Property.Array({
      displayName: "Companies",
      description: "Company domain_search_id UUIDs",
      required: !1
    }),
    employees_min: import_pieces_framework.Property.Number({
      displayName: "Min Employees",
      required: !1
    }),
    employees_max: import_pieces_framework.Property.Number({
      displayName: "Max Employees",
      required: !1
    }),
    revenue_min: import_pieces_framework.Property.Number({
      displayName: "Min Revenue",
      required: !1
    }),
    revenue_max: import_pieces_framework.Property.Number({
      displayName: "Max Revenue",
      required: !1
    }),
    founded_year_start: import_pieces_framework.Property.Number({
      displayName: "Founded Year Start",
      description: "Minimum founding year",
      required: !1
    }),
    founded_year_end: import_pieces_framework.Property.Number({
      displayName: "Founded Year End",
      description: "Maximum founding year",
      required: !1
    }),
    is_enable_similarity_search: import_pieces_framework.Property.Checkbox({
      displayName: "Enable Similarity Search",
      required: !1,
      defaultValue: !1
    }),
    similarity_score: import_pieces_framework.Property.Number({
      displayName: "Similarity Score",
      description: "0-1",
      required: !1
    }),
    exclude_fields: import_pieces_framework.Property.Array({
      displayName: "Exclude Fields",
      description: "Field names to exclude",
      required: !1
    }),
    job_titles: import_pieces_framework.Property.Array({
      displayName: "Job Titles",
      required: !1
    }),
    job_locations: import_pieces_framework.Property.Array({
      displayName: "Job Locations",
      description: "Country codes for job locations",
      required: !1
    }),
    job_exclude_locations: import_pieces_framework.Property.Array({
      displayName: "Job Exclude Locations",
      description: "Country codes to exclude",
      required: !1
    }),
    job_posted_dates: import_pieces_framework.Property.Array({
      displayName: "Job Posted Dates",
      description: "Date range YYYY-MM-DD",
      required: !1
    }),
    news_categories: import_pieces_framework.Property.Array({
      displayName: "News Categories",
      description: "News category slugs",
      required: !1
    }),
    news_published_dates: import_pieces_framework.Property.Array({
      displayName: "News Published Dates",
      description: "Date range YYYY-MM-DD",
      required: !1
    }),
    news_galleries: import_pieces_framework.Property.Array({
      displayName: "News Galleries",
      description: "News gallery slugs",
      required: !1
    }),
    news_gallery_ids: import_pieces_framework.Property.Array({
      displayName: "News Gallery IDs",
      description: "News gallery UUIDs",
      required: !1
    }),
    advertisement_search_terms: import_pieces_framework.Property.Array({
      displayName: "Advertisement Search Terms",
      description: "Keywords",
      required: !1
    }),
    advertisement_target_locations: import_pieces_framework.Property.Array({
      displayName: "Advertisement Target Locations",
      description: "Country codes",
      required: !1
    }),
    advertisement_exclude_target_locations: import_pieces_framework.Property.Array({
      displayName: "Advertisement Exclude Target Locations",
      description: "Country codes",
      required: !1
    }),
    advertisement_start_dates: import_pieces_framework.Property.Array({
      displayName: "Advertisement Start Dates",
      description: "Date range YYYY-MM-DD",
      required: !1
    }),
    advertisement_end_dates: import_pieces_framework.Property.Array({
      displayName: "Advertisement End Dates",
      description: "Date range YYYY-MM-DD",
      required: !1
    }),
    page: import_pieces_framework.Property.Number({
      displayName: "Page",
      required: !1,
      defaultValue: 1
    }),
    per_page: import_pieces_framework.Property.Number({
      displayName: "Per Page",
      description: "Max 25",
      required: !1,
      defaultValue: 25
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h;
    let body = {
      page: (_a = context.propsValue.page) != null ? _a : 1,
      per_page: (_b = context.propsValue.per_page) != null ? _b : 25
    };
    return context.propsValue.company_name && (body.company_name = context.propsValue.company_name), context.propsValue.domains && (body.domains = context.propsValue.domains), context.propsValue.linkedin_urls && (body.linkedin_urls = context.propsValue.linkedin_urls), context.propsValue.locations && (body.locations = context.propsValue.locations), context.propsValue.exclude_locations && (body.exclude_locations = context.propsValue.exclude_locations), context.propsValue.places && (body.places = context.propsValue.places), context.propsValue.exclude_places && (body.exclude_places = context.propsValue.exclude_places), context.propsValue.keywords && (body.keywords = context.propsValue.keywords), context.propsValue.verticals && (body.verticals = context.propsValue.verticals), context.propsValue.vertical_categories && (body.vertical_categories = context.propsValue.vertical_categories), context.propsValue.vertical_sub_categories && (body.vertical_sub_categories = context.propsValue.vertical_sub_categories), context.propsValue.technologies && (body.technologies = context.propsValue.technologies), context.propsValue.categories && (body.categories = context.propsValue.categories), context.propsValue.companies && (body.companies = context.propsValue.companies), (context.propsValue.employees_min != null || context.propsValue.employees_max != null) && (body.employees = [
      (_c = context.propsValue.employees_min) != null ? _c : 1,
      (_d = context.propsValue.employees_max) != null ? _d : 1e6
    ]), (context.propsValue.revenue_min != null || context.propsValue.revenue_max != null) && (body.revenues = [
      (_e = context.propsValue.revenue_min) != null ? _e : 0,
      (_f = context.propsValue.revenue_max) != null ? _f : 999999999
    ]), (context.propsValue.founded_year_start != null || context.propsValue.founded_year_end != null) && (body.founded_dates = [
      (_g = context.propsValue.founded_year_start) != null ? _g : 1800,
      (_h = context.propsValue.founded_year_end) != null ? _h : (/* @__PURE__ */ new Date()).getFullYear()
    ]), context.propsValue.is_enable_similarity_search && (body.is_enable_similarity_search = context.propsValue.is_enable_similarity_search), context.propsValue.similarity_score != null && (body.similarity_score = context.propsValue.similarity_score), context.propsValue.exclude_fields && (body.exclude_fields = context.propsValue.exclude_fields), context.propsValue.job_titles && (body.job_titles = context.propsValue.job_titles), context.propsValue.job_locations && (body.job_locations = context.propsValue.job_locations), context.propsValue.job_exclude_locations && (body.job_exclude_locations = context.propsValue.job_exclude_locations), context.propsValue.job_posted_dates && (body.job_posted_dates = context.propsValue.job_posted_dates), context.propsValue.news_categories && (body.news_categories = context.propsValue.news_categories), context.propsValue.news_published_dates && (body.news_published_dates = context.propsValue.news_published_dates), context.propsValue.news_galleries && (body.news_galleries = context.propsValue.news_galleries), context.propsValue.news_gallery_ids && (body.news_gallery_ids = context.propsValue.news_gallery_ids), context.propsValue.advertisement_search_terms && (body.advertisement_search_terms = context.propsValue.advertisement_search_terms), context.propsValue.advertisement_target_locations && (body.advertisement_target_locations = context.propsValue.advertisement_target_locations), context.propsValue.advertisement_exclude_target_locations && (body.advertisement_exclude_target_locations = context.propsValue.advertisement_exclude_target_locations), context.propsValue.advertisement_start_dates && (body.advertisement_start_dates = context.propsValue.advertisement_start_dates), context.propsValue.advertisement_end_dates && (body.advertisement_end_dates = context.propsValue.advertisement_end_dates), await pubrioRequest(
      context.auth.secret_text,
      import_pieces_common2.HttpMethod.POST,
      "/companies/search",
      body
    );
  }
});

// ../ap/packages/pieces/community/pubrio/src/lib/actions/lookup-company.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var lookupCompany = (0, import_pieces_framework2.createAction)({
  auth: pubrioAuth,
  name: "lookup_company",
  displayName: "Lookup Company",
  description: "Look up detailed company information by domain or LinkedIn URL",
  audience: "both",
  aiMetadata: {
    description: "Enrich a single known company and return its firmographic profile, given one identifier selected via lookup_type: domain, LinkedIn URL, domain_search_id, or domain_id (integer). Read-only and repeatable. Use when you already have one company and want its details; to discover companies from criteria use a search action, and for live LinkedIn scraping use Company LinkedIn Lookup.",
    idempotent: !0
  },
  props: {
    lookup_type: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Lookup Type",
      required: !0,
      options: {
        options: [
          { label: "Domain", value: "domain" },
          { label: "LinkedIn URL", value: "linkedin_url" },
          { label: "Domain Search ID", value: "domain_search_id" },
          { label: "Domain ID", value: "domain_id" }
        ]
      }
    }),
    value: import_pieces_framework2.Property.ShortText({
      displayName: "Value",
      required: !0,
      description: "Domain, LinkedIn URL, Domain Search ID, or Domain ID (integer)"
    })
  },
  async run(context) {
    let lookupType = context.propsValue.lookup_type, rawValue = context.propsValue.value, lookupValue = rawValue;
    if (lookupType === "domain_id") {
      let parsed = parseInt(rawValue, 10);
      if (isNaN(parsed))
        throw new Error(
          `domain_id must be a valid integer, got: "${rawValue}"`
        );
      lookupValue = parsed;
    }
    let body = {
      [lookupType]: lookupValue
    };
    return await pubrioRequest(
      context.auth.secret_text,
      import_pieces_common3.HttpMethod.POST,
      "/companies/lookup",
      body
    );
  }
});

// ../ap/packages/pieces/community/pubrio/src/lib/actions/enrich-company.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var enrichCompany = (0, import_pieces_framework3.createAction)({
  auth: pubrioAuth,
  name: "enrich_company",
  displayName: "Enrich Company",
  description: "Get enriched company data with full firmographic details (uses credits)",
  audience: "both",
  aiMetadata: {
    description: "Enrich a single known company by domain, LinkedIn URL, domain search ID, or numeric domain ID to retrieve full firmographic details. A read-only lookup that is safe to repeat (it consumes credits but does not change data). Pick this when you already have one company identifier; use Search Companies to discover companies by criteria.",
    idempotent: !0
  },
  props: {
    lookup_type: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Lookup Type",
      required: !0,
      options: {
        options: [
          { label: "Domain", value: "domain" },
          { label: "LinkedIn URL", value: "linkedin_url" },
          { label: "Domain Search ID", value: "domain_search_id" },
          { label: "Domain ID", value: "domain_id" }
        ]
      }
    }),
    value: import_pieces_framework3.Property.ShortText({
      displayName: "Value",
      required: !0,
      description: "Domain, LinkedIn URL, Domain Search ID, or Domain ID (integer)"
    })
  },
  async run(context) {
    let lookupType = context.propsValue.lookup_type, rawValue = context.propsValue.value, lookupValue = rawValue;
    if (lookupType === "domain_id") {
      let parsed = parseInt(rawValue, 10);
      if (isNaN(parsed))
        throw new Error(
          `domain_id must be a valid integer, got: "${rawValue}"`
        );
      lookupValue = parsed;
    }
    let body = {
      [lookupType]: lookupValue
    };
    return await pubrioRequest(
      context.auth.secret_text,
      import_pieces_common4.HttpMethod.POST,
      "/companies/lookup/enrich",
      body
    );
  }
});

// ../ap/packages/pieces/community/pubrio/src/lib/actions/search-people.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var searchPeople = (0, import_pieces_framework4.createAction)({
  auth: pubrioAuth,
  name: "search_people",
  displayName: "Search People",
  description: "Search business professionals by name, title, department, seniority, or company",
  audience: "both",
  aiMetadata: {
    description: "Search the people database for business professionals using filters such as name, job title, management level, department, location, employer company/domain, or LinkedIn URL, with paging (per_page max 25). Read-only and repeatable; results identify people but do not include contact details. Use to discover prospects, then pass a people_search_id to Reveal Contact (credit cost) or Lookup Person.",
    idempotent: !0
  },
  props: {
    search_term: import_pieces_framework4.Property.ShortText({
      displayName: "Search Term",
      required: !1
    }),
    people_name: import_pieces_framework4.Property.ShortText({
      displayName: "Person Name",
      required: !1
    }),
    people_titles: import_pieces_framework4.Property.Array({
      displayName: "Job Titles",
      description: "",
      required: !1
    }),
    peoples: import_pieces_framework4.Property.Array({
      displayName: "People IDs",
      description: "People UUIDs",
      required: !1
    }),
    management_levels: import_pieces_framework4.Property.Array({
      displayName: "Management Levels",
      required: !1
    }),
    departments: import_pieces_framework4.Property.Array({
      displayName: "Departments",
      required: !1
    }),
    department_functions: import_pieces_framework4.Property.Array({
      displayName: "Department Functions",
      required: !1
    }),
    employees: import_pieces_framework4.Property.Array({
      displayName: "Employee Ranges",
      description: "Employee count ranges",
      required: !1
    }),
    people_locations: import_pieces_framework4.Property.Array({
      displayName: "People Locations",
      required: !1
    }),
    company_locations: import_pieces_framework4.Property.Array({
      displayName: "Company Locations",
      required: !1
    }),
    company_linkedin_urls: import_pieces_framework4.Property.Array({
      displayName: "Company LinkedIn URLs",
      required: !1
    }),
    linkedin_urls: import_pieces_framework4.Property.Array({
      displayName: "People LinkedIn URLs",
      required: !1
    }),
    companies: import_pieces_framework4.Property.Array({
      displayName: "Companies",
      description: "Company UUIDs",
      required: !1
    }),
    domains: import_pieces_framework4.Property.Array({
      displayName: "Company Domains",
      required: !1
    }),
    page: import_pieces_framework4.Property.Number({
      displayName: "Page",
      required: !1,
      defaultValue: 1
    }),
    per_page: import_pieces_framework4.Property.Number({
      displayName: "Per Page",
      description: "Max 25",
      required: !1,
      defaultValue: 25
    })
  },
  async run(context) {
    var _a, _b;
    let body = {
      page: (_a = context.propsValue.page) != null ? _a : 1,
      per_page: (_b = context.propsValue.per_page) != null ? _b : 25
    };
    return context.propsValue.search_term && (body.search_term = context.propsValue.search_term), context.propsValue.people_name && (body.people_name = context.propsValue.people_name), context.propsValue.people_titles && (body.people_titles = context.propsValue.people_titles), context.propsValue.peoples && (body.peoples = context.propsValue.peoples), context.propsValue.management_levels && (body.management_levels = context.propsValue.management_levels), context.propsValue.departments && (body.departments = context.propsValue.departments), context.propsValue.department_functions && (body.department_functions = context.propsValue.department_functions), context.propsValue.employees && (body.employees = context.propsValue.employees), context.propsValue.people_locations && (body.people_locations = context.propsValue.people_locations), context.propsValue.company_locations && (body.company_locations = context.propsValue.company_locations), context.propsValue.company_linkedin_urls && (body.company_linkedin_urls = context.propsValue.company_linkedin_urls), context.propsValue.linkedin_urls && (body.linkedin_urls = context.propsValue.linkedin_urls), context.propsValue.companies && (body.companies = context.propsValue.companies), context.propsValue.domains && (body.domains = context.propsValue.domains), await pubrioRequest(
      context.auth.secret_text,
      import_pieces_common5.HttpMethod.POST,
      "/people/search",
      body
    );
  }
});

// ../ap/packages/pieces/community/pubrio/src/lib/actions/lookup-person.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var lookupPerson = (0, import_pieces_framework5.createAction)({
  auth: pubrioAuth,
  name: "lookup_person",
  displayName: "Lookup Person",
  description: "Look up a person's professional profile by LinkedIn URL or Pubrio ID",
  audience: "both",
  aiMetadata: {
    description: "Enrich a single known person and return their professional profile, identified via lookup_type: LinkedIn URL or people_search_id. Read-only and repeatable; the profile does not include revealed contact details. Use when you already have one person and want their details (use Reveal Contact for email/phone, which costs credits); to discover people from criteria use Search People.",
    idempotent: !0
  },
  props: {
    lookup_type: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Lookup Type",
      required: !0,
      options: {
        options: [
          { label: "LinkedIn URL", value: "linkedin_url" },
          { label: "People Search ID", value: "people_search_id" }
        ]
      }
    }),
    value: import_pieces_framework5.Property.ShortText({ displayName: "Value", required: !0 })
  },
  async run(context) {
    let body = {
      [context.propsValue.lookup_type]: context.propsValue.value
    };
    return await pubrioRequest(
      context.auth.secret_text,
      import_pieces_common6.HttpMethod.POST,
      "/people/lookup",
      body
    );
  }
});

// ../ap/packages/pieces/community/pubrio/src/lib/actions/reveal-contact.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var revealContact = (0, import_pieces_framework6.createAction)({
  auth: pubrioAuth,
  name: "reveal_contact",
  displayName: "Reveal Contact",
  description: "Reveal email or phone number for a person (uses credits)",
  audience: "both",
  aiMetadata: {
    description: "Reveal a person's contact details (work/personal email and/or phone) for a single individual identified by people_search_id or LinkedIn URL. This consumes account credits and is not idempotent in cost, so call it only once contacts are actually needed. Use for one person at a time after locating them via Search People or Lookup Person.",
    idempotent: !1
  },
  props: {
    lookup_type: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Lookup Type",
      required: !0,
      options: {
        options: [
          { label: "People Search ID", value: "people_search_id" },
          { label: "LinkedIn URL", value: "linkedin_url" }
        ]
      }
    }),
    value: import_pieces_framework6.Property.ShortText({
      displayName: "Value",
      required: !0,
      description: "People Search ID or LinkedIn URL"
    }),
    people_contact_types: import_pieces_framework6.Property.StaticMultiSelectDropdown({
      displayName: "Contact Types",
      required: !1,
      options: {
        options: [
          { label: "Work Email", value: "email-work" },
          { label: "Personal Email", value: "email-personal" },
          { label: "Phone", value: "phone" }
        ]
      }
    })
  },
  async run(context) {
    let body = {
      [context.propsValue.lookup_type]: context.propsValue.value
    };
    return context.propsValue.people_contact_types && context.propsValue.people_contact_types.length > 0 && (body.people_contact_types = context.propsValue.people_contact_types), await pubrioRequest(
      context.auth.secret_text,
      import_pieces_common7.HttpMethod.POST,
      "/redeem/people",
      body
    );
  }
});

// ../ap/packages/pieces/community/pubrio/src/lib/actions/search-jobs.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var searchJobs = (0, import_pieces_framework7.createAction)({
  auth: pubrioAuth,
  name: "search_jobs",
  displayName: "Search Jobs",
  description: "Search job postings across companies",
  audience: "both",
  aiMetadata: {
    description: "Search job postings across companies, filtering by keywords, job titles, posted-date ranges, locations (or excluded locations), and target companies/domains/LinkedIn URLs, with paging (per_page max 25). Read-only and repeatable. Use to find hiring signals or open roles, then pass a job_search_id to Lookup Job for full detail.",
    idempotent: !0
  },
  props: {
    search_term: import_pieces_framework7.Property.ShortText({
      displayName: "Search Term",
      required: !1
    }),
    search_terms: import_pieces_framework7.Property.Array({
      displayName: "Search Terms",
      required: !1
    }),
    titles: import_pieces_framework7.Property.Array({
      displayName: "Job Titles",
      required: !1
    }),
    posted_dates: import_pieces_framework7.Property.Array({
      displayName: "Posted Dates",
      description: "Date filters",
      required: !1
    }),
    locations: import_pieces_framework7.Property.Array({
      displayName: "Locations",
      required: !1
    }),
    exclude_locations: import_pieces_framework7.Property.Array({
      displayName: "Exclude Locations",
      description: "Locations to exclude",
      required: !1
    }),
    company_locations: import_pieces_framework7.Property.Array({
      displayName: "Company Locations",
      required: !1
    }),
    companies: import_pieces_framework7.Property.Array({
      displayName: "Companies",
      description: "Company UUIDs",
      required: !1
    }),
    domains: import_pieces_framework7.Property.Array({
      displayName: "Company Domains",
      required: !1
    }),
    linkedin_urls: import_pieces_framework7.Property.Array({
      displayName: "LinkedIn URLs",
      required: !1
    }),
    page: import_pieces_framework7.Property.Number({
      displayName: "Page",
      required: !1,
      defaultValue: 1
    }),
    per_page: import_pieces_framework7.Property.Number({
      displayName: "Per Page",
      description: "Max 25",
      required: !1,
      defaultValue: 25
    })
  },
  async run(context) {
    var _a, _b;
    let body = {
      page: (_a = context.propsValue.page) != null ? _a : 1,
      per_page: (_b = context.propsValue.per_page) != null ? _b : 25
    };
    return context.propsValue.search_term && (body.search_term = context.propsValue.search_term), context.propsValue.search_terms && (body.search_terms = context.propsValue.search_terms), context.propsValue.titles && (body.titles = context.propsValue.titles), context.propsValue.posted_dates && (body.posted_dates = context.propsValue.posted_dates), context.propsValue.locations && (body.locations = context.propsValue.locations), context.propsValue.exclude_locations && (body.exclude_locations = context.propsValue.exclude_locations), context.propsValue.company_locations && (body.company_locations = context.propsValue.company_locations), context.propsValue.companies && (body.companies = context.propsValue.companies), context.propsValue.domains && (body.domains = context.propsValue.domains), context.propsValue.linkedin_urls && (body.linkedin_urls = context.propsValue.linkedin_urls), await pubrioRequest(
      context.auth.secret_text,
      import_pieces_common8.HttpMethod.POST,
      "/companies/jobs/search",
      body
    );
  }
});

// ../ap/packages/pieces/community/pubrio/src/lib/actions/search-news.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var searchNews = (0, import_pieces_framework8.createAction)({
  auth: pubrioAuth,
  name: "search_news",
  displayName: "Search News",
  description: "Search company news and press releases",
  audience: "both",
  aiMetadata: {
    description: "Search company news articles and press releases by keyword, company domain, category, location, or publish date, with pagination (max 25 per page). Read-only and repeatable. Pick this for news/PR monitoring of companies; use Search Companies for firmographic discovery instead.",
    idempotent: !0
  },
  props: {
    search_term: import_pieces_framework8.Property.ShortText({
      displayName: "Search Term",
      required: !1
    }),
    search_terms: import_pieces_framework8.Property.Array({
      displayName: "Search Terms",
      description: "Keywords",
      required: !1
    }),
    domains: import_pieces_framework8.Property.Array({
      displayName: "Company Domains",
      required: !1
    }),
    companies: import_pieces_framework8.Property.Array({
      displayName: "Companies",
      description: "Company domain_search_id UUIDs",
      required: !1
    }),
    categories: import_pieces_framework8.Property.Array({
      displayName: "News Categories",
      required: !1
    }),
    published_dates: import_pieces_framework8.Property.Array({
      displayName: "Published Dates",
      description: "Date filters",
      required: !1
    }),
    locations: import_pieces_framework8.Property.Array({
      displayName: "Locations",
      required: !1
    }),
    company_locations: import_pieces_framework8.Property.Array({
      displayName: "Company Locations",
      required: !1
    }),
    news_gallery_ids: import_pieces_framework8.Property.Array({
      displayName: "News Gallery IDs",
      description: "Gallery IDs",
      required: !1
    }),
    news_galleries: import_pieces_framework8.Property.Array({
      displayName: "News Galleries",
      description: "Gallery names",
      required: !1
    }),
    news_languages: import_pieces_framework8.Property.Array({
      displayName: "News Languages",
      description: "Language codes",
      required: !1
    }),
    page: import_pieces_framework8.Property.Number({
      displayName: "Page",
      required: !1,
      defaultValue: 1
    }),
    per_page: import_pieces_framework8.Property.Number({
      displayName: "Per Page",
      description: "Max 25",
      required: !1,
      defaultValue: 25
    })
  },
  async run(context) {
    var _a, _b;
    let body = {
      page: (_a = context.propsValue.page) != null ? _a : 1,
      per_page: (_b = context.propsValue.per_page) != null ? _b : 25
    };
    return context.propsValue.search_term && (body.search_term = context.propsValue.search_term), context.propsValue.search_terms && (body.search_terms = context.propsValue.search_terms), context.propsValue.domains && (body.domains = context.propsValue.domains), context.propsValue.companies && (body.companies = context.propsValue.companies), context.propsValue.categories && (body.categories = context.propsValue.categories), context.propsValue.published_dates && (body.published_dates = context.propsValue.published_dates), context.propsValue.locations && (body.locations = context.propsValue.locations), context.propsValue.company_locations && (body.company_locations = context.propsValue.company_locations), context.propsValue.news_gallery_ids && (body.news_gallery_ids = context.propsValue.news_gallery_ids), context.propsValue.news_galleries && (body.news_galleries = context.propsValue.news_galleries), context.propsValue.news_languages && (body.news_languages = context.propsValue.news_languages), await pubrioRequest(
      context.auth.secret_text,
      import_pieces_common9.HttpMethod.POST,
      "/companies/news/search",
      body
    );
  }
});

// ../ap/packages/pieces/community/pubrio/src/lib/actions/find-similar-companies.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var findSimilarCompanies = (0, import_pieces_framework9.createAction)({
  auth: pubrioAuth,
  name: "find_similar_companies",
  displayName: "Find Similar Companies",
  description: "Find companies similar to a given company by domain or LinkedIn URL",
  audience: "both",
  aiMetadata: {
    description: "Return a paged list of companies that resemble one seed company, identified via lookup_type (domain or LinkedIn URL). Read-only and repeatable (per_page max 25). Use for lookalike prospecting from a known account; to enrich that one seed company instead use Lookup Company.",
    idempotent: !0
  },
  props: {
    lookup_type: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Lookup Type",
      required: !0,
      options: {
        options: [
          { label: "Domain", value: "domain" },
          { label: "LinkedIn URL", value: "linkedin_url" }
        ]
      }
    }),
    value: import_pieces_framework9.Property.ShortText({ displayName: "Value", required: !0 }),
    page: import_pieces_framework9.Property.Number({
      displayName: "Page",
      required: !1,
      defaultValue: 1
    }),
    per_page: import_pieces_framework9.Property.Number({
      displayName: "Per Page",
      description: "Max 25",
      required: !1,
      defaultValue: 25
    })
  },
  async run(context) {
    var _a, _b;
    let body = {
      [context.propsValue.lookup_type]: context.propsValue.value,
      page: (_a = context.propsValue.page) != null ? _a : 1,
      per_page: (_b = context.propsValue.per_page) != null ? _b : 25
    };
    return await pubrioRequest(
      context.auth.secret_text,
      import_pieces_common10.HttpMethod.POST,
      "/companies/lookalikes/search",
      body
    );
  }
});

// ../ap/packages/pieces/community/pubrio/src/lib/actions/lookup-technology.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var lookupTechnology = (0, import_pieces_framework10.createAction)({
  auth: pubrioAuth,
  name: "lookup_technology",
  displayName: "Lookup Technology",
  description: "Look up technologies used by a company",
  audience: "both",
  aiMetadata: {
    description: "Return the technology stack (technographics) detected for a single company, identified via lookup_type: domain, LinkedIn URL, domain_search_id, or domain_id (integer). Read-only and repeatable. Use to learn what tools/software a known company uses; for general firmographic detail use Lookup Company instead.",
    idempotent: !0
  },
  props: {
    lookup_type: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Lookup Type",
      required: !0,
      options: {
        options: [
          { label: "Domain", value: "domain" },
          { label: "LinkedIn URL", value: "linkedin_url" },
          { label: "Domain Search ID", value: "domain_search_id" },
          { label: "Domain ID", value: "domain_id" }
        ]
      }
    }),
    value: import_pieces_framework10.Property.ShortText({
      displayName: "Value",
      required: !0,
      description: "Domain, LinkedIn URL, Domain Search ID, or Domain ID (integer)"
    })
  },
  async run(context) {
    let lookupType = context.propsValue.lookup_type, rawValue = context.propsValue.value, body = {};
    if (lookupType === "domain_id") {
      let parsed = parseInt(rawValue, 10);
      if (isNaN(parsed))
        throw new Error(`domain_id must be a valid integer, got: "${rawValue}"`);
      body[lookupType] = parsed;
    } else
      body[lookupType] = rawValue;
    return await pubrioRequest(
      context.auth.secret_text,
      import_pieces_common11.HttpMethod.POST,
      "/technologies/lookup",
      body
    );
  }
});

// ../ap/packages/pieces/community/pubrio/src/lib/actions/linkedin-person-lookup.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common");
var linkedinPersonLookup = (0, import_pieces_framework11.createAction)({
  auth: pubrioAuth,
  name: "linkedin_person_lookup",
  displayName: "People LinkedIn Lookup",
  description: "Real-time LinkedIn person lookup by LinkedIn URL",
  audience: "both",
  aiMetadata: {
    description: "Resolve a single person profile in real time directly from their LinkedIn profile URL. Read-only and repeatable. Use when you only have a LinkedIn URL and want fresh data; prefer Lookup Person when you have a people_search_id, since this path triggers a live LinkedIn fetch.",
    idempotent: !0
  },
  props: {
    people_linkedin_url: import_pieces_framework11.Property.ShortText({
      displayName: "LinkedIn URL",
      required: !0,
      description: "Person LinkedIn profile URL"
    })
  },
  async run(context) {
    let body = {
      people_linkedin_url: context.propsValue.people_linkedin_url
    };
    return await pubrioRequest(
      context.auth.secret_text,
      import_pieces_common12.HttpMethod.POST,
      "/people/linkedin/lookup",
      body
    );
  }
});

// ../ap/packages/pieces/community/pubrio/src/lib/actions/search-ads.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common13 = require("@activepieces/pieces-common");
var searchAds = (0, import_pieces_framework12.createAction)({
  auth: pubrioAuth,
  name: "search_ads",
  displayName: "Search Advertisements",
  description: "Search company advertisements and ad campaigns",
  audience: "both",
  aiMetadata: {
    description: "Search the advertisement database for company ad campaigns, filtering by keywords, headlines, target/company locations, date ranges, domains, LinkedIn URLs, or company IDs, with paging (per_page max 25). Read-only and repeatable. Use to discover which companies are running ads or to find ad creatives; pass an advertisement_search_id from the results to the Lookup Advertisement action for full detail.",
    idempotent: !0
  },
  props: {
    search_terms: import_pieces_framework12.Property.Array({
      displayName: "Search Terms",
      required: !1
    }),
    target_locations: import_pieces_framework12.Property.Array({
      displayName: "Target Locations",
      required: !1
    }),
    exclude_target_locations: import_pieces_framework12.Property.Array({
      displayName: "Exclude Target Locations",
      description: "Locations to exclude",
      required: !1
    }),
    headlines: import_pieces_framework12.Property.Array({
      displayName: "Headlines",
      description: "Headline keywords",
      required: !1
    }),
    start_dates: import_pieces_framework12.Property.Array({
      displayName: "Start Dates",
      description: "Start date filters",
      required: !1
    }),
    end_dates: import_pieces_framework12.Property.Array({
      displayName: "End Dates",
      description: "End date filters",
      required: !1
    }),
    company_locations: import_pieces_framework12.Property.Array({
      displayName: "Company Locations",
      required: !1
    }),
    domains: import_pieces_framework12.Property.Array({
      displayName: "Company Domains",
      required: !1
    }),
    linkedin_urls: import_pieces_framework12.Property.Array({
      displayName: "LinkedIn URLs",
      required: !1
    }),
    companies: import_pieces_framework12.Property.Array({
      displayName: "Companies",
      description: "Company domain_search_id UUIDs",
      required: !1
    }),
    page: import_pieces_framework12.Property.Number({
      displayName: "Page",
      required: !1,
      defaultValue: 1
    }),
    per_page: import_pieces_framework12.Property.Number({
      displayName: "Per Page",
      description: "Max 25",
      required: !1,
      defaultValue: 25
    })
  },
  async run(context) {
    var _a, _b;
    let body = {
      page: (_a = context.propsValue.page) != null ? _a : 1,
      per_page: (_b = context.propsValue.per_page) != null ? _b : 25
    };
    return context.propsValue.search_terms && (body.search_terms = context.propsValue.search_terms), context.propsValue.target_locations && (body.target_locations = context.propsValue.target_locations), context.propsValue.exclude_target_locations && (body.exclude_target_locations = context.propsValue.exclude_target_locations), context.propsValue.headlines && (body.headlines = context.propsValue.headlines), context.propsValue.start_dates && (body.start_dates = context.propsValue.start_dates), context.propsValue.end_dates && (body.end_dates = context.propsValue.end_dates), context.propsValue.company_locations && (body.company_locations = context.propsValue.company_locations), context.propsValue.domains && (body.domains = context.propsValue.domains), context.propsValue.linkedin_urls && (body.linkedin_urls = context.propsValue.linkedin_urls), context.propsValue.companies && (body.companies = context.propsValue.companies), await pubrioRequest(
      context.auth.secret_text,
      import_pieces_common13.HttpMethod.POST,
      "/companies/advertisements/search",
      body
    );
  }
});

// ../ap/packages/pieces/community/pubrio/src/lib/actions/linkedin-company-lookup.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common14 = require("@activepieces/pieces-common");
var linkedinCompanyLookup = (0, import_pieces_framework13.createAction)({
  auth: pubrioAuth,
  name: "linkedin_company_lookup",
  displayName: "Company LinkedIn Lookup",
  description: "Real-time LinkedIn company lookup by LinkedIn URL",
  audience: "both",
  aiMetadata: {
    description: "Resolve a company profile in real time directly from its LinkedIn company URL. Read-only and repeatable. Use when you only have a LinkedIn URL and want fresh data; prefer Lookup Company when you have a domain or internal ID, since this path triggers a live LinkedIn fetch.",
    idempotent: !0
  },
  props: {
    linkedin_url: import_pieces_framework13.Property.ShortText({
      displayName: "LinkedIn URL",
      required: !0,
      description: "Company LinkedIn profile URL"
    })
  },
  async run(context) {
    let body = {
      linkedin_url: context.propsValue.linkedin_url
    };
    return await pubrioRequest(
      context.auth.secret_text,
      import_pieces_common14.HttpMethod.POST,
      "/companies/linkedin/lookup",
      body
    );
  }
});

// ../ap/packages/pieces/community/pubrio/src/lib/actions/lookup-job.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_common15 = require("@activepieces/pieces-common");
var lookupJob = (0, import_pieces_framework14.createAction)({
  auth: pubrioAuth,
  name: "lookup_job",
  displayName: "Lookup Job",
  description: "Look up detailed job information by job search ID",
  audience: "both",
  aiMetadata: {
    description: "Fetch the full detail of one job posting given a job_search_id (obtained from the Search Jobs results). Read-only and repeatable. Use after Search Jobs to expand a single result; to discover jobs from criteria use Search Jobs instead.",
    idempotent: !0
  },
  props: {
    job_search_id: import_pieces_framework14.Property.ShortText({
      displayName: "Job Search ID",
      required: !0,
      description: "The job search ID to look up"
    })
  },
  async run(context) {
    let body = {
      job_search_id: context.propsValue.job_search_id
    };
    return await pubrioRequest(
      context.auth.secret_text,
      import_pieces_common15.HttpMethod.POST,
      "/companies/jobs/lookup",
      body
    );
  }
});

// ../ap/packages/pieces/community/pubrio/src/lib/actions/lookup-news.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework"), import_pieces_common16 = require("@activepieces/pieces-common");
var lookupNews = (0, import_pieces_framework15.createAction)({
  auth: pubrioAuth,
  name: "lookup_news",
  displayName: "Lookup News",
  description: "Look up detailed news information by news search ID",
  audience: "both",
  aiMetadata: {
    description: "Fetch the full detail of one news item given a news_search_id (obtained from a prior news search). Read-only and repeatable. Use to expand a single news result into its complete record.",
    idempotent: !0
  },
  props: {
    news_search_id: import_pieces_framework15.Property.ShortText({
      displayName: "News Search ID",
      required: !0,
      description: "The news search ID to look up"
    })
  },
  async run(context) {
    let body = {
      news_search_id: context.propsValue.news_search_id
    };
    return await pubrioRequest(
      context.auth.secret_text,
      import_pieces_common16.HttpMethod.POST,
      "/companies/news/lookup",
      body
    );
  }
});

// ../ap/packages/pieces/community/pubrio/src/lib/actions/lookup-advertisement.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework"), import_pieces_common17 = require("@activepieces/pieces-common");
var lookupAdvertisement = (0, import_pieces_framework16.createAction)({
  auth: pubrioAuth,
  name: "lookup_advertisement",
  displayName: "Lookup Advertisement",
  description: "Look up detailed advertisement information by advertisement search ID",
  audience: "both",
  aiMetadata: {
    description: "Fetch the full detail of one advertisement given an advertisement_search_id (obtained from Search Advertisements). Read-only and repeatable. Use after Search Advertisements to expand a single ad result.",
    idempotent: !0
  },
  props: {
    advertisement_search_id: import_pieces_framework16.Property.ShortText({
      displayName: "Advertisement Search ID",
      required: !0,
      description: "The advertisement search ID to look up"
    })
  },
  async run(context) {
    let body = {
      advertisement_search_id: context.propsValue.advertisement_search_id
    };
    return await pubrioRequest(
      context.auth.secret_text,
      import_pieces_common17.HttpMethod.POST,
      "/companies/advertisements/lookup",
      body
    );
  }
});

// ../ap/packages/pieces/community/pubrio/src/lib/actions/lookup-lookalike.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework"), import_pieces_common18 = require("@activepieces/pieces-common");
var lookupLookalike = (0, import_pieces_framework17.createAction)({
  auth: pubrioAuth,
  name: "lookup_lookalike",
  displayName: "Lookup Lookalike Company",
  description: "Look up a similar company result by domain, LinkedIn URL, or domain search ID",
  audience: "both",
  aiMetadata: {
    description: "Find companies similar to a given seed company, identified by its domain, LinkedIn URL, or domain search ID. Read-only and repeatable. Pick this for lookalike/competitor discovery from one reference company; use Search Companies for criteria-based discovery and Enrich Company for full details on one firm.",
    idempotent: !0
  },
  props: {
    lookup_type: import_pieces_framework17.Property.StaticDropdown({
      displayName: "Lookup Type",
      required: !0,
      options: {
        options: [
          { label: "Domain", value: "domain" },
          { label: "LinkedIn URL", value: "linkedin_url" },
          { label: "Domain Search ID", value: "domain_search_id" }
        ]
      }
    }),
    value: import_pieces_framework17.Property.ShortText({
      displayName: "Value",
      required: !0,
      description: "Domain, LinkedIn URL, or Domain Search ID"
    })
  },
  async run(context) {
    let body = {
      [context.propsValue.lookup_type]: context.propsValue.value
    };
    return await pubrioRequest(
      context.auth.secret_text,
      import_pieces_common18.HttpMethod.POST,
      "/companies/lookalikes/lookup",
      body
    );
  }
});

// ../ap/packages/pieces/community/pubrio/src/lib/actions/batch-redeem-contacts.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework"), import_pieces_common19 = require("@activepieces/pieces-common");
var batchRedeemContacts = (0, import_pieces_framework18.createAction)({
  auth: pubrioAuth,
  name: "batch_redeem_contacts",
  displayName: "Batch Redeem Contacts",
  description: "Reveal email or phone numbers for multiple people at once (uses credits)",
  audience: "both",
  aiMetadata: {
    description: "Reveal contact details (work/personal email or phone) for a batch of people identified by their search IDs. Not idempotent: each call consumes account credits per reveal, so avoid retrying the same IDs unnecessarily.",
    idempotent: !1
  },
  props: {
    peoples: import_pieces_framework18.Property.Array({
      displayName: "People Search IDs",
      required: !0,
      description: "People search IDs"
    }),
    people_contact_types: import_pieces_framework18.Property.StaticMultiSelectDropdown({
      displayName: "Contact Types",
      required: !1,
      options: {
        options: [
          { label: "Work Email", value: "email-work" },
          { label: "Personal Email", value: "email-personal" },
          { label: "Phone", value: "phone" }
        ]
      }
    })
  },
  async run(context) {
    let body = {
      peoples: context.propsValue.peoples
    };
    return context.propsValue.people_contact_types && context.propsValue.people_contact_types.length > 0 && (body.people_contact_types = context.propsValue.people_contact_types), await pubrioRequest(
      context.auth.secret_text,
      import_pieces_common19.HttpMethod.POST,
      "/redeem/people/batch",
      body
    );
  }
});

// ../ap/packages/pieces/community/pubrio/src/lib/actions/query-batch-redeem.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework"), import_pieces_common20 = require("@activepieces/pieces-common");
var queryBatchRedeem = (0, import_pieces_framework19.createAction)({
  auth: pubrioAuth,
  name: "query_batch_redeem",
  displayName: "Query Batch Redeem",
  description: "Query the status and results of a batch redeem operation",
  audience: "both",
  aiMetadata: {
    description: "Poll the status and results of a previously submitted bulk people-redeem (contact reveal) job using its redeem_query_id. Read-only and safe to call repeatedly; it does not start or modify a redeem and consumes no credits itself. Use after a batch redeem has been queued to fetch the resolved emails/phones once ready.",
    idempotent: !0
  },
  props: {
    redeem_query_id: import_pieces_framework19.Property.ShortText({
      displayName: "Redeem Query ID",
      required: !0,
      description: "The batch redeem query ID"
    })
  },
  async run(context) {
    let body = {
      redeem_query_id: context.propsValue.redeem_query_id
    };
    return await pubrioRequest(
      context.auth.secret_text,
      import_pieces_common20.HttpMethod.POST,
      "/redeem/people/batch/query",
      body
    );
  }
});

// ../ap/packages/pieces/community/pubrio/src/lib/actions/create-monitor.ts
var import_pieces_framework20 = require("@activepieces/pieces-framework"), import_pieces_common21 = require("@activepieces/pieces-common");
var createMonitor = (0, import_pieces_framework20.createAction)({
  auth: pubrioAuth,
  name: "create_monitor",
  displayName: "Create Monitor",
  description: "Create a new signal monitor for jobs, news, or advertisements",
  audience: "both",
  aiMetadata: {
    description: "Create a new signal monitor that watches selected companies for jobs, news, and/or advertisement signals and delivers matches to a webhook, email, or sequence. Not idempotent: each call creates a separate monitor, so calling twice yields duplicates. Use to set up ongoing monitoring; to change an existing monitor use Update Monitor.",
    idempotent: !1
  },
  props: {
    name: import_pieces_framework20.Property.ShortText({
      displayName: "Name",
      required: !0,
      description: "Monitor name"
    }),
    detection_mode: import_pieces_framework20.Property.StaticDropdown({
      displayName: "Detection Mode",
      required: !0,
      options: {
        options: [
          { label: "Company First", value: "company_first" },
          { label: "Signal First", value: "signal_first" }
        ]
      }
    }),
    signal_types: import_pieces_framework20.Property.StaticMultiSelectDropdown({
      displayName: "Signal Types",
      required: !0,
      options: {
        options: [
          { label: "Jobs", value: "jobs" },
          { label: "News", value: "news" },
          { label: "Advertisements", value: "advertisements" }
        ]
      }
    }),
    destination_type: import_pieces_framework20.Property.StaticDropdown({
      displayName: "Destination Type",
      required: !0,
      options: {
        options: [
          { label: "Webhook", value: "webhook" },
          { label: "Email", value: "email" },
          { label: "Sequences", value: "sequences" }
        ]
      }
    }),
    webhook_url: import_pieces_framework20.Property.ShortText({
      displayName: "Webhook URL",
      required: !1,
      description: "Required when destination type is webhook"
    }),
    email: import_pieces_framework20.Property.ShortText({
      displayName: "Email",
      required: !1,
      description: "Required when destination type is email"
    }),
    sequence_identifier: import_pieces_framework20.Property.ShortText({
      displayName: "Sequence Identifier",
      required: !1,
      description: "Required when destination type is sequences"
    }),
    record_type: import_pieces_framework20.Property.ShortText({
      displayName: "Record Type",
      required: !1,
      description: "Required when destination type is sequences"
    }),
    description: import_pieces_framework20.Property.LongText({
      displayName: "Description",
      required: !1
    }),
    frequency_minute: import_pieces_framework20.Property.Number({
      displayName: "Frequency (minutes)",
      required: !1
    }),
    max_daily_trigger: import_pieces_framework20.Property.Number({
      displayName: "Max Daily Triggers",
      required: !1
    }),
    max_records_per_trigger: import_pieces_framework20.Property.Number({
      displayName: "Max Records Per Trigger",
      required: !1
    }),
    companies: import_pieces_framework20.Property.Array({
      displayName: "Companies",
      description: "Company names",
      required: !1
    }),
    domains: import_pieces_framework20.Property.Array({
      displayName: "Domains",
      required: !1
    }),
    linkedin_urls: import_pieces_framework20.Property.Array({
      displayName: "LinkedIn URLs",
      required: !1
    }),
    company_filters: import_pieces_framework20.Property.LongText({
      displayName: "Company Filters",
      description: "Advanced company filters as JSON string",
      required: !1
    }),
    signal_filters: import_pieces_framework20.Property.LongText({
      displayName: "Signal Filters",
      description: "Signal-specific filters as JSON array string",
      required: !1
    }),
    people_enrichment_configs: import_pieces_framework20.Property.LongText({
      displayName: "People Enrichment Configs",
      description: "People enrichment configs as JSON array string",
      required: !1
    }),
    is_company_enrichment: import_pieces_framework20.Property.Checkbox({
      displayName: "Company Enrichment",
      required: !1,
      defaultValue: !1
    }),
    is_people_enrichment: import_pieces_framework20.Property.Checkbox({
      displayName: "People Enrichment",
      required: !1,
      defaultValue: !1
    }),
    max_failure_trigger: import_pieces_framework20.Property.Number({
      displayName: "Max Failure Trigger",
      description: "1-10",
      required: !1
    }),
    max_retry_per_trigger: import_pieces_framework20.Property.Number({
      displayName: "Max Retry Per Trigger",
      description: "0-3",
      required: !1
    }),
    retry_delay_second: import_pieces_framework20.Property.Number({
      displayName: "Retry Delay (seconds)",
      description: "1-5",
      required: !1
    }),
    notification_email: import_pieces_framework20.Property.ShortText({
      displayName: "Notification Email",
      required: !1
    })
  },
  async run(context) {
    let body = {
      name: context.propsValue.name,
      detection_mode: context.propsValue.detection_mode,
      signal_types: context.propsValue.signal_types,
      destination_type: context.propsValue.destination_type
    }, destConfig = {};
    return context.propsValue.webhook_url && (destConfig.webhook_url = context.propsValue.webhook_url), context.propsValue.email && (destConfig.email = context.propsValue.email), context.propsValue.sequence_identifier && (destConfig.sequence_identifier = context.propsValue.sequence_identifier), context.propsValue.record_type && (destConfig.record_type = context.propsValue.record_type), Object.keys(destConfig).length > 0 && (body.destination_config = destConfig), context.propsValue.description && (body.description = context.propsValue.description), context.propsValue.frequency_minute != null && (body.frequency_minute = context.propsValue.frequency_minute), context.propsValue.max_daily_trigger != null && (body.max_daily_trigger = context.propsValue.max_daily_trigger), context.propsValue.max_records_per_trigger != null && (body.max_records_per_trigger = context.propsValue.max_records_per_trigger), context.propsValue.companies && (body.companies = context.propsValue.companies), context.propsValue.domains && (body.domains = context.propsValue.domains), context.propsValue.linkedin_urls && (body.linkedin_urls = context.propsValue.linkedin_urls), context.propsValue.company_filters && (body.company_filters = JSON.parse(context.propsValue.company_filters)), context.propsValue.signal_filters && (body.signal_filters = JSON.parse(context.propsValue.signal_filters)), context.propsValue.people_enrichment_configs && (body.people_enrichment_configs = JSON.parse(
      context.propsValue.people_enrichment_configs
    )), context.propsValue.is_company_enrichment && (body.is_company_enrichment = context.propsValue.is_company_enrichment), context.propsValue.is_people_enrichment && (body.is_people_enrichment = context.propsValue.is_people_enrichment), context.propsValue.max_failure_trigger != null && (body.max_failure_trigger = context.propsValue.max_failure_trigger), context.propsValue.max_retry_per_trigger != null && (body.max_retry_per_trigger = context.propsValue.max_retry_per_trigger), context.propsValue.retry_delay_second != null && (body.retry_delay_second = context.propsValue.retry_delay_second), context.propsValue.notification_email && (body.notification_email = context.propsValue.notification_email), await pubrioRequest(
      context.auth.secret_text,
      import_pieces_common21.HttpMethod.POST,
      "/monitors/create",
      body
    );
  }
});

// ../ap/packages/pieces/community/pubrio/src/lib/actions/update-monitor.ts
var import_pieces_framework21 = require("@activepieces/pieces-framework"), import_pieces_common22 = require("@activepieces/pieces-common");
var updateMonitor = (0, import_pieces_framework21.createAction)({
  auth: pubrioAuth,
  name: "update_monitor",
  displayName: "Update Monitor",
  description: "Update an existing signal monitor",
  audience: "both",
  aiMetadata: {
    description: "Modify the configuration of an existing signal monitor identified by monitor_id, changing only the fields you supply (name, signal types, destination, filters, frequency, active/paused state, etc.). Mutating: it overwrites the named settings on the live monitor, so re-running with the same inputs is safe but changes take effect immediately. Use to edit an already-created monitor; use Create Monitor to make a new one.",
    idempotent: !1
  },
  props: {
    monitor_id: import_pieces_framework21.Property.ShortText({
      displayName: "Monitor ID",
      required: !0
    }),
    name: import_pieces_framework21.Property.ShortText({ displayName: "Name", required: !1 }),
    detection_mode: import_pieces_framework21.Property.StaticDropdown({
      displayName: "Detection Mode",
      required: !1,
      options: {
        options: [
          { label: "Company First", value: "company_first" },
          { label: "Signal First", value: "signal_first" }
        ]
      }
    }),
    signal_types: import_pieces_framework21.Property.StaticMultiSelectDropdown({
      displayName: "Signal Types",
      required: !1,
      options: {
        options: [
          { label: "Jobs", value: "jobs" },
          { label: "News", value: "news" },
          { label: "Advertisements", value: "advertisements" }
        ]
      }
    }),
    destination_type: import_pieces_framework21.Property.StaticDropdown({
      displayName: "Destination Type",
      required: !1,
      options: {
        options: [
          { label: "Webhook", value: "webhook" },
          { label: "Email", value: "email" },
          { label: "Sequences", value: "sequences" }
        ]
      }
    }),
    webhook_url: import_pieces_framework21.Property.ShortText({
      displayName: "Webhook URL",
      required: !1
    }),
    email: import_pieces_framework21.Property.ShortText({ displayName: "Email", required: !1 }),
    sequence_identifier: import_pieces_framework21.Property.ShortText({
      displayName: "Sequence Identifier",
      required: !1
    }),
    record_type: import_pieces_framework21.Property.ShortText({
      displayName: "Record Type",
      required: !1,
      description: "Required when destination type is sequences"
    }),
    description: import_pieces_framework21.Property.LongText({
      displayName: "Description",
      required: !1
    }),
    frequency_minute: import_pieces_framework21.Property.Number({
      displayName: "Frequency (minutes)",
      required: !1
    }),
    max_daily_trigger: import_pieces_framework21.Property.Number({
      displayName: "Max Daily Triggers",
      required: !1
    }),
    max_records_per_trigger: import_pieces_framework21.Property.Number({
      displayName: "Max Records Per Trigger",
      required: !1
    }),
    companies: import_pieces_framework21.Property.Array({
      displayName: "Companies",
      description: "Company names",
      required: !1
    }),
    domains: import_pieces_framework21.Property.Array({
      displayName: "Domains",
      required: !1
    }),
    linkedin_urls: import_pieces_framework21.Property.Array({
      displayName: "LinkedIn URLs",
      required: !1
    }),
    company_filters: import_pieces_framework21.Property.LongText({
      displayName: "Company Filters",
      description: "Advanced company filters as JSON string",
      required: !1
    }),
    signal_filters: import_pieces_framework21.Property.LongText({
      displayName: "Signal Filters",
      description: "Signal-specific filters as JSON array string",
      required: !1
    }),
    people_enrichment_configs: import_pieces_framework21.Property.LongText({
      displayName: "People Enrichment Configs",
      description: "People enrichment configs as JSON array string",
      required: !1
    }),
    is_company_enrichment: import_pieces_framework21.Property.Checkbox({
      displayName: "Company Enrichment",
      required: !1,
      defaultValue: !1
    }),
    is_people_enrichment: import_pieces_framework21.Property.Checkbox({
      displayName: "People Enrichment",
      required: !1,
      defaultValue: !1
    }),
    is_active: import_pieces_framework21.Property.Checkbox({
      displayName: "Is Active",
      required: !1,
      defaultValue: !1
    }),
    is_paused: import_pieces_framework21.Property.Checkbox({
      displayName: "Is Paused",
      required: !1,
      defaultValue: !1
    }),
    max_failure_trigger: import_pieces_framework21.Property.Number({
      displayName: "Max Failure Trigger",
      required: !1
    }),
    max_retry_per_trigger: import_pieces_framework21.Property.Number({
      displayName: "Max Retry Per Trigger",
      required: !1
    }),
    retry_delay_second: import_pieces_framework21.Property.Number({
      displayName: "Retry Delay (seconds)",
      required: !1
    }),
    notification_email: import_pieces_framework21.Property.ShortText({
      displayName: "Notification Email",
      required: !1
    })
  },
  async run(context) {
    let body = {
      monitor_id: context.propsValue.monitor_id
    };
    context.propsValue.name && (body.name = context.propsValue.name), context.propsValue.detection_mode && (body.detection_mode = context.propsValue.detection_mode), context.propsValue.signal_types && context.propsValue.signal_types.length > 0 && (body.signal_types = context.propsValue.signal_types), context.propsValue.destination_type && (body.destination_type = context.propsValue.destination_type);
    let destConfig = {};
    return context.propsValue.webhook_url && (destConfig.webhook_url = context.propsValue.webhook_url), context.propsValue.email && (destConfig.email = context.propsValue.email), context.propsValue.sequence_identifier && (destConfig.sequence_identifier = context.propsValue.sequence_identifier), context.propsValue.record_type && (destConfig.record_type = context.propsValue.record_type), Object.keys(destConfig).length > 0 && (body.destination_config = destConfig), context.propsValue.description && (body.description = context.propsValue.description), context.propsValue.frequency_minute != null && (body.frequency_minute = context.propsValue.frequency_minute), context.propsValue.max_daily_trigger != null && (body.max_daily_trigger = context.propsValue.max_daily_trigger), context.propsValue.max_records_per_trigger != null && (body.max_records_per_trigger = context.propsValue.max_records_per_trigger), context.propsValue.companies && (body.companies = context.propsValue.companies), context.propsValue.domains && (body.domains = context.propsValue.domains), context.propsValue.linkedin_urls && (body.linkedin_urls = context.propsValue.linkedin_urls), context.propsValue.company_filters && (body.company_filters = JSON.parse(context.propsValue.company_filters)), context.propsValue.signal_filters && (body.signal_filters = JSON.parse(context.propsValue.signal_filters)), context.propsValue.people_enrichment_configs && (body.people_enrichment_configs = JSON.parse(
      context.propsValue.people_enrichment_configs
    )), context.propsValue.is_company_enrichment && (body.is_company_enrichment = context.propsValue.is_company_enrichment), context.propsValue.is_people_enrichment && (body.is_people_enrichment = context.propsValue.is_people_enrichment), context.propsValue.is_active && (body.is_active = context.propsValue.is_active), context.propsValue.is_paused && (body.is_paused = context.propsValue.is_paused), context.propsValue.max_failure_trigger != null && (body.max_failure_trigger = context.propsValue.max_failure_trigger), context.propsValue.max_retry_per_trigger != null && (body.max_retry_per_trigger = context.propsValue.max_retry_per_trigger), context.propsValue.retry_delay_second != null && (body.retry_delay_second = context.propsValue.retry_delay_second), context.propsValue.notification_email && (body.notification_email = context.propsValue.notification_email), await pubrioRequest(
      context.auth.secret_text,
      import_pieces_common22.HttpMethod.POST,
      "/monitors/update",
      body
    );
  }
});

// ../ap/packages/pieces/community/pubrio/src/lib/actions/get-monitor.ts
var import_pieces_framework22 = require("@activepieces/pieces-framework"), import_pieces_common23 = require("@activepieces/pieces-common");
var getMonitor = (0, import_pieces_framework22.createAction)({
  auth: pubrioAuth,
  name: "get_monitor",
  displayName: "Get Monitor",
  description: "Look up a monitor by ID",
  audience: "both",
  aiMetadata: {
    description: "Fetch a single monitor by its ID, optionally including signature-reveal data. Read-only and repeatable; requires a known monitor ID rather than search criteria.",
    idempotent: !0
  },
  props: {
    monitor_id: import_pieces_framework22.Property.ShortText({
      displayName: "Monitor ID",
      required: !0
    }),
    is_signature_reveal: import_pieces_framework22.Property.Checkbox({
      displayName: "Include Signature Reveal",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let body = {
      monitor_id: context.propsValue.monitor_id
    };
    return context.propsValue.is_signature_reveal && (body.is_signature_reveal = context.propsValue.is_signature_reveal), await pubrioRequest(
      context.auth.secret_text,
      import_pieces_common23.HttpMethod.POST,
      "/monitors/lookup",
      body
    );
  }
});

// ../ap/packages/pieces/community/pubrio/src/lib/actions/list-monitors.ts
var import_pieces_framework23 = require("@activepieces/pieces-framework"), import_pieces_common24 = require("@activepieces/pieces-common");
var listMonitors = (0, import_pieces_framework23.createAction)({
  auth: pubrioAuth,
  name: "list_monitors",
  displayName: "List Monitors",
  description: "List all monitors with pagination",
  audience: "both",
  aiMetadata: {
    description: "List the account's signal monitors with paging and ordering (by created_at, updated_at, or name). Read-only and repeatable. Use to enumerate existing monitors or find a monitor_id to pass to Update, Delete, Test Run, or Reveal Monitor Signature.",
    idempotent: !0
  },
  props: {
    page: import_pieces_framework23.Property.Number({
      displayName: "Page",
      required: !1,
      defaultValue: 1
    }),
    per_page: import_pieces_framework23.Property.Number({
      displayName: "Per Page",
      required: !1,
      defaultValue: 25
    }),
    order_by: import_pieces_framework23.Property.StaticDropdown({
      displayName: "Order By",
      required: !1,
      options: {
        options: [
          { label: "Created At", value: "created_at" },
          { label: "Updated At", value: "updated_at" },
          { label: "Name", value: "name" }
        ]
      }
    }),
    is_ascending_order: import_pieces_framework23.Property.Checkbox({
      displayName: "Ascending Order",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    var _a, _b;
    let body = {
      page: (_a = context.propsValue.page) != null ? _a : 1,
      per_page: (_b = context.propsValue.per_page) != null ? _b : 25
    };
    return context.propsValue.order_by && (body.order_by = context.propsValue.order_by), context.propsValue.is_ascending_order && (body.is_ascending_order = context.propsValue.is_ascending_order), await pubrioRequest(
      context.auth.secret_text,
      import_pieces_common24.HttpMethod.POST,
      "/monitors",
      body
    );
  }
});

// ../ap/packages/pieces/community/pubrio/src/lib/actions/delete-monitor.ts
var import_pieces_framework24 = require("@activepieces/pieces-framework"), import_pieces_common25 = require("@activepieces/pieces-common");
var deleteMonitor = (0, import_pieces_framework24.createAction)({
  auth: pubrioAuth,
  name: "delete_monitor",
  displayName: "Delete Monitor",
  description: "Delete a monitor by ID",
  audience: "both",
  aiMetadata: {
    description: "Permanently delete the signal monitor identified by monitor_id. Destructive and irreversible; re-running on the same id after deletion has no further effect. Use only when the monitor should be removed entirely rather than paused (pause it via Update Monitor instead).",
    idempotent: !1
  },
  props: {
    monitor_id: import_pieces_framework24.Property.ShortText({
      displayName: "Monitor ID",
      required: !0
    })
  },
  async run(context) {
    let body = {
      monitor_id: context.propsValue.monitor_id
    };
    return await pubrioRequest(
      context.auth.secret_text,
      import_pieces_common25.HttpMethod.POST,
      "/monitors/delete",
      body
    );
  }
});

// ../ap/packages/pieces/community/pubrio/src/lib/actions/duplicate-monitor.ts
var import_pieces_framework25 = require("@activepieces/pieces-framework"), import_pieces_common26 = require("@activepieces/pieces-common");
var duplicateMonitor = (0, import_pieces_framework25.createAction)({
  auth: pubrioAuth,
  name: "duplicate_monitor",
  displayName: "Duplicate Monitor",
  description: "Duplicate an existing monitor",
  audience: "both",
  aiMetadata: {
    description: "Create a copy of an existing monitor (referenced by ID), optionally with a new name. Not idempotent: each call creates a separate new monitor, so repeated calls produce duplicates.",
    idempotent: !1
  },
  props: {
    monitor_id: import_pieces_framework25.Property.ShortText({
      displayName: "Monitor ID",
      required: !0
    }),
    name: import_pieces_framework25.Property.ShortText({
      displayName: "New Name",
      required: !1,
      description: "Name for the duplicated monitor"
    })
  },
  async run(context) {
    let body = {
      monitor_id: context.propsValue.monitor_id
    };
    return context.propsValue.name && (body.name = context.propsValue.name), await pubrioRequest(
      context.auth.secret_text,
      import_pieces_common26.HttpMethod.POST,
      "/monitors/duplicate",
      body
    );
  }
});

// ../ap/packages/pieces/community/pubrio/src/lib/actions/test-run-monitor.ts
var import_pieces_framework26 = require("@activepieces/pieces-framework"), import_pieces_common27 = require("@activepieces/pieces-common");
var testRunMonitor = (0, import_pieces_framework26.createAction)({
  auth: pubrioAuth,
  name: "test_run_monitor",
  displayName: "Test Run Monitor",
  description: "Perform a test run of a monitor",
  audience: "both",
  aiMetadata: {
    description: "Trigger a one-off test execution of an existing monitor (by monitor_id) to verify its configuration and preview matching signals without waiting for the schedule. Not idempotent: each call performs a fresh processing run and may dispatch to the monitor destination. Use for validating a monitor setup, not for routine data retrieval.",
    idempotent: !1
  },
  props: {
    monitor_id: import_pieces_framework26.Property.ShortText({
      displayName: "Monitor ID",
      required: !0
    }),
    tried_at: import_pieces_framework26.Property.ShortText({
      displayName: "Tried At",
      required: !1,
      description: "Timestamp for the test run (ISO 8601)"
    })
  },
  async run(context) {
    let body = {
      monitor_id: context.propsValue.monitor_id
    };
    return context.propsValue.tried_at && (body.tried_at = context.propsValue.tried_at), await pubrioRequest(
      context.auth.secret_text,
      import_pieces_common27.HttpMethod.POST,
      "/monitors/process/try",
      body
    );
  }
});

// ../ap/packages/pieces/community/pubrio/src/lib/actions/reveal-monitor-signature.ts
var import_pieces_framework27 = require("@activepieces/pieces-framework"), import_pieces_common28 = require("@activepieces/pieces-common");
var revealMonitorSignature = (0, import_pieces_framework27.createAction)({
  auth: pubrioAuth,
  name: "reveal_monitor_signature",
  displayName: "Reveal Monitor Signature",
  description: "Reveal the signature for a monitor (uses credits)",
  audience: "both",
  aiMetadata: {
    description: "Reveal the enriched signature/contact data for a monitor identified by monitor_id. This consumes account credits, so call it only when the revealed data is actually needed rather than to inspect a monitor (use List Monitors for that). Not idempotent in cost.",
    idempotent: !1
  },
  props: {
    monitor_id: import_pieces_framework27.Property.ShortText({
      displayName: "Monitor ID",
      required: !0
    })
  },
  async run(context) {
    let body = {
      monitor_id: context.propsValue.monitor_id
    };
    return await pubrioRequest(
      context.auth.secret_text,
      import_pieces_common28.HttpMethod.POST,
      "/monitors/signature/reveal",
      body
    );
  }
});

// ../ap/packages/pieces/community/pubrio/src/lib/triggers/webhook.ts
var import_pieces_framework28 = require("@activepieces/pieces-framework");
var pubrioWebhookTrigger = (0, import_pieces_framework28.createTrigger)({
  auth: pubrioAuth,
  name: "pubrio_monitor_event",
  displayName: "Monitor Event (Webhook)",
  description: "Triggers once per signal (signal_first mode) or once per company (company_first mode) when a Pubrio monitor fires. Copy the webhook URL into your Pubrio monitor destination configuration.",
  aiMetadata: {
    description: "Fires when a Pubrio monitor detects a matching event and posts to this webhook. In signal_first detection mode it emits one event per detected signal (e.g. a job posting or news item), each enriched with the matched companies and people; in company_first mode it emits one event per matched company. Each event includes the monitor definition and the triggered_at timestamp, representing newly detected sales/intent signals or companies to act on."
  },
  type: import_pieces_framework28.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable() {
  },
  async onDisable() {
  },
  async run(context) {
    let body = context.payload.body;
    if (!body) return [];
    let monitor = body.monitor, triggeredAt = body.triggered_at, detectionMode = monitor == null ? void 0 : monitor.detection_mode, base = { monitor, triggered_at: triggeredAt };
    if (detectionMode === "signal_first") {
      let signals = body.signals;
      if (Array.isArray(signals) && signals.length > 0)
        return signals.map((signal) => __spreadValues(__spreadValues({}, signal), base));
    }
    if (detectionMode === "company_first") {
      let companies = body.companies;
      if (Array.isArray(companies) && companies.length > 0)
        return companies.map((company) => __spreadValues(__spreadValues({}, company), base));
    }
    return [body];
  },
  sampleData: {
    signal_type: "jobs",
    signal: {
      signal_type: "jobs",
      job_search_id: "job-search-001",
      companies: [
        {
          domain_search_id: "domain-search-001",
          company_name: "Acme Corp",
          domain: "acme.com"
        }
      ]
    },
    companies: [
      {
        domain_search_id: "domain-search-001",
        company_name: "Acme Corp",
        domain: "acme.com",
        logo_url: "https://logo.clearbit.com/acme.com",
        country_code: "US",
        company_size: 5e3,
        industry: "Software",
        estimated_revenue: 5e7,
        founded_year: 2015,
        linkedin_url: "https://linkedin.com/company/acme-corp",
        people: [
          {
            people_search_id: "person-001",
            name: "Jane Smith",
            title: "VP of Engineering",
            linkedin_url: "https://linkedin.com/in/janesmith",
            email: "j.smith@acme.com",
            phone: "+14155551234"
          }
        ],
        emails: ["info@acme.com"],
        phones: ["+14155550000"]
      }
    ],
    monitor: {
      monitor_id: "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
      name: "My Signal Monitor",
      detection_mode: "signal_first",
      signal_types: ["jobs", "news"],
      is_company_enrichment: !0,
      is_people_enrichment: !0
    },
    triggered_at: "2026-04-05T20:29:43.832Z"
  }
});

// ../ap/packages/pieces/community/pubrio/src/index.ts
var import_pieces_common29 = require("@activepieces/pieces-common");
var pubrioAuth = import_pieces_framework29.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Go to dashboard.pubrio.com \u2192 Settings \u2192 API Keys \u2192 Create New Key \u2192 copy the key here.",
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await pubrioRequest(auth, import_pieces_common29.HttpMethod.GET, "/user"), {
        valid: !0
      };
    } catch (error) {
      return { valid: !1, error: "Error validating API key" };
    }
  }
}), pubrio = (0, import_pieces_framework29.createPiece)({
  displayName: "Pubrio",
  description: "Enrich company and people data, monitor changes, and get notified with Pubrio.",
  auth: pubrioAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/pubrio.png",
  authors: ["pubrio", "sanket-a11y"],
  actions: [
    searchCompanies,
    lookupCompany,
    enrichCompany,
    searchPeople,
    lookupPerson,
    revealContact,
    searchJobs,
    searchNews,
    findSimilarCompanies,
    lookupTechnology,
    linkedinPersonLookup,
    searchAds,
    linkedinCompanyLookup,
    lookupJob,
    lookupNews,
    lookupAdvertisement,
    lookupLookalike,
    batchRedeemContacts,
    queryBatchRedeem,
    createMonitor,
    updateMonitor,
    getMonitor,
    listMonitors,
    deleteMonitor,
    duplicateMonitor,
    testRunMonitor,
    revealMonitorSignature,
    (0, import_pieces_common29.createCustomApiCallAction)({
      baseUrl: () => "https://api.pubrio.com",
      auth: pubrioAuth,
      authMapping: async (auth) => ({
        "pubrio-api-key": auth.secret_text
      })
    })
  ],
  triggers: [pubrioWebhookTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  pubrio,
  pubrioAuth
});
