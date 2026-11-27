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

// ../ap/packages/pieces/community/enrichlayer/src/index.ts
var index_exports = {};
__export(index_exports, {
  enrichlayer: () => enrichlayer
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework28 = require("@activepieces/pieces-framework"), import_pieces_framework29 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/enrichlayer/src/lib/actions/get-school-profile.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/enrichlayer/src/lib/auth.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/enrichlayer/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/enrichlayer/src/lib/common/constants.ts
var BASE_URL = "https://enrichlayer.com", ENDPOINTS = {
  SCHOOL_PROFILE: "/api/v2/school",
  COMPANY_PROFILE: "/api/v2/company",
  PERSON_PROFILE: "/api/v2/profile",
  COMPANY_LOOKUP: "/api/v2/company/resolve",
  COMPANY_ID_LOOKUP: "/api/v2/company/resolve-id",
  COMPANY_PICTURE: "/api/v2/company/profile-picture",
  EMPLOYEE_LISTING: "/api/v2/company/employees/",
  EMPLOYEE_COUNT: "/api/v2/company/employees/count",
  EMPLOYEE_SEARCH: "/api/v2/company/employee/search/",
  PERSON_LOOKUP: "/api/v2/profile/resolve",
  PERSON_PICTURE: "/api/v2/person/profile-picture",
  ROLE_LOOKUP: "/api/v2/find/company/role/",
  REVERSE_EMAIL_LOOKUP: "/api/v2/profile/resolve/email",
  REVERSE_PHONE_LOOKUP: "/api/v2/resolve/phone",
  WORK_EMAIL_LOOKUP: "/api/v2/profile/email",
  PERSONAL_CONTACT: "/api/v2/contact-api/personal-contact",
  PERSONAL_EMAIL: "/api/v2/contact-api/personal-email",
  DISPOSABLE_EMAIL: "/api/v2/disposable-email",
  STUDENT_LISTING: "/api/v2/school/students/",
  JOB_PROFILE: "/api/v2/job",
  JOB_SEARCH: "/api/v2/company/job",
  JOB_COUNT: "/api/v2/company/job/count",
  COMPANY_SEARCH: "/api/v2/search/company",
  PERSON_SEARCH: "/api/v2/search/person",
  CREDIT_BALANCE: "/api/v2/credit-balance"
};

// ../ap/packages/pieces/community/enrichlayer/src/lib/common/client.ts
var import_pieces_framework = require("@activepieces/pieces-framework");
async function enrichlayerApiCall(auth, path, queryParams) {
  let filteredParams = {};
  for (let [key, value] of Object.entries(queryParams))
    !(0, import_pieces_framework.isNil)(value) && value !== "" && (filteredParams[key] = value);
  return (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.GET,
    url: `${BASE_URL}${path}`,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token: auth
    },
    queryParams: filteredParams
  })).body;
}

// ../ap/packages/pieces/community/enrichlayer/src/lib/auth.ts
var enrichlayerAuth = import_pieces_framework2.PieceAuth.SecretText({
  displayName: "API Key",
  description: "You can obtain your API key from [Dashboard Settings](https://enrichlayer.com/dashboard/api-key/).",
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await enrichlayerApiCall(
        auth,
        ENDPOINTS.CREDIT_BALANCE,
        {}
      ), { valid: !0 };
    } catch (e) {
      return { valid: !1, error: "Could not validate API key" };
    }
  }
});

// ../ap/packages/pieces/community/enrichlayer/src/lib/actions/get-school-profile.ts
var getSchoolProfile = (0, import_pieces_framework3.createAction)({
  name: "get_school_profile",
  auth: enrichlayerAuth,
  displayName: "Get School Profile",
  description: "Get structured data of a School Profile from a professional network URL (1 credit)",
  audience: "both",
  aiMetadata: {
    description: "Enrich a single school or university into structured profile data given its professional-network school URL. Pick this when you already have the school URL and need its details; read-only and idempotent. Use Live Fetch to force a fresh fetch instead of cached data at a higher credit cost.",
    idempotent: !0
  },
  props: {
    url: import_pieces_framework3.Property.ShortText({
      displayName: "School URL",
      description: "Professional network school URL to enrich (e.g., https://www.linkedin.com/school/national-university-of-singapore)",
      required: !0
    }),
    use_cache: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Cache Strategy",
      description: "Control how cached data is used",
      required: !1,
      options: {
        options: [
          { label: "If Present (default)", value: "if-present" },
          { label: "If Recent (+1 credit)", value: "if-recent" }
        ]
      }
    }),
    live_fetch: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Live Fetch",
      description: "Force a fresh profile fetch (+9 credits)",
      required: !1,
      options: {
        options: [
          { label: "Default", value: "default" },
          { label: "Force (+9 credits)", value: "force" }
        ]
      }
    })
  },
  async run(context) {
    return await enrichlayerApiCall(
      context.auth.secret_text,
      ENDPOINTS.SCHOOL_PROFILE,
      {
        url: context.propsValue.url,
        use_cache: context.propsValue.use_cache,
        live_fetch: context.propsValue.live_fetch
      }
    );
  }
});

// ../ap/packages/pieces/community/enrichlayer/src/lib/actions/get-company-profile.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var getCompanyProfile = (0, import_pieces_framework4.createAction)({
  name: "get_company_profile",
  auth: enrichlayerAuth,
  displayName: "Get Company Profile",
  description: "Get structured data of a Company Profile from a professional network URL (1 credit)",
  audience: "both",
  aiMetadata: {
    description: "Retrieve a full structured company profile from a known professional-network company URL; read-only and safe to retry. Use this when you already have the company's profile URL. If you only have a name or domain, call Look Up Company first to resolve the URL. Optional add-ons (funding, acquisitions, categories, extra Crunchbase data) cost extra credits.",
    idempotent: !0
  },
  props: {
    url: import_pieces_framework4.Property.ShortText({
      displayName: "Company URL",
      description: "Professional network company URL to enrich (e.g., https://www.linkedin.com/company/google/)",
      required: !0
    }),
    categories: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Include Categories",
      description: "Append category data for this company (+1 credit)",
      required: !1,
      options: {
        options: [
          { label: "Exclude (default)", value: "exclude" },
          { label: "Include (+1 credit)", value: "include" }
        ]
      }
    }),
    funding_data: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Include Funding Data",
      description: "Return a list of funding rounds (+1 credit)",
      required: !1,
      options: {
        options: [
          { label: "Exclude (default)", value: "exclude" },
          { label: "Include (+1 credit)", value: "include" }
        ]
      }
    }),
    exit_data: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Include Exit Data",
      description: "Return investment portfolio exits (+1 credit)",
      required: !1,
      options: {
        options: [
          { label: "Exclude (default)", value: "exclude" },
          { label: "Include (+1 credit)", value: "include" }
        ]
      }
    }),
    acquisitions: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Include Acquisitions",
      description: "Return enriched acquisition data (+1 credit)",
      required: !1,
      options: {
        options: [
          { label: "Exclude (default)", value: "exclude" },
          { label: "Include (+1 credit)", value: "include" }
        ]
      }
    }),
    extra: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Include Extra Data",
      description: "Enrich with Crunchbase ranking, contact info, social accounts, funding details, IPO status (+1 credit)",
      required: !1,
      options: {
        options: [
          { label: "Exclude (default)", value: "exclude" },
          { label: "Include (+1 credit)", value: "include" }
        ]
      }
    }),
    use_cache: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Cache Strategy",
      description: "Control how cached data is used",
      required: !1,
      options: {
        options: [
          { label: "If Present (default)", value: "if-present" },
          { label: "If Recent (+1 credit)", value: "if-recent" }
        ]
      }
    }),
    fallback_to_cache: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Fallback to Cache",
      description: "Fallback behavior if fetching a fresh profile fails",
      required: !1,
      options: {
        options: [
          { label: "On Error (default)", value: "on-error" },
          { label: "Never", value: "never" }
        ]
      }
    })
  },
  async run(context) {
    return await enrichlayerApiCall(
      context.auth.secret_text,
      ENDPOINTS.COMPANY_PROFILE,
      {
        url: context.propsValue.url,
        categories: context.propsValue.categories,
        funding_data: context.propsValue.funding_data,
        exit_data: context.propsValue.exit_data,
        acquisitions: context.propsValue.acquisitions,
        extra: context.propsValue.extra,
        use_cache: context.propsValue.use_cache,
        fallback_to_cache: context.propsValue.fallback_to_cache
      }
    );
  }
});

// ../ap/packages/pieces/community/enrichlayer/src/lib/actions/get-person-profile.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var getPersonProfile = (0, import_pieces_framework5.createAction)({
  name: "get_person_profile",
  auth: enrichlayerAuth,
  displayName: "Get Person Profile",
  description: "Get structured data of a Person Profile from a professional network URL (1 credit)",
  audience: "both",
  aiMetadata: {
    description: "Retrieve a full structured person profile from a known professional-network, Twitter/X, or Facebook URL (supply exactly one); read-only and safe to retry. Use when you already have the profile URL. If you only have a name and company, call Look Up Person first to resolve the URL. Optional enrichments (contact numbers, personal emails, social IDs, Live Fetch) add credits.",
    idempotent: !0
  },
  props: {
    profile_url: import_pieces_framework5.Property.ShortText({
      displayName: "Profile URL",
      description: "Professional network profile URL (e.g., https://linkedin.com/in/johnrmarty/). Provide only one of Profile URL, Twitter URL, or Facebook URL.",
      required: !1
    }),
    twitter_profile_url: import_pieces_framework5.Property.ShortText({
      displayName: "Twitter/X Profile URL",
      description: "Twitter/X profile URL (e.g., https://x.com/johnrmarty/). Provide only one of Profile URL, Twitter URL, or Facebook URL.",
      required: !1
    }),
    facebook_profile_url: import_pieces_framework5.Property.ShortText({
      displayName: "Facebook Profile URL",
      description: "Facebook profile URL (e.g., https://facebook.com/johnrmarty/). Provide only one of Profile URL, Twitter URL, or Facebook URL.",
      required: !1
    }),
    extra: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Include Extra Data",
      description: "Enrich with gender, birth date, industry, and interests (+1 credit)",
      required: !1,
      options: {
        options: [
          { label: "Exclude (default)", value: "exclude" },
          { label: "Include (+1 credit)", value: "include" }
        ]
      }
    }),
    github_profile_id: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Include GitHub Profile ID",
      description: "Enrich with GitHub ID (+1 credit)",
      required: !1,
      options: {
        options: [
          { label: "Exclude (default)", value: "exclude" },
          { label: "Include (+1 credit)", value: "include" }
        ]
      }
    }),
    facebook_profile_id: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Include Facebook Profile ID",
      description: "Enrich with Facebook ID (+1 credit)",
      required: !1,
      options: {
        options: [
          { label: "Exclude (default)", value: "exclude" },
          { label: "Include (+1 credit)", value: "include" }
        ]
      }
    }),
    twitter_profile_id: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Include Twitter Profile ID",
      description: "Enrich with Twitter ID (+1 credit)",
      required: !1,
      options: {
        options: [
          { label: "Exclude (default)", value: "exclude" },
          { label: "Include (+1 credit)", value: "include" }
        ]
      }
    }),
    personal_contact_number: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Include Personal Contact Numbers",
      description: "Enrich with personal numbers (+1 credit per number)",
      required: !1,
      options: {
        options: [
          { label: "Exclude (default)", value: "exclude" },
          { label: "Include (+1 credit per number)", value: "include" }
        ]
      }
    }),
    personal_email: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Include Personal Emails",
      description: "Enrich with personal emails (+1 credit per email)",
      required: !1,
      options: {
        options: [
          { label: "Exclude (default)", value: "exclude" },
          { label: "Include (+1 credit per email)", value: "include" }
        ]
      }
    }),
    skills: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Include Skills",
      description: "Include skills data (no extra credit)",
      required: !1,
      options: {
        options: [
          { label: "Exclude (default)", value: "exclude" },
          { label: "Include (no extra credit)", value: "include" }
        ]
      }
    }),
    inferred_salary: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Include Inferred Salary (Deprecated)",
      description: "Include inferred salary range (+1 credit)",
      required: !1,
      options: {
        options: [
          { label: "Exclude (default)", value: "exclude" },
          { label: "Include (+1 credit)", value: "include" }
        ]
      }
    }),
    use_cache: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Cache Strategy",
      description: "Control how cached data is used",
      required: !1,
      options: {
        options: [
          { label: "If Present (default)", value: "if-present" },
          { label: "If Recent (+1 credit)", value: "if-recent" }
        ]
      }
    }),
    fallback_to_cache: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Fallback to Cache",
      description: "Fallback behavior if fetching a fresh profile fails",
      required: !1,
      options: {
        options: [
          { label: "On Error (default)", value: "on-error" },
          { label: "Never", value: "never" }
        ]
      }
    }),
    live_fetch: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Live Fetch",
      description: "Force a fresh profile fetch (+9 credits)",
      required: !1,
      options: {
        options: [
          { label: "Default", value: "default" },
          { label: "Force (+9 credits)", value: "force" }
        ]
      }
    })
  },
  async run(context) {
    return await enrichlayerApiCall(
      context.auth.secret_text,
      ENDPOINTS.PERSON_PROFILE,
      {
        profile_url: context.propsValue.profile_url,
        twitter_profile_url: context.propsValue.twitter_profile_url,
        facebook_profile_url: context.propsValue.facebook_profile_url,
        extra: context.propsValue.extra,
        github_profile_id: context.propsValue.github_profile_id,
        facebook_profile_id: context.propsValue.facebook_profile_id,
        twitter_profile_id: context.propsValue.twitter_profile_id,
        personal_contact_number: context.propsValue.personal_contact_number,
        personal_email: context.propsValue.personal_email,
        skills: context.propsValue.skills,
        inferred_salary: context.propsValue.inferred_salary,
        use_cache: context.propsValue.use_cache,
        fallback_to_cache: context.propsValue.fallback_to_cache,
        live_fetch: context.propsValue.live_fetch
      }
    );
  }
});

// ../ap/packages/pieces/community/enrichlayer/src/lib/actions/get-company-lookup.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var getCompanyLookup = (0, import_pieces_framework6.createAction)({
  name: "get_company_lookup",
  auth: enrichlayerAuth,
  displayName: "Look Up Company",
  description: "Resolve a Company Profile from company name, domain, or location (2 credits)",
  audience: "both",
  aiMetadata: {
    description: "Resolve a single company's professional-network URL/profile from its name or domain (one of the two is required), optionally narrowed by country. Read-only and safe to retry. Use this as the entry point when you only know a company name or domain; to find many companies by attributes use Search Companies, and to enrich an already-known URL use Get Company Profile.",
    idempotent: !0
  },
  props: {
    company_name: import_pieces_framework6.Property.ShortText({
      displayName: "Company Name",
      description: "Name of the company. Requires either Company Name or Company Domain.",
      required: !1
    }),
    company_domain: import_pieces_framework6.Property.ShortText({
      displayName: "Company Domain",
      description: "Company website or domain (e.g., accenture.com). Requires either Company Name or Company Domain.",
      required: !1
    }),
    company_location: import_pieces_framework6.Property.ShortText({
      displayName: "Company Location",
      description: "ISO 3166-1 alpha-2 country code (e.g., sg, us)",
      required: !1
    }),
    enrich_profile: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Enrich Profile",
      description: "Enrich the result with cached company profile data (+1 credit)",
      required: !1,
      options: {
        options: [
          { label: "Skip (default)", value: "skip" },
          { label: "Enrich (+1 credit)", value: "enrich" }
        ]
      }
    })
  },
  async run(context) {
    return await enrichlayerApiCall(
      context.auth.secret_text,
      ENDPOINTS.COMPANY_LOOKUP,
      {
        company_name: context.propsValue.company_name,
        company_domain: context.propsValue.company_domain,
        company_location: context.propsValue.company_location,
        enrich_profile: context.propsValue.enrich_profile
      }
    );
  }
});

// ../ap/packages/pieces/community/enrichlayer/src/lib/actions/get-company-id-lookup.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var getCompanyIdLookup = (0, import_pieces_framework7.createAction)({
  name: "get_company_id_lookup",
  auth: enrichlayerAuth,
  displayName: "Look Up Company by Numeric ID",
  description: "Resolve the vanity ID of a company from its numeric ID (0 credits)",
  audience: "both",
  aiMetadata: {
    description: "Resolve a company's vanity (public) identifier from its internal immutable numeric ID. Read-only, free, and safe to retry. Use only when you have the numeric company ID and need the vanity URL slug; to find a company by name or domain use Look Up Company instead.",
    idempotent: !0
  },
  props: {
    id: import_pieces_framework7.Property.ShortText({
      displayName: "Numeric Company ID",
      description: "The company's internal, immutable numeric ID (e.g., 1441 for Google)",
      required: !0
    })
  },
  async run(context) {
    return await enrichlayerApiCall(
      context.auth.secret_text,
      ENDPOINTS.COMPANY_ID_LOOKUP,
      {
        id: context.propsValue.id
      }
    );
  }
});

// ../ap/packages/pieces/community/enrichlayer/src/lib/actions/get-company-picture.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var getCompanyPicture = (0, import_pieces_framework8.createAction)({
  name: "get_company_picture",
  auth: enrichlayerAuth,
  displayName: "Get Company Profile Picture",
  description: "Get the profile picture URL of a company from cached profiles (0 credits)",
  audience: "both",
  aiMetadata: {
    description: "Retrieve a company's profile picture URL from its professional-network company page URL. Pick this when you only need the logo/avatar image link and not the full company record; it reads from cached profiles, costs no credits, and is read-only and idempotent.",
    idempotent: !0
  },
  props: {
    company_profile_url: import_pieces_framework8.Property.ShortText({
      displayName: "Company Profile URL",
      description: "Professional network company URL (e.g., https://www.linkedin.com/company/apple/)",
      required: !0
    })
  },
  async run(context) {
    return await enrichlayerApiCall(
      context.auth.secret_text,
      ENDPOINTS.COMPANY_PICTURE,
      {
        company_profile_url: context.propsValue.company_profile_url
      }
    );
  }
});

// ../ap/packages/pieces/community/enrichlayer/src/lib/actions/get-person-picture.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var getPersonPicture = (0, import_pieces_framework9.createAction)({
  name: "get_person_picture",
  auth: enrichlayerAuth,
  displayName: "Get Person Profile Picture",
  description: "Get the profile picture URL of a person from cached profiles (0 credits)",
  audience: "both",
  aiMetadata: {
    description: "Return the profile picture URL for a person from a known professional-network profile URL, served from cached profiles. Read-only, free, and safe to retry. Use only when you need the avatar image link; for full profile data use Get Person Profile.",
    idempotent: !0
  },
  props: {
    person_profile_url: import_pieces_framework9.Property.ShortText({
      displayName: "Person Profile URL",
      description: "Professional network person URL (e.g., https://www.linkedin.com/in/williamhgates/)",
      required: !0
    })
  },
  async run(context) {
    return await enrichlayerApiCall(
      context.auth.secret_text,
      ENDPOINTS.PERSON_PICTURE,
      {
        person_profile_url: context.propsValue.person_profile_url
      }
    );
  }
});

// ../ap/packages/pieces/community/enrichlayer/src/lib/actions/get-employee-listing.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var getEmployeeListing = (0, import_pieces_framework10.createAction)({
  name: "get_employee_listing",
  auth: enrichlayerAuth,
  displayName: "List Company Employees",
  description: "Get a list of employees of a company (3 credits per employee returned)",
  audience: "both",
  aiMetadata: {
    description: "List a company's employees from its professional-network URL, filterable by employment status (current/past/all), country, and sortable by tenure. Read-only and safe to retry. Use this for a broad roster; to target a specific job title use Search Company Employees, and for just a head count use Get Employee Count. Boolean role filtering, sorting, and profile enrichment add credits.",
    idempotent: !0
  },
  props: {
    url: import_pieces_framework10.Property.ShortText({
      displayName: "Company URL",
      description: "Professional network company URL (e.g., https://www.linkedin.com/company/microsoft)",
      required: !0
    }),
    page_size: import_pieces_framework10.Property.ShortText({
      displayName: "Page Size",
      description: "Maximum results per call (1-9999, default: 10)",
      required: !1
    }),
    employment_status: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Employment Status",
      description: "Filter by current, past, or all employees",
      required: !1,
      options: {
        options: [
          { label: "Current (default)", value: "current" },
          { label: "Past", value: "past" },
          { label: "All", value: "all" }
        ]
      }
    }),
    enrich_profiles: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Enrich Profiles",
      description: "Return full profiles instead of just URLs (+1 credit each)",
      required: !1,
      options: {
        options: [
          { label: "Skip (default)", value: "skip" },
          { label: "Enrich (+1 credit each)", value: "enrich" }
        ]
      }
    }),
    boolean_role_search: import_pieces_framework10.Property.ShortText({
      displayName: "Boolean Role Search",
      description: 'Filter employees by title using boolean expression (e.g., "founder" OR "co-founder"). Base cost: 10 credits + 3 per match.',
      required: !1
    }),
    role_search: import_pieces_framework10.Property.ShortText({
      displayName: "Role Search (Deprecated)",
      description: "Filter employees by title using regex. Prefer Boolean Role Search instead.",
      required: !1
    }),
    country: import_pieces_framework10.Property.ShortText({
      displayName: "Country",
      description: "Filter by country (ISO 3166-1 alpha-2 code, e.g., us). +3 credits per result.",
      required: !1
    }),
    sort_by: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Sort By",
      description: "Sort employees (+50 credits base + 10 per employee returned)",
      required: !1,
      options: {
        options: [
          { label: "None (default)", value: "none" },
          { label: "Recently Joined", value: "recently-joined" },
          { label: "Recently Left", value: "recently-left" },
          { label: "Oldest", value: "oldest" }
        ]
      }
    }),
    coy_name_match: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Company Name Match",
      description: "Include profiles that match the company name in addition to URL",
      required: !1,
      options: {
        options: [
          { label: "Include (default)", value: "include" },
          { label: "Exclude", value: "exclude" }
        ]
      }
    }),
    resolve_numeric_id: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Resolve Numeric ID",
      description: "Enable support for company URLs with numerical IDs (+2 credits)",
      required: !1,
      options: {
        options: [
          { label: "False (default)", value: "false" },
          { label: "True (+2 credits)", value: "true" }
        ]
      }
    }),
    use_cache: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Cache Strategy",
      description: "Control how cached data is used",
      required: !1,
      options: {
        options: [
          { label: "If Present (default)", value: "if-present" },
          { label: "If Recent (+1-2 credits per result)", value: "if-recent" }
        ]
      }
    })
  },
  async run(context) {
    return await enrichlayerApiCall(
      context.auth.secret_text,
      ENDPOINTS.EMPLOYEE_LISTING,
      {
        url: context.propsValue.url,
        page_size: context.propsValue.page_size,
        employment_status: context.propsValue.employment_status,
        enrich_profiles: context.propsValue.enrich_profiles,
        boolean_role_search: context.propsValue.boolean_role_search,
        role_search: context.propsValue.role_search,
        country: context.propsValue.country,
        sort_by: context.propsValue.sort_by,
        coy_name_match: context.propsValue.coy_name_match,
        resolve_numeric_id: context.propsValue.resolve_numeric_id,
        use_cache: context.propsValue.use_cache
      }
    );
  }
});

// ../ap/packages/pieces/community/enrichlayer/src/lib/actions/get-employee-count.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var getEmployeeCount = (0, import_pieces_framework11.createAction)({
  name: "get_employee_count",
  auth: enrichlayerAuth,
  displayName: "Get Employee Count",
  description: "Get the total number of employees of a company (1 credit)",
  audience: "both",
  aiMetadata: {
    description: "Return only the total employee head count for a company from its professional-network URL, with optional current/past/all status and an as-of date. Read-only and safe to retry. Use when you need a size metric, not the people; to retrieve the actual employee roster use List Company Employees. Estimated-count and historical date options add credits.",
    idempotent: !0
  },
  props: {
    url: import_pieces_framework11.Property.ShortText({
      displayName: "Company URL",
      description: "Professional network company URL (e.g., https://www.linkedin.com/company/apple/)",
      required: !0
    }),
    employment_status: import_pieces_framework11.Property.StaticDropdown({
      displayName: "Employment Status",
      description: "Count current, past, or all employees",
      required: !1,
      options: {
        options: [
          { label: "Current (default)", value: "current" },
          { label: "Past", value: "past" },
          { label: "All", value: "all" }
        ]
      }
    }),
    coy_name_match: import_pieces_framework11.Property.StaticDropdown({
      displayName: "Company Name Match",
      description: "Include profiles that match the company name in addition to URL",
      required: !1,
      options: {
        options: [
          { label: "Include (default)", value: "include" },
          { label: "Exclude", value: "exclude" }
        ]
      }
    }),
    estimated_employee_count: import_pieces_framework11.Property.StaticDropdown({
      displayName: "Include Estimated Count",
      description: "Include estimated employee count from the company profile (+1 credit)",
      required: !1,
      options: {
        options: [
          { label: "Exclude (default)", value: "exclude" },
          { label: "Include (+1 credit)", value: "include" }
        ]
      }
    }),
    at_date: import_pieces_framework11.Property.ShortText({
      displayName: "At Date",
      description: "Get employee count at a specific date (YYYY-MM-DD format). +1-5 credits.",
      required: !1
    }),
    use_cache: import_pieces_framework11.Property.StaticDropdown({
      displayName: "Cache Strategy",
      description: "Control how cached data is used",
      required: !1,
      options: {
        options: [
          { label: "If Present (default)", value: "if-present" },
          { label: "If Recent (+1 credit)", value: "if-recent" }
        ]
      }
    })
  },
  async run(context) {
    return await enrichlayerApiCall(
      context.auth.secret_text,
      ENDPOINTS.EMPLOYEE_COUNT,
      {
        url: context.propsValue.url,
        employment_status: context.propsValue.employment_status,
        coy_name_match: context.propsValue.coy_name_match,
        estimated_employee_count: context.propsValue.estimated_employee_count,
        at_date: context.propsValue.at_date,
        use_cache: context.propsValue.use_cache
      }
    );
  }
});

// ../ap/packages/pieces/community/enrichlayer/src/lib/actions/get-employee-search.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var getEmployeeSearch = (0, import_pieces_framework12.createAction)({
  name: "get_employee_search",
  auth: enrichlayerAuth,
  displayName: "Search Company Employees",
  description: "Search employees of a company by job title using boolean or regex keywords (10 credits base + 3 per match)",
  audience: "both",
  aiMetadata: {
    description: 'Find employees of a company whose job title matches a boolean (or legacy regex) keyword, given the company URL. Read-only and safe to retry. Use this when you need people in specific roles (e.g. "ceo OR cto"); to list all employees without a title filter use List Company Employees. Carries a base cost plus per-match credits, and country filtering or profile enrichment add more.',
    idempotent: !0
  },
  props: {
    company_profile_url: import_pieces_framework12.Property.ShortText({
      displayName: "Company Profile URL",
      description: "Professional network company URL (e.g., https://www.linkedin.com/company/microsoft/)",
      required: !0
    }),
    keyword_boolean: import_pieces_framework12.Property.ShortText({
      displayName: "Keyword (Boolean)",
      description: 'Job title keyword in boolean search syntax (e.g., "ceo OR cto"). Max 255 characters. Takes precedence over regex.',
      required: !0
    }),
    keyword_regex: import_pieces_framework12.Property.ShortText({
      displayName: "Keyword (Regex, Deprecated)",
      description: "Job title keyword in regex format (e.g., ceo|cto). Prefer Boolean keyword instead.",
      required: !1
    }),
    page_size: import_pieces_framework12.Property.ShortText({
      displayName: "Page Size",
      description: "Maximum results per call (1-9999, default: 10)",
      required: !1
    }),
    country: import_pieces_framework12.Property.ShortText({
      displayName: "Country",
      description: "Filter by country (ISO 3166-1 alpha-2, e.g., us). +3 credits per result.",
      required: !1
    }),
    enrich_profiles: import_pieces_framework12.Property.StaticDropdown({
      displayName: "Enrich Profiles",
      description: "Return full profiles instead of just URLs (+1 credit each)",
      required: !1,
      options: {
        options: [
          { label: "Skip (default)", value: "skip" },
          { label: "Enrich (+1 credit each)", value: "enrich" }
        ]
      }
    }),
    resolve_numeric_id: import_pieces_framework12.Property.StaticDropdown({
      displayName: "Resolve Numeric ID",
      description: "Enable support for company URLs with numerical IDs (+2 credits)",
      required: !1,
      options: {
        options: [
          { label: "False (default)", value: "false" },
          { label: "True (+2 credits)", value: "true" }
        ]
      }
    })
  },
  async run(context) {
    return await enrichlayerApiCall(
      context.auth.secret_text,
      ENDPOINTS.EMPLOYEE_SEARCH,
      {
        company_profile_url: context.propsValue.company_profile_url,
        keyword_boolean: context.propsValue.keyword_boolean,
        keyword_regex: context.propsValue.keyword_regex,
        page_size: context.propsValue.page_size,
        country: context.propsValue.country,
        enrich_profiles: context.propsValue.enrich_profiles,
        resolve_numeric_id: context.propsValue.resolve_numeric_id
      }
    );
  }
});

// ../ap/packages/pieces/community/enrichlayer/src/lib/actions/get-person-lookup.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var getPersonLookup = (0, import_pieces_framework13.createAction)({
  name: "get_person_lookup",
  auth: enrichlayerAuth,
  displayName: "Look Up Person",
  description: "Look up a person by name and company information (2 credits)",
  audience: "both",
  aiMetadata: {
    description: "Resolve a single person's professional-network profile from their first name plus a company name or domain (both required), optionally refined by last name, title, or location. Read-only and safe to retry. Use this as the entry point when you have a name and employer but no profile URL; once you have the URL use Get Person Profile to enrich. Similarity checks consume credits even when no match is found.",
    idempotent: !0
  },
  props: {
    first_name: import_pieces_framework13.Property.ShortText({
      displayName: "First Name",
      description: "First name of the person (e.g., Bill)",
      required: !0
    }),
    company_domain: import_pieces_framework13.Property.ShortText({
      displayName: "Company Name or Domain",
      description: "Company name or domain (e.g., gatesfoundation.org)",
      required: !0
    }),
    last_name: import_pieces_framework13.Property.ShortText({
      displayName: "Last Name",
      description: "Last name of the person (e.g., Gates)",
      required: !1
    }),
    title: import_pieces_framework13.Property.ShortText({
      displayName: "Title",
      description: "Title the person holds at their current job (e.g., Co-chair)",
      required: !1
    }),
    location: import_pieces_framework13.Property.ShortText({
      displayName: "Location",
      description: "Country, city, or state name (e.g., Seattle)",
      required: !1
    }),
    enrich_profile: import_pieces_framework13.Property.StaticDropdown({
      displayName: "Enrich Profile",
      description: "Enrich the result with cached profile data (+1 credit)",
      required: !1,
      options: {
        options: [
          { label: "Skip (default)", value: "skip" },
          { label: "Enrich (+1 credit)", value: "enrich" }
        ]
      }
    }),
    similarity_checks: import_pieces_framework13.Property.StaticDropdown({
      displayName: "Similarity Checks",
      description: "Perform similarity checks to eliminate false positives. Credits are deducted even if null is returned.",
      required: !1,
      options: {
        options: [
          { label: "Include (default)", value: "include" },
          { label: "Skip (no credits if no result)", value: "skip" }
        ]
      }
    })
  },
  async run(context) {
    return await enrichlayerApiCall(
      context.auth.secret_text,
      ENDPOINTS.PERSON_LOOKUP,
      {
        first_name: context.propsValue.first_name,
        company_domain: context.propsValue.company_domain,
        last_name: context.propsValue.last_name,
        title: context.propsValue.title,
        location: context.propsValue.location,
        enrich_profile: context.propsValue.enrich_profile,
        similarity_checks: context.propsValue.similarity_checks
      }
    );
  }
});

// ../ap/packages/pieces/community/enrichlayer/src/lib/actions/get-role-lookup.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var getRoleLookup = (0, import_pieces_framework14.createAction)({
  name: "get_role_lookup",
  auth: enrichlayerAuth,
  displayName: "Look Up Role at Company",
  description: "Find the person who most closely matches a specified role at a company (3 credits)",
  audience: "both",
  aiMetadata: {
    description: 'Find the single person who best matches a given role (e.g. "ceo", "vp of engineering") at a named company. Read-only and safe to retry. Use when you need the one person holding a role; to get many people by job-title keyword use Search Company Employees. Profile enrichment adds a credit.',
    idempotent: !0
  },
  props: {
    company_name: import_pieces_framework14.Property.ShortText({
      displayName: "Company Name",
      description: "Name of the company (e.g., enrichlayer)",
      required: !0
    }),
    role: import_pieces_framework14.Property.ShortText({
      displayName: "Role",
      description: "Role to look up (e.g., ceo, cto, vp of engineering)",
      required: !0
    }),
    enrich_profile: import_pieces_framework14.Property.StaticDropdown({
      displayName: "Enrich Profile",
      description: "Enrich the result with cached profile data (+1 credit)",
      required: !1,
      options: {
        options: [
          { label: "Skip (default)", value: "skip" },
          { label: "Enrich (+1 credit)", value: "enrich" }
        ]
      }
    })
  },
  async run(context) {
    return await enrichlayerApiCall(
      context.auth.secret_text,
      ENDPOINTS.ROLE_LOOKUP,
      {
        company_name: context.propsValue.company_name,
        role: context.propsValue.role,
        enrich_profile: context.propsValue.enrich_profile
      }
    );
  }
});

// ../ap/packages/pieces/community/enrichlayer/src/lib/actions/get-reverse-email-lookup.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");
var getReverseEmailLookup = (0, import_pieces_framework15.createAction)({
  name: "get_reverse_email_lookup",
  auth: enrichlayerAuth,
  displayName: "Reverse Email Lookup",
  description: "Find social media profiles from an email address. Works with both personal and work emails (3 credits)",
  audience: "both",
  aiMetadata: {
    description: "Find social/professional-network profiles associated with an email address (personal or work). Read-only and safe to retry. Use to go from a known email back to a person's profiles; the reverse direction (profile to emails) is Look Up Personal Email or Look Up Work Email. Superficial lookup depth charges no credits when there are no results; Deep charges regardless.",
    idempotent: !0
  },
  props: {
    email: import_pieces_framework15.Property.ShortText({
      displayName: "Email Address",
      description: "Email address to look up (e.g., johndoe@enrichlayer.com)",
      required: !0
    }),
    lookup_depth: import_pieces_framework15.Property.StaticDropdown({
      displayName: "Lookup Depth",
      description: "Superficial: no credits if no results. Deep: credits used regardless.",
      required: !1,
      options: {
        options: [
          { label: "Deep (default)", value: "deep" },
          {
            label: "Superficial (no credits if no results)",
            value: "superficial"
          }
        ]
      }
    }),
    enrich_profile: import_pieces_framework15.Property.StaticDropdown({
      displayName: "Enrich Profile",
      description: "Enrich the result with cached profile data (+1 credit)",
      required: !1,
      options: {
        options: [
          { label: "Skip (default)", value: "skip" },
          { label: "Enrich (+1 credit)", value: "enrich" }
        ]
      }
    })
  },
  async run(context) {
    return await enrichlayerApiCall(
      context.auth.secret_text,
      ENDPOINTS.REVERSE_EMAIL_LOOKUP,
      {
        email: context.propsValue.email,
        lookup_depth: context.propsValue.lookup_depth,
        enrich_profile: context.propsValue.enrich_profile
      }
    );
  }
});

// ../ap/packages/pieces/community/enrichlayer/src/lib/actions/get-reverse-phone-lookup.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework");
var getReversePhoneLookup = (0, import_pieces_framework16.createAction)({
  name: "get_reverse_phone_lookup",
  auth: enrichlayerAuth,
  displayName: "Reverse Phone Lookup",
  description: "Find social media profiles from a phone number (3 credits)",
  audience: "both",
  aiMetadata: {
    description: "Look up the social media profiles associated with a phone number supplied in E.164 format. Pick this when a phone number is your only identifier and you want to resolve it to people/profiles; read-only and idempotent, billed per call.",
    idempotent: !0
  },
  props: {
    phone_number: import_pieces_framework16.Property.ShortText({
      displayName: "Phone Number",
      description: "E.164 formatted phone number (e.g., +14155552671)",
      required: !0
    })
  },
  async run(context) {
    return await enrichlayerApiCall(
      context.auth.secret_text,
      ENDPOINTS.REVERSE_PHONE_LOOKUP,
      {
        phone_number: context.propsValue.phone_number
      }
    );
  }
});

// ../ap/packages/pieces/community/enrichlayer/src/lib/actions/get-work-email-lookup.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework");
var getWorkEmailLookup = (0, import_pieces_framework17.createAction)({
  name: "get_work_email_lookup",
  auth: enrichlayerAuth,
  displayName: "Look Up Work Email",
  description: "Get the work email address for a professional network profile. 95%+ deliverability guarantee (3 credits)",
  audience: "both",
  aiMetadata: {
    description: "Find a person's work (business) email address from their professional-network profile URL, with a 95%+ deliverability guarantee. Read-only and safe to retry. Use for B2B/work contact; for personal emails use Look Up Personal Email, and to go from an email back to profiles use Reverse Email Lookup. Processing may be asynchronous, so an optional callback URL can be supplied for completion notification.",
    idempotent: !0
  },
  props: {
    profile_url: import_pieces_framework17.Property.ShortText({
      displayName: "Profile URL",
      description: "Professional network profile URL (e.g., https://sg.linkedin.com/in/williamhgates)",
      required: !0
    }),
    callback_url: import_pieces_framework17.Property.ShortText({
      displayName: "Callback URL",
      description: "Webhook URL to notify when the request finishes processing (optional)",
      required: !1
    })
  },
  async run(context) {
    return await enrichlayerApiCall(
      context.auth.secret_text,
      ENDPOINTS.WORK_EMAIL_LOOKUP,
      {
        profile_url: context.propsValue.profile_url,
        callback_url: context.propsValue.callback_url
      }
    );
  }
});

// ../ap/packages/pieces/community/enrichlayer/src/lib/actions/get-personal-contact.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework");
var getPersonalContact = (0, import_pieces_framework18.createAction)({
  name: "get_personal_contact",
  auth: enrichlayerAuth,
  displayName: "Look Up Personal Contact Numbers",
  description: "Find personal phone numbers from a social media profile (1 credit per number returned)",
  audience: "both",
  aiMetadata: {
    description: "Find a person's personal phone numbers from one social profile URL (professional-network, Twitter/X, or Facebook \u2014 supply exactly one). Read-only and safe to retry. Use when you have a profile link and need phone contact details; the email counterpart is Look Up Personal Email.",
    idempotent: !0
  },
  props: {
    profile_url: import_pieces_framework18.Property.ShortText({
      displayName: "Profile URL",
      description: "Professional network profile URL. Provide only one of Profile URL, Twitter URL, or Facebook URL.",
      required: !1
    }),
    twitter_profile_url: import_pieces_framework18.Property.ShortText({
      displayName: "Twitter/X Profile URL",
      description: "Twitter/X profile URL (e.g., https://x.com/enrichlayer). Provide only one of Profile URL, Twitter URL, or Facebook URL.",
      required: !1
    }),
    facebook_profile_url: import_pieces_framework18.Property.ShortText({
      displayName: "Facebook Profile URL",
      description: "Facebook profile URL (e.g., https://www.facebook.com/zuck). Provide only one of Profile URL, Twitter URL, or Facebook URL.",
      required: !1
    }),
    page_size: import_pieces_framework18.Property.ShortText({
      displayName: "Page Size",
      description: "Maximum numbers returned (0 = no limit, default: 0)",
      required: !1
    })
  },
  async run(context) {
    return await enrichlayerApiCall(
      context.auth.secret_text,
      ENDPOINTS.PERSONAL_CONTACT,
      {
        profile_url: context.propsValue.profile_url,
        twitter_profile_url: context.propsValue.twitter_profile_url,
        facebook_profile_url: context.propsValue.facebook_profile_url,
        page_size: context.propsValue.page_size
      }
    );
  }
});

// ../ap/packages/pieces/community/enrichlayer/src/lib/actions/get-personal-email.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework");
var getPersonalEmail = (0, import_pieces_framework19.createAction)({
  name: "get_personal_email",
  auth: enrichlayerAuth,
  displayName: "Look Up Personal Email",
  description: "Find personal email addresses from a social media profile (1 credit per email returned)",
  audience: "both",
  aiMetadata: {
    description: "Find a person's personal (non-work) email addresses given one social profile URL (professional-network, Twitter/X, or Facebook \u2014 supply exactly one). Read-only and safe to retry. Use when you have a profile link and need personal contact emails; for work emails use Look Up Work Email, and to go from email back to profiles use Reverse Email Lookup.",
    idempotent: !0
  },
  props: {
    profile_url: import_pieces_framework19.Property.ShortText({
      displayName: "Profile URL",
      description: "Professional network profile URL. Provide only one of Profile URL, Twitter URL, or Facebook URL.",
      required: !1
    }),
    twitter_profile_url: import_pieces_framework19.Property.ShortText({
      displayName: "Twitter/X Profile URL",
      description: "Twitter/X profile URL (e.g., https://x.com/enrichlayer). Provide only one of Profile URL, Twitter URL, or Facebook URL.",
      required: !1
    }),
    facebook_profile_url: import_pieces_framework19.Property.ShortText({
      displayName: "Facebook Profile URL",
      description: "Facebook profile URL (e.g., https://www.facebook.com/zuck). Provide only one of Profile URL, Twitter URL, or Facebook URL.",
      required: !1
    }),
    email_validation: import_pieces_framework19.Property.StaticDropdown({
      displayName: "Email Validation",
      description: "How to validate each email found",
      required: !1,
      options: {
        options: [
          { label: "None (default)", value: "none" },
          { label: "Fast (no extra credit)", value: "fast" },
          {
            label: "Precise (+1 credit per email)",
            value: "precise"
          }
        ]
      }
    }),
    page_size: import_pieces_framework19.Property.ShortText({
      displayName: "Page Size",
      description: "Maximum emails returned (0 = no limit, default: 0)",
      required: !1
    })
  },
  async run(context) {
    return await enrichlayerApiCall(
      context.auth.secret_text,
      ENDPOINTS.PERSONAL_EMAIL,
      {
        profile_url: context.propsValue.profile_url,
        twitter_profile_url: context.propsValue.twitter_profile_url,
        facebook_profile_url: context.propsValue.facebook_profile_url,
        email_validation: context.propsValue.email_validation,
        page_size: context.propsValue.page_size
      }
    );
  }
});

// ../ap/packages/pieces/community/enrichlayer/src/lib/actions/get-disposable-email-check.ts
var import_pieces_framework20 = require("@activepieces/pieces-framework");
var getDisposableEmailCheck = (0, import_pieces_framework20.createAction)({
  name: "get_disposable_email_check",
  auth: enrichlayerAuth,
  displayName: "Check Disposable Email",
  description: "Check if an email address belongs to a disposable email service (0 credits)",
  audience: "both",
  aiMetadata: {
    description: "Check whether an email address belongs to a disposable/temporary email provider. Read-only, free, and safe to retry. Use to validate or filter sign-up and lead emails before further enrichment; this only flags disposability and does not return profile data.",
    idempotent: !0
  },
  props: {
    email: import_pieces_framework20.Property.ShortText({
      displayName: "Email Address",
      description: "Email address to check (e.g., johndoe@enrichlayer.com)",
      required: !0
    })
  },
  async run(context) {
    return await enrichlayerApiCall(
      context.auth.secret_text,
      ENDPOINTS.DISPOSABLE_EMAIL,
      {
        email: context.propsValue.email
      }
    );
  }
});

// ../ap/packages/pieces/community/enrichlayer/src/lib/actions/get-student-listing.ts
var import_pieces_framework21 = require("@activepieces/pieces-framework");
var getStudentListing = (0, import_pieces_framework21.createAction)({
  name: "get_student_listing",
  auth: enrichlayerAuth,
  displayName: "List School Students",
  description: "Get a list of students at a school (3 credits per student returned)",
  audience: "both",
  aiMetadata: {
    description: "List students/alumni of a school from its professional-network school URL, filterable by status (current/past/all), major (boolean keyword), and country, and sortable by matriculation or graduation. Read-only and safe to retry. Use for school rosters; the company equivalent is List Company Employees. Boolean major search, sorting, and profile enrichment add credits.",
    idempotent: !0
  },
  props: {
    school_url: import_pieces_framework21.Property.ShortText({
      displayName: "School URL",
      description: "Professional network school URL (e.g., https://www.linkedin.com/school/stanford-university)",
      required: !0
    }),
    page_size: import_pieces_framework21.Property.ShortText({
      displayName: "Page Size",
      description: "Maximum results per call (1-9999, default: 10)",
      required: !1
    }),
    student_status: import_pieces_framework21.Property.StaticDropdown({
      displayName: "Student Status",
      description: "Filter by current, past, or all students",
      required: !1,
      options: {
        options: [
          { label: "Current (default)", value: "current" },
          { label: "Past", value: "past" },
          { label: "All", value: "all" }
        ]
      }
    }),
    enrich_profiles: import_pieces_framework21.Property.StaticDropdown({
      displayName: "Enrich Profiles",
      description: "Return full profiles instead of just URLs (+1 credit each)",
      required: !1,
      options: {
        options: [
          { label: "Skip (default)", value: "skip" },
          { label: "Enrich (+1 credit each)", value: "enrich" }
        ]
      }
    }),
    boolean_search_keyword: import_pieces_framework21.Property.ShortText({
      displayName: "Boolean Search Keyword",
      description: 'Filter students by major using boolean expression (e.g., "computer OR cs"). Base: 10 credits + 6 per student.',
      required: !1
    }),
    country: import_pieces_framework21.Property.ShortText({
      displayName: "Country",
      description: "Filter by country (ISO 3166-1 alpha-2, e.g., us). +3 credits per result.",
      required: !1
    }),
    sort_by: import_pieces_framework21.Property.StaticDropdown({
      displayName: "Sort By",
      description: "Sort students (+50 credits base + 10 per student returned)",
      required: !1,
      options: {
        options: [
          { label: "None (default)", value: "none" },
          { label: "Recently Matriculated", value: "recently-matriculated" },
          { label: "Recently Graduated", value: "recently-graduated" }
        ]
      }
    }),
    resolve_numeric_id: import_pieces_framework21.Property.StaticDropdown({
      displayName: "Resolve Numeric ID",
      description: "Enable support for school URLs with numerical IDs (+2 credits)",
      required: !1,
      options: {
        options: [
          { label: "False (default)", value: "false" },
          { label: "True (+2 credits)", value: "true" }
        ]
      }
    })
  },
  async run(context) {
    return await enrichlayerApiCall(
      context.auth.secret_text,
      ENDPOINTS.STUDENT_LISTING,
      {
        school_url: context.propsValue.school_url,
        page_size: context.propsValue.page_size,
        student_status: context.propsValue.student_status,
        enrich_profiles: context.propsValue.enrich_profiles,
        boolean_search_keyword: context.propsValue.boolean_search_keyword,
        country: context.propsValue.country,
        sort_by: context.propsValue.sort_by,
        resolve_numeric_id: context.propsValue.resolve_numeric_id
      }
    );
  }
});

// ../ap/packages/pieces/community/enrichlayer/src/lib/actions/get-job-profile.ts
var import_pieces_framework22 = require("@activepieces/pieces-framework");
var getJobProfile = (0, import_pieces_framework22.createAction)({
  name: "get_job_profile",
  auth: enrichlayerAuth,
  displayName: "Get Job Profile",
  description: "Get structured data of a job posting (2 credits)",
  audience: "both",
  aiMetadata: {
    description: "Retrieve full structured data for a single job posting from its professional-network job URL. Read-only and safe to retry. Use when you have a specific job URL; to discover job postings for a company use Search Company Jobs first.",
    idempotent: !0
  },
  props: {
    url: import_pieces_framework22.Property.ShortText({
      displayName: "Job URL",
      description: "Professional network job URL (e.g., https://www.linkedin.com/jobs/view/4222036951/)",
      required: !0
    })
  },
  async run(context) {
    return await enrichlayerApiCall(
      context.auth.secret_text,
      ENDPOINTS.JOB_PROFILE,
      {
        url: context.propsValue.url
      }
    );
  }
});

// ../ap/packages/pieces/community/enrichlayer/src/lib/actions/get-job-search.ts
var import_pieces_framework23 = require("@activepieces/pieces-framework");
var getJobSearch = (0, import_pieces_framework23.createAction)({
  name: "get_job_search",
  auth: enrichlayerAuth,
  displayName: "Search Company Jobs",
  description: "List jobs posted by a company with optional filters (2 credits)",
  audience: "both",
  aiMetadata: {
    description: "List job postings from a company, filterable by keyword, job type, experience level, recency, and work flexibility. Read-only and safe to retry. Use when you need the actual job listings; to get only the matching count without the listings use Get Job Count. Requires the company search_id obtained from a company profile lookup.",
    idempotent: !0
  },
  props: {
    search_id: import_pieces_framework23.Property.ShortText({
      displayName: "Company Search ID",
      description: "The search_id of the company (from Company Profile API)",
      required: !1
    }),
    keyword: import_pieces_framework23.Property.ShortText({
      displayName: "Keyword",
      description: "Keyword to search for (e.g., engineer)",
      required: !1
    }),
    job_type: import_pieces_framework23.Property.StaticDropdown({
      displayName: "Job Type",
      description: "Filter by job type",
      required: !1,
      options: {
        options: [
          { label: "Anything (default)", value: "anything" },
          { label: "Full-time", value: "full-time" },
          { label: "Part-time", value: "part-time" },
          { label: "Contract", value: "contract" },
          { label: "Internship", value: "internship" },
          { label: "Temporary", value: "temporary" },
          { label: "Volunteer", value: "volunteer" }
        ]
      }
    }),
    experience_level: import_pieces_framework23.Property.StaticDropdown({
      displayName: "Experience Level",
      description: "Filter by experience level",
      required: !1,
      options: {
        options: [
          { label: "Anything (default)", value: "anything" },
          { label: "Internship", value: "internship" },
          { label: "Entry Level", value: "entry_level" },
          { label: "Associate", value: "associate" },
          { label: "Mid-Senior Level", value: "mid_senior_level" },
          { label: "Director", value: "director" }
        ]
      }
    }),
    when: import_pieces_framework23.Property.StaticDropdown({
      displayName: "When Posted",
      description: "Filter by when the job was posted",
      required: !1,
      options: {
        options: [
          { label: "Anytime (default)", value: "anytime" },
          { label: "Yesterday", value: "yesterday" },
          { label: "Past Week", value: "past-week" },
          { label: "Past Month", value: "past-month" }
        ]
      }
    }),
    flexibility: import_pieces_framework23.Property.StaticDropdown({
      displayName: "Flexibility",
      description: "Filter by work flexibility",
      required: !1,
      options: {
        options: [
          { label: "Anything (default)", value: "anything" },
          { label: "Remote", value: "remote" },
          { label: "On-site", value: "on-site" },
          { label: "Hybrid", value: "hybrid" }
        ]
      }
    }),
    geo_id: import_pieces_framework23.Property.ShortText({
      displayName: "Geo ID",
      description: "Geographic location ID (e.g., 92000000 for worldwide)",
      required: !1
    })
  },
  async run(context) {
    return await enrichlayerApiCall(
      context.auth.secret_text,
      ENDPOINTS.JOB_SEARCH,
      {
        search_id: context.propsValue.search_id,
        keyword: context.propsValue.keyword,
        job_type: context.propsValue.job_type,
        experience_level: context.propsValue.experience_level,
        when: context.propsValue.when,
        flexibility: context.propsValue.flexibility,
        geo_id: context.propsValue.geo_id
      }
    );
  }
});

// ../ap/packages/pieces/community/enrichlayer/src/lib/actions/get-job-count.ts
var import_pieces_framework24 = require("@activepieces/pieces-framework");
var getJobCount = (0, import_pieces_framework24.createAction)({
  name: "get_job_count",
  auth: enrichlayerAuth,
  displayName: "Get Job Count",
  description: "Count the number of jobs posted by a company (2 credits)",
  audience: "both",
  aiMetadata: {
    description: "Return only the number of job postings from a company matching optional filters (keyword, job type, experience level, recency, flexibility), not the listings themselves. Read-only and safe to retry. Use when you need a hiring-volume metric; to retrieve the actual postings use Search Company Jobs. Requires the company search_id from a company profile lookup.",
    idempotent: !0
  },
  props: {
    search_id: import_pieces_framework24.Property.ShortText({
      displayName: "Company Search ID",
      description: "The search_id of the company (from Company Profile API)",
      required: !1
    }),
    keyword: import_pieces_framework24.Property.ShortText({
      displayName: "Keyword",
      description: "Keyword to search for (e.g., engineer)",
      required: !1
    }),
    job_type: import_pieces_framework24.Property.StaticDropdown({
      displayName: "Job Type",
      description: "Filter by job type",
      required: !1,
      options: {
        options: [
          { label: "Anything (default)", value: "anything" },
          { label: "Full-time", value: "full-time" },
          { label: "Part-time", value: "part-time" },
          { label: "Contract", value: "contract" },
          { label: "Internship", value: "internship" },
          { label: "Temporary", value: "temporary" },
          { label: "Volunteer", value: "volunteer" }
        ]
      }
    }),
    experience_level: import_pieces_framework24.Property.StaticDropdown({
      displayName: "Experience Level",
      description: "Filter by experience level",
      required: !1,
      options: {
        options: [
          { label: "Anything (default)", value: "anything" },
          { label: "Internship", value: "internship" },
          { label: "Entry Level", value: "entry_level" },
          { label: "Associate", value: "associate" },
          { label: "Mid-Senior Level", value: "mid_senior_level" },
          { label: "Director", value: "director" }
        ]
      }
    }),
    when: import_pieces_framework24.Property.StaticDropdown({
      displayName: "When Posted",
      description: "Filter by when the job was posted",
      required: !1,
      options: {
        options: [
          { label: "Anytime (default)", value: "anytime" },
          { label: "Yesterday", value: "yesterday" },
          { label: "Past Week", value: "past-week" },
          { label: "Past Month", value: "past-month" }
        ]
      }
    }),
    flexibility: import_pieces_framework24.Property.StaticDropdown({
      displayName: "Flexibility",
      description: "Filter by work flexibility",
      required: !1,
      options: {
        options: [
          { label: "Anything (default)", value: "anything" },
          { label: "Remote", value: "remote" },
          { label: "On-site", value: "on-site" },
          { label: "Hybrid", value: "hybrid" }
        ]
      }
    }),
    geo_id: import_pieces_framework24.Property.ShortText({
      displayName: "Geo ID",
      description: "Geographic location ID (e.g., 92000000 for worldwide)",
      required: !1
    })
  },
  async run(context) {
    return await enrichlayerApiCall(
      context.auth.secret_text,
      ENDPOINTS.JOB_COUNT,
      {
        search_id: context.propsValue.search_id,
        keyword: context.propsValue.keyword,
        job_type: context.propsValue.job_type,
        experience_level: context.propsValue.experience_level,
        when: context.propsValue.when,
        flexibility: context.propsValue.flexibility,
        geo_id: context.propsValue.geo_id
      }
    );
  }
});

// ../ap/packages/pieces/community/enrichlayer/src/lib/actions/get-company-search.ts
var import_pieces_framework25 = require("@activepieces/pieces-framework");
var getCompanySearch = (0, import_pieces_framework25.createAction)({
  name: "get_company_search",
  auth: enrichlayerAuth,
  displayName: "Search Companies",
  description: "Search for companies matching a set of criteria across an exhaustive dataset (3 credits per company URL returned)",
  audience: "both",
  aiMetadata: {
    description: "Discover companies matching filters such as industry, location, size, founding year, or funding across the full dataset, returning matching company URLs. Read-only and safe to retry. Use when you do not know a specific company and need to find candidates by attributes; to resolve one known company use Look Up Company instead. Enriching profiles inline costs extra credits per result.",
    idempotent: !0
  },
  props: {
    name: import_pieces_framework25.Property.ShortText({
      displayName: "Company Name",
      description: 'Filter by company name (boolean search syntax, e.g., "circle || amelex")',
      required: !1
    }),
    country: import_pieces_framework25.Property.ShortText({
      displayName: "Country",
      description: "Filter by country (ISO 3166-1 alpha-2, e.g., US)",
      required: !1
    }),
    region: import_pieces_framework25.Property.ShortText({
      displayName: "Region",
      description: 'Filter by state/province (e.g., Maryland OR "New York")',
      required: !1
    }),
    city: import_pieces_framework25.Property.ShortText({
      displayName: "City",
      description: 'Filter by city (e.g., "Los Angeles")',
      required: !1
    }),
    industry: import_pieces_framework25.Property.ShortText({
      displayName: "Industry",
      description: 'Filter by industry (boolean search, e.g., "technology || manufacturing")',
      required: !1
    }),
    primary_industry: import_pieces_framework25.Property.ShortText({
      displayName: "Primary Industry",
      description: "Filter by primary industry only",
      required: !1
    }),
    type: import_pieces_framework25.Property.StaticDropdown({
      displayName: "Company Type",
      description: "Filter by company type",
      required: !1,
      options: {
        options: [
          { label: "Educational", value: "EDUCATIONAL" },
          { label: "Government Agency", value: "GOVERNMENT_AGENCY" },
          { label: "Non-Profit", value: "NON_PROFIT" },
          { label: "Partnership", value: "PARTNERSHIP" },
          { label: "Privately Held", value: "PRIVATELY_HELD" },
          { label: "Public Company", value: "PUBLIC_COMPANY" },
          { label: "Self-Employed", value: "SELF_EMPLOYED" },
          { label: "Sole Proprietorship", value: "SELF_OWNED" }
        ]
      }
    }),
    employee_count_category: import_pieces_framework25.Property.StaticDropdown({
      displayName: "Employee Count Category",
      description: "Filter by employee count category (takes precedence over min/max)",
      required: !1,
      options: {
        options: [
          { label: "Custom (default)", value: "custom" },
          { label: "Startup (1-10)", value: "startup" },
          { label: "Small (11-50)", value: "small" },
          { label: "Medium (51-250)", value: "medium" },
          { label: "Large (251-1000)", value: "large" },
          { label: "Enterprise (1001+)", value: "enterprise" }
        ]
      }
    }),
    employee_count_min: import_pieces_framework25.Property.ShortText({
      displayName: "Employee Count Min",
      description: "Minimum number of employees",
      required: !1
    }),
    employee_count_max: import_pieces_framework25.Property.ShortText({
      displayName: "Employee Count Max",
      description: "Maximum number of employees",
      required: !1
    }),
    follower_count_min: import_pieces_framework25.Property.ShortText({
      displayName: "Follower Count Min",
      description: "Minimum follower count",
      required: !1
    }),
    follower_count_max: import_pieces_framework25.Property.ShortText({
      displayName: "Follower Count Max",
      description: "Maximum follower count",
      required: !1
    }),
    description: import_pieces_framework25.Property.ShortText({
      displayName: "Description",
      description: "Filter by company description (boolean search)",
      required: !1
    }),
    specialities: import_pieces_framework25.Property.ShortText({
      displayName: "Specialities",
      description: "Filter by speciality (boolean search)",
      required: !1
    }),
    founded_after_year: import_pieces_framework25.Property.ShortText({
      displayName: "Founded After Year",
      description: "Filter companies founded after this year",
      required: !1
    }),
    founded_before_year: import_pieces_framework25.Property.ShortText({
      displayName: "Founded Before Year",
      description: "Filter companies founded before this year",
      required: !1
    }),
    funding_amount_min: import_pieces_framework25.Property.ShortText({
      displayName: "Funding Amount Min (USD)",
      description: "Minimum funding raised",
      required: !1
    }),
    funding_amount_max: import_pieces_framework25.Property.ShortText({
      displayName: "Funding Amount Max (USD)",
      description: "Maximum funding raised",
      required: !1
    }),
    funding_raised_after: import_pieces_framework25.Property.ShortText({
      displayName: "Funding Raised After",
      description: "Filter companies that raised funding after this date (YYYY-MM-DD)",
      required: !1
    }),
    funding_raised_before: import_pieces_framework25.Property.ShortText({
      displayName: "Funding Raised Before",
      description: "Filter companies that raised funding before this date (YYYY-MM-DD)",
      required: !1
    }),
    domain_name: import_pieces_framework25.Property.ShortText({
      displayName: "Domain Name",
      description: "Filter by domain name (e.g., pfizer.com)",
      required: !1
    }),
    public_identifier_in_list: import_pieces_framework25.Property.ShortText({
      displayName: "Public Identifier In List",
      description: "Comma-separated list of public identifiers the company must be in",
      required: !1
    }),
    public_identifier_not_in_list: import_pieces_framework25.Property.ShortText({
      displayName: "Public Identifier Not In List",
      description: "Comma-separated list of public identifiers the company must not be in",
      required: !1
    }),
    page_size: import_pieces_framework25.Property.ShortText({
      displayName: "Page Size",
      description: "Maximum results per call (1-100, default: 100)",
      required: !1
    }),
    enrich_profiles: import_pieces_framework25.Property.StaticDropdown({
      displayName: "Enrich Profiles",
      description: "Return full profiles instead of just URLs (+1 credit each)",
      required: !1,
      options: {
        options: [
          { label: "Skip (default)", value: "skip" },
          { label: "Enrich (+1 credit each)", value: "enrich" }
        ]
      }
    }),
    use_cache: import_pieces_framework25.Property.StaticDropdown({
      displayName: "Cache Strategy",
      description: "Control how cached data is used",
      required: !1,
      options: {
        options: [
          { label: "If Present (default)", value: "if-present" },
          { label: "If Recent (+1-2 credits per result)", value: "if-recent" }
        ]
      }
    })
  },
  async run(context) {
    return await enrichlayerApiCall(
      context.auth.secret_text,
      ENDPOINTS.COMPANY_SEARCH,
      {
        name: context.propsValue.name,
        country: context.propsValue.country,
        region: context.propsValue.region,
        city: context.propsValue.city,
        industry: context.propsValue.industry,
        primary_industry: context.propsValue.primary_industry,
        type: context.propsValue.type,
        employee_count_category: context.propsValue.employee_count_category,
        employee_count_min: context.propsValue.employee_count_min,
        employee_count_max: context.propsValue.employee_count_max,
        follower_count_min: context.propsValue.follower_count_min,
        follower_count_max: context.propsValue.follower_count_max,
        description: context.propsValue.description,
        specialities: context.propsValue.specialities,
        founded_after_year: context.propsValue.founded_after_year,
        founded_before_year: context.propsValue.founded_before_year,
        funding_amount_min: context.propsValue.funding_amount_min,
        funding_amount_max: context.propsValue.funding_amount_max,
        funding_raised_after: context.propsValue.funding_raised_after,
        funding_raised_before: context.propsValue.funding_raised_before,
        domain_name: context.propsValue.domain_name,
        public_identifier_in_list: context.propsValue.public_identifier_in_list,
        public_identifier_not_in_list: context.propsValue.public_identifier_not_in_list,
        page_size: context.propsValue.page_size,
        enrich_profiles: context.propsValue.enrich_profiles,
        use_cache: context.propsValue.use_cache
      }
    );
  }
});

// ../ap/packages/pieces/community/enrichlayer/src/lib/actions/get-person-search.ts
var import_pieces_framework26 = require("@activepieces/pieces-framework");
var getPersonSearch = (0, import_pieces_framework26.createAction)({
  name: "get_person_search",
  auth: enrichlayerAuth,
  displayName: "Search People",
  description: "Search for people matching a set of criteria across an exhaustive dataset (3 credits per profile URL returned)",
  audience: "both",
  aiMetadata: {
    description: "Find people by querying a large professional dataset on attributes like name, location, current/past role and company, education, skills, languages, and follower counts. Pick this for discovery when you do not already have a specific profile URL; read-only and idempotent, but billed per result so narrow filters before running. Set Enrich Profiles to return full profile records instead of just URLs (costs extra credits each).",
    idempotent: !0
  },
  props: {
    country: import_pieces_framework26.Property.ShortText({
      displayName: "Country",
      description: "Filter by country (ISO 3166-1 alpha-2, e.g., US)",
      required: !1
    }),
    region: import_pieces_framework26.Property.ShortText({
      displayName: "Region",
      description: "Filter by state/province (e.g., California or Washington)",
      required: !1
    }),
    city: import_pieces_framework26.Property.ShortText({
      displayName: "City",
      description: 'Filter by city (e.g., Seattle OR "Palo Alto")',
      required: !1
    }),
    first_name: import_pieces_framework26.Property.ShortText({
      displayName: "First Name",
      description: "Filter by first name (e.g., Bill OR Mark)",
      required: !1
    }),
    last_name: import_pieces_framework26.Property.ShortText({
      displayName: "Last Name",
      description: "Filter by last name (e.g., Gates or Zuckerberg)",
      required: !1
    }),
    headline: import_pieces_framework26.Property.ShortText({
      displayName: "Headline",
      description: "Filter by headline (boolean search)",
      required: !1
    }),
    summary: import_pieces_framework26.Property.ShortText({
      displayName: "Summary",
      description: "Filter by summary (boolean search)",
      required: !1
    }),
    current_role_title: import_pieces_framework26.Property.ShortText({
      displayName: "Current Role Title",
      description: "Filter by current job title (e.g., CEO || Founder)",
      required: !1
    }),
    past_role_title: import_pieces_framework26.Property.ShortText({
      displayName: "Past Role Title",
      description: "Filter by past job title (boolean search)",
      required: !1
    }),
    current_role_before: import_pieces_framework26.Property.ShortText({
      displayName: "Current Role Before",
      description: "People who started current role before this date (YYYY-MM-DD)",
      required: !1
    }),
    current_role_after: import_pieces_framework26.Property.ShortText({
      displayName: "Current Role After",
      description: "People who started current role after this date (YYYY-MM-DD)",
      required: !1
    }),
    current_company_profile_url: import_pieces_framework26.Property.ShortText({
      displayName: "Current Company Profile URL",
      description: "Filter by current company profile URL",
      required: !1
    }),
    past_company_profile_url: import_pieces_framework26.Property.ShortText({
      displayName: "Past Company Profile URL",
      description: "Filter by past company profile URL",
      required: !1
    }),
    current_company_name: import_pieces_framework26.Property.ShortText({
      displayName: "Current Company Name",
      description: "Filter by current company name (e.g., Stripe OR Apple)",
      required: !1
    }),
    past_company_name: import_pieces_framework26.Property.ShortText({
      displayName: "Past Company Name",
      description: "Filter by past company name (e.g., Stripe OR Apple)",
      required: !1
    }),
    current_job_description: import_pieces_framework26.Property.ShortText({
      displayName: "Current Job Description",
      description: "Filter by current job description (boolean search)",
      required: !1
    }),
    past_job_description: import_pieces_framework26.Property.ShortText({
      displayName: "Past Job Description",
      description: "Filter by past job description (boolean search)",
      required: !1
    }),
    current_company_country: import_pieces_framework26.Property.ShortText({
      displayName: "Current Company Country",
      description: "Filter by current company country (ISO alpha-2)",
      required: !1
    }),
    current_company_region: import_pieces_framework26.Property.ShortText({
      displayName: "Current Company Region",
      description: "Filter by current company region",
      required: !1
    }),
    current_company_city: import_pieces_framework26.Property.ShortText({
      displayName: "Current Company City",
      description: "Filter by current company city",
      required: !1
    }),
    current_company_type: import_pieces_framework26.Property.StaticDropdown({
      displayName: "Current Company Type",
      description: "Filter by current company type",
      required: !1,
      options: {
        options: [
          { label: "Educational", value: "EDUCATIONAL" },
          { label: "Government Agency", value: "GOVERNMENT_AGENCY" },
          { label: "Non-Profit", value: "NON_PROFIT" },
          { label: "Partnership", value: "PARTNERSHIP" },
          { label: "Privately Held", value: "PRIVATELY_HELD" },
          { label: "Public Company", value: "PUBLIC_COMPANY" },
          { label: "Self-Employed", value: "SELF_EMPLOYED" },
          { label: "Sole Proprietorship", value: "SELF_OWNED" }
        ]
      }
    }),
    current_company_industry: import_pieces_framework26.Property.ShortText({
      displayName: "Current Company Industry",
      description: "Filter by current company industry (boolean search)",
      required: !1
    }),
    current_company_primary_industry: import_pieces_framework26.Property.ShortText({
      displayName: "Current Company Primary Industry",
      description: "Filter by current company primary industry",
      required: !1
    }),
    current_company_specialities: import_pieces_framework26.Property.ShortText({
      displayName: "Current Company Specialities",
      description: "Filter by current company speciality (boolean search)",
      required: !1
    }),
    current_company_employee_count_category: import_pieces_framework26.Property.StaticDropdown({
      displayName: "Current Company Employee Count Category",
      description: "Filter by current company size category",
      required: !1,
      options: {
        options: [
          { label: "Custom (default)", value: "custom" },
          { label: "Startup (1-10)", value: "startup" },
          { label: "Small (11-50)", value: "small" },
          { label: "Medium (51-250)", value: "medium" },
          { label: "Large (251-1000)", value: "large" },
          { label: "Enterprise (1001+)", value: "enterprise" }
        ]
      }
    }),
    current_company_employee_count_min: import_pieces_framework26.Property.ShortText({
      displayName: "Current Company Min Employees",
      description: "Minimum employees at current company",
      required: !1
    }),
    current_company_employee_count_max: import_pieces_framework26.Property.ShortText({
      displayName: "Current Company Max Employees",
      description: "Maximum employees at current company",
      required: !1
    }),
    current_company_follower_count_min: import_pieces_framework26.Property.ShortText({
      displayName: "Current Company Min Followers",
      description: "Minimum follower count at current company",
      required: !1
    }),
    current_company_follower_count_max: import_pieces_framework26.Property.ShortText({
      displayName: "Current Company Max Followers",
      description: "Maximum follower count at current company",
      required: !1
    }),
    current_company_description: import_pieces_framework26.Property.ShortText({
      displayName: "Current Company Description",
      description: "Filter by current company description (boolean search)",
      required: !1
    }),
    current_company_founded_after_year: import_pieces_framework26.Property.ShortText({
      displayName: "Current Company Founded After Year",
      description: "Filter by current company founded after this year",
      required: !1
    }),
    current_company_founded_before_year: import_pieces_framework26.Property.ShortText({
      displayName: "Current Company Founded Before Year",
      description: "Filter by current company founded before this year",
      required: !1
    }),
    current_company_funding_amount_min: import_pieces_framework26.Property.ShortText({
      displayName: "Current Company Funding Min (USD)",
      description: "Minimum funding raised by current company",
      required: !1
    }),
    current_company_funding_amount_max: import_pieces_framework26.Property.ShortText({
      displayName: "Current Company Funding Max (USD)",
      description: "Maximum funding raised by current company",
      required: !1
    }),
    current_company_funding_raised_after: import_pieces_framework26.Property.ShortText({
      displayName: "Current Company Funding Raised After",
      description: "Current company raised funding after this date (YYYY-MM-DD)",
      required: !1
    }),
    current_company_funding_raised_before: import_pieces_framework26.Property.ShortText({
      displayName: "Current Company Funding Raised Before",
      description: "Current company raised funding before this date (YYYY-MM-DD)",
      required: !1
    }),
    current_company_domain_name: import_pieces_framework26.Property.ShortText({
      displayName: "Current Company Domain Name",
      description: "Filter by current company domain (e.g., pfizer.com)",
      required: !1
    }),
    education_field_of_study: import_pieces_framework26.Property.ShortText({
      displayName: "Education Field of Study",
      description: "Filter by field of study (e.g., computer science)",
      required: !1
    }),
    education_degree_name: import_pieces_framework26.Property.ShortText({
      displayName: "Education Degree Name",
      description: "Filter by degree name (e.g., MBA)",
      required: !1
    }),
    education_school_name: import_pieces_framework26.Property.ShortText({
      displayName: "Education School Name",
      description: 'Filter by school name (e.g., "Harvard University")',
      required: !1
    }),
    education_school_profile_url: import_pieces_framework26.Property.ShortText({
      displayName: "Education School Profile URL",
      description: "Filter by school profile URL",
      required: !1
    }),
    industries: import_pieces_framework26.Property.ShortText({
      displayName: "Industries",
      description: "Filter by person's inferred industry (boolean search)",
      required: !1
    }),
    interests: import_pieces_framework26.Property.ShortText({
      displayName: "Interests",
      description: "Filter by interests (boolean search)",
      required: !1
    }),
    skills: import_pieces_framework26.Property.ShortText({
      displayName: "Skills",
      description: "Filter by skills (boolean search)",
      required: !1
    }),
    skills_all_in_list: import_pieces_framework26.Property.ShortText({
      displayName: "Skills (All in List)",
      description: "Filter by all skills in a comma-separated list (e.g., rust,mongodb). Cannot combine with Skills.",
      required: !1
    }),
    languages: import_pieces_framework26.Property.ShortText({
      displayName: "Languages",
      description: "Filter by language (e.g., Mandarin OR Chinese)",
      required: !1
    }),
    groups: import_pieces_framework26.Property.ShortText({
      displayName: "Groups",
      description: "Filter by group membership (boolean search)",
      required: !1
    }),
    follower_count_min: import_pieces_framework26.Property.ShortText({
      displayName: "Follower Count Min",
      description: "Minimum follower count",
      required: !1
    }),
    follower_count_max: import_pieces_framework26.Property.ShortText({
      displayName: "Follower Count Max",
      description: "Maximum follower count",
      required: !1
    }),
    public_identifier_in_list: import_pieces_framework26.Property.ShortText({
      displayName: "Public Identifier In List",
      description: "Comma-separated list of public identifiers person must be in",
      required: !1
    }),
    public_identifier_not_in_list: import_pieces_framework26.Property.ShortText({
      displayName: "Public Identifier Not In List",
      description: "Comma-separated list of public identifiers person must not be in",
      required: !1
    }),
    page_size: import_pieces_framework26.Property.ShortText({
      displayName: "Page Size",
      description: "Maximum results per call (1-100, default: 100)",
      required: !1
    }),
    enrich_profiles: import_pieces_framework26.Property.StaticDropdown({
      displayName: "Enrich Profiles",
      description: "Return full profiles instead of just URLs (+1 credit each)",
      required: !1,
      options: {
        options: [
          { label: "Skip (default)", value: "skip" },
          { label: "Enrich (+1 credit each)", value: "enrich" }
        ]
      }
    }),
    use_cache: import_pieces_framework26.Property.StaticDropdown({
      displayName: "Cache Strategy",
      description: "Control how cached data is used",
      required: !1,
      options: {
        options: [
          { label: "If Present (default)", value: "if-present" },
          { label: "If Recent (+1-2 credits per result)", value: "if-recent" }
        ]
      }
    })
  },
  async run(context) {
    return await enrichlayerApiCall(
      context.auth.secret_text,
      ENDPOINTS.PERSON_SEARCH,
      {
        country: context.propsValue.country,
        region: context.propsValue.region,
        city: context.propsValue.city,
        first_name: context.propsValue.first_name,
        last_name: context.propsValue.last_name,
        headline: context.propsValue.headline,
        summary: context.propsValue.summary,
        current_role_title: context.propsValue.current_role_title,
        past_role_title: context.propsValue.past_role_title,
        current_role_before: context.propsValue.current_role_before,
        current_role_after: context.propsValue.current_role_after,
        current_company_profile_url: context.propsValue.current_company_profile_url,
        past_company_profile_url: context.propsValue.past_company_profile_url,
        current_company_name: context.propsValue.current_company_name,
        past_company_name: context.propsValue.past_company_name,
        current_job_description: context.propsValue.current_job_description,
        past_job_description: context.propsValue.past_job_description,
        current_company_country: context.propsValue.current_company_country,
        current_company_region: context.propsValue.current_company_region,
        current_company_city: context.propsValue.current_company_city,
        current_company_type: context.propsValue.current_company_type,
        current_company_industry: context.propsValue.current_company_industry,
        current_company_primary_industry: context.propsValue.current_company_primary_industry,
        current_company_specialities: context.propsValue.current_company_specialities,
        current_company_employee_count_category: context.propsValue.current_company_employee_count_category,
        current_company_employee_count_min: context.propsValue.current_company_employee_count_min,
        current_company_employee_count_max: context.propsValue.current_company_employee_count_max,
        current_company_follower_count_min: context.propsValue.current_company_follower_count_min,
        current_company_follower_count_max: context.propsValue.current_company_follower_count_max,
        current_company_description: context.propsValue.current_company_description,
        current_company_founded_after_year: context.propsValue.current_company_founded_after_year,
        current_company_founded_before_year: context.propsValue.current_company_founded_before_year,
        current_company_funding_amount_min: context.propsValue.current_company_funding_amount_min,
        current_company_funding_amount_max: context.propsValue.current_company_funding_amount_max,
        current_company_funding_raised_after: context.propsValue.current_company_funding_raised_after,
        current_company_funding_raised_before: context.propsValue.current_company_funding_raised_before,
        current_company_domain_name: context.propsValue.current_company_domain_name,
        education_field_of_study: context.propsValue.education_field_of_study,
        education_degree_name: context.propsValue.education_degree_name,
        education_school_name: context.propsValue.education_school_name,
        education_school_profile_url: context.propsValue.education_school_profile_url,
        industries: context.propsValue.industries,
        interests: context.propsValue.interests,
        skills: context.propsValue.skills,
        skills_all_in_list: context.propsValue.skills_all_in_list,
        languages: context.propsValue.languages,
        groups: context.propsValue.groups,
        follower_count_min: context.propsValue.follower_count_min,
        follower_count_max: context.propsValue.follower_count_max,
        public_identifier_in_list: context.propsValue.public_identifier_in_list,
        public_identifier_not_in_list: context.propsValue.public_identifier_not_in_list,
        page_size: context.propsValue.page_size,
        enrich_profiles: context.propsValue.enrich_profiles,
        use_cache: context.propsValue.use_cache
      }
    );
  }
});

// ../ap/packages/pieces/community/enrichlayer/src/lib/actions/get-credit-balance.ts
var import_pieces_framework27 = require("@activepieces/pieces-framework");
var getCreditBalance = (0, import_pieces_framework27.createAction)({
  name: "get_credit_balance",
  auth: enrichlayerAuth,
  displayName: "Get Credit Balance",
  description: "View your Enrich Layer API credit balance (0 credits)",
  audience: "both",
  aiMetadata: {
    description: "Read the remaining credit balance on the connected Enrich Layer API account. Pick this to check available quota before running billed enrichment or search actions; takes no input, costs no credits, and is read-only and idempotent.",
    idempotent: !0
  },
  props: {},
  async run(context) {
    return await enrichlayerApiCall(
      context.auth.secret_text,
      ENDPOINTS.CREDIT_BALANCE,
      {}
    );
  }
});

// ../ap/packages/pieces/community/enrichlayer/src/index.ts
var enrichlayer = (0, import_pieces_framework28.createPiece)({
  displayName: "Enrich Layer",
  description: "Professional network data enrichment API \u2014 company profiles, person data, work emails, and more.",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/enrichlayer.png",
  categories: [import_pieces_framework29.PieceCategory.SALES_AND_CRM],
  authors: ["enrichlayer"],
  auth: enrichlayerAuth,
  actions: [
    getCompanyProfile,
    getPersonProfile,
    getSchoolProfile,
    getCompanyLookup,
    getCompanyIdLookup,
    getPersonLookup,
    getRoleLookup,
    getEmployeeListing,
    getEmployeeCount,
    getEmployeeSearch,
    getStudentListing,
    getWorkEmailLookup,
    getReverseEmailLookup,
    getReversePhoneLookup,
    getPersonalContact,
    getPersonalEmail,
    getDisposableEmailCheck,
    getCompanyPicture,
    getPersonPicture,
    getJobProfile,
    getJobSearch,
    getJobCount,
    getCompanySearch,
    getPersonSearch,
    getCreditBalance,
    (0, import_pieces_common2.createCustomApiCallAction)({
      auth: enrichlayerAuth,
      baseUrl: () => "https://enrichlayer.com",
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  enrichlayer
});
