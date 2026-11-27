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

// ../ap/packages/pieces/community/bitly/src/index.ts
var index_exports = {};
__export(index_exports, {
  bitly: () => bitly
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common10 = require("@activepieces/pieces-common"), import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_framework10 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/bitly/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/bitly/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
async function bitlyApiCall({
  method,
  resourceUri,
  query,
  body,
  auth
}) {
  var _a, _b;
  let { accessToken } = auth;
  if (!accessToken)
    throw new Error("Bitly Access Token is required for authentication");
  let queryParams = {};
  if (query)
    for (let [key, value] of Object.entries(query))
      value != null && (queryParams[key] = String(value));
  let request = {
    method,
    url: `https://api-ssl.bitly.com/v4${resourceUri}`,
    headers: {
      Authorization: `Bearer ${accessToken}`,
      "Content-Type": "application/json"
    },
    queryParams,
    body
  };
  try {
    return (await import_pieces_common.httpClient.sendRequest(request)).body;
  } catch (error) {
    let statusCode = (_a = error.response) == null ? void 0 : _a.status, errorData = (_b = error.response) == null ? void 0 : _b.data, errorMessage = (errorData == null ? void 0 : errorData.description) || (errorData == null ? void 0 : errorData.message) || "Unknown error occurred";
    switch (statusCode) {
      case 400:
        throw new Error(
          `Bad Request: ${errorMessage}. Please check your input parameters.`
        );
      case 401:
        throw new Error(
          "Authentication Failed: Invalid Access Token. Please verify your Bitly credentials in the connection settings."
        );
      case 402:
        throw new Error(
          `Payment Required: ${errorMessage}. Your account has been suspended or you have reached a usage limit.`
        );
      case 403:
        throw new Error(
          `Access Forbidden: ${errorMessage}. You do not have permission to access this resource.`
        );
      case 404:
        throw new Error(
          `Resource Not Found: ${errorMessage}. The requested resource does not exist.`
        );
      case 417:
        throw new Error(
          `Expectation Failed: ${errorMessage}. You must agree to the latest terms of service.`
        );
      case 422:
        throw new Error(
          `Unprocessable Entity: ${errorMessage}. The request was well-formed but was unable to be followed due to semantic errors.`
        );
      case 429:
        throw new Error(
          `Rate Limit Exceeded: ${errorMessage}. Too many requests. Please wait before trying again.`
        );
      case 500:
        throw new Error(
          "Internal Server Error: Bitly is experiencing technical difficulties. Please try again later."
        );
      case 503:
        throw new Error(
          "Service Unavailable: Bitly service is temporarily unavailable. Please try again in a few minutes."
        );
      default:
        throw new Error(
          `Bitly API Error (${statusCode || "Unknown"}): ${errorMessage}`
        );
    }
  }
}

// ../ap/packages/pieces/community/bitly/src/lib/common/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), bitlyAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: `
  To get your Access Token:
  1. Log in to your Bitly account.
  2. Click your profile icon in the top right corner.
  3. Go to **Profile Settings**.
  4. Navigate to the **Developer settings** section.
  5. Click on **API**.
  6. Click the **Generate token** button and enter your password to get your access token.
  `,
  props: {
    accessToken: import_pieces_framework.PieceAuth.SecretText({
      displayName: "Access Token",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    try {
      return await bitlyApiCall({
        method: import_pieces_common2.HttpMethod.GET,
        auth,
        resourceUri: "/user"
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid Access Token"
      };
    }
  },
  required: !0
});

// ../ap/packages/pieces/community/bitly/src/lib/triggers/new-bitlink-created.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/bitly/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var getBitlyGroups = async (auth) => (await bitlyApiCall({
  auth,
  method: import_pieces_common3.HttpMethod.GET,
  resourceUri: "/groups"
})).groups || [], groupGuid = import_pieces_framework2.Property.Dropdown({
  displayName: "Group",
  description: "The group where the item will be managed.",
  required: !0,
  refreshers: [],
  auth: bitlyAuth,
  options: async ({ auth }) => {
    if (!auth)
      return { disabled: !0, options: [], placeholder: "Please connect your Bitly account first." };
    let { accessToken } = auth.props;
    if (!accessToken)
      return { disabled: !0, options: [], placeholder: "Please connect your Bitly account first." };
    try {
      let groups = await getBitlyGroups({ accessToken });
      return groups.length === 0 ? { disabled: !0, options: [], placeholder: "No groups found in your account." } : {
        disabled: !1,
        options: groups.map((group) => ({
          label: group.name,
          value: group.guid
        }))
      };
    } catch (e) {
      return { disabled: !0, options: [], placeholder: `Error fetching groups: ${e.message}` };
    }
  }
}), domain = import_pieces_framework2.Property.Dropdown({
  auth: bitlyAuth,
  displayName: "Domain",
  description: "Domain to use for the Bitlink.",
  required: !1,
  refreshers: ["group_guid"],
  options: async ({ auth, group_guid }) => {
    var _a;
    if (!auth)
      return { disabled: !0, options: [], placeholder: "Please connect your Bitly account first." };
    let { accessToken } = auth.props;
    if (!accessToken || !group_guid)
      return { disabled: !0, options: [], placeholder: "Please select a group first." };
    try {
      let selectedGroup = (await getBitlyGroups({ accessToken })).find((g) => g.guid === group_guid);
      return {
        disabled: !1,
        options: ["bit.ly", ...((_a = selectedGroup == null ? void 0 : selectedGroup.bsds) == null ? void 0 : _a.map((bsd) => bsd.domain)) || []].map((d) => ({
          label: d,
          value: d
        }))
      };
    } catch (e) {
      return { disabled: !0, options: [], placeholder: `Error fetching domains: ${e.message}` };
    }
  }
}), bitlinkDropdown = import_pieces_framework2.Property.Dropdown({
  displayName: "Bitlink",
  description: "Select the Bitlink to modify.",
  required: !0,
  refreshers: ["group_guid"],
  auth: bitlyAuth,
  options: async ({ auth, group_guid }) => {
    if (!auth)
      return { disabled: !0, options: [], placeholder: "Please connect your Bitly account first." };
    let { accessToken } = auth.props;
    if (!accessToken) return { disabled: !0, options: [], placeholder: "Please connect your Bitly account first." };
    if (!group_guid) return { disabled: !0, options: [], placeholder: "Please select a group first." };
    try {
      return {
        disabled: !1,
        options: (await bitlyApiCall({
          auth: { accessToken },
          method: import_pieces_common3.HttpMethod.GET,
          resourceUri: `/groups/${group_guid}/bitlinks`
        })).links.map((link) => ({
          label: `${link.title || "No Title"} (${link.id})`,
          value: link.id
        }))
      };
    } catch (e) {
      return { disabled: !0, options: [], placeholder: `Error fetching Bitlinks: ${e.message}` };
    }
  }
});

// ../ap/packages/pieces/community/bitly/src/lib/triggers/new-bitlink-created.ts
var LAST_BITLINK_IDS_KEY = "bitly-last-bitlink-ids", newBitlinkCreatedTrigger = (0, import_pieces_framework3.createTrigger)({
  auth: bitlyAuth,
  name: "new_bitlink_created",
  classification: "READ",
  displayName: "New Bitlink Created",
  description: "Fires when a new Bitlink is created.",
  aiMetadata: {
    description: "Fires when a new Bitlink appears in the selected group, detected by polling. Represents a newly created short link, optionally narrowed to those whose title or tags match a filter and optionally including archived links."
  },
  type: import_pieces_framework3.TriggerStrategy.POLLING,
  props: {
    pollingInterval: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Polling Interval",
      description: "How frequently to check for new Bitlinks.",
      required: !1,
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
    }),
    group_guid: groupGuid,
    titleFilter: import_pieces_framework3.Property.ShortText({
      displayName: "Title Filter (Optional)",
      description: "Only trigger for Bitlinks containing this text in their title.",
      required: !1
    }),
    tagFilter: import_pieces_framework3.Property.ShortText({
      displayName: "Tag Filter (Optional)",
      description: "Only trigger for Bitlinks containing this tag.",
      required: !1
    }),
    includeArchived: import_pieces_framework3.Property.Checkbox({
      displayName: "Include Archived Bitlinks",
      description: "Include archived Bitlinks in monitoring.",
      required: !1,
      defaultValue: !1
    })
  },
  async onEnable(context) {
    var _a, _b;
    let { group_guid } = context.propsValue, { accessToken } = context.auth.props;
    try {
      let linkIds = (await bitlyApiCall({
        auth: { accessToken },
        method: import_pieces_common4.HttpMethod.GET,
        resourceUri: `/groups/${group_guid}/bitlinks`,
        query: {
          size: 50,
          archived: context.propsValue.includeArchived ? "both" : "off"
        }
      })).links.map((link) => link.id);
      await context.store.put(LAST_BITLINK_IDS_KEY, linkIds), console.log(`Bitly New Bitlink trigger initialized with ${linkIds.length} existing links`);
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 401 ? new Error(
        "Authentication failed: Please check your access token. Make sure your token has permission to access Bitlinks."
      ) : ((_b = error.response) == null ? void 0 : _b.status) === 403 ? new Error(
        "Access denied: You do not have permission to list Bitlinks. Please check your Bitly account permissions."
      ) : new Error(
        `Failed to initialize Bitlink monitoring: ${error.message || "Unknown error occurred"}. Please check your Bitly connection.`
      );
    }
  },
  async onDisable() {
    console.log("Bitly New Bitlink trigger disabled and cleaned up");
  },
  async run(context) {
    var _a, _b, _c;
    let { group_guid, titleFilter, tagFilter, includeArchived } = context.propsValue, { accessToken } = context.auth.props;
    try {
      let previousLinkIds = await context.store.get(LAST_BITLINK_IDS_KEY) || [], allLinks = (await bitlyApiCall({
        auth: { accessToken },
        method: import_pieces_common4.HttpMethod.GET,
        resourceUri: `/groups/${group_guid}/bitlinks`,
        query: {
          size: 50,
          archived: includeArchived ? "both" : "off"
        }
      })).links || [], currentLinkIds = allLinks.map((l) => l.id);
      await context.store.put(LAST_BITLINK_IDS_KEY, currentLinkIds);
      let newLinks = allLinks.filter((link) => !previousLinkIds.includes(link.id));
      if (titleFilter && titleFilter.trim()) {
        let filterText = titleFilter.trim().toLowerCase();
        newLinks = newLinks.filter(
          (link) => link.title && link.title.toLowerCase().includes(filterText)
        );
      }
      if (tagFilter && tagFilter.trim()) {
        let filterTag = tagFilter.trim().toLowerCase();
        newLinks = newLinks.filter(
          (link) => link.tags && Array.isArray(link.tags) && link.tags.some((tag) => tag.toLowerCase().includes(filterTag))
        );
      }
      return newLinks.map((link) => {
        var _a2;
        return {
          id: link.id,
          link: link.link,
          longUrl: link.long_url,
          title: link.title,
          tags: link.tags || [],
          isArchived: link.archived,
          createdAt: link.created_at,
          modifiedAt: link.modified_at,
          customBitlinks: link.custom_bitlinks || [],
          references: {
            group: (_a2 = link.references) == null ? void 0 : _a2.group
          },
          rawLinkData: link,
          triggerInfo: {
            detectedAt: (/* @__PURE__ */ new Date()).toISOString(),
            source: "bitly",
            type: "new_bitlink"
          }
        };
      });
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 401 ? new Error(
        "Authentication failed: Your access token may have expired. Please check your Bitly authentication."
      ) : ((_b = error.response) == null ? void 0 : _b.status) === 429 ? new Error(
        "Rate limit exceeded: Bitly API rate limit reached. Consider increasing your polling interval."
      ) : ((_c = error.response) == null ? void 0 : _c.status) === 403 ? new Error(
        "Access denied: You do not have permission to list Bitlinks. Please check your account permissions."
      ) : new Error(
        `Failed to check for new Bitlinks: ${error.message || "Unknown error occurred"}. The trigger will retry on the next polling interval.`
      );
    }
  },
  async test(context) {
    var _a;
    let { group_guid, includeArchived } = context.propsValue, { accessToken } = context.auth.props;
    try {
      let links = (await bitlyApiCall({
        auth: { accessToken },
        method: import_pieces_common4.HttpMethod.GET,
        resourceUri: `/groups/${group_guid}/bitlinks`,
        query: {
          size: 1,
          archived: includeArchived ? "both" : "off"
        }
      })).links || [];
      if (links.length > 0) {
        let testLink = links[0];
        return [
          {
            id: testLink.id,
            link: testLink.link,
            longUrl: testLink.long_url,
            title: testLink.title,
            tags: testLink.tags || [],
            isArchived: testLink.archived,
            createdAt: testLink.created_at,
            modifiedAt: testLink.modified_at,
            customBitlinks: testLink.custom_bitlinks || [],
            references: {
              group: (_a = testLink.references) == null ? void 0 : _a.group
            },
            rawLinkData: testLink,
            triggerInfo: {
              detectedAt: (/* @__PURE__ */ new Date()).toISOString(),
              source: "bitly",
              type: "new_bitlink"
            }
          }
        ];
      } else
        return [
          {
            id: "bit.ly/test123",
            link: "https://bit.ly/test123",
            longUrl: "https://example.com/very-long-url",
            title: "Sample Bitlink",
            tags: ["sample", "test"],
            isArchived: !1,
            createdAt: "2025-01-15T10:00:00+0000",
            modifiedAt: "2025-01-15T10:00:00+0000",
            customBitlinks: [],
            references: {
              group: "Ba1bc23dE4F"
            },
            rawLinkData: {
              id: "bit.ly/test123",
              link: "https://bit.ly/test123",
              long_url: "https://example.com/very-long-url",
              title: "Sample Bitlink",
              tags: ["sample", "test"],
              archived: !1,
              created_at: "2025-01-15T10:00:00+0000",
              modified_at: "2025-01-15T10:00:00+0000",
              custom_bitlinks: [],
              references: {
                group: "Ba1bc23dE4F"
              }
            },
            triggerInfo: {
              detectedAt: (/* @__PURE__ */ new Date()).toISOString(),
              source: "bitly",
              type: "new_bitlink"
            }
          }
        ];
    } catch (error) {
      return [
        {
          id: "bit.ly/test123",
          link: "https://bit.ly/test123",
          longUrl: "https://example.com/test-url",
          title: "Test Bitlink",
          tags: ["test"],
          isArchived: !1,
          createdAt: "2025-01-15T10:00:00+0000",
          modifiedAt: "2025-01-15T10:00:00+0000",
          customBitlinks: [],
          references: {
            group: "Ba1bc23dE4F"
          },
          rawLinkData: {
            id: "bit.ly/test123",
            link: "https://bit.ly/test123",
            long_url: "https://example.com/test-url",
            title: "Test Bitlink",
            tags: ["test"],
            archived: !1,
            created_at: "2025-01-15T10:00:00+0000",
            modified_at: "2025-01-15T10:00:00+0000",
            custom_bitlinks: [],
            references: {
              group: "Ba1bc23dE4F"
            }
          },
          triggerInfo: {
            detectedAt: (/* @__PURE__ */ new Date()).toISOString(),
            source: "bitly",
            type: "new_bitlink"
          }
        }
      ];
    }
  },
  sampleData: {
    id: "bit.ly/3XYZ123",
    link: "https://bit.ly/3XYZ123",
    longUrl: "https://example.com/marketing-campaign-landing-page",
    title: "Marketing Campaign Landing Page",
    tags: ["marketing", "campaign", "2025"],
    isArchived: !1,
    createdAt: "2025-01-15T09:30:00+0000",
    modifiedAt: "2025-01-15T09:30:00+0000",
    customBitlinks: [],
    references: {
      group: "Ba1bc23dE4F"
    },
    rawLinkData: {
      id: "bit.ly/3XYZ123",
      link: "https://bit.ly/3XYZ123",
      long_url: "https://example.com/marketing-campaign-landing-page",
      title: "Marketing Campaign Landing Page",
      tags: ["marketing", "campaign", "2025"],
      archived: !1,
      created_at: "2025-01-15T09:30:00+0000",
      modified_at: "2025-01-15T09:30:00+0000",
      custom_bitlinks: [],
      references: {
        group: "Ba1bc23dE4F"
      }
    },
    triggerInfo: {
      detectedAt: "2025-01-15T09:30:00.000Z",
      source: "bitly",
      type: "new_bitlink"
    }
  }
});

// ../ap/packages/pieces/community/bitly/src/lib/actions/archive-bitlink.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var archiveBitlinkAction = (0, import_pieces_framework4.createAction)({
  auth: bitlyAuth,
  name: "archive_bitlink",
  classification: "DESTRUCTIVE",
  displayName: "Archive Bitlink",
  description: "Archive a Bitlink to stop redirects.",
  audience: "both",
  aiMetadata: { description: "Archives an existing Bitlink so it stops redirecting, identified by its Bitlink ID (e.g. bit.ly/xyz123). Use to retire or disable a short link. Idempotent: re-archiving an already-archived link leaves it archived with no additional effect.", idempotent: !0 },
  props: {
    group_guid: groupGuid,
    bitlink: bitlinkDropdown
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h;
    let { bitlink } = context.propsValue;
    try {
      let body = {
        archived: !0
      };
      return await bitlyApiCall({
        method: import_pieces_common5.HttpMethod.PATCH,
        auth: context.auth.props,
        resourceUri: `/bitlinks/${bitlink}`,
        body
      });
    } catch (error) {
      let errorMessage = ((_b = (_a = error.response) == null ? void 0 : _a.data) == null ? void 0 : _b.description) || ((_d = (_c = error.response) == null ? void 0 : _c.data) == null ? void 0 : _d.message) || error.message;
      throw ((_e = error.response) == null ? void 0 : _e.status) === 429 ? new Error(
        "Rate limit exceeded. Please wait before trying again."
      ) : ((_f = error.response) == null ? void 0 : _f.status) === 404 ? new Error(
        `Bitlink not found: ${errorMessage}. Please verify the link (e.g., 'bit.ly/xyz123') is correct.`
      ) : ((_g = error.response) == null ? void 0 : _g.status) === 401 || ((_h = error.response) == null ? void 0 : _h.status) === 403 ? new Error(
        `Authentication failed or forbidden: ${errorMessage}. Please check your Access Token and permissions.`
      ) : new Error(
        `Failed to archive Bitlink: ${errorMessage || "Unknown error occurred"}`
      );
    }
  }
});

// ../ap/packages/pieces/community/bitly/src/lib/actions/create-bitlink.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");
var createBitlinkAction = (0, import_pieces_framework5.createAction)({
  auth: bitlyAuth,
  name: "create_bitlink",
  classification: "WRITE",
  displayName: "Create Bitlink",
  description: "Shorten a long URL with optional customization.",
  audience: "both",
  aiMetadata: { description: "Shortens a long URL into a Bitlink, optionally setting title, tags, custom branded domain, and mobile app deeplinks. Use to create a trackable short link from a destination URL (must start with http:// or https://). Not idempotent: each call creates a link, though Bitly may return an existing Bitlink for the same URL unless Force New Link is enabled.", idempotent: !1 },
  props: {
    long_url: import_pieces_framework5.Property.ShortText({
      displayName: "Long URL",
      description: "The URL to shorten (must include http:// or https://).",
      required: !0
    }),
    group_guid: groupGuid,
    domain: __spreadProps(__spreadValues({}, domain), {
      defaultValue: "bit.ly"
    }),
    title: import_pieces_framework5.Property.ShortText({
      displayName: "Title",
      description: "Custom title for the Bitlink.",
      required: !1
    }),
    tags: import_pieces_framework5.Property.Array({
      displayName: "Tags",
      description: "Tags to apply to the Bitlink.",
      required: !1
    }),
    force_new_link: import_pieces_framework5.Property.Checkbox({
      displayName: "Force New Link",
      description: "Create new link even if one exists for this URL.",
      required: !1,
      defaultValue: !1
    }),
    // Mobile App Deeplink Configuration
    app_id: import_pieces_framework5.Property.ShortText({
      displayName: "Mobile App ID",
      description: "Mobile app identifier (e.g., com.yourapp.name).",
      required: !1
    }),
    app_uri_path: import_pieces_framework5.Property.ShortText({
      displayName: "App URI Path",
      description: "Path within the mobile app (e.g., /product/123).",
      required: !1
    }),
    install_url: import_pieces_framework5.Property.LongText({
      displayName: "App Install URL",
      description: "URL where users can install the mobile app.",
      required: !1
    }),
    install_type: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Install Type",
      description: "How to handle app installation.",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "No Install", value: "no_install" },
          { label: "Auto Install", value: "auto_install" },
          { label: "Promote Install", value: "promote_install" }
        ]
      }
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h;
    let {
      long_url,
      group_guid,
      domain: domain2,
      title,
      tags,
      force_new_link,
      app_id,
      app_uri_path,
      install_url,
      install_type
    } = context.propsValue;
    try {
      if (!long_url.startsWith("http://") && !long_url.startsWith("https://"))
        throw new Error(
          "Invalid Long URL. It must start with 'http://' or 'https://'."
        );
      let body = { long_url };
      return group_guid && (body.group_guid = group_guid), domain2 && (body.domain = domain2), title && (body.title = title), tags && tags.length > 0 && (body.tags = tags), force_new_link && (body.force_new_link = force_new_link), app_id && app_uri_path && install_url && install_type && (body.deeplinks = [
        {
          app_id,
          app_uri_path,
          install_url,
          install_type
        }
      ]), await bitlyApiCall({
        method: import_pieces_common6.HttpMethod.POST,
        auth: context.auth.props,
        resourceUri: "/bitlinks",
        body
      });
    } catch (error) {
      let errorMessage = ((_b = (_a = error.response) == null ? void 0 : _a.data) == null ? void 0 : _b.description) || ((_d = (_c = error.response) == null ? void 0 : _c.data) == null ? void 0 : _d.message) || error.message;
      throw ((_e = error.response) == null ? void 0 : _e.status) === 429 ? new Error(
        "Rate limit exceeded. Please wait before trying again."
      ) : ((_f = error.response) == null ? void 0 : _f.status) === 422 ? new Error(
        `Unprocessable Entity: ${errorMessage}. Please check the format of your Long URL or other inputs.`
      ) : ((_g = error.response) == null ? void 0 : _g.status) === 401 || ((_h = error.response) == null ? void 0 : _h.status) === 403 ? new Error(
        `Authentication failed or forbidden: ${errorMessage}. Please check your Access Token and permissions.`
      ) : new Error(
        `Failed to create Bitlink: ${errorMessage || "Unknown error occurred"}`
      );
    }
  }
});

// ../ap/packages/pieces/community/bitly/src/lib/actions/create-qr-code.ts
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var createQrCodeAction = (0, import_pieces_framework6.createAction)({
  auth: bitlyAuth,
  name: "create_qr_code",
  classification: "WRITE",
  displayName: "Create QR Code",
  description: "Generate a customized QR code for a Bitlink.",
  audience: "both",
  aiMetadata: { description: "Creates a styled QR code that points either to a raw long URL or to an existing Bitlink (selected via the destination type), with extensive optional customization of colors, dot patterns, corners, gradients, frames, branding, and error correction. Use to generate a scannable QR for a destination. Not idempotent: each call creates a new QR code.", idempotent: !1 },
  props: {
    group_guid: groupGuid,
    destination_type: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Destination Type",
      required: !0,
      defaultValue: "long_url",
      options: {
        options: [
          { label: "Long URL", value: "long_url" },
          { label: "Existing Bitlink", value: "bitlink_id" }
        ]
      }
    }),
    destination: import_pieces_framework6.Property.DynamicProperties({
      auth: bitlyAuth,
      displayName: "Destination",
      required: !0,
      refreshers: ["destination_type"],
      props: async (propsValue) => {
        let destination_type = propsValue.destination_type, props = {};
        return destination_type === "long_url" ? props.long_url = import_pieces_framework6.Property.ShortText({
          displayName: "Long URL",
          required: !0
        }) : destination_type === "bitlink_id" && (props.bitlink_id = import_pieces_framework6.Property.ShortText({
          displayName: "Bitlink (e.g., bit.ly/xyz)",
          required: !0
        })), props;
      }
    }),
    title: import_pieces_framework6.Property.ShortText({
      displayName: "Title",
      description: "Internal title for the QR Code.",
      required: !1
    }),
    archived: import_pieces_framework6.Property.Checkbox({
      displayName: "Archive on Create",
      description: "Archive the QR code upon creation.",
      required: !1
    }),
    background_color: import_pieces_framework6.Property.ShortText({
      displayName: "Style: Background Color",
      description: "Hex code (e.g., #FFFFFF)",
      required: !1
    }),
    dot_pattern_color: import_pieces_framework6.Property.ShortText({
      displayName: "Style: Dot Pattern Color",
      description: "Hex code (e.g., #000000)",
      required: !1
    }),
    dot_pattern_type: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Style: Dot Pattern Type",
      required: !1,
      options: {
        options: [
          { label: "Standard", value: "standard" },
          { label: "Circle", value: "circle" },
          { label: "Block", value: "block" },
          { label: "Blob", value: "blob" },
          { label: "Rounded", value: "rounded" },
          { label: "Vertical", value: "vertical" },
          { label: "Horizontal", value: "horizontal" },
          { label: "Triangle", value: "triangle" },
          { label: "Heart", value: "heart" },
          { label: "Star", value: "star" },
          { label: "Diamond", value: "diamond" }
        ]
      }
    }),
    corner_1_shape: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Corner 1 (Top-Left): Shape",
      required: !1,
      options: {
        options: [
          { label: "Standard", value: "standard" },
          { label: "Slightly Round", value: "slightly_round" },
          { label: "Rounded", value: "rounded" },
          { label: "Extra Round", value: "extra_round" },
          { label: "Leaf", value: "leaf" },
          { label: "Leaf Inner", value: "leaf_inner" },
          { label: "Leaf Outer", value: "leaf_outer" },
          { label: "Target", value: "target" },
          { label: "Concave", value: "concave" }
        ]
      }
    }),
    corner_1_inner_color: import_pieces_framework6.Property.ShortText({
      displayName: "Corner 1 (Top-Left): Inner Color",
      required: !1
    }),
    corner_1_outer_color: import_pieces_framework6.Property.ShortText({
      displayName: "Corner 1 (Top-Left): Outer Color",
      required: !1
    }),
    corner_2_shape: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Corner 2 (Top-Right): Shape",
      required: !1,
      options: {
        options: [
          { label: "Standard", value: "standard" },
          { label: "Slightly Round", value: "slightly_round" },
          { label: "Rounded", value: "rounded" },
          { label: "Extra Round", value: "extra_round" },
          { label: "Leaf", value: "leaf" },
          { label: "Leaf Inner", value: "leaf_inner" },
          { label: "Leaf Outer", value: "leaf_outer" },
          { label: "Target", value: "target" },
          { label: "Concave", value: "concave" }
        ]
      }
    }),
    corner_2_inner_color: import_pieces_framework6.Property.ShortText({
      displayName: "Corner 2 (Top-Right): Inner Color",
      required: !1
    }),
    corner_2_outer_color: import_pieces_framework6.Property.ShortText({
      displayName: "Corner 2 (Top-Right): Outer Color",
      required: !1
    }),
    corner_3_shape: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Corner 3 (Bottom-Right): Shape",
      required: !1,
      options: {
        options: [
          { label: "Standard", value: "standard" },
          { label: "Slightly Round", value: "slightly_round" },
          { label: "Rounded", value: "rounded" },
          { label: "Extra Round", value: "extra_round" },
          { label: "Leaf", value: "leaf" },
          { label: "Leaf Inner", value: "leaf_inner" },
          { label: "Leaf Outer", value: "leaf_outer" },
          { label: "Target", value: "target" },
          { label: "Concave", value: "concave" }
        ]
      }
    }),
    corner_3_inner_color: import_pieces_framework6.Property.ShortText({
      displayName: "Corner 3 (Bottom-Right): Inner Color",
      required: !1
    }),
    corner_3_outer_color: import_pieces_framework6.Property.ShortText({
      displayName: "Corner 3 (Bottom-Right): Outer Color",
      required: !1
    }),
    gradient_style: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Gradient: Style",
      required: !1,
      options: {
        options: [
          { label: "No Gradient", value: "no_gradient" },
          { label: "Linear", value: "linear" },
          { label: "Radial", value: "radial" }
        ]
      }
    }),
    gradient_color_1: import_pieces_framework6.Property.ShortText({
      displayName: "Gradient: Color 1",
      description: "First gradient color (hex code)",
      required: !1
    }),
    gradient_color_2: import_pieces_framework6.Property.ShortText({
      displayName: "Gradient: Color 2",
      description: "Second gradient color (hex code)",
      required: !1
    }),
    gradient_angle: import_pieces_framework6.Property.Number({
      displayName: "Gradient: Angle (for Linear)",
      required: !1
    }),
    gradient_exclude_corners: import_pieces_framework6.Property.Checkbox({
      displayName: "Gradient: Exclude Corners",
      required: !1
    }),
    frame_id: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Frame: Type",
      required: !1,
      options: {
        options: [
          { label: "None", value: "none" },
          { label: "Border Only", value: "border_only" },
          { label: "Text Bottom", value: "text_bottom" },
          { label: "Tooltip Bottom", value: "tooltip_bottom" },
          { label: "Arrow", value: "arrow" },
          { label: "Text Top", value: "text_top" },
          { label: "Text Bottom In Frame", value: "text_bottom_in_frame" },
          { label: "Script", value: "script" },
          { label: "Text Top and Bottom", value: "text_top_and_bottom" },
          { label: "URL", value: "url" },
          { label: "Instagram", value: "instagram" }
        ]
      }
    }),
    frame_primary_color: import_pieces_framework6.Property.ShortText({
      displayName: "Frame: Primary Color",
      required: !1
    }),
    frame_secondary_color: import_pieces_framework6.Property.ShortText({
      displayName: "Frame: Secondary Color",
      required: !1
    }),
    frame_background_color: import_pieces_framework6.Property.ShortText({
      displayName: "Frame: Background Color",
      required: !1
    }),
    frame_text: import_pieces_framework6.Property.ShortText({
      displayName: "Frame: Text",
      description: "Primary text for frames that support it.",
      required: !1
    }),
    logo_image_guid: import_pieces_framework6.Property.ShortText({
      displayName: "Branding: Logo Image GUID",
      description: "A GUID for a logo image previously uploaded to Bitly.",
      required: !1
    }),
    bitly_brand: import_pieces_framework6.Property.Checkbox({
      displayName: "Branding: Show Bitly Logo",
      description: "Show the Bitly logo in the bottom right corner.",
      required: !1,
      defaultValue: !0
    }),
    error_correction: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Specs: Error Correction",
      required: !1,
      options: {
        options: [
          { label: "Low (1)", value: 1 },
          { label: "Medium (2)", value: 2 },
          { label: "Quartile (3)", value: 3 },
          { label: "High (4)", value: 4 }
        ]
      }
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h;
    let props = context.propsValue;
    try {
      let body = {
        group_guid: props.group_guid,
        destination: __spreadValues({}, props.destination)
      };
      props.title && (body.title = props.title), props.archived && (body.archived = props.archived);
      let customizations = {};
      props.background_color && (customizations.background_color = props.background_color), props.dot_pattern_color && (customizations.dot_pattern_color = props.dot_pattern_color), props.dot_pattern_type && (customizations.dot_pattern_type = props.dot_pattern_type);
      let corners = {};
      (props.corner_1_shape || props.corner_1_inner_color || props.corner_1_outer_color) && (corners.corner_1 = {
        shape: props.corner_1_shape,
        inner_color: props.corner_1_inner_color,
        outer_color: props.corner_1_outer_color
      }), (props.corner_2_shape || props.corner_2_inner_color || props.corner_2_outer_color) && (corners.corner_2 = {
        shape: props.corner_2_shape,
        inner_color: props.corner_2_inner_color,
        outer_color: props.corner_2_outer_color
      }), (props.corner_3_shape || props.corner_3_inner_color || props.corner_3_outer_color) && (corners.corner_3 = {
        shape: props.corner_3_shape,
        inner_color: props.corner_3_inner_color,
        outer_color: props.corner_3_outer_color
      }), Object.keys(corners).length > 0 && (customizations.corners = corners);
      let gradient = {};
      if (props.gradient_style && (gradient.style = props.gradient_style), props.gradient_angle && (gradient.angle = props.gradient_angle), props.gradient_exclude_corners && (gradient.exclude_corners = props.gradient_exclude_corners), props.gradient_color_1 || props.gradient_color_2) {
        let colors = [];
        props.gradient_color_1 && colors.push({ color: props.gradient_color_1, offset: 0 }), props.gradient_color_2 && colors.push({ color: props.gradient_color_2, offset: 100 }), gradient.colors = colors;
      }
      Object.keys(gradient).length > 0 && (customizations.gradient = gradient);
      let frame = {};
      props.frame_id && (frame.id = props.frame_id);
      let frameColors = {};
      props.frame_primary_color && (frameColors.primary = props.frame_primary_color), props.frame_secondary_color && (frameColors.secondary = props.frame_secondary_color), props.frame_background_color && (frameColors.background = props.frame_background_color), Object.keys(frameColors).length > 0 && (frame.colors = frameColors), props.frame_text && (frame.text = { primary: { content: props.frame_text } }), Object.keys(frame).length > 0 && (customizations.frame = frame);
      let branding = {};
      props.bitly_brand !== void 0 && (branding.bitly_brand = props.bitly_brand), Object.keys(branding).length > 0 && (customizations.branding = branding);
      let logo = {};
      props.logo_image_guid && (logo.image_guid = props.logo_image_guid), Object.keys(logo).length > 0 && (customizations.logo = logo);
      let specSettings = {};
      return props.error_correction && (specSettings.error_correction = props.error_correction), Object.keys(specSettings).length > 0 && (customizations.spec_settings = specSettings), Object.keys(customizations).length > 0 && (body.render_customizations = customizations), await bitlyApiCall({
        method: import_pieces_common7.HttpMethod.POST,
        auth: context.auth.props,
        resourceUri: "/qr-codes",
        body
      });
    } catch (error) {
      let errorMessage = ((_b = (_a = error.response) == null ? void 0 : _a.data) == null ? void 0 : _b.description) || ((_d = (_c = error.response) == null ? void 0 : _c.data) == null ? void 0 : _d.message) || error.message;
      throw ((_e = error.response) == null ? void 0 : _e.status) === 429 ? new Error(
        "Rate limit exceeded. Please wait before trying again."
      ) : ((_f = error.response) == null ? void 0 : _f.status) === 422 ? new Error(
        `Unprocessable Entity: ${errorMessage}. Please check the format of your Long URL or other inputs.`
      ) : ((_g = error.response) == null ? void 0 : _g.status) === 401 || ((_h = error.response) == null ? void 0 : _h.status) === 403 ? new Error(
        `Authentication failed or forbidden: ${errorMessage}. Please check your Access Token and permissions.`
      ) : new Error(
        `Failed to create QR Code: ${errorMessage || "Unknown error occurred"}`
      );
    }
  }
});

// ../ap/packages/pieces/community/bitly/src/lib/actions/get-bitlink-details.ts
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework");
var getBitlinkDetailsAction = (0, import_pieces_framework7.createAction)({
  auth: bitlyAuth,
  name: "get_bitlink_details",
  classification: "READ",
  displayName: "Get Bitlink Details",
  description: "Retrieve metadata for a Bitlink.",
  audience: "both",
  aiMetadata: { description: "Retrieves metadata for a single Bitlink (title, long URL, tags, archive status, timestamps) identified by its Bitlink ID. Use to look up the current state of a known short link. Idempotent read-only lookup.", idempotent: !0 },
  props: {
    group_guid: groupGuid,
    bitlink: bitlinkDropdown
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h;
    let { bitlink } = context.propsValue;
    try {
      return await bitlyApiCall({
        method: import_pieces_common8.HttpMethod.GET,
        auth: context.auth.props,
        resourceUri: `/bitlinks/${bitlink}`
      });
    } catch (error) {
      let errorMessage = ((_b = (_a = error.response) == null ? void 0 : _a.data) == null ? void 0 : _b.description) || ((_d = (_c = error.response) == null ? void 0 : _c.data) == null ? void 0 : _d.message) || error.message;
      throw ((_e = error.response) == null ? void 0 : _e.status) === 429 ? new Error(
        "Rate limit exceeded. Please wait before trying again."
      ) : ((_f = error.response) == null ? void 0 : _f.status) === 404 ? new Error(
        `Bitlink not found: ${errorMessage}. Please verify the link ID is correct.`
      ) : ((_g = error.response) == null ? void 0 : _g.status) === 401 || ((_h = error.response) == null ? void 0 : _h.status) === 403 ? new Error(
        `Authentication failed or forbidden: ${errorMessage}. Please check your Access Token and permissions.`
      ) : new Error(
        `Failed to get Bitlink details: ${errorMessage || "Unknown error occurred"}`
      );
    }
  }
});

// ../ap/packages/pieces/community/bitly/src/lib/actions/update-bitlink.ts
var import_pieces_common9 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework");
var updateBitlinkAction = (0, import_pieces_framework8.createAction)({
  auth: bitlyAuth,
  name: "update_bitlink",
  classification: "WRITE",
  displayName: "Update Bitlink",
  description: "Modify properties of an existing Bitlink.",
  audience: "both",
  aiMetadata: { description: "Updates properties of an existing Bitlink identified by its Bitlink ID, including title, archive status, tags (overwrites existing), and mobile app deeplinks. Use to edit or re-tag a known short link; at least one field must be provided. Idempotent: applying the same values repeatedly yields the same final state.", idempotent: !0 },
  props: {
    group_guid: groupGuid,
    bitlink: bitlinkDropdown,
    title: import_pieces_framework8.Property.ShortText({
      displayName: "Title",
      description: "New title for the Bitlink.",
      required: !1
    }),
    archived: import_pieces_framework8.Property.Checkbox({
      displayName: "Archived",
      description: "Archive or unarchive the Bitlink.",
      required: !1
    }),
    tags: import_pieces_framework8.Property.Array({
      displayName: "Tags",
      description: "Tags to apply (overwrites existing tags).",
      required: !1
    }),
    // Mobile App Deeplink Configuration
    app_uri_path: import_pieces_framework8.Property.ShortText({
      displayName: "App URI Path",
      description: "Path within the mobile app (e.g., /product/123).",
      required: !1
    }),
    install_url: import_pieces_framework8.Property.LongText({
      displayName: "App Install URL",
      description: "URL where users can install the mobile app.",
      required: !1
    }),
    os: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Mobile OS",
      description: "Target mobile operating system.",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "iOS", value: "ios" },
          { label: "Android", value: "android" }
        ]
      }
    }),
    install_type: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Install Type",
      description: "How to handle app installation.",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "No Install", value: "no_install" },
          { label: "Auto Install", value: "auto_install" },
          { label: "Promote Install", value: "promote_install" }
        ]
      }
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h;
    let {
      bitlink,
      title,
      archived,
      tags,
      app_uri_path,
      install_url,
      os,
      install_type
    } = context.propsValue;
    try {
      let body = {};
      if (title != null && (body.title = title), archived != null && (body.archived = archived), tags != null && Array.isArray(tags) && (body.tags = tags), app_uri_path || install_url || os || install_type) {
        let deeplink = {};
        app_uri_path && (deeplink.app_uri_path = app_uri_path), install_url && (deeplink.install_url = install_url), os && (deeplink.os = os), install_type && (deeplink.install_type = install_type), Object.keys(deeplink).length > 0 && (body.deeplinks = [deeplink]);
      }
      if (Object.keys(body).length === 0)
        throw new Error(
          "No fields were provided to update. Please provide a title, tags, archive status, or deeplinks."
        );
      return await bitlyApiCall({
        method: import_pieces_common9.HttpMethod.PATCH,
        auth: context.auth.props,
        resourceUri: `/bitlinks/${bitlink}`,
        body
      });
    } catch (error) {
      let errorMessage = ((_b = (_a = error.response) == null ? void 0 : _a.data) == null ? void 0 : _b.description) || ((_d = (_c = error.response) == null ? void 0 : _c.data) == null ? void 0 : _d.message) || error.message;
      throw ((_e = error.response) == null ? void 0 : _e.status) === 429 ? new Error(
        "Rate limit exceeded. Please wait before trying again."
      ) : ((_f = error.response) == null ? void 0 : _f.status) === 404 ? new Error(
        `Bitlink not found: ${errorMessage}. Please verify the link (e.g., 'bit.ly/xyz123') is correct.`
      ) : ((_g = error.response) == null ? void 0 : _g.status) === 401 || ((_h = error.response) == null ? void 0 : _h.status) === 403 ? new Error(
        `Authentication failed or forbidden: ${errorMessage}. Please check your Access Token and permissions.`
      ) : error.message.includes("Invalid JSON format") ? error : new Error(
        `Failed to update Bitlink: ${errorMessage || "Unknown error occurred"}`
      );
    }
  }
});

// ../ap/packages/pieces/community/bitly/src/index.ts
var bitly = (0, import_pieces_framework9.createPiece)({
  displayName: "Bitly",
  description: "URL shortening and link management platform with analytics.",
  auth: bitlyAuth,
  minimumSupportedRelease: "0.20.0",
  logoUrl: "https://cdn.activepieces.com/pieces/bitly.png",
  authors: ["aryel780"],
  categories: [import_pieces_framework10.PieceCategory.MARKETING],
  actions: [
    archiveBitlinkAction,
    createBitlinkAction,
    createQrCodeAction,
    getBitlinkDetailsAction,
    updateBitlinkAction,
    (0, import_pieces_common10.createCustomApiCallAction)({
      auth: bitlyAuth,
      baseUrl: () => "https://api-ssl.bitly.com/v4",
      authMapping: async (auth) => {
        let { accessToken } = auth.props;
        return {
          Authorization: `Bearer ${accessToken}`
        };
      }
    })
  ],
  triggers: [newBitlinkCreatedTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  bitly
});
