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

// ../ap/packages/pieces/community/xquik/src/index.ts
var index_exports = {};
__export(index_exports, {
  xquik: () => xquik
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/xquik/src/lib/actions/get-trends.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/xquik/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/xquik/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common");
function cleanQueryParams(params) {
  let queryParams = {};
  for (let [key, value] of Object.entries(params))
    value == null || value === "" || (queryParams[key] = String(value));
  return queryParams;
}
function encodePathPart(value) {
  return encodeURIComponent(requireText({ name: "path value", value }));
}
function requireText({ name, value }) {
  let trimmed = value.trim();
  if (trimmed.length === 0)
    throw new Error(`${name} is required`);
  return trimmed;
}
function stripAtPrefix(value) {
  let trimmed = requireText({ name: "user", value });
  return trimmed.startsWith("@") ? trimmed.slice(1) : trimmed;
}
async function get({
  apiKey,
  path,
  queryParams
}) {
  return (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.GET,
    url: `${xquikCommon.config.baseUrl}${path}`,
    headers: {
      Accept: "application/json",
      "User-Agent": xquikCommon.config.userAgent,
      "x-api-key": apiKey,
      "xquik-api-contract": xquikCommon.config.apiContract
    },
    queryParams
  })).body;
}
var xquikCommon = {
  api: {
    get
  },
  config: {
    apiContract: "2026-04-29",
    baseUrl: "https://xquik.com/api/v1",
    userAgent: "activepieces-xquik/0.0.1"
  },
  utils: {
    cleanQueryParams,
    encodePathPart,
    requireText,
    stripAtPrefix
  }
};

// ../ap/packages/pieces/community/xquik/src/lib/auth.ts
var xquikAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Xquik API Key",
  description: "Create an API key in Xquik, then paste it here. API docs: https://docs.xquik.com/api-reference/overview",
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await xquikCommon.api.get({
        apiKey: auth,
        path: "/account"
      }), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API key"
      };
    }
  }
});

// ../ap/packages/pieces/community/xquik/src/lib/actions/get-trends.ts
var getTrends = (0, import_pieces_framework2.createAction)({
  auth: xquikAuth,
  name: "get_trends",
  displayName: "Get Trends",
  description: "Get X/Twitter trending topics by region",
  audience: "both",
  aiMetadata: {
    description: "Fetches current X/Twitter trending topics for a region, selected via a Yahoo WOEID (default 1 for worldwide). Use to see what is trending in a given location. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    woeid: import_pieces_framework2.Property.Number({
      displayName: "WOEID",
      description: "Yahoo Where On Earth ID. Use 1 for worldwide, 23424977 for US, 23424975 for UK, or 23424969 for Turkey.",
      required: !1,
      defaultValue: 1
    }),
    count: import_pieces_framework2.Property.Number({
      displayName: "Count",
      description: "Number of trends to return. Use 1-50.",
      required: !1,
      defaultValue: 30
    })
  },
  async run(context) {
    return xquikCommon.api.get({
      apiKey: context.auth.secret_text,
      path: "/x/trends",
      queryParams: xquikCommon.utils.cleanQueryParams({
        count: context.propsValue.count,
        woeid: context.propsValue.woeid
      })
    });
  }
});

// ../ap/packages/pieces/community/xquik/src/lib/actions/get-tweet.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var getTweet = (0, import_pieces_framework3.createAction)({
  auth: xquikAuth,
  name: "get_tweet",
  displayName: "Get Tweet",
  description: "Look up a public X/Twitter post by ID",
  audience: "both",
  aiMetadata: {
    description: "Retrieves a single public X/Twitter post by its numeric post ID. Use when you already have a tweet ID and need its full details. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    tweetId: import_pieces_framework3.Property.ShortText({
      displayName: "Tweet ID",
      description: "Numeric X/Twitter post ID.",
      required: !0
    })
  },
  async run(context) {
    return xquikCommon.api.get({
      apiKey: context.auth.secret_text,
      path: `/x/tweets/${xquikCommon.utils.encodePathPart(
        context.propsValue.tweetId
      )}`
    });
  }
});

// ../ap/packages/pieces/community/xquik/src/lib/actions/get-user-tweets.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var getUserTweets = (0, import_pieces_framework4.createAction)({
  auth: xquikAuth,
  name: "get_user_tweets",
  displayName: "Get User Tweets",
  description: "List recent posts from a public X/Twitter user",
  audience: "both",
  aiMetadata: {
    description: "Lists a public X/Twitter user's recent timeline posts, identified by username (with or without the @) or numeric user ID. Use to read an account's activity; replies and parent-post context can optionally be included. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    user: import_pieces_framework4.Property.ShortText({
      displayName: "User",
      description: "Username with or without @, or a numeric user ID.",
      required: !0
    }),
    cursor: import_pieces_framework4.Property.ShortText({
      displayName: "Cursor",
      description: "Pagination cursor from a previous response.",
      required: !1
    }),
    includeReplies: import_pieces_framework4.Property.Checkbox({
      displayName: "Include Replies",
      description: "Include reply posts in the timeline response.",
      required: !1,
      defaultValue: !1
    }),
    includeParentTweet: import_pieces_framework4.Property.Checkbox({
      displayName: "Include Parent Tweet",
      description: "Include parent post context when available.",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let user = xquikCommon.utils.stripAtPrefix(context.propsValue.user);
    return xquikCommon.api.get({
      apiKey: context.auth.secret_text,
      path: `/x/users/${xquikCommon.utils.encodePathPart(user)}/tweets`,
      queryParams: xquikCommon.utils.cleanQueryParams({
        cursor: context.propsValue.cursor,
        includeParentTweet: context.propsValue.includeParentTweet,
        includeReplies: context.propsValue.includeReplies
      })
    });
  }
});

// ../ap/packages/pieces/community/xquik/src/lib/actions/get-user.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var getUser = (0, import_pieces_framework5.createAction)({
  auth: xquikAuth,
  name: "get_user",
  displayName: "Get User",
  description: "Look up a public X/Twitter user by username or user ID",
  audience: "both",
  aiMetadata: {
    description: "Retrieves a single public X/Twitter user profile, identified either by username (with or without the @) or by numeric user ID. Use to resolve an account to its profile details. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    user: import_pieces_framework5.Property.ShortText({
      displayName: "User",
      description: "Username with or without @, or a numeric user ID.",
      required: !0
    })
  },
  async run(context) {
    let user = xquikCommon.utils.stripAtPrefix(context.propsValue.user);
    return xquikCommon.api.get({
      apiKey: context.auth.secret_text,
      path: `/x/users/${xquikCommon.utils.encodePathPart(user)}`
    });
  }
});

// ../ap/packages/pieces/community/xquik/src/lib/actions/search-tweets.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var searchTweets = (0, import_pieces_framework6.createAction)({
  auth: xquikAuth,
  name: "search_tweets",
  displayName: "Search Tweets",
  description: "Search public X/Twitter posts with X query operators.",
  audience: "both",
  aiMetadata: {
    description: "Searches public X/Twitter posts matching a query, which supports keywords, hashtags, exact phrases, from:user, and X search operators. Use to find posts on a topic; set the result type to fetch the latest posts or top engagement-ranked ones, and optionally bound results by a time window. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    query: import_pieces_framework6.Property.ShortText({
      displayName: "Query",
      description: "Search query. Supports keywords, hashtags, from:user, exact phrases, and X search operators.",
      required: !0
    }),
    queryType: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Result Type",
      description: "Return latest posts or top engagement-ranked posts.",
      required: !1,
      defaultValue: "Latest",
      options: {
        options: [
          {
            label: "Latest",
            value: "Latest"
          },
          {
            label: "Top",
            value: "Top"
          }
        ]
      }
    }),
    limit: import_pieces_framework6.Property.Number({
      displayName: "Limit",
      description: "Maximum posts to return. Use 1-200.",
      required: !1,
      defaultValue: 20
    }),
    cursor: import_pieces_framework6.Property.ShortText({
      displayName: "Cursor",
      description: "Pagination cursor from a previous response.",
      required: !1
    }),
    sinceTime: import_pieces_framework6.Property.DateTime({
      displayName: "Since Time",
      description: "Only return posts after this ISO timestamp.",
      required: !1
    }),
    untilTime: import_pieces_framework6.Property.DateTime({
      displayName: "Until Time",
      description: "Only return posts before this ISO timestamp.",
      required: !1
    })
  },
  async run(context) {
    return xquikCommon.api.get({
      apiKey: context.auth.secret_text,
      path: "/x/tweets/search",
      queryParams: xquikCommon.utils.cleanQueryParams({
        cursor: context.propsValue.cursor,
        limit: context.propsValue.limit,
        q: context.propsValue.query,
        queryType: context.propsValue.queryType,
        sinceTime: context.propsValue.sinceTime,
        untilTime: context.propsValue.untilTime
      })
    });
  }
});

// ../ap/packages/pieces/community/xquik/src/lib/actions/search-users.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var searchUsers = (0, import_pieces_framework7.createAction)({
  auth: xquikAuth,
  name: "search_users",
  displayName: "Search Users",
  description: "Search public X/Twitter users by name or username.",
  audience: "both",
  aiMetadata: {
    description: "Searches public X/Twitter user accounts by name, username, brand, or keyword. Use to discover or disambiguate accounts before fetching a specific user or their posts. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    query: import_pieces_framework7.Property.ShortText({
      displayName: "Query",
      description: "Name, username, brand, or keyword to search for.",
      required: !0
    }),
    cursor: import_pieces_framework7.Property.ShortText({
      displayName: "Cursor",
      description: "Pagination cursor from a previous response.",
      required: !1
    })
  },
  async run(context) {
    return xquikCommon.api.get({
      apiKey: context.auth.secret_text,
      path: "/x/users/search",
      queryParams: xquikCommon.utils.cleanQueryParams({
        cursor: context.propsValue.cursor,
        q: context.propsValue.query
      })
    });
  }
});

// ../ap/packages/pieces/community/xquik/src/index.ts
var xquik = (0, import_pieces_framework8.createPiece)({
  displayName: "Xquik",
  description: "Search public X/Twitter posts, users, timelines, and trends for automation workflows.",
  auth: xquikAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/xquik.png",
  authors: ["kriptoburak"],
  categories: [import_pieces_framework9.PieceCategory.BUSINESS_INTELLIGENCE, import_pieces_framework9.PieceCategory.MARKETING],
  actions: [
    searchTweets,
    getTweet,
    searchUsers,
    getUser,
    getUserTweets,
    getTrends,
    (0, import_pieces_common2.createCustomApiCallAction)({
      auth: xquikAuth,
      baseUrl: () => xquikCommon.config.baseUrl,
      authMapping: async (auth) => ({
        Accept: "application/json",
        "User-Agent": xquikCommon.config.userAgent,
        "x-api-key": auth.secret_text,
        "xquik-api-contract": xquikCommon.config.apiContract
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  xquik
});
