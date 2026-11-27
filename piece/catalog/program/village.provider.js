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

// ../ap/packages/pieces/community/village/src/index.ts
var index_exports = {};
__export(index_exports, {
  village: () => village,
  villageAuth: () => villageAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework42 = require("@activepieces/pieces-framework"), import_pieces_framework43 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/village/src/lib/common/auth.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework"), VILLAGE_API_BASE_URL = "https://api.village.ai", villageAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Token",
  description: "Your Village API token. Generate one from your Village settings and paste it here.",
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${VILLAGE_API_BASE_URL}/v2/app`,
        headers: { Authorization: `Bearer ${auth}` }
      }), { valid: !0 };
    } catch (e) {
      return { valid: !1, error: "Invalid connection details" };
    }
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/app/get-app-info.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var getAppInfo = (0, import_pieces_framework2.createAction)({
  auth: villageAuth,
  name: "get_app_info",
  displayName: "Get App Information",
  description: "Get information about the partner application associated with your token \u2014 title, description, domain, logo URL, public key, and active flag. Useful for verifying your app configuration.",
  audience: "both",
  aiMetadata: {
    description: "Fetch metadata about the partner application tied to the current API token (title, domain, public key, active flag). Read-only and idempotent. Use to verify which app a token belongs to and confirm its configuration, not for user or team data.",
    idempotent: !0
  },
  props: {},
  async run(context) {
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.GET,
      url: `${VILLAGE_API_BASE_URL}/v2/app`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` }
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/companies/check-company-paths.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var checkCompanyPaths = (0, import_pieces_framework3.createAction)({
  auth: villageAuth,
  name: "check_company_paths",
  displayName: "Check Company Paths",
  description: "Quickly check if you have connections to a company without fetching full details. Returns whether paths exist, an overall score, count of reachable people, and avatar thumbnails.",
  audience: "both",
  aiMetadata: {
    description: "Read-only lightweight check of whether any introduction paths to a company exist, identified by domain, LinkedIn URL, or generic url (supply exactly one). Use as a cheap yes/no-with-score screen before calling Get Company Paths, which returns the actual reachable employees. Pure query, safe to retry.",
    idempotent: !0
  },
  props: {
    domain: import_pieces_framework3.Property.ShortText({
      displayName: "Domain",
      description: 'Company domain, e.g. "acme.com" (provide one of: domain, linkedin_url, url)',
      required: !1
    }),
    linkedin_url: import_pieces_framework3.Property.ShortText({
      displayName: "LinkedIn URL",
      description: 'LinkedIn company page URL, e.g. "https://linkedin.com/company/acme-corp" (provide one of: domain, linkedin_url, url)',
      required: !1
    }),
    url: import_pieces_framework3.Property.ShortText({
      displayName: "URL",
      description: "Generic company URL (auto-detected) (provide one of: domain, linkedin_url, url)",
      required: !1
    })
  },
  async run(context) {
    let { domain, linkedin_url, url } = context.propsValue, provided = [domain, linkedin_url, url].filter((value) => value !== void 0 && value !== "");
    if (provided.length === 0)
      throw new Error("Provide one of: domain, linkedin_url, url");
    if (provided.length > 1)
      throw new Error("Provide only one of: domain, linkedin_url, url");
    let identifier = domain ? { domain } : linkedin_url ? { linkedin_url } : { url };
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: `${VILLAGE_API_BASE_URL}/v2/companies/paths/check`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      body: identifier
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/companies/companies-refresh.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var companiesRefresh = (0, import_pieces_framework4.createAction)({
  auth: villageAuth,
  name: "companies_refresh",
  displayName: "Refresh Companies",
  description: "Refresh/import company data from LinkedIn URLs or domains. Realtime mode returns enriched data synchronously (or times out after 25s); async mode returns job IDs for later status checking. At least one of linkedin_urls or domains must be provided.",
  audience: "both",
  aiMetadata: {
    description: "Trigger a re-fetch or first-time import of companies from LinkedIn URLs and/or domains; it kicks off backend processing, so treat it as a non-idempotent write. Set realtime=true to wait up to ~25s for inline data, or false to get job IDs; pass those job_ids back on a later call to poll async results. To read an already-imported company without re-fetching, use Enrich Company.",
    idempotent: !1
  },
  props: {
    job_ids: import_pieces_framework4.Property.Array({
      displayName: "Job IDs",
      description: "Job IDs to check status for (returned by async refresh calls).",
      required: !1
    }),
    linkedin_urls: import_pieces_framework4.Property.Array({
      displayName: "LinkedIn URLs",
      description: "LinkedIn company page URLs to refresh.",
      required: !1
    }),
    domains: import_pieces_framework4.Property.Array({
      displayName: "Domains",
      description: 'Company domains to refresh, e.g. "example.com".',
      required: !1
    }),
    realtime: import_pieces_framework4.Property.Checkbox({
      displayName: "Realtime",
      description: "If true, wait for enrichment (up to ~25s) and return company data inline. If false, return job IDs immediately.",
      required: !0
    })
  },
  async run(context) {
    let { linkedin_urls, domains, realtime, job_ids } = context.propsValue, linkedinUrlsArray = linkedin_urls && Array.isArray(linkedin_urls) ? linkedin_urls.map((value) => String(value)) : [], domainsArray = domains && Array.isArray(domains) ? domains.map((value) => String(value)) : [], jobIdsArray = job_ids && Array.isArray(job_ids) ? job_ids.map((value) => String(value)) : [];
    if (linkedinUrlsArray.length === 0 && domainsArray.length === 0 && jobIdsArray.length === 0)
      throw new Error("At least one of linkedin_urls, domains, or job_ids must be provided");
    let body = { realtime };
    return linkedinUrlsArray.length > 0 && (body.linkedin_urls = linkedinUrlsArray), domainsArray.length > 0 && (body.domains = domainsArray), jobIdsArray.length > 0 && (body.job_ids = jobIdsArray), (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: `${VILLAGE_API_BASE_URL}/v2/companies/refresh`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/companies/enrich-company.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var enrichCompany = (0, import_pieces_framework5.createAction)({
  auth: villageAuth,
  name: "enrich_company",
  displayName: "Enrich Company",
  description: "Get detailed information about a company by domain, LinkedIn URL, or any URL. Returns name, website, LinkedIn URL, logo, employee count, industry, founded year, and description.",
  audience: "both",
  aiMetadata: {
    description: "Read-only firmographic lookup for one company you can already identify by domain, LinkedIn URL, or generic url (supply exactly one). Use when you have the identifier; for multiple companies use Enrich Company (Bulk), and to find companies by criteria use Search Companies. Pure query, safe to retry.",
    idempotent: !0
  },
  props: {
    domain: import_pieces_framework5.Property.ShortText({
      displayName: "Domain",
      description: 'Company domain, e.g. "example.com" (provide one of: domain, linkedin_url, url)',
      required: !1
    }),
    linkedin_url: import_pieces_framework5.Property.ShortText({
      displayName: "LinkedIn URL",
      description: 'LinkedIn company page URL, e.g. "https://linkedin.com/company/acme-corp" (provide one of: domain, linkedin_url, url)',
      required: !1
    }),
    url: import_pieces_framework5.Property.ShortText({
      displayName: "URL",
      description: "Generic company URL (auto-detected) (provide one of: domain, linkedin_url, url)",
      required: !1
    })
  },
  async run(context) {
    let { domain, linkedin_url, url } = context.propsValue, provided = [domain, linkedin_url, url].filter((value) => value !== void 0 && value !== "");
    if (provided.length === 0)
      throw new Error("Provide one of: domain, linkedin_url, url");
    if (provided.length > 1)
      throw new Error("Provide only one of: domain, linkedin_url, url");
    let identifier = domain ? { domain } : linkedin_url ? { linkedin_url } : { url };
    return (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.POST,
      url: `${VILLAGE_API_BASE_URL}/v2/companies/enrich`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      body: identifier
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/companies/enrich-company-bulk.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var MAX_IDENTIFIERS = 10, enrichCompanyBulk = (0, import_pieces_framework6.createAction)({
  auth: villageAuth,
  name: "enrich_company_bulk",
  displayName: "Enrich Company (Bulk)",
  description: "Get detailed information for up to 10 companies in a single request. Each result contains either company data or an error.",
  audience: "both",
  aiMetadata: {
    description: "Read-only batch lookup of detailed firmographics for up to 10 companies, each identified by domain, LinkedIn URL, or generic url. Use when you already have multiple known companies; for one company use Enrich Company, and for criteria-based discovery use Search Companies. Per-row errors are returned for misses. Pure query, safe to retry.",
    idempotent: !0
  },
  props: {
    identifiers: import_pieces_framework6.Property.Array({
      displayName: "Identifiers",
      description: "Up to 10 companies to look up. Each row specifies an identifier type and value.",
      properties: {
        identifierType: import_pieces_framework6.Property.StaticDropdown({
          displayName: "Identifier Type",
          required: !0,
          options: {
            options: [
              { label: "Domain", value: "domain" },
              { label: "LinkedIn URL", value: "linkedin_url" },
              { label: "URL (auto-detected)", value: "url" }
            ]
          }
        }),
        identifierValue: import_pieces_framework6.Property.ShortText({
          displayName: "Identifier Value",
          required: !0
        })
      },
      required: !0,
      defaultValue: []
    })
  },
  async run(context) {
    var _a;
    let rows = (_a = context.propsValue.identifiers) != null ? _a : [];
    if (rows.length === 0)
      throw new Error("At least one identifier is required");
    if (rows.length > MAX_IDENTIFIERS)
      throw new Error(`Maximum ${MAX_IDENTIFIERS} identifiers per bulk request`);
    let identifiers = rows.map((row, index) => {
      if (!row.identifierType || !row.identifierValue)
        throw new Error(
          `Identifier at index ${index} is missing identifierType or identifierValue`
        );
      return { [row.identifierType]: row.identifierValue };
    });
    return (await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.POST,
      url: `${VILLAGE_API_BASE_URL}/v2/companies/enrich/bulk`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      body: { identifiers }
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/companies/get-company-paths.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var getCompanyPaths = (0, import_pieces_framework7.createAction)({
  auth: villageAuth,
  name: "get_company_paths",
  displayName: "Get Company Paths",
  description: "Find introduction paths to people at a specific company. Returns up to 50 employees you can reach through your network, ranked by connection strength.",
  audience: "both",
  aiMetadata: {
    description: "Read-only lookup of the reachable employees at one company (identified by domain, LinkedIn URL, or generic url; supply exactly one), ranked by connection strength. Use when you want the actual people to approach; for a cheaper exists/score screen first use Check Company Paths. Cursor-paginated and safe to retry.",
    idempotent: !0
  },
  props: {
    domain: import_pieces_framework7.Property.ShortText({
      displayName: "Domain",
      description: 'Company domain, e.g. "acme.com" (provide one of: domain, linkedin_url, url)',
      required: !1
    }),
    linkedin_url: import_pieces_framework7.Property.ShortText({
      displayName: "LinkedIn URL",
      description: 'LinkedIn company page URL, e.g. "https://linkedin.com/company/acme-corp" (provide one of: domain, linkedin_url, url)',
      required: !1
    }),
    url: import_pieces_framework7.Property.ShortText({
      displayName: "URL",
      description: "Generic company URL (auto-detected) (provide one of: domain, linkedin_url, url)",
      required: !1
    }),
    cursor: import_pieces_framework7.Property.ShortText({
      displayName: "Cursor",
      description: "Opaque cursor for pagination (from previous response)",
      required: !1
    }),
    limit: import_pieces_framework7.Property.Number({
      displayName: "Limit",
      description: "Number of results per page (default 25, max 50)",
      required: !1
    }),
    connection_degrees: import_pieces_framework7.Property.Array({
      displayName: "Connection Degrees",
      description: "Filter by connection degree (1=direct, 2=second-degree, 3=third-degree). Provide one or more values.",
      required: !1
    })
  },
  async run(context) {
    let { domain, linkedin_url, url, cursor, limit, connection_degrees } = context.propsValue, provided = [domain, linkedin_url, url].filter((value) => value !== void 0 && value !== "");
    if (provided.length === 0)
      throw new Error("Provide one of: domain, linkedin_url, url");
    if (provided.length > 1)
      throw new Error("Provide only one of: domain, linkedin_url, url");
    let body = __spreadValues({}, domain ? { domain } : linkedin_url ? { linkedin_url } : { url });
    return cursor !== void 0 && (body.cursor = cursor), limit !== void 0 && (body.limit = limit), connection_degrees !== void 0 && Array.isArray(connection_degrees) && (body.connection_degrees = connection_degrees.map((value) => Number(value))), (await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.POST,
      url: `${VILLAGE_API_BASE_URL}/v2/companies/paths`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/companies/list-companies.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var listCompanies = (0, import_pieces_framework8.createAction)({
  auth: villageAuth,
  name: "list_companies",
  displayName: "List Companies",
  description: "Get your top-connected companies ranked by network strength. Returns a paginated list with connection score, label, LinkedIn URL, domain, and enrichment data.",
  audience: "both",
  aiMetadata: {
    description: "Read-only browse of the companies your network reaches best, ranked by connection strength, with optional connection-degree filtering. Use to enumerate companies you already have a path into; for criteria-based discovery use Search Companies. Cursor-paginated and safe to retry.",
    idempotent: !0
  },
  props: {
    cursor: import_pieces_framework8.Property.ShortText({
      displayName: "Cursor",
      description: "Opaque cursor for pagination (from previous response)",
      required: !1
    }),
    limit: import_pieces_framework8.Property.Number({
      displayName: "Limit",
      description: "Number of results per page (default 25, max 100)",
      required: !1
    }),
    connection_degrees: import_pieces_framework8.Property.Array({
      displayName: "Connection Degrees",
      description: "Filter by connection degree (1=direct, 2=second-degree, 3=third-degree). Provide one or more values.",
      required: !1
    })
  },
  async run(context) {
    let { cursor, limit, connection_degrees } = context.propsValue, body = {};
    return cursor !== void 0 && (body.cursor = cursor), limit !== void 0 && (body.limit = limit), connection_degrees !== void 0 && Array.isArray(connection_degrees) && (body.connection_degrees = connection_degrees.map((value) => Number(value))), (await import_pieces_common8.httpClient.sendRequest({
      method: import_pieces_common8.HttpMethod.POST,
      url: `${VILLAGE_API_BASE_URL}/v2/companies`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/companies/search-companies.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var searchCompanies = (0, import_pieces_framework9.createAction)({
  auth: villageAuth,
  name: "search_companies",
  displayName: "Search Companies",
  description: 'Search for companies using natural language (in the "prompt" field) and/or structured filters. Returns matching companies with relevance and connection-strength scores; supports cursor-based pagination.',
  audience: "both",
  aiMetadata: {
    description: "Read-only discovery search for companies by a natural-language prompt and/or structured firmographic filters (at least one required); scope restricts to your network or searches globally. Use when you do not already have a domain or URL \u2014 for a known company use Enrich Company. Cursor-paginated and safe to retry.",
    idempotent: !0
  },
  props: {
    prompt: import_pieces_framework9.Property.LongText({
      displayName: "Prompt",
      description: 'Natural language search query, e.g. "Find B2B SaaS companies with 50-200 employees in California". Either prompt or filters must be provided.',
      required: !1
    }),
    filters: import_pieces_framework9.Property.Json({
      displayName: "Filters",
      description: "Structured filters. Supported keys: name, domain, linkedinUrl, linkedinId, industry, companySize, companyType, country, city, foundedAfter, foundedBefore, keywords.",
      required: !1
    }),
    limit: import_pieces_framework9.Property.Number({
      displayName: "Limit",
      description: "Maximum number of results per page (default 25, max 100)",
      required: !1
    }),
    mode: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Mode",
      description: "Search mode: auto (let system decide), lookup (direct match), simple (LLM-powered), or complex (thinking + high-reasoning SQL generation).",
      required: !1,
      options: {
        options: [
          { label: "Auto", value: "auto" },
          { label: "Lookup", value: "lookup" },
          { label: "Simple", value: "simple" },
          { label: "Complex", value: "complex" }
        ]
      }
    }),
    scope: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Scope",
      description: "Search scope: network (connections first) or global (all companies).",
      required: !1,
      options: {
        options: [
          { label: "Network", value: "network" },
          { label: "Global", value: "global" }
        ]
      }
    }),
    cursor: import_pieces_framework9.Property.ShortText({
      displayName: "Cursor",
      description: "Pagination cursor (encoded string) from a previous response.",
      required: !1
    }),
    network_filters: import_pieces_framework9.Property.Json({
      displayName: "Network Filters",
      description: "Network path and relationship filters.",
      required: !1
    }),
    include_counts: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Include Counts",
      description: "Inline count scope to compute on the first page only. Ignored when cursor is provided and adds latency.",
      required: !1,
      options: {
        options: [
          { label: "Global", value: "global" },
          { label: "Network", value: "network" },
          { label: "All", value: "all" }
        ]
      }
    })
  },
  async run(context) {
    let {
      prompt,
      filters,
      limit,
      mode,
      scope,
      cursor,
      network_filters,
      include_counts
    } = context.propsValue;
    if (!prompt && !filters)
      throw new Error("Either prompt or filters must be provided.");
    let body = {};
    return prompt !== void 0 && (body.prompt = prompt), filters !== void 0 && (body.filters = filters), limit !== void 0 && (body.limit = limit), mode !== void 0 && (body.mode = mode), scope !== void 0 && (body.scope = scope), cursor !== void 0 && (body.cursor = cursor), network_filters !== void 0 && (body.network_filters = network_filters), include_counts !== void 0 && (body.include_counts = include_counts), (await import_pieces_common9.httpClient.sendRequest({
      method: import_pieces_common9.HttpMethod.POST,
      url: `${VILLAGE_API_BASE_URL}/v2/companies/search`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/companies/sort-companies.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var MAX_COMPANIES = 100, sortCompanies = (0, import_pieces_framework10.createAction)({
  auth: villageAuth,
  name: "sort_companies",
  displayName: "Sort Companies",
  description: "Rank a list of companies (up to 100 LinkedIn URLs or domains) by how well-connected you are to people there. Each result includes a score, label, LinkedIn URL, and domain.",
  audience: "both",
  aiMetadata: {
    description: "Read-only ranking of a caller-supplied set of companies (up to 100 LinkedIn URLs or domains) by how strongly your network reaches people there, ordered strongest-first. Use when you already have specific companies to prioritize; to discover companies use Search Companies. Pure query, safe to retry.",
    idempotent: !0
  },
  props: {
    companies: import_pieces_framework10.Property.Array({
      displayName: "Companies",
      description: 'Array of company URLs (LinkedIn URLs or domains), e.g. "https://linkedin.com/company/acme-corp" or "example.com". Max 100.',
      required: !0
    })
  },
  async run(context) {
    let { companies } = context.propsValue;
    if (!Array.isArray(companies) || companies.length === 0)
      throw new Error("At least one company URL is required");
    if (companies.length > MAX_COMPANIES)
      throw new Error(`Maximum ${MAX_COMPANIES} companies per request`);
    return (await import_pieces_common10.httpClient.sendRequest({
      method: import_pieces_common10.HttpMethod.POST,
      url: `${VILLAGE_API_BASE_URL}/v2/companies/sort`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      body: {
        companies: companies.map((value) => String(value))
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/groups/join-group.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var joinGroup = (0, import_pieces_framework11.createAction)({
  auth: villageAuth,
  name: "join_group",
  displayName: "Join Group",
  description: "Join a group using an invite link. Once joined, you gain access to the shared network of all group members.",
  audience: "both",
  aiMetadata: {
    description: "Add yourself to a group via its invite-link code, which changes your membership and grants access to the group's shared network. This is a state-changing write, not a read; to enumerate groups you already belong to use List Groups, and to exit one use Leave Group.",
    idempotent: !1
  },
  props: {
    group_invite_link: import_pieces_framework11.Property.ShortText({
      displayName: "Group Invite Link",
      description: "The invite link code shared by a group admin or member",
      required: !0
    })
  },
  async run(context) {
    let { group_invite_link } = context.propsValue;
    return (await import_pieces_common11.httpClient.sendRequest({
      method: import_pieces_common11.HttpMethod.POST,
      url: `${VILLAGE_API_BASE_URL}/v2/groups/join`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      body: { group_invite_link }
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/groups/leave-group.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common");
var leaveGroup = (0, import_pieces_framework12.createAction)({
  auth: villageAuth,
  name: "leave_group",
  displayName: "Leave Group",
  description: "Leave a group you are a member of. If you are the last member, the group is deleted. Admins must remove all other members before leaving.",
  audience: "both",
  aiMetadata: {
    description: "Remove yourself from a group by group_id, a state-changing write that revokes your access to its shared network; if you are the last member the group is deleted entirely. Admins must remove all other members before they can leave. Find the group_id via List Groups.",
    idempotent: !1
  },
  props: {
    group_id: import_pieces_framework12.Property.ShortText({
      displayName: "Group ID",
      description: "ID of the group to leave",
      required: !0
    })
  },
  async run(context) {
    let { group_id } = context.propsValue;
    return (await import_pieces_common12.httpClient.sendRequest({
      method: import_pieces_common12.HttpMethod.POST,
      url: `${VILLAGE_API_BASE_URL}/v2/groups/leave`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      body: { group_id }
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/groups/list-groups.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common13 = require("@activepieces/pieces-common");
var listGroups = (0, import_pieces_framework13.createAction)({
  auth: villageAuth,
  name: "list_groups",
  displayName: "List Groups",
  description: "Get all groups (communities) you are a member of. Groups allow members to share network access with each other, expanding everyone's reach.",
  audience: "both",
  aiMetadata: {
    description: "Read-only listing of every group you currently belong to, useful for discovering group_ids needed by Leave Group or Create or Update Group. Takes no input. Pure query, safe to retry.",
    idempotent: !0
  },
  props: {},
  async run(context) {
    return (await import_pieces_common13.httpClient.sendRequest({
      method: import_pieces_common13.HttpMethod.GET,
      url: `${VILLAGE_API_BASE_URL}/v2/groups`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` }
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/groups/upsert-group.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_common14 = require("@activepieces/pieces-common");
var upsertGroup = (0, import_pieces_framework14.createAction)({
  auth: villageAuth,
  name: "upsert_group",
  displayName: "Create or Update Group",
  description: "Create a new group or update an existing one. To create: provide a name (id is auto-generated). To update: provide group_id (requires admin permissions).",
  audience: "both",
  aiMetadata: {
    description: "Create a new group or update an existing one in a single write. Omit group_id to create (a new id is generated); pass an existing group_id to update its name/logo (requires admin permissions). Updating by a stable group_id is idempotent, but omitting it creates a fresh group on each call.",
    idempotent: !0
  },
  props: {
    name: import_pieces_framework14.Property.ShortText({
      displayName: "Name",
      description: "Group name (max 100 characters)",
      required: !0
    }),
    group_id: import_pieces_framework14.Property.ShortText({
      displayName: "Group ID",
      description: "Existing group ID to update. Omit to create a new group.",
      required: !1
    }),
    logo: import_pieces_framework14.Property.ShortText({
      displayName: "Logo",
      description: "URL to the group logo image",
      required: !1
    })
  },
  async run(context) {
    let { name, group_id, logo } = context.propsValue, body = { name };
    return group_id !== void 0 && (body.group_id = group_id), logo !== void 0 && (body.logo = logo), (await import_pieces_common14.httpClient.sendRequest({
      method: import_pieces_common14.HttpMethod.POST,
      url: `${VILLAGE_API_BASE_URL}/v2/groups`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/integrations/list-integrations.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework"), import_pieces_common15 = require("@activepieces/pieces-common");
var listIntegrations = (0, import_pieces_framework15.createAction)({
  auth: villageAuth,
  name: "list_integrations",
  displayName: "List Gmail Integrations",
  description: "Get all your connected Gmail integrations, including rate limits, timezone, the default sending account, and whether you can add more based on your plan.",
  audience: "both",
  aiMetadata: {
    description: "List the caller's connected Gmail integrations with their rate limits, timezone, and default-sender status. Read-only and idempotent. Use to discover integration IDs before updating or resyncing one.",
    idempotent: !0
  },
  props: {},
  async run(context) {
    return (await import_pieces_common15.httpClient.sendRequest({
      method: import_pieces_common15.HttpMethod.GET,
      url: `${VILLAGE_API_BASE_URL}/v2/integrations`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` }
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/integrations/resync-integration.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework"), import_pieces_common16 = require("@activepieces/pieces-common");
var resyncIntegration = (0, import_pieces_framework16.createAction)({
  auth: villageAuth,
  name: "resync_integration",
  displayName: "Reset Integration",
  description: "Trigger a full resync of a Google or LinkedIn integration. Clears sync cursors and re-imports all contacts/calendar data; all graph operations use MERGE so it is fully idempotent.",
  audience: "both",
  aiMetadata: {
    description: "Trigger a full resync of a Google or LinkedIn integration by numeric ID, clearing sync cursors and re-importing all contacts and calendar data. Idempotent: graph writes use MERGE, so repeating it re-imports without creating duplicates. Use when synced network data looks stale or incomplete.",
    idempotent: !0
  },
  props: {
    id: import_pieces_framework16.Property.Number({
      displayName: "Integration ID",
      description: "Numeric ID of the integration to resync",
      required: !0
    })
  },
  async run(context) {
    let { id } = context.propsValue;
    return (await import_pieces_common16.httpClient.sendRequest({
      method: import_pieces_common16.HttpMethod.POST,
      url: `${VILLAGE_API_BASE_URL}/v2/integrations/${id}/resync`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` }
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/integrations/update-integration.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework"), import_pieces_common17 = require("@activepieces/pieces-common");
var updateIntegration = (0, import_pieces_framework17.createAction)({
  auth: villageAuth,
  name: "update_integration",
  displayName: "Update Gmail Integration",
  description: "Update settings for a connected Gmail integration \u2014 adjust rate limits, timezone, status, or set as the default sending account. Provide only the fields you want to change.",
  audience: "both",
  aiMetadata: {
    description: "Update settings on an existing connected Gmail integration by numeric ID: email send rate limits, minimum delay, timezone, connection status, or default-sender flag. Idempotent: only the supplied fields change and re-applying the same values is a no-op. Use List Gmail Integrations to find the ID first.",
    idempotent: !0
  },
  props: {
    id: import_pieces_framework17.Property.Number({
      displayName: "Integration ID",
      description: "Numeric ID of the integration to update",
      required: !0
    }),
    email_sending_per_day: import_pieces_framework17.Property.Number({
      displayName: "Emails Per Day",
      description: "Daily email sending limit (1-500)",
      required: !1
    }),
    email_sending_per_hour: import_pieces_framework17.Property.Number({
      displayName: "Emails Per Hour",
      description: "Hourly email sending limit (1-100)",
      required: !1
    }),
    email_sending_min_delay_seconds: import_pieces_framework17.Property.Number({
      displayName: "Minimum Delay (seconds)",
      description: "Minimum delay between emails in seconds (60-3600)",
      required: !1
    }),
    timezone: import_pieces_framework17.Property.ShortText({
      displayName: "Timezone",
      description: 'Timezone for scheduling, e.g. "America/New_York"',
      required: !1
    }),
    status: import_pieces_framework17.Property.StaticDropdown({
      displayName: "Status",
      description: "Integration status",
      required: !1,
      options: {
        options: [
          { label: "Connected", value: "connected" },
          { label: "Disconnected", value: "disconnected" },
          { label: "Failed", value: "failed" }
        ]
      }
    }),
    email_sending_default: import_pieces_framework17.Property.Checkbox({
      displayName: "Set as Default",
      description: "Whether this should be the default integration for email sending",
      required: !1
    })
  },
  async run(context) {
    let {
      id,
      email_sending_per_day,
      email_sending_per_hour,
      email_sending_min_delay_seconds,
      timezone,
      status,
      email_sending_default
    } = context.propsValue, assertRange = (value, min, max, field) => {
      if (!Number.isInteger(value) || value < min || value > max)
        throw new Error(`${field} must be an integer between ${min} and ${max} (got ${value})`);
    }, body = {};
    if (email_sending_per_day != null && (assertRange(email_sending_per_day, 1, 500, "email_sending_per_day"), body.email_sending_per_day = email_sending_per_day), email_sending_per_hour != null && (assertRange(email_sending_per_hour, 1, 100, "email_sending_per_hour"), body.email_sending_per_hour = email_sending_per_hour), email_sending_min_delay_seconds != null && (assertRange(email_sending_min_delay_seconds, 60, 3600, "email_sending_min_delay_seconds"), body.email_sending_min_delay_seconds = email_sending_min_delay_seconds), timezone != null && timezone !== "" && (body.timezone = timezone), status != null && (body.status = status), email_sending_default != null && (body.email_sending_default = email_sending_default), Object.keys(body).length === 0)
      throw new Error(
        "At least one field must be provided for update (email_sending_per_day, email_sending_per_hour, email_sending_min_delay_seconds, timezone, status, or email_sending_default)"
      );
    return (await import_pieces_common17.httpClient.sendRequest({
      method: import_pieces_common17.HttpMethod.PATCH,
      url: `${VILLAGE_API_BASE_URL}/v2/integrations/${id}`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/lists/add-list-items.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework"), import_pieces_common18 = require("@activepieces/pieces-common");
var MAX_ITEMS = 100, addListItems = (0, import_pieces_framework18.createAction)({
  auth: villageAuth,
  name: "add_list_items",
  displayName: "Add items to list",
  description: "Add people or companies to a list. Each identifier is resolved (LinkedIn URLs, company domains, emails, or graph IDs) and added. Returns detailed results per item.",
  audience: "both",
  aiMetadata: {
    description: "Add up to 100 people or companies to an existing list, resolving each identifier (LinkedIn URL, company domain, email, or graph ID) to an entity. Effectively idempotent per entity: re-adding one already on the list does not duplicate it. Per-item results report which identifiers resolved and were added.",
    idempotent: !0
  },
  props: {
    id: import_pieces_framework18.Property.ShortText({
      displayName: "List ID",
      description: "List ID",
      required: !0
    }),
    items: import_pieces_framework18.Property.Array({
      displayName: "Items",
      description: "Items to add to the list (max 100)",
      required: !0,
      properties: {
        identifier: import_pieces_framework18.Property.ShortText({
          displayName: "Identifier",
          description: "Identifier for the entity (LinkedIn URL, domain, email, or graph ID)",
          required: !0
        }),
        type: import_pieces_framework18.Property.StaticDropdown({
          displayName: "Type",
          description: "Entity type hint (optional, inferred from identifier if not provided)",
          required: !1,
          options: {
            options: [
              { label: "People", value: "people" },
              { label: "Company", value: "company" }
            ]
          }
        })
      }
    })
  },
  async run(context) {
    let { id, items } = context.propsValue, rows = items != null ? items : [];
    if (rows.length === 0)
      throw new Error("At least one item is required");
    if (rows.length > MAX_ITEMS)
      throw new Error(`Maximum ${MAX_ITEMS} items per request`);
    let normalizedItems = rows.map((row, index) => {
      if (!row.identifier)
        throw new Error(`Item at index ${index} is missing required 'identifier'`);
      let entry = { identifier: row.identifier };
      return row.type && (entry.type = row.type), entry;
    });
    return (await import_pieces_common18.httpClient.sendRequest({
      method: import_pieces_common18.HttpMethod.POST,
      url: `${VILLAGE_API_BASE_URL}/v2/lists/${encodeURIComponent(id)}/items`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      body: {
        items: normalizedItems
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/lists/check-list-membership.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework"), import_pieces_common19 = require("@activepieces/pieces-common");
var MAX_ENTITY_IDS = 1e3, checkListMembership = (0, import_pieces_framework19.createAction)({
  auth: villageAuth,
  name: "check_list_membership",
  displayName: "Check list membership",
  description: "Check which lists contain the given entities. Returns a map of entity_id to the lists that contain it. Entities not in any list are omitted.",
  audience: "both",
  aiMetadata: {
    description: "For up to 1000 entity IDs (all people or all companies), look up which lists each one belongs to. Read-only and idempotent despite using POST. Use to reverse the list-to-members relationship \u2014 finding an entity's memberships rather than reading a list's contents.",
    idempotent: !0
  },
  props: {
    entity_ids: import_pieces_framework19.Property.Array({
      displayName: "Entity IDs",
      description: "Array of entity IDs (person or company IDs) to check membership for (max 1000)",
      required: !0
    }),
    entity_type: import_pieces_framework19.Property.StaticDropdown({
      displayName: "Entity Type",
      description: "Type of entities being checked",
      required: !0,
      options: {
        options: [
          { label: "People", value: "people" },
          { label: "Company", value: "company" }
        ]
      }
    })
  },
  async run(context) {
    let { entity_ids, entity_type } = context.propsValue, ids = entity_ids != null ? entity_ids : [];
    if (ids.length === 0)
      throw new Error("At least one entity ID is required");
    if (ids.length > MAX_ENTITY_IDS)
      throw new Error(`Maximum ${MAX_ENTITY_IDS} entity IDs per request`);
    return (await import_pieces_common19.httpClient.sendRequest({
      method: import_pieces_common19.HttpMethod.POST,
      url: `${VILLAGE_API_BASE_URL}/v2/lists/check-membership`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      body: {
        entity_ids: ids,
        entity_type
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/lists/create-list.ts
var import_pieces_framework20 = require("@activepieces/pieces-framework"), import_pieces_common20 = require("@activepieces/pieces-common");
var createList = (0, import_pieces_framework20.createAction)({
  auth: villageAuth,
  name: "create_list",
  displayName: "Create a list",
  description: "Create a new list to organize people or companies. The creator becomes the list owner. List creation may be limited based on your subscription plan.",
  audience: "both",
  aiMetadata: {
    description: "Create a new people or company list, making the caller its owner. Not idempotent: each call creates a separate list even with the same title, and the entity type is fixed at creation. Subscription plan limits may cap how many lists can exist.",
    idempotent: !1
  },
  props: {
    title: import_pieces_framework20.Property.ShortText({
      displayName: "Title",
      description: "List title",
      required: !0
    }),
    type: import_pieces_framework20.Property.StaticDropdown({
      displayName: "Type",
      description: "Type of entities in the list",
      required: !1,
      defaultValue: "people",
      options: {
        options: [
          { label: "People", value: "people" },
          { label: "Company", value: "company" }
        ]
      }
    }),
    description: import_pieces_framework20.Property.LongText({
      displayName: "Description",
      description: "Optional list description",
      required: !1
    }),
    is_public: import_pieces_framework20.Property.Checkbox({
      displayName: "Is Public",
      description: "Whether the list is publicly accessible",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let { title, type, description, is_public } = context.propsValue, body = { title };
    return type && (body.type = type), description !== void 0 && (body.description = description), is_public !== void 0 && (body.is_public = is_public), (await import_pieces_common20.httpClient.sendRequest({
      method: import_pieces_common20.HttpMethod.POST,
      url: `${VILLAGE_API_BASE_URL}/v2/lists`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/lists/delete-list.ts
var import_pieces_framework21 = require("@activepieces/pieces-framework"), import_pieces_common21 = require("@activepieces/pieces-common");
var deleteList = (0, import_pieces_framework21.createAction)({
  auth: villageAuth,
  name: "delete_list",
  displayName: "Delete a list",
  description: "Permanently delete a list and all its items. This action cannot be undone.",
  audience: "both",
  aiMetadata: {
    description: "Permanently delete an entire list and all its items by list ID; this cannot be undone. To remove only specific entries while keeping the list, use Remove items from list instead. Effectively idempotent once deleted.",
    idempotent: !0
  },
  props: {
    id: import_pieces_framework21.Property.ShortText({
      displayName: "List ID",
      description: "List ID",
      required: !0
    })
  },
  async run(context) {
    let { id } = context.propsValue;
    return (await import_pieces_common21.httpClient.sendRequest({
      method: import_pieces_common21.HttpMethod.DELETE,
      url: `${VILLAGE_API_BASE_URL}/v2/lists/${encodeURIComponent(id)}`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` }
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/lists/get-list.ts
var import_pieces_framework22 = require("@activepieces/pieces-framework"), import_pieces_common22 = require("@activepieces/pieces-common");
var getList = (0, import_pieces_framework22.createAction)({
  auth: villageAuth,
  name: "get_list",
  displayName: "Get a list",
  description: "Get a list with all its items. Returns the list metadata plus paginated items. For people lists, items are sorted by connection strength (warmth score).",
  audience: "both",
  aiMetadata: {
    description: "Fetch a single list by ID along with its items, paginated via an opaque cursor. Read-only and idempotent. Use this to read members of one known list (and to obtain item IDs for removal); use List all lists to discover list IDs first.",
    idempotent: !0
  },
  props: {
    id: import_pieces_framework22.Property.ShortText({
      displayName: "List ID",
      description: "List ID",
      required: !0
    }),
    cursor: import_pieces_framework22.Property.ShortText({
      displayName: "Cursor",
      description: "Opaque cursor for items pagination (from previous response)",
      required: !1
    }),
    limit: import_pieces_framework22.Property.Number({
      displayName: "Limit",
      description: "Number of items per page (max 100)",
      required: !1,
      defaultValue: 20
    })
  },
  async run(context) {
    let { id, cursor, limit } = context.propsValue, queryParams = {};
    return cursor && (queryParams.cursor = cursor), limit != null && (queryParams.limit = String(limit)), (await import_pieces_common22.httpClient.sendRequest({
      method: import_pieces_common22.HttpMethod.GET,
      url: `${VILLAGE_API_BASE_URL}/v2/lists/${encodeURIComponent(id)}`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      queryParams
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/lists/list-lists.ts
var import_pieces_framework23 = require("@activepieces/pieces-framework"), import_pieces_common23 = require("@activepieces/pieces-common");
var listLists = (0, import_pieces_framework23.createAction)({
  auth: villageAuth,
  name: "list_lists",
  displayName: "List all lists",
  description: "Get all your saved lists of people or companies. Supports pagination, filtering by type, and searching by title.",
  audience: "both",
  aiMetadata: {
    description: "List the caller's saved lists, with optional cursor pagination, type filter (people or company), and title search. Read-only and idempotent. Use to discover list IDs and titles; use Get a list to read the items inside a specific list.",
    idempotent: !0
  },
  props: {
    cursor: import_pieces_framework23.Property.ShortText({
      displayName: "Cursor",
      description: "Opaque cursor for pagination (from previous response)",
      required: !1
    }),
    limit: import_pieces_framework23.Property.Number({
      displayName: "Limit",
      description: "Number of lists per page (max 100)",
      required: !1,
      defaultValue: 20
    }),
    type: import_pieces_framework23.Property.StaticDropdown({
      displayName: "Type",
      description: "Filter lists by type",
      required: !1,
      options: {
        options: [
          { label: "People", value: "people" },
          { label: "Company", value: "company" }
        ]
      }
    }),
    search: import_pieces_framework23.Property.ShortText({
      displayName: "Search",
      description: "Search lists by title",
      required: !1
    })
  },
  async run(context) {
    let { cursor, limit, type, search } = context.propsValue, queryParams = {};
    return cursor && (queryParams.cursor = cursor), limit != null && (queryParams.limit = String(limit)), type && (queryParams.type = type), search && (queryParams.search = search), (await import_pieces_common23.httpClient.sendRequest({
      method: import_pieces_common23.HttpMethod.GET,
      url: `${VILLAGE_API_BASE_URL}/v2/lists`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      queryParams
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/lists/remove-list-items.ts
var import_pieces_framework24 = require("@activepieces/pieces-framework"), import_pieces_common24 = require("@activepieces/pieces-common");
var MAX_ITEM_IDS = 100, removeListItems = (0, import_pieces_framework24.createAction)({
  auth: villageAuth,
  name: "remove_list_items",
  displayName: "Remove items from list",
  description: "Remove items from a list by their item IDs (obtained from get_list). Returns the count of successfully removed items.",
  audience: "both",
  aiMetadata: {
    description: "Remove up to 100 items from a list by their list-item IDs, which you must first obtain from Get a list (these are not entity/person IDs). Effectively idempotent: removing an item already gone is a no-op rather than an error.",
    idempotent: !0
  },
  props: {
    id: import_pieces_framework24.Property.ShortText({
      displayName: "List ID",
      description: "List ID",
      required: !0
    }),
    item_ids: import_pieces_framework24.Property.Array({
      displayName: "Item IDs",
      description: "Array of item IDs to remove from the list (max 100)",
      required: !0
    })
  },
  async run(context) {
    let { id, item_ids } = context.propsValue, ids = item_ids != null ? item_ids : [];
    if (ids.length === 0)
      throw new Error("At least one item ID is required");
    if (ids.length > MAX_ITEM_IDS)
      throw new Error(`Maximum ${MAX_ITEM_IDS} item IDs per request`);
    return (await import_pieces_common24.httpClient.sendRequest({
      method: import_pieces_common24.HttpMethod.DELETE,
      url: `${VILLAGE_API_BASE_URL}/v2/lists/${encodeURIComponent(id)}/items`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      body: {
        item_ids: ids
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/lists/update-list.ts
var import_pieces_framework25 = require("@activepieces/pieces-framework"), import_pieces_common25 = require("@activepieces/pieces-common");
var updateList = (0, import_pieces_framework25.createAction)({
  auth: villageAuth,
  name: "update_list",
  displayName: "Update a list",
  description: "Update a list's metadata. Provide at least one of: title or description. The list type cannot be changed after creation.",
  audience: "both",
  aiMetadata: {
    description: "Update an existing list's title and/or description by ID; at least one field must be supplied. Idempotent: applying the same values repeatedly yields the same result. The list type (people vs company) is fixed at creation and cannot be changed here.",
    idempotent: !0
  },
  props: {
    id: import_pieces_framework25.Property.ShortText({
      displayName: "List ID",
      description: "List ID",
      required: !0
    }),
    title: import_pieces_framework25.Property.ShortText({
      displayName: "Title",
      description: "New list title",
      required: !1
    }),
    description: import_pieces_framework25.Property.LongText({
      displayName: "Description",
      description: "New list description. Leave blank to keep unchanged.",
      required: !1
    })
  },
  async run(context) {
    let { id, title, description } = context.propsValue, body = {};
    if (title !== void 0 && (body.title = title), description !== void 0 && (body.description = description), Object.keys(body).length === 0)
      throw new Error("At least one field must be provided for update (title or description)");
    return (await import_pieces_common25.httpClient.sendRequest({
      method: import_pieces_common25.HttpMethod.PATCH,
      url: `${VILLAGE_API_BASE_URL}/v2/lists/${encodeURIComponent(id)}`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/people/enrich-person.ts
var import_pieces_framework26 = require("@activepieces/pieces-framework"), import_pieces_common26 = require("@activepieces/pieces-common");
var enrichPerson = (0, import_pieces_framework26.createAction)({
  auth: villageAuth,
  name: "enrich_person",
  displayName: "Enrich Person",
  description: "Get detailed profile information (name, headline, current company, location, photo, LinkedIn URL) for a person identified by LinkedIn URL or generic URL.",
  audience: "both",
  aiMetadata: {
    description: "Read-only profile lookup for one person you can already identify by their LinkedIn URL (or a generic url to auto-detect; supply exactly one). Use when you have the URL and want their profile attributes; to find candidates by criteria use Search People, and to get someone's email use Enrich Person Email (Bulk). Pure query, safe to retry.",
    idempotent: !0
  },
  props: {
    linkedin_url: import_pieces_framework26.Property.ShortText({
      displayName: "LinkedIn URL",
      description: "LinkedIn profile URL, e.g. https://linkedin.com/in/johndoe (provide this OR url)",
      required: !1
    }),
    url: import_pieces_framework26.Property.ShortText({
      displayName: "URL",
      description: "Generic URL \u2014 auto-detected (provide this OR linkedin_url)",
      required: !1
    })
  },
  async run(context) {
    let { linkedin_url, url } = context.propsValue;
    if (!linkedin_url && !url)
      throw new Error("Provide one of: linkedin_url, url");
    if (linkedin_url && url)
      throw new Error("Provide only one of: linkedin_url, url");
    let identifier = linkedin_url ? { linkedin_url } : { url };
    return (await import_pieces_common26.httpClient.sendRequest({
      method: import_pieces_common26.HttpMethod.POST,
      url: `${VILLAGE_API_BASE_URL}/v2/people/enrich`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      body: identifier
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/people/enrich-person-bulk.ts
var import_pieces_framework27 = require("@activepieces/pieces-framework"), import_pieces_common27 = require("@activepieces/pieces-common");
var MAX_BULK_IDENTIFIERS = 10, IDENTIFIER_TYPE_LINKEDIN = "linkedin_url", IDENTIFIER_TYPE_URL = "url", enrichPersonBulk = (0, import_pieces_framework27.createAction)({
  auth: villageAuth,
  name: "enrich_person_bulk",
  displayName: "Enrich Person (Bulk)",
  description: "Get detailed profile information for up to 10 people in a single request. Partial results are returned if some lookups fail \u2014 check each result for success or error status.",
  audience: "both",
  aiMetadata: {
    description: "Retrieve detailed profiles for up to 10 people in one call, each identified by a LinkedIn URL or generic URL. Read-only and idempotent; partial results are returned with per-person success or error status when some lookups fail. Use for full profile enrichment; use Enrich Person Email when only an email address is needed.",
    idempotent: !0
  },
  props: {
    identifiers: import_pieces_framework27.Property.Array({
      displayName: "Identifiers",
      description: "Up to 10 people to enrich. Each row is one person.",
      required: !0,
      defaultValue: [],
      properties: {
        identifierType: import_pieces_framework27.Property.StaticDropdown({
          displayName: "Identifier Type",
          required: !0,
          options: {
            options: [
              { label: "LinkedIn URL", value: IDENTIFIER_TYPE_LINKEDIN },
              { label: "URL (auto-detected)", value: IDENTIFIER_TYPE_URL }
            ]
          }
        }),
        identifierValue: import_pieces_framework27.Property.ShortText({
          displayName: "Identifier Value",
          description: "For LinkedIn URL: a LinkedIn person URL. For URL: any URL to auto-detect.",
          required: !0
        })
      }
    })
  },
  async run(context) {
    let { identifiers } = context.propsValue;
    if (!Array.isArray(identifiers) || identifiers.length === 0)
      throw new Error("At least one identifier is required.");
    if (identifiers.length > MAX_BULK_IDENTIFIERS)
      throw new Error(`Maximum ${MAX_BULK_IDENTIFIERS} identifiers per bulk request.`);
    let body = {
      identifiers: identifiers.map((row, index) => {
        if (!(row != null && row.identifierType) || !(row != null && row.identifierValue))
          throw new Error(
            `Identifier at index ${index} is missing identifierType or identifierValue.`
          );
        return { [row.identifierType]: row.identifierValue };
      })
    };
    return (await import_pieces_common27.httpClient.sendRequest({
      method: import_pieces_common27.HttpMethod.POST,
      url: `${VILLAGE_API_BASE_URL}/v2/people/enrich/bulk`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/people/enrich-person-email.ts
var import_pieces_framework28 = require("@activepieces/pieces-framework"), import_pieces_common28 = require("@activepieces/pieces-common");
var enrichPersonEmail = (0, import_pieces_framework28.createAction)({
  auth: villageAuth,
  name: "enrich_person_email",
  displayName: "Enrich Person Email",
  description: "Find the email address for a person by LinkedIn URL or generic URL. Returns the verified email address if found, or null if unavailable.",
  audience: "both",
  aiMetadata: {
    description: "Look up a single person's email address from exactly one identifier \u2014 either a LinkedIn URL or a generic URL (provide one, not both). Read-only and idempotent. Use for email discovery specifically; use Enrich Person (Bulk) for full profile data on many people.",
    idempotent: !0
  },
  props: {
    linkedin_url: import_pieces_framework28.Property.ShortText({
      displayName: "LinkedIn URL",
      description: "LinkedIn profile URL, e.g. https://linkedin.com/in/johndoe (provide this OR url)",
      required: !1
    }),
    url: import_pieces_framework28.Property.ShortText({
      displayName: "URL",
      description: "Generic URL \u2014 auto-detected (provide this OR linkedin_url)",
      required: !1
    })
  },
  async run(context) {
    let { linkedin_url, url } = context.propsValue;
    if (!linkedin_url && !url)
      throw new Error("Provide one of: linkedin_url, url");
    if (linkedin_url && url)
      throw new Error("Provide only one of: linkedin_url, url");
    let identifier = linkedin_url ? { linkedin_url } : { url };
    return (await import_pieces_common28.httpClient.sendRequest({
      method: import_pieces_common28.HttpMethod.POST,
      url: `${VILLAGE_API_BASE_URL}/v2/people/enrich/emails`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      body: identifier
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/people/enrich-person-email-bulk.ts
var import_pieces_framework29 = require("@activepieces/pieces-framework"), import_pieces_common29 = require("@activepieces/pieces-common");
var MAX_BULK_IDENTIFIERS2 = 10, IDENTIFIER_TYPE_LINKEDIN2 = "linkedin_url", IDENTIFIER_TYPE_URL2 = "url", enrichPersonEmailBulk = (0, import_pieces_framework29.createAction)({
  auth: villageAuth,
  name: "enrich_person_email_bulk",
  displayName: "Enrich Person Email (Bulk)",
  description: "Find email addresses for up to 10 people in a single request. Returns email addresses where found, with error messages for failed lookups.",
  audience: "both",
  aiMetadata: {
    description: "Read-only batch lookup of email addresses for up to 10 people, each identified by a LinkedIn URL or generic URL. Use specifically when you need contact emails; for general profile attributes use Enrich Person. Per-row errors are returned for misses rather than failing the whole call. Pure query, safe to retry.",
    idempotent: !0
  },
  props: {
    identifiers: import_pieces_framework29.Property.Array({
      displayName: "Identifiers",
      description: "Up to 10 people to look up emails for. Each row is one person.",
      required: !0,
      defaultValue: [],
      properties: {
        identifierType: import_pieces_framework29.Property.StaticDropdown({
          displayName: "Identifier Type",
          required: !0,
          options: {
            options: [
              { label: "LinkedIn URL", value: IDENTIFIER_TYPE_LINKEDIN2 },
              { label: "URL (auto-detected)", value: IDENTIFIER_TYPE_URL2 }
            ]
          }
        }),
        identifierValue: import_pieces_framework29.Property.ShortText({
          displayName: "Identifier Value",
          description: "For LinkedIn URL: a LinkedIn person URL. For URL: any URL to auto-detect.",
          required: !0
        })
      }
    })
  },
  async run(context) {
    let { identifiers } = context.propsValue;
    if (!Array.isArray(identifiers) || identifiers.length === 0)
      throw new Error("At least one identifier is required.");
    if (identifiers.length > MAX_BULK_IDENTIFIERS2)
      throw new Error(`Maximum ${MAX_BULK_IDENTIFIERS2} identifiers per bulk request.`);
    let body = {
      identifiers: identifiers.map((row, index) => {
        if (!(row != null && row.identifierType) || !(row != null && row.identifierValue))
          throw new Error(
            `Identifier at index ${index} is missing identifierType or identifierValue.`
          );
        return { [row.identifierType]: row.identifierValue };
      })
    };
    return (await import_pieces_common29.httpClient.sendRequest({
      method: import_pieces_common29.HttpMethod.POST,
      url: `${VILLAGE_API_BASE_URL}/v2/people/enrich/emails/bulk`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/people/get-person-paths.ts
var import_pieces_framework30 = require("@activepieces/pieces-framework"), import_pieces_common30 = require("@activepieces/pieces-common");
var getPersonPaths = (0, import_pieces_framework30.createAction)({
  auth: villageAuth,
  name: "get_person_paths",
  displayName: "Get Person Paths",
  description: "Find introduction paths to reach a specific person through your professional network. Provide a LinkedIn URL and get back direct connections, mutual contacts, and connection strength scores (0-100).",
  audience: "both",
  aiMetadata: {
    description: "Read-only lookup of warm-introduction paths to one person via your network, identified by their LinkedIn URL (or a generic url to auto-detect; supply exactly one). Use for a single target; for many targets at once use Get Person Paths (Bulk). Pure query, safe to retry.",
    idempotent: !0
  },
  props: {
    linkedin_url: import_pieces_framework30.Property.ShortText({
      displayName: "LinkedIn URL",
      description: "LinkedIn profile URL of the target person, e.g. https://linkedin.com/in/johndoe (provide this OR url)",
      required: !1
    }),
    url: import_pieces_framework30.Property.ShortText({
      displayName: "URL",
      description: "Generic URL \u2014 auto-detected (provide this OR linkedin_url)",
      required: !1
    })
  },
  async run(context) {
    let { linkedin_url, url } = context.propsValue;
    if (!linkedin_url && !url)
      throw new Error("Provide one of: linkedin_url, url");
    if (linkedin_url && url)
      throw new Error("Provide only one of: linkedin_url, url");
    let identifier = linkedin_url ? { linkedin_url } : { url };
    return (await import_pieces_common30.httpClient.sendRequest({
      method: import_pieces_common30.HttpMethod.POST,
      url: `${VILLAGE_API_BASE_URL}/v2/people/paths`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      body: identifier
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/people/get-person-paths-bulk.ts
var import_pieces_framework31 = require("@activepieces/pieces-framework"), import_pieces_common31 = require("@activepieces/pieces-common");
var MAX_BULK_LINKEDIN_IDS = 100, getPersonPathsBulk = (0, import_pieces_framework31.createAction)({
  auth: villageAuth,
  name: "get_person_paths_bulk",
  displayName: "Get Person Paths (Bulk)",
  description: "Find introduction paths to multiple people in a single request. Provide up to 100 LinkedIn IDs (the slug after linkedin.com/in/) and get connection paths for each.",
  audience: "both",
  aiMetadata: {
    description: "Read-only batch lookup of warm-introduction paths to many people at once, keyed by LinkedIn ID (the slug after linkedin.com/in/, not a full URL). Pick this over Get Person Paths when you have more than one target to evaluate together; capped at 100 IDs per call. Pure query, safe to retry.",
    idempotent: !0
  },
  props: {
    linkedin_ids: import_pieces_framework31.Property.Array({
      displayName: "LinkedIn IDs",
      description: 'LinkedIn identifiers (e.g. "johndoe" from linkedin.com/in/johndoe). Max 100 per request.',
      required: !0,
      defaultValue: []
    })
  },
  async run(context) {
    let { linkedin_ids } = context.propsValue;
    if (!Array.isArray(linkedin_ids) || linkedin_ids.length === 0)
      throw new Error("At least one LinkedIn ID is required.");
    if (linkedin_ids.length > MAX_BULK_LINKEDIN_IDS)
      throw new Error(`Maximum ${MAX_BULK_LINKEDIN_IDS} LinkedIn IDs per bulk request.`);
    return (await import_pieces_common31.httpClient.sendRequest({
      method: import_pieces_common31.HttpMethod.POST,
      url: `${VILLAGE_API_BASE_URL}/v2/people/paths/bulk`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      body: { linkedin_ids }
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/people/list-people.ts
var import_pieces_framework32 = require("@activepieces/pieces-framework"), import_pieces_common32 = require("@activepieces/pieces-common");
var listPeople = (0, import_pieces_framework32.createAction)({
  auth: villageAuth,
  name: "list_people",
  displayName: "List People",
  description: "Get your network contacts ranked by connection strength. Returns a paginated list with enrichment data, supporting cursor-based pagination and optional connection-degree filtering.",
  audience: "both",
  aiMetadata: {
    description: "Read-only browse of your own network contacts ranked by connection strength, with optional filtering by connection degree or current company domain. Use to enumerate who you already know; for criteria-based discovery beyond your contacts use Search People. Cursor-paginated and safe to retry.",
    idempotent: !0
  },
  props: {
    cursor: import_pieces_framework32.Property.ShortText({
      displayName: "Cursor",
      description: "Opaque cursor for pagination (from previous response)",
      required: !1
    }),
    limit: import_pieces_framework32.Property.Number({
      displayName: "Limit",
      description: "Number of results per page (default 25, max 100)",
      required: !1,
      defaultValue: 25
    }),
    connection_degrees: import_pieces_framework32.Property.Array({
      displayName: "Connection Degrees",
      description: "Filter by connection degree (1=direct, 2=second-degree, 3=third-degree). Provide one or more values.",
      required: !1
    }),
    company_domain: import_pieces_framework32.Property.ShortText({
      displayName: "Company Domain",
      description: 'Filter by current company domain (exact match), e.g. "google.com"',
      required: !1
    })
  },
  async run(context) {
    let { cursor, limit, connection_degrees, company_domain } = context.propsValue, body = {};
    return cursor !== void 0 && (body.cursor = cursor), limit !== void 0 && (body.limit = limit), connection_degrees !== void 0 && Array.isArray(connection_degrees) && (body.connection_degrees = connection_degrees.map((value) => Number(value))), company_domain !== void 0 && (body.company_domain = company_domain), (await import_pieces_common32.httpClient.sendRequest({
      method: import_pieces_common32.HttpMethod.POST,
      url: `${VILLAGE_API_BASE_URL}/v2/people`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/people/people-refresh.ts
var import_pieces_framework33 = require("@activepieces/pieces-framework"), import_pieces_common33 = require("@activepieces/pieces-common");
var peopleRefresh = (0, import_pieces_framework33.createAction)({
  auth: villageAuth,
  name: "people_refresh",
  displayName: "Refresh People",
  description: "Refresh or import people data from LinkedIn URLs. In realtime mode, returns enriched person data immediately (or times out after 25 seconds and converts to async). In async mode, returns job IDs for later status checking.",
  audience: "both",
  aiMetadata: {
    description: "Trigger a re-fetch or first-time import of people into your network from LinkedIn URLs; this kicks off backend processing, so treat it as a non-idempotent write rather than a plain read. Set realtime=true to wait up to ~25s for enriched data inline, or false to get job IDs to poll later. To read already-imported profiles without re-fetching, use Enrich Person instead.",
    idempotent: !1
  },
  props: {
    linkedin_urls: import_pieces_framework33.Property.Array({
      displayName: "LinkedIn URLs",
      description: "LinkedIn profile URLs to refresh. At least one is required.",
      required: !0,
      defaultValue: []
    }),
    realtime: import_pieces_framework33.Property.Checkbox({
      displayName: "Realtime",
      description: "If true, waits synchronously for results (up to 25s). If false, returns job IDs for async processing.",
      required: !0
    })
  },
  async run(context) {
    let { linkedin_urls, realtime } = context.propsValue;
    if (!Array.isArray(linkedin_urls) || linkedin_urls.length === 0)
      throw new Error("At least one LinkedIn URL is required.");
    return (await import_pieces_common33.httpClient.sendRequest({
      method: import_pieces_common33.HttpMethod.POST,
      url: `${VILLAGE_API_BASE_URL}/v2/people/refresh`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      body: {
        linkedin_urls: linkedin_urls.map(String),
        realtime
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/people/search-people.ts
var import_pieces_framework34 = require("@activepieces/pieces-framework"), import_pieces_common34 = require("@activepieces/pieces-common");
var searchPeople = (0, import_pieces_framework34.createAction)({
  auth: villageAuth,
  name: "search_people",
  displayName: "Search People",
  description: 'Search for people using natural language queries. Pass your search text in "prompt" (e.g. "engineers at Google"). Supports cursor-based pagination and structured filters.',
  audience: "both",
  aiMetadata: {
    description: "Read-only discovery search for people by a natural-language prompt and/or structured filters (at least one required); scope controls whether you search your network, everyone (global), or a caller-supplied candidate list. Use when you do not already have a specific profile URL \u2014 for a known person use Enrich Person instead. Cursor-paginated and safe to retry.",
    idempotent: !0
  },
  props: {
    prompt: import_pieces_framework34.Property.LongText({
      displayName: "Prompt",
      description: 'Natural language search query (e.g. "Find CTOs at SaaS companies in San Francisco")',
      required: !1
    }),
    filters: import_pieces_framework34.Property.Json({
      displayName: "Filters",
      description: "Structured search filters as JSON (alternative or complement to prompt). Either prompt or filters must be provided.",
      required: !1
    }),
    limit: import_pieces_framework34.Property.Number({
      displayName: "Limit",
      description: "Maximum number of results per page (1-100, default 25)",
      required: !1,
      defaultValue: 25
    }),
    mode: import_pieces_framework34.Property.StaticDropdown({
      displayName: "Mode",
      description: 'Search execution mode. "auto" classifies the query. Use "lookup" for direct matches, "simple" for firmographic queries, and "complex" for multi-step queries.',
      required: !1,
      options: {
        options: [
          { label: "Auto", value: "auto" },
          { label: "Lookup", value: "lookup" },
          { label: "Simple", value: "simple" },
          { label: "Complex", value: "complex" }
        ]
      }
    }),
    scope: import_pieces_framework34.Property.StaticDropdown({
      displayName: "Scope",
      description: '"network" searches your connections first, "global" searches everyone, "specific_list" restricts to caller-provided identifiers (requires specific_list_data).',
      required: !1,
      options: {
        options: [
          { label: "Network", value: "network" },
          { label: "Global", value: "global" },
          { label: "Specific List", value: "specific_list" }
        ]
      }
    }),
    cursor: import_pieces_framework34.Property.ShortText({
      displayName: "Cursor",
      description: "Pagination cursor (encoded string from a previous response)",
      required: !1
    }),
    network_filters: import_pieces_framework34.Property.Json({
      displayName: "Network Filters",
      description: "Network filtering options as JSON",
      required: !1
    }),
    include_counts: import_pieces_framework34.Property.StaticDropdown({
      displayName: "Include Counts",
      description: "Inline count scopes to compute on the first page only. Ignored when cursor is provided and adds latency.",
      required: !1,
      options: {
        options: [
          { label: "Global", value: "global" },
          { label: "Network", value: "network" },
          { label: "All", value: "all" }
        ]
      }
    }),
    specific_list_data: import_pieces_framework34.Property.Json({
      displayName: "Specific List Data",
      description: 'Required when scope="specific_list". Caller-supplied candidate set (1..50,000 entries). Each entry is a bare LinkedIn identifier or { id, score? }.',
      required: !1
    })
  },
  async run(context) {
    let {
      prompt,
      filters,
      limit,
      mode,
      scope,
      cursor,
      network_filters,
      include_counts,
      specific_list_data
    } = context.propsValue;
    if (!prompt && !filters)
      throw new Error("Either prompt or filters must be provided.");
    let body = {};
    return prompt !== void 0 && (body.prompt = prompt), filters !== void 0 && (body.filters = filters), limit !== void 0 && (body.limit = limit), mode !== void 0 && (body.mode = mode), scope !== void 0 && (body.scope = scope), cursor !== void 0 && (body.cursor = cursor), network_filters !== void 0 && (body.network_filters = network_filters), include_counts !== void 0 && (body.include_counts = include_counts), specific_list_data !== void 0 && (body.specific_list_data = specific_list_data), (await import_pieces_common34.httpClient.sendRequest({
      method: import_pieces_common34.HttpMethod.POST,
      url: `${VILLAGE_API_BASE_URL}/v2/people/search`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/people/sort-people.ts
var import_pieces_framework35 = require("@activepieces/pieces-framework"), import_pieces_common35 = require("@activepieces/pieces-common");
var sortPeople = (0, import_pieces_framework35.createAction)({
  auth: villageAuth,
  name: "sort_people",
  displayName: "Sort People",
  description: "Rank a list of LinkedIn profile URLs by how well-connected you are to them. Returns each person sorted by connection strength (highest first), with score (0-100) and score_label.",
  audience: "both",
  aiMetadata: {
    description: "Read-only ranking of a caller-supplied set of people (LinkedIn profile URLs) by how strongly you are connected to each, ordered strongest-first. Use when you already have specific candidates and want them prioritized; to discover candidates use Search People. Pure query, safe to retry.",
    idempotent: !0
  },
  props: {
    people: import_pieces_framework35.Property.Array({
      displayName: "People",
      description: "Array of LinkedIn person profile URLs (e.g. https://linkedin.com/in/johndoe). At least one is required.",
      required: !0,
      defaultValue: []
    })
  },
  async run(context) {
    let { people } = context.propsValue;
    if (!Array.isArray(people) || people.length === 0)
      throw new Error("At least one person URL is required.");
    return (await import_pieces_common35.httpClient.sendRequest({
      method: import_pieces_common35.HttpMethod.POST,
      url: `${VILLAGE_API_BASE_URL}/v2/people/sort`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      body: { people: people.map(String) }
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/teams/join-team.ts
var import_pieces_framework36 = require("@activepieces/pieces-framework"), import_pieces_common36 = require("@activepieces/pieces-common");
var joinTeam = (0, import_pieces_framework36.createAction)({
  auth: villageAuth,
  name: "join_team",
  displayName: "Join Team",
  description: "Join a team using an invite link. Provide the invite link code (the part after /join/ in the URL).",
  audience: "both",
  aiMetadata: {
    description: "Add the authenticated user to a team using an invite-link code (the segment after /join/ in the share URL). Use when you have an invite code rather than wanting to create a team. Not idempotent in effect, though re-joining a team the user already belongs to is harmless.",
    idempotent: !1
  },
  props: {
    team_invite_link: import_pieces_framework36.Property.ShortText({
      displayName: "Team Invite Link",
      description: "The invite link code shared by a team admin (e.g. the part after /join/ in the invite URL)",
      required: !0
    })
  },
  async run(context) {
    let { team_invite_link } = context.propsValue;
    return (await import_pieces_common36.httpClient.sendRequest({
      method: import_pieces_common36.HttpMethod.POST,
      url: `${VILLAGE_API_BASE_URL}/v2/teams/join`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      body: { team_invite_link }
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/teams/leave-team.ts
var import_pieces_framework37 = require("@activepieces/pieces-framework"), import_pieces_common37 = require("@activepieces/pieces-common");
var leaveTeam = (0, import_pieces_framework37.createAction)({
  auth: villageAuth,
  name: "leave_team",
  displayName: "Leave Team",
  description: "Leave a team you are a member of. If you are the last member of a non-paid team, the team is deleted. Admins of paid teams cannot leave \u2014 use Cancel Plan instead.",
  audience: "both",
  aiMetadata: {
    description: "Remove the authenticated user from a team by team ID. Not idempotent: leaving the same team again fails since membership is already gone, and leaving as the last member of a non-paid team deletes the team. Admins of paid teams cannot leave this way.",
    idempotent: !1
  },
  props: {
    team_id: import_pieces_framework37.Property.ShortText({
      displayName: "Team ID",
      description: "ID of the team to leave",
      required: !0
    })
  },
  async run(context) {
    let { team_id } = context.propsValue;
    return (await import_pieces_common37.httpClient.sendRequest({
      method: import_pieces_common37.HttpMethod.POST,
      url: `${VILLAGE_API_BASE_URL}/v2/teams/leave`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      body: { team_id }
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/teams/list-teams.ts
var import_pieces_framework38 = require("@activepieces/pieces-framework"), import_pieces_common38 = require("@activepieces/pieces-common");
var listTeams = (0, import_pieces_framework38.createAction)({
  auth: villageAuth,
  name: "list_teams",
  displayName: "List Teams",
  description: "Get all teams you are a member of. Each team includes id, name, logo, invite_link, and an is_admin flag indicating your admin status.",
  audience: "both",
  aiMetadata: {
    description: "List every team the authenticated user belongs to, including each team ID and the admin-status flag. Read-only and idempotent. Use to discover team IDs before joining, leaving, or updating a team.",
    idempotent: !0
  },
  props: {},
  async run(context) {
    return (await import_pieces_common38.httpClient.sendRequest({
      method: import_pieces_common38.HttpMethod.GET,
      url: `${VILLAGE_API_BASE_URL}/v2/teams`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` }
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/teams/upsert-team.ts
var import_pieces_framework39 = require("@activepieces/pieces-framework"), import_pieces_common39 = require("@activepieces/pieces-common");
var upsertTeam = (0, import_pieces_framework39.createAction)({
  auth: villageAuth,
  name: "upsert_team",
  displayName: "Create or Update Team",
  description: "Create or update a team. The team ID is derived from the team name; repeated calls with the same name update the same team (requires admin permissions on update).",
  audience: "both",
  aiMetadata: {
    description: "Create a team, or update an existing one when a team with the same name already exists since the team ID is derived from the name. Idempotent by stable name: repeating the same name and fields converges to one team rather than creating duplicates (updating requires admin permission on that team).",
    idempotent: !0
  },
  props: {
    name: import_pieces_framework39.Property.ShortText({
      displayName: "Name",
      description: "Team name (max 100 characters)",
      required: !0
    }),
    logo: import_pieces_framework39.Property.ShortText({
      displayName: "Logo",
      description: "URL to the team logo image",
      required: !1
    })
  },
  async run(context) {
    let { name, logo } = context.propsValue, body = { name };
    return logo !== void 0 && (body.logo = logo), (await import_pieces_common39.httpClient.sendRequest({
      method: import_pieces_common39.HttpMethod.POST,
      url: `${VILLAGE_API_BASE_URL}/v2/teams`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/user/get-current-user.ts
var import_pieces_framework40 = require("@activepieces/pieces-framework"), import_pieces_common40 = require("@activepieces/pieces-common");
var getCurrentUser = (0, import_pieces_framework40.createAction)({
  auth: villageAuth,
  name: "get_current_user",
  displayName: "Get Current User",
  description: "Get profile information for the authenticated user \u2014 id, email, name, account creation date, sync completion status, and active flag. Useful for verifying auth and checking whether the user's network data is ready.",
  audience: "both",
  aiMetadata: {
    description: "Fetch the authenticated user's own profile, including their ID and a sync-completion flag. Read-only and idempotent. Use to confirm the token is valid and to check whether the user's network data has finished syncing before relying on graph queries.",
    idempotent: !0
  },
  props: {},
  async run(context) {
    return (await import_pieces_common40.httpClient.sendRequest({
      method: import_pieces_common40.HttpMethod.GET,
      url: `${VILLAGE_API_BASE_URL}/v2/user/me`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` }
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/lib/actions/user/import-relationships.ts
var import_pieces_framework41 = require("@activepieces/pieces-framework"), import_pieces_common41 = require("@activepieces/pieces-common");
var MAX_RELATIONSHIPS_PER_REQUEST = 1e3, MIN_SCORE = 0, MAX_SCORE = 10, USER_RELATIONSHIP_TYPE_OPTIONS = [
  { label: "Met in person", value: "met_in_person" },
  { label: "Worked together", value: "worked_together" },
  { label: "Studied together", value: "studied_together" },
  { label: "Attended event", value: "attended_event" },
  { label: "Introduced by someone", value: "introduced_by_someone" }
], importRelationships = (0, import_pieces_framework41.createAction)({
  auth: villageAuth,
  name: "import_relationships",
  displayName: "Import Relationships",
  description: "Import manual relationships to expand your network graph \u2014 useful for adding people you know but aren't connected to digitally. Provide email addresses or LinkedIn URLs with optional scores (0-10); higher scores indicate stronger relationships. Max 1000 per request.",
  audience: "both",
  aiMetadata: {
    description: "Import up to 1000 manual relationships into the network graph, each identified by email or LinkedIn URL with an optional 0-10 strength score and relationship type. Effectively idempotent per person: re-importing the same identifier updates that relationship rather than duplicating it. Use to add people known offline who would not surface from a synced integration.",
    idempotent: !0
  },
  props: {
    people: import_pieces_framework41.Property.Array({
      displayName: "People",
      description: "Up to 1000 people to import. Each row: identifier (email or LinkedIn URL), optional score (0-10), optional relationship type.",
      required: !0,
      defaultValue: [],
      properties: {
        identifier: import_pieces_framework41.Property.ShortText({
          displayName: "Identifier",
          description: "Email address or LinkedIn URL",
          required: !0
        }),
        score: import_pieces_framework41.Property.Number({
          displayName: "Score",
          description: `Relationship score (${MIN_SCORE}-${MAX_SCORE}). Falls back to "Default Score" or the system default.`,
          required: !1
        }),
        user_relationship_type: import_pieces_framework41.Property.StaticDropdown({
          displayName: "Relationship Type",
          description: "Optional category for how you know this person",
          required: !1,
          options: {
            options: USER_RELATIONSHIP_TYPE_OPTIONS
          }
        })
      }
    }),
    min_score: import_pieces_framework41.Property.Number({
      displayName: "Default Score",
      description: `Default score (${MIN_SCORE}-${MAX_SCORE}) applied to rows without an explicit score.`,
      required: !1
    })
  },
  async run(context) {
    let { people, min_score } = context.propsValue, rows = people != null ? people : [];
    if (rows.length === 0)
      throw new Error("At least one person is required");
    if (rows.length > MAX_RELATIONSHIPS_PER_REQUEST)
      throw new Error(
        `Maximum ${MAX_RELATIONSHIPS_PER_REQUEST} people allowed per request (got ${rows.length})`
      );
    if (min_score != null && (min_score < MIN_SCORE || min_score > MAX_SCORE))
      throw new Error(
        `Default Score must be between ${MIN_SCORE} and ${MAX_SCORE} (got ${min_score})`
      );
    let body = { people: rows.map((row, index) => {
      var _a;
      let identifier = ((_a = row.identifier) != null ? _a : "").trim();
      if (!identifier)
        throw new Error(`people[${index}].identifier is required`);
      let item = { identifier };
      if (row.score !== void 0 && row.score !== null) {
        if (row.score < MIN_SCORE || row.score > MAX_SCORE)
          throw new Error(
            `people[${index}].score must be between ${MIN_SCORE} and ${MAX_SCORE} (got ${row.score})`
          );
        item.score = row.score;
      }
      return row.user_relationship_type && (item.user_relationship_type = row.user_relationship_type), item;
    }) };
    return min_score != null && (body.min_score = min_score), (await import_pieces_common41.httpClient.sendRequest({
      method: import_pieces_common41.HttpMethod.POST,
      url: `${VILLAGE_API_BASE_URL}/v2/user/relationships`,
      headers: { Authorization: `Bearer ${context.auth.secret_text}` },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/village/src/index.ts
var village = (0, import_pieces_framework42.createPiece)({
  displayName: "Village",
  description: "The Social Capital API",
  auth: villageAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/village.png",
  categories: [import_pieces_framework43.PieceCategory.PRODUCTIVITY, import_pieces_framework43.PieceCategory.SALES_AND_CRM],
  authors: ["rafaelmuttoni"],
  actions: [
    getAppInfo,
    checkCompanyPaths,
    companiesRefresh,
    enrichCompany,
    enrichCompanyBulk,
    getCompanyPaths,
    listCompanies,
    searchCompanies,
    sortCompanies,
    joinGroup,
    leaveGroup,
    listGroups,
    upsertGroup,
    listIntegrations,
    resyncIntegration,
    updateIntegration,
    addListItems,
    checkListMembership,
    createList,
    deleteList,
    getList,
    listLists,
    removeListItems,
    updateList,
    enrichPerson,
    enrichPersonBulk,
    enrichPersonEmail,
    enrichPersonEmailBulk,
    getPersonPaths,
    getPersonPathsBulk,
    listPeople,
    peopleRefresh,
    searchPeople,
    sortPeople,
    joinTeam,
    leaveTeam,
    listTeams,
    upsertTeam,
    getCurrentUser,
    importRelationships
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  village,
  villageAuth
});
