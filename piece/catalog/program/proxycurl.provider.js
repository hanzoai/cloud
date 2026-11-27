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

// ../ap/packages/pieces/community/proxycurl/src/index.ts
var index_exports = {};
__export(index_exports, {
  proxycurl: () => proxycurl,
  proxycurlAuth: () => proxycurlAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/proxycurl/src/lib/actions/get-person-profile.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/proxycurl/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://nubela.co/proxycurl/api";
async function proxycurlApiCall({
  apiKey,
  method,
  resourceUri,
  query,
  body
}) {
  var _a, _b, _c, _d, _e;
  let queryParams = {};
  if (query)
    for (let [key, value] of Object.entries(query))
      value != null && value !== "" && (queryParams[key] = String(value));
  let request = {
    method,
    url: `${BASE_URL}${resourceUri}`,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token: apiKey
    },
    headers: {
      Accept: "application/json"
    },
    queryParams,
    body
  };
  try {
    let response = await import_pieces_common.httpClient.sendRequest(request);
    if (response.status >= 400) {
      let bodyMessage = typeof response.body == "string" ? response.body : JSON.stringify(response.body);
      throw new Error(
        `Proxycurl API error ${response.status}: ${bodyMessage}`
      );
    }
    return response.body;
  } catch (error) {
    let proxycurlError = error, statusCode = (_c = (_b = (_a = proxycurlError.response) == null ? void 0 : _a.status) != null ? _b : proxycurlError.statusCode) != null ? _c : proxycurlError.status, errorBody = (_e = (_d = proxycurlError.response) == null ? void 0 : _d.body) != null ? _e : proxycurlError.body;
    if (statusCode) {
      let bodyMessage = typeof errorBody == "string" ? errorBody : JSON.stringify(errorBody);
      throw new Error(
        `Proxycurl API error ${statusCode}: ${bodyMessage || proxycurlError.message || "Unknown error"}`
      );
    }
    throw new Error(
      `Proxycurl request failed: ${proxycurlError.message || "Unknown error"}`
    );
  }
}

// ../ap/packages/pieces/community/proxycurl/src/lib/actions/get-person-profile.ts
var getPersonProfileAction = (0, import_pieces_framework.createAction)({
  name: "get_person_profile",
  displayName: "Get Person Profile",
  description: "Fetch a LinkedIn person profile from Proxycurl.",
  audience: "both",
  aiMetadata: { description: "Enrich a single individual by fetching their LinkedIn person profile from Proxycurl. Choose this when you already have a person's public LinkedIn profile URL and need their structured profile data; for finding people without a known URL use Search People instead. Requires the public LinkedIn profile URL (e.g. https://www.linkedin.com/in/williamhgates). Read-only lookup, safe to repeat.", idempotent: !0 },
  auth: proxycurlAuth,
  props: {
    linkedin_profile_url: import_pieces_framework.Property.ShortText({
      displayName: "LinkedIn Profile URL",
      description: "Public LinkedIn profile URL, for example https://www.linkedin.com/in/williamhgates",
      required: !0
    })
  },
  async run(context) {
    return proxycurlApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common2.HttpMethod.GET,
      resourceUri: "/v2/linkedin",
      query: {
        url: context.propsValue.linkedin_profile_url
      }
    });
  }
});

// ../ap/packages/pieces/community/proxycurl/src/lib/actions/get-company-profile.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var getCompanyProfileAction = (0, import_pieces_framework2.createAction)({
  name: "get_company_profile",
  displayName: "Get Company Profile",
  description: "Fetch a LinkedIn company profile from Proxycurl.",
  audience: "both",
  aiMetadata: { description: "Enrich a company by fetching its LinkedIn company profile from Proxycurl. Choose this when you have a company's LinkedIn URL and need its structured firmographic data. Requires the LinkedIn company URL (e.g. https://www.linkedin.com/company/apple/). Read-only lookup, safe to repeat.", idempotent: !0 },
  auth: proxycurlAuth,
  props: {
    url: import_pieces_framework2.Property.ShortText({
      displayName: "LinkedIn Company URL",
      description: "LinkedIn company URL, for example https://www.linkedin.com/company/apple/",
      required: !0
    })
  },
  async run(context) {
    return proxycurlApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common3.HttpMethod.GET,
      resourceUri: "/linkedin/company",
      query: {
        url: context.propsValue.url
      }
    });
  }
});

// ../ap/packages/pieces/community/proxycurl/src/lib/actions/search-people.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");
var searchPeopleAction = (0, import_pieces_framework3.createAction)({
  name: "search_people",
  displayName: "Search People",
  description: "Search for people in Proxycurl using lightweight keyword filters.",
  audience: "both",
  aiMetadata: { description: "Find people via Proxycurl using keyword filters (country, headline keywords, summary keywords) when you do not already have a specific LinkedIn profile URL; to enrich a known URL use Get Person Profile instead. At least one filter must be provided or the call fails. Read-only search, safe to repeat.", idempotent: !0 },
  auth: proxycurlAuth,
  props: {
    country: import_pieces_framework3.Property.ShortText({
      displayName: "Country",
      description: "Optional country filter, for example us, gb, or sg.",
      required: !1
    }),
    headline: import_pieces_framework3.Property.ShortText({
      displayName: "Headline Keywords",
      description: "Optional keywords expected in the person headline.",
      required: !1
    }),
    summary_keywords: import_pieces_framework3.Property.ShortText({
      displayName: "Summary Keywords",
      description: "Optional keywords expected in the summary/about section.",
      required: !1
    })
  },
  async run(context) {
    let { country, headline, summary_keywords } = context.propsValue;
    if (!country && !headline && !summary_keywords)
      throw new Error(
        "At least one search filter must be provided: Country, Headline Keywords, or Summary Keywords."
      );
    return proxycurlApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common4.HttpMethod.GET,
      resourceUri: "/v2/search/person",
      query: {
        country,
        headline,
        summary_keywords
      }
    });
  }
});

// ../ap/packages/pieces/community/proxycurl/src/lib/actions/lookup-person-email.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var lookupPersonEmailAction = (0, import_pieces_framework4.createAction)({
  name: "lookup_person_email",
  displayName: "Lookup Person Email",
  description: "Lookup the work email address for a LinkedIn person profile.",
  audience: "both",
  aiMetadata: { description: "Resolve the work email address for a person from their LinkedIn profile URL via Proxycurl. Choose this when you have a profile URL and specifically need a contact email rather than the full profile. Requires the public LinkedIn profile URL; an optional callback URL receives an async completion webhook. Read-only lookup, safe to repeat.", idempotent: !0 },
  auth: proxycurlAuth,
  props: {
    linkedin_profile_url: import_pieces_framework4.Property.ShortText({
      displayName: "LinkedIn Profile URL",
      description: "Public LinkedIn profile URL to enrich.",
      required: !0
    }),
    callback_url: import_pieces_framework4.Property.ShortText({
      displayName: "Callback URL",
      description: "Optional webhook URL for async completion callbacks from Proxycurl.",
      required: !1
    })
  },
  async run(context) {
    return proxycurlApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common5.HttpMethod.GET,
      resourceUri: "/linkedin/profile/email",
      query: {
        linkedin_profile_url: context.propsValue.linkedin_profile_url,
        callback_url: context.propsValue.callback_url
      }
    });
  }
});

// ../ap/packages/pieces/community/proxycurl/src/lib/actions/custom-api-call.ts
var import_pieces_common6 = require("@activepieces/pieces-common");
var customApiCallAction = (0, import_pieces_common6.createCustomApiCallAction)({
  auth: proxycurlAuth,
  name: "custom_api_call",
  displayName: "Custom API Call",
  description: "Make a custom API call to any Proxycurl endpoint.",
  baseUrl: () => BASE_URL,
  authMapping: async (auth) => ({
    Authorization: `Bearer ${auth.secret_text}`,
    Accept: "application/json"
  })
});

// ../ap/packages/pieces/community/proxycurl/src/index.ts
var markdownDescription = `
To use Proxycurl:
1. Sign in to your Proxycurl account at https://nubela.co/proxycurl.
2. Open your dashboard and copy your API key.
3. Paste the API key here.

Authentication note:
- Proxycurl's public SDK/docs show Bearer token authorization for API requests.
`, proxycurlAuth = import_pieces_framework5.PieceAuth.SecretText({
  displayName: "Proxycurl API Key",
  description: markdownDescription,
  required: !0
}), proxycurl = (0, import_pieces_framework5.createPiece)({
  displayName: "Proxycurl",
  description: "Enrich LinkedIn people and company profiles with Proxycurl.",
  auth: proxycurlAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/proxycurl.png",
  categories: [import_pieces_framework6.PieceCategory.SALES_AND_CRM],
  authors: ["Harmatta"],
  actions: [
    getPersonProfileAction,
    getCompanyProfileAction,
    searchPeopleAction,
    lookupPersonEmailAction,
    customApiCallAction
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  proxycurl,
  proxycurlAuth
});
