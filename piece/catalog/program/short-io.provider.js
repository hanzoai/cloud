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

// ../ap/packages/pieces/community/short-io/src/index.ts
var index_exports = {};
__export(index_exports, {
  shortIo: () => shortIo
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common14 = require("@activepieces/pieces-common"), import_pieces_framework14 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/short-io/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/short-io/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
async function shortIoApiCall({
  method,
  resourceUri,
  url,
  query,
  body,
  auth
}) {
  var _a, _b, _c;
  let finalUrl = url != null ? url : `https://api.short.io${resourceUri != null ? resourceUri : ""}`, queryParams = {};
  if (query)
    for (let [key, value] of Object.entries(query))
      value != null && (queryParams[key] = String(value));
  let request = {
    method,
    url: finalUrl,
    headers: {
      authorization: auth.props.apiKey,
      "Content-Type": "application/json",
      accept: "application/json"
    },
    queryParams,
    body
  };
  try {
    return (await import_pieces_common.httpClient.sendRequest(request)).body;
  } catch (error) {
    let status = (_a = error.response) == null ? void 0 : _a.status, message = ((_c = (_b = error.response) == null ? void 0 : _b.body) == null ? void 0 : _c.message) || error.message || "Unknown Short.io API error";
    throw new Error(
      `Short.io API Error (${status || "No Status"}): ${message}`
    );
  }
}

// ../ap/packages/pieces/community/short-io/src/lib/common/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework"), shortIoAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: "Enter your Short.io API Key",
  props: {
    apiKey: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API Key",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    try {
      return await shortIoApiCall({
        method: import_pieces_common2.HttpMethod.GET,
        auth: {
          type: import_pieces_framework2.AppConnectionType.CUSTOM_AUTH,
          props: auth
        },
        resourceUri: "/api/domains"
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API Key"
      };
    }
  },
  required: !0
});

// ../ap/packages/pieces/community/short-io/src/lib/actions/create-country-targeting-for-a-link.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/short-io/src/lib/common/props.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var domainIdDropdown = import_pieces_framework3.Property.Dropdown({
  auth: shortIoAuth,
  displayName: "Domain",
  description: "Select the domain to use for the link",
  required: !0,
  refreshers: ["auth"],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Connect your Short.io account first"
      };
    try {
      let domains = await shortIoApiCall({
        auth,
        method: import_pieces_common3.HttpMethod.GET,
        resourceUri: "/api/domains"
      });
      return {
        disabled: !1,
        options: domains.map((domain) => ({
          label: domain.hostname,
          value: JSON.stringify({
            id: domain.id,
            hostname: domain.hostname
          })
        })),
        placeholder: domains.length === 0 ? "No domains available" : "Select a domain"
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: `Error loading domains: ${error.message}`
      };
    }
  }
}), linkIdDropdown = import_pieces_framework3.Property.Dropdown({
  auth: shortIoAuth,
  displayName: "Short Link",
  description: "Select the short link from the domain",
  required: !0,
  refreshers: ["auth", "domain"],
  options: async ({ auth, domain }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Connect your Short.io account first"
      };
    if (!domain)
      return {
        disabled: !0,
        options: [],
        placeholder: "Select a domain first"
      };
    try {
      let domainObject = JSON.parse(domain), response = await shortIoApiCall({
        auth,
        method: import_pieces_common3.HttpMethod.GET,
        resourceUri: "/api/links",
        query: {
          domain_id: domainObject.id,
          limit: 100
        }
      });
      return {
        disabled: !1,
        options: response.links.map((link) => ({
          label: `${link.path || "(auto)"} \u2192 ${link.originalURL}`,
          value: link.idString
        })),
        placeholder: response.links.length === 0 ? "No links available" : "Select a link"
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: `Error loading links: ${error.message}`
      };
    }
  }
}), folderIdDropdown = import_pieces_framework3.Property.Dropdown({
  auth: shortIoAuth,
  displayName: "Folder",
  description: "Select the folder to add the link to.",
  required: !1,
  refreshers: ["auth", "domain"],
  options: async ({ auth, domain }) => {
    if (!auth || !domain)
      return {
        disabled: !0,
        options: [],
        placeholder: "Select a domain first"
      };
    try {
      let domainObject = JSON.parse(domain), foldersArray = (await shortIoApiCall({
        auth,
        method: import_pieces_common3.HttpMethod.GET,
        resourceUri: `/links/folders/${domainObject.id}`
      })).linkFolders;
      return !foldersArray || foldersArray.length === 0 ? {
        disabled: !0,
        options: [],
        placeholder: "No folders found in this domain"
      } : {
        disabled: !1,
        options: foldersArray.map((folder) => ({
          label: folder.name,
          value: folder.id
        })),
        placeholder: "Select a folder"
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: `Error: Could not load folders. ${error.message}`
      };
    }
  }
});

// ../ap/packages/pieces/community/short-io/src/lib/actions/create-country-targeting-for-a-link.ts
var createCountryTargetingRuleAction = (0, import_pieces_framework4.createAction)({
  auth: shortIoAuth,
  name: "create-country-targeting-rule",
  classification: "WRITE",
  displayName: "Create Country Targeting Rule",
  description: "Set geographic targeting rules for a link with specific destination per country.",
  audience: "both",
  aiMetadata: { description: "Adds a country-targeting rule to an existing short link so visitors from a given country are redirected to a country-specific URL instead of the default destination. Use to set up geo-redirects on a link you already have the ID for. Requires the link ID, a 2-letter ISO country code, and a valid redirect URL; not idempotent \u2014 a rule for a country that already exists is rejected (409).", idempotent: !1 },
  props: {
    domain: domainIdDropdown,
    linkId: linkIdDropdown,
    country: import_pieces_framework4.Property.ShortText({
      displayName: "Country Code",
      description: "ISO 3166-1 alpha-2 country code (e.g., US, GB, CA, IN). Must be exactly 2 characters.",
      required: !0
    }),
    originalURL: import_pieces_framework4.Property.ShortText({
      displayName: "Country-Specific Redirect URL",
      description: "The destination URL when users from this country access the link. Must be a valid URL.",
      required: !0
    })
  },
  async run({ propsValue, auth }) {
    let { linkId, domain: domainString, country, originalURL } = propsValue;
    if (!country || country.trim().length !== 2)
      throw new Error("Country code must be exactly 2 characters (e.g., US, GB, CA)");
    let countryCode = country.trim().toUpperCase();
    try {
      new URL(originalURL);
    } catch (error) {
      throw new Error("Invalid URL format. Please provide a valid URL starting with http:// or https://");
    }
    let query = {};
    if (domainString) {
      let domainObject = JSON.parse(domainString);
      query.domainId = String(domainObject.id);
    }
    let body = {
      country: countryCode,
      originalURL
    };
    try {
      let response = await shortIoApiCall({
        method: import_pieces_common4.HttpMethod.POST,
        auth,
        resourceUri: `/link_country/${linkId}`,
        query,
        body
      });
      return {
        success: !0,
        message: `Country targeting rule for ${countryCode} created successfully`,
        data: response
      };
    } catch (error) {
      throw error.message.includes("400") ? new Error(
        "Invalid request parameters. Please check the country code format and URL validity."
      ) : error.message.includes("401") || error.message.includes("403") ? new Error(
        "Authentication failed. Please check your API key and permissions."
      ) : error.message.includes("404") ? new Error(
        "Short link not found. Please check the link ID and try again."
      ) : error.message.includes("409") ? new Error(
        `A country targeting rule for ${countryCode} already exists for this link.`
      ) : error.message.includes("429") ? new Error(
        "Rate limit exceeded. Please wait a moment before trying again."
      ) : new Error(`Failed to create country targeting rule: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/short-io/src/lib/actions/create-short-link.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");
var createShortLinkAction = (0, import_pieces_framework5.createAction)({
  auth: shortIoAuth,
  name: "create-short-link",
  classification: "WRITE",
  displayName: "Create Short Link",
  description: "Create a new short link with optional parameters (title, UTM tags, expiration, cloaking, etc.)",
  audience: "both",
  aiMetadata: { description: "Creates a new short link on a Short.io domain pointing to a target URL, with optional path/slug, title, UTM tags, expiration, click limit, cloaking, A/B split, and analytics integrations. Use to generate a shortened URL. Requires an original URL and a domain; not idempotent \u2014 each call creates a new link (set Allow Duplicates to avoid 409 conflicts on a repeated path).", idempotent: !1 },
  props: {
    originalURL: import_pieces_framework5.Property.ShortText({
      displayName: "Original URL",
      required: !0
    }),
    domain: domainIdDropdown,
    title: import_pieces_framework5.Property.ShortText({
      displayName: "Title",
      required: !1
    }),
    folderId: folderIdDropdown,
    cloaking: import_pieces_framework5.Property.Checkbox({
      displayName: "Enable Cloaking",
      required: !1
    }),
    password: import_pieces_framework5.Property.ShortText({
      displayName: "Password",
      required: !1
    }),
    redirectType: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Redirect Type",
      description: "The HTTP status code for the redirect. The default is 302 (Found).",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "301 (Moved Permanently)", value: "301" },
          { label: "302 (Found)", value: "302" },
          { label: "303 (See Other)", value: "303" },
          { label: "307 (Temporary Redirect)", value: "307" },
          { label: "308 (Permanent Redirect)", value: "308" }
        ]
      }
    }),
    expiresAt: import_pieces_framework5.Property.DateTime({
      displayName: "Expiration Date",
      description: "The date and time when the link will become inactive.",
      required: !1
    }),
    expiredURL: import_pieces_framework5.Property.ShortText({
      displayName: "Expired Redirect URL",
      required: !1
    }),
    tags: import_pieces_framework5.Property.Array({
      displayName: "Tags",
      description: "Array of tags for the link",
      required: !1
    }),
    utmSource: import_pieces_framework5.Property.ShortText({
      displayName: "UTM Source",
      required: !1
    }),
    utmMedium: import_pieces_framework5.Property.ShortText({
      displayName: "UTM Medium",
      required: !1
    }),
    utmCampaign: import_pieces_framework5.Property.ShortText({
      displayName: "UTM Campaign",
      required: !1
    }),
    utmTerm: import_pieces_framework5.Property.ShortText({
      displayName: "UTM Term",
      required: !1
    }),
    utmContent: import_pieces_framework5.Property.ShortText({
      displayName: "UTM Content",
      required: !1
    }),
    ttl: import_pieces_framework5.Property.Number({
      displayName: "Time to Live (in seconds)",
      description: "\u26A0\uFE0F CAUTION: Link will be PERMANENTLY DELETED after this many seconds. This action cannot be undone. Use with extreme caution.",
      required: !1
    }),
    androidURL: import_pieces_framework5.Property.ShortText({
      displayName: "Android URL",
      required: !1
    }),
    iphoneURL: import_pieces_framework5.Property.ShortText({
      displayName: "iPhone URL",
      required: !1
    }),
    createdAt: import_pieces_framework5.Property.DateTime({
      displayName: "Creation Time",
      description: "Overrides the creation time of the link.",
      required: !1
    }),
    clicksLimit: import_pieces_framework5.Property.Number({
      displayName: "Clicks Limit",
      description: "Disable link after specified number of clicks (minimum: 1)",
      required: !1
    }),
    passwordContact: import_pieces_framework5.Property.Checkbox({
      displayName: "Show Contact for Password Recovery",
      required: !1
    }),
    skipQS: import_pieces_framework5.Property.Checkbox({
      displayName: "Skip Query String Merge",
      required: !1
    }),
    archived: import_pieces_framework5.Property.Checkbox({
      displayName: "Archive Link",
      required: !1
    }),
    splitURL: import_pieces_framework5.Property.ShortText({
      displayName: "Split URL",
      required: !1
    }),
    splitPercent: import_pieces_framework5.Property.Number({
      displayName: "Split Percent",
      description: "Split URL percentage (1-100)",
      required: !1
    }),
    integrationAdroll: import_pieces_framework5.Property.ShortText({
      displayName: "Adroll Integration ID",
      required: !1
    }),
    integrationFB: import_pieces_framework5.Property.ShortText({
      displayName: "Facebook Integration ID",
      required: !1
    }),
    integrationGA: import_pieces_framework5.Property.ShortText({
      displayName: "Google Analytics ID",
      required: !1
    }),
    integrationGTM: import_pieces_framework5.Property.ShortText({
      displayName: "Google Tag Manager ID",
      required: !1
    }),
    allowDuplicates: import_pieces_framework5.Property.Checkbox({
      displayName: "Allow Duplicates",
      required: !1
    }),
    path: import_pieces_framework5.Property.ShortText({
      displayName: "Path",
      description: "Custom path for the short link (optional).",
      required: !1
    })
  },
  async run({ propsValue, auth }) {
    let {
      originalURL,
      domain: domainString,
      path,
      title,
      cloaking,
      password,
      redirectType,
      expiresAt,
      expiredURL,
      tags,
      utmSource,
      utmMedium,
      utmCampaign,
      utmTerm,
      utmContent,
      ttl,
      androidURL,
      iphoneURL,
      createdAt,
      clicksLimit,
      passwordContact,
      skipQS,
      archived,
      splitURL,
      splitPercent,
      integrationAdroll,
      integrationFB,
      integrationGA,
      integrationGTM,
      allowDuplicates,
      folderId
    } = propsValue;
    if (clicksLimit && clicksLimit < 1)
      throw new Error("Clicks limit must be at least 1");
    if (splitPercent && (splitPercent < 1 || splitPercent > 100))
      throw new Error("Split percent must be between 1 and 100");
    if (redirectType && !["301", "302", "303", "307", "308"].includes(redirectType))
      throw new Error("Invalid redirect type. Must be one of: 301, 302, 303, 307, 308");
    let domainObject = JSON.parse(domainString), body = {
      originalURL,
      domain: domainObject.hostname
    }, optionalParams = {
      path,
      title,
      cloaking,
      password,
      redirectType,
      expiresAt,
      expiredURL,
      tags,
      utmSource,
      utmMedium,
      utmCampaign,
      utmTerm,
      utmContent,
      ttl: ttl ? ttl * 1e3 : void 0,
      androidURL,
      iphoneURL,
      createdAt,
      clicksLimit,
      passwordContact,
      skipQS,
      archived,
      splitURL,
      splitPercent,
      integrationAdroll,
      integrationFB,
      integrationGA,
      integrationGTM,
      allowDuplicates,
      folderId
    };
    for (let [key, value] of Object.entries(optionalParams))
      value != null && value !== "" && (body[key] = value);
    try {
      return {
        success: !0,
        message: "Short link created successfully",
        data: await shortIoApiCall({
          method: import_pieces_common5.HttpMethod.POST,
          auth,
          resourceUri: "/links",
          body
        })
      };
    } catch (error) {
      throw error.message.includes("409") ? new Error(
        "A short link with this path already exists but points to a different original URL."
      ) : error.message.includes("400") ? new Error(
        "Invalid request parameters. Please check your input values and try again."
      ) : error.message.includes("401") || error.message.includes("403") ? new Error(
        "Authentication failed. Please check your API key and permissions."
      ) : error.message.includes("429") ? new Error(
        "Rate limit exceeded. Please wait a moment before trying again."
      ) : new Error(`Failed to create short link: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/short-io/src/lib/actions/delete-short-link.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var deleteShortLinkAction = (0, import_pieces_framework6.createAction)({
  auth: shortIoAuth,
  name: "delete-short-link",
  classification: "DESTRUCTIVE",
  displayName: "Delete Short Link",
  description: "Permanently delete a short link by its unique link ID.",
  audience: "both",
  aiMetadata: { description: "Permanently deletes a short link identified by its link ID. Use to remove a link that is no longer needed. Not idempotent \u2014 repeating the call returns a not-found error once the link is already gone; this is destructive and cannot be undone.", idempotent: !1 },
  props: {
    linkId: import_pieces_framework6.Property.ShortText({
      displayName: "Link ID",
      description: "The ID of the short link you want to delete (e.g., lnk_61Mb_0dnRUg3vvtmAPZh3dhQh6)",
      required: !0
    })
  },
  async run({ propsValue, auth }) {
    let { linkId } = propsValue;
    if (!linkId || linkId.trim() === "")
      throw new Error("Link ID is required and cannot be empty");
    try {
      let response = await shortIoApiCall({
        method: import_pieces_common6.HttpMethod.DELETE,
        auth,
        resourceUri: `/links/${linkId}`
      });
      return {
        success: !0,
        message: `Short link with ID ${linkId} deleted successfully`,
        data: response
      };
    } catch (error) {
      throw error.message.includes("400") ? new Error(
        "Invalid request. Please check the link ID format and try again."
      ) : error.message.includes("401") || error.message.includes("403") ? new Error(
        "Authentication failed. Please check your API key and permissions."
      ) : error.message.includes("404") ? new Error(
        `Short link with ID ${linkId} not found. It may have already been deleted.`
      ) : error.message.includes("429") ? new Error(
        "Rate limit exceeded. Please wait a moment before trying again."
      ) : new Error(`Failed to delete short link: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/short-io/src/lib/actions/expire-short-link.ts
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework");
var expireShortLinkAction = (0, import_pieces_framework7.createAction)({
  auth: shortIoAuth,
  name: "expire-short-link",
  classification: "DESTRUCTIVE",
  displayName: "Expire Short Link",
  description: "Set an expiration date or click limit to deactivate a short link.",
  audience: "both",
  aiMetadata: { description: "Sets an expiration on an existing short link (identified by its link ID) so it deactivates at a given date/time or after a number of clicks, optionally redirecting expired visitors to a fallback URL. Use to schedule a link to stop working. Requires either an expiration date or a click limit; idempotent \u2014 re-applying the same expiry settings to the same link leaves it in the same state.", idempotent: !0 },
  props: {
    domain: domainIdDropdown,
    linkId: linkIdDropdown,
    expiresAt: import_pieces_framework7.Property.DateTime({
      displayName: "Expiration Date",
      description: "The date and time when the link will become inactive.",
      required: !1
    }),
    clicksLimit: import_pieces_framework7.Property.Number({
      displayName: "Clicks Limit",
      description: "Disable link after specified number of clicks (minimum: 1)",
      required: !1
    }),
    expiredURL: import_pieces_framework7.Property.ShortText({
      displayName: "Expired Redirect URL",
      description: "Optional URL to redirect users when the link has expired.",
      required: !1
    })
  },
  async run({ propsValue, auth }) {
    let {
      linkId,
      domain: domainString,
      expiresAt,
      clicksLimit,
      expiredURL
    } = propsValue;
    if (!expiresAt && !clicksLimit)
      throw new Error("You must provide either an expiration date or a clicks limit to expire the link.");
    if (clicksLimit && clicksLimit < 1)
      throw new Error("Clicks limit must be at least 1");
    let query = {};
    if (domainString) {
      let domainObject = JSON.parse(domainString);
      query.domain_id = String(domainObject.id);
    }
    let body = {};
    expiresAt && (body.expiresAt = expiresAt), clicksLimit && (body.clicksLimit = clicksLimit), expiredURL && expiredURL.trim() !== "" && (body.expiredURL = expiredURL);
    try {
      return {
        success: !0,
        message: "Short link expiration settings updated successfully",
        data: await shortIoApiCall({
          method: import_pieces_common7.HttpMethod.POST,
          auth,
          resourceUri: `/links/${linkId}`,
          query,
          body
        })
      };
    } catch (error) {
      throw error.message.includes("400") ? new Error(
        "Invalid request parameters. Please check your expiration settings and try again."
      ) : error.message.includes("401") || error.message.includes("403") ? new Error(
        "Authentication failed. Please check your API key and permissions."
      ) : error.message.includes("404") ? new Error(
        "Short link not found. Please check the link ID and try again."
      ) : error.message.includes("429") ? new Error(
        "Rate limit exceeded. Please wait a moment before trying again."
      ) : new Error(`Failed to set link expiration: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/short-io/src/lib/actions/domain-statistics.ts
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework");
var domainStatisticsAction = (0, import_pieces_framework8.createAction)({
  auth: shortIoAuth,
  name: "get-domain-statistics",
  classification: "READ",
  displayName: "Domain Statistics",
  description: "Retrieve usage stats (clicks, conversions) for a domain within a time period.",
  audience: "both",
  aiMetadata: { description: "Retrieves aggregate click and link statistics for a whole Short.io domain over a chosen time window \u2014 either a preset range (today, last 7/30 days, this/last month) or a custom start/end date. Use to report on overall domain performance rather than a single link. Custom range requires both start and end dates; read-only and idempotent.", idempotent: !0 },
  props: {
    domainId: __spreadProps(__spreadValues({}, domainIdDropdown), {
      required: !0,
      description: "Select the domain to get statistics for"
    }),
    period: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Time Period",
      description: 'Select a predefined time interval or choose "Custom" to set specific dates.',
      required: !0,
      defaultValue: "last30",
      options: {
        disabled: !1,
        options: [
          { label: "Today", value: "today" },
          { label: "Yesterday", value: "yesterday" },
          { label: "Last 7 Days", value: "last7" },
          { label: "Last 30 Days", value: "last30" },
          { label: "This Month", value: "thisMonth" },
          { label: "Last Month", value: "lastMonth" },
          { label: "Custom Date Range", value: "custom" }
        ]
      }
    }),
    startDate: import_pieces_framework8.Property.DateTime({
      displayName: "Start Date",
      description: 'Start date for statistics (required when period is "Custom").',
      required: !1
    }),
    endDate: import_pieces_framework8.Property.DateTime({
      displayName: "End Date",
      description: 'End date for statistics (required when period is "Custom").',
      required: !1
    }),
    tz: import_pieces_framework8.Property.ShortText({
      displayName: "Timezone",
      description: "Timezone for statistics (e.g., UTC, America/New_York, Europe/London). Defaults to UTC.",
      required: !1
    }),
    clicksChartInterval: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Chart Interval",
      description: "Granularity for click statistics chart data.",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Hour", value: "hour" },
          { label: "Day", value: "day" },
          { label: "Week", value: "week" },
          { label: "Month", value: "month" }
        ]
      }
    })
  },
  async run({ propsValue, auth }) {
    let { domainId: domainString, period, startDate, endDate, tz, clicksChartInterval } = propsValue;
    if (!domainString)
      throw new Error("Domain is required. Please select a domain.");
    let domainObject = JSON.parse(domainString);
    if (period === "custom") {
      if (!startDate || !endDate)
        throw new Error("Start Date and End Date are both required when using custom period.");
      let start = new Date(startDate), end = new Date(endDate);
      if (start >= end)
        throw new Error("Start date must be before end date.");
    }
    if (tz && tz.trim() !== "") {
      let tzValue = tz.trim();
      if (tzValue.length < 3 || tzValue.includes(" "))
        throw new Error("Invalid timezone format. Use standard timezone names like UTC, America/New_York, Europe/London.");
    }
    let query = {
      period
    };
    period === "custom" && startDate && endDate && (query.startDate = new Date(startDate).toISOString().split("T")[0], query.endDate = new Date(endDate).toISOString().split("T")[0]), tz && tz.trim() !== "" && (query.tz = tz.trim()), clicksChartInterval && (query.clicksChartInterval = clicksChartInterval);
    try {
      let response = await shortIoApiCall({
        method: import_pieces_common8.HttpMethod.GET,
        auth,
        url: `https://statistics.short.io/statistics/domain/${domainObject.id}`,
        query
      }), stats = response, clicks = stats.clicks || 0, links = stats.links || 0;
      return {
        success: !0,
        message: `Retrieved statistics for ${period === "custom" ? "custom period" : period.replace(/([a-z])([A-Z])/g, "$1 $2").toLowerCase()}: ${clicks} clicks across ${links} links`,
        data: response
      };
    } catch (error) {
      throw error.message.includes("400") ? new Error(
        "Invalid request parameters. Please check your date range, timezone, and other settings."
      ) : error.message.includes("401") || error.message.includes("403") ? new Error(
        "Authentication failed or insufficient permissions. Please check your API key and domain access."
      ) : error.message.includes("404") ? new Error(
        "Domain not found. Please verify the domain exists and you have access to its statistics."
      ) : error.message.includes("429") ? new Error(
        "Rate limit exceeded. Please wait a moment before trying again."
      ) : new Error(`Failed to retrieve domain statistics: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/short-io/src/lib/actions/get-link-by-path.ts
var import_pieces_common9 = require("@activepieces/pieces-common"), import_pieces_framework9 = require("@activepieces/pieces-framework");
var getLinkByPathAction = (0, import_pieces_framework9.createAction)({
  auth: shortIoAuth,
  name: "get-short-link-info-by-path",
  classification: "READ",
  displayName: "Get Link by Path",
  description: "Retrieve detailed information about a short link using its domain and path.",
  audience: "both",
  aiMetadata: { description: "Looks up a single short link and its full details by its domain plus path/slug (rather than by link ID). Use when you know the public short URL path but not the internal link ID. Requires a domain and the bare slug (no domain or slashes in the path); read-only and idempotent.", idempotent: !0 },
  props: {
    domain: domainIdDropdown,
    path: import_pieces_framework9.Property.ShortText({
      displayName: "Link Path",
      description: 'The path/slug of the short link (e.g., "abc123", "my-link"). Do not include domain or slashes.',
      required: !0
    })
  },
  async run({ propsValue, auth }) {
    let { domain: domainString, path } = propsValue;
    if (!domainString)
      throw new Error("Domain is required. Please select a domain first.");
    if (!path || path.trim() === "")
      throw new Error("Path is required and cannot be empty.");
    let cleanPath = path.trim();
    if (cleanPath.includes("/") || cleanPath.includes("http"))
      throw new Error('Path should only contain the slug (e.g., "abc123"), not the full URL or domain.');
    let domainObject = JSON.parse(domainString), query = {
      domain: domainObject.hostname,
      path: cleanPath
    };
    try {
      let response = await shortIoApiCall({
        method: import_pieces_common9.HttpMethod.GET,
        auth,
        resourceUri: "/links/expand",
        query
      });
      return {
        success: !0,
        message: `Link information for "${cleanPath}" retrieved successfully`,
        data: response
      };
    } catch (error) {
      throw error.message.includes("400") ? new Error(
        "Invalid request parameters. Please check the domain and path values."
      ) : error.message.includes("401") || error.message.includes("403") ? new Error(
        "Authentication failed. Please check your API key and permissions."
      ) : error.message.includes("404") ? new Error(
        `Short link with path "${cleanPath}" not found on domain "${domainObject.hostname}". Please verify the path exists.`
      ) : error.message.includes("429") ? new Error(
        "Rate limit exceeded. Please wait a moment before trying again."
      ) : new Error(`Failed to retrieve link information: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/short-io/src/lib/actions/get-link-clicks.ts
var import_pieces_common10 = require("@activepieces/pieces-common"), import_pieces_framework10 = require("@activepieces/pieces-framework");
var getLinkClicksAction = (0, import_pieces_framework10.createAction)({
  auth: shortIoAuth,
  name: "get-link-clicks",
  classification: "READ",
  displayName: "Get Link Clicks",
  description: "Retrieve click statistics for specific short links by their IDs.",
  audience: "both",
  aiMetadata: { description: "Retrieves click statistics for one or more specific short links (by their link IDs) on a domain, optionally scoped to a start/end date range. Use to measure performance of particular links rather than a whole domain. Requires the domain and at least one link ID, and if filtering by date both start and end must be supplied; read-only and idempotent.", idempotent: !0 },
  props: {
    domain: __spreadProps(__spreadValues({}, domainIdDropdown), {
      required: !0,
      description: "Select the domain containing the links"
    }),
    ids: import_pieces_framework10.Property.Array({
      displayName: "Link IDs",
      description: "List of link IDs to fetch click statistics for (e.g., lnk_61Mb_0dnRUg3vvtmAPZh3dhQh6).",
      required: !0
    }),
    startDate: import_pieces_framework10.Property.DateTime({
      displayName: "Start Date",
      description: "Start date for click statistics (optional).",
      required: !1
    }),
    endDate: import_pieces_framework10.Property.DateTime({
      displayName: "End Date",
      description: "End date for click statistics (optional).",
      required: !1
    })
  },
  async run({ propsValue, auth }) {
    let { domain: domainString, ids, startDate, endDate } = propsValue;
    if (!domainString)
      throw new Error("Domain is required. Please select a domain.");
    if (!ids || ids.length === 0)
      throw new Error("At least one Link ID is required.");
    if (startDate && !endDate || !startDate && endDate)
      throw new Error("Both Start Date and End Date must be provided if using a date range filter.");
    if (startDate && endDate) {
      let start = new Date(startDate), end = new Date(endDate);
      if (start >= end)
        throw new Error("Start date must be before end date.");
    }
    let domainObject = JSON.parse(domainString), query = {
      ids: ids.join(",")
    };
    startDate && endDate && (query.startDate = new Date(startDate).toISOString().split("T")[0], query.endDate = new Date(endDate).toISOString().split("T")[0]);
    try {
      let response = await shortIoApiCall({
        method: import_pieces_common10.HttpMethod.GET,
        auth,
        url: `https://statistics.short.io/statistics/domain/${domainObject.id}/link_clicks`,
        query
      }), linkCount = ids.length, dateRange = startDate && endDate ? ` for ${query.startDate} to ${query.endDate}` : "";
      return {
        success: !0,
        message: `Retrieved click statistics for ${linkCount} link${linkCount > 1 ? "s" : ""}${dateRange}`,
        data: response
      };
    } catch (error) {
      throw error.message.includes("400") ? new Error(
        "Invalid request parameters. Please check your link IDs and date range."
      ) : error.message.includes("401") || error.message.includes("403") ? new Error(
        "Authentication failed or insufficient permissions. Please check your API key and domain access."
      ) : error.message.includes("404") ? new Error(
        "Domain or links not found. Please verify the domain and link IDs exist."
      ) : error.message.includes("429") ? new Error(
        "Rate limit exceeded. Please wait a moment before trying again."
      ) : new Error(`Failed to retrieve link click statistics: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/short-io/src/lib/actions/list-links.ts
var import_pieces_common11 = require("@activepieces/pieces-common"), import_pieces_framework11 = require("@activepieces/pieces-framework");
var listLinksAction = (0, import_pieces_framework11.createAction)({
  auth: shortIoAuth,
  name: "list-short-links",
  classification: "SEARCH",
  displayName: "List Links",
  description: "Retrieve all links on a domain, with pagination and date-range filters.",
  audience: "both",
  aiMetadata: { description: "Lists short links on a Short.io domain, with optional filters by folder, link ID, exact or before/after creation date, sort order, and a page token for paginating large result sets. Use to enumerate or search a domain's links, or to fetch the next page of a prior result. Requires a domain; read-only and idempotent.", idempotent: !0 },
  props: {
    domain: __spreadProps(__spreadValues({}, domainIdDropdown), {
      required: !0,
      description: "Select the domain to retrieve links from"
    }),
    folderId: folderIdDropdown,
    limit: import_pieces_framework11.Property.Number({
      displayName: "Limit",
      description: "Number of results to return (1-150). Default is 50.",
      required: !1
    }),
    idString: import_pieces_framework11.Property.ShortText({
      displayName: "Link ID Filter",
      description: "Filter by specific link ID (optional).",
      required: !1
    }),
    createdAt: import_pieces_framework11.Property.ShortText({
      displayName: "Exact Creation Time",
      description: "Filter by exact creation time (ISO format or timestamp).",
      required: !1
    }),
    beforeDate: import_pieces_framework11.Property.DateTime({
      displayName: "Before Date",
      description: "Return links created before this date and time.",
      required: !1
    }),
    afterDate: import_pieces_framework11.Property.DateTime({
      displayName: "After Date",
      description: "Return links created after this date and time.",
      required: !1
    }),
    dateSortOrder: import_pieces_framework11.Property.StaticDropdown({
      displayName: "Date Sort Order",
      description: "Order links by creation date.",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Ascending (Oldest First)", value: "asc" },
          { label: "Descending (Newest First)", value: "desc" }
        ]
      }
    }),
    pageToken: import_pieces_framework11.Property.ShortText({
      displayName: "Page Token",
      description: "Token for paginated results (get this from previous response).",
      required: !1
    })
  },
  async run({ propsValue, auth }) {
    let {
      domain: domainString,
      limit,
      idString,
      createdAt,
      beforeDate,
      afterDate,
      dateSortOrder,
      pageToken,
      folderId
    } = propsValue;
    if (!domainString)
      throw new Error("Domain is required. Please select a domain.");
    if (limit && (limit < 1 || limit > 150))
      throw new Error("Limit must be between 1 and 150.");
    let query = {}, domainObject = JSON.parse(domainString);
    query.domain_id = String(domainObject.id), limit && (query.limit = String(limit)), idString && idString.trim() !== "" && (query.idString = String(idString)), createdAt && createdAt.trim() !== "" && (query.createdAt = String(createdAt)), beforeDate && (query.beforeDate = beforeDate), afterDate && (query.afterDate = afterDate), dateSortOrder && (query.dateSortOrder = String(dateSortOrder)), pageToken && pageToken.trim() !== "" && (query.pageToken = String(pageToken)), folderId && (query.folderId = String(folderId));
    try {
      let response = await shortIoApiCall({
        method: import_pieces_common11.HttpMethod.GET,
        auth,
        resourceUri: "/api/links",
        query
      }), linkCount = response.count || (response.links ? response.links.length : 0), hasNextPage = response.nextPageToken ? " (more pages available)" : "";
      return {
        success: !0,
        message: `Retrieved ${linkCount} links successfully${hasNextPage}`,
        data: response
      };
    } catch (error) {
      throw error.message.includes("400") ? new Error(
        "Invalid request parameters. Please check your filter values and try again."
      ) : error.message.includes("401") || error.message.includes("403") ? new Error(
        "Authentication failed or insufficient permissions. Please check your API key and domain access."
      ) : error.message.includes("404") ? new Error(
        "Domain not found. Please verify the domain exists and you have access to it."
      ) : error.message.includes("429") ? new Error(
        "Rate limit exceeded. Please wait a moment before trying again."
      ) : new Error(`Failed to retrieve links: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/short-io/src/lib/actions/update-short-link.ts
var import_pieces_common12 = require("@activepieces/pieces-common"), import_pieces_framework12 = require("@activepieces/pieces-framework");
var updateShortLinkAction = (0, import_pieces_framework12.createAction)({
  auth: shortIoAuth,
  name: "update-short-link",
  classification: "WRITE",
  displayName: "Update Short Link",
  description: "Update an existing short link's original URL, path, title, or other properties using its link ID.",
  audience: "both",
  aiMetadata: { description: "Updates properties of an existing short link identified by its link ID \u2014 destination URL, path/slug, title, folder, UTM tags, expiration, cloaking, A/B split, and analytics integrations. Use to modify a link you already created rather than making a new one. Only the fields you set are changed; idempotent \u2014 re-sending the same values keyed on the link ID yields the same result.", idempotent: !0 },
  props: {
    domain: domainIdDropdown,
    linkId: linkIdDropdown,
    originalURL: import_pieces_framework12.Property.ShortText({
      displayName: "Original URL",
      required: !1
    }),
    path: import_pieces_framework12.Property.ShortText({
      displayName: "Custom Path (Slug)",
      required: !1
    }),
    title: import_pieces_framework12.Property.ShortText({
      displayName: "Title",
      required: !1
    }),
    folderId: folderIdDropdown,
    cloaking: import_pieces_framework12.Property.Checkbox({
      displayName: "Enable Cloaking",
      required: !1
    }),
    password: import_pieces_framework12.Property.ShortText({
      displayName: "Password",
      required: !1
    }),
    redirectType: import_pieces_framework12.Property.StaticDropdown({
      displayName: "Redirect Type",
      description: "The HTTP status code for the redirect. The default is 302 (Found).",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "301 (Moved Permanently)", value: "301" },
          { label: "302 (Found)", value: "302" },
          { label: "303 (See Other)", value: "303" },
          { label: "307 (Temporary Redirect)", value: "307" },
          { label: "308 (Permanent Redirect)", value: "308" }
        ]
      }
    }),
    expiresAt: import_pieces_framework12.Property.DateTime({
      displayName: "Expiration Date",
      description: "The date and time when the link will become inactive.",
      required: !1
    }),
    expiredURL: import_pieces_framework12.Property.ShortText({
      displayName: "Expired Redirect URL",
      required: !1
    }),
    tags: import_pieces_framework12.Property.Array({
      displayName: "Tags",
      description: "Array of tags for the link",
      required: !1
    }),
    utmSource: import_pieces_framework12.Property.ShortText({
      displayName: "UTM Source",
      required: !1
    }),
    utmMedium: import_pieces_framework12.Property.ShortText({
      displayName: "UTM Medium",
      required: !1
    }),
    utmCampaign: import_pieces_framework12.Property.ShortText({
      displayName: "UTM Campaign",
      required: !1
    }),
    utmTerm: import_pieces_framework12.Property.ShortText({
      displayName: "UTM Term",
      required: !1
    }),
    utmContent: import_pieces_framework12.Property.ShortText({
      displayName: "UTM Content",
      required: !1
    }),
    ttl: import_pieces_framework12.Property.Number({
      displayName: "Time to Live (in seconds)",
      description: "\u26A0\uFE0F CAUTION: Link will be PERMANENTLY DELETED after this many seconds. This action cannot be undone. Use with extreme caution.",
      required: !1
    }),
    androidURL: import_pieces_framework12.Property.ShortText({
      displayName: "Android URL",
      required: !1
    }),
    iphoneURL: import_pieces_framework12.Property.ShortText({
      displayName: "iPhone URL",
      required: !1
    }),
    createdAt: import_pieces_framework12.Property.DateTime({
      displayName: "Creation Time",
      description: "Overrides the creation time of the link.",
      required: !1
    }),
    clicksLimit: import_pieces_framework12.Property.Number({
      displayName: "Clicks Limit",
      description: "Disable link after specified number of clicks (minimum: 1)",
      required: !1
    }),
    passwordContact: import_pieces_framework12.Property.Checkbox({
      displayName: "Show Contact for Password Recovery",
      required: !1
    }),
    skipQS: import_pieces_framework12.Property.Checkbox({
      displayName: "Skip Query String Merge",
      required: !1
    }),
    archived: import_pieces_framework12.Property.Checkbox({
      displayName: "Archive Link",
      required: !1
    }),
    splitURL: import_pieces_framework12.Property.ShortText({
      displayName: "Split URL",
      required: !1
    }),
    splitPercent: import_pieces_framework12.Property.Number({
      displayName: "Split Percent",
      description: "Split URL percentage (1-100)",
      required: !1
    }),
    integrationAdroll: import_pieces_framework12.Property.ShortText({
      displayName: "Adroll Integration ID",
      required: !1
    }),
    integrationFB: import_pieces_framework12.Property.ShortText({
      displayName: "Facebook Integration ID",
      required: !1
    }),
    integrationGA: import_pieces_framework12.Property.ShortText({
      displayName: "Google Analytics ID",
      required: !1
    }),
    integrationGTM: import_pieces_framework12.Property.ShortText({
      displayName: "Google Tag Manager ID",
      required: !1
    })
  },
  async run({ propsValue, auth }) {
    let {
      linkId,
      domain: domainString,
      originalURL,
      path,
      title,
      folderId,
      cloaking,
      password,
      redirectType,
      expiresAt,
      expiredURL,
      tags,
      utmSource,
      utmMedium,
      utmCampaign,
      utmTerm,
      utmContent,
      ttl,
      androidURL,
      iphoneURL,
      createdAt,
      clicksLimit,
      passwordContact,
      skipQS,
      archived,
      splitURL,
      splitPercent,
      integrationAdroll,
      integrationFB,
      integrationGA,
      integrationGTM
    } = propsValue;
    if (clicksLimit && clicksLimit < 1)
      throw new Error("Clicks limit must be at least 1");
    if (splitPercent && (splitPercent < 1 || splitPercent > 100))
      throw new Error("Split percent must be between 1 and 100");
    if (redirectType && !["301", "302", "303", "307", "308"].includes(redirectType))
      throw new Error("Invalid redirect type. Must be one of: 301, 302, 303, 307, 308");
    let query = {};
    if (domainString) {
      let domainObject = JSON.parse(domainString);
      query.domain_id = String(domainObject.id);
    }
    let optionalParams = {
      originalURL,
      path,
      title,
      folderId,
      cloaking,
      password,
      redirectType,
      expiresAt,
      expiredURL,
      tags,
      utmSource,
      utmMedium,
      utmCampaign,
      utmTerm,
      utmContent,
      ttl: ttl ? ttl * 1e3 : void 0,
      androidURL,
      iphoneURL,
      createdAt,
      clicksLimit,
      passwordContact,
      skipQS,
      archived,
      splitURL,
      splitPercent,
      integrationAdroll,
      integrationFB,
      integrationGA,
      integrationGTM
    }, body = {};
    for (let [key, value] of Object.entries(optionalParams))
      value != null && value !== "" && (body[key] = value);
    try {
      return {
        success: !0,
        message: "Short link updated successfully",
        data: await shortIoApiCall({
          method: import_pieces_common12.HttpMethod.POST,
          auth,
          resourceUri: `/links/${linkId}`,
          query,
          body
        })
      };
    } catch (error) {
      throw error.message.includes("400") ? new Error(
        "Invalid request parameters. Please check your input values and try again."
      ) : error.message.includes("401") || error.message.includes("403") ? new Error(
        "Authentication failed. Please check your API key and permissions."
      ) : error.message.includes("404") ? new Error(
        "Short link not found. Please check the link ID and try again."
      ) : error.message.includes("429") ? new Error(
        "Rate limit exceeded. Please wait a moment before trying again."
      ) : new Error(`Failed to update short link: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/short-io/src/lib/triggers/new-link-created.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common13 = require("@activepieces/pieces-common");
var LAST_LINK_IDS_KEY = "shortio-last-link-ids", newLinkCreatedTrigger = (0, import_pieces_framework13.createTrigger)({
  auth: shortIoAuth,
  name: "new_link_created",
  classification: "READ",
  displayName: "New Link Created",
  description: "Fires when a new short link is created on a domain. Useful to sync newly created links to other systems.",
  aiMetadata: {
    description: "Fires when a new short link appears on the selected Short.io domain, detected by polling the domain's link list on a configurable interval. Each event represents one newly created link with its details. Use to kick off a workflow whenever someone creates a link on the domain."
  },
  type: import_pieces_framework13.TriggerStrategy.POLLING,
  props: {
    domain: __spreadProps(__spreadValues({}, domainIdDropdown), {
      required: !0,
      description: "Select the domain to monitor for new links"
    }),
    pollingInterval: import_pieces_framework13.Property.StaticDropdown({
      displayName: "Polling Interval",
      description: "How often to check for new links. More frequent polling may hit rate limits.",
      required: !0,
      defaultValue: "5",
      options: {
        disabled: !1,
        options: [
          { label: "Every 1 minute", value: "1" },
          { label: "Every 5 minutes", value: "5" },
          { label: "Every 15 minutes", value: "15" },
          { label: "Every 30 minutes", value: "30" },
          { label: "Every hour", value: "60" }
        ]
      }
    })
  },
  async onEnable(context) {
    let { domain: domainString } = context.propsValue;
    if (!domainString)
      throw new Error("Domain is required. Please select a domain.");
    let domainId = JSON.parse(domainString).id;
    try {
      let currentIds = ((await shortIoApiCall({
        auth: context.auth,
        method: import_pieces_common13.HttpMethod.GET,
        resourceUri: "/api/links",
        query: {
          domain_id: domainId,
          limit: 100,
          dateSortOrder: "desc"
        }
      })).links || []).map((link) => link.idString);
      await context.store.put(LAST_LINK_IDS_KEY, currentIds);
    } catch (error) {
      throw new Error(`Failed to initialize trigger: ${error.message}`);
    }
  },
  async onDisable() {
  },
  async run(context) {
    let { domain: domainString } = context.propsValue;
    if (!domainString)
      throw new Error("Domain is required. Please select a domain.");
    let domainObject = JSON.parse(domainString), domainId = domainObject.id, previousIds = await context.store.get(LAST_LINK_IDS_KEY) || [];
    try {
      let allLinks = (await shortIoApiCall({
        auth: context.auth,
        method: import_pieces_common13.HttpMethod.GET,
        resourceUri: "/api/links",
        query: {
          domain_id: domainId,
          limit: 100,
          dateSortOrder: "desc"
        }
      })).links || [], currentIds = allLinks.map((link) => link.idString);
      return await context.store.put(LAST_LINK_IDS_KEY, currentIds), allLinks.filter((link) => !previousIds.includes(link.idString)).map((link) => ({
        id: link.idString,
        originalURL: link.originalURL,
        shortURL: link.shortURL,
        secureShortURL: link.secureShortURL,
        title: link.title,
        path: link.path,
        createdAt: link.createdAt,
        updatedAt: link.updatedAt,
        tags: link.tags,
        redirectType: link.redirectType,
        domainId: link.DomainId || link.domainId,
        folderId: link.FolderId,
        ownerId: link.OwnerId,
        hasPassword: link.hasPassword,
        cloaking: link.cloaking,
        password: link.password,
        expiresAt: link.expiresAt,
        expiredURL: link.expiredURL,
        clicksLimit: link.clicksLimit,
        archived: link.archived,
        utmSource: link.utmSource,
        utmMedium: link.utmMedium,
        utmCampaign: link.utmCampaign,
        utmTerm: link.utmTerm,
        utmContent: link.utmContent,
        androidURL: link.androidURL,
        iphoneURL: link.iphoneURL,
        triggerInfo: {
          source: "short.io",
          type: "new_link_created",
          detectedAt: (/* @__PURE__ */ new Date()).toISOString(),
          domain: domainObject.hostname
        },
        raw: link
      }));
    } catch (error) {
      throw error.message.includes("403") ? new Error(`Access denied to domain. Please check your API key permissions for domain: ${domainObject.hostname}`) : error.message.includes("404") ? new Error(`Domain not found: ${domainObject.hostname}. Please verify the domain exists.`) : error.message.includes("429") ? new Error("Rate limit exceeded. Consider increasing the polling interval.") : new Error(`Failed to check for new links: ${error.message}`);
    }
  },
  async test(context) {
    var _a;
    let { domain: domainString } = context.propsValue;
    if (!domainString)
      throw new Error("Domain is required. Please select a domain.");
    let domainObject = JSON.parse(domainString), domainId = domainObject.id;
    try {
      let link = (_a = (await shortIoApiCall({
        auth: context.auth,
        method: import_pieces_common13.HttpMethod.GET,
        resourceUri: "/api/links",
        query: {
          domain_id: domainId,
          limit: 1,
          dateSortOrder: "desc"
        }
      })).links) == null ? void 0 : _a[0];
      return link ? [{
        id: link.idString,
        originalURL: link.originalURL,
        shortURL: link.shortURL,
        secureShortURL: link.secureShortURL,
        title: link.title,
        path: link.path,
        createdAt: link.createdAt,
        updatedAt: link.updatedAt,
        tags: link.tags,
        redirectType: link.redirectType,
        domainId: link.DomainId || link.domainId,
        folderId: link.FolderId,
        ownerId: link.OwnerId,
        hasPassword: link.hasPassword,
        cloaking: link.cloaking,
        password: link.password,
        expiresAt: link.expiresAt,
        expiredURL: link.expiredURL,
        clicksLimit: link.clicksLimit,
        archived: link.archived,
        utmSource: link.utmSource,
        utmMedium: link.utmMedium,
        utmCampaign: link.utmCampaign,
        utmTerm: link.utmTerm,
        utmContent: link.utmContent,
        androidURL: link.androidURL,
        iphoneURL: link.iphoneURL,
        triggerInfo: {
          source: "short.io",
          type: "new_link_created",
          detectedAt: (/* @__PURE__ */ new Date()).toISOString(),
          domain: domainObject.hostname
        },
        raw: link
      }] : [];
    } catch (error) {
      throw error.message.includes("403") ? new Error(`Access denied to domain. Please check your API key permissions for domain: ${domainObject.hostname}`) : error.message.includes("404") ? new Error(`Domain not found: ${domainObject.hostname}. Please verify the domain exists.`) : new Error(`Failed to test trigger: ${error.message}`);
    }
  },
  sampleData: {
    id: "lnk_61Mb_0dnRUg3vvtmAPZh3dhQh6",
    originalURL: "https://example.com/page",
    shortURL: "https://yourdomain.short.io/abc123",
    secureShortURL: "https://yourdomain.short.io/abc123",
    title: "Example Page Title",
    path: "abc123",
    createdAt: "2025-01-15T12:00:00.000Z",
    updatedAt: "2025-01-15T12:00:00.000Z",
    tags: ["marketing", "campaign"],
    redirectType: 302,
    domainId: 123456,
    folderId: null,
    ownerId: 789,
    hasPassword: !1,
    cloaking: !1,
    password: null,
    expiresAt: null,
    expiredURL: null,
    clicksLimit: null,
    archived: !1,
    utmSource: "newsletter",
    utmMedium: "email",
    utmCampaign: "january_promo",
    utmTerm: null,
    utmContent: null,
    androidURL: null,
    iphoneURL: null,
    triggerInfo: {
      source: "short.io",
      type: "new_link_created",
      detectedAt: "2025-01-15T12:01:00.000Z",
      domain: "yourdomain.short.io"
    },
    raw: {}
  }
});

// ../ap/packages/pieces/community/short-io/src/index.ts
var shortIo = (0, import_pieces_framework14.createPiece)({
  displayName: "Short.io",
  auth: shortIoAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/short-io.png",
  authors: ["aryel780"],
  actions: [
    createCountryTargetingRuleAction,
    createShortLinkAction,
    deleteShortLinkAction,
    expireShortLinkAction,
    domainStatisticsAction,
    getLinkByPathAction,
    getLinkClicksAction,
    listLinksAction,
    updateShortLinkAction,
    (0, import_pieces_common14.createCustomApiCallAction)({
      auth: shortIoAuth,
      baseUrl: () => "https://api.short.io",
      authMapping: async (auth) => {
        let { apiKey } = auth.props;
        return {
          Authorization: apiKey
        };
      }
    })
  ],
  triggers: [newLinkCreatedTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  shortIo
});
