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

// ../ap/packages/pieces/community/influencers-club/src/index.ts
var index_exports = {};
__export(index_exports, {
  influencersClub: () => influencersClub
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/influencers-club/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), influencersClubAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Influencers Club API Key",
  description: "API Key for Influencers Club",
  required: !0
});

// ../ap/packages/pieces/community/influencers-club/src/lib/actions/enrich-creator-by-email.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/influencers-club/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api-dashboard.influencers.club/public/v1";
async function makeRequest(apikey, method, path, body) {
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers: {
        Authorization: `Bearer ${apikey}`,
        "Content-Type": "application/json"
      },
      body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/influencers-club/src/lib/actions/enrich-creator-by-email.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), enrichCreatorByEmail = (0, import_pieces_framework2.createAction)({
  auth: influencersClubAuth,
  name: "enrichCreatorByEmail",
  displayName: "Enrich Creator by Email",
  description: "Enrich creator data by email address with advanced mode including all social medias and full profile stats",
  audience: "both",
  aiMetadata: {
    description: "Look up and enrich a creator/influencer profile from a known email address, returning their connected social accounts and profile stats. Choose this when you have an email and want the matching creator data; use the by-handle action instead when you only have a social handle. Optionally filter results by minimum follower count or exclude specific platforms. This is a read-only lookup and is idempotent.",
    idempotent: !0
  },
  props: {
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email",
      description: "The email address of the creator to enrich",
      required: !0
    }),
    exclude_platforms: import_pieces_framework2.Property.StaticMultiSelectDropdown({
      displayName: "Exclude Platforms",
      description: "Platforms to exclude from enrichment (optional)",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Instagram", value: "instagram" },
          { label: "TikTok", value: "tiktok" },
          { label: "YouTube", value: "youtube" },
          { label: "Twitter", value: "twitter" },
          { label: "Twitch", value: "twitch" },
          { label: "OnlyFans", value: "onlyfans" },
          { label: "LinkedIn", value: "linkedin" },
          { label: "Facebook", value: "facebook" },
          { label: "Snapchat", value: "snapchat" },
          { label: "Reddit", value: "reddit" },
          { label: "Pinterest", value: "pinterest" },
          { label: "Discord", value: "discord" }
        ]
      }
    }),
    min_followers: import_pieces_framework2.Property.Number({
      displayName: "Minimum Followers",
      description: "Minimum follower count filter (default: 1000)",
      required: !1,
      defaultValue: 1e3
    })
  },
  async run(context) {
    let body = {
      email: context.propsValue.email
    };
    return context.propsValue.exclude_platforms && context.propsValue.exclude_platforms.length > 0 && (body.exclude_platforms = context.propsValue.exclude_platforms), context.propsValue.min_followers !== void 0 && context.propsValue.min_followers !== null && (body.min_followers = context.propsValue.min_followers), await makeRequest(
      context.auth.secret_text,
      import_pieces_common2.HttpMethod.POST,
      "/creators/enrich/email/advanced",
      body
    );
  }
});

// ../ap/packages/pieces/community/influencers-club/src/lib/actions/enrich-creator-by-handle.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common"), enrichCreatorByHandle = (0, import_pieces_framework3.createAction)({
  auth: influencersClubAuth,
  name: "enrichCreatorByHandle",
  displayName: "Enrich Creator Social Profile by Handle",
  description: "Enrich creator data by social media handle with full mode including all connected platforms and detailed stats",
  audience: "both",
  aiMetadata: {
    description: "Look up and enrich a creator/influencer profile from a social media handle plus the platform it belongs to (Instagram, TikTok, YouTube, Twitter/X, Twitch, or OnlyFans), returning their connected platforms and detailed stats. Choose this when you have a handle rather than an email; use the by-email action when you only have an email. The platform is required, and you can require a valid email be present (must_have) or accept partial data (preferred). This is a read-only lookup and is idempotent.",
    idempotent: !0
  },
  props: {
    handle: import_pieces_framework3.Property.ShortText({
      displayName: "Handle",
      description: "The social media handle of the creator",
      required: !0
    }),
    platform: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Platform",
      description: "The social media platform for the handle",
      required: !0,
      options: {
        disabled: !1,
        options: [
          { label: "Instagram", value: "instagram" },
          { label: "TikTok", value: "tiktok" },
          { label: "YouTube", value: "youtube" },
          { label: "Twitter/X", value: "twitter" },
          { label: "Twitch", value: "twitch" },
          { label: "OnlyFans", value: "onlyfans" }
        ]
      }
    }),
    include_lookalikes: import_pieces_framework3.Property.Checkbox({
      displayName: "Include Lookalikes",
      description: "Include similar creators for faster discovery (30-40% quicker). Default: true",
      required: !1,
      defaultValue: !0
    }),
    email_required: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Email Requirement",
      description: "Controls how to handle email availability",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Preferred (return data even if no email found)", value: "preferred" },
          { label: "Must Have (only return if valid email exists)", value: "must_have" }
        ]
      },
      defaultValue: "preferred"
    })
  },
  async run(context) {
    let body = {
      handle: context.propsValue.handle,
      platform: context.propsValue.platform
    };
    return context.propsValue.include_lookalikes !== void 0 && context.propsValue.include_lookalikes !== null && (body.include_lookalikes = context.propsValue.include_lookalikes), context.propsValue.email_required !== void 0 && context.propsValue.email_required !== null && (body.email_required = context.propsValue.email_required), await makeRequest(
      context.auth.secret_text,
      import_pieces_common3.HttpMethod.POST,
      "/creators/enrich/handle/full",
      body
    );
  }
});

// ../ap/packages/pieces/community/influencers-club/src/lib/actions/find-similar-creator.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common"), findSimilarCreator = (0, import_pieces_framework4.createAction)({
  auth: influencersClubAuth,
  name: "findSimilarCreator",
  displayName: "Find Similar Creator",
  description: "Find creators similar to a given creator handle or profile URL with optional filters",
  audience: "both",
  aiMetadata: {
    description: "Discover creators/influencers similar to a seed creator on a given platform, identifying that seed by URL, username, or platform user ID (set via the filter key). Choose this for lookalike discovery once you have a reference creator. Optionally narrow results by follower-count range, engagement-rate range, verified-only, or excluding private profiles, and cap the result count (1-50). This is a read-only search and is idempotent.",
    idempotent: !0
  },
  props: {
    platform: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Platform",
      description: "The social media platform",
      required: !0,
      options: {
        disabled: !1,
        options: [
          { label: "Instagram", value: "instagram" },
          { label: "TikTok", value: "tiktok" },
          { label: "YouTube", value: "youtube" },
          { label: "Twitch", value: "twitch" },
          { label: "Twitter/X", value: "twitter" },
          { label: "OnlyFans", value: "onlyfans" }
        ]
      }
    }),
    filter_key: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Filter Key",
      description: "The type of identifier for the creator",
      required: !0,
      options: {
        disabled: !1,
        options: [
          { label: "URL", value: "url" },
          { label: "Username", value: "username" },
          { label: "ID", value: "id" }
        ]
      }
    }),
    filter_value: import_pieces_framework4.Property.ShortText({
      displayName: "Filter Value",
      description: "Platform URL, profile handle, or user ID",
      required: !0
    }),
    limit: import_pieces_framework4.Property.Number({
      displayName: "Results Limit",
      description: "Maximum number of similar creators to return (min: 1, max: 50)",
      required: !1,
      defaultValue: 10
    }),
    number_of_followers: import_pieces_framework4.Property.Object({
      displayName: "Follower Count Range",
      description: "Filter by follower count (min and max)",
      required: !1
    }),
    engagement_percent: import_pieces_framework4.Property.Object({
      displayName: "Engagement Percentage Range",
      description: "Filter by engagement rate (min and max)",
      required: !1
    }),
    is_verified: import_pieces_framework4.Property.Checkbox({
      displayName: "Verified Only",
      description: "Only return verified creators",
      required: !1
    }),
    exclude_private_profile: import_pieces_framework4.Property.Checkbox({
      displayName: "Exclude Private Profiles",
      description: "Exclude creators with private profiles",
      required: !1
    })
  },
  async run(context) {
    let body = {
      platform: context.propsValue.platform,
      filter_key: context.propsValue.filter_key,
      filter_value: context.propsValue.filter_value,
      filters: {},
      paging: {
        limit: context.propsValue.limit || 10,
        page: 1
      }
    }, filters = {};
    return context.propsValue.number_of_followers && (filters.number_of_followers = context.propsValue.number_of_followers), context.propsValue.engagement_percent && (filters.engagement_percent = context.propsValue.engagement_percent), context.propsValue.is_verified !== void 0 && context.propsValue.is_verified !== null && (filters.is_verified = context.propsValue.is_verified), context.propsValue.exclude_private_profile !== void 0 && context.propsValue.exclude_private_profile !== null && (filters.exclude_private_profile = context.propsValue.exclude_private_profile), body.filters = filters, await makeRequest(
      context.auth.secret_text,
      import_pieces_common4.HttpMethod.POST,
      "/discovery/creators/similar/",
      body
    );
  }
});

// ../ap/packages/pieces/community/influencers-club/src/index.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common"), influencersClub = (0, import_pieces_framework5.createPiece)({
  displayName: "Influencers.club",
  auth: influencersClubAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/influencers-club.png",
  categories: [import_pieces_framework6.PieceCategory.MARKETING],
  authors: ["sanket-a11y"],
  description: "Connect to Influencers.club to enrich and find influencers for your marketing campaigns.",
  actions: [
    enrichCreatorByEmail,
    enrichCreatorByHandle,
    findSimilarCreator,
    (0, import_pieces_common5.createCustomApiCallAction)({
      auth: influencersClubAuth,
      baseUrl: () => "https://api-dashboard.influencers.club/public/v1",
      authMapping: async (auth) => ({ Authorization: `Bearer ${auth.secret_text}` })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  influencersClub
});
