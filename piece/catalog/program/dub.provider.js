"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __objRest = (source, exclude) => {
  var target = {};
  for (var prop in source)
    __hasOwnProp.call(source, prop) && exclude.indexOf(prop) < 0 && (target[prop] = source[prop]);
  if (source != null && __getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(source))
      exclude.indexOf(prop) < 0 && __propIsEnum.call(source, prop) && (target[prop] = source[prop]);
  return target;
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

// ../ap/packages/pieces/community/dub/src/index.ts
var index_exports = {};
__export(index_exports, {
  dub: () => dub
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/dub/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), DUB_API_BASE = "https://api.dub.co", authDescription = "\nTo obtain your Dub API key:\n1. Go to [https://app.dub.co/settings/tokens](https://app.dub.co/settings/tokens)\n2. Click **Create API Key**\n3. Give your key a name and select the required scopes\n4. Copy the key \u2014 it starts with `dub_` (e.g. `dub_xxxxxxxxxxxxxxxx`)\n5. Paste it here\n\n> **Required scopes:** `links.read`, `links.write`, `webhooks.write` (for triggers)\n", dubAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: authDescription,
  required: !0,
  validate: async ({ auth }) => {
    var _a, _b;
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${DUB_API_BASE}/folders`,
        headers: {
          Authorization: `Bearer ${auth.trim()}`,
          "Content-Type": "application/json"
        }
      }), { valid: !0 };
    } catch (e) {
      let err = e;
      return ((_a = err == null ? void 0 : err.response) == null ? void 0 : _a.status) === 401 ? {
        valid: !1,
        error: "Invalid API key. Please check your Dub API credentials."
      } : ((_b = err == null ? void 0 : err.response) == null ? void 0 : _b.status) === 403 ? {
        valid: !1,
        error: "Insufficient scopes. Ensure your API key has links.read, links.write, and webhooks.write permissions."
      } : {
        valid: !1,
        error: "Unable to validate API key. Please try again."
      };
    }
  }
});

// ../ap/packages/pieces/community/dub/src/lib/actions/create-link.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var createLink = (0, import_pieces_framework2.createAction)({
  name: "create_link",
  displayName: "Create Link",
  description: "Create a new shortened link in your Dub workspace.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new Dub short link for a destination URL, optionally with a custom domain, slug, tags, UTM parameters, expiration, password protection, and device-specific redirects. Use when an agent needs to generate a trackable short link. Not idempotent: each call creates a separate link, and if no slug is supplied a new random one is generated, so repeating the call yields duplicate links.",
    idempotent: !1
  },
  auth: dubAuth,
  props: {
    url: import_pieces_framework2.Property.ShortText({
      displayName: "Destination URL",
      description: "The full destination URL the short link should redirect to.",
      required: !0
    }),
    domain: import_pieces_framework2.Property.ShortText({
      displayName: "Domain",
      description: "The domain to use for the short link (e.g. `dub.sh`). If omitted, the primary workspace domain is used.",
      required: !1
    }),
    key: import_pieces_framework2.Property.ShortText({
      displayName: "Short Link Slug",
      description: "Custom slug for the short link (e.g. `my-promo`). If omitted, a random 7-character slug is generated.",
      required: !1
    }),
    externalId: import_pieces_framework2.Property.ShortText({
      displayName: "External ID",
      description: "Your internal database ID for this link. Must be unique per workspace. Useful for looking up the link later.",
      required: !1
    }),
    title: import_pieces_framework2.Property.ShortText({
      displayName: "Title",
      description: "A human-readable title for the link (used in the Dub dashboard).",
      required: !1
    }),
    description: import_pieces_framework2.Property.LongText({
      displayName: "Description",
      description: "A short description of what the link points to.",
      required: !1
    }),
    image: import_pieces_framework2.Property.ShortText({
      displayName: "OG Image URL",
      description: "Custom link preview image (og:image). Used when Custom Link Previews (`proxy`) is enabled.",
      required: !1
    }),
    video: import_pieces_framework2.Property.ShortText({
      displayName: "OG Video URL",
      description: "Custom link preview video (og:video). Used when Custom Link Previews (`proxy`) is enabled.",
      required: !1
    }),
    tags: import_pieces_framework2.Property.Array({
      displayName: "Tag Names",
      description: "List of tag names to attach to this link.",
      required: !1
    }),
    folderId: import_pieces_framework2.Property.ShortText({
      displayName: "Folder ID",
      description: "The unique ID of an existing folder to assign this link to.",
      required: !1
    }),
    expiresAt: import_pieces_framework2.Property.ShortText({
      displayName: "Expires At",
      description: "ISO 8601 datetime when the link should expire (e.g. `2025-12-31T23:59:59Z`). Expired links redirect to the expiration URL.",
      required: !1
    }),
    expiredUrl: import_pieces_framework2.Property.ShortText({
      displayName: "Expired URL",
      description: "URL to redirect to after the link expires.",
      required: !1
    }),
    password: import_pieces_framework2.Property.ShortText({
      displayName: "Password",
      description: "Password-protect the link. Visitors must enter this password before being redirected.",
      required: !1
    }),
    trackConversion: import_pieces_framework2.Property.Checkbox({
      displayName: "Track Conversions",
      description: "Whether to track conversions for this link.",
      required: !1,
      defaultValue: !1
    }),
    proxy: import_pieces_framework2.Property.Checkbox({
      displayName: "Custom Link Previews",
      description: "Enable Custom Link Previews (og:title, og:description, og:image) for this link.",
      required: !1,
      defaultValue: !1
    }),
    rewrite: import_pieces_framework2.Property.Checkbox({
      displayName: "Cloaked URL (URL Rewriting)",
      description: "Cloak the destination URL \u2014 visitors see the short link URL in the browser.",
      required: !1,
      defaultValue: !1
    }),
    doIndex: import_pieces_framework2.Property.Checkbox({
      displayName: "Allow Search Engine Indexing",
      description: "Allow search engines to index this short link.",
      required: !1,
      defaultValue: !1
    }),
    archived: import_pieces_framework2.Property.Checkbox({
      displayName: "Archived",
      description: "Archive this link immediately after creation.",
      required: !1,
      defaultValue: !1
    }),
    ios: import_pieces_framework2.Property.ShortText({
      displayName: "iOS Redirect URL",
      description: "Redirect iOS device visitors to this URL instead.",
      required: !1
    }),
    android: import_pieces_framework2.Property.ShortText({
      displayName: "Android Redirect URL",
      description: "Redirect Android device visitors to this URL instead.",
      required: !1
    }),
    comments: import_pieces_framework2.Property.LongText({
      displayName: "Comments",
      description: "Internal notes about this link (not shown to visitors).",
      required: !1
    }),
    // UTM parameters
    utm_source: import_pieces_framework2.Property.ShortText({
      displayName: "UTM Source",
      description: "UTM source parameter (e.g. `newsletter`, `twitter`).",
      required: !1
    }),
    utm_medium: import_pieces_framework2.Property.ShortText({
      displayName: "UTM Medium",
      description: "UTM medium parameter (e.g. `email`, `social`).",
      required: !1
    }),
    utm_campaign: import_pieces_framework2.Property.ShortText({
      displayName: "UTM Campaign",
      description: "UTM campaign parameter (e.g. `spring-sale`).",
      required: !1
    }),
    utm_term: import_pieces_framework2.Property.ShortText({
      displayName: "UTM Term",
      description: "UTM term parameter for paid search keywords.",
      required: !1
    }),
    utm_content: import_pieces_framework2.Property.ShortText({
      displayName: "UTM Content",
      description: "UTM content parameter for A/B testing.",
      required: !1
    })
  },
  async run(context) {
    let { auth, propsValue } = context, body = {
      url: propsValue.url
    };
    return propsValue.domain && (body.domain = propsValue.domain), propsValue.key && (body.key = propsValue.key), propsValue.externalId && (body.externalId = propsValue.externalId), propsValue.title && (body.title = propsValue.title), propsValue.description && (body.description = propsValue.description), propsValue.image && (body.image = propsValue.image), propsValue.video && (body.video = propsValue.video), propsValue.tags && propsValue.tags.length > 0 && (body.tagNames = propsValue.tags), propsValue.folderId && (body.folderId = propsValue.folderId), propsValue.expiresAt && (body.expiresAt = propsValue.expiresAt), propsValue.expiredUrl && (body.expiredUrl = propsValue.expiredUrl), propsValue.password && (body.password = propsValue.password), propsValue.trackConversion && (body.trackConversion = propsValue.trackConversion), propsValue.proxy && (body.proxy = propsValue.proxy), propsValue.rewrite && (body.rewrite = propsValue.rewrite), propsValue.doIndex && (body.doIndex = propsValue.doIndex), propsValue.archived && (body.archived = propsValue.archived), propsValue.ios && (body.ios = propsValue.ios), propsValue.android && (body.android = propsValue.android), propsValue.comments && (body.comments = propsValue.comments), propsValue.utm_source && (body.utm_source = propsValue.utm_source), propsValue.utm_medium && (body.utm_medium = propsValue.utm_medium), propsValue.utm_campaign && (body.utm_campaign = propsValue.utm_campaign), propsValue.utm_term && (body.utm_term = propsValue.utm_term), propsValue.utm_content && (body.utm_content = propsValue.utm_content), (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: `${DUB_API_BASE}/links`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`,
        "Content-Type": "application/json"
      },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/dub/src/lib/actions/get-link.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var getLink = (0, import_pieces_framework3.createAction)({
  name: "get_link",
  displayName: "Get Link",
  description: "Retrieve details of a specific Dub link by its ID, external ID, or domain + key.",
  audience: "both",
  aiMetadata: {
    description: "Fetches the full details of a single existing Dub link. Choose the lookup mode: by Dub link ID, by your own external ID (prefixed with ext_), or by domain + slug key. Use when an agent has a known identifier and needs the current link record. Idempotent read-only lookup.",
    idempotent: !0
  },
  auth: dubAuth,
  props: {
    lookupMethod: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Look Up By",
      description: "How to identify the link you want to retrieve.",
      required: !0,
      defaultValue: "linkId",
      options: {
        options: [
          { label: "Link ID", value: "linkId" },
          { label: "External ID", value: "externalId" },
          { label: "Domain + Key", value: "domainKey" }
        ]
      }
    }),
    linkId: import_pieces_framework3.Property.ShortText({
      displayName: "Link ID",
      description: "The Dub-generated link ID (e.g. `clv3g2...`). Used when Look Up By = Link ID.",
      required: !1
    }),
    externalId: import_pieces_framework3.Property.ShortText({
      displayName: "External ID",
      description: "Your internal ID for the link. Pass with prefix `ext_` (e.g. `ext_123456`). Used when Look Up By = External ID.",
      required: !1
    }),
    domain: import_pieces_framework3.Property.ShortText({
      displayName: "Domain",
      description: "The domain of the link (e.g. `dub.sh`). Used when Look Up By = Domain + Key.",
      required: !1
    }),
    key: import_pieces_framework3.Property.ShortText({
      displayName: "Key (Slug)",
      description: "The slug of the link (e.g. `my-promo`). Used when Look Up By = Domain + Key.",
      required: !1
    })
  },
  async run(context) {
    let { auth, propsValue } = context, { lookupMethod, linkId, externalId, domain, key } = propsValue, url;
    switch (lookupMethod) {
      case "linkId":
        if (!linkId) throw new Error("Link ID is required when Look Up By = Link ID.");
        url = `${DUB_API_BASE}/links/${linkId}`;
        break;
      case "externalId":
        if (!externalId) throw new Error("External ID is required when Look Up By = External ID.");
        url = `${DUB_API_BASE}/links/info?externalId=${encodeURIComponent(externalId)}`;
        break;
      case "domainKey":
        if (!domain || !key)
          throw new Error("Both Domain and Key are required when Look Up By = Domain + Key.");
        url = `${DUB_API_BASE}/links/info?domain=${encodeURIComponent(domain)}&key=${encodeURIComponent(key)}`;
        break;
      default:
        throw new Error(`Unsupported lookup method: ${lookupMethod}`);
    }
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.GET,
      url,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`,
        "Content-Type": "application/json"
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/dub/src/lib/actions/list-links.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var listLinks = (0, import_pieces_framework4.createAction)({
  name: "list_links",
  displayName: "List Links",
  description: "Retrieve a paginated list of links in your Dub workspace.",
  audience: "both",
  aiMetadata: {
    description: "Lists short links in the Dub workspace, with optional filters by domain, tag names, user, archived status, and a free-text search over URL/title/slug; leave filters empty to return all links. Use when an agent needs to discover links or find a link ID without already knowing an identifier. Supports pagination and sorting. Idempotent read-only query.",
    idempotent: !0
  },
  auth: dubAuth,
  props: {
    domain: import_pieces_framework4.Property.ShortText({
      displayName: "Domain Filter",
      description: "Filter by a specific domain (e.g. `dub.sh`). Leave empty to return all domains.",
      required: !1
    }),
    tagNames: import_pieces_framework4.Property.Array({
      displayName: "Tag Name Filter",
      description: "Filter by one or more tag names.",
      required: !1
    }),
    search: import_pieces_framework4.Property.ShortText({
      displayName: "Search Query",
      description: "Search links by their URL, title, or slug.",
      required: !1
    }),
    userId: import_pieces_framework4.Property.ShortText({
      displayName: "User ID Filter",
      description: "Filter links created by a specific user ID.",
      required: !1
    }),
    showArchived: import_pieces_framework4.Property.Checkbox({
      displayName: "Include Archived Links",
      description: "Whether to include archived links in the results.",
      required: !1,
      defaultValue: !1
    }),
    page: import_pieces_framework4.Property.Number({
      displayName: "Page",
      description: "Page number for pagination (starts at 1).",
      required: !1,
      defaultValue: 1
    }),
    pageSize: import_pieces_framework4.Property.Number({
      displayName: "Page Size",
      description: "Number of links to return per page (1\u2013100). Default is 100.",
      required: !1,
      defaultValue: 100
    }),
    sort: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Sort By",
      description: "Field to sort results by.",
      required: !1,
      defaultValue: "createdAt",
      options: {
        options: [
          { label: "Created At (newest first)", value: "createdAt" },
          { label: "Clicks (most clicks first)", value: "clicks" },
          { label: "Last Clicked", value: "lastClicked" }
        ]
      }
    })
  },
  async run(context) {
    let { auth, propsValue } = context, params = new URLSearchParams();
    if (propsValue.domain && params.set("domain", propsValue.domain), propsValue.search && params.set("search", propsValue.search), propsValue.userId && params.set("userId", propsValue.userId), propsValue.showArchived && params.set("showArchived", "true"), propsValue.page && params.set("page", String(propsValue.page)), propsValue.pageSize && params.set("pageSize", String(propsValue.pageSize)), propsValue.sort && params.set("sort", propsValue.sort), propsValue.tagNames && propsValue.tagNames.length > 0)
      for (let tag of propsValue.tagNames)
        params.append("tagName", tag);
    let queryString = params.toString(), url = `${DUB_API_BASE}/links${queryString ? `?${queryString}` : ""}`;
    return (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.GET,
      url,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`,
        "Content-Type": "application/json"
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/dub/src/lib/actions/update-link.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var updateLink = (0, import_pieces_framework5.createAction)({
  name: "update_link",
  displayName: "Update Link",
  description: "Update an existing Dub link. Only the fields you fill in will be updated.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing Dub link identified by its link ID, applying only the fields supplied (destination URL, slug, tags, UTM parameters, expiration, password, device redirects, archived state, etc.). Use when an agent needs to modify a known link rather than create one. Requires the link ID and at least one field to change. Idempotent: applying the same field values repeatedly leaves the link in the same state.",
    idempotent: !0
  },
  auth: dubAuth,
  props: {
    linkId: import_pieces_framework5.Property.ShortText({
      displayName: "Link ID",
      description: "The ID of the link to update (e.g. `clv3g2...`). Find this from the Create Link or List Links actions.",
      required: !0
    }),
    url: import_pieces_framework5.Property.ShortText({
      displayName: "New Destination URL",
      description: "Update the destination URL the link redirects to.",
      required: !1
    }),
    key: import_pieces_framework5.Property.ShortText({
      displayName: "New Slug",
      description: "Update the short link slug (the part after the domain).",
      required: !1
    }),
    title: import_pieces_framework5.Property.ShortText({
      displayName: "Title",
      description: "Update the human-readable title.",
      required: !1
    }),
    description: import_pieces_framework5.Property.LongText({
      displayName: "Description",
      description: "Update the link description.",
      required: !1
    }),
    image: import_pieces_framework5.Property.ShortText({
      displayName: "OG Image URL",
      description: "Update the custom link preview image (og:image).",
      required: !1
    }),
    video: import_pieces_framework5.Property.ShortText({
      displayName: "OG Video URL",
      description: "Update the custom link preview video (og:video).",
      required: !1
    }),
    tags: import_pieces_framework5.Property.Array({
      displayName: "Tag Names",
      description: "Replace the link's tags with this new list of tag names. Send an empty array to clear all tags.",
      required: !1
    }),
    folderId: import_pieces_framework5.Property.ShortText({
      displayName: "Folder ID",
      description: "Move this link to a different folder by its ID.",
      required: !1
    }),
    expiresAt: import_pieces_framework5.Property.ShortText({
      displayName: "Expires At",
      description: "ISO 8601 datetime when the link should expire. Set to empty to remove expiration.",
      required: !1
    }),
    expiredUrl: import_pieces_framework5.Property.ShortText({
      displayName: "Expired URL",
      description: "URL to redirect to after expiration.",
      required: !1
    }),
    password: import_pieces_framework5.Property.ShortText({
      displayName: "Password",
      description: "Update the password for this link. Leave blank to remove password protection.",
      required: !1
    }),
    trackConversion: import_pieces_framework5.Property.Checkbox({
      displayName: "Track Conversions",
      description: "Whether to track conversions for this link.",
      required: !1
    }),
    proxy: import_pieces_framework5.Property.Checkbox({
      displayName: "Custom Link Previews",
      description: "Enable or disable Custom Link Previews (og:title, og:description, og:image) for this link.",
      required: !1
    }),
    rewrite: import_pieces_framework5.Property.Checkbox({
      displayName: "Cloaked URL (URL Rewriting)",
      description: "Toggle URL cloaking.",
      required: !1
    }),
    doIndex: import_pieces_framework5.Property.Checkbox({
      displayName: "Allow Search Engine Indexing",
      description: "Allow search engines to index this short link.",
      required: !1
    }),
    archived: import_pieces_framework5.Property.Checkbox({
      displayName: "Archived",
      description: "Archive or unarchive the link.",
      required: !1
    }),
    ios: import_pieces_framework5.Property.ShortText({
      displayName: "iOS Redirect URL",
      description: "Update the iOS-specific redirect URL.",
      required: !1
    }),
    android: import_pieces_framework5.Property.ShortText({
      displayName: "Android Redirect URL",
      description: "Update the Android-specific redirect URL.",
      required: !1
    }),
    comments: import_pieces_framework5.Property.LongText({
      displayName: "Comments",
      description: "Update internal notes for this link.",
      required: !1
    }),
    utm_source: import_pieces_framework5.Property.ShortText({
      displayName: "UTM Source",
      description: "Update the UTM source parameter.",
      required: !1
    }),
    utm_medium: import_pieces_framework5.Property.ShortText({
      displayName: "UTM Medium",
      description: "Update the UTM medium parameter.",
      required: !1
    }),
    utm_campaign: import_pieces_framework5.Property.ShortText({
      displayName: "UTM Campaign",
      description: "Update the UTM campaign parameter.",
      required: !1
    }),
    utm_term: import_pieces_framework5.Property.ShortText({
      displayName: "UTM Term",
      description: "Update the UTM term parameter.",
      required: !1
    }),
    utm_content: import_pieces_framework5.Property.ShortText({
      displayName: "UTM Content",
      description: "Update the UTM content parameter.",
      required: !1
    })
  },
  async run(context) {
    let { auth, propsValue } = context, _a = propsValue, { linkId } = _a, rest = __objRest(_a, ["linkId"]), body = {};
    if (rest.url !== void 0 && rest.url !== null && rest.url !== "" && (body.url = rest.url), rest.key !== void 0 && rest.key !== null && rest.key !== "" && (body.key = rest.key), rest.title !== void 0 && rest.title !== null && (body.title = rest.title), rest.description !== void 0 && rest.description !== null && (body.description = rest.description), rest.image !== void 0 && rest.image !== null && (body.image = rest.image), rest.video !== void 0 && rest.video !== null && (body.video = rest.video), rest.tags !== void 0 && (body.tagNames = rest.tags), rest.folderId !== void 0 && rest.folderId !== null && (body.folderId = rest.folderId), rest.expiresAt !== void 0 && rest.expiresAt !== null && (body.expiresAt = rest.expiresAt), rest.expiredUrl !== void 0 && rest.expiredUrl !== null && (body.expiredUrl = rest.expiredUrl), rest.password !== void 0 && rest.password !== null && (body.password = rest.password), rest.trackConversion !== void 0 && (body.trackConversion = rest.trackConversion), rest.proxy !== void 0 && (body.proxy = rest.proxy), rest.rewrite !== void 0 && (body.rewrite = rest.rewrite), rest.doIndex !== void 0 && (body.doIndex = rest.doIndex), rest.archived !== void 0 && (body.archived = rest.archived), rest.ios !== void 0 && rest.ios !== null && (body.ios = rest.ios), rest.android !== void 0 && rest.android !== null && (body.android = rest.android), rest.comments !== void 0 && rest.comments !== null && (body.comments = rest.comments), rest.utm_source !== void 0 && rest.utm_source !== null && (body.utm_source = rest.utm_source), rest.utm_medium !== void 0 && rest.utm_medium !== null && (body.utm_medium = rest.utm_medium), rest.utm_campaign !== void 0 && rest.utm_campaign !== null && (body.utm_campaign = rest.utm_campaign), rest.utm_term !== void 0 && rest.utm_term !== null && (body.utm_term = rest.utm_term), rest.utm_content !== void 0 && rest.utm_content !== null && (body.utm_content = rest.utm_content), Object.keys(body).length === 0)
      throw new Error("At least one field must be provided to update.");
    return (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.PATCH,
      url: `${DUB_API_BASE}/links/${linkId}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`,
        "Content-Type": "application/json"
      },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/dub/src/lib/actions/delete-link.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var deleteLink = (0, import_pieces_framework6.createAction)({
  name: "delete_link",
  displayName: "Delete Link",
  description: "Permanently delete a Dub link. This action cannot be undone.",
  audience: "both",
  aiMetadata: {
    description: "Permanently deletes a Dub link by its link ID; this is destructive and cannot be undone. Use only when an agent is certain the link should be removed. Idempotent on end state \u2014 once deleted the link stays gone \u2014 but a repeat call targets an already-removed link.",
    idempotent: !0
  },
  auth: dubAuth,
  props: {
    linkId: import_pieces_framework6.Property.ShortText({
      displayName: "Link ID",
      description: "The ID of the link to delete (e.g. `clv3g2...`). Find this from the Create Link or List Links actions.",
      required: !0
    })
  },
  async run(context) {
    let { auth, propsValue } = context;
    return (await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.DELETE,
      url: `${DUB_API_BASE}/links/${propsValue.linkId}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`,
        "Content-Type": "application/json"
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/dub/src/lib/triggers/link-clicked.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_framework8 = require("@activepieces/pieces-framework");
var linkClicked = (0, import_pieces_framework7.createTrigger)({
  auth: dubAuth,
  name: "link_clicked",
  displayName: "Link Clicked",
  description: "Triggers in real time whenever one of your Dub short links is clicked.",
  aiMetadata: {
    description: "Fires in real time each time any short link in the Dub workspace is clicked, delivering the click event together with the link and visitor context (location, device, browser, referrer). Use to react to traffic or attribution events as they happen."
  },
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  props: {
    md: import_pieces_framework7.Property.MarkDown({
      value: `## Setup Instructions

1. Go to your Dub workspace and navigate to **Settings \u2192 [Webhooks](https://app.dub.co/settings/webhooks)**.
2. Click **Create Webhook**.
3. Give your webhook a name.
4. Paste the following URL into the **URL** field:
\`\`\`text
{{webhookUrl}}
\`\`\`
5. Under **Events**, select **link.clicked**.
6. Click **Create webhook**.`,
      variant: import_pieces_framework8.MarkdownVariant.INFO
    })
  },
  sampleData: {
    id: "evt_abc123",
    event: "link.clicked",
    createdAt: "2024-01-15T10:30:00.000Z",
    data: {
      link: {
        id: "clv3g2xyz",
        domain: "dub.sh",
        key: "my-promo",
        url: "https://example.com/landing",
        shortLink: "https://dub.sh/my-promo",
        clicks: 42
      },
      click: {
        id: "click_abc",
        timestamp: "2024-01-15T10:30:00.000Z",
        identity: "anonymous",
        url: "https://dub.sh/my-promo",
        country: "US",
        city: "San Francisco",
        region: "CA",
        continent: "NA",
        device: "Desktop",
        browser: "Chrome",
        os: "macOS",
        referer: "https://twitter.com",
        refererUrl: "https://twitter.com"
      }
    }
  },
  async onEnable() {
  },
  async onDisable() {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/dub/src/lib/triggers/link-created.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_framework10 = require("@activepieces/pieces-framework");
var linkCreated = (0, import_pieces_framework9.createTrigger)({
  auth: dubAuth,
  name: "link_created",
  displayName: "Link Created",
  description: "Triggers in real time whenever a new short link is created in your Dub workspace.",
  aiMetadata: {
    description: "Fires in real time whenever a new short link is created in the Dub workspace, delivering the newly created link record. Use to react to link creation, regardless of whether the link was made via the API, dashboard, or another integration."
  },
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  props: {
    md: import_pieces_framework9.Property.MarkDown({
      value: `## Setup Instructions

1. Go to your Dub workspace and navigate to **Settings \u2192 [Webhooks](https://app.dub.co/settings/webhooks)**.
2. Click **Create Webhook**.
3. Give your webhook a name.
4. Paste the following URL into the **URL** field:
\`\`\`text
{{webhookUrl}}
\`\`\`
5. Under **Events**, select **link.created**.
6. Click **Create webhook**.`,
      variant: import_pieces_framework10.MarkdownVariant.INFO
    })
  },
  sampleData: {
    id: "evt_def456",
    event: "link.created",
    createdAt: "2024-01-15T10:30:00.000Z",
    data: {
      link: {
        id: "clv3g2xyz",
        domain: "dub.sh",
        key: "new-link",
        url: "https://example.com/new-page",
        shortLink: "https://dub.sh/new-link",
        clicks: 0,
        createdAt: "2024-01-15T10:30:00.000Z"
      }
    }
  },
  async onEnable() {
  },
  async onDisable() {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/dub/src/index.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), dub = (0, import_pieces_framework11.createPiece)({
  displayName: "Dub",
  description: "Dub is the modern link attribution platform for creating, managing, and analysing short links, tracking conversions, and running affiliate programmes.",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/dub.png",
  categories: [import_pieces_framework12.PieceCategory.MARKETING, import_pieces_framework12.PieceCategory.DEVELOPER_TOOLS],
  auth: dubAuth,
  actions: [
    createLink,
    getLink,
    listLinks,
    updateLink,
    deleteLink,
    (0, import_pieces_common7.createCustomApiCallAction)({
      auth: dubAuth,
      baseUrl: () => DUB_API_BASE,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  authors: ["Harmatta"],
  triggers: [linkClicked, linkCreated]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  dub
});
