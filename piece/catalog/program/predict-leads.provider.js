"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames;
var __hasOwnProp = Object.prototype.hasOwnProperty;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value;
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
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key != "symbol" ? key + "" : key, value);

// ../ap/packages/pieces/community/predict-leads/src/index.ts
var index_exports = {};
__export(index_exports, {
  predictLeads: () => predictLeads
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_framework10 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/predict-leads/src/lib/actions/companies.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/predict-leads/src/lib/common/index.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/predict-leads/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
function emptyValueFilter(accessor) {
  return (key) => {
    let val = accessor(key);
    return val != null && (typeof val != "string" || val.length > 0);
  };
}
function prepareQuery(request) {
  let params = {};
  return request && Object.keys(request).filter(emptyValueFilter((k) => request[k])).forEach((k) => {
    params[k] = request[k];
  }), params;
}
var PredictLeadsClient = class {
  constructor(apiKey, apiToken) {
    __publicField(this, "apiKey", apiKey);
    __publicField(this, "apiToken", apiToken);
  }
  async makeRequest(method, resourceUri, query, body = void 0) {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: "https://predictleads.com/api" + resourceUri,
      headers: {
        "X-Api-Key": this.apiKey,
        "X-Api-Token": this.apiToken
      },
      queryParams: query,
      body
    })).body;
  }
  async findCompanies(query) {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, "/v3/discover/companies", query);
  }
  async findCompanyByDomain(domain) {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, `/v3/companies/${domain}`);
  }
  async findCompanyJobOpenings(companyId, query) {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, `/v3/companies/${companyId}/job_openings`, query);
  }
  async getAJobOpeningById(jobOpeningId) {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, `/v3/job_openings/${jobOpeningId}`);
  }
  async findJobOpenings(query) {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, "/v3/discover/job_openings", query);
  }
  async findTechnologies(domain, query) {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, `/v3/companies/${domain}/technology_detections`, query);
  }
  async findCompaniesTechnologyById(technologyId, query) {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, `/v3/discover/technologies/${technologyId}/technology_detections`, query);
  }
  async findNewsByDomain(domain, query) {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, `/v3/companies/${domain}/news_events`, query);
  }
  async findNewsEventById(newsEventId) {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, `/v3/news_events/${newsEventId}`);
  }
  async findConnectionsByDomain(domain, query) {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, `/v3/companies/${domain}/connections`, query);
  }
  async findConnections(query) {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, "/v3/discover/portfolio_companies/connections", query);
  }
};

// ../ap/packages/pieces/community/predict-leads/src/lib/common/index.ts
function makeClient(auth) {
  return new PredictLeadsClient(auth.props.apiKey, auth.props.apiToken);
}
var CommonFields = {
  page: import_pieces_framework.Property.Number({
    displayName: "Page",
    description: "Page number of shown items",
    required: !1
  }),
  limit: import_pieces_framework.Property.Number({
    displayName: "Limit",
    description: "Limit the number of shown items per page. Max 1000.",
    required: !1
  })
}, pageField = import_pieces_framework.Property.Number({
  displayName: "Page",
  description: "Page number of shown items",
  required: !1
}), limitField = import_pieces_framework.Property.Number({
  displayName: "Limit",
  description: "Limit the number of shown items per page. Max 1000.",
  required: !1
}), firstSeenAtFromField = import_pieces_framework.Property.ShortText({
  displayName: "First Seen At From",
  description: "Only return data first seen after given date . Example 2024-09-25",
  required: !1
}), firstSeenAtUntilField = import_pieces_framework.Property.ShortText({
  displayName: "First Seen At Until",
  description: "Only return data first seen before given date . Example 2024-09-25",
  required: !1
}), lastSeenAtFromField = import_pieces_framework.Property.ShortText({
  displayName: "Last Seen At From",
  description: "Only return data last seen after given date . Example 2024-09-25",
  required: !1
}), lastSeenAtUntilField = import_pieces_framework.Property.ShortText({
  displayName: "Last Seen At Until",
  description: "Only return data last seen before given date . Example 2024-09-25",
  required: !1
});

// ../ap/packages/pieces/community/predict-leads/src/lib/auth.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_framework3 = require("@activepieces/pieces-framework");
var PredictLeadsAuth = import_pieces_framework2.PieceAuth.CustomAuth({
  required: !0,
  description: `
    To obtain your PredictLeads tokens, follow these steps:

    1. Have a PredictLeads account - Create a new user here: https://predictleads.com/sign_up.
    2. Go to: https://predictleads.com/subscriptions.
    3. In the subscription page, locate the API Key and Tokens section where you can also find the usage.
    `,
  props: {
    apiKey: import_pieces_framework2.PieceAuth.SecretText({
      displayName: "API Key",
      description: "The key of the Predict Leads account.",
      required: !0
    }),
    apiToken: import_pieces_framework2.PieceAuth.SecretText({
      displayName: "API Token",
      description: "The token of the Predict Leads account.",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    try {
      return await makeClient(
        {
          type: import_pieces_framework3.AppConnectionType.CUSTOM_AUTH,
          props: auth
        }
      ).findCompanyByDomain("google.com"), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API Token or API Key."
      };
    }
  }
});

// ../ap/packages/pieces/community/predict-leads/src/lib/actions/companies.ts
var findCompaniesAction = (0, import_pieces_framework4.createAction)({
  auth: PredictLeadsAuth,
  name: "predict-leads_find_companies",
  displayName: "List Companies",
  description: "Retrieves all companies.",
  audience: "both",
  aiMetadata: { description: 'Discovers companies in the PredictLeads database filtered by location and employee headcount range (e.g. "11-50"). Use to prospect or build a target-account list when you do not yet know specific company domains; both location and size range are required. Read-only and idempotent; supports pagination.', idempotent: !0 },
  props: {
    location: import_pieces_framework4.Property.ShortText({
      displayName: "Location",
      required: !0
    }),
    minMaxSizes: import_pieces_framework4.Property.ShortText({
      displayName: "Min Max Size",
      description: 'A min-max size range. Examples: "11-50"". For now, only single range is supported.',
      required: !0
    }),
    page: pageField,
    limit: limitField
  },
  async run(context) {
    var _a, _b;
    let page = (_a = context.propsValue.page) != null ? _a : 1, limit = (_b = context.propsValue.limit) != null ? _b : 1e3, location = context.propsValue.location, min_max_sizes = context.propsValue.minMaxSizes, client = makeClient(
      context.auth
    );
    try {
      return await client.findCompanies(
        prepareQuery({
          location,
          min_max_sizes,
          page,
          limit
        })
      );
    } catch (error) {
      throw new Error(JSON.stringify(error, void 0, 2));
    }
  }
}), findCompanyByDomainAction = (0, import_pieces_framework4.createAction)({
  auth: PredictLeadsAuth,
  name: "predict-leads_find_company_by_domain",
  displayName: "Retrieve a company",
  description: "Retrieves a company by its domain.",
  audience: "both",
  aiMetadata: { description: `Looks up a single company profile in PredictLeads by its web domain (e.g. "stripe.com"). Use when you already know the domain and want that company's details. Read-only and idempotent.`, idempotent: !0 },
  props: {
    domain: import_pieces_framework4.Property.ShortText({
      displayName: "Domain",
      description: "The domain of the company to find.",
      required: !0
    })
  },
  async run(context) {
    let domain = context.propsValue.domain, client = makeClient(
      context.auth
    );
    try {
      return await client.findCompanyByDomain(domain);
    } catch (error) {
      throw new Error(JSON.stringify(error, void 0, 2));
    }
  }
});

// ../ap/packages/pieces/community/predict-leads/src/lib/actions/jobs.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var categories = [
  { value: "administration", label: "Administration" },
  { value: "consulting", label: "Consulting" },
  { value: "data_analysis", label: "Data Analysis" },
  { value: "design", label: "Design" },
  { value: "directors", label: "Directors" },
  { value: "education", label: "Education" },
  { value: "engineering", label: "Engineering" },
  { value: "finance", label: "Finance" },
  { value: "healthcare_services", label: "Healthcare Services" },
  { value: "human_resources", label: "Human Resources" },
  { value: "information_technology", label: "Information Technology" },
  { value: "internship", label: "Internship" },
  { value: "legal", label: "Legal" },
  { value: "management", label: "Management" },
  { value: "marketing", label: "Marketing" },
  { value: "military_and_protective_services", label: "Military And Protective Services" },
  { value: "operations", label: "Operations" },
  { value: "purchasing", label: "Purchasing" },
  { value: "product_management", label: "Product Management" },
  { value: "quality_assurance", label: "Quality Assurance" },
  { value: "real_estate", label: "Real Estate" },
  { value: "research", label: "Research" },
  { value: "sales", label: "Sales" },
  { value: "software_development", label: "Software Development" },
  { value: "support", label: "Support" },
  { value: "manual_work", label: "Manual Work" },
  { value: "food", label: "Food" }
], getCompanyJobOpeningsActions = (0, import_pieces_framework5.createAction)({
  auth: PredictLeadsAuth,
  name: "predict-leads_find_company_job_openings",
  displayName: "List Company's Job Openings",
  description: "Retrieves job openings by company domain.",
  audience: "both",
  aiMetadata: { description: "Lists a specific company's job openings in PredictLeads, keyed by its domain. Optional filters narrow results to active or not-yet-closed postings, date windows, openings that have a description or location, and job categories. Use to gauge a known company's hiring activity as a buying or growth signal; requires the company domain. Read-only and idempotent; supports pagination.", idempotent: !0 },
  props: {
    domain: import_pieces_framework5.Property.ShortText({
      displayName: "Domain",
      description: "Company's domain",
      required: !0
    }),
    active_only: import_pieces_framework5.Property.Checkbox({
      displayName: "Active Only",
      description: "Set to true if you'd like to receive JobOpenings that are not closed, have last_seen_at more recent than 5 days and were found in the last year",
      required: !1
    }),
    not_closed: import_pieces_framework5.Property.Checkbox({
      displayName: "Not Closed",
      description: "Similar to active_only, but without considering last_seen_at timestamp.",
      required: !1
    }),
    first_seen_at_from: firstSeenAtFromField,
    first_seen_at_until: firstSeenAtUntilField,
    last_seen_at_from: lastSeenAtFromField,
    last_seen_at_until: lastSeenAtUntilField,
    with_description_only: import_pieces_framework5.Property.Checkbox({
      displayName: "With Description Only",
      description: "Only return JobOpenings that have description",
      required: !1
    }),
    with_location_only: import_pieces_framework5.Property.Checkbox({
      displayName: "With Location Only",
      description: "Only return JobOpenings that have location",
      required: !1
    }),
    categories: import_pieces_framework5.Property.StaticMultiSelectDropdown({
      displayName: "Categories",
      description: "Select categories to filter job openings",
      required: !1,
      options: {
        options: categories
      }
    }),
    page: pageField,
    limit: limitField
  },
  async run(context) {
    var _a, _b;
    let client = makeClient(
      context.auth
    ), domain = context.propsValue.domain, active_only = context.propsValue.active_only, not_closed = context.propsValue.not_closed, first_seen_at_from = context.propsValue.first_seen_at_from, first_seen_at_until = context.propsValue.first_seen_at_until, last_seen_at_from = context.propsValue.last_seen_at_from, last_seen_at_until = context.propsValue.last_seen_at_until, with_description_only = context.propsValue.with_description_only, with_location_only = context.propsValue.with_location_only, categories3 = context.propsValue.categories, page = (_a = context.propsValue.page) != null ? _a : 1, limit = (_b = context.propsValue.limit) != null ? _b : 1e3;
    try {
      return await client.findCompanyJobOpenings(
        domain,
        prepareQuery({
          active_only,
          not_closed,
          first_seen_at_from,
          first_seen_at_until,
          last_seen_at_from,
          last_seen_at_until,
          with_description_only,
          with_location_only,
          categories: categories3,
          page,
          limit
        })
      );
    } catch (error) {
      throw new Error(JSON.stringify(error, void 0, 2));
    }
  }
}), getAJobOpeningByIdAction = (0, import_pieces_framework5.createAction)({
  auth: PredictLeadsAuth,
  name: "predict-leads_get_a_job_opening_by_id",
  displayName: "Get Job Opening",
  description: "Retrieves a single job opening by ID.",
  audience: "both",
  aiMetadata: { description: "Retrieves the full details of one job opening in PredictLeads by its job opening ID. Use when you already have an opening ID (e.g. from a list/discover call) and want its complete record. Read-only and idempotent.", idempotent: !0 },
  props: {
    jobOpeningId: import_pieces_framework5.Property.ShortText({
      displayName: "Job Opening ID",
      description: "The ID of the job opening to retrieve.",
      required: !0
    })
  },
  async run(context) {
    let client = makeClient(
      context.auth
    ), jobOpeningId = context.propsValue.jobOpeningId;
    try {
      return await client.getAJobOpeningById(jobOpeningId);
    } catch (error) {
      throw new Error(JSON.stringify(error, void 0, 2));
    }
  }
}), findJobOpeningsAction = (0, import_pieces_framework5.createAction)({
  auth: PredictLeadsAuth,
  name: "predict-leads_find_job_openings",
  displayName: "List Job Openings",
  description: "Retrieves a list of job openings",
  audience: "both",
  aiMetadata: { description: `Discovers job openings across all companies in PredictLeads, optionally filtered by O*NET occupation codes (e.g. "15-1254.00" for Web Developers). Use to find hiring activity for specific roles without targeting a particular company; for one known company's postings use the company job-openings action instead. Read-only and idempotent; supports pagination.`, idempotent: !0 },
  props: {
    onet_codes: import_pieces_framework5.Property.Array({
      displayName: "onet_codes",
      description: 'O*NET codes to filter by, such as "17-2071.00" for "Electrical Engineers" or "15-1254.00" for "Web Developers". For full list of possible codes see: https://www.onetonline.org/find/all',
      required: !1
    }),
    page: pageField,
    limit: limitField
  },
  async run(context) {
    var _a, _b, _c;
    let client = makeClient(
      context.auth
    ), page = (_a = context.propsValue.page) != null ? _a : 1, limit = (_b = context.propsValue.limit) != null ? _b : 1e3, onet_codes = (_c = context.propsValue.onet_codes) != null ? _c : [];
    try {
      return await client.findJobOpenings(prepareQuery({
        page,
        limit,
        onet_codes
      }));
    } catch (error) {
      throw new Error(JSON.stringify(error, void 0, 2));
    }
  }
});

// ../ap/packages/pieces/community/predict-leads/src/lib/actions/technology.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var findTechnologiesByCompanyAction = (0, import_pieces_framework6.createAction)({
  auth: PredictLeadsAuth,
  name: "predict-leads_find_technologies_by_domain",
  displayName: "List Technologies by domain",
  description: "Retrieve technologies used by specific company",
  audience: "both",
  aiMetadata: { description: "Lists the technologies (technology detections) that a specific company uses, keyed by its domain in PredictLeads. Optional first/last-seen date filters scope detections to a time window. Use for technographic profiling of a known company; requires the company domain. Read-only and idempotent; supports pagination.", idempotent: !0 },
  props: {
    domain: import_pieces_framework6.Property.ShortText({
      displayName: "Domain",
      description: "The domain of the company to find.",
      required: !0
    }),
    first_seen_at_from: firstSeenAtFromField,
    first_seen_at_until: firstSeenAtUntilField,
    last_seen_at_from: lastSeenAtFromField,
    last_seen_at_until: lastSeenAtUntilField,
    page: pageField,
    limit: limitField
  },
  async run(context) {
    var _a, _b;
    let domain = context.propsValue.domain, first_seen_at_from = context.propsValue.first_seen_at_from, first_seen_at_until = context.propsValue.first_seen_at_until, last_seen_at_from = context.propsValue.last_seen_at_from, last_seen_at_until = context.propsValue.last_seen_at_until, page = (_a = context.propsValue.page) != null ? _a : 1, limit = (_b = context.propsValue.limit) != null ? _b : 1e3, client = makeClient(
      context.auth
    );
    try {
      return await client.findTechnologies(
        domain,
        prepareQuery({
          first_seen_at_from,
          first_seen_at_until,
          last_seen_at_from,
          last_seen_at_until,
          page,
          limit
        })
      );
    } catch (error) {
      throw new Error(JSON.stringify(error, void 0, 2));
    }
  }
}), findCompaniesByTechnologyIdAction = (0, import_pieces_framework6.createAction)({
  auth: PredictLeadsAuth,
  name: "predict-leads_find_companies_by_technology_id",
  displayName: "Retrieve companies by technology ID",
  description: "Retrieves company using specific technology ID",
  audience: "both",
  aiMetadata: { description: "Discovers companies that use a given technology, keyed by that technology's ID in PredictLeads. Optional first/last-seen date filters scope results to a time window. Use to build a target list of companies on a specific tool/stack; requires the technology ID (not a name). Read-only and idempotent; supports pagination.", idempotent: !0 },
  props: {
    id: import_pieces_framework6.Property.ShortText({
      displayName: "ID",
      description: "The ID of the technology to find.",
      required: !0
    }),
    first_seen_at_from: firstSeenAtFromField,
    first_seen_at_until: firstSeenAtUntilField,
    last_seen_at_from: lastSeenAtFromField,
    last_seen_at_until: lastSeenAtUntilField,
    page: pageField,
    limit: limitField
  },
  async run(context) {
    var _a, _b;
    let id = context.propsValue.id, first_seen_at_from = context.propsValue.first_seen_at_from, first_seen_at_until = context.propsValue.first_seen_at_until, last_seen_at_from = context.propsValue.last_seen_at_from, last_seen_at_until = context.propsValue.last_seen_at_until, page = (_a = context.propsValue.page) != null ? _a : 1, limit = (_b = context.propsValue.limit) != null ? _b : 1e3, client = makeClient(
      context.auth
    );
    try {
      return await client.findCompaniesTechnologyById(
        id,
        prepareQuery({
          first_seen_at_from,
          first_seen_at_until,
          last_seen_at_from,
          last_seen_at_until,
          page,
          limit
        })
      );
    } catch (error) {
      throw new Error(JSON.stringify(error, void 0, 2));
    }
  }
});

// ../ap/packages/pieces/community/predict-leads/src/lib/actions/news-events.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var categories2 = [
  { value: "acquires", label: "Acquires" },
  { value: "merges_with", label: "Merges With" },
  { value: "sells_assets_to", label: "Sells Assets To" },
  { value: "signs_new_client", label: "Signs New Client" },
  { value: "files_suit_against", label: "Files Suit Against" },
  { value: "has_issues_with", label: "Has Issues With" },
  { value: "closes_offices_in", label: "Closes Offices In" },
  { value: "decreases_headcount_by", label: "Decreases Headcount By" },
  { value: "attends_event", label: "Attends Event" },
  { value: "expands_facilities", label: "Expands Facilities" },
  { value: "expands_offices_in", label: "Expands Offices In" },
  { value: "expands_offices_to", label: "Expands Offices To" },
  { value: "increases_headcount_by", label: "Increases Headcount By" },
  { value: "opens_new_location", label: "Opens New Location" },
  { value: "goes_public", label: "Goes Public" },
  { value: "invests_into", label: "Invests Into" },
  { value: "invests_into_assets", label: "Invests Into Assets" },
  { value: "receives_financing", label: "Receives Financing" },
  { value: "hires", label: "Hires" },
  { value: "leaves", label: "Leaves" },
  { value: "promotes", label: "Promotes" },
  { value: "retires_from", label: "Retires From" },
  { value: "integrates_with", label: "Integrates With" },
  { value: "is_developing", label: "Is Developing" },
  { value: "launches", label: "Launches" },
  { value: "partners_with", label: "Partners With" },
  { value: "receives_award", label: "Receives Award" },
  { value: "recognized_as", label: "Recognized As" },
  { value: "identified_as_competitor_of", label: "Identified As Competitor Of" }
], findNewsEventsByDomainAction = (0, import_pieces_framework7.createAction)({
  auth: PredictLeadsAuth,
  name: "predict-leads_find_news_by_domain",
  displayName: "List Company News Events",
  description: "Retrieves news events by company domain",
  audience: "both",
  aiMetadata: { description: "Lists news events (buying/intent signals such as funding, acquisitions, leadership changes, office expansions, product launches) for a specific company, keyed by its domain in PredictLeads. Optional found-at date range and event categories narrow the results. Use to surface recent triggers for a known company; requires the company domain. Read-only and idempotent; supports pagination.", idempotent: !0 },
  props: {
    domain: import_pieces_framework7.Property.ShortText({
      displayName: "Domain",
      description: "The domain of the company to find.",
      required: !0
    }),
    found_at_from: import_pieces_framework7.Property.ShortText({
      displayName: "Found At From",
      description: "Only return data first seen after given date . Example 2024-09-25",
      required: !1
    }),
    found_at_until: import_pieces_framework7.Property.ShortText({
      displayName: "Found At Until",
      description: "Only return data first seen before given date . Example 2024-09-25",
      required: !1
    }),
    categories: import_pieces_framework7.Property.StaticMultiSelectDropdown({
      displayName: "Categories",
      description: "The categories of news to find.",
      required: !1,
      options: {
        options: categories2
      }
    }),
    page: pageField,
    limit: limitField
  },
  async run(context) {
    var _a, _b;
    let domain = context.propsValue.domain, page = (_a = context.propsValue.page) != null ? _a : 1, limit = (_b = context.propsValue.limit) != null ? _b : 1e3, found_at_from = context.propsValue.found_at_from, found_at_until = context.propsValue.found_at_until, categories3 = context.propsValue.categories, client = makeClient(
      context.auth
    );
    try {
      return await client.findNewsByDomain(domain, prepareQuery({
        page,
        limit,
        found_at_from,
        found_at_until,
        categories: categories3
      }));
    } catch (error) {
      throw new Error(JSON.stringify(error, void 0, 2));
    }
  }
}), findNewsEventByIdAction = (0, import_pieces_framework7.createAction)({
  auth: PredictLeadsAuth,
  name: "predict-leads_find_news_event_by_id",
  displayName: "Get News Event",
  description: "Retrieves a single news event by ID.",
  audience: "both",
  aiMetadata: { description: "Retrieves the full details of one news event in PredictLeads by its event ID. Use when you already have a news event ID (e.g. from a list call) and want its complete record. Read-only and idempotent.", idempotent: !0 },
  props: {
    id: import_pieces_framework7.Property.ShortText({
      displayName: "ID",
      description: "The ID of the news event to find.",
      required: !0
    })
  },
  async run(context) {
    let id = context.propsValue.id, client = makeClient(
      context.auth
    );
    try {
      return await client.findNewsEventById(id);
    } catch (error) {
      throw new Error(JSON.stringify(error, void 0, 2));
    }
  }
});

// ../ap/packages/pieces/community/predict-leads/src/lib/actions/connections.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var findConnectionsByDomainAction = (0, import_pieces_framework8.createAction)({
  auth: PredictLeadsAuth,
  name: "predict-leads_find_connections_by_domain",
  displayName: "Get Company Connections",
  description: "Retrieves company connections by domain.",
  audience: "both",
  aiMetadata: { description: "Lists a company's business connections (related/linked companies) in PredictLeads, keyed by its domain. Use to map a known company's relationship graph; requires the company domain. Read-only and idempotent; supports pagination.", idempotent: !0 },
  props: {
    domain: import_pieces_framework8.Property.ShortText({
      displayName: "Domain",
      description: "The domain to find.",
      required: !0
    }),
    // first_seen_at_from: firstSeenAtFromField,
    // first_seen_at_until: firstSeenAtUntilField,
    page: pageField,
    limit: limitField
  },
  async run(context) {
    var _a, _b;
    let domain = context.propsValue.domain, page = (_a = context.propsValue.page) != null ? _a : 1, limit = (_b = context.propsValue.limit) != null ? _b : 1e3, client = makeClient(
      context.auth
    );
    try {
      return await client.findConnectionsByDomain(domain, prepareQuery({
        page,
        limit
      }));
    } catch (error) {
      throw new Error(JSON.stringify(error, void 0, 2));
    }
  }
}), findConnectionsAction = (0, import_pieces_framework8.createAction)({
  auth: PredictLeadsAuth,
  name: "predict-leads_find_connections",
  displayName: "List Connections",
  description: "Finds all portfolio companies.",
  audience: "both",
  aiMetadata: { description: "Lists all portfolio-company connections available to the authenticated PredictLeads account, across companies (not scoped to a single domain). Use to retrieve the full connection set rather than one company's connections. Read-only and idempotent; supports pagination.", idempotent: !0 },
  props: {
    // first_seen_at_from: firstSeenAtFromField,
    // first_seen_at_until: firstSeenAtUntilField,
    // last_seen_at_from: lastSeenAtFromField,
    // last_seen_at_until: lastSeenAtUntilField,
    page: pageField,
    limit: limitField
  },
  async run(context) {
    var _a, _b;
    let client = makeClient(
      context.auth
    ), page = (_a = context.propsValue.page) != null ? _a : 1, limit = (_b = context.propsValue.limit) != null ? _b : 1e3;
    try {
      return await client.findConnections(prepareQuery({
        page,
        limit
      }));
    } catch (error) {
      throw new Error(JSON.stringify(error, void 0, 2));
    }
  }
});

// ../ap/packages/pieces/community/predict-leads/src/index.ts
var import_pieces_common2 = require("@activepieces/pieces-common");
var predictLeads = (0, import_pieces_framework9.createPiece)({
  displayName: "PredictLeads",
  auth: PredictLeadsAuth,
  description: "Company Intelligence Data Source",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/predict-leads.png",
  categories: [import_pieces_framework10.PieceCategory.BUSINESS_INTELLIGENCE, import_pieces_framework10.PieceCategory.SALES_AND_CRM, import_pieces_framework10.PieceCategory.MARKETING],
  authors: [
    "codegino"
  ],
  actions: [
    findCompaniesAction,
    findCompanyByDomainAction,
    findJobOpeningsAction,
    getCompanyJobOpeningsActions,
    getAJobOpeningByIdAction,
    findTechnologiesByCompanyAction,
    findCompaniesByTechnologyIdAction,
    findNewsEventsByDomainAction,
    findNewsEventByIdAction,
    findConnectionsAction,
    findConnectionsByDomainAction,
    (0, import_pieces_common2.createCustomApiCallAction)({
      auth: PredictLeadsAuth,
      baseUrl: () => "https://predictleads.com/api/v3",
      authMapping: async (auth) => {
        let authValue = auth.props;
        return {
          "X-Api-Key": authValue.apiKey,
          "X-Api-Token": authValue.apiToken
        };
      }
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  predictLeads
});
