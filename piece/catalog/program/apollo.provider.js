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

// ../ap/packages/pieces/community/apollo/src/index.ts
var index_exports = {};
__export(index_exports, {
  apollo: () => apollo
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework8 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/apollo/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), apolloAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `
To create your Apollo API key:

1. Go to **Settings** > **[Integrations](https://app.apollo.io/#/settings/integrations)** in Apollo
2. Click **Connect** beside Apollo API
3. Click **API Keys** > **Create new key**
4. Enter a name and description, then select the endpoints you need (or toggle **Set as master key** for full access)
5. Click **Create API key** and copy it to a secure location

**Note:** Some endpoints require a master API key. Keep your API keys secure and be careful with whom you share access.

[Learn more about creating API keys](https://docs.apollo.io/docs/create-api-key)
  `,
  required: !0
});

// ../ap/packages/pieces/community/apollo/src/lib/actions/match-person.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework"), matchPerson = (0, import_pieces_framework2.createAction)({
  name: "matchPerson",
  classification: "READ",
  displayName: "Match Person",
  description: "Enrich a persons details using their email address",
  audience: "both",
  aiMetadata: {
    description: "Look up and enrich a single person in the Apollo database by their email address, returning their profile data. Use when you have an email and need the matching contact/profile details. Read-only and idempotent; an optional project-store cache may return a previously fetched result for the same email.",
    idempotent: !0
  },
  props: {
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email",
      description: " The email address of the person to be matched",
      required: !0
    }),
    cacheResponse: import_pieces_framework2.Property.Checkbox({
      displayName: "Cache Response",
      description: "Store the response in the project store for future use.",
      required: !1,
      defaultValue: !0
    })
  },
  auth: apolloAuth,
  async run({ propsValue, auth, store }) {
    if (propsValue.cacheResponse) {
      let cachedResult = await store.get(
        `_apollo_person_${propsValue.email}`,
        import_pieces_framework2.StoreScope.PROJECT
      );
      if (cachedResult)
        return cachedResult;
    }
    let personResult = (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://api.apollo.io/v1/people/match",
      headers: {
        "Content-Type": "application/json",
        "x-api-key": `${auth.secret_text}`
      },
      body: {
        email: propsValue.email
      }
    })).body.person || {};
    return propsValue.cacheResponse && await store.put(
      `_apollo_person_${propsValue.email}`,
      personResult,
      import_pieces_framework2.StoreScope.PROJECT
    ), personResult;
  }
});

// ../ap/packages/pieces/community/apollo/src/lib/actions/enrich-company.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common"), enrichCompany = (0, import_pieces_framework3.createAction)({
  name: "enrichCompany",
  classification: "READ",
  description: "",
  audience: "both",
  aiMetadata: {
    description: "Look up and enrich a single company in the Apollo database by its web domain, returning the organization profile. Use when you have a company domain and need firmographic details. Read-only and idempotent; an optional project-store cache may return a previously fetched result for the same domain.",
    idempotent: !0
  },
  displayName: "Enrich Company",
  props: {
    domain: import_pieces_framework3.Property.ShortText({
      displayName: "Domain",
      description: "",
      required: !0
    }),
    cacheResponse: import_pieces_framework3.Property.Checkbox({
      displayName: "Cache Response",
      description: "Store the response in the project store for future use.",
      required: !1,
      defaultValue: !0
    })
  },
  auth: apolloAuth,
  async run({ propsValue, auth, store }) {
    if (propsValue.cacheResponse) {
      let cachedResult = await store.get(
        `_apollo_org_${propsValue.domain}`,
        import_pieces_framework3.StoreScope.PROJECT
      );
      if (cachedResult)
        return cachedResult;
    }
    let resultOrg = (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.GET,
      url: `https://api.apollo.io/v1/organizations/enrich?domain=${propsValue.domain}`,
      headers: {
        "Content-Type": "application/json",
        "x-api-key": `${auth.secret_text}`
      }
    })).body.organization || {};
    return propsValue.cacheResponse && await store.put(
      `_apollo_org_${propsValue.domain}`,
      resultOrg,
      import_pieces_framework3.StoreScope.PROJECT
    ), resultOrg;
  }
});

// ../ap/packages/pieces/community/apollo/src/lib/actions/news-articles-search.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework"), newsArticlesSearch = (0, import_pieces_framework4.createAction)({
  auth: apolloAuth,
  name: "newsArticlesSearch",
  classification: "SEARCH",
  displayName: "News Articles Search",
  description: "Search for news articles related to companies in the Apollo database",
  audience: "both",
  aiMetadata: {
    description: "Search the Apollo database for news articles about specific companies, filterable by news category and a published-date range. Use to surface recent events (hires, funding, contracts) for given organizations. Requires Apollo organization IDs (obtained from Organization Search). Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    organization_ids: import_pieces_framework4.Property.Array({
      displayName: "Organization IDs",
      description: "The Apollo IDs for the companies you want to include in your search results. To find IDs, call the Organization Search endpoint.",
      required: !0
    }),
    categories: import_pieces_framework4.Property.Array({
      displayName: "Categories",
      description: "Filter your search to include only certain categories or sub-categories of news (e.g., hires, investment, contract)",
      required: !1
    }),
    published_at_min: import_pieces_framework4.Property.ShortText({
      displayName: "Published Date Min",
      description: "Lower bound of the date range (YYYY-MM-DD format)",
      required: !1
    }),
    published_at_max: import_pieces_framework4.Property.ShortText({
      displayName: "Published Date Max",
      description: "Upper bound of the date range (YYYY-MM-DD format)",
      required: !1
    }),
    cacheResponse: import_pieces_framework4.Property.Checkbox({
      displayName: "Cache Response",
      description: "Store the response in the project store for future use.",
      required: !1,
      defaultValue: !1
    })
  },
  async run({ propsValue, auth, store }) {
    let queryParams = new URLSearchParams();
    propsValue.organization_ids && propsValue.organization_ids.length > 0 && propsValue.organization_ids.forEach((id) => {
      queryParams.append("organization_ids[]", id);
    }), propsValue.categories && propsValue.categories.length > 0 && propsValue.categories.forEach((category) => {
      queryParams.append("categories[]", category);
    }), propsValue.published_at_min && queryParams.append("published_at[min]", propsValue.published_at_min), propsValue.published_at_max && queryParams.append("published_at[max]", propsValue.published_at_max);
    let cacheKey = `_apollo_news_${queryParams.toString()}`;
    if (propsValue.cacheResponse) {
      let cachedResult = await store.get(cacheKey, import_pieces_framework4.StoreScope.PROJECT);
      if (cachedResult)
        return cachedResult;
    }
    let result = await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: `https://api.apollo.io/api/v1/news_articles/search?${queryParams.toString()}`,
      headers: {
        "Content-Type": "application/json",
        "Cache-Control": "no-cache",
        "x-api-key": `${auth.secret_text}`
      }
    }), response = {
      news_articles: result.body.news_articles || [],
      pagination: result.body.pagination || {}
    };
    return propsValue.cacheResponse && await store.put(cacheKey, response, import_pieces_framework4.StoreScope.PROJECT), response;
  }
});

// ../ap/packages/pieces/community/apollo/src/lib/actions/organization-job-postings.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework"), organizationJobPostings = (0, import_pieces_framework5.createAction)({
  auth: apolloAuth,
  name: "organizationJobPostings",
  classification: "SEARCH",
  displayName: "Organization Job Postings",
  description: "Retrieve current job postings for a company to identify growing headcount areas",
  audience: "both",
  aiMetadata: {
    description: "Retrieve the current job postings for a single company in the Apollo database, useful for spotting hiring activity and growth areas. Requires the Apollo organization ID (obtained from Organization Search). Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    organization_id: import_pieces_framework5.Property.ShortText({
      displayName: "Organization ID",
      description: "The Apollo ID of the organization to get job postings for",
      required: !0
    }),
    cacheResponse: import_pieces_framework5.Property.Checkbox({
      displayName: "Cache Response",
      description: "Store the response in the project store for future use.",
      required: !1,
      defaultValue: !1
    })
  },
  async run({ propsValue, auth, store }) {
    let cacheKey = `_apollo_job_postings_${propsValue.organization_id}`;
    if (propsValue.cacheResponse) {
      let cachedResult = await store.get(cacheKey, import_pieces_framework5.StoreScope.PROJECT);
      if (cachedResult)
        return cachedResult;
    }
    let result = await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.GET,
      url: `https://api.apollo.io/api/v1/organizations/${propsValue.organization_id}/job_postings`,
      headers: {
        "Content-Type": "application/json",
        "Cache-Control": "no-cache",
        "x-api-key": `${auth.secret_text}`
      }
    });
    return propsValue.cacheResponse && await store.put(cacheKey, result.body.organization_job_postings || [], import_pieces_framework5.StoreScope.PROJECT), result.body.organization_job_postings || [];
  }
});

// ../ap/packages/pieces/community/apollo/src/lib/actions/organization-search.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework"), organizationSearch = (0, import_pieces_framework6.createAction)({
  auth: apolloAuth,
  name: "organizationSearch",
  classification: "SEARCH",
  displayName: "Organization Search",
  description: "Search for companies in the Apollo database with various filters",
  audience: "both",
  aiMetadata: {
    description: "Search the Apollo database for companies using filters such as name, location, employee-count range, domain, and industry tags. Use to discover organizations or to resolve a company to its Apollo organization ID for downstream actions. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    q_organization_name: import_pieces_framework6.Property.ShortText({
      displayName: "Organization Name",
      description: "Search by organization name",
      required: !1
    }),
    organization_locations: import_pieces_framework6.Property.Array({
      displayName: "Organization Locations",
      description: "Filter by locations (e.g., texas, tokyo)",
      required: !1
    }),
    organization_not_locations: import_pieces_framework6.Property.Array({
      displayName: "Exclude Locations",
      description: "Exclude specific locations (e.g., minnesota)",
      required: !1
    }),
    organization_num_employees_ranges: import_pieces_framework6.Property.Array({
      displayName: "Employee Count Ranges",
      description: 'Filter by employee count ranges (e.g., "1,10", "250,500")',
      required: !1
    }),
    organization_ids: import_pieces_framework6.Property.Array({
      displayName: "Organization IDs",
      description: "Filter by specific organization IDs",
      required: !1
    }),
    q_organization_domains: import_pieces_framework6.Property.Array({
      displayName: "Domains",
      description: "Filter by organization domains",
      required: !1
    }),
    organization_industry_tag_ids: import_pieces_framework6.Property.Array({
      displayName: "Industry Tag IDs",
      description: "Filter by industry tag IDs",
      required: !1
    }),
    cacheResponse: import_pieces_framework6.Property.Checkbox({
      displayName: "Cache Response",
      description: "Store the response in the project store for future use.",
      required: !1,
      defaultValue: !1
    })
  },
  async run({ propsValue, auth, store }) {
    let queryParams = new URLSearchParams();
    propsValue.q_organization_name && queryParams.append("q_organization_name", propsValue.q_organization_name), propsValue.organization_locations && propsValue.organization_locations.length > 0 && propsValue.organization_locations.forEach((location) => {
      queryParams.append("organization_locations[]", location);
    }), propsValue.organization_not_locations && propsValue.organization_not_locations.length > 0 && propsValue.organization_not_locations.forEach((location) => {
      queryParams.append("organization_not_locations[]", location);
    }), propsValue.organization_num_employees_ranges && propsValue.organization_num_employees_ranges.length > 0 && propsValue.organization_num_employees_ranges.forEach((range) => {
      queryParams.append(
        "organization_num_employees_ranges[]",
        range
      );
    }), propsValue.organization_ids && propsValue.organization_ids.length > 0 && propsValue.organization_ids.forEach((id) => {
      queryParams.append("organization_ids[]", id);
    }), propsValue.q_organization_domains && propsValue.q_organization_domains.length > 0 && propsValue.q_organization_domains.forEach((domain) => {
      queryParams.append("q_organization_domains[]", domain);
    }), propsValue.organization_industry_tag_ids && propsValue.organization_industry_tag_ids.length > 0 && propsValue.organization_industry_tag_ids.forEach((tagId) => {
      queryParams.append("organization_industry_tag_ids[]", tagId);
    });
    let cacheKey = `_apollo_org_search_${queryParams.toString()}`;
    if (propsValue.cacheResponse) {
      let cachedResult = await store.get(cacheKey, import_pieces_framework6.StoreScope.PROJECT);
      if (cachedResult)
        return cachedResult;
    }
    let result = await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.POST,
      url: `https://api.apollo.io/api/v1/mixed_companies/search?${queryParams.toString()}`,
      headers: {
        "Content-Type": "application/json",
        "Cache-Control": "no-cache",
        "x-api-key": `${auth.secret_text}`
      }
    });
    return propsValue.cacheResponse && await store.put(cacheKey, result.body, import_pieces_framework6.StoreScope.PROJECT), result.body;
  }
});

// ../ap/packages/pieces/community/apollo/src/lib/actions/people-search.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework"), peopleSearch = (0, import_pieces_framework7.createAction)({
  auth: apolloAuth,
  name: "peopleSearch",
  classification: "SEARCH",
  displayName: "People Search",
  description: "Search for people in the Apollo database (does not return email/phone, use enrichment for that)",
  audience: "both",
  aiMetadata: {
    description: "Search the Apollo database for people using filters such as keywords, job titles, seniority, location, and their organization (domain, location, employee-count range). Use to find prospects matching criteria; note the results do not include email or phone, so use Match Person enrichment for contact details. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    q_keywords: import_pieces_framework7.Property.ShortText({
      displayName: "Keywords",
      description: "Search keywords for people",
      required: !1
    }),
    person_titles: import_pieces_framework7.Property.Array({
      displayName: "Person Titles",
      description: 'Filter by job titles (e.g., "CEO", "Sales Manager")',
      required: !1
    }),
    person_locations: import_pieces_framework7.Property.Array({
      displayName: "Person Locations",
      description: "Filter by person locations",
      required: !1
    }),
    person_seniorities: import_pieces_framework7.Property.Array({
      displayName: "Person Seniorities",
      description: 'Filter by seniority levels (e.g., "senior", "manager", "director")',
      required: !1
    }),
    organization_ids: import_pieces_framework7.Property.Array({
      displayName: "Organization IDs",
      description: "Filter by specific organization IDs",
      required: !1
    }),
    organization_num_employees_ranges: import_pieces_framework7.Property.Array({
      displayName: "Organization Employee Ranges",
      description: 'Filter by organization employee count ranges (e.g., "1,10", "250,500")',
      required: !1
    }),
    q_organization_domains: import_pieces_framework7.Property.Array({
      displayName: "Organization Domains",
      description: "Filter by organization domains",
      required: !1
    }),
    organization_locations: import_pieces_framework7.Property.Array({
      displayName: "Organization Locations",
      description: "Filter by organization locations",
      required: !1
    }),
    cacheResponse: import_pieces_framework7.Property.Checkbox({
      displayName: "Cache Response",
      description: "Store the response in the project store for future use.",
      required: !1,
      defaultValue: !1
    })
  },
  async run({ propsValue, auth, store }) {
    let queryParams = new URLSearchParams();
    propsValue.q_keywords && queryParams.append("q_keywords", propsValue.q_keywords), propsValue.person_titles && propsValue.person_titles.length > 0 && propsValue.person_titles.forEach((title) => {
      queryParams.append("person_titles[]", title);
    }), propsValue.person_locations && propsValue.person_locations.length > 0 && propsValue.person_locations.forEach((location) => {
      queryParams.append("person_locations[]", location);
    }), propsValue.person_seniorities && propsValue.person_seniorities.length > 0 && propsValue.person_seniorities.forEach((seniority) => {
      queryParams.append("person_seniorities[]", seniority);
    }), propsValue.organization_ids && propsValue.organization_ids.length > 0 && propsValue.organization_ids.forEach((id) => {
      queryParams.append("organization_ids[]", id);
    }), propsValue.organization_num_employees_ranges && propsValue.organization_num_employees_ranges.length > 0 && propsValue.organization_num_employees_ranges.forEach((range) => {
      queryParams.append(
        "organization_num_employees_ranges[]",
        range
      );
    }), propsValue.q_organization_domains && propsValue.q_organization_domains.length > 0 && propsValue.q_organization_domains.forEach((domain) => {
      queryParams.append("q_organization_domains[]", domain);
    }), propsValue.organization_locations && propsValue.organization_locations.length > 0 && propsValue.organization_locations.forEach((location) => {
      queryParams.append("organization_locations[]", location);
    });
    let cacheKey = `_apollo_people_search_${queryParams.toString()}`;
    if (propsValue.cacheResponse) {
      let cachedResult = await store.get(cacheKey, import_pieces_framework7.StoreScope.PROJECT);
      if (cachedResult)
        return cachedResult;
    }
    let result = await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.POST,
      url: `https://api.apollo.io/api/v1/mixed_people/api_search?${queryParams.toString()}`,
      headers: {
        "Content-Type": "application/json",
        "Cache-Control": "no-cache",
        "x-api-key": `${auth.secret_text}`
      }
    }), response = {
      people: result.body.people || [],
      pagination: result.body.pagination || {}
    };
    return propsValue.cacheResponse && await store.put(cacheKey, response, import_pieces_framework7.StoreScope.PROJECT), response;
  }
});

// ../ap/packages/pieces/community/apollo/src/index.ts
var import_pieces_common7 = require("@activepieces/pieces-common");
var apollo = (0, import_pieces_framework8.createPiece)({
  displayName: "Apollo",
  auth: apolloAuth,
  description: "AI sales platform for prospecting, lead gen, and deal automation. Close more deals, faster, with smart data.",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/apollo.png",
  authors: ["abuaboud", "sanket-a11y"],
  actions: [
    matchPerson,
    enrichCompany,
    newsArticlesSearch,
    organizationJobPostings,
    organizationSearch,
    peopleSearch,
    (0, import_pieces_common7.createCustomApiCallAction)({
      auth: apolloAuth,
      baseUrl: () => "https://api.apollo.io/api/v1",
      authMapping: async (auth) => ({
        "x-api-key": auth.secret_text
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  apollo
});
